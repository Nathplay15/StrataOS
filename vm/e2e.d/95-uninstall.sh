# Uninstalling components and apps: light components really removed (rclone,
# FTP, DLNA) and installed again at the end, shared packages kept, blockers
# (Docker with apps, ZFS with pool tank: never really uninstalled here), and
# App Center apps uninstalled with and without their data.
section "Uninstall components and apps"

pkg() { vm "dpkg-query -W -f '\${db:Status-Abbrev}' $1 2>/dev/null | grep -q '^ii' && echo present || echo absent"; }
plan() { c GET "/api/system/components/$1/uninstall"; }

check "unknown component refused" "$(plan nope)" "cannot be uninstalled"

# ---- rclone: shared and StrataOS packages are kept ----
check "rclone plan: rclone removed" "$(plan rclone | py 'print(d["installed"], d["packages"])')" "True ['rclone']"
check "rclone plan: fuse3 and ssh client kept" "$(plan rclone | py 'print(sorted((k["package"], k["reason"]) for k in d["kept"]))')" "('fuse3', 'Needed by StrataOS'), ('openssh-client', 'Needed by StrataOS')"
DAV='"provider":"webdav","params":{"url":"http://127.0.0.1:8088","vendor":"owncloud","user":"dav"}'
SYNC=$(c POST /api/cloudsync -d "{\"name\":\"e2e-un\",$DAV,\"secrets\":{\"pass\":\"x\"},\"localPath\":\"/srv/strata/main/Photos\",\"remotePath\":\"un\",\"direction\":\"upload\",\"cron\":\"\",\"enabled\":true}" | py 'print(d["id"])')
check "a cloud sync blocks rclone" "$(plan rclone | py 'print([(b["message"], b["force"]) for b in d["blockers"]])')" "e2e-un"
check "uninstall refused without force" "$(c POST /api/system/components/rclone/uninstall -d '{}')" "cannot be uninstalled: Cloud syncs"
c DELETE "/api/cloudsync/$SYNC" >/dev/null
if [ "$(plan rclone | py 'print(len(d["blockers"]))')" = 0 ]; then
  check "rclone uninstalled" "$(job "$(c POST /api/system/components/rclone/uninstall -d '{}')")" "success"
  check "rclone package gone" "$(pkg rclone)" "absent"
  check "fuse3 kept (encrypted folders)" "$(pkg fuse3)" "present"
  check "rclone no longer installed" "$(c GET /api/system/components | py 'print(d["rclone"])')" "False"
  check "uninstall event" "$(c GET /api/system/events | py '[print(e["message"]) for e in d[:20]]')" "Component Cloud sync (rclone) uninstalled"
  check "rclone installed again" "$(job "$(c POST /api/system/components/rclone/install)")" "success"
else
  ok "rclone still used by other tasks: not uninstalled"
fi

# ---- FTP: the server is turned off, its files and package go ----
FS=$(c GET /api/fileservices | py 'print(json.dumps(d["config"]))')
c PUT /api/fileservices -d "$(echo "$FS" | py 'd["ftp"]=True; print(json.dumps(d))')" >/dev/null
check "FTP on blocks, can be forced" "$(plan ftp | py 'print([(b["message"], b["force"]) for b in d["blockers"]])')" "('The FTP server is on', True)"
check "FTP plan: files removed" "$(plan ftp | py 'print(d["packages"], "/etc/strata/ftp-users" in d["removed"])')" "['vsftpd'] True"
check "FTP refused without force" "$(c POST /api/system/components/ftp/uninstall -d '{}')" "The FTP server is on"
check "FTP uninstalled (forced)" "$(job "$(c POST /api/system/components/ftp/uninstall -d '{"force":true}')")" "success"
check "vsftpd gone" "$(pkg vsftpd)" "absent"
check "FTP files removed" "$(vm "ls /etc/strata/ftp-users /etc/pam.d/strata-ftp /etc/vsftpd.conf 2>&1 | grep -c 'No such'")" "3"
check "nothing listens on port 21" "$(vm "ss -ltnH 'sport = :21' | wc -l")" "0"
check "FTP setting off" "$(c GET /api/fileservices | py 'print(d["config"]["ftp"], d["ftpInstalled"])')" "False False"
check "FTP installed again" "$(job "$(c POST /api/system/components/ftp/install)")" "success"
c PUT /api/fileservices -d "$FS" >/dev/null

# ---- DLNA: StrataOS unit, configuration and cache removed ----
DL=$(c GET /api/media/dlna | py 'print(json.dumps(d["config"]))')
c PUT /api/media/dlna -d '{"enabled":true,"name":"E2E Un","folders":[{"share":"Media"}]}' >/dev/null
check "DLNA plan" "$(plan dlna | py 'print(d["packages"], "strata-minidlna.service" in d["services"], "/var/cache/strata-minidlna" in d["removed"], d["blockers"][0]["force"])')" "['minidlna'] True True True"
check "DLNA uninstalled (forced)" "$(job "$(c POST /api/system/components/dlna/uninstall -d '{"force":true}')")" "success"
check "minidlna gone" "$(pkg minidlna)" "absent"
check "StrataOS unit and files gone" "$(vm "ls /etc/systemd/system/strata-minidlna.service /etc/strata-minidlna.conf /var/cache/strata-minidlna 2>&1 | grep -c 'No such'")" "3"
check "DLNA service gone" "$(vm "systemctl is-active strata-minidlna.service 2>&1")" "inactive"
check "DLNA setting off" "$(c GET /api/media/dlna | py 'print(d["config"].get("enabled", False))')" "False"
check "DLNA installed again" "$(job "$(c POST /api/system/components/dlna/install)")" "success"
c PUT /api/media/dlna -d "$DL" >/dev/null

