// Package confbackup exports the StrataOS configuration as a JSON document
// and plans its import on another (or a reinstalled) NAS.
//
// The document holds configuration only: what an administrator set up, never
// data (files, snapshots, container volumes, albums) nor runtime state
// (events, sessions, task history, measured usage, blocked accounts). Secrets
// are left out of the readable part: each one shows as "********" there, and
// its value goes, when asked, into an encrypted section (see Seal).
//
// Field by field choices, from store.State:
//   - Users, groups, quotas: kept (with the Linux UID/GID so files on moved
//     disks keep their owner). Sign-in, shadow and Samba hashes and 2FA keys
//     are secrets. TOTPPending/TOTPLast are runtime.
//   - Shares: kept, without Used/UsedAt/QuotaFull/Frozen/Locked (measured or
//     runtime).
//   - Volumes: listed for the mapping and the report, never recreated.
//   - Schedules, backups, cloud syncs: kept without History; cloud Synced is
//     cleared (a two-way sync starts again from a fresh baseline).
//   - Docker stacks: compose.yaml and .env (password-like values are
//     secrets), never container data.
//   - Network, file services, ransomware (without Blocked/NFSPaused), VPN
//     (keys are secrets), dynamic DNS (without its last result), energy,
//     address blocking (settings and manual blocks still running; automatic
//     blocks are runtime), UPS (without the generated local password), photo
//     folders, backup vaults, consoles this NAS watches, automatic config
//     backups: kept.
//   - HA and directory: exported for reference, never applied (a pairing or
//     a domain join must be redone on purpose).
//   - Not exported: events, share links (they belong to their owners and
//     count downloads), albums (user content), the NAS allowed to watch this
//     one (they pinned this NAS's certificate), the state schema.
package confbackup

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"strataos/internal/guard"
	"strataos/internal/network"
	"strataos/internal/store"
	"strataos/internal/ups"
)

// Format names the document; Version is its layout, raised on any change an
// older StrataOS could misread.
const (
	Format  = "strataos-config"
	Version = 1
)

// Masked stands for a secret in the readable part of the document.
const Masked = "********"

// Document is the exported file.
type Document struct {
	Format   string    `json:"format"`
	Version  int       `json:"version"`
	StrataOS string    `json:"strataos"`
	Created  time.Time `json:"created"`
	Hostname string    `json:"hostname"`
	Config   Config    `json:"config"`
	// Secrets is the encrypted Secrets (absent when they were not exported).
	Secrets *Sealed `json:"secrets,omitempty"`
}

// Config is the readable configuration, one field per section.
type Config struct {
	System       *System             `json:"system,omitempty"`
	Settings     *store.Settings     `json:"settings,omitempty"`
	Groups       []Group             `json:"groups"`
	Users        []User              `json:"users"`
	Quotas       []Quota             `json:"quotas"`
	Volumes      []Volume            `json:"volumes"`
	Shares       []store.Share       `json:"shares"`
	Schedules    []store.Schedule    `json:"schedules"`
	Backups      []store.BackupTask  `json:"backups"`
	CloudSyncs   []store.CloudSync   `json:"cloudSyncs"`
	Stacks       []Stack             `json:"stacks"`
	Network      *network.Config     `json:"network,omitempty"`
	FileServices *store.FileServices `json:"fileServices,omitempty"`
	Ransomware   *store.Ransomware   `json:"ransomware,omitempty"`
	VPN          *store.VPN          `json:"vpn,omitempty"`
	DDNS         *store.DDNS         `json:"ddns,omitempty"`
	Energy       *store.Energy       `json:"energy,omitempty"`
	Guard        *Guard              `json:"guard,omitempty"`
	UPS          *ups.Config         `json:"ups,omitempty"`
	Photos       []string            `json:"photoFolders"`
	Vaults       []store.Vault       `json:"vaults"`
	Fleet        []store.FleetMember `json:"fleet"`
	ConfigBackup *store.ConfigBackup `json:"configBackup,omitempty"`
	HA           *store.HA           `json:"ha,omitempty"`
	Directory    *store.Directory    `json:"directory,omitempty"`
}

// System holds settings kept by the operating system rather than the store.
type System struct {
	Hostname string `json:"hostname"`
	Timezone string `json:"timezone"`
}

// Group is a group created from the interface.
type Group struct {
	Name    string   `json:"name"`
	GID     int      `json:"gid"`
	Members []string `json:"members"`
}

// User is a StrataOS account with its Linux identity.
type User struct {
	Username string `json:"username"`
	FullName string `json:"fullName"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
	SSH      bool   `json:"ssh"`
	Source   string `json:"source,omitempty"` // ad | ldap: directory account (profile only)
	UID      int    `json:"uid,omitempty"`
	GID      int    `json:"gid,omitempty"`
	// Groups: supplementary groups (groups of the interface, users, docker).
	Groups []string `json:"groups"`
	// Password and TwoFactor are Masked when set (the hashes are secrets).
	Password  string `json:"password,omitempty"`
	TwoFactor string `json:"twoFactor,omitempty"`
}

// Quota is the limit of a user on a volume or ZFS dataset.
type Quota struct {
	User       string `json:"user"`
	Kind       string `json:"kind"` // ext4 | xfs | zfs
	MountPoint string `json:"mountPoint"`
	Limit      int64  `json:"limit"` // bytes
}

// Volume is a storage space of the exported NAS (volume or ZFS pool).
type Volume struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	Kind       string `json:"kind"` // volume | zfs
	FSType     string `json:"fsType"`
	MountPoint string `json:"mountPoint"`
	mapped     string // mount point here, set by Remap
}

// Stack is a Docker Compose stack (an App Center app when App is set).
type Stack struct {
	Name    string `json:"name"`
	App     string `json:"app,omitempty"`
	Compose string `json:"compose"`
	Env     string `json:"env"`
}

// Guard is the address blocking: settings and manual blocks still running.
type Guard struct {
	Config guard.Config  `json:"config"`
	Blocks []guard.Block `json:"blocks,omitempty"`
}

// Errors of Parse and Open.
var (
	ErrFormat     = errors.New("this is not a StrataOS configuration file")
	ErrPassphrase = errors.New("wrong passphrase")
)

// Parse reads a document, refusing other files and newer layouts.
func Parse(data []byte) (*Document, error) {
	var d Document
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	if err := d.Check(); err != nil {
		return nil, err
	}
	return &d, nil
}

// Check verifies the format and version of a decoded document.
func (d *Document) Check() error {
	if d.Format != Format || d.Version < 1 {
		return ErrFormat
	}
	if d.Version > Version {
		return fmt.Errorf("this file comes from a newer StrataOS (%s, format %d): update this NAS first", d.StrataOS, d.Version)
	}
	return nil
}
