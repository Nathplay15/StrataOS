// Package store persists StrataOS state (users, shares, volumes, settings, events) as JSON.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strataos/internal/energy"
	"strataos/internal/guard"
	"sync"
	"time"

	"strataos/internal/network"
	"strataos/internal/sys"
	"strataos/internal/ups"
)

type User struct {
	Username     string `json:"username"`
	FullName     string `json:"fullName"`
	Email        string `json:"email"`
	Role         string `json:"role"` // admin | user
	PasswordHash string `json:"passwordHash"`
	Disabled     bool   `json:"disabled"`
	SSH          bool   `json:"ssh"`
	// Source is "" for local accounts, "ad" or "ldap" for directory accounts
	// (created at their first sign-in, password checked by the directory).
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
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
	// TimeMachineMaxGB caps the space Macs may use on a Time Machine share (0 = no limit).
	TimeMachineMaxGB float64 `json:"timeMachineMaxGb,omitempty"`
	// Permissions maps a username or "@group" to "rw", "ro" or "none".
	Permissions map[string]string `json:"permissions"`
	NFSClients  string            `json:"nfsClients"` // e.g. "192.168.1.0/24"
	NFSOptions  string            `json:"nfsOptions"` // e.g. "rw,sync,no_subtree_check"
	// Quota: size limit in GB (0 = none); with QuotaBlock the share becomes
	// read-only while full. Used/QuotaFull are measured by StrataOS.
	QuotaGB    float64   `json:"quotaGb,omitempty"`
	QuotaBlock bool      `json:"quotaBlock,omitempty"`
	QuotaFull  bool      `json:"quotaFull,omitempty"`
	Used       int64     `json:"used,omitempty"`
	UsedAt     time.Time `json:"usedAt,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	// Encrypted: the files live encrypted in CipherDir (gocryptfs) and appear
	// at Path only while unlocked; Locked is filled in when listed.
	// Frozen: read-only on every protocol after a trap file was touched
	// (ransomware protection), until an administrator releases it.
	Frozen    bool   `json:"frozen,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
	CipherDir string `json:"cipherDir,omitempty"`
	Locked    bool   `json:"locked,omitempty"`
}

// DataPath is the folder holding the share's data on disk: the encrypted
// store for an encrypted share (what replication and backups must copy).
func (s *Share) DataPath() string {
	if s.Encrypted && s.CipherDir != "" {
		return s.CipherDir
	}
	return s.Path
}

// ReadOnly: over its quota, or frozen by the ransomware protection.
func (s *Share) ReadOnly() bool { return s.QuotaFull || s.Frozen }

// Writable reports whether a permission allows writing, a full or frozen share being read-only.
func (s *Share) Writable(perm string) bool { return perm == "rw" && !s.ReadOnly() }

type Volume struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Devices    []string `json:"devices"`
	FSType     string   `json:"fsType"`
	UUID       string   `json:"uuid"`
	MountPoint string   `json:"mountPoint"`
	Profile    string   `json:"profile,omitempty"` // btrfs data profile
	// ManagedVG: the volume sits on an LVM group StrataOS made for it (SSD cache).
	ManagedVG string    `json:"managedVg,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
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
	// Telegram bot (token from @BotFather) and the chat that receives alerts.
	TelegramToken  string `json:"telegramToken,omitempty"`
	TelegramChatID string `json:"telegramChatId,omitempty"`
	// Push notifications through ntfy: topic URL (https://ntfy.sh/<topic> or a
	// self-hosted server) and an optional access token.
	NtfyURL   string `json:"ntfyUrl,omitempty"`
	NtfyToken string `json:"ntfyToken,omitempty"`
	// Per-channel switch and minimum level, and quiet hours (store/notify.go).
	NotifyChannels NotifyChannels `json:"notifyChannels"`
	QuietHours     QuietHours     `json:"quietHours"`
	// MetricsToken enables /metrics (Prometheus) for clients sending it.
	MetricsToken string `json:"metricsToken,omitempty"`
	// NotifyLang is the language of notifications and reports ("" = en).
	NotifyLang string `json:"notifyLang,omitempty"`
	// MonthlyReport sends a health report on the 1st of each month.
	MonthlyReport bool   `json:"monthlyReport,omitempty"`
	SMBMinProto   string `json:"smbMinProtocol"`
	TrashDays     int    `json:"trashDays"`
	ACMEDomain    string `json:"acmeDomain"`
	ACMEEmail     string `json:"acmeEmail"`
}

// Schedule is a periodic maintenance task (snapshot, scrub or SMART test).
type Schedule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"` // snapshot | scrub | smart | appupdate | antivirus
	Cron    string `json:"cron"`
	Enabled bool   `json:"enabled"`
	// TargetKind is zfs | btrfs | mdadm | disks; Target is a dataset, pool, volume ID,
	// array name, or "all" / a disk path for SMART tests.
	TargetKind string `json:"targetKind"`
	Target     string `json:"target"`
	Subvolume  string `json:"subvolume,omitempty"` // btrfs: snapshot only this subvolume
	Recursive  bool   `json:"recursive,omitempty"` // zfs: include child datasets
	KeepCount  int    `json:"keepCount,omitempty"` // retention: newest N snapshots
	KeepDays   int    `json:"keepDays,omitempty"`  // retention: snapshots younger than N days
	LockDays   int    `json:"lockDays,omitempty"`  // snapshots cannot be deleted for N days (immutable)
	SmartTest  string `json:"smartTest,omitempty"` // short | long
	// Antivirus: what to do with infected files (report | quarantine).
	AVAction  string        `json:"avAction,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	History   []ScheduleRun `json:"history"`
}

type ScheduleRun struct {
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration"` // seconds
	Status   string    `json:"status"`   // success | failed
	Message  string    `json:"message"`
	Manual   bool      `json:"manual,omitempty"`
}

