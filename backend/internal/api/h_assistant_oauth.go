package api

// OAuth2 device-flow sign-in (RFC 8628) for AI providers whose API is
// OpenAI-compatible, e.g. Mistral (La Plateforme): the administrator
// registers an app at the provider and pastes its client id; the NAS asks
// the provider for a device code, the administrator approves it in a
// browser, and the NAS polls until it receives the tokens. The short-lived
// access token is then renewed automatically from the refresh token and
// used as the API key: no long-lived key is ever stored. AssistantProvider
// is "oauth:<id>" while such an account is connected.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"strataos/internal/store"
)

// oauthProvider is one sign-in-capable provider. The endpoints are fields
// (not constants) so tests can point them at a local server.
type oauthProvider struct {
	ID           string
	Name         string
	DeviceURL    string // device authorization endpoint (RFC 8628 §3.1)
	TokenURL     string // token endpoint
	APIBase      string // OpenAI-compatible base; the access token is its Bearer key
	Scope        string
	DefaultModel string
}

var oauthProviders = map[string]*oauthProvider{
	"mistral": {
		ID:           "mistral",
		Name:         "Mistral (La Plateforme)",
		DeviceURL:    "https://auth.mistral.ai/oauth/device/authorize",
		TokenURL:     "https://auth.mistral.ai/oauth/token",
		APIBase:      "https://api.mistral.ai/v1",
		Scope:        "openid profile email",
		DefaultModel: "mistral-small-latest",
	},
}

// oauthFormPost posts a form to one provider endpoint and decodes the JSON
// answer into out. A non-2xx status is returned as *oauthError when the body
// carries an OAuth error code.
func oauthFormPost(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := llmClient.Do(req)
	if err != nil {
		return llmDoError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var oe struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &oe)
		return &oauthError{Status: resp.StatusCode, Code: oe.Error}
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return badRequest("invalid answer from the provider")
		}
	}
	return nil
}

type oauthError struct {
	Status int
	Code   string
}

func (e *oauthError) Error() string { return fmt.Sprintf("provider answered %d: %s", e.Status, e.Code) }

// jwtEmail reads the email claim of an id_token without verifying it (the
// token came from the provider over TLS; the email is display-only).
func jwtEmail(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(b, &claims) != nil {
		return ""
	}
	return claims.Email
}

// oauthValidClientID checks the app client id pasted by the administrator.
func oauthValidClientID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return false
		}
	}
	return true
}

// oauthAccessToken returns a valid access token, renewing it from the
// refresh token when it expired (or always, when force).
// oauthRefreshMu serializes the refreshes (rotating refresh tokens).
var oauthRefreshMu sync.Mutex

