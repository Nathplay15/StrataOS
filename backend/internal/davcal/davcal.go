// Package davcal serves the accounts' calendars (CalDAV) and address books
// (CardDAV) to iPhone, Android (DAVx⁵) and Thunderbird, without another
// server to maintain. The protocol is github.com/emersion/go-webdav (MIT);
// this package is its storage: one file per object, as the client named it,
// under <dataDir>/dav/<account>/{calendar,contacts}/, written atomically
// under a per-account lock, with a content ETag.
//
// Paths: principal /caldav/<account>/ (and /carddav/<account>/), home set
// …/<account>/calendars/ (…/addressbooks/), one collection "default".
package davcal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"
)

// Limits of one account.
const (
	MaxObjectSize = 1 << 20
	MaxObjects    = 20000
)

// Handler serves /caldav/..., /carddav/... (and the /.well-known/ redirects)
// with HTTP Basic authentication delegated to Auth.
type Handler struct {
	Dir  string // <dataDir>/dav
	Auth func(w http.ResponseWriter, r *http.Request) (user string, ok bool)

	locks sync.Map // account -> *sync.Mutex
}

type ctxUser struct{}

func userFrom(ctx context.Context) string {
	u, _ := ctx.Value(ctxUser{}).(string)
	return u
}

var accountRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/caldav":
		http.Redirect(w, r, "/caldav/", http.StatusMovedPermanently)
		return
	case "/.well-known/carddav":
		http.Redirect(w, r, "/carddav/", http.StatusMovedPermanently)
		return
	}
	user, ok := h.Auth(w, r)
	if !ok {
		return
	}
	if !accountRe.MatchString(user) {
		http.Error(w, "unsupported account name", http.StatusForbidden)
		return
	}
	// Another account's tree: refused before the protocol layer.
	kind, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if owner, _, _ := strings.Cut(rest, "/"); owner != "" && owner != user {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.ContentLength > MaxObjectSize {
		http.Error(w, "object too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxObjectSize)
	// Some clients send their PROPFIND/REPORT XML without a Content-Type.
	if (r.Method == "PROPFIND" || r.Method == "REPORT") && r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/xml")
	}
	if err := h.migrate(user); err != nil {
		http.Error(w, "storage unavailable", http.StatusInternalServerError)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxUser{}, user))
	switch kind {
	case "caldav":
		(&caldav.Handler{Backend: &calBackend{h}, Prefix: "/caldav"}).ServeHTTP(w, r)
	case "carddav":
		(&carddav.Handler{Backend: &cardBackend{h}, Prefix: "/carddav"}).ServeHTTP(w, r)
	case "principals":
		// 0.15.0-0.15.1 principal: send clients to the calendar one.
		http.Redirect(w, r, "/caldav/"+user+"/", http.StatusPermanentRedirect)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) lock(user string) func() {
	m, _ := h.locks.LoadOrStore(user, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// store is one kind of collection of one account.
type store struct {
	h      *Handler
	kind   string // caldav | carddav
	folder string // calendar | contacts
	home   string // calendars | addressbooks
	ext    string // .ics | .vcf
}

var (
	calStore  = store{kind: "caldav", folder: "calendar", home: "calendars", ext: ".ics"}
	cardStore = store{kind: "carddav", folder: "contacts", home: "addressbooks", ext: ".vcf"}
)

func (s store) dir(user string) string { return filepath.Join(s.h.Dir, user, s.folder) }
func (s store) principal(user string) string {
	return "/" + s.kind + "/" + user + "/"
}
func (s store) homePath(user string) string { return s.principal(user) + s.home + "/" }
func (s store) collPath(user string) string { return s.homePath(user) + "default/" }

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+=~-]{0,199}$`)

// file maps an object URL path of the account to its file, or errors.
func (s store) file(user, p string) (string, error) {
	dir, name := path.Split(p)
	if dir != s.collPath(user) || !nameRe.MatchString(name) || !strings.HasSuffix(strings.ToLower(name), s.ext) {
		return "", webdav.NewHTTPError(http.StatusNotFound, errors.New("no such object"))
	}
	return filepath.Join(s.dir(user), name), nil
}

func (s store) checkColl(user, p string) error {
	if strings.TrimSuffix(p, "/")+"/" != s.collPath(user) {
		return webdav.NewHTTPError(http.StatusNotFound, errors.New("no such collection"))
	}
	return nil
}

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

type raw struct {
	path string
	mod  time.Time
	data []byte
}

func (s store) read(user, p string) (raw, error) {
	f, err := s.file(user, p)
	if err != nil {
		return raw{}, err
	}
	st, err := os.Lstat(f)
	if err != nil || !st.Mode().IsRegular() {
		return raw{}, webdav.NewHTTPError(http.StatusNotFound, errors.New("no such object"))
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return raw{}, err
	}
	return raw{path: p, mod: st.ModTime(), data: b}, nil
}

func (s store) list(user string) ([]raw, error) {
	ents, err := os.ReadDir(s.dir(user))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []raw
	for _, e := range ents {
		if !e.Type().IsRegular() || !nameRe.MatchString(e.Name()) || !strings.HasSuffix(strings.ToLower(e.Name()), s.ext) {
			continue
		}
		r, err := s.read(user, s.collPath(user)+e.Name())
		if err == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// put writes data at p after checking the conditions, under the account's
// lock, through a temporary file renamed in place.
func (s store) put(user, p string, data []byte, ifMatch, ifNoneMatch webdav.ConditionalMatch) (raw, error) {
	if len(data) > MaxObjectSize {
		return raw{}, webdav.NewHTTPError(http.StatusRequestEntityTooLarge, errors.New("object too large"))
	}
	f, err := s.file(user, p)
	if err != nil {
		return raw{}, webdav.NewHTTPError(http.StatusForbidden, errors.New("objects go in the default collection, named <name>"+s.ext))
	}
	defer s.h.lock(user)()
	cur, err := os.ReadFile(f)
	exists := err == nil
	if ifNoneMatch.IsSet() && exists {
		if ok, _ := ifNoneMatch.MatchETag(etagOf(cur)); ok || ifNoneMatch.IsWildcard() {
			return raw{}, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("the object exists"))
		}
	}
	if ifMatch.IsSet() {
		if !exists {
			return raw{}, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("the object does not exist"))
		}
		if ok, _ := ifMatch.MatchETag(etagOf(cur)); !ok && !ifMatch.IsWildcard() {
			return raw{}, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("the object changed"))
		}
	}
	if !exists {
		if ents, _ := os.ReadDir(s.dir(user)); len(ents) >= MaxObjects {
			return raw{}, webdav.NewHTTPError(http.StatusInsufficientStorage, errors.New("too many objects"))
		}
	}
	if err := writeAtomic(f, data); err != nil {
		return raw{}, err
	}
	st, _ := os.Stat(f)
	return raw{path: p, mod: st.ModTime(), data: data}, nil
}

func (s store) del(user, p string) error {
	f, err := s.file(user, p)
	if err != nil {
		return err
	}
	defer s.h.lock(user)()
	if err := os.Remove(f); errors.Is(err, os.ErrNotExist) {
		return webdav.NewHTTPError(http.StatusNotFound, err)
	} else if err != nil {
		return err
	}
	return nil
}

func writeAtomic(f string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), ".put-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), f)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// ---- CalDAV ----

type calBackend struct{ h *Handler }

func (b *calBackend) st() store { s := calStore; s.h = b.h; return s }

func (b *calBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return b.st().principal(userFrom(ctx)), nil
}
func (b *calBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return b.st().homePath(userFrom(ctx)), nil
}
func (b *calBackend) calendar(user string) caldav.Calendar {
	return caldav.Calendar{Path: b.st().collPath(user), Name: "StrataOS", Description: "Calendar of " + user,
		MaxResourceSize: MaxObjectSize, SupportedComponentSet: []string{ical.CompEvent, ical.CompToDo}}
}
func (b *calBackend) CreateCalendar(ctx context.Context, c *caldav.Calendar) error {
	return webdav.NewHTTPError(http.StatusForbidden, errors.New("one calendar per account"))
}
func (b *calBackend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	return []caldav.Calendar{b.calendar(userFrom(ctx))}, nil
}
func (b *calBackend) GetCalendar(ctx context.Context, p string) (*caldav.Calendar, error) {
	u := userFrom(ctx)
	if err := b.st().checkColl(u, p); err != nil {
		return nil, err
	}
	c := b.calendar(u)
	return &c, nil
}

func toCal(r raw) (caldav.CalendarObject, error) {
	cal, err := ical.NewDecoder(bytes.NewReader(r.data)).Decode()
	if err != nil {
		return caldav.CalendarObject{}, err
	}
	return caldav.CalendarObject{Path: r.path, ModTime: r.mod, ContentLength: int64(len(r.data)), ETag: etagOf(r.data), Data: cal}, nil
}

func (b *calBackend) GetCalendarObject(ctx context.Context, p string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	r, err := b.st().read(userFrom(ctx), p)
	if err != nil {
		return nil, err
	}
	co, err := toCal(r)
	return &co, err
}
func (b *calBackend) ListCalendarObjects(ctx context.Context, p string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	u := userFrom(ctx)
	if err := b.st().checkColl(u, p); err != nil {
		return nil, err
	}
	rs, err := b.st().list(u)
	if err != nil {
		return nil, err
	}
	out := make([]caldav.CalendarObject, 0, len(rs))
	for _, r := range rs {
		if co, err := toCal(r); err == nil {
			out = append(out, co)
		}
	}
	return out, nil
}
func (b *calBackend) QueryCalendarObjects(ctx context.Context, p string, q *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	all, err := b.ListCalendarObjects(ctx, p, &q.CompRequest)
	if err != nil {
		return nil, err
	}
	return caldav.Filter(q, all)
}
func (b *calBackend) PutCalendarObject(ctx context.Context, p string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	r, err := b.st().put(userFrom(ctx), p, buf.Bytes(), opts.IfMatch, opts.IfNoneMatch)
	if err != nil {
		return nil, err
	}
	co, err := toCal(r)
	return &co, err
}
func (b *calBackend) DeleteCalendarObject(ctx context.Context, p string) error {
	return b.st().del(userFrom(ctx), p)
}

// ---- CardDAV ----

type cardBackend struct{ h *Handler }

func (b *cardBackend) st() store { s := cardStore; s.h = b.h; return s }

func (b *cardBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return b.st().principal(userFrom(ctx)), nil
}
func (b *cardBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return b.st().homePath(userFrom(ctx)), nil
}
func (b *cardBackend) book(user string) carddav.AddressBook {
	return carddav.AddressBook{Path: b.st().collPath(user), Name: "StrataOS", Description: "Contacts of " + user, MaxResourceSize: MaxObjectSize,
		SupportedAddressData: []carddav.AddressDataType{{ContentType: vcard.MIMEType, Version: "3.0"}, {ContentType: vcard.MIMEType, Version: "4.0"}}}
}
func (b *cardBackend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	return []carddav.AddressBook{b.book(userFrom(ctx))}, nil
}
func (b *cardBackend) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	u := userFrom(ctx)
	if err := b.st().checkColl(u, p); err != nil {
		return nil, err
	}
	a := b.book(u)
	return &a, nil
}
func (b *cardBackend) CreateAddressBook(ctx context.Context, a *carddav.AddressBook) error {
	return webdav.NewHTTPError(http.StatusForbidden, errors.New("one address book per account"))
}
func (b *cardBackend) DeleteAddressBook(ctx context.Context, p string) error {
	return webdav.NewHTTPError(http.StatusForbidden, errors.New("the address book cannot be deleted"))
}

func toCard(r raw) (carddav.AddressObject, error) {
	c, err := vcard.NewDecoder(bytes.NewReader(r.data)).Decode()
	if err != nil {
		return carddav.AddressObject{}, err
	}
	return carddav.AddressObject{Path: r.path, ModTime: r.mod, ContentLength: int64(len(r.data)), ETag: etagOf(r.data), Card: c}, nil
}

func (b *cardBackend) GetAddressObject(ctx context.Context, p string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	r, err := b.st().read(userFrom(ctx), p)
	if err != nil {
		return nil, err
	}
	ao, err := toCard(r)
	return &ao, err
}
func (b *cardBackend) ListAddressObjects(ctx context.Context, p string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	u := userFrom(ctx)
	if err := b.st().checkColl(u, p); err != nil {
		return nil, err
	}
	rs, err := b.st().list(u)
	if err != nil {
		return nil, err
	}
	out := make([]carddav.AddressObject, 0, len(rs))
	for _, r := range rs {
		if ao, err := toCard(r); err == nil {
			out = append(out, ao)
		}
	}
	return out, nil
}
func (b *cardBackend) QueryAddressObjects(ctx context.Context, p string, q *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	all, err := b.ListAddressObjects(ctx, p, &q.DataRequest)
	if err != nil {
		return nil, err
	}
	if len(q.PropFilters) == 0 {
		// No filter: every card ("anyof" over nothing would match none).
		q2 := *q
		q2.FilterTest = carddav.FilterAllOf
		q = &q2
	}
	return carddav.Filter(q, all)
}
func (b *cardBackend) PutAddressObject(ctx context.Context, p string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	r, err := b.st().put(userFrom(ctx), p, buf.Bytes(), opts.IfMatch, opts.IfNoneMatch)
	if err != nil {
		return nil, err
	}
	ao, err := toCard(r)
	return &ao, err
}
func (b *cardBackend) DeleteAddressObject(ctx context.Context, p string) error {
	return b.st().del(userFrom(ctx), p)
}

// ---- Migration of the 0.15.0-0.15.1 single files ----

// migrate splits the old calendar.ics / addressbook.vcf of an account (one
// file of bare objects) into one object per file, once.
func (h *Handler) migrate(user string) error {
	defer h.lock(user)()
	for _, m := range []struct {
		old, obj string
		s        store
	}{{"calendar.ics", "VEVENT", calStore}, {"addressbook.vcf", "VCARD", cardStore}} {
		oldPath := filepath.Join(h.Dir, user, m.old)
		data, err := os.ReadFile(oldPath)
		if err != nil {
			continue
		}
		m.s.h = h
		objs := blocks(string(data), m.obj)
		if m.obj == "VEVENT" {
			objs = append(objs, blocks(string(data), "VTODO")...)
		}
		for i, b := range objs {
			name := sanitize(uidOf(b))
			if name == "" {
				name = fmt.Sprintf("migrated-%d", i)
			}
			body := strings.ReplaceAll(strings.TrimRight(b, "\n"), "\r", "")
			body = strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
			if m.obj != "VCARD" {
				body = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//StrataOS//CalDAV//EN\r\n" + body + "END:VCALENDAR\r\n"
			}
			f := filepath.Join(m.s.dir(user), name+m.s.ext)
			if _, err := os.Stat(f); err == nil {
				continue
			}
			if err := writeAtomic(f, []byte(body)); err != nil {
				return err
			}
		}
		if err := os.Rename(oldPath, oldPath+".migrated"); err != nil {
			return err
		}
	}
	return nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._@+=~-]`)

func sanitize(uid string) string {
	s := strings.TrimLeft(unsafeName.ReplaceAllString(uid, "_"), "._-~+=@")
	if len(s) > 150 {
		s = s[:150]
	}
	return s
}

var uidRe = regexp.MustCompile(`(?mi)^UID:([^\r\n]+)`)

// blocks splits a file into its objects (VEVENT, VTODO, VCARD...).
func blocks(data, obj string) []string {
	var out []string
	var cur []string
	in := false
	for _, l := range strings.Split(data, "\n") {
		up := strings.ToUpper(strings.TrimRight(l, "\r"))
		if up == "BEGIN:"+obj {
			in, cur = true, []string{l}
			continue
		}
		if in {
			cur = append(cur, l)
			if up == "END:"+obj {
				out = append(out, strings.Join(cur, "\n"))
				in = false
			}
		}
	}
	return out
}

func uidOf(block string) string {
	if m := uidRe.FindStringSubmatch(block); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
