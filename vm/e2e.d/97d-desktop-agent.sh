# shellcheck shell=bash
# The sync engine of the desktop app run as its sidecar ("strataos-sync
# agent", desktop/internal/agent) against the NAS: its token, the NAS API
# proxy (device token added, the window's credentials never passed on),
# the web interface proxy (signed in with the device token, session kept by
# the agent), uploads and downloads of the file browser, and the file
# manager menus' "which NAS file is this". The client runs on the host
# (built with Go) and reaches the VM on its HTTPS port (VM_HTTPS_PORT).
section "Desktop app engine (agent)"
HTTPS="https://localhost:${VM_HTTPS_PORT:-7443}"
GO=$(command -v go || echo "$HOME/.local/opt/go/bin/go")
S="$TMP/strataos-sync-agent"
AC="$TMP/agent-config" AL="$TMP/agent-local"
(cd desktop && CGO_ENABLED=0 "$GO" build -o "$S" ./cmd/strataos-sync) 2>&1 | tail -3
mkdir -p "$AL"

PR=$(u POST /api/me/devices/pairing)
check "paired (main account)" "$("$S" --config "$AC" pair --server "$HTTPS" --code "$(echo "$PR" | py 'print(d["code"])')" --fingerprint "$(echo "$PR" | py 'print(d["fingerprint"])')" --name e2e-agent 2>&1)" "paired with"
check "headless accounts list" "$("$S" --config "$AC" accounts 2>&1)" "main"

ATOK=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
coproc AGENT { STRATAOS_AGENT_TOKEN=$ATOK exec "$S" --config "$AC" agent 2>"$TMP/agent.log"; }
read -r -t 20 HELLO <&"${AGENT[0]}"
APORT=$(echo "$HELLO" | py 'print(d["port"])' 2>/dev/null)
check "agent started (first line)" "$HELLO" '"agent":1'
A="http://127.0.0.1:$APORT"
ag() { local m=$1 p=$2; shift 2; curl -s -X "$m" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' "$A$p" "$@"; }

check "no token: refused" "$(curl -s -o /dev/null -w '%{http_code}' "$A/v1/state")" "403"
check "other web page: refused" "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $ATOK" -H 'Origin: https://evil.example' "$A/v1/state")" "403"
ST=$(ag GET /v1/state)
check "state: paired, no device token" "$(echo "$ST" | py 'a=d["accounts"][0]; print(a["paired"], a["config"]["user"], "stdev_" in json.dumps(d))')" "True $USER_NAME False"

# The NAS API through the agent, as the window calls it.
check "files of the NAS through the agent" "$(ag GET "/v1/accounts/main/nas/api/files?path=$P" | py 'print(d["path"])')" "$P"
check "token in the address for pictures (GET only)" "$(curl -s -o /dev/null -w '%{http_code}' "$A/v1/accounts/main/nas/api/mobile/info?st=$ATOK")" "200"
check "the device's own rights apply" "$(ag GET /v1/accounts/main/nas/api/users -o /dev/null -w '%{http_code}')" "403"
check "only the API is forwarded" "$(ag GET /v1/accounts/main/nas/index.html -o /dev/null -w '%{http_code}')" "403"

# Uploads and downloads of the file browser (resumable uploads, then a copy back).
mkdir -p "$AL/up/sub"
head -c 3000000 /dev/urandom > "$AL/up/big.bin"
echo hello > "$AL/up/sub/small.txt"
ag POST /v1/accounts/main/upload -d "{\"paths\":[\"$AL/up\"],\"folder\":\"$P\"}" >/dev/null
for _ in $(seq 1 60); do st=$(ag GET /v1/transfers | py 'print(d[0]["state"])'); [ "$st" != running ] && break; sleep 1; done
check "folder uploaded" "$(ag GET /v1/transfers | py 'print(d[0]["state"], d[0]["files"])')" "done 2"
check "tree kept on the NAS" "$(u GET "/api/files?path=$P/up/sub" | py 'print([e["name"] for e in d["entries"]])')" "small.txt"
check "existing file not replaced without asking" "$(ag POST /v1/accounts/main/upload -d "{\"paths\":[\"$AL/up/sub/small.txt\"],\"folder\":\"$P/up/sub\"}" >/dev/null; sleep 3; ag GET /v1/transfers | py 'print(d[0]["state"], d[0].get("error",""))')" "error"
ag POST /v1/accounts/main/download -d "{\"path\":\"$P/up/big.bin\",\"dest\":\"$AL/back.bin\"}" >/dev/null
for _ in $(seq 1 60); do [ -f "$AL/back.bin" ] && break; sleep 1; done
check "downloaded identical" "$(cmp "$AL/up/big.bin" "$AL/back.bin" && echo same)" "same"
ag POST /v1/transfers/clear >/dev/null

# The web interface in a window: one-time address, session held by the agent.
WURL=$(ag POST /v1/accounts/main/web -d '{"path":"/files"}' | py 'print(d["url"])')
WJ="$TMP/agent-web.jar"
check "web interface: signed in by the device" "$(curl -s -c "$WJ" -o /dev/null -w '%{http_code} %{redirect_url}' "$WURL")" "302"
WB=${WURL%%/__strataos*}
check "web pages served through the agent" "$(curl -s -b "$WJ" "$WB/")" "<div id=\"root\">"
check "web API as the user, session kept by the agent" "$(curl -s -b "$WJ" "$WB/api/me" | py 'print(d["username"])')" "$USER_NAME"
check "web change accepted (origin rewritten)" "$(curl -s -b "$WJ" -X POST -H 'Content-Type: application/json' -H "Origin: $WB" "$WB/api/files/mkdir" -d "{\"path\":\"$P/up/from-web\"}")" '"ok":true'
check "no NAS cookie given to the webview" "$(grep -c strata_session "$WJ" || true)" "0"
check "one-time address used once" "$(curl -s -o /dev/null -w '%{http_code}' "$WURL")" "403"
check "without the proxy cookie: refused" "$(curl -s -o /dev/null -w '%{http_code}' "$WB/api/me")" "403"

# File manager menus: a synchronized file to its NAS path.
ag POST /v1/accounts/main/pairs -d "{\"local\":\"$AL/sync\",\"remote\":\"$P/up\"}" >/dev/null
mkdir -p "$AL/sync/sub" && echo x > "$AL/sync/sub/n.txt"
check "local file resolved to the NAS" "$(ag POST /v1/resolve -d "{\"path\":\"$AL/sync/sub/n.txt\"}" | py 'print(d["remote"])')" "$P/up/sub/n.txt"
check "outside synchronized folders" "$(ag POST /v1/resolve -d "{\"path\":\"$AL/up/big.bin\"}" -o /dev/null -w '%{http_code}')" "404"

# Signing out revokes the device: the agent's proxies stop with it.
check "signed out" "$(ag POST /v1/accounts/main/signout)" '"ok":true'
check "NAS proxy closed" "$(ag GET "/v1/accounts/main/nas/api/files?path=$P" -o /dev/null -w '%{http_code}')" "404"
check "web session ended" "$(curl -s -b "$WJ" -o /dev/null -w '%{http_code}' "$WB/api/me")" "40"
exec {AGENT[1]}>&-
wait "$AGENT_PID" 2>/dev/null
check "agent quits when the app closes its input" "$(kill -0 "$AGENT_PID" 2>&1 || echo gone)" "gone"
u POST /api/files/delete -d "{\"paths\":[\"$P/up\"]}" >/dev/null
rm -rf "$AC" "$AL" "$S"
