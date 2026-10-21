# Virtual machines: snapshots, clones and templates, import, USB, ordered
# autostart, VM backups, cloud image with cloud-init, PCI listing.
# Nested KVM in a 4 GB test VM: guests have 256-512 MB and at most two run at
# once. The Debian cloud image (~350 MB) is downloaded once.
section "Virtual machines: snapshots, clones, cloud images, devices, backups"
VMDIR=/srv/strata/main/Photos
vmjson() { c GET /api/vms | py "v=[x for x in d['vms'] if x['name']=='$1']; v=v[0] if v else {}; $2"; }
mkvm() { # name firmware
  c POST /api/vms -d "{\"name\":\"$1\",\"os\":\"other\",\"vcpus\":1,\"memoryMb\":256,\"diskGb\":1,\"storage\":\"$VMDIR\",\"network\":\"nat\",\"firmware\":\"$2\"}"
}
check "small BIOS VM created" "$(job "$(mkvm e2e-a bios)")" "success"

# Snapshots: disk + memory while running, disk only when stopped.
check "snapshot of a running VM" "$(c POST /api/vms/e2e-a/snapshots -d '{"name":"s1","description":"first"}')" '"kind":"internal"'
check "snapshot listed with memory" "$(c GET /api/vms/e2e-a/snapshots | py 'print([(s["name"], s["description"], s["memory"], s["current"]) for s in d])')" "('s1', 'first', True, True)"
check "snapshot name validated" "$(c POST /api/vms/e2e-a/snapshots -d '{"name":"../x"}')" "invalid snapshot name"
c POST /api/vms/e2e-a/destroy >/dev/null
check "revert to a running snapshot" "$(c POST /api/vms/e2e-a/snapshots/s1/revert)" '"ok":true'
check "VM running again after the revert" "$(vmjson e2e-a 'print(v.get("state"))')" "running"
c POST /api/vms/e2e-a/destroy >/dev/null
check "disk-only snapshot of a stopped VM" "$(c POST /api/vms/e2e-a/snapshots -d '{"name":"s2"}'; c GET /api/vms/e2e-a/snapshots | py 'print([(s["name"], s["memory"]) for s in d if s["name"]=="s2"])')" "('s2', False)"
check "snapshots deleted" "$(c DELETE /api/vms/e2e-a/snapshots/s2 >/dev/null; c DELETE /api/vms/e2e-a/snapshots/s1 >/dev/null; c GET /api/vms/e2e-a/snapshots)" "[]"

# UEFI: libvirt may refuse internal snapshots with the pflash NVRAM; StrataOS then takes an external one.
check "small UEFI VM created" "$(job "$(mkvm e2e-u uefi)")" "success"
UK=$(c POST /api/vms/e2e-u/snapshots -d '{"name":"u1"}')
echo "    (UEFI snapshot: $UK)"
check "UEFI snapshot taken" "$UK" '"kind":"'
check "UEFI snapshot reverted" "$(c POST /api/vms/e2e-u/snapshots/u1/revert)" '"ok":true'
check "UEFI snapshot deleted" "$(c DELETE /api/vms/e2e-u/snapshots/u1)" '"ok":true'
c POST /api/vms/e2e-u/destroy >/dev/null
check "UEFI VM deleted" "$(c DELETE '/api/vms/e2e-u?disks=1')" '"ok":true'

# Clone, template, VM from template.
check "clone refused while running" "$(c POST /api/vms/e2e-a/start >/dev/null; job "$(c POST /api/vms/e2e-a/clone -d '{"name":"e2e-b"}')")" "shut the VM down first"
c POST /api/vms/e2e-a/destroy >/dev/null
check "VM cloned" "$(job "$(c POST /api/vms/e2e-a/clone -d '{"name":"e2e-b"}')")" "success"
check "clone has its own disk" "$(vm "sudo ls $VMDIR/e2e-b")" "disk0.qcow2"
check "clone has a new MAC and UUID" "$(c GET /api/vms | py 'a=[v for v in d["vms"] if v["name"]=="e2e-a"][0]; b=[v for v in d["vms"] if v["name"]=="e2e-b"][0]; print(a["nics"][0]["mac"]!=b["nics"][0]["mac"], a["uuid"]!=b["uuid"], b["disks"][0]["source"])')" "True True $VMDIR/e2e-b/disk0.qcow2"
check "converted to a template" "$(c POST /api/vms/e2e-b/template -d '{"template":true}' >/dev/null; vmjson e2e-b 'print(v.get("template"))')" "True"
check "a template cannot start" "$(c POST /api/vms/e2e-b/start)" "template"
check "VM created from the template" "$(job "$(c POST /api/vms/e2e-b/clone -d '{"name":"e2e-c"}')")" "success"
check "the new VM is not a template" "$(vmjson e2e-c 'print(v.get("template"), v.get("state"))')" "False shut off"

