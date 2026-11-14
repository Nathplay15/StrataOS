package store

import "time"

// Transcoding is how videos use the graphics processor (Hardware page).
type Transcoding struct {
	// Accel: "" (automatic: the GPU when one encodes H.264) or "off".
	Accel string `json:"accel,omitempty"`
	// Device: the render node chosen ("" = automatic).
	Device string `json:"device,omitempty"`
	// Apps: App Center stacks given the graphics card (/dev/dri).
	Apps []string `json:"apps,omitempty"`
}

// HomeAssistant links the NAS to Home Assistant through an MQTT broker
// (MQTT discovery).
type HomeAssistant struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	TLS      bool   `json:"tls,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"` // secret
	// Prefix: Home Assistant's discovery prefix ("" = homeassistant).
	Prefix string `json:"prefix,omitempty"`
	// IntervalSec between two state updates ("" = 60).
	IntervalSec int `json:"intervalSec,omitempty"`
	// Commands Home Assistant may send.
	AllowPower   bool `json:"allowPower,omitempty"`   // restart, shutdown
	AllowBackups bool `json:"allowBackups,omitempty"` // run a backup task
	AllowWake    bool `json:"allowWake,omitempty"`    // wake a device or a NAS of the fleet
	// Published: discovery topics sent, removed when turned off or when
	// their entity goes away.
	Published []string `json:"published,omitempty"`
	// Stack: the Mosquitto app it was connected to by the App Center ("" otherwise).
	Stack string `json:"stack,omitempty"`
	// LastError of the connection, and when it last worked.
	LastError     string    `json:"lastError,omitempty"`
	LastConnected time.Time `json:"lastConnected,omitzero"`
}
