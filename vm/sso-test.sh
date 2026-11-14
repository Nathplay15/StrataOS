#!/bin/bash
# Single sign-on of the apps on one VM (2.5 GB): the NAS as OpenID provider
# and the forward authentication of the app domains, with real clients.
#   - Grafana from the App Center, "Sign in with StrataOS" turned on (the
#     override compose file, the container calling the NAS through
#     host.docker.internal): sign-in of alice through Grafana's own flow,
#     consent, Grafana account created; bob (not in the allowed group)
#     refused; Grafana's sign-out ending the NAS session (RP-initiated
#     logout, registered return address only);
#   - a client played with curl (confidential, PKCE): code, tokens, userinfo,
#     refresh, code reuse, revocation by the administrator and by the user,
#     discovery for containers (split endpoints);
#   - a header-echo app (python in the VM) on an app domain protected by
#     forward authentication: sign-in redirect, Remote-User/Groups/Email
#     passed and spoofed copies stripped (protected and open domains),
#     group refusal, NAS sign-out ending the app session;
#   - configuration export: client secret only in the sealed part.
# The browser is curl inside the VM (cookie jars), the issuer is
# http://127.0.0.1:7070 (the address both the browser and the apps reach).
# About 15 minutes (Grafana image download). Usage:
#   make deb && flock -o /home/nathan/Documents/StrataOS-wt/.vm.lock timeout 60m ./vm/sso-test.sh
#   KEEP=1 keeps the VM.
# Ports: SSO_WEB (7470), SSO_SSH (2570), SSO_HTTPS (7770).
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${DEB:-dist/strataos_$(cat VERSION)_amd64.deb}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
T=$(mktemp -d)
PASSES=0 FAILS=0
START=$(date +%s)
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:600}" ;; esac; }
refute() { case "$2" in *"$3"*) ko "$1" "unexpected '$3' in: ${2:0:600}" ;; *) ok "$1" ;; esac; }
section() { printf '\n\033[1;36m== %s  (%d min)\033[0m\n' "$1" $(( ($(date +%s) - START) / 60 )); }
V() { VM_STATE=$PWD/vm/state-sso VM_HOSTNAME=sso VM_WEB_PORT=${SSO_WEB:-7470} VM_SSH_PORT=${SSO_SSH:-2570} VM_HTTPS_PORT=${SSO_HTTPS:-7770} VM_SMB_PORT=${SSO_SMB:-4770} SEED_PORT=${SSO_SEED:-9070} \
  VM_EXTRA_PORTS="" VM_MEM=2560 VM_CPUS=2 VM_DISKS=1 VM_DISK_SIZE=2G ./vm/vm.sh "$@" | tr -d '\r'; }
PORT=${SSO_WEB:-7470}
api() { local m=$1 p=$2; shift 2; curl -s -m 120 -b "$T/ck" -c "$T/ck" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "http://localhost:$PORT$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
job() { local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 ${2:-300}); do [ "$(api GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null)" = running ] || break; sleep 3; done
  api GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-800:])'; }
cleanup_vm() { [ -n "${KEEP:-}" ] && return; V destroy >/dev/null 2>&1; rm -rf "$T"; }
diag() { [ "$FAILS" -eq 0 ] && return; printf '\n--- journal ---\n'; V ssh 'sudo journalctl -u strata --no-pager -n 40 -o cat; sudo docker logs --tail 30 grafana-grafana-1 2>&1' 2>&1 | tail -70; }
trap 'diag; cleanup_vm; exit 1' INT TERM

