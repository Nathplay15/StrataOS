// Package directory joins the NAS to an Active Directory domain (Samba member
// server with winbind) or to an LDAP directory (sssd). Domain users and groups
// then get rights on the shares like local ones.
package directory

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

var (
	Krb5Conf    = "/etc/krb5.conf"
	NSSwitch    = "/etc/nsswitch.conf"
	SSSDConf    = "/etc/sssd/sssd.conf"
	realmRe     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)+[A-Za-z]{2,63}$`)
	workgroupRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,14}$`)
	hostRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-:]{0,252}$`)
	adminRe     = regexp.MustCompile(`^[^\s\\/:*?"<>|,@%]{1,64}$`)
	dnRe        = regexp.MustCompile(`^[^\n\r]{1,512}$`)
)

func ADInstalled() bool {
	return sys.Has("net") && sys.Has("wbinfo") && fileExists("/usr/sbin/winbindd")
}
func LDAPInstalled() bool { return fileExists("/usr/sbin/sssd") }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Validate normalises a configuration before joining.
func Validate(c *store.Directory) error {
	switch c.Type {
	case "ad":
		c.Realm = strings.ToUpper(strings.TrimSpace(c.Realm))
		if !realmRe.MatchString(c.Realm) {
			return fmt.Errorf("invalid domain name (e.g. corp.example.com)")
		}
		c.Workgroup = strings.ToUpper(strings.TrimSpace(c.Workgroup))
		if c.Workgroup == "" {
			c.Workgroup = strings.SplitN(c.Realm, ".", 2)[0]
			if len(c.Workgroup) > 15 {
				c.Workgroup = c.Workgroup[:15]
			}
		}
		if !workgroupRe.MatchString(c.Workgroup) {
			return fmt.Errorf("invalid NetBIOS domain name")
		}
		if c.Server != "" && !hostRe.MatchString(c.Server) {
			return fmt.Errorf("invalid domain controller")
		}
	case "ldap":
		u, err := url.Parse(c.URI)
		if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Host == "" || strings.ContainsAny(c.URI, " \n\r") {
			return fmt.Errorf("the server must be ldap://host or ldaps://host")
		}
		if !dnRe.MatchString(c.BaseDN) || !strings.Contains(c.BaseDN, "=") {
			return fmt.Errorf("invalid base DN (e.g. dc=example,dc=com)")
		}
		if c.BindDN != "" && !dnRe.MatchString(c.BindDN) {
			return fmt.Errorf("invalid bind DN")
		}
		if strings.ContainsAny(c.BindPassword, "\n\r") {
			return fmt.Errorf("invalid bind password")
		}
	default:
		return fmt.Errorf("choose Active Directory or LDAP")
	}
	c.AdminGroup = strings.TrimSpace(c.AdminGroup)
	if strings.ContainsAny(c.AdminGroup, "\n\r:,") || len(c.AdminGroup) > 128 {
		return fmt.Errorf("invalid administrators group")
	}
	return nil
}

func writeKrb5(c store.Directory) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by StrataOS\n[libdefaults]\n    default_realm = %s\n    dns_lookup_realm = false\n    dns_lookup_kdc = true\n    rdns = false\n    forwardable = true\n", c.Realm)
	if c.Server != "" {
		fmt.Fprintf(&b, "\n[realms]\n    %s = {\n        kdc = %s\n        admin_server = %s\n    }\n", c.Realm, c.Server, c.Server)
	}
	fmt.Fprintf(&b, "\n[domain_realm]\n    .%s = %s\n    %s = %s\n", strings.ToLower(c.Realm), c.Realm, strings.ToLower(c.Realm), c.Realm)
	old, _ := os.ReadFile(Krb5Conf)
	if len(old) > 0 && !strings.HasPrefix(string(old), "# Managed by StrataOS") {
		_ = os.WriteFile(Krb5Conf+".pre-strata", old, 0o644)
	}
	return sys.WriteFileAtomic(Krb5Conf, []byte(b.String()), 0o644)
}

