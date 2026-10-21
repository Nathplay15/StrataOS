package vpn

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"

	"strataos/internal/store"
)

// ValidKey reports whether k is a WireGuard key (32 bytes in base64).
func ValidKey(k string) bool {
	b, err := base64.StdEncoding.DecodeString(k)
	return err == nil && len(b) == 32
}

// ValidatePeer checks a device restored from a configuration backup before
// it goes into the WireGuard configuration of c.
func ValidatePeer(c store.VPN, p store.VPNPeer) error {
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("invalid device name")
	}
	switch p.Mode {
	case "nas", "lan", "all":
	default:
		return fmt.Errorf("invalid access")
	}
	if p.Kind != "" && p.Kind != "site" {
		return fmt.Errorf("invalid peer kind")
	}
	if len(p.Owner) > 64 || strings.ContainsAny(p.Owner, "\n\r/:") {
		return fmt.Errorf("invalid owner")
	}
	if _, err := ValidateServices(p.Services); err != nil {
		return err
	}
	if p.Kind == "site" {
		if _, err := checkSubnets(c, p.Subnets, p.ID); err != nil {
			return err
		}
		if (p.Endpoint != "" && !ValidEndpoint(p.Endpoint)) || p.Keepalive < 0 || p.Keepalive > 3600 {
			return fmt.Errorf("invalid site address")
		}
	} else if len(p.Subnets) > 0 || p.Endpoint != "" {
		return fmt.Errorf("only sites have networks")
	}
	if !ValidKey(p.PublicKey) || !ValidKey(p.PrivateKey) || (p.PresharedKey != "" && !ValidKey(p.PresharedKey)) {
		return fmt.Errorf("invalid device keys")
	}
	ip := net.ParseIP(p.IP)
	_, n, err := net.ParseCIDR(c.Subnet)
	if ip == nil || err != nil || !n.Contains(ip) || p.IP == ServerIP(c) {
		return fmt.Errorf("the address %s is not in the VPN network", p.IP)
	}
	for _, x := range c.Peers {
		if x.IP == p.IP || x.PublicKey == p.PublicKey {
			return fmt.Errorf("the address or key of %s is already used", p.Name)
		}
	}
	return nil
}
