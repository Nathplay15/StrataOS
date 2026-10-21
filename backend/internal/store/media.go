package store

// Media holds the settings of the Media app: the DLNA server and the music
// server (OwnTone) folders. Casting keeps no state.
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
