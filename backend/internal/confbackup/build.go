package confbackup

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"strataos/internal/guard"
	"strataos/internal/network"
	"strataos/internal/store"
	"strataos/internal/ups"
)

// Secrets is the content of the encrypted section.
type Secrets struct {
	Settings map[string]string            `json:"settings,omitempty"` // smtpPassword, telegramToken, ntfyToken, metricsToken
	Users    map[string]UserSecrets       `json:"users,omitempty"`
	Backups  map[string]BackupSecrets     `json:"backups,omitempty"` // by task ID
	Clouds   map[string]map[string]string `json:"clouds,omitempty"`
	Stacks   map[string]string            `json:"stacks,omitempty"` // complete .env
	VPN      *VPNSecrets                  `json:"vpn,omitempty"`
	DDNS     string                       `json:"ddns,omitempty"`
	UPS      map[string]string            `json:"ups,omitempty"` // remotePassword, sharePassword
	HA       map[string]string            `json:"ha,omitempty"`  // authPass, token
	LDAPBind string                       `json:"ldapBind,omitempty"`
	Vaults   map[string]map[string]string `json:"vaults,omitempty"` // vault ID → client ID → token hash
	Fleet    map[string]string            `json:"fleet,omitempty"`  // member ID → token
	AutoPass string                       `json:"autoPassphrase,omitempty"`
}

// UserSecrets lets an account sign in again without a new password.
type UserSecrets struct {
	PasswordHash  string   `json:"passwordHash,omitempty"` // bcrypt, web sign-in
	Shadow        string   `json:"shadow,omitempty"`       // crypt(3), /etc/shadow
	SMB           string   `json:"smb,omitempty"`          // pdbedit smbpasswd line (NT hash)
	TOTPSecret    string   `json:"totpSecret,omitempty"`
	RecoveryCodes []string `json:"recoveryCodes,omitempty"`
}

type BackupSecrets struct {
	Password   string `json:"password,omitempty"`
	SecretKey  string `json:"secretKey,omitempty"`
	VaultToken string `json:"vaultToken,omitempty"`
}

type VPNSecrets struct {
	PrivateKey string               `json:"privateKey,omitempty"`
	Peers      map[string][2]string `json:"peers,omitempty"`  // peer ID → private key, preshared key
	Links      map[string][2]string `json:"links,omitempty"`  // site link ID → private key, preshared key
	Client     map[string]string    `json:"client,omitempty"` // outgoing tunnel: privateKey, presharedKey
}

// LinuxUser is what the system knows of an account.
type LinuxUser struct {
	UID, GID int
	Groups   []string
	Shadow   string
}

// Source gathers what an export reads: a copy of the state, and what the
// system holds outside of it.
type Source struct {
	State   store.State
	System  *System
	Groups  []Group
	Linux   map[string]LinuxUser
	SMB     map[string]string // smbpasswd line per user
	Quotas  []Quota
	Pools   []Volume // ZFS pools (the volumes come from the state)
	Stacks  []Stack  // with their complete .env
	Network *network.Config
	Now     time.Time
}

// mask replaces a set secret by Masked and returns its value.
func mask(p *string) string {
	v := *p
	if v != "" {
		*p = Masked
	}
	return v
}

func put(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// secretEnvRe matches .env keys holding passwords, tokens or keys.
var secretEnvRe = regexp.MustCompile(`(?i)(pass|secret|token|key|pwd)`)

// RedactEnv masks the password-like values of an .env file.
func RedactEnv(env string) string {
	lines := strings.Split(env, "\n")
	for i, l := range lines {
		k, v, ok := strings.Cut(l, "=")
		if ok && v != "" && !strings.HasPrefix(strings.TrimSpace(k), "#") && secretEnvRe.MatchString(k) {
			lines[i] = k + "=" + Masked
		}
	}
	return strings.Join(lines, "\n")
}

// MergeEnv takes the masked values of env from cur (same key), or empties
// them when cur does not have the key.
func MergeEnv(env, cur string) string {
	have := map[string]string{}
	for _, l := range strings.Split(cur, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			have[k] = v
		}
	}
	lines := strings.Split(env, "\n")
	for i, l := range lines {
		if k, v, ok := strings.Cut(l, "="); ok && v == Masked {
			lines[i] = k + "=" + have[k]
		}
	}
	return strings.Join(lines, "\n")
}

