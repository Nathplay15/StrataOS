package api

// Share links, continued: link options (file requests, limits), visitor rate
// limits and statistics, uploads and the owner's notification.

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/smtp"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"strataos/internal/gate"
	"strataos/internal/i18n"
	"strataos/internal/store"
)

var (
	// Per visitor address: page loads, listings, thumbnails and downloads…
	linkReads = &gate.RateLimiter{Burst: 120, Every: 250 * time.Millisecond}
	// …and upload requests (each may carry many files).
	linkUploads = &gate.RateLimiter{Burst: 20, Every: 15 * time.Second}
	// A view is counted once per address and link every 30 minutes, which
	// also bounds the state writes a visitor can cause.
	linkViews struct {
		sync.Mutex
		seen map[string]time.Time
	}
	// Uploads to one link run one at a time, so its size limits hold.
	linkUploadLocks sync.Map // link ID → *sync.Mutex
)

var errLinkRate = &Error{Status: http.StatusTooManyRequests, Msg: "too many requests, try again in a moment"}

// linkRate applies the visitor rate limit before a link handler.
func linkRate(l *gate.RateLimiter, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(clientIP(r), 1) {
			w.Header().Set("Retry-After", "30")
			writeErr(w, errLinkRate)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// countView reports whether this visit counts as a new view.
func countView(id, ip string, now time.Time) bool {
	linkViews.Lock()
	defer linkViews.Unlock()
	if linkViews.seen == nil || len(linkViews.seen) > 50000 {
		linkViews.seen = map[string]time.Time{}
	}
	k := id + "|" + ip
	if t, ok := linkViews.seen[k]; ok && now.Sub(t) < 30*time.Minute {
		return false
	}
	linkViews.seen[k] = now
	return true
}

// linkOptions are the settings of a link its owner can change.
type linkOptions struct {
	Title        *string `json:"title"`
	Message      *string `json:"message"`
	AskGuest     *string `json:"askGuest"`
	GuestFolders *bool   `json:"guestFolders"`
	MaxFileMB    *int64  `json:"maxFileMb"`
	MaxTotalMB   *int64  `json:"maxTotalMb"`
	Notify       *bool   `json:"notify"`
	MaxDownloads *int    `json:"maxDownloads"`
	// Expires: an RFC 3339 date, "" for never; ExpiresDays (creation and
	// older clients) counts from now.
	Expires     *string `json:"expires"`
	ExpiresDays *int    `json:"expiresDays"`
	// Password: a new one; ClearPassword removes it.
	Password      *string `json:"password"`
	ClearPassword bool    `json:"clearPassword"`
	hashed        string  // the new password, hashed before the state lock
}

func cleanText(s string, max int, multiline bool) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > max {
		return "", badRequest(fmt.Sprintf("text too long (%d characters at most)", max))
	}
	if strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t')) }) {
		return "", badRequest("invalid characters")
	}
	return s, nil
}

// apply validates the options and sets them on l.
func (o linkOptions) apply(l *store.ShareLink, now time.Time) error {
	var err error
	if o.Title != nil {
		if l.Title, err = cleanText(*o.Title, 120, false); err != nil {
			return err
		}
	}
	if o.Message != nil {
		if l.Message, err = cleanText(*o.Message, 2000, true); err != nil {
			return err
		}
	}
	if o.AskGuest != nil {
		switch *o.AskGuest {
		case "", "optional", "required":
			l.AskGuest = *o.AskGuest
		default:
			return badRequest("invalid choice for the sender's name and email")
		}
	}
	if o.GuestFolders != nil {
		l.GuestFolders = *o.GuestFolders
	}
	const maxMB = 1 << 24 // 16 TB
	if o.MaxFileMB != nil {
		if *o.MaxFileMB < 0 || *o.MaxFileMB > maxMB {
			return badRequest("invalid size limit")
		}
		l.MaxFileBytes = gate.MB(*o.MaxFileMB)
	}
	if o.MaxTotalMB != nil {
		if *o.MaxTotalMB < 0 || *o.MaxTotalMB > maxMB {
			return badRequest("invalid size limit")
		}
		l.MaxTotal = gate.MB(*o.MaxTotalMB)
	}
	if o.Notify != nil {
		l.Notify = *o.Notify
	}
	if o.MaxDownloads != nil {
		if *o.MaxDownloads < 0 || *o.MaxDownloads > 1000000 {
			return badRequest("invalid limits")
		}
		l.MaxDownloads = *o.MaxDownloads
	}
	switch {
	case o.Expires != nil && *o.Expires == "":
		l.Expires = time.Time{}
	case o.Expires != nil:
		t, err := time.Parse(time.RFC3339, *o.Expires)
		if err != nil {
			return badRequest("invalid expiry date")
		}
		if !t.After(now) || t.After(now.AddDate(10, 0, 0)) {
			return badRequest("the expiry date must be in the next ten years")
		}
		l.Expires = t
	case o.ExpiresDays != nil:
		if *o.ExpiresDays < 0 || *o.ExpiresDays > 3650 {
			return badRequest("invalid limits")
		}
		l.Expires = time.Time{}
		if *o.ExpiresDays > 0 {
			l.Expires = now.AddDate(0, 0, *o.ExpiresDays)
		}
	}
	if o.ClearPassword {
		l.PasswordHash = ""
	}
	if o.Password != nil && *o.Password != "" {
		if len(*o.Password) < 4 || len(*o.Password) > 200 {
			return badRequest("password too short")
		}
		h, err := hashPassword(*o.Password)
		if err != nil {
			return err
		}
		l.PasswordHash = h
	}
	if !l.Upload {
		l.AskGuest, l.GuestFolders, l.MaxFileBytes, l.MaxTotal = "", false, 0, 0
	}
	return nil
}

