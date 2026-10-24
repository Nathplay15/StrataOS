package ceph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Status is the part of `ceph status -f json` StrataOS shows.
type Status struct {
	FSID    string `json:"fsid"`
	Health  Health `json:"health"`
	Quorum  []string
	Mons    int
	OSDs    int
	OSDsUp  int
	OSDsIn  int
	PGs     int
	PGState []PGState `json:"pgStates"`
	Pools   int
	Objects int64
	// Bytes
	Used, Avail, Total int64
	// Client I/O per second
	ReadBps, WriteBps, ReadOps, WriteOps int64
	// Recovery
	RecoveringBps   int64
	DegradedRatio   float64
	MisplacedRatio  float64
	DegradedObjects int64
	MgrAvailable    bool
	MgrStandbys     int
	MDS             []MDSRank
	MDSStandbys     int
	Progress        []Progress
}

// Health is the cluster health and its checks.
type Health struct {
	Status string  `json:"status"` // HEALTH_OK | HEALTH_WARN | HEALTH_ERR
	Checks []Check `json:"checks"`
}

// Check is one health check, with an explanation for non-specialists.
type Check struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Explain  string `json:"explain,omitempty"`
	Muted    bool   `json:"muted,omitempty"`
}

// PGState counts the placement groups in one state.
type PGState struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// MDSRank is an active metadata server.
type MDSRank struct {
	FS     string `json:"fs,omitempty"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Progress is a long event of the cluster (recovery, rebalancing).
type Progress struct {
	Message  string  `json:"message"`
	Progress float64 `json:"progress"`
}

// MarshalJSON gives Status lower camel case keys for the interface.
func (s Status) MarshalJSON() ([]byte, error) {
	type alias struct {
		FSID            string     `json:"fsid"`
		Health          Health     `json:"health"`
		Quorum          []string   `json:"quorum"`
		Mons            int        `json:"mons"`
		OSDs            int        `json:"osds"`
		OSDsUp          int        `json:"osdsUp"`
		OSDsIn          int        `json:"osdsIn"`
		PGs             int        `json:"pgs"`
		PGState         []PGState  `json:"pgStates"`
		Pools           int        `json:"pools"`
		Objects         int64      `json:"objects"`
		Used            int64      `json:"used"`
		Avail           int64      `json:"avail"`
		Total           int64      `json:"total"`
		ReadBps         int64      `json:"readBps"`
		WriteBps        int64      `json:"writeBps"`
		ReadOps         int64      `json:"readOps"`
		WriteOps        int64      `json:"writeOps"`
		RecoveringBps   int64      `json:"recoveringBps"`
		DegradedRatio   float64    `json:"degradedRatio"`
		MisplacedRatio  float64    `json:"misplacedRatio"`
		DegradedObjects int64      `json:"degradedObjects"`
		MgrAvailable    bool       `json:"mgrAvailable"`
		MgrStandbys     int        `json:"mgrStandbys"`
		MDS             []MDSRank  `json:"mds"`
		MDSStandbys     int        `json:"mdsStandbys"`
		Progress        []Progress `json:"progress"`
	}
	a := alias(s)
	for _, p := range []*[]string{&a.Quorum} {
		if *p == nil {
			*p = []string{}
		}
	}
	if a.PGState == nil {
		a.PGState = []PGState{}
	}
	if a.MDS == nil {
		a.MDS = []MDSRank{}
	}
	if a.Progress == nil {
		a.Progress = []Progress{}
	}
	if a.Health.Checks == nil {
		a.Health.Checks = []Check{}
	}
	return json.Marshal(a)
}

// ParseStatus reads `ceph status -f json`.
func ParseStatus(data []byte) (Status, error) {
	var raw struct {
		FSID   string `json:"fsid"`
		Health struct {
			Status string `json:"status"`
			Checks map[string]struct {
				Severity string `json:"severity"`
				Summary  struct {
					Message string `json:"message"`
				} `json:"summary"`
				Muted bool `json:"muted"`
			} `json:"checks"`
		} `json:"health"`
		QuorumNames []string `json:"quorum_names"`
		MonMap      struct {
			NumMons int `json:"num_mons"`
		} `json:"monmap"`
		OSDMap struct {
			NumOSDs   int `json:"num_osds"`
			NumUpOSDs int `json:"num_up_osds"`
			NumInOSDs int `json:"num_in_osds"`
		} `json:"osdmap"`
		PGMap struct {
			PGsByState []struct {
				StateName string `json:"state_name"`
				Count     int    `json:"count"`
			} `json:"pgs_by_state"`
			NumPGs          int     `json:"num_pgs"`
			NumPools        int     `json:"num_pools"`
			NumObjects      int64   `json:"num_objects"`
			BytesUsed       int64   `json:"bytes_used"`
			BytesAvail      int64   `json:"bytes_avail"`
			BytesTotal      int64   `json:"bytes_total"`
			ReadBps         int64   `json:"read_bytes_sec"`
			WriteBps        int64   `json:"write_bytes_sec"`
			ReadOps         int64   `json:"read_op_per_sec"`
			WriteOps        int64   `json:"write_op_per_sec"`
			RecoveringBps   int64   `json:"recovering_bytes_per_sec"`
			DegradedRatio   float64 `json:"degraded_ratio"`
			MisplacedRatio  float64 `json:"misplaced_ratio"`
			DegradedObjects int64   `json:"degraded_objects"`
		} `json:"pgmap"`
		FSMap struct {
			ByRank []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"by_rank"`
			Standby int `json:"up:standby"`
		} `json:"fsmap"`
		MgrMap struct {
			Available   bool `json:"available"`
			NumStandbys int  `json:"num_standbys"`
		} `json:"mgrmap"`
		ProgressEvents map[string]struct {
			Message  string  `json:"message"`
			Progress float64 `json:"progress"`
		} `json:"progress_events"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Status{}, fmt.Errorf("unexpected answer of ceph status: %w", err)
	}
	s := Status{
		FSID: raw.FSID, Quorum: raw.QuorumNames, Mons: raw.MonMap.NumMons,
		OSDs: raw.OSDMap.NumOSDs, OSDsUp: raw.OSDMap.NumUpOSDs, OSDsIn: raw.OSDMap.NumInOSDs,
		PGs: raw.PGMap.NumPGs, Pools: raw.PGMap.NumPools, Objects: raw.PGMap.NumObjects,
		Used: raw.PGMap.BytesUsed, Avail: raw.PGMap.BytesAvail, Total: raw.PGMap.BytesTotal,
		ReadBps: raw.PGMap.ReadBps, WriteBps: raw.PGMap.WriteBps, ReadOps: raw.PGMap.ReadOps, WriteOps: raw.PGMap.WriteOps,
		RecoveringBps: raw.PGMap.RecoveringBps, DegradedRatio: raw.PGMap.DegradedRatio, MisplacedRatio: raw.PGMap.MisplacedRatio,
		DegradedObjects: raw.PGMap.DegradedObjects,
		MgrAvailable:    raw.MgrMap.Available, MgrStandbys: raw.MgrMap.NumStandbys, MDSStandbys: raw.FSMap.Standby,
	}
	s.Health.Status = raw.Health.Status
	for code, c := range raw.Health.Checks {
		s.Health.Checks = append(s.Health.Checks, Check{Code: code, Severity: c.Severity, Message: c.Summary.Message, Explain: Explain(code), Muted: c.Muted})
	}
	// Errors first, then by code.
	sort.Slice(s.Health.Checks, func(i, j int) bool {
		a, b := s.Health.Checks[i], s.Health.Checks[j]
		if a.Severity != b.Severity {
			return a.Severity == "HEALTH_ERR"
		}
		return a.Code < b.Code
	})
	for _, p := range raw.PGMap.PGsByState {
		s.PGState = append(s.PGState, PGState{p.StateName, p.Count})
	}
	sort.Slice(s.PGState, func(i, j int) bool { return s.PGState[i].Count > s.PGState[j].Count })
	for _, r := range raw.FSMap.ByRank {
		s.MDS = append(s.MDS, MDSRank{Name: r.Name, Status: r.Status})
	}
	for _, p := range raw.ProgressEvents {
		msg, _, _ := strings.Cut(p.Message, "\n")
		s.Progress = append(s.Progress, Progress{Message: strings.TrimSpace(msg), Progress: p.Progress})
	}
	sort.Slice(s.Progress, func(i, j int) bool { return s.Progress[i].Message < s.Progress[j].Message })
	return s, nil
}

// Clean reports whether every placement group is active+clean.
func (s Status) Clean() bool {
	if s.PGs == 0 {
		return true
	}
	for _, p := range s.PGState {
		if p.State == "active+clean" && p.Count == s.PGs {
			return true
		}
	}
	return false
}

// explanations of the health checks an administrator meets most, in plain
// words: what it means and what to do.
var explanations = map[string]string{
	"OSD_DOWN":                 "A disk daemon (OSD) does not answer: its NAS is off or unreachable, the disk failed, or the daemon stopped. Data stays available from the other copies while enough remain.",
	"OSD_HOST_DOWN":            "Every disk of a NAS is unreachable: that NAS is probably off or disconnected from the cluster network.",
	"OSD_ROOT_DOWN":            "No disk of the cluster answers.",
	"OSD_OUT_OF_ORDER_FULL":    "The full thresholds of the cluster are out of order.",
	"OSD_NEARFULL":             "A disk is nearly full (85%). Add disks or delete data before writes are blocked.",
	"OSD_BACKFILLFULL":         "A disk is too full to receive moved data (90%). Add disks or delete data.",
	"OSD_FULL":                 "A disk is full (95%): writes are blocked. Add disks or delete data now.",
	"POOL_NEARFULL":            "A pool is nearly full.",
	"POOL_FULL":                "A pool is full: writes to it are blocked.",
	"PG_DEGRADED":              "Some data has fewer copies than wanted, usually because a disk or a NAS is down. Ceph copies it again by itself once the disk is back, or after 10 minutes on the other disks.",
	"PG_AVAILABILITY":          "Some data cannot be read or written right now: too many copies are missing. Bring back the missing NAS or disks.",
	"PG_DAMAGED":               "A consistency check found damaged data. Ceph can usually repair it from the other copies (repair from the Ceph page).",
	"OSD_SCRUB_ERRORS":         "A consistency check found errors on a disk. Check the disk health (SMART).",
	"PG_RECOVERY_FULL":         "Recovery is blocked because disks are full.",
	"PG_BACKFILL_FULL":         "Rebalancing is blocked because disks are full.",
	"PG_NOT_SCRUBBED":          "Some data has not been checked recently. The cluster is busy, or the checks are paused.",
	"PG_NOT_DEEP_SCRUBBED":     "Some data has not been deeply checked recently. The cluster is busy, or the checks are paused.",
	"TOO_FEW_PGS":              "Pools have few placement groups; the autoscaler normally fixes this.",
	"TOO_MANY_PGS":             "The disks hold too many placement groups. Add disks or lower the number of pools.",
	"TOO_FEW_OSDS":             "There are fewer disks than copies wanted: add disks so that every copy has a place.",
	"MON_DOWN":                 "A monitor does not answer: its NAS is off or unreachable. The cluster keeps working while more than half of the monitors answer.",
	"MON_CLOCK_SKEW":           "The clocks of the NAS differ. Make sure every NAS synchronises its time (NTP).",
	"MON_DISK_LOW":             "The system disk of a monitor is getting full (less than 30% free).",
	"MON_DISK_CRIT":            "The system disk of a monitor is almost full: free space now.",
	"MGR_DOWN":                 "No manager runs: statistics are not updated. Check the NAS of the cluster.",
	"MDS_ALL_DOWN":             "No metadata server runs: the CephFS filesystem is unavailable.",
	"MDS_UP_LESS_THAN_MAX":     "Fewer metadata servers are active than configured.",
	"MDS_INSUFFICIENT_STANDBY": "No spare metadata server is ready to take over.",
	"FS_DEGRADED":              "The CephFS filesystem is degraded: a metadata server is down or starting.",
	"FS_WITH_FAILED_MDS":       "A metadata server of the CephFS filesystem failed.",
	"OSDMAP_FLAGS":             "Cluster flags are set (for example noout during maintenance): Ceph does not rebalance data while they are set.",
	"OSD_FLAGS":                "Flags are set on some disks or NAS (for example noout during maintenance).",
	"POOL_NO_REDUNDANCY":       "A pool keeps a single copy of its data: one failed disk loses data. This is only acceptable for tests.",
	"POOL_APP_NOT_ENABLED":     "A pool is not tagged with its use (rbd, cephfs).",
	"RECENT_CRASH":             "A Ceph daemon crashed recently. It restarted; archive the report once the cause is known.",
	"AUTH_INSECURE_GLOBAL_ID_RECLAIM_ALLOWED": "The monitors accept old clients with a known security flaw.",
	"SLOW_OPS":                 "Requests take more than 30 seconds: a disk or the network is overloaded or failing.",
	"BLUESTORE_SLOW_OP_ALERT":  "A disk answers slowly.",
	"DEVICE_HEALTH":            "A disk is expected to fail soon (SMART): replace it.",
	"OBJECT_UNFOUND":           "Some data cannot be found on any disk. Bring back every NAS and disk that was turned off.",
	"OSD_NO_DOWN_OUT_INTERVAL": "Down disks are never marked out: data is not copied elsewhere when a disk fails.",
}

// Explain returns the plain explanation of a health check ("" when unknown).
func Explain(code string) string { return explanations[code] }

// OSD is a disk daemon as `ceph osd tree` and `ceph osd df` show it.
type OSD struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Host        string  `json:"host"`
	Class       string  `json:"class"`
	Status      string  `json:"status"` // up | down | destroyed
	In          bool    `json:"in"`
	Weight      float64 `json:"weight"` // CRUSH weight (TiB)
	Reweight    float64 `json:"reweight"`
	Size        int64   `json:"size"`
	Used        int64   `json:"used"`
	Utilization float64 `json:"utilization"`
	PGs         int     `json:"pgs"`
	Device      string  `json:"device,omitempty"` // from osd metadata
}

// ParseOSDTree reads `ceph osd tree -f json` into OSDs with their host.
func ParseOSDTree(data []byte) ([]OSD, error) {
	var raw struct {
		Nodes []struct {
			ID          int     `json:"id"`
			Name        string  `json:"name"`
			Type        string  `json:"type"`
			Class       string  `json:"device_class"`
			Children    []int   `json:"children"`
			Status      string  `json:"status"`
			CrushWeight float64 `json:"crush_weight"`
			Reweight    float64 `json:"reweight"`
		} `json:"nodes"`
		Stray []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"stray"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unexpected answer of ceph osd tree: %w", err)
	}
	host := map[int]string{}
	for _, n := range raw.Nodes {
		if n.Type == "host" {
			for _, c := range n.Children {
				host[c] = n.Name
			}
		}
	}
	var out []OSD
	for _, n := range raw.Nodes {
		if n.Type != "osd" {
			continue
		}
		out = append(out, OSD{ID: n.ID, Name: n.Name, Host: host[n.ID], Class: n.Class, Status: n.Status, In: n.Reweight > 0, Weight: n.CrushWeight, Reweight: n.Reweight})
	}
	for _, n := range raw.Stray {
		out = append(out, OSD{ID: n.ID, Name: n.Name, Status: n.Status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// MergeOSDDF adds the usage of `ceph osd df -f json` to the OSDs.
func MergeOSDDF(osds []OSD, data []byte) error {
	var raw struct {
		Nodes []struct {
			ID          int     `json:"id"`
			KB          int64   `json:"kb"`
			KBUsed      int64   `json:"kb_used"`
			Utilization float64 `json:"utilization"`
			PGs         int     `json:"pgs"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unexpected answer of ceph osd df: %w", err)
	}
	for _, n := range raw.Nodes {
		for i := range osds {
			if osds[i].ID == n.ID {
				osds[i].Size, osds[i].Used, osds[i].Utilization, osds[i].PGs = n.KB*1024, n.KBUsed*1024, n.Utilization, n.PGs
			}
		}
	}
	return nil
}

