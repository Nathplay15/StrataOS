package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"

	"strataos/internal/store"
)

// Web push of the installable web app (PWA): the browser registers a
// PushSubscription, stored as a "browser" device of the account, and the NAS
// then reaches it like a paired phone (roadmap 0.15, "Web push notifications").

// browserDeviceID derives the device id of a push endpoint.
func browserDeviceID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])[:16]
}

// validPushKeys checks the Web Push keys (RFC 8291, base64url): P-256 DH is
// 65 bytes, the auth secret 16.
func validPushKeys(p256dh, auth string) error {
	if (p256dh == "") != (auth == "") || len(p256dh) > 200 || len(auth) > 64 {
		return badRequest("invalid push keys")
	}
	if p256dh != "" {
		k, err := base64.RawURLEncoding.DecodeString(p256dh)
		if err != nil || len(k) != 65 {
			return badRequest("invalid push keys")
		}
		a, err := base64.RawURLEncoding.DecodeString(auth)
		if err != nil || len(a) != 16 {
			return badRequest("invalid push keys")
		}
	}
	return nil
}

func (s *Server) browserPushRoutes() {
	// State of this account's browser subscription and the VAPID public key.
	s.user("GET /api/me/push", func(r *http.Request) (any, error) {
		enabled, name := false, ""
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				for _, d := range u.MobileDevices {
					if d.Browser && d.PushURL != "" {
						enabled, name = true, d.Name
					}
				}
			}
		})
		return map[string]any{"enabled": enabled, "name": name, "vapid": s.vapidPublicKey()}, nil
	})

	// The service worker of the web app subscribes here.
	s.user("POST /api/me/push/subscribe", func(r *http.Request) (any, error) {
		var req struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256DH string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
			Name string `json:"name"` // User-Agent of the browser
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := validPushURL(req.Endpoint); err != nil {
			return nil, err
		}
		if err := validPushKeys(req.Keys.P256DH, req.Keys.Auth); err != nil {
			return nil, err
		}
		name := cleanLabel(req.Name, 64)
		if name == "" {
			name = "Browser"
		}
		id := browserDeviceID(req.Endpoint)
		return nil, s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return errUnauthorized
			}
			if d := u.MobileDevice(id); d != nil {
				d.Kind, d.Browser = "browser", true
				d.PushURL, d.PushP256DH, d.PushAuth, d.PushError = req.Endpoint, req.Keys.P256DH, req.Keys.Auth, ""
				if d.Name == "" {
					d.Name = name
				}
				return nil
			}
			u.MobileDevices = append(u.MobileDevices, store.MobileDevice{
				ID: id, Name: name, Kind: "browser", Browser: true, CreatedAt: time.Now(),
				PushURL: req.Endpoint, PushP256DH: req.Keys.P256DH, PushAuth: req.Keys.Auth,
			})
			return nil
		})
	})

	// Unsubscribe: one endpoint (body or ?endpoint=), or every browser of the account.
	s.user("DELETE /api/me/push", func(r *http.Request) (any, error) {
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		_ = decode(r, &req)
		if req.Endpoint == "" {
			req.Endpoint = r.URL.Query().Get("endpoint")
		}
		return nil, s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return errUnauthorized
			}
			kept := u.MobileDevices[:0]
			for _, d := range u.MobileDevices {
				if d.Browser && (req.Endpoint == "" || d.PushURL == req.Endpoint) {
					continue
				}
				kept = append(kept, d)
			}
			u.MobileDevices = kept
			return nil
		})
	})
}
