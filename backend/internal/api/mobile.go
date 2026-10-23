package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"strataos/internal/store"
)

// The StrataOS app for Android pairs a phone once (password and two-factor
// code, or a one-time code shown as a QR code in the web interface) and then
// calls the API with "Authorization: Bearer <device token>".
//
// CSRF: the cookie checks (SameSite, Origin, X-Strata) exist because browsers
// attach cookies on their own. They never attach an Authorization header to a
// cross-site request without a CORS preflight, which StrataOS never allows,
// so bearer requests skip those checks. A request with a bearer token never
// falls back to the cookie.

const (
	deviceTokenPrefix = "stdev_"
	maxDevicesPerUser = 20
	pairingCodeTTL    = 10 * time.Minute
)

type deviceCtxKey struct{}

// deviceRef identifies the device a bearer request comes from.
type deviceRef struct{ user, id string }

func deviceFrom(ctx context.Context) *deviceRef {
	d, _ := ctx.Value(deviceCtxKey{}).(*deviceRef)
	return d
}

// deviceDenied lists what a device token may not do even when its user may:
// managing the account's sign-in methods (password, two-factor, passkeys,
// devices and pairing codes). A stolen phone must not be able to make itself
// a way back in once revoked.
func deviceDenied(p string) bool {
	return strings.HasPrefix(p, "/api/me/") && p != "/api/me/quotas" || p == "/api/logout"
}

// bearerAuth authenticates a request carrying a device token (see authenticate).
func (s *Server) bearerAuth(w http.ResponseWriter, r *http.Request, next http.Handler, adminOnly bool) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	ip := clientIP(r)
	ss, dev := s.deviceSession(tok, ip)
	if ss == nil {
		s.guardFail(ip, "mobile app", "")
		writeErr(w, errUnauthorized)
		return
	}
	if adminOnly && ss.Role != "admin" {
		writeErr(w, errForbidden)
		return
	}
	if deviceDenied(r.URL.Path) {
		writeErr(w, &Error{Status: http.StatusForbidden, Msg: "not allowed from the mobile app: use the web interface"})
		return
	}
	ctx := context.WithValue(context.WithValue(r.Context(), ctxKey{}, ss), deviceCtxKey{}, dev)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// deviceSession finds the device of a token and returns a session with the
// current role of its user (nil: unknown token, disabled or deleted user).
func (s *Server) deviceSession(tok, ip string) (*session, *deviceRef) {
	if !strings.HasPrefix(tok, deviceTokenPrefix) || len(tok) > 128 {
		return nil, nil
	}
	h := sha256.Sum256([]byte(tok))
	hash := hex.EncodeToString(h[:])
	var ss *session
	var dev *deviceRef
	stale := false
	s.store.Read(func(st *store.State) {
		for _, u := range st.Users {
			for _, d := range u.MobileDevices {
				if subtle.ConstantTimeCompare([]byte(d.TokenHash), []byte(hash)) == 1 && !u.Disabled {
					ss = &session{User: u.Username, Role: u.Role, Created: d.CreatedAt, Expires: time.Now().Add(time.Hour), IP: ip}
					dev = &deviceRef{user: u.Username, id: d.ID}
					stale = time.Since(d.LastSeen) > time.Minute || d.LastIP != ip
				}
			}
		}
	})
	if dev != nil && stale {
		// Last seen is written at most once a minute: the state file is
		// rewritten on every change.
		_ = s.store.Update(func(st *store.State) error {
			if u := st.User(dev.user); u != nil {
				if d := u.MobileDevice(dev.id); d != nil {
					d.LastSeen, d.LastIP = time.Now(), ip
				}
			}
			return nil
		})
	}
	return ss, dev
}

// devicePairings are one-time codes shown in the QR code of the web interface,
// kept (hashed) in memory only: a restart cancels them.
type devicePairings struct {
	mu sync.Mutex
	m  map[string]devicePairing
}

type devicePairing struct {
	user    string
	expires time.Time
}

var mobilePairing = &devicePairings{m: map[string]devicePairing{}}