func linkLimits(l *store.ShareLink) gate.LinkLimits {
	return gate.LinkLimits{Expires: l.Expires, Download: l.Download, Upload: l.Upload, MaxDownloads: l.MaxDownloads, Downloads: l.Downloads, MaxTotal: l.MaxTotal, Received: l.Received}
}

// reserveDownload counts a download before it starts, so concurrent
// requests cannot go past the limit.
func (s *Server) reserveDownload(id string) error {
	return s.store.Update(func(st *store.State) error {
		for _, x := range st.Links {
			if x.ID != id {
				continue
			}
			if x.MaxDownloads > 0 && x.Downloads >= x.MaxDownloads {
				return &Error{Status: http.StatusGone, Msg: "the download limit of this link is reached"}
			}
			x.Downloads++
			x.LastDownload, x.LastUsed = time.Now(), time.Now()
			return nil
		}
		return errLinkGone
	})
}

// firstRange tells whether a request starts a download (no Range, or one
// from the first byte): resumed parts are not counted again.
func firstRange(r *http.Request) bool {
	rg := r.Header.Get("Range")
	return rg == "" || strings.HasPrefix(strings.ReplaceAll(rg, " ", ""), "bytes=0-")
}

func freeSpace(dir string) int64 {
	var sfs syscall.Statfs_t
	if syscall.Statfs(dir, &sfs) != nil {
		return 0
	}
	return int64(sfs.Bavail) * int64(sfs.Bsize)
}

func validUploadName(name string) bool {
	return name != "/" && name != "." && name != ".." && !strings.HasPrefix(name, ".") && len(name) <= 255 &&
		!strings.ContainsFunc(name, unicode.IsControl)
}

var errUploadRefused = &Error{Status: http.StatusForbidden, Msg: "this link cannot receive files"}

// linkUpload receives the files of a visitor: guest fields first ("name",
// "email"), then the files, each written under a new name (never replacing
// a file) and within the link's limits.
func (s *Server) linkUpload(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, &Error{Status: http.StatusForbidden, Msg: "cross-site request refused"})
		return
	}
	l, admin, err := s.openLink(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.linkUnlocked(r, &l) {
		writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "password required"})
		return
	}
	t, err := s.linkTarget(&l, admin, "")
	if err != nil || !l.Upload {
		writeErr(w, firstErr(err, errUploadRefused))
		return
	}
	if reason := s.readOnlyReason(t); reason != "" {
		writeErr(w, errUploadRefused)
		return
	}
	mu, _ := linkUploadLocks.LoadOrStore(l.ID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	// Read the counters again under the lock.
	if l, admin, err = s.openLink(l.ID); err != nil {
		writeErr(w, err)
		return
	}
	rt, err := t.open()
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rt.Close()
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, badRequest("multipart body expected"))
		return
	}
	var guestName, guestEmail string
	var guest gate.Guest
	guestChecked := false
	dir := t.rel
	var names []string
	var log []store.LinkUpload
	var received int64
	room := s.quotaRoom(l.Owner, admin, t.abs)
	fail := func(err error) {
		if len(log) > 0 {
			s.linkReceived(&l, t.abs, guest, log, received)
		}
		writeErr(w, err)
	}
	for parts := 0; ; parts++ {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || parts > 1000 {
			fail(badRequest("invalid upload"))
			return
		}
		if part.FileName() == "" {
			v, _ := io.ReadAll(io.LimitReader(part, 1024))
			switch part.FormName() {
			case "name":
				guestName = string(v)
			case "email":
				guestEmail = string(v)
			}
			continue
		}
		if !guestChecked {
			guestChecked = true
			if l.AskGuest != "" {
				if guest, err = gate.CleanGuest(guestName, guestEmail, l.AskGuest == "required"); err != nil {
					fail(badRequest(err.Error()))
					return
				}
			}
			if sub := gate.GuestFolder(guest.Name); l.GuestFolders && sub != "" {
				dir = path.Join(t.rel, sub)
				if err := rt.Mkdir(dir, 0o775); err == nil && !admin {
					s.chownTo(l.Owner, rt, dir)
				} else if err != nil && !errors.Is(err, os.ErrExist) {
					fail(err)
					return
				}
				if st, err := rt.Lstat(dir); err != nil || !st.IsDir() {
					fail(errUploadRefused)
					return
				}
			}
		}
		_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		raw := params["filename"]
		name := path.Base(path.Clean("/" + strings.ReplaceAll(raw, "\\", "/")))
		if !validUploadName(name) {
			continue
		}
		budget, err := gate.UploadBudget(l.MaxFileBytes, l.MaxTotal, l.Received+received, freeSpace(t.abs))
		if err == nil && room >= 0 {
			if room-received <= 0 {
				err = errors.New("the quota of the link's owner is full")
			}
			budget = min(budget, room-received)
		}
		if err != nil {
			fail(&Error{Status: http.StatusRequestEntityTooLarge, Msg: err.Error()})
			return
		}
		stored := uniqueName(func(n string) bool { _, err := rt.Lstat(path.Join(dir, n)); return err == nil }, name)
		rel := path.Join(dir, stored)
		f, err := rt.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o664)
		if err != nil {
			fail(errUploadRefused)
			return
		}
		n, err := io.Copy(f, io.LimitReader(part, budget+1))
		f.Close()
		if err == nil && n > budget {
			err = &Error{Status: http.StatusRequestEntityTooLarge, Msg: gate.ErrFileTooBig.Error()}
		}
		if err != nil {
			_ = rt.Remove(rel)
			var e *Error
			if errors.As(err, &e) {
				fail(e)
			} else {
				fail(badRequest("upload interrupted"))
			}
			return
		}
		if !admin {
			s.chownTo(l.Owner, rt, rel)
		}
		received += n
		names = append(names, name) // as sent: the stored name could reveal other files
		log = append(log, store.LinkUpload{Time: time.Now(), Name: relTo(t.rel, rel), Size: n, Guest: guest.Name, Email: guest.Email})
	}
	if len(log) > 0 {
		s.linkReceived(&l, t.abs, guest, log, received)
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploaded": len(names), "names": names})
}

