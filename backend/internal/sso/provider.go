package sso

import (
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strataos/internal/sys"
	"sync"
	"time"
)

// Identity is what a relying party learns about a user.
type Identity struct {
	Subject  string // stable per user, never reused
	Username string
	Name     string
	Email    string
	Groups   []string
}

// ErrBusy: too many requests waiting.
var ErrBusy = errors.New("too many sign-ins in progress, try again in a few minutes")

// Token is an access or refresh token (kept by the hash of its value).
type Token struct {
	Client   string    `json:"client"`
	User     string    `json:"user"`
	Scopes   []string  `json:"scopes"`
	Sid      string    `json:"sid,omitempty"` // NAS session it came from
	AuthTime time.Time `json:"authTime"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	// Family: every token descending from one code shares it, so a code or
	// refresh token used twice revokes them all (RFC 6749 4.1.2, 9700).
	Family string `json:"family"`
	Used   bool   `json:"used,omitempty"` // refresh token already exchanged
	// Nonce of the request, repeated in the ID tokens of refreshes (what
	// most providers do; some clients check it).
	Nonce string `json:"nonce,omitempty"`
}

// FwdSession is a sign-in to a host protected by forward authentication.
type FwdSession struct {
	Client  string    `json:"client"`
	User    string    `json:"user"`
	Sid     string    `json:"sid,omitempty"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

// Grant is a user's consent to an application.
type Grant struct {
	User     string    `json:"user"`
	Client   string    `json:"client"`
	Scopes   []string  `json:"scopes"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
}

type persisted struct {
	Access  map[string]*Token      `json:"access"`
	Refresh map[string]*Token      `json:"refresh"`
	Fwd     map[string]*FwdSession `json:"fwd"`
	Grants  []*Grant               `json:"grants"`
}

type codeEntry struct {
	req      AuthRequest
	user     string
	sid      string
	authTime time.Time
	expires  time.Time
	used     bool
	family   string
}

// LogoutRequest is an RP-initiated logout waiting for the user.
type LogoutRequest struct {
	ID       string    `json:"id"`
	Client   string    `json:"client,omitempty"`
	Redirect string    `json:"-"` // post_logout_redirect_uri, registered
	State    string    `json:"-"`
	User     string    `json:"user,omitempty"` // from a valid id_token_hint
	Created  time.Time `json:"created"`
}

// FwdFlow is a forward-auth sign-in started by the proxy, keyed by state.
type FwdFlow struct {
	Client   string
	Host     string
	Verifier string
	Next     string // path to go back to
	Expires  time.Time
}

// Provider is the OpenID provider state.
type Provider struct {
	// Lookup gives the identity of a user for a client, or an error when
	// the user may not use it (deleted, disabled, not in an allowed group).
	Lookup func(user, client string) (*Identity, error)
	Now    func() time.Time

	dir string
	mu  sync.Mutex

	keys     KeyRing
	requests map[string]*AuthRequest
	logouts  map[string]*LogoutRequest
	flows    map[string]*FwdFlow
	codes    map[string]*codeEntry
	d        persisted
}

const maxPending = 2000

// Open loads the provider files of dir ("" = memory only, for tests) and
// makes the signing keys when needed.
func Open(dir string) (*Provider, error) {
	p := &Provider{dir: dir, Now: time.Now}
	p.requests, p.logouts, p.flows, p.codes = map[string]*AuthRequest{}, map[string]*LogoutRequest{}, map[string]*FwdFlow{}, map[string]*codeEntry{}
	p.d = persisted{Access: map[string]*Token{}, Refresh: map[string]*Token{}, Fwd: map[string]*FwdSession{}}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if b, err := os.ReadFile(filepath.Join(dir, "keys.json")); err == nil {
			if err := json.Unmarshal(b, &p.keys); err != nil {
				return nil, err
			}
		}
		_ = sys.LoadJSON(filepath.Join(dir, "tokens.json"), &p.d)
		if p.d.Access == nil {
			p.d.Access = map[string]*Token{}
		}
		if p.d.Refresh == nil {
			p.d.Refresh = map[string]*Token{}
		}
		if p.d.Fwd == nil {
			p.d.Fwd = map[string]*FwdSession{}
		}
	}
	if err := p.MaintainKeys(); err != nil {
		return nil, err
	}
	return p, nil
}

func writeFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *Provider) saveKeysLocked() error {
	if p.dir == "" {
		return nil
	}
	return writeFile(filepath.Join(p.dir, "keys.json"), &p.keys)
}

func (p *Provider) saveLocked() {
	now := p.Now()
	for k, t := range p.d.Access {
		if now.After(t.Expires) {
			delete(p.d.Access, k)
		}
	}
	for k, t := range p.d.Refresh {
		if now.After(t.Expires) {
			delete(p.d.Refresh, k)
		}
	}
	for k, f := range p.d.Fwd {
		if now.After(f.Expires) {
			delete(p.d.Fwd, k)
		}
	}
	if p.dir == "" {
		return
	}
	if err := writeFile(filepath.Join(p.dir, "tokens.json"), &p.d); err != nil {
		slog.Warn("save sso tokens", "err", err)
	}
}

// MaintainKeys rotates the signing keys when due (called daily).
func (p *Provider) MaintainKeys() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	changed, err := p.keys.Maintain(p.Now())
	if err != nil {
		return err
	}
	if changed {
		return p.saveKeysLocked()
	}
	return nil
}

// RotateKeys starts signing with the next key now.
func (p *Provider) RotateKeys() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.keys.Rotate(p.Now()); err != nil {
		return err
	}
	return p.saveKeysLocked()
}

// JWKS is the public key set.
func (p *Provider) JWKS() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keys.JWKS()
}

// KeyInfo lists the keys.
func (p *Provider) KeyInfo() []KeyInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keys.Info()
}

// Sign signs claims with the active key.
func (p *Provider) Sign(claims any) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keys.Sign(claims)
}

// VerifyToken checks a token this provider signed.
func (p *Provider) VerifyToken(tok string, out any, checkExp bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keys.Verify(tok, out, p.Now(), checkExp)
}

// ---- pending requests (memory only) ----

func (p *Provider) pruneLocked(now time.Time) {
	for k, r := range p.requests {
		if now.Sub(r.Created) > RequestTTL {
			delete(p.requests, k)
		}
	}
	for k, r := range p.logouts {
		if now.Sub(r.Created) > RequestTTL {
			delete(p.logouts, k)
		}
	}
	for k, f := range p.flows {
		if now.After(f.Expires) {
			delete(p.flows, k)
		}
	}
	for k, c := range p.codes {
		if now.After(c.expires) {
			delete(p.codes, k)
		}
	}
}

// PutRequest keeps an authorization request until the user answers.
func (p *Provider) PutRequest(r *AuthRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(p.Now())
	if len(p.requests) >= maxPending {
		return ErrBusy
	}
	p.requests[r.ID] = r
	return nil
}

// Request returns a copy of a pending request.
func (p *Provider) Request(id string) *AuthRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.requests[id]
	if !ok || p.Now().Sub(r.Created) > RequestTTL {
		return nil
	}
	c := *r
	return &c
}

// TakeRequest removes a pending request (answered).
func (p *Provider) TakeRequest(id string) *AuthRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.requests[id]
	delete(p.requests, id)
	if !ok || p.Now().Sub(r.Created) > RequestTTL {
		return nil
	}
	return r
}

// PutLogout keeps a logout request.
func (p *Provider) PutLogout(r *LogoutRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(p.Now())
	if len(p.logouts) >= maxPending {
		return ErrBusy
	}
	p.logouts[r.ID] = r
	return nil
}

// Logout returns a pending logout; take removes it.
func (p *Provider) Logout(id string, take bool) *LogoutRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.logouts[id]
	if take {
		delete(p.logouts, id)
	}
	if !ok || p.Now().Sub(r.Created) > RequestTTL {
		return nil
	}
	c := *r
	return &c
}

// PutFlow keeps a forward-auth sign-in started by the proxy.
func (p *Provider) PutFlow(state string, f *FwdFlow) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(p.Now())
	if len(p.flows) >= maxPending {
		return ErrBusy
	}
	f.Expires = p.Now().Add(RequestTTL)
	p.flows[state] = f
	return nil
}

// TakeFlow removes and returns a forward-auth sign-in.
func (p *Provider) TakeFlow(state string) *FwdFlow {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, ok := p.flows[state]
	delete(p.flows, state)
	if !ok || p.Now().After(f.Expires) {
		return nil
	}
	return f
}

// IssueCode answers an approved request with a single-use code.
func (p *Provider) IssueCode(r *AuthRequest, user, sid string, authTime time.Time) string {
	code := Random(32)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.Now()
	p.pruneLocked(now)
	p.codes[Hash(code)] = &codeEntry{req: *r, user: user, sid: sid, authTime: authTime, expires: now.Add(CodeTTL), family: Random(12)}
	return code
}

// ---- grants ----

// Granted reports whether the user already consented to these scopes.
func (p *Provider) Granted(user, client string, scopes []string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, g := range p.d.Grants {
		if g.User == user && g.Client == client {
			for _, s := range scopes {
				if !slices.Contains(g.Scopes, s) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// Grant records a consent (or its use).
func (p *Provider) Grant(user, client string, scopes []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.Now()
	for _, g := range p.d.Grants {
		if g.User == user && g.Client == client {
			for _, s := range scopes {
				if !slices.Contains(g.Scopes, s) {
					g.Scopes = append(g.Scopes, s)
				}
			}
			g.LastUsed = now
			p.saveLocked()
			return
		}
	}
	p.d.Grants = append(p.d.Grants, &Grant{User: user, Client: client, Scopes: slices.Clone(scopes), Created: now, LastUsed: now})
	p.saveLocked()
}

// Grants lists the consents of a user ("" = everybody's).
func (p *Provider) Grants(user string) []Grant {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Grant
	for _, g := range p.d.Grants {
		if user == "" || g.User == user {
			out = append(out, *g)
		}
	}
	return out
}

// Active counts the live sessions (refresh tokens not used, access tokens
// and forward-auth sessions) per user and client.
func (p *Provider) Active() map[[2]string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.Now()
	out := map[[2]string]int{}
	for _, t := range p.d.Refresh {
		if !t.Used && now.Before(t.Expires) {
			out[[2]string{t.User, t.Client}]++
		}
	}
	for _, f := range p.d.Fwd {
		if now.Before(f.Expires) {
			out[[2]string{f.User, f.Client}]++
		}
	}
	return out
}

// ---- revocation ----

func (p *Provider) dropLocked(keep func(user, client, sid, family string, scopes []string) bool) {
	for _, m := range []map[string]*Token{p.d.Access, p.d.Refresh} {
		maps.DeleteFunc(m, func(_ string, t *Token) bool { return !keep(t.User, t.Client, t.Sid, t.Family, t.Scopes) })
	}
	maps.DeleteFunc(p.d.Fwd, func(_ string, f *FwdSession) bool { return !keep(f.User, f.Client, f.Sid, "", nil) })
	maps.DeleteFunc(p.codes, func(_ string, c *codeEntry) bool { return !keep(c.user, c.req.ClientID, c.sid, c.family, nil) })
}

// RevokeUserClient ends a user's sessions of one client and forgets the
// consent (the user's "revoke" button).
func (p *Provider) RevokeUserClient(user, client string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLocked(func(u, c, _, _ string, _ []string) bool { return u != user || c != client })
	p.d.Grants = slices.DeleteFunc(p.d.Grants, func(g *Grant) bool { return g.User == user && g.Client == client })
	p.saveLocked()
}

// RevokeClient ends every session of a client; with grants, consents go too.
func (p *Provider) RevokeClient(client string, grants bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLocked(func(_, c, _, _ string, _ []string) bool { return c != client })
	if grants {
		p.d.Grants = slices.DeleteFunc(p.d.Grants, func(g *Grant) bool { return g.Client == client })
	}
	p.saveLocked()
}

// RevokeUser ends every app session of a user (password changed, disabled,
// deleted); with grants, consents go too (deleted).
func (p *Provider) RevokeUser(user string, grants bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLocked(func(u, _, _, _ string, _ []string) bool { return u != user })
	if grants {
		p.d.Grants = slices.DeleteFunc(p.d.Grants, func(g *Grant) bool { return g.User == user })
	}
	p.saveLocked()
}

// EndSession follows the end of a NAS session: forward-auth sessions and
// tokens issued from it end, except refresh tokens asked for offline use.
func (p *Provider) EndSession(sid string) {
	if sid == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	maps.DeleteFunc(p.d.Fwd, func(_ string, f *FwdSession) bool { return f.Sid == sid })
	maps.DeleteFunc(p.d.Access, func(_ string, t *Token) bool { return t.Sid == sid })
	maps.DeleteFunc(p.d.Refresh, func(_ string, t *Token) bool {
		return t.Sid == sid && !slices.Contains(t.Scopes, "offline_access")
	})
	p.saveLocked()
}

// ---- forward authentication sessions ----

// NewFwdSession opens a session on a protected host.
func (p *Provider) NewFwdSession(client, user, sid string, ttl time.Duration) string {
	t := Random(32)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.Now()
	p.d.Fwd[Hash(t)] = &FwdSession{Client: client, User: user, Sid: sid, Created: now, Expires: now.Add(ttl)}
	p.saveLocked()
	return t
}

// Fwd returns the session of a cookie value.
func (p *Provider) Fwd(t string) *FwdSession {
	if t == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f, ok := p.d.Fwd[Hash(t)]
	if !ok || p.Now().After(f.Expires) {
		return nil
	}
	c := *f
	return &c
}

// DropFwd ends a forward-auth session.
func (p *Provider) DropFwd(t string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.d.Fwd[Hash(t)]; ok {
		delete(p.d.Fwd, Hash(t))
		p.saveLocked()
	}
}