# The browser: curl in the VM with a cookie jar per person.
#   B <jar> <curl args…>  prints the body; BH prints "code location".
B() { local j=$1; shift; V ssh "curl -s -m 30 -b /tmp/$j.jar -c /tmp/$j.jar $(printf '%q ' "$@")"; }
BH() { local j=$1; shift; V ssh "curl -s -m 30 -o /dev/null -w '%{http_code} %{redirect_url}' -b /tmp/$j.jar -c /tmp/$j.jar $(printf '%q ' "$@")"; }
NAS=http://127.0.0.1:7070
nas_login() { B "$1" -X POST -H 'Content-Type: application/json' -d "{\"username\":\"$1\",\"password\":\"$2\"}" "$NAS/api/login" >/dev/null; }
# approve <jar> <authorize url> [yes|no]: the sign-in page of the NAS, by hand.
approve() {
  local loc id
  loc=$(BH "$1" "$2" | cut -d' ' -f2)
  id=${loc##*/sso/r/}
  [ "$id" != "$loc" ] || { echo "no-request: $loc"; return; }
  B "$1" -X POST -H "Origin: $NAS" -H 'X-Strata: 1' -H 'Content-Type: application/json' -d "{\"approve\":${3:-true}}" "$NAS/api/sso/requests/$id" | py 'print(d.get("redirect") or d.get("error"))'
}
view() { local loc; loc=$(BH "$1" "$2" | cut -d' ' -f2); B "$1" "$NAS/api/sso/requests/${loc##*/sso/r/}"; }

section "VM and accounts"
mkdir -p vm/images
V destroy >/dev/null 2>&1
if ! { V create >"$T/create.log" 2>&1 && V deploy "$DEB" >>"$T/create.log" 2>&1; }; then tail -20 "$T/create.log"; cleanup_vm; exit 1; fi
api POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"sso\"}" >/dev/null
api POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
check "group famille" "$(api POST /api/groups -d '{"name":"famille"}')" '"ok":true'
check "alice (famille)" "$(api POST /api/users -d '{"username":"alice","password":"Alice-Pass-1","fullName":"Alice Martin","email":"alice@example.com","role":"user","groups":["famille"]}')" '"ok":true'
check "bob" "$(api POST /api/users -d '{"username":"bob","password":"Bob-Pass-12","fullName":"Bob","email":"bob@example.com","role":"user"}')" '"ok":true'

section "Settings"
check "plain issuer refused" "$(api PUT /api/sso -d '{"enabled":true,"issuer":"https://nas.example.com/path"}')" "no path"
check "SSO on" "$(api PUT /api/sso -d "{\"enabled\":true,\"issuer\":\"$NAS\"}")" '"ok":true'
DISC=$(V ssh "curl -s $NAS/.well-known/openid-configuration")
check "discovery" "$(echo "$DISC" | py 'print(d["issuer"], d["token_endpoint"], d["code_challenge_methods_supported"])')" "$NAS $NAS/api/sso/token ['S256']"
check "keys published (active and next)" "$(V ssh "curl -s $NAS/api/sso/jwks" | py 'print(len(d["keys"]), d["keys"][0]["alg"])')" "2 RS256"

section "Grafana from the App Center"
check "Grafana installed" "$(job "$(api POST /api/apps/grafana/install -d '{"name":"grafana","values":{"WEB_PORT":"3004","ADMIN_PASSWORD":"Grafana-Admin-1"}}')" 400)" "success"
R=$(api PUT /api/sso/apps/grafana -d '{"enabled":true}')
CID=$(echo "$R" | py 'print(d["client"])')
check "sign in with StrataOS turned on" "$(job "$(echo "$R" | py 'print(json.dumps(d["job"]))')")" "success"
check "override file root only" "$(V ssh 'sudo stat -c %a /var/lib/strata/stacks/grafana/compose.strata.yaml')" "600"
check "secret not sent back" "$(api GET /api/sso | py "print([c['hasSecret'] and c.get('secret','') == '' for c in d['clients'] if c['id']=='$CID'])")" "[True]"
check "access: famille only" "$(api PUT "/api/sso/clients/$CID" -d '{"name":"Grafana","redirectUris":["http://127.0.0.1:3004/login/generic_oauth"],"postLogoutUris":["http://127.0.0.1:3004/login"],"requirePkce":true,"groups":["famille"]}')" '"ok":true'
for _ in $(seq 1 60); do V ssh "curl -sf -o /dev/null http://127.0.0.1:3004/api/health" && break; sleep 3; done
check "container reaches the NAS (host.docker.internal)" "$(V ssh 'sudo docker exec grafana-grafana-1 wget -q -O - http://host.docker.internal:7070/.well-known/openid-configuration')" '"token_endpoint":"http://host.docker.internal:7070/api/sso/token"'
check "split discovery keeps the public issuer" "$(V ssh 'sudo docker exec grafana-grafana-1 wget -q -O - http://host.docker.internal:7070/.well-known/openid-configuration')" "\"authorization_endpoint\":\"$NAS/api/sso/authorize\""

nas_login alice Alice-Pass-1
GO=$(BH alice http://127.0.0.1:3004/login/generic_oauth)
check "Grafana sends to the NAS" "$GO" "302 $NAS/api/sso/authorize?"
check "PKCE S256 from Grafana" "$GO" "code_challenge_method=S256"
AUTH=${GO#302 }
check "consent asked the first time" "$(view alice "$AUTH" | py 'print(d["consent"], d["client"], d["user"])')" "True Grafana alice"
GO=$(BH alice http://127.0.0.1:3004/login/generic_oauth); AUTH=${GO#302 }
CB=$(approve alice "$AUTH")
check "code sent back to Grafana" "$CB" "http://127.0.0.1:3004/login/generic_oauth?code="
check "Grafana signs alice in" "$(BH alice "$CB")" "302"
check "Grafana user is alice" "$(B alice http://127.0.0.1:3004/api/user | py 'print(d["login"], d["email"], d["name"])')" "alice alice@example.com Alice Martin"
check "alice's apps list Grafana" "$(B alice -H 'X-Strata: 1' "$NAS/api/me/sso" | py 'print([a["name"] for a in d])')" "['Grafana']"

nas_login bob Bob-Pass-12
GO=$(BH bob http://127.0.0.1:3004/login/generic_oauth); AUTH=${GO#302 }
check "bob refused (not in famille)" "$(view bob "$AUTH" | py 'print(d.get("denied",""))')" "may not use Grafana"
GO=$(BH bob http://127.0.0.1:3004/login/generic_oauth); AUTH=${GO#302 }
CB=$(approve bob "$AUTH")
check "refusal sent to Grafana" "$CB" "error=access_denied"
BH bob "$CB" >/dev/null
check "bob has no Grafana session" "$(B bob -o /dev/null -w '%{http_code}' http://127.0.0.1:3004/api/user)" "401"

# Grafana's sign-out goes through the NAS's end_session endpoint.
OUT=$(BH alice http://127.0.0.1:3004/logout)
check "Grafana sign-out goes to the NAS" "$OUT" "$NAS/api/sso/logout?"
L=$(BH alice "${OUT#302 }")
check "sign-out page" "$L" "/sso/logout/"
LID=${L##*/sso/logout/}
check "NAS session ended, back to Grafana" "$(B alice -X POST -H "Origin: $NAS" -H 'X-Strata: 1' "$NAS/api/sso/logouts/$LID" | py 'print(d["redirect"])')" "http://127.0.0.1:3004/login"
check "NAS session gone" "$(B alice -o /dev/null -w '%{http_code}' -H 'X-Strata: 1' "$NAS/api/me")" "401"
check "unregistered return address refused" "$(BH alice "$NAS/api/sso/logout?client_id=$CID&post_logout_redirect_uri=https://evil.example.com/")" "400"

section "Client played by curl (tokens, refresh, revocation)"
R=$(api POST /api/sso/clients -d '{"name":"Test RP","redirectUris":["http://127.0.0.1:9999/cb"],"requirePkce":true,"allUsers":true}')
RP=$(echo "$R" | py 'print(d["client"]["id"])'); RPS=$(echo "$R" | py 'print(d["secret"])')
VER=verifier-verifier-verifier-verifier-verifier-1
CH=$(printf %s "$VER" | openssl dgst -sha256 -binary | basenc --base64url | tr -d '=')
nas_login alice Alice-Pass-1
A="$NAS/api/sso/authorize?client_id=$RP&redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fcb&response_type=code&scope=openid+profile+email+groups+offline_access&state=s1&nonce=n1&code_challenge=$CH&code_challenge_method=S256"
check "unregistered redirect: page, no redirect" "$(BH alice "${A/9999%2Fcb/9999%2Fevil}")" "400 "
check "plain PKCE refused" "$(BH alice "${A/S256/plain}")" "error=invalid_request"
CB=$(approve alice "$A"); CODE=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["code"][0])' "$CB")
TOK=$(V ssh "curl -s -u $RP:$RPS -d grant_type=authorization_code -d code=$CODE -d redirect_uri=http://127.0.0.1:9999/cb -d code_verifier=$VER $NAS/api/sso/token")
AT=$(echo "$TOK" | py 'print(d["access_token"])'); RT=$(echo "$TOK" | py 'print(d["refresh_token"])')
check "tokens" "$(echo "$TOK" | py 'print(d["token_type"], d["expires_in"], len(d["id_token"].split(".")))')" "Bearer 3600 3"
check "ID token claims" "$(echo "$TOK" | py 'import base64; p=d["id_token"].split(".")[1]; c=json.loads(base64.urlsafe_b64decode(p+"==")); print(c["iss"], c["aud"], c["nonce"], c["preferred_username"], c["groups"])')" "$NAS $RP n1 alice ['famille']"
check "userinfo" "$(V ssh "curl -s -H 'Authorization: Bearer $AT' $NAS/api/sso/userinfo" | py 'print(d["preferred_username"], d["email"])')" "alice alice@example.com"
check "code reuse refused" "$(V ssh "curl -s -u $RP:$RPS -d grant_type=authorization_code -d code=$CODE -d redirect_uri=http://127.0.0.1:9999/cb -d code_verifier=$VER $NAS/api/sso/token")" "invalid_grant"
check "its tokens revoked" "$(V ssh "curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer $AT' $NAS/api/sso/userinfo")" "401"
CB=$(approve alice "$A"); CODE=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["code"][0])' "$CB")
check "wrong verifier refused" "$(V ssh "curl -s -u $RP:$RPS -d grant_type=authorization_code -d code=$CODE -d redirect_uri=http://127.0.0.1:9999/cb -d code_verifier=${VER}x $NAS/api/sso/token")" "PKCE"
CB=$(approve alice "$A"); CODE=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["code"][0])' "$CB")
check "wrong secret refused" "$(V ssh "curl -s -u $RP:nope -d grant_type=authorization_code -d code=$CODE $NAS/api/sso/token")" "invalid_client"
CB=$(approve alice "$A"); CODE=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["code"][0])' "$CB")
TOK=$(V ssh "curl -s -d client_id=$RP -d client_secret=$RPS -d grant_type=authorization_code -d code=$CODE -d redirect_uri=http://127.0.0.1:9999/cb -d code_verifier=$VER $NAS/api/sso/token")
AT=$(echo "$TOK" | py 'print(d["access_token"])'); RT=$(echo "$TOK" | py 'print(d["refresh_token"])')
TOK2=$(V ssh "curl -s -u $RP:$RPS -d grant_type=refresh_token -d refresh_token=$RT $NAS/api/sso/token")
check "refresh (client_secret_post first, basic now)" "$(echo "$TOK2" | py 'print("access_token" in d)')" "True"
check "refresh token single use" "$(V ssh "curl -s -u $RP:$RPS -d grant_type=refresh_token -d refresh_token=$RT $NAS/api/sso/token")" "already used"
CB=$(approve alice "$A"); CODE=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["code"][0])' "$CB")
AT=$(V ssh "curl -s -u $RP:$RPS -d grant_type=authorization_code -d code=$CODE -d redirect_uri=http://127.0.0.1:9999/cb -d code_verifier=$VER $NAS/api/sso/token" | py 'print(d["access_token"])')
check "admin ends the sessions" "$(api POST "/api/sso/clients/$RP/revoke")" '"ok":true'
check "token dead after admin revocation" "$(V ssh "curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer $AT' $NAS/api/sso/userinfo")" "401"
CB=$(approve alice "$A"); CODE=$(python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)["code"][0])' "$CB")
AT=$(V ssh "curl -s -u $RP:$RPS -d grant_type=authorization_code -d code=$CODE -d redirect_uri=http://127.0.0.1:9999/cb -d code_verifier=$VER $NAS/api/sso/token" | py 'print(d["access_token"])')
check "user revokes the app" "$(B alice -X DELETE -H "Origin: $NAS" -H 'X-Strata: 1' "$NAS/api/me/sso/$RP")" '"ok":true'
check "token dead after user revocation" "$(V ssh "curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer $AT' $NAS/api/sso/userinfo")" "401"
check "consent asked again" "$(view alice "$A" | py 'print(d["consent"])')" "True"
check "cross-site approval refused" "$(BH alice -X POST -H 'Origin: http://evil.test' -H 'Content-Type: application/json' -d '{"approve":true}' "$NAS/api/sso/requests/0123456789abcdef0123456789abcdef")" "403"
check "audit: sign-ins logged" "$(api GET /api/system/events)" "Signed in to Grafana"

section "Forward authentication (header app on an app domain)"
V ssh "sudo tee /tmp/echo.py >/dev/null" <<'EOF'
import http.server, json
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        b = json.dumps({k: v for k, v in self.headers.items()}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", 18090), H).serve_forever()
EOF
V ssh "sudo systemd-run --unit=e2e-echo python3 /tmp/echo.py" >/dev/null 2>&1
check "app domains on" "$(api PUT /api/remote/domains/settings -d '{"enabled":true}')" '"ok":true'
DP=$(api POST /api/remote/domains -d '{"host":"echo.sso.test","kind":"url","target":"http://127.0.0.1:18090"}' | py 'print(d["id"])')
api POST /api/remote/domains -d '{"host":"open.sso.test","kind":"url","target":"http://127.0.0.1:18090"}' >/dev/null
check "protection on" "$(api PUT "/api/sso/domains/$DP" -d '{"enabled":true}')" '"ok":true'
FWD=fwd-$DP
check "access: famille only" "$(api PUT "/api/sso/clients/$FWD" -d '{"name":"echo","trusted":true,"groups":["famille"]}')" '"ok":true'
RS=(--resolve echo.sso.test:443:127.0.0.1 --resolve open.sso.test:443:127.0.0.1 -k)
SPOOF=(-H 'Remote-User: admin' -H 'Remote-Groups: admins' -H 'X-Forwarded-User: admin' -H 'X-Webauth-User: admin')
check "open domain: spoofed headers stripped" "$(B anon "${RS[@]}" "${SPOOF[@]}" https://open.sso.test/ | py 'print(sorted(k for k in d if k.lower().startswith(("remote","x-forwarded-user","x-webauth"))))')" "[]"
G=$(BH alice "${RS[@]}" "${SPOOF[@]}" https://echo.sso.test/page?x=1)
check "protected domain sends to the NAS (app not reached)" "$G" "302 $NAS/api/sso/authorize?client_id=$FWD"
check "scripts get 401" "$(BH alice "${RS[@]}" -H 'Sec-Fetch-Mode: cors' https://echo.sso.test/api)" "401"
nas_login alice Alice-Pass-1
CB=$(approve alice "${G#302 }")
check "callback on the protected host" "$CB" "https://echo.sso.test/.strata-sso/callback?code="
check "back to the page asked" "$(BH alice "${RS[@]}" "$CB")" "302 https://echo.sso.test/page?x=1"
H=$(B alice "${RS[@]}" "${SPOOF[@]}" https://echo.sso.test/page)
check "identity passed" "$(echo "$H" | py 'print(d.get("Remote-User"), d.get("Remote-Groups"), d.get("Remote-Email"), d.get("Remote-Name"))')" "alice famille alice@example.com Alice Martin"
check "spoofed copies gone" "$(echo "$H" | py 'print(d.get("X-Forwarded-User"), d.get("X-Webauth-User"))')" "None None"
refute "session cookie not passed to the app" "$(echo "$H" | py 'print(d.get("Cookie",""))')" "strata_fwd"
nas_login bob Bob-Pass-12
G=$(BH bob "${RS[@]}" https://echo.sso.test/)
check "bob refused" "$(approve bob "${G#302 }")" "error=access_denied"
B alice -X POST -H "Origin: $NAS" -H 'X-Strata: 1' "$NAS/api/logout" >/dev/null
check "NAS sign-out ends the app session" "$(BH alice "${RS[@]}" https://echo.sso.test/page)" "302 $NAS/api/sso/authorize"
check "protection off" "$(api PUT "/api/sso/domains/$DP" -d '{"enabled":false}')" '"ok":true'
check "open again, still no identity headers" "$(B anon "${RS[@]}" "${SPOOF[@]}" https://echo.sso.test/ | py 'print(d.get("Remote-User"))')" "None"

section "Configuration export"
EXP=$(api GET /api/config/export)
check "SSO section exported" "$EXP" "\"issuer\":\"$NAS\""
refute "client secret not in the readable part" "$EXP" "$RPS"

section "Clean up"
V ssh "sudo systemctl stop e2e-echo; sudo docker image prune -af >/dev/null" >/dev/null 2>&1
printf '\n%s passed, %s failed  (%d min)\n' "$PASSES" "$FAILS" $(( ($(date +%s) - START) / 60 ))
diag
cleanup_vm
[ "$FAILS" -eq 0 ]
