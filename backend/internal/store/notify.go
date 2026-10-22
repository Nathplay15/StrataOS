package store

// NotifyChannel holds the delivery preferences of one notification channel.
// The zero value (enabled, every level) is what channels configured before
// 0.6.0 get, so an upgrade changes nothing.
type NotifyChannel struct {
	// Disabled pauses the channel and keeps its configuration.
	Disabled bool `json:"disabled,omitempty"`
	// MinLevel is the lowest level sent: "" or "info" (everything), "warning", "error".
	MinLevel string `json:"minLevel,omitempty"`
}

type NotifyChannels struct {
	Email    NotifyChannel `json:"email"`
	Telegram NotifyChannel `json:"telegram"`
	Ntfy     NotifyChannel `json:"ntfy"`
	Webhook  NotifyChannel `json:"webhook"`
	// Mobile: phones paired with the StrataOS app (UnifiedPush).
	Mobile NotifyChannel `json:"mobile,omitzero"`
}

// QuietHours is a daily window (local time, may cross midnight) during which
// only errors are sent; notifications held back are dropped, not delayed.
type QuietHours struct {
	Enabled bool   `json:"enabled,omitempty"`
	Start   string `json:"start,omitempty"` // HH:MM
	End     string `json:"end,omitempty"`   // HH:MM
	// SilenceAll drops errors too during the window.
	SilenceAll bool `json:"silenceAll,omitempty"`
}
