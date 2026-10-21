package vms

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// OVF is what StrataOS reads from an OVF descriptor (VMware, VirtualBox exports).
type OVF struct {
	Name     string   `json:"name"`
	VCPUs    int      `json:"vcpus"`
	MemoryMB int      `json:"memoryMb"`
	Firmware string   `json:"firmware"` // bios | uefi
	OS       string   `json:"os"`       // linux | windows | other
	Disks    []string `json:"disks"`    // disk files, in controller order
}

type ovfEnvelope struct {
	Files []struct {
		ID   string `xml:"id,attr"`
		Href string `xml:"href,attr"`
	} `xml:"References>File"`
	Disks []struct {
		DiskID  string `xml:"diskId,attr"`
		FileRef string `xml:"fileRef,attr"`
	} `xml:"DiskSection>Disk"`
	System struct {
		ID   string `xml:"id,attr"`
		Name string `xml:"Name"`
		OS   struct {
			ID          string `xml:"id,attr"`
			OSType      string `xml:"osType,attr"`
			Description string `xml:"Description"`
		} `xml:"OperatingSystemSection"`
		Hardware struct {
			Items []struct {
				ResourceType    int    `xml:"ResourceType"`
				VirtualQuantity int64  `xml:"VirtualQuantity"`
				AllocationUnits string `xml:"AllocationUnits"`
				HostResource    string `xml:"HostResource"`
			} `xml:"Item"`
			// OVF 2 exports use StorageItem for disks.
			StorageItems []struct {
				ResourceType int    `xml:"ResourceType"`
				HostResource string `xml:"HostResource"`
			} `xml:"StorageItem"`
			Config []struct {
				Key   string `xml:"key,attr"`
				Value string `xml:"value,attr"`
			} `xml:"Config"`
		} `xml:"VirtualHardwareSection"`
		// VirtualBox: <vbox:Machine><Hardware><Firmware type="EFI"/>
		VBox struct {
			Firmware struct {
				Type string `xml:"type,attr"`
			} `xml:"Hardware>Firmware"`
		} `xml:"Machine"`
	} `xml:"VirtualSystem"`
}

// allocMB converts a quantity of memory expressed in OVF allocation units to MiB.
func allocMB(q int64, units string) int {
	u := strings.ToLower(strings.ReplaceAll(units, " ", ""))
	switch {
	case strings.Contains(u, "2^30"), strings.Contains(u, "gigabyte"):
		return int(q * 1024)
	case strings.Contains(u, "2^20"), strings.Contains(u, "megabyte"), u == "":
		return int(q)
	case strings.Contains(u, "2^10"), strings.Contains(u, "kilobyte"):
		return int(q / 1024)
	case u == "byte", u == "bytes":
		return int(q >> 20)
	}
	return int(q)
}

// safeBase accepts a file name without any directory part.
func safeBase(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.ContainsAny(n, "/\\\x00") && !strings.HasPrefix(n, "-")
}

// ParseOVF reads an OVF descriptor.
func ParseOVF(data []byte) (*OVF, error) {
	var e ovfEnvelope
	if err := xml.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("invalid OVF descriptor: %v", err)
	}
	o := &OVF{Name: e.System.Name, Firmware: "bios", OS: "other", Disks: []string{}}
	if o.Name == "" {
		o.Name = e.System.ID
	}
	files := map[string]string{}
	for _, f := range e.Files {
		files[f.ID] = f.Href
	}
	diskFile := map[string]string{}
	var order []string
	for _, d := range e.Disks {
		diskFile[d.DiskID] = files[d.FileRef]
		order = append(order, d.DiskID)
	}
	var used []string
	addDisk := func(res string) {
		id := res
		for _, p := range []string{"ovf:/disk/", "/disk/"} {
			if i := strings.Index(res, p); i >= 0 {
				id = res[i+len(p):]
			}
		}
		if f, ok := diskFile[id]; ok && f != "" {
			used = append(used, f)
		}
	}
	for _, it := range e.System.Hardware.Items {
		switch it.ResourceType {
		case 3:
			o.VCPUs = int(it.VirtualQuantity)
		case 4:
			o.MemoryMB = allocMB(it.VirtualQuantity, it.AllocationUnits)
		case 17:
			addDisk(it.HostResource)
		}
	}
	for _, it := range e.System.Hardware.StorageItems {
		if it.ResourceType == 17 {
			addDisk(it.HostResource)
		}
	}
	if len(used) == 0 {
		for _, id := range order {
			if f := diskFile[id]; f != "" {
				used = append(used, f)
			}
		}
	}
	for _, f := range used {
		if !safeBase(f) {
			return nil, fmt.Errorf("unsafe disk file name %q in the OVF", f)
		}
		o.Disks = append(o.Disks, f)
	}
	for _, c := range e.System.Hardware.Config {
		if c.Key == "firmware" && strings.EqualFold(c.Value, "efi") {
			o.Firmware = "uefi"
		}
	}
	if strings.HasPrefix(strings.ToUpper(e.System.VBox.Firmware.Type), "EFI") {
		o.Firmware = "uefi"
	}
	osDesc := strings.ToLower(e.System.OS.OSType + " " + e.System.OS.Description)
	switch {
	case strings.Contains(osDesc, "windows") || strings.Contains(osDesc, "win"):
		o.OS = "windows"
	case osDesc != " " && osDesc != "":
		o.OS = "linux"
	}
	if o.VCPUs < 1 {
		o.VCPUs = 1
	}
	if o.MemoryMB < 256 {
		o.MemoryMB = 1024
	}
	return o, nil
}

