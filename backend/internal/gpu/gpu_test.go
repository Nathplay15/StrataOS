package gpu

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseVainfo(t *testing.T) {
	cases := []struct {
		file, name     string
		decode, encode []string
		lowPower       bool
	}{
		{"vainfo-ihd-adln.txt", "iHD", []string{H264, HEVC, HEVC10, AV1, VP9, VP9_10, VP8, MPEG2, JPEG}, []string{H264, HEVC, HEVC10, VP9, VP9_10, JPEG}, true},
		{"vainfo-ihd-free-decodeonly.txt", "iHD", []string{H264, HEVC, HEVC10, MPEG2, JPEG}, []string{}, false},
		{"vainfo-i965-haswell.txt", "i965", []string{H264, MPEG2, VC1, JPEG}, []string{H264, MPEG2}, false},
		{"vainfo-radeonsi.txt", "radeonsi", []string{H264, HEVC, HEVC10, AV1, VP9, VP9_10, JPEG}, []string{H264, HEVC, HEVC10, AV1}, false},
	}
	for _, c := range cases {
		v, err := ParseVainfo(fixture(t, c.file))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if v.Name != c.name || !slices.Equal(v.Decode, c.decode) || !slices.Equal(v.Encode, c.encode) || v.LowPower != c.lowPower {
			t.Errorf("%s: got name %q decode %v encode %v lp %v", c.file, v.Name, v.Decode, v.Encode, v.LowPower)
		}
	}
	if _, err := ParseVainfo(fixture(t, "vainfo-fail.txt")); err == nil || !strings.Contains(err.Error(), "vaInitialize failed") {
		t.Errorf("failure: %v", err)
	}
	if _, err := ParseVainfo(""); err == nil {
		t.Error("empty output accepted")
	}
}

func TestParseLspciMM(t *testing.T) {
	v, d := ParseLspciMM(`00:02.0 "VGA compatible controller" "Intel Corporation" "Alder Lake-N [UHD Graphics]" -r00 "Intel Corporation" "Device 7270"` + "\n")
	if v != "Intel Corporation" || d != "Alder Lake-N [UHD Graphics]" {
		t.Errorf("got %q %q", v, d)
	}
	if v, d := ParseLspciMM("garbage"); v != "" || d != "" {
		t.Errorf("garbage: %q %q", v, d)
	}
}

