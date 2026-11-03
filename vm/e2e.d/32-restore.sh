# shellcheck shell=bash
# Data protection 0.10: snapshot schedule with calendar retention, guided
# restore (preview, in place, to a folder, download) from a Btrfs snapshot
# and a restic backup, restore tests (success, then a corrupted copy), the
# 3-2-1 assessment and its advisor items.
section "Data protection: snapshots, guided restore, restore tests, 3-2-1"

FASTID=$(c GET /api/storage/volumes | py 'print([v["id"] for v in d if v["name"]=="fast"][0])')
FAST=/srv/strata/fast

# ---- Snapshot schedule with calendar retention ----
SCH=$(c POST /api/schedules -d "{\"name\":\"e2e snapshots\",\"type\":\"snapshot\",\"cron\":\"0 0 1 1 *\",\"enabled\":false,\"targetKind\":\"btrfs\",\"target\":\"$FASTID\",\"keepDaily\":2,\"keepMonthly\":3}" | py 'print(d["id"])')
check "schedule with daily/monthly retention" "$(c GET /api/schedules | py "print([(s['keepDaily'],s['keepMonthly']) for s in d if s['id']=='$SCH'])")" "[(2, 3)]"
check "retention required" "$(c POST /api/schedules -d "{\"name\":\"x\",\"type\":\"snapshot\",\"cron\":\"0 0 * * *\",\"enabled\":false,\"targetKind\":\"btrfs\",\"target\":\"$FASTID\"}")" "set a retention"
check "negative retention refused" "$(c POST /api/schedules -d "{\"name\":\"x\",\"type\":\"snapshot\",\"cron\":\"0 0 * * *\",\"enabled\":false,\"targetKind\":\"btrfs\",\"target\":\"$FASTID\",\"keepHourly\":-1}")" "positive"
check "shared folders offered as snapshot targets" "$(c GET /api/schedules/targets | py 'print(sorted(s["share"] for s in d["shares"]))')" "Photos"
# Older snapshots of this task, dated by their name.
for st in 20260101-010000 20260101-020000 20260101-030000 20260102-120000 20260201-120000; do
  vm "sudo btrfs subvolume snapshot -r $FAST $FAST/.snapshots/root@auto-$SCH-$st >/dev/null"
done
check "retention preview" "$(c GET /api/schedules/$SCH/snapshots | py 'print(" ".join(s["name"][-15:]+("+" if s["keep"] else "-") for s in d))')" "20260201-120000+ 20260102-120000+ 20260101-030000- 20260101-020000- 20260101-010000-"
vm "sudo mkdir -p $FAST/rtest/sub && echo version-one | sudo tee $FAST/rtest/doc.txt >/dev/null && echo keep | sudo tee $FAST/rtest/sub/other.txt >/dev/null"
check "snapshot task run" "$(job "$(c POST /api/schedules/$SCH/run)")" "success"
check "pruned by calendar retention" "$(vm "sudo ls $FAST/.snapshots | grep 'root@auto-$SCH-' | sed 's/.*-\([0-9]*-[0-9]*\)$/\1/' | sort | tr '\n' ' '")" "20260102-120000 20260201-120000 2026"
check "three kept" "$(vm "sudo ls $FAST/.snapshots | grep -c 'root@auto-$SCH-'")" "3"
NEWSNAP=$(vm "sudo ls $FAST/.snapshots | grep 'root@auto-$SCH-' | sort | tail -1" | tr -d '\r')

