# shellcheck shell=bash
# App Center community catalogs: a test catalog served on the NAS itself
# (http on 127.0.0.1 is allowed for local mirrors), pinned by checksum, then
# the same catalog signed. Strict checks (an app reading /etc/shadow left
# out), the permission review (privileged, Docker socket, a path outside
# /srv/strata needing an approval), an install, a catalog change waiting for
# approval, an update, removal of the source. Pulls traefik/whoami.
section "App Center: community catalogs"
CP=${CATALOG_PORT:-8879}
CD=/tmp/e2e-catalog

# catalog VERSION: a test index (whoami, an app with a host path, a risky one, an invalid one).
catalog() {
  python3 - "$1" <<'EOF'
import json, sys
v = sys.argv[1]
who = "services:\n  whoami:\n    image: traefik/whoami:latest\n    restart: unless-stopped\n    ports:\n      - \"${WEB_PORT}:80\"\n"
host = "services:\n  hostinfo:\n    image: traefik/whoami:latest\n    ports:\n      - \"${WEB_PORT}:80\"\n    volumes:\n      - /etc/hostname:/hostname:ro\n      - ${DATA_DIR}:/data\n"
risky = "services:\n  r:\n    image: traefik/whoami:latest\n    privileged: true\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n"
evil = "services:\n  e:\n    image: traefik/whoami:latest\n    env_file: /etc/shadow\n"
port = lambda d: [{"key": "WEB_PORT", "label": "Web port", "default": d, "type": "port"}]
print(json.dumps({"format": 1, "name": "E2E catalog", "maintainer": "tests", "apps": [
  {"id": "e2e-whoami", "name": "E2E Whoami", "version": v, "maintainer": "tests", "category": "Tools", "webPort": "WEB_PORT", "vars": port("8199"), "compose": who},
  {"id": "e2e-hostinfo", "name": "E2E Host info", "version": "1", "maintainer": "tests", "webPort": "WEB_PORT",
   "vars": port("8198") + [{"key": "DATA_DIR", "label": "Data", "default": "/srv/strata/main/e2e-hostinfo", "type": "path"}], "compose": host},
  {"id": "e2e-risky", "name": "E2E Risky", "version": "1", "maintainer": "tests", "compose": risky},
  {"id": "e2e-evil", "name": "E2E Evil", "version": "1", "maintainer": "tests", "compose": evil}]}, indent=1))
EOF
}
put_catalog() { # put_catalog FILE CONTENT-FILE: writes a file served by the test server
  vm "sudo mkdir -p $CD && base64 -d | sudo tee $CD/$1 >/dev/null" < <(base64 -w0 "$2"; echo)
}
web_code() {
  local code=000
  for _ in $(seq 1 30); do
    code=$(vm "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:$1/")
    [ "$code" = 200 ] && break
    sleep 2
  done
  echo "$code"
}

catalog 1.0 > "$TMP/cat.json"
put_catalog catalog.json "$TMP/cat.json"
vm "cd $CD && (setsid nohup python3 -m http.server $CP --bind 127.0.0.1 >/tmp/e2e-catalog.log 2>&1 &)"
for _ in $(seq 1 20); do vm "curl -sf http://127.0.0.1:$CP/catalog.json >/dev/null" && break; sleep 1; done
U="http://127.0.0.1:$CP/catalog.json"

check "remote http refused" "$(c POST /api/apps/sources -d '{"url":"http://example.org/catalog.json"}')" "https://"
CONF=$(c POST /api/apps/sources -d "{\"url\":\"$U\"}")
check "first add asks to confirm the checksum" "$(echo "$CONF" | py 'c=d["confirm"]; print(c["apps"], [x["id"] for x in c["invalid"]], c["sha256"]=="'"$(sha256sum "$TMP/cat.json" | cut -d' ' -f1)"'")')" "3 ['e2e-evil'] True"
check "nothing saved before confirmation" "$(c GET /api/apps/sources)" "[]"
SHA=$(echo "$CONF" | py 'print(d["confirm"]["sha256"])')
SRC=$(c POST /api/apps/sources -d "{\"url\":\"$U\",\"sha256\":\"$SHA\"}" | py 'print(d["id"])')
check "catalog pinned" "$(c GET /api/apps/sources | py 'print([(s["trust"], s["apps"], s["sha256"][:8]) for s in d])')" "('pinned', 3, '${SHA:0:8}')"
check "its apps listed" "$(c GET /api/apps/community | py 'print(sorted(a["id"] for a in d["apps"]))')" "['e2e-hostinfo', 'e2e-risky', 'e2e-whoami']"
check "invalid app not installable" "$(c POST "/api/apps/community/$SRC/e2e-evil/check" -d '{}')" "unknown app"

