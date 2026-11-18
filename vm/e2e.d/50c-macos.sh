# shellcheck shell=bash
# macOS virtual machines (0.12.1), against Apple's real recovery servers: only
# with E2E_MACOS=1 (downloads the OpenCore boot disk and about 1 GB from
# Apple). Version: E2E_MACOS_VERSION (tahoe).
# The test VM needs an invariant TSC for macOS to boot, and room for two 4 GB
# guests to run at once:
#   VM_MEM=9216 VM_CPU=host,+invtsc ./vm/vm.sh start
# With less memory, only one macOS VM is booted; without invtsc, the creation
# must warn about it and the boot is not checked.
if [ "${E2E_MACOS:-}" != 1 ]; then
  section "macOS virtual machines (skipped: E2E_MACOS=1 downloads from Apple)"
else
section "macOS virtual machines: Apple recovery, OpenCore, shared cache, overlays"
MV=${E2E_MACOS_VERSION:-tahoe}
VMDIR=/srv/strata/main/Photos
MCACHE=/srv/strata/main/.strata-cloud-images
OC=OpenCore-6ed36c0c2a42.qcow2
OC_SHA=6ed36c0c2a4206ccc695f6b1a734a1cc6f94d288b0517c705d351c63cb92a6f3
mkmac() { # name [memoryMb] [diskGb]
  c POST /api/vms/macos -d "{\"name\":\"$1\",\"vcpus\":2,\"memoryMb\":${2:-4096},\"diskGb\":${3:-32},\"storage\":\"$VMDIR\",\"network\":\"nat\",\"version\":\"$MV\",\"ignoreMsrs\":true}"
}
joblog() { # waits for a job and prints its status, then its log
  local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null)
  [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 600); do
    [ "$(c GET "/api/jobs/$id" | py 'print(d["status"])')" != running ] && break; sleep 2
  done
  c GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log",""))'
}
qrss() { vm "ps -o rss= -p \$(pgrep -f 'guest=$1,' | head -1) 2>/dev/null" | tr -d ' '; }
# boot_recovery <vm>: picks "macOS Base System" in the OpenCore picker, then
# waits for macOS to be loaded (QEMU holding more than 1 GB, no exception on
# the serial console, where OpenCore and the firmware log).
boot_recovery() {
  for _ in $(seq 1 60); do vm "sudo grep -q 'BdsDxe: starting' /var/log/libvirt/qemu/$1-console.log" && break; sleep 2; done
  sleep 15 # the picker draws its entries
  vm "sudo virsh send-key $1 KEY_RIGHT >/dev/null; sleep 1; sudo virsh send-key $1 KEY_ENTER >/dev/null"
}
booted() { # vm -> "booted" once macOS runs, "exception" if the firmware or boot.efi crashed
  local r
  for _ in $(seq 1 60); do
    vm "sudo grep -q 'Exception Type' /var/log/libvirt/qemu/$1-console.log" && { echo exception; return; }
    r=$(qrss "$1"); [ "${r:-0}" -gt 1000000 ] && { echo booted; return; }
    sleep 5
  done
  echo "not booted (QEMU RSS ${r:-none} KB)"
}

check "macOS versions listed" "$(c GET /api/vms/macos | py 'print([v["id"] for v in d])')" "'$MV'"
check "unknown version refused" "$(job "$(c POST /api/vms/macos -d "{\"name\":\"e2e-macx\",\"vcpus\":2,\"memoryMb\":4096,\"diskGb\":32,\"storage\":\"$VMDIR\",\"network\":\"nat\",\"version\":\"lion\"}")")" "unknown macOS version"
check "less than 4 GB of memory refused" "$(job "$(mkmac e2e-macx 2048)")" "at least 4096 MiB"
check "disk under 32 GB refused" "$(job "$(mkmac e2e-macx 4096 16)")" "at least a 32 GiB disk"

# A fresh download: Apple's recovery image (signed chunklist) and OpenCore (SHA-256).
vm "sudo rm -f $MCACHE/BaseSystem-$MV.* $MCACHE/$OC"
INVTSC=$(vm "grep -m1 '^flags' /proc/cpuinfo | grep -c 'constant_tsc.*nonstop_tsc'")
MEMKB=$(vm "awk '/MemTotal/ {print \$2}' /proc/meminfo")
L1=$(joblog "$(mkmac e2e-mac1)")
check "first macOS VM created" "$L1" "success"
check "recovery image fetched from Apple" "$L1" "Downloading BaseSystem.dmg"
check "recovery image verified against Apple's signed chunklist" "$L1" "Verifying against Apple's signed chunklist"
check "OpenCore boot disk downloaded" "$L1" "Downloading the OpenCore boot disk"
check "OpenCore matches its pinned SHA-256" "$(vm "sudo sha256sum $MCACHE/$OC")" "$OC_SHA"
check "cache: converted image only (no .dmg, chunklist or .part left)" "$(vm "sudo ls $MCACHE | grep -E '^BaseSystem-$MV' | tr '\n' ' '")" "BaseSystem-$MV.img "
check "KVM ignores unknown MSRs (asked)" "$(vm "cat /sys/module/kvm/parameters/ignore_msrs; cat /etc/modprobe.d/strataos-macos.conf")" "options kvm ignore_msrs=1"
check "per-VM overlays of the shared images" "$(vm "sudo qemu-img info -U $VMDIR/e2e-mac1/basesystem.qcow2 | grep '^backing file:'; sudo qemu-img info -U $VMDIR/e2e-mac1/opencore.qcow2 | grep '^backing file:'")" "$MCACHE/$OC"
check "overlay of the recovery image" "$(vm "sudo qemu-img info -U $VMDIR/e2e-mac1/basesystem.qcow2")" "backing file: $MCACHE/BaseSystem-$MV.img"
check "target disk 32 GB, thin" "$(vm "sudo qemu-img info -U $VMDIR/e2e-mac1/disk0.qcow2 | grep '^virtual size'; sudo du -k $VMDIR/e2e-mac1/disk0.qcow2 | awk '{print (\$1 < 10240) ? \"thin\" : \"full\"}'")" "thin"
MX=$(vm "sudo virsh dumpxml e2e-mac1")
check "domain: Apple SMC, CPU, USB keyboard on xHCI" "$(echo "$MX" | grep -c -e isa-applesmc -e 'vendor=GenuineIntel' -e "model='qemu-xhci'" -e "type='keyboard' bus='usb'")" "4"
check "first macOS VM running" "$(c GET /api/vms | py 'print([v["state"] for v in d["vms"] if v["name"]=="e2e-mac1"])')" "['running']"
if [ "$INVTSC" = 1 ]; then
  boot_recovery e2e-mac1
  check "macOS starts (OpenCore picker, boot.efi, then the macOS kernel)" "$(booted e2e-mac1)" "booted"
