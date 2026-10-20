package store

import "time"

// Remote groups the remote access methods that need no open port (Tailscale,
// Cloudflare Tunnel) and the app domains served by the built-in reverse proxy.
type Remote struct {
	Tailscale Tailscale        `json:"tailscale,omitzero"`
	Tunnel    CloudflareTunnel `json:"tunnel,omitzero"`
	Proxy     AppProxy         `json:"proxy,omitzero"`
}

// Tailscale holds the options StrataOS applies with "tailscale set" (the
// sign-in itself lives in tailscaled).
type Tailscale struct {
	AdvertiseLAN bool   `json:"advertiseLan,omitempty"`
	Routes       string `json:"routes,omitempty"` // comma-separated networks offered to the tailnet
	ExitNode     bool   `json:"exitNode,omitempty"`
	AcceptRoutes bool   `json:"acceptRoutes,omitempty"`
	SSH          bool   `json:"ssh,omitempty"`
}

// CloudflareTunnel runs cloudflared with the token of a tunnel created in the
// Cloudflare dashboard.
type CloudflareTunnel struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Token    string `json:"token,omitempty"`
	TunnelID string `json:"tunnelId,omitempty"`
}

// AppProxy is the reverse proxy of the daemon on ports 80 and 443.
type AppProxy struct {
	Enabled bool         `json:"enabled,omitempty"`
	Email   string       `json:"email,omitempty"` // Let's Encrypt account
	Domains []*AppDomain `json:"domains,omitempty"`
}

// AppDomain sends a host name to a local service.
type AppDomain struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	// Kind: "app" (installed app, by stack name), "url" (http(s)://host:port
	// on this machine or the local network) or "ui" (the StrataOS interface).
	Kind      string    `json:"kind"`
	App       string    `json:"app,omitempty"`
	Target    string    `json:"target,omitempty"`
	LANOnly   bool      `json:"lanOnly,omitempty"`
	Created   time.Time `json:"created"`
	CertError string    `json:"certError,omitempty"`
	CertTried time.Time `json:"certTried,omitzero"`
}
