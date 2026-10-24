# LXC system containers (Virtual machines app): component, Debian image from
# images.linuxcontainers.org (download template, GPG checked; ~100 MB the
# first time), unprivileged container on an image file in "main", network,
# exec API, shared folder mount, limits, snapshots, backup and import on the
# Btrfs volume "fast", clone, rename, uninstall blocker, deletion.
section "Containers (LXC)"
CT=e2e-ct
ctjson() { c GET "/api/lxc/$1" | py "$2"; }
cexec() { # container, command words... → stdout (exit code appended when not 0)
  local n=$1; shift
  c POST "/api/lxc/$n/exec" -d "$(python3 -c 'import json,sys; print(json.dumps({"command": sys.argv[1:], "timeout": 120}))' "$@")" |
    py 'print(d.get("stdout","").strip() + ("" if d.get("exitCode")==0 else " [exit %s] %s %s" % (d.get("exitCode"), d.get("stderr","").strip(), d.get("error",""))))'
}
wait_state() { for _ in $(seq 1 90); do [ "$(ctjson "$1" 'print(d.get("state"))')" = "$2" ] && break; sleep 2; done; ctjson "$1" 'print(d.get("state"))'; }

if [ "$(c GET /api/system/components | py 'print(d.get("lxc"))')" != True ]; then
  check "LXC component installed" "$(job "$(c POST /api/system/components/lxc/install)")" "success"
fi
check "LXC reported installed" "$(c GET /api/lxc | py 'print(d["installed"], d["natBridge"])')" "True lxcbr0"
check "NAT bridge up" "$(vm "ip -br addr show lxcbr0")" "10.0.3.1"
check "image index lists Debian 13" "$(c GET /api/lxc/templates | py 'print([i["label"] for i in d["images"] if i["distro"]=="debian" and i["release"]=="trixie"])')" "Debian 13 (trixie)"
check "storage locations" "$(c GET /api/lxc/storage | py 'print(sorted((l["label"], l["backends"][0]) for l in d if l["label"] in ("main","fast")))')" "[('fast', 'btrfs'), ('main', 'image')]"
check "invalid name refused" "$(c POST /api/lxc -d '{"name":"Bad_Name"}')" "invalid container name"
check "mount of an unknown share refused" "$(c POST /api/lxc -d '{"name":"x1","location":"/srv/strata/main","backend":"image","sizeGb":2,"mounts":[{"share":"Nope","target":"/mnt/x"}]}')" "unknown shared folder"
check "location outside the volumes refused" "$(c POST /api/lxc -d '{"name":"x1","location":"/etc","backend":"dir"}')" "inside a volume"

vm "echo shared-file-ok | sudo tee $P/lxc-e2e.txt >/dev/null; sudo chown $USER_NAME $P/lxc-e2e.txt"
AUID=$(vm "id -u $USER_NAME")
CREATE=$(c POST /api/lxc -d "{\"name\":\"$CT\",\"image\":{\"distro\":\"debian\",\"release\":\"trixie\"},\"location\":\"/srv/strata/main\",\"backend\":\"image\",\"sizeGb\":3,
  \"cpus\":1,\"memoryMb\":256,\"swapMb\":0,\"network\":{\"mode\":\"nat\"},\"mounts\":[{\"share\":\"Photos\",\"target\":\"/mnt/photos\",\"readOnly\":true}],
  \"mapUsers\":[\"$USER_NAME\"],\"start\":true,\"rootPassword\":\"Ct-Root-Pass-1\"}")
