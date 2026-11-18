package vms

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"strataos/internal/sys"
)

// macOS on KVM, following the reference setup of OSX-KVM (kholia): an OpenCore
// boot disk boots the VM, Apple's recovery image downloads macOS itself, and
// the installation is finished in the console. The recovery protocol below is
// the one of OpenCorePkg's macrecovery utility (also fetch-macOS-v2.py).

type MacOSVersion struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// osrecovery.apple.com parameters and the QEMU CPU that macOS accepts.
	bid    string // board-id of a real Mac shipped with that release
	osType string // default (oldest for the board) or latest
	cpu    string // full -cpu value (model, kvm=on, vendor spoof, flags)
}

func macosVersions() []MacOSVersion {
	common := "+ssse3,+sse4.2,+popcnt,+avx,+aes,+xsave,+xsaveopt,check"
	return []MacOSVersion{
		{ID: "sonoma", Name: "macOS Sonoma (14)", bid: "Mac-827FAC58A8FDFA22", osType: "default",
			cpu: "Haswell-noTSX,kvm=on,vendor=GenuineIntel,+invtsc,vmware-cpuid-freq=on," + common},
		{ID: "sequoia", Name: "macOS Sequoia (15)", bid: "Mac-7BA5B2D9E42DDD94", osType: "default",
			cpu: "Skylake-Client,-hle,-rtm,kvm=on,vendor=GenuineIntel,+invtsc,vmware-cpuid-freq=on," + common},
		{ID: "tahoe", Name: "macOS Tahoe (26)", bid: "Mac-CFF7D910A743CAAF", osType: "latest",
			cpu: "Skylake-Client,-hle,-rtm,kvm=on,vendor=GenuineIntel,+invtsc,vmware-cpuid-freq=on," + common},
	}
}

func macosVersion(id string) (MacOSVersion, error) {
	for _, v := range macosVersions() {
		if v.ID == id {
			return v, nil
		}
	}
	return MacOSVersion{}, fmt.Errorf("unknown macOS version %q", id)
}

// MacOSVersions lists the versions a NAS can install (x86-64 with KVM only:
// macOS has no ARM64 KVM build, and OpenCore expects an Intel-like CPU).
func MacOSVersions() []MacOSVersion {
	list := macosVersions()
	supported := runtime.GOARCH == "amd64" && KVM()
	for i := range list {
		if !supported {
			list[i].Name += " (unavailable on this NAS)"
		}
	}
	return list
}

const (
	recoveryHost = "osrecovery.apple.com"
	recoveryUA   = "InternetRecovery/1.0"
	// OSX-KVM's OpenCore boot disk, pinned to a commit and its SHA-256: an
	// update of the project's branch is never taken without review.
	openCoreURL    = "https://raw.githubusercontent.com/kholia/OSX-KVM/4c378a4b5e0b219783683012bec680325eb40719/OpenCore/OpenCore.qcow2"
	openCoreSHA256 = "6ed36c0c2a4206ccc695f6b1a734a1cc6f94d288b0517c705d351c63cb92a6f3"
	applesmcOSK    = "ourhardworkbythesewordsguardedpleasedontsteal(c)AppleComputerInc"
	appleKeyHex    = "C3E748CAD9CD384329E10E25A91E43E1A762FF529ADE578C935BDDF9B13F2179D4855E6FC89E9E29CA12517D17DFA1EDCE0BEBF0EA7B461FFE61D94E2BDF72C196F89ACD3536B644064014DAE25A15DB6BB0852ECBD120916318D1CCDEA3C84C92ED743FC176D0BACA920D3FCF3158AFF731F88CE0623182A8ED67E650515F75745909F07D415F55FC15A35654D118C55A462D37A3ACDA08612F3F3F6571761EFCCBCC299AEE99B3A4FD6212CCFFF5EF37A2C334E871191F7E1C31960E010A54E86FA3F62E6D6905E1CD57732410A3EB0C6B4DEFDABE9F59BF1618758C751CD56CEF851D1C0EAA1C558E37AC108DA9089863D20E2E7E4BF475EC66FE6B3EFDCF"
	recoveryMagic  = "CNKL"
)

