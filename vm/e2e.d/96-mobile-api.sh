# shellcheck shell=bash
# Server side of the Android app: pairing (password + two-factor code, and the
# one-time code of the QR code), device tokens (rights, CSRF, revocation),
# resumable uploads with an interrupted chunk, backup dedupe, the events feed,
# UnifiedPush delivery (Web Push encrypted, decrypted here by a sink running on
# the host with the Python `cryptography` module, like the passkeys section)
# and address blocking on bad tokens. The sink listens on the host's loopback,
# which the VM reaches as 10.0.2.2.
section "Mobile app API"
MP="$P/MobileTest"
mb() { local t=$1 m=$2 p=$3; shift 3; curl -s -X "$m" -H "Authorization: Bearer $t" -H 'Content-Type: application/json' "$B$p" "$@"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

# Pairing with the password.
R=$(curl -s -X POST "$B/api/mobile/pair" -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\",\"deviceName\":\"Pixel e2e\",\"model\":\"Google Pixel 8\",\"app\":\"0.7.0\"}")
TA=$(echo "$R" | py 'print(d.get("token",""))' 2>/dev/null)
check "paired with the password" "$TA" "stdev_"
check "wrong password refused" "$(curl -s -X POST "$B/api/mobile/pair" -d "{\"username\":\"$USER_NAME\",\"password\":\"nope\"}")" "invalid username or password"
check "device listed in the web interface" "$(u GET /api/me/devices | py 'print([(x["name"], x["model"]) for x in d])')" "('Pixel e2e', 'Google Pixel 8')"
check "token hash only in the state" "$(vm "sudo grep -c '$TA' /var/lib/strata/state.json")" "0"

# Pairing with a two-factor code (here a recovery code: no wait for a new time step).
SEC=$(u POST /api/me/2fa/setup | py 'print(d["secret"])')
RC=$(u POST /api/me/2fa/enable -d "{\"code\":\"$(python3 -I vm/totp.py "$SEC")\"}" | py 'print(d["recoveryCodes"][0])')
check "two-factor code asked" "$(curl -s -X POST "$B/api/mobile/pair" -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}")" '"twoFactor":true'
check "paired with password and code" "$(curl -s -X POST "$B/api/mobile/pair" -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\",\"code\":\"$RC\",\"deviceName\":\"Tablet\"}")" '"token":"stdev_'
c POST "/api/users/$USER_NAME/2fa/reset" >/dev/null
u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null

# Pairing with the QR code of the web interface (administrator).
PR=$(c POST /api/me/devices/pairing)
PC=$(echo "$PR" | py 'print(d["code"])')
FP=$(echo | openssl s_client -connect localhost:7443 2>/dev/null | openssl x509 -noout -fingerprint -sha256 | cut -d= -f2)
check "QR code carries the certificate fingerprint" "$(echo "$PR" | py 'print(d["fingerprint"])')" "$FP"
TD=$(curl -s -X POST "$B/api/mobile/pair" -d "{\"pairingCode\":\"$PC\",\"deviceName\":\"Admin phone\",\"model\":\"Fairphone 5\"}" | py 'print(d.get("token",""))')
check "paired with the one-time code" "$TD" "stdev_"
check "pairing code single-use" "$(curl -s -X POST "$B/api/mobile/pair" -d "{\"pairingCode\":\"$PC\"}")" "invalid or expired pairing code"

# The token acts as its user, with the user's rights.
check "token identifies the user" "$(mb "$TA" GET /api/mobile/info | py 'print(d["user"]["username"], d["device"]["name"], bool(d["vapidKey"]))')" "$USER_NAME Pixel e2e True"
check "files listed with the token" "$(mb "$TA" GET "/api/files?path=$P" | py 'print(d["writable"])')" "True"
check "writes without cookie or X-Strata (no CSRF exposure)" "$(mb "$TA" POST /api/files/mkdir -d "{\"path\":\"$MP\"}")" '"ok":true'
check "no administrator rights for a user's token" "$(code -H "Authorization: Bearer $TA" "$B/api/system/services")" "403"
check "token cannot create pairing codes" "$(code -X POST -H "Authorization: Bearer $TA" "$B/api/me/devices/pairing")" "403"
check "token does not fall back to the cookie" "$(curl -s -o /dev/null -w '%{http_code}' -b "$J" -H 'Authorization: Bearer stdev_bad' "$B/api/me")" "401"

