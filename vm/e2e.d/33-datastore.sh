# Backup repositories (datastores, 0.11): two similar VMs backed up as
# images into one repository (deduplication), sizes and contents, single
# files read from a VM disk backup opened read-only (ext4 partition and an
# LVM volume inside the guest), a whole VM restored (new name, then in
# place), the VM app and 3-2-1 views, then an integrity check that passes,
# a damaged pack file and a check that finds it (advisor item).
# The guests are never started: their disks are built here (256 MB each).
section "Backup repositories: deduplicated VM images, checks, single-file restore"
DSVM=/srv/strata/main/Photos
DSWORK=/srv/strata/main/ds-e2e-work
DSREPO=/srv/strata/fast/ds-e2e
DSFILES=/srv/strata/main/ds-e2e-files
vm "sudo mkdir -p $DSFILES && echo folder-in-repository | sudo tee $DSFILES/note.txt >/dev/null"
for comp in vms restic; do
  if [ "$(c GET /api/system/components | py "print(d.get('$comp'))")" != True ]; then
    check "$comp component installed" "$(job "$(c POST /api/system/components/$comp/install)")" "success"
  fi
done

vm "sudo rm -rf $DSVM/pbs-a $DSVM/pbs-b $DSVM/pbs-a2 $DSREPO" # left by an interrupted run
# Guest disk: GPT, an ext4 partition and an LVM group (e2egvg/data, ext4),
# 40 MB of random data both guests share, and one file of their own.
vm "sudo bash -s" <<EOF >/dev/null 2>&1
set -e
mkdir -p $DSWORK && cd $DSWORK
truncate -s 256M a.raw
sfdisk -q a.raw <<'P'
label: gpt
size=96MiB, type=linux
type=lvm
P
L=\$(losetup --find --show --partscan a.raw)
udevadm settle
mkfs.ext4 -q -L e2eroot \${L}p1
pvcreate -q \${L}p2 && vgcreate -q e2egvg \${L}p2 && lvcreate -q -y -L 64M -n data e2egvg
mkfs.ext4 -q /dev/e2egvg/data
mkdir -p m && mount \${L}p1 m && head -c 40M /dev/urandom > m/shared.bin && echo datastore-file-ok > m/hello.txt && umount m
mount /dev/e2egvg/data m && echo lvm-file-ok > m/lvm.txt && umount m
vgchange -q -an e2egvg && losetup -d \$L
cp a.raw b.raw
L=\$(losetup --find --show --partscan b.raw); udevadm settle
mount \${L}p1 m && echo only-in-b > m/b.txt && umount m
vgchange -q -an e2egvg 2>/dev/null || true
losetup -d \$L
qemu-img convert -O qcow2 a.raw a.qcow2 && qemu-img convert -O qcow2 b.raw b.qcow2 && rm -f a.raw b.raw
EOF
check "guest disks prepared" "$(vm "sudo ls $DSWORK" | tr '\n' ' ')" "a.qcow2 b.qcow2"
for g in a b; do
  check "VM pbs-$g imported" "$(job "$(c POST /api/vms/import -d "{\"name\":\"pbs-$g\",\"source\":\"$DSWORK/$g.qcow2\",\"os\":\"linux\",\"vcpus\":1,\"memoryMb\":256,\"storage\":\"$DSVM\",\"network\":\"nat\",\"firmware\":\"bios\"}")")" "success"
  c POST /api/vms/pbs-$g/destroy >/dev/null # imports start; the guests have no system
done