# ---- Guided restore from the snapshot ----
vm "echo version-two-longer | sudo tee $FAST/rtest/doc.txt >/dev/null; sudo rm $FAST/rtest/sub/other.txt"
SRC="snap:$FAST"
check "snapshot source listed" "$(c GET /api/restore/sources | py 'print([s["id"] for s in d])')" "$SRC"
check "sources for a path" "$(c GET "/api/restore/sources?path=$FAST/rtest/doc.txt" | py 'print([s["id"] for s in d])')" "$SRC"
check "point in time listed" "$(c GET "/api/restore/points?source=$SRC" | py 'print([p["id"] for p in d])')" "$NEWSNAP"
check "browse the snapshot" "$(c GET "/api/restore/browse?source=$SRC&point=$NEWSNAP&path=$FAST/rtest" | py 'print(" ".join(e["name"] for e in d["entries"]))')" "sub doc.txt"
check "browse refuses .." "$(c GET "/api/restore/browse?source=$SRC&point=$NEWSNAP&path=$FAST/rtest/../.." )" "invalid path"
RQ="\"source\":\"$SRC\",\"point\":\"$NEWSNAP\",\"paths\":[\"$FAST/rtest\"]"
check "preview in place: 1 conflict, 1 missing" "$(c POST /api/restore/preview -d "{$RQ,\"mode\":\"inplace\"}" | py 'print(d["files"], d["new"], d["conflicts"], d["items"][0]["path"])')" "2 1 1 $FAST/rtest/doc.txt"
check "preview to a folder: all new" "$(c POST /api/restore/preview -d "{$RQ,\"mode\":\"folder\",\"target\":\"$FAST/restored\"}" | py 'print(d["new"], d["conflicts"])')" "2 0"
check "target outside the volumes refused" "$(c POST /api/restore/preview -d "{$RQ,\"mode\":\"folder\",\"target\":\"/etc\"}")" "error"
check "target inside the selection refused" "$(c POST /api/restore/preview -d "{$RQ,\"mode\":\"folder\",\"target\":\"$FAST/rtest/x\"}")" "inside"
check "restore keeping both" "$(job "$(c POST /api/restore/run -d "{$RQ,\"mode\":\"inplace\",\"policy\":\"rename\"}")")" "success"
check "current file kept, restored copy renamed" "$(vm "sudo cat $FAST/rtest/doc.txt; sudo cat '$FAST/rtest/doc (restored '*').txt'; sudo cat $FAST/rtest/sub/other.txt" | tr '\n' ' ')" "version-two-longer version-one keep"
vm "sudo rm -f '$FAST/rtest/doc (restored '*').txt'"
check "restore to a folder" "$(job "$(c POST /api/restore/run -d "{$RQ,\"mode\":\"folder\",\"target\":\"$FAST/restored\",\"policy\":\"skip\"}")")" "success"
check "restored under the folder" "$(vm "sudo cat $FAST/restored/rtest/doc.txt")" "version-one"
check "restore overwriting" "$(job "$(c POST /api/restore/run -d "{\"source\":\"$SRC\",\"point\":\"$NEWSNAP\",\"paths\":[\"$FAST/rtest/doc.txt\"],\"mode\":\"inplace\",\"policy\":\"overwrite\"}")")" "success"
check "file back to the snapshot version" "$(vm "sudo cat $FAST/rtest/doc.txt")" "version-one"
check "safety snapshot taken first" "$(vm "sudo ls $FAST/.snapshots | grep -c 'root@pre-restore-'")" "1"
check "download a file" "$(c GET "/api/restore/download?source=$SRC&point=$NEWSNAP&path=$FAST/rtest/doc.txt")" "version-one"
check "download a folder as zip" "$(c GET "/api/restore/download?source=$SRC&point=$NEWSNAP&path=$FAST/rtest" -o "$TMP/r.zip"; python3 -c "import zipfile; print(sorted(zipfile.ZipFile('$TMP/r.zip').namelist()))")" "['rtest/doc.txt', 'rtest/sub/other.txt']"
check "restore needs admin" "$(u GET /api/restore/sources)" "error"

# ---- Restic backup: restore and restore test ----
vm "sudo mkdir -p /srv/strata/main/rtest/sub && echo alpha | sudo tee /srv/strata/main/rtest/a.txt >/dev/null && head -c 200000 /dev/urandom | sudo tee /srv/strata/main/rtest/sub/b.bin >/dev/null"
RB=$(c POST /api/backups -d '{"name":"e2e restic","kind":"restic","sources":["/srv/strata/main/rtest"],"destType":"local","destPath":"/srv/strata/fast/restic-e2e","password":"Restic-E2E-Pass","cron":"0 2 * * *","enabled":true}' | py 'print(d["id"])')
check "restic backup" "$(job "$(c POST /api/backups/$RB/run)")" "success"
RP=$(c GET "/api/restore/points?source=backup:$RB" | py 'print(d[0]["id"])')
check "restic roots" "$(c GET "/api/restore/browse?source=backup:$RB&point=$RP" | py 'print([e["path"] for e in d["entries"]])')" "/srv/strata/main/rtest"
check "browse restic" "$(c GET "/api/restore/browse?source=backup:$RB&point=$RP&path=/srv/strata/main/rtest" | py 'print(" ".join(e["name"] for e in d["entries"]))')" "sub a.txt"
vm "sudo rm /srv/strata/main/rtest/a.txt"
RR="\"source\":\"backup:$RB\",\"point\":\"$RP\",\"paths\":[\"/srv/strata/main/rtest\"]"
check "restic preview" "$(c POST /api/restore/preview -d "{$RR,\"mode\":\"inplace\"}" | py 'print(d["new"], d["same"])')" "1 1"
check "restic restore in place" "$(job "$(c POST /api/restore/run -d "{$RR,\"mode\":\"inplace\",\"policy\":\"skip\"}")")" "success"
check "deleted file is back" "$(vm "sudo cat /srv/strata/main/rtest/a.txt")" "alpha"
check "no scratch folder left" "$(vm "sudo ls -a /srv/strata/main | grep -c strata-restore")" "0"
check "restic file download" "$(c GET "/api/restore/download?source=backup:$RB&point=$RP&path=/srv/strata/main/rtest/a.txt")" "alpha"
check "restore test (restic)" "$(job "$(c POST /api/protection/restore-test -d "{\"id\":\"backup:$RB\"}")")" "success"
check "result shown in the task" "$(c GET /api/backups | py "print([(b['restoreTest']['status'], b['restoreTest']['files'], b['restoreTest']['verified']) for b in d if b['id']=='$RB'])")" "[('success', 2, 2)]"

