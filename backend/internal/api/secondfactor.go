package api

// Second factor at sign-in: after the password (or an OIDC sign-in), an
// account with an authenticator app or security keys gets a pending sign-in
// (cookie strata_2fa, single use, 5 minutes, 5 tries) completed by
// POST /api/login/2fa (code) or /api/login/key/* (WebAuthn). Security keys
// (YubiKey…) are WebAuthn credentials registered as a second factor: not
// discoverable, user verification preferred (or discouraged for old U2F
// keys), unlike passkeys, which replace the password.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"strataos/internal/gate"
	"strataos/internal/store"
	"strataos/internal/totp"
)

const (
	// session.MFA of an OIDC sign-in whose provider checked a second
	// factor (a hardware key for the second).
	mfaIdP         = "idp"
	mfaIdPHardware = "idp-hwk"

	pendingCookie = "strata_2fa"
	pendingTTL    = 5 * time.Minute
	pendingTries  = 5
	maxKeys       = 20
)

// pendingLogin: first factor checked, second factor awaited.
type pendingLogin struct {
	user     string
	remember bool
	ip       string
	via      string // "" password, else the OIDC provider name (for the log)
	mfa      string // session.MFA once complete
	tries    int
	expires  time.Time
}

type pendingLogins struct {
	mu sync.Mutex
	m  map[string]*pendingLogin
}

var pending2FA = &pendingLogins{m: map[string]*pendingLogin{}}

func (p *pendingLogins) put(pl *pendingLogin, now time.Time) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.m {
		if now.After(v.expires) {
			delete(p.m, k)
		}
	}
	if len(p.m) >= maxPasskeyFlows {
		return "", &Error{Status: http.StatusTooManyRequests, Msg: "too many sign-ins in progress, try again in a few minutes"}
	}
	t := token()
	pl.expires = now.Add(pendingTTL)
	p.m[key(t)] = pl
	return t, nil
}

// get returns a copy of the pending sign-in of the cookie.
func (p *pendingLogins) get(r *http.Request, now time.Time) (pendingLogin, string, bool) {
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		return pendingLogin{}, "", false
	}
	k := key(c.Value)
	p.mu.Lock()
	defer p.mu.Unlock()
	pl, ok := p.m[k]
	if !ok || now.After(pl.expires) {
		delete(p.m, k)
		return pendingLogin{}, "", false
	}
	return *pl, k, true
}

// fail counts a wrong second factor; the pending sign-in ends after pendingTries.
func (p *pendingLogins) fail(k string) {
	p.mu.Lock()
	if pl, ok := p.m[k]; ok {
		if pl.tries++; pl.tries >= pendingTries {
			delete(p.m, k)
		}
	}
	p.mu.Unlock()
}

func (p *pendingLogins) drop(k string) {
	p.mu.Lock()
	delete(p.m, k)
	p.mu.Unlock()
}

func setPendingCookie(w http.ResponseWriter, r *http.Request, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: value, Path: "/api/login", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil, MaxAge: int(ttl.Seconds())})
}

// startSession opens a new session (a new token every time: no fixation)
// and answers like POST /api/login.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User, remember bool, mfa, how string) {
	ip := clientIP(r)
	s.limiter.reset(ip)
	ttl := s.sessionTTL()
	if remember {
		ttl = 30 * 24 * time.Hour
	}
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.delete(c.Value) // a session already open in this browser ends
	}
	s.setCookie(w, r, s.sessions.add(&session{User: u.Username, Role: u.Role, Expires: time.Now().Add(ttl), Created: time.Now(), IP: ip, MFA: mfa}), ttl)
	s.store.Log("info", "auth", u.Username, how+ip)
	writeJSON(w, http.StatusOK, meResp{Username: u.Username, FullName: u.FullName, Role: u.Role, TwoFactor: hasSecondFactor(&u), TwoFactorPrompt: s.twoFactorPrompt(u.Username, true, mfa)})
}

type factorMethods struct {
	TOTP     bool `json:"totp"`
	Key      bool `json:"key"`
	Recovery bool `json:"recovery"`
	// KeyHere: a security key or passkey of the account works on the
	// address the page is open with (keys are bound to a host name).
	KeyHere   bool   `json:"keyHere"`
	KeyReason string `json:"keyReason,omitempty"`
}

