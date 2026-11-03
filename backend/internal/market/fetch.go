package market

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Source kinds.
const (
	KindHTTPS = "https" // the address of catalog.json
	KindGit   = "git"   // a Git repository with catalog.json at its root
)

// Location is a parsed source address.
type Location struct {
	Kind string
	URL  string // index address, or repository address for git
	Ref  string // git branch or tag ("" = default branch)
}

// allowFileURLs lets tests clone local repositories.
var allowFileURLs = false

// loopbackHost tells whether plain http is acceptable for host (a local
// mirror or a test server: the content is verified by its signature or
// checksum either way).
func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ParseLocation checks a source address: https://…/catalog.json, a Git
// repository (git+https://… or an https address ending in .git, with an
// optional #branch), or plain http on the NAS itself.
func ParseLocation(raw string) (Location, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 512 || strings.ContainsAny(raw, " \t\r\n\"'<>`\\") {
		return Location{}, errors.New("bad address")
	}
	kind := KindHTTPS
	if strings.HasPrefix(raw, "git+") {
		kind, raw = KindGit, strings.TrimPrefix(raw, "git+")
	}
	ref := ""
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw, ref = raw[:i], raw[i+1:]
		if kind != KindGit && !strings.HasSuffix(raw, ".git") {
			return Location{}, errors.New("a #branch is for Git repositories")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" && u.Scheme != "file" || u.User != nil || u.RawQuery != "" && kind == KindGit {
		return Location{}, errors.New("bad address")
	}
	if strings.HasSuffix(u.Path, ".git") {
		kind = KindGit
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && loopbackHost(u.Hostname()):
	case u.Scheme == "file" && allowFileURLs && kind == KindGit:
	default:
		return Location{}, errors.New("the address must start with https:// (http:// only on the NAS itself)")
	}
	if ref != "" && (strings.HasPrefix(ref, "-") || !refRe.MatchString(ref)) {
		return Location{}, errors.New("bad branch or tag")
	}
	return Location{Kind: kind, URL: u.String(), Ref: ref}, nil
}

// String is the address as the user typed it, normalized.
func (l Location) String() string {
	s := l.URL
	if l.Kind == KindGit && !strings.HasSuffix(s, ".git") {
		s = "git+" + s
	}
	if l.Ref != "" {
		s += "#" + l.Ref
	}
	return s
}

// Fetched is an index and its signature as downloaded.
type Fetched struct {
	Index  []byte
	Sig    []byte // nil when there is none
	SHA256 string
	Commit string // git only
}

func httpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && loopbackHost(req.URL.Hostname())) {
				return errors.New("redirect to a non-https address")
			}
			return nil
		},
	}
}

func get(ctx context.Context, u string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "StrataOS-AppCenter")
	resp, err := httpClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode, fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 200, err
	}
	if int64(len(b)) > limit {
		return nil, 200, fmt.Errorf("%s is too large", u)
	}
	return b, 200, nil
}

// Fetch downloads the index of a source (and catalog.json.sig next to it,
// when there is one).
func Fetch(ctx context.Context, l Location) (*Fetched, error) {
	var f Fetched
	switch l.Kind {
	case KindHTTPS:
		b, _, err := get(ctx, l.URL, MaxCatalogSize)
		if err != nil {
			return nil, err
		}
		f.Index = b
		sig, code, err := get(ctx, l.URL+".sig", 4096)
		switch {
		case err == nil:
			f.Sig = sig
		case code == 404 || code == 403 || code == 410:
		default:
			return nil, fmt.Errorf("signature: %v", err)
		}
	case KindGit:
		if err := fetchGit(ctx, l, &f); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unknown source kind")
	}
	sum := sha256.Sum256(f.Index)
	f.SHA256 = hex.EncodeToString(sum[:])
	return &f, nil
}

// fetchGit clones the repository (shallow, no hooks, no submodules, no
// symlinks) in a temporary folder and reads catalog.json from it.
func fetchGit(ctx context.Context, l Location, f *Fetched) error {
	if !sys.Has("git") {
		return errors.New("git is not installed on the NAS (apt install git), or give the https address of catalog.json")
	}
	tmp, err := os.MkdirTemp("", "strata-catalog-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "repo")
	protocols := "https"
	if allowFileURLs {
		protocols = "https:file"
	} else if strings.HasPrefix(l.URL, "http://") {
		protocols = "http"
	}
	args := []string{"-c", "core.symlinks=false", "-c", "core.hooksPath=/dev/null",
		"clone", "--quiet", "--depth", "1", "--no-tags", "--single-branch", "--no-recurse-submodules"}
	if l.Ref != "" {
		args = append(args, "--branch", l.Ref)
	}
	args = append(args, "--", l.URL, dir)
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + tmp,
		"GIT_ALLOW_PROTOCOL=" + protocols, "GIT_LFS_SKIP_SMUDGE=1", "GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false"}
	if _, err := sys.RunOpts(ctx, sys.Opts{Env: env, Timeout: 2 * time.Minute, Dir: tmp}, "git", args...); err != nil {
		var ce *sys.CmdError
		if errors.As(err, &ce) {
			return fmt.Errorf("git clone failed: %s", strings.TrimSpace(lastLine(ce.Stderr)))
		}
		return fmt.Errorf("git clone failed: %v", err)
	}
	read := func(name string, limit int64) ([]byte, error) {
		p := filepath.Join(dir, name)
		st, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", name)
		}
		if st.Size() > limit {
			return nil, fmt.Errorf("%s is too large", name)
		}
		return os.ReadFile(p)
	}
	b, err := read("catalog.json", MaxCatalogSize)
	if err != nil {
		return fmt.Errorf("the repository has no catalog.json at its root")
	}
	f.Index = b
	if sig, err := read("catalog.json.sig", 4096); err == nil {
		f.Sig = sig
	}
	if out, err := sys.RunOpts(ctx, sys.Opts{Env: env, Dir: dir}, "git", "rev-parse", "HEAD"); err == nil {
		f.Commit = strings.TrimSpace(out)
	}
	return nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// ParsePublicKey reads an Ed25519 public key: "ed25519:<base64>", the bare
// base64 of the 32 bytes, or a PEM "PUBLIC KEY" (openssl pkey -pubout).
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "-----BEGIN") {
		blk, _ := pem.Decode([]byte(s))
		if blk == nil {
			return nil, errors.New("bad PEM public key")
		}
		k, err := x509.ParsePKIXPublicKey(blk.Bytes)
		if err != nil {
			return nil, errors.New("bad PEM public key")
		}
		pk, ok := k.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("the public key must be an Ed25519 key")
		}
		return pk, nil
	}
	s = strings.TrimPrefix(s, "ed25519:")
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("bad public key (ed25519:<base64 of 32 bytes>, or a PEM public key)")
	}
	return ed25519.PublicKey(b), nil
}

// FormatPublicKey is the canonical form of a key ("ed25519:<base64>").
func FormatPublicKey(k ed25519.PublicKey) string {
	return "ed25519:" + base64.StdEncoding.EncodeToString(k)
}

// Verify checks the signature of an index: 64 bytes, raw or in base64.
func Verify(k ed25519.PublicKey, index, sig []byte) error {
	if len(sig) == 0 {
		return errors.New("the catalog is not signed (catalog.json.sig missing)")
	}
	if len(sig) != ed25519.SignatureSize {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
		if err != nil {
			return errors.New("bad signature file")
		}
		sig = b
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(k, index, sig) {
		return errors.New("the signature of the catalog does not match its key")
	}
	return nil
}

var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
