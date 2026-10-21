package vms

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sampleDomain = `<domain type='kvm'>
  <name>web</name>
  <uuid>1b4e28ba-2fa1-11d2-883f-0016d3cca427</uuid>
  <metadata>
    <libosinfo:libosinfo xmlns:libosinfo="http://libosinfo.org/xmlns/libvirt/domain/1.0">
      <libosinfo:os id="http://debian.org/debian/13"/>
    </libosinfo:libosinfo>
    <strata:vm xmlns:strata="https://strataos.org/xmlns/vm/1.0" template="yes"/>
  </metadata>
  <memory unit='KiB'>524288</memory>
  <vcpu placement='static'>1</vcpu>
  <os firmware='efi'>
    <type arch='x86_64' machine='pc-q35-9.2'>hvm</type>
    <nvram template='/usr/share/OVMF/OVMF_VARS_4M.fd'>/var/lib/libvirt/qemu/nvram/web_VARS.fd</nvram>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' cache='none'/>
      <source file='/srv/strata/main/VMs/web/disk0.snap-s1.qcow2'/>
      <backingStore type='file'><format type='qcow2'/><source file='/srv/strata/main/VMs/web/disk0.qcow2'/></backingStore>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/srv/strata/main/VMs/web/seed.iso'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
    <disk type='file' device='cdrom'>
      <source file='/srv/strata/main/ISO/debian.iso'/>
      <target dev='sdb' bus='sata'/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:11:22:33'/>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <serial type='pty'><log file='/srv/strata/main/VMs/web/console.log'/><target port='0'/></serial>
    <channel type='unix'><source mode='bind' path='/run/libvirt/qemu/channel/1-web/org.qemu.guest_agent.0'/><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
    <hostdev mode='subsystem' type='usb' managed='yes'><source><vendor id='0x10c4'/><product id='0xea60'/></source></hostdev>
    <hostdev mode='subsystem' type='pci' managed='yes'><source><address domain='0x0000' bus='0x01' slot='0x00' function='0x0'/></source></hostdev>
  </devices>
</domain>`

func TestXMLRoundTrip(t *testing.T) {
	root, err := parseXML(sampleDomain)
	if err != nil {
		t.Fatal(err)
	}
	again, err := parseXML(root.String())
	if err != nil {
		t.Fatal(err)
	}
	if root.String() != again.String() {
		t.Fatal("round trip changed the document")
	}
	if !strings.Contains(root.String(), `<libosinfo:os id="http://debian.org/debian/13"/>`) {
		t.Fatalf("namespace prefix lost: %s", root.String())
	}
	if got := root.path("devices/disk")[0].child("source").attr("file"); got != "/srv/strata/main/VMs/web/disk0.snap-s1.qcow2" {
		t.Fatal(got)
	}
}