func (p *devicePairings) create(user string) string {
	code := token()[:32]
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.m {
		if time.Now().After(v.expires) || v.user == user { // one live code per user
			delete(p.m, k)
		}
	}
	p.m[key(code)] = devicePairing{user: user, expires: time.Now().Add(pairingCodeTTL)}
	return code
}

// take consumes a code and returns its user ("" = unknown or expired).
func (p *devicePairings) take(code string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := key(strings.ToLower(strings.TrimSpace(code)))
	c, ok := p.m[k]
	delete(p.m, k)
	if !ok || time.Now().After(c.expires) {
		return ""
	}
	return c.user
}

// cleanLabel keeps a printable, bounded name given by a phone.
func cleanLabel(v string, max int) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(v))
	if !utf8.ValidString(v) {
		v = strings.ToValidUTF8(v, "")
	}
	for utf8.RuneCountInString(v) > max {
		_, n := utf8.DecodeLastRuneInString(v)
		v = v[:len(v)-n]
	}
	return v
}

type deviceView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Model     string    `json:"model"`
	App       string    `json:"app,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`
	LastIP    string    `json:"lastIp,omitempty"`
	Push      bool      `json:"push"`
	PushError string    `json:"pushError,omitempty"`
	Current   bool      `json:"current,omitempty"`
}

func viewDevice(d store.MobileDevice) deviceView {
	return deviceView{ID: d.ID, Name: d.Name, Model: d.Model, App: d.App, CreatedAt: d.CreatedAt, LastSeen: d.LastSeen, LastIP: d.LastIP, Push: d.PushURL != "", PushError: d.PushError}
}

// addDevice creates a device and returns its token (shown once).
func (s *Server) addDevice(user, name, model, app, ip string) (string, store.MobileDevice, error) {
	tok := deviceTokenPrefix + token()
	h := sha256.Sum256([]byte(tok))
	now := time.Now()
	d := store.MobileDevice{ID: token()[:16], Name: cleanLabel(name, 64), Model: cleanLabel(model, 80), App: cleanLabel(app, 32),
		TokenHash: hex.EncodeToString(h[:]), CreatedAt: now, LastSeen: now, LastIP: ip}
	if d.Name == "" {
		d.Name = d.Model
	}
	if d.Name == "" {
		d.Name = "Phone"
	}
	err := s.store.Update(func(st *store.State) error {
		u := st.User(user)
		if u == nil || u.Disabled {
			return errUnauthorized
		}
		if len(u.MobileDevices) >= maxDevicesPerUser {
			return badRequest(fmt.Sprintf("at most %d mobile devices per account: remove one first", maxDevicesPerUser))
		}
		u.MobileDevices = append(u.MobileDevices, d)
		s.store.LogLocked(st, "info", "auth", user, "Mobile device paired: "+d.Name+" from "+ip)
		return nil
	})
	return tok, d, err
}

// certFingerprint is the SHA-256 of the certificate served for host, as
// the app shows and pins it ("AB:CD:…").
func (s *Server) certFingerprint(host string) string {
	if s.certs == nil {
		return ""
	}
	c, err := s.certs.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
	if err != nil || c == nil || len(c.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(c.Certificate[0])
	var b strings.Builder
	for i, x := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", x)
	}
	return b.String()
}