// linkReceived records uploads and tells the owner.
func (s *Server) linkReceived(l *store.ShareLink, folder string, g gate.Guest, log []store.LinkUpload, bytes int64) {
	s.touchLink(l.ID, func(x *store.ShareLink) {
		x.Uploads += len(log)
		x.Received += bytes
		x.LastUpload = time.Now()
		x.UploadLog = append(x.UploadLog, log...)
		if n := len(x.UploadLog); n > store.MaxLinkUploads {
			x.UploadLog = x.UploadLog[n-store.MaxLinkUploads:]
		}
	})
	msg := fmt.Sprintf("Drop by link: %d file(s) received in %s", len(log), folder)
	s.store.Log("info", "links", l.Owner, msg)
	if !l.Notify {
		return
	}
	from := g.Name
	if g.Email != "" {
		from = strings.TrimSpace(from + " <" + g.Email + ">")
	}
	if from == "" {
		s.notifyLinkOwner(l.Owner, "Files received by link", fmt.Sprintf("%d file(s) received in %s", len(log), folder))
	} else {
		s.notifyLinkOwner(l.Owner, "Files received by link", fmt.Sprintf("%d file(s) received in %s from %s", len(log), folder, from))
	}
}

// notifyLinkOwner tells the owner of a link: administrators through the NAS
// notification channels, other users by email when they have an address
// and the NAS can send mail.
func (s *Server) notifyLinkOwner(owner, subject, msg string) {
	var u store.User
	var set store.Settings
	s.store.Read(func(st *store.State) {
		if x := st.User(owner); x != nil {
			u = *x
		}
		set = st.Settings
	})
	if u.Role == "admin" {
		s.notify("info", subject, msg)
		return
	}
	if u.Email == "" || set.SMTPHost == "" {
		return
	}
	s.bg.Go(func() {
		if err := mailTo(set, u.Email, i18n.Lines(set.NotifyLang, subject), i18n.Lines(set.NotifyLang, msg)); err != nil {
			s.store.Log("warning", "links", owner, "Link notification not sent: "+err.Error())
		}
	})
}

// mailTo sends one plain text message with the NAS mail settings.
func mailTo(set store.Settings, to, subject, msg string) error {
	host, _ := hostname()
	from := firstNonEmpty(set.SMTPFrom, set.SMTPUser)
	port := set.SMTPPort
	if port == 0 {
		port = 587
	}
	var auth smtp.Auth
	if set.SMTPUser != "" {
		auth = smtp.PlainAuth("", set.SMTPUser, set.SMTPPassword, set.SMTPHost)
	}
	hdr := func(v string) string { return strings.NewReplacer("\r", "", "\n", " ").Replace(v) }
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n",
		hdr(from), hdr(to), mime.QEncoding.Encode("utf-8", hdr("["+host+"] "+subject)), time.Now().Format(time.RFC1123Z), token()[:24], hdr(host), msg)
	return sendMail(set.SMTPHost, port, set.SMTPSecurity, auth, hdr(from), hdr(to), []byte(body))
}
