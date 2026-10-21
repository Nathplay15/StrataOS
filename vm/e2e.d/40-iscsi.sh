# iSCSI targets (LIO): a fileio LUN on volume main and a zvol LUN on tank,
# reached by the VM's own initiator (open-iscsi) through 127.0.0.1.
section "iSCSI targets"
check "iSCSI component installed" "$(job "$(c POST /api/system/components/iscsi/install)")" "success"
check "iSCSI reported installed" "$(c GET /api/iscsi | py 'print(d["installed"], d["supported"])')" "True True"
check "iSCSI service enabled" "$(c PUT /api/iscsi/settings -d '{"enabled":true}')" '"ok":true'
vm "command -v iscsiadm >/dev/null || sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq open-iscsi >/dev/null 2>&1; sudo systemctl start iscsid"
INI=$(vm "sudo sed -n 's/^InitiatorName=//p' /etc/iscsi/initiatorname.iscsi" | tr -d '\r')
check "initiator name of the VM" "$INI" "iqn."
open3260() { vm "timeout 3 bash -c '</dev/tcp/127.0.0.1/3260' 2>/dev/null && echo open || echo closed"; }

# Validation and safety.
check "invalid initiator IQN refused" "$(c POST /api/iscsi/targets -d '{"name":"bad","initiators":["windows-pc"],"luns":[{"kind":"fileio","path":"/srv/strata/main/iscsi/bad.img","size":67108864}]}')" "invalid iSCSI name"
check "image outside volumes refused" "$(c POST /api/iscsi/targets -d '{"name":"bad","luns":[{"kind":"fileio","path":"/etc/bad.img","size":67108864}]}')" "invalid image file"
check "image inside a share refused" "$(c POST /api/iscsi/targets -d '{"name":"bad","luns":[{"kind":"fileio","path":"/srv/strata/main/Photos/bad.img","size":67108864}]}')" "shared folder"
vm "sudo zfs create -V 64M tank/e2e-busy && sleep 2 && sudo mkfs.ext4 -q /dev/zvol/tank/e2e-busy && sudo mkdir -p /mnt/e2e-busy && sudo mount /dev/zvol/tank/e2e-busy /mnt/e2e-busy"
check "mounted block device refused" "$(c POST /api/iscsi/targets -d '{"name":"busy","luns":[{"kind":"zvol","path":"tank/e2e-busy"}]}')" "is mounted"
vm "sudo umount /mnt/e2e-busy; sudo rmdir /mnt/e2e-busy; sudo zfs destroy tank/e2e-busy"
check "refused targets not kept" "$(c GET /api/iscsi | py 'print(len(d["targets"]))')" "0"

# Target with two LUNs, open to the VM's initiator only.
T=$(c POST /api/iscsi/targets -d '{"name":"e2e","initiators":["'"$INI"'"],"luns":[{"kind":"fileio","path":"/srv/strata/main/iscsi/e2e.img","size":67108864},{"kind":"zvol","path":"tank/iscsi-e2e","size":67108864,"create":true,"sparse":true}]}')
check "target created" "$T" '"name":"e2e"'
TID=$(echo "$T" | py 'print(d["id"])')
IQN=$(echo "$T" | py 'print(d["iqn"])')
check "IQN generated from the host name" "$IQN" "iqn.2026-01.local.strata-vm:e2e"
check "single-label IQN refused (rtslib needs a second dot)" "$(c POST /api/iscsi/targets -d '{"name":"bad","iqn":"iqn.2026-01.nas:bad","luns":[{"kind":"fileio","path":"/srv/strata/main/iscsi/bad.img","size":67108864}]}')" "invalid iSCSI name"
st() { c GET /api/iscsi | py "print([(t['live'], len(t['luns']), [s['initiator'] for s in t['sessions']]) for t in d['targets'] if t['name']=='e2e'])"; }
check "target running with 2 LUNs" "$(st)" "(True, 2,"
check "sparse image file" "$(vm "[ \$(sudo stat -c %b /srv/strata/main/iscsi/e2e.img) -lt 2048 ] && sudo stat -c %s /srv/strata/main/iscsi/e2e.img")" "67108864"
check "port 3260 open" "$(open3260)" "open"
check "discovery" "$(vm "sudo iscsiadm -m discovery -t sendtargets -p 127.0.0.1 2>&1")" "$IQN"
login() { vm "sudo iscsiadm -m node -T $IQN -p 127.0.0.1:3260 --login 2>&1; echo rc=\$?"; }
logout() { vm "sudo iscsiadm -m node -T $IQN -p 127.0.0.1:3260 --logout 2>&1"; }
check "login" "$(login)" "successful"
vm "sudo udevadm settle; sleep 2"
D0=/dev/disk/by-path/ip-127.0.0.1:3260-iscsi-$IQN-lun-0
D1=/dev/disk/by-path/ip-127.0.0.1:3260-iscsi-$IQN-lun-1
check "two new block devices" "$(vm "ls $D0 $D1 2>/dev/null | wc -l")" "2"
check "session listed with the initiator" "$(st)" "$INI"
vm "head -c 1048576 /dev/urandom > /tmp/e2e-pat; for d in $D0 $D1; do sudo dd if=/tmp/e2e-pat of=\$d bs=1M count=1 oflag=direct 2>/dev/null; done; sync"
check "pattern read back (fileio LUN)" "$(vm "sudo dd if=$D0 bs=1M count=1 iflag=direct 2>/dev/null | cmp - /tmp/e2e-pat && echo same")" "same"
check "pattern read back (zvol LUN)" "$(vm "sudo dd if=$D1 bs=1M count=1 iflag=direct 2>/dev/null | cmp - /tmp/e2e-pat && echo same")" "same"
check "pattern in the image file" "$(vm "sudo head -c 1048576 /srv/strata/main/iscsi/e2e.img | cmp - /tmp/e2e-pat && echo same")" "same"
check "pattern on the zvol" "$(vm "sudo dd if=/dev/zvol/tank/iscsi-e2e bs=1M count=1 iflag=direct 2>/dev/null | cmp - /tmp/e2e-pat && echo same")" "same"
check "zvol LUN snapshot" "$(c POST "/api/iscsi/targets/$TID/luns/1/snapshot")" "tank/iscsi-e2e@iscsi-"
check "fileio LUN snapshot refused" "$(c POST "/api/iscsi/targets/$TID/luns/0/snapshot")" "snapshots need"
check "exported zvol cannot be destroyed" "$(c DELETE '/api/storage/zfs-datasets?name=tank/iscsi-e2e')" "exported over iSCSI"
check "LUN removal refused while connected" "$(c DELETE "/api/iscsi/targets/$TID/luns/1?backing=1")" "initiators are connected"
check "logout" "$(logout)" "successful"
sleep 1
check "no session after logout" "$(c GET /api/iscsi | py 'print(sum(len(t["sessions"]) for t in d["targets"]))')" "0"

