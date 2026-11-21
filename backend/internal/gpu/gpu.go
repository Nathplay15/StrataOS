// Package gpu finds the graphics processors of the NAS and what their video
// engines decode and encode: render nodes and PCI identity from sysfs, the
// VA-API driver and its profiles from vainfo, NVIDIA's encoder from its
// proprietary driver. The parsers are pure (tested on captured outputs);
// Detect reads the machine.
package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"strataos/internal/sys"
)

// SysRoot is "/" (tests point it at a fixture tree).
var SysRoot = "/"

// Codecs, in the order they are shown.
const (
	H264   = "H.264"
	HEVC   = "HEVC"
	HEVC10 = "HEVC 10-bit"
	AV1    = "AV1"
	VP9    = "VP9"
	VP9_10 = "VP9 10-bit"
	VP8    = "VP8"
	MPEG2  = "MPEG-2"
	VC1    = "VC-1"
	JPEG   = "JPEG"
)

var codecOrder = []string{H264, HEVC, HEVC10, AV1, VP9, VP9_10, VP8, MPEG2, VC1, JPEG}

// VAInfo is what vainfo reports for one render node.
type VAInfo struct {
	// Driver is the version line ("Intel iHD driver for Intel(R) Gen Graphics - 24.1.0").
	Driver string `json:"driver"`
	// Name: iHD, i965, radeonsi, r600, nouveau, virtio_gpu… ("" when unknown).
	Name   string   `json:"name"`
	Decode []string `json:"decode"`
	Encode []string `json:"encode"`
	// LowPower: H.264 is encoded only through the low-power entry point
	// (recent Intel: ffmpeg needs -low_power 1, Jellyfin its "low-power" box).
	LowPower bool `json:"lowPower,omitempty"`
}

// CanEncode tells whether the engine encodes c.
func (v *VAInfo) CanEncode(c string) bool { return v != nil && slices.Contains(v.Encode, c) }

// CanDecode tells whether the engine decodes c.
func (v *VAInfo) CanDecode(c string) bool { return v != nil && slices.Contains(v.Decode, c) }

func profileCodec(p string) string {
	p = strings.TrimPrefix(p, "VAProfile")
	switch {
	case strings.HasPrefix(p, "H264"):
		return H264
	case p == "HEVCMain10":
		return HEVC10
	case strings.HasPrefix(p, "HEVCMain"):
		// HEVCMain, HEVCMain422_10, HEVCMain444…: count the plain 8-bit one only.
		if p == "HEVCMain" {
			return HEVC
		}
	case strings.HasPrefix(p, "AV1Profile0"):
		return AV1
	case p == "VP9Profile0":
		return VP9
	case p == "VP9Profile2":
		return VP9_10
	case strings.HasPrefix(p, "VP8"):
		return VP8
	case strings.HasPrefix(p, "MPEG2"):
		return MPEG2
	case strings.HasPrefix(p, "VC1"):
		return VC1
	case strings.HasPrefix(p, "JPEG"):
		return JPEG
	}
	return ""
}

var driverName = regexp.MustCompile(`\b(iHD|i965)\b|\((radeonsi|r600|nouveau|virgl|zink|virtio_gpu)\b`)

// ParseVainfo reads the output of vainfo. It fails when the driver could not
// be loaded (no profiles and no driver line).
func ParseVainfo(out string) (*VAInfo, error) {
	v := &VAInfo{Decode: []string{}, Encode: []string{}}
	dec, enc := map[string]bool{}, map[string]bool{}
	h264Full := false
	inList := false
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if d, ok := strings.CutPrefix(t, "vainfo: Driver version:"); ok {
			v.Driver = strings.TrimSpace(d)
			continue
		}
		if strings.HasPrefix(t, "vainfo: Supported profile and entrypoints") {
			inList = true
			continue
		}
		if !inList || !strings.HasPrefix(t, "VAProfile") {
			continue
		}
		prof, ep, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		c := profileCodec(strings.TrimSpace(prof))
		if c == "" {
			continue
		}
		switch ep = strings.TrimSpace(ep); ep {
		case "VAEntrypointVLD":
			dec[c] = true
		case "VAEntrypointEncSlice", "VAEntrypointEncPicture":
			enc[c] = true
			if c == H264 {
				h264Full = true
			}
		case "VAEntrypointEncSliceLP":
			enc[c] = true
		}
	}
	if v.Driver == "" && len(dec) == 0 && len(enc) == 0 {
		msg := "the video driver could not be loaded"
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "vaInitialize failed") || strings.Contains(l, "libva error") {
				msg = strings.TrimSpace(l)
				break
			}
		}
		return nil, fmt.Errorf("%s", msg)
	}
	for _, c := range codecOrder {
		if dec[c] {
			v.Decode = append(v.Decode, c)
		}
		if enc[c] {
			v.Encode = append(v.Encode, c)
		}
	}
	v.LowPower = enc[H264] && !h264Full
	if m := driverName.FindStringSubmatch(v.Driver); m != nil {
		for _, s := range m[1:] {
			if s != "" {
				v.Name = s
				break
			}
		}
	}
	return v, nil
}

