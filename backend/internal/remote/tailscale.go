// Package remote drives the remote access methods that need no open port on
// the router: Tailscale (tailscaled) and Cloudflare Tunnel (cloudflared).
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// TailscaleInstalled reports whether the tailscale client is present.
func TailscaleInstalled() bool { return sys.Has("tailscale") && sys.Has("tailscaled") }

// TSPeer is a machine of the tailnet.
type TSPeer struct {
	Name     string   `json:"name"`
	DNSName  string   `json:"dnsName"`
	IPs      []string `json:"ips"`
	OS       string   `json:"os"`
	Online   bool     `json:"online"`
	ExitNode bool     `json:"exitNode,omitempty"`
}

// TSStatus is the useful part of "tailscale status --json".
type TSStatus struct {
	Installed bool     `json:"installed"`
	State     string   `json:"state"` // NeedsLogin, Running, Stopped, NeedsMachineAuth, Starting, NotRunning
	LoggedIn  bool     `json:"loggedIn"`
	AuthURL   string   `json:"authUrl,omitempty"`
	Name      string   `json:"name,omitempty"`
	DNSName   string   `json:"dnsName,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	Tailnet   string   `json:"tailnet,omitempty"`
	MagicDNS  bool     `json:"magicDns,omitempty"`
	Peers     []TSPeer `json:"peers"`
	Online    int      `json:"online"`
	Health    []string `json:"health,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type tsNode struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	OS           string   `json:"OS"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	ExitNode     bool     `json:"ExitNode"`
}

