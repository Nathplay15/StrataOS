// Package s3gw drives the built-in S3 service: versitygw (Versity S3
// Gateway, Apache 2.0), a single static Go binary pinned by checksum
// (packaging/scripts/install-s3.sh), with its POSIX backend. Each exposed
// shared folder is a bucket: a symbolic link in BucketsDir (--bucketlinks)
// to the folder itself, so objects are the folder's ordinary files, seen
// over SMB, NFS and the file manager. StrataOS writes the gateway's
// accounts (internal IAM, users.json) and the bucket ACLs (sidecar
// metadata) from the share permissions; the gateway reads both at every
// request, so keys and rights change without a restart.
package s3gw

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Paths and unit of the service.
var (
	Binary     = "/usr/local/lib/strata/versitygw"
	Dir        = "/var/lib/strata/s3"
	BucketsDir = Dir + "/buckets" // bucket name → shared folder links
	MetaDir    = Dir + "/meta"    // sidecar metadata (ACLs, ETags…), out of the shares
	IAMDir     = Dir + "/iam"
	EnvFile    = Dir + "/root.env"
	AccessLog  = "/var/log/strata/s3-access.log"
	Unit       = "strata-s3.service"
	UnitPath   = "/etc/systemd/system/" + Unit
)

// DefaultPort is the HTTP(S) port of the service.
const DefaultPort = 7380

var bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// ValidBucketName applies the S3 naming rules (those virtual-host-style
// addressing needs).
func ValidBucketName(n string) error {
	switch {
	case !bucketRe.MatchString(n):
		return fmt.Errorf("invalid bucket name %q: 3 to 63 lowercase letters, digits, dots and hyphens", n)
	case strings.Contains(n, ".."), strings.Contains(n, ".-"), strings.Contains(n, "-."):
		return fmt.Errorf("invalid bucket name %q", n)
	case net.ParseIP(n) != nil:
		return fmt.Errorf("a bucket name cannot look like an IP address")
	case strings.HasPrefix(n, "xn--"), strings.HasPrefix(n, "sthree-"), strings.HasSuffix(n, "-s3alias"), strings.HasSuffix(n, "--ol-s3"):
		return fmt.Errorf("reserved bucket name %q", n)
	}
	return nil
}

