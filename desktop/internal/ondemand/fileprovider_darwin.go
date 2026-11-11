package ondemand

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// macOS: the shared folders are a File Provider domain per account
// (Finder's sidebar, ~/Library/CloudStorage/…), served by the app's File
// Provider extension (desktop/macos), which reads the Service through the
// agent's /v1/fp API. The system keeps downloaded files itself and offers
// "Download Now" / "Remove Download". The agent drives the domains through
// the strataos-fileprovider helper next to it in Contents/MacOS (the
// FileProvider framework is easier from Swift than from Go without cgo).

func supported() (bool, string) { return true, "fileprovider" }

func helperPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "strataos-fileprovider"
	}
	return filepath.Join(filepath.Dir(exe), "strataos-fileprovider")
}

// runHelper runs the helper (stdin may be nil).
func runHelper(stdin []byte, args ...string) (string, error) {
	h := helperPath()
	if _, err := os.Stat(h); err != nil {
		return "", errors.New("the File Provider helper is missing: files on demand needs the desktop app")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("File Provider: %s", msg)
	}
	return out.String(), nil
}

// What agent.json holds: the agent's address and the folders of the menus
// (synchronized folders and the domains' folders).
var published struct {
	mu      sync.Mutex
	port    int
	token   string
	folders []string
	roots   map[string]string // account → domain folder
}

// PublishAgent tells the macOS extensions where the agent listens.
func PublishAgent(port int, token string, folders []string) error {
	published.mu.Lock()
	published.port, published.token, published.folders = port, token, folders
	published.mu.Unlock()
	return publish()
}

func publish() error {
	published.mu.Lock()
	if published.port == 0 {
		published.mu.Unlock()
		return nil // the agent is not listening yet: PublishAgent will
	}
	l := append([]string(nil), published.folders...)
	for _, r := range published.roots {
		l = append(l, r)
	}
	b := AgentJSON(published.port, published.token, l)
	published.mu.Unlock()
	_, err := runHelper(b, "publish")
	return err
}

func setRoot(account, root string) {
	published.mu.Lock()
	if published.roots == nil {
		published.roots = map[string]string{}
	}
	if root == "" {
		delete(published.roots, account)
	} else {
		published.roots[account] = root
	}
	published.mu.Unlock()
	if err := publish(); err != nil {
		log.Printf("files on demand: %v", err)
	}
}

// RemoveDomain removes the File Provider domain of an account (files on
// demand turned off, or signed out): Finder no longer shows it and the
// downloaded copies go.
func RemoveDomain(account string) error {
	setRoot(account, "")
	_, err := runHelper(nil, "remove", account)
	return err
}

type fpProvider struct {
	s       *Service
	account string
	root    string
	sig     *debouncer
	mu      sync.Mutex
	errMsg  string
}

// StartProvider adds (or keeps) the File Provider domain of the account.
func StartProvider(s *Service, o ProviderOptions) (Provider, error) {
	if o.Account == "" {
		return nil, errors.New("no account for the File Provider domain")
	}
	out, err := runHelper(nil, "add", o.Account, o.Name)
	if err != nil {
		return nil, err
	}
	root, err := HelperRoot(out)
	if err != nil {
		return nil, err
	}
	p := &fpProvider{s: s, account: o.Account, root: root}
	p.sig = &debouncer{delay: time.Second, f: p.signal}
	s.OnChange = func([]Change) { p.sig.call() }
	setRoot(o.Account, root)
	p.sig.call() // what changed while the agent was not running
	return p, nil
}

func (p *fpProvider) signal() {
	_, err := runHelper(nil, "signal", p.account)
	p.mu.Lock()
	p.errMsg = ""
	if err != nil {
		p.errMsg = err.Error()
	}
	p.mu.Unlock()
}

func (p *fpProvider) Info() ProviderInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := "ready"
	if p.errMsg != "" {
		st = "error"
	}
	return ProviderInfo{Kind: "fileprovider", Root: p.root, State: st, Error: p.errMsg}
}

// Pin: Finder has its own "Download Now" and "Remove Download".
func (p *fpProvider) Pin(string, bool) error { return ErrUnsupported }

// Close stops following; the domain stays (Finder keeps showing it, and
// its downloaded files) until RemoveDomain.
func (p *fpProvider) Close() error {
	p.s.OnChange = nil
	return nil
}