# Import: a qcow2 made with qemu-img, and an OVA (OVF + VMDK).
vm "sudo qemu-img create -q -f qcow2 $VMDIR/import-test.qcow2 1G && sudo qemu-img convert -O vmdk $VMDIR/import-test.qcow2 /tmp/e2e-ova-disk1.vmdk"
vm "cat > /tmp/e2e-ova.ovf <<'OVF'
<?xml version=\"1.0\"?>
<Envelope xmlns=\"http://schemas.dmtf.org/ovf/envelope/1\" xmlns:ovf=\"http://schemas.dmtf.org/ovf/envelope/1\" xmlns:rasd=\"http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData\">
 <References><File ovf:id=\"file1\" ovf:href=\"e2e-ova-disk1.vmdk\"/></References>
 <DiskSection><Disk ovf:diskId=\"vmdisk1\" ovf:fileRef=\"file1\"/></DiskSection>
 <VirtualSystem ovf:id=\"appliance\"><Name>OVA Appliance</Name>
  <OperatingSystemSection ovf:id=\"96\"><Description>Debian Linux</Description></OperatingSystemSection>
  <VirtualHardwareSection>
   <Item><rasd:ResourceType>3</rasd:ResourceType><rasd:VirtualQuantity>1</rasd:VirtualQuantity></Item>
   <Item><rasd:AllocationUnits>byte * 2^20</rasd:AllocationUnits><rasd:ResourceType>4</rasd:ResourceType><rasd:VirtualQuantity>256</rasd:VirtualQuantity></Item>
   <Item><rasd:HostResource>ovf:/disk/vmdisk1</rasd:HostResource><rasd:ResourceType>17</rasd:ResourceType></Item>
  </VirtualHardwareSection>
 </VirtualSystem>
</Envelope>
OVF
cd /tmp && sudo tar -cf $VMDIR/e2e-appliance.ova e2e-ova.ovf e2e-ova-disk1.vmdk"
check "importable files listed" "$(c GET /api/vms/importable)" "import-test.qcow2"
check "qcow2 inspected" "$(c POST /api/vms/import/inspect -d "{\"source\":\"$VMDIR/import-test.qcow2\"}")" '"format":"qcow2"'
check "OVF read from the OVA" "$(c POST /api/vms/import/inspect -d "{\"source\":\"$VMDIR/e2e-appliance.ova\"}" | py 'o=d["ovf"]; print(o["name"], o["vcpus"], o["memoryMb"], o["disks"])')" "OVA Appliance 1 256 ['e2e-ova-disk1.vmdk']"
check "import confined to the volumes" "$(c POST /api/vms/import/inspect -d '{"source":"/etc/shadow"}')" "inside a volume"
check "qcow2 imported" "$(job "$(c POST /api/vms/import -d "{\"name\":\"e2e-imp\",\"source\":\"$VMDIR/import-test.qcow2\",\"os\":\"other\",\"vcpus\":1,\"memoryMb\":256,\"storage\":\"$VMDIR\",\"network\":\"nat\",\"firmware\":\"bios\"}")")" "success"
check "imported disk converted into the VM folder" "$(vm "sudo qemu-img info $VMDIR/e2e-imp/disk0.qcow2 | grep -E '^file format'")" "qcow2"
c POST /api/vms/e2e-imp/destroy >/dev/null; c DELETE '/api/vms/e2e-imp?disks=1' >/dev/null
check "OVA imported with its settings" "$(job "$(c POST /api/vms/import -d "{\"name\":\"e2e-ova\",\"source\":\"$VMDIR/e2e-appliance.ova\",\"storage\":\"$VMDIR\",\"network\":\"nat\"}")"; vmjson e2e-ova 'print(v.get("vcpus"), v.get("memoryMb"))')" "success"
check "OVA VM sized from the OVF" "$(vmjson e2e-ova 'print(v.get("vcpus"), v.get("memoryMb"))')" "1 256"
c POST /api/vms/e2e-ova/destroy >/dev/null; c DELETE '/api/vms/e2e-ova?disks=1' >/dev/null
vm "sudo rm -f $VMDIR/import-test.qcow2 $VMDIR/e2e-appliance.ova /tmp/e2e-ova.ovf /tmp/e2e-ova-disk1.vmdk"