# Resumable upload: 1 MiB, an interrupted chunk, a wrong offset, then the rest.
head -c 3145728 /dev/urandom > "$TMP/up.bin"
SUM=$(sha256sum "$TMP/up.bin" | cut -d' ' -f1)
UP=$(mb "$TA" POST /api/mobile/uploads -d "{\"folder\":\"$MP/Pixel/2021\",\"mkdirs\":true,\"name\":\"IMG_0001.jpg\",\"size\":3145728,\"modTime\":1626258600000,\"sha256\":\"$SUM\"}")
UID_=$(echo "$UP" | py 'print(d["id"])')
check "upload created" "$UP" '"offset":0'
check "temporary file hidden in the folder" "$(vm "sudo ls -A $MP/Pixel/2021")" ".strata-upload-$UID_"
check "first chunk" "$(head -c 1048576 "$TMP/up.bin" | mb "$TA" PATCH "/api/mobile/uploads/$UID_" -H 'Upload-Offset: 0' -H 'Content-Type: application/offset+octet-stream' --data-binary @-)" '"offset":1048576'
python3 -I - "$B" "$TA" "$UID_" "$TMP/up.bin" <<'PYEOF'
# Announces 1 MiB, sends 300000 bytes and drops the connection.
import socket, sys, urllib.parse
base, tok, uid, path = sys.argv[1:]
u = urllib.parse.urlparse(base)
data = open(path, "rb").read()[1048576:1048576 + 300000]
s = socket.create_connection((u.hostname, u.port or 80))
s.sendall((f"PATCH /api/mobile/uploads/{uid} HTTP/1.1\r\nHost: {u.netloc}\r\nAuthorization: Bearer {tok}\r\n"
           "Upload-Offset: 1048576\r\nContent-Type: application/offset+octet-stream\r\nContent-Length: 1048576\r\n\r\n").encode() + data)
s.close()
PYEOF
sleep 1
OFF=$(mb "$TA" GET "/api/mobile/uploads/$UID_" | py 'print(d["offset"])')
check "interrupted chunk kept" "$OFF" "1348576"
check "wrong offset refused with the right one" "$(echo x | mb "$TA" PATCH "/api/mobile/uploads/$UID_" -H 'Upload-Offset: 5' --data-binary @-)" "\"offset\":$OFF"
DONE=$(tail -c +$((OFF + 1)) "$TMP/up.bin" | mb "$TA" PATCH "/api/mobile/uploads/$UID_" -H "Upload-Offset: $OFF" -H 'Content-Type: application/offset+octet-stream' --data-binary @-)
check "resumed and finished" "$DONE" '"done":true'
F="$MP/Pixel/2021/IMG_0001.jpg"
check "file intact" "$(vm "sudo sha256sum '$F'" | cut -d' ' -f1)" "$SUM"
check "owned by the user, original date" "$(vm "sudo stat -c '%U %a %Y' '$F'")" "$USER_NAME 664 1626258600"
check "folders created for the user" "$(vm "sudo stat -c '%U' $MP/Pixel/2021")" "$USER_NAME"
check "no temporary file left" "$(vm "sudo ls -A $MP/Pixel/2021 | grep -c strata-upload")" "0"
check "finished upload forgotten" "$(code -H "Authorization: Bearer $TA" "$B/api/mobile/uploads/$UID_")" "410"