// PCI vendors.
const (
	VendorIntel  = "intel"
	VendorAMD    = "amd"
	VendorNVIDIA = "nvidia"
	VendorOther  = "other"
)

func vendorOf(id string) string {
	switch strings.ToLower(id) {
	case "0x8086":
		return VendorIntel
	case "0x1002", "0x1022":
		return VendorAMD
	case "0x10de":
		return VendorNVIDIA
	}
	return VendorOther
}

// GPU is one render node.
type GPU struct {
	Node     string  `json:"node"`           // /dev/dri/renderD128
	Card     string  `json:"card,omitempty"` // card0 (display node of the same device)
	Slot     string  `json:"slot,omitempty"` // PCI address
	Vendor   string  `json:"vendor"`         // intel, amd, nvidia, other
	VendorID string  `json:"vendorId,omitempty"`
	DeviceID string  `json:"deviceId,omitempty"`
	Model    string  `json:"model,omitempty"`
	Kernel   string  `json:"kernel,omitempty"` // kernel driver: i915, xe, amdgpu, nvidia, virtio_gpu, vgem…
	VA       *VAInfo `json:"va,omitempty"`
	VAError  string  `json:"vaError,omitempty"`
	// NVENC: NVIDIA's proprietary driver is loaded and ffmpeg has its encoder.
	NVENC bool `json:"nvenc,omitempty"`
	// Group owning the node and its permissions ("crw-rw----").
	Group string `json:"group,omitempty"`
	Perm  string `json:"perm,omitempty"`
	// WorldWritable: anyone on the NAS may use the node (should not happen).
	WorldWritable bool `json:"worldWritable,omitempty"`
}

// Usable tells whether the built-in player can encode H.264 with this GPU.
func (g GPU) Usable() bool { return g.VA.CanEncode(H264) || g.NVENC }

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// ParseLspciMM reads one line of lspci -mm (vendor and device names).
func ParseLspciMM(line string) (vendor, device string) {
	var f []string
	for {
		i := strings.IndexByte(line, '"')
		if i < 0 {
			break
		}
		j := strings.IndexByte(line[i+1:], '"')
		if j < 0 {
			break
		}
		f = append(f, line[i+1:i+1+j])
		line = line[i+1+j+1:]
	}
	if len(f) >= 3 {
		return f[1], f[2]
	}
	return "", ""
}

var renderRe = regexp.MustCompile(`^renderD\d+$`)

// Scan lists the render nodes from sysfs, without running anything.
func Scan() []GPU {
	base := filepath.Join(SysRoot, "sys/class/drm")
	ents, _ := os.ReadDir(base)
	var out []GPU
	cards := map[string]string{} // device path → card name
	for _, e := range ents {
		if n := e.Name(); strings.HasPrefix(n, "card") && !strings.Contains(n, "-") {
			if d, err := filepath.EvalSymlinks(filepath.Join(base, n, "device")); err == nil {
				cards[d] = n
			}
		}
	}
	for _, e := range ents {
		if !renderRe.MatchString(e.Name()) {
			continue
		}
		g := GPU{Node: "/dev/dri/" + e.Name(), Vendor: VendorOther}
		dev := filepath.Join(base, e.Name(), "device")
		if real, err := filepath.EvalSymlinks(dev); err == nil {
			g.Card = cards[real]
			if b := filepath.Base(real); strings.Count(b, ":") == 2 {
				g.Slot = b
			}
		}
		if v := readTrim(filepath.Join(dev, "vendor")); v != "" {
			g.VendorID, g.Vendor = v, vendorOf(v)
		}
		g.DeviceID = readTrim(filepath.Join(dev, "device"))
		if l, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			g.Kernel = filepath.Base(l)
		}
		if st, err := os.Stat(filepath.Join(SysRoot, "dev/dri", e.Name())); err == nil {
			g.Perm = st.Mode().String()
			g.WorldWritable = st.Mode().Perm()&0o002 != 0
			if s, ok := st.Sys().(*syscall.Stat_t); ok {
				g.Group = groupName(s.Gid)
			}
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b GPU) int { return strings.Compare(a.Node, b.Node) })
	return out
}

func groupName(gid uint32) string {
	b, _ := os.ReadFile(filepath.Join(SysRoot, "etc/group"))
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 3 && f[2] == fmt.Sprint(gid) {
			return f[0]
		}
	}
	return fmt.Sprint(gid)
}

