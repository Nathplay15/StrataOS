package ondemand

import "errors"

// ProviderOptions say where and under which name the shared folders show.
type ProviderOptions struct {
	Root    string // Linux: mount point; Windows: sync root folder (macOS: chosen by the system)
	Account string // account ID: Windows sync root ID and macOS domain identifier
	Name    string // display name ("StrataOS - NAS")
	// Shell: the app's shell registers the Windows sync root with Explorer
	// (StorageProviderSyncRootManager); without it (headless) the agent
	// registers a plain one itself.
	Shell bool
}

// ProviderInfo is the state of a provider for the window.
type ProviderInfo struct {
	Kind  string `json:"kind"`            // "fuse", "cfapi", "fileprovider"
	Root  string `json:"root,omitempty"`  // the folder the user opens
	State string `json:"state"`           // "ready", "register" (Windows: waiting for the shell), "error"
	Error string `json:"error,omitempty"` //
	// Windows: what the shell registers.
	SyncRootID string `json:"syncRootId,omitempty"`
}

// Provider plugs a Service into the file manager of the system.
type Provider interface {
	Info() ProviderInfo
	// Pin keeps a path on this device (on) or frees it, as the file
	// manager's own menu does (Windows: pin state; Linux: the cache).
	Pin(p string, on bool) error
	Close() error
}

// ErrUnsupported: this system has no files on demand.
var ErrUnsupported = errors.New("files on demand is not available on this system")

// Supported tells whether StartProvider can work here (Kind: which one).
func Supported() (bool, string) { return supported() }

// UsesCache tells whether the Service keeps the downloaded bytes itself
// (Linux) or the system does (Windows placeholders, macOS File Provider).
func UsesCache() bool {
	_, k := supported()
	return k == "fuse"
}