# ACL: another initiator only → this one is refused.
check "ACL changed" "$(c PUT "/api/iscsi/targets/$TID" -d '{"iqn":"'"$IQN"'","initiators":["iqn.2026-01.test:other"]}')" '"ok":true'
check "initiator outside the ACL refused" "$(login)" "rc=2"
# CHAP.
check "CHAP enabled" "$(c PUT "/api/iscsi/targets/$TID" -d '{"iqn":"'"$IQN"'","initiators":["'"$INI"'"],"chap":{"user":"e2e","secret":"E2e-Chap-Secret1"}}')" '"ok":true'
check "CHAP secret masked" "$(c GET /api/iscsi | py 'print([t["chap"] for t in d["targets"]])')" "'secret': '********'"
check "masked secret kept on save" "$(c PUT "/api/iscsi/targets/$TID" -d '{"iqn":"'"$IQN"'","initiators":["'"$INI"'"],"chap":{"user":"e2e","secret":"********"}}')" '"ok":true'
nodeauth() { vm "for kv in authmethod=CHAP username=e2e password=$1; do sudo iscsiadm -m node -T $IQN -p 127.0.0.1:3260 -o update -n node.session.auth.\${kv%%=*} -v \${kv#*=}; done"; }
nodeauth wrong-secret-123
check "wrong CHAP secret refused" "$(login)" "authorization failure"
nodeauth E2e-Chap-Secret1
check "right CHAP secret accepted" "$(login)" "successful"
logout >/dev/null
check "saved configuration is root-only" "$(vm "sudo stat -c %a /etc/rtslib-fb-target/saveconfig.json")" "600"

# Persistence: clear the kernel target, restore it as at boot.
check "target saved for the next boot" "$(vm "sudo grep -c '\"$IQN\"' /etc/rtslib-fb-target/saveconfig.json")" "1"
check "targetctl clears the kernel target" "$(vm "sudo targetctl clear 2>&1; sudo ls /sys/kernel/config/target/iscsi | grep -c e2e")" "0"
check "target reported stopped" "$(st)" "(False, 2,"
vm "sudo systemctl restart rtslib-fb-targetctl.service"
check "target restored by rtslib-fb-targetctl" "$(st)" "(True, 2,"
check "login after restore (CHAP)" "$(login)" "successful"
logout >/dev/null
# The daemon rebuilds what the boot restore missed (about 20 s after starting).
vm "sudo targetctl clear && sudo systemctl restart strata"
wait_api
for _ in $(seq 1 30); do [ "$(st)" = "[(True, 2, [])]" ] && break; sleep 2; done
check "target rebuilt by StrataOS at startup" "$(st)" "(True, 2,"

# Off means nothing listens.
check "iSCSI service disabled" "$(c PUT /api/iscsi/settings -d '{"enabled":false}')" '"ok":true'
check "port 3260 closed when disabled" "$(open3260)" "closed"
c PUT /api/iscsi/settings -d '{"enabled":true}' >/dev/null

# Cleanup.
check "target deleted with its storage" "$(c DELETE "/api/iscsi/targets/$TID?backing=1")" '"ok":true'
check "image file removed" "$(vm "test -e /srv/strata/main/iscsi/e2e.img && echo left || echo gone")" "gone"
check "zvol removed" "$(vm "sudo zfs list tank/iscsi-e2e 2>&1")" "does not exist"
check "kernel target removed" "$(vm "sudo ls /sys/kernel/config/target/iscsi | grep -c e2e")" "0"
c PUT /api/iscsi/settings -d '{"enabled":false}' >/dev/null
vm "sudo iscsiadm -m node -o delete -T $IQN >/dev/null 2>&1; sudo rmdir /srv/strata/main/iscsi 2>/dev/null; rm -f /tmp/e2e-pat"
