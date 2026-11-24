package store

// HDMIConsole is the graphical menu StrataOS shows on a TV plugged to its
// HDMI output (a kiosk browser on the connected display). The zero value is
// the default: the menu runs whenever a display is connected.
type HDMIConsole struct {
	// Off disables the HDMI menu (the output stays dark).
	Off bool `json:"off,omitempty"`
	// Decided: an administrator chose, so the menu is not left on by default.
	Decided bool `json:"decided,omitempty"`
	// Account: the TV shows the cinema library of this account (what it may
	// read, its rating cap), read-only. Empty: the TV shows nothing.
	Account string `json:"account,omitempty"`
	// Remote: accounts besides administrators that may drive the TV menu.
	Remote []string `json:"remote,omitempty"`
}
