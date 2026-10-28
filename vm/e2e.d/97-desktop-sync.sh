# shellcheck shell=bash
# Desktop sync app (desktop/) against the sync API, headless: pairing with
# the one-time code of the web interface (pinned certificate), two-way
# changes, a change over SMB and one from the web file manager, a conflict,
# deletions to the NAS recycle bin and to the local trash, renames without
# transfers, the deletion brake, the change feed (long poll, conditional
# uploads, read-only shares) and revocation. The client runs on the host
# (built here with Go: desktop/go.mod) and reaches the VM through its
# forwarded HTTPS port (VM_HTTPS_PORT, 7443 by default).
section "Desktop sync app"
SP="$P/SyncTest"
HTTPS="https://localhost:${VM_HTTPS_PORT:-7443}"
SC="$TMP/sync-config" L="$TMP/sync-local"
export XDG_DATA_HOME="$TMP/xdg" # the local trash of the test
GO=$(command -v go || echo "$HOME/.local/opt/go/bin/go")
S="$TMP/strataos-sync"
(cd desktop && CGO_ENABLED=0 "$GO" build -o "$S" ./cmd/strataos-sync) 2>&1 | tail -3
check "client built" "$("$S" --version 2>&1)" "strataos-sync"
sync1() { # a hang dumps the goroutines (kept in SYNC_E2E_LOG when set)
  local out rc; out=$(timeout -s QUIT 240 "$S" --config "$SC" sync-once "$@" 2>&1); rc=$?
  printf "%s\n" "$out" | tee -a "${SYNC_E2E_LOG:-/dev/null}"; return $rc
}
bt() { local m=$1 p=$2; shift 2; curl -sk -X "$m" -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' "$HTTPS$p" "$@"; }
web_put() { # web_put <folder> <file>: upload through the web file manager
  curl -s -b "$J2" -H 'X-Strata: 1' -F "file=@$2" "$B/api/files/upload?path=$1" >/dev/null
}

# Pairing: the code and fingerprint the web interface shows.
PR=$(u POST /api/me/devices/pairing)
PC=$(echo "$PR" | py 'print(d["code"])')
FP=$(echo "$PR" | py 'print(d["fingerprint"])')
check "a wrong fingerprint is refused" "$("$S" --config "$SC" pair --server "$HTTPS" --code "$PC" --fingerprint "$(echo "$FP" | tr '0-9A-F' 'A-F0-9')" 2>&1)" "pairing refused"
GROUPED=$(echo "$PC" | sed 's/.\{4\}/&-/g;s/-$//')
check "paired with the code (typed grouped)" "$("$S" --config "$SC" pair --server "$HTTPS" --code "$GROUPED" --fingerprint "$FP" --name "e2e-laptop" 2>&1)" "paired with"
check "token kept readable by the user only" "$(stat -c %a "$SC/config.json")" "600"
TOK=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['token'])" "$SC/config.json")
check "listed as a desktop device with its system" "$(u GET /api/me/devices | py 'print([(x["name"], x["kind"]) for x in d if x["kind"]=="desktop"])')" "('e2e-laptop', 'desktop')"
check "administrators see it" "$(c GET /api/devices | py 'print([x["user"] for x in d if x["name"]=="e2e-laptop"])')" "$USER_NAME"
check "device token cannot list the devices of others" "$(bt GET /api/devices -o /dev/null -w '%{http_code}')" "403"

# First synchronization: files on both sides, merged.
u POST /api/files/mkdir -d "{\"path\":\"$SP\"}" >/dev/null
echo "from the NAS" > "$TMP/nas.txt"
web_put "$SP" "$TMP/nas.txt"
mkdir -p "$L/docs"
echo "from the computer" > "$L/docs/local.txt"
echo "dotfile" > "$L/.config-x"
echo "junk" > "$L/.DS_Store"
check "folder added" "$("$S" --config "$SC" add --local "$L" --remote "$SP" >/dev/null 2>&1 && "$S" --config "$SC" list)" "$L  <->  $SP"
check "outside the shares refused" "$("$S" --config "$SC" add --local "$TMP/other" --remote /srv/strata/main 2>&1)" "shared folder"
check "first sync" "$(sync1)" "idle"
check "NAS file downloaded" "$(cat "$L/nas.txt")" "from the NAS"
check "local files uploaded, ignored ones left out" "$(vm "sudo ls -A $SP | tr '\n' ' '"; vm "sudo ls -A $SP/docs")" ".config-x docs nas.txt local.txt"
check "uploaded file owned by the user" "$(vm "sudo stat -c %U $SP/docs/local.txt")" "$USER_NAME"
check "nothing more to do" "$(sync1 && sync1)" "idle"

