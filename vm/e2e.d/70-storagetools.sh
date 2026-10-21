# shellcheck shell=bash
# Storage tools: space analyzer on volume main, duplicate finder with deletion
# into the recycle bin of share Photos, burn-in test of a free disk (read-only,
# limited to the first 256 MiB so it ends in a minute), fan control reported as
# unavailable (the VM has no hwmon PWM output).
section "Storage tools: analyzer, duplicates, burn-in, fans"
ST=/srv/strata/main/e2e-st
vm "sudo mkdir -p $ST/vid $ST/doc && sudo dd if=/dev/urandom of=$ST/vid/film.mkv bs=1M count=6 status=none && sudo dd if=/dev/urandom of=$ST/doc/report.pdf bs=1M count=2 status=none && sudo dd if=/dev/urandom of=$ST/old.iso bs=1M count=3 status=none && sudo touch -d '2 years ago' $ST/old.iso && sudo ln $ST/doc/report.pdf $ST/doc/hardlink.pdf"
check "analyzer targets list volume main" "$(c GET /api/storagetools/targets | py 'print([t["kind"] for t in d if t["path"]=="/srv/strata/main"])')" "['volume']"
check "analysis of main" "$(job "$(c POST /api/storagetools/analyzer -d '{"path":"/srv/strata/main"}')")" "success"
check "path outside the targets refused" "$(c POST /api/storagetools/analyzer -d '{"path":"/etc"}')" "choose a volume"
AN=$(c GET '/api/storagetools/analyzer?path=/srv/strata/main&dir=e2e-st')
check "folder sizes (hard link counted once)" "$(echo "$AN" | py 'print(d["tree"]["files"], sorted(c["name"] for c in d["tree"]["children"]), d["tree"]["size"] >= 11*2**20, d["tree"]["size"] < 12*2**20)')" "3 ['doc', 'vid'] True True"
check "drill-down order (largest first)" "$(echo "$AN" | py 'print(d["tree"]["children"][0]["name"])')" "vid"
check "largest files" "$(echo "$AN" | py 'print("e2e-st/vid/film.mkv" in [f["path"] for f in d["report"]["largest"]])')" "True"
check "old large files" "$(echo "$AN" | py 'print("e2e-st/old.iso" in [f["path"] for f in d["report"]["old"]])')" "True"
check "type breakdown" "$(echo "$AN" | py 'print(sorted(t["category"] for t in d["report"]["types"] if t["category"] in ("video","documents","diskimages")))')" "['diskimages', 'documents', 'video']"
check "analysis cached with its date" "$(c GET /api/storagetools/targets | py 'print([bool(t.get("scanned")) for t in d if t["path"]=="/srv/strata/main"])')" "[True]"
check "user cannot analyze" "$(u POST /api/storagetools/analyzer -d '{"path":"/srv/strata/main"}')" "administrator rights required"

P=/srv/strata/main/Photos
vm "sudo mkdir -p $P/e2e-dupA $P/e2e-dupB && sudo dd if=/dev/urandom of=$P/e2e-dupA/same.bin bs=1M count=2 status=none && sudo cp $P/e2e-dupA/same.bin $P/e2e-dupB/same.bin && sudo cp $P/e2e-dupA/same.bin $P/e2e-dupB/other.bin && sudo truncate -s 2M $P/e2e-dupB/zero.bin && sudo ln $P/e2e-dupA/same.bin $P/e2e-dupA/zz-link.bin"
check "duplicate search" "$(job "$(c POST /api/storagetools/duplicates -d '{"paths":["'$P'/e2e-dupA","'$P'/e2e-dupB"]}')")" "success"
DUP=$(c GET /api/storagetools/duplicates)
check "one group of 3 copies (hard link not counted)" "$(echo "$DUP" | py 'print(len(d["report"]["groups"]), len(d["report"]["groups"][0]["files"]), d["report"]["wasted"])')" "1 3 4194304"
check "deleting every copy refused" "$(c POST /api/storagetools/duplicates/delete -d '{"paths":["'$P'/e2e-dupA/same.bin","'$P'/e2e-dupB/same.bin","'$P'/e2e-dupB/other.bin"]}')" "at least one must stay"
check "path outside the result refused" "$(c POST /api/storagetools/duplicates/delete -d '{"paths":["/etc/passwd"]}')" "not in the duplicate list"
check "duplicate moved to the recycle bin" "$(c POST /api/storagetools/duplicates/delete -d '{"paths":["'$P'/e2e-dupB/same.bin"]}' | py 'print(d["recycled"], d["deleted"], d["skipped"])')" "1 0 []"
check "copy in .recycle, original kept" "$(vm "sudo test -f $P/.recycle/$ADMIN_USER/e2e-dupB/same.bin && sudo test ! -e $P/e2e-dupB/same.bin && sudo test -f $P/e2e-dupA/same.bin && echo ok")" "ok"
check "result updated" "$(c GET /api/storagetools/duplicates | py 'print(len(d["report"]["groups"][0]["files"]))')" "2"
vm "sudo rm -rf $P/e2e-dupA $P/e2e-dupB $P/.recycle/$ADMIN_USER/e2e-dupB $ST"

