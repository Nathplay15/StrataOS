# System disk (Storage → Disks → System disk): layout, plans and refusals of
# the mirror and move wizards, advisor item, system backup onto a data
# volume. Nothing is changed on the system disk here: the mirror, the move,
# the replacement of a mirror disk and the restore from the ISO run on their
# own VMs (vm/sysdisk-test.sh, vm/sysdisk-iso-test.sh).
section "System disk"
SD=$(c GET /api/system/disk)
check "layout of the system" "$(echo "$SD" | py 'l=d["layout"]; print(l["boot"], l["root"]["fstype"], l["rootOn"], l["mirrored"], [x["name"] for x in l["disks"]], l.get("problems"))')" "bios ext4 part False ['vda'] None"
check "system mounts" "$(echo "$SD" | py 'print(sorted((m["target"], m["role"]) for m in d["layout"]["mounts"]))')" "[('/', 'root'), ('/boot/efi', 'esp')]"
check "the system disk is no candidate" "$(echo "$SD" | py 'print([x["path"] for x in d["mirror"]["candidates"] if x["path"]=="/dev/vda"])')" "[]"
check "disks holding data are refused" "$(echo "$SD" | py 'print(sorted(set(x["reason"].split(" (")[0].split(":")[0] for x in d["move"]["candidates"] if not x["ok"])))')" "in use"
check "plan refuses the system disk" "$(c GET '/api/system/disk/plan?kind=mirror&target=/dev/vda' | py 'print(d["refusal"])')" "cannot receive the system"
BUSYD=$(echo "$SD" | py 'print([x["path"] for x in d["move"]["candidates"] if not x["ok"]][0])')
check "plan refuses a data disk" "$(c GET "/api/system/disk/plan?kind=move&target=$BUSYD" | py 'print(d["refusal"])')" "cannot receive the system"
check "start refuses a data disk" "$(c POST /api/system/disk/start -d '{"kind":"move","target":"'"$BUSYD"'","confirm":"'"${BUSYD#/dev/}"'"}')" "cannot receive the system"
check "start refuses an unknown kind" "$(c POST /api/system/disk/start -d '{"kind":"zfs","target":"/dev/sda","confirm":"sda"}')" "unknown operation"
check "no operation recorded" "$(c GET /api/system/disk | py 'print(d.get("ops"))')" "None"
check "user cannot read the system disk" "$(u GET /api/system/disk)" "administrator rights required"
check "advisor: system disk without redundancy" "$(c POST /api/security/advisor/run | py 'print([(i["severity"], i["link"]) for i in d["items"] if i["id"]=="system-disk"])')" "[('low', '/storage/disks#system-disk')]"
check "replace wizard points to the move" "$(c GET '/api/storage/replace/plan?disk=/dev/vda' | py 'print(d["plan"]["refusal"])')" "Move the system to another disk"
check "backup outside the volumes refused" "$(c POST /api/system/disk/backup -d '{"folder":"/etc"}')" "data volume"
check "backup on the system disk refused" "$(c POST /api/system/disk/backup -d '{"folder":"/srv/strata"}')" "data volume"
# A default ACL like those of shared folders: the backup must not inherit it.
vm "sudo mkdir -p /srv/strata/main/sysbackup && sudo setfacl -d -m u:nobody:rx /srv/strata/main/sysbackup"
check "system backup" "$(job "$(c POST /api/system/disk/backup -d '{"folder":"/srv/strata/main/sysbackup"}' | py 'print(json.dumps(d["job"]))')")" "success"
check "backup listed" "$(c GET /api/system/disk | py 'b=d["backups"][0]; print(b["path"].startswith("/srv/strata/main/sysbackup/strataos-system-backup/"), b["size"] > 100e6, len(b["sha256"]))')" "True True 64"
check "archive matches its checksum" "$(vm "sudo sh -c 'cd /srv/strata/main/sysbackup/strataos-system-backup/*/ && sha256sum system.tar.* | cut -d\" \" -f1 | grep -c -f - manifest.json'")" "1"
check "archive holds the system and the settings" "$(vm "sudo sh -c 'tar -tf /srv/strata/main/sysbackup/strataos-system-backup/*/system.tar.* | grep -c -E \"^./(etc/fstab|usr/bin/strata|var/lib/strata/state.json)\$\"'")" "3"
check "archive leaves the data volumes out" "$(vm "sudo sh -c 'tar -tf /srv/strata/main/sysbackup/strataos-system-backup/*/system.tar.* | grep -c \"^./srv/strata/main/.\"'")" "0"
check "archive readable by root only" "$(vm "sudo sh -c 'stat -c %a /srv/strata/main/sysbackup/strataos-system-backup /srv/strata/main/sysbackup/strataos-system-backup/*/ /srv/strata/main/sysbackup/strataos-system-backup/*/system.tar.*' | tr '\n' ' '")" "700 700 600"
check "no ACL passed down to the backup" "$(vm "sudo getfacl -p -c /srv/strata/main/sysbackup/strataos-system-backup 2>&1 | grep -c -E '^(default|user|group):[^:]+:'")" "0"
vm "sudo rm -rf /srv/strata/main/sysbackup"