func randHex(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

func recoveryRequest(ctx context.Context, method, url, cookie string, body string) (string, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Host = recoveryHost
	req.Header.Set("User-Agent", recoveryUA)
	req.Header.Set("Connection", "close")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if body != "" {
		req.Header.Set("Content-Type", "text/plain")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), resp.Header, err
}

// recoveryInfo asks Apple's recovery servers for the BaseSystem image of a
// macOS release: an anonymous protocol, the same one Macs use for internet
// recovery (session cookie, then a text payload with the image links).
func recoveryInfo(ctx context.Context, v MacOSVersion) (dmgURL, dmgSess, cnkURL, cnkSess string, err error) {
	_, head, err := recoveryRequest(ctx, http.MethodGet, "http://"+recoveryHost+"/", "", "")
	if err != nil {
		return "", "", "", "", err
	}
	var session string
	for _, c := range head.Values("Set-Cookie") {
		for _, part := range strings.Split(c, "; ") {
			if strings.HasPrefix(part, "session=") {
				session = part
			}
		}
	}
	if session == "" {
		return "", "", "", "", fmt.Errorf("no session from %s", recoveryHost)
	}
	body := strings.Join([]string{
		"cid=" + randHex(16), "sn=00000000000000000", "bid=" + v.bid,
		"k=" + randHex(64), "fg=" + randHex(64), "os=" + v.osType,
	}, "\n")
	out, _, err := recoveryRequest(ctx, http.MethodPost, "http://"+recoveryHost+"/InstallationPayload/RecoveryImage", session, body)
	if err != nil {
		return "", "", "", "", err
	}
	info := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if k, val, ok := strings.Cut(sc.Text(), ": "); ok {
			info[k] = strings.TrimSpace(val)
		}
	}
	for _, k := range []string{"AU", "AT", "CU", "CT"} {
		if info[k] == "" {
			return "", "", "", "", fmt.Errorf("incomplete recovery payload (missing %s)", k)
		}
	}
	return info["AU"], info["AT"], info["CU"], info["CT"], nil
}

// downloadAsset fetches a recovery asset with its AssetToken cookie.
func downloadAsset(ctx context.Context, url, sess, path string, p Progress) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", recoveryUA)
	req.Header.Set("Connection", "close")
	req.Header.Set("Cookie", "AssetToken="+sess)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	total, done := resp.ContentLength, int64(0)
	buf := make([]byte, 1<<20)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			done += int64(n)
			if total > 0 && time.Since(last) > time.Second {
				p.set(float64(done) * 100 / float64(total))
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	return f.Close()
}

// Apple EFI ROM public key: RSA-2048, exponent 65537 (macrecovery).
func verifyChunklistSignature(chunklist []byte, sigOffset int, digest []byte) error {
	sig := make([]byte, 256)
	copy(sig, chunklist[sigOffset:sigOffset+256])
	for i, j := 0, len(sig)-1; i < j; i, j = i+1, j-1 { // little-endian integer
		sig[i], sig[j] = sig[j], sig[i]
	}
	n, ok := new(big.Int).SetString(appleKeyHex, 16)
	if !ok {
		return fmt.Errorf("bad Apple public key")
	}
	// EMSA-PKCS1-v1_5-like block for SHA-256, as built by macrecovery.
	plain, _ := new(big.Int).SetString("1"+strings.Repeat("f", 404)+"003031300d060960864801650304020105000420"+hex.EncodeToString(digest), 16)
	got := new(big.Int).Exp(new(big.Int).SetBytes(sig), big.NewInt(65537), n)
	if plain == nil || got.Cmp(plain) != 0 {
		return fmt.Errorf("invalid chunklist signature")
	}
	return nil
}

// maxChunk bounds a chunk of BaseSystem.dmg (Apple's are 10 MiB).
const maxChunk = 64 << 20

