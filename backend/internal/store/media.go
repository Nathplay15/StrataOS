package store

import (
	"slices"
	"time"
)

// Media holds the settings of the Media app: the DLNA server and the music
// server (OwnTone) folders (by share name: they follow renamed and deleted
// shares, see FollowShare).
type Media struct {
	DLNA  DLNAServer  `json:"dlna,omitzero"`
	Music MusicServer `json:"music,omitzero"`
	// Positions: where each account stopped watching a video (user → file
	// path → position), so "continue watching" follows the viewer from one
	// screen to the next. A finished video (95% seen) has no position.
	Positions map[string]map[string]PlayPos `json:"positions,omitempty"`
	// Night: limits brightness and volume during the evening hours.
	Night NightMode `json:"night,omitzero"`
	// WatchLists are the "watch together" lists: videos shared between
	// accounts (roadmap 0.15, cinema).
	WatchLists []WatchList `json:"watchLists,omitempty"`
}

// WatchList is a named list of videos shared with other accounts.
type WatchList struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Owner   string    `json:"owner"`
	Items   []string  `json:"items"`          // video paths, as the files API spells them
	With    []string  `json:"with,omitempty"` // accounts the list is shared with
	Created time.Time `json:"created"`
}

// NightMode limits the players late in the evening (roadmap 0.15, cinema
// polish): from From to To (local hours, From > To wraps past midnight).
type NightMode struct {
	Enabled    bool `json:"enabled,omitempty"`
	From       int  `json:"from,omitempty"`       // hour 0-23
	To         int  `json:"to,omitempty"`         // hour 0-23
	MaxVolume  int  `json:"maxVolume,omitempty"`  // percent
	Brightness int  `json:"brightness,omitempty"` // percent of the picture
}

// Active reports whether the night mode applies at t (local time).
func (n NightMode) Active(t time.Time) bool {
	if !n.Enabled || n.From == n.To {
		return false
	}
	h := t.Hour()
	if n.From < n.To {
		return h >= n.From && h < n.To
	}
	return h >= n.From || h < n.To // wraps past midnight
}

// PlayPos is the playback position of one video for one account.
type PlayPos struct {
	At       float64   `json:"at"`  // seconds
	Duration float64   `json:"dur"` // seconds
	Updated  time.Time `json:"upd"`
}

// DLNAServer is the ReadyMedia (minidlna) server.
type DLNAServer struct {
	Enabled   bool         `json:"enabled,omitempty"`
	Name      string       `json:"name,omitempty"`      // shown by TVs ("" = host name)
	Interface string       `json:"interface,omitempty"` // "" = all interfaces
	Folders   []DLNAFolder `json:"folders,omitempty"`
}

// DLNAFolder publishes a shared folder (by name) as one kind of content.
type DLNAFolder struct {
	Share string `json:"share"`
	Type  string `json:"type,omitempty"` // "" (all), video, audio, pictures
}

// MusicServer is the integration of the OwnTone app.
type MusicServer struct {
	Shares []string `json:"shares,omitempty"` // shared folders scanned for music
	// Users lets every user (not only administrators) control the music.
	Users bool `json:"users,omitempty"`
}

// FollowShare keeps the media settings on a shared folder that was renamed
// (to is its new name) or deleted (to == ""). It reports whether the DLNA
// folders and the music folders referred to it.
func (m *Media) FollowShare(from, to string) (dlna, music bool) {
	folders := m.DLNA.Folders[:0:0]
	for _, f := range m.DLNA.Folders {
		if f.Share == from {
			dlna = true
			if to == "" {
				continue
			}
			f.Share = to
		}
		folders = append(folders, f)
	}
	if dlna {
		m.DLNA.Folders = folders
	}
	shares := m.Music.Shares[:0:0]
	for _, n := range m.Music.Shares {
		if n == from {
			music = true
			if to == "" {
				continue
			}
			n = to
		}
		shares = append(shares, n)
	}
	if music {
		m.Music.Shares = shares
	}
	return dlna, music
}

// UsesShare reports whether the DLNA server and the music server read a
// shared folder.
func (m *Media) UsesShare(name string) (dlna, music bool) {
	for _, f := range m.DLNA.Folders {
		dlna = dlna || f.Share == name
	}
	return dlna, slices.Contains(m.Music.Shares, name)
}
