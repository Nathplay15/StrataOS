# shellcheck shell=bash
# Passkeys (WebAuthn) without a browser: a small software authenticator in
# Python (P-256 key, "none" attestation, run on the host with the
# `cryptography` module) registers a passkey for the admin on the name
# "localhost" (the test URL), signs in with it (discoverable flow), then sends
# a wrong signature through the VM's loopback with X-Forwarded-For so the
# address blocking counts it against a documentation address (198.51.100.7)
# instead of the test runner. Needs STRATA_URL on localhost (the default).
section "Passkeys (WebAuthn)"
PKA="$TMP/pk-auth.py" PKS="$TMP/pk-state.json" PKJ="$TMP/pk.jar"
ORIGIN=${B%/}
cat > "$PKA" <<'PYEOF'
import base64, hashlib, json, os, struct, sys
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec

def b64(b): return base64.urlsafe_b64encode(b).rstrip(b"=").decode()
def unb64(s): return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))
def head(major, n):
    if n < 24: return bytes([major << 5 | n])
    if n < 256: return bytes([major << 5 | 24, n])
    if n < 65536: return bytes([major << 5 | 25]) + struct.pack(">H", n)
    return bytes([major << 5 | 26]) + struct.pack(">I", n)
def cbor(v):  # the few CBOR types WebAuthn needs, keys given in canonical order
    if isinstance(v, int): return head(0, v) if v >= 0 else head(1, -1 - v)
    if isinstance(v, bytes): return head(2, len(v)) + v
    if isinstance(v, str): return head(3, len(v.encode())) + v.encode()
    return head(5, len(v)) + b"".join(cbor(k) + cbor(x) for k, x in v.items())

cmd, state, origin = sys.argv[1], sys.argv[2], sys.argv[3] if len(sys.argv) > 3 else ""
if cmd == "new":
    k = ec.generate_private_key(ec.SECP256R1())
    pem = k.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()).decode()
    json.dump({"key": pem, "id": b64(os.urandom(16)), "user": "", "count": 0}, open(state, "w"))
    sys.exit()
st = json.load(open(state))
key = serialization.load_pem_private_key(st["key"].encode(), None)
begin = json.load(sys.stdin)
pk = begin["publicKey"]
def client_data(typ): return json.dumps({"type": typ, "challenge": pk["challenge"], "origin": origin, "crossOrigin": False}).encode()
def auth_data(rp, flags, count, rest=b""): return hashlib.sha256(rp.encode()).digest() + bytes([flags]) + struct.pack(">I", count) + rest
if cmd == "create":
    st["user"] = pk["user"]["id"]
    n = key.public_key().public_numbers()
    cose = cbor({1: 2, 3: -7, -1: 1, -2: n.x.to_bytes(32, "big"), -3: n.y.to_bytes(32, "big")})
    cid = unb64(st["id"])
    att = bytes(16) + struct.pack(">H", len(cid)) + cid + cose
    ao = cbor({"fmt": "none", "attStmt": {}, "authData": auth_data(pk["rp"]["id"], 0x45, 0, att)})
    resp = {"clientDataJSON": b64(client_data("webauthn.create")), "attestationObject": b64(ao), "transports": ["internal"]}
else:  # get [bad]
    st["count"] += 1
    ad = auth_data(pk["rpId"], 0x05, st["count"])
    cd = client_data("webauthn.get")
    signed = ad + hashlib.sha256(cd).digest()
    if len(sys.argv) > 4: signed = b"x" + signed[1:]
    sig = key.sign(signed, ec.ECDSA(hashes.SHA256()))
    resp = {"clientDataJSON": b64(cd), "authenticatorData": b64(ad), "signature": b64(sig), "userHandle": st["user"]}
json.dump(st, open(state, "w"))
print(json.dumps({"flow": begin["flow"], "credential": {"id": st["id"], "rawId": st["id"], "type": "public-key", "response": resp, "clientExtensionResults": {}}}))
PYEOF
pk_login() { # pk_login [bad] -> finish body of a discoverable sign-in
  curl -s -X POST -H 'X-Strata: 1' "$B/api/login/passkey/begin" -d '{}' | python3 -I "$PKA" get "$PKS" "$ORIGIN" "$@"
}

