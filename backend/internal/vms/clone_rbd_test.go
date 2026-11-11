package vms

import (
	"strings"
	"testing"
)

func TestRewriteCloneRBD(t *testing.T) {
	doc := `<domain type='kvm'><name>web</name><uuid>u</uuid><devices>
<disk type='network' device='disk'><driver name='qemu' type='raw'/><auth username='libvirt'><secret type='ceph' uuid='x'/></auth><source protocol='rbd' name='vms/web-0'><host name='10.0.0.1' port='6789'/></source><target dev='vda' bus='virtio'/></disk>
<disk type='network' device='disk'><driver name='qemu' type='raw'/><source protocol='rbd' name='vms/other'/><target dev='vdb' bus='virtio'/></disk>
</devices></domain>`
	out, err := rewriteClone(doc, cloneOpts{Name: "web2", Disks: map[string]string{"rbd:vms/web-0": "/srv/strata/v/web2/disk0.qcow2"}, MAC: func() string { return "52:54:00:00:00:01" }})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<disk type="file" device="disk"><driver name="qemu" type="qcow2"`, `<source file="/srv/strata/v/web2/disk0.qcow2"`, `protocol="rbd" name="vms/other"`} {
		if !strings.Contains(strings.ReplaceAll(out, "'", `"`), want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "web-0") || strings.Count(out, "<auth") != 0 {
		t.Errorf("RBD source left: %s", out)
	}
}