// Detect lists the GPUs with their capabilities: vainfo for each render
// node (when installed), the model from lspci, NVENC from the NVIDIA driver.
func Detect() []GPU {
	gpus := Scan()
	vainfo := sys.Has("vainfo")
	lspci := sys.Has("lspci")
	encoders := ""
	for i := range gpus {
		g := &gpus[i]
		if lspci && g.Slot != "" {
			if out, err := sys.Run("lspci", "-mm", "-s", g.Slot); err == nil {
				_, g.Model = ParseLspciMM(out)
			}
		}
		if g.Vendor == VendorNVIDIA && g.Kernel == "nvidia" {
			if encoders == "" {
				encoders, _ = sys.Run("ffmpeg", "-hide_banner", "-encoders")
			}
			_, err := os.Stat(filepath.Join(SysRoot, "dev/nvidiactl"))
			g.NVENC = err == nil && strings.Contains(encoders, "h264_nvenc")
		}
		if !vainfo {
			continue
		}
		// The profiles are on stdout, libva's errors on stderr.
		out, err := sys.Run("vainfo", "--display", "drm", "--device", g.Node)
		if ce, ok := err.(*sys.CmdError); ok {
			out += "\n" + ce.Stderr
		}
		if va, perr := ParseVainfo(out); perr == nil {
			g.VA = va
		} else {
			g.VAError = perr.Error()
		}
	}
	return gpus
}

// Best is the GPU the built-in player uses: the given node when it can
// encode H.264, otherwise the first one that can (VA-API before NVENC).
func Best(gpus []GPU, node string) (GPU, bool) {
	for _, g := range gpus {
		if g.Node == node && g.Usable() {
			return g, true
		}
	}
	if node != "" {
		return GPU{}, false
	}
	for _, g := range gpus {
		if g.VA.CanEncode(H264) {
			return g, true
		}
	}
	for _, g := range gpus {
		if g.NVENC {
			return g, true
		}
	}
	return GPU{}, false
}

// Hint is a piece of advice about a GPU (English, translated by the UI).
type Hint struct {
	Level string `json:"level"` // info, warn
	Text  string `json:"text"`
}

// Advise says what is missing for a GPU to be used. installed tells whether
// a Debian package is installed; nonFree whether the non-free component of
// Debian is enabled (its packages have an installation candidate).
func Advise(g GPU, vainfo bool, installed func(string) bool, nonFree bool) []Hint {
	var h []Hint
	add := func(level, text string) { h = append(h, Hint{level, text}) }
	if g.WorldWritable {
		add("warn", "Every account of the NAS may use this device: its permissions should be 0660, group render.")
	}
	switch g.Vendor {
	case VendorNVIDIA:
		if g.Kernel != "nvidia" {
			add("info", "NVIDIA graphics cards encode video only with NVIDIA's proprietary driver, which StrataOS does not install (non-free, built for each kernel). The open nouveau driver has no usable video encoder.")
		} else if !g.NVENC {
			add("info", "NVIDIA's driver is loaded, but ffmpeg has no NVENC encoder or /dev/nvidiactl is missing.")
		}
		if g.Kernel == "nvidia" {
			add("info", "Apps of the App Center get Intel and AMD graphics only: NVIDIA needs the NVIDIA container toolkit, which StrataOS does not set up.")
		}
		return h
	case VendorOther:
		if g.VA == nil {
			add("info", "No video acceleration on this device (virtual or display-only graphics).")
		}
		return h
	}
	if !vainfo {
		add("info", "Install the media tools (vainfo and the video drivers) to see what this graphics processor decodes and encodes.")
		return h
	}
	if g.VA == nil {
		switch g.Vendor {
		case VendorIntel:
			if nonFree {
				add("warn", "No working video driver: install intel-media-va-driver-non-free (processors from 2015 on) or i965-va-driver (older ones).")
			} else {
				add("warn", "No working video driver: install intel-media-va-driver (processors from 2015 on) or i965-va-driver (older ones). Encoding on many processors needs intel-media-va-driver-non-free, from the non-free component of Debian, which this NAS does not use.")
			}
		case VendorAMD:
			add("warn", "No working video driver: install mesa-va-drivers.")
		}
		return h
	}
	if !g.VA.CanEncode(H264) {
		switch {
		case g.Vendor == VendorIntel && g.VA.Name == "iHD" && installed("intel-media-va-driver") && !installed("intel-media-va-driver-non-free"):
			if nonFree {
				add("warn", "This driver decodes only: install intel-media-va-driver-non-free to encode.")
			} else {
				add("warn", "This driver decodes only: encoding needs intel-media-va-driver-non-free, from the non-free component of Debian (add non-free to the APT sources, then install it).")
			}
		case g.Vendor == VendorAMD && !g.VA.CanDecode(H264):
			add("warn", "This Mesa build has no H.264/HEVC support: the graphics processor is not used for these codecs.")
		default:
			add("info", "This graphics processor does not encode H.264: videos are converted by the processor (CPU).")
		}
	}
	if g.VA.LowPower {
		add("info", "H.264 is encoded by the low-power engine: on 9th to 11th generation Intel processors it needs the GuC/HuC firmware (i915.enable_guc=2), already on by default from the 12th generation.")
	}
	return h
}
