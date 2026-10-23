package docker

// Container updates: whether a registry holds a newer image for the tag a
// container was created from (manifest digest against the local RepoDigests,
// anonymous tokens for Docker Hub, GHCR and any registry using the standard
// token flow), and the create body that recreates a container identically on
// a new image.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ImageRef is a parsed image reference (docker.io/library/nginx:latest).
type ImageRef struct {
	Registry string // docker.io, ghcr.io, localhost:5000…
	Repo     string // library/nginx
	Tag      string
	Digest   string // pinned by digest: never updated
}

var (
	refComponentRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	refTagRe       = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	refHostRe      = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$|^\[[0-9a-fA-F:]+\](:[0-9]{1,5})?$`)
	refDigestRe    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ParseRef parses an image reference the way the Docker CLI normalizes it.
func ParseRef(s string) (ImageRef, error) {
	var r ImageRef
	if s == "" || len(s) > 512 || strings.ContainsAny(s, " \t\n") {
		return r, fmt.Errorf("invalid image reference")
	}
	if i := strings.Index(s, "@"); i >= 0 {
		r.Digest = s[i+1:]
		s = s[:i]
		if !refDigestRe.MatchString(r.Digest) {
			return r, fmt.Errorf("invalid image digest")
		}
	}
	name := s
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		name, r.Tag = s[:i], s[i+1:]
		if !refTagRe.MatchString(r.Tag) {
			return r, fmt.Errorf("invalid image tag")
		}
	}
	parts := strings.Split(name, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost" || strings.HasPrefix(parts[0], "[")) {
		r.Registry, parts = parts[0], parts[1:]
		if !refHostRe.MatchString(r.Registry) {
			return r, fmt.Errorf("invalid registry")
		}
	} else {
		r.Registry = "docker.io"
		if len(parts) == 1 {
			parts = []string{"library", parts[0]}
		}
	}
	for _, p := range parts {
		if !refComponentRe.MatchString(p) {
			return r, fmt.Errorf("invalid image name")
		}
	}
	r.Repo = strings.Join(parts, "/")
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	return r, nil
}

// Name is the reference without tag, as Docker shows it in RepoDigests.
func (r ImageRef) Name() string {
	if r.Registry == "docker.io" {
		return strings.TrimPrefix(r.Repo, "library/")
	}
	return r.Registry + "/" + r.Repo
}

// apiHost is the host serving the registry API.
func (r ImageRef) apiHost() string {
	if r.Registry == "docker.io" {
		return "registry-1.docker.io"
	}
	return r.Registry
}

// localRegistry: a registry on this machine, spoken to in plain HTTP like
// Docker does for localhost (a test registry, a pull-through cache).
func localRegistry(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Challenge is a parsed WWW-Authenticate: Bearer header.
type Challenge struct{ Realm, Service, Scope string }

// ParseChallenge reads `Bearer realm="…",service="…",scope="…"`.
func ParseChallenge(h string) (Challenge, bool) {
	var c Challenge
	rest, ok := strings.CutPrefix(strings.TrimSpace(h), "Bearer ")
	if !ok {
		return c, false
	}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, found := strings.Cut(rest, "=")
		if !found {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		var val string
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				return c, false
			}
			val, rest = v[1:end+1], v[end+2:]
		} else {
			val, rest, _ = strings.Cut(v, ",")
		}
		switch k {
		case "realm":
			c.Realm = val
		case "service":
			c.Service = val
		case "scope":
			c.Scope = val
		}
	}
	return c, c.Realm != ""
}

// RegistryClient is the HTTP client of the update checks (a variable for tests).
var RegistryClient = &http.Client{Timeout: 30 * time.Second}

var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}, ", ")

// token gets an anonymous pull token for a challenge.
func token(ctx context.Context, c Challenge, ref ImageRef, local bool) (string, error) {
	u, err := url.Parse(c.Realm)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && local && localRegistry(u.Host))) {
		return "", fmt.Errorf("registry asks for an unsupported authentication")
	}
	q := u.Query()
	if c.Service != "" {
		q.Set("service", c.Service)
	}
	scope := c.Scope
	if scope == "" {
		scope = "repository:" + ref.Repo + ":pull"
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := RegistryClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("registry token refused (HTTP %d): private image?", resp.StatusCode)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return "", err
	}
	return firstOf(t.Token, t.AccessToken), nil
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// RemoteDigest returns the digest the registry serves for ref's tag.
func RemoteDigest(ctx context.Context, ref ImageRef) (string, error) {
	if ref.Digest != "" {
		return "", errors.New("pinned to a digest")
	}
	host := ref.apiHost()
	local := localRegistry(host)
	scheme := "https"
	if local {
		scheme = "http"
	}
	u := scheme + "://" + host + "/v2/" + ref.Repo + "/manifests/" + url.PathEscape(ref.Tag)
	do := func(method, bearer string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", manifestTypes)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		return RegistryClient.Do(req)
	}
	bearer := ""
	resp, err := do("HEAD", "")
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		c, ok := ParseChallenge(resp.Header.Get("WWW-Authenticate"))
		if !ok {
			return "", errors.New("registry needs credentials")
		}
		if bearer, err = token(ctx, c, ref, local); err != nil {
			return "", err
		}
		if resp, err = do("HEAD", bearer); err != nil {
			return "", err
		}
		resp.Body.Close()
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", errors.New("tag not found in the registry")
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", errors.New("registry refused access: private image?")
	case http.StatusTooManyRequests:
		return "", errors.New("registry rate limit reached, try again later")
	default:
		return "", fmt.Errorf("registry answered HTTP %d", resp.StatusCode)
	}
	if d := resp.Header.Get("Docker-Content-Digest"); refDigestRe.MatchString(d) {
		return d, nil
	}
	// Some registries only send the digest with the body.
	if resp, err = do("GET", bearer); err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if d := resp.Header.Get("Docker-Content-Digest"); refDigestRe.MatchString(d) {
		return d, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(resp.Body, 4<<20)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// HasDigest reports whether repoDigests (name@sha256:…) of a local image
// contain digest for the repository of ref.
func HasDigest(repoDigests []string, ref ImageRef, digest string) bool {
	for _, rd := range repoDigests {
		name, d, ok := strings.Cut(rd, "@")
		if !ok || d != digest {
			continue
		}
		if r, err := ParseRef(name); err == nil && r.Registry == ref.Registry && r.Repo == ref.Repo {
			return true
		}
	}
	return false
}

// RecreateSpec builds the /containers/create body that recreates container
// ctr (docker inspect) on a new image: its own settings, without what it got
// from its old image (oldImage, docker image inspect) so the new image's
// defaults apply. extra are the networks to connect after creation (the API
// takes one at creation).
func RecreateSpec(ctr, oldImage map[string]any) (body map[string]any, extra map[string]any) {
	cfg := cloneMap(asMap(ctr["Config"]))
	img := asMap(asMap(oldImage)["Config"])
	for _, k := range []string{"Cmd", "Entrypoint", "WorkingDir", "User", "Healthcheck", "StopSignal", "Shell", "OnBuild"} {
		if v, ok := cfg[k]; ok && (isEmpty(v) || reflect.DeepEqual(v, img[k])) {
			delete(cfg, k)
		}
	}
	if env, ok := cfg["Env"].([]any); ok {
		base, _ := img["Env"].([]any)
		cfg["Env"] = slices.DeleteFunc(slices.Clone(env), func(e any) bool { return slices.Contains(base, e) })
	}
	// Labels of the old image (its version…) would stick to the new
	// container. Exposed ports and volumes stay: extra ones are harmless.
	if own := cloneMap(asMap(cfg["Labels"])); len(own) > 0 {
		for key, v := range asMap(img["Labels"]) {
			if reflect.DeepEqual(own[key], v) {
				delete(own, key)
			}
		}
		cfg["Labels"] = own
	}
	id, _ := ctr["Id"].(string)
	if h, _ := cfg["Hostname"].(string); h == "" || (len(id) >= 12 && h == id[:12]) {
		delete(cfg, "Hostname") // Docker's default: the new container's own ID
	}
	host := cloneMap(asMap(ctr["HostConfig"]))
	// Anonymous volumes would be left behind with the old container: the new
	// one mounts them by name, like Compose does when it recreates.
	binds, _ := host["Binds"].([]any)
	binds = slices.Clone(binds)
	taken := map[string]bool{}
	for _, b := range binds {
		if parts := strings.Split(fmt.Sprint(b), ":"); len(parts) >= 2 {
			taken[parts[1]] = true
		}
	}
	for _, m := range asSlice(host["Mounts"]) {
		if t, ok := asMap(m)["Target"].(string); ok {
			taken[t] = true
		}
	}
	for _, m := range asSlice(ctr["Mounts"]) {
		mm := asMap(m)
		name, _ := mm["Name"].(string)
		dest, _ := mm["Destination"].(string)
		if mm["Type"] != "volume" || name == "" || dest == "" || taken[dest] {
			continue
		}
		b := name + ":" + dest
		if rw, ok := mm["RW"].(bool); ok && !rw {
			b += ":ro"
		}
		binds = append(binds, b)
		taken[dest] = true
	}
	if len(binds) > 0 {
		host["Binds"] = binds
	}
	body = cfg
	body["HostConfig"] = host
	mode, _ := host["NetworkMode"].(string)
	extra = map[string]any{}
	endpoints := map[string]any{}
	for name, ep := range asMap(asMap(ctr["NetworkSettings"])["Networks"]) {
		e := endpointConfig(asMap(ep), id)
		if name == mode || (mode == "default" && name == "bridge") {
			endpoints[name] = e
		} else if !strings.HasPrefix(mode, "container:") && mode != "host" && mode != "none" {
			extra[name] = e
		}
	}
	if len(endpoints) > 0 {
		body["NetworkingConfig"] = map[string]any{"EndpointsConfig": endpoints}
	}
	return body, extra
}

// endpointConfig keeps the user settings of a network endpoint (aliases,
// fixed addresses, links), not what Docker assigned to the old container.
func endpointConfig(ep map[string]any, id string) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"IPAMConfig", "Links", "DriverOpts"} {
		if v, ok := ep[k]; ok && !isEmpty(v) {
			out[k] = v
		}
	}
	if al, ok := ep["Aliases"].([]any); ok {
		keep := slices.DeleteFunc(slices.Clone(al), func(a any) bool { s, _ := a.(string); return len(id) >= 12 && s == id[:12] })
		if len(keep) > 0 {
			out["Aliases"] = keep
		}
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
