package api

import (
	"strings"
	"testing"

	"strataos/internal/gpu"
)

func TestGPUOverride(t *testing.T) {
	y, err := gpuOverride("jellyfin", []string{"105", "44"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"jellyfin:", "- /dev/dri:/dev/dri", "group_add:", `- "105"`, `- "44"`} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q in\n%s", want, y)
		}
	}
}

func TestAppSettingsFor(t *testing.T) {
	ihd := &gpu.VAInfo{Name: "iHD", Decode: []string{gpu.H264, gpu.HEVC}, Encode: []string{gpu.H264, gpu.HEVC}, LowPower: true}
	amd := &gpu.VAInfo{Name: "radeonsi", Decode: []string{gpu.H264}, Encode: []string{gpu.H264}}
	if s := appSettingsFor([]gpu.GPU{{Node: "/dev/dri/renderD128", Vendor: gpu.VendorIntel, VA: ihd}}); s.Method != "qsv" || !s.HEVC || !s.LowPower || s.Device != "/dev/dri/renderD128" {
		t.Errorf("intel: %+v", s)
	}
	if s := appSettingsFor([]gpu.GPU{{Node: "/dev/dri/renderD129", Vendor: gpu.VendorNVIDIA}, {Node: "/dev/dri/renderD128", Vendor: gpu.VendorAMD, VA: amd}}); s.Method != "vaapi" || s.HEVC || s.Device != "/dev/dri/renderD128" {
		t.Errorf("amd: %+v", s)
	}
	if s := appSettingsFor(nil); s.Method != "" {
		t.Errorf("none: %+v", s)
	}
}