// importFormat maps a file extension to the qemu-img format ("ova", "ovf" for appliances).
func importFormat(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ova":
		return "ova", nil
	case ".ovf":
		return "ovf", nil
	case ".vmdk":
		return "vmdk", nil
	case ".qcow2":
		return "qcow2", nil
	case ".raw", ".img":
		return "raw", nil
	case ".vhd":
		return "vpc", nil
	case ".vhdx":
		return "vhdx", nil
	case ".vdi":
		return "vdi", nil
	}
	return "", fmt.Errorf("unsupported file type (OVA, OVF, VMDK, qcow2, raw/img, VHD, VHDX or VDI)")
}

var vmdkExtentRe = regexp.MustCompile(`^(RW|RDONLY|NOACCESS)\s+\d+\s+\S+\s+"([^"]*)"`)

// checkDiskFile refuses images that would make qemu-img read other host files:
// qcow2 backing files and VMDK descriptors pointing outside their folder.
func checkDiskFile(path, format string) (string, error) {
	info, err := imageInfo(path)
	if err != nil {
		return "", err
	}
	if info.BackingFile != "" {
		return "", fmt.Errorf("%s depends on another image (%s): import the full image", filepath.Base(path), info.BackingFile)
	}
	// .img files are often qcow2 (Ubuntu cloud images): trust the content then.
	if format == "raw" && info.Format == "qcow2" {
		format = "qcow2"
	}
	if format == "vmdk" {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		head := make([]byte, 64<<10)
		n, _ := io.ReadFull(f, head)
		if strings.Contains(string(head[:n]), "# Disk DescriptorFile") {
			sc := bufio.NewScanner(strings.NewReader(string(head[:n])))
			for sc.Scan() {
				if m := vmdkExtentRe.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil && !safeBase(m[2]) {
					return "", fmt.Errorf("the VMDK descriptor points outside its folder (%s)", m[2])
				}
			}
		}
	}
	return format, nil
}

// ImportInfo describes a file before it is imported (pre-fills the form).
type ImportInfo struct {
	Format string `json:"format"`
	OVF    *OVF   `json:"ovf,omitempty"`
	Size   int64  `json:"size"`
}

// InspectImport reads what can be known of a file to import.
func InspectImport(src string) (*ImportInfo, error) {
	path, err := insideStrata(src)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("file not found")
	}
	format, err := importFormat(path)
	if err != nil {
		return nil, err
	}
	info := &ImportInfo{Format: format, Size: fi.Size()}
	switch format {
	case "ova":
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		tr := tar.NewReader(f)
		for {
			h, err := tr.Next()
			if err != nil {
				return nil, fmt.Errorf("no OVF descriptor in the OVA archive")
			}
			if strings.HasSuffix(strings.ToLower(h.Name), ".ovf") {
				data, err := io.ReadAll(io.LimitReader(tr, 16<<20))
				if err != nil {
					return nil, err
				}
				if info.OVF, err = ParseOVF(data); err != nil {
					return nil, err
				}
				return info, nil
			}
		}
	case "ovf":
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if info.OVF, err = ParseOVF(data); err != nil {
			return nil, err
		}
	}
	return info, nil
}

// ImportReq imports an existing disk or appliance as a new VM.
type ImportReq struct {
	CreateReq
	Source string `json:"source"` // file inside a volume
}

// importExts are the files kept when an OVA archive is unpacked.
var importExts = map[string]bool{".ovf": true, ".mf": true, ".vmdk": true, ".img": true, ".qcow2": true, ".vhd": true, ".vhdx": true, ".raw": true, ".vdi": true}

