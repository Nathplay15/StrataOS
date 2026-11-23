package store

import "time"

// Migration imports data from another NAS (SMB, NFS or rsync daemon) into a
// shared folder of this NAS, with an optional schedule (roadmap 0.15,
// "migration assistant").
type Migration struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is the protocol of the source: smb | nfs | rsync (rsync daemon).
	Kind  string `json:"kind"`
	Host  string `json:"host"`
	Share string `json:"share"`          // SMB share, NFS export or rsync module
	Path  string `json:"path,omitempty"` // folder inside the share/export/module
	Port  int    `json:"port,omitempty"` // rsync daemon port (0 = 873)
	// Credentials of the source, kept apart and never sent back to the browser.
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	// Dest is the destination inside this NAS: "/Share/folder".
	Dest   string `json:"dest"`
	Delete bool   `json:"delete,omitempty"` // rsync --delete: remove what the source lacks
	// Cron repeats the import (empty = once, run manually).
	Cron    string `json:"cron,omitempty"`
	Enabled bool   `json:"enabled"`

	CreatedAt time.Time      `json:"createdAt"`
	History   []MigrationRun `json:"history"`
}

// MigrationRun is one execution of a migration.
type MigrationRun struct {
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration"`
	Status   string    `json:"status"` // success | failed | stopped
	Message  string    `json:"message"`
	Manual   bool      `json:"manual,omitempty"`
	Files    int64     `json:"files,omitempty"`
	Bytes    int64     `json:"bytes,omitempty"`
}

// Migration returns the migration with this id, or nil.
func (st *State) Migration(id string) *Migration {
	for _, m := range st.Migrations {
		if m.ID == id {
			return m
		}
	}
	return nil
}
