# Guided disk replacement (Storage → Disks → Replace): RAID 1 replaced with
# the new disk added first, rebuild of a degraded array (with a restart of
# StrataOS in the middle), ZFS mirror, LVM pvmove, and the refusals.
# Needs 4 extra blank disks (serials STRATA-VD-10…13: VM_DISKS=13); skipped otherwise.
section "Disk replacement"
sdev() { c GET /api/storage/disks | py "l=[x['path'] for x in d if x['serial']=='$1']; print(l[0] if l else '')"; }
R1=$(sdev STRATA-VD-10) R2=$(sdev STRATA-VD-11) R3=$(sdev STRATA-VD-12) R4=$(sdev STRATA-VD-13)
if [ -z "$R4" ]; then
  echo "  (skipped: needs 4 extra disks, VM_DISKS=13)"
else
  usage_of() { c GET /api/storage/disks | py "print([x['usage'] for x in d if x['path']=='$1'][0])"; }
  wait_free() { for d in "$@"; do for _ in $(seq 1 15); do [ "$(usage_of "$d")" = free ] && break; sleep 2; done; done; }
  plan() { c GET "/api/storage/replace/plan?$1"; }
  rstart() { c POST /api/storage/replace -d "$1" | py 'print(d.get("id") or d)'; }
  rwait() { # waits for a replacement and prints its phase and error
    for _ in $(seq 1 200); do
      ph=$(c GET /api/storage/replace | py "print([i['phase'] for i in d['items'] if i['id']=='$1'][0])" 2>/dev/null)
      case $ph in done | failed) break ;; esac; sleep 3
    done
    c GET /api/storage/replace | py "i=[i for i in d['items'] if i['id']=='$1'][0]; print(i['phase'], i.get('error',''))"
  }
  md_of() { c GET /api/storage/raid | py "print([a['name'] for a in d if a['label']=='$1'][0])"; }
  raid_sync_wait() { for _ in $(seq 1 100); do [ "$(c GET /api/storage/raid | py "print([a.get('syncAction','') for a in d if a['label']=='$1'][0])")" = "" ] && break; sleep 3; done; }
  members() { c GET /api/storage/raid | py "print(sorted(m['device']+':'+m['state'] for a in d if a['label']=='$1' for m in a['members']))"; }
  n() { basename "$1"; }

  check "free disk: nothing to rebuild" "$(plan "disk=$R4" | py 'print(d["plan"]["refusal"])')" "nothing to rebuild"

  # RAID 1, the new disk added first (mdadm --replace).
  check "RAID 1 for the replacement" "$(job "$(c POST /api/storage/raid -d '{"name":"rp","level":"1","devices":["'"$R1"'","'"$R2"'"]}')")" "success"
  raid_sync_wait rp
  check "plan: add the new disk first" "$(plan "disk=$R1" | py 'p=d["plan"]; print(p["mode"], p["left"], p["group"]["level"], p.get("refusal",""))')" "add-first 1 raid1"
  check "plan: the new disk is offered" "$(plan "disk=$R1" | py "print([c['ok'] for c in d['plan']['candidates'] if c['path']=='$R3'])")" "[True]"
  check "plan: degraded mode refused for a working disk" "$(plan "disk=$R1&mode=degraded" | py 'print(d["plan"]["refusal"])')" "still works"
  check "refused: new disk already a member" "$(c POST /api/storage/replace -d '{"disk":"'"$R1"'","new":"'"$R2"'","confirm":"'"$(n "$R2")"'"}')" "already a member"
  check "refused: name not typed" "$(c POST /api/storage/replace -d '{"disk":"'"$R1"'","new":"'"$R3"'","confirm":"x"}')" "type the name"
  ID=$(rstart '{"disk":"'"$R1"'","new":"'"$R3"'","confirm":"'"$(n "$R3")"'","smartTest":true}')
  check "replacement done" "$(rwait "$ID")" "done"
  check "new disk active, old disk out" "$(members rp)" "['$R2:active sync', '$R3:active sync']"
  check "array healthy" "$(c GET /api/storage/raid | py "print([a['health'] for a in d if a['label']=='rp'][0])")" "healthy"
  check "disk to pull named by serial" "$(c GET /api/storage/replace | py "i=[i for i in d['items'] if i['id']=='$ID'][0]; print(i['oldSerial'], i['oldDisk'])")" "STRATA-VD-10 $R1"
  check "old disk released" "$(wait_free "$R1"; usage_of "$R1")" "free"
  check "event logged" "$(c GET /api/system/events | py 'print([e["message"] for e in (d if isinstance(d, list) else d.get("events", [])) if "you can pull the old disk" in e["message"]][:1])')" "STRATA-VD-10"
  check "mdadm.conf updated" "$(vm "grep -c rp /etc/mdadm/mdadm.conf")" "1"

  # Degraded array: the failed disk is already gone.
  c POST /api/storage/raid/rp/remove -d "{\"device\":\"$R2\"}" >/dev/null
  wait_free "$R2"
  MD=$(md_of rp)
  check "array degraded" "$(c GET /api/storage/raid | py "print([a['health'] for a in d if a['label']=='rp'][0])")" "degraded"
  check "alert opens the wizard" "$(c GET /api/system/overview | py "print([a.get('replace') for a in d['alerts'] if 'rp' in a['message']])")" "raid:$MD"
  check "plan: rebuild the missing disk" "$(plan "group=raid:$MD" | py 'p=d["plan"]; print(p["mode"], p["left"], p["old"]["state"])')" "degraded 0 missing"
  check "plan: no redundancy warning" "$(plan "group=raid:$MD" | py 'print(d["plan"]["warnings"])')" "No redundancy is left"
  check "plan: add-first refused" "$(plan "group=raid:$MD&mode=add-first" | py 'print(d["plan"]["refusal"])')" "out of service"
  ID=$(rstart '{"group":"raid:'"$MD"'","new":"'"$R1"'","confirm":"'"$(n "$R1")"'"}')
  # A restart in the middle: the replacement is resumed.
  sleep 2; vm "sudo systemctl restart strata"; sleep 3; wait_api
  c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
  check "rebuild done after a restart" "$(rwait "$ID")" "done"
  check "array complete again" "$(members rp)" "['$R1:active sync', '$R3:active sync']"
  check "nothing to pull" "$(c GET /api/storage/replace | py "i=[i for i in d['items'] if i['id']=='$ID'][0]; print(repr(i.get('oldDisk','')))")" "''"
  check "RAID 1 deleted" "$(c DELETE /api/storage/raid/rp)" '"ok":true'
  wait_free "$R1" "$R3"

  # No redundancy: refused.
  check "RAID 0 for the refusal" "$(job "$(c POST /api/storage/raid -d '{"name":"r0","level":"0","devices":["'"$R1"'","'"$R2"'"]}')")" "success"
  check "RAID 0 refused" "$(plan "disk=$R1" | py 'print(d["plan"]["refusal"])')" "no redundancy"
  check "RAID 0 refused by the API too" "$(c POST /api/storage/replace -d '{"disk":"'"$R1"'","new":"'"$R3"'","confirm":"'"$(n "$R3")"'"}')" "no redundancy"
  c DELETE /api/storage/raid/r0 >/dev/null
  wait_free "$R1" "$R2"

  # ZFS mirror.
  if [ "$(c GET /api/storage/zfs | py 'print(d["available"])')" = True ]; then
    check "ZFS mirror for the replacement" "$(job "$(c POST /api/storage/zfs -d '{"name":"rz","vdevs":[{"type":"mirror","disks":["'"$R1"'","'"$R2"'"]}]}')")" "success"
    vm "sudo dd if=/dev/urandom of=/srv/strata/rz/blob bs=1M count=64 status=none; sync"
    SUM=$(vm "sudo sha256sum /srv/strata/rz/blob")
    check "plan: ZFS add-first" "$(plan "disk=$R1" | py 'p=d["plan"]; print(p["mode"], p["group"]["kind"], p["group"]["level"])')" "add-first zfs mirror"
    ID=$(rstart '{"disk":"'"$R1"'","new":"'"$R3"'","confirm":"'"$(n "$R3")"'"}')
    check "ZFS replacement done" "$(rwait "$ID")" "done"
    check "new disk in the mirror, old one out" "$(plan "disk=$R2" | py "print(sorted(m.get('disk','') for m in d['plan']['group']['members']))")" "$(python3 -c "print(sorted(['$R2','$R3']))")"
    check "pool online" "$(c GET /api/storage/zfs | py "print([p['health'] for p in d['pools'] if p['name']=='rz'][0])")" "ONLINE"
    check "data intact" "$(vm "sudo sha256sum /srv/strata/rz/blob")" "$SUM"
    c DELETE /api/storage/zfs/rz >/dev/null
    for d in "$R1" "$R2" "$R3"; do [ "$(usage_of "$d")" = free ] || c POST /api/storage/disks/wipe -d "{\"device\":\"$d\"}" >/dev/null; done
    wait_free "$R1" "$R2" "$R3"
  else
    echo "  (ZFS not installed: ZFS replacement skipped)"
  fi

  # LVM: pvmove onto the new disk.
  check "volume group for the replacement" "$(c POST /api/storage/lvm -d '{"name":"rv","devices":["'"$R4"'"]}')" '"ok":true'
  check "logical volume" "$(c POST /api/storage/lvm/rv/lv -d '{"name":"data","size":536870912}')" '"ok":true'
  vm "sudo mkfs.ext4 -q /dev/rv/data && sudo mkdir -p /mnt/rv && sudo mount /dev/rv/data /mnt/rv && sudo dd if=/dev/urandom of=/mnt/rv/blob bs=1M count=64 status=none && sync"
  SUM=$(vm "sudo sha256sum /mnt/rv/blob")
  check "plan: pvmove" "$(plan "disk=$R4" | py 'p=d["plan"]; print(p["mode"], p["group"]["kind"], p["modes"][1]["ok"])')" "add-first lvm False"
  check "plan: no redundancy warning" "$(plan "disk=$R4" | py 'print(d["plan"]["warnings"])')" "no redundant copy"
  ID=$(rstart '{"disk":"'"$R4"'","new":"'"$R2"'","confirm":"'"$(n "$R2")"'"}')
  check "LVM replacement done" "$(rwait "$ID")" "done"
  check "volume group on the new disk only" "$(vm "sudo pvs --noheadings -o pv_name -S vg_name=rv" | tr -d ' ')" "$R2"
  check "mounted data intact" "$(vm "sudo sha256sum /mnt/rv/blob")" "$SUM"
  vm "sudo umount /mnt/rv"
  c DELETE /api/storage/lvm/rv/lv/data >/dev/null; c DELETE /api/storage/lvm/rv >/dev/null
  for d in "$R1" "$R2" "$R3" "$R4"; do [ "$(usage_of "$d")" = free ] || c POST /api/storage/disks/wipe -d "{\"device\":\"$d\"}" >/dev/null; done
  wait_free "$R1" "$R2" "$R3" "$R4"
  check "extra disks free again" "$(for d in "$R1" "$R2" "$R3" "$R4"; do usage_of "$d"; done | sort -u | tr '\n' ' ')" "free "
  for id in $(c GET /api/storage/replace | py 'print(" ".join(i["id"] for i in d["items"] if not i["running"]))'); do c DELETE "/api/storage/replace/$id" >/dev/null; done
fi
