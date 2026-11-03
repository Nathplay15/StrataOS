# shellcheck shell=bash
# End-to-end encrypted sync folders: two desktop clients (built here, run on
# the host against the forwarded HTTPS port) share an encrypted folder of
# $P. The NAS only sees ciphertext; the file manager sees it read-only and
# flagged; a client without the key (old app, phone, plain device token) is
# refused; the second computer unlocks with the recovery key; a passphrase
# change; a non-empty folder cannot be marked; the folder can be deleted as
# a whole.
section "Desktop sync: end-to-end encrypted folders"
HTTPS="https://localhost:${VM_HTTPS_PORT:-7443}"
EP="$P/E2ESecret"
GO=$(command -v go || echo "$HOME/.local/opt/go/bin/go")
S="$TMP/strataos-sync"
[ -x "$S" ] || (cd desktop && CGO_ENABLED=0 "$GO" build -o "$S" ./cmd/strataos-sync) 2>&1 | tail -3
EA="$TMP/e2e-a" EB="$TMP/e2e-b" LA="$TMP/e2e-la" LB="$TMP/e2e-lb"
pair_client() { # pair_client CONFIG NAME
  local pr; pr=$(u POST /api/me/devices/pairing)
  "$S" --config "$1" pair --server "$HTTPS" --code "$(echo "$pr" | py 'print(d["code"])')" --fingerprint "$(echo "$pr" | py 'print(d["fingerprint"])')" --name "$2" 2>&1
}
once() { timeout -s QUIT 240 "$S" --config "$1" sync-once 2>&1; }
check "first computer paired" "$(pair_client "$EA" e2e-enc-a)" "paired with"
check "second computer paired" "$(pair_client "$EB" e2e-enc-b)" "paired with"
TOKA=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['token'])" "$EA/config.json")
bt() { local m=$1 p=$2; shift 2; curl -sk -X "$m" -H "Authorization: Bearer $TOKA" -H 'Content-Type: application/json' "$HTTPS$p" "$@"; }

u POST /api/files/mkdir -d "{\"path\":\"$EP\"}" >/dev/null
printf 'correct horse battery staple\n' > "$TMP/e2e-pass"
mkdir -p "$LA/notes"
echo "top secret plan" > "$LA/notes/plan.txt"
head -c 300000 /dev/urandom > "$LA/big.bin"
check "short passphrase refused" "$(STRATAOS_SYNC_PASSPHRASE=short "$S" --config "$EA" add --local "$LA" --remote "$EP" --encrypt 2>&1)" "at least 10"
ADD=$("$S" --config "$EA" add --local "$LA" --remote "$EP" --encrypt --passphrase-file "$TMP/e2e-pass" 2>&1)
check "encrypted folder added, recovery key shown" "$(echo "$ADD" | grep -c '^[A-Z2-7]\{4\}-')" "1"
RK=$(echo "$ADD" | grep '^[A-Z2-7]\{4\}-')
check "listed as encrypted" "$("$S" --config "$EA" list)" "(end-to-end encrypted)"
ID=$(echo "$ADD" | tail -1)
check "recovery key exported again" "$("$S" --config "$EA" recovery-key "$ID" 2>&1)" "$RK"
check "first sync" "$(once "$EA")" "idle"

# What the NAS has: a header and ciphertext only.
check "header on the NAS" "$(vm "sudo cat $EP/.strataos-sync-e2e.json" | py 'print(d["v"], d["cipher"], d["kdf"]["alg"], "key" in d)')" "1 xchacha20poly1305 argon2id False"
check "no plaintext name" "$(vm "sudo find $EP -mindepth 1 -not -name .strataos-sync-e2e.json | grep -c -e plan -e notes -e big")" "0"
check "no plaintext content" "$(vm "sudo grep -rl 'top secret' $EP | wc -l")" "0"
check "ciphertext larger by the format overhead" "$(vm "sudo find $EP -type f -size +290k -printf '%s\n'")" "300157"

# The file manager: flagged, read-only.
check "flagged in its parent's listing" "$(u GET "/api/files?path=$P" | py 'print([e["encrypted"] for e in d["entries"] if e["name"]=="E2ESecret"])')" "[True]"
check "listing inside: encrypted and read-only" "$(u GET "/api/files?path=$EP" | py 'print(d["encrypted"], d["writable"])')" "True False"
echo "plain" > "$TMP/plain.txt"
check "web upload refused" "$(curl -s -b "$J2" -H 'X-Strata: 1' -F "file=@$TMP/plain.txt" "$B/api/files/upload?path=$EP" -o /dev/null -w '%{http_code}')" "403"
check "web new folder refused" "$(u POST /api/files/mkdir -d "{\"path\":\"$EP/new\"}" -o /dev/null -w '%{http_code}')" "403"
check "administrators too" "$(c POST /api/files/mkdir -d "{\"path\":\"$EP/new\"}" -o /dev/null -w '%{http_code}')" "403"