func (s *Server) mobileRoutes() {
	s.mobileEventRoutes()
	// Pairing: with the password (and the two-factor code when the account
	// has one), or with a one-time code from the web interface. Failures
	// count like failed sign-ins (limiter and address blocking).
	s.mux.Handle("POST /api/mobile/pair", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"})
			return
		}
		var req struct {
			Username    string `json:"username"`
			Password    string `json:"password"`
			Code        string `json:"code"`
			PairingCode string `json:"pairingCode"`
			DeviceName  string `json:"deviceName"`
			Model       string `json:"model"`
			App         string `json:"app"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		var u store.User
		if req.PairingCode != "" {
			name := mobilePairing.take(req.PairingCode)
			ok := false
			s.store.Read(func(st *store.State) {
				if p := st.User(name); name != "" && p != nil && !p.Disabled {
					u, ok = *p, true
				}
			})
			if !ok {
				s.store.Log("warning", "auth", "", "Invalid mobile pairing code from "+ip)
				s.guardFail(ip, "mobile app", "")
				writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "invalid or expired pairing code"})
				return
			}
		} else {
			var resp map[string]any
			u, resp = s.passwordSignIn(r, req.Username, req.Password, req.Code, ip)
			if resp != nil {
				writeJSON(w, http.StatusUnauthorized, resp)
				return
			}
		}
		s.limiter.reset(ip)
		tok, d, err := s.addDevice(u.Username, req.DeviceName, req.Model, req.App, ip)
		if err != nil {
			writeErr(w, err)
			return
		}
		host, _ := hostname()
		writeJSON(w, http.StatusOK, map[string]any{
			"token": tok, "device": viewDevice(d),
			"user":   meResp{Username: u.Username, FullName: u.FullName, Role: u.Role, TwoFactor: u.TOTPSecret != ""},
			"server": map[string]string{"hostname": host, "version": s.version},
		})
	}))

	// What the app needs after pairing (and to check its token still works).
	s.user("GET /api/mobile/info", func(r *http.Request) (any, error) {
		host, _ := hostname()
		out := map[string]any{"hostname": host, "version": s.version, "vapidKey": s.vapidPublicKey(),
			"features": []string{"uploads", "exists", "events", "push"}}
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				out["user"] = meResp{Username: u.Username, FullName: u.FullName, Role: u.Role, TwoFactor: u.TOTPSecret != ""}
				if dev := deviceFrom(r.Context()); dev != nil {
					if d := u.MobileDevice(dev.id); d != nil {
						v := viewDevice(*d)
						v.Current = true
						out["device"] = v
					}
				}
			}
		})
		return out, nil
	})

	// The app registers its UnifiedPush endpoint (or clears it: pushUrl "").
	s.user("PUT /api/mobile/device", func(r *http.Request) (any, error) {
		dev := deviceFrom(r.Context())
		if dev == nil {
			return nil, badRequest("only the mobile app can do this")
		}
		var req struct {
			Name    *string `json:"name"`
			App     *string `json:"app"`
			PushURL *string `json:"pushUrl"`
			P256DH  string  `json:"p256dh"`
			Auth    string  `json:"auth"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.PushURL != nil && *req.PushURL != "" {
			if err := validPushURL(*req.PushURL); err != nil {
				return nil, err
			}
			if (req.P256DH == "") != (req.Auth == "") || len(req.P256DH) > 200 || len(req.Auth) > 64 {
				return nil, badRequest("invalid push keys")
			}
		}
		return nil, s.store.Update(func(st *store.State) error {
			u := st.User(dev.user)
			if u == nil {
				return errUnauthorized
			}
			d := u.MobileDevice(dev.id)
			if d == nil {
				return errUnauthorized
			}
			if req.Name != nil && cleanLabel(*req.Name, 64) != "" {
				d.Name = cleanLabel(*req.Name, 64)
			}
			if req.App != nil {
				d.App = cleanLabel(*req.App, 32)
			}
			if req.PushURL != nil {
				d.PushURL, d.PushP256DH, d.PushAuth, d.PushError = *req.PushURL, req.P256DH, req.Auth, ""
				if d.PushURL == "" {
					d.PushP256DH, d.PushAuth = "", ""
				}
			}
			return nil
		})
	})

	// The app signs out: its token stops working.
	s.user("DELETE /api/mobile/device", func(r *http.Request) (any, error) {
		dev := deviceFrom(r.Context())
		if dev == nil {
			return nil, badRequest("only the mobile app can do this")
		}
		return nil, s.removeDevice(dev.user, dev.id, "Mobile device signed out: ")
	})

	// Web interface: the devices of the current user.
	s.user("GET /api/me/devices", func(r *http.Request) (any, error) {
		out := []deviceView{}
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				for _, d := range u.MobileDevices {
					out = append(out, viewDevice(d))
				}
			}
		})
		return out, nil
	})

	s.user("PUT /api/me/devices/{id}", func(r *http.Request) (any, error) {
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name := cleanLabel(req.Name, 64)
		if name == "" {
			return nil, badRequest("name required")
		}
		return nil, s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil || u.MobileDevice(r.PathValue("id")) == nil {
				return notFound("device not found")
			}
			u.MobileDevice(r.PathValue("id")).Name = name
			return nil
		})
	})

	s.user("DELETE /api/me/devices/{id}", func(r *http.Request) (any, error) {
		return nil, s.removeDevice(userOf(r), r.PathValue("id"), "Mobile device revoked: ")
	})

	s.user("POST /api/me/devices/{id}/test", func(r *http.Request) (any, error) {
		var d store.MobileDevice
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				if p := u.MobileDevice(r.PathValue("id")); p != nil {
					d = *p
				}
			}
		})
		if d.ID == "" {
			return nil, notFound("device not found")
		}
		if d.PushURL == "" {
			return nil, badRequest("this device has not registered for push notifications (install a UnifiedPush distributor such as ntfy on the phone)")
		}
		return nil, s.pushDevice(userOf(r), d, mobileEvent{Time: time.Now(), Level: "info", Subject: "Test", Message: "Notifications from StrataOS reach this phone."})
	})

	// A one-time pairing code for the QR code; the address comes from the
	// browser, the certificate fingerprint from the server.
	s.user("POST /api/me/devices/pairing", func(r *http.Request) (any, error) {
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		httpsPort := "7443"
		if s.certs != nil && s.certs.HTTPSPort != "" {
			httpsPort = s.certs.HTTPSPort
		}
		code := mobilePairing.create(userOf(r))
		s.audit(r, "auth", "Mobile pairing code created")
		return map[string]any{"code": code, "expires": time.Now().Add(pairingCodeTTL), "fingerprint": s.certFingerprint(host),
			"httpsPort": httpsPort, "user": userOf(r)}, nil
	})
}

