// Package accounts manages the Linux and Samba accounts behind StrataOS users and groups.
package accounts

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

// BaseGroup is the Linux group every StrataOS user belongs to.
const BaseGroup = "strata"

var nameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

var reserved = map[string]bool{
	"root": true, "daemon": true, "bin": true, "sys": true, "sync": true, "games": true, "man": true, "lp": true,
	"mail": true, "news": true, "uucp": true, "proxy": true, "www-data": true, "backup": true, "list": true,
	"irc": true, "nobody": true, "nogroup": true, "systemd-network": true, "systemd-resolve": true, "messagebus": true,
	"sshd": true, "docker": true, "admin": false, "sudo": true, "adm": true, "staff": true, "users": false,
	"strata": true, "disk": true, "shadow": true, "kvm": true, "tty": true, "_apt": true, "polkitd": true,
}

func ValidName(n string) error {
	if !nameRe.MatchString(n) {
		return fmt.Errorf("invalid name %q (lowercase letters, digits, - and _)", n)
	}
	if reserved[n] {
		return fmt.Errorf("%q is a reserved system name", n)
	}
	return nil
}

func ValidPassword(p string) error {
	if len(p) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if strings.ContainsAny(p, "\n\r:") {
		return fmt.Errorf("password contains forbidden characters")
	}
	return nil
}

// ValidMemberGroup accepts the supplementary groups StrataOS manages: groups
// created from the interface, "users", and "docker" (Docker access option).
// Privileged system groups (sudo, disk, shadow, adm...) are never granted.
func ValidMemberGroup(g string) error {
	if g == "users" || g == "docker" {
		return nil
	}
	if err := ValidName(g); err != nil {
		return err
	}
	if _, err := user.LookupGroup(g); err != nil {
		return fmt.Errorf("group %s does not exist", g)
	}
	return nil
}

// EnsureBaseGroup creates the strata group when missing.
func EnsureBaseGroup() {
	if _, err := user.LookupGroup(BaseGroup); err != nil {
		_, _ = sys.Run("groupadd", "--system", BaseGroup)
	}
}

func shell(ssh bool) string {
	if ssh {
		return "/bin/bash"
	}
	return "/usr/sbin/nologin"
}

// LinuxUserExists reports whether a Linux account exists.
func LinuxUserExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}

// Create creates (or adopts) the Linux account and its Samba password.
func Create(name, fullName, password string, groups []string, ssh bool) error {
	EnsureBaseGroup()
	gs := append([]string{BaseGroup}, groups...)
	for _, g := range groups {
		if err := ValidMemberGroup(g); err != nil {
			return err
		}
	}
	if LinuxUserExists(name) {
		if _, err := sys.Run("usermod", "-a", "-G", strings.Join(gs, ","), name); err != nil {
			return err
		}
	} else {
		args := []string{"-m", "-s", shell(ssh), "-G", strings.Join(gs, ","), "-c", sanitizeGecos(fullName), name}
		if _, err := sys.Run("useradd", args...); err != nil {
			return err
		}
	}
	return SetPassword(name, password)
}

func sanitizeGecos(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ':' || r == ',' || r == '\n' || r == '=' {
			return -1
		}
		return r
	}, s)
}

func SetPassword(name, password string) error {
	if _, err := sys.RunInput(name+":"+password+"\n", "chpasswd"); err != nil {
		return err
	}
	if sys.Has("smbpasswd") {
		if _, err := sys.RunInput(password+"\n"+password+"\n", "smbpasswd", "-s", "-a", name); err != nil {
			return err
		}
	}
	return nil
}

func Update(name, fullName string, ssh bool) error {
	_, err := sys.Run("usermod", "-c", sanitizeGecos(fullName), "-s", shell(ssh), name)
	return err
}

func SetEnabled(name string, enabled bool) error {
	if enabled {
		if _, err := sys.Run("usermod", "-U", name); err != nil {
			return err
		}
		if sys.Has("smbpasswd") {
			_, _ = sys.Run("smbpasswd", "-e", name)
		}
		return nil
	}
	if _, err := sys.Run("usermod", "-L", name); err != nil {
		return err
	}
	if sys.Has("smbpasswd") {
		_, _ = sys.Run("smbpasswd", "-d", name)
	}
	return nil
}

