# Routines that act (0.13): a routine whose actions run a snapshot task (ZFS
# dataset tank/media), a backup task and start then stop a Docker container,
# as jobs named after the routine; refusals of unknown targets and of
# operations other than start/stop; only for administrators.
section "Routines that act"
check "acting routines are for administrators" "$(u POST /api/routines -d '{"name":"x","trigger":"cpu","value":90,"actions":[{"kind":"backup","target":"x"}]}')" "administrator rights required"
check "unknown backup task refused" "$(c POST /api/routines -d '{"name":"x","trigger":"cpu","value":90,"actions":[{"kind":"backup","target":"nope"}]}')" "unknown backup task"
check "only start or stop" "$(c POST /api/routines -d '{"name":"x","trigger":"cpu","value":90,"actions":[{"kind":"vm","target":"e2e","op":"destroy"}]}')" "start or stop"
check "unknown action refused" "$(c POST /api/routines -d '{"name":"x","trigger":"cpu","value":90,"actions":[{"kind":"shell","target":"ls"}]}')" "unknown action"
# A snapshot task of the ZFS dataset and a backup task to a local folder.
SNAP=$(c POST /api/schedules -d '{"name":"E2E routine snapshots","type":"snapshot","targetKind":"zfs","target":"tank/media","cron":"0 3 1 1 *","enabled":false,"keepCount":3}' | py 'print(d.get("id",""))')
check "snapshot task for the routine" "$([ -n "$SNAP" ] && echo created)" "created"
vm "sudo mkdir -p /srv/strata/main/Photos/e2e-rt-src /srv/strata/main/e2e-rt-dst && echo hello | sudo tee /srv/strata/main/Photos/e2e-rt-src/f >/dev/null"
BK=$(c POST /api/backups -d '{"name":"E2E routine backup","kind":"rsync","sources":["/srv/strata/main/Photos/e2e-rt-src"],"destType":"local","destPath":"/srv/strata/main/e2e-rt-dst","cron":"","enabled":true}' | py 'print(d.get("id",""))')
check "backup task for the routine" "$([ -n "$BK" ] && echo created)" "created"
vm "sudo docker rm -f e2e-rt >/dev/null 2>&1; sudo docker run -d --name e2e-rt --restart=no alpine:3 sleep 3600 >/dev/null 2>&1 || sudo docker run -d --name e2e-rt busybox sleep 3600 >/dev/null"
vm "sudo docker stop -t 1 e2e-rt >/dev/null"
RT=$(c POST /api/routines -d '{"name":"E2E act","enabled":false,"trigger":"cpu","value":99,"actions":[{"kind":"snapshot","target":"'"$SNAP"'"},{"kind":"backup","target":"'"$BK"'"},{"kind":"docker","target":"e2e-rt","op":"start"}],"cooldown":60}' | py 'print(d["id"])')
check "acting routine created" "$(c GET /api/routines | py 'print([[a["kind"] for a in r["actions"]] for r in d if r["id"]=="'"$RT"'"])')" "['snapshot', 'backup', 'docker']"
c POST "/api/routines/$RT/fire" >/dev/null
for _ in $(seq 1 60); do [ "$(c GET /api/jobs | py 'print(sum(1 for j in d if j.get("user")=="routine:E2E act" and j["status"]=="running"))')" = 0 ] && break; sleep 2; done
check "jobs named after the routine" "$(c GET /api/jobs | py 'print(sorted({j["status"] for j in d if j.get("user")=="routine:E2E act"}))')" "['success']"
check "backup ran" "$(vm 'sudo find /srv/strata/main/e2e-rt-dst -name f | head -1')" "/f"
check "snapshot taken" "$(c GET /api/schedules | py 'print([len(s.get("history") or []) > 0 for s in d if s["id"]=="'"$SNAP"'"])')" "[True]"
check "container started" "$(vm 'sudo docker inspect -f {{.State.Running}} e2e-rt')" "true"
c PUT "/api/routines/$RT" -d '{"name":"E2E act","enabled":false,"trigger":"cpu","value":99,"actions":[{"kind":"docker","target":"e2e-rt","op":"stop"}]}' >/dev/null
c POST "/api/routines/$RT/fire" >/dev/null
for _ in $(seq 1 30); do [ "$(vm 'sudo docker inspect -f {{.State.Running}} e2e-rt')" = false ] && break; sleep 2; done
check "container stopped" "$(vm 'sudo docker inspect -f {{.State.Running}} e2e-rt')" "false"
check "event log names the routine" "$(c GET '/api/system/events' | grep -o 'Routine E2E act: [A-Za-z ]*' | head -1)" "Routine E2E act"
c DELETE "/api/routines/$RT" >/dev/null
c DELETE "/api/backups/$BK" >/dev/null
c DELETE "/api/schedules/$SNAP" >/dev/null
vm "sudo docker rm -f e2e-rt >/dev/null 2>&1; sudo rm -rf /srv/strata/main/Photos/e2e-rt-src /srv/strata/main/e2e-rt-dst"
