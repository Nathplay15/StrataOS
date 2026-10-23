package store

// Console is the appliance screen on the machine's own display (tty1).
// The zero value is the default: the screen on, recovery codes allowed.
type Console struct {
	// Off gives tty1 back to a plain login prompt.
	Off bool `json:"off,omitempty"`
	// Decided: an administrator chose, so the screen is not left off on
	// machines with a graphical login (which may use tty1).
	Decided bool `json:"decided,omitempty"`
	// NoRecovery forbids administrator recovery codes from the console.
	NoRecovery bool `json:"noRecovery,omitempty"`
}