else
  echo "    (no invariant TSC in the test VM: boot not checked, start it with VM_CPU=host,+invtsc)"
  check "creation warns about the missing invariant TSC" "$L1" "does not report an invariant TSC"
fi

# A second VM shares the cache (no download) and runs alongside the first one.
L2=$(joblog "$(mkmac e2e-mac2)")
check "second macOS VM created" "$L2" "success"
check "second VM reuses the cache (nothing downloaded)" "$(echo "$L2" | grep -c Downloading)" "0"
check "its own overlays" "$(vm "sudo qemu-img info -U $VMDIR/e2e-mac2/basesystem.qcow2 | grep '^backing file:'")" "$MCACHE/BaseSystem-$MV.img"
if [ "$INVTSC" = 1 ] && [ "${MEMKB:-0}" -gt 8500000 ]; then
  boot_recovery e2e-mac2
  check "second macOS starts" "$(booted e2e-mac2)" "booted"
  sleep 60
  check "both macOS VMs still running together" "$(c GET /api/vms | py 'print(sorted(v["state"] for v in d["vms"] if v["name"] in ("e2e-mac1","e2e-mac2")))')" "['running', 'running']"
  check "no crash on the consoles" "$(vm "sudo grep -c 'Exception Type' /var/log/libvirt/qemu/e2e-mac1-console.log /var/log/libvirt/qemu/e2e-mac2-console.log | tr '\n' ' '")" "e2e-mac1-console.log:0 /var/log/libvirt/qemu/e2e-mac2-console.log:0"
  vm "sudo virsh screenshot e2e-mac1 /tmp/e2e-mac1.png >/dev/null; sudo virsh screenshot e2e-mac2 /tmp/e2e-mac2.png >/dev/null"
  echo "    (screens, macOS Recovery expected: /tmp/e2e-mac1.png and /tmp/e2e-mac2.png in the test VM)"
else
  echo "    (two 4 GB macOS guests need VM_MEM=9216 and invtsc: second boot not checked)"
  c POST /api/vms/e2e-mac2/destroy >/dev/null
fi

# Deleting a VM removes its own files only: the other VM keeps running on the shared images.
SUM=$(vm "sudo stat -c '%n %s %Y' $MCACHE/$OC $MCACHE/BaseSystem-$MV.img")
check "first macOS VM deleted" "$(c DELETE '/api/vms/e2e-mac1?disks=1')" '"ok":true'
check "its folder, NVRAM and console log removed" "$(vm "sudo ls -d $VMDIR/e2e-mac1 /var/lib/libvirt/qemu/nvram/e2e-mac1_VARS.fd /var/log/libvirt/qemu/e2e-mac1-console.log 2>&1 | grep -c 'No such file'")" "3"
[ "$INVTSC" = 1 ] && [ "${MEMKB:-0}" -gt 8500000 ] && sleep 30 &&
  check "the second VM still runs" "$(c GET /api/vms | py 'print([v["state"] for v in d["vms"] if v["name"]=="e2e-mac2"])' | tr '\n' ' '; qrss e2e-mac2 | awk '{print ($1 > 1000000) ? "alive" : "gone"}')" "['running'] alive"
check "second macOS VM deleted" "$(c DELETE '/api/vms/e2e-mac2?disks=1')" '"ok":true'
check "its folder removed" "$(vm "sudo ls -d $VMDIR/e2e-mac2 2>&1")" "No such file"
check "shared cache intact (same files, sizes, dates)" "$(vm "sudo stat -c '%n %s %Y' $MCACHE/$OC $MCACHE/BaseSystem-$MV.img")" "$SUM"
vm "sudo rm -f $MCACHE/BaseSystem-$MV.img $MCACHE/$OC; sudo rmdir $MCACHE 2>/dev/null"
check "all macOS test VMs removed" "$(c GET /api/vms | py 'print([v["name"] for v in d["vms"] if v["name"].startswith("e2e-mac")])')" "[]"
fi
