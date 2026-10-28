package store

import "time"

// Cluster is the group of StrataOS NAS this one belongs to (Ceph storage
// today, VM clustering later). Members trust each other: they share Token
// (sent in X-Strata-Cluster on peer calls) and pin each other's certificate.
type Cluster struct {
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Token   string          `json:"token,omitempty"`
	Self    string          `json:"self,omitempty"` // ID of this NAS among Members
	Members []ClusterMember `json:"members,omitempty"`
	// Networks (CIDR): Public carries client and monitor traffic, Private the
	// replication between nodes (optional, same as Public when empty).
	PublicNet  string    `json:"publicNet,omitempty"`
	PrivateNet string    `json:"privateNet,omitempty"`
	Created    time.Time `json:"created,omitzero"`
	// VMs: the VM cluster settings every member holds a copy of.
	VMs VMCluster `json:"vms,omitzero"`
}

// VMCluster is the shared table of the VM cluster: high availability, nodes
// in maintenance, the CPU model of cluster VMs. Every member keeps a copy;
// Version orders them (a change is committed only when a majority of the
// members accepted it, see internal/vmcluster).
type VMCluster struct {
	Version int64 `json:"version,omitempty"`
	// HA lists the VMs restarted on another member when their node fails.
	HA []ClusterHAVM `json:"ha,omitempty"`
	// Maintenance: IDs of the members in maintenance (no VM is placed there).
	Maintenance []string `json:"maintenance,omitempty"`
	// CPUModel of the VMs created while in the cluster: "" (the host's own
	// CPU, fastest, live migration only between identical CPUs) or a named
	// QEMU model all members can run (e.g. "Haswell-noTSX").
	CPUModel string `json:"cpuModel,omitempty"`
}

// ClusterHAVM is a VM under high availability.
type ClusterHAVM struct {
	Name      string `json:"name"`
	Owner     string `json:"owner"`               // member ID that runs (or holds) it
	Preferred string `json:"preferred,omitempty"` // member ID chosen first on a restart
	// Running: last state its owner reported (only running VMs are restarted).
	Running bool `json:"running,omitempty"`
	// Recover: the owner was declared dead; the new owner must start it.
	Recover bool `json:"recover,omitempty"`
	// Migrating: member ID it is being migrated to (its copy there is expected).
	Migrating string `json:"migrating,omitempty"`
	// XML: inactive libvirt definition, to define it on another member.
	XML string `json:"xml,omitempty"`
}

// ClusterMember is one NAS of the cluster.
type ClusterMember struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`               // hostname, also the Ceph daemon name
	Host     string    `json:"host"`               // address of its web API (port 7443)
	Cert     string    `json:"cert"`               // SHA-256 of its HTTPS certificate
	Addr     string    `json:"addr,omitempty"`     // its address in PublicNet
	PrivAddr string    `json:"privAddr,omitempty"` // its address in PrivateNet
	Roles    []string  `json:"roles,omitempty"`    // mon, mgr, mds, osd…
	Joined   time.Time `json:"joined,omitzero"`
}

// Ceph is what this NAS knows of Ceph: the cluster it is a node of (Local)
// and the Ceph storage it uses (its own cluster or external ones).
type Ceph struct {
	Local    *CephLocal     `json:"local,omitempty"`
	External []CephExternal `json:"external,omitempty"`
	Volumes  []CephVolume   `json:"volumes,omitempty"`
	VMPools  []CephVMPool   `json:"vmPools,omitempty"`
	// LastHealth: last health status seen (alerts on change).
	LastHealth string `json:"lastHealth,omitempty"`
}

// CephLocal is the Ceph cluster formed by the NAS of the Cluster.
type CephLocal struct {
	FSID    string    `json:"fsid"`
	Mons    []CephMon `json:"mons"`
	Test    bool      `json:"test,omitempty"`
	Size    int       `json:"size"`
	MinSize int       `json:"minSize"`
	Created time.Time `json:"created"`
	// PublicNet/ClusterNet as given to ceph.conf.
	PublicNet  string `json:"publicNet"`
	ClusterNet string `json:"clusterNet,omitempty"`
	// SingleHost: copies on different disks rather than NAS (one-NAS test).
	SingleHost bool `json:"singleHost,omitempty"`
	// SmallMemory: daemon caches bounded for NAS with little RAM.
	SmallMemory bool `json:"smallMemory,omitempty"`
}

// CephMon is a monitor of the local cluster: member name and address.
type CephMon struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}

// CephExternal is a Ceph cluster managed elsewhere, used as storage. Its key
// lives only in /etc/ceph/strata (root only), never in the state.
type CephExternal struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Mons  []string  `json:"mons"`
	User  string    `json:"user"` // cephx user without "client."
	FSID  string    `json:"fsid,omitempty"`
	Added time.Time `json:"added"`
}

// CephVolume links a StrataOS volume to the Ceph storage behind it.
type CephVolume struct {
	VolumeID string `json:"volumeId"`
	Cluster  string `json:"cluster"` // "local" or a CephExternal ID
	Kind     string `json:"kind"`    // cephfs | rbd
	FS       string `json:"fs,omitempty"`
	Path     string `json:"path,omitempty"` // folder inside the filesystem
	Pool     string `json:"pool,omitempty"`
	Image    string `json:"image,omitempty"`
	// Created: StrataOS created the RBD image (deleted with the volume on request).
	Created bool `json:"created,omitempty"`
}

// CephVMPool is a libvirt RBD storage pool that holds VM disks.
type CephVMPool struct {
	Name    string `json:"name"` // libvirt pool name
	Cluster string `json:"cluster"`
	Pool    string `json:"pool"`
	User    string `json:"user"`
	Secret  string `json:"secret"` // UUID of the libvirt secret holding the key
}