# Changes on each side, over SMB and through the web interface.
echo "edited here" > "$L/docs/local.txt"
vm "echo 'over SMB' > /tmp/smb.txt; smbclient //localhost/Photos -U '$USER_NAME%$USER_PASS' -c 'cd SyncTest; put /tmp/smb.txt smb.txt' >/dev/null 2>&1"
echo "web upload" > "$TMP/web.txt"
web_put "$SP/docs" "$TMP/web.txt"
sync1 >/dev/null
check "local change on the NAS" "$(vm "sudo cat $SP/docs/local.txt")" "edited here"
check "SMB change reached the computer" "$(cat "$L/smb.txt" 2>&1)" "over SMB"
check "web upload reached the computer" "$(cat "$L/docs/web.txt" 2>&1)" "web upload"

# The change feed: a long poll wakes up on a change.
CUR=$(bt GET "/api/sync/changes?folder=$SP&cursor=" | py 'print(d["cursor"])')
check "nothing after the cursor" "$(bt GET "/api/sync/changes?folder=$SP&cursor=$CUR" | py 'print(len(d["changes"]), d["reset"])')" "0 False"
( sleep 2; vm "echo late | sudo -u $USER_NAME tee $SP/late.txt >/dev/null" ) &
T0=$(date +%s)
LP=$(bt GET "/api/sync/changes?folder=$SP&cursor=$CUR&wait=30")
check "long poll answered by the change" "$(echo "$LP" | py 'print([c["path"] for c in d["changes"]], len(d["changes"][0]["version"])>0)') $(( $(date +%s) - T0 < 15 ))" "['late.txt'] True 1"
wait
check "unknown cursor: full listing" "$(bt GET "/api/sync/changes?folder=$SP&cursor=zzz:1" | py 'print(d["reset"], len(d["changes"])>3)')" "True True"
check "hash of a file" "$(bt POST /api/sync/hashes -d "{\"folder\":\"$SP\",\"paths\":[\"nas.txt\"]}" | py 'print(d["hashes"]["nas.txt"])')" "$(sha256sum "$TMP/nas.txt" | cut -d' ' -f1)"
V=$(bt GET "/api/sync/changes?folder=$SP" | py 'print([c["version"] for c in d["changes"] if c["path"]=="nas.txt"][0])')
check "download with Range and version" "$(bt GET "/api/sync/file?folder=$SP&path=nas.txt" -H 'Range: bytes=5-' -D - | tr -d '\r' | grep -E '^(HTTP|X-Strata-Version)' | tr '\n' ' ')" "206"
check "conditional upload: a stale version is a conflict" "$(bt POST /api/mobile/uploads -d "{\"folder\":\"$SP\",\"name\":\"nas.txt\",\"size\":0,\"sync\":true,\"replace\":true,\"ifMatch\":\"1-2-3\"}" -o /dev/null -w '%{http_code}')" "412"
check "conditional delete: a stale version is a conflict" "$(bt POST /api/sync/delete -d "{\"folder\":\"$SP\",\"path\":\"nas.txt\",\"ifMatch\":\"1-2-3\"}" | py 'print(d.get("conflict"), d["current"]["version"]=="'"$V"'")')" "True True"
check "path escape refused" "$(bt POST /api/sync/mkdir -d "{\"folder\":\"$SP\",\"path\":\"../../x\"}")" "invalid file name"
check "share without access refused" "$(bt POST /api/sync/mkdir -d '{"folder":"/srv/strata/tank/media","path":"x"}')" "access denied"
c POST /api/shares -d "{\"name\":\"SyncRO\",\"path\":\"/srv/strata/main/SyncRO\",\"smb\":false,\"permissions\":{\"$USER_NAME\":\"ro\"}}" >/dev/null
check "read-only share: listed, clear error on writes" "$(bt GET /api/sync/shares | py 'print([s["writable"] for s in d if s["name"]=="SyncRO"])') $(bt POST /api/sync/mkdir -d '{"folder":"/srv/strata/main/SyncRO","path":"x"}')" "[False] {\"error\":\"read-only"
c DELETE /api/shares/SyncRO >/dev/null
vm "sudo rm -rf /srv/strata/main/SyncRO"
sync1 >/dev/null

