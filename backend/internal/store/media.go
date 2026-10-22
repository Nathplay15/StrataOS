package store

import "slices"

// Media holds the settings of the Media app: the DLNA server and the music
// server (OwnTone) folders (by share name: they follow renamed and deleted
// shares, see FollowShare).
type Media struct {
	DLNA  DLNAServer  `json:"dlna,omitzero"`
	Music MusicServer `json:"music,omitzero"`
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