func (s *Server) oauthAccessToken(ctx context.Context, force bool) (string, error) {
	oauthRefreshMu.Lock()
	defer oauthRefreshMu.Unlock()
	var set store.Settings
	s.store.Read(func(st *store.State) { set = st.Settings })
	if set.OAuthRefresh == "" || set.OAuthProvider == "" {
		return "", badRequest("no account is connected")
	}
	if !force && set.OAuthAccess != "" && time.Until(set.OAuthExpires) > time.Minute {
		return set.OAuthAccess, nil
	}
	prov := oauthProviders[set.OAuthProvider]
	if prov == nil {
		return "", badRequest("unknown sign-in provider")
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		IDToken      string `json:"id_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	err := oauthFormPost(ctx, prov.TokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {set.OAuthRefresh},
		"client_id":     {set.OAuthClientID},
	}, &tok)
	// A refused refresh token (revoked session, changed password) means a
	// new sign-in, not a transient failure.
	var oe *oauthError
	if err != nil && asOAuthErr(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "invalid_client") {
		return "", badRequest("the sign-in session expired: connect the account again")
	}
	if err != nil {
		return "", err
	}
	if tok.Error != "" || tok.AccessToken == "" {
		return "", badRequest("the sign-in session expired: connect the account again")
	}
	access, refresh := tok.AccessToken, tok.RefreshToken
	if refresh == "" {
		refresh = set.OAuthRefresh
	}
	email := set.OAuthEmail
	if tok.IDToken != "" {
		if e := jwtEmail(tok.IDToken); e != "" {
			email = e
		}
	}
	if err := s.store.Update(func(st *store.State) error {
		st.Settings.OAuthAccess = access
		st.Settings.OAuthRefresh = refresh
		st.Settings.OAuthExpires = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		st.Settings.OAuthEmail = email
		return nil
	}); err != nil {
		return "", err
	}
	return access, nil
}

// assistantResolve turns an "oauth:<id>" provider into the plain endpoint
// settings (URL + fresh access token as the key) used by llmChat/llmStream.
func (s *Server) assistantResolve(ctx context.Context, set store.Settings) (store.Settings, error) {
	if !strings.HasPrefix(set.AssistantProvider, "oauth:") {
		return set, nil
	}
	id := strings.TrimPrefix(set.AssistantProvider, "oauth:")
	prov := oauthProviders[id]
	if prov == nil || prov.APIBase == "" {
		return set, badRequest("this sign-in provider has no compatible API")
	}
	tok, err := s.oauthAccessToken(ctx, false)
	if err != nil {
		return set, err
	}
	set.AssistantProvider = ""
	set.AssistantURL = prov.APIBase
	set.AssistantKey = tok
	return set, nil
}

func (s *Server) assistantOAuthRoutes() {
	// The providers the interface can offer for an account sign-in.
	s.admin("GET /api/assistant/oauth/providers", func(r *http.Request) (any, error) {
		var cur string
		s.store.Read(func(st *store.State) { cur = st.Settings.OAuthProvider })
		out := []map[string]any{}
		for _, p := range oauthProviders {
			out = append(out, map[string]any{"id": p.ID, "name": p.Name, "connected": cur == p.ID})
		}
		return out, nil
	})

	// Start: ask the provider for a device code to show to the administrator.
	s.admin("POST /api/assistant/oauth/start", func(r *http.Request) (any, error) {
		var req struct {
			Provider string `json:"provider"`
			ClientID string `json:"clientId"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		prov := oauthProviders[req.Provider]
		if prov == nil {
			return nil, badRequest("unknown sign-in provider")
		}
		clientID := strings.TrimSpace(req.ClientID)
		if !oauthValidClientID(clientID) {
			return nil, badRequest("register an app at the provider (its console) and paste its client id here")
		}
		var res struct {
			DeviceCode           string `json:"device_code"`
			UserCode             string `json:"user_code"`
			VerificationURI      string `json:"verification_uri"`
			VerificationComplete string `json:"verification_uri_complete"`
			ExpiresIn            int    `json:"expires_in"`
			Interval             int    `json:"interval"`
			Error                string `json:"error"`
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err := oauthFormPost(ctx, prov.DeviceURL, url.Values{
			"client_id": {clientID},
			"scope":     {prov.Scope},
		}, &res)
		if err != nil {
			return nil, err
		}
		if res.Error != "" || res.DeviceCode == "" {
			return nil, badRequest("the provider refused the sign-in request: " + firstNonEmpty(res.Error, "no device code"))
		}
		if res.Interval <= 0 {
			res.Interval = 5
		}
		return map[string]any{
			"provider":     prov.ID,
			"clientId":     clientID,
			"deviceCode":   res.DeviceCode,
			"userCode":     res.UserCode,
			"verification": firstNonEmpty(res.VerificationComplete, res.VerificationURI),
			"expiresIn":    res.ExpiresIn,
			"interval":     res.Interval,
		}, nil
	})

	// Poll: exchange the device code for tokens once the administrator
	// approved it in the browser (RFC 8628 §3.3-3.5).
	s.admin("POST /api/assistant/oauth/poll", func(r *http.Request) (any, error) {
		var req struct {
			Provider   string `json:"provider"`
			ClientID   string `json:"clientId"`
			DeviceCode string `json:"deviceCode"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		prov := oauthProviders[req.Provider]
		if prov == nil {
			return nil, badRequest("unknown sign-in provider")
		}
		if req.DeviceCode == "" || !oauthValidClientID(req.ClientID) {
			return nil, badRequest("invalid sign-in request")
		}
		var tok struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
			IDToken      string `json:"id_token"`
			Error        string `json:"error"`
			ErrorDesc    string `json:"error_description"`
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err := oauthFormPost(ctx, prov.TokenURL, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {req.DeviceCode},
			"client_id":   {req.ClientID},
		}, &tok)
		var oe *oauthError
		if err != nil && !asOAuthErr(err, &oe) {
			return nil, err
		}
		switch {
		case err == nil && tok.AccessToken != "" && tok.RefreshToken != "":
			// Connected: store the tokens, point the assistant at the API.
			email := jwtEmail(tok.IDToken)
			err := s.store.Update(func(st *store.State) error {
				st.Settings.OAuthProvider = prov.ID
				st.Settings.OAuthClientID = req.ClientID
				st.Settings.OAuthAccess = tok.AccessToken
				st.Settings.OAuthRefresh = tok.RefreshToken
				st.Settings.OAuthExpires = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
				st.Settings.OAuthEmail = email
				st.Settings.AssistantProvider = "oauth:" + prov.ID
				st.Settings.AssistantURL = prov.APIBase
				if st.Settings.AssistantModel == "" {
					st.Settings.AssistantModel = prov.DefaultModel
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			s.audit(r, "assistant", "account connected: "+prov.Name+emailSuffix(email))
			return map[string]any{"done": true, "provider": prov.ID, "email": email}, nil
		case oe != nil && oe.Code == "authorization_pending":
			return map[string]any{"done": false, "status": "pending"}, nil
		case oe != nil && oe.Code == "slow_down":
			return map[string]any{"done": false, "status": "slow_down"}, nil
		case oe != nil && oe.Code == "access_denied":
			return nil, badRequest("the sign-in was refused at the provider")
		case oe != nil && (oe.Code == "expired_token" || oe.Code == "invalid_grant"):
			return map[string]any{"done": false, "status": "expired"}, nil
		case tok.Error != "":
			return nil, badRequest("the provider refused the sign-in: " + firstNonEmpty(tok.ErrorDesc, tok.Error))
		default:
			return nil, badRequest("the sign-in did not complete: " + firstNonEmpty(errStr(err), "invalid answer"))
		}
	})

	s.admin("POST /api/assistant/oauth/disconnect", func(r *http.Request) (any, error) {
		err := s.store.Update(func(st *store.State) error {
			prov := oauthProviders[st.Settings.OAuthProvider]
			if strings.HasPrefix(st.Settings.AssistantProvider, "oauth:") {
				st.Settings.AssistantProvider = ""
			}
			// The API URL was set by the sign-in: give it back only if it is
			// still the provider's (the admin may have changed it since).
			if prov != nil && st.Settings.AssistantURL == prov.APIBase {
				st.Settings.AssistantURL = ""
			}
			st.Settings.OAuthProvider = ""
			st.Settings.OAuthClientID = ""
			st.Settings.OAuthAccess = ""
			st.Settings.OAuthRefresh = ""
			st.Settings.OAuthExpires = time.Time{}
			st.Settings.OAuthEmail = ""
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.audit(r, "assistant", "OAuth account disconnected")
		return nil, nil
	})
}

// asOAuthErr reports whether err is an *oauthError.
func asOAuthErr(err error, target **oauthError) bool {
	for err != nil {
		if oe, ok := err.(*oauthError); ok {
			*target = oe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
