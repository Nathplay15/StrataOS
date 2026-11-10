# shellcheck shell=bash
# Centralized sign-in and S3 (0.11):
# - security key as a second factor, with a software U2F-style key in Python
#   (P-256, user presence only, no user handle; needs `cryptography` on the host);
# - OpenID Connect against Dex in a container of the VM (the browser part
#   runs with curl inside the VM, where the issuer and the NAS share localhost);
# - LDAP sign-in to the web interface and the phone pairing, the administrators
#   group, and the certificate checks of ldaps:// (vm/test-ldap.sh);
# - the built-in S3 service with rclone inside the VM.
# Uses alice (group famille) and the Photos share (rw for @famille).

section "Security key as a second factor"
SKA="$TMP/sk-auth.py" SKS="$TMP/sk-state.json" SKJ="$TMP/sk.jar"
ORIGIN=${B%/}
cat > "$SKA" <<'PYEOF'
import base64, hashlib, json, os, struct, sys
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec

def b64(b): return base64.urlsafe_b64encode(b).rstrip(b"=").decode()
def head(major, n):
    if n < 24: return bytes([major << 5 | n])
    if n < 256: return bytes([major << 5 | 24, n])
    return bytes([major << 5 | 25]) + struct.pack(">H", n)
def cbor(v):
    if isinstance(v, int): return head(0, v) if v >= 0 else head(1, -1 - v)
    if isinstance(v, bytes): return head(2, len(v)) + v
    if isinstance(v, str): return head(3, len(v.encode())) + v.encode()
    return head(5, len(v)) + b"".join(cbor(k) + cbor(x) for k, x in v.items())

cmd, state = sys.argv[1], sys.argv[2]
origin = sys.argv[3] if len(sys.argv) > 3 else ""
if cmd == "new":
    k = ec.generate_private_key(ec.SECP256R1())
    pem = k.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()).decode()
    json.dump({"key": pem, "id": b64(os.urandom(32)), "count": 0}, open(state, "w"))
    sys.exit()
st = json.load(open(state))
key = serialization.load_pem_private_key(st["key"].encode(), None)
begin = json.load(sys.stdin)
pk = begin["publicKey"]
def cdata(typ): return json.dumps({"type": typ, "challenge": pk["challenge"], "origin": origin, "crossOrigin": False}).encode()
def adata(rp, flags, count, rest=b""): return hashlib.sha256(rp.encode()).digest() + bytes([flags]) + struct.pack(">I", count) + rest
cid = base64.urlsafe_b64decode(st["id"] + "=" * (-len(st["id"]) % 4))
if cmd == "create":  # user present only (0x41), like a key without PIN
    n = key.public_key().public_numbers()
    cose = cbor({1: 2, 3: -7, -1: 1, -2: n.x.to_bytes(32, "big"), -3: n.y.to_bytes(32, "big")})
    ao = cbor({"fmt": "none", "attStmt": {}, "authData": adata(pk["rp"]["id"], 0x41, 0, bytes(16) + struct.pack(">H", len(cid)) + cid + cose)})
    resp = {"clientDataJSON": b64(cdata("webauthn.create")), "attestationObject": b64(ao), "transports": ["usb"]}
else:
    st["count"] += 1
    ad = adata(pk["rpId"], 0x01, st["count"])
    cd = cdata("webauthn.get")
    signed = ad + hashlib.sha256(cd).digest()
    if len(sys.argv) > 4: signed = b"x" + signed[1:]
    resp = {"clientDataJSON": b64(cd), "authenticatorData": b64(ad), "signature": b64(key.sign(signed, ec.ECDSA(hashes.SHA256())))}
