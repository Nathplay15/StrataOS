package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/user"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/backup"
	"strataos/internal/cloudsync"
	"strataos/internal/confbackup"
	"strataos/internal/crypt"
	"strataos/internal/docker"
	"strataos/internal/jobs"
	"strataos/internal/scheduler"
	"strataos/internal/shares"
	"strataos/internal/storage"
	"strataos/internal/store"
	"strataos/internal/sys"
	"strataos/internal/system"
	"strataos/internal/vpn"
)

// importer applies a planned import, item by item: a failing item is logged
// and the others go on. Nothing that is not in the file is ever removed.
type importer struct {
	s      *Server
	ctx    context.Context
	j      *jobs.Job
	p      *importPlan
	rp     *replayer
	self   string
	done   int
	failed int
	// what to refresh at the end
	shares, files bool
}

func (im *importer) ok(section string, it confbackup.Item) {
	im.done++
	im.j.Logf("%s: %s %s", section, it.Action, it.Name)
}

func (im *importer) fail(section string, it confbackup.Item, err error) {
	im.failed++
	im.j.Logf("%s: %s failed: %v", section, it.Name, err)
}

// each runs fn on the items of a section to apply.
func (im *importer) each(section string, fn func(it confbackup.Item) error) {
	for _, sec := range im.p.sections {
		if sec.ID != section {
			continue
		}
		for _, it := range sec.Items {
			if im.ctx.Err() != nil {
				return
			}
			if it.Action == confbackup.Skip {
				im.j.Logf("%s: %s skipped (%s)", section, it.Name, it.Detail)
			}
			if !applicable(it) {
				continue
			}
			if err := fn(it); err != nil {
				im.fail(section, it, err)
			} else {
				im.ok(section, it)
			}
		}
	}
}

func (s *Server) applyImport(ctx context.Context, j *jobs.Job, p *importPlan, sections []string, self, ip string) error {
	im := &importer{s: s, ctx: ctx, j: j, p: p, rp: s.newReplayer(self, ip), self: self}
	defer im.rp.close()
	f := p.full
	if p.secrets == nil {
		j.Logf("Secrets not imported: accounts are created disabled, passwords and keys must be entered again")
	}
	steps := map[string]func(){
		"system":       im.system,
		"settings":     im.settings,
		"groups":       im.groups,
		"users":        im.users,
		"quotas":       im.quotas,
		"shares":       im.sharesSection,
		"schedules":    im.schedules,
		"backups":      im.backups,
		"cloudSyncs":   im.clouds,
		"stacks":       im.stacks,
		"fileServices": func() { im.put("fileServices", "/api/fileservices", f.FileServices) },
		"ransomware":   func() { im.put("ransomware", "/api/ransomware", f.Ransomware) },
		"vpn":          im.vpn,
		"ddns":         func() { im.put("ddns", "/api/ddns", f.DDNS) },
		"energy":       func() { im.put("energy", "/api/energy", f.Energy) },
		"guard":        im.guard,
		"ups":          func() { im.put("ups", "/api/ups", f.UPS) },
		"photos":       im.photos,
		"vaults":       im.vaults,
		"fleet":        im.fleet,
		"configBackup": im.configBackup,
	}
	for i, id := range confbackup.Sections {
		if !slices.Contains(sections, id) || ctx.Err() != nil {
			continue
		}
		j.SetProgress(float64(i) * 100 / float64(len(confbackup.Sections)))
		if id == "network" {
			// Applied on trial from the browser, which confirms it when it
			// still reaches the NAS (see the import dialog).
			continue
		}
		if fn := steps[id]; fn != nil {
			fn()
		}
	}
	// Members once the accounts exist.
	if slices.Contains(sections, "groups") && ctx.Err() == nil {
		im.groupMembers()
	}
	if im.shares {
		_ = s.applyShares()
	}
	if im.files {
		go s.applyFileServices()
	}
	msg := fmt.Sprintf("Configuration imported from %s: %d item(s) applied, %d failed", p.doc.Hostname, im.done, im.failed)
	j.Logf("%s", msg)
	level := "info"
	if im.failed > 0 {
		level = "warning"
	}
	s.store.Log(level, "config", self, msg)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if im.failed > 0 {
		return fmt.Errorf("%d item(s) could not be imported (see the log)", im.failed)
	}
	return nil
}

