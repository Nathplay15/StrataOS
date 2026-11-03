package gate

import (
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"
)

// FreeReserve is the space uploads by link always leave free on a volume.
const FreeReserve = 1 << 30

// Link states, as shown to the owner.
const (
	LinkActive    = "active"
	LinkExpired   = "expired"
	LinkExhausted = "exhausted" // download limit reached (and nothing else to do)
	LinkFull      = "full"      // upload size limit reached
)

// LinkLimits are the limits of a share link and what was used of them.
type LinkLimits struct {
	Expires      time.Time
	Download     bool
	Upload       bool
	MaxDownloads int
	Downloads    int
	MaxTotal     int64 // bytes all uploads together may take (0: no limit)
	Received     int64
}

// State tells whether a link still does something.
func (l LinkLimits) State(now time.Time) string {
	switch {
	case !l.Expires.IsZero() && !now.Before(l.Expires):
		return LinkExpired
	case l.Upload && l.MaxTotal > 0 && l.Received >= l.MaxTotal && !l.Download:
		return LinkFull
	case l.Download && l.MaxDownloads > 0 && l.Downloads >= l.MaxDownloads && !l.Upload:
		return LinkExhausted
	}
	return LinkActive
}

var (
	ErrUploadFull  = errors.New("this link has received all the data it accepts")
	ErrNoSpace     = errors.New("not enough free space on the NAS")
	ErrFileTooBig  = errors.New("file too large for this link")
	ErrBadGuest    = errors.New("invalid name")
	ErrBadEmail    = errors.New("invalid email address")
	ErrGuestNeeded = errors.New("your name and email address are requested")
)

// UploadBudget returns how many bytes the next file may have: the smallest
// of the per-file limit, what is left of the total, and the free space
// above FreeReserve. 0 limits mean none.
func UploadBudget(maxFile, maxTotal, received, free int64) (int64, error) {
	budget := free - FreeReserve
	if budget <= 0 {
		return 0, ErrNoSpace
	}
	if maxTotal > 0 {
		left := maxTotal - received
		if left <= 0 {
			return 0, ErrUploadFull
		}
		budget = min(budget, left)
	}
	if maxFile > 0 {
		budget = min(budget, maxFile)
	}
	return budget, nil
}

// Guest is who sent files to a file request, as they typed it.
type Guest struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// CleanGuest validates the optional name and email of a visitor; required
// asks for both. Control characters and long values are refused, since they
// end up in notifications and in the owner's list.
func CleanGuest(name, email string, required bool) (Guest, error) {
	name = strings.Join(strings.Fields(name), " ")
	email = strings.TrimSpace(email)
	if len([]rune(name)) > 80 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || r == '<' || r == '>' }) {
		return Guest{}, ErrBadGuest
	}
	if email != "" {
		a, err := mail.ParseAddress(email)
		if err != nil || a.Address != email || len(email) > 254 || a.Name != "" {
			return Guest{}, ErrBadEmail
		}
	}
	if required && (name == "" || email == "") {
		return Guest{}, ErrGuestNeeded
	}
	return Guest{Name: name, Email: email}, nil
}

// GuestFolder is the sub-folder of a file request where a guest's files go
// ("" when the guest gave no name): the name, made safe for a folder.
func GuestFolder(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '|' || unicode.IsControl(r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Trim(strings.TrimSpace(b.String()), ".")
	if len(s) > 64 { // bytes: keep some room for the file name below it
		rs := []rune(s)
		s = string(rs[:min(len(rs), 20)])
	}
	return strings.TrimSpace(s)
}

// MB converts megabytes to bytes.
func MB(n int64) int64 { return n << 20 }
