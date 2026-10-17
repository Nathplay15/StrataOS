package directory

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"strataos/internal/store"
	"strataos/internal/sys"
)

// ErrBadCredentials is returned for a wrong account or password.
var ErrBadCredentials = fmt.Errorf("invalid credentials")

var loginRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,63}$`)

// Canonical turns what the user typed (marie, CORP\marie, marie@corp.example.com)
// into the account name known to the system, without contacting the directory.
func Canonical(c store.Directory, login string) (string, bool) {
	login = strings.TrimSpace(login)
	switch c.Type {
	case "ad":
		user := login
		if d, u, ok := strings.Cut(login, `\`); ok {
			if !strings.EqualFold(d, c.Workgroup) {
				return "", false
			}
			user = u
		} else if u, d, ok := strings.Cut(login, "@"); ok {
			if !strings.EqualFold(d, c.Realm) {
				return "", false
			}
			user = u
		}
		if !loginRe.MatchString(user) {
			return "", false
		}
		return c.Workgroup + `\` + strings.ToLower(user), true
	case "ldap":
		if !loginRe.MatchString(login) || strings.Contains(login, " ") {
			return "", false
		}
		return login, true
	}
	return "", false
}

// Authenticate checks a password against the directory. name comes from
// Canonical. The password never appears on a command line.
func Authenticate(ctx context.Context, c store.Directory, name, password string) error {
	if password == "" || strings.ContainsAny(password, "\n\r") {
		return ErrBadCredentials
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch c.Type {
	case "ad":
		// ntlm_auth (winbind) in squid basic-helper mode: "user password", URL-escaped.
		in := url.PathEscape(name) + " " + url.PathEscape(password) + "\n"
		out, _ := sys.RunOpts(ctx, sys.Opts{Stdin: in}, "ntlm_auth", "--helper-protocol=squid-2.5-basic")
		if strings.HasPrefix(strings.TrimSpace(out), "OK") {
			return nil
		}
		return ErrBadCredentials
	case "ldap":
		dn, err := findDN(ctx, c, name)
		if err != nil {
			return err
		}
		pw, cleanup, err := secretFile(password)
		if err != nil {
			return err
		}
		defer cleanup()
		args := append(ldapBase(c), "-D", dn, "-y", pw)
		if _, err := sys.RunOpts(ctx, sys.Opts{Env: ldapEnv}, "ldapwhoami", args...); err != nil {
			return ErrBadCredentials
		}
		return nil
	}
	return ErrBadCredentials
}

// Same certificate policy as sssd (ldap_tls_reqcert = allow).
var ldapEnv = []string{"LDAPTLS_REQCERT=allow"}

func ldapBase(c store.Directory) []string {
	args := []string{"-x", "-H", c.URI}
	if c.StartTLS {
		args = append(args, "-ZZ")
	}
	return args
}

var filterEscape = strings.NewReplacer(`\`, `\5c`, "*", `\2a`, "(", `\28`, ")", `\29`, "\x00", `\00`)

func findDN(ctx context.Context, c store.Directory, name string) (string, error) {
	args := append(ldapBase(c), "-LLL", "-b", c.BaseDN)
	if c.BindDN != "" {
		pw, cleanup, err := secretFile(c.BindPassword)
		if err != nil {
			return "", err
		}
		defer cleanup()
		args = append(args, "-D", c.BindDN, "-y", pw)
	}
	args = append(args, "(&(objectClass=posixAccount)(uid="+filterEscape.Replace(name)+"))", "dn")
	out, err := sys.RunOpts(ctx, sys.Opts{Env: ldapEnv}, "ldapsearch", args...)
	if err != nil {
		return "", fmt.Errorf("directory unreachable")
	}
	// Long values are wrapped over several lines (continuations start with a space).
	out = strings.ReplaceAll(out, "\n ", "")
	for _, l := range strings.Split(out, "\n") {
		if dn, ok := strings.CutPrefix(l, "dn: "); ok {
			return strings.TrimSpace(dn), nil
		}
		if enc, ok := strings.CutPrefix(l, "dn:: "); ok { // non-ASCII DN, base64
			if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc)); err == nil {
				return string(b), nil
			}
		}
	}
	return "", ErrBadCredentials
}

// secretFile writes a secret without trailing newline (ldap -y reads it raw).
func secretFile(secret string) (string, func(), error) {
	f, err := os.CreateTemp("", "strata-pw-")
	if err != nil {
		return "", nil, err
	}
	_ = f.Chmod(0o600)
	_, err = f.WriteString(secret)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}

// IsAdmin reports whether the account belongs to the directory's admin group.
func IsAdmin(c store.Directory, groups []string) bool {
	if c.AdminGroup == "" {
		return false
	}
	for _, g := range groups {
		if strings.EqualFold(g, c.AdminGroup) {
			return true
		}
	}
	return false
}