check "available on a name (localhost)" "$(curl -s "$B/api/login/passkey")" '"available":true'
check "refused on an IP address" "$(curl -s -X POST -H 'X-Strata: 1' -H "Host: 127.0.0.1:${ORIGIN##*:}" "$B/api/login/passkey/begin" -d '{}')" "strataos.local"
check "registration needs the password" "$(c POST /api/me/passkeys/begin -d '{"password":"wrong","name":"e2e"}')" "incorrect password"
if python3 -c 'import cryptography' 2>/dev/null; then
  python3 -I "$PKA" new "$PKS"
  REG=$(c POST /api/me/passkeys/begin -d "{\"password\":\"$ADMIN_PASS\",\"name\":\"e2e key\"}")
  check "registration options (user verification required)" "$(echo "$REG" | py 'print(d["publicKey"]["rp"]["id"], d["publicKey"]["authenticatorSelection"]["userVerification"])')" "localhost required"
  BODY=$(echo "$REG" | python3 -I "$PKA" create "$PKS" "$ORIGIN")
  check "passkey registered" "$(c POST /api/me/passkeys/finish -d "$BODY")" '"rpId":"localhost"'
  check "registration challenge single use" "$(c POST /api/me/passkeys/finish -d "$BODY")" "expired"
  PKID=$(c GET /api/me/passkeys | py 'print([k["id"] for k in d["passkeys"] if k["name"]=="e2e key"][0])')
  check "passkey count on the Users page" "$(c GET /api/users | py "print([x['passkeys'] for x in d if x['username']=='$ADMIN_USER'])")" "[1]"
  check "discoverable sign-in" "$(pk_login | curl -s -c "$PKJ" -X POST -H 'X-Strata: 1' "$B/api/login/passkey/finish" -d @-)" "\"username\":\"$ADMIN_USER\""
  check "session works" "$(curl -s -b "$PKJ" -H 'X-Strata: 1' "$B/api/me")" "\"username\":\"$ADMIN_USER\""
  check "last use recorded" "$(c GET /api/me/passkeys | py "print([bool(k.get('lastUsed')) for k in d['passkeys'] if k['id']=='$PKID'])")" "[True]"
  # Wrong signatures, seen from 198.51.100.7 (loopback forwarder headers), counted by the address blocking.
  c PUT /api/guard -d '{"enabled":true,"maxFailures":2,"windowMin":10,"blockMin":5,"neverLan":true,"allow":["10.0.2.2/32"],"geo":{"enabled":false}}' >/dev/null
  for _ in 1 2; do
    BAD=$(pk_login bad | base64 -w0)
    R=$(vm "echo $BAD | base64 -d | curl -s -X POST -H 'X-Strata: 1' -H 'Host: localhost:${ORIGIN##*:}' -H 'X-Forwarded-For: 198.51.100.7' --data-binary @- http://127.0.0.1:7070/api/login/passkey/finish")
  done
  check "wrong signature refused" "$R" "passkey not accepted"
  check "failed passkey sign-ins block the address" "$(c GET /api/guard | py 'print([(b["ip"], b["service"]) for b in d["blocks"]])')" "('198.51.100.7', 'web')"
  c POST /api/guard/unblock -d '{"ip":"198.51.100.7"}' >/dev/null
  c PUT /api/guard -d '{"enabled":false,"maxFailures":5,"windowMin":10,"blockMin":60,"neverLan":true,"geo":{"enabled":false}}' >/dev/null
  check "renamed" "$(c PUT "/api/me/passkeys/$PKID" -d '{"name":"e2e renamed"}')" '"ok":true'
  check "deleted" "$(c DELETE "/api/me/passkeys/$PKID")" '"ok":true'
  check "deleted passkey refused" "$(pk_login | curl -s -X POST -H 'X-Strata: 1' "$B/api/login/passkey/finish" -d @-)" "passkey not accepted"
  check "no passkey left" "$(c GET /api/me/passkeys | py 'print(len(d["passkeys"]))')" "0"
  check "password sign-in still works" "$(curl -s -X POST -H 'X-Strata: 1' "$B/api/login" -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" "\"username\":\"$ADMIN_USER\""
else
  echo "  (python3 cryptography missing on the host: round trip covered by the Go test only)"
fi
check "admin removes passkeys of an account" "$(c DELETE "/api/users/$ADMIN_USER/passkeys")" '"ok":true'
check "user cannot remove another's passkeys" "$(u DELETE "/api/users/$ADMIN_USER/passkeys")" "administrator rights required"
rm -f "$PKA" "$PKS" "$PKJ"
