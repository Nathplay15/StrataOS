# One-click container updates: a local registry (registry:2) serves a small
# image (traefik/whoami, already pulled by 90-apps) under a tag that is then
# pushed again with another content. Checks the update check, the update of
# a plain container and of a stack, the rollback of an image that does not
# start, and the cleanup. Pulls registry:2 (~10 MB).
section "Docker: one-click container updates"

R=localhost:5000/e2e-upd:v1
# push_variant LABEL [ENTRYPOINT]: pushes a new image under $R (a commit of
# whoami with one more label), then puts the local tag back on the previous
# image so that the NAS must pull it.
push_variant() {
  vm "set -e; prev=\$(sudo docker image inspect -f '{{.Id}}' $R); sudo docker rm -f e2e-upd-build >/dev/null 2>&1 || true
    sudo docker create --name e2e-upd-build traefik/whoami:latest >/dev/null
    sudo docker commit --change 'LABEL e2e.variant=$1' ${2:+--change 'ENTRYPOINT [\"$2\"]'} e2e-upd-build $R >/dev/null
    sudo docker rm -f e2e-upd-build >/dev/null; sudo docker push -q $R >/dev/null; sudo docker tag \$prev $R" >/dev/null
}
image_of() { vm "sudo docker inspect -f '{{.Image}}' $1" | tr -d '\r'; }
upd_status() { c GET /api/docker/updates | py "print([x['status'] for x in d['containers'] if x['name']=='$1'])"; }
web_ok() { vm "for i in \$(seq 1 20); do curl -s -m 3 http://127.0.0.1:$1/ | grep -q Hostname && echo up && break; sleep 1; done"; }

vm "sudo docker rm -f e2e-registry >/dev/null 2>&1; sudo docker run -d --name e2e-registry -p 127.0.0.1:5000:5000 registry:2 >/dev/null
  for i in \$(seq 1 30); do curl -s http://127.0.0.1:5000/v2/ >/dev/null && break; sleep 1; done
  sudo docker pull -q traefik/whoami:latest >/dev/null; sudo docker tag traefik/whoami:latest $R; sudo docker push -q $R >/dev/null" >/dev/null
check "local registry serves the test image" "$(vm "curl -s http://127.0.0.1:5000/v2/e2e-upd/tags/list")" '"v1"'
# The container runs an image of its own (not also tagged traefik/whoami),
# so that the update can remove it.
push_variant 1
vm "sudo docker pull -q $R" >/dev/null

check "container created from the registry" "$(job "$(c POST /api/docker/containers -d "{\"name\":\"e2e-upd\",\"image\":\"$R\",\"ports\":[{\"host\":8093,\"container\":80}],\"volumes\":[{\"source\":\"/srv/strata/main\",\"target\":\"/data\",\"readOnly\":true}],\"env\":{\"E2E\":\"1\"},\"start\":true}")")" "success"
check "update check runs as a job" "$(job "$(c POST /api/docker/updates/check)")" "success"
check "fresh container is up to date" "$(upd_status e2e-upd)" "['current']"
J1=$(c POST /api/docker/updates/check)
check "check refused while one runs" "$(c POST /api/docker/updates/check)" "already running"
job "$J1" >/dev/null

OLD=$(image_of e2e-upd)
push_variant 2
job "$(c POST /api/docker/updates/check)" >/dev/null
check "new image in the registry is detected" "$(upd_status e2e-upd)" "['available']"
check "available count" "$(c GET /api/docker/updates | py 'print(d["available"] >= 1)')" "True"
check "update event logged" "$(c GET /api/system/events | py 'print(any("e2e-upd" in e["message"] for e in d[:50]))')" "True"
check "invalid container refused" "$(c POST /api/docker/updates/apply -d '{"container":"../x"}')" "invalid container"
check "container or stack required" "$(c POST /api/docker/updates/apply -d '{}')" "give a container or a stack"

