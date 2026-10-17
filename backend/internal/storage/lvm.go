package storage

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"strataos/internal/sys"
)

type PV struct {
	Name string `json:"name"`
	VG   string `json:"vg"`
	Size int64  `json:"size"`
	Free int64  `json:"free"`
}

type VG struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Free    int64  `json:"free"`
	PVCount int    `json:"pvCount"`
	LVCount int    `json:"lvCount"`
	PVs     []PV   `json:"pvs"`
	LVs     []LV   `json:"lvs"`
}

type LV struct {
	Name     string     `json:"name"`
	VG       string     `json:"vg"`
	Path     string     `json:"path"`
	Size     int64      `json:"size"`
	Attr     string     `json:"attr"`
	Type     string     `json:"type"`
	Origin   string     `json:"origin,omitempty"`
	DataPct  string     `json:"dataPercent,omitempty"`
	FSType   string     `json:"fsType,omitempty"`
	Snapshot bool       `json:"snapshot"`
	Cache    *CacheInfo `json:"cache,omitempty"`
}

func lvmReport(cmd string, fields string) ([]map[string]string, error) {
	if !sys.Has(cmd) {
		return nil, nil
	}
	out, err := sys.Run(cmd, "--reportformat", "json", "--units", "b", "--nosuffix", "-o", fields)
	if err != nil {
		return nil, err
	}
	var res struct {
		Report []map[string][]map[string]string `json:"report"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	var rows []map[string]string
	for _, r := range res.Report {
		for _, v := range r {
			rows = append(rows, v...)
		}
	}
	return rows, nil
}

func num(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// ListLVM returns volume groups with their physical and logical volumes.
func ListLVM() ([]VG, error) {
	vgRows, err := lvmReport("vgs", "vg_name,vg_size,vg_free,pv_count,lv_count")
	if err != nil {
		return nil, err
	}
	pvRows, _ := lvmReport("pvs", "pv_name,vg_name,pv_size,pv_free")
	lvRows, _ := lvmReport("lvs", "lv_name,vg_name,lv_path,lv_size,lv_attr,segtype,origin,data_percent")
	vgs := []VG{}
	for _, r := range vgRows {
		vg := VG{Name: r["vg_name"], Size: num(r["vg_size"]), Free: num(r["vg_free"]), PVCount: int(num(r["pv_count"])), LVCount: int(num(r["lv_count"])), PVs: []PV{}, LVs: []LV{}}
		for _, p := range pvRows {
			if p["vg_name"] == vg.Name {
				vg.PVs = append(vg.PVs, PV{Name: p["pv_name"], VG: vg.Name, Size: num(p["pv_size"]), Free: num(p["pv_free"])})
			}
		}
		for _, l := range lvRows {
			if l["vg_name"] != vg.Name {
				continue
			}
			lv := LV{Name: l["lv_name"], VG: vg.Name, Path: l["lv_path"], Size: num(l["lv_size"]), Attr: l["lv_attr"], Type: l["segtype"], Origin: l["origin"], DataPct: l["data_percent"]}
			lv.Snapshot = strings.HasPrefix(lv.Attr, "s")
			if lv.Path != "" {
				lv.FSType, _, _ = Probe(lv.Path)
			}
			if lv.Type == "cache" {
				lv.Cache = LVCache(vg.Name, lv.Name)
			}
			vg.LVs = append(vg.LVs, lv)
		}
		vgs = append(vgs, vg)
	}
	return vgs, nil
}

// pvVG returns the VG name a PV belongs to.
func pvVG(dev string) string {
	if !sys.Has("pvs") {
		return ""
	}
	out, err := sys.Run("pvs", "--noheadings", "-o", "vg_name", dev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func CreateVG(name string, devices []string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := ValidateFreeDisks(devices, false); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	for _, d := range devices {
		if err := Wipe(d); err != nil {
			return err
		}
		if _, err := sys.Run("pvcreate", "-ff", "-y", d); err != nil {
			return err
		}
	}
	_, err := sys.Run("vgcreate", append([]string{name}, devices...)...)
	return err
}

func ExtendVG(name string, devices []string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := ValidateFreeDisks(devices, false); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	for _, d := range devices {
		if err := Wipe(d); err != nil {
			return err
		}
		if _, err := sys.Run("pvcreate", "-ff", "-y", d); err != nil {
			return err
		}
	}
	_, err := sys.Run("vgextend", append([]string{name}, devices...)...)
	return err
}

func RemoveVG(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	vgs, err := ListLVM()
	if err != nil {
		return err
	}
	for _, vg := range vgs {
		if vg.Name != name {
			continue
		}
		if len(vg.LVs) > 0 {
			return fmt.Errorf("volume group %s still has logical volumes", name)
		}
		Lock.Lock()
		defer Lock.Unlock()
		if _, err := sys.Run("vgremove", "-f", name); err != nil {
			return err
		}
		for _, pv := range vg.PVs {
			_, _ = sys.Run("pvremove", "-ff", "-y", pv.Name)
		}
		return nil
	}
	return fmt.Errorf("volume group %s not found", name)
}

type CreateLVReq struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"` // bytes; 0 = all free space
	Type    string `json:"type"` // linear | striped | raid1 | raid5 | raid6 | raid10
	Stripes int    `json:"stripes"`
}

func CreateLV(vg string, r CreateLVReq) error {
	if err := ValidName(vg); err != nil {
		return err
	}
	if err := ValidName(r.Name); err != nil {
		return err
	}
	args := []string{"-y", "-W", "y", "-n", r.Name}
	if r.Size > 0 {
		args = append(args, "-L", fmt.Sprintf("%db", r.Size))
	} else {
		args = append(args, "-l", "100%FREE")
	}
	switch r.Type {
	case "", "linear":
	case "striped":
		if r.Stripes < 2 {
			return fmt.Errorf("striped volumes need at least 2 stripes")
		}
		args = append(args, "-i", strconv.Itoa(r.Stripes))
	case "raid1", "raid5", "raid6", "raid10":
		args = append(args, "--type", r.Type)
		if r.Type == "raid1" {
			args = append(args, "-m", "1")
		} else if r.Stripes > 0 {
			args = append(args, "-i", strconv.Itoa(r.Stripes))
		}
	default:
		return fmt.Errorf("unsupported LV type %q", r.Type)
	}
	Lock.Lock()
	defer Lock.Unlock()
	_, err := sys.Run("lvcreate", append(args, vg)...)
	return err
}

func ResizeLV(vg, lv string, size int64) error {
	if err := ValidName(vg); err != nil {
		return err
	}
	if err := ValidName(lv); err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	path := "/dev/" + vg + "/" + lv
	args := []string{"-r", "-y"}
	if size > 0 {
		args = append(args, "-L", fmt.Sprintf("%db", size))
	} else {
		args = append(args, "-l", "+100%FREE")
	}
	_, err := sys.Run("lvextend", append(args, path)...)
	return err
}

func RemoveLV(vg, lv string) error {
	if err := ValidName(vg); err != nil {
		return err
	}
	if err := ValidName(lv); err != nil {
		return err
	}
	path := "/dev/" + vg + "/" + lv
	if mounted(resolve(path)) {
		return fmt.Errorf("%s is mounted; delete the volume first", path)
	}
	Lock.Lock()
	defer Lock.Unlock()
	_, err := sys.Run("lvremove", "-f", path)
	return err
}

func SnapshotLV(vg, lv, name string, size int64) error {
	for _, n := range []string{vg, lv} {
		if err := ValidName(n); err != nil {
			return err
		}
	}
	if err := ValidName(name); err != nil {
		return err
	}
	if size <= 0 {
		size = 1 << 30
	}
	Lock.Lock()
	defer Lock.Unlock()
	_, err := sys.Run("lvcreate", "-y", "-s", "-n", name, "-L", fmt.Sprintf("%db", size), "/dev/"+vg+"/"+lv)
	return err
}
