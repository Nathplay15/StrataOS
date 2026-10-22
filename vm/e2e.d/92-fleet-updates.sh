# shellcheck shell=bash
# Grouped updates from the Multi-NAS console. Like the "Multi-NAS console"
# section, the VM pairs with itself (127.0.0.1): it is both the console and
# the watched NAS. The update permission is refused, then granted; updates
# are checked (apt-get update: needs the VM's network) and a StrataOS update
# runs through the console as a simulation (apt-get -s, nothing installed).
# The console's token is read from the state file to call the watched NAS
# directly, as the console does.
section "Multi-NAS console: grouped updates"
FC=$(c POST /api/fleet/code | py 'print(d["code"])')
FM=$(c POST /api/fleet -d "{\"host\":\"127.0.0.1\",\"code\":\"$FC\"}" | py 'print(d["id"])')
MG=$(c GET /api/fleet/managers | py 'print(d[-1]["id"])')
check "versions and updates shown" "$(c GET /api/fleet | py 'r=[x for x in d if x["id"]=="'"$FM"'"][0]["summary"]; print(r["version"] == r["updates"]["version"], "system" in r["updates"], sorted(r["access"].items()))')" "True True [('reboot', False), ('updates', False)]"
check "update permission off by default" "$(c GET /api/fleet/managers | py 'print([m.get("allowUpdates", False) for m in d if m["id"]=="'"$MG"'"])')" "[False]"
check "console refuses without the permission" "$(c POST /api/fleet/updates -d "{\"ids\":[\"$FM\"],\"kind\":\"strata\",\"strategy\":\"rolling\",\"dryRun\":true}")" "does not allow the console to install updates"

# Direct calls with the console token (exactly what the console sends).
FTOK=$(vm "sudo python3 -c 'import json; print([m[\"token\"] for m in json.load(open(\"/var/lib/strata/state.json\"))[\"fleet\"][\"members\"] if m[\"id\"]==\"$FM\"][0])'")
peer() { vm "curl -sk -m 30 -X $1 -H 'Authorization: Bearer $FTOK' -H 'Content-Type: application/json' https://localhost:7443$2 ${3:+-d '$3'}"; }
check "watched NAS refuses an update when the permission is off" "$(peer POST /api/fleet/peer/updates/start '{"kind":"strata","dryRun":true}')" "does not allow the console to install updates"
check "token reads the update state" "$(peer GET /api/fleet/peer/updates | py 'print("candidate" in d, "rebootRequired" in d, d["access"]["updates"])')" "True True False"

check "permission granted without pairing again" "$(c PUT /api/fleet/managers/$MG -d '{"allowUpdates":true}')" '"ok":true'
check "permission saved, restart still off" "$(c GET /api/fleet/managers | py 'print([(m.get("allowUpdates", False), m.get("allowReboot", False)) for m in d if m["id"]=="'"$MG"'"])')" "[(True, False)]"
check "permission change audited" "$(c GET /api/system/events | py 'print(any("may now install updates" in e["message"] for e in d[:20]))')" "True"
check "only the fixed actions" "$(peer POST /api/fleet/peer/updates/start '{"kind":"bash -c id"}')" "invalid update kind"
check "restart refused without its own permission" "$(peer POST /api/fleet/peer/reboot)" "does not allow the console to restart it"
check "jobs of others hidden from the console" "$(peer GET /api/fleet/peer/jobs/0000000000000000)" "job not found"
check "console sees the permission" "$(c GET /api/fleet | py 'print([x["summary"]["access"]["updates"] for x in d if x["id"]=="'"$FM"'"])')" "[True]"

check "updates checked through the console" "$(job "$(c POST /api/fleet/check -d "{\"ids\":[\"$FM\"]}")")" "success"
check "check audited on the watched NAS" "$(c GET /api/system/events | py 'print(any(e["message"]=="Update check started by the console strata-vm" for e in d[:30]))')" "True"

RUN=$(c POST /api/fleet/updates -d "{\"ids\":[\"$FM\"],\"kind\":\"strata\",\"strategy\":\"rolling\",\"dryRun\":true}" | py 'print(d["id"])')
check "second grouped update refused while one runs" "$(c POST /api/fleet/updates -d "{\"ids\":[\"$FM\"],\"kind\":\"system\",\"strategy\":\"parallel\",\"dryRun\":true}")" "already running"
for _ in $(seq 1 100); do
  [ "$(c GET /api/fleet/updates | py 'print([r["status"] for r in d if r["id"]=="'"$RUN"'"][0])')" != running ] && break; sleep 3
done
check "grouped update (simulation) succeeded" "$(c GET /api/fleet/updates | py 'r=[r for r in d if r["id"]=="'"$RUN"'"][0]; print(r["status"], r["items"][0]["state"], r["totals"]["success"], r["items"][0].get("error",""))')" "success success 1"
check "simulation job ran on the watched NAS" "$(c GET /api/jobs | py 'print([(j["title"], j["status"], j["user"]) for j in d if j["title"]=="Update simulation"][:1])')" "('Update simulation', 'success', 'strata-vm (console)')"
SIMJOB=$(c GET /api/jobs | py 'print([j["id"] for j in d if j["title"]=="Update simulation"][0])')
check "simulation only (apt-get -s)" "$(c GET /api/jobs/$SIMJOB | py 'print(d["log"][-2000:])')" "strataos"
check "nothing installed" "$(vm "pgrep -f '[a]pt-get -y' >/dev/null && echo busy || echo idle")" "idle"
check "update audited on the watched NAS" "$(c GET /api/system/events | py 'print(any(e["message"]=="Update simulation started by the console strata-vm" for e in d[:40]))')" "True"
check "summary on the console" "$(c GET /api/system/events | py 'print([e["message"] for e in d[:40] if e["message"].startswith("Grouped update finished")][:1])')" "Grouped update finished: 1 succeeded, 0 failed, 0 skipped"

check "permission revoked" "$(c PUT /api/fleet/managers/$MG -d '{"allowUpdates":false,"allowReboot":true}')" '"ok":true'
check "restart cannot stay on alone" "$(c GET /api/fleet/managers | py 'print([m.get("allowReboot", False) for m in d if m["id"]=="'"$MG"'"])')" "[False]"
check "watched NAS refuses again" "$(peer POST /api/fleet/peer/updates/check)" "does not allow the console to install updates"

c DELETE /api/fleet/managers/$MG >/dev/null
c DELETE /api/fleet/$FM >/dev/null
check "pairing removed" "$(c GET /api/fleet | py 'print(len(d))')$(c GET /api/fleet/managers | py 'print(len(d))')" "10"