check "Debian container created from the download template" "$(job "$CREATE")" "success"
check "container running" "$(wait_state $CT running)" "running"
IP=""
for _ in $(seq 1 30); do IP=$(ctjson $CT 'print(" ".join(d["ips"]))'); case "$IP" in 10.0.3.*) break ;; esac; sleep 2; done
check "container got an address on the NAT network" "$IP" "10.0.3."
check "listed with the VMs' API shape" "$(c GET /api/lxc | py "print([(x['name'], x['state'], x['storage']['backend'], x['privileged']) for x in d['containers'] if x['name']=='$CT'])")" "('$CT', 'running', 'image', False)"
check "command run through the API" "$(cexec $CT sh -c 'echo hello; id -u')" "hello
0"
check "host name" "$(cexec $CT cat /etc/hostname)" "$CT"
FUID=$(vm "sudo stat -c %u /srv/strata/main/.strata-lxc/$CT/rootfs/etc/passwd")
check "unprivileged: files owned by shifted ids" "$([ "${FUID:-0}" -ge 100000 ] && echo shifted || echo "uid $FUID")" "shifted"
PUID=$(vm "ps -o uid= -p \$(sudo lxc-info -P /var/lib/strata/lxc -n $CT -p -H)" | tr -d ' ')
check "container root is not the NAS's root" "$([ "${PUID:-0}" -ge 100000 ] && echo unprivileged || echo "uid $PUID")" "unprivileged"
check "shared folder mounted read-only" "$(cexec $CT cat /mnt/photos/lxc-e2e.txt)" "shared-file-ok"
check "read-only mount refuses writes" "$(cexec $CT sh -c 'touch /mnt/photos/x 2>&1')" "Read-only"
check "mapped user keeps its uid inside" "$(cexec $CT stat -c %u /mnt/photos/lxc-e2e.txt)" "$AUID"
check "memory limit applied (memory.max)" "$(cexec $CT cat /sys/fs/cgroup/memory.max)" "268435456"
check "CPU limit applied (cpu.max)" "$(cexec $CT cat /sys/fs/cgroup/cpu.max)" "100000 100000"
check "root password set" "$(cexec $CT sh -c 'grep ^root: /etc/shadow | cut -d: -f2 | cut -c1')" '$'
check "network reaches the internet" "$(cexec $CT sh -c 'getent hosts deb.debian.org >/dev/null && echo dns-ok')" "dns-ok"
check "stats from cgroup v2" "$(c GET "/api/lxc/$CT/stats?disk=1" | py 'print(d["memory"]>0, d["memoryMax"], d["cpuUsec"]>0, d["netRx"]>0, d["diskLimit"])')" "True 268435456 True True 3221225472"
# Interactive shell over the websocket (the browser terminal's protocol).
WSPY=$(mktemp)
cat > "$WSPY" <<'PY'
import base64, json, os, socket, sys, time, urllib.parse
base, jar, name = sys.argv[1:4]
u = urllib.parse.urlparse(base)
cookies = "; ".join(f"{f[5]}={f[6]}" for f in (l.rstrip("\n").split("\t") for l in open(jar) if l.count("\t") == 6))
s = socket.create_connection((u.hostname, u.port or 80), timeout=20)
key = base64.b64encode(os.urandom(16)).decode()
s.sendall((f"GET /api/lxc/{name}/console?mode=shell HTTP/1.1\r\nHost: {u.netloc}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
           f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nCookie: {cookies}\r\nX-Strata: 1\r\n\r\n").encode())
head = b""
while b"\r\n\r\n" not in head:
    head += s.recv(1)
if b" 101 " not in head.split(b"\r\n")[0]:
    print("no upgrade:", head.split(b"\r\n")[0].decode()); sys.exit()
def send(obj):
    p, m = json.dumps(obj).encode(), os.urandom(4)
    s.sendall(bytes([0x81, 0x80 | 126]) + len(p).to_bytes(2, "big") + m + bytes(b ^ m[i % 4] for i, b in enumerate(p)))
send({"type": "resize", "cols": 100, "rows": 30})
time.sleep(1)
send({"type": "input", "data": "echo ws-$((40+2))\n"})
out, end = b"", time.time() + 15
while time.time() < end and b"ws-42" not in out:
    try:
        out += s.recv(65536)
    except socket.timeout:
        break
print("ws-42" if b"ws-42" in out else "no output: " + repr(out[-200:]))
PY
WS=$(python3 "$WSPY" "$B" "$J" "$CT"); rm -f "$WSPY"
check "shell in the browser terminal (websocket)" "$WS" "ws-42"
check "systemd unit of the container" "$(vm "systemctl is-active strata-lxc@$CT.service")" "active"

# Limits changed live, mount changed for the next start.
SET=$(ctjson $CT 'import json; d["memoryMb"]=384; d["mounts"]=[{"share":"Photos","target":"/mnt/photos","readOnly":False}]; print(json.dumps(d))')
check "settings changed (mount needs a restart)" "$(c PUT /api/lxc/$CT -d "$SET")" '"restart":true'
check "memory limit changed live" "$(cexec $CT cat /sys/fs/cgroup/memory.max)" "402653184"
check "config regenerated" "$(vm "sudo grep -c 'mnt/photos none bind,create=dir 0 0' /var/lib/strata/lxc/$CT/config")" "1"

# Survives a restart of StrataOS (own systemd unit).
vm "sudo systemctl restart strata"; wait_api
c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
check "still running after StrataOS restarted" "$(ctjson $CT 'print(d["state"])')" "running"

# Snapshot, change, restore.
cexec $CT sh -c 'echo before > /root/marker' >/dev/null
check "snapshot taken while running" "$(job "$(c POST /api/lxc/$CT/snapshots -d '{"name":"s1","description":"first"}')")" "success"
check "snapshot listed" "$(c GET /api/lxc/$CT/snapshots | py 'print([(s["name"], s["description"], s["running"]) for s in d])')" "('s1', 'first', True)"
cexec $CT sh -c 'echo after > /root/marker' >/dev/null
check "restore refused while running" "$(job "$(c POST /api/lxc/$CT/snapshots/s1/restore)")" "stop $CT first"
c POST /api/lxc/$CT/stop >/dev/null
check "stopped cleanly" "$(wait_state $CT stopped)" "stopped"
check "restored to the snapshot" "$(job "$(c POST /api/lxc/$CT/snapshots/s1/restore)")" "success"
c POST /api/lxc/$CT/start >/dev/null
check "running again" "$(wait_state $CT running)" "running"
check "file back to its snapshot content" "$(cexec $CT cat /root/marker)" "before"
check "read-write mount after the restart" "$(cexec $CT sh -c 'touch /mnt/photos/.lxc-rw && echo rw-ok')" "rw-ok"
vm "sudo rm -f $P/.lxc-rw"
check "snapshot deleted" "$(c DELETE /api/lxc/$CT/snapshots/s1 >/dev/null; c GET /api/lxc/$CT/snapshots)" "[]"

# Backup (live, from a snapshot of the image), import on Btrfs.
vm "sudo mkdir -p /srv/strata/main/lxc-backups"
check "backup written" "$(job "$(c POST /api/lxc/$CT/backup -d '{"dir":"/srv/strata/main/lxc-backups","keep":2}')")" "success"
ARCH=$(c GET "/api/lxc/$CT/backups?dir=/srv/strata/main/lxc-backups" | py 'print(d[0]["path"] if d else "")')
check "backup listed" "$ARCH" ".tar.zst"
check "archive holds container-relative owners" "$(vm "sudo tar --zstd -tvf $ARCH rootfs/etc/passwd")" "0/0"
check "backup offered for import" "$(c GET /api/lxc/importable | py "print([x['kind'] for x in d if x['path']=='$ARCH'])")" "archive"
check "backup imported as a new container on Btrfs" "$(job "$(c POST /api/lxc/import -d "{\"source\":\"$ARCH\",\"name\":\"e2e-imp\",\"location\":\"/srv/strata/fast\",\"backend\":\"btrfs\",\"sizeGb\":3,\"start\":true}")")" "success"
check "imported container runs" "$(wait_state e2e-imp running)" "running"
check "imported files" "$(cexec e2e-imp sh -c 'cat /root/marker; cat /etc/hostname')" "before
e2e-imp"
check "imported container has its own MAC" "$(c GET /api/lxc | py "m={x['name']: x['network']['mac'] for x in d['containers']}; print(m['$CT'] != m['e2e-imp'])")" "True"
check "root on a Btrfs subvolume with a quota" "$(vm "sudo btrfs subvolume show /srv/strata/fast/.strata-lxc/e2e-imp/rootfs | grep -i 'limit\|name'")" "rootfs"
c POST /api/lxc/e2e-imp/kill >/dev/null
check "force stop" "$(wait_state e2e-imp stopped)" "stopped"
check "Btrfs snapshot" "$(job "$(c POST /api/lxc/e2e-imp/snapshots -d '{"name":"b1"}')")" "success"
check "clone of a stopped container" "$(job "$(c POST /api/lxc/e2e-imp/clone -d '{"name":"e2e-clone"}')")" "success"
check "clone has its own host name" "$(vm "sudo cat /srv/strata/fast/.strata-lxc/e2e-clone/rootfs/etc/hostname")" "e2e-clone"
check "rename" "$(c POST /api/lxc/e2e-clone/rename -d '{"name":"e2e-renamed"}'; c GET /api/lxc | py 'print(sorted(x["name"] for x in d["containers"]))')" "'e2e-renamed'"
check "renamed host name" "$(vm "sudo cat /srv/strata/fast/.strata-lxc/e2e-renamed/rootfs/etc/hostname")" "e2e-renamed"

# The component cannot go while containers exist.
check "uninstall blocked by the containers" "$(c GET /api/system/components/lxc/uninstall | py 'print(d["blockers"][0]["message"])')" "Containers (LXC):"

# Deletion.
check "delete refused while running" "$(c DELETE /api/lxc/$CT)" "stop $CT first"
c POST /api/lxc/$CT/kill >/dev/null; wait_state $CT stopped >/dev/null
for n in $CT e2e-imp e2e-renamed; do c DELETE /api/lxc/$n >/dev/null; done
check "containers deleted" "$(c GET /api/lxc | py 'print(len(d["containers"]))')" "0"
check "their storage removed" "$(vm "sudo ls -A /srv/strata/main/.strata-lxc /srv/strata/fast/.strata-lxc; sudo btrfs subvolume list /srv/strata/fast | grep -c strata-lxc; systemctl list-units --all 'strata-lxc*' --no-legend | grep -c rootfs")" "0
0"
vm "sudo rm -rf /srv/strata/main/lxc-backups $P/lxc-e2e.txt"
