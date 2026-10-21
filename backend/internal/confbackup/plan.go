package confbackup

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"

	"strataos/internal/guard"
	"strataos/internal/store"
)

// Actions of a planned item.
const (
	Create = "create"
	Update = "update"
	Same   = "same"
	Skip   = "skip"
	Report = "report" // shown, never applied (volumes, HA, directory)
)

// Item is one thing the import would create, change or leave alone.
type Item struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Action  string   `json:"action"`
	Detail  string   `json:"detail,omitempty"`
	Changes []string `json:"changes,omitempty"`
	// Target is the existing item updated (its ID here, when it differs from Key).
	Target string `json:"target,omitempty"`
}

// Section groups the items of one part of the configuration.
type Section struct {
	ID    string `json:"id"`
	Items []Item `json:"items"`
}

// Sections lists the section IDs in the order they are applied.
var Sections = []string{"system", "settings", "groups", "users", "quotas", "volumes", "shares", "schedules", "backups", "cloudSyncs", "stacks", "network", "fileServices", "ransomware", "vpn", "ddns", "energy", "guard", "ups", "photos", "vaults", "fleet", "configBackup", "ha", "directory"}

// Env is what the plan needs to know about this NAS.
type Env struct {
	Self       string // the administrator importing (never changed)
	Secrets    bool   // the secrets of the file were decrypted
	OnVolume   func(path string) bool
	Exists     func(path string) bool
	VolumeID   func(mount string) string // ID of the volume mounted there ("" when none)
	QuotaOn    func(mount string) bool
	Docker     bool
	Directory  string   // directory type joined here
	Interfaces []string // physical network interfaces
}

// toMap gives the JSON fields of v.
func toMap(v any) map[string]any {
	m := map[string]any{}
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &m)
	return m
}

