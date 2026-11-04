# shellcheck shell=bash
# Access and sharing (0.10): the two-factor setup step of administrators and
# the setting that requires it (a second administrator with TOTP, codes
# computed here in Python, switches it on; the main admin is then limited to
# the setup and never locked out), the remote access assistant and the
# reachability test against targets inside the VM (a name only the VM's hosts
# file knows, the HTTPS interface with its self-signed certificate, a closed
# port), and share links: file requests (upload only, no listing, guest
# name/email, size limits, names never revealed), password, expiry, download
# limit, statistics and the visitor rate limit (last: it fills the bucket of
# the test runner's address).
section "Access: two-factor for administrators"
AJ="$TMP/acc-admin2.jar" VJ="$TMP/acc-visitor.jar"
a2() { local m=$1 p=$2; shift 2; curl -s -b "$AJ" -c "$AJ" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
pub() { curl -s -b "$VJ" -c "$VJ" -H 'X-Strata: 1' "$@"; }
totp() { python3 -I -c '
import base64, hashlib, hmac, struct, sys, time
k = base64.b32decode(sys.argv[1] + "=" * (-len(sys.argv[1]) % 8))
h = hmac.new(k, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
o = h[-1] & 15
print("%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7fffffff) % 1000000))' "$1"; }
next_step() { sleep $((31 - $(date +%s) % 30)); }

check "admin without second factor is prompted" "$(c GET /api/me/2fa/prompt | py 'print(d["show"], d["canPostpone"], d["enforced"])')" "True True False"
check "prompt in /api/me" "$(c GET /api/me | py 'print(d["twoFactorPrompt"]["show"])')" "True"
check "a user is not prompted" "$(u GET /api/me/2fa/prompt | py 'print(d["show"])')" "False"
check "remind me in 7 days" "$(c POST /api/me/2fa/postpone | py 'print(bool(d["postponedUntil"]))')" "True"
check "hidden once postponed" "$(c GET /api/me/2fa/prompt | py 'print(d["show"], bool(d.get("postponedUntil")))')" "False True"
check "requiring needs a second factor of one's own" "$(c PUT /api/security/2fa-policy -d '{"require":true}')" "your own account first"
check "second admin" "$(c POST /api/users -d '{"username":"e2e2fa","password":"E2e-2fa-Pass-1","role":"admin"}')" '"ok":true'
a2 POST /api/login -d '{"username":"e2e2fa","password":"E2e-2fa-Pass-1"}' >/dev/null
SECRET=$(a2 POST /api/me/2fa/setup | py 'print(d["secret"])')
check "TOTP enabled" "$(a2 POST /api/me/2fa/enable -d "{\"code\":\"$(totp "$SECRET")\"}" | py 'print(len(d["recoveryCodes"]))')" "8"
check "protected admin not prompted" "$(a2 GET /api/me/2fa/prompt | py 'print(d["show"])')" "False"
check "require, no grace" "$(a2 PUT /api/security/2fa-policy -d '{"require":true,"graceDays":-1}')" '"ok":true'
check "policy lists the admins" "$(a2 GET /api/security/2fa-policy | py "print(d['require'], [a['enforced'] for a in d['admins'] if a['username']=='$ADMIN_USER'], [a['totp'] for a in d['admins'] if a['username']=='e2e2fa'])")" "True [True] [True]"
check "unprotected admin limited to the setup" "$(c GET /api/users)" '"twoFactorSetup":true'
check "…and to no settings" "$(c PUT /api/security/2fa-policy -d '{"require":false}')" '"twoFactorSetup":true'
check "…but still knows who it is" "$(c GET /api/me | py 'print(d["username"], d["twoFactorPrompt"]["enforced"], d["twoFactorPrompt"]["canPostpone"])')" "$ADMIN_USER True False"
check "…and can start the TOTP setup" "$(c POST /api/me/2fa/setup | py 'print(len(d["secret"]) > 10)')" "True"
check "sign-in still works (no lockout)" "$(curl -s -X POST -H 'X-Strata: 1' "$B/api/login" -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" | py 'print(d["username"], d["twoFactorPrompt"]["enforced"])')" "$ADMIN_USER True"
check "users are not concerned" "$(u GET /api/me | py 'print(d.get("twoFactorPrompt", {}).get("show", False))')" "False"
check "last second factor kept while required" "$(a2 POST /api/me/2fa/disable -d '{"password":"E2e-2fa-Pass-1"}')" "add another passkey"
next_step
check "TOTP sign-in of the second admin" "$(curl -s -X POST -H 'X-Strata: 1' "$B/api/login" -d "{\"username\":\"e2e2fa\",\"password\":\"E2e-2fa-Pass-1\",\"code\":\"$(totp "$SECRET")\"}" | py 'print(d["username"])')" "e2e2fa"
check "no longer required" "$(a2 PUT /api/security/2fa-policy -d '{"require":false}')" '"ok":true'
check "admin unlocked" "$(c GET /api/users | py 'print(type(d).__name__)')" "list"
check "second admin removed" "$(c DELETE /api/users/e2e2fa)" '"ok":true'

section "Access: remote access assistant and reachability test"
check "assistant: links need a public method" "$(c GET '/api/remote/assistant?goals=links,ui' | py 'print(d["options"][0]["method"], d["options"][0]["recommended"], "nat" in d["facts"])')" "tunnel True True"
check "assistant: private access" "$(c GET '/api/remote/assistant?goals=ui,phones' | py 'print([o["method"] for o in d["options"] if not o["suitable"] or o["missing"]])')" "[]"
check "probe proves the NAS (HMAC of the nonce)" "$(curl -s "$B/api/remote/probe?n=0123456789abcdef0123" | py 'print(len(d["proof"]))')" "64"
check "probe refuses short nonces" "$(curl -s "$B/api/remote/probe?n=x")" "invalid probe"
check "check refuses IP addresses" "$(c POST /api/remote/check -d '{"host":"127.0.0.1"}')" "not an IP address"
vm "echo '127.0.0.1 reach-e2e.example.net' | sudo tee -a /etc/hosts >/dev/null"
R=$(c POST /api/remote/check -d '{"host":"reach-e2e.example.net","port":7443,"ui":true}')
check "local name: not resolvable from outside" "$(echo "$R" | py 'print([f["code"] for f in d["findings"] if f["step"]=="dns"])')" "dns-"
check "local name: self-signed certificate seen" "$(echo "$R" | py 'print([f["code"] for f in d["findings"]])')" "tls-untrusted"
check "local name: this NAS answered" "$(echo "$R" | py 'print([f["code"] for f in d["findings"]])')" "http-this-nas"
check "local name never reported reachable" "$(echo "$R" | py 'print(d["verdict"] in ("unreachable","unknown"))')" "True"
check "closed port" "$(c POST /api/remote/check -d '{"host":"reach-e2e.example.net","port":7999}' | py 'print(d["verdict"], [f["code"] for f in d["findings"] if f["step"]=="tcp"])')" "tcp-refused"
check "plain HTTP" "$(c POST /api/remote/check -d '{"host":"reach-e2e.example.net","port":7070,"http":true,"ui":true}' | py 'print([f["code"] for f in d["findings"]])')" "http-this-nas"
check "users cannot run it" "$(u POST /api/remote/check -d '{"host":"reach-e2e.example.net"}')" "administrator rights required"
vm "sudo sed -i '/reach-e2e.example.net/d' /etc/hosts"

section "Access: file requests and share links"
REQ=$P/e2e-request
vm "sudo mkdir -p $REQ && echo secret | sudo tee $REQ/secret.txt >/dev/null && sudo chmod -R a+rwX $REQ"
LID=$(c POST /api/links -d "{\"path\":\"$REQ\",\"download\":false,\"upload\":true,\"title\":\"Send me your files\",\"message\":\"Thanks!\",\"askGuest\":\"required\",\"maxFileMb\":1,\"maxTotalMb\":1,\"notify\":true,\"expiresDays\":30}" | py 'print(d["id"])')
check "file request created" "$(c GET /api/links | py "print([(l['upload'], l['download'], l['askGuest'], l['maxFileBytes'], l['state']) for l in d if l['id']=='$LID'])")" "(True, False, 'required', 1048576, 'active')"
INFO=$(pub "$B/api/s/$LID")
check "visitor sees the request" "$(echo "$INFO" | py 'print(d["upload"], d["download"], d["title"], d["askGuest"], d["maxFileBytes"])')" "True False Send me your files required 1048576"
check "no listing" "$(pub "$B/api/s/$LID/files" | py 'print(d["entries"])')" "[]"
check "no download" "$(pub -o /dev/null -w '%{http_code}' "$B/api/s/$LID/download?path=secret.txt")" "403"
check "no thumbnails" "$(pub -o /dev/null -w '%{http_code}' "$B/api/s/$LID/thumb?path=secret.txt")" "401"
echo hello > "$TMP/secret.txt"
check "name and email required" "$(pub -F "file=@$TMP/secret.txt" "$B/api/s/$LID/upload")" "name and email address are requested"
check "invalid email refused" "$(pub -F name=Marie -F 'email=marie@' -F "file=@$TMP/secret.txt" "$B/api/s/$LID/upload")" "invalid email"
check "upload keeps the name sent" "$(pub -F name='Marie Curie' -F email=marie@example.org -F "file=@$TMP/secret.txt" "$B/api/s/$LID/upload" | py 'print(d["uploaded"], d["names"])')" "1 ['secret.txt']"
check "…stored without replacing" "$(vm "sudo cat '$REQ/secret.txt'; sudo cat '$REQ/secret (2).txt'" | tr '\n' ' ')" "secret hello"
check "traversal in the file name" "$(pub -F name=M -F email=m@example.org -F "file=@$TMP/secret.txt;filename=../../e2e-escape.txt" "$B/api/s/$LID/upload" | py 'print(d["names"])'; vm "sudo test -e $P/e2e-escape.txt && echo escaped; sudo test -e $REQ/e2e-escape.txt && echo inside")" "inside"
head -c 1200000 /dev/urandom > "$TMP/big.bin"
check "per-file size limit" "$(pub -F name=M -F email=m@example.org -F "file=@$TMP/big.bin" "$B/api/s/$LID/upload")" "file too large"
check "…partial file removed" "$(vm "ls $REQ | grep -c big.bin")" "0"
check "cross-site upload refused" "$(curl -s -H 'Origin: http://evil.example' -F name=M -F email=m@example.org -F "file=@$TMP/secret.txt" "$B/api/s/$LID/upload")" "cross-site"
head -c 900000 /dev/urandom > "$TMP/mid.bin"
pub -F name=M -F email=m@example.org -F "file=@$TMP/mid.bin" "$B/api/s/$LID/upload" >/dev/null
check "total size limit" "$(pub -o /dev/null -w '%{http_code}' -F name=M -F email=m@example.org -F "file=@$TMP/mid.bin" "$B/api/s/$LID/upload")" "413"
check "statistics" "$(c GET /api/links | py "print([(l['uploads'], l['views'] >= 1, l['uploadLog'][0]['guest'], l['uploadLog'][0]['email'], bool(l.get('lastUpload'))) for l in d if l['id']=='$LID'])")" "(3, True, 'Marie Curie', 'marie@example.org', True)"
check "guest folders" "$(c PUT /api/links/$LID -d '{"guestFolders":true,"maxTotalMb":0}' | py 'print(d["guestFolders"], d["maxTotalBytes"] if "maxTotalBytes" in d else 0)')" "True 0"
pub -F name='Ada/../Lovelace' -F email=ada@example.org -F "file=@$TMP/secret.txt" "$B/api/s/$LID/upload" >/dev/null
check "…files in the sender's folder" "$(vm "sudo ls '$REQ/Ada_.._Lovelace/'")" "secret.txt"
check "event logged" "$(c GET /api/system/events | py '[print(e["message"]) for e in d[:10]]')" "received in"

# Password, download limit, expiry.
vm "echo download-me | sudo tee $P/e2e-link.txt >/dev/null"
DID=$(c POST /api/links -d "{\"path\":\"$P/e2e-link.txt\",\"download\":true,\"password\":\"open-sesame\",\"maxDownloads\":1}" | py 'print(d["id"])')
check "password asked" "$(pub "$B/api/s/$DID" | py 'print(d["password"], d["unlocked"], d["downloadsLeft"])')" "True False 1"
check "locked download" "$(pub -o /dev/null -w '%{http_code}' "$B/api/s/$DID/download")" "401"
check "wrong password" "$(pub -X POST -H 'Content-Type: application/json' "$B/api/s/$DID/unlock" -d '{"password":"nope"}')" "wrong password"
check "unlocked" "$(pub -X POST -H 'Content-Type: application/json' "$B/api/s/$DID/unlock" -d '{"password":"open-sesame"}')" '"ok":true'
check "preview does not count" "$(pub -o /dev/null -w '%{http_code}' "$B/api/s/$DID/download?inline=1")" "200"
check "download" "$(pub "$B/api/s/$DID/download")" "download-me"
check "limit reached" "$(pub "$B/api/s/$DID/download")" "download limit"
check "state for the owner" "$(c GET /api/links | py "print([(l['downloads'], l['state']) for l in d if l['id']=='$DID'])")" "(1, 'exhausted')"
check "limit raised" "$(c PUT /api/links/$DID -d '{"maxDownloads":5}' | py 'print(d["state"])')" "active"
check "new password signs visitors out" "$(c PUT /api/links/$DID -d '{"password":"other-pass"}' >/dev/null; pub -o /dev/null -w '%{http_code}' "$B/api/s/$DID/download")" "401"
check "password removed" "$(c PUT /api/links/$DID -d '{"clearPassword":true}' | py 'print(d["password"])')" "False"
check "past expiry refused" "$(c PUT /api/links/$DID -d '{"expires":"2020-01-01T00:00:00Z"}')" "next ten years"
check "others cannot edit" "$(u PUT /api/links/$DID -d '{"maxDownloads":0}')" "link not found"
SOON=$(date -u -d '+3 seconds' +%Y-%m-%dT%H:%M:%SZ)
c PUT /api/links/$DID -d "{\"expires\":\"$SOON\"}" >/dev/null
sleep 4
check "expired link is gone" "$(pub -o /dev/null -w '%{http_code}' "$B/api/s/$DID")" "410"
check "expired for the owner" "$(c GET /api/links | py "print([l['state'] for l in d if l['id']=='$DID'])")" "expired"

# Visitor rate limit (fills this address's bucket: last, then let it refill).
codes=$(for _ in $(seq 1 140); do curl -s -o /dev/null -w '%{http_code} ' -H 'X-Strata: 1' "$B/api/s/$LID"; done)
check "visitor rate limit" "$codes" "429"
c DELETE /api/links/$LID >/dev/null; c DELETE /api/links/$DID >/dev/null
vm "sudo rm -rf $REQ $P/e2e-link.txt"
sleep 30