# USB devices of the test VM (QEMU may expose none: then only the listing is checked).
USBL=$(c GET /api/vms/usb)
check "USB devices listed" "$USBL" "["
USBDEV=$(echo "$USBL" | py 'u=[x for x in d if not x.get("internal") and not x.get("usedBy")]; print(u[0]["vendor"]+" "+u[0]["product"] if u else "")' 2>/dev/null)
if [ -n "$USBDEV" ]; then
  read -r UV UP <<<"$USBDEV"
  check "USB device given to a VM" "$(c POST /api/vms/e2e-a/usb -d "{\"vendor\":\"$UV\",\"product\":\"$UP\"}" >/dev/null; vmjson e2e-a 'print([u["vendor"]+":"+u["product"] for u in v["usb"]])')" "$UV:$UP"
  check "USB hostdev in the definition" "$(vm "sudo virsh -c qemu:///system dumpxml --inactive e2e-a | grep -c \"vendor id='0x$UV'\"")" "1"
  check "USB device taken back" "$(c DELETE /api/vms/e2e-a/usb/$UV/$UP >/dev/null; vmjson e2e-a 'print(v["usb"])')" "[]"
else
  echo "    (no USB device to pass through in this VM: attach/detach not tested)"
fi
check "invalid USB IDs refused" "$(c POST /api/vms/e2e-a/usb -d '{"vendor":"zz","product":"1"}')" "invalid USB device"

# Ordered autostart, applied by StrataOS at (daemon) start once per boot.
check "start order saved" "$(c PUT /api/vms/autostart -d '[{"name":"e2e-c","order":2,"delay":0},{"name":"e2e-a","order":1,"delay":5}]')" '"ok":true'
check "start order listed" "$(c GET /api/vms/autostart | py 'print([(e["name"], e["order"], e["delay"]) for e in d])')" "[('e2e-a', 1, 5), ('e2e-c', 2, 0)]"
check "libvirt autostart off (one owner)" "$(vm "sudo virsh -c qemu:///system dominfo e2e-a | grep Autostart")" "disable"
check "templates refused in the start order" "$(c PUT /api/vms/autostart -d '[{"name":"e2e-b","order":1,"delay":0}]')" "template"
vm "sudo rm -f /run/strata/vm-autostart.done && sudo systemctl restart strata"
wait_api
c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
for _ in $(seq 1 30); do [ "$(vmjson e2e-c 'print(v.get("state"))')" = running ] && break; sleep 2; done
check "autostart VMs started after the restart" "$(vmjson e2e-a 'print(v.get("state"))') $(vmjson e2e-c 'print(v.get("state"))')" "running running"
check "started in order" "$(c GET /api/system/events | py 'print(" ".join(reversed([e["message"].split()[1] for e in d if e["message"].endswith("started with the NAS") and e["message"].split()[1] in ("e2e-a","e2e-c")][:2])))')" "e2e-a e2e-c"
check "ordered shutdown unit installed" "$(vm "systemctl is-active strata-vms.service; systemctl is-enabled strata-vms.service" | tr '\n' ' ')" "active enabled"
c POST /api/vms/e2e-c/destroy >/dev/null
check "start order cleared" "$(c PUT /api/vms/autostart -d '[]'; vmjson e2e-a 'print(v.get("autostart"))')" "False"

# VM backup to a folder (live snapshot of the running e2e-a, no guest agent:
# crash-consistent), then restore as a new VM.
BK=$(c POST /api/backups -d '{"name":"e2e-vms","kind":"rsync","sources":[],"vms":["e2e-a"],"destType":"local","destPath":"/srv/strata/fast/vmbackup","cron":"","enabled":true}' | py 'print(d["id"])')
check "VM backup task saved" "$(c GET /api/backups | py "print([b['vms'] for b in d if b['id']=='$BK'])")" "['e2e-a']"
check "VM backup ran" "$(job "$(c POST /api/backups/$BK/run)")" "success"
check "definition and disk backed up" "$(vm "sudo ls /srv/strata/fast/vmbackup/vms/e2e-a" | tr '\n' ' ')" "domain.xml manifest.json vda.qcow2"
check "VM still running on its own disk" "$(vmjson e2e-a 'print(v.get("state"), v["disks"][0]["source"])')" "running $VMDIR/e2e-a/disk0.qcow2"
check "no snapshot overlay left" "$(vm "sudo ls -a $VMDIR/e2e-a | grep -c strata-backup")" "0"
check "VM backup listed" "$(c GET /api/vms/backups | py 'print([(b["vm"], b["consistency"]) for b in d])')" "('e2e-a', '"
c POST /api/vms/e2e-a/destroy >/dev/null
check "VM restored from the backup" "$(job "$(c POST /api/vms/backups/restore -d "{\"task\":\"$BK\",\"vm\":\"e2e-a\",\"name\":\"e2e-r\",\"storage\":\"$VMDIR\"}")")" "success"
check "restored VM defined with its own disk" "$(vmjson e2e-r 'print(v.get("state"), v["disks"][0]["source"])')" "shut off $VMDIR/e2e-r/disk0.qcow2"
check "restored VM has a new MAC" "$(c GET /api/vms | py 'a=[v for v in d["vms"] if v["name"]=="e2e-a"][0]; r=[v for v in d["vms"] if v["name"]=="e2e-r"][0]; print(a["nics"][0]["mac"]!=r["nics"][0]["mac"])')" "True"
c DELETE /api/backups/$BK >/dev/null
vm "sudo rm -rf /srv/strata/fast/vmbackup"