func Delete(name string, removeHome bool) error {
	if sys.Has("smbpasswd") {
		_, _ = sys.Run("smbpasswd", "-x", name)
	}
	_, _ = sys.Run("pkill", "-KILL", "-u", name)
	args := []string{}
	if removeHome {
		args = append(args, "-r")
	}
	_, err := sys.Run("userdel", append(args, name)...)
	return err
}

// UserGroups returns the supplementary groups of a user (excluding the base group).
func UserGroups(name string) []string {
	u, err := user.Lookup(name)
	if err != nil {
		// Directory accounts (winbind, sssd): the static binary's os/user only
		// reads /etc/passwd and /etc/group, ask NSS through id(1).
		return nssGroups(name)
	}
	ids, _ := u.GroupIds()
	out := []string{}
	for _, id := range ids {
		g, err := user.LookupGroupId(id)
		if err != nil || g.Name == BaseGroup || g.Gid == u.Gid {
			continue
		}
		out = append(out, g.Name)
	}
	sort.Strings(out)
	return out
}

func nssGroups(name string) []string {
	out := []string{}
	if strings.HasPrefix(name, "-") {
		return out
	}
	// id -Gn separates names with spaces, which domain group names contain:
	// resolve the numeric IDs one by one instead.
	ids, err := sys.Run("id", "-G", name)
	if err != nil {
		return out
	}
	for _, id := range strings.Fields(ids) {
		line, err := sys.Run("getent", "group", id)
		if err != nil {
			continue
		}
		if n, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && n != BaseGroup {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// SetUserGroups replaces the supplementary groups of a user.
func SetUserGroups(name string, groups []string) error {
	gs := []string{BaseGroup}
	// Memberships the account already has (e.g. sudo on an adopted Debian user)
	// may be kept; only new privileged ones are refused.
	current := map[string]bool{}
	for _, g := range UserGroups(name) {
		current[g] = true
	}
	for _, g := range groups {
		if !current[g] {
			if err := ValidMemberGroup(g); err != nil {
				return err
			}
		}
		gs = append(gs, g)
	}
	// Keep docker membership when an admin set it.
	_, err := sys.Run("usermod", "-G", strings.Join(gs, ","), name)
	return err
}

type Group struct {
	Name    string   `json:"name"`
	GID     int      `json:"gid"`
	Members []string `json:"members"`
	System  bool     `json:"system"`
}

// ListGroups returns user groups (gid >= 1000 and "users") from /etc/group.
func ListGroups() ([]Group, error) {
	f, err := os.Open("/etc/group")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	primary := map[int]bool{}
	if pf, err := os.Open("/etc/passwd"); err == nil {
		sc := bufio.NewScanner(pf)
		for sc.Scan() {
			p := strings.Split(sc.Text(), ":")
			if len(p) > 3 {
				gid, _ := strconv.Atoi(p[3])
				primary[gid] = true
			}
		}
		pf.Close()
	}
	var out []Group
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ":")
		if len(p) < 4 {
			continue
		}
		gid, _ := strconv.Atoi(p[2])
		if (gid < 1000 || gid >= 60000) && p[0] != "users" {
			continue
		}
		if primary[gid] && p[3] == "" {
			continue // per-user private group
		}
		g := Group{Name: p[0], GID: gid, Members: []string{}, System: p[0] == "users"}
		if p[3] != "" {
			g.Members = strings.Split(p[3], ",")
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func CreateGroup(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	_, err := sys.Run("groupadd", name)
	return err
}

func DeleteGroup(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	_, err := sys.Run("groupdel", name)
	return err
}

func SetGroupMembers(name string, members []string) error {
	if err := ValidName(name); err != nil && name != "users" {
		return err
	}
	for _, m := range members {
		if !nameRe.MatchString(m) {
			return fmt.Errorf("invalid member name %q", m)
		}
	}
	_, err := sys.Run("gpasswd", "-M", strings.Join(members, ","), name)
	return err
}

// IDs returns the uid and primary gid of a user.
func IDs(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		if strings.HasPrefix(name, "-") {
			return 0, 0, err
		}
		line, gerr := sys.Run("getent", "passwd", name)
		f := strings.Split(strings.TrimSpace(line), ":")
		if gerr != nil || len(f) < 4 {
			return 0, 0, err
		}
		uid, _ := strconv.Atoi(f[2])
		gid, _ := strconv.Atoi(f[3])
		return uid, gid, nil
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid, nil
}