// Build makes the readable configuration and its secrets.
func Build(src Source) (Config, Secrets) {
	st := src.State
	sec := Secrets{Settings: map[string]string{}, Users: map[string]UserSecrets{}, Backups: map[string]BackupSecrets{}, Clouds: map[string]map[string]string{}, Stacks: map[string]string{}, UPS: map[string]string{}, HA: map[string]string{}, Vaults: map[string]map[string]string{}, Fleet: map[string]string{}}
	c := Config{System: src.System, Groups: src.Groups, Quotas: src.Quotas, Network: src.Network}

	set := st.Settings
	put(sec.Settings, "smtpPassword", mask(&set.SMTPPassword))
	put(sec.Settings, "telegramToken", mask(&set.TelegramToken))
	put(sec.Settings, "ntfyToken", mask(&set.NtfyToken))
	put(sec.Settings, "metricsToken", mask(&set.MetricsToken))
	c.Settings = &set

	c.Users = []User{}
	for _, u := range st.Users {
		x := User{Username: u.Username, FullName: u.FullName, Email: u.Email, Role: u.Role, Disabled: u.Disabled, SSH: u.SSH, Source: u.Source, Groups: []string{}}
		us := UserSecrets{PasswordHash: u.PasswordHash, TOTPSecret: u.TOTPSecret, RecoveryCodes: u.RecoveryCodes}
		if l, ok := src.Linux[u.Username]; ok && u.Source == "" {
			x.UID, x.GID, us.Shadow, us.SMB = l.UID, l.GID, l.Shadow, src.SMB[u.Username]
			x.Groups = append(x.Groups, l.Groups...)
		}
		x.Password, x.TwoFactor = us.PasswordHash, us.TOTPSecret
		mask(&x.Password)
		mask(&x.TwoFactor)
		if us.PasswordHash != "" || us.Shadow != "" || us.TOTPSecret != "" {
			sec.Users[u.Username] = us
		}
		c.Users = append(c.Users, x)
	}

	c.Volumes = []Volume{}
	for _, v := range st.Volumes {
		c.Volumes = append(c.Volumes, Volume{ID: v.ID, Name: v.Name, Kind: "volume", FSType: v.FSType, MountPoint: v.MountPoint})
	}
	c.Volumes = append(c.Volumes, src.Pools...)

	c.Shares = []store.Share{}
	for _, sh := range st.Shares {
		x := *sh
		x.Used, x.UsedAt, x.QuotaFull, x.Frozen, x.Locked = 0, time.Time{}, false, false, false
		x.Permissions = cloneMap(sh.Permissions)
		c.Shares = append(c.Shares, x)
	}

	c.Schedules = []store.Schedule{}
	for _, s := range st.Schedules {
		x := *s
		x.History = nil
		c.Schedules = append(c.Schedules, x)
	}

	c.Backups = []store.BackupTask{}
	for _, b := range st.Backups {
		x := *b
		x.History = nil
		x.Sources, x.Excludes = slices.Clone(b.Sources), slices.Clone(b.Excludes)
		bs := BackupSecrets{Password: mask(&x.Password), SecretKey: mask(&x.SecretKey), VaultToken: mask(&x.VaultToken)}
		if bs != (BackupSecrets{}) {
			sec.Backups[x.ID] = bs
		}
		c.Backups = append(c.Backups, x)
	}

	c.CloudSyncs = []store.CloudSync{}
	for _, cs := range st.Clouds {
		x := *cs
		x.History, x.Synced = nil, false
		x.Params, x.Secrets, x.Excludes = cloneMap(cs.Params), map[string]string{}, slices.Clone(cs.Excludes)
		m := map[string]string{}
		for k, v := range cs.Secrets {
			if v != "" {
				m[k], x.Secrets[k] = v, Masked
			}
		}
		if len(m) > 0 {
			sec.Clouds[x.ID] = m
		}
		c.CloudSyncs = append(c.CloudSyncs, x)
	}

	c.Stacks = []Stack{}
	for _, s := range src.Stacks {
		if red := RedactEnv(s.Env); red != s.Env {
			sec.Stacks[s.Name] = s.Env
			s.Env = red
		}
		c.Stacks = append(c.Stacks, s)
	}
	sort.Slice(c.Stacks, func(i, j int) bool { return c.Stacks[i].Name < c.Stacks[j].Name })

	fs := st.Files
	c.FileServices = &fs
	rw := st.Ransom
	rw.Blocked, rw.NFSPaused = nil, false
	c.Ransomware = &rw

	vpn := st.VPN
	vs := VPNSecrets{PrivateKey: mask(&vpn.PrivateKey), Peers: map[string][2]string{}}
	vpn.Peers = slices.Clone(st.VPN.Peers)
	for i := range vpn.Peers {
		p := &vpn.Peers[i]
		priv, psk := mask(&p.PrivateKey), mask(&p.PresharedKey)
		if priv != "" || psk != "" {
			vs.Peers[p.ID] = [2]string{priv, psk}
		}
	}
	vs.Links, vs.Client = map[string][2]string{}, map[string]string{}
	vpn.Links = slices.Clone(st.VPN.Links)
	for i := range vpn.Links {
		l := &vpn.Links[i]
		priv, psk := mask(&l.PrivateKey), mask(&l.PresharedKey)
		if priv != "" || psk != "" {
			vs.Links[l.ID] = [2]string{priv, psk}
		}
	}
	vpn.Client.Apps = slices.Clone(st.VPN.Client.Apps)
	vpn.Client.Detached = nil // runtime: networks taken from containers here
	put(vs.Client, "privateKey", mask(&vpn.Client.PrivateKey))
	put(vs.Client, "presharedKey", mask(&vpn.Client.PresharedKey))
	if vs.PrivateKey != "" || len(vs.Peers) > 0 || len(vs.Links) > 0 || len(vs.Client) > 0 {
		sec.VPN = &vs
	}
	c.VPN = &vpn

	dd := st.DDNS
	dd.LastIP, dd.LastUpdate, dd.LastResult = "", time.Time{}, ""
	sec.DDNS = mask(&dd.Token)
	c.DDNS = &dd

	en := st.Energy
	c.Energy = &en

	g := Guard{Config: st.Guard.Config}
	g.Config.Geo.Updated, g.Config.Geo.Error = time.Time{}, ""
	for _, b := range st.Guard.Blocks {
		if b.Manual && b.Until.After(src.Now) {
			g.Blocks = append(g.Blocks, guard.Block{IP: b.IP, Until: b.Until, At: b.At, Service: b.Service, Manual: true})
		}
	}
	c.Guard = &g

	if st.UPS != nil {
		u := *st.UPS
		u.LocalPassword = ""
		put(sec.UPS, "remotePassword", mask(&u.RemotePassword))
		put(sec.UPS, "sharePassword", mask(&u.SharePassword))
		c.UPS = &u
	} else {
		c.UPS = &ups.Config{Mode: "off"}
	}

	c.Photos = slices.Clone(st.Photos.Folders)
	if c.Photos == nil {
		c.Photos = []string{}
	}

	c.Vaults = []store.Vault{}
	for _, v := range st.Vaults {
		x := *v
		x.Clients = nil
		m := map[string]string{}
		for _, cl := range v.Clients {
			cl.Size, cl.LastSeen = 0, time.Time{}
			put(m, cl.ID, mask(&cl.TokenHash))
			x.Clients = append(x.Clients, cl)
		}
		if len(m) > 0 {
			sec.Vaults[x.ID] = m
		}
		c.Vaults = append(c.Vaults, x)
	}

	c.Fleet = []store.FleetMember{}
	for _, m := range st.Fleet.Members {
		put(sec.Fleet, m.ID, mask(&m.Token))
		c.Fleet = append(c.Fleet, m)
	}

	cb := st.ConfigBackup
	cb.LastRun, cb.LastResult, cb.LastFile = time.Time{}, "", ""
	sec.AutoPass = mask(&cb.Passphrase)
	c.ConfigBackup = &cb

	if st.HA.Role != "" {
		ha := st.HA
		put(sec.HA, "authPass", mask(&ha.AuthPass))
		put(sec.HA, "token", mask(&ha.Token))
		ha.LastSync, ha.LastResult = time.Time{}, ""
		c.HA = &ha
	}
	if st.Directory.Type != "" {
		d := st.Directory
		sec.LDAPBind = mask(&d.BindPassword)
		c.Directory = &d
	}
	return c, sec
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func unmask(p *string, v string) {
	if *p == Masked && v != "" {
		*p = v
	}
}

// Merge puts the secrets back into a configuration read from a file. Values
// left Masked afterwards were not exported.
func Merge(c *Config, sec *Secrets) {
	if sec == nil {
		return
	}
	if s := c.Settings; s != nil {
		unmask(&s.SMTPPassword, sec.Settings["smtpPassword"])
		unmask(&s.TelegramToken, sec.Settings["telegramToken"])
		unmask(&s.NtfyToken, sec.Settings["ntfyToken"])
		unmask(&s.MetricsToken, sec.Settings["metricsToken"])
	}
	for i := range c.Backups {
		b := &c.Backups[i]
		bs := sec.Backups[b.ID]
		unmask(&b.Password, bs.Password)
		unmask(&b.SecretKey, bs.SecretKey)
		unmask(&b.VaultToken, bs.VaultToken)
	}
	for i := range c.CloudSyncs {
		cs := &c.CloudSyncs[i]
		for k, v := range cs.Secrets {
			if v == Masked {
				if real, ok := sec.Clouds[cs.ID][k]; ok {
					cs.Secrets[k] = real
				}
			}
		}
	}
	for i := range c.Stacks {
		if env, ok := sec.Stacks[c.Stacks[i].Name]; ok {
			c.Stacks[i].Env = env
		}
	}
	if v := c.VPN; v != nil && sec.VPN != nil {
		unmask(&v.PrivateKey, sec.VPN.PrivateKey)
		for i := range v.Peers {
			p := &v.Peers[i]
			k := sec.VPN.Peers[p.ID]
			unmask(&p.PrivateKey, k[0])
			unmask(&p.PresharedKey, k[1])
		}
		for i := range v.Links {
			l := &v.Links[i]
			k := sec.VPN.Links[l.ID]
			unmask(&l.PrivateKey, k[0])
			unmask(&l.PresharedKey, k[1])
		}
		unmask(&v.Client.PrivateKey, sec.VPN.Client["privateKey"])
		unmask(&v.Client.PresharedKey, sec.VPN.Client["presharedKey"])
	}
	if c.DDNS != nil {
		unmask(&c.DDNS.Token, sec.DDNS)
	}
	if c.UPS != nil {
		unmask(&c.UPS.RemotePassword, sec.UPS["remotePassword"])
		unmask(&c.UPS.SharePassword, sec.UPS["sharePassword"])
	}
	for i := range c.Vaults {
		v := &c.Vaults[i]
		for j := range v.Clients {
			unmask(&v.Clients[j].TokenHash, sec.Vaults[v.ID][v.Clients[j].ID])
		}
	}
	for i := range c.Fleet {
		unmask(&c.Fleet[i].Token, sec.Fleet[c.Fleet[i].ID])
	}
	if c.ConfigBackup != nil {
		unmask(&c.ConfigBackup.Passphrase, sec.AutoPass)
	}
	if c.HA != nil {
		unmask(&c.HA.AuthPass, sec.HA["authPass"])
		unmask(&c.HA.Token, sec.HA["token"])
	}
	if c.Directory != nil {
		unmask(&c.Directory.BindPassword, sec.LDAPBind)
	}
}
