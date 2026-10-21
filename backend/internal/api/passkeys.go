package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"strataos/internal/directory"
	"strataos/internal/store"
)

// Passkeys (WebAuthn): sign-in with the fingerprint reader, Face ID, Windows
// Hello or a security key. User verification is required, so a passkey
// sign-in counts as two factors and skips the TOTP prompt.

const (
	passkeyTimeout = 3 * time.Minute
	maxPasskeys    = 20
	// maxPasskeyFlows bounds the pending challenges: login begin is public.
	maxPasskeyFlows = 2000
)

var (
	errPasskeyIP      = badRequest("Open the NAS by its name, e.g. https://strataos.local:7443 or your domain, to use passkeys")
	errPasskeyHTTP    = badRequest("Passkeys need a secure connection: open the interface over HTTPS with a valid certificate")
	errPasskeyHost    = badRequest("Passkeys need a valid host name in the address")
	errPasskeyExpired = badRequest("This passkey request has expired, try again")
	errPasskeyRefused = &Error{Status: http.StatusUnauthorized, Msg: "passkey not accepted"}
)

// relyingParty is what a passkey is bound to: the name the NAS was opened
// with (RP ID) and the exact origin of the page.
type relyingParty struct {
	ID     string `json:"rpId"`
	Origin string `json:"origin"`
}

var hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// deriveRelyingParty takes the Host of a request. WebAuthn refuses IP
// addresses, and browsers only allow it in a secure context: HTTPS, or plain
// HTTP on localhost.
func deriveRelyingParty(hostport string, https bool) (relyingParty, error) {
	host, port := hostport, ""
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		host, port = h, p
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" || len(host) > 253 {
		return relyingParty{}, errPasskeyHost
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return relyingParty{}, errPasskeyIP
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if !hostLabelRe.MatchString(l) {
			return relyingParty{}, errPasskeyHost
		}
	}
	// A host ending in a number is parsed as an IPv4 address by browsers (10.1, 0x7f.1…).
	if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" || strings.HasPrefix(last, "0x") {
		return relyingParty{}, errPasskeyIP
	}
	local := host == "localhost" || strings.HasSuffix(host, ".localhost")
	if !https && !local {
		return relyingParty{}, errPasskeyHTTP
	}
	scheme := "http"
	if https {
		scheme = "https"
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return relyingParty{}, errPasskeyHost
		}
		if (https && port == "443") || (!https && port == "80") {
			port = "" // browsers leave the default port out of the origin
		}
	}
	origin := scheme + "://" + host
	if port != "" {
		origin += ":" + port
	}
	return relyingParty{ID: host, Origin: origin}, nil
}

func requestRelyingParty(r *http.Request) (relyingParty, error) {
	https := r.TLS != nil
	// A tunnel running on the NAS (cloudflared) ends TLS itself.
	if peer, _, err := net.SplitHostPort(r.RemoteAddr); !https && err == nil {
		if a, err := netip.ParseAddr(peer); err == nil && a.Unmap().IsLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			https = true
		}
	}
	return deriveRelyingParty(r.Host, https)
}

func newWebAuthn(rp relyingParty) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:                  rp.ID,
		RPDisplayName:         "StrataOS",
		RPOrigins:             []string{rp.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Timeout: passkeyTimeout, TimeoutUVD: passkeyTimeout},
			Registration: webauthn.TimeoutConfig{Timeout: passkeyTimeout, TimeoutUVD: passkeyTimeout},
		},
		// Password managers sometimes add extension results nobody asked for;
		// none of them is used here.
		ExtensionsUnsolicitedOutputPolicy: protocol.UnsolicitedOutputPolicyIgnore,
	})
}

// passkeyUser adapts a user to the library, with the passkeys of one name only.
type passkeyUser struct {
	u    store.User
	rpID string
}

func (p *passkeyUser) WebAuthnID() []byte   { return p.u.WebAuthnID }
func (p *passkeyUser) WebAuthnName() string { return p.u.Username }
func (p *passkeyUser) WebAuthnDisplayName() string {
	if p.u.FullName != "" {
		return p.u.FullName
	}
	return p.u.Username
}
func (p *passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	var out []webauthn.Credential
	for _, k := range p.u.Passkeys {
		if k.RPID != p.rpID {
			continue
		}
		id, err := base64.RawURLEncoding.DecodeString(k.ID)
		if err != nil {
			continue
		}
		c := webauthn.Credential{ID: id, PublicKey: k.PublicKey,
			Flags:         webauthn.CredentialFlags{UserPresent: true, UserVerified: true, BackupEligible: k.BackupEligible, BackupState: k.BackupState},
			Authenticator: webauthn.Authenticator{AAGUID: k.AAGUID, SignCount: k.SignCount}}
		for _, t := range k.Transports {
			c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
		}
		out = append(out, c)
	}
	return out
}