# ---- Surveillance recorder: ffmpeg stays while the media tools need it ----
check "photos tools present" "$(c GET /api/system/components | py 'print(d["photos"], d["nvr"])')" "True True"
check "recorder plan keeps ffmpeg" "$(plan nvr | py 'print(d["packages"], [(k["package"], k["reason"]) for k in d["kept"]])')" "[] [('ffmpeg', 'Used by Media tools (ffmpeg, libvips)')]"
if [ "$(plan nvr | py 'print(len(d["blockers"]))')" = 0 ]; then
  check "recorder uninstalled" "$(job "$(c POST /api/system/components/nvr/uninstall -d '{}')")" "success"
  check "ffmpeg still there" "$(pkg ffmpeg)" "present"
fi
check "document previews plan" "$(plan docpreview | py 'print("libreoffice-core-nogui" in d["packages"], d["blockers"])')" "True []"

# ---- Blockers that cannot be forced ----
check "Docker blocked by the apps" "$(plan docker | py 'print(d["blocked"], d["blockers"][0]["message"])')" "True Installed apps and stacks:"
check "Docker blocker lists Uptime Kuma" "$(plan docker | py 'print(d["blockers"][0]["message"])')" "uptime"
check "Docker refused even forced" "$(c POST /api/system/components/docker/uninstall -d '{"force":true}')" "Docker cannot be uninstalled: Installed apps and stacks"
check "ZFS refused while tank exists" "$(c POST /api/system/components/zfs/uninstall -d '{"force":true}')" "Imported ZFS pools: tank"
check "Docker still running" "$(vm "systemctl is-active docker")" "active"
check "ZFS still there" "$(vm "sudo zpool list -H -o name")" "tank"

# ---- App Center apps ----
check "catalog app plan lists its data folder" "$(c GET /api/apps/uptime/uninstall | py 'print(d["app"], d["folders"])')" "uptimekuma ['/srv/strata/main/apps/uptime-kuma']"
# Two custom apps with a folder of their own each.
un_app() { # un_app <stack> <port>
  vm "sudo mkdir -p /srv/strata/apps/$1/data && echo keep | sudo tee /srv/strata/apps/$1/data/file >/dev/null"
  local yml=$'services:\n  whoami:\n    image: traefik/whoami:latest\n    ports:\n      - "'"$2"$':80"\n    volumes:\n      - /srv/strata/apps/'"$1"$'/data:/data\n      - /srv/strata/tank/media:/media:ro\n'
  YML="$yml" python3 -c 'import json,os,sys; print(json.dumps({"stack":sys.argv[1],"name":sys.argv[1],"compose":os.environ["YML"]}))' "$1"
}
check "custom app 1 installed" "$(job "$(c POST /api/apps/custom -d "$(un_app e2e-un1 8093)")")" "success"
check "custom app 2 installed" "$(job "$(c POST /api/apps/custom -d "$(un_app e2e-un2 8094)")")" "success"
check "app plan: own folder deleted, share kept" "$(c GET /api/apps/e2e-un1/uninstall | py 'print(d["folders"], [(k["path"], k["reason"]) for k in d["kept"]], d["images"])')" "['/srv/strata/apps/e2e-un1/data'] [('/srv/strata/tank/media', 'In shared folder Media')] ['traefik/whoami:latest']"
check "app 1 uninstalled, data kept" "$(job "$(c POST /api/apps/e2e-un1/uninstall -d '{"deleteData":false,"removeImages":true}')")" "success"
check "app 1 stack and containers gone" "$(echo $(c GET /api/docker/stacks | py 'print([s["name"] for s in d if s["name"]=="e2e-un1"])'; vm "sudo docker ps -aq --filter label=com.docker.compose.project=e2e-un1 | wc -l"))" "[] 0"
check "app 1 network gone" "$(vm "sudo docker network ls --format '{{.Name}}' | grep -c e2e-un1")" "0"
check "app 1 data kept" "$(vm "cat /srv/strata/apps/e2e-un1/data/file")" "keep"
check "image kept while app 2 uses it" "$(vm "sudo docker image inspect -f ok traefik/whoami:latest")" "ok"
check "app 2 uninstalled with its data" "$(job "$(c POST /api/apps/e2e-un2/uninstall -d '{"deleteData":true,"removeImages":true}')")" "success"
check "app 2 folder deleted" "$(vm "ls -d /srv/strata/apps/e2e-un2/data 2>&1")" "No such file"
check "shared folder untouched" "$(vm "ls -d /srv/strata/tank/media")" "/srv/strata/tank/media"
check "unused image removed" "$(vm "sudo docker image inspect traefik/whoami:latest >/dev/null 2>&1 && echo present || echo absent")" "absent"
check "app uninstall events" "$(c GET /api/system/events | py '[print(e["message"]) for e in d[:20]]')" "App e2e-un2 uninstalled with its data"
check "unknown app" "$(c GET /api/apps/e2e-nope/uninstall)" "no app or stack named"
vm "sudo rm -rf /srv/strata/apps/e2e-un1 /srv/strata/apps/e2e-un2"