// zero: absent, null, false, 0, "" and empty lists or objects are the same.
func zero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case float64:
		return x == 0
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// changes lists the JSON fields that differ between a and b.
func changes(a, b any, ignore ...string) []string {
	ma, mb := toMap(a), toMap(b)
	keys := map[string]bool{}
	for k := range ma {
		keys[k] = true
	}
	for k := range mb {
		keys[k] = true
	}
	var out []string
	for k := range keys {
		if slices.Contains(ignore, k) || (zero(ma[k]) && zero(mb[k])) {
			continue
		}
		if !reflect.DeepEqual(ma[k], mb[k]) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// diff makes the item of a file value against the current one (nil: absent).
func diff(key, name string, file, cur any, exists bool, ignore ...string) Item {
	it := Item{Key: key, Name: name}
	if !exists {
		it.Action = Create
		return it
	}
	if it.Changes = changes(file, cur, ignore...); len(it.Changes) > 0 {
		it.Action = Update
	} else {
		it.Action = Same
	}
	return it
}

func skip(key, name, detail string) Item {
	return Item{Key: key, Name: name, Action: Skip, Detail: detail}
}

// hasMasked reports whether a value still holds a secret that was not exported.
func hasMasked(v any) bool {
	b, _ := json.Marshal(v)
	return strings.Contains(string(b), `"`+Masked+`"`)
}

// Plan compares a configuration from a file (already remapped, secrets
// still masked) with the current one.
func Plan(file, cur *Config, env Env) []Section {
	if env.OnVolume == nil {
		env.OnVolume = func(string) bool { return false }
	}
	if env.Exists == nil {
		env.Exists = func(string) bool { return false }
	}
	if env.VolumeID == nil {
		env.VolumeID = func(string) string { return "" }
	}
	if env.QuotaOn == nil {
		env.QuotaOn = func(string) bool { return false }
	}
	var out []Section
	add := func(id string, items []Item) {
		if len(items) > 0 {
			out = append(out, Section{ID: id, Items: items})
		}
	}

	// System
	var items []Item
	if f, c := file.System, cur.System; f != nil && c != nil {
		str := func(key, v, cur string) {
			if v == "" {
				return
			}
			it := Item{Key: key, Name: v, Action: Same}
			if v != cur {
				it.Action, it.Changes = Update, []string{key}
			}
			items = append(items, it)
		}
		str("hostname", f.Hostname, c.Hostname)
		str("timezone", f.Timezone, c.Timezone)
	}
	add("system", items)

	if file.Settings != nil {
		add("settings", []Item{diff("settings", "Settings", file.Settings, cur.Settings, cur.Settings != nil)})
	}

	// Groups: members are added, never removed.
	items = nil
	for _, g := range file.Groups {
		i := slices.IndexFunc(cur.Groups, func(x Group) bool { return x.Name == g.Name })
		if i < 0 {
			items = append(items, Item{Key: g.Name, Name: g.Name, Action: Create})
			continue
		}
		it := Item{Key: g.Name, Name: g.Name, Action: Same}
		for _, m := range g.Members {
			if !slices.Contains(cur.Groups[i].Members, m) {
				it.Action, it.Changes = Update, []string{"members"}
			}
		}
		items = append(items, it)
	}
	add("groups", items)

	// Users
	items = nil
	for _, u := range file.Users {
		i := slices.IndexFunc(cur.Users, func(x User) bool { return x.Username == u.Username })
		switch {
		case u.Username == env.Self:
			items = append(items, skip(u.Username, u.Username, "Your own account is left unchanged"))
			continue
		case u.Source != "" && u.Source != env.Directory:
			items = append(items, skip(u.Username, u.Username, "Directory account: join the directory first"))
			continue
		}
		var c User
		if i >= 0 {
			c = cur.Users[i]
		}
		it := diff(u.Username, u.Username, u, c, i >= 0, "uid", "gid", "password", "twoFactor", "disabled")
		if it.Action == Create && u.Source == "" {
			if env.Secrets && u.Password != "" {
				it.Detail = "Password and Samba password restored"
			} else {
				it.Detail = "Created disabled: set a password to enable it"
			}
		}
		items = append(items, it)
	}
	add("users", items)

	// Quotas
	items = nil
	for _, q := range file.Quotas {
		key := q.User + "@" + q.MountPoint
		name := q.User + " · " + q.MountPoint
		known := slices.ContainsFunc(file.Users, func(u User) bool { return u.Username == q.User }) || slices.ContainsFunc(cur.Users, func(u User) bool { return u.Username == q.User })
		switch {
		case !known:
			items = append(items, skip(key, name, "Account "+q.User+" not found"))
		case !env.QuotaOn(q.MountPoint):
			items = append(items, skip(key, name, "Per-user quotas are not enabled on "+q.MountPoint))
		default:
			i := slices.IndexFunc(cur.Quotas, func(x Quota) bool { return x.User == q.User && x.MountPoint == q.MountPoint })
			var c Quota
			if i >= 0 {
				c = cur.Quotas[i]
			}
			items = append(items, diff(key, name, q, c, i >= 0, "kind"))
		}
	}
	add("quotas", items)

	// Volumes: report where each one is on this NAS.
	items = nil
	for _, v := range file.Volumes {
		it := Item{Key: v.MountPoint, Name: v.Name, Action: Report}
		switch {
		case v.Mapped() != v.MountPoint && env.OnVolume(v.Mapped()):
			it.Detail = "Mapped to " + v.Mapped()
		case env.OnVolume(v.MountPoint):
			it.Detail = "Found here"
		default:
			it.Detail = "Not found: what it holds is skipped unless it is mapped to a volume of this NAS"
		}
		items = append(items, it)
	}
	add("volumes", items)

	// Shares
	items = nil
	for _, sh := range file.Shares {
		i := slices.IndexFunc(cur.Shares, func(x store.Share) bool { return strings.EqualFold(x.Name, sh.Name) })
		switch {
		case !env.OnVolume(sh.Path):
			items = append(items, skip(sh.Name, sh.Name, "Volume missing here: "+sh.Path))
			continue
		case sh.Encrypted && i < 0 && !env.Exists(sh.CipherDir+"/gocryptfs.conf"):
			items = append(items, skip(sh.Name, sh.Name, "Encrypted data not found: "+sh.CipherDir))
			continue
		}
		var c store.Share
		if i >= 0 {
			c = cur.Shares[i]
		}
		it := diff(sh.Name, sh.Name, sh, c, i >= 0, "createdAt")
		if i >= 0 {
			it.Target = c.Name
		}
		items = append(items, it)
	}
	add("shares", items)

	// Schedules: matched by ID, then by name and type.
	items = nil
	for _, s := range file.Schedules {
		if s.TargetKind == "btrfs" && s.Target == "" {
			items = append(items, skip(s.ID, s.Name, "Volume of the target missing here"))
			continue
		}
		i := slices.IndexFunc(cur.Schedules, func(x store.Schedule) bool { return x.ID == s.ID })
		if i < 0 {
			i = slices.IndexFunc(cur.Schedules, func(x store.Schedule) bool { return x.Name == s.Name && x.Type == s.Type })
		}
		var c store.Schedule
		if i >= 0 {
			c = cur.Schedules[i]
		}
		it := diff(s.ID, s.Name, s, c, i >= 0, "id", "createdAt", "history")
		it.Target = c.ID
		items = append(items, it)
	}
	add("schedules", items)

	// Backup tasks
	items = nil
	for _, b := range file.Backups {
		if p := missingPath(b.Kind != "zfs", b.Sources, env); p != "" {
			items = append(items, skip(b.ID, b.Name, "Volume missing here: "+p))
			continue
		}
		if b.DestType == "local" && b.Kind != "zfs" && !env.OnVolume(b.DestPath) {
			items = append(items, skip(b.ID, b.Name, "Volume missing here: "+b.DestPath))
			continue
		}
		i := slices.IndexFunc(cur.Backups, func(x store.BackupTask) bool { return x.ID == b.ID })
		if i < 0 {
			i = slices.IndexFunc(cur.Backups, func(x store.BackupTask) bool { return x.Name == b.Name })
		}
		var c store.BackupTask
		if i >= 0 {
			c = cur.Backups[i]
		}
		it := diff(b.ID, b.Name, b, c, i >= 0, "id", "createdAt", "history")
		it.Target = c.ID
		if it.Action == Create && !env.Secrets && hasMasked(b) {
			it.Detail = "Created disabled: enter its passwords again"
		}
		items = append(items, it)
	}
	add("backups", items)

	// Cloud syncs
	items = nil
	for _, cs := range file.CloudSyncs {
		if !env.OnVolume(cs.LocalPath) {
			items = append(items, skip(cs.ID, cs.Name, "Volume missing here: "+cs.LocalPath))
			continue
		}
		i := slices.IndexFunc(cur.CloudSyncs, func(x store.CloudSync) bool { return x.ID == cs.ID })
		if i < 0 {
			i = slices.IndexFunc(cur.CloudSyncs, func(x store.CloudSync) bool { return x.Name == cs.Name })
		}
		if i < 0 && !env.Secrets && hasMasked(cs.Secrets) {
			items = append(items, skip(cs.ID, cs.Name, "Credentials not in the file (export with secrets)"))
			continue
		}
		var c store.CloudSync
		if i >= 0 {
			c = cur.CloudSyncs[i]
		}
		it := diff(cs.ID, cs.Name, cs, c, i >= 0, "id", "createdAt", "history", "synced")
		it.Target = c.ID
		items = append(items, it)
	}
	add("cloudSyncs", items)

	// Docker stacks
	items = nil
	for _, s := range file.Stacks {
		if !env.Docker {
			items = append(items, skip(s.Name, s.Name, "Docker is not installed"))
			continue
		}
		i := slices.IndexFunc(cur.Stacks, func(x Stack) bool { return x.Name == s.Name })
		var c Stack
		if i >= 0 {
			c = cur.Stacks[i]
		}
		it := diff(s.Name, s.Name, s, c, i >= 0, "app")
		if it.Action != Same && !env.Secrets && strings.Contains(s.Env, "="+Masked) {
			it.Detail = "Passwords in .env not in the file: set them before deploying"
		}
		items = append(items, it)
	}
	add("stacks", items)

	// Network: applied on trial, confirmed by the browser.
	if n := file.Network; n != nil && (len(n.Interfaces)+len(n.Bonds)+len(n.Bridges)+len(n.VLANs) > 0) {
		var missing string
		names := []string{}
		for _, i := range n.Interfaces {
			names = append(names, i.Name)
		}
		for _, b := range n.Bonds {
			names = append(names, b.Members...)
		}
		for _, x := range names {
			if !slices.Contains(env.Interfaces, x) && missing == "" {
				missing = x
			}
		}
		if missing != "" {
			add("network", []Item{skip("network", "Network", "Interface missing here: "+missing)})
		} else {
			it := diff("network", "Network", n, cur.Network, cur.Network != nil)
			if it.Action == Create {
				it.Action = Update
			}
			if it.Action == Update {
				it.Detail = "Applied on trial: confirm it within 2 minutes"
			}
			add("network", []Item{it})
		}
	}

	single := func(id string, f, c any, ignore ...string) {
		if !reflect.ValueOf(f).IsNil() {
			add(id, []Item{diff(id, id, f, c, !reflect.ValueOf(c).IsNil(), ignore...)})
		}
	}
	single("fileServices", file.FileServices, cur.FileServices)
	single("ransomware", file.Ransomware, cur.Ransomware)

	// VPN: settings, then devices (their keys are secrets).
	if v := file.VPN; v != nil {
		var c store.VPN
		if cur.VPN != nil {
			c = *cur.VPN
		}
		it := diff("settings", "Settings", vpnSettings(*v), vpnSettings(c), true)
		if !env.Secrets && c.PublicKey == "" && len(v.Peers) > 0 {
			it.Detail = "New server key: devices must be configured again"
		}
		items = []Item{it}
		for _, p := range v.Peers {
			i := slices.IndexFunc(c.Peers, func(x store.VPNPeer) bool { return x.PublicKey == p.PublicKey || x.ID == p.ID })
			switch {
			case i >= 0:
				items = append(items, diff(p.ID, p.Name, p, c.Peers[i], true, "createdAt", "privateKey", "presharedKey"))
			case !env.Secrets:
				items = append(items, skip(p.ID, p.Name, "Device keys not in the file (export with secrets)"))
			default:
				items = append(items, Item{Key: p.ID, Name: p.Name, Action: Create})
			}
		}
		// Sites imported from another StrataOS, then the outgoing tunnel.
		for _, l := range v.Links {
			i := slices.IndexFunc(c.Links, func(x store.VPNLink) bool {
				return x.ID == l.ID || (x.PeerPublicKey == l.PeerPublicKey && x.Address == l.Address)
			})
			switch {
			case i >= 0:
				items = append(items, diff("link:"+l.ID, l.Name, l, c.Links[i], true, "createdAt", "privateKey", "presharedKey", "iface"))
			case !env.Secrets:
				items = append(items, skip("link:"+l.ID, l.Name, "Site keys not in the file (export with secrets)"))
			default:
				items = append(items, Item{Key: "link:" + l.ID, Name: l.Name, Action: Create})
			}
		}
		if v.Client.PeerPublicKey != "" {
			if !env.Secrets && c.Client.PrivateKey == "" {
				items = append(items, skip("client", "Outgoing tunnel", "Tunnel keys not in the file (export with secrets)"))
			} else {
				items = append(items, diff("client", "Outgoing tunnel", v.Client, c.Client, c.Client.PeerPublicKey != "", "privateKey", "presharedKey", "detached"))
			}
		}
		add("vpn", items)
	}

	single("ddns", file.DDNS, cur.DDNS)
	single("energy", file.Energy, cur.Energy)
	if g := file.Guard; g != nil {
		var c Guard
		if cur.Guard != nil {
			c = *cur.Guard
		}
		items = []Item{diff("settings", "Settings", g.Config, c.Config, true)}
		for _, b := range g.Blocks {
			done := slices.ContainsFunc(c.Blocks, func(x guard.Block) bool { return x.IP == b.IP })
			items = append(items, Item{Key: b.IP, Name: b.IP, Action: map[bool]string{true: Same, false: Create}[done]})
		}
		add("guard", items)
	}
	single("ups", file.UPS, cur.UPS)

	// Photo folders: added to the current ones.
	items = nil
	for _, p := range file.Photos {
		switch {
		case slices.Contains(cur.Photos, p):
			items = append(items, Item{Key: p, Name: p, Action: Same})
		case !env.Exists(p):
			items = append(items, skip(p, p, "Folder not found: "+p))
		default:
			items = append(items, Item{Key: p, Name: p, Action: Create})
		}
	}
	add("photos", items)

	// Backup vaults
	items = nil
	for _, v := range file.Vaults {
		if !env.OnVolume(v.Path) {
			items = append(items, skip(v.ID, v.Name, "Volume missing here: "+v.Path))
			continue
		}
		i := slices.IndexFunc(cur.Vaults, func(x store.Vault) bool { return x.ID == v.ID || x.Path == v.Path })
		var c store.Vault
		if i >= 0 {
			c = cur.Vaults[i]
		}
		it := diff(v.ID, v.Name, v, c, i >= 0, "id", "created", "clients")
		it.Target = c.ID
		items = append(items, it)
	}
	add("vaults", items)

	// NAS watched by this one
	items = nil
	for _, m := range file.Fleet {
		switch {
		case slices.ContainsFunc(cur.Fleet, func(x store.FleetMember) bool { return x.ID == m.ID || x.Host == m.Host }):
			items = append(items, Item{Key: m.ID, Name: m.Name, Action: Same})
		case !env.Secrets:
			items = append(items, skip(m.ID, m.Name, "Access token not in the file (export with secrets)"))
		default:
			items = append(items, Item{Key: m.ID, Name: m.Name, Action: Create})
		}
	}
	add("fleet", items)

	if cb := file.ConfigBackup; cb != nil && (cb.Enabled || cb.Folder != "") {
		if cb.Folder != "" && !env.OnVolume(cb.Folder) {
			add("configBackup", []Item{skip("configBackup", "configBackup", "Volume missing here: "+cb.Folder)})
		} else {
			single("configBackup", cb, cur.ConfigBackup)
		}
	}

	if h := file.HA; h != nil {
		add("ha", []Item{{Key: "ha", Name: h.Role + " · " + h.PeerHost, Action: Report, Detail: "Never recreated: pair the NAS again from High availability"}})
	}
	if d := file.Directory; d != nil {
		add("directory", []Item{{Key: "directory", Name: d.Type + " · " + firstOf(d.Realm, d.URI), Action: Report, Detail: "Never applied: join the directory again"}})
	}
	return out
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// missingPath returns the first path not on a volume here.
func missingPath(paths bool, list []string, env Env) string {
	if !paths {
		return ""
	}
	for _, p := range list {
		if !env.OnVolume(p) {
			return p
		}
	}
	return ""
}

type vpnSet struct {
	Enabled     bool   `json:"enabled"`
	Port        int    `json:"port"`
	Subnet      string `json:"subnet"`
	Endpoint    string `json:"endpoint"`
	LAN         string `json:"lan"`
	DefaultMode string `json:"defaultMode,omitempty"`
	UserDevices bool   `json:"userDevices,omitempty"`
}

func vpnSettings(v store.VPN) vpnSet {
	return vpnSet{v.Enabled, v.Port, v.Subnet, v.Endpoint, v.LAN, v.DefaultMode, v.UserDevices}
}

// Mapped is set by Remap on the copy of the volume list (its new mount point).
func (v Volume) Mapped() string {
	if v.mapped != "" {
		return v.mapped
	}
	return v.MountPoint
}
