# Virtual machines (0.12.1): linked clones from templates only, templates
# frozen while they have clones, rename of a VM folder (its own only),
# follow-up of the name in the start order and the backup tasks.
section "Virtual machines: linked clones, templates, rename"
VMDIR=/srv/strata/main/Photos
vmjson() { c GET /api/vms | py "v=[x for x in d['vms'] if x['name']=='$1']; v=v[0] if v else {}; $2"; }
check "source VM created" "$(job "$(c POST /api/vms -d "{\"name\":\"e2e-src\",\"os\":\"other\",\"vcpus\":1,\"memoryMb\":256,\"diskGb\":1,\"storage\":\"$VMDIR\",\"network\":\"nat\",\"firmware\":\"bios\"}")")" "success"
c POST /api/vms/e2e-src/destroy >/dev/null
check "linked clone of an ordinary VM refused" "$(job "$(c POST /api/vms/e2e-src/clone -d '{"name":"e2e-lc","linked":true}')")" "templates only"
check "converted to a template" "$(c POST /api/vms/e2e-src/template -d '{"template":true}')" '"ok":true'
check "linked clone of the template" "$(job "$(c POST /api/vms/e2e-src/clone -d '{"name":"e2e-lc","linked":true}')")" "success"
check "the clone reads the template's disk" "$(vm "sudo qemu-img info -U $VMDIR/e2e-lc/disk0.qcow2 | grep -E '^backing file:'")" "$VMDIR/e2e-src/disk0.qcow2"
check "template: snapshot refused" "$(c POST /api/vms/e2e-src/snapshots -d '{"name":"s1"}')" "template"
check "template: new disk refused" "$(c POST /api/vms/e2e-src/disks -d '{"sizeGb":1}')" "template"
check "template: back to a VM refused while a clone reads it" "$(c POST /api/vms/e2e-src/template -d '{"template":false}')" "linked clones of this template"
check "template: folder move refused while a clone reads it" "$(c POST /api/vms/e2e-src/rename -d '{"name":"e2e-src2"}')" "linked clone"
check "template disk unchanged" "$(vm "sudo qemu-img info -U $VMDIR/e2e-src/disk0.qcow2 | grep -c 'backing file'")" "0"

# Rename of the clone: its own folder moves, the definition follows, it starts.
BK=$(c POST /api/backups -d '{"name":"e2e-vmbk","kind":"rsync","sources":[],"vms":["e2e-lc"],"destType":"local","destPath":"/srv/strata/fast/e2e-vmbk","cron":"","enabled":true}' | py 'print(d.get("id",""))')
c PUT /api/vms/autostart -d '[{"name":"e2e-lc","order":1,"delay":0}]' >/dev/null
check "clone renamed" "$(c POST /api/vms/e2e-lc/rename -d '{"name":"e2e-lc2"}')" '"ok":true'
check "its folder moved with it" "$(vm "sudo ls $VMDIR/e2e-lc2; sudo test -e $VMDIR/e2e-lc && echo old-left")" "disk0.qcow2"
check "its disk path rewritten" "$(vmjson e2e-lc2 'print(v["disks"][0]["source"])')" "$VMDIR/e2e-lc2/disk0.qcow2"
check "start order follows the name" "$(c GET /api/vms/autostart | py 'print([e["name"] for e in d])')" "['e2e-lc2']"
check "backup task follows the name" "$(c GET /api/backups | py "print([b.get('vms') for b in d if b.get('id')=='$BK'])")" "[['e2e-lc2']]"
check "renamed clone starts" "$(c POST /api/vms/e2e-lc2/start >/dev/null; sleep 3; vmjson e2e-lc2 'print(v.get("state"))')" "running"
c POST /api/vms/e2e-lc2/destroy >/dev/null
c PUT /api/vms/autostart -d '[]' >/dev/null
c DELETE "/api/backups/$BK" >/dev/null

# A VM whose disk sits in a shared folder it does not own (an imported disk):
# only the definition is renamed, the shared folder stays where it is.
vm "sudo mkdir -p $VMDIR/shared-disks && sudo qemu-img create -q -f qcow2 $VMDIR/shared-disks/e2e-sh.qcow2 64M && sudo chmod 755 $VMDIR/shared-disks && echo keep | sudo tee $VMDIR/shared-disks/notes.txt >/dev/null"
vm "cat > /tmp/e2e-sh.xml <<'XML'
<domain type='qemu'><name>e2e-sh</name><memory unit='MiB'>128</memory><vcpu>1</vcpu><os><type arch='x86_64'>hvm</type></os>
<devices><disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='$VMDIR/shared-disks/e2e-sh.qcow2'/><target dev='vda' bus='virtio'/></disk></devices></domain>
XML
sudo virsh --connect qemu:///system define /tmp/e2e-sh.xml >/dev/null; rm -f /tmp/e2e-sh.xml"
check "VM with a disk in a shared folder renamed" "$(c POST /api/vms/e2e-sh/rename -d '{"name":"shared-disks"}')" '"ok":true'
check "the shared folder was not moved" "$(vm "sudo cat $VMDIR/shared-disks/notes.txt; sudo test -e $VMDIR/shared-disks/e2e-sh.qcow2 && echo disk-there")" "keep
disk-there"
check "its disk path unchanged" "$(vmjson shared-disks 'print(v["disks"][0]["source"])')" "$VMDIR/shared-disks/e2e-sh.qcow2"
c DELETE '/api/vms/shared-disks' >/dev/null
vm "sudo rm -rf $VMDIR/shared-disks"

# Clean up: the clone first (the template refuses to go while it is read).
check "template deletion refused while its clone exists" "$(c DELETE '/api/vms/e2e-src?disks=1')" "linked clone"
c DELETE '/api/vms/e2e-lc2?disks=1' >/dev/null
check "template back to a VM once alone" "$(c POST /api/vms/e2e-src/template -d '{"template":false}')" '"ok":true'
c DELETE '/api/vms/e2e-src?disks=1' >/dev/null
check "VMs cleaned up" "$(vm "sudo ls $VMDIR | grep -c '^e2e-'")" "0"