// BucketName proposes a bucket name for a shared folder name.
func BucketName(share string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(share) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	n := strings.Trim(b.String(), "-")
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	if len(n) > 63 {
		n = strings.TrimRight(n[:63], "-")
	}
	for len(n) < 3 {
		n += "-0"
		n = strings.TrimLeft(n, "-")
	}
	return n
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidDomain checks the base domain of virtual-host-style addressing.
func ValidDomain(d string) error {
	if d != "" && (!domainRe.MatchString(d) || len(d) > 200) {
		return fmt.Errorf("invalid domain %q (e.g. s3.nas.example.com)", d)
	}
	return nil
}

// Account is a gateway account: one access key of a StrataOS user. Files
// it writes belong to UID/GID (--chuid/--chgid).
type Account struct {
	Access string
	Secret string
	UID    int
	GID    int
}

// IAMJSON is the users.json of the gateway's internal IAM.
func IAMJSON(accounts []Account) []byte {
	type acc struct {
		Access    string `json:"access"`
		Secret    string `json:"secret"`
		Role      string `json:"role"`
		UserID    int    `json:"userID"`
		GroupID   int    `json:"groupID"`
		ProjectID int    `json:"projectID"`
	}
	m := map[string]acc{}
	for _, a := range accounts {
		m[a.Access] = acc{Access: a.Access, Secret: a.Secret, Role: "user", UserID: a.UID, GroupID: a.GID}
	}
	b, _ := json.Marshal(map[string]any{"accessAccounts": m})
	return b
}

// Grant is what an access key may do in a bucket.
type Grant struct {
	Access string
	Write  bool
}

// Perm turns a share permission (rw, ro, none, "") into a grant.
func Perm(perm string) (read, write bool) {
	switch perm {
	case "rw":
		return true, true
	case "ro":
		return true, false
	}
	return false, false
}

// ACLJSON is a bucket ACL as the gateway stores it: owned by the root
// account, READ (list, get) for every grant, WRITE (put, delete) for the
// writable ones.
func ACLJSON(owner string, grants []Grant) []byte {
	type grantee struct {
		Permission string
		Access     string
		Type       string
	}
	gs := slices.Clone(grants)
	sort.Slice(gs, func(i, j int) bool { return gs[i].Access < gs[j].Access })
	out := struct {
		Owner    string
		Grantees []grantee
	}{Owner: owner, Grantees: []grantee{}}
	for _, g := range gs {
		out.Grantees = append(out.Grantees, grantee{"READ", g.Access, "CanonicalUser"})
		if g.Write {
			out.Grantees = append(out.Grantees, grantee{"WRITE", g.Access, "CanonicalUser"})
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// Options of the service.
type Options struct {
	Port     int
	Domain   string // virtual-host-style base domain
	CertFile string // HTTPS with the NAS certificate when set
	KeyFile  string
	// CertID changes with the certificate, so a renewed one restarts the
	// service (it reads the files at start).
	CertID string
}

// UnitFile runs the gateway. It runs as root to give new files to their
// user (--chuid/--chgid); the root credentials come from EnvFile, never
// the command line.
func UnitFile(o Options) string {
	args := []string{Binary, "--port", ":" + strconv.Itoa(o.Port), "--region", "us-east-1",
		"--iam-dir", IAMDir, "--iam-cache-disable", "--access-log", AccessLog, "--quiet"}
	if o.CertFile != "" && o.KeyFile != "" {
		args = append(args, "--cert", o.CertFile, "--key", o.KeyFile)
	}
	if o.Domain != "" {
		args = append(args, "--virtual-domain", o.Domain)
	}
	args = append(args, "posix", "--bucketlinks", "--chuid", "--chgid", "--sidecar", MetaDir, BucketsDir)
	return fmt.Sprintf(`# Managed by StrataOS: built-in S3 service
# Certificate: %s
[Unit]
Description=StrataOS S3 service (versitygw)
After=network-online.target local-fs.target
Wants=network-online.target

[Service]
EnvironmentFile=%s
ExecStart=%s
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=full
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=CAP_CHOWN CAP_FOWNER CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH CAP_NET_BIND_SERVICE
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, o.CertID, EnvFile, strings.Join(args, " "))
}

// EnvFileContent holds the root credentials of the gateway.
func EnvFileContent(access, secret string) string {
	return "ROOT_ACCESS_KEY=" + access + "\nROOT_SECRET_KEY=" + secret + "\n"
}

// Entry is one request of the access log.
type Entry struct {
	Time   time.Time `json:"time"`
	IP     string    `json:"ip"`
	Access string    `json:"access"`
	Op     string    `json:"op"`
	Bucket string    `json:"bucket"`
	Key    string    `json:"key"`
	Status int       `json:"status"`
	Error  string    `json:"error,omitempty"`
}

// ParseLine reads a line of the access log (the S3 server access log
// format: owner bucket [time] ip requester request-id operation key
// request-uri status error-code …).
func ParseLine(line string) (Entry, bool) {
	open := strings.IndexByte(line, '[')
	end := strings.IndexByte(line, ']')
	if open < 0 || end < open {
		return Entry{}, false
	}
	head := strings.Fields(line[:open])
	rest := strings.Fields(line[end+1:])
	if len(head) < 2 || len(rest) < 8 {
		return Entry{}, false
	}
	t, err := time.Parse("02/January/2006:15:04:05 -0700", line[open+1:end])
	if err != nil {
		return Entry{}, false
	}
	status, err := strconv.Atoi(rest[6])
	if err != nil {
		return Entry{}, false
	}
	dash := func(s string) string {
		if s == "-" {
			return ""
		}
		return s
	}
	return Entry{Time: t, IP: rest[0], Access: dash(rest[1]), Op: strings.TrimPrefix(rest[3], "s3_"), Bucket: dash(head[1]),
		Key: dash(rest[4]), Status: status, Error: dash(rest[7])}, true
}

// Tail returns the last n entries of the log, newest first.
func Tail(r io.Reader, n int) []Entry {
	var ring []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if e, ok := ParseLine(sc.Text()); ok {
			ring = append(ring, e)
			if len(ring) > n {
				ring = ring[len(ring)-n:]
			}
		}
	}
	slices.Reverse(ring)
	return ring
}