// copyUser detaches a user from the state (its slices are shared otherwise).
func copyUser(u *store.User) store.User {
	c := *u
	c.Passkeys = slices.Clone(u.Passkeys)
	c.WebAuthnID = slices.Clone(u.WebAuthnID)
	return c
}

// passkeyFlow is a pending ceremony: single use, short-lived, kept in memory.
type passkeyFlow struct {
	kind    string // "register" or "login"
	user    string // register: owner; login: the typed username ("" = any passkey)
	sess    string // register: key of the session that started it
	name    string // register: name of the new passkey
	rp      relyingParty
	data    webauthn.SessionData
	expires time.Time
}

type passkeyFlows struct {
	mu sync.Mutex
	m  map[string]*passkeyFlow
}

var pendingPasskeys = &passkeyFlows{m: map[string]*passkeyFlow{}}

func (f *passkeyFlows) put(fl *passkeyFlow, now time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.m {
		if now.After(v.expires) {
			delete(f.m, k)
		}
	}
	if len(f.m) >= maxPasskeyFlows {
		return "", &Error{Status: http.StatusTooManyRequests, Msg: "too many passkey requests, try again in a few minutes"}
	}
	id := token()
	fl.expires = now.Add(passkeyTimeout + 15*time.Second)
	f.m[id] = fl
	return id, nil
}

// take removes the flow whatever happens next: a challenge is never reused.
func (f *passkeyFlows) take(id, kind string, now time.Time) *passkeyFlow {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[id]
	if !ok {
		return nil
	}
	delete(f.m, id)
	if fl.kind != kind || now.After(fl.expires) {
		return nil
	}
	return fl
}

func validPasskeyName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" {
		n = "Passkey"
	}
	if len([]rune(n)) > 64 || strings.IndexFunc(n, unicode.IsControl) >= 0 {
		return "", badRequest("invalid passkey name (64 characters at most)")
	}
	return n, nil
}

func newUserHandle() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

func sessionKey(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil {
		return key(c.Value)
	}
	return ""
}

// confirmPassword re-checks the password of the signed-in user before a
// sensitive change (local hash, or the directory for directory accounts).
func (s *Server) confirmPassword(ctx context.Context, name, password string) bool {
	if password == "" {
		return false
	}
	var u store.User
	var c store.Directory
	s.store.Read(func(st *store.State) {
		if x := st.User(name); x != nil {
			u = *x
		}
		c = st.Directory
	})
	if u.Source != "" {
		login, ok := directory.Canonical(c, name)
		return ok && !c.JoinedAt.IsZero() && directory.Authenticate(ctx, c, login, password) == nil
	}
	return u.PasswordHash != "" && checkPassword(u.PasswordHash, password)
}

// lookupLogin finds the account a typed username designates (directory
// accounts are stored with their system spelling, e.g. DOMAIN\alice).
func lookupLogin(st *store.State, login string) *store.User {
	if u := st.User(login); u != nil {
		return u
	}
	names := []string{login}
	if !st.Directory.JoinedAt.IsZero() {
		if c, ok := directory.Canonical(st.Directory, login); ok {
			names = append(names, c)
		}
	}
	for _, u := range st.Users {
		for _, n := range names {
			if strings.EqualFold(u.Username, n) {
				return u
			}
		}
	}
	return nil
}

