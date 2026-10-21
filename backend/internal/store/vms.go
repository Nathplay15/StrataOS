package store

// VMSettings are the virtual machine settings StrataOS keeps itself (the rest
// lives in the libvirt definitions).
type VMSettings struct {
	// Autostart lists the VMs StrataOS starts with the NAS, in order.
	Autostart []VMAutostart `json:"autostart,omitempty"`
	// TookOver is set once the libvirt autostart flags have been imported.
	TookOver bool `json:"tookOver,omitempty"`
}

type VMAutostart struct {
	Name  string `json:"name"`
	Order int    `json:"order"`
	Delay int    `json:"delay"` // seconds to wait after starting it
}
