package vms

import (
	"slices"
	"strings"
	"testing"
)

const rbdDomain = `<domain type='kvm'>
  <name>web</name>
  <memory unit='KiB'>1048576</memory>
  <cpu mode='host-passthrough' check='none'><topology sockets='1' cores='2' threads='1'/></cpu>
  <devices>
    <disk type='network' device='disk'>
      <driver name='qemu' type='raw'/>
      <auth username='strata-vms'><secret type='ceph' uuid='aaaaaaaa-0000-0000-0000-000000000001'/></auth>
      <source protocol='rbd' name='rbd/web-disk0'><host name='10.0.0.1' port='6789'/></source>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/srv/strata/main/vms/web/disk1.qcow2'/><target dev='vdb' bus='virtio'/></disk>
    <disk type='file' device='cdrom'><source file='/srv/strata/nowhere/x.iso'/><target dev='sda' bus='sata'/></disk>
    <interface type='bridge'><source bridge='br0'/><mac address='52:54:00:00:00:01'/></interface>
  </devices>
</domain>`

func TestRewriteDomain(t *testing.T) {
	out, err := rewriteDomain(rbdDomain, Rewrite{
		Secrets:      map[string]string{"aaaaaaaa-0000-0000-0000-000000000001": "bbbbbbbb-0000-0000-0000-000000000002"},
		Bridges:      map[string]string{"br0": "vmbr1"},
		Paths:        map[string]string{"/srv/strata/main/vms/web/disk1.qcow2": "/srv/strata/data/vms/web/disk1.qcow2"},
		EjectMissing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bbbbbbbb-0000-0000-0000-000000000002", `bridge="vmbr1"`, "/srv/strata/data/vms/web/disk1.qcow2", "rbd/web-disk0", `<target dev="sda"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "x.iso") || strings.Contains(out, "aaaaaaaa") {
		t.Errorf("missing ISO kept or old secret left:\n%s", out)
	}
	if got := DomainRBDSecrets(rbdDomain); len(got) != 1 || got[0].Pool != "rbd" || got[0].Secret != "aaaaaaaa-0000-0000-0000-000000000001" {
		t.Errorf("secrets = %v", got)
	}
	if got := DomainRBD(rbdDomain); !slices.Equal(got, []string{"rbd/web-disk0"}) {
		t.Errorf("rbd = %v", got)
	}
	if DomainMemoryMB(rbdDomain) != 1024 {
		t.Error("memory")
	}
	if n, err := DomainName(rbdDomain); err != nil || n != "web" {
		t.Errorf("name %q %v", n, err)
	}
}

func TestSetCPU(t *testing.T) {
	out, err := setCPU(rbdDomain, "Haswell-noTSX")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `mode="custom"`) || !strings.Contains(out, ">Haswell-noTSX</model>") || !strings.Contains(out, `cores="2"`) || strings.Contains(out, "host-passthrough") {
		t.Errorf("custom model:\n%s", out)
	}
	back, err := setCPU(out, "")
	if err != nil || !strings.Contains(back, "host-passthrough") || strings.Contains(back, "Haswell") || !strings.Contains(back, `cores="2"`) {
		t.Errorf("host CPU again:\n%s", back)
	}
}

func TestUsableModels(t *testing.T) {
	doc := `<domainCapabilities><cpu><mode name='host-passthrough' supported='yes'/>
<mode name='custom' supported='yes'><model usable='yes' vendor='Intel'>Nehalem</model><model usable='no' vendor='Intel'>Skylake-Client</model><model usable='yes' vendor='AMD'>Opteron_G4</model></mode></cpu></domainCapabilities>`
	if got := parseUsableModels(doc); !slices.Equal(got, []string{"Nehalem", "Opteron_G4"}) {
		t.Errorf("usable = %v", got)
	}
}

func TestMigrationProgress(t *testing.T) {
	var got []float64
	w := &progressWriter{set: func(p float64) { got = append(got, p) }, logf: func(string, ...any) {}, last: -1}
	_, _ = w.Write([]byte("Migration: [  0 %]\rMigration: [ 42 %]\rMigration: [ 42 %]\rMigration: [100 %]\n"))
	if !slices.Equal(got, []float64{0, 42, 100}) {
		t.Errorf("progress = %v", got)
	}
}