# PCI passthrough: listing only (the test VM has no IOMMU; never touch GRUB here).
check "PCI devices and IOMMU status" "$(c GET /api/vms/pci | py 'print(type(d["iommu"]["enabled"]).__name__, len(d["devices"]) > 0, all(x["protected"] for x in d["devices"] if x["class"].startswith("06")))')" "bool True True"
check "PCI passthrough refused without IOMMU" "$(c POST /api/vms/e2e-r/pci -d '{"address":"0000:00:01.0"}')" "error"

for n in e2e-a e2e-b e2e-c e2e-r; do c POST /api/vms/$n/destroy >/dev/null; c DELETE "/api/vms/$n?disks=1" >/dev/null; done

# Cloud image: Debian 13 with cloud-init, reachable over SSH.
vm "sudo rm -f /root/.ssh/e2e_vm && sudo ssh-keygen -q -t ed25519 -N '' -f /root/.ssh/e2e_vm"
PUB=$(vm "sudo cat /root/.ssh/e2e_vm.pub")
check "cloud-init settings validated" "$(c POST /api/vms/cloud -d "{\"name\":\"e2e-cloud\",\"image\":\"debian13\",\"vcpus\":1,\"memoryMb\":512,\"diskGb\":4,\"storage\":\"$VMDIR\",\"network\":\"nat\",\"firmware\":\"bios\",\"cloudInit\":{\"user\":\"root\",\"password\":\"x\"}}")" "invalid user name"
check "VM created from the Debian cloud image" "$(job "$(c POST /api/vms/cloud -d "{\"name\":\"e2e-cloud\",\"image\":\"debian13\",\"vcpus\":1,\"memoryMb\":512,\"diskGb\":4,\"storage\":\"$VMDIR\",\"network\":\"nat\",\"firmware\":\"bios\",\"cloudInit\":{\"user\":\"e2e\",\"password\":\"E2e-Cloud-1\",\"sshKeys\":[\"$PUB\"],\"packages\":[\"curl\"]}}")")" "success"
check "cloud image cached and verified" "$(c GET /api/vms/cloud-images | py 'print([i["cached"] for i in d if i["id"]=="debian13"])'; vm "sudo cat /srv/strata/main/.strata-cloud-images/debian-13-genericcloud-amd64.qcow2.verified | wc -c")" "[True]"
check "seed ISO holds the user and the key" "$(vm "sudo xorriso -osirrox on -indev $VMDIR/e2e-cloud/seed.iso -extract /user-data /tmp/e2e-ud >/dev/null 2>&1; sudo cat /tmp/e2e-ud; sudo rm -f /tmp/e2e-ud")" "\"name\": \"e2e\""
check "password stored hashed" "$(vm "sudo grep -c 'passwd: \"\$6\$' $VMDIR/e2e-cloud/cloud-init/user-data")" "1"
check "cloud VM disk grown" "$(vm "sudo qemu-img info $VMDIR/e2e-cloud/disk0.qcow2 | grep 'virtual size'")" "4 GiB"
READY=""
for _ in $(seq 1 60); do
  IP=$(vmjson e2e-cloud 'print(([n.get("ip") for n in v.get("nics", []) if n.get("ip")] + [""])[0])')
  if [ -n "$IP" ] && vm "sudo ssh -i /root/.ssh/e2e_vm -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o BatchMode=yes e2e@$IP id" | grep -q "uid="; then READY="ssh-ok"; break; fi
  sleep 5
done
LOG=$(c GET /api/vms/e2e-cloud/console-log | py 'print(d["log"][-4000:])')
[ -z "$READY" ] && echo "    (no SSH within 5 min; console log tail:)" && echo "$LOG" | tail -5
check "cloud VM ready (SSH as the new user, or cloud-init done on the console)" "$READY$(echo "$LOG" | grep -o 'strata-cloud-init-done' | head -1)" "s"
check "cloud-init created the user (console log)" "$LOG" "e2e"
c POST /api/vms/e2e-cloud/destroy >/dev/null
check "cloud VM deleted" "$(c DELETE '/api/vms/e2e-cloud?disks=1')" '"ok":true'
vm "sudo rm -rf /srv/strata/main/.strata-cloud-images /root/.ssh/e2e_vm /root/.ssh/e2e_vm.pub"
check "all test VMs removed" "$(c GET /api/vms | py 'print([v["name"] for v in d["vms"] if v["name"].startswith("e2e-")])')" "[]"
