# shellcheck shell=bash
# Stopping running tasks (0.11.1): a restic backup, an rsync copy and a cloud
# sync are stopped from the API; their jobs end "cancelled" (not failed),
# the history says "stopped", no rclone/restic/rsync process is left and the
# restic repository is unlocked. Updates cannot be stopped.
section "Stopping running tasks"

JCSRC=/srv/strata/main/e2e-cancel-src
JCREPO=/srv/strata/main/e2e-cancel-repo
JCPASS=Cancel-Test-Pass
# Incompressible data, so that restic and a throttled rsync take a while.
vm "sudo mkdir -p $JCSRC && for i in 1 2 3 4; do sudo dd if=/dev/urandom of=$JCSRC/big\$i bs=1M count=200 status=none; done"

# jc_pat <pattern>: the pattern with its first letter in brackets, so that
# it does not match the shell running pgrep over SSH.
jc_pat() { echo "[${1:0:1}]${1:1}"; }
# jc_wait_proc <pattern>: waits until a process matching it runs.
jc_wait_proc() { for _ in $(seq 1 60); do vm "pgrep -f '$(jc_pat "$1")' >/dev/null" && return 0; sleep 0.5; done; return 1; }
# jc_stop <job json> <process pattern> <label>
jc_stop() {
  local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null)
  check "$3: job cancellable" "$(echo "$1" | py 'print(d.get("cancellable"))')" "True"
  jc_wait_proc "$2" || ko "$3: process started" "no process matching $2"
  check "$3: stop accepted" "$(c POST /api/jobs/$id/cancel -o /dev/null -w '%{http_code}')" "200"
  check "$3: job cancelled" "$(job "{\"id\":\"$id\"}")" "cancelled"
  check "$3: stopped by the administrator" "$(c GET /api/jobs/$id | py 'print(d.get("cancelledBy",""), "Stopped by" in d.get("log",""))')" "$ADMIN_USER True"
  sleep 1
  check "$3: no process left" "$(vm "pgrep -f '$(jc_pat "$2")' | wc -l")" "0"
  check "$3: second stop refused (finished)" "$(c POST /api/jobs/$id/cancel -o /dev/null -w '%{http_code}')" "409"
}

# restic, local repository.
JCR=$(c POST /api/backups -d "{\"name\":\"e2e-cancel-restic\",\"kind\":\"restic\",\"sources\":[\"$JCSRC\"],\"destType\":\"local\",\"destPath\":\"$JCREPO\",\"password\":\"$JCPASS\",\"cron\":\"\",\"enabled\":true}" | py 'print(d["id"])')
J=$(c POST /api/backups/$JCR/run)
check "running backup exposes its job" "$(sleep 1; c GET /api/backups | py "print([b.get('jobId','') for b in d if b['id']=='$JCR'][0] == '$(echo "$J" | py 'print(d["id"])')')")" "True"
jc_stop "$J" "restic.*backup" "restic backup"
check "restic backup: history says stopped" "$(c GET /api/backups | py "h=[b['history'][0] for b in d if b['id']=='$JCR'][0]; print(h['status'], h['message'])")" "stopped Stopped by $ADMIN_USER"
check "restic repository unlocked" "$(vm "sudo RESTIC_PASSWORD=$JCPASS restic -r $JCREPO list locks --no-lock 2>&1 | wc -l")" "0"
check "restic repository still usable" "$(job "$(c POST /api/backups/$JCR/run)")" "success"
c DELETE /api/backups/$JCR >/dev/null

# rsync, throttled.
JCY=$(c POST /api/backups -d "{\"name\":\"e2e-cancel-rsync\",\"kind\":\"rsync\",\"sources\":[\"$JCSRC\"],\"destType\":\"local\",\"destPath\":\"/srv/strata/main/e2e-cancel-copy\",\"bwLimit\":\"2M\",\"cron\":\"\",\"enabled\":true}" | py 'print(d["id"])')
jc_stop "$(c POST /api/backups/$JCY/run)" "rsync .*e2e-cancel" "rsync copy"
check "rsync copy: history says stopped" "$(c GET /api/backups | py "print([b['history'][0]['status'] for b in d if b['id']=='$JCY'][0])")" "stopped"
c DELETE /api/backups/$JCY >/dev/null

# Cloud sync to a local WebDAV server, throttled.
JCDAV=$(head -c 12 /dev/urandom | base64 | tr -d '/+=')
vm "sudo mkdir -p /tmp/e2e-jc-dav && sudo systemd-run --unit=e2e-jc-dav rclone serve webdav /tmp/e2e-jc-dav --addr 127.0.0.1:18089 --user dav --pass '$JCDAV' >/dev/null 2>&1; sleep 1"
JCC=$(c POST /api/cloudsync -d "{\"name\":\"e2e-cancel-cloud\",\"provider\":\"webdav\",\"params\":{\"url\":\"http://127.0.0.1:18089\",\"vendor\":\"other\",\"user\":\"dav\"},\"secrets\":{\"pass\":\"$JCDAV\"},\"localPath\":\"$JCSRC\",\"remotePath\":\"up\",\"direction\":\"upload\",\"bwLimit\":\"2M\",\"cron\":\"\",\"enabled\":true}" | py 'print(d["id"])')
jc_stop "$(c POST /api/cloudsync/$JCC/run)" "rclone.*copy" "cloud sync"
check "cloud sync: history says stopped" "$(c GET /api/cloudsync | py "print([t['history'][0]['status'] for t in d['tasks'] if t['id']=='$JCC'][0])")" "stopped"
c DELETE /api/cloudsync/$JCC >/dev/null
vm "sudo systemctl stop e2e-jc-dav 2>/dev/null; sudo rm -rf /tmp/e2e-jc-dav"

# Updates refuse to stop.
J=$(c POST /api/system/updates/refresh)
check "update check: not cancellable" "$(echo "$J" | py 'print(d.get("cancellable"), bool(d.get("cancelReason")))')" "False True"
check "update check: stop refused" "$(c POST /api/jobs/$(echo "$J" | py 'print(d["id"])')/cancel -o /dev/null -w '%{http_code}')" "409"
job "$J" >/dev/null
check "unknown job: 404" "$(c POST /api/jobs/nope/cancel -o /dev/null -w '%{http_code}')" "404"

vm "sudo rm -rf $JCSRC $JCREPO /srv/strata/main/e2e-cancel-copy"
