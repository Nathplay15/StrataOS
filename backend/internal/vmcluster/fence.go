package vmcluster

import (
	"net"
	"slices"
	"strings"
)

// Fencing: before an HA VM restarts elsewhere, the Ceph clients of the dead
// member on its disks are blocklisted (`ceph osd blocklist add`), so the old
// QEMU, if it still runs somewhere, can never write to the disk again, and
// its exclusive lock is released. Only the clients of the dead member (by
// address) are fenced: each is an address with its own nonce, so the NAS
// itself and its future clients are not affected.

// Lock is an RBD lock as `rbd lock ls --format json` reports it.
type Lock struct {
	ID      string `json:"id"`
	Locker  string `json:"locker"`
	Address string `json:"address"`
}

// Watcher is a client watching an RBD image (`rbd status --format json`).
type Watcher struct {
	Address string `json:"address"`
}

// AddrIP returns the IP of a Ceph entity address ("10.0.0.2:0/3416770527",
// "v1:10.0.0.2:0/12", "[fd00::2]:0/12"), or "".
func AddrIP(a string) string {
	a = strings.TrimPrefix(strings.TrimPrefix(a, "v1:"), "v2:")
	hp, _, _ := strings.Cut(a, "/")
	h, _, err := net.SplitHostPort(hp)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.String()
	}
	return ""
}

// FenceAddrs lists the client addresses (with their nonce) to blocklist:
// those of the lock holders and watchers whose IP belongs to the dead member.
// A client with a nonce of 0 would stand for the whole address and is
// never returned (it would cut the member's other daemons).
func FenceAddrs(locks []Lock, watchers []Watcher, deadIPs []string) []string {
	var ips []string
	for _, ip := range deadIPs {
		if p := net.ParseIP(ip); p != nil {
			ips = append(ips, p.String())
		}
	}
	var out []string
	add := func(a string) {
		_, nonce, ok := strings.Cut(a, "/")
		if !ok || nonce == "" || nonce == "0" || !slices.Contains(ips, AddrIP(a)) || slices.Contains(out, a) {
			return
		}
		out = append(out, a)
	}
	for _, l := range locks {
		add(l.Address)
	}
	for _, w := range watchers {
		add(w.Address)
	}
	return out
}

// LocksToBreak lists the locks held by the dead member (by address).
func LocksToBreak(locks []Lock, deadIPs []string) []Lock {
	var out []Lock
	for _, l := range locks {
		ip := AddrIP(l.Address)
		if ip != "" && slices.ContainsFunc(deadIPs, func(d string) bool { p := net.ParseIP(d); return p != nil && p.String() == ip }) {
			out = append(out, l)
		}
	}
	return out
}

// SafeToStart reports whether an image is free for a new QEMU: no lock and
// no watcher left from another address than ownIPs (the starting member).
func SafeToStart(locks []Lock, watchers []Watcher, ownIPs []string) bool {
	other := func(a string) bool { ip := AddrIP(a); return ip == "" || !slices.Contains(ownIPs, ip) }
	for _, l := range locks {
		if other(l.Address) {
			return false
		}
	}
	for _, w := range watchers {
		if other(w.Address) {
			return false
		}
	}
	return true
}