// put replays the page saving a single-object section.
func (im *importer) put(section, path string, v any) {
	im.each(section, func(it confbackup.Item) error {
		_, err := im.rp.call("PUT", path, v)
		return err
	})
}

func (im *importer) system() {
	sys := im.p.full.System
	im.each("system", func(it confbackup.Item) error {
		if it.Key == "hostname" {
			return system.SetHostname(sys.Hostname)
		}
		return system.SetTimezone(sys.Timezone)
	})
}

func (im *importer) settings() {
	set := *im.p.full.Settings
	im.each("settings", func(it confbackup.Item) error {
		if _, err := im.rp.call("PUT", "/api/settings", set); err != nil {
			return err
		}
		// The metrics token is not saved by the settings page.
		if tok := set.MetricsToken; tok != "" && tok != confbackup.Masked {
			return im.s.store.Update(func(st *store.State) error { st.Settings.MetricsToken = tok; return nil })
		}
		return nil
	})
}

// gidFree / uidFree: the IDs of the exported NAS are kept when possible, so
// the files of moved disks keep their owner.
func gidFree(id int) bool { _, err := user.LookupGroupId(strconv.Itoa(id)); return err != nil }
func uidFree(id int) bool { _, err := user.LookupId(strconv.Itoa(id)); return err != nil }

func (im *importer) groups() {
	im.each("groups", func(it confbackup.Item) error {
		if it.Action != confbackup.Create {
			return nil // members are added after the users
		}
		i := slices.IndexFunc(im.p.full.Groups, func(g confbackup.Group) bool { return g.Name == it.Key })
		g := im.p.full.Groups[i]
		if err := accounts.ValidName(g.Name); err != nil {
			return err
		}
		if g.GID >= 1000 && g.GID < 60000 && gidFree(g.GID) {
			_, err := sys.Run("groupadd", "-g", strconv.Itoa(g.GID), g.Name)
			return err
		}
		return accounts.CreateGroup(g.Name)
	})
}

// groupMembers adds the members of the file to the groups (never removes any).
func (im *importer) groupMembers() {
	if !slices.ContainsFunc(im.p.sections, func(s confbackup.Section) bool { return s.ID == "groups" }) {
		return
	}
	cur := map[string][]string{}
	if gl, err := accounts.ListGroups(); err == nil {
		for _, g := range gl {
			cur[g.Name] = g.Members
		}
	}
	for _, g := range im.p.full.Groups {
		it := findItem(im.p, "groups", g.Name)
		have, exists := cur[g.Name]
		if it == nil || !applicable(*it) || !exists {
			continue
		}
		members := slices.Clone(have)
		for _, m := range g.Members {
			if accounts.LinuxUserExists(m) && !slices.Contains(members, m) {
				members = append(members, m)
			}
		}
		if len(members) == len(have) {
			continue
		}
		if err := accounts.SetGroupMembers(g.Name, members); err != nil {
			im.j.Logf("groups: members of %s: %v", g.Name, err)
		}
		im.shares = true
	}
}