func (s *Server) removeDevice(user, id, msg string) error {
	return s.store.Update(func(st *store.State) error {
		u := st.User(user)
		if u == nil || u.MobileDevice(id) == nil {
			return notFound("device not found")
		}
		name := u.MobileDevice(id).Name
		u.MobileDevices = slices.DeleteFunc(u.MobileDevices, func(d store.MobileDevice) bool { return d.ID == id })
		s.store.LogLocked(st, "info", "auth", user, msg+name)
		return nil
	})
}

// passwordSignIn checks a username, password and two-factor code like
// POST /api/login. It returns the account, or the JSON answer of a failure
// (twoFactor: true when a code is needed or wrong).
func (s *Server) passwordSignIn(r *http.Request, username, password, code, ip string) (store.User, map[string]any) {
	username = strings.ToLower(strings.TrimSpace(username))
	var u store.User
	var found bool
	s.store.Read(func(st *store.State) {
		if p := st.User(username); p != nil {
			u, found = *p, true
		}
	})
	if (!found || u.Source != "") && !u.Disabled {
		if du, ok := s.directoryLogin(r.Context(), username, password); ok {
			u, found = du, true
		} else {
			found = false
		}
	} else if found && !u.Disabled && !checkPassword(u.PasswordHash, password) {
		found = false
	}
	if !found || u.Disabled {
		if !found {
			checkPassword(dummyHash(), password)
		}
		s.store.Log("warning", "auth", username, "Failed mobile app sign-in from "+ip)
		s.guardFail(ip, "mobile app", username)
		return u, map[string]any{"error": errBadLogin.Error()}
	}
	if u.TOTPSecret != "" {
		if strings.TrimSpace(code) == "" {
			return u, map[string]any{"error": "two-factor code required", "twoFactor": true}
		}
		if !s.checkSecondFactor(u.Username, code) {
			s.store.Log("warning", "auth", u.Username, "Wrong two-factor code from "+ip)
			s.guardFail(ip, "mobile app", u.Username)
			return u, map[string]any{"error": "wrong two-factor code", "twoFactor": true}
		}
	}
	return u, nil
}