func TestRewriteClone(t *testing.T) {
	out, err := rewriteClone(sampleDomain, cloneOpts{
		Name: "web2", OldDir: "/srv/strata/main/VMs/web", NewDir: "/srv/strata/main/VMs/web2",
		Disks: map[string]string{"/srv/strata/main/VMs/web/disk0.snap-s1.qcow2": "/srv/strata/main/VMs/web2/disk0.qcow2"},
		NVRAM: "/var/lib/libvirt/qemu/nvram/web2_VARS.fd", Seed: "/srv/strata/main/VMs/web2/seed.iso",
		MAC: func() string { return "52:54:00:aa:bb:cc" },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<name>web2</name>",
		`<source file="/srv/strata/main/VMs/web2/disk0.qcow2"/>`,
		`<source file="/srv/strata/main/VMs/web2/seed.iso"/>`,
		`<source file="/srv/strata/main/ISO/debian.iso"/>`,
		`<mac address="52:54:00:aa:bb:cc"/>`,
		">/var/lib/libvirt/qemu/nvram/web2_VARS.fd</nvram>",
		`<log file="/srv/strata/main/VMs/web2/console.log"/>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	for _, bad := range []string{"<uuid>", "backingStore", "52:54:00:11:22:33", "hostdev", "strata:vm", "channel/1-web", "/VMs/web/"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %s in\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "libosinfo:os") {
		t.Error("libosinfo metadata lost")
	}
	// A disk that was not copied is an error, not a shared disk.
	if _, err := rewriteClone(sampleDomain, cloneOpts{Name: "x", Disks: map[string]string{}, MAC: randomMAC}); err == nil {
		t.Error("uncopied disk accepted")
	}
	// Restore in place: identity and USB kept, seed ejected.
	out, err = rewriteClone(sampleDomain, cloneOpts{Name: "web", OldDir: "/srv/strata/main/VMs/web", NewDir: "/srv/strata/fast/web",
		Disks: map[string]string{"/srv/strata/main/VMs/web/disk0.snap-s1.qcow2": "/srv/strata/fast/web/disk0.qcow2"}, MAC: randomMAC, KeepUSB: true, KeepIdentity: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<uuid>1b4e28ba", "52:54:00:11:22:33", `type="usb"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(out, `type="pci"`) || strings.Contains(out, "seed.iso") {
		t.Error("PCI or old seed kept")
	}
}

func TestNVRAMOf(t *testing.T) {
	if got := nvramOf(sampleDomain); got != "/var/lib/libvirt/qemu/nvram/web_VARS.fd" {
		t.Fatal(got)
	}
}

func TestHostdevXML(t *testing.T) {
	if got := usbHostdevXML("10c4", "ea60", 0, 0); got != "<hostdev mode='subsystem' type='usb' managed='yes'><source startupPolicy='optional'><vendor id='0x10c4'/><product id='0xea60'/></source></hostdev>" {
		t.Fatal(got)
	}
	if got := usbHostdevXML("10c4", "ea60", 1, 5); !strings.Contains(got, "<address bus='1' device='5'/>") {
		t.Fatal(got)
	}
	got, err := pciHostdevXML("0000:01:00.1")
	if err != nil || !strings.Contains(got, "domain='0x0000' bus='0x01' slot='0x00' function='0x1'") {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"01:00.0", "0000:01:00.8", "0000:01:00.0'/><x", ""} {
		if _, err := pciHostdevXML(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got := pciAddr("0x0000", "0x01", "0x1f", "0x3"); got != "0000:01:1f.3" {
		t.Fatal(got)
	}
	hs := usbHostdevs(sampleDomain, "10c4", "ea60")
	if len(hs) != 1 || usbHostdevs(sampleDomain, "10c4", "0001") != nil {
		t.Fatal("usb hostdev lookup", hs)
	}
}

func TestPickUSB(t *testing.T) {
	host := []USBDevice{{Vendor: "10c4", Product: "ea60", Serial: "A", Bus: 1, Device: 3}, {Vendor: "10c4", Product: "ea60", Serial: "B", Bus: 2, Device: 7}, {Vendor: "0627", Product: "0001", Bus: 1, Device: 2}}
	if b, d := pickUSB(host, "0627", "0001", ""); b != 0 || d != 0 {
		t.Error("a single device must not be pinned")
	}
	if b, d := pickUSB(host, "10c4", "ea60", "B"); b != 2 || d != 7 {
		t.Error("serial not used", b, d)
	}
}

func TestHostUSB(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, files map[string]string) {
		_ = os.MkdirAll(filepath.Join(dir, name), 0o755)
		for f, v := range files {
			_ = os.WriteFile(filepath.Join(dir, name, f), []byte(v+"\n"), 0o644)
		}
	}
	mk("usb1", map[string]string{"idVendor": "1d6b", "idProduct": "0002", "bDeviceClass": "09"})
	mk("1-1", map[string]string{"idVendor": "10c4", "idProduct": "ea60", "bDeviceClass": "00", "product": "Sonoff Zigbee 3.0", "manufacturer": "ITead", "serial": "abc", "busnum": "1", "devnum": "4"})
	mk("1-1:1.0", map[string]string{"bInterfaceClass": "ff"})
	mk("1-2", map[string]string{"idVendor": "0781", "idProduct": "5581", "bDeviceClass": "00", "busnum": "1", "devnum": "5"})
	mk("1-2/1-2:1.0", map[string]string{"bInterfaceClass": "08"})
	old := USBRoot
	USBRoot = dir
	defer func() { USBRoot = old }()
	list := HostUSB()
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
	if list[0].Name != "ITead Sonoff Zigbee 3.0" || list[0].Serial != "abc" || list[0].Bus != 1 || list[0].Device != 4 || list[0].Internal {
		t.Errorf("%+v", list[0])
	}
	if !list[1].Internal || list[1].Name != "USB device 0781:5581" {
		t.Errorf("mass storage: %+v", list[1])
	}
}

const vmwareOVF = `<?xml version="1.0" encoding="UTF-8"?>
<Envelope xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1" xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData" xmlns:vmw="http://www.vmware.com/schema/ovf">
  <References>
    <File ovf:href="app-disk1.vmdk" ovf:id="file1" ovf:size="1024"/>
    <File ovf:href="app-disk2.vmdk" ovf:id="file2" ovf:size="1024"/>
  </References>
  <DiskSection>
    <Disk ovf:capacity="20" ovf:capacityAllocationUnits="byte * 2^30" ovf:diskId="vmdisk2" ovf:fileRef="file2"/>
    <Disk ovf:capacity="10" ovf:capacityAllocationUnits="byte * 2^30" ovf:diskId="vmdisk1" ovf:fileRef="file1"/>
  </DiskSection>
  <VirtualSystem ovf:id="appliance">
    <Name>Home Assistant</Name>
    <OperatingSystemSection ovf:id="101" vmw:osType="otherLinux64Guest"><Description>Other Linux (64-bit)</Description></OperatingSystemSection>
    <VirtualHardwareSection>
      <Item><rasd:ElementName>2 virtual CPU(s)</rasd:ElementName><rasd:ResourceType>3</rasd:ResourceType><rasd:VirtualQuantity>2</rasd:VirtualQuantity></Item>
      <Item><rasd:AllocationUnits>byte * 2^20</rasd:AllocationUnits><rasd:ResourceType>4</rasd:ResourceType><rasd:VirtualQuantity>4096</rasd:VirtualQuantity></Item>
      <Item><rasd:HostResource>ovf:/disk/vmdisk1</rasd:HostResource><rasd:ResourceType>17</rasd:ResourceType></Item>
      <Item><rasd:HostResource>ovf:/disk/vmdisk2</rasd:HostResource><rasd:ResourceType>17</rasd:ResourceType></Item>
      <vmw:Config ovf:required="false" vmw:key="firmware" vmw:value="efi"/>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>`

const vboxOVF = `<?xml version="1.0"?>
<Envelope ovf:version="1.0" xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1" xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData" xmlns:vbox="http://www.virtualbox.org/ovf/machine">
  <References><File ovf:id="file1" ovf:href="Win10-disk001.vmdk"/></References>
  <DiskSection><Disk ovf:capacity="53687091200" ovf:diskId="vmdisk1" ovf:fileRef="file1"/></DiskSection>
  <VirtualSystem ovf:id="Win10">
    <OperatingSystemSection ovf:id="105"><Description>Microsoft Windows 10</Description><vbox:OSType ovf:required="false">Windows10_64</vbox:OSType></OperatingSystemSection>
    <VirtualHardwareSection>
      <Item><rasd:ResourceType>3</rasd:ResourceType><rasd:VirtualQuantity>4</rasd:VirtualQuantity></Item>
      <Item><rasd:AllocationUnits>MegaBytes</rasd:AllocationUnits><rasd:ResourceType>4</rasd:ResourceType><rasd:VirtualQuantity>8192</rasd:VirtualQuantity></Item>
      <Item><rasd:HostResource>/disk/vmdisk1</rasd:HostResource><rasd:ResourceType>17</rasd:ResourceType></Item>
    </VirtualHardwareSection>
    <vbox:Machine ovf:required="false" name="Win10"><Hardware><Firmware type="EFI"/></Hardware></vbox:Machine>
  </VirtualSystem>
</Envelope>`

func TestParseOVF(t *testing.T) {
	o, err := ParseOVF([]byte(vmwareOVF))
	if err != nil {
		t.Fatal(err)
	}
	want := &OVF{Name: "Home Assistant", VCPUs: 2, MemoryMB: 4096, Firmware: "uefi", OS: "linux", Disks: []string{"app-disk1.vmdk", "app-disk2.vmdk"}}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("vmware: %+v", o)
	}
	o, err = ParseOVF([]byte(vboxOVF))
	if err != nil {
		t.Fatal(err)
	}
	want = &OVF{Name: "Win10", VCPUs: 4, MemoryMB: 8192, Firmware: "uefi", OS: "windows", Disks: []string{"Win10-disk001.vmdk"}}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("virtualbox: %+v", o)
	}
	evil := strings.Replace(vboxOVF, `ovf:href="Win10-disk001.vmdk"`, `ovf:href="../../etc/shadow"`, 1)
	if _, err := ParseOVF([]byte(evil)); err == nil {
		t.Error("path traversal in OVF accepted")
	}
	if _, err := ParseOVF([]byte("not xml")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestAllocMB(t *testing.T) {
	for _, c := range []struct {
		q     int64
		units string
		want  int
	}{{2, "byte * 2^30", 2048}, {512, "byte * 2^20", 512}, {1048576, "byte * 2^10", 1024}, {1 << 30, "byte", 1024}, {768, "", 768}, {4, "GigaBytes", 4096}} {
		if got := allocMB(c.q, c.units); got != c.want {
			t.Errorf("%d %s: %d", c.q, c.units, got)
		}
	}
}

func TestImportFormat(t *testing.T) {
	for in, want := range map[string]string{"a.OVA": "ova", "b.vmdk": "vmdk", "c.img": "raw", "d.vhd": "vpc", "e.vhdx": "vhdx", "f.qcow2": "qcow2", "g.raw": "raw"} {
		if got, err := importFormat(in); err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	if _, err := importFormat("x.exe"); err == nil {
		t.Error("exe accepted")
	}
}

func TestSHA512Crypt(t *testing.T) {
	// Test vectors of the SHA-crypt specification.
	if got := sha512Crypt("Hello world!", "saltstring"); got != "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1" {
		t.Fatal(got)
	}
	if got := sha512Crypt("a very much longer text to encrypt.  This one even stretches over morethan one line.", "toolongsaltstring"); !strings.HasPrefix(got, "$6$toolongsaltstrin$") {
		t.Fatal(got)
	}
	if len(randomSalt()) != 16 {
		t.Fatal("salt length")
	}
}

func TestCloudInitValidate(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk2s0j4p6l0b8f3VZ0dS3m4o5p6q7r8s9t0u1v2w3x4 admin@nas"
	ok := CloudInit{Hostname: "web-1", User: "debian", Password: "secret1", SSHKeys: []string{key + "\n\n# comment"}, Packages: []string{"nginx", "htop"}, Address: "192.168.1.50/24", Gateway: "192.168.1.1", DNS: []string{"1.1.1.1"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(ok.SSHKeys) != 1 {
		t.Fatal(ok.SSHKeys)
	}
	bad := []CloudInit{
		{Hostname: "web_1", User: "debian", Password: "secret1"},
		{Hostname: "web", User: "root", Password: "secret1"},
		{Hostname: "web", User: "debian"},
		{Hostname: "web", User: "debian", Password: "x"},
		{Hostname: "web", User: "debian", SSHKeys: []string{"ssh-ed25519 AAAA\nrm -rf /"}},
		{Hostname: "web", User: "debian", Password: "secret1", Packages: []string{"nginx; reboot"}},
		{Hostname: "web", User: "debian", Password: "secret1", Address: "192.168.1.50"},
		{Hostname: "web", User: "debian", Password: "secret1", Gateway: "192.168.1.1"},
		{Hostname: "web", User: "debian", Password: "secret1", DNS: []string{"dns.example"}},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestSeedFiles(t *testing.T) {
	c := CloudInit{Hostname: "web", User: "debian", SSHKeys: []string{"ssh-ed25519 AAAAkey me@x"}, Packages: []string{"nginx", "qemu-guest-agent"}}
	meta, user, network := seedFiles(c, "web-1234", "$6$salt$hash")
	if meta != "instance-id: \"web-1234\"\nlocal-hostname: \"web\"\n" {
		t.Errorf("meta-data: %q", meta)
	}
	for _, want := range []string{"#cloud-config\n", `hostname: "web"`, `- name: "debian"`, `passwd: "$6$salt$hash"`, "lock_passwd: false", `- "ssh-ed25519 AAAAkey me@x"`, "ssh_pwauth: true", `- "nginx"`, "systemctl, enable, --now, qemu-guest-agent", CloudDoneMarker} {
		if !strings.Contains(user, want) {
			t.Errorf("user-data lacks %s:\n%s", want, user)
		}
	}
	if strings.Count(user, `"qemu-guest-agent"`) != 1 {
		t.Error("package listed twice")
	}
	if network != "" {
		t.Error("network-config without a static address")
	}
	c.Address, c.Gateway, c.DNS = "10.0.0.5/24", "10.0.0.1", []string{"10.0.0.1", "9.9.9.9"}
	_, user, network = seedFiles(c, "id", "")
	if strings.Contains(user, "passwd:") || !strings.Contains(user, "ssh_pwauth: false") {
		t.Error("password set without one")
	}
	for _, want := range []string{"version: 2", `addresses: ["10.0.0.5/24"]`, `via: "10.0.0.1"`, `addresses: ["10.0.0.1", "9.9.9.9"]`, "dhcp4: false"} {
		if !strings.Contains(network, want) {
			t.Errorf("network-config lacks %s:\n%s", want, network)
		}
	}
	// Values are quoted: no YAML injection through a host name or key comment.
	c.SSHKeys = []string{"ssh-ed25519 AAAA x\": y"}
	_, user, _ = seedFiles(c, "id", "")
	if !strings.Contains(user, `"ssh-ed25519 AAAA x\": y"`) {
		t.Error(user)
	}
}

func TestChecksums(t *testing.T) {
	sums := parseChecksums(strings.NewReader("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 *empty.img\nbad line\n" +
		"cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e  debian.qcow2\n"))
	if len(sums) != 2 || sums["empty.img"][:6] != "e3b0c4" || sums["debian.qcow2"][:6] != "cf83e1" {
		t.Fatal(sums)
	}
	f := filepath.Join(t.TempDir(), "empty.img")
	_ = os.WriteFile(f, nil, 0o644)
	if err := verifyChecksum(f, "sha256", sums["empty.img"]); err != nil {
		t.Error(err)
	}
	if err := verifyChecksum(f, "sha512", sums["debian.qcow2"]); err != nil {
		t.Error(err)
	}
	_ = os.WriteFile(f, []byte("x"), 0o644)
	if err := verifyChecksum(f, "sha256", sums["empty.img"]); err == nil {
		t.Error("tampered file accepted")
	}
}

func TestAutostartOrder(t *testing.T) {
	list := []AutostartEntry{{Name: "web", Order: 2}, {Name: "db", Order: 1, Delay: 30}, {Name: "app", Order: 2}, {Name: "dns", Order: 0}}
	var names []string
	for _, e := range StartOrder(list) {
		names = append(names, e.Name)
	}
	if strings.Join(names, " ") != "dns db app web" {
		t.Fatal(names)
	}
	names = nil
	for _, e := range ShutdownOrder(list) {
		names = append(names, e.Name)
	}
	if strings.Join(names, " ") != "web app db dns" {
		t.Fatal(names)
	}
	if list[0].Name != "web" {
		t.Fatal("input reordered")
	}
}

func TestAddKernelParams(t *testing.T) {
	g := "GRUB_DEFAULT=0\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\nGRUB_CMDLINE_LINUX=\"\"\n"
	out, changed := addKernelParams(g, "intel_iommu=on iommu=pt")
	if !changed || !strings.Contains(out, "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet intel_iommu=on iommu=pt\"\n") || !strings.Contains(out, "GRUB_CMDLINE_LINUX=\"\"") {
		t.Fatal(out)
	}
	if again, changed := addKernelParams(out, "intel_iommu=on iommu=pt"); changed || again != out {
		t.Fatal("not idempotent")
	}
	out, changed = addKernelParams("GRUB_DEFAULT=0\n", "amd_iommu=on")
	if !changed || !strings.HasSuffix(out, "GRUB_CMDLINE_LINUX_DEFAULT=\"amd_iommu=on\"\n") {
		t.Fatal(out)
	}
	if !hasParams("BOOT_IMAGE=/vmlinuz quiet intel_iommu=on iommu=pt", "intel_iommu=on iommu=pt") || hasParams("quiet", "iommu=pt") {
		t.Fatal("hasParams")
	}
}

func TestParseLspci(t *testing.T) {
	out := `0000:00:00.0 "Host bridge" "Intel Corporation" "440FX - 82441FX PMC [Natoma]" -r02 "Red Hat, Inc." "Qemu virtual machine"
0000:01:00.0 "VGA compatible controller" "NVIDIA Corporation" "GA106 [GeForce RTX 3060]" -ra1 "" ""
garbage`
	m := parseLspci(out)
	if len(m) != 2 || m["0000:01:00.0"] != [3]string{"VGA compatible controller", "NVIDIA Corporation", "GA106 [GeForce RTX 3060]"} {
		t.Fatal(m)
	}
}

func TestCheckGroup(t *testing.T) {
	g := []PCIDevice{{Address: "0000:00:01.0", Class: "060400", Protected: "PCI bridge"}, {Address: "0000:01:00.0", Class: "030000"}, {Address: "0000:01:00.1", Class: "040300"}}
	if err := checkGroup(g); err != nil {
		t.Fatal(err)
	}
	g[2].Protected = "network interface eth0 in use"
	if err := checkGroup(g); err == nil {
		t.Fatal("group with a device of the NAS accepted")
	}
	if _, err := groupOf([]PCIDevice{{Address: "0000:01:00.0", Group: -1}}, "0000:01:00.0"); err == nil {
		t.Fatal("no IOMMU accepted")
	}
}

func TestSnapshotXML(t *testing.T) {
	s, err := parseSnapshotXML(`<domainsnapshot><name>before-upgrade</name><description>Before apt upgrade</description><state>running</state><parent><name>base</name></parent><creationTime>1760000000</creationTime><memory snapshot='internal'/><disks><disk name='vda' snapshot='internal'/></disks><domain type='kvm'><name>web</name></domain></domainsnapshot>`)
	if err != nil || s.Name != "before-upgrade" || s.Description != "Before apt upgrade" || !s.Memory || s.Kind != "internal" || s.Parent != "base" || s.Created.Unix() != 1760000000 {
		t.Fatalf("%+v %v", s, err)
	}
	s, _ = parseSnapshotXML(`<domainsnapshot><name>s</name><state>shutoff</state><memory snapshot='no'/><disks><disk name='vda' snapshot='external'><source file='/x'/></disk></disks></domainsnapshot>`)
	if s.Memory || s.Kind != "external" {
		t.Fatalf("%+v", s)
	}
}

func TestExternalSnapshotArgs(t *testing.T) {
	vm := &VM{Name: "web", Dir: "/srv/strata/main/VMs/web", Disks: []Disk{
		{Device: "disk", Target: "vda", Source: "/srv/strata/main/VMs/web/disk0.snap-old.qcow2"},
		{Device: "cdrom", Target: "sda", Source: "/srv/strata/main/VMs/web/seed.iso"}}}
	got := strings.Join(externalSnapshotArgs(vm, "s1", "", true), " ")
	want := "snapshot-create-as web --name s1 --atomic --memspec file=/srv/strata/main/VMs/web/web.s1.mem,snapshot=external --diskspec vda,snapshot=external,file=/srv/strata/main/VMs/web/disk0.snap-s1.qcow2 --diskspec sda,snapshot=no"
	if got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
	if got := strings.Join(externalSnapshotArgs(vm, "s2", "d", false), " "); !strings.Contains(got, "--disk-only") || !strings.Contains(got, "--description d") {
		t.Fatal(got)
	}
	if validSnap("a b") == nil || validSnap("-x") == nil || validSnap("ok_1.2") != nil {
		t.Fatal("snapshot names")
	}
}

func TestCleanDescriptionAndSafeBase(t *testing.T) {
	if got := cleanDescription(" a\nb\x00 "); got != "a b" {
		t.Fatalf("%q", got)
	}
	for in, want := range map[string]bool{"disk1.vmdk": true, "../x": false, "a/b": false, "..": false, "-rf": false, "": false} {
		if safeBase(in) != want {
			t.Errorf("%q", in)
		}
	}
	if VolumeRoot("/srv/strata/main/Photos/vms") != "/srv/strata/main" {
		t.Fatal("volume root")
	}
}
