#!/bin/bash
# Ceph test with three VMs on a shared LAN (QEMU multicast, no root):
#   ceph1..3 = vm/state-ceph1..3 (10.98.0.1-3), 1280 MB of RAM each, two 8 GB data disks.
# Cluster of NAS through the API, Ceph on one disk of each NAS, RBD pool and CephFS,
# CephFS and RBD volumes, a shared folder on Ceph over SMB, VM storage (libvirt RBD pool),
# an external connection with a restricted key, adding and safely removing a disk,
# maintenance, one NAS stopped (degraded but available) then back (recovery), and the
# removal of everything. About 15 minutes (with the Debian image cached); needs about 4 GB of free RAM.
# Usage: make deb && ./vm/ceph-test.sh   (recreates the three VMs; KEEP=1 keeps them)
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${DEB:-dist/strataos_$(cat VERSION)_amd64.deb}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
LAN=${CEPH_LAN:-230.0.0.5:12355}
T=$(mktemp -d)
PASSES=0 FAILS=0
START=$(date +%s)
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:600}" ;; esac; }
section() { printf '\n\033[1;36m== %s  (%d min)\033[0m\n' "$1" $(( ($(date +%s) - START) / 60 )); }
# Node i: web 757(i-1), https 757(i+2), ssh 227(i+4), smb 447(i+4), seed 877(i+4).
N() {
  local i=$1; shift
  VM_STATE=$PWD/vm/state-ceph$i VM_HOSTNAME=ceph$i VM_MEM=${CEPH_VM_MEM:-1280} VM_CPUS=2 VM_DISKS=2 VM_DISK_SIZE=8G \
    VM_WEB_PORT=$((7569 + i)) VM_HTTPS_PORT=$((7572 + i)) VM_SSH_PORT=$((2274 + i)) VM_SMB_PORT=$((4474 + i)) SEED_PORT=$((8774 + i)) \
    VM_EXTRA_PORTS="" VM_LAN=$LAN ./vm/vm.sh "$@" | tr -d '\r'
}
port() { echo $((7569 + $1)); }
api() { local p; p=$(port "$1"); local m=$2 path=$3; shift 3; curl -s -m 120 -b "$T/ck$p" -c "$T/ck$p" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "http://localhost:$p$path" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
login() { api "$1" POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null; }
job() { local n=$1 id; id=$(echo "$2" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $2"; return; }
  for _ in $(seq 1 ${3:-600}); do [ "$(api "$n" GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null)" = running ] || break; sleep 3; done
  api "$n" GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-3000:]) if d["status"]!="success" else None'; }
# Status and the codes of the health checks that are not muted.
health() { api "$1" GET /api/ceph | py 'st=d.get("status") or {}; h=st.get("health",{}); print(h.get("status","?"), " ".join(c["code"] for c in h.get("checks",[]) if not c.get("muted")), d.get("error",""))' 2>/dev/null; }
# Health warnings that are expected on small test VMs.
EXPECTED="MON_DISK_LOW|RECENT_CRASH|POOL_TOO_FEW_PGS|TOO_FEW_PGS|MDS_INSUFFICIENT_STANDBY"
# Slow heartbeats come from a loaded host (and are remembered for 15 minutes,
# e.g. after a NAS was killed): only tolerated when nothing else is left at the end.
SLOW="OSD_SLOW_PING_TIME_BACK|OSD_SLOW_PING_TIME_FRONT|BLUESTORE_SLOW_OP_ALERT"
healthy() { local h; h=$(health "$1"); case "$h" in HEALTH_OK*) echo ok; return ;; esac
  case "$h" in HEALTH_WARN*) [ -z "$(echo "$h" | cut -d' ' -f2- | tr ' ' '\n' | grep -v '^$' | grep -Ev "^($EXPECTED)$")" ] && { echo "ok ($h)"; return; } ;; esac; echo "$h"; }
wait_healthy() { local n=$1 t=${2:-600} h=""; for _ in $(seq 1 $((t / 10))); do h=$(healthy "$n"); case "$h" in ok*) break ;; esac; sleep 10; done
  case "$h" in ok*) ;; HEALTH_WARN*) [ -z "$(echo "$h" | cut -d' ' -f2- | tr ' ' '\n' | grep -v '^$' | grep -Ev "^($EXPECTED|$SLOW)$")" ] && h="ok (slow heartbeats on a loaded host: $h)" ;; esac; echo "$h"; }
