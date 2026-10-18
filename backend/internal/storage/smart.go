package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"strataos/internal/sys"
)

type SmartAttr struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Value  int    `json:"value"`
	Worst  int    `json:"worst"`
	Thresh int    `json:"thresh"`
	Raw    string `json:"raw"`
	Failed string `json:"failed,omitempty"`
}

type SmartInfo struct {
	Device       string      `json:"device"`
	Available    bool        `json:"available"`
	Enabled      bool        `json:"enabled"`
	Passed       *bool       `json:"passed"`
	Model        string      `json:"model"`
	Serial       string      `json:"serial"`
	Firmware     string      `json:"firmware"`
	Capacity     int64       `json:"capacity"`
	Temperature  int         `json:"temperature"`
	PowerOnHours int         `json:"powerOnHours"`
	PowerCycles  int         `json:"powerCycles"`
	Attributes   []SmartAttr `json:"attributes"`
	NVMe         any         `json:"nvme,omitempty"`
	SelfTest     any         `json:"selfTest,omitempty"`
	Messages     []string    `json:"messages,omitempty"`
	// Warnings lists early signs of wear (the disk still passes its overall test).
	Warnings []string `json:"warnings,omitempty"`
	// Standby: the disk is asleep and was left alone (no data read).
	Standby bool `json:"standby,omitempty"`
	nvmeLog nvmeHealth
}

type nvmeHealth struct {
	CriticalWarning int `json:"critical_warning"`
	AvailableSpare  int `json:"available_spare"`
	SpareThreshold  int `json:"available_spare_threshold"`
	PercentageUsed  int `json:"percentage_used"`
	MediaErrors     int `json:"media_errors"`
	present         bool
}

