# shellcheck shell=bash
# Folder picker of the backup, cloud sync, restore and datastore editors:
# the API calls it makes to create a folder or a shared folder on the spot
# (with the defaults of the Shares page), then a backup into the new share.
section "Folder picker: new folder and new shared folder from an editor"

check "picker lists the volume" "$(c GET '/api/files?path=/srv/strata/main' | py 'print("ok" if any(e["dir"] for e in d["entries"]) else d)')" "ok"
check "new folder" "$(c POST /api/files/mkdir -d '{"path":"/srv/strata/main/picker-e2e"}'; c GET '/api/files?path=/srv/strata/main' | py 'print([e["name"] for e in d["entries"] if e["name"]=="picker-e2e"])')" "picker-e2e"
check "new sub-folder" "$(c POST /api/files/mkdir -d '{"path":"/srv/strata/main/picker-e2e/sub"}'; vm "sudo test -d /srv/strata/main/picker-e2e/sub && echo yes")" "yes"
check "new folder outside the storage refused" "$(c POST /api/files/mkdir -d '{"path":"/srv/strata/main/../../../etc/picker-e2e"}'; vm "sudo test -e /etc/picker-e2e && echo created || echo absent")" "absent"

# "New shared folder": name, volume, the creator writes and the others read.
check "new shared folder" "$(c POST /api/shares -d "{\"name\":\"PickerBk\",\"path\":\"/srv/strata/main/PickerBk\",\"description\":\"e2e\",\"smb\":true,\"nfs\":false,\"guest\":false,\"browseable\":true,\"recycleBin\":true,\"timeMachine\":false,\"permissions\":{\"$ADMIN_USER\":\"rw\",\"@strata\":\"ro\"},\"nfsClients\":\"\",\"nfsOptions\":\"\"}" | py 'print("error: "+str(d) if isinstance(d,dict) and d.get("error") else "created")')" "created"
check "share defaults" "$(c GET /api/shares | py "s=[x for x in d if x['name']=='PickerBk'][0]; print(s['smb'], s['recycleBin'], s['permissions'].get('$ADMIN_USER'), s['permissions'].get('@strata'))")" "True True rw ro"
check "share folder created" "$(vm "sudo test -d /srv/strata/main/PickerBk && echo yes")" "yes"
check "share in smb.conf" "$(vm "sudo testparm -s 2>/dev/null | grep -c '^\[PickerBk\]'")" "1"
check "new share offered as a backup source" "$(c GET /api/backups/sources | py 'print([f["label"] for f in d["folders"] if f["path"]=="/srv/strata/main/PickerBk"])')" "Share PickerBk"

# A backup whose destination is a folder of the new share.
vm "sudo mkdir -p /srv/strata/main/picker-e2e/sub && echo picked | sudo tee /srv/strata/main/picker-e2e/sub/p.txt >/dev/null"
FPB=$(c POST /api/backups -d '{"name":"picker-backup","kind":"rsync","sources":["/srv/strata/main/picker-e2e"],"destType":"local","destPath":"/srv/strata/main/PickerBk/backups","cron":"","enabled":true}' | py 'print(d.get("id",d))')
check "backup into the new share" "$(job "$(c POST /api/backups/$FPB/run)")" "success"
check "backed-up file in the share" "$(vm "sudo cat /srv/strata/main/PickerBk/backups/picker-e2e/sub/p.txt")" "picked"
c DELETE /api/backups/$FPB >/dev/null

# Cloud sync: a picked folder (inside the new share, not a share root) is accepted.
FPC=$(c POST /api/cloudsync -d '{"name":"picker-cloud","provider":"webdav","params":{"url":"https://dav.invalid/remote.php/dav/files/x","vendor":"other"},"secrets":{},"localPath":"/srv/strata/main/PickerBk/cloud","remotePath":"nas","direction":"download","mirror":false,"cron":"","enabled":false}' | py 'print(d.get("id",""), d.get("localPath", d))')
check "cloud sync accepts a picked folder" "$FPC" "/srv/strata/main/PickerBk/cloud"
[ -n "${FPC%% *}" ] && c DELETE "/api/cloudsync/${FPC%% *}" >/dev/null

c DELETE /api/shares/PickerBk >/dev/null
check "share removed (files kept)" "$(c GET /api/shares | py 'print([x["name"] for x in d if x["name"]=="PickerBk"])'; vm "sudo test -d /srv/strata/main/PickerBk && echo kept")" "[] kept"
vm "sudo rm -rf /srv/strata/main/PickerBk /srv/strata/main/picker-e2e"
