package vms

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"strataos/internal/sys"
)

// CloudInit is the first-boot configuration of a VM made from a cloud image,
// given to cloud-init as a NoCloud seed (an ISO labelled "cidata").
type CloudInit struct {
	Hostname string   `json:"hostname"`
	User     string   `json:"user"`
	Password string   `json:"password,omitempty"` // hashed (SHA-512 crypt) before it is written
	SSHKeys  []string `json:"sshKeys,omitempty"`
	Packages []string `json:"packages,omitempty"`
	// Static IPv4 configuration (empty: DHCP).
	Address string   `json:"address,omitempty"` // CIDR, e.g. 192.168.1.50/24
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

var (
	hostLabelRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9\-]{0,61}[A-Za-z0-9])?$`)
	guestUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_\-]{0,31}$`)
	sshKeyRe    = regexp.MustCompile(`^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com) [A-Za-z0-9+/]+={0,3}( [^\x00-\x1f]{0,200})?$`)
	debPkgRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9+.\-]{0,62}$`)
)

// ValidSSHKey reports whether a line is an SSH public key.
func ValidSSHKey(l string) bool { return sshKeyRe.MatchString(l) }

// reservedUsers are accounts of the guest image that must not be redefined.
var reservedUsers = map[string]bool{"root": true, "daemon": true, "bin": true, "sys": true, "nobody": true, "systemd-network": true}

// Validate checks and normalises the configuration.
func (c *CloudInit) Validate() error {
	if len(c.Hostname) > 253 {
		return fmt.Errorf("invalid host name")
	}
	for _, l := range strings.Split(c.Hostname, ".") {
		if !hostLabelRe.MatchString(l) {
			return fmt.Errorf("invalid host name")
		}
	}
	if !guestUserRe.MatchString(c.User) || reservedUsers[c.User] {
		return fmt.Errorf("invalid user name")
	}
	if c.Password == "" && len(c.SSHKeys) == 0 {
		return fmt.Errorf("give a password or an SSH key, or the VM cannot be signed in to")
	}
	if c.Password != "" && (len(c.Password) < 6 || len(c.Password) > 256 || strings.ContainsAny(c.Password, "\n\r")) {
		return fmt.Errorf("the password must be 6 to 256 characters")
	}
	var keys []string
	for _, k := range c.SSHKeys {
		for _, l := range strings.Split(k, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			if !sshKeyRe.MatchString(l) {
				return fmt.Errorf("invalid SSH public key")
			}
			keys = append(keys, l)
		}
	}
	c.SSHKeys = keys
	for _, p := range c.Packages {
		if !debPkgRe.MatchString(p) {
			return fmt.Errorf("invalid package name %q", p)
		}
	}
	if c.Address != "" {
		ip, _, err := net.ParseCIDR(c.Address)
		if err != nil || ip.To4() == nil {
			return fmt.Errorf("the static address must be IPv4 with a prefix, e.g. 192.168.1.50/24")
		}
		if c.Gateway != "" && net.ParseIP(c.Gateway).To4() == nil {
			return fmt.Errorf("invalid gateway")
		}
	} else if c.Gateway != "" {
		return fmt.Errorf("a gateway needs a static address")
	}
	for _, d := range c.DNS {
		if net.ParseIP(d) == nil {
			return fmt.Errorf("invalid DNS server %q", d)
		}
	}
	return nil
}

// yq quotes a YAML scalar (a JSON string is a valid YAML double-quoted scalar).
func yq(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// CloudDoneMarker is printed on the console when cloud-init has finished.
const CloudDoneMarker = "strata-cloud-init-done"

// seedFiles returns the meta-data, user-data and network-config ("" = DHCP)
// documents. passwordHash is the SHA-512 crypt of the password.
func seedFiles(c CloudInit, instanceID, passwordHash string) (meta, user, network string) {
	meta = "instance-id: " + yq(instanceID) + "\nlocal-hostname: " + yq(c.Hostname) + "\n"
	var u strings.Builder
	u.WriteString("#cloud-config\n")
	u.WriteString("hostname: " + yq(c.Hostname) + "\n")
	u.WriteString("manage_etc_hosts: true\n")
	u.WriteString("users:\n  - name: " + yq(c.User) + "\n")
	u.WriteString("    groups: [sudo]\n    shell: /bin/bash\n    sudo: \"ALL=(ALL) NOPASSWD:ALL\"\n")
	if passwordHash != "" {
		u.WriteString("    lock_passwd: false\n    passwd: " + yq(passwordHash) + "\n")
	}
	if len(c.SSHKeys) > 0 {
		u.WriteString("    ssh_authorized_keys:\n")
		for _, k := range c.SSHKeys {
			u.WriteString("      - " + yq(k) + "\n")
		}
	}
	if passwordHash != "" {
		u.WriteString("ssh_pwauth: true\n")
	} else {
		u.WriteString("ssh_pwauth: false\n")
	}
	// The guest agent lets StrataOS read the VM addresses and freeze its file
	// systems for consistent backups.
	pkgs := append([]string{"qemu-guest-agent"}, c.Packages...)
	u.WriteString("package_update: true\npackages:\n")
	seen := map[string]bool{}
	for _, p := range pkgs {
		if !seen[p] {
			seen[p] = true
			u.WriteString("  - " + yq(p) + "\n")
		}
	}
	u.WriteString("runcmd:\n  - [systemctl, enable, --now, qemu-guest-agent]\n")
	u.WriteString("final_message: " + yq(CloudDoneMarker+" after $UPTIME seconds") + "\n")
	user = u.String()
	if c.Address != "" {
		var n strings.Builder
		n.WriteString("version: 2\nethernets:\n  primary:\n    match:\n      name: \"e*\"\n    dhcp4: false\n")
		n.WriteString("    addresses: [" + yq(c.Address) + "]\n")
		if c.Gateway != "" {
			n.WriteString("    routes:\n      - to: default\n        via: " + yq(c.Gateway) + "\n")
		}
		if len(c.DNS) > 0 {
			q := make([]string, len(c.DNS))
			for i, d := range c.DNS {
				q[i] = yq(d)
			}
			n.WriteString("    nameservers:\n      addresses: [" + strings.Join(q, ", ") + "]\n")
		}
		network = n.String()
	}
	return meta, user, network
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeSeed writes the cloud-init documents into dir/cloud-init and builds dir/seed.iso.
func writeSeed(dir, name string, c CloudInit) (string, error) {
	hash := ""
	if c.Password != "" {
		hash = sha512Crypt(c.Password, randomSalt())
	}
	meta, user, network := seedFiles(c, name+"-"+randomHex(4), hash)
	return buildSeed(dir, meta, user, network)
}

func buildSeed(dir, meta, user, network string) (string, error) {
	cdir := filepath.Join(dir, "cloud-init")
	if err := os.MkdirAll(cdir, 0o700); err != nil {
		return "", err
	}
	// The folder sits in a shared folder: keep its ACLs (and the password hash) out.
	if sys.Has("setfacl") {
		_, _ = sys.Run("setfacl", "-b", cdir)
	}
	files := map[string]string{"meta-data": meta, "user-data": user}
	if network != "" {
		files["network-config"] = network
	} else {
		_ = os.Remove(filepath.Join(cdir, "network-config"))
	}
	var paths []string
	for _, f := range []string{"user-data", "meta-data", "network-config"} {
		if body, ok := files[f]; ok {
			p := filepath.Join(cdir, f)
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				return "", err
			}
			if sys.Has("setfacl") {
				_, _ = sys.Run("setfacl", "-b", p)
			}
			paths = append(paths, p)
		}
	}
	iso := filepath.Join(dir, "seed.iso")
	_ = os.Remove(iso)
	var err error
	switch {
	case sys.Has("xorriso"):
		_, err = sys.Run("xorriso", append([]string{"-as", "genisoimage", "-output", iso, "-volid", "cidata", "-joliet", "-rock"}, paths...)...)
	case sys.Has("genisoimage"):
		_, err = sys.Run("genisoimage", append([]string{"-output", iso, "-volid", "cidata", "-joliet", "-rock"}, paths...)...)
	case sys.Has("cloud-localds"):
		args := []string{}
		if network != "" {
			args = append(args, "-N", filepath.Join(cdir, "network-config"))
		}
		_, err = sys.Run("cloud-localds", append(args, iso, filepath.Join(cdir, "user-data"), filepath.Join(cdir, "meta-data"))...)
	default:
		err = fmt.Errorf("xorriso is not installed (reinstall the virtualisation component)")
	}
	if err != nil {
		return "", err
	}
	_ = os.Chmod(iso, 0o640)
	return iso, grantQemu(iso, false)
}

var hostnameLineRe = regexp.MustCompile(`(?m)^hostname: .*$`)

// cloneSeed gives the copy of a cloud VM its own seed: new instance id (cloud-init
// then runs again: host name, SSH host keys) and the new name as host name.
func cloneSeed(oldDir, newDir, name string) (string, error) {
	cdir := filepath.Join(oldDir, "cloud-init")
	user, err := os.ReadFile(filepath.Join(cdir, "user-data"))
	if err != nil {
		return "", nil
	}
	network, _ := os.ReadFile(filepath.Join(cdir, "network-config"))
	host := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "_", "-"), ".", "-"))
	u := hostnameLineRe.ReplaceAllString(string(user), "hostname: "+yq(host))
	meta := "instance-id: " + yq(name+"-"+randomHex(4)) + "\nlocal-hostname: " + yq(host) + "\n"
	return buildSeed(newDir, meta, u, string(network))
}

// ---- SHA-512 crypt ($6$), the password hash format of glibc / cloud-init ----

const cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func randomSalt() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = cryptAlphabet[int(b[i])%64]
	}
	return string(b)
}

// sha512Crypt implements Ulrich Drepper's SHA-crypt with 5000 rounds.
func sha512Crypt(password, salt string) string {
	if len(salt) > 16 {
		salt = salt[:16]
	}
	P, S := []byte(password), []byte(salt)
	h := sha512.New()
	h.Write(P)
	h.Write(S)
	h.Write(P)
	B := h.Sum(nil)

	h = sha512.New()
	h.Write(P)
	h.Write(S)
	i := len(P)
	for ; i > 64; i -= 64 {
		h.Write(B)
	}
	h.Write(B[:i])
	for i = len(P); i > 0; i >>= 1 {
		if i&1 != 0 {
			h.Write(B)
		} else {
			h.Write(P)
		}
	}
	A := h.Sum(nil)

	h = sha512.New()
	for range len(P) {
		h.Write(P)
	}
	DP := h.Sum(nil)
	Pseq := make([]byte, 0, len(P))
	for len(Pseq) < len(P) {
		n := min(64, len(P)-len(Pseq))
		Pseq = append(Pseq, DP[:n]...)
	}
	h = sha512.New()
	for range 16 + int(A[0]) {
		h.Write(S)
	}
	DS := h.Sum(nil)
	Sseq := DS[:len(S)]

	C := A
	for r := range 5000 {
		h = sha512.New()
		if r&1 != 0 {
			h.Write(Pseq)
		} else {
			h.Write(C)
		}
		if r%3 != 0 {
			h.Write(Sseq)
		}
		if r%7 != 0 {
			h.Write(Pseq)
		}
		if r&1 != 0 {
			h.Write(C)
		} else {
			h.Write(Pseq)
		}
		C = h.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$6$" + salt + "$")
	enc := func(b2, b1, b0 byte, n int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for range n {
			out.WriteByte(cryptAlphabet[w&0x3f])
			w >>= 6
		}
	}
	order := [][3]int{{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48}, {28, 49, 7}, {50, 8, 29}, {9, 30, 51},
		{31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13}, {56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41}}
	for _, o := range order {
		enc(C[o[0]], C[o[1]], C[o[2]], 4)
	}
	enc(0, 0, C[63], 2)
	return out.String()
}
