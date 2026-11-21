package store

// Transcoding is how videos use the graphics processor (Hardware page).
type Transcoding struct {
	// Accel: "" (automatic: the GPU when one encodes H.264) or "off".
	Accel string `json:"accel,omitempty"`
	// Device: the render node chosen ("" = automatic).
	Device string `json:"device,omitempty"`
	// Apps: App Center stacks given the graphics card (/dev/dri).
	Apps []string `json:"apps,omitempty"`
}
