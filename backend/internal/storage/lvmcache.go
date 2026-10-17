package storage

// SSD cache of a logical volume (lvmcache / dm-cache): the SSD is added to the
// volume group as a cache pool attached to the LV, while it stays mounted.
// The SSD is remembered by its PV UUID (kernel names change between boots).

import (
	"fmt"
	"strings"

	"strataos/internal/sys"
)

const cacheTag = "strata_cache_pv="

// CacheInfo describes the cache of a logical volume.
type CacheInfo struct {
	Mode       string  `json:"mode"`
	Device     string  `json:"device,omitempty"`
	UsedPct    float64 `json:"usedPercent"`
	Dirty      int64   `json:"dirtyBlocks"`
	ReadHits   int64   `json:"readHits"`
	ReadMisses int64   `json:"readMisses"`
	WriteHits  int64   `json:"writeHits"`
	WriteMiss  int64   `json:"writeMisses"`
}

func lvPath(vg, lv string) (string, error) {
	if err := ValidName(vg); err != nil {
		return "", err
	}
	if err := ValidName(lv); err != nil {
		return "", err
	}
	return vg + "/" + lv, nil
}

// AttachCache turns dev (a free disk, ideally an SSD) into the cache of vg/lv.
// mode: writethrough (safe: the SSD only speeds up reads of recent data) or
// writeback (faster writes; data not yet copied is lost if the SSD dies).
func AttachCache(vg, lv, dev, mode string) error {
	name, err := lvPath(vg, lv)
	if err != nil {
		return err
	}
	if mode != "writethrough" && mode != "writeback" {
		return fmt.Errorf("invalid cache mode")
	}
	if err := ValidateFreeDisks([]string{dev}, false); err != nil {
		return err
	}
	if c := LVCache(vg, lv); c != nil {
		return fmt.Errorf("%s already has a cache", name)
	}
	Lock.Lock()
	defer Lock.Unlock()
	if err := Wipe(dev); err != nil {
		return err
	}
	if _, err := sys.Run("pvcreate", "-ff", "-y", dev); err != nil {
		return err
	}
	uuid, _ := sys.Run("pvs", "--noheadings", "-o", "pv_uuid", dev)
	uuid = strings.TrimSpace(uuid)
	undo := func() {
		_, _ = sys.Run("lvremove", "-f", vg+"/"+lv+"_ssd")
		_, _ = sys.Run("vgreduce", vg, dev)
		_, _ = sys.Run("pvremove", "-ff", "-y", dev)
	}
	if _, err := sys.Run("vgextend", vg, dev); err != nil {
		_, _ = sys.Run("pvremove", "-ff", "-y", dev)
		return err
	}
	// Room is left on the SSD for the pool metadata and its spare copy.
	if _, err := sys.Run("lvcreate", "-y", "--type", "cache-pool", "-l", "90%PVS", "-n", lv+"_ssd", vg, dev); err != nil {
		undo()
		return err
	}
	if _, err := sys.Run("lvconvert", "-y", "--type", "cache", "--cachepool", vg+"/"+lv+"_ssd", "--cachemode", mode, name); err != nil {
		undo()
		return err
	}
	_, _ = sys.Run("lvchange", "--addtag", cacheTag+uuid, name)
	return nil
}

// DetachCache flushes and removes the cache, then frees the SSD.
func DetachCache(vg, lv string) error {
	name, err := lvPath(vg, lv)
	if err != nil {
		return err
	}
	Lock.Lock()
	defer Lock.Unlock()
	tags, _ := sys.Run("lvs", "--noheadings", "-o", "lv_tags", name)
	if _, err := sys.Run("lvconvert", "-y", "--uncache", name); err != nil {
		return err
	}
	for _, t := range strings.Split(strings.TrimSpace(tags), ",") {
		uuid, ok := strings.CutPrefix(strings.TrimSpace(t), cacheTag)
		if !ok {
			continue
		}
		_, _ = sys.Run("lvchange", "--deltag", strings.TrimSpace(t), name)
		if dev := pvByUUID(uuid); dev != "" {
			if _, err := sys.Run("vgreduce", vg, dev); err == nil {
				_, _ = sys.Run("pvremove", "-y", dev)
			}
		}
	}
	return nil
}

func pvByUUID(uuid string) string {
	rows, _ := lvmReport("pvs", "pv_name,pv_uuid")
	for _, r := range rows {
		if r["pv_uuid"] == uuid {
			return r["pv_name"]
		}
	}
	return ""
}

// LVCache returns the cache of vg/lv, or nil.
func LVCache(vg, lv string) *CacheInfo {
	rows, _ := lvmReport("lvs", "lv_name,vg_name,segtype,lv_tags,cache_mode,cache_used_blocks,cache_total_blocks,cache_dirty_blocks,cache_read_hits,cache_read_misses,cache_write_hits,cache_write_misses")
	for _, r := range rows {
		if r["vg_name"] != vg || r["lv_name"] != lv || r["segtype"] != "cache" {
			continue
		}
		c := &CacheInfo{Mode: r["cache_mode"], Dirty: num(r["cache_dirty_blocks"]), ReadHits: num(r["cache_read_hits"]), ReadMisses: num(r["cache_read_misses"]), WriteHits: num(r["cache_write_hits"]), WriteMiss: num(r["cache_write_misses"])}
		if t := num(r["cache_total_blocks"]); t > 0 {
			c.UsedPct = float64(num(r["cache_used_blocks"])) * 100 / float64(t)
		}
		for _, t := range strings.Split(r["lv_tags"], ",") {
			if uuid, ok := strings.CutPrefix(strings.TrimSpace(t), cacheTag); ok {
				c.Device = pvByUUID(uuid)
			}
		}
		return c
	}
	return nil
}
