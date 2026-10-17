// Package store persists StrataOS state (users, shares, volumes, settings, events) as JSON.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"strataos/internal/network"
	"strataos/internal/sys"
	"strataos/internal/ups"
)

type User struct {
	Username     string    `json:"username"`
	FullName     string    `json:"fullName"`
	Email        string    `json:"email"`
	Role         string    `json:"role"` // admin | user
	PasswordHash string    `json:"passwordHash"`
	Disabled     bool      `json:"disabled"`
	SSH          bool      `json:"ssh"`
	CreatedAt    time.Time `json:"createdAt"`
	// Two-factor authentication (TOTP). Secrets never leave the server.
	TOTPSecret    string   `json:"totpSecret,omitempty"`
	TOTPPending   string   `json:"totpPending,omitempty"`   // being enrolled, not active yet
	TOTPLast      int64    `json:"totpLast,omitempty"`      // last accepted time step (anti-replay)
	RecoveryCodes []string `json:"recoveryCodes,omitempty"` // bcrypt hashes, consumed on use
}

type Share struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description"`
	SMB         bool   `json:"smb"`
	NFS         bool   `json:"nfs"`
	Guest       bool   `json:"guest"`
	Browseable  bool   `json:"browseable"`
	RecycleBin  bool   `json:"recycleBin"`
	TimeMachine bool   `json:"timeMachine"`
	// Permissions maps a username or "@group" to "rw", "ro" or "none".
	Permissions map[string]string `json:"permissions"`
	NFSClients  string            `json:"nfsClients"` // e.g. "192.168.1.0/24"
	NFSOptions  string            `json:"nfsOptions"` // e.g. "rw,sync,no_subtree_check"
	CreatedAt   time.Time         `json:"createdAt"`
}

type Volume struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Devices    []string  `json:"devices"`
	FSType     string    `json:"fsType"`
	UUID       string    `json:"uuid"`
	MountPoint string    `json:"mountPoint"`
	Profile    string    `json:"profile,omitempty"` // btrfs data profile
	CreatedAt  time.Time `json:"createdAt"`
}

type Event struct {
	Time     time.Time `json:"time"`
	Level    string    `json:"level"` // info | warning | error
	Category string    `json:"category"`
	Message  string    `json:"message"`
	User     string    `json:"user,omitempty"`
}

type Settings struct {
	Workgroup    string `json:"workgroup"`
	ServerString string `json:"serverString"`
	SessionHours int    `json:"sessionHours"`
	WebhookURL   string `json:"webhookUrl"`
	SMTPHost     string `json:"smtpHost"`
	SMTPPort     int    `json:"smtpPort"`
	SMTPSecurity string `json:"smtpSecurity"` // auto | tls | starttls | none
	SMTPUser     string `json:"smtpUser"`
	SMTPPassword string `json:"smtpPassword,omitempty"`
	SMTPFrom     string `json:"smtpFrom"`
	NotifyEmail  string `json:"notifyEmail"`
	SMBMinProto  string `json:"smbMinProtocol"`
	TrashDays    int    `json:"trashDays"`
	ACMEDomain   string `json:"acmeDomain"`
	ACMEEmail    string `json:"acmeEmail"`
}

// Schedule is a periodic maintenance task (snapshot, scrub or SMART test).
type Schedule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"` // snapshot | scrub | smart
	Cron    string `json:"cron"`
	Enabled bool   `json:"enabled"`
	// TargetKind is zfs | btrfs | mdadm | disks; Target is a dataset, pool, volume ID,
	// array name, or "all" / a disk path for SMART tests.
	TargetKind string        `json:"targetKind"`
	Target     string        `json:"target"`
	Subvolume  string        `json:"subvolume,omitempty"` // btrfs: snapshot only this subvolume
	Recursive  bool          `json:"recursive,omitempty"` // zfs: include child datasets
	KeepCount  int           `json:"keepCount,omitempty"` // retention: newest N snapshots
	KeepDays   int           `json:"keepDays,omitempty"`  // retention: snapshots younger than N days
	SmartTest  string        `json:"smartTest,omitempty"` // short | long
	CreatedAt  time.Time     `json:"createdAt"`
	History    []ScheduleRun `json:"history"`
}

type ScheduleRun struct {
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration"` // seconds
	Status   string    `json:"status"`   // success | failed
	Message  string    `json:"message"`
	Manual   bool      `json:"manual,omitempty"`
}

const MaxScheduleHistory = 30