lan_ip() { # a LAN address that comes back at every boot, before Ceph starts
  N "$1" ssh "sudo tee /etc/systemd/system/lan-ip.service >/dev/null <<EOF
[Unit]
Description=Test LAN address
BindsTo=sys-subsystem-net-devices-enp0s5.device
After=sys-subsystem-net-devices-enp0s5.device
Before=network-online.target
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'ip link set enp0s5 up; ip addr add 10.98.0.$1/24 dev enp0s5 || true'
[Install]
WantedBy=multi-user.target network-online.target
EOF
sudo systemctl daemon-reload && sudo systemctl enable --now lan-ip.service"
}
cleanup_vms() { [ -n "${KEEP:-}" ] && return; for i in 1 2 3; do N "$i" destroy >/dev/null 2>&1; done; }
# On failure: what the NAS logged, to debug without keeping the VMs.
diag() {
  [ "$FAILS" -eq 0 ] && return
  for i in 1 2 3; do
    N "$i" status | grep -q running || continue
    printf '\n--- ceph%s ---\n' "$i"
    N "$i" ssh 'sudo journalctl -u strata --no-pager -n 40 -o cat; sudo timeout 20 ceph -s 2>&1 | head -30; free -m; sudo dmesg | grep -i -E "oom|killed process" | tail -5' 2>&1
  done
}

section "Three VMs on a shared LAN"
for i in 1 2 3; do N "$i" destroy >/dev/null 2>&1; done
for i in 1 2 3; do (N "$i" create >"$T/create$i.log" 2>&1 && N "$i" deploy "$DEB" >>"$T/create$i.log" 2>&1; echo $? >"$T/create$i.rc") & done
wait
for i in 1 2 3; do [ "$(cat "$T/create$i.rc")" = 0 ] || { tail -20 "$T/create$i.log"; cleanup_vms; exit 1; }; done
trap 'diag; cleanup_vms' INT TERM
for i in 1 2 3; do lan_ip "$i"; done
check "LAN between the VMs" "$(N 3 ssh 'ping -c1 -W2 10.98.0.1 >/dev/null && ping -c1 -W2 10.98.0.2 >/dev/null && echo up')" "up"
for i in 1 2 3; do
  api "$i" POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"ceph$i\"}" >/dev/null
  login "$i"
done
for i in 1 2 3; do echo "$(api "$i" POST /api/system/components/ceph/install)" >"$T/inst$i"; done
for i in 1 2 3; do check "Ceph component on ceph$i" "$(job "$i" "$(cat "$T/inst$i")")" "success"; done
[ "$FAILS" -eq 0 ] || { diag; cleanup_vms; exit 1; }
check "Ceph installed" "$(api 1 GET /api/ceph | py 'print(d["installed"], d["version"][:2])')" "True 18"
N 1 ssh 'free -m | sed -n 2p' | sed 's/^/  RAM after install, ceph1: /'

section "Cluster of NAS"
check "a NAS without a cluster" "$(api 1 GET /api/cluster | py 'print(d["cluster"], "10.98.0.0/24" in d["networks"])')" "None True"
check "test mode needed under 3 NAS" "$(api 1 POST /api/cluster -d '{"name":"Lab","publicNet":"10.98.0.0/24"}' >/dev/null; api 1 POST /api/ceph/bootstrap -d '{}')" "3 NAS at least"
check "cluster created" "$(api 1 GET /api/cluster | py 'print(d["cluster"]["name"], len(d["members"]), d["members"][0]["addr"])')" "Lab 1 10.98.0.1"
check "wrong pairing code refused" "$(api 2 POST /api/cluster/prepare >/dev/null; api 1 POST /api/cluster/members -d '{"host":"10.98.0.2","code":"AAAAA-BBBBB"}')" "wrong or expired"
for i in 2 3; do
  CODE=$(api "$i" POST /api/cluster/prepare | py 'print(d["code"])')
  check "ceph$i joins" "$(api 1 POST /api/cluster/members -d "{\"host\":\"10.98.0.$i\",\"code\":\"$CODE\"}")" "\"name\":\"ceph$i\""
