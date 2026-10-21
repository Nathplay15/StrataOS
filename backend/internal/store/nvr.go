package store

import "time"

// NVR is the video surveillance of the Surveillance app. Nothing runs until
// a camera is added.
type NVR struct {
	// Folder receives the recordings of the built-in recorder (one subfolder
	// per camera) and Frigate's when it is installed from the app.
	Folder string `json:"folder,omitempty"`
	// AlertMinutes: a camera unreachable this long is notified (0 = 5).
	AlertMinutes int `json:"alertMinutes,omitempty"`
	// SegmentSeconds: length of the recorded files (0 = 10 minutes; API only, for tests).
	SegmentSeconds int          `json:"segmentSeconds,omitempty"`
	Cameras        []*Camera    `json:"cameras,omitempty"`
	Frigate        FrigateState `json:"frigate,omitzero"`
}

// Camera is a network camera. The credentials are kept apart from the URLs
// and never sent back to the browser.
type Camera struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`              // main stream (recorded), without credentials
	SubURL   string `json:"subUrl,omitempty"` // optional low resolution stream (live view, detection)
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Enabled  bool   `json:"enabled"`
	// Engine records the camera: "" (built-in recorder) or "frigate".
	Engine string `json:"engine,omitempty"`
	Audio  bool   `json:"audio,omitempty"` // built-in recorder: keep the sound (re-encoded to AAC)
	// Retention: days kept and total size in GB (0 = no limit).
	KeepDays  int       `json:"keepDays,omitempty"`
	MaxGB     float64   `json:"maxGb,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// FrigateState holds the options of the Frigate engine.
type FrigateState struct {
	// Detector: "" (auto) | cpu | openvino | edgetpu.
	Detector string   `json:"detector,omitempty"`
	Objects  []string `json:"objects,omitempty"` // tracked objects (default: person, car)
	// Notify sends a notification on new events, at most one per camera every
	// NotifyMinutes (0 = 10).
	Notify        bool `json:"notify,omitempty"`
	NotifyMinutes int  `json:"notifyMinutes,omitempty"`
}
