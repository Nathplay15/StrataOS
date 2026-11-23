// Package davcal serves the accounts' calendars (CalDAV) and address books
// (CardDAV) to iPhone, Android and Thunderbird, without another server to
// maintain (roadmap 0.15). Each account has one collection per kind, backed
// by a single .ics / .vcf file under the NAS data directory; the objects are
// addressed as <uid>.ics / <uid>.vcf inside the collection.
package davcal

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Handler serves /caldav/..., /carddav/... and /principals/... with HTTP
// Basic authentication delegated to Auth.
type Handler struct {
	Dir  string // <dataDir>/dav
	Auth func(w http.ResponseWriter, r *http.Request) (user string, ok bool)
}

func (h *Handler) file(user, kind string) string {
	if kind == "carddav" {
		return filepath.Join(h.Dir, user, "addressbook.vcf")
	}
	return filepath.Join(h.Dir, user, "calendar.ics")
}

var uidRe = regexp.MustCompile(`(?mi)^UID:([^\r\n]+)`)

// blocks splits an .ics/.vcf file into its objects (VEVENT, VTODO, VCARD...).
func blocks(data, obj string) []string {
	var out []string
	lines := strings.Split(data, "\n")
	var cur []string
	in := false
	for _, l := range lines {
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

// upsertBlock replaces the object with the same UID, or appends it.
func upsertBlock(path, obj, block string) error {
	data, _ := os.ReadFile(path)
	uid := uidOf(block)
	var kept []string
	for _, b := range blocks(string(data), obj) {
		if uidOf(b) != uid {
			kept = append(kept, b)
		}
	}
	kept = append(kept, strings.TrimRight(block, "\n"))
	out := ""
	if obj == "VCARD" {
		out = strings.Join(kept, "\n") + "\n"
	} else {
		out = strings.Join(kept, "\n") + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), 0o600)
}

// deleteBlock removes the object with that UID.
func deleteBlock(path, obj, uid string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil
	}
	var kept []string
	removed := false
	for _, b := range blocks(string(data), obj) {
		if uidOf(b) == uid {
			removed = true
			continue
		}
		kept = append(kept, b)
	}
	if !removed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// ---- XML (multistatus) ----

type href struct {
	Value string `xml:",chardata"`
}

type resourcetype struct {
	Collection  *struct{} `xml:"D:collection,omitempty"`
	Calendar    *struct{} `xml:"C:calendar,omitempty"`
	Addressbook *struct{} `xml:"C:addressbook,omitempty"`
}

type homeSet struct {
	Href href `xml:"D:href"`
}

type principal struct {
	Href href `xml:"D:href"`
}

type prop struct {
	DisplayName          string       `xml:"D:displayname,omitempty"`
	Resourcetype         resourcetype `xml:"D:resourcetype"`
	GetContentType       string       `xml:"D:getcontenttype,omitempty"`
	GetEtag              string       `xml:"D:getetag,omitempty"`
	GetLastModified      string       `xml:"D:getlastmodified,omitempty"`
	CurrentUserPrincipal *principal   `xml:"D:current-user-principal,omitempty"`
	CalendarHomeSet      *homeSet     `xml:"C:calendar-home-set,omitempty"`
	AddressbookHomeSet   *homeSet     `xml:"C:addressbook-home-set,omitempty"`
	CalendarData         string       `xml:"C:calendar-data,omitempty"`
	AddressData          string       `xml:"C:addressbook-data,omitempty"`
}

type propstat struct {
	Prop   prop   `xml:"D:prop"`
	Status string `xml:"D:status"`
}

type response struct {
	Href     string     `xml:"D:href"`
	Propstat []propstat `xml:"D:propstat"`
}

type multistatus struct {
	XMLName xml.Name   `xml:"D:multistatus"`
	XmlnsC  string     `xml:"xmlns:C,attr"`
	Resp    []response `xml:"D:response"`
}

const statusOK = "HTTP/1.1 200 OK"

func writeXML(w http.ResponseWriter, status int, m *multistatus) {
	m.XmlnsC = "urn:ietf:params:xml:ns:caldav"
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(m)
}

func okProp(p prop) propstat { return propstat{Prop: p, Status: statusOK} }

// ---- HTTP ----

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := h.Auth(w, r)
	if !ok {
		return
	}
	w.Header().Set("DAV", "1, 2, calendar-access, addressbook")
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, REPORT, GET, PUT, DELETE, MKCALENDAR")
		w.WriteHeader(http.StatusNoContent)
		return
	case "PROPFIND":
		h.propfind(w, r, user)
	case "REPORT":
		h.report(w, r, user)
	case http.MethodGet:
		h.get(w, r, user)
	case http.MethodPut:
		h.put(w, r, user)
	case http.MethodDelete:
		h.del(w, r, user)
	case "MKCALENDAR", "MKCOL":
		w.WriteHeader(http.StatusCreated)
	default:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, REPORT, GET, PUT, DELETE, MKCALENDAR")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// splitPath returns the kind ("caldav"|"carddav"|"principals") and the rest.
func splitPath(p string) (kind, rest string) {
	p = strings.TrimPrefix(p, "/")
	k, r, _ := strings.Cut(p, "/")
	return k, r
}

func (h *Handler) propfind(w http.ResponseWriter, r *http.Request, user string) {
	kind, rest := splitPath(r.URL.Path)
	depth := r.Header.Get("Depth")
	m := &multistatus{}
	switch {
	case kind == "principals" || rest == "":
		// The principal of the signed-in account (discovery step).
		base := "/" + kind
		if kind != "caldav" && kind != "carddav" {
			base = "/principals"
		}
		p := prop{
			Resourcetype:         resourcetype{Collection: &struct{}{}},
			CurrentUserPrincipal: &principal{Href: href{Value: "/principals/" + user + "/"}},
			CalendarHomeSet:      &homeSet{Href: href{Value: "/caldav/" + user + "/default/"}},
			AddressbookHomeSet:   &homeSet{Href: href{Value: "/carddav/" + user + "/default/"}},
		}
		m.Resp = append(m.Resp, response{Href: base + "/" + user + "/", Propstat: []propstat{okProp(p)}})
	case kind == "caldav" || kind == "carddav":
		obj := "VEVENT"
		if kind == "carddav" {
			obj = "VCARD"
		}
		// /caldav/<user>/[default/…]
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		if len(parts) < 2 || parts[1] == "" {
			// Home: list the default collection.
			m.Resp = append(m.Resp, response{Href: "/" + kind + "/" + user + "/", Propstat: []propstat{okProp(prop{
				DisplayName:  user,
				Resourcetype: resourcetype{Collection: &struct{}{}},
			})}})
			if depth != "0" {
				m.Resp = append(m.Resp, response{Href: "/" + kind + "/" + user + "/default/", Propstat: []propstat{okProp(prop{
					DisplayName:  "default",
					Resourcetype: resourcetype{Collection: &struct{}{}, Calendar: &struct{}{}, Addressbook: &struct{}{}},
				})}})
			}
			writeXML(w, http.StatusMultiStatus, m)
			return
		}
		coll := "/" + kind + "/" + user + "/default/"
		if strings.Trim(r.URL.Path, "/")+"/" != coll && r.URL.Path != coll {
			// A resource inside the collection.
			name := parts[len(parts)-1]
			uid := strings.TrimSuffix(name, map[string]string{"caldav": ".ics", "carddav": ".vcf"}[kind])
			data, _ := os.ReadFile(h.file(user, kind))
			for _, b := range blocks(string(data), obj) {
				if uidOf(b) == uid {
					m.Resp = append(m.Resp, response{Href: coll + name, Propstat: []propstat{okProp(prop{
						Resourcetype:    resourcetype{},
						GetContentType:  contentType(kind),
						GetEtag:         fmt.Sprintf(`"%x"`, len(b)),
						GetLastModified: time.Now().UTC().Format(http.TimeFormat),
					})}})
				}
			}
			writeXML(w, http.StatusMultiStatus, m)
			return
		}
		// The collection itself.
		m.Resp = append(m.Resp, response{Href: coll, Propstat: []propstat{okProp(prop{
			DisplayName:  "default",
			Resourcetype: resourcetype{Collection: &struct{}{}, Calendar: &struct{}{}, Addressbook: &struct{}{}},
		})}})
		if depth != "0" {
			data, _ := os.ReadFile(h.file(user, kind))
			for _, b := range blocks(string(data), obj) {
				uid := uidOf(b)
				ext := map[string]string{"caldav": ".ics", "carddav": ".vcf"}[kind]
				m.Resp = append(m.Resp, response{Href: coll + uid + ext, Propstat: []propstat{okProp(prop{
					Resourcetype:    resourcetype{},
					GetContentType:  contentType(kind),
					GetEtag:         fmt.Sprintf(`"%x"`, len(b)),
					GetLastModified: time.Now().UTC().Format(http.TimeFormat),
				})}})
			}
		}
		writeXML(w, http.StatusMultiStatus, m)
		return
	}
	writeXML(w, http.StatusMultiStatus, m)
}

func contentType(kind string) string {
	if kind == "carddav" {
		return "text/vcard; charset=utf-8"
	}
	return "text/calendar; charset=utf-8"
}

func (h *Handler) objOf(kind string) string {
	if kind == "carddav" {
		return "VCARD"
	}
	return "VEVENT"
}

func (h *Handler) extOf(kind string) string {
	if kind == "carddav" {
		return ".vcf"
	}
	return ".ics"
}

// report answers calendar-query / addressbook-query with every object.
func (h *Handler) report(w http.ResponseWriter, r *http.Request, user string) {
	kind, _ := splitPath(r.URL.Path)
	if kind != "caldav" && kind != "carddav" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data, _ := os.ReadFile(h.file(user, kind))
	coll := "/" + kind + "/" + user + "/default/"
	m := &multistatus{}
	for _, b := range blocks(string(data), h.objOf(kind)) {
		p := prop{Resourcetype: resourcetype{}, GetContentType: contentType(kind)}
		if kind == "caldav" {
			p.CalendarData = b
		} else {
			p.AddressData = b
		}
		m.Resp = append(m.Resp, response{Href: coll + uidOf(b) + h.extOf(kind), Propstat: []propstat{okProp(p)}})
	}
	writeXML(w, http.StatusMultiStatus, m)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, user string) {
	kind, rest := splitPath(r.URL.Path)
	name := filepath.Base(rest)
	uid := strings.TrimSuffix(name, h.extOf(kind))
	data, _ := os.ReadFile(h.file(user, kind))
	for _, b := range blocks(string(data), h.objOf(kind)) {
		if uidOf(b) == uid {
			w.Header().Set("Content-Type", contentType(kind))
			w.Header().Set("Content-Disposition", "attachment")
			_, _ = w.Write([]byte(b))
			return
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request, user string) {
	kind, _ := splitPath(r.URL.Path)
	if kind != "caldav" && kind != "carddav" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := upsertBlock(h.file(user, kind), h.objOf(kind), string(body)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, len(body)))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request, user string) {
	kind, rest := splitPath(r.URL.Path)
	name := filepath.Base(rest)
	uid := strings.TrimSuffix(name, h.extOf(kind))
	removed, err := deleteBlock(h.file(user, kind), h.objOf(kind), uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !removed {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SortedUIDs lists the object UIDs of a file (used by tests and the interface).
func SortedUIDs(data, obj string) []string {
	out := []string{}
	for _, b := range blocks(data, obj) {
		if u := uidOf(b); u != "" {
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}
