# macOS VMs (0.13), without Apple's servers: the host reports whether its
# processor has the invariant TSC macOS needs (the test VM runs with
# "-cpu host", without invtsc unless VM_CPU adds it), and the shared macOS
# images no VM uses are listed and removed by one request, the cloud images
# of the same folder left alone.
section "macOS images and invariant TSC"
check "macOS host details are for administrators" "$(u GET /api/vms/macos/host)" "administrator rights required"
WANT=$(vm "grep -m1 -q -w nonstop_tsc /proc/cpuinfo && grep -m1 -q -w constant_tsc /proc/cpuinfo && echo True || echo False")
check "invariant TSC reported as in /proc/cpuinfo" "$(c GET /api/vms/macos/host | py 'print(d["invariantTsc"])')" "$WANT"
CD=/srv/strata/main/.strata-cloud-images
vm "sudo mkdir -p $CD && sudo truncate -s 3M $CD/BaseSystem-e2eold.img && sudo truncate -s 1M $CD/BaseSystem-e2eold.dmg && sudo truncate -s 2M $CD/OpenCore-e2e00000000.qcow2 && sudo truncate -s 1M $CD/e2e-cloud.qcow2"
check "unused images listed" "$(c GET /api/vms/macos/host | py 'print(sorted((c["kind"], c.get("version",""), bool(c.get("usedBy"))) for c in d["cache"] if "e2e" in c["path"]))')" "[('opencore', '', False), ('recovery', 'e2eold', False), ('recovery', 'e2eold', False)]"
R=$(c DELETE /api/vms/macos/cache)
check "unused images removed" "$(echo "$R" | py 'print(sorted(p.rsplit("/",1)[1] for p in d["removed"] if "e2e" in p), d["freed"] >= 6*1024*1024)')" "['BaseSystem-e2eold.dmg', 'BaseSystem-e2eold.img', 'OpenCore-e2e00000000.qcow2'] True"
check "cloud image of the same folder kept" "$(vm "ls $CD")" "e2e-cloud.qcow2"
check "nothing left to list" "$(c GET /api/vms/macos/host | py 'print(len([c for c in d["cache"] if "e2e" in c["path"]]))')" "0"
vm "sudo rm -f $CD/e2e-cloud.qcow2"