const MaxScheduleHistory = 30

// BackupTask copies data elsewhere: rsync mirror, restic versioned backup, ZFS
// replication or two-way sync. With a SourceType, the task pulls from another
// NAS (Host, User, Port, RemotePassword describe it) into a local destination.
type BackupTask struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`    // rsync | restic | zfs | sync
	Sources []string `json:"sources"` // folders under /srv/strata, or one ZFS dataset (remote ones when pulling)
	// SourceType: "" (this NAS) | ssh | rsyncd | smb (rsync) | ssh (zfs).
	SourceType     string `json:"sourceType,omitempty"`
	RemotePassword string `json:"remotePassword,omitempty"` // rsync daemon or SMB password
	Domain         string `json:"domain,omitempty"`         // SMB domain or workgroup
	BWLimit        string `json:"bwLimit,omitempty"`        // e.g. 10M (rsync, rclone)
	// Versions: snapshot the destination after each pull (Btrfs/ZFS), keeping
	// KeepLast snapshots and/or KeepDays days. Two-way syncs always snapshot.
	Versions bool `json:"versions,omitempty"`
	KeepDays int  `json:"keepDays,omitempty"`
	// Synced is set once a two-way sync has been initialised (rclone bisync --resync).
	Synced bool `json:"synced,omitempty"`
	// Destination. DestType: local | ssh (rsync, zfs) ; local | sftp | s3 | rest (restic) ; sftp (sync).
	DestType   string `json:"destType"`
	DestPath   string `json:"destPath"` // folder, remote path, ZFS dataset, bucket path or REST URL
	Host       string `json:"host,omitempty"`
	User       string `json:"user,omitempty"`
	Port       int    `json:"port,omitempty"`
	S3Endpoint string `json:"s3Endpoint,omitempty"`
	AccessKey  string `json:"accessKey,omitempty"`
	SecretKey  string `json:"secretKey,omitempty"`
	Password   string `json:"password,omitempty"` // restic repository password
	Delete     bool   `json:"delete,omitempty"`   // rsync: remove files deleted at the source
	// Vault on another StrataOS (DestType "strataos"): REST URL, access token
	// and pinned certificate (SHA-256), append-only when the vault forbids deletions.
	VaultURL    string        `json:"vaultUrl,omitempty"`
	VaultToken  string        `json:"vaultToken,omitempty"`
	VaultCert   string        `json:"vaultCert,omitempty"`
	AppendOnly  bool          `json:"appendOnly,omitempty"`
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

// Directory is the domain the NAS belongs to: Active Directory (Samba/winbind
// member server) or an LDAP server (sssd).
type Directory struct {
	Type      string `json:"type"`      // "" | ad | ldap
	Realm     string `json:"realm"`     // AD: DNS domain, e.g. CORP.EXAMPLE.COM
	Workgroup string `json:"workgroup"` // AD: NetBIOS domain name, e.g. CORP
	Server    string `json:"server"`    // AD: domain controller (optional, DNS otherwise)
	// LDAP
	URI          string `json:"uri,omitempty"`
	BaseDN       string `json:"baseDn,omitempty"`
	BindDN       string `json:"bindDn,omitempty"`
	BindPassword string `json:"bindPassword,omitempty"`
	StartTLS     bool   `json:"startTls,omitempty"`
	// AdminGroup: members of this directory group are StrataOS administrators.
	AdminGroup string    `json:"adminGroup,omitempty"`
	JoinedAt   time.Time `json:"joinedAt"`
}

// HA pairs two StrataOS NAS: the primary replicates to the secondary, which
// takes over a shared virtual IP address (keepalived/VRRP) when it fails.
type HA struct {
	Role       string    `json:"role"`               // "" | primary | secondary
	PeerHost   string    `json:"peerHost"`           // address of the other NAS
	PeerName   string    `json:"peerName"`           // its host name
	PeerCert   string    `json:"peerCert"`           // SHA-256 of its HTTPS certificate (pinned at pairing)
	VIP        string    `json:"vip"`                // shared address with prefix, e.g. 192.168.1.50/24
	Interface  string    `json:"interface"`          // network interface carrying the VIP
	VRID       int       `json:"vrid"`               // VRRP router ID
	AuthPass   string    `json:"authPass,omitempty"` // VRRP password
	Token      string    `json:"token,omitempty"`    // shared secret for calls between the two NAS
	Interval   int       `json:"interval"`           // replication period, minutes
	LastSync   time.Time `json:"lastSync"`
	LastResult string    `json:"lastResult"`
	PairedAt   time.Time `json:"pairedAt"`
	// Maintenance: the primary handed the address over on purpose (planned
	// reboot); AutoReturn: the secondary gives it back as soon as the primary
	// is back, after copying the changes.
	Maintenance bool `json:"maintenance,omitempty"`
	AutoReturn  bool `json:"autoReturn,omitempty"`
	// Continuous: changed folders are copied a few seconds after the change
	// (the periodic replication still runs, as a safety net).
	Continuous bool `json:"continuous,omitempty"`
	// More than two NAS: the VRRP priority of this secondary (100, 90…), the
	// addresses of every other member, and (on a secondary, and on the
	// primary for its first secondary) the volume mapping: primary volume
	// mount point → mount point on the secondary.
	Priority int               `json:"priority,omitempty"`
	Peers    []string          `json:"peers,omitempty"`
	PathMap  map[string]string `json:"pathMap,omitempty"`
	// Extra secondaries of the primary (the first one is PeerHost…).
	Extra []HASecondary `json:"extra,omitempty"`
}

// HASecondary is a secondary NAS beyond the first one.
type HASecondary struct {
	Host       string            `json:"host"`
	Name       string            `json:"name"`
	Cert       string            `json:"cert"`
	Priority   int               `json:"priority"`
	PathMap    map[string]string `json:"pathMap,omitempty"`
	LastSync   time.Time         `json:"lastSync"`
	LastResult string            `json:"lastResult"`
}

// Fleet: the NAS this one watches (Members) and those allowed to watch it
// (Managers, read-only access by token).
type Fleet struct {
	Members  []FleetMember  `json:"members,omitempty"`
	Managers []FleetManager `json:"managers,omitempty"`
}

type FleetMember struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Host  string    `json:"host"`
	Cert  string    `json:"cert"` // SHA-256 of its certificate, pinned at pairing
	Token string    `json:"token,omitempty"`
	Added time.Time `json:"added"`
}

type FleetManager struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"tokenHash,omitempty"`
	Created   time.Time `json:"created"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`
}

