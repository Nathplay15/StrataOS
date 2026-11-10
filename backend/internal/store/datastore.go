package store

import "time"

// Datastore is a backup repository (restic) that several backup tasks, and
// several NAS, write to: identical data is stored once whatever task, VM or
// NAS it comes from. Its location takes the fields of a restic destination.
type Datastore struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// DestType: local | sftp | s3 | rest | strataos (vault of another NAS).
	DestType   string `json:"destType"`
	DestPath   string `json:"destPath"`
	Host       string `json:"host,omitempty"`
	User       string `json:"user,omitempty"`
	Port       int    `json:"port,omitempty"`
	S3Endpoint string `json:"s3Endpoint,omitempty"`
	AccessKey  string `json:"accessKey,omitempty"`
	SecretKey  string `json:"secretKey,omitempty"`
	Password   string `json:"password,omitempty"`
	VaultURL   string `json:"vaultUrl,omitempty"`
	VaultToken string `json:"vaultToken,omitempty"`
	VaultCert  string `json:"vaultCert,omitempty"`
	AppendOnly bool   `json:"appendOnly,omitempty"`
	// Offsite: a local destination kept away from the NAS (USB disk).
	Offsite bool      `json:"offsite,omitempty"`
	Created time.Time `json:"created"`

	// Verify: scheduled integrity checks; VerifyNext is the data subset
	// (1..Subsets) the next check reads, so that every pack file is read
	// once every Subsets runs.
	Verify     VerifyPolicy `json:"verify"`
	VerifyNext int          `json:"verifyNext,omitempty"`
	// Verifies: last checks, newest first (MaxVerifies).
	Verifies []VerifyRun `json:"verifies,omitempty"`
	// PruneCron: when unused data is removed (forget runs after each backup).
	PruneCron string    `json:"pruneCron,omitempty"`
	LastPrune time.Time `json:"lastPrune,omitzero"`
	// Stats: last measured sizes (refreshed after backups and on demand).
	Stats *DatastoreStats `json:"stats,omitempty"`
}

// VerifyPolicy says when a datastore is checked.
type VerifyPolicy struct {
	Off  bool   `json:"off,omitempty"`
	Cron string `json:"cron,omitempty"` // "" = weekly (DefaultVerifyCron)
	// Subsets: the data is read over this many runs (1: all of it each
	// time, 0 = DefaultVerifySubsets).
	Subsets int `json:"subsets,omitempty"`
}

const (
	DefaultVerifyCron    = "40 3 * * 0"
	DefaultVerifySubsets = 4
	DefaultPruneCron     = "10 4 * * *"
	MaxVerifies          = 20
)

// VerifyRun is the outcome of one integrity check.
type VerifyRun struct {
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration"`
	Status   string    `json:"status"` // success | failed
	Subset   string    `json:"subset"` // "2/4", "all", or "" (structure only)
	Manual   bool      `json:"manual,omitempty"`
	Message  string    `json:"message"`
	Errors   []string  `json:"errors,omitempty"`   // first errors found
	Broken   []string  `json:"broken,omitempty"`   // damaged pack files
	Affected []string  `json:"affected,omitempty"` // snapshots holding data of those packs
}

// DatastoreStats are the sizes of a datastore.
type DatastoreStats struct {
	Updated   time.Time `json:"updated"`
	Snapshots int       `json:"snapshots"`
	// Logical: what the snapshots hold, summed (what restoring all of them
	// would write); Unique: the deduplicated data before compression;
	// Stored: the data as stored (compressed, encrypted).
	Logical int64 `json:"logical"`
	Unique  int64 `json:"unique"`
	Stored  int64 `json:"stored"`
	// Dedup = Logical / Unique, Ratio = Logical / Stored.
	Dedup float64 `json:"dedup"`
	Ratio float64 `json:"ratio"`
}

func (st *State) Datastore(id string) *Datastore {
	for _, d := range st.Datastores {
		if d.ID == id {
			return d
		}
	}
	return nil
}