func (s *Server) methodsOf(r *http.Request, u store.User) factorMethods {
	var m factorMethods
	s.store.Read(func(st *store.State) {
		x := st.User(u.Username)
		if x == nil {
			return
		}
		m.TOTP = x.TOTPSecret != "" && gate.TOTPAccepted(policyOf(st), accountOf(x), time.Now())
		m.Key = len(x.SecurityKeys) > 0
		m.Recovery = len(x.RecoveryCodes) > 0
		rp, err := requestRelyingParty(r)
		if err != nil {
			m.KeyReason = err.Error()
			return
		}
		ku := &keyUser{u: copyUser(x), rpID: rp.ID}
		m.KeyHere = len(ku.WebAuthnCredentials()) > 0
		if m.Key && !m.KeyHere {
			m.KeyReason = "your security keys were registered on another address of the NAS"
		}
	})
	return m
}

// askSecondFactor answers a correct first factor of an account that needs
// a second one: the pending sign-in cookie and the methods it can use.
func (s *Server) askSecondFactor(w http.ResponseWriter, r *http.Request, u store.User, remember bool, via, mfa string) {
	t, err := pending2FA.put(&pendingLogin{user: u.Username, remember: remember, ip: clientIP(r), via: via, mfa: mfa}, time.Now())
	if err != nil {
		writeErr(w, err)
		return
	}
	setPendingCookie(w, r, t, pendingTTL)
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "two-factor code required", "twoFactor": true, "methods": s.methodsOf(r, u)})
}

// checkSecondFactor accepts a TOTP code (unless the policy wants a key of
// this administrator) or a single-use recovery code.
func (s *Server) checkSecondFactor(user, code string) bool {
	code = strings.TrimSpace(code)
	ok := false
	_ = s.store.Update(func(st *store.State) error {
		u := st.User(user)
		if u == nil || code == "" {
			return nil
		}
		if u.TOTPSecret != "" && gate.TOTPAccepted(policyOf(st), accountOf(u), time.Now()) {
			if valid, counter := totp.Verify(u.TOTPSecret, code, time.Now(), u.TOTPLast); valid {
				u.TOTPLast = counter
				ok = true
				return nil
			}
		}
		norm := strings.ToLower(strings.ReplaceAll(code, " ", ""))
		if len(norm) < 10 {
			return nil
		}
		for i, h := range u.RecoveryCodes {
			if checkPassword(h, norm) {
				u.RecoveryCodes = append(u.RecoveryCodes[:i], u.RecoveryCodes[i+1:]...)
				ok = true
				s.store.LogLocked(st, "warning", "auth", user, fmt.Sprintf("Recovery code used (%d left)", len(u.RecoveryCodes)))
				return nil
			}
		}
		return nil
	})
	return ok
}

// userRecoveryCodes returns 8 codes and their hashes.
func userRecoveryCodes() ([]string, []string, error) {
	codes := make([]string, 8)
	hashes := make([]string, 8)
	for i := range codes {
		codes[i] = recoveryCode()
		h, err := hashPassword(codes[i])
		if err != nil {
			return nil, nil, err
		}
		hashes[i] = h
	}
	return codes, hashes, nil
}

// keyUser adapts a user to the library for the second-factor step: its
// security keys and passkeys of one host name.
type keyUser struct {
	u    store.User
	rpID string
}

func (k *keyUser) WebAuthnID() []byte   { return k.u.WebAuthnID }
func (k *keyUser) WebAuthnName() string { return k.u.Username }
func (k *keyUser) WebAuthnDisplayName() string {
	if k.u.FullName != "" {
		return k.u.FullName
	}
	return k.u.Username
}
func (k *keyUser) WebAuthnCredentials() []webauthn.Credential {
	var out []webauthn.Credential
	for _, list := range [][]store.Passkey{k.u.SecurityKeys, k.u.Passkeys} {
		for _, p := range list {
			if p.RPID != k.rpID {
				continue
			}
			id, err := base64.RawURLEncoding.DecodeString(p.ID)
			if err != nil {
				continue
			}
			c := webauthn.Credential{ID: id, PublicKey: p.PublicKey,
				Flags:         webauthn.CredentialFlags{UserPresent: true, BackupEligible: p.BackupEligible, BackupState: p.BackupState},
				Authenticator: webauthn.Authenticator{AAGUID: p.AAGUID, SignCount: p.SignCount}}
			for _, t := range p.Transports {
				c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
			}
			out = append(out, c)
		}
	}
	return out
}