# Conflict: both sides changed the same file.
echo "my version" > "$L/nas.txt"
echo "their version" > "$TMP/nas.txt"
web_put "$SP" "$TMP/nas.txt"
check "conflict reported" "$(sync1)" "1 conflict"
CF=$(ls "$L" | grep 'nas (conflict from e2e-laptop ')
check "NAS version keeps the name" "$(cat "$L/nas.txt")" "their version"
check "local version kept as a conflict copy" "$(cat "$L/$CF" 2>&1)" "my version"
sync1 >/dev/null
check "conflict copy on the NAS too" "$(vm "sudo cat '$SP/$CF'")" "my version"

# Renames without transfers, both ways.
mv "$L/docs" "$L/papers"
sync1 >/dev/null
check "local folder rename done on the NAS" "$(vm "sudo ls $SP/papers | tr '\n' ' '")" "local.txt web.txt"
check "nothing deleted by the rename" "$(vm "sudo ls $P/.recycle/$USER_NAME/SyncTest 2>/dev/null | grep -c papers")" "0"
u POST /api/files/rename -d "{\"from\":\"$SP/smb.txt\",\"to\":\"$SP/renamed-on-nas.txt\"}" >/dev/null
sync1 >/dev/null
check "NAS rename done locally" "$(ls "$L" | grep -c 'smb.txt\|renamed-on-nas.txt') $(cat "$L/renamed-on-nas.txt" 2>&1)" "1 over SMB"

# Deletions: to the recycle bin of the share, and to the local trash.
rm "$L/papers/local.txt"
u POST /api/files/delete -d "{\"paths\":[\"$SP/late.txt\"]}" >/dev/null
sync1 >/dev/null
check "local deletion: in the NAS recycle bin" "$(vm "sudo ls $P/.recycle/$USER_NAME/SyncTest/papers 2>&1")" "local.txt"
check "NAS deletion: in the local trash" "$(ls "$XDG_DATA_HOME/Trash/files" 2>&1; test -e "$L/late.txt" && echo still-there)" "late.txt"

# The deletion brake: most files gone on the NAS at once.
mkdir -p "$L/many"
for i in $(seq 1 30); do echo "$i" > "$L/many/f$i.txt"; done
sync1 >/dev/null
DEL=$(for i in $(seq 1 30); do printf '"%s/many/f%d.txt",' "$SP" "$i"; done)
u POST /api/files/delete -d "{\"paths\":[${DEL%,}]}" >/dev/null
OUT=$(sync1); RC=$?
check "brake holds the deletions" "$RC $(ls "$L/many" | wc -l)" "3 30"
check "brake explained" "$OUT" "deletions held"
sync1 --confirm-deletes >/dev/null
check "deletions applied once confirmed" "$(ls "$L/many" 2>/dev/null | wc -l)" "0"

# Revoked in the web interface: the client stops.
DID=$(u GET /api/me/devices | py 'print([x["id"] for x in d if x["name"]=="e2e-laptop"][-1])')
u DELETE "/api/me/devices/$DID" >/dev/null
check "revoked computer stops" "$(sync1)" "no longer paired"
check "its files stay" "$(cat "$L/nas.txt")" "their version"

u POST /api/files/delete -d "{\"paths\":[\"$SP\"]}" >/dev/null
vm "sudo rm -rf $P/.recycle/$USER_NAME/SyncTest"
unset XDG_DATA_HOME