# Burn-in: the smallest free disk, read-only, first 65536 blocks of 4 KiB.
BD=$(c GET /api/storage/disks | py 'f=[x for x in d if x["usage"]=="free" and not x.get("system")]; f.sort(key=lambda x: x["size"]); print(f[0]["path"] if f else "")')
if [ -n "$BD" ]; then
  BN=${BD#/dev/}
  check "write test needs the typed confirmation" "$(c POST /api/storage/burnin -d '{"device":"'$BD'","mode":"write"}')" "erases the whole disk"
  check "system disk refused" "$(c POST /api/storage/burnin -d '{"device":"/dev/vda","mode":"read"}')" "system disk"
  BJ=$(c POST /api/storage/burnin -d '{"device":"'$BD'","mode":"read","shortTest":true,"longTest":false,"blocks":65536}')
  check "disk reserved during the test" "$(c POST /api/storage/disks/wipe -d '{"device":"'$BD'"}')" "busy"
  check "burn-in test of $BN" "$(job "$BJ")" "success"
  BR=$(c GET /api/storage/burnin)
  check "report stored: passed, no bad block" "$(echo "$BR" | py 'r=d["reports"][0]; print(r["device"], r["result"], r["badBlocks"], [s["name"] for s in r["steps"]])')" "$BD passed 0 ['smart-short', 'badblocks']"
  check "badblocks step passed" "$(echo "$BR" | py 'print([s["status"] for s in d["reports"][0]["steps"] if s["name"]=="badblocks"])')" "['passed']"
  check "disk free again" "$(c GET /api/storage/disks | py "print([x['usage'] for x in d if x['path']=='$BD'])")" "['free']"
  # Cancelling stops badblocks cleanly. A full read of an empty virtual disk
  # takes seconds: repeat the scan of a short range (hidden "passes") so the
  # test is still running, and cancel once the API shows the badblocks step.
  BJ=$(c POST /api/storage/burnin -d '{"device":"'$BD'","mode":"read","shortTest":false,"blocks":65536,"passes":1000}')
  STEP=""
  for _ in $(seq 1 60); do
    STEP=$(c GET /api/storage/burnin | py "print([r['step'] for r in d['running'] if r['device']=='$BD'])")
    [ "$STEP" = "['badblocks']" ] && break
    sleep 1
  done
  check "test reached the badblocks step" "$STEP" "['badblocks']"
  sleep 2
  c POST "/api/jobs/$(echo "$BJ" | py 'print(d["id"])')/cancel" >/dev/null
  check "cancelled test" "$(job "$BJ")" "failed burn-in test cancelled"
  check "badblocks gone after cancel" "$(vm "pgrep -x badblocks || echo none")" "none"
  check "cancelled report" "$(c GET /api/storage/burnin | py 'print(d["reports"][0]["result"])')" "cancelled"
  for id in $(c GET /api/storage/burnin | py 'print(" ".join(r["id"] for r in d["reports"]))'); do c DELETE "/api/storage/burnin/$id" >/dev/null; done
else
  ko "burn-in test" "no free disk left in the VM"
fi

HW=$(c GET /api/hardware)
check "no controllable fans reported" "$(echo "$HW" | py 'print(d["notice"], d["profile"], d["active"])')" "none bios False"
check "fan profile refused without PWM" "$(c PUT /api/hardware/fans -d '{"profile":"quiet"}')" "no controllable fans"
check "BIOS profile accepted" "$(c PUT /api/hardware/fans -d '{"profile":"bios"}')" '"ok":true'
check "bad custom curve refused" "$(c PUT /api/hardware/fans -d '{"profile":"custom","cpuCurve":[{"temp":50,"pwm":5}],"diskCurve":[]}')" "processor curve"
check "LED list (none in the VM)" "$(echo "$HW" | py 'print(type(d["leds"]).__name__)')" "list"