// keyUV is the user verification asked of security keys (policy setting).
func (s *Server) keyUV() protocol.UserVerificationRequirement {
	uv := protocol.VerificationPreferred
	s.store.Read(func(st *store.State) {
		if st.TwoFactor.KeyUV == "discouraged" {
			uv = protocol.VerificationDiscouraged
		}
	})
	return uv
}

func newKeyWebAuthn(rp relyingParty, uv protocol.UserVerificationRequirement) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:                  rp.ID,
		RPDisplayName:         "StrataOS",
		RPOrigins:             []string{rp.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			AuthenticatorAttachment: protocol.CrossPlatform,
			ResidentKey:             protocol.ResidentKeyRequirementDiscouraged,
			RequireResidentKey:      protocol.ResidentKeyNotRequired(),
			UserVerification:        uv,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Timeout: passkeyTimeout, TimeoutUVD: passkeyTimeout},
			Registration: webauthn.TimeoutConfig{Timeout: passkeyTimeout, TimeoutUVD: passkeyTimeout},
		},
		ExtensionsUnsolicitedOutputPolicy: protocol.UnsolicitedOutputPolicyIgnore,
	})
}

var errKeyRefused = &Error{Status: http.StatusUnauthorized, Msg: "security key not accepted"}

func (s *Server) secondFactorRoutes() {
	// What the pending sign-in can use (the page reloaded after an OIDC sign-in).
	s.mux.Handle("GET /api/login/2fa", publicAuth(func(r *http.Request) (any, error) {
		pl, _, ok := pending2FA.get(r, time.Now())
		if !ok {
			return map[string]any{"pending": false}, nil
		}
		var u store.User
		s.store.Read(func(st *store.State) {
			if x := st.User(pl.user); x != nil {
				u = copyUser(x)
			}
		})
		return map[string]any{"pending": true, "methods": s.methodsOf(r, u), "via": pl.via}, nil
	}))

	s.mux.Handle("POST /api/login/2fa", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"})
			return
		}
		var req struct {
			Code string `json:"code"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		pl, k, ok := pending2FA.get(r, time.Now())
		if !ok {
			writeErr(w, &Error{Status: http.StatusUnauthorized, Msg: "this sign-in has expired, start again"})
			return
		}
		u, ok := s.pendingUser(pl)
		if !ok || !s.checkSecondFactor(u.Username, req.Code) {
			pending2FA.fail(k)
			s.store.Log("warning", "auth", pl.user, "Wrong two-factor code from "+ip)
			s.guardFail(ip, "web", pl.user)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "wrong two-factor code", "twoFactor": true})
			return
		}
		pending2FA.drop(k)
		setPendingCookie(w, r, "", -time.Second)
		s.startSession(w, r, u, pl.remember, pl.mfa, signedInHow(pl.via, "")+" from ")
	}))

	s.mux.Handle("POST /api/login/key/begin", publicAuth(func(r *http.Request) (any, error) {
		pl, _, ok := pending2FA.get(r, time.Now())
		if !ok {
			return nil, &Error{Status: http.StatusUnauthorized, Msg: "this sign-in has expired, start again"}
		}
		rp, err := requestRelyingParty(r)
		if err != nil {
			return nil, err
		}
		u, ok := s.pendingUser(pl)
		if !ok {
			return nil, errKeyRefused
		}
		ku := &keyUser{u: u, rpID: rp.ID}
		if len(ku.WebAuthnCredentials()) == 0 {
			return nil, badRequest("no security key of this account works on this address: open the NAS by the name you registered it with, or use another method")
		}
		wa, err := newKeyWebAuthn(rp, s.keyUV())
		if err != nil {
			return nil, err
		}
		assertion, data, err := wa.BeginLogin(ku, webauthn.WithUserVerification(s.keyUV()))
		if err != nil {
			return nil, err
		}
		id, err := pendingPasskeys.put(&passkeyFlow{kind: "2fa", user: u.Username, rp: rp, data: *data}, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{"flow": id, "publicKey": assertion.Response}, nil
	}))

	s.mux.Handle("POST /api/login/key/finish", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		ip := clientIP(r)
		if !s.limiter.attempt(ip) {
			writeErr(w, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"})
			return
		}
		var req struct {
			Flow       string          `json:"flow"`
			Credential json.RawMessage `json:"credential"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		pl, k, ok := pending2FA.get(r, time.Now())
		fl := pendingPasskeys.take(req.Flow, "2fa", time.Now())
		if !ok || fl == nil || fl.user != pl.user {
			writeErr(w, errPasskeyExpired)
			return
		}
		u, err := s.keyAssertion(r, fl, req.Credential)
		if err != nil {
			pending2FA.fail(k)
			slog.Info("security key refused", "ip", ip, "user", pl.user, "err", err)
			s.store.Log("warning", "auth", pl.user, "Security key refused from "+ip)
			s.guardFail(ip, "web", pl.user)
			writeErr(w, errKeyRefused)
			return
		}
		pending2FA.drop(k)
		setPendingCookie(w, r, "", -time.Second)
		s.startSession(w, r, u, pl.remember, pl.mfa, signedInHow(pl.via, " and a security key")+" from ")
	}))

	s.user("GET /api/me/security-keys", func(r *http.Request) (any, error) {
		rp, rpErr := requestRelyingParty(r)
		out := map[string]any{"available": rpErr == nil, "rpId": rp.ID}
		if rpErr != nil {
			out["reason"] = rpErr.Error()
		}
		list := []passkeyView{}
		s.store.Read(func(st *store.State) {
			if u := st.User(userOf(r)); u != nil {
				for _, k := range u.SecurityKeys {
					list = append(list, viewPasskey(k, rp.ID))
				}
				out["recoveryCodes"] = len(u.RecoveryCodes)
				out["totp"] = u.TOTPSecret != ""
				out["password"] = u.PasswordHash != "" || u.Source != ""
			}
			out["keyRequired"] = st.TwoFactor.Require && st.TwoFactor.RequireKey
		})
		out["keys"] = list
		return out, nil
	})

	s.user("POST /api/me/security-keys/begin", func(r *http.Request) (any, error) {
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
		if strings.TrimSpace(req.Name) == "" {
			name = "Security key"
		}
		me, ip := userOf(r), clientIP(r)
		if !s.limiter.attempt(ip) {
			return nil, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"}
		}
		if !s.confirmIdentity(r, me, req.Password) {
			return nil, badRequest("incorrect password")
		}
		s.limiter.reset(ip)
		var u store.User
		err = s.store.Update(func(st *store.State) error {
			x := st.User(me)
			if x == nil {
				return notFound("user not found")
			}
			if len(x.SecurityKeys) >= maxKeys {
				return badRequest(fmt.Sprintf("%d security keys at most per account: delete one first", maxKeys))
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
		wa, err := newKeyWebAuthn(rp, s.keyUV())
		if err != nil {
			return nil, err
		}
		ku := &keyUser{u: u, rpID: rp.ID}
		creation, data, err := wa.BeginRegistration(ku, webauthn.WithExclusions(webauthn.Credentials(ku.WebAuthnCredentials()).CredentialDescriptors()))
		if err != nil {
			return nil, err
		}
		id, err := pendingPasskeys.put(&passkeyFlow{kind: "key-register", user: me, sess: sessionKey(r), name: name, rp: rp, data: *data}, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{"flow": id, "publicKey": creation.Response}, nil
	})

	s.user("POST /api/me/security-keys/finish", func(r *http.Request) (any, error) {
		var req struct {
			Flow       string          `json:"flow"`
			Credential json.RawMessage `json:"credential"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		me := userOf(r)
		fl := pendingPasskeys.take(req.Flow, "key-register", time.Now())
		if fl == nil || fl.user != me || fl.sess != sessionKey(r) {
			return nil, errPasskeyExpired
		}
		k, codes, err := s.keyRegister(r, fl, req.Credential)
		if err != nil {
			return nil, err
		}
		s.audit(r, "auth", fmt.Sprintf("Security key %s added (%s)", k.Name, k.RPID))
		out := map[string]any{"key": viewPasskey(k, fl.rp.ID)}
		if codes != nil {
			out["recoveryCodes"] = codes
		}
		return out, nil
	})

	s.user("PUT /api/me/security-keys/{id}", func(r *http.Request) (any, error) {
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
			if u == nil || u.SecurityKeyByID(r.PathValue("id")) == nil {
				return notFound("security key not found")
			}
			u.SecurityKeyByID(r.PathValue("id")).Name = name
			return nil
		})
		if err == nil {
			s.audit(r, "auth", "Security key renamed to "+name)
		}
		return nil, err
	})

	s.user("DELETE /api/me/security-keys/{id}", func(r *http.Request) (any, error) {
		var name string
		err := s.store.Update(func(st *store.State) error {
			u := st.User(userOf(r))
			if u == nil || u.SecurityKeyByID(r.PathValue("id")) == nil {
				return notFound("security key not found")
			}
			name = u.SecurityKeyByID(r.PathValue("id")).Name
			if err := keepsSecondFactor(st, u, u.TOTPSecret != "", len(u.Passkeys), len(u.SecurityKeys)-1); err != nil {
				return err
			}
			u.SecurityKeys = slices.DeleteFunc(u.SecurityKeys, func(p store.Passkey) bool { return p.ID == r.PathValue("id") })
			if !hasSecondFactor(u) {
				u.RecoveryCodes = nil // nothing left for them to stand in for
			}
			return nil
		})
		if err == nil {
			s.audit(r, "auth", "Security key "+name+" deleted")
		}
		return nil, err
	})

	// New recovery codes (the old ones stop working), for an account with a
	// second factor: shown once.
	s.user("POST /api/me/recovery-codes", func(r *http.Request) (any, error) {
		var req struct {
			Password string `json:"password"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		me, ip := userOf(r), clientIP(r)
		if !s.limiter.attempt(ip) {
			return nil, &Error{Status: http.StatusTooManyRequests, Msg: "too many failed attempts, try again in a few minutes"}
		}
		if !s.confirmIdentity(r, me, req.Password) {
			return nil, badRequest("incorrect password")
		}
		s.limiter.reset(ip)
		codes, hashes, err := userRecoveryCodes()
		if err != nil {
			return nil, err
		}
		err = s.store.Update(func(st *store.State) error {
			u := st.User(me)
			if u == nil {
				return notFound("user not found")
			}
			if !hasSecondFactor(u) {
				return badRequest("set up an authenticator app or a security key first")
			}
			u.RecoveryCodes = hashes
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "auth", "New recovery codes generated")
		return map[string]any{"recoveryCodes": codes}, nil
	})

	s.admin("DELETE /api/users/{name}/security-keys", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil {
				return notFound("user not found")
			}
			u.SecurityKeys = nil
			if !hasSecondFactor(u) {
				u.RecoveryCodes = nil
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if name != userOf(r) {
			s.sessions.dropUser(name)
		}
		s.audit(r, "users", "Security keys removed for "+name)
		return nil, nil
	})
}

func signedInHow(via, extra string) string {
	if via != "" {
		return "Signed in with " + via + extra
	}
	if extra != "" {
		return "Signed in with a password" + extra
	}
	return "Signed in with a password and a code"
}

// pendingUser returns the account of a pending sign-in while it may still
// sign in.
func (s *Server) pendingUser(pl pendingLogin) (store.User, bool) {
	var u store.User
	var ok bool
	s.store.Read(func(st *store.State) {
		if x := st.User(pl.user); x != nil && !x.Disabled {
			u, ok = copyUser(x), true
		}
	})
	return u, ok
}

// confirmIdentity re-checks the password before a sensitive change; an
// account without a password (created by an identity provider) proves it by
// a session opened in the last 15 minutes.
func (s *Server) confirmIdentity(r *http.Request, name, password string) bool {
	var noPassword bool
	s.store.Read(func(st *store.State) {
		if u := st.User(name); u != nil {
			noPassword = u.PasswordHash == "" && u.Source == ""
		}
	})
	if noPassword {
		ss := sessionFrom(r.Context())
		return ss != nil && ss.Device == "" && time.Since(ss.Created) < 15*time.Minute
	}
	return s.confirmPassword(r.Context(), name, password)
}

// keyRegister verifies a new security key and stores it; it returns new
// recovery codes when the account had none.
func (s *Server) keyRegister(r *http.Request, fl *passkeyFlow, raw json.RawMessage) (store.Passkey, []string, error) {
	if rp, err := requestRelyingParty(r); err != nil || rp != fl.rp {
		return store.Passkey{}, nil, errPasskeyExpired
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(raw)
	if err != nil {
		slog.Info("security key registration", "err", err)
		return store.Passkey{}, nil, badRequest("the security key could not be verified")
	}
	var u store.User
	s.store.Read(func(st *store.State) {
		if x := st.User(fl.user); x != nil {
			u = copyUser(x)
		}
	})
	if u.Username == "" {
		return store.Passkey{}, nil, notFound("user not found")
	}
	wa, err := newKeyWebAuthn(fl.rp, s.keyUV())
	if err != nil {
		return store.Passkey{}, nil, err
	}
	cred, err := wa.CreateCredential(&keyUser{u: u, rpID: fl.rp.ID}, fl.data, parsed)
	if err != nil {
		slog.Info("security key registration", "err", err)
		return store.Passkey{}, nil, badRequest("the security key could not be verified")
	}
	k := store.Passkey{
		ID: base64.RawURLEncoding.EncodeToString(cred.ID), PublicKey: cred.PublicKey, SignCount: cred.Authenticator.SignCount,
		RPID: fl.rp.ID, Name: fl.name, AAGUID: cred.Authenticator.AAGUID,
		BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState, CreatedAt: time.Now(),
	}
	for _, t := range cred.Transport {
		k.Transports = append(k.Transports, string(t))
	}
	var codes, hashes []string
	if len(u.RecoveryCodes) == 0 {
		if codes, hashes, err = userRecoveryCodes(); err != nil {
			return store.Passkey{}, nil, err
		}
	}
	err = s.store.Update(func(st *store.State) error {
		x := st.User(fl.user)
		if x == nil || !bytes.Equal(x.WebAuthnID, u.WebAuthnID) {
			return notFound("user not found")
		}
		for _, o := range st.Users {
			if o.SecurityKeyByID(k.ID) != nil || o.PasskeyByID(k.ID) != nil {
				return badRequest("this security key is already registered")
			}
		}
		if len(x.SecurityKeys) >= maxKeys {
			return badRequest(fmt.Sprintf("%d security keys at most per account: delete one first", maxKeys))
		}
		x.SecurityKeys = append(x.SecurityKeys, k)
		if len(x.RecoveryCodes) == 0 && hashes != nil {
			x.RecoveryCodes = hashes
		} else {
			codes = nil
		}
		return nil
	})
	return k, codes, err
}

// keyAssertion verifies the second-factor assertion of a pending sign-in.
func (s *Server) keyAssertion(r *http.Request, fl *passkeyFlow, raw json.RawMessage) (store.User, error) {
	if rp, err := requestRelyingParty(r); err != nil || rp != fl.rp {
		return store.User{}, errors.New("request made on another address")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		return store.User{}, err
	}
	var u store.User
	s.store.Read(func(st *store.State) {
		if x := st.User(fl.user); x != nil {
			u = copyUser(x)
		}
	})
	if u.Username == "" || u.Disabled {
		return store.User{}, errors.New("account gone or disabled")
	}
	wa, err := newKeyWebAuthn(fl.rp, s.keyUV())
	if err != nil {
		return store.User{}, err
	}
	cred, err := wa.ValidateLogin(&keyUser{u: u, rpID: fl.rp.ID}, fl.data, parsed)
	if err != nil {
		return u, err
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	if cred.Authenticator.CloneWarning {
		msg := fmt.Sprintf("Security key of %s refused: its signature counter went backwards (cloned key?)", u.Username)
		s.store.Log("warning", "auth", u.Username, msg)
		s.notify("warning", "Suspicious security key", msg)
		return u, errors.New("signature counter went backwards")
	}
	err = s.store.Update(func(st *store.State) error {
		x := st.User(u.Username)
		if x == nil || x.Disabled || !bytes.Equal(x.WebAuthnID, u.WebAuthnID) {
			return errors.New("account changed")
		}
		k := x.SecurityKeyByID(id)
		if k == nil {
			k = x.PasskeyByID(id)
		}
		if k == nil {
			return errors.New("key deleted")
		}
		k.SignCount, k.BackupState, k.LastUsed = cred.Authenticator.SignCount, cred.Flags.BackupState, time.Now()
		u = copyUser(x)
		return nil
	})
	return u, err
}
