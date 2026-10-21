package confbackup

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// MapPath moves p from an exported volume to a volume of this NAS (the
// longest matching prefix of m wins), like the HA volume mapping.
func MapPath(p string, m map[string]string) string {
	best := ""
	for from := range m {
		if (p == from || strings.HasPrefix(p, from+"/")) && len(from) > len(best) {
			best = from
		}
	}
	if best == "" {
		return p
	}
	return m[best] + strings.TrimPrefix(p, best)
}

// ValidPathMap checks a mapping: mount points under /srv/strata on both sides.
func ValidPathMap(m map[string]string) error {
	if len(m) > 64 {
		return fmt.Errorf("too many mapped volumes")
	}
	for from, to := range m {
		for _, p := range []string{from, to} {
			if filepath.Clean(p) != p || !strings.HasPrefix(p, "/srv/strata/") || strings.Count(p, "/") > 4 {
				return fmt.Errorf("invalid volume path %q", p)
			}
		}
	}
	return nil
}

// Remap applies a volume mapping to every path of the configuration.
func Remap(c *Config, m map[string]string) {
	if len(m) == 0 {
		return
	}
	mp := func(p *string) {
		if *p != "" {
			*p = MapPath(*p, m)
		}
	}
	for i := range c.Volumes {
		c.Volumes[i].mapped = MapPath(c.Volumes[i].MountPoint, m)
	}
	for i := range c.Shares {
		mp(&c.Shares[i].Path)
		mp(&c.Shares[i].CipherDir)
	}
	for i := range c.Backups {
		b := &c.Backups[i]
		if b.Kind == "zfs" {
			continue // datasets, not paths
		}
		for j := range b.Sources {
			mp(&b.Sources[j])
		}
		if b.DestType == "local" {
			mp(&b.DestPath)
		}
	}
	for i := range c.CloudSyncs {
		mp(&c.CloudSyncs[i].LocalPath)
	}
	for i := range c.Photos {
		mp(&c.Photos[i])
	}
	for i := range c.Vaults {
		mp(&c.Vaults[i].Path)
	}
	for i := range c.Quotas {
		mp(&c.Quotas[i].MountPoint)
	}
	if c.ConfigBackup != nil {
		mp(&c.ConfigBackup.Folder)
	}
	// App folders (DATA_DIR=/srv/strata/main/apps/…) follow their volume.
	for i := range c.Stacks {
		lines := strings.Split(c.Stacks[i].Env, "\n")
		for j, l := range lines {
			if k, v, ok := strings.Cut(l, "="); ok && strings.HasPrefix(v, "/srv/strata/") {
				lines[j] = k + "=" + MapPath(v, m)
			}
		}
		c.Stacks[i].Env = strings.Join(lines, "\n")
	}
}

// ResolveTargets points the scheduled tasks on Btrfs volumes (targets are
// volume IDs) at the volume of this NAS now mounted where theirs was, or at
// nothing ("") when there is none.
func ResolveTargets(c *Config, volumeID func(mount string) string) {
	for i := range c.Schedules {
		s := &c.Schedules[i]
		if s.TargetKind != "btrfs" {
			continue
		}
		id := ""
		for _, v := range c.Volumes {
			if v.ID == s.Target && volumeID != nil {
				id = volumeID(v.Mapped())
			}
		}
		s.Target = id
	}
}

// Prepare decrypts the secrets (when a passphrase is given) and returns the
// configuration to compare (secrets masked) and the one to apply (secrets
// put back), both moved to the volumes of this NAS.
func (d *Document) Prepare(passphrase string, m map[string]string, volumeID func(mount string) string) (plain, full *Config, sec *Secrets, err error) {
	if err := ValidPathMap(m); err != nil {
		return nil, nil, nil, err
	}
	if d.Secrets != nil && passphrase != "" {
		sec = &Secrets{}
		if err := d.Secrets.Open(passphrase, sec); err != nil {
			return nil, nil, nil, err
		}
	}
	b, err := json.Marshal(d.Config)
	if err != nil {
		return nil, nil, nil, err
	}
	plain, full = &Config{}, &Config{}
	if err := json.Unmarshal(b, plain); err != nil {
		return nil, nil, nil, err
	}
	_ = json.Unmarshal(b, full)
	Merge(full, sec)
	for _, c := range []*Config{plain, full} {
		Remap(c, m)
		ResolveTargets(c, volumeID)
	}
	return plain, full, sec, nil
}