// MergeOSDMetadata adds the disk of each OSD from `ceph osd metadata -f json`.
func MergeOSDMetadata(osds []OSD, data []byte) error {
	var raw []struct {
		ID       int    `json:"id"`
		Hostname string `json:"hostname"`
		Devices  string `json:"devices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unexpected answer of ceph osd metadata: %w", err)
	}
	for _, m := range raw {
		for i := range osds {
			if osds[i].ID == m.ID {
				if m.Devices != "" {
					osds[i].Device = "/dev/" + strings.Split(m.Devices, ",")[0]
				}
				if osds[i].Host == "" {
					osds[i].Host = m.Hostname
				}
			}
		}
	}
	return nil
}

// Daemon is a monitor, manager or metadata server.
type Daemon struct {
	Kind   string `json:"kind"` // mon | mgr | mds
	Name   string `json:"name"`
	Addr   string `json:"addr,omitempty"`
	Status string `json:"status"` // up (in quorum / active) | standby | down
}

// ParseMonDump reads `ceph mon dump -f json` with the quorum of the status.
func ParseMonDump(data []byte, quorum []string) ([]Daemon, error) {
	var raw struct {
		Mons []struct {
			Name string `json:"name"`
			Addr string `json:"addr"`
		} `json:"mons"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unexpected answer of ceph mon dump: %w", err)
	}
	in := map[string]bool{}
	for _, q := range quorum {
		in[q] = true
	}
	var out []Daemon
	for _, m := range raw.Mons {
		st := "down"
		if in[m.Name] {
			st = "up"
		}
		addr, _, _ := strings.Cut(m.Addr, "/")
		out = append(out, Daemon{Kind: "mon", Name: m.Name, Addr: addr, Status: st})
	}
	return out, nil
}