var (
	bcryptRe = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)
	shadowRe = regexp.MustCompile(`^!?\$(y|gy|7|2[aby]|5|6|1)\$[./A-Za-z0-9$=,+-]{8,300}$`)
	smbRe    = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}):[0-9]+:[0-9A-FX]{32}:[0-9A-FX]{32}:\[[A-Z ]{11}\]:LCT-[0-9A-F]{8}:$`)
)

func gecos(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ':' || r == ',' || r == '\n' || r == '=' {
			return -1
		}
		return r
	}, s)
}

func randomPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (im *importer) users() {
	for _, it := range im.findSection("users") {
		if it.Action == confbackup.Skip {
			im.j.Logf("users: %s skipped (%s)", it.Name, it.Detail)
		}
		if !applicable(it) || im.ctx.Err() != nil {
			continue
		}
		i := slices.IndexFunc(im.p.full.Users, func(u confbackup.User) bool { return u.Username == it.Key })
		u := im.p.full.Users[i]
		var err error
		if it.Action == confbackup.Create {
			err = im.createUser(u)
		} else {
			err = im.updateUser(u)
		}
		if err != nil {
			im.fail("users", it, err)
		} else {
			im.ok("users", it)
			im.shares, im.files = true, true
		}
	}
}

func (im *importer) findSection(id string) []confbackup.Item {
	for _, s := range im.p.sections {
		if s.ID == id {
			return s.Items
		}
	}
	return nil
}

func role(r string) string {
	if r == "admin" {
		return "admin"
	}
	return "user"
}

// userGroups sets the supplementary groups that exist here.
func (im *importer) userGroups(name string, groups []string, union bool) {
	want := []string{}
	if union {
		want = append(want, accounts.UserGroups(name)...)
	}
	for _, g := range groups {
		if slices.Contains(want, g) {
			continue
		}
		if err := accounts.ValidMemberGroup(g); err != nil {
			im.j.Logf("users: %s: group %s ignored (%v)", name, g, err)
			continue
		}
		want = append(want, g)
	}
	if err := accounts.SetUserGroups(name, want); err != nil {
		im.j.Logf("users: groups of %s: %v", name, err)
	}
}

func (im *importer) createUser(u confbackup.User) error {
	if err := accounts.ValidName(u.Username); err != nil {
		return err
	}
	rec := store.User{Username: u.Username, FullName: u.FullName, Email: u.Email, Role: role(u.Role), Disabled: u.Disabled, SSH: u.SSH, Source: u.Source, CreatedAt: time.Now()}
	if u.Source != "" {
		// Directory account: only the StrataOS profile, the identity is the directory's.
		rec.SSH = false
		return im.addUser(rec)
	}
	var sec confbackup.UserSecrets
	if im.p.secrets != nil {
		sec = im.p.secrets.Users[u.Username]
	}
	if !accounts.LinuxUserExists(u.Username) {
		accounts.EnsureBaseGroup()
		shell := "/usr/sbin/nologin"
		if u.SSH {
			shell = "/bin/bash"
		}
		args := []string{"-m", "-s", shell, "-c", gecos(u.FullName), "-G", accounts.BaseGroup}
		if u.UID >= 1000 && u.UID < 60000 && uidFree(u.UID) {
			args = append(args, "-u", strconv.Itoa(u.UID))
		} else if u.UID != 0 {
			im.j.Logf("users: UID %d of %s is taken here: files on moved disks may need a new owner", u.UID, u.Username)
		}
		if u.GID >= 1000 && u.GID < 60000 && gidFree(u.GID) {
			if _, err := sys.Run("groupadd", "-g", strconv.Itoa(u.GID), u.Username); err == nil {
				args = append(args, "-g", u.Username)
			}
		}
		if _, err := sys.Run("useradd", append(args, u.Username)...); err != nil {
			return err
		}
	}
	im.userGroups(u.Username, u.Groups, false)

	restored := sec.Shadow != "" && shadowRe.MatchString(sec.Shadow) && bcryptRe.MatchString(sec.PasswordHash)
	if restored {
		// chpasswd -e reads "name:hash" on stdin: the hash never shows in ps.
		if _, err := sys.RunInput(u.Username+":"+sec.Shadow+"\n", "chpasswd", "-e"); err != nil {
			return err
		}
		if m := smbRe.FindStringSubmatch(sec.SMB); m != nil && m[1] == u.Username && sys.Has("pdbedit") {
			if err := importSMB(sec.SMB); err != nil {
				im.j.Logf("users: Samba password of %s: %v", u.Username, err)
			}
		} else {
			im.j.Logf("users: %s has no Samba password in the file: set a password to use SMB", u.Username)
		}
		rec.PasswordHash = sec.PasswordHash
		if sec.TOTPSecret != "" {
			rec.TOTPSecret, rec.RecoveryCodes = sec.TOTPSecret, sec.RecoveryCodes
		}
	} else {
		// No password in the file: an unknown random one, then the account is
		// disabled everywhere (sign-in, SMB, SSH) until an administrator sets
		// a password and enables it in Users.
		if err := accounts.SetPassword(u.Username, randomPassword()); err != nil {
			return err
		}
		rec.Disabled = true
	}
	if rec.Disabled {
		if err := accounts.SetEnabled(u.Username, false); err != nil {
			return err
		}
	}
	return im.addUser(rec)
}

func (im *importer) addUser(rec store.User) error {
	return im.s.store.Update(func(st *store.State) error {
		if st.User(rec.Username) != nil {
			return fmt.Errorf("the account already exists")
		}
		st.Users = append(st.Users, &rec)
		return nil
	})
}

func (im *importer) updateUser(u confbackup.User) error {
	var prev store.User
	err := im.s.store.Update(func(st *store.State) error {
		x := st.User(u.Username)
		if x == nil {
			return notFound("user not found")
		}
		prev = *x
		x.FullName, x.Email, x.Role = u.FullName, u.Email, role(u.Role)
		if x.Source == "" {
			x.SSH = u.SSH
		}
		return nil
	})
	if err != nil {
		return err
	}
	if prev.Role != role(u.Role) {
		im.s.sessions.dropUser(u.Username)
	}
	if prev.Source != "" {
		return nil
	}
	if err := accounts.Update(u.Username, u.FullName, u.SSH); err != nil {
		return err
	}
	im.userGroups(u.Username, u.Groups, true)
	return nil
}

// importSMB loads one smbpasswd line (the NT hash) into the Samba database.
func importSMB(line string) error {
	tmp, err := os.CreateTemp("", "strata-smb-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o600)
	_, _ = tmp.WriteString(line + "\n")
	tmp.Close()
	_, err = sys.Run("pdbedit", "-i", "smbpasswd:"+tmp.Name())
	return err
}

func (im *importer) quotas() {
	targets := im.s.quotaTargets()
	im.each("quotas", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Quotas, func(q confbackup.Quota) bool { return q.User+"@"+q.MountPoint == it.Key })
		q := im.p.full.Quotas[i]
		if !accounts.LinuxUserExists(q.User) {
			return fmt.Errorf("account %s not found", q.User)
		}
		for _, t := range targets {
			if t.MountPoint == q.MountPoint && t.Supported && t.Enabled {
				return storage.SetUserQuota(t, q.User, q.Limit)
			}
		}
		return fmt.Errorf("per-user quotas are not enabled on %s", q.MountPoint)
	})
}

func (im *importer) sharesSection() {
	im.each("shares", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Shares, func(x store.Share) bool { return x.Name == it.Key })
		sh := im.p.full.Shares[i]
		if sh.Encrypted && it.Action == confbackup.Create {
			return im.encryptedShare(sh)
		}
		var err error
		if it.Action == confbackup.Create {
			_, err = im.rp.call("POST", "/api/shares", sh)
		} else {
			_, err = im.rp.call("PUT", "/api/shares/"+urlPathEscape(it.Target), sh)
		}
		return err
	})
}

func urlPathEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "%", "%25"), " ", "%20")
}

// encryptedShare puts back an encrypted share whose encrypted folder came with
// the disks: it stays locked until its passphrase is entered.
func (im *importer) encryptedShare(sh store.Share) error {
	if !shareNameRe.MatchString(sh.Name) || reservedShares[strings.ToLower(sh.Name)] {
		return fmt.Errorf("invalid share name")
	}
	p, err := shares.ValidatePath(sh.Path)
	if err != nil {
		return err
	}
	want, err := crypt.CipherDir(storage.MountOf(p), p)
	if err != nil || want != sh.CipherDir || !im.s.pathOnVolume(p) {
		return fmt.Errorf("the encrypted folder of %s does not match its share", sh.Name)
	}
	if fi, err := os.Lstat(want + "/gocryptfs.conf"); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("encrypted data not found: %s", want)
	}
	if sh.Permissions == nil {
		sh.Permissions = map[string]string{}
	}
	if err := validPerms(sh.Permissions); err != nil {
		return err
	}
	if err := shares.ValidateNFS(sh.NFSClients, sh.NFSOptions); err != nil {
		return err
	}
	sh.Path, sh.NFS, sh.Locked, sh.QuotaFull, sh.Frozen, sh.Used = p, false, false, false, false, 0
	sh.CreatedAt = time.Now()
	im.shares = true
	return im.s.store.Update(func(st *store.State) error {
		if err := shareConflict(st, sh.Name, true); err != nil {
			return err
		}
		st.Shares = append(st.Shares, &sh)
		return nil
	})
}

// newID keeps the ID of the file unless it is taken here.
func newID(id string, taken func(string) bool) string {
	if id == "" || len(id) > 32 || strings.ContainsAny(id, "/ \n") || taken(id) {
		return shortID()
	}
	return id
}

func (im *importer) schedules() {
	im.each("schedules", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Schedules, func(x store.Schedule) bool { return x.ID == it.Key })
		sch := im.p.full.Schedules[i]
		if err := scheduler.Validate(&sch, im.s.resolveVolume); err != nil {
			return err
		}
		return im.s.store.Update(func(st *store.State) error {
			if it.Action == confbackup.Update {
				x := st.Schedule(it.Target)
				if x == nil {
					return notFound("task not found")
				}
				sch.ID, sch.CreatedAt, sch.History = x.ID, x.CreatedAt, x.History
				*x = sch
				return nil
			}
			sch.ID = newID(sch.ID, func(id string) bool { return st.Schedule(id) != nil })
			sch.CreatedAt, sch.History = time.Now(), []store.ScheduleRun{}
			st.Schedules = append(st.Schedules, &sch)
			return nil
		})
	})
}

func (im *importer) backups() {
	im.each("backups", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Backups, func(x store.BackupTask) bool { return x.ID == it.Key })
		b := im.p.full.Backups[i]
		b.Sources, b.Excludes = slices.Clone(b.Sources), slices.Clone(b.Excludes)
		if it.Action == confbackup.Update {
			im.s.unmaskBackup(&b, it.Target) // secrets not in the file: keep the current ones
		}
		missing := false
		for _, p := range []*string{&b.Password, &b.SecretKey, &b.VaultToken} {
			if *p == masked {
				*p, missing = "", true
			}
		}
		if missing {
			b.Enabled = false
		}
		if err := backup.Validate(&b); err != nil {
			return err
		}
		if b.Cron != "" {
			if _, err := scheduler.ParseCron(b.Cron); err != nil {
				return err
			}
		}
		return im.s.store.Update(func(st *store.State) error {
			if it.Action == confbackup.Update {
				x := st.Backup(it.Target)
				if x == nil {
					return notFound("backup not found")
				}
				b.ID, b.CreatedAt, b.History = x.ID, x.CreatedAt, x.History
				*x = b
				return nil
			}
			b.ID = newID(b.ID, func(id string) bool { return st.Backup(id) != nil })
			b.CreatedAt, b.History = time.Now(), []store.ScheduleRun{}
			st.Backups = append(st.Backups, &b)
			return nil
		})
	})
}

func (im *importer) clouds() {
	im.each("cloudSyncs", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.CloudSyncs, func(x store.CloudSync) bool { return x.ID == it.Key })
		c := im.p.full.CloudSyncs[i]
		c.Params, c.Secrets = maps(c.Params), maps(c.Secrets)
		if it.Action == confbackup.Update {
			im.s.unmaskCloud(&c, it.Target)
		}
		for k, v := range c.Secrets {
			if v == masked {
				delete(c.Secrets, k)
			}
		}
		if err := cloudsync.Validate(&c); err != nil {
			return err
		}
		if c.Cron != "" {
			if _, err := scheduler.ParseCron(c.Cron); err != nil {
				return err
			}
		}
		return im.s.store.Update(func(st *store.State) error {
			c.Synced = false
			if it.Action == confbackup.Update {
				x := st.Cloud(it.Target)
				if x == nil {
					return notFound("cloud sync not found")
				}
				c.ID, c.CreatedAt, c.History = x.ID, x.CreatedAt, x.History
				c.Synced = x.Synced && x.LocalPath == c.LocalPath && x.RemotePath == c.RemotePath && x.Provider == c.Provider && x.Direction == c.Direction
				*x = c
				return nil
			}
			c.ID = newID(c.ID, func(id string) bool { return st.Cloud(id) != nil })
			c.CreatedAt, c.History = time.Now(), []store.ScheduleRun{}
			st.Clouds = append(st.Clouds, &c)
			return nil
		})
	})
}

func (im *importer) stacks() {
	im.each("stacks", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Stacks, func(x confbackup.Stack) bool { return x.Name == it.Key })
		st := im.p.full.Stacks[i]
		if err := docker.ValidStack(st.Name); err != nil {
			return err
		}
		cur, _ := os.ReadFile(docker.StacksDir + "/" + st.Name + "/.env")
		env := confbackup.MergeEnv(st.Env, string(cur))
		app := st.App
		if app != "" && docker.FindApp(app) == nil {
			app = ""
		}
		// Saved only: the containers are created when the stack is deployed
		// (their data folders must be there first).
		return docker.SaveStack(im.ctx, st.Name, st.Compose, env, app)
	})
}

func (im *importer) vpn() {
	f := im.p.full.VPN
	im.each("vpn", func(it confbackup.Item) error {
		if it.Key == "settings" {
			// The server key comes back when no device of this NAS relies on its own key.
			if vpn.ValidKey(f.PrivateKey) && vpn.ValidKey(f.PublicKey) {
				err := im.s.store.Update(func(st *store.State) error {
					if len(st.VPN.Peers) == 0 {
						st.VPN.PrivateKey, st.VPN.PublicKey = f.PrivateKey, f.PublicKey
					}
					return nil
				})
				if err != nil {
					return err
				}
			}
			_, err := im.rp.call("PUT", "/api/vpn", f)
			return err
		}
		i := slices.IndexFunc(f.Peers, func(p store.VPNPeer) bool { return p.ID == it.Key })
		p := f.Peers[i]
		var c store.VPN
		err := im.s.store.Update(func(st *store.State) error {
			if it.Action == confbackup.Update {
				for k := range st.VPN.Peers {
					x := &st.VPN.Peers[k]
					if x.PublicKey == p.PublicKey || x.ID == p.ID {
						if p.Mode == "nas" || p.Mode == "lan" || p.Mode == "all" {
							x.Mode = p.Mode
						}
						x.Disabled = p.Disabled
					}
				}
			} else {
				if st.VPN.PrivateKey == "" {
					return fmt.Errorf("save the VPN settings first")
				}
				p.Name = strings.TrimSpace(p.Name)
				if err := vpn.ValidatePeer(st.VPN, p); err != nil {
					return err
				}
				if p.CreatedAt.IsZero() {
					p.CreatedAt = time.Now()
				}
				st.VPN.Peers = append(st.VPN.Peers, p)
			}
			c = st.VPN
			return nil
		})
		if err != nil {
			return err
		}
		return vpn.Apply(c)
	})
}

func (im *importer) guard() {
	g := im.p.full.Guard
	im.each("guard", func(it confbackup.Item) error {
		if it.Key == "settings" {
			_, err := im.rp.call("PUT", "/api/guard", g.Config)
			return err
		}
		for _, b := range g.Blocks {
			if b.IP == it.Key {
				min := int(math.Ceil(time.Until(b.Until).Minutes()))
				if min < 1 {
					return nil
				}
				_, err := im.rp.call("POST", "/api/guard/block", map[string]any{"ip": b.IP, "minutes": min})
				return err
			}
		}
		return nil
	})
}

func (im *importer) photos() {
	var add []string
	for _, it := range im.findSection("photos") {
		if it.Action == confbackup.Create {
			add = append(add, it.Key)
		}
	}
	if len(add) == 0 {
		return
	}
	var folders []string
	im.s.store.Read(func(st *store.State) { folders = slices.Clone(st.Photos.Folders) })
	folders = append(folders, add...)
	im.each("photos", func(it confbackup.Item) error { return nil })
	if _, err := im.rp.call("PUT", "/api/photos/settings", map[string]any{"folders": folders}); err != nil {
		im.failed++
		im.j.Logf("photos: %v", err)
	}
}

var (
	certRe     = regexp.MustCompile(`^[0-9a-f]{64}$`) // SHA-256 of a certificate
	hexHashRe  = certRe                               // SHA-256 of an access token
	clientIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
)

func (im *importer) vaults() {
	im.each("vaults", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Vaults, func(v store.Vault) bool { return v.ID == it.Key })
		v := im.p.full.Vaults[i]
		name := strings.TrimSpace(v.Name)
		if name == "" || len(name) > 60 {
			return fmt.Errorf("invalid name")
		}
		if it.Action == confbackup.Update {
			return im.s.store.Update(func(st *store.State) error {
				for _, x := range st.Vaults {
					if x.ID == it.Target {
						x.Name, x.AppendOnly = name, v.AppendOnly
					}
				}
				return nil
			})
		}
		p, err := shares.ValidatePath(v.Path)
		if err != nil || !im.s.pathOnVolume(p) {
			return fmt.Errorf("the vault folder must be inside a mounted volume")
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
		_ = os.Chmod(p, 0o700)
		nv := &store.Vault{Name: name, Path: p, AppendOnly: v.AppendOnly, Created: time.Now(), Clients: []store.VaultClient{}}
		// The NAS that backed up here keep their access when the secrets came along.
		for _, c := range v.Clients {
			if hexHashRe.MatchString(c.TokenHash) && clientIDRe.MatchString(c.ID) {
				nv.Clients = append(nv.Clients, store.VaultClient{ID: c.ID, Name: c.Name, TokenHash: c.TokenHash, Created: c.Created})
			}
		}
		return im.s.store.Update(func(st *store.State) error {
			nv.ID = newID(v.ID, func(id string) bool {
				return slices.ContainsFunc(st.Vaults, func(x *store.Vault) bool { return x.ID == id })
			})
			st.Vaults = append(st.Vaults, nv)
			return nil
		})
	})
}

func (im *importer) fleet() {
	im.each("fleet", func(it confbackup.Item) error {
		i := slices.IndexFunc(im.p.full.Fleet, func(m store.FleetMember) bool { return m.ID == it.Key })
		m := im.p.full.Fleet[i]
		host := strings.TrimSpace(m.Host)
		if host == "" || strings.ContainsAny(host, "/ @?#\n") || !certRe.MatchString(m.Cert) || m.Token == "" || m.Token == masked || strings.ContainsAny(m.Token, "\n\r ") {
			return fmt.Errorf("invalid NAS")
		}
		return im.s.store.Update(func(st *store.State) error {
			if len(st.Fleet.Members) >= 200 {
				return fmt.Errorf("too many NAS")
			}
			m.ID = newID(m.ID, func(id string) bool {
				return slices.ContainsFunc(st.Fleet.Members, func(x store.FleetMember) bool { return x.ID == id })
			})
			m.Host, m.Added = host, time.Now()
			st.Fleet.Members = append(st.Fleet.Members, m)
			return nil
		})
	})
}

func (im *importer) configBackup() {
	im.each("configBackup", func(it confbackup.Item) error {
		cb := *im.p.full.ConfigBackup
		if cb.Passphrase == masked {
			im.s.store.Read(func(st *store.State) { cb.Passphrase = st.ConfigBackup.Passphrase })
		}
		return im.s.saveConfigAuto(cb)
	})
}
