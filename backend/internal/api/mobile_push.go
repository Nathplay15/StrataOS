package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/webpush"
)

// NAS notifications reach the app two ways: pushed to each paired phone of
// an administrator through its UnifiedPush endpoint, and kept in a short
// feed the app reads (GET /api/mobile/events) when it opens or, without a
// push distributor on the phone, every 15 minutes.

type mobileEvent struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Subject string    `json:"subject"`
	Message string    `json:"message"`
}

const maxMobileEvents = 200

type mobileFeed struct {
	mu     sync.Mutex
	loaded bool
	Next   int64         `json:"next"`
	Events []mobileEvent `json:"events"`
}

var feed = &mobileFeed{}

func (s *Server) feedPath() string { return filepath.Join(s.dataDir, "mobile-events.json") }

func (s *Server) feedLoadLocked() {
	if feed.loaded {
		return
	}
	feed.loaded = true
	if data, err := os.ReadFile(s.feedPath()); err == nil {
		_ = json.Unmarshal(data, feed)
	}
	if feed.Next < 1 {
		feed.Next = 1
	}
}

// mobileFeedAdd records a notification for the app.
func (s *Server) mobileFeedAdd(level, subject, msg string) mobileEvent {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	s.feedLoadLocked()
	if level == "" {
		level = "info"
	}
	e := mobileEvent{ID: feed.Next, Time: time.Now(), Level: level, Subject: subject, Message: msg}
	feed.Next++
	feed.Events = append(feed.Events, e)
	if len(feed.Events) > maxMobileEvents {
		feed.Events = feed.Events[len(feed.Events)-maxMobileEvents:]
	}
	if s.dataDir != "" {
		if data, err := json.Marshal(feed); err == nil {
			if err := sys.WriteFileAtomic(s.feedPath(), data, 0o600); err != nil {
				slog.Warn("save mobile events", "err", err)
			}
		}
	}
	return e
}

// mobileFeedSince returns the events after cursor and the new cursor. A
// cursor of 0 (first run) or from before a reset gets the latest ones.
func (s *Server) mobileFeedSince(since int64, limit int) ([]mobileEvent, int64) {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	s.feedLoadLocked()
	out := []mobileEvent{}
	if since >= feed.Next {
		since = 0 // the feed was reset (restore): start over
	}
	for _, e := range feed.Events {
		if e.ID > since {
			out = append(out, e)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, feed.Next - 1
}

var vapidOnce sync.Mutex

// vapid returns the key of this NAS for push services, created on first use.
func (s *Server) vapid() *webpush.VAPID {
	vapidOnce.Lock()
	defer vapidOnce.Unlock()
	var ks string
	s.store.Read(func(st *store.State) { ks = st.Mobile.VAPIDKey })
	if ks == "" {
		k, err := webpush.NewVAPIDKey()
		if err != nil {
			return nil
		}
		if s.store.Update(func(st *store.State) error { st.Mobile.VAPIDKey = k; return nil }) != nil {
			return nil
		}
		ks = k
	}
	v, err := webpush.ParseVAPID(ks)
	if err != nil {
		slog.Warn("vapid key", "err", err)
		return nil
	}
	return v
}

func (s *Server) vapidPublicKey() string {
	if v := s.vapid(); v != nil {
		return v.PublicKey()
	}
	return ""
}

// validPushURL accepts http(s) URLs with a host; where they lead is checked
// when connecting (pushClient).
func validPushURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(raw) > 2048 {
		return badRequest("invalid push endpoint")
	}
	return nil
}

// pushAddrAllowed refuses the NAS itself and link-local addresses (cloud
// metadata…): the endpoint comes from a phone, the request from root.
// Private LAN addresses stay allowed for a self-hosted ntfy server.
func pushAddrAllowed(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

var pushClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !pushAddrAllowed(ip) {
				return fmt.Errorf("push endpoint address %s refused", host)
			}
			return nil
		}}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// pushPayload is what the app receives (and decrypts when keys are set).
func pushPayload(host string, e mobileEvent) []byte {
	b, _ := json.Marshal(map[string]any{"type": "notification", "host": host, "id": e.ID, "level": e.Level, "subject": e.Subject, "message": e.Message, "time": e.Time.Unix()})
	if len(b) > webpush.MaxPlaintext {
		e.Message = e.Message[:min(len(e.Message), 2000)] + "…"
		b, _ = json.Marshal(map[string]any{"type": "notification", "host": host, "id": e.ID, "level": e.Level, "subject": e.Subject, "message": e.Message, "time": e.Time.Unix()})
	}
	return b
}

// pushDevice sends one event to one device and records the outcome.
func (s *Server) pushDevice(user string, d store.MobileDevice, e mobileEvent) error {
	host, _ := hostname()
	urgency := "normal"
	if e.Level == "error" || e.Level == "warning" {
		urgency = "high"
	}
	err := webpush.Send(pushClient, webpush.Message{Endpoint: d.PushURL, P256DH: d.PushP256DH, Auth: d.PushAuth,
		Payload: pushPayload(host, e), Urgency: urgency, TTL: 24 * time.Hour}, s.vapid(), "mailto:strataos@"+host+".invalid")
	_ = s.store.Update(func(st *store.State) error {
		if u := st.User(user); u != nil {
			if p := u.MobileDevice(d.ID); p != nil && p.PushURL == d.PushURL {
				p.PushError = ""
				if err != nil {
					p.PushError = err.Error()
				}
				if errors.Is(err, webpush.ErrGone) {
					// Unregistered on the phone: it re-registers when the app starts.
					p.PushURL, p.PushP256DH, p.PushAuth = "", "", ""
				}
			}
		}
		return nil
	})
	return err
}

type pushTarget struct {
	user string
	dev  store.MobileDevice
}

// mobilePushTargets: the devices of administrators with a push endpoint,
// and how many administrator devices there are (those without push read
// the feed every 15 minutes).
func (s *Server) mobilePushTargets() (out []pushTarget, devices int) {
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			if u.Role != "admin" || u.Disabled {
				continue
			}
			for _, d := range u.MobileDevices {
				devices++
				if d.PushURL != "" {
					out = append(out, pushTarget{u.Username, d})
				}
			}
		}
	})
	return out, devices
}

// pushMobile delivers an event to every target, in parallel.
func (s *Server) pushMobile(targets []pushTarget, e mobileEvent) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []string
	for _, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.pushDevice(t.user, t.dev, e); err != nil {
				mu.Lock()
				errs = append(errs, t.dev.Name+": "+err.Error())
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return errors.New(fmt.Sprint(errs))
	}
	return nil
}

// mobileNotify is called by sendNotification: the event always goes to the
// feed; use reports whether the mobile channel sends it (level, quiet hours).
// Paired administrator phones make the channel configured, push or not.
func (s *Server) mobileNotify(level, subject, msg string, use func(store.NotifyChannel) bool, ch store.NotifyChannel) (sent bool, err error) {
	e := s.mobileFeedAdd(level, subject, msg)
	targets, devices := s.mobilePushTargets()
	if devices == 0 || !use(ch) {
		return false, nil
	}
	return true, s.pushMobile(targets, e)
}

func (s *Server) mobileEventRoutes() {
	// Notifications since a cursor (administrators: NAS notifications).
	s.user("GET /api/mobile/events", func(r *http.Request) (any, error) {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		if !isAdmin(r) {
			return map[string]any{"cursor": since, "events": []mobileEvent{}}, nil
		}
		ev, cursor := s.mobileFeedSince(since, min(max(qInt(r, "limit", 50), 1), maxMobileEvents))
		return map[string]any{"cursor": cursor, "events": ev}, nil
	})
}