# What the apps get.
check "plain app needs no approval" "$(c POST "/api/apps/community/$SRC/e2e-whoami/check" -d '{}' | py 'print(d["required"], [p["published"] for p in d["grants"]["ports"]])')" "{} ['8199']"
check "risky app: privileged and the Docker socket" "$(c POST "/api/apps/community/$SRC/e2e-risky/check" -d '{}' | py 'r=d["required"]; print(r.get("privileged"), r.get("hostPaths"), d["grants"]["dockerSocket"])')" "True ['/var/run/docker.sock'] True"
check "risky app refused without approval" "$(c POST "/api/apps/community/$SRC/e2e-risky/install" -d '{}')" "not approved: access to /var/run/docker.sock, privileged mode"
check "path outside /srv/strata needs approval" "$(c POST "/api/apps/community/$SRC/e2e-hostinfo/check" -d '{"values":{"DATA_DIR":"/etc/e2e"}}' | py 'print(d["required"]["hostPaths"])')" "['/etc/e2e', '/etc/hostname']"
check "unapproved host path refused" "$(c POST "/api/apps/community/$SRC/e2e-hostinfo/install" -d '{"approve":{"hostPaths":["/etc/hostname"]},"values":{"DATA_DIR":"/etc/e2e"}}')" "not approved: access to /etc/e2e"
check "a symlink under /srv/strata counts as its target" "$(vm "sudo ln -sfn /etc /srv/strata/main/e2e-etc"; c POST "/api/apps/community/$SRC/e2e-hostinfo/check" -d '{"values":{"DATA_DIR":"/srv/strata/main/e2e-etc"}}' | py 'print(d["required"]["hostPaths"])'; vm "sudo rm /srv/strata/main/e2e-etc")" "['/etc', '/etc/hostname']"

# Installs: a plain one, and one with an approved host path (checked
# again on what docker compose itself reads).
check "community app installed" "$(job "$(c POST "/api/apps/community/$SRC/e2e-whoami/install" -d '{"name":"e2e-cwho"}')")" "success"
check "it answers" "$(web_code 8199)" "200"
check "listed as installed, from its catalog" "$(c GET /api/apps/community | py 'print([(i["stack"], i["version"], i["sourceName"], i["update"], i["port"]) for i in d["installed"]])')" "('e2e-cwho', '1.0', 'E2E catalog', False, '8199')"
check "approved host path installed" "$(job "$(c POST "/api/apps/community/$SRC/e2e-hostinfo/install" -d '{"name":"e2e-chost","approve":{"hostPaths":["/etc/hostname"]}}')")" "success"
check "its data folder created under /srv/strata" "$(vm "sudo stat -c %u /srv/strata/main/e2e-hostinfo")" "1000"
check "approval recorded" "$(vm "sudo cat /var/lib/strata/stacks/e2e-chost/.strata-community.json" | py 'print(d["approved"])')" "{'hostPaths': ['/etc/hostname']}"
c DELETE '/api/docker/stacks/e2e-chost?volumes=1' >/dev/null
vm "sudo rm -rf /srv/strata/main/e2e-hostinfo"