// Vault receives the backups of other StrataOS NAS (restic REST protocol);
// each paired NAS gets its own repository folder and token.
type Vault struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	AppendOnly bool          `json:"appendOnly"` // clients can add, never delete
	Created    time.Time     `json:"created"`
	Clients    []VaultClient `json:"clients"`
}

// VaultClient is a NAS allowed to write to a vault.
type VaultClient struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"tokenHash,omitempty"`
	Created   time.Time `json:"created"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`
	Size      int64     `json:"size"`
}

// Guard is the automatic blocking of addresses (and countries).
type Guard struct {
	Config guard.Config  `json:"config"`
	Blocks []guard.Block `json:"blocks,omitempty"`
}

// Energy holds the power saving settings.
type Energy struct {
	SpindownMin int             `json:"spindownMin"` // 0 = disks never sleep
	Governor    string          `json:"governor,omitempty"`
	Schedule    energy.Schedule `json:"schedule"`
	WoL         []string        `json:"wol,omitempty"` // interfaces with Wake-on-LAN on
	Devices     []WakeDevice    `json:"devices,omitempty"`
}

// WakeDevice is a machine the NAS can wake up (Wake-on-LAN).
type WakeDevice struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
}

// Photos lists the folders indexed by the Photos app and the albums.
type Photos struct {
	Folders []string `json:"folders"`
	Albums  []*Album `json:"albums,omitempty"`
}

// Album is a named selection of photos of one user.
type Album struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Owner   string    `json:"owner"`
	Items   []string  `json:"items"` // file paths
	Created time.Time `json:"created"`
}

// ShareLink gives access to a file, a folder or an album without an account.
// It acts with the rights of its owner, checked again on every request.
type ShareLink struct {
	ID           string    `json:"id"` // random, part of the URL
	Owner        string    `json:"owner"`
	Path         string    `json:"path,omitempty"`
	Album        string    `json:"album,omitempty"` // album ID instead of a path
	Dir          bool      `json:"dir"`
	Download     bool      `json:"download"` // files can be seen and downloaded
	Upload       bool      `json:"upload"`   // folders only: visitors can drop files
	PasswordHash string    `json:"passwordHash,omitempty"`
	Expires      time.Time `json:"expires,omitzero"`
	MaxDownloads int       `json:"maxDownloads,omitempty"`
	Downloads    int       `json:"downloads"`
	Uploads      int       `json:"uploads"`
	Created      time.Time `json:"created"`
	LastUsed     time.Time `json:"lastUsed,omitzero"`
}

// VPN is the WireGuard server of the NAS.
type VPN struct {
	Enabled    bool      `json:"enabled"`
	Port       int       `json:"port"`
	Subnet     string    `json:"subnet"`   // e.g. 10.8.0.0/24
	Endpoint   string    `json:"endpoint"` // public name or address devices connect to
	LAN        string    `json:"lan"`      // local network reachable in "lan" mode, e.g. 192.168.1.0/24
	PrivateKey string    `json:"privateKey,omitempty"`
	PublicKey  string    `json:"publicKey"`
	Peers      []VPNPeer `json:"peers"`
}

// VPNPeer is a device allowed to connect.
type VPNPeer struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Mode         string    `json:"mode"` // nas | lan | all
	IP           string    `json:"ip"`
	PublicKey    string    `json:"publicKey"`
	PrivateKey   string    `json:"privateKey,omitempty"`
	PresharedKey string    `json:"presharedKey,omitempty"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"createdAt"`
}

// DDNS keeps a host name pointing at the public address.
type DDNS struct {
	Provider   string    `json:"provider"` // "" | duckdns | cloudflare | dynv6 | custom
	Hostname   string    `json:"hostname"`
	Token      string    `json:"token,omitempty"`
	ZoneID     string    `json:"zoneId,omitempty"`
	URL        string    `json:"url,omitempty"` // custom: update URL with {ip}
	LastIP     string    `json:"lastIp"`
	LastUpdate time.Time `json:"lastUpdate"`
	LastResult string    `json:"lastResult"`
}

// Ransomware: detection of mass renames/deletions over SMB (Samba audit log).
type Ransomware struct {
	Enabled   bool     `json:"enabled"`
	Threshold int      `json:"threshold"` // renames + deletions per user per minute
	Block     bool     `json:"block"`     // block the account and close its sessions
	Blocked   []string `json:"blocked"`   // accounts blocked by the protection
	// Traps puts decoy files in the shares: touching one freezes the share.
	Traps bool `json:"traps,omitempty"`
	// NFSPaused: NFS exports suspended after mass changes through NFS.
	NFSPaused bool `json:"nfsPaused,omitempty"`
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
	// Schema is the layout version of the state file (0 before StrataOS 0.5).
	Schema    int           `json:"schema,omitempty"`
	Users     []*User       `json:"users"`
	Shares    []*Share      `json:"shares"`
	Volumes   []*Volume     `json:"volumes"`
	Schedules []*Schedule   `json:"schedules"`
	Backups   []*BackupTask `json:"backups"`
	Clouds    []*CloudSync  `json:"cloudSyncs"`
	Files     FileServices  `json:"fileServices"`
	Directory Directory     `json:"directory"`
	HA        HA            `json:"ha"`
	Ransom    Ransomware    `json:"ransomware"`
	VPN       VPN           `json:"vpn"`
	Photos    Photos        `json:"photos"`
	DDNS      DDNS          `json:"ddns"`
	Links     []*ShareLink  `json:"links,omitempty"`
	Energy    Energy        `json:"energy"`
	Guard     Guard         `json:"guard"`
	Vaults    []*Vault      `json:"vaults,omitempty"`
	Fleet     Fleet         `json:"fleet"`
	Remote    Remote        `json:"remote,omitzero"`
	ISCSI     ISCSI         `json:"iscsi"`
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

// schema is the current layout version of the state file.
const schema = 1

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
	s.st.Schema = schema
	data, err := os.ReadFile(s.path)
	if err == nil {
		s.st.Schema = 0
		if err := json.Unmarshal(data, &s.st); err != nil {
			return nil, err
		}
		// Before 0.5, notifications were in French by default.
		if s.st.Schema < 1 && s.st.Settings.NotifyLang == "" && len(s.st.Users) > 0 {
			s.st.Settings.NotifyLang = "fr"
		}
		s.st.Schema = schema
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