// verifyChunklist checks BaseSystem.dmg against Apple's signed chunklist.
// Only RSA-signed chunklists (method 1) are accepted: method 2 holds a bare
// digest anyone can compute, and the recovery protocol is plain HTTP.
func verifyChunklist(dmgPath, cnkPath string) error {
	cnk, err := os.ReadFile(cnkPath)
	if err != nil {
		return err
	}
	if len(cnk) < 0x24 || string(cnk[0:4]) != recoveryMagic || binary.LittleEndian.Uint32(cnk[4:8]) != 0x24 || cnk[8] != 1 || cnk[9] != 1 {
		return fmt.Errorf("unsupported chunklist")
	}
	if cnk[10] != 1 {
		return fmt.Errorf("the chunklist is not signed by Apple (method %d)", cnk[10])
	}
	count := binary.LittleEndian.Uint64(cnk[12:20])
	chunkOffset := binary.LittleEndian.Uint64(cnk[20:28])
	sigOffset := binary.LittleEndian.Uint64(cnk[28:36])
	// Bounded before any arithmetic: 36*count must not wrap around.
	if chunkOffset != 0x24 || count == 0 || count > uint64(len(cnk))/36 ||
		sigOffset != chunkOffset+36*count || sigOffset+256 > uint64(len(cnk)) {
		return fmt.Errorf("malformed chunklist")
	}
	h := sha256.New()
	h.Write(cnk[:chunkOffset])
	f, err := os.Open(dmgPath)
	if err != nil {
		return err
	}
	defer f.Close()
	entries := chunkOffset
	for i := uint64(0); i < count; i++ {
		e := cnk[entries : entries+36]
		size := binary.LittleEndian.Uint32(e[0:4])
		want := e[4:36] // 32 bytes of SHA-256
		if size == 0 || size > maxChunk {
			return fmt.Errorf("malformed chunklist")
		}
		h.Write(e)
		chunk := make([]byte, size)
		if _, err := io.ReadFull(f, chunk); err != nil {
			return fmt.Errorf("BaseSystem.dmg is shorter than its chunklist")
		}
		if sum := sha256.Sum256(chunk); !bytes.Equal(sum[:], want) {
			return fmt.Errorf("chunk %d: hash mismatch", i+1)
		}
		entries += 36
	}
	digest := h.Sum(nil)
	if rest, err := io.ReadAll(io.LimitReader(f, 1)); err == nil && len(rest) > 0 {
		return fmt.Errorf("BaseSystem.dmg is larger than its chunklist")
	}
	return checkChunklistSignature(cnk, int(sigOffset), digest)
}

// checkChunklistSignature is replaced by the tests (only Apple can sign).
var checkChunklistSignature = verifyChunklistSignature

// EnsureOpenCore downloads the OpenCore boot disk (OSX-KVM's prebuilt image,
// pinned) once into the volume's cache folder, under a name of its own: the
// unpinned OpenCore.qcow2 of earlier versions stays for the VMs that use it.
func EnsureOpenCore(ctx context.Context, cacheDir string, p Progress) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(cacheDir, "OpenCore-"+openCoreSHA256[:12]+".qcow2")
	unlock, err := lockDownload(ctx, path)
	if err != nil {
		return "", err
	}
	defer unlock()
	if verifyChecksum(path, "sha256", openCoreSHA256) == nil {
		return path, standalone(path, "qcow2")
	}
	p.log("Downloading the OpenCore boot disk (OSX-KVM)")
	resp, err := httpGet(ctx, openCoreURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 256<<20))
	f.Close()
	if err == nil {
		err = verifyChecksum(tmp, "sha256", openCoreSHA256)
	}
	if err == nil {
		err = standalone(tmp, "qcow2")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("OpenCore boot disk: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// EnsureBaseSystem fetches (once) and verifies Apple's recovery image of a
// macOS release, converted to a raw disk QEMU can boot.
func EnsureBaseSystem(ctx context.Context, v MacOSVersion, cacheDir string, p Progress) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	img := filepath.Join(cacheDir, "BaseSystem-"+v.ID+".img")
	unlock, err := lockDownload(ctx, img)
	if err != nil {
		return "", err
	}
	defer unlock()
	if fi, err := os.Stat(img); err == nil && fi.Size() > 64<<20 {
		p.set(100)
		return img, nil
	}
	dmgURL, dmgSess, cnkURL, cnkSess, err := recoveryInfo(ctx, v)
	if err != nil {
		return "", fmt.Errorf("cannot reach Apple's recovery servers: %v", err)
	}
	dmg := filepath.Join(cacheDir, "BaseSystem-"+v.ID+".dmg")
	cnk := dmg + ".chunklist"
	p.log("Downloading BaseSystem.dmg (%s)", v.Name)
	if err := downloadAsset(ctx, cnkURL, cnkSess, cnk, p.scaled(0, 2)); err != nil {
		return "", err
	}
	if err := downloadAsset(ctx, dmgURL, dmgSess, dmg, p.scaled(2, 40)); err != nil {
		return "", err
	}
	p.log("Verifying against Apple's signed chunklist")
	if err := verifyChunklist(dmg, cnk); err != nil {
		_ = os.Remove(dmg)
		return "", err
	}
	p.log("qemu-img convert BaseSystem.dmg -> raw")
	// Converted under a temporary name: a raw image has its full size from
	// the start, so an interrupted conversion (crash, power cut) left under
	// the final name would pass the size check above and be used half empty.
	part := img + ".part"
	_ = os.Remove(img)
	_ = os.Remove(part)
	args := []string{"convert", "-p", "-f", "dmg", "-O", "raw", "--", dmg, part}
	if _, err := sys.RunOpts(ctx, sys.Opts{Timeout: 2 * time.Hour, Output: &pctWriter{p: p.scaled(40, 95)}}, "qemu-img", args...); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, img); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	_ = os.Remove(dmg)
	_ = os.Remove(cnk)
	p.set(100)
	return img, nil
}

