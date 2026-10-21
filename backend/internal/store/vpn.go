package store

import "time"

// VPNPeerPolicy is what StrataOS 0.6 added to a VPN peer: its owner, its
// kind (device or site) and what it may reach.
type VPNPeerPolicy struct {
	Owner string `json:"owner,omitempty"` // account the device belongs to ("" = the administrators)
	Kind  string `json:"kind,omitempty"`  // "" device | site
	// Services restricts the peer to these services or ports (smb, web,
	// nfs, ssh, tcp/8080, udp/5000-5010); empty = no restriction.
	Services []string  `json:"services,omitempty"`
	Expires  time.Time `json:"expires,omitzero"`
	Expired  bool      `json:"expired,omitempty"` // disabled by its expiry date
	// Site: networks behind the other side, its address (host:port, when
	// this NAS should connect to it) and the keepalive interval.
	Subnets   []string `json:"subnets,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	Keepalive int      `json:"keepalive,omitempty"`
}

// VPNLink is a site-to-site tunnel created from another StrataOS's pairing
// code: this NAS connects out to it, on an interface of its own.
type VPNLink struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Iface         string    `json:"iface"`   // strata-l1…
	Address       string    `json:"address"` // address of this NAS in the tunnel
	PrivateKey    string    `json:"privateKey,omitempty"`
	PeerPublicKey string    `json:"peerPublicKey"`
	PresharedKey  string    `json:"presharedKey,omitempty"`
	Endpoint      string    `json:"endpoint"` // host:port of the other NAS
	Subnets       []string  `json:"subnets"`  // networks reached through it
	ListenPort    int       `json:"listenPort,omitempty"`
	Keepalive     int       `json:"keepalive,omitempty"`
	Disabled      bool      `json:"disabled,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// VPNClient is the outgoing tunnel: the NAS as a WireGuard client of a VPN
// provider (interface strata-out).
type VPNClient struct {
	Enabled bool   `json:"enabled"`
	Name    string `json:"name,omitempty"` // provider, for display
	// From the provider's configuration file.
	PrivateKey    string   `json:"privateKey,omitempty"`
	Addresses     []string `json:"addresses,omitempty"`
	DNS           []string `json:"dns,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	PeerPublicKey string   `json:"peerPublicKey,omitempty"`
	PresharedKey  string   `json:"presharedKey,omitempty"`
	Endpoint      string   `json:"endpoint,omitempty"`
	AllowedIPs    []string `json:"allowedIps,omitempty"`
	Keepalive     int      `json:"keepalive,omitempty"`
	// Scope: "apps" (the containers in Apps only) or "all" (the whole NAS).
	Scope      string   `json:"scope,omitempty"`
	Apps       []string `json:"apps,omitempty"`       // container names
	AppsSubnet string   `json:"appsSubnet,omitempty"` // Docker network of the routed apps
	CheckURL   string   `json:"checkUrl,omitempty"`   // returns the public address as text
	// Detached remembers the networks taken from a container to put it on
	// the routed network alone (given back when it leaves the tunnel).
	Detached map[string][]string `json:"detached,omitempty"`
}