# Dedupe.
EX=$(mb "$TA" POST /api/mobile/exists -d "{\"folder\":\"$MP/Pixel/2021\",\"files\":[{\"name\":\"IMG_0001.jpg\",\"size\":3145728},{\"name\":\"renamed.jpg\",\"size\":3145728,\"sha256\":\"$SUM\"},{\"name\":\"new.jpg\",\"size\":10}]}")
check "dedupe: same name and size, same content renamed, new file" "$(echo "$EX" | py 'print([(f["exists"], f.get("as","")) for f in d["files"]])')" "[(True, ''), (True, 'IMG_0001.jpg'), (False, '')]"
check "dedupe of a missing folder" "$(mb "$TA" POST /api/mobile/exists -d "{\"folder\":\"$MP/none\",\"files\":[{\"name\":\"a\",\"size\":1}]}")" '"exists":false'
one() { # one <name> <content> -> finished upload
  local id; id=$(mb "$TA" POST /api/mobile/uploads -d "{\"folder\":\"$MP\",\"name\":\"$1\",\"size\":${#2}}" | py 'print(d["id"])')
  printf '%s' "$2" | mb "$TA" PATCH "/api/mobile/uploads/$id" -H 'Upload-Offset: 0' --data-binary @-
}
one same.txt hello >/dev/null
check "same file again: duplicate, not added" "$(one same.txt hello)" '"duplicate":true'
check "different file, same name: kept side by side" "$(one same.txt other!)" '"name":"same (2).txt"'
AB=$(mb "$TA" POST /api/mobile/uploads -d "{\"folder\":\"$MP\",\"name\":\"abandoned.bin\",\"size\":100}" | py 'print(d["id"])')
mb "$TA" DELETE "/api/mobile/uploads/$AB" >/dev/null
check "cancelled upload removed" "$(vm "sudo ls -A $MP | grep -c strata-upload")" "0"
check "upload refused without write access" "$(mb "$TA" POST /api/mobile/uploads -d '{"folder":"/srv/strata/tank/media","name":"x","size":1}')" "access"
check "path escape refused" "$(mb "$TA" POST /api/mobile/uploads -d "{\"folder\":\"$MP\",\"name\":\"../x\",\"size\":1}")" "invalid file name"

# UnifiedPush: an encrypted endpoint (RFC 8291 + VAPID) and a plain one (ntfy).
cat > "$TMP/up-sink.py" <<'PYEOF'
import base64, hashlib, hmac, json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.hkdf import HKDF, HKDFExpand
out = sys.argv[1]
b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=").decode()
key = ec.generate_private_key(ec.SECP256R1())
pub = key.public_key().public_bytes(serialization.Encoding.X962, serialization.PublicFormat.UncompressedPoint)
auth = os.urandom(16)
json.dump({"p256dh": b64(pub), "auth": b64(auth)}, open(out + ".keys", "w"))
def decrypt(body):
    salt, idlen = body[:16], body[20]
    aspub = body[21:21 + idlen]
    shared = key.exchange(ec.ECDH(), ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), aspub))
    ikm = HKDF(hashes.SHA256(), 32, auth, b"WebPush: info\0" + pub + aspub).derive(shared)
    prk = hmac.new(salt, ikm, hashlib.sha256).digest()  # HKDF-Extract
    cek = HKDFExpand(hashes.SHA256(), 16, b"Content-Encoding: aes128gcm\0").derive(prk)
    nonce = HKDFExpand(hashes.SHA256(), 12, b"Content-Encoding: nonce\0").derive(prk)
    plain = AESGCM(cek).decrypt(nonce, body[21 + idlen:], None)
    return plain.rstrip(b"\0")[:-1]
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        rec = {"path": self.path, "enc": self.headers.get("Content-Encoding", ""), "vapid": self.headers.get("Authorization", "").startswith("vapid t=")}
        try:
            rec["body"] = (decrypt(body) if rec["enc"] == "aes128gcm" else body).decode()
        except Exception as e:
            rec["body"] = "decrypt failed: %r" % e
        with open(out, "a") as f:
            f.write(json.dumps(rec) + "\n")
        self.send_response(201); self.end_headers()
    def log_message(self, *a): pass