// setNSS adds (or removes) a source to the passwd and group databases.
func setNSS(source string, on bool) error {
	data, err := os.ReadFile(NSSwitch)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		key, rest, ok := strings.Cut(l, ":")
		if !ok || (strings.TrimSpace(key) != "passwd" && strings.TrimSpace(key) != "group") {
			continue
		}
		var srcs []string
		for _, f := range strings.Fields(rest) {
			if f != source {
				srcs = append(srcs, f)
			}
		}
		if on {
			srcs = append(srcs, source)
		}
		lines[i] = fmt.Sprintf("%-15s %s", strings.TrimSpace(key)+":", strings.Join(srcs, " "))
	}
	return sys.WriteFileAtomic(NSSwitch, []byte(strings.Join(lines, "\n")), 0o644)
}

// JoinAD makes the NAS a member of the domain. applySMB must write smb.conf for
// the domain (security = ads) before the join. The administrator password is
// only handed to `net` through its environment and never stored.
func JoinAD(ctx context.Context, c store.Directory, admin, password string, applySMB func() error, logf func(string, ...any)) error {
	if !ADInstalled() {
		return fmt.Errorf("Active Directory support is not installed")
	}
	if !adminRe.MatchString(admin) || password == "" || strings.ContainsAny(password, "\n\r") {
		return fmt.Errorf("enter a domain administrator account and its password")
	}
	flushWinbind()
	logf("Configuring Kerberos for %s", c.Realm)
	if err := writeKrb5(c); err != nil {
		return err
	}
	logf("Configuring Samba as a member server of domain %s", c.Workgroup)
	if err := applySMB(); err != nil {
		return err
	}
	auth, cleanup, err := authFile(admin, password)
	if err != nil {
		return err
	}
	defer cleanup()
	args := []string{"ads", "join", "-A", auth, "--no-dns-updates"}
	if c.Server != "" {
		args = append(args, "-S", c.Server)
	}
	logf("Joining the domain…")
	out, err := sys.RunOpts(ctx, sys.Opts{Timeout: 2 * time.Minute}, "net", args...)
	if err != nil {
		msg := strings.TrimSpace(firstLine(out + " " + err.Error()))
		if strings.Contains(out+err.Error(), "Invalid credentials") || strings.Contains(out+err.Error(), "LOGON_FAILURE") {
			msg = "wrong administrator account or password"
		}
		return fmt.Errorf("join failed: %s", msg)
	}
	logf("%s", strings.TrimSpace(out))
	if err := setNSS("winbind", true); err != nil {
		return err
	}
	for _, u := range []string{"winbind", "smbd", "nmbd"} {
		_, _ = sys.Run("systemctl", "enable", u+".service")
		if _, err := sys.Run("systemctl", "restart", u+".service"); err != nil {
			logf("%s : %v", u, err)
		}
	}
	// winbind needs a moment to reach the domain controller.
	for i := 0; i < 30; i++ {
		if _, err := sys.Run("wbinfo", "-t"); err == nil {
			// Rights on shares need domain names resolvable by the system.
			if _, err := sys.Run("getent", "group", c.Workgroup+`\domain users`); err == nil {
				logf("Trust relationship verified, domain accounts available")
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("joined, but winbind cannot reach the domain controller (check DNS)")
}

// LeaveAD removes the computer account (when credentials are given) and the
// local membership; applySMB then writes a standalone configuration again.
func LeaveAD(ctx context.Context, c store.Directory, admin, password string, applySMB func() error) error {
	var leaveErr error
	if admin != "" && password != "" && adminRe.MatchString(admin) {
		auth, cleanup, err := authFile(admin, password)
		if err == nil {
			defer cleanup()
			args := []string{"ads", "leave", "-A", auth}
			if c.Server != "" {
				args = append(args, "-S", c.Server)
			}
			var out string
			out, err = sys.RunOpts(ctx, sys.Opts{Timeout: time.Minute}, "net", args...)
			if err != nil {
				err = fmt.Errorf("%s", firstLine(out+" "+err.Error()))
			}
		}
		if err != nil {
			leaveErr = fmt.Errorf("the computer account could not be removed from the domain: %v", err)
		}
	}
	_ = setNSS("winbind", false)
	_, _ = sys.Run("systemctl", "disable", "--now", "winbind.service")
	flushWinbind()
	_ = os.Remove("/etc/krb5.keytab")
	if err := applySMB(); err != nil {
		return err
	}
	return leaveErr
}

// authFile writes the administrator credentials to a private temporary file for
// `net -A` (never on a command line, where any local user could read them).
func authFile(admin, password string) (string, func(), error) {
	f, err := os.CreateTemp("", "strata-auth-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	_ = f.Chmod(0o600)
	_, err = fmt.Fprintf(f, "username = %s\npassword = %s\n", admin, password)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}

// flushWinbind forgets cached domain SIDs and ID mappings, which would be wrong
// for another domain (or the same name recreated).
func flushWinbind() {
	_, _ = sys.Run("systemctl", "stop", "winbind.service")
	_, _ = sys.Run("net", "cache", "flush")
	for _, f := range []string{"winbindd_cache.tdb", "netsamlogon_cache.tdb"} {
		_ = os.Remove("/var/lib/samba/" + f)
		_ = os.Remove("/var/cache/samba/" + f)
	}
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return s
}

// ApplyLDAP configures sssd for the LDAP directory (or removes it).
func ApplyLDAP(c store.Directory, on bool) error {
	if !on {
		_ = setNSS("sss", false)
		if LDAPInstalled() {
			_, _ = sys.Run("systemctl", "disable", "--now", "sssd.service")
		}
		_ = os.Remove(SSSDConf)
		return nil
	}
	if !LDAPInstalled() {
		return fmt.Errorf("LDAP support is not installed")
	}
	tls := "false"
	if c.StartTLS {
		tls = "true"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `# Managed by StrataOS
[sssd]
services = nss, pam
domains = strata

[domain/strata]
id_provider = ldap
auth_provider = ldap
ldap_uri = %s
ldap_search_base = %s
ldap_id_use_start_tls = %s
ldap_tls_reqcert = allow
cache_credentials = true
enumerate = true
`, c.URI, c.BaseDN, tls)
	if c.BindDN != "" {
		fmt.Fprintf(&b, "ldap_default_bind_dn = %s\nldap_default_authtok_type = password\nldap_default_authtok = %s\n", c.BindDN, c.BindPassword)
	}
	_ = os.MkdirAll("/etc/sssd", 0o711)
	if err := sys.WriteFileAtomic(SSSDConf, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := setNSS("sss", true); err != nil {
		return err
	}
	_, _ = sys.Run("systemctl", "enable", "sssd.service")
	_, _ = sys.Run("sss_cache", "-E")
	if _, err := sys.Run("systemctl", "restart", "sssd.service"); err != nil {
		return fmt.Errorf("sssd did not start: %w", err)
	}
	return nil
}

// Status describes the live state of the membership.
type Status struct {
	Connected bool   `json:"connected"`
	Message   string `json:"message"`
}

func CheckStatus(c store.Directory) Status {
	switch c.Type {
	case "ad":
		if out, err := sys.Run("wbinfo", "-t"); err != nil {
			return Status{Message: firstLine(out + " " + err.Error())}
		}
		return Status{Connected: true, Message: "Trust relationship with the domain is valid"}
	case "ldap":
		if _, err := sys.Run("systemctl", "is-active", "sssd.service"); err != nil {
			return Status{Message: "sssd is not running"}
		}
		return Status{Connected: true, Message: "LDAP directory connected"}
	}
	return Status{}
}

// Search lists domain users (or groups) whose name contains q (at most 200).
func Search(c store.Directory, groups bool, q string) []string {
	var names []string
	q = strings.ToLower(q)
	switch c.Type {
	case "ad":
		flag := "-u"
		if groups {
			flag = "-g"
		}
		out, _ := sys.Run("wbinfo", flag)
		for _, n := range strings.Split(out, "\n") {
			if n = strings.TrimSpace(n); n != "" && strings.Contains(strings.ToLower(n), q) {
				names = append(names, n)
			}
		}
	case "ldap":
		db := "passwd"
		if groups {
			db = "group"
		}
		out, _ := sys.Run("getent", "-s", "sss", db)
		for _, l := range strings.Split(out, "\n") {
			if n, _, ok := strings.Cut(l, ":"); ok && strings.Contains(strings.ToLower(n), q) {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	if len(names) > 200 {
		names = names[:200]
	}
	return names
}