check "container updated" "$(job "$(c POST /api/docker/updates/apply -d '{"container":"e2e-upd"}')")" "success"
NEW=$(image_of e2e-upd)
check "runs the new image" "$([ -n "$NEW" ] && [ "$NEW" != "$OLD" ] && echo changed)" "changed"
check "same settings kept" "$(vm "sudo docker inspect -f '{{json .HostConfig.PortBindings}} {{json .HostConfig.Binds}} {{json .Config.Env}}' e2e-upd")" '"8093"'
check "bind mount and environment kept" "$(vm "sudo docker inspect -f '{{json .HostConfig.Binds}} {{json .Config.Env}}' e2e-upd")" '"E2E=1"'
check "new label from the new image" "$(vm "sudo docker inspect -f '{{index .Config.Labels \"e2e.variant\"}}' e2e-upd")" "2"
check "updated container answers" "$(web_ok 8093)" "up"
check "old container removed" "$(vm "sudo docker ps -a --format '{{.Names}}' | grep -c e2e-upd-strata-old")" "0"
check "old image removed" "$(vm "sudo docker image inspect $OLD >/dev/null 2>&1 && echo kept || echo gone")" "gone"
check "marked up to date" "$(upd_status e2e-upd)" "['current']"

# An image that cannot start: the update must restore the previous container.
push_variant 3 /nonexistent
job "$(c POST /api/docker/updates/check)" >/dev/null
check "broken image detected as an update" "$(upd_status e2e-upd)" "['available']"
check "broken update rolled back" "$(job "$(c POST /api/docker/updates/apply -d '{"container":"e2e-upd"}')")" "previous version restored"
check "previous image still running" "$(image_of e2e-upd)" "$NEW"
check "previous container answers" "$(web_ok 8093)" "up"
check "no leftover container" "$(vm "sudo docker ps -a --format '{{.Names}}' | grep -c e2e-upd-strata-old")" "0"
check "rollback event logged" "$(c GET /api/system/events | py 'print(any("previous version restored" in e["message"] for e in d[:50]))')" "True"

# A stack (App Center apps are stacks): compose pull + up, with a good image.
c DELETE '/api/docker/containers/e2e-upd' >/dev/null
push_variant 4
vm "sudo docker pull -q $R" >/dev/null
STACK=$'services:\n  web:\n    image: '"$R"$'\n    ports:\n      - "8094:80"\n'
check "stack deployed" "$(job "$(c POST /api/docker/stacks -d "$(STACK="$STACK" python3 -c 'import json,os; print(json.dumps({"name":"e2e-upd-stack","compose":os.environ["STACK"],"deploy":True}))')")")" "success"
SOLD=$(vm "sudo docker ps -q --filter label=com.docker.compose.project=e2e-upd-stack | head -1" | tr -d '\r')
push_variant 5
job "$(c POST /api/docker/updates/check)" >/dev/null
check "stack update available" "$(c GET /api/docker/updates | py 'print(d["stacks"].get("e2e-upd-stack"))')" "True"
check "stack updated" "$(job "$(c POST /api/docker/updates/apply -d '{"stack":"e2e-upd-stack"}')")" "success"
SNEW=$(vm "sudo docker ps -q --filter label=com.docker.compose.project=e2e-upd-stack | head -1" | tr -d '\r')
check "stack container recreated" "$([ -n "$SNEW" ] && [ "$SNEW" != "$SOLD" ] && echo recreated)" "recreated"
check "stack runs the new image" "$(vm "sudo docker inspect -f '{{index .Config.Labels \"e2e.variant\"}}' $SNEW")" "5"
check "stack no longer has an update" "$(c GET /api/docker/updates | py 'print(d["stacks"].get("e2e-upd-stack", False))')" "False"
check "update all with nothing to do" "$(job "$(c POST /api/docker/updates/apply-all)")" "success"
check "non-admin refused" "$(u GET /api/docker/updates)" "administrator"

c DELETE '/api/docker/stacks/e2e-upd-stack?volumes=1' >/dev/null
vm "sudo docker rm -f e2e-registry e2e-upd e2e-upd-build >/dev/null 2>&1; sudo docker images -q localhost:5000/e2e-upd | xargs -r sudo docker rmi -f >/dev/null 2>&1; sudo docker image prune -f >/dev/null" >/dev/null
job "$(c POST /api/docker/updates/check)" >/dev/null
check "test containers gone from the results" "$(c GET /api/docker/updates | py 'print([x["name"] for x in d["containers"] if x["name"].startswith("e2e-upd")])')" "[]"
