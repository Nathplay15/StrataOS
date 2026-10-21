package store

import "time"

// ISCSI is the iSCSI target service (Linux kernel target, LIO).
type ISCSI struct {
	Enabled bool           `json:"enabled,omitempty"`
	Targets []*ISCSITarget `json:"targets,omitempty"`
}

// ISCSITarget is one target with its LUNs and access rules.
type ISCSITarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"` // short name, also names the LIO backstores
	IQN      string `json:"iqn"`
	PortalIP string `json:"portalIp,omitempty"` // "" = every address (0.0.0.0)
	Port     int    `json:"port,omitempty"`     // 0 = 3260
	// AllowAny accepts every initiator (no ACL): unsafe, for a trusted network.
	AllowAny   bool        `json:"allowAny,omitempty"`
	Initiators []string    `json:"initiators,omitempty"` // allowed initiator names; empty = nobody
	CHAP       ISCSICHAP   `json:"chap"`
	LUNs       []*ISCSILUN `json:"luns,omitempty"`
	CreatedAt  time.Time   `json:"createdAt"`
}

// ISCSICHAP holds the CHAP credentials; CHAP is required when User is set.
type ISCSICHAP struct {
	User         string `json:"user,omitempty"`
	Secret       string `json:"secret,omitempty"`
	MutualUser   string `json:"mutualUser,omitempty"` // mutual CHAP: the target proves itself too
	MutualSecret string `json:"mutualSecret,omitempty"`
}

// ISCSILUN is a logical unit and its backing store.
type ISCSILUN struct {
	LUN        int    `json:"lun"`
	Kind       string `json:"kind"` // fileio | lvm | zvol
	Path       string `json:"path"` // fileio: the image file; lvm: vg/lv; zvol: pool/volume
	Size       int64  `json:"size,omitempty"`
	WriteCache bool   `json:"writeCache,omitempty"` // fileio only: buffered writes (faster, unsafe on power loss)
	ReadOnly   bool   `json:"readOnly,omitempty"`
	Serial     string `json:"serial"` // SCSI unit serial: initiators recognise the disk by it
	Created    bool   `json:"created,omitempty"`
}