// extractOVA unpacks the descriptor, manifest and disks of an OVA into dir.
func extractOVA(ctx context.Context, path, dir string, p Progress) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, _ := f.Stat()
	cr := &countReader{r: f}
	tr := tar.NewReader(cr)
	for n := 0; ; n++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid OVA archive: %v", err)
		}
		if n > 64 {
			return fmt.Errorf("too many files in the OVA archive")
		}
		base := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || !safeBase(base) || strings.HasPrefix(base, ".") || !importExts[strings.ToLower(filepath.Ext(base))] {
			continue
		}
		out, err := os.OpenFile(filepath.Join(dir, base), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
		if err != nil {
			return err
		}
		p.log("Extracting %s", base)
		buf := make([]byte, 1<<20)
		for {
			k, rerr := tr.Read(buf)
			if k > 0 {
				if _, werr := out.Write(buf[:k]); werr != nil {
					out.Close()
					return werr
				}
				if fi != nil && fi.Size() > 0 {
					p.set(float64(cr.n) * 100 / float64(fi.Size()))
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				out.Close()
				return rerr
			}
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

var manifestRe = regexp.MustCompile(`^(SHA1|SHA256)\(([^)]+)\)\s*=\s*([0-9a-fA-F]+)$`)

// checkManifest verifies the files of an appliance against its .mf manifest.
func checkManifest(dir, mf string, p Progress) error {
	data, err := os.ReadFile(mf)
	if err != nil {
		return nil
	}
	for _, l := range strings.Split(string(data), "\n") {
		m := manifestRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil || !safeBase(m[2]) {
			continue
		}
		f, err := os.Open(filepath.Join(dir, m[2]))
		if err != nil {
			continue
		}
		var h hash.Hash = sha256.New()
		if m[1] == "SHA1" {
			h = sha1.New()
		}
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m[3]) {
			return fmt.Errorf("%s does not match the appliance manifest (damaged download?)", m[2])
		}
		p.log("%s: %s checksum OK", m[2], m[1])
	}
	return nil
}

// Import converts the disks of a file into a new VM folder and defines the VM.
func Import(ctx context.Context, r ImportReq, p Progress) error {
	src, err := insideStrata(r.Source)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("file not found")
	}
	format, err := importFormat(src)
	if err != nil {
		return err
	}
	// Settings of an appliance come from its OVF when the request leaves them
	// out (they are validated with the rest by newVMDir).
	if format == "ova" || format == "ovf" {
		info, err := InspectImport(src)
		if err != nil {
			return err
		}
		applyOVF(&r.CreateReq, info.OVF)
	}
	dir, err := newVMDir(&r.CreateReq)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	type disk struct{ path, format string }
	var disks []disk
	from := 0.0
	switch format {
	case "ova", "ovf":
		folder := filepath.Dir(src)
		var desc string
		if format == "ova" {
			folder = filepath.Join(dir, ".import")
			if err := os.MkdirAll(folder, 0o700); err != nil {
				return err
			}
			defer os.RemoveAll(folder)
			if err := extractOVA(ctx, src, folder, p.scaled(0, 40)); err != nil {
				return err
			}
			from = 40
			m, _ := filepath.Glob(filepath.Join(folder, "*.ovf"))
			if len(m) == 0 {
				return fmt.Errorf("no OVF descriptor in the OVA archive")
			}
			desc = m[0]
		} else {
			desc = src
		}
		data, err := os.ReadFile(desc)
		if err != nil {
			return err
		}
		o, err := ParseOVF(data)
		if err != nil {
			return err
		}
		if err := checkManifest(folder, strings.TrimSuffix(desc, filepath.Ext(desc))+".mf", p); err != nil {
			return err
		}
		if len(o.Disks) == 0 {
			return fmt.Errorf("the appliance has no disk")
		}
		for _, d := range o.Disks {
			f, err := importFormat(d)
			if err != nil || f == "ova" || f == "ovf" {
				f = "vmdk"
			}
			disks = append(disks, disk{filepath.Join(folder, d), f})
		}
		applyOVF(&r.CreateReq, o)
	default:
		disks = []disk{{src, format}}
	}
	if r.VCPUs < 1 || r.MemoryMB < 256 {
		return fmt.Errorf("invalid CPU, memory or disk size")
	}
	spec := installSpec{import_: true, extra: []string{"--serial", "pty,log.file=" + consoleLogPath(r.Name)}}
	for i, d := range disks {
		f, err := checkDiskFile(d.path, d.format)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, fmt.Sprintf("disk%d.qcow2", i))
		step := (95 - from) / float64(len(disks))
		p.log("Converting %s (%s) to qcow2", filepath.Base(d.path), f)
		if err := convertImage(ctx, d.path, f, dst, false, p.scaled(from+float64(i)*step, from+float64(i+1)*step)); err != nil {
			return err
		}
		if i == 0 && r.DiskGB > 0 {
			if err := resizeImage(dst, r.DiskGB); err != nil {
				return err
			}
		}
		spec.disks = append(spec.disks, "path="+dst+",format=qcow2,bus="+diskBus(r.OS)+",cache=none,discard=unmap")
	}
	if err := install(ctx, r.CreateReq, spec, p.Log); err != nil {
		return err
	}
	ok = true
	p.set(100)
	return nil
}

// applyOVF fills the settings a request leaves out with those of the appliance.
func applyOVF(r *CreateReq, o *OVF) {
	if o == nil {
		return
	}
	if r.VCPUs == 0 {
		r.VCPUs = o.VCPUs
	}
	if r.MemoryMB == 0 {
		r.MemoryMB = o.MemoryMB
	}
	if r.Firmware == "" {
		r.Firmware = o.Firmware
	}
	if r.OS == "" {
		r.OS = o.OS
	}
}
