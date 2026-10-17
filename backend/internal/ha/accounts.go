package ha

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

// Account is a Linux account to recreate identically on the peer.
type Account struct {
	Name     string `json:"name"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	Gecos    string `json:"gecos"`
	Shell    string `json:"shell"`
	Shadow   string `json:"shadow"` // crypt(3) hash from /etc/shadow
	Disabled bool   `json:"disabled"`
}

// Group is a Linux group with its numeric ID and members.
type Group struct {
	Name    string   `json:"name"`
	GID     int      `json:"gid"`
	Members []string `json:"members"`
}

// Accounts is what the primary sends: identities and Samba password hashes.
type Accounts struct {
	Users  []Account `json:"users"`
	Groups []Group   `json:"groups"`
	SMB    string    `json:"smb"` // pdbedit smbpasswd export
}

func readColon(path string) map[string][]string {
	out := map[string][]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ":")
		if len(p) > 1 {
			out[p[0]] = p
		}
	}
	return out
}

// ExportAccounts collects the given users and groups from the system.
func ExportAccounts(users, groups []string) (Accounts, error) {
	pw, sh, gr := readColon("/etc/passwd"), readColon("/etc/shadow"), readColon("/etc/group")
	var a Accounts
	for _, u := range users {
		p, ok := pw[u]
		if !ok || len(p) < 7 {
			continue // directory account: nothing to copy
		}
		uid, _ := strconv.Atoi(p[2])
		gid, _ := strconv.Atoi(p[3])
		acc := Account{Name: u, UID: uid, GID: gid, Gecos: p[4], Shell: p[6]}
		if s, ok := sh[u]; ok && len(s) > 1 {
			acc.Shadow = s[1]
			acc.Disabled = strings.HasPrefix(s[1], "!")
		}
		a.Users = append(a.Users, acc)
		// The primary group of each user (same name) goes along.
		for name, g := range gr {
			if len(g) >= 3 && g[2] == p[3] && name == u {
				a.Groups = append(a.Groups, Group{Name: name, GID: gid})
			}
		}
	}
	for _, name := range groups {
		g, ok := gr[name]
		if !ok || len(g) < 4 {
			continue
		}
		gid, _ := strconv.Atoi(g[2])
		var members []string
		if g[3] != "" {
			members = strings.Split(g[3], ",")
		}
		a.Groups = append(a.Groups, Group{Name: name, GID: gid, Members: members})
	}
	if sys.Has("pdbedit") {
		tmp, err := os.CreateTemp("", "strata-smb-")
		if err == nil {
			tmp.Close()
			defer os.Remove(tmp.Name())
			if _, err := sys.Run("pdbedit", "-e", "smbpasswd:"+tmp.Name()); err == nil {
				b, _ := os.ReadFile(tmp.Name())
				// Only the accounts that are replicated.
				want := map[string]bool{}
				for _, u := range a.Users {
					want[u.Name] = true
				}
				var keep []string
				for _, l := range strings.Split(string(b), "\n") {
					if n, _, ok := strings.Cut(l, ":"); ok && want[n] {
						keep = append(keep, l)
					}
				}
				a.SMB = strings.Join(keep, "\n") + "\n"
			}
		}
	}
	return a, nil
}

var nameRe = func(n string) bool {
	if n == "" || len(n) > 32 || strings.ContainsAny(n, ":,\n\r\\ ") || strings.HasPrefix(n, "-") {
		return false
	}
	return true
}

// ImportAccounts recreates groups and users with the same IDs, password
// hashes and Samba passwords. Accounts with the same name but another ID are
// reported and left alone.
func ImportAccounts(a Accounts) []string {
	var problems []string
	pw, gr := readColon("/etc/passwd"), readColon("/etc/group")
	for _, g := range a.Groups {
		if !nameRe(g.Name) || g.GID < 1000 {
			continue
		}
		if cur, ok := gr[g.Name]; ok {
			if cur[2] != strconv.Itoa(g.GID) {
				problems = append(problems, fmt.Sprintf("group %s has GID %s here, %d on the primary", g.Name, cur[2], g.GID))
			}
			continue
		}
		if _, err := sys.Run("groupadd", "-g", strconv.Itoa(g.GID), g.Name); err != nil {
			problems = append(problems, "group "+g.Name+": "+err.Error())
		}
	}
	for _, u := range a.Users {
		if !nameRe(u.Name) || u.UID < 1000 || u.Name == "root" {
			continue
		}
		shell := "/usr/sbin/nologin"
		if u.Shell == "/bin/bash" {
			shell = "/bin/bash"
		}
		gecos := strings.NewReplacer(":", "", "\n", "", ",", "").Replace(u.Gecos)
		if cur, ok := pw[u.Name]; ok {
			if cur[2] != strconv.Itoa(u.UID) {
				problems = append(problems, fmt.Sprintf("user %s has UID %s here, %d on the primary", u.Name, cur[2], u.UID))
				continue
			}
			_, _ = sys.Run("usermod", "-c", gecos, "-s", shell, u.Name)
		} else if _, err := sys.Run("useradd", "-M", "-u", strconv.Itoa(u.UID), "-g", strconv.Itoa(u.GID), "-s", shell, "-c", gecos, u.Name); err != nil {
			problems = append(problems, "user "+u.Name+": "+err.Error())
			continue
		}
		if u.Shadow != "" && !strings.ContainsAny(u.Shadow, ":\n") {
			// chpasswd -e reads "name:hash" on stdin: the hash never shows in ps.
			if _, err := sys.RunInput(u.Name+":"+u.Shadow+"\n", "chpasswd", "-e"); err != nil {
				problems = append(problems, "password of "+u.Name+": "+err.Error())
			}
		}
	}
	for _, g := range a.Groups {
		if nameRe(g.Name) && g.GID >= 1000 && len(g.Members) > 0 {
			_, _ = sys.Run("gpasswd", "-M", strings.Join(g.Members, ","), g.Name)
		}
	}
	if a.SMB != "" && sys.Has("pdbedit") {
		tmp, err := os.CreateTemp("", "strata-smb-")
		if err == nil {
			_ = tmp.Chmod(0o600)
			_, _ = tmp.WriteString(a.SMB)
			tmp.Close()
			defer os.Remove(tmp.Name())
			if out, err := sys.Run("pdbedit", "-i", "smbpasswd:"+tmp.Name()); err != nil {
				problems = append(problems, "Samba passwords: "+lastLine(out+" "+err.Error()))
			}
		}
	}
	return problems
}