check "repository password checked" "$(c POST /api/datastores -d '{"name":"x","destType":"local","destPath":"'$DSREPO'","password":"short","verify":{}}')" "at least 8"
check "repository outside the volumes refused" "$(c POST /api/datastores -d '{"name":"x","destType":"local","destPath":"/etc/ds","password":"Datastore-E2E-1","verify":{}}')" "inside a volume"
DSR=$(c POST /api/datastores -d '{"name":"e2e repository","destType":"local","destPath":"'$DSREPO'","password":"Datastore-E2E-1","verify":{"subsets":2}}')
DS=$(echo "$DSR" | py 'print(d["id"])' 2>/dev/null) || echo "    datastore not created: ${DSR:0:300}"
check "repository created (restic init)" "$(vm "sudo ls $DSREPO")" "config"
check "repository listed, secrets masked, never checked" "$(c GET /api/datastores | py "x=[s for s in d if s['id']=='$DS'][0]; print(x['password'], x['subsets'], len(x.get('verifies') or []), bool(x.get('nextVerify')))")" "******** 2 0 True"
check "task needs an existing repository" "$(c POST /api/backups -d '{"name":"x","kind":"restic","sources":[],"vms":["pbs-a"],"destType":"datastore","datastore":"nope","cron":"","enabled":true}')" "choose a backup repository"
DT=$(c POST /api/backups -d '{"name":"e2e images","kind":"restic","sources":["'$DSFILES'"],"vms":["pbs-a","pbs-b"],"destType":"datastore","datastore":"'$DS'","keepLast":3,"keepYearly":2,"cron":"","enabled":true}' | py 'print(d["id"])')
check "image backup of two VMs and a folder" "$(job "$(c POST /api/backups/$DT/run)")" "success"
check "history tells what was stored" "$(c GET /api/backups | py "print([b['history'][0]['message'] for b in d if b['id']=='$DT'][0])")" "2 VM(s) and container(s) backed up"
STATS=$(c GET /api/datastores | py "x=[s for s in d if s['id']=='$DS'][0]['stats']; print(x['snapshots'], x['logical'] > 500e6, round(x['dedup'], 2), x['ratio'] > 1.5)")
check "sizes measured: 5 snapshots, 512 MB backed up, ratio > 1.5" "$STATS" "5 True"
check "deduplication ratio above 1" "$(echo "$STATS" | awk '{print ($3 > 1.2) ? "dedup>1" : "dedup=" $3}')" "dedup>1"
check "contents: both VMs complete with disk and definition" "$(c GET /api/datastores/$DS/contents | py "print(sorted((i['name'], i['complete'], [p['name'] for p in i['parts']]) for i in d['items'] if i['kind']=='vm'))")" "[('pbs-a', True, ['config', 'disk-vda']), ('pbs-b', True, ['config', 'disk-vda'])]"
check "second VM stored little new data" "$(c GET /api/datastores/$DS/contents | py "v={i['name']: i['added'] for i in d['items'] if i['kind']=='vm'}; print('shared' if min(v.values()) < 0.5*max(v.values()) else v)")" "shared"
check "second run stores almost nothing" "$(job "$(c POST /api/backups/$DT/run)"; c GET /api/datastores/$DS/contents | py "v=[i for i in d['items'] if i['kind']=='vm']; print('small' if v[0]['added'] < 5e6 else v[0]['added'])")" "small"
check "VM app lists the image backups" "$(c GET /api/vms/backups | py "print(sorted(set((b['vm'], b['consistency']) for b in d if b['task']=='$DT')))")" "[('pbs-a', 'image'), ('pbs-b', 'image')]"
check "3-2-1 counts the repository task" "$(c GET /api/protection | py "print([(x['id'], x['media']) for x in d['rule']['list'] if x['id']=='backup:$DT'])")" "('backup:$DT', 'nas')"