# ---- File copy: a corrupted copy fails the restore test ----
RC=$(c POST /api/backups -d '{"name":"e2e copy","kind":"rsync","sources":["/srv/strata/main/rtest"],"destType":"local","destPath":"/srv/strata/fast/rsync-e2e","cron":"0 3 * * *","enabled":true}' | py 'print(d["id"])')
check "file copy" "$(job "$(c POST /api/backups/$RC/run)")" "success"
check "restore test (file copy)" "$(job "$(c POST /api/protection/restore-test -d "{\"id\":\"backup:$RC\"}")")" "success"
vm "sudo sh -c 'printf omega > /srv/strata/fast/rsync-e2e/rtest/a.txt; printf \"\\n\" >> /srv/strata/fast/rsync-e2e/rtest/a.txt; touch -r /srv/strata/main/rtest/a.txt /srv/strata/fast/rsync-e2e/rtest/a.txt'"
check "restore test detects the corruption" "$(job "$(c POST /api/protection/restore-test -d "{\"id\":\"backup:$RC\"}")")" "failed"
check "failure in the task" "$(c GET /api/backups | py "print([b['restoreTest']['message'] for b in d if b['id']=='$RC'])")" "content differs from the original"
check "failure in the events" "$(c GET /api/system/events | py 'print([e["message"] for e in d][:50])')" "Restore test failed"
check "advisor: restore test failed" "$(c POST /api/security/advisor/run | py 'print([i["id"] for i in d["items"]])')" "restore-test-failed"

# ---- 3-2-1 ----
# Other sections may leave backups: compare before and after.
read -r O1 MISSING <<<"$(c GET /api/protection | py 'r=d["rule"]; print(r["offsite"], ",".join(r["missing"]) or "none")')"
check "3-2-1: copies counted" "$(c GET /api/protection | py 'r=d["rule"]; print(r["copies"] >= 3, [c["name"] for c in r["list"] if c["healthy"]])')" "True"
if [ "$MISSING" != none ]; then
  check "advisor: 3-2-1 not met" "$(c POST /api/security/advisor/run | py 'print([i["title"] for i in d["items"] if i["id"]=="backup-321"])')" "off-site"
fi
c PUT /api/backups/$RB -d "$(c GET /api/backups | py "b=[x for x in d if x['id']=='$RB'][0]; b['offsite']=True; print(json.dumps(b))")" >/dev/null
check "3-2-1: a disk kept off-site counts" "$(c GET /api/protection | py 'print(d["rule"]["offsite"])')" "$((O1 + 1))"
check "restore tests can be turned off" "$(c PUT /api/protection -d '{"restoreTestOff":true}'; c GET /api/protection | py 'print(d["restoreTestOff"])')" "True"
check "advisor: restore tests off" "$(c POST /api/security/advisor/run | py 'print([i["id"] for i in d["items"]])')" "restore-test-off"
c PUT /api/protection -d '{"restoreTestOff":false}' >/dev/null

# ---- Clean up ----
c DELETE /api/backups/$RB >/dev/null
c DELETE /api/backups/$RC >/dev/null
c DELETE /api/schedules/$SCH >/dev/null
vm "for s in \$(sudo ls $FAST/.snapshots | grep -e 'root@auto-$SCH-' -e 'root@pre-restore-'); do sudo btrfs subvolume delete $FAST/.snapshots/\$s >/dev/null; done; sudo rm -rf $FAST/rtest $FAST/restored $FAST/restic-e2e $FAST/rsync-e2e /srv/strata/main/rtest"
c POST /api/security/advisor/run >/dev/null