// MacOSReq creates a macOS VM (OpenCore + Apple's recovery image).
type MacOSReq struct {
	CreateReq
	Version string `json:"version"`
	// IgnoreMSRs lets KVM ignore the CPU registers (MSR) it does not know,
	// which macOS reads: a setting of the whole NAS (every VM), kept after
	// a restart. Only changed when asked.
	IgnoreMSRs bool `json:"ignoreMsrs"`
}

const (
	ignoreMSRsParam = "/sys/module/kvm/parameters/ignore_msrs"
	ignoreMSRsConf  = "/etc/modprobe.d/strataos-macos.conf"
)

const cpuinfoPath = "/proc/cpuinfo"

// invariantTSC reports whether the CPU's TSC runs at a constant rate in every
// power state (the invtsc CPUID bit, shown as constant_tsc + nonstop_tsc).
func invariantTSC(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return true // unknown: no warning
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.TrimSpace(k) == "flags" {
			f := " " + v + " "
			return strings.Contains(f, " constant_tsc ") && strings.Contains(f, " nonstop_tsc ")
		}
	}
	return true
}

// ignoringMSRs reports whether KVM ignores unknown MSRs now.
func ignoringMSRs() bool {
	b, err := os.ReadFile(ignoreMSRsParam)
	return err == nil && (strings.TrimSpace(string(b)) == "Y" || strings.TrimSpace(string(b)) == "1")
}

