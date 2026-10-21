# App Center: the larger catalog, one new app installed (Dozzle, small and
# without data), and custom apps made from a compose file (traefik/whoami).
# Pulls two small images (~20 MB together).
section "App Center: more apps, custom apps"

# http_code PORT: answer of a web port of the NAS, after the container starts.
web_code() {
  local code=000
  for _ in $(seq 1 30); do
    code=$(vm "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:$1/")
    [ "$code" = 200 ] && break
    sleep 2
  done
  echo "$code"
}

check "catalog has the new apps" "$(c GET /api/apps | py 'ids={a["id"] for a in d["apps"]}; print(len(d["apps"]) >= 50, all(x in ids for x in ["nodered","zigbee2mqtt","mosquitto","lidarr","bazarr","tdarr","kavita","actual","memos","linkwarden","vikunja","ollama","openwebui","dozzle","speedtest","netdata"]))')" "True True"
check "new categories" "$(c GET /api/apps | py 'print(sorted({a["category"] for a in d["apps"]} & {"AI","Home automation","Monitoring"}))')" "['AI', 'Home automation', 'Monitoring']"
check "catalog sorted by name" "$(c GET /api/apps | py 'n=[a["name"].lower() for a in d["apps"]]; print(n == sorted(n))')" "True"
check "no Ollama link before Ollama" "$(c GET /api/apps | py 'print("openwebui" in d.get("linked",{}))')" "False"
check "bad device refused" "$(c POST /api/apps/zigbee2mqtt/install -d '{"name":"z2m","values":{"ZIGBEE_DEVICE":"/etc/shadow","MQTT_PASSWORD":"xxxxxxxxxx"}}')" "device under /dev"

# One new catalog app: install, answer, remove.
check "install Dozzle" "$(job "$(c POST /api/apps/dozzle/install -d '{"name":"dozzle","values":{"WEB_PORT":"8888"}}')")" "success"
check "Dozzle installed and its port reported" "$(c GET /api/apps | py 'print(d["installed"].get("dozzle"), d["ports"].get("dozzle"))')" "dozzle 8888"
check "Dozzle answers" "$(web_code 8888)" "200"
check "Dozzle removed" "$(c DELETE '/api/docker/stacks/dozzle?volumes=1')" '"ok":true'
check "Dozzle no longer installed" "$(c GET /api/apps | py 'print("dozzle" in d["installed"])')" "False"
check "Dozzle port closed" "$(vm "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:8888/")" "000"

# Custom app from a compose file.
WHOAMI=$'services:\n  whoami:\n    image: traefik/whoami:latest\n    restart: unless-stopped\n    ports:\n      - "8099:80"\n'
body() { WHOAMI="$WHOAMI" python3 -c 'import json,os,sys; d=json.loads(sys.argv[1]); d.setdefault("compose", os.environ["WHOAMI"]); print(json.dumps(d))' "$1"; }
check "invalid compose refused" "$(c POST /api/apps/custom -d "$(body '{"stack":"e2e-bad","name":"Bad","compose":"services:\n  x:\n    imagee: nginx\n"}')")" "invalid compose file"
check "bad stack left nothing" "$(c GET /api/docker/stacks | py 'print([s["name"] for s in d if s["name"]=="e2e-bad"])')" "[]"
check "bad icon refused" "$(c POST /api/apps/custom -d "$(body '{"stack":"e2e-who","name":"Who","icon":"javascript:alert(1)","webPort":8099}')")" "http(s) address"
check "custom app installed" "$(job "$(c POST /api/apps/custom -d "$(body '{"stack":"e2e-who","name":"Who am I","icon":"https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/traefik.svg","webPort":8099,"description":"e2e test"}')")")" "success"
check "listed under My apps" "$(c GET /api/apps | py 'print([(x["stack"], x["name"], x["webPort"]) for x in d["custom"]])')" "('e2e-who', 'Who am I', 8099)"
check "custom app answers" "$(vm "for i in \$(seq 1 30); do curl -s -m 5 http://127.0.0.1:8099/ | grep -q Hostname && break; sleep 2; done; curl -s -m 5 http://127.0.0.1:8099/")" "Hostname"
check "duplicate stack refused" "$(c POST /api/apps/custom -d "$(body '{"stack":"e2e-who","name":"Again"}')")" "already exists"
check "custom app renamed" "$(c PUT /api/apps/custom/e2e-who -d '{"name":"Whoami","webPort":8099}')" '"ok":true'
check "new name listed" "$(c GET /api/apps | py 'print([x["name"] for x in d["custom"] if x["stack"]=="e2e-who"])')" "['Whoami']"
KUMA=$(c GET /api/apps | py 'print(d["installed"].get("uptimekuma",""))')
[ -n "$KUMA" ] && check "catalog app cannot become custom" "$(c PUT "/api/apps/custom/$KUMA" -d '{"name":"Kuma"}')" "already an App Center app"

# An existing stack saved as a custom app, then back to a plain stack.
check "plain stack saved" "$(c POST /api/docker/stacks -d "$(WHOAMI="${WHOAMI/8099/8097}" body '{"name":"e2e-plain","deploy":false}')")" '"ok":true'
check "stack saved as a custom app" "$(c PUT /api/apps/custom/e2e-plain -d '{"name":"Plain","webPort":8097}')" '"ok":true'
check "stack shown with its app" "$(c GET /api/docker/stacks | py 'print([s.get("custom",{}).get("name") for s in d if s["name"]=="e2e-plain"])')" "['Plain']"
check "removed from My apps" "$(c DELETE /api/apps/custom/e2e-plain; c GET /api/apps | py 'print([x["stack"] for x in d["custom"]])')" "['e2e-who']"
check "stack kept" "$(c GET /api/docker/stacks | py 'print([(s["name"], "custom" in s) for s in d if s["name"]=="e2e-plain"])')" "('e2e-plain', False)"
c DELETE '/api/docker/stacks/e2e-plain?volumes=1' >/dev/null

check "custom app removed" "$(c DELETE '/api/docker/stacks/e2e-who?volumes=1')" '"ok":true'
check "My apps empty again" "$(c GET /api/apps | py 'print(d["custom"])')" "[]"
check "custom app port closed" "$(vm "curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:8099/")" "000"