done
check "three members everywhere" "$(for i in 1 2 3; do api "$i" GET /api/cluster | py 'print(sum(1 for m in d["members"] if m["online"]), end=" ")'; done)" "3 3 3"
check "no token in the API" "$(api 2 GET /api/cluster | py 'print(repr(d["cluster"].get("token") or None))')" "None"
check "peer API refuses a wrong token" "$(N 1 ssh "curl -sk -X POST -H 'X-Strata-Cluster: $(printf 'ab%.0s' $(seq 32))' https://10.98.0.2:7443/api/cluster/peer/info")" "unknown NAS"

section "Creating the Ceph cluster"
DISKS=$(api 1 GET /api/ceph/disks | py '
out={}
for r in d:
  ok=[x for x in r["disks"] if x["eligible"] and x["serial"].endswith("-1")]
  out[r["member"]]=[ok[0]["path"]] if ok else []
print(json.dumps(out))')
check "one eligible disk per NAS" "$(echo "$DISKS" | py 'print(sum(len(v) for v in d.values()))')" "3"
check "system disk refused" "$(api 1 GET /api/ceph/disks | py 'print([x["reason"] for x in d[0]["disks"] if x["usage"]=="system"])')" "System disk"
T0=$(date +%s)
check "cluster bootstrap" "$(job 1 "$(api 1 POST /api/ceph/bootstrap -d "{\"disks\":$DISKS,\"rbd\":true,\"cephfs\":true}")" 400)" "success"
echo "  bootstrap took $(( $(date +%s) - T0 )) s"
[ "$FAILS" -eq 0 ] || { diag; cleanup_vms; exit 1; }  # nothing else can work
check "healthy" "$(wait_healthy 1 600)" "ok"
api 1 GET /api/ceph | py 'st=d["status"]; print("  ", st["health"]["status"], [c["code"]+": "+c["message"] for c in st["health"]["checks"]], st["pgStates"])'
check "known Debian 13 manager warning muted (if present)" "$(api 1 GET /api/ceph | py 'm=[c["muted"] for c in d["status"]["health"]["checks"] if c["code"]=="MGR_MODULE_DEPENDENCY"]; print("ok" if all(m) else m)')" "ok"
check "3 monitors in quorum, 3 OSDs up" "$(api 1 GET /api/ceph | py 'st=d["status"]; print(len(st["quorum"]), st["osdsUp"], st["mgrAvailable"])')" "3 3 True"
check "OSDs on each NAS" "$(api 1 GET /api/ceph | py 'print(sorted(o["host"] for o in d["osds"]))')" "['ceph1', 'ceph2', 'ceph3']"
check "pools and filesystem" "$(api 2 GET /api/ceph | py 'print(sorted(p["name"] for p in d["pools"]), [f["name"] for f in d["filesystems"]], [p["size"] for p in d["pools"] if p["name"]=="rbd"])')" "'rbd'] ['cephfs'] [3]"
check "metadata servers" "$(api 1 GET /api/ceph | py 'print(sorted(x["status"] for x in d["daemons"] if x["kind"]=="mds"))')" "'up'"
check "roles of the members" "$(api 3 GET /api/cluster | py 'print([m["roles"] for m in d["members"]])')" "'mds', 'mgr', 'mon', 'osd'"
check "Ceph disk shown as such" "$(api 2 GET /api/storage/disks | py 'print([x["usage"] for x in d if x["serial"].endswith("-1")])')" "['ceph']"
check "a Ceph disk cannot be wiped" "$(api 2 POST /api/storage/disks/wipe -d "{\"device\":\"$(api 2 GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="ceph"][0])')\"}")" "error"
check "keys root only" "$(N 2 ssh 'stat -c "%a %U" /etc/ceph/ceph.client.admin.keyring /var/lib/ceph/bootstrap-osd/ceph.keyring')" "600 root"
check "no key in the job logs" "$(api 1 GET /api/jobs | py 'import re; print([j["id"] for j in d if re.search(r"AQ[A-Za-z0-9+/]{36}", json.dumps(j))] or "none")')" "none"
for i in 1 2 3; do N "$i" ssh 'free -m | sed -n 2p; ps -eo rss,comm --sort=-rss | head -6 | tr "\n" " "' | sed "s/^/  ceph$i: /"; done

section "CephFS and RBD volumes"
check "CephFS volume" "$(job 1 "$(api 1 POST /api/ceph/volumes -d '{"name":"cephfs1","source":"local","kind":"cephfs","fs":"cephfs","path":"/nas"}')")" "success"
check "mounted" "$(N 1 ssh 'findmnt -n -o FSTYPE /srv/strata/cephfs1')" "ceph"
check "not mounted at boot by systemd (StrataOS does it)" "$(N 1 ssh 'grep cephfs1 /etc/fstab')" "noauto"
check "secret not in fstab" "$(N 1 ssh 'grep -c "AQ" /etc/fstab || true')" "0"
check "listed with the volumes" "$(api 1 GET /api/storage/volumes | py 'print([(v["name"], v["fsType"], v["mounted"]) for v in d])')" "('cephfs1', 'cephfs', True)"
check "shared folder on CephFS" "$(api 1 POST /api/shares -d '{"name":"CephShare","path":"/srv/strata/cephfs1/CephShare","smb":true,"browseable":true,"permissions":{"admin":"rw"}}')" "CephShare"
N 1 ssh 'command -v smbclient >/dev/null || sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq smbclient >/dev/null 2>&1'
N 1 ssh "echo 'hello ceph' > /tmp/h.txt; smbclient //127.0.0.1/CephShare -U '$ADMIN_USER%$ADMIN_PASS' -c 'put /tmp/h.txt hello.txt' >/dev/null 2>&1"
check "written over SMB" "$(N 1 ssh 'sudo cat /srv/strata/cephfs1/CephShare/hello.txt')" "hello ceph"
check "same files on another NAS" "$(job 2 "$(api 2 POST /api/ceph/volumes -d '{"name":"cephfs2","source":"local","kind":"cephfs","fs":"cephfs","path":"/nas"}')") $(N 2 ssh 'sudo cat /srv/strata/cephfs2/CephShare/hello.txt')" "hello ceph"
check "RBD volume needs the single-writer confirmation" "$(api 1 POST /api/ceph/volumes -d '{"name":"rbd1","source":"local","kind":"rbd","pool":"rbd","image":"nas1","create":true,"sizeGb":2,"fsType":"ext4"}')" "one NAS only"
check "RBD volume" "$(job 1 "$(api 1 POST /api/ceph/volumes -d '{"name":"rbd1","source":"local","kind":"rbd","pool":"rbd","image":"nas1","create":true,"sizeGb":2,"fsType":"ext4","confirm":true}')")" "success"
N 1 ssh 'echo block | sudo tee /srv/strata/rbd1/f.txt >/dev/null; sync'
check "RBD mounted ext4" "$(N 1 ssh 'findmnt -n -o FSTYPE,SOURCE /srv/strata/rbd1')" "ext4 /dev/rbd"
check "the same image refused on another NAS" "$(job 2 "$(api 2 POST /api/ceph/volumes -d '{"name":"rbd1b","source":"local","kind":"rbd","pool":"rbd","image":"nas1","confirm":true}')")" "in use by another client"
check "RBD devices are not NAS disks" "$(api 1 GET /api/storage/disks | py 'print([x["name"] for x in d if x["name"].startswith("rbd")] or "none")')" "none"
check "Ceph storage of this NAS" "$(api 1 GET /api/ceph/storage | py 'print(sorted((v["name"], v["kind"], v["mounted"]) for v in d["volumes"]))')" "[('cephfs1', 'cephfs', True), ('rbd1', 'rbd', True)]"
check "pool still in use cannot be deleted" "$(api 2 DELETE '/api/ceph/pools/rbd?confirm=rbd')" "uses"
check "uninstalling Ceph refused" "$(api 1 GET /api/system/components/ceph/uninstall | py 'print([b["message"] for b in d["blockers"]])')" "node of a Ceph cluster"

section "VM storage on Ceph"
check "virtualisation on ceph1" "$(job 1 "$(api 1 POST /api/system/components/vms/install)")" "success"
check "RBD drivers of libvirt" "$(job 1 "$(api 1 POST /api/system/components/ceph/install)") $(N 1 ssh 'dpkg -s libvirt-daemon-driver-storage-rbd | grep Status')" "install ok installed"
check "libvirt RBD pool" "$(api 1 POST /api/ceph/vm-pools -d '{"source":"local","pool":"rbd"}')" '"name":"ceph-local-rbd"'
check "listed for the VM wizard" "$(api 1 GET /api/ceph/vm-pools | py 'print([(p["name"], p["active"]) for p in d])')" "('ceph-local-rbd', True)"
check "libvirt sees the images" "$(N 1 ssh 'sudo virsh -c qemu:///system vol-list ceph-local-rbd')" "nas1"
check "libvirt key not readable by users" "$(N 1 ssh 'virsh -c qemu:///system secret-list 2>&1 | head -3; sudo ls -l /etc/libvirt/secrets | tail -1')" "------"
check "VM with its disk on Ceph" "$(job 1 "$(api 1 POST /api/vms -d '{"name":"cephvm","os":"other","vcpus":1,"memoryMb":256,"diskGb":1,"storage":"rbd:ceph-local-rbd","network":"nat"}')")" "success"
check "disk of the VM in the pool" "$(api 1 GET /api/vms | py 'print([d2["source"] for v in d["vms"] for d2 in v["disks"] if v["name"]=="cephvm"])')" "rbd:rbd/cephvm-disk0"
check "VM deleted with its Ceph disk" "$(api 1 DELETE '/api/vms/cephvm?disks=1') $(N 1 ssh 'sudo rbd ls rbd')" "true"
check "image gone" "$(N 1 ssh 'sudo rbd ls rbd | grep -c cephvm || true')" "0"

section "External cluster with a restricted key"
KEYJSON=$(api 1 POST /api/ceph/clients -d '{"name":"nas3ext","fs":"cephfs","pool":"rbd"}')
check "client key created (shown once)" "$(echo "$KEYJSON" | py 'print(d["user"], d["key"][:2], len(d["mons"]))')" "nas3ext AQ 3"
check "client keys listed without their key" "$(api 1 GET /api/ceph | py 'print([c["name"] for c in d["clients"]], "key" in json.dumps(d["clients"]))')" "'nas3ext', 'strata-vms'] False"
EXT=$(echo "$KEYJSON" | py 'print(json.dumps({"name":"Lab external","mons":", ".join(d["mons"][:2]),"user":"client."+d["user"],"key":d["key"]}))')
check "wrong key refused" "$(api 3 POST /api/ceph/external/test -d "$(echo "$EXT" | py 'd["key"]="AQBSdFRiAAAAABAAhYqOzKwRFT1CRGlWbTOKEQ=="; print(json.dumps(d))')")" "error"
check "connection test" "$(api 3 POST /api/ceph/external/test -d "$EXT" | py 'print(len(d["fsid"]), [f["name"] for f in d["filesystems"]])')" "36 ['cephfs']"
EID=$(api 3 POST /api/ceph/external -d "$EXT" | py 'print(d["id"])')
check "external cluster saved, key masked" "$(api 3 GET /api/ceph/storage | py 'print([(s["name"], s["user"]) for s in d["sources"]], "AQ" in json.dumps(d))')" "('Lab external', 'nas3ext')] False"
check "key root only" "$(N 3 ssh "sudo stat -c '%a' /etc/ceph/strata/$EID.keyring /etc/ceph/strata; sudo grep -c AQ /var/lib/strata/state.json || true")" "600
700
0"
check "CephFS of the external cluster" "$(job 3 "$(api 3 POST /api/ceph/volumes -d "{\"name\":\"extfs\",\"source\":\"$EID\",\"kind\":\"cephfs\",\"fs\":\"cephfs\",\"path\":\"/nas\"}")") $(N 3 ssh 'sudo cat /srv/strata/extfs/CephShare/hello.txt')" "hello ceph"
check "the restricted key cannot reach other pools" "$(N 3 ssh "sudo ceph --conf /etc/ceph/strata/$EID.conf --id nas3ext --keyring /etc/ceph/strata/$EID.keyring --connect-timeout 10 osd pool create forbidden 2>&1")" "denied"

section "Disks: add, maintenance, safe removal"
D2=$(api 1 GET /api/ceph/disks | py 'print([x["path"] for x in d[0]["disks"] if x["eligible"]][0])')
M1=$(api 1 GET /api/cluster | py 'print([m["id"] for m in d["members"] if m["self"]][0])')
check "second OSD on ceph1" "$(job 1 "$(api 1 POST /api/ceph/osds -d "{\"member\":\"$M1\",\"device\":\"$D2\"}")")" "success"
for _ in $(seq 1 24); do [ "$(api 1 GET /api/ceph | py 'st=d.get("status") or {}; print(st.get("osds"), st.get("osdsUp"))' 2>/dev/null)" = "4 4" ] && break; sleep 5; done
check "4 OSDs" "$(api 1 GET /api/ceph | py 'st=d.get("status") or {}; print(st.get("osds"), st.get("osdsUp"), d.get("error",""))')" "4 4"
check "healthy after adding a disk" "$(wait_healthy 1 600)" "ok"
check "removal of a disk Ceph cannot do without refused" "$(api 1 DELETE /api/ceph/osds/$(api 1 GET /api/ceph | py 'print([o["id"] for o in d["osds"] if o["host"]=="ceph2"][0])'))" "cannot keep 3 copies"
OSD4=$(api 1 GET /api/ceph | py 'print(max(o["id"] for o in d["osds"]))')
check "maintenance on" "$(api 1 POST /api/ceph/maintenance -d '{"on":true}'; api 1 GET /api/ceph | py 'print(d["noout"], "OSDMAP_FLAGS" in [c["code"] for c in d["status"]["health"]["checks"]])')" "True True"
check "maintenance off" "$(api 1 POST /api/ceph/maintenance -d '{"on":false}'; api 1 GET /api/ceph | py 'print(d["noout"])')" "False"
T0=$(date +%s)
check "safe removal (out, wait, destroy)" "$(job 1 "$(api 1 DELETE /api/ceph/osds/$OSD4)" 600)" "success"
echo "  removal took $(( $(date +%s) - T0 )) s"
check "3 OSDs again, disk free" "$(api 1 GET /api/ceph | py 'print(d["status"]["osds"])') $(api 1 GET /api/storage/disks | py "print([x['usage'] for x in d if x['path']=='$D2'])")" "3 ['free']"
check "data intact" "$(N 1 ssh 'sudo cat /srv/strata/rbd1/f.txt /srv/strata/cephfs1/CephShare/hello.txt')" "block
hello ceph"

section "One NAS down, then back"
N 3 ssh 'sync' >/dev/null; kill -9 "$(cat vm/state-ceph3/qemu.pid)"
for _ in $(seq 1 30); do [ "$(api 1 GET /api/ceph | py 'print(d["status"]["osdsUp"])' 2>/dev/null)" = 2 ] && break; sleep 5; done
check "degraded" "$(health 1)" "HEALTH_WARN"
check "explained" "$(api 1 GET /api/ceph | py 'print([c["explain"][:20] for c in d["status"]["health"]["checks"] if c["code"] in ("OSD_DOWN","MON_DOWN")])')" "A disk daemon (OSD)"
check "quorum kept with 2 monitors" "$(api 2 GET /api/ceph | py 'print(len(d["status"]["quorum"]))')" "2"
check "CephFS still writable" "$(N 1 ssh 'echo during | sudo tee /srv/strata/cephfs1/CephShare/during.txt >/dev/null && sudo cat /srv/strata/cephfs1/CephShare/during.txt' 2>&1)" "during"
check "RBD still writable" "$(N 1 ssh 'echo during | sudo tee /srv/strata/rbd1/during.txt >/dev/null && sync && sudo cat /srv/strata/rbd1/during.txt' 2>&1)" "during"
check "the member shows offline" "$(api 1 GET /api/cluster | py 'print([m["online"] for m in d["members"] if m["name"]=="ceph3"])')" "[False]"
sleep 60
check "health change notified" "$(api 1 GET /api/system/events | py '[print(e["message"]) for e in d[:40] if e["category"]=="ceph"]'; api 2 GET /api/system/events | py '[print(e["message"]) for e in d[:40] if e["category"]=="ceph"]')" "Ceph cluster warning"
N 3 start >/dev/null
T0=$(date +%s)
for _ in $(seq 1 60); do curl -sf "http://localhost:$(port 3)/api/setup" >/dev/null && break; sleep 3; done
login 3
check "recovered" "$(wait_healthy 1 900)" "ok"
echo "  recovery took $(( $(date +%s) - T0 )) s"
check "back to 3 OSDs" "$(api 1 GET /api/ceph | py 'print(d["status"]["osdsUp"], len(d["status"]["quorum"]))')" "3 3"
for _ in $(seq 1 20); do N 3 ssh 'mountpoint -q /srv/strata/extfs' && break; sleep 10; done
check "external CephFS mounted again by StrataOS after the boot" "$(N 3 ssh 'sudo cat /srv/strata/extfs/CephShare/during.txt' 2>&1)" "during"

section "Removal"
check "a node with disks cannot leave Ceph" "$(job 1 "$(api 1 DELETE /api/ceph/nodes/$(api 1 GET /api/cluster | py 'print([m["id"] for m in d["members"] if m["name"]=="ceph3"][0])'))")" "still holds osd"
check "external volume removed" "$(api 3 DELETE "/api/storage/volumes/$(api 3 GET /api/storage/volumes | py 'print([v["id"] for v in d if v["name"]=="extfs"][0])')")" '"ok":true'
check "external cluster removed" "$(api 3 DELETE "/api/ceph/external/$EID") $(N 3 ssh "sudo ls /etc/ceph/strata/ | grep -c $EID || true")" "0"
check "destroy refused while volumes are used" "$(job 1 "$(api 1 DELETE '/api/ceph?confirm=Lab')")" "still uses"
api 1 DELETE /api/shares/CephShare >/dev/null
check "VM storage removed" "$(api 1 DELETE /api/ceph/vm-pools/ceph-local-rbd)" '"ok":true'
for n in 1 2; do for v in $(api $n GET /api/ceph/storage | py 'print(" ".join(v["volumeId"] for v in d["volumes"]))'); do
  check "Ceph volume removed on ceph$n" "$(api $n DELETE "/api/storage/volumes/$v?wipe=1")" '"ok":true'; done; done
check "RBD image deleted with its volume" "$(N 1 ssh 'sudo rbd ls rbd | grep -c nas1 || true') $(N 1 ssh 'ls /dev/rbd* 2>/dev/null | wc -l')" "0 0"
check "Ceph cluster destroyed" "$(job 1 "$(api 1 DELETE '/api/ceph?confirm=Lab')" 300)" "success"
check "disks free again" "$(for i in 1 2 3; do api "$i" GET /api/storage/disks | py 'print([x["usage"] for x in d if not x.get("system")], end=" ")'; done)" "['free', 'free'] ['free', 'free'] ['free', 'free']"
check "no Ceph daemon left" "$(for i in 1 2 3; do N "$i" ssh 'pgrep -c -x "ceph-(mon|mgr|osd|mds)" || true'; done | tr '\n' ' ')" "0 0 0"
for i in 2 3; do check "ceph$i removed from the cluster" "$(api 1 DELETE "/api/cluster/members/$(api 1 GET /api/cluster | py "print([m['id'] for m in d['members'] if m['name']=='ceph$i'][0])")")" '"ok":true'; done
check "cluster dissolved" "$(api 1 POST /api/cluster/leave) $(api 2 GET /api/cluster | py 'print(d["cluster"])')" "None"
check "Ceph component uninstalled" "$(job 1 "$(api 1 POST /api/system/components/ceph/uninstall -d '{}')") $(N 1 ssh 'command -v ceph-mon || echo gone')" "gone"

printf '\n%s passed, %s failed in %d min\n' "$PASSES" "$FAILS" $(( ($(date +%s) - START) / 60 ))
diag
cleanup_vms
[ "$FAILS" -eq 0 ]