srv = HTTPServer(("127.0.0.1", 8791), H)
print("ready", flush=True)
srv.serve_forever()
PYEOF
python3 -I "$TMP/up-sink.py" "$TMP/push.log" > "$TMP/up-sink.out" 2>&1 &
SINKS_PID=$!
for _ in $(seq 1 20); do grep -q ready "$TMP/up-sink.out" 2>/dev/null && break; sleep 0.5; done
KEYS=$(cat "$TMP/push.log.keys" 2>/dev/null)
REG=$(echo "$KEYS" | py 'd["pushUrl"]="http://10.0.2.2:8791/up/admin-phone"; print(json.dumps(d))')
check "push endpoint registered" "$(mb "$TD" PUT /api/mobile/device -d "$REG")" '"ok":true'
DID=$(c GET /api/me/devices | py 'print([x["id"] for x in d if x["name"]=="Admin phone"][0])')
check "test push sent" "$(c POST "/api/me/devices/$DID/test")" '"ok":true'
sleep 1
check "push received encrypted, with VAPID, decrypted" "$(tail -1 "$TMP/push.log" 2>/dev/null | py 'print(d["enc"], d["vapid"], json.loads(d["body"])["message"])')" "aes128gcm True Notifications from StrataOS reach this phone."
check "NAS notification pushed to the phone" "$(c POST /api/settings/test-notification)" "mobile"
sleep 1
check "notification payload" "$(tail -1 "$TMP/push.log" 2>/dev/null | py 'b=json.loads(d["body"]); print(b["type"], b["level"], bool(b["subject"]))')" "notification info True"
check "events feed (fallback without push)" "$(mb "$TD" GET '/api/mobile/events?since=0' | py 'print(len(d["events"]) > 0, d["cursor"] == d["events"][-1]["id"])')" "True True"
CUR=$(mb "$TD" GET '/api/mobile/events?since=0' | py 'print(d["cursor"])')
check "nothing new after the cursor" "$(mb "$TD" GET "/api/mobile/events?since=$CUR" | py 'print(len(d["events"]))')" "0"
check "no NAS events for a user's phone" "$(mb "$TA" GET '/api/mobile/events?since=0' | py 'print(len(d["events"]))')" "0"
mb "$TD" PUT /api/mobile/device -d '{"pushUrl":"http://10.0.2.2:8791/up/plain"}' >/dev/null
c POST "/api/me/devices/$DID/test" >/dev/null
sleep 1
check "plain endpoint (ntfy topic) gets JSON" "$(tail -1 "$TMP/push.log" 2>/dev/null | py 'print(d["path"], d["enc"] == "", json.loads(d["body"])["subject"])')" "/up/plain True Test"
mb "$TD" PUT /api/mobile/device -d '{"pushUrl":"http://127.0.0.1:7070/api/setup"}' >/dev/null
check "endpoint on the NAS itself refused" "$(c POST "/api/me/devices/$DID/test")" "refused"
check "invalid endpoint rejected" "$(mb "$TD" PUT /api/mobile/device -d '{"pushUrl":"file:///etc/passwd"}')" "invalid push endpoint"
kill "$SINKS_PID" 2>/dev/null; SINKS_PID=""

# Revocation, from the web interface and by the app itself.
AID=$(u GET /api/me/devices | py 'print([x["id"] for x in d if x["name"]=="Pixel e2e"][0])')
check "revoked from the web interface" "$(u DELETE "/api/me/devices/$AID")" '"ok":true'
check "revoked token refused" "$(code -H "Authorization: Bearer $TA" "$B/api/mobile/info")" "401"
check "app signs itself out" "$(mb "$TD" DELETE /api/mobile/device)" '"ok":true'
check "signed-out token refused" "$(code -H "Authorization: Bearer $TD" "$B/api/mobile/info")" "401"
check "pairing and revocation logged" "$(c GET /api/system/events | py 'print(sorted({e["message"].split(":")[0] for e in d if e["message"].startswith("Mobile device")}))')" "['Mobile device paired', 'Mobile device revoked', 'Mobile device signed out']"

# Bad tokens count in address blocking (sent through the VM's loopback with
# X-Forwarded-For, so the documentation address is blocked, not the runner).
c PUT /api/guard -d '{"enabled":true,"maxFailures":2,"windowMin":10,"blockMin":5,"neverLan":true,"allow":["10.0.2.2/32"],"geo":{"enabled":false}}' >/dev/null
for _ in 1 2 3; do vm "curl -s -o /dev/null -H 'X-Forwarded-For: 198.51.100.9' -H 'Authorization: Bearer stdev_$(printf '0%.0s' $(seq 1 64))' http://127.0.0.1:7070/api/mobile/info"; done
check "bad device tokens block the address" "$(c GET /api/guard | py 'print([(b["ip"], b["service"]) for b in d["blocks"]])')" "('198.51.100.9', 'mobile app')"
c POST /api/guard/unblock -d '{"ip":"198.51.100.9"}' >/dev/null
c PUT /api/guard -d '{"enabled":false,"maxFailures":5,"windowMin":10,"blockMin":60,"neverLan":true,"geo":{"enabled":false}}' >/dev/null

# Clean up: remaining devices (the tablet), test files.
for id in $(u GET /api/me/devices | py 'print(" ".join(x["id"] for x in d))'); do u DELETE "/api/me/devices/$id" >/dev/null; done
for id in $(c GET /api/me/devices | py 'print(" ".join(x["id"] for x in d))'); do c DELETE "/api/me/devices/$id" >/dev/null; done
vm "sudo rm -rf '$MP'"
