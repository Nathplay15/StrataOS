#!/bin/bash
# Networking 0.11 test with two VMs on a shared LAN (QEMU multicast, no root):
#   A = vm/state-na (10.96.1.1), B = vm/state-nb (10.96.1.2), 1.75 GB of RAM each.
# NAS firewall (rules, trial confirmed, anti-lockout, automatic rollback),
# guest firewall of Alpine LXC containers ("isolate from the LAN", an allowed
# NAS port, logged drops, rules kept across a restart), virtual networks: NAT
# with DHCP and isolation, isolated bridge, VLAN 802.1Q between the two NAS,
# VXLAN between the two NAS (MTU 1450). About 25 minutes (downloads packages
# and an Alpine image).
# Usage: make deb && flock -o /home/nathan/Documents/StrataOS-wt/.vm.lock timeout 90m ./vm/net-test.sh
#   (recreates both VMs; KEEP=1 keeps them). Ports: NA_WEB/NB_WEB…, see A() and B().
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${DEB:-dist/strataos_$(cat VERSION)_amd64.deb}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
LAN=${NET_LAN:-230.0.0.11:12371}
T=$(mktemp -d)
PASSES=0 FAILS=0
START=$(date +%s)
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:600}" ;; esac; }
section() { printf '\n\033[1;36m== %s  (%d min)\033[0m\n' "$1" $(( ($(date +%s) - START) / 60 )); }
A() { VM_STATE=$PWD/vm/state-na VM_HOSTNAME=na VM_WEB_PORT=${NA_WEB:-7320} VM_SSH_PORT=${NA_SSH:-2422} VM_HTTPS_PORT=${NA_HTTPS:-7623} VM_SMB_PORT=${NA_SMB:-4620} SEED_PORT=${NA_SEED:-8929} \
  VM_EXTRA_PORTS="" VM_MEM=1792 VM_CPUS=2 VM_DISKS=1 VM_DISK_SIZE=8G VM_LAN=$LAN ./vm/vm.sh "$@" | tr -d '\r'; }
B() { VM_STATE=$PWD/vm/state-nb VM_HOSTNAME=nb VM_WEB_PORT=${NB_WEB:-7330} VM_SSH_PORT=${NB_SSH:-2432} VM_HTTPS_PORT=${NB_HTTPS:-7633} VM_SMB_PORT=${NB_SMB:-4630} SEED_PORT=${NB_SEED:-8939} \
  VM_EXTRA_PORTS="" VM_MEM=1792 VM_CPUS=2 VM_DISKS=1 VM_DISK_SIZE=8G VM_LAN=$LAN ./vm/vm.sh "$@" | tr -d '\r'; }