json.dump(st, open(state, "w"))
print(json.dumps({"flow": begin["flow"], "credential": {"id": st["id"], "rawId": st["id"], "type": "public-key", "response": resp, "clientExtensionResults": {}}}))
PYEOF
sk() { local m=$1 p=$2; shift 2; curl -s -b "$SKJ" -c "$SKJ" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
if python3 -c 'import cryptography' 2>/dev/null; then
  python3 -I "$SKA" new "$SKS"
  REG=$(u POST /api/me/security-keys/begin -d "{\"password\":\"$USER_PASS\",\"name\":\"e2e key\"}")
  check "registration options (not discoverable, cross-platform)" "$(echo "$REG" | py 'a=d["publicKey"]["authenticatorSelection"]; print(a["residentKey"], a["userVerification"], a["authenticatorAttachment"])')" "discouraged preferred cross-platform"
  OUT=$(echo "$REG" | python3 -I "$SKA" create "$SKS" "$ORIGIN" | u POST /api/me/security-keys/finish -d @-)
  check "security key registered, recovery codes given" "$(echo "$OUT" | py 'print(d["key"]["name"], len(d["recoveryCodes"]))')" "e2e key 8"
  RC=$(echo "$OUT" | py 'print(d["recoveryCodes"][0])')
  rm -f "$SKJ"
  check "password alone asks for the key" "$(sk POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" | py 'print(d["twoFactor"], d["methods"]["key"], d["methods"]["keyHere"])')" "True True True"
  check "no session before the key" "$(sk GET /api/me)" "authentication required"
  check "wrong signature refused" "$(sk POST /api/login/key/begin | python3 -I "$SKA" get "$SKS" "$ORIGIN" bad | sk POST /api/login/key/finish -d @-)" "security key not accepted"
  check "security key completes the sign-in" "$(sk POST /api/login/key/begin | python3 -I "$SKA" get "$SKS" "$ORIGIN" | sk POST /api/login/key/finish -d @-)" "\"username\":\"$USER_NAME\""
  check "session works" "$(sk GET /api/me | py 'print(d["username"], d["twoFactor"])')" "$USER_NAME True"
  rm -f "$SKJ"
  sk POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null
  check "recovery code instead of the key" "$(sk POST /api/login/2fa -d "{\"code\":\"$RC\"}")" "\"username\":\"$USER_NAME\""
  rm -f "$SKJ"
  sk POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null
  check "recovery code single use" "$(sk POST /api/login/2fa -d "{\"code\":\"$RC\"}")" "wrong two-factor code"
  check "app pairing with the password refused (QR code instead)" "$(curl -s -X POST "$B/api/mobile/pair" -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\",\"deviceName\":\"e2e\"}")" "pairingCodeRequired"
  check "WebDAV refused (no second factor over Basic)" "$(c PUT /api/fileservices -d '{"webdav":true,"ftp":false,"ftpRequireTls":true,"sftp":false}' >/dev/null; curl -s -o /dev/null -w '%{http_code}' -u "$USER_NAME:$USER_PASS" "$B/dav/")" "403"
  c PUT /api/fileservices -d '{"webdav":false,"ftp":false,"ftpRequireTls":true,"sftp":false}' >/dev/null
  KID=$(u GET /api/me/security-keys | py 'print(d["keys"][0]["id"])')
  check "renamed" "$(u PUT "/api/me/security-keys/$KID" -d '{"name":"blue"}')" '"ok":true'
  check "removed" "$(u DELETE "/api/me/security-keys/$KID")" '"ok":true'
  check "password alone signs in again" "$(curl -s -X POST -H 'X-Strata: 1' "$B/api/login" -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}")" "\"username\":\"$USER_NAME\""
else
  echo "  (python3 cryptography missing on the host: covered by the Go test only)"
fi
check "admin policy accepts the key requirement setting" "$(c GET /api/security/2fa-policy | py 'print("requireKey" in d, "keyUv" in d)')" "True True"
rm -f "$SKA" "$SKS" "$SKJ"

section "OpenID Connect (Dex)"
DEXPW="Dex-$(head -c 6 /dev/urandom | base64 | tr -d '/+=')1"
vm "command -v htpasswd >/dev/null || sudo apt-get install -y -qq apache2-utils >/dev/null 2>&1"
DEXHASH=$(vm "htpasswd -nbBC 10 x '$DEXPW' | cut -d: -f2")
DEXHASH="\$2a${DEXHASH#\$2y}" # Go's bcrypt reads $2a$ ($2y$ is the same algorithm)
DEXSECRET=$(head -c 18 /dev/urandom | base64 | tr -d '/+=')
cat > "$TMP/dex.yaml" <<EOF
issuer: http://127.0.0.1:5556/dex
storage: {type: memory}
web: {http: 0.0.0.0:5556}
oauth2: {skipApprovalScreen: true}
enablePasswordDB: true
staticClients:
- id: strataos
  name: StrataOS
  secret: $DEXSECRET
  redirectURIs: ['http://localhost:7070/api/login/oidc/callback']
staticPasswords:
- email: oidc.user@example.com
  hash: "$DEXHASH"
  username: oidcuser
  preferredUsername: oidcuser
  userID: 4f0c2d1e-0000-4000-8000-000000000001