# Single files from a VM disk backup.
SRC="image:$DT/pbs-b"
check "VM disks offered by the guided restore" "$(c GET /api/restore/sources | py "print([s['kind'] for s in d if s['id']=='$SRC'])")" "['image']"
PT=$(c GET "/api/restore/points?source=$SRC" | py 'print(d[0]["id"], d[0]["label"])')
check "disk points" "$PT" " vda"
PT=${PT%% *}
check "closed disk cannot be browsed" "$(c GET "/api/restore/browse?source=$SRC&point=$PT")" "open the disk first"
check "disk opened read-only" "$(job "$(c POST /api/restore/disk/open -d "{\"source\":\"$SRC\",\"point\":\"$PT\"}")")" "success"
check "partition and LVM volume mounted" "$(c GET "/api/restore/disk?source=$SRC&point=$PT" | py 'print(d["state"], [(r["path"], r["fsType"]) for r in d["session"]["roots"]])')" "open [('/vda1', 'ext4'), ('/e2egvg-data', 'ext4')]"
check "mounts are read-only" "$(vm "sudo findmnt -rn -o TARGET,OPTIONS" | grep diskmount | grep -c ' ro,')" "2"
check "browse the roots" "$(c GET "/api/restore/browse?source=$SRC&point=$PT" | py 'print([e["path"] for e in d["entries"]])')" "['/vda1', '/e2egvg-data']"
check "browse a partition" "$(c GET "/api/restore/browse?source=$SRC&point=$PT&path=/vda1" | py 'print(" ".join(e["name"] for e in d["entries"]))')" "b.txt hello.txt"
check "download one file" "$(c GET "/api/restore/download?source=$SRC&point=$PT&path=/vda1/b.txt")" "only-in-b"
check "download from the guest LVM" "$(c GET "/api/restore/download?source=$SRC&point=$PT&path=/e2egvg-data/lvm.txt")" "lvm-file-ok"
check "in place refused for a disk" "$(c POST /api/restore/preview -d "{\"source\":\"$SRC\",\"point\":\"$PT\",\"paths\":[\"/vda1/hello.txt\"],\"mode\":\"inplace\"}")" "restored to a folder"
check "single file restored to a folder" "$(job "$(c POST /api/restore/run -d "{\"source\":\"$SRC\",\"point\":\"$PT\",\"paths\":[\"/vda1/hello.txt\"],\"mode\":\"folder\",\"target\":\"$DSWORK/out\",\"policy\":\"rename\"}")"; vm "sudo cat $DSWORK/out/hello.txt")" "datastore-file-ok"
check "disk closed: mounts, loop and image gone" "$(c POST /api/restore/disk/close -d "{\"source\":\"$SRC\",\"point\":\"$PT\"}" >/dev/null; echo "$(vm "sudo findmnt -rn | grep -c diskmount; sudo losetup -l | grep -c diskmount; ls -d /srv/strata/*/.strata-diskmount-* 2>/dev/null | wc -l; sudo vgs e2egvg 2>&1 | grep -c 'not found'")" | tr '\n' ' ')" "0 0 0 1"

# Whole VM restores.
AID=$(c GET /api/datastores/$DS/contents | py "print([i['id'] for i in d['items'] if i['name']=='pbs-a'][0])")
check "VM restored under a new name" "$(job "$(c POST /api/datastores/$DS/restore -d "{\"snapshot\":\"$AID\",\"name\":\"pbs-a2\",\"storage\":\"$DSVM\"}")")" "success"
check "restored disk identical to the original" "$(vm "sudo qemu-img compare -U -q $DSVM/pbs-a/disk0.qcow2 $DSVM/pbs-a2/disk0.qcow2 && echo same")" "same"
check "existing name refused without replace" "$(job "$(c POST /api/datastores/$DS/restore -d "{\"snapshot\":\"$AID\",\"name\":\"pbs-a\",\"storage\":\"$DSVM\"}")")" "already exists"
vm "echo changed | sudo dd of=$DSVM/pbs-a/disk0.qcow2 bs=1 seek=300000000 conv=notrunc 2>/dev/null; true" >/dev/null
check "VM replaced by its backup" "$(job "$(c POST /api/datastores/$DS/restore -d "{\"snapshot\":\"$AID\",\"name\":\"pbs-a\",\"storage\":\"$DSVM\",\"replace\":true}")")" "success"
check "replaced VM back to the backed-up disk" "$(vm "sudo qemu-img compare -U -q $DSVM/pbs-a/disk0.qcow2 $DSVM/pbs-a2/disk0.qcow2 && echo same")" "same"

# Integrity checks.
check "verify now passes (part 1/2)" "$(job "$(c POST /api/datastores/$DS/verify -d '{}')"; c GET /api/datastores | py "x=[s for s in d if s['id']=='$DS'][0]; print(x['verifies'][0]['status'], x['verifies'][0]['subset'], x['verifyNext'], bool(x.get('lastVerified')))")" "success 1/2 2 True"
check "rotation reads the other half next" "$(job "$(c POST /api/datastores/$DS/verify -d '{}')"; c GET /api/datastores | py "x=[s for s in d if s['id']=='$DS'][0]; print(x['verifies'][0]['subset'], x['verifyNext'])")" "2/2 1"
check "cleanup (prune) runs" "$(job "$(c POST /api/datastores/$DS/prune)")" "success"
PACK=$(vm "sudo find $DSREPO/data -type f -size +1M -printf '%s %p\n' | sort -n | tail -1 | cut -d' ' -f2")
vm "sudo chmod u+w $PACK && printf 'CORRUPTED-BY-E2E' | sudo dd of=$PACK bs=1 seek=4096 conv=notrunc 2>/dev/null"
check "full check finds the damaged pack" "$(job "$(c POST /api/datastores/$DS/verify -d '{"full":true}')")" "failed"
check "damaged pack and affected snapshots recorded" "$(c GET /api/datastores | py "v=[s for s in d if s['id']=='$DS'][0]['verifies'][0]; print(v['subset'], [b[:12] for b in v.get('broken', [])], len(v.get('affected', [])) > 0)")" "all ['$(basename "$PACK" | cut -c1-12)'] True"
check "advisor: repository check failed" "$(c POST /api/security/advisor/run | py 'print([i["id"] for i in d["items"]])')" "datastore-verify-failed"
check "failure in the event log" "$(c GET /api/system/events | py 'print([e["message"] for e in d][:50])')" "Integrity check of the backup repository e2e repository failed"

# Clean up.
check "repository in use cannot be removed" "$(c DELETE /api/datastores/$DS)" "uses this repository"
c DELETE /api/backups/$DT >/dev/null
check "repository removed (data kept)" "$(c DELETE /api/datastores/$DS; c GET /api/datastores | py "print(len([s for s in d if s['id']=='$DS']))")" "0"
for g in pbs-a pbs-b pbs-a2; do c POST /api/vms/$g/destroy >/dev/null; c DELETE "/api/vms/$g?disks=1" >/dev/null; done
vm "sudo rm -rf $DSREPO $DSWORK $DSFILES $DSVM/pbs-a $DSVM/pbs-b $DSVM/pbs-a2"
c POST /api/security/advisor/run >/dev/null
