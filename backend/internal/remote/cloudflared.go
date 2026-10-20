package remote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// CloudflaredInstalled reports whether cloudflared is present.
func CloudflaredInstalled() bool { return sys.Has("cloudflared") }

const (
	tunnelUnit = "strata-cloudflared.service"
	// MetricsAddr is where cloudflared answers /ready (local only).
	MetricsAddr = "127.0.0.1:20241"
)

// Paths, replaced in tests.
var (
	TunnelUnitPath = "/etc/systemd/system/" + tunnelUnit
	TunnelEnvPath  = "/etc/strata/cloudflared.env"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseTunnelToken checks a token of the Cloudflare dashboard (base64 JSON
// with the account "a", the tunnel "t" and its secret "s") and returns the
// tunnel ID. The command "cloudflared service install <token>" is accepted too.
func ParseTunnelToken(tok string) (token, tunnelID string, err error) {
	tok = strings.TrimSpace(tok)
	if f := strings.Fields(tok); len(f) > 1 {
		tok = f[len(f)-1]
	}
	if tok == "" || len(tok) > 2048 || strings.ContainsAny(tok, " \t\r\n\"'\\") {
		return "", "", fmt.Errorf("invalid tunnel token")
	}
	raw, err := base64.StdEncoding.DecodeString(tok)
	if err != nil {
		if raw, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(tok, "=")); err != nil {
			return "", "", fmt.Errorf("invalid tunnel token (copy it from Cloudflare: Networks → Tunnels → your tunnel → install command)")
		}
	}
	var j struct {
		A string `json:"a"`
		T string `json:"t"`
		S string `json:"s"`
	}
	if json.Unmarshal(raw, &j) != nil || !uuidRe.MatchString(strings.ToLower(j.T)) || j.A == "" || j.S == "" {
		return "", "", fmt.Errorf("invalid tunnel token (copy it from Cloudflare: Networks → Tunnels → your tunnel → install command)")
	}
	return tok, strings.ToLower(j.T), nil
}

// TunnelUnit is the systemd service running the tunnel; the token is read
// from an environment file only root can read.
func TunnelUnit(bin string) string {
	return `[Unit]
Description=Cloudflare Tunnel (StrataOS)
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=` + TunnelEnvPath + `
ExecStart=` + bin + ` --no-autoupdate --metrics ` + MetricsAddr + ` tunnel run
Restart=on-failure
RestartSec=5s
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`
}

// ApplyTunnel starts (or stops) the tunnel service.
func ApplyTunnel(c store.CloudflareTunnel) error {
	if !c.Enabled || c.Token == "" {
		if _, err := os.Stat(TunnelUnitPath); err == nil {
			_, _ = sys.Run("systemctl", "disable", "--now", tunnelUnit)
		}
		_ = os.Remove(TunnelEnvPath)
		return nil
	}
	if !CloudflaredInstalled() {
		return fmt.Errorf("install cloudflared first")
	}
	bin, err := exec.LookPath("cloudflared")
	if err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/strata", 0o755); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(TunnelEnvPath, []byte("TUNNEL_TOKEN="+c.Token+"\n"), 0o600); err != nil {
		return err
	}
	if err := sys.WriteFileAtomic(TunnelUnitPath, []byte(TunnelUnit(bin)), 0o644); err != nil {
		return err
	}
	if _, err := sys.Run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := sys.Run("systemctl", "enable", tunnelUnit); err != nil {
		return err
	}
	_, err = sys.Run("systemctl", "restart", tunnelUnit)
	return err
}

// TunnelStatus is the state of the connector.
type TunnelStatus struct {
	Installed   bool   `json:"installed"`
	Configured  bool   `json:"configured"`
	Service     string `json:"service"` // systemd state
	Connected   bool   `json:"connected"`
	Connections int    `json:"connections"`
	ConnectorID string `json:"connectorId,omitempty"`
	TunnelID    string `json:"tunnelId,omitempty"`
}

// ParseReady reads the answer of cloudflared's /ready endpoint.
func ParseReady(b []byte) (connections int, connector string) {
	var j struct {
		ReadyConnections int    `json:"readyConnections"`
		ConnectorID      string `json:"connectorId"`
	}
	_ = json.Unmarshal(b, &j)
	return j.ReadyConnections, j.ConnectorID
}

// Tunnel returns the state of the tunnel.
func Tunnel(ctx context.Context, c store.CloudflareTunnel) TunnelStatus {
	st := TunnelStatus{Installed: CloudflaredInstalled(), Configured: c.Enabled && c.Token != "", TunnelID: c.TunnelID, Service: "inactive"}
	if !st.Configured {
		return st
	}
	out, _ := sys.RunOpts(ctx, sys.Opts{AllowCodes: []int{-2}, Timeout: 5 * time.Second}, "systemctl", "is-active", tunnelUnit)
	if s := strings.TrimSpace(out); s != "" {
		st.Service = s
	}
	cl := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+MetricsAddr+"/ready", nil)
	if resp, err := cl.Do(req); err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		st.Connections, st.ConnectorID = ParseReady(b)
		st.Connected = resp.StatusCode == http.StatusOK && st.Connections > 0
	}
	return st
}