EOF
vm "cat > /tmp/dex.yaml" < "$TMP/dex.yaml"
vm "sudo docker rm -f e2e-dex >/dev/null 2>&1; sudo docker run -d --name e2e-dex --network host -v /tmp/dex.yaml:/etc/dex/config.yaml:ro ghcr.io/dexidp/dex:v2.41.1 dex serve /etc/dex/config.yaml >/dev/null"
for _ in $(seq 1 60); do vm "curl -sf http://127.0.0.1:5556/dex/.well-known/openid-configuration >/dev/null" && break; sleep 2; done
PROV="{\"id\":\"dex\",\"name\":\"Dex\",\"issuer\":\"http://127.0.0.1:5556/dex\",\"clientId\":\"strataos\",\"clientSecret\":\"$DEXSECRET\",\"enabled\":true,\"allowHttp\":true,\"autoCreate\":false}"
check "plain HTTP issuer needs the explicit setting" "$(c POST /api/security/oidc -d "${PROV/\"allowHttp\":true/\"allowHttp\":false}")" "HTTPS"
check "provider metadata test" "$(c POST /api/security/oidc/test -d '{"issuer":"http://127.0.0.1:5556/dex","allowHttp":true}' | py 'print(d["ok"], d["keys"] > 0)')" "True True"
check "provider added" "$(c POST /api/security/oidc -d "$PROV")" '"id":"dex"'
check "button on the sign-in page" "$(curl -s "$B/api/login/providers")" '"name":"Dex"'
check "client secret never sent back" "$(c GET /api/security/oidc | grep -c "$DEXSECRET")" "0"
# The browser: inside the VM, follow the redirects, post the Dex login form, come back.
cat > "$TMP/oidc-browser.sh" <<'EOF'
J=$(mktemp); trap 'rm -f "$J"' EXIT
start=${1:-/api/login/oidc/dex}
form=$(curl -s -b "$J" -c "$J" -L -o /dev/null -w '%{url_effective}' "http://localhost:7070$start")
final=$(curl -s -b "$J" -c "$J" -L -o /dev/null -w '%{url_effective}' --data-urlencode "login=oidc.user@example.com" --data-urlencode "password=$2" "$form")
echo "final=$final"
curl -s -b "$J" -H 'X-Strata: 1' http://localhost:7070/api/me
EOF
vm "cat > /tmp/oidc-browser.sh" < "$TMP/oidc-browser.sh"
OUT=$(vm "bash /tmp/oidc-browser.sh /api/login/oidc/dex '$DEXPW'")
check "unknown identity refused without automatic accounts" "$OUT" "oidc_error="
c PUT /api/security/oidc/dex -d "${PROV/\"autoCreate\":false/\"autoCreate\":true}" >/dev/null
OUT=$(vm "bash /tmp/oidc-browser.sh /api/login/oidc/dex '$DEXPW'")
check "signed in through Dex, account created" "$OUT" '"username":"oidcuser"'
check "…as a user, without password" "$OUT" '"noPassword":true'
check "linked account listed" "$(c GET /api/users | py 'print([x.get("oidc") for x in d if x["username"]=="oidcuser"])')" "[['dex']]"
OUT=$(vm "bash /tmp/oidc-browser.sh /api/login/oidc/dex '$DEXPW'")
check "second sign-in uses the same account" "$(c GET /api/users | py 'print(sum(1 for x in d if x["username"].startswith("oidcuser")))')" "1"
check "redirect outside the NAS refused" "$(vm "bash /tmp/oidc-browser.sh '/api/login/oidc/dex?next=//evil.example/' '$DEXPW'" | head -1)" "final=http://localhost:7070/"
# Login CSRF: a callback with a state started by another browser is refused.
STATE=$(vm "curl -s -o /dev/null -w '%{redirect_url}' http://localhost:7070/api/login/oidc/dex" | python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.stdin.read()).query)["state"][0])')
check "callback from another browser refused" "$(vm "curl -s -o /dev/null -w '%{redirect_url}' 'http://localhost:7070/api/login/oidc/callback?state=$STATE&code=x'")" "oidc_error="
check "provider removed" "$(c DELETE /api/security/oidc/dex)" '"ok":true'
check "account kept, link gone" "$(c GET /api/users | py 'print([x.get("oidc") for x in d if x["username"]=="oidcuser"])')" "[None]"
c DELETE /api/users/oidcuser >/dev/null
vm "sudo docker rm -f e2e-dex >/dev/null; rm -f /tmp/dex.yaml /tmp/oidc-browser.sh"