type passkeyView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	RPID       string    `json:"rpId"`
	Here       bool      `json:"here"` // usable on the name the page is open with
	Synced     bool      `json:"synced"`
	Transports []string  `json:"transports"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsed   time.Time `json:"lastUsed,omitzero"`
}

func viewPasskey(k store.Passkey, rpID string) passkeyView {
	t := k.Transports
	if t == nil {
		t = []string{}
	}
	return passkeyView{ID: k.ID, Name: k.Name, RPID: k.RPID, Here: k.RPID == rpID, Synced: k.BackupEligible, Transports: t, CreatedAt: k.CreatedAt, LastUsed: k.LastUsed}
}

// publicAuth serves an unauthenticated JSON route with the small body limit of login.
func publicAuth(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		v, err := h(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
}

func (s *Server) passkeyRoutes() {
	// Whether passkeys can work on the address the page is open with.
	s.public("GET /api/login/passkey", func(r *http.Request) (any, error) {
		rp, err := requestRelyingParty(r)
		if err != nil {
			return map[string]any{"available": false, "reason": err.Error()}, nil
		}
		return map[string]any{"available": true, "rpId": rp.ID}, nil
	})

	s.mux.Handle("POST /api/login/passkey/begin", publicAuth(func(r *http.Request) (any, error) {
		var req struct {
			Username string `json:"username"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		rp, err := requestRelyingParty(r)
		if err != nil {
			return nil, err
		}
		wa, err := newWebAuthn(rp)
		if err != nil {
			return nil, err
		}
		opts := []webauthn.LoginOption{webauthn.WithUserVerification(protocol.VerificationRequired)}
		login := strings.ToLower(strings.TrimSpace(req.Username))
		if login != "" {
			// Username first: only that account's passkeys are offered. An
			// unknown name gets an open request (no enumeration) that cannot succeed.
			var descs []protocol.CredentialDescriptor
			s.store.Read(func(st *store.State) {
				if u := lookupLogin(st, login); u != nil {
					login = u.Username
					pu := &passkeyUser{u: copyUser(u), rpID: rp.ID}
					descs = webauthn.Credentials(pu.WebAuthnCredentials()).CredentialDescriptors()
				}
			})
			if len(descs) > 0 {
				opts = append(opts, webauthn.WithAllowedCredentials(descs))
			}
		}
		assertion, data, err := wa.BeginDiscoverableLogin(opts...)
		if err != nil {
			return nil, err
		}
		id, err := pendingPasskeys.put(&passkeyFlow{kind: "login", user: login, rp: rp, data: *data}, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{"flow": id, "publicKey": assertion.Response}, nil
	}))

	s.mux.Handle("POST /api/login/passkey/finish", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"})
			return
		}
		var req struct {
			Flow       string          `json:"flow"`
			Remember   bool            `json:"remember"`
			Credential json.RawMessage `json:"credential"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		fl := pendingPasskeys.take(req.Flow, "login", time.Now())
		if fl == nil {
			writeErr(w, errPasskeyExpired)
			return
		}
		u, err := s.passkeyLogin(r, fl, req.Credential)
		if err != nil {
			who := fl.user
			if u.Username != "" {
				who = u.Username
			}
			slog.Info("passkey sign-in refused", "ip", ip, "user", who, "err", err)
			s.store.Log("warning", "auth", who, "Failed passkey sign-in from "+ip)
			s.guardFail(ip, "web", who)
			writeErr(w, errPasskeyRefused)
			return
		}
		s.limiter.reset(ip)
		ttl := s.sessionTTL()
		if req.Remember {
			ttl = 30 * 24 * time.Hour
		}
		s.setCookie(w, r, s.sessions.create(u.Username, u.Role, ip, ttl), ttl)
		s.store.Log("info", "auth", u.Username, "Signed in with a passkey from "+ip)
		writeJSON(w, http.StatusOK, meResp{Username: u.Username, FullName: u.FullName, Role: u.Role, TwoFactor: u.TOTPSecret != ""})
	}))

	s.user("GET /api/me/passkeys", func(r *http.Request) (any, error) {
		rp, rpErr := requestRelyingParty(r)
		out := map[string]any{"available": rpErr == nil, "rpId": rp.ID}
		if rpErr != nil {
			out["reason"] = rpErr.Error()
		}
		list := []passkeyView{}
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				for _, k := range u.Passkeys {
					list = append(list, viewPasskey(k, rp.ID))
				}
			}
		})
		out["passkeys"] = list
		return out, nil
	})

	s.user("POST /api/me/passkeys/begin", func(r *http.Request) (any, error) {
		var req struct {
			Password string `json:"password"`
			Name     string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		rp, err := requestRelyingParty(r)
		if err != nil {
			return nil, err
		}
		name, err := validPasskeyName(req.Name)
		if err != nil {
			return nil, err
		}
		me, ip := userOf(r), clientIP(r)
		if !s.limiter.attempt(ip) {
			return nil, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"}
		}
		if !s.confirmPassword(r.Context(), me, req.Password) {
			return nil, badRequest("incorrect password")
		}
		s.limiter.reset(ip)
		var u store.User
		err = s.store.Update(func(st *store.State) error {
			x := st.User(me)
			if x == nil {
				return notFound("user not found")
			}
			if len(x.Passkeys) >= maxPasskeys {
				return badRequest(fmt.Sprintf("%d passkeys at most per account: delete one first", maxPasskeys))
			}
			if len(x.WebAuthnID) == 0 {
				x.WebAuthnID = newUserHandle()
			}
			u = copyUser(x)
			return nil
		})
		if err != nil {
			return nil, err
		}
		wa, err := newWebAuthn(rp)
		if err != nil {
			return nil, err
		}
		pu := &passkeyUser{u: u, rpID: rp.ID}
		creation, data, err := wa.BeginRegistration(pu, webauthn.WithExclusions(webauthn.Credentials(pu.WebAuthnCredentials()).CredentialDescriptors()))
		if err != nil {
			return nil, err
		}
		id, err := pendingPasskeys.put(&passkeyFlow{kind: "register", user: me, sess: sessionKey(r), name: name, rp: rp, data: *data}, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{"flow": id, "publicKey": creation.Response}, nil
	})

	s.user("POST /api/me/passkeys/finish", func(r *http.Request) (any, error) {
		var req struct {
			Flow       string          `json:"flow"`
			Credential json.RawMessage `json:"credential"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		me := userOf(r)
		fl := pendingPasskeys.take(req.Flow, "register", time.Now())
		if fl == nil || fl.user != me || fl.sess != sessionKey(r) {
			return nil, errPasskeyExpired
		}
		k, err := s.passkeyRegister(r, fl, req.Credential)
		if err != nil {
			return nil, err
		}
		s.audit(r, "auth", fmt.Sprintf("Passkey %s added (%s)", k.Name, k.RPID))
		return viewPasskey(k, fl.rp.ID), nil
	})

	s.user("PUT /api/me/passkeys/{id}", func(r *http.Request) (any, error) {
		var req struct {
			Name string `json:"name"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		name, err := validPasskeyName(req.Name)
		if err != nil {
			return nil, err
		}
		err = s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil || u.PasskeyByID(r.PathValue("id")) == nil {
				return notFound("passkey not found")
			}
			u.PasskeyByID(r.PathValue("id")).Name = name
			return nil
		})
		if err == nil {
			s.audit(r, "auth", "Passkey renamed to "+name)
		}
		return nil, err
	})

	// Deleting the last passkey is fine: the password stays.
	s.user("DELETE /api/me/passkeys/{id}", func(r *http.Request) (any, error) {
		var name string
		err := s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil {
				return notFound("passkey not found")
			}
			k := u.PasskeyByID(r.PathValue("id"))
			if k == nil {
				return notFound("passkey not found")
			}
			name = k.Name
			u.Passkeys = slices.DeleteFunc(u.Passkeys, func(p store.Passkey) bool { return p.ID == r.PathValue("id") })
			return nil
		})
		if err == nil {
			s.audit(r, "auth", "Passkey "+name+" deleted")
		}
		return nil, err
	})

	s.admin("DELETE /api/users/{name}/passkeys", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil {
				return notFound("user not found")
			}
			u.Passkeys = nil
			return nil
		})
		if err != nil {
			return nil, err
		}
		if name != userOf(r) {
			s.sessions.dropUser(name)
		}
		s.audit(r, "users", "Passkeys removed for "+name)
		return nil, nil
	})
}

// passkeyRegister verifies a new credential and stores it.
func (s *Server) passkeyRegister(r *http.Request, fl *passkeyFlow, raw json.RawMessage) (store.Passkey, error) {
	if rp, err := requestRelyingParty(r); err != nil || rp != fl.rp {
		return store.Passkey{}, errPasskeyExpired
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(raw)
	if err != nil {
		slog.Info("passkey registration", "err", err)
		return store.Passkey{}, badRequest("the passkey could not be verified")
	}
	var u store.User
	s.store.Read(func(st *store.State) {
		if x := st.User(fl.user); x != nil {
			u = copyUser(x)
		}
	})
	if u.Username == "" {
		return store.Passkey{}, notFound("user not found")
	}
	wa, err := newWebAuthn(fl.rp)
	if err != nil {
		return store.Passkey{}, err
	}
	cred, err := wa.CreateCredential(&passkeyUser{u: u, rpID: fl.rp.ID}, fl.data, parsed)
	if err != nil {
		slog.Info("passkey registration", "err", err)
		return store.Passkey{}, badRequest("the passkey could not be verified")
	}
	k := store.Passkey{
		ID: base64.RawURLEncoding.EncodeToString(cred.ID), PublicKey: cred.PublicKey, SignCount: cred.Authenticator.SignCount,
		RPID: fl.rp.ID, Name: fl.name, AAGUID: cred.Authenticator.AAGUID,
		BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState, CreatedAt: time.Now(),
	}
	for _, t := range cred.Transport {
		k.Transports = append(k.Transports, string(t))
	}
	err = s.store.Update(func(st *store.State) error {
		x := st.User(fl.user)
		if x == nil || !bytes.Equal(x.WebAuthnID, u.WebAuthnID) {
			return notFound("user not found")
		}
		for _, o := range st.Users {
			if o.PasskeyByID(k.ID) != nil {
				return badRequest("this passkey is already registered")
			}
		}
		if len(x.Passkeys) >= maxPasskeys {
			return badRequest(fmt.Sprintf("%d passkeys at most per account: delete one first", maxPasskeys))
		}
		x.Passkeys = append(x.Passkeys, k)
		return nil
	})
	return k, err
}

// passkeyLogin verifies an assertion; the returned user names the account
// even on failure when the passkey designated one (for the event log).
func (s *Server) passkeyLogin(r *http.Request, fl *passkeyFlow, raw json.RawMessage) (store.User, error) {
	if rp, err := requestRelyingParty(r); err != nil || rp != fl.rp {
		return store.User{}, errors.New("request made on another address")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		return store.User{}, err
	}
	var owner store.User
	lookup := func(rawID, userHandle []byte) (webauthn.User, error) {
		id := base64.RawURLEncoding.EncodeToString(rawID)
		var found bool
		s.store.Read(func(st *store.State) {
			for _, u := range st.Users {
				if len(u.WebAuthnID) > 0 && bytes.Equal(u.WebAuthnID, userHandle) {
					if k := u.PasskeyByID(id); k != nil && k.RPID == fl.rp.ID {
						owner, found = copyUser(u), true
					}
					return
				}
			}
		})
		if !found {
			return nil, errors.New("unknown passkey")
		}
		return &passkeyUser{u: owner, rpID: fl.rp.ID}, nil
	}
	wa, err := newWebAuthn(fl.rp)
	if err != nil {
		return store.User{}, err
	}
	_, cred, err := wa.ValidatePasskeyLogin(lookup, fl.data, parsed)
	if err != nil {
		return owner, err
	}
	if fl.user != "" && owner.Username != fl.user {
		return owner, errors.New("passkey of another account")
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	if cred.Authenticator.CloneWarning {
		var name string
		if k := owner.PasskeyByID(id); k != nil {
			name = k.Name
		}
		msg := fmt.Sprintf("Passkey %s of %s refused: its signature counter went backwards (cloned authenticator?)", name, owner.Username)
		s.store.Log("warning", "auth", owner.Username, msg)
		s.notify("warning", "Suspicious passkey", msg)
		return owner, errors.New("signature counter went backwards")
	}
	var u store.User
	var dir store.Directory
	err = s.store.Update(func(st *store.State) error {
		x := st.User(owner.Username)
		if x == nil || !bytes.Equal(x.WebAuthnID, owner.WebAuthnID) {
			return errors.New("account deleted")
		}
		k := x.PasskeyByID(id)
		if k == nil {
			return errors.New("passkey deleted")
		}
		// The passkey is never a way around a disabled account.
		if x.Disabled {
			return errors.New("account disabled")
		}
		k.SignCount, k.BackupState, k.LastUsed = cred.Authenticator.SignCount, cred.Flags.BackupState, time.Now()
		u, dir = copyUser(x), st.Directory
		return nil
	})
	if err != nil {
		return owner, err
	}
	// Directory accounts: only while the NAS is still joined.
	if u.Source != "" && dir.JoinedAt.IsZero() {
		return u, errors.New("directory left")
	}
	return u, nil
}
