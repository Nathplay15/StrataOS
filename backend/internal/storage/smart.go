package storage

import (
	"encoding/json"
	"fmt"
	"os"

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

// Smart reads SMART data of a disk with smartctl.
func Smart(dev string) (*SmartInfo, error) {
	if err := validateBlockDevice(dev); err != nil {
		return nil, err
	}
	if !sys.Has("smartctl") {
		return nil, fmt.Errorf("smartmontools is not installed")
	}
	// smartctl exit status is a bitmask; parse output whatever the code.
	out, _ := sys.RunOpts(nilCtx(), sys.Opts{AllowCodes: []int{-2}}, "smartctl", "-j", "-a", dev)
	var j smartctlJSON
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		return nil, fmt.Errorf("smartctl: unreadable output")
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
	}
	if len(j.AtaSelfTestLog) > 0 {
		var v any
		_ = json.Unmarshal(j.AtaSelfTestLog, &v)
		info.SelfTest = v
	}
	for _, m := range j.Smartctl.Messages {
		info.Messages = append(info.Messages, m.String)
	}
	return info, nil
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