# The catalog changes: the new version waits for approval, then updates.
catalog 1.1 > "$TMP/cat2.json"
put_catalog catalog.json "$TMP/cat2.json"
SHA2=$(sha256sum "$TMP/cat2.json" | cut -d' ' -f1)
check "change detected, not applied" "$(c POST "/api/apps/sources/$SRC/refresh" | py 'print(d["pending"]=="'"$SHA2"'", d["sha256"]=="'"$SHA"'")')" "True True"
check "still version 1.0 offered" "$(c GET /api/apps/community | py 'print(d["installed"][0]["update"])')" "False"
check "accepting another checksum refused" "$(c POST "/api/apps/sources/$SRC/accept" -d "{\"sha256\":\"$SHA\"}")" "no pending change"
check "new version accepted" "$(c POST "/api/apps/sources/$SRC/accept" -d "{\"sha256\":\"$SHA2\"}" | py 'print(d["sha256"]=="'"$SHA2"'")')" "True"
check "update offered" "$(c GET /api/apps/community | py 'print([(i["latest"], i["update"]) for i in d["installed"]])')" "('1.1', True)"
check "update review" "$(c POST /api/apps/community/stacks/e2e-cwho/check -d '{}' | py 'print(d["from"], d["version"], d["required"])')" "1.0 1.1 {}"
check "community app updated" "$(job "$(c POST /api/apps/community/stacks/e2e-cwho/update -d '{}')")" "success"
check "now at 1.1" "$(c GET /api/apps/community | py 'print([(i["version"], i["update"]) for i in d["installed"]])')" "('1.1', False)"
check "still answers" "$(web_code 8199)" "200"

# The same catalog, signed (Ed25519, openssl on this host).
openssl genpkey -algorithm ed25519 -out "$TMP/cat.key" 2>/dev/null
PUB="ed25519:$(openssl pkey -in "$TMP/cat.key" -pubout -outform DER | tail -c 32 | base64)"
mkdir -p "$TMP/signed" && cp "$TMP/cat2.json" "$TMP/signed/catalog.json"
openssl pkeyutl -sign -inkey "$TMP/cat.key" -rawin -in "$TMP/signed/catalog.json" | base64 -w0 > "$TMP/signed/catalog.json.sig"
vm "sudo mkdir -p $CD/signed"
put_catalog signed/catalog.json "$TMP/signed/catalog.json"
put_catalog signed/catalog.json.sig "$TMP/signed/catalog.json.sig"
U2="http://127.0.0.1:$CP/signed/catalog.json"
OTHER="ed25519:$(openssl genpkey -algorithm ed25519 2>/dev/null | openssl pkey -pubout -outform DER | tail -c 32 | base64)"
check "wrong key refused" "$(c POST /api/apps/sources -d "{\"url\":\"$U2\",\"publicKey\":\"$OTHER\"}")" "signature"
SRC2=$(c POST /api/apps/sources -d "{\"url\":\"$U2\",\"publicKey\":\"$PUB\"}" | py 'print(d["id"])')
check "signed catalog added" "$(c GET /api/apps/sources | py 'print([s["trust"] for s in d if s["id"]=="'"$SRC2"'"])')" "['signed']"
catalog 6.6.6 > "$TMP/evil.json"
put_catalog signed/catalog.json "$TMP/evil.json"
check "tampered catalog refused" "$(c POST "/api/apps/sources/$SRC2/refresh")" "signature"
check "last good version kept" "$(c GET /api/apps/community | py 'print(sorted({a["version"] for a in d["apps"] if a["source"]=="'"$SRC2"'"}))')" "['1', '1.1']"

# Removing the sources keeps the installed app (no more updates).
check "sources removed" "$(c DELETE "/api/apps/sources/$SRC"; c DELETE "/api/apps/sources/$SRC2"; c GET /api/apps/sources)" "[]"
check "installed app kept, orphaned" "$(c GET /api/apps/community | py 'print([(i["stack"], i.get("orphaned")) for i in d["installed"]])')" "('e2e-cwho', True)"
check "community app removed" "$(c DELETE '/api/docker/stacks/e2e-cwho?volumes=1')" '"ok":true'
check "audit trail" "$(c GET /api/system/events | grep -c "community catalog E2E catalog")" "1"
vm "sudo pkill -f 'http.server $CP'; sudo rm -rf $CD /tmp/e2e-catalog.log"