// ParseMgrDump reads `ceph mgr dump -f json`.
func ParseMgrDump(data []byte) ([]Daemon, error) {
	var raw struct {
		ActiveName string `json:"active_name"`
		Available  bool   `json:"available"`
		Standbys   []struct {
			Name string `json:"name"`
		} `json:"standbys"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unexpected answer of ceph mgr dump: %w", err)
	}
	var out []Daemon
	if raw.ActiveName != "" {
		st := "up"
		if !raw.Available {
			st = "down"
		}
		out = append(out, Daemon{Kind: "mgr", Name: raw.ActiveName, Status: st})
	}
	for _, s := range raw.Standbys {
		out = append(out, Daemon{Kind: "mgr", Name: s.Name, Status: "standby"})
	}
	return out, nil
}

// ParseFSDump reads the metadata servers of `ceph fs dump -f json`.
func ParseFSDump(data []byte) ([]Daemon, error) {
	var raw struct {
		Standbys []struct {
			Name string `json:"name"`
		} `json:"standbys"`
		Filesystems []struct {
			MDSMap struct {
				FSName string `json:"fs_name"`
				Info   map[string]struct {
					Name  string `json:"name"`
					State string `json:"state"`
				} `json:"info"`
			} `json:"mdsmap"`
		} `json:"filesystems"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unexpected answer of ceph fs dump: %w", err)
	}
	var out []Daemon
	for _, f := range raw.Filesystems {
		for _, i := range f.MDSMap.Info {
			st := "standby"
			if i.State == "up:active" {
				st = "up"
			}
			out = append(out, Daemon{Kind: "mds", Name: i.Name, Addr: f.MDSMap.FSName, Status: st})
		}
	}
	for _, s := range raw.Standbys {
		out = append(out, Daemon{Kind: "mds", Name: s.Name, Status: "standby"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Pool is a pool with its usage.
type Pool struct {
	Name        string   `json:"name"`
	ID          int      `json:"id"`
	Size        int      `json:"size"`
	MinSize     int      `json:"minSize"`
	PGs         int      `json:"pgs"`
	Autoscale   string   `json:"autoscale"`
	Apps        []string `json:"apps"`
	Stored      int64    `json:"stored"`
	Used        int64    `json:"used"`
	MaxAvail    int64    `json:"maxAvail"`
	Objects     int64    `json:"objects"`
	PercentUsed float64  `json:"percentUsed"`
}

// ParsePools reads `ceph osd pool ls detail -f json`.
func ParsePools(data []byte) ([]Pool, error) {
	var raw []struct {
		Name      string                     `json:"pool_name"`
		ID        int                        `json:"pool_id"`
		Size      int                        `json:"size"`
		MinSize   int                        `json:"min_size"`
		PGs       int                        `json:"pg_num"`
		Autoscale string                     `json:"pg_autoscale_mode"`
		Apps      map[string]json.RawMessage `json:"application_metadata"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unexpected answer of ceph osd pool ls: %w", err)
	}
	out := []Pool{}
	for _, p := range raw {
		pl := Pool{Name: p.Name, ID: p.ID, Size: p.Size, MinSize: p.MinSize, PGs: p.PGs, Autoscale: p.Autoscale, Apps: []string{}}
		for a := range p.Apps {
			pl.Apps = append(pl.Apps, a)
		}
		sort.Strings(pl.Apps)
		out = append(out, pl)
	}
	return out, nil
}

// MergeDF adds the usage of `ceph df -f json` to the pools.
func MergeDF(pools []Pool, data []byte) error {
	var raw struct {
		Pools []struct {
			ID    int `json:"id"`
			Stats struct {
				Stored      int64   `json:"stored"`
				Used        int64   `json:"bytes_used"`
				MaxAvail    int64   `json:"max_avail"`
				Objects     int64   `json:"objects"`
				PercentUsed float64 `json:"percent_used"`
			} `json:"stats"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unexpected answer of ceph df: %w", err)
	}
	for _, d := range raw.Pools {
		for i := range pools {
			if pools[i].ID == d.ID {
				st := d.Stats
				pools[i].Stored, pools[i].Used, pools[i].MaxAvail, pools[i].Objects, pools[i].PercentUsed = st.Stored, st.Used, st.MaxAvail, st.Objects, st.PercentUsed*100
			}
		}
	}
	return nil
}

// FS is a CephFS filesystem.
type FS struct {
	Name         string   `json:"name"`
	MetadataPool string   `json:"metadataPool"`
	DataPools    []string `json:"dataPools"`
}

// ParseFSList reads `ceph fs ls -f json`.
func ParseFSList(data []byte) ([]FS, error) {
	var raw []struct {
		Name      string   `json:"name"`
		Metadata  string   `json:"metadata_pool"`
		DataPools []string `json:"data_pools"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unexpected answer of ceph fs ls: %w", err)
	}
	out := []FS{}
	for _, f := range raw {
		out = append(out, FS{Name: f.Name, MetadataPool: f.Metadata, DataPools: f.DataPools})
	}
	return out, nil
}

// HasFlag tells whether `ceph osd dump -f json` lists a cluster flag.
func HasFlag(osdDump []byte, flag string) bool {
	var raw struct {
		Flags string `json:"flags"`
	}
	if json.Unmarshal(osdDump, &raw) != nil {
		return false
	}
	for _, f := range strings.Split(raw.Flags, ",") {
		if f == flag {
			return true
		}
	}
	return false
}
