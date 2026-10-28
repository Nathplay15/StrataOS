// Package config holds the settings of the app (config.json in its
// folder, readable by the user only: it holds the device token).
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"strataos-sync/internal/engine"
)

// Config is the state of the app.
type Config struct {
	Server      string        `json:"server,omitempty"`      // https://host:7443
	Fingerprint string        `json:"fingerprint,omitempty"` // pinned certificate
	Token       string        `json:"token,omitempty"`       // device token (stdev_…)
	User        string        `json:"user,omitempty"`
	NASName     string        `json:"nasName,omitempty"`
	DeviceName  string        `json:"deviceName,omitempty"`
	Pairs       []engine.Pair `json:"pairs,omitempty"`
	Ignore      []string      `json:"ignore,omitempty"` // nil: the defaults
	UpKBps      int64         `json:"upKBps,omitempty"` // 0: unlimited
	DownKBps    int64         `json:"downKBps,omitempty"`
	Paused      bool          `json:"paused,omitempty"`
	Language    string        `json:"language,omitempty"` // "" = the system's
	Autostart   bool          `json:"autostart,omitempty"`
	NoNotify    bool          `json:"noNotify,omitempty"`
	// Configured once: the first launch turns start at login on.
	Initialized bool `json:"initialized,omitempty"`
}

// Paired tells whether the app is signed in to a NAS.
func (c *Config) Paired() bool { return c.Server != "" && c.Token != "" }

// Store reads and writes the configuration of a folder.
type Store struct {
	Dir string
	mu  sync.Mutex
}

// DefaultDir is the folder of the app in the user's configuration.
func DefaultDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "StrataOS Sync")
}

func (s *Store) file() string { return filepath.Join(s.Dir, "config.json") }

// StateFile is the engine database.
func (s *Store) StateFile() string { return filepath.Join(s.Dir, "state.db") }

// TrashDir is where deleted files go when the system trash fails.
func (s *Store) TrashDir() string { return filepath.Join(s.Dir, "trash") }

// LogFile is the log of the app.
func (s *Store) LogFile() string { return filepath.Join(s.Dir, "strataos-sync.log") }

// Load reads the configuration (empty when there is none yet).
func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Config
	b, err := os.ReadFile(s.file())
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// Save writes the configuration atomically.
func (s *Store) Save(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
}

// Update loads, changes and saves the configuration.
func (s *Store) Update(f func(*Config) error) (Config, error) {
	c, err := s.Load()
	if err != nil {
		return c, err
	}
	if err := f(&c); err != nil {
		return c, err
	}
	return c, s.Save(c)
}

// NewID returns a random pair ID.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