// ParseTSStatus reads the output of "tailscale status --json".
func ParseTSStatus(b []byte) (TSStatus, error) {
	var j struct {
		BackendState   string   `json:"BackendState"`
		AuthURL        string   `json:"AuthURL"`
		Self           *tsNode  `json:"Self"`
		Health         []string `json:"Health"`
		MagicDNSSuffix string   `json:"MagicDNSSuffix"`
		CurrentTailnet *struct {
			Name            string `json:"Name"`
			MagicDNSEnabled bool   `json:"MagicDNSEnabled"`
		} `json:"CurrentTailnet"`
		Peer map[string]tsNode `json:"Peer"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return TSStatus{}, fmt.Errorf("unreadable tailscale status: %w", err)
	}
	st := TSStatus{Installed: true, State: j.BackendState, AuthURL: j.AuthURL, Health: j.Health, Peers: []TSPeer{}}
	st.LoggedIn = j.BackendState == "Running" || j.BackendState == "Starting" || j.BackendState == "Stopped" && j.Self != nil && len(j.Self.TailscaleIPs) > 0
	if j.Self != nil && st.LoggedIn {
		st.Name, st.DNSName, st.IPs = j.Self.HostName, strings.TrimSuffix(j.Self.DNSName, "."), j.Self.TailscaleIPs
	}
	if j.CurrentTailnet != nil {
		st.Tailnet, st.MagicDNS = j.CurrentTailnet.Name, j.CurrentTailnet.MagicDNSEnabled
	}
	for _, p := range j.Peer {
		st.Peers = append(st.Peers, TSPeer{Name: p.HostName, DNSName: strings.TrimSuffix(p.DNSName, "."), IPs: p.TailscaleIPs, OS: p.OS, Online: p.Online, ExitNode: p.ExitNode})
		if p.Online {
			st.Online++
		}
	}
	slices.SortFunc(st.Peers, func(a, b TSPeer) int {
		if a.Online != b.Online {
			if a.Online {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return st, nil
}

// TailscaleStatus asks tailscaled for its state.
func TailscaleStatus(ctx context.Context) TSStatus {
	if !TailscaleInstalled() {
		return TSStatus{State: "NotInstalled", Peers: []TSPeer{}}
	}
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 10 * time.Second, AllowCodes: []int{1}}, "tailscale", "status", "--json")
	st, perr := ParseTSStatus([]byte(out))
	if perr != nil {
		st = TSStatus{Installed: true, State: "NotRunning", Peers: []TSPeer{}}
		if err != nil {
			st.Error = err.Error()
		}
	}
	login.Lock()
	if st.AuthURL == "" && !st.LoggedIn && time.Since(login.at) < 10*time.Minute {
		st.AuthURL = login.url
	}
	login.Unlock()
	return st
}

// login is the "tailscale login" waiting for the user in a browser.
var login struct {
	sync.Mutex
	cancel context.CancelFunc
	url    string
	at     time.Time
}

var authURLRe = regexp.MustCompile(`https://\S+`)

// TailscaleLogin starts a sign-in. With an auth key it completes at once;
// otherwise it returns the URL to open, while "tailscale login" keeps waiting
// in the background (10 minutes at most) and done runs once it ends.
func TailscaleLogin(authKey string, done func(error)) (string, error) {
	if !TailscaleInstalled() {
		return "", fmt.Errorf("install Tailscale first")
	}
	login.Lock()
	if login.cancel != nil {
		login.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	login.cancel, login.url, login.at = cancel, "", time.Now()
	login.Unlock()
	args := []string{"login"}
	if authKey != "" {
		// A file keeps the key out of the process list.
		f, err := os.CreateTemp("", "strata-ts-*")
		if err != nil {
			cancel()
			return "", err
		}
		defer os.Remove(f.Name())
		_ = f.Chmod(0o600)
		_, _ = f.WriteString(authKey)
		f.Close()
		args = append(args, "--auth-key=file:"+f.Name())
		_, err = sys.RunOpts(ctx, sys.Opts{Timeout: 90 * time.Second}, "tailscale", args...)
		cancel()
		if done != nil {
			done(err)
		}
		return "", err
	}
	cmd := exec.CommandContext(ctx, "tailscale", args...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		cancel()
		return "", err
	}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		sent := false
		for sc.Scan() {
			if u := authURLRe.FindString(sc.Text()); u != "" && !sent {
				sent = true
				login.Lock()
				login.url = u
				login.Unlock()
				found <- u
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		pw.Close()
		cancel()
		login.Lock()
		login.url = ""
		login.Unlock()
		if done != nil {
			done(err)
		}
	}()
	// The URL comes within a few seconds; tailscaled also publishes it in its status.
	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case u := <-found:
			return u, nil
		case <-tick.C:
			if st := TailscaleStatus(context.Background()); st.AuthURL != "" {
				return st.AuthURL, nil
			} else if st.LoggedIn {
				return "", nil
			}
		case <-deadline:
			return "", fmt.Errorf("Tailscale did not give a sign-in address (is tailscaled running?)")
		}
	}
}

// TailscaleLogout signs the NAS out of its tailnet.
func TailscaleLogout() error {
	login.Lock()
	if login.cancel != nil {
		login.cancel()
	}
	login.Unlock()
	_, err := sys.Run("tailscale", "logout")
	return err
}

// ValidateTailscale checks and normalises the routes (CIDR list).
func ValidateTailscale(c *store.Tailscale) error {
	var out []string
	for _, r := range strings.FieldsFunc(c.Routes, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		p, err := netip.ParsePrefix(r)
		if err != nil {
			return fmt.Errorf("invalid network %q (expected 192.168.1.0/24)", r)
		}
		if p.Bits() == 0 {
			return fmt.Errorf("use the exit node option to route all traffic")
		}
		out = append(out, p.Masked().String())
	}
	slices.Sort(out)
	c.Routes = strings.Join(slices.Compact(out), ",")
	return nil
}

// TailscaleSetArgs are the arguments of "tailscale set" for the options;
// lan is the network advertised when AdvertiseLAN is on without routes.
func TailscaleSetArgs(c store.Tailscale, lan string) []string {
	routes := ""
	if c.AdvertiseLAN {
		routes = c.Routes
		if routes == "" {
			routes = lan
		}
	}
	return []string{"set",
		"--advertise-routes=" + routes,
		fmt.Sprintf("--advertise-exit-node=%v", c.ExitNode),
		fmt.Sprintf("--accept-routes=%v", c.AcceptRoutes),
		fmt.Sprintf("--ssh=%v", c.SSH),
	}
}

// ApplyTailscale sets the options (also when the NAS is not signed in yet:
// tailscaled keeps them).
func ApplyTailscale(c store.Tailscale, lan string) error {
	if !TailscaleInstalled() {
		return fmt.Errorf("install Tailscale first")
	}
	if c.AdvertiseLAN || c.ExitNode {
		// Routing for the other machines of the tailnet.
		_ = os.WriteFile("/etc/sysctl.d/90-strata-tailscale.conf", []byte("net.ipv4.ip_forward = 1\nnet.ipv6.conf.all.forwarding = 1\n"), 0o644)
		_, _ = sys.Run("sysctl", "-q", "-p", "/etc/sysctl.d/90-strata-tailscale.conf")
	}
	_, err := sys.Run("tailscale", TailscaleSetArgs(c, lan)...)
	return err
}