// ignoreMSRs turns ignore_msrs on now and at every start of the NAS.
func ignoreMSRs() error {
	if err := os.WriteFile(ignoreMSRsParam, []byte("1"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(ignoreMSRsConf, []byte("# Written by StrataOS (macOS VMs): KVM ignores unknown MSRs, for every VM.\noptions kvm ignore_msrs=1 report_ignored_msrs=0\n"), 0o644)
}

// CreateMacOS builds a macOS VM: OpenCore boot disk, Apple's BaseSystem and an
// empty target disk, all on SATA, with the Apple SMC device macOS requires.
// The user finishes the installation in the console (it downloads several GB
// from Apple inside the guest).
func CreateMacOS(ctx context.Context, r MacOSReq, p Progress) error {
	if runtime.GOARCH != "amd64" || !KVM() {
		return fmt.Errorf("macOS needs KVM on an x86-64 NAS")
	}
	v, err := macosVersion(r.Version)
	if err != nil {
		return err
	}
	if r.MemoryMB < 4096 {
		return fmt.Errorf("macOS needs at least 4096 MiB of memory")
	}
	if r.DiskGB < 32 {
		return fmt.Errorf("macOS needs at least a 32 GiB disk")
	}
	r.Firmware, r.OS, r.CPUModel = "uefi", "other", "none"
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
	// macOS reads MSR registers KVM does not emulate: unless they are
	// ignored (a host-wide KVM setting, no per-VM equivalent), it can stop
	// at boot. Changed only when asked, and said either way.
	switch {
	case ignoringMSRs():
	case r.IgnoreMSRs:
		if err := ignoreMSRs(); err != nil {
			p.log("Warning: cannot make KVM ignore unknown MSRs: %v", err)
		} else {
			p.log("KVM now ignores unknown MSRs for every VM of this NAS, also after a restart (%s)", ignoreMSRsConf)
		}
	default:
		p.log("Warning: KVM does not ignore unknown MSRs: macOS may stop at boot (option of the macOS VM creation)")
	}
	// macOS's booter divides by the TSC frequency, which QEMU only gives
	// (vmware-cpuid-freq) with an invariant TSC: without it, boot.efi stops
	// on a divide error right after the OpenCore picker. Typical of a NAS
	// that is itself a VM ("-cpu host" leaves invtsc out).
	if !invariantTSC(cpuinfoPath) {
		p.log("Warning: this CPU does not report an invariant TSC (constant_tsc, nonstop_tsc), as when StrataOS itself runs in a VM: macOS stops at boot. Give StrataOS's VM the invtsc CPU flag (QEMU: -cpu host,+invtsc)")
	}
	cache := filepath.Join(VolumeRoot(dir), cacheDirName)
	oc, err := EnsureOpenCore(ctx, cache, p.scaled(0, 5))
	if err != nil {
		return err
	}
	base, err := EnsureBaseSystem(ctx, v, cache, p.scaled(5, 60))
	if err != nil {
		return err
	}
	// The cache's images are shared by every macOS VM of the volume and only
	// ever read: each VM writes to its own overlays, in its own folder (two
	// macOS VMs can run at once, and the VM's folder is its first disk's).
	ocDisk, baseDisk := filepath.Join(dir, "opencore.qcow2"), filepath.Join(dir, "basesystem.qcow2")
	if err := linkImage(oc, "qcow2", ocDisk); err != nil {
		return err
	}
	if err := linkImage(base, "raw", baseDisk); err != nil {
		return err
	}
	disk := filepath.Join(dir, "disk0.qcow2")
	if _, err := sys.Run("qemu-img", "create", "-f", "qcow2", "--", disk, fmt.Sprintf("%dG", r.DiskGB)); err != nil {
		return err
	}
	spec := installSpec{import_: true, video: "vmvga",
		disks: []string{
			"path=" + ocDisk + ",format=qcow2,bus=sata,boot.order=1",
			"path=" + baseDisk + ",format=qcow2,bus=sata,cache=none,boot.order=2",
			"path=" + disk + ",format=qcow2,bus=sata,cache=none,discard=unmap,boot.order=3",
		},
		// The serial console logs the boot; OpenCore prints its progress there.
		// The QEMU arguments are given as --qemu-commandline=<value>: in two
		// words, virt-install takes a value starting with "-" for an option.
		extra: []string{
			"--serial", "pty,log.file=" + consoleLogPath(r.Name),
			"--qemu-commandline=-cpu " + v.cpu,
			"--qemu-commandline=-device isa-applesmc,osk=" + applesmcOSK,
			"--qemu-commandline=-smbios type=2",
			// A USB keyboard and tablet as in OSX-KVM, on an xHCI controller
			// of their own (virt-install adds no USB bus for "generic").
			"--controller", "usb,model=qemu-xhci",
			"--input", "keyboard,bus=usb",
			"--input", "tablet,bus=usb",
		},
	}
	if err := install(ctx, r.CreateReq, spec, p.Log); err != nil {
		return err
	}
	ok = true
	_ = writeMeta(r.Name, strataMeta{Cloud: "macos-" + v.ID})
	p.log("Finish the macOS installation in the console (Disk Utility first: erase the largest disk as APFS, then Reinstall macOS)")
	p.set(100)
	return nil
}
