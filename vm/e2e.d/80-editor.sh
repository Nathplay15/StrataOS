# shellcheck shell=bash
# File editor: content + version, atomic save, conflicts, read-only shares,
# Range requests for the media players; OnlyOffice integration against a fake
# document server (python http.server in the VM). The real OnlyOffice image
# (about 3 GB) is installed only with E2E_ONLYOFFICE=1.
section "File editor and OnlyOffice"
EP=/srv/strata/main/Notes ERO=/srv/strata/main/NotesRO
check "share for the editor" "$(c POST /api/shares -d "{\"name\":\"Notes\",\"path\":\"$EP\",\"smb\":true,\"browseable\":true,\"permissions\":{\"$USER_NAME\":\"rw\"}}")" '"name":"Notes"'
check "read-only share" "$(c POST /api/shares -d "{\"name\":\"NotesRO\",\"path\":\"$ERO\",\"smb\":true,\"browseable\":true,\"permissions\":{\"$USER_NAME\":\"ro\"}}")" '"name":"NotesRO"'
vm "echo locked | sudo tee $ERO/ro.txt >/dev/null"

# put_as <cookie jar> <path> <query> <file>: PUT /api/files/content with a raw body.
put_as() { curl -s -b "$1" -X PUT -H 'X-Strata: 1' -H 'Content-Type: text/plain' --data-binary "@$4" "$B/api/files/content?path=$2$3"; }
etag() { u GET "/api/files/content?path=$1" | py 'print(d.get("etag",""))'; }

printf 'hello\n' > "$TMP/ed1.txt"
check "new text file created" "$(put_as "$J2" "$EP/notes.md" "&create=1" "$TMP/ed1.txt")" '"etag"'
check "owned by the user, like uploads" "$(vm "sudo stat -c %U $EP/notes.md")" "$USER_NAME"
check "share ACL inherited" "$(vm "sudo getfacl -p $EP/notes.md | grep ^user:$USER_NAME")" "user:$USER_NAME:rw"
check "create refuses an existing file" "$(put_as "$J2" "$EP/notes.md" "&create=1" "$TMP/ed1.txt")" '"conflict":true'
GOT=$(u GET "/api/files/content?path=$EP/notes.md")
check "content and version read" "$(echo "$GOT" | py 'print(d["content"].strip(), d["writable"], bool(d["etag"]), d["modTime"][:2])')" "hello True True 20"
vm "sudo chmod 640 $EP/notes.md"
E1=$(etag "$EP/notes.md")
printf '# Title\nsecond version\n' > "$TMP/ed2.txt"
SAVED=$(put_as "$J2" "$EP/notes.md" "&etag=$E1" "$TMP/ed2.txt")
check "saved with the loaded version" "$SAVED" '"etag"'
E2=$(echo "$SAVED" | py 'print(d["etag"])')
check "new version returned" "$([ "$E2" != "$E1" ] && [ "$E2" = "$(etag "$EP/notes.md")" ] && echo yes)" "yes"
check "content on disk" "$(vm "sudo cat $EP/notes.md")" "second version"
check "mode and owner kept, no temporary file" "$(vm "sudo stat -c '%U %a' $EP/notes.md; sudo ls -A $EP | grep -c strata-")" "$USER_NAME 640"
check "stale version: conflict" "$(put_as "$J2" "$EP/notes.md" "&etag=$E1" "$TMP/ed1.txt")" '"conflict":true'
check "conflict left the file alone" "$(vm "sudo cat $EP/notes.md")" "second version"
check "changed behind the editor: conflict" "$(vm "echo smb-edit | sudo tee -a $EP/notes.md >/dev/null"; put_as "$J2" "$EP/notes.md" "&etag=$E2" "$TMP/ed1.txt")" '"conflict":true'
check "overwrite after a conflict" "$(put_as "$J2" "$EP/notes.md" "&force=1" "$TMP/ed1.txt")" '"etag"'
check "read-only share: opened read-only" "$(u GET "/api/files/content?path=$ERO/ro.txt" | py 'print(d["writable"], d["readOnlyReason"])')" "False read-only"
check "read-only share: save refused" "$(put_as "$J2" "$ERO/ro.txt" "&force=1" "$TMP/ed1.txt")" "error"
check "read-only share: file untouched" "$(vm "sudo cat $ERO/ro.txt")" "locked"
check "path outside the storage refused" "$(put_as "$J2" "/etc/e2e-editor" "&create=1" "$TMP/ed1.txt")" "outside of the NAS storage"
check "traversal refused" "$(put_as "$J2" "$EP/../../../../etc/e2e-editor" "&create=1" "$TMP/ed1.txt"; vm "test -e /etc/e2e-editor && echo LEAK")" "error"
vm "sudo ln -sfn /etc $EP/etc-link"
check "symlink escape refused" "$(put_as "$J2" "$EP/etc-link/e2e-editor" "&create=1" "$TMP/ed1.txt"; vm "test -e /etc/e2e-editor && echo LEAK")" "error"
head -c $((5 * 1048576 + 10)) /dev/zero > "$TMP/big.txt"
check "over 5 MB refused" "$(put_as "$J2" "$EP/big.txt" "&create=1" "$TMP/big.txt")" "larger than 5 MB"