PA=${NA_WEB:-7320} PB=${NB_WEB:-7330}
api() { local port=$1 m=$2 p=$3; shift 3; curl -s -m 120 -b "$T/ck$port" -c "$T/ck$port" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "http://localhost:$port$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
login() { api "$1" POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null; }
job() { local port=$1 id; id=$(echo "$2" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $2"; return; }
  for _ in $(seq 1 ${3:-400}); do [ "$(api "$port" GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null)" = running ] || break; sleep 3; done
  api "$port" GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-2000:]) if d["status"]!="success" else None'; }
# trial PORT RESPONSE [CONFIRM_PATH]: confirms a change applied on trial, prints "confirmed" or the error.
trial() { local port=$1 tok; tok=$(echo "$2" | py 'print(d.get("token",""))' 2>/dev/null)
  [ -n "$tok" ] || { echo "no-token: $2"; return; }
  for _ in $(seq 1 20); do
    sleep 3
    r=$(api "$port" POST "${3:-/api/system/network/confirm}" -d "{\"token\":\"$tok\"}" -o /dev/null -w '%{http_code}')
    [ "$r" = 200 ] && { echo confirmed; return; }
  done
  echo "not confirmed ($r)"; }
# ex PORT CONTAINER SHELL-COMMAND: runs a command in a container, prints its output.
ex() { api "$1" POST "/api/lxc/$2/exec" -d "$(python3 -c 'import json,sys; print(json.dumps({"command":["sh","-c",sys.argv[1]],"timeout":30}))' "$3")" | py 'print((d.get("stdout","")+d.get("stderr","")).strip())'; }
# reach PORT CONTAINER URL: "ok" when the container gets an HTTP answer.
reach() { ex "$1" "$2" "wget -q -T 4 -O /dev/null $3 && echo ok || echo blocked"; }
tcp() { $1 ssh "timeout 4 bash -c '</dev/tcp/$2/$3' 2>/dev/null && echo open || echo closed"; }
ct() { # ct PORT NAME NETWORK-JSON: creates and starts an Alpine container
  local r; r=$(job "$1" "$(api "$1" POST /api/lxc -d "{\"name\":\"$2\",\"image\":{\"distro\":\"alpine\",\"release\":\"3.22\"},\"location\":\"/srv/strata/volume1\",\"backend\":\"dir\",\"network\":$3}")" 600)
  api "$1" POST "/api/lxc/$2/start" >/dev/null; sleep 3
  # Wait for its address (DHCP) and a route before testing anything.
  for _ in $(seq 1 20); do [ -n "$(ex "$1" "$2" "ip -4 route | grep -m1 -E 'default|dev eth0'")" ] && break; sleep 2; done
  echo "$r" | head -1; }
ipof() { for _ in $(seq 1 15); do a=$(ex "$1" "$2" "ip -4 -o addr show eth0 | awk '{print \$4}' | cut -d/ -f1"); [ -n "$a" ] && { echo "$a"; return; }; sleep 2; done; }
cleanup_vms() { [ -n "${KEEP:-}" ] && return; A destroy >/dev/null 2>&1; B destroy >/dev/null 2>&1; }
diag() {
  [ "$FAILS" -eq 0 ] && return
  for w in A B; do printf '\n--- %s ---\n' "$w"; $w ssh 'sudo journalctl -u strata --no-pager -n 30 -o cat; sudo nft list ruleset | head -120; networkctl --no-pager; free -m' 2>&1; done
}
trap 'diag; cleanup_vms; exit 1' INT TERM

section "Two NAS on a shared LAN"
A destroy >/dev/null 2>&1; B destroy >/dev/null 2>&1
# Both VMs would download the base image at the same time: fetch it once first.
[ -f vm/images/debian-13-generic-amd64.qcow2 ] || { A create >"$T/ca0.log" 2>&1; A destroy >/dev/null 2>&1; }
(A create >"$T/ca.log" 2>&1 && A deploy "$DEB" >>"$T/ca.log" 2>&1; echo $? >"$T/ca.rc") &
(B create >"$T/cb.log" 2>&1 && B deploy "$DEB" >>"$T/cb.log" 2>&1; echo $? >"$T/cb.rc") &
wait
for w in a b; do [ "$(cat "$T/c$w.rc")" = 0 ] || { tail -20 "$T/c$w.log"; cleanup_vms; exit 1; }; done
for p in $PA $PB; do
  api $p POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"nas-$p\"}" >/dev/null
  login $p
done
LINK=$(A ssh "ip -o link | awk -F': ' '/52:54:00:aa/{print \$2}' | head -1" | tr -d ' ')
check "LAN port found" "$LINK" "en"
# The LAN port managed by StrataOS (needed for VLANs), on trial and confirmed.
for x in "A $PA 1" "B $PB 2"; do set -- $x
  R=$(api $2 PUT /api/system/network/config -d "{\"config\":{\"interfaces\":[{\"name\":\"$LINK\",\"mode\":\"static\",\"addresses\":[\"10.96.1.$3/24\"]}],\"bonds\":[],\"bridges\":[],\"vlans\":[]},\"timeout\":90}")
  check "LAN port managed on $1" "$(trial $2 "$R")" "confirmed"
done
check "LAN between the NAS" "$(B ssh 'ping -c1 -W2 10.96.1.1 >/dev/null && echo up')" "up"
# A public address on B, reached from A through B (stands for the Internet:
# every other address of the test is private).
B ssh 'sudo ip addr add 203.0.113.5/32 dev lo'
# systemd-networkd drops routes it did not set when it reconfigures a link it
# manages (each network change): route203 puts this one back.
route203() { A ssh 'sudo ip route replace 203.0.113.5/32 via 10.96.1.2'; }
route203
for p in $PA $PB; do
  D=$(api $p GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
  check "volume on $p" "$(job $p "$(api $p POST /api/storage/volumes -d "{\"name\":\"volume1\",\"fsType\":\"btrfs\",\"devices\":[\"$D\"],\"dataProfile\":\"single\"}")")" "success"
  echo "$(api $p POST /api/system/components/lxc/install)" >"$T/inst$p"
done
for p in $PA $PB; do check "lxc component on $p" "$(job $p "$(cat "$T/inst$p")" 600)" "success"; done
[ "$FAILS" -eq 0 ] || { diag; cleanup_vms; exit 1; }

section "NAS firewall"
check "off by default, nothing loaded" "$(api $PA GET /api/system/network/firewall | py 'print(d["config"]["host"]["enabled"], d["ruleset"]["host"]=="", d["nft"])')" "False True True"
check "SMB open from B" "$(tcp B 10.96.1.1 445)" "open"
FW='{"host":{"enabled":true,"policyIn":"drop","logDrops":true,"rules":[{"dir":"in","action":"drop","proto":"tcp","ports":"445","source":"set:blocked","log":true},{"dir":"in","action":"accept","source":"lan"}]},"ipsets":[{"name":"blocked","entries":["10.96.1.2"]}]}'
check "invalid rule refused" "$(api $PA PUT /api/system/network/firewall -d '{"config":{"host":{"enabled":true,"rules":[{"dir":"in","action":"accept","ports":"445"}]}}}')" "ports need the TCP or UDP protocol"
R=$(api $PA PUT /api/system/network/firewall -d "{\"config\":$FW,\"timeout\":60}")
check "host change applied on trial" "$R" '"token"'
check "trial shown" "$(api $PA GET /api/system/network/firewall | py 'print(d["trial"] is not None)')" "True"
check "confirmed" "$(trial $PA "$R" /api/system/network/firewall/confirm)" "confirmed"
check "table loaded" "$(A ssh 'sudo nft list table inet strata_host' | grep -m1 -o 'ip saddr @s_blocked4 tcp dport 445')" "@s_blocked4"
check "SMB from B dropped" "$(tcp B 10.96.1.1 445)" "closed"
check "web interface from B still open" "$(tcp B 10.96.1.1 7070)" "open"
check "SSH from B (accepted from the LAN)" "$(tcp B 10.96.1.1 22)" "open"
sleep 2
check "drop logged" "$(api $PA GET '/api/system/network/firewall/log?scope=host' | py 'print([(e["src"], e["dport"]) for e in d][:3])')" "('10.96.1.2', 445)"
# Anti-lockout: everything dropped, no rule at all.
R=$(api $PA PUT /api/system/network/firewall -d '{"config":{"host":{"enabled":true,"policyIn":"drop","rules":[]}},"timeout":30}')
check "lock-out change on trial" "$R" '"token"'
sleep 4
check "anti-lockout: web interface from the LAN" "$(tcp B 10.96.1.1 7070)" "open"
check "anti-lockout: API from the host (NAT)" "$(api $PA GET /api/system/network/firewall | py 'print(d["trial"] is not None)')" "True"
check "SSH from the LAN kept" "$(tcp B 10.96.1.1 22)" "open"
check "everything else dropped" "$(tcp B 10.96.1.1 445)" "closed"
check "other firewall changes refused meanwhile" "$(api $PA PUT /api/system/network/firewall/guest/lxc/x -d '{"enabled":true,"rules":[]}')" "on trial"
# Not confirmed: rolled back after 30 s.
sleep 32
check "rolled back on timeout" "$(api $PA GET /api/system/network/firewall | py 'print(d["trial"], len(d["config"]["host"]["rules"]))')" "None 2"
check "previous rules back" "$(tcp B 10.96.1.1 445)" "closed"
check "rollback event" "$(api $PA GET /api/system/events | py '[print(e["message"]) for e in d[:10]]')" "Firewall change rolled back (timeout)"
R=$(api $PA PUT /api/system/network/firewall -d '{"config":{"host":{"enabled":false,"rules":[]}},"timeout":30}')
check "firewall off (on trial)" "$(trial $PA "$R" /api/system/network/firewall/confirm)" "confirmed"
check "SMB open again" "$(tcp B 10.96.1.1 445)" "open"
check "host table gone" "$(A ssh 'sudo nft list tables' | grep -c strata_host)" "0"

section "Guest firewall: isolate from the LAN"
check "container on lxcbr0 (NAT)" "$(ct $PA web '{"mode":"nat"}')" "success"
check "LAN reachable before" "$(reach $PA web http://10.96.1.2:7070/api/setup)" "ok"
check "NAS reachable before" "$(reach $PA web http://10.0.3.1:7070/api/setup)" "ok"
check "isolated" "$(api $PA PUT /api/system/network/firewall/guest/lxc/web -d '{"enabled":true,"isolate":true,"logDrops":true,"policyIn":"drop","rules":[]}')" ""
G=$(api $PA GET /api/system/network/firewall/guest/lxc/web)
check "tracked by MAC and veth" "$(echo "$G" | py 'b=d["binding"]; print(len(b["macs"]), len(b["ifaces"] or []))')" "1 1"
check "LAN blocked" "$(reach $PA web http://10.96.1.2:7070/api/setup)" "blocked"
check "NAS blocked" "$(reach $PA web http://10.0.3.1:7070/api/setup)" "blocked"
check "Internet still reachable" "$(reach $PA web http://203.0.113.5:7070/api/setup)" "ok"
api $PA PUT /api/system/network/firewall/guest/lxc/web -d '{"enabled":true,"isolate":true,"logDrops":true,"rules":[{"dir":"out","action":"accept","proto":"tcp","ports":"7070","dest":"nas"}]}' >/dev/null
check "NAS port opened by a rule" "$(reach $PA web http://10.0.3.1:7070/api/setup)" "ok"
check "LAN still blocked" "$(reach $PA web http://10.96.1.2:7070/api/setup)" "blocked"
check "drops logged for the guest" "$(api $PA GET '/api/system/network/firewall/log?scope=lxc.web' | py 'print(set(e["dir"] for e in d), any(e.get("dst")=="10.96.1.2" for e in d))')" "{'out'} True"
OLD=$(api $PA GET /api/system/network/firewall/guest/lxc/web | py 'print(d["binding"]["ifaces"][0])')
api $PA POST /api/lxc/web/stop >/dev/null; sleep 3; api $PA POST /api/lxc/web/start >/dev/null; sleep 4
for _ in $(seq 1 20); do [ -n "$(ex $PA web "ip -4 route | grep -m1 default")" ] && break; sleep 2; done
check "still blocked after a restart" "$(reach $PA web http://10.96.1.2:7070/api/setup)" "blocked"
NEW=$(api $PA GET /api/system/network/firewall | py 'print([g["binding"]["ifaces"] for g in d["guests"] if g["name"]=="web"][0])')
check "new interface followed" "$( [ -n "$NEW" ] && [ "$NEW" != "['$OLD']" ] && echo followed)" "followed"
# Incoming: ping from the NAS, dropped by the default policy, then allowed.
WIP=$(ipof $PA web)
check "incoming dropped by the default policy" "$(A ssh "ping -c1 -W2 $WIP >/dev/null && echo up || echo dropped")" "dropped"
api $PA PUT /api/system/network/firewall/guest/lxc/web -d '{"enabled":true,"isolate":true,"rules":[{"dir":"in","action":"accept","proto":"icmp","source":"nas"}]}' >/dev/null
check "incoming allowed by a rule" "$(A ssh "ping -c1 -W2 $WIP >/dev/null && echo up || echo dropped")" "up"
api $PA PUT /api/system/network/firewall/guest/lxc/web -d '{"enabled":false,"rules":[]}' >/dev/null
check "firewall off: LAN again" "$(reach $PA web http://10.96.1.2:7070/api/setup)" "ok"

section "Virtual network: NAT with DHCP, isolated from the LAN"
R=$(api $PA POST /api/system/network/vnets -d '{"id":"pub","label":"Public","kind":"nat","address":"10.30.0.1/24","dhcp":true,"dns":["10.96.1.2"],"isolate":true,"nasPorts":"7443","logDrops":true}')
check "created on trial" "$(trial $PA "$R")" "confirmed"
check "bridge up with its address" "$(A ssh 'ip -4 -o addr show vn0')" "10.30.0.1/24"
check "overlapping network refused" "$(api $PA POST /api/system/network/vnets -d '{"id":"dup","kind":"nat","address":"10.30.0.129/25"}')" "overlap"
check "container on the network" "$(ct $PA pub1 '{"mode":"bridge","bridge":"vn0"}')" "success"
check "address from the DHCP server of the NAS" "$(ipof $PA pub1)" "10.30.0."
check "LAN blocked" "$(reach $PA pub1 http://10.96.1.2:7070/api/setup)" "blocked"
check "NAS blocked" "$(reach $PA pub1 http://10.30.0.1:7070/api/setup)" "blocked"
# Plain HTTP to the HTTPS port: a 400 answer proves the connection went through.
check "allowed NAS port" "$(ex $PA pub1 'wget -T 4 -O /dev/null http://10.30.0.1:7443/ 2>&1')" "400"
route203
check "Internet through the NAS" "$(reach $PA pub1 http://203.0.113.5:7070/api/setup)" "ok"
check "listed with its guest" "$(api $PA GET /api/system/network/vnets | py 'n=d["networks"][0]; print(n["bridge"], n["status"]["up"], [g["name"] for g in n["guests"]])')" "vn0 True ['pub1']"
check "deletion refused while used" "$(api $PA DELETE /api/system/network/vnets/pub)" "used by pub1"
check "network named in the wizards' list" "$(api $PA GET /api/lxc | py 'print(d["bridges"])')" "vn0"

section "Virtual network: isolated bridge"
R=$(api $PA POST /api/system/network/vnets -d '{"id":"lab","kind":"isolated"}')
check "created" "$(trial $PA "$R")" "confirmed"
check "first container" "$(ct $PA lab1 '{"mode":"bridge","bridge":"vn1","address":"10.31.0.11/24"}')" "success"
check "second container" "$(ct $PA lab2 '{"mode":"bridge","bridge":"vn1","address":"10.31.0.12/24"}')" "success"
check "they talk to each other" "$(ex $PA lab1 'ping -c1 -W2 10.31.0.12 >/dev/null && echo up || echo down')" "up"
check "no way out" "$(ex $PA lab1 'ping -c1 -W2 10.96.1.2 >/dev/null && echo up || echo down')" "down"

section "Virtual network: VLAN 802.1Q between the two NAS"
for x in "$PA 21" "$PB 22"; do set -- $x
  R=$(api $1 POST /api/system/network/vnets -d "{\"id\":\"cams\",\"kind\":\"vlan\",\"parent\":\"$LINK\",\"vlan\":42,\"subnet\":\"10.42.0.0/24\"}")
  check "VLAN network on $1" "$(trial $1 "$R")" "confirmed"
  check "container on $1" "$(ct $1 cam$2 "{\"mode\":\"bridge\",\"bridge\":\"$(api $1 GET /api/system/network/vnets | py 'print([n["bridge"] for n in d["networks"] if n["id"]=="cams"][0])')\",\"address\":\"10.42.0.$2/24\"}")" "success"
done
# QEMU's multicast LAN sends every frame back to its sender: a bridge would
# learn its own containers' MACs on the uplink. Not learning there avoids it.
for w in A B; do $w ssh "sudo ip link set dev $LINK.42 type bridge_slave learning off"; done
check "VLAN link on A" "$(A ssh "ip -d link show $LINK.42")" "vlan protocol 802.1Q id 42"
check "containers talk over the VLAN" "$(ex $PA cam21 'ping -c2 -W2 10.42.0.22 >/dev/null && echo up || echo down')" "up"
check "untagged LAN does not see them" "$(B ssh 'sudo ip addr add 10.42.0.99/24 dev '"$LINK"'; ping -c1 -W2 10.42.0.21 >/dev/null && echo up || echo down; sudo ip addr del 10.42.0.99/24 dev '"$LINK")" "down"

section "Virtual network: VXLAN between the two NAS"
for x in "$PA 2 11" "$PB 1 12"; do set -- $x
  R=$(api $1 POST /api/system/network/vnets -d "{\"id\":\"site\",\"kind\":\"vxlan\",\"vni\":4242,\"peers\":[\"10.96.1.$2\"]}")
  check "VXLAN network on $1" "$(trial $1 "$R")" "confirmed"
  BR=$(api $1 GET /api/system/network/vnets | py 'print([n["bridge"] for n in d["networks"] if n["id"]=="site"][0])')
  check "container on $1" "$(ct $1 site$3 "{\"mode\":\"bridge\",\"bridge\":\"$BR\",\"address\":\"10.100.0.$3/24\"}")" "success"
done
check "own address refused as peer" "$(api $PA POST /api/system/network/vnets -d '{"id":"bad","kind":"vxlan","vni":7,"peers":["10.96.1.1"]}')" "is an address of this NAS"
check "MTU 1450 in the container" "$(ex $PA site11 'cat /sys/class/net/eth0/mtu')" "1450"
check "containers talk through the tunnel" "$(ex $PA site11 'ping -c2 -W2 10.100.0.12 >/dev/null && echo up || echo down')" "up"
check "full-size packets fit" "$(ex $PA site11 'ping -c1 -W2 -s 1422 10.100.0.12 >/dev/null && echo up || echo down')" "up"
check "VXLAN peer listed in the firewall" "$(api $PA GET /api/system/network/firewall | py 'print(d["peers"])')" "10.96.1.2"

section "Rollback of a virtual network change"
R=$(api $PA POST /api/system/network/vnets -d '{"id":"tmp","kind":"isolated","address":"10.32.0.1/24"}')
check "on trial" "$R" '"token"'
sleep 5
check "bridge created meanwhile" "$(A ssh 'ip -4 -o addr show' | grep -c 10.32.0.1)" "1"
check "manual rollback" "$(api $PA POST /api/system/network/rollback)" ""
sleep 4
check "network gone" "$(api $PA GET /api/system/network/vnets | py 'print(sorted(n["id"] for n in d["networks"]))')" "['cams', 'lab', 'pub', 'site']"
check "its bridge removed" "$(A ssh 'ip -4 -o addr show' | grep -c 10.32.0.1)" "0"

printf '\n%s passed, %s failed in %d min\n' "$PASSES" "$FAILS" $(( ($(date +%s) - START) / 60 ))
diag
cleanup_vms
[ "$FAILS" -eq 0 ]