# Clients without the key are refused instead of mixing plaintext in.
check "plain sync client refused" "$(bt GET "/api/sync/changes?folder=$EP")" "end-to-end encrypted"
check "header with another key ID refused" "$(bt GET "/api/sync/changes?folder=$EP" -H "X-Strata-E2E: $(printf 'ab%.0s' $(seq 16))")" "end-to-end encrypted"
check "plain sync upload refused" "$(bt POST /api/mobile/uploads -d "{\"folder\":\"$EP\",\"name\":\"x.txt\",\"size\":1,\"sync\":true,\"replace\":true}" -o /dev/null -w '%{http_code}')" "403"
check "header served to clients" "$(bt GET "/api/sync/e2e?folder=$EP" | py 'print(d["encrypted"], d["root"]=="'"$EP"'", d["header"]["v"])')" "True True 1"
check "state of a plain folder" "$(bt GET "/api/sync/e2e?folder=$P" | py 'print(d)')" "{'encrypted': False}"
check "non-empty folder cannot be marked" "$(bt POST /api/sync/e2e -d "{\"folder\":\"$P\",\"header\":$(vm "sudo cat $EP/.strataos-sync-e2e.json")}")" "only an empty folder"
check "unmarking a non-empty folder refused" "$(bt DELETE "/api/sync/e2e?folder=$EP")" "empty the folder first"

# The second computer: locked without the passphrase, unlocked with the recovery key.
check "second computer: passphrase asked" "$("$S" --config "$EB" add --local "$LB" --remote "$EP" 2>&1)" "end-to-end encrypted"
printf 'wrong passphrase!!\n' > "$TMP/e2e-wrong"
check "wrong passphrase refused" "$("$S" --config "$EB" add --local "$LB" --remote "$EP" --passphrase-file "$TMP/e2e-wrong" 2>&1)" "Wrong passphrase"
echo "$RK" > "$TMP/e2e-rk"
check "unlocked with the recovery key" "$("$S" --config "$EB" add --local "$LB" --remote "$EP" --recovery-key-file "$TMP/e2e-rk" >/dev/null 2>&1 && "$S" --config "$EB" list)" "(end-to-end encrypted)"
check "second computer syncs" "$(once "$EB")" "idle"
check "decrypted contents" "$(cat "$LB/notes/plan.txt"; cmp "$LA/big.bin" "$LB/big.bin" && echo same)" "top secret plan
same"

# Changes travel back, encrypted.
echo "revised plan" > "$LB/notes/plan.txt"
mv "$LB/big.bin" "$LB/notes/moved.bin"
once "$EB" >/dev/null
check "changes reach the first computer" "$(once "$EA" >/dev/null; cat "$LA/notes/plan.txt"; ls "$LA/notes" | tr '\n' ' ')" "revised plan
moved.bin plan.txt"
check "still no plaintext on the NAS" "$(vm "sudo grep -rl 'revised' $EP | wc -l; sudo find $EP | grep -c moved")" "0
0"

# A new passphrase: the same key, the other computers keep going.
echo "another long passphrase" > "$TMP/e2e-pass2"
ID_BEFORE=$(vm "sudo cat $EP/.strataos-sync-e2e.json" | py 'print(d["id"])')
check "header only replaced with the same key ID" "$(bt PUT /api/sync/e2e -d "{\"folder\":\"$EP\",\"header\":$(vm "sudo cat $EP/.strataos-sync-e2e.json" | py 'import json; d["id"]="cd"*16; print(json.dumps(d))')}" -o /dev/null -w '%{http_code}')" "412"
check "passphrase changed" "$("$S" --config "$EA" passphrase "$ID" --passphrase-file "$TMP/e2e-pass2" 2>&1)" "passphrase changed"
check "key ID unchanged" "$(vm "sudo cat $EP/.strataos-sync-e2e.json" | py 'print(d["id"])')" "$ID_BEFORE"
check "the second computer keeps syncing" "$(echo more > "$LA/more.txt"; once "$EA" >/dev/null; once "$EB" >/dev/null; cat "$LB/more.txt")" "more"
EC="$TMP/e2e-c"
"$S" --config "$EC" pair --server "$HTTPS" --code "$(u POST /api/me/devices/pairing | py 'print(d["code"])')" --trust >/dev/null 2>&1
check "old passphrase no longer opens it" "$("$S" --config "$EC" add --local "$TMP/e2e-lc" --remote "$EP" --passphrase-file "$TMP/e2e-pass" 2>&1)" "Wrong passphrase"
check "new passphrase opens it" "$("$S" --config "$EC" add --local "$TMP/e2e-lc" --remote "$EP" --passphrase-file "$TMP/e2e-pass2" >/dev/null 2>&1 && once "$EC" >/dev/null; cat "$TMP/e2e-lc/notes/plan.txt")" "revised plan"

# Cleanup: the folder can be deleted as a whole from the web interface.
for x in "$EA" "$EB" "$EC"; do "$S" --config "$x" unpair >/dev/null 2>&1; done
check "encrypted folder deleted as a whole" "$(u POST /api/files/delete -d "{\"paths\":[\"$EP\"]}")" '"ok":true'
check "gone" "$(vm "sudo test -e $EP && echo still || echo gone")" "gone"