# Media players seek with Range requests.
vm "sudo dd if=/dev/urandom of=$EP/clip.mp4 bs=1M count=3 status=none"
check "Range request: 206" "$(curl -s -b "$J2" -r 1000-1999 -o /dev/null -w '%{http_code} %{size_download}' "$B/api/files/download?path=$EP/clip.mp4&inline=1")" "206 1000"
check "Range: content range and inline video" "$(curl -s -b "$J2" -r 0-99 -D - -o /dev/null "$B/api/files/download?path=$EP/clip.mp4&inline=1" | tr -d '\r' | grep -iE '^(content-range|content-type):' | sort | tr '\n' ' ')" "Content-Range: bytes 0-99/3145728 Content-Type: video/mp4"
check "HTML never served inline" "$(vm "echo '<script>x</script>' | sudo tee $EP/x.html >/dev/null"; curl -s -b "$J2" -D - -o /dev/null "$B/api/files/download?path=$EP/x.html&inline=1" | tr -d '\r' | grep -i '^content-type')" "application/octet-stream"

# ---- OnlyOffice with a fake document server ----
OOS=e2e-onlyoffice-secret-0123456789
vm "sudo mkdir -p /tmp/oo && printf 'edited-by-onlyoffice' | sudo tee /tmp/oo/output.docx >/dev/null && sudo systemd-run --unit=e2e-oo python3 -m http.server 18095 --bind 127.0.0.1 --directory /tmp/oo >/dev/null 2>&1"
check "office off by default" "$(u GET /api/office/status | py 'print(d["enabled"])')" "False"
check "config refused while off" "$(u GET "/api/office/config?path=$EP/notes.md")" "not enabled"
check "office settings refused to users" "$(u GET /api/office/settings)" "administrator"
check "invalid server URL refused" "$(c PUT /api/office/settings -d '{"enabled":true,"serverUrl":"javascript:alert(1)"}')" "invalid URL"
check "short secret refused" "$(c PUT /api/office/settings -d '{"enabled":true,"serverUrl":"http://127.0.0.1:18095","secret":"short"}')" "16 characters"
check "fake document server set" "$(c PUT /api/office/settings -d "{\"enabled\":true,\"serverUrl\":\"http://127.0.0.1:18095\",\"strataUrl\":\"http://127.0.0.1:7070\",\"secret\":\"$OOS\"}")" '"ok":true'
check "secret masked" "$(c GET /api/office/settings | py 'print(d["settings"]["secret"], d["ready"])')" "******** True"
check "masked secret kept on save" "$(c GET /api/office/settings | py 'import json; print(json.dumps(d["settings"]))' | c PUT /api/office/settings -d @- >/dev/null; c GET /api/office/settings | py 'print(d["ready"])')" "True"
check "status for users" "$(u GET /api/office/status | py 'print(d["enabled"], d["types"]["docx"])')" "True word"
vm "printf 'original-docx' | sudo tee $EP/report.docx $ERO/report.docx >/dev/null; sudo chown $USER_NAME $EP/report.docx"
CFG=$(u GET "/api/office/config?path=$EP/report.docx&lang=fr")
check "editor config: edit mode, callback" "$(echo "$CFG" | py 'c=d["config"]; print(c["documentType"], c["editorConfig"]["mode"], c["document"]["permissions"]["edit"], "callbackUrl" in c["editorConfig"], c["editorConfig"]["lang"])')" "word edit True True fr"
# The config token verifies with the shared secret.
check "config signed with the JWT secret (HS256)" "$(echo "$CFG" | OOS=$OOS python3 -c '
import sys,json,hmac,hashlib,base64,os
d=json.load(sys.stdin); t=d["config"]["token"]; h,p,s=t.split(".")
pad=lambda x: x+"="*(-len(x)%4)
ok=hmac.compare_digest(base64.urlsafe_b64decode(pad(s)), hmac.new(os.environ["OOS"].encode(), (h+"."+p).encode(), hashlib.sha256).digest())
body=json.loads(base64.urlsafe_b64decode(pad(p))); print(ok, json.loads(base64.urlsafe_b64decode(pad(h)))["alg"], body["document"]["key"]==d["config"]["document"]["key"])')" "True HS256 True"
check "same key for everyone on the same version" "$(c GET "/api/office/config?path=$EP/report.docx" | py 'print(d["config"]["document"]["key"])')" "$(echo "$CFG" | py 'print(d["config"]["document"]["key"])')"
DOCURL=$(echo "$CFG" | py 'print(d["config"]["document"]["url"].replace("http://127.0.0.1:7070", ""))')
CBURL=$(echo "$CFG" | py 'print(d["config"]["editorConfig"]["callbackUrl"].replace("http://127.0.0.1:7070", ""))')
KEY=$(echo "$CFG" | py 'print(d["config"]["document"]["key"])')
check "document server downloads with the signed URL (no session)" "$(curl -s "$B$DOCURL")" "original-docx"
check "tampered signed URL refused" "$(curl -s -o /dev/null -w '%{http_code}' "$B${DOCURL}x")" "403"
check "document URL cannot be used as a callback" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$B${DOCURL/file/callback}" -d '{}')" "403"
# jwtsign <json> [secret]: signs a callback like the document server.
jwtsign() { OOS=${2:-$OOS} python3 -c '
import sys,json,hmac,hashlib,base64,os
e=lambda b: base64.urlsafe_b64encode(b).rstrip(b"=").decode()
h=e(b"{\"alg\":\"HS256\",\"typ\":\"JWT\"}"); p=e(sys.argv[1].encode())
print(h+"."+p+"."+e(hmac.new(os.environ["OOS"].encode(), (h+"."+p).encode(), hashlib.sha256).digest()))' "$1"; }
cb() { curl -s -X POST -H 'Content-Type: application/json' "$B$CBURL" -d "{\"token\":\"$(jwtsign "$1" "${2:-}")\"}"; }
check "callback with another secret refused" "$(cb "{\"key\":\"$KEY\",\"status\":2,\"url\":\"http://127.0.0.1:18095/output.docx\"}" wrong-secret-0123456789)" "signature"
check "unsigned callback refused" "$(curl -s -X POST "$B$CBURL" -d "{\"key\":\"$KEY\",\"status\":2,\"url\":\"http://127.0.0.1:18095/output.docx\"}")" "signature"
check "other document key refused" "$(cb '{"key":"other","status":2,"url":"http://127.0.0.1:18095/output.docx"}')" "key mismatch"
check "status 1 (editing) acknowledged" "$(cb "{\"key\":\"$KEY\",\"status\":1}")" '"error":0'
check "status 4 (closed, no change) leaves the file" "$(cb "{\"key\":\"$KEY\",\"status\":4}"; vm "sudo cat $EP/report.docx")" "original-docx"
# The URL names another host: StrataOS still fetches from the document server only.
check "status 2 saves from the document server only" "$(cb "{\"key\":\"$KEY\",\"status\":2,\"url\":\"http://169.254.169.254/output.docx\",\"filetype\":\"docx\"}")" '"error":0'
check "edited document saved in place, owner kept" "$(vm "sudo cat $EP/report.docx; echo; sudo stat -c %U $EP/report.docx")" "edited-by-onlyoffice
$USER_NAME"
check "save logged" "$(c GET /api/system/events | py 'print(any("(OnlyOffice)" in e["message"] and "report.docx" in e["message"] for e in d))')" "True"
check "force save (6) again on the same session" "$(vm "printf 'second-save' | sudo tee /tmp/oo/output.docx >/dev/null"; cb "{\"key\":\"$KEY\",\"status\":6,\"url\":\"http://x/output.docx\"}"; vm "sudo cat $EP/report.docx")" "second-save"
check "missing file on the document server: error reported" "$(cb "{\"key\":\"$KEY\",\"status\":2,\"url\":\"http://x/nothing.docx\"}")" '"error":1'
check "read-only share: view mode, no callback" "$(u GET "/api/office/config?path=$ERO/report.docx" | py 'c=d["config"]; print(c["editorConfig"]["mode"], c["document"]["permissions"]["edit"], "callbackUrl" in c["editorConfig"])')" "view False False"
check "unsupported type refused" "$(u GET "/api/office/config?path=$EP/notes.md")" "cannot open"

if [ "${E2E_ONLYOFFICE:-}" = 1 ]; then
  # Real OnlyOffice Docs from the App Center (downloads about 3 GB).
  c PUT /api/office/settings -d '{"enabled":true}' >/dev/null
  check "OnlyOffice app installed" "$(job "$(c POST /api/apps/onlyoffice/install -d "{\"values\":{\"JWT_SECRET\":\"$OOS\"}}")")" "success"
  for _ in $(seq 1 120); do [ "$(vm "curl -s http://127.0.0.1:8095/healthcheck")" = true ] && break; sleep 5; done
  check "document server healthy" "$(c POST /api/office/check)" '"ok":true'
  check "app settings picked up (port, secret)" "$(c GET /api/office/settings | py 'print(d["installed"], d["port"], d["appSecret"], d["ready"])')" "True 8095 True True"
  check "editor script served" "$(vm "curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8095/web-apps/apps/api/documents/api.js")" "200"
  DU=$(u GET "/api/office/config?path=$EP/report.docx" | py 'print(d["config"]["document"]["url"])')
  CT=$(vm "sudo docker ps -q --filter label=com.docker.compose.project=onlyoffice | head -1" | tr -d '\r')
  check "container reaches StrataOS through host.docker.internal" "$(vm "sudo docker exec $CT curl -s '$DU'")" "second-save"
  c DELETE "/api/docker/stacks/onlyoffice?volumes=1" >/dev/null
  vm "sudo rm -rf /srv/strata/apps/onlyoffice"
fi

c PUT /api/office/settings -d '{}' >/dev/null
vm "sudo systemctl stop e2e-oo; sudo rm -rf /tmp/oo"
c DELETE /api/shares/Notes >/dev/null
c DELETE /api/shares/NotesRO >/dev/null
vm "sudo rm -rf $EP $ERO"