// sysfsTree builds /sys/class/drm and /dev/dri like the kernel: an Intel
// iGPU (card0 + renderD128), a vgem render node (no PCI device) and an
// NVIDIA card with the nouveau driver.
func sysfsTree(t *testing.T) string {
	root := t.TempDir()
	mk := func(p string) string {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write := func(p, v string) {
		if err := os.WriteFile(filepath.Join(root, p), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		if err := os.Symlink(target, filepath.Join(root, p)); err != nil {
			t.Fatal(err)
		}
	}
	intel := "sys/devices/pci0000:00/0000:00:02.0"
	mk(intel + "/drm/card0")
	mk(intel + "/drm/renderD128")
	write(intel+"/vendor", "0x8086")
	write(intel+"/device", "0x46d1")
	mk("sys/bus/pci/drivers/i915")
	link("../../../bus/pci/drivers/i915", intel+"/driver")
	nv := "sys/devices/pci0000:00/0000:01:00.0"
	mk(nv + "/drm/renderD129")
	write(nv+"/vendor", "0x10de")
	write(nv+"/device", "0x1c82")
	mk("sys/bus/pci/drivers/nouveau")
	link("../../../bus/pci/drivers/nouveau", nv+"/driver")
	vg := "sys/devices/platform/vgem"
	mk(vg + "/drm/renderD130")
	mk("sys/class/drm")
	link("../../devices/pci0000:00/0000:00:02.0/drm/card0", "sys/class/drm/card0")
	link("../../devices/pci0000:00/0000:00:02.0/drm/renderD128", "sys/class/drm/renderD128")
	link("../../devices/pci0000:00/0000:01:00.0/drm/renderD129", "sys/class/drm/renderD129")
	link("../../devices/platform/vgem/drm/renderD130", "sys/class/drm/renderD130")
	link("../../../0000:00:02.0", intel+"/drm/card0/device")
	link("../../../0000:00:02.0", intel+"/drm/renderD128/device")
	link("../../../0000:01:00.0", nv+"/drm/renderD129/device")
	link("../../vgem", vg+"/drm/renderD130/device")
	mk("dev/dri")
	for n, mode := range map[string]os.FileMode{"renderD128": 0o660, "renderD129": 0o660, "renderD130": 0o666} {
		write("dev/dri/"+n, "")
		if err := os.Chmod(filepath.Join(root, "dev/dri", n), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScan(t *testing.T) {
	old := SysRoot
	SysRoot = sysfsTree(t)
	defer func() { SysRoot = old }()
	g := Scan()
	if len(g) != 3 {
		t.Fatalf("got %d GPUs: %+v", len(g), g)
	}
	if g[0].Node != "/dev/dri/renderD128" || g[0].Vendor != VendorIntel || g[0].Card != "card0" || g[0].Slot != "0000:00:02.0" || g[0].Kernel != "i915" || g[0].DeviceID != "0x46d1" || g[0].WorldWritable {
		t.Errorf("intel: %+v", g[0])
	}
	if g[1].Vendor != VendorNVIDIA || g[1].Kernel != "nouveau" || g[1].Card != "" {
		t.Errorf("nvidia: %+v", g[1])
	}
	if g[2].Vendor != VendorOther || g[2].Slot != "" || !g[2].WorldWritable {
		t.Errorf("vgem: %+v", g[2])
	}
}

func TestBestAndAdvise(t *testing.T) {
	ihd, _ := ParseVainfo(fixture(t, "vainfo-ihd-adln.txt"))
	free, _ := ParseVainfo(fixture(t, "vainfo-ihd-free-decodeonly.txt"))
	amd, _ := ParseVainfo(fixture(t, "vainfo-radeonsi.txt"))
	gpus := []GPU{
		{Node: "/dev/dri/renderD128", Vendor: VendorIntel, VA: free},
		{Node: "/dev/dri/renderD129", Vendor: VendorAMD, VA: amd},
		{Node: "/dev/dri/renderD130", Vendor: VendorNVIDIA, Kernel: "nvidia", NVENC: true},
	}
	if g, ok := Best(gpus, ""); !ok || g.Node != "/dev/dri/renderD129" {
		t.Errorf("auto: %v %v", g.Node, ok)
	}
	if g, ok := Best(gpus, "/dev/dri/renderD130"); !ok || !g.NVENC {
		t.Errorf("chosen nvenc: %v %v", g.Node, ok)
	}
	if _, ok := Best(gpus, "/dev/dri/renderD128"); ok {
		t.Error("decode-only GPU chosen")
	}
	if _, ok := Best(gpus[:1], ""); ok {
		t.Error("decode-only GPU chosen automatically")
	}

	none := func(string) bool { return false }
	only := func(p string) bool { return p == "intel-media-va-driver" }
	has := func(h []Hint, sub string) bool {
		return slices.ContainsFunc(h, func(x Hint) bool { return strings.Contains(x.Text, sub) })
	}
	if h := Advise(gpus[0], true, only, false); !has(h, "non-free component of Debian") {
		t.Errorf("free iHD without non-free: %v", h)
	}
	if h := Advise(gpus[0], true, only, true); !has(h, "install intel-media-va-driver-non-free to encode") {
		t.Errorf("free iHD with non-free: %v", h)
	}
	if h := Advise(GPU{Vendor: VendorIntel, VA: ihd}, true, none, false); !has(h, "low-power") {
		t.Errorf("low power: %v", h)
	}
	if h := Advise(GPU{Vendor: VendorAMD}, true, none, false); !has(h, "mesa-va-drivers") {
		t.Errorf("amd no driver: %v", h)
	}
	if h := Advise(GPU{Vendor: VendorIntel}, false, none, false); !has(h, "media tools") {
		t.Errorf("no vainfo: %v", h)
	}
	if h := Advise(GPU{Vendor: VendorNVIDIA, Kernel: "nouveau"}, true, none, false); !has(h, "proprietary driver") {
		t.Errorf("nouveau: %v", h)
	}
	if h := Advise(GPU{Vendor: VendorOther, WorldWritable: true}, true, none, false); !has(h, "0660") || !has(h, "No video acceleration") {
		t.Errorf("vgem: %v", h)
	}
	if h := Advise(GPU{Vendor: VendorAMD, VA: amd}, true, none, false); len(h) != 0 {
		t.Errorf("working AMD: %v", h)
	}
}