type smartctlJSON struct {
	Smartctl struct {
		Messages []struct {
			String string `json:"string"`
		} `json:"messages"`
	} `json:"smartctl"`
	ModelName       string `json:"model_name"`
	SerialNumber    string `json:"serial_number"`
	FirmwareVersion string `json:"firmware_version"`
	UserCapacity    struct {
		Bytes int64 `json:"bytes"`
	} `json:"user_capacity"`
	SmartSupport struct {
		Available bool `json:"available"`
		Enabled   bool `json:"enabled"`
	} `json:"smart_support"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount int `json:"power_cycle_count"`
	AtaAttrs        struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Value      int    `json:"value"`
			Worst      int    `json:"worst"`
			Thresh     int    `json:"thresh"`
			WhenFailed string `json:"when_failed"`
			Raw        struct {
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMeLog        json.RawMessage `json:"nvme_smart_health_information_log"`
	AtaSelfTestLog json.RawMessage `json:"ata_smart_self_test_log"`
	SelfTestStatus json.RawMessage `json:"ata_smart_data"`
}

// validateBlockDevice only checks that dev is a block device: SMART is read-only and
// non-destructive, so unlike ValidateDevice the system disk is allowed.
func validateBlockDevice(dev string) error {
	if !devNameRe.MatchString(dev) {
		return fmt.Errorf("invalid device path %q", dev)
	}
	fi, err := os.Stat(dev)
	if err != nil || fi.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("%s is not a block device", dev)
	}
	return nil
}

// Smart reads SMART data of a disk with smartctl (waking it up if needed).
func Smart(dev string) (*SmartInfo, error) { return smart(dev, false) }

// SmartIfAwake is Smart for periodic checks: a sleeping disk is not woken up.
func SmartIfAwake(dev string) (*SmartInfo, error) { return smart(dev, true) }

func smart(dev string, noWake bool) (*SmartInfo, error) {
	if err := validateBlockDevice(dev); err != nil {
		return nil, err
	}
	if !sys.Has("smartctl") {
		return nil, fmt.Errorf("smartmontools is not installed")
	}
	// smartctl exit status is a bitmask; parse output whatever the code.
	args := []string{"-j", "-a", dev}
	if noWake {
		args = append([]string{"-n", "standby"}, args...)
	}
	out, _ := sys.RunOpts(nilCtx(), sys.Opts{AllowCodes: []int{-2}}, "smartctl", args...)
	var j smartctlJSON
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		return nil, fmt.Errorf("smartctl: unreadable output")
	}
	if noWake {
		for _, m := range j.Smartctl.Messages {
			if strings.Contains(m.String, "STANDBY") || strings.Contains(m.String, "SLEEP") {
				return &SmartInfo{Device: dev, Standby: true, Attributes: []SmartAttr{}}, nil
			}
		}
	}
	info := &SmartInfo{
		Device: dev, Available: j.SmartSupport.Available, Enabled: j.SmartSupport.Enabled,
		Model: j.ModelName, Serial: j.SerialNumber, Firmware: j.FirmwareVersion, Capacity: j.UserCapacity.Bytes,
		Temperature: j.Temperature.Current, PowerOnHours: j.PowerOnTime.Hours, PowerCycles: j.PowerCycleCount,
		Attributes: []SmartAttr{},
	}
	if j.SmartStatus != nil {
		p := j.SmartStatus.Passed
		info.Passed = &p
	}
	for _, a := range j.AtaAttrs.Table {
		info.Attributes = append(info.Attributes, SmartAttr{ID: a.ID, Name: a.Name, Value: a.Value, Worst: a.Worst, Thresh: a.Thresh, Raw: a.Raw.String, Failed: a.WhenFailed})
	}
	if len(j.NVMeLog) > 0 {
		var v any
		_ = json.Unmarshal(j.NVMeLog, &v)
		info.NVMe = v
		info.Available = true
		if json.Unmarshal(j.NVMeLog, &info.nvmeLog) == nil {
			info.nvmeLog.present = true
		}
	}
	if len(j.AtaSelfTestLog) > 0 {
		var v any
		_ = json.Unmarshal(j.AtaSelfTestLog, &v)
		info.SelfTest = v
	}
	for _, m := range j.Smartctl.Messages {
		info.Messages = append(info.Messages, m.String)
	}
	info.Warnings = SmartWarnings(info)
	return info, nil
}

// rawCount reads the leading number of a raw SMART value ("8", "12 (Min/Max…)").
func rawCount(raw string) int {
	n := 0
	for _, c := range strings.TrimSpace(raw) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			break
		}
	}
	return n
}

// SmartWarnings returns the early signs that a disk is getting weak, before
// its overall SMART test fails. Counts are part of the text, so a growing
// count produces a new alert.
func SmartWarnings(info *SmartInfo) []string {
	var w []string
	counters := map[int]string{
		5:   "%d reallocated sector(s)",
		197: "%d sector(s) pending reallocation",
		198: "%d unreadable sector(s)",
		187: "%d uncorrected error(s)",
		10:  "%d spin-up failure(s)",
		184: "%d internal transfer error(s)",
	}
	for _, a := range info.Attributes {
		if f, ok := counters[a.ID]; ok {
			if n := rawCount(a.Raw); n > 0 {
				w = append(w, fmt.Sprintf(f, n))
			}
			continue
		}
		// A pre-failure attribute close to its threshold.
		if a.Thresh > 0 && a.Value > 0 && a.Value <= a.Thresh+5 && a.Failed == "" {
			w = append(w, fmt.Sprintf("attribute %s close to its threshold (%d / %d)", a.Name, a.Value, a.Thresh))
		}
	}
	if n := info.nvmeLog; n.present {
		if n.CriticalWarning != 0 {
			w = append(w, fmt.Sprintf("NVMe critical warning (code %d)", n.CriticalWarning))
		}
		if n.MediaErrors > 0 {
			w = append(w, fmt.Sprintf("%d media error(s)", n.MediaErrors))
		}
		if n.PercentageUsed >= 90 {
			w = append(w, fmt.Sprintf("SSD wear at %d%%", n.PercentageUsed))
		}
		if n.SpareThreshold > 0 && n.AvailableSpare <= n.SpareThreshold {
			w = append(w, fmt.Sprintf("spare cells exhausted (%d%%)", n.AvailableSpare))
		}
	}
	if info.Temperature >= 60 {
		w = append(w, fmt.Sprintf("high temperature (%d °C)", info.Temperature))
	}
	return w
}

// SmartTest starts a short or long self-test.
func SmartTest(dev, kind string) error {
	if err := validateBlockDevice(dev); err != nil {
		return err
	}
	if kind != "short" && kind != "long" {
		return fmt.Errorf("test type must be short or long")
	}
	_, err := sys.RunOpts(nilCtx(), sys.Opts{AllowCodes: []int{4}}, "smartctl", "-t", kind, dev)
	return err
}