// BackupTask copies data elsewhere: rsync mirror, restic versioned backup or ZFS replication.
type BackupTask struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`    // rsync | restic | zfs
	Sources []string `json:"sources"` // folders under /srv/strata, or one ZFS dataset
	// Destination. DestType: local | ssh (rsync, zfs) ; local | sftp | s3 | rest (restic).
	DestType    string        `json:"destType"`
	DestPath    string        `json:"destPath"` // folder, remote path, ZFS dataset, bucket path or REST URL
	Host        string        `json:"host,omitempty"`
	User        string        `json:"user,omitempty"`
	Port        int           `json:"port,omitempty"`
	S3Endpoint  string        `json:"s3Endpoint,omitempty"`
	AccessKey   string        `json:"accessKey,omitempty"`
	SecretKey   string        `json:"secretKey,omitempty"`
	Password    string        `json:"password,omitempty"` // restic repository password
	Delete      bool          `json:"delete,omitempty"`   // rsync: remove files deleted at the source
	Excludes    []string      `json:"excludes,omitempty"`
	Recursive   bool          `json:"recursive,omitempty"` // zfs: include child datasets
	KeepLast    int           `json:"keepLast,omitempty"`
	KeepDaily   int           `json:"keepDaily,omitempty"`
	KeepWeekly  int           `json:"keepWeekly,omitempty"`
	KeepMonthly int           `json:"keepMonthly,omitempty"`
	Cron        string        `json:"cron"`
	Enabled     bool          `json:"enabled"`
	CreatedAt   time.Time     `json:"createdAt"`
	History     []ScheduleRun `json:"history"`
}

// FileServices are the optional file access protocols.
type FileServices struct {
	WebDAV        bool `json:"webdav"`
	FTP           bool `json:"ftp"`
	FTPRequireTLS bool `json:"ftpRequireTls"`
	FTPPassiveMin int  `json:"ftpPassiveMin"`
	FTPPassiveMax int  `json:"ftpPassiveMax"`
	SFTP          bool `json:"sftp"`
}

// CloudSync keeps a folder of the NAS in sync with a cloud storage (rclone).
type CloudSync struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"` // s3 | b2 | drive | onedrive | dropbox | webdav | sftp
	// Params holds the provider settings, Secrets the credentials (never sent back to the UI).
	Params     map[string]string `json:"params"`
	Secrets    map[string]string `json:"secrets,omitempty"`
	LocalPath  string            `json:"localPath"`  // folder under /srv/strata
	RemotePath string            `json:"remotePath"` // bucket/folder or folder in the cloud
	Direction  string            `json:"direction"`  // upload | download | bisync
	Mirror     bool              `json:"mirror"`     // one-way: also delete what was deleted at the source
	Excludes   []string          `json:"excludes,omitempty"`
	BWLimit    string            `json:"bwLimit,omitempty"` // e.g. 10M
	Cron       string            `json:"cron"`
	Enabled    bool              `json:"enabled"`
	// Synced is set once a two-way sync has been initialised (rclone bisync --resync).
	Synced    bool          `json:"synced,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	History   []ScheduleRun `json:"history"`
}

type State struct {
	Users     []*User       `json:"users"`
	Shares    []*Share      `json:"shares"`
	Volumes   []*Volume     `json:"volumes"`
	Schedules []*Schedule   `json:"schedules"`
	Backups   []*BackupTask `json:"backups"`
	Clouds    []*CloudSync  `json:"cloudSyncs"`
	Files     FileServices  `json:"fileServices"`
	// Network is the configuration StrataOS applied (links it manages).
	Network  *network.Config `json:"network,omitempty"`
	UPS      *ups.Config     `json:"ups,omitempty"`
	Events   []Event         `json:"events"`
	Settings Settings        `json:"settings"`
}

const maxEvents = 2000

// Store guards State behind a mutex and persists it on every mutation.
type Store struct {
	mu   sync.RWMutex
	path string
	st   State
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Holds password hashes, sessions, the TLS key and the SMTP password.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "state.json")}
	s.st.Settings = Settings{Workgroup: "WORKGROUP", ServerString: "StrataOS", SessionHours: 12, SMBMinProto: "SMB2_10", SMTPPort: 587, TrashDays: 30}
	data, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(data, &s.st); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(&s.st, "", "  ")
	if err != nil {
		return err
	}
	return sys.WriteFileAtomic(s.path, data, 0o600)
}

// Read runs fn with a read lock.
func (s *Store) Read(fn func(st *State)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.st)
}

// Update runs fn with a write lock and persists the state if fn succeeds.
func (s *Store) Update(fn func(st *State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.st); err != nil {
		return err
	}
	return s.saveLocked()
}

// LogLocked records an event from inside an Update callback (the lock is held).
func (s *Store) LogLocked(st *State, level, category, user, msg string) {
	st.Events = append(st.Events, Event{Time: time.Now(), Level: level, Category: category, User: user, Message: msg})
	if len(st.Events) > maxEvents {
		st.Events = st.Events[len(st.Events)-maxEvents:]
	}
}

// Log records an audit/system event.
func (s *Store) Log(level, category, user, msg string) {
	_ = s.Update(func(st *State) error {
		st.Events = append(st.Events, Event{Time: time.Now(), Level: level, Category: category, User: user, Message: msg})
		if len(st.Events) > maxEvents {
			st.Events = st.Events[len(st.Events)-maxEvents:]
		}
		return nil
	})
}

func (st *State) User(name string) *User {
	for _, u := range st.Users {
		if u.Username == name {
			return u
		}
	}
	return nil
}

func (st *State) Share(name string) *Share {
	for _, sh := range st.Shares {
		if sh.Name == name {
			return sh
		}
	}
	return nil
}

func (st *State) Backup(id string) *BackupTask {
	for _, b := range st.Backups {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (st *State) Cloud(id string) *CloudSync {
	for _, c := range st.Clouds {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (st *State) Schedule(id string) *Schedule {
	for _, s := range st.Schedules {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (st *State) Volume(id string) *Volume {
	for _, v := range st.Volumes {
		if v.ID == id {
			return v
		}
	}
	return nil
}