section "LDAP sign-in, administrators group, certificate checks"
LPASS="Ld-$(head -c 9 /dev/urandom | base64 | tr -d '/+=')9"
c POST /api/system/components/directory/install >/dev/null 2>&1
for _ in $(seq 1 120); do c GET /api/directory | py 'print(d["adInstalled"] and d["ldapInstalled"])' | grep -q True && break; sleep 5; done
LIP=$(./vm/vm.sh ssh "cat > /tmp/test-ldap.sh && sudo bash /tmp/test-ldap.sh '$LPASS'" < vm/test-ldap.sh 2>/dev/null | tail -1)
check "test LDAP server" "$LIP" "172.17."
CA=$(vm "sudo docker exec e2e-ldap cat /ca.pem")
JOIN="{\"type\":\"ldap\",\"uri\":\"ldaps://$LIP\",\"baseDn\":\"dc=strata,dc=test\",\"bindDn\":\"cn=admin,dc=strata,dc=test\",\"bindPassword\":\"$LPASS\",\"adminGroup\":\"nasadmins\",\"tlsVerify\":true"
check "unknown certificate refused" "$(job "$(c POST /api/directory/join -d "$JOIN}")")" "certificate"
check "bad CA refused" "$(c POST /api/directory/join -d "$JOIN,\"caCert\":\"hello\"}")" "PEM"
CAJ=$(printf '%s' "$CA" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')
check "joined over ldaps with its CA" "$(job "$(c POST /api/directory/join -d "$JOIN,\"caCert\":$CAJ}")")" "success"
check "sssd checks the certificate" "$(vm "sudo grep -E 'ldap_tls_(reqcert|cacert)' /etc/sssd/sssd.conf" | tr '\n' ' ')" "demand"
for _ in $(seq 1 20); do vm "id lea" | grep -q nasadmins && break; sleep 2; done
L=$(mktemp)
check "LDAP user signs in to the web interface" "$(curl -s -c "$L" -X POST -H 'X-Strata: 1' "$B/api/login" -d "{\"username\":\"paul\",\"password\":\"$LPASS\"}")" '"role":"user"'
check "…and uses it" "$(curl -s -b "$L" -H 'X-Strata: 1' "$B/api/me" | py 'print(d["username"])')" "paul"
check "wrong LDAP password refused" "$(curl -s -X POST -H 'X-Strata: 1' "$B/api/login" -d '{"username":"paul","password":"nope"}')" "invalid"
check "administrators group gives the admin role" "$(curl -s -X POST -H 'X-Strata: 1' "$B/api/login" -d "{\"username\":\"lea\",\"password\":\"$LPASS\"}")" '"role":"admin"'
check "LDAP user pairs the app with the password" "$(curl -s -X POST "$B/api/mobile/pair" -d "{\"username\":\"paul\",\"password\":\"$LPASS\",\"deviceName\":\"e2e phone\"}" | py 'print(d["token"][:6], d["user"]["username"])')" "stdev_ paul"
check "certificate checks can be turned off" "$(c PUT /api/directory/settings -d '{"adminGroup":"nasadmins","tlsVerify":false}')" '"ok":true'
check "LDAP left" "$(c POST /api/directory/leave -d '{}')" '"ok":true'
check "LDAP sessions ended" "$(curl -s -b "$L" -H 'X-Strata: 1' "$B/api/me")" "authentication required"
rm -f "$L"
for n in paul lea; do c DELETE "/api/users/$n" >/dev/null; done
vm "sudo docker rm -f e2e-ldap >/dev/null"

section "Built-in S3 service"
check "S3 component installed" "$(job "$(c POST /api/system/components/s3/install)")" "success"
check "binary pinned by checksum" "$(vm "/usr/local/lib/strata/versitygw --version | head -1")" "1.9.0"
check "invalid bucket name refused" "$(c PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[{"name":"Bad_Name","share":"Photos"}]}')" "invalid bucket name"
check "S3 on, Photos as a bucket" "$(c PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[{"name":"photos","share":"Photos"}]}')" '"ok":true'
for _ in $(seq 1 15); do vm "curl -sk -o /dev/null https://localhost:7380/" && break; sleep 1; done
check "running over HTTPS" "$(c GET /api/s3 | py 'print(d["running"], d["endpoint"].startswith("https://"))')" "True True"
AK=$(u POST /api/me/s3/keys -d '{"name":"e2e"}')
ALICE_A=$(echo "$AK" | py 'print(d["access"])') ALICE_S=$(echo "$AK" | py 'print(d["secret"])')
check "secret shown once" "$(u GET /api/me/s3 | grep -c "$ALICE_S")" "0"
check "the user sees the bucket read-write" "$(u GET /api/me/s3 | py 'print([(b["name"], b["perm"]) for b in d["buckets"]])')" "[('photos', 'rw')]"
AD=$(c POST /api/me/s3/keys -d '{"name":"admin e2e"}')
ADMIN_A=$(echo "$AD" | py 'print(d["access"])') ADMIN_S=$(echo "$AD" | py 'print(d["secret"])')
vm "command -v rclone >/dev/null || sudo apt-get install -y -qq rclone >/dev/null 2>&1"
rc() { # rc <access> <secret> <rclone args…>, inside the VM
  local a=$1 s=$2; shift 2
  vm "RCLONE_CONFIG_N_TYPE=s3 RCLONE_CONFIG_N_PROVIDER=Other RCLONE_CONFIG_N_ENDPOINT=https://localhost:7380 RCLONE_CONFIG_N_ACCESS_KEY_ID=$a RCLONE_CONFIG_N_SECRET_ACCESS_KEY=$s RCLONE_CONFIG_N_FORCE_PATH_STYLE=true RCLONE_S3_NO_CHECK_BUCKET=true rclone --no-check-certificate $* 2>&1"
}
sleep 1
check "put" "$(vm "head -c 7000000 /dev/urandom > /tmp/s3.bin"; rc "$ALICE_A" "$ALICE_S" copyto --s3-upload-cutoff 5M --s3-chunk-size 5M /tmp/s3.bin N:photos/s3/e2e.bin; rc "$ALICE_A" "$ALICE_S" ls N:photos/s3)" "7000000 e2e.bin"
check "the object is a file of the share, owned by the user" "$(vm "sudo stat -c '%U %s' /srv/strata/main/Photos/s3/e2e.bin")" "$USER_NAME 7000000"
rc "$ALICE_A" "$ALICE_S" copyto N:photos/s3/e2e.bin /tmp/s3.back >/dev/null
check "get" "$(vm "cmp /tmp/s3.bin /tmp/s3.back && echo same")" "same"
check "files from SMB listed" "$(rc "$ALICE_A" "$ALICE_S" lsf N:photos)" "via-smb.txt"
check "denied without permission on the share" "$(rc "$ADMIN_A" "$ADMIN_S" ls N:photos)" "AccessDenied"
check "wrong secret refused" "$(rc "$ALICE_A" "x$ALICE_S" ls N:photos)" "SignatureDoesNotMatch"
SH=$(c GET /api/shares | py 's=[x for x in d if x["name"]=="Photos"][0]; s["permissions"]["'"$ADMIN_USER"'"]="ro"; print(json.dumps(s))')
c PUT /api/shares/Photos -d "$SH" >/dev/null
for _ in $(seq 1 10); do rc "$ADMIN_A" "$ADMIN_S" ls N:photos/s3 | grep -q e2e.bin && break; sleep 1; done
check "read-only right follows the share" "$(rc "$ADMIN_A" "$ADMIN_S" ls N:photos/s3)" "e2e.bin"
check "…without writing" "$(rc "$ADMIN_A" "$ADMIN_S" copyto /tmp/s3.bin N:photos/s3/admin.bin)" "AccessDenied"
SH=$(c GET /api/shares | py 's=[x for x in d if x["name"]=="Photos"][0]; s["permissions"].pop("'"$ADMIN_USER"'", None); print(json.dumps(s))')
c PUT /api/shares/Photos -d "$SH" >/dev/null
check "delete" "$(rc "$ALICE_A" "$ALICE_S" deletefile N:photos/s3/e2e.bin; vm "ls /srv/strata/main/Photos/s3/ 2>&1 | wc -l")" "0"
check "requests in the audit log" "$(c GET '/api/s3/log?lines=100' | py 'print(sorted({(e["op"], e.get("user","")) for e in d if e["op"] in ("PutObject","CompleteMultipartUpload","GetObject")}))')" "$USER_NAME"
check "revoked key refused at once" "$(u DELETE "/api/me/s3/keys/$ALICE_A" >/dev/null; rc "$ALICE_A" "$ALICE_S" ls N:photos)" "InvalidAccessKeyId"
c DELETE "/api/me/s3/keys/$ADMIN_A" >/dev/null
check "S3 off" "$(c PUT /api/s3 -d '{"enabled":false,"port":7380,"buckets":[]}'; vm "systemctl is-active strata-s3.service")" "inactive"
vm "sudo rm -rf /srv/strata/main/Photos/s3 /srv/strata/main/Photos/.sgwtmp /tmp/s3.bin /tmp/s3.back"
