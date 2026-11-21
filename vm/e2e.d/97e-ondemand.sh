# shellcheck shell=bash
# Files on demand of the desktop app on Linux (desktop/internal/ondemand):
# the headless client (built here) mounts the shared folders with FUSE and
# serves them with `strataos-sync daemon`, against the forwarded HTTPS port.
# Sizes without downloading, a ranged read, a whole read, keep on this
# device / free up space, new files, edits, an editor's save through a
# temporary file, renames, deletions, a change made on the NAS showing up,
# encrypted folders left out. The mount runs on the host when it can
# (fusermount3), else in a container (root, /dev/fuse): ONDEMAND_DOCKER=1
# forces that.
section "Desktop app: files on demand (FUSE)"
HTTPS="https://localhost:${VM_HTTPS_PORT:-7443}"
OP="$P/OnDemand"
GO=$(command -v go || echo "$HOME/.local/opt/go/bin/go")
S="$TMP/strataos-sync"
[ -x "$S" ] || (cd desktop && CGO_ENABLED=0 "$GO" build -o "$S" ./cmd/strataos-sync) 2>&1 | tail -3
OC="$TMP/od-config" MNT="$TMP/od-mnt"
PR=$(u POST /api/me/devices/pairing)
check "paired" "$(STRATA_LANG=en "$S" --config "$OC" pair --server "$HTTPS" --code "$(echo "$PR" | py 'print(d["code"])')" --fingerprint "$(echo "$PR" | py 'print(d["fingerprint"])')" --name ondemand-e2e 2>&1)" "paired with"

# What the NAS holds: a 3 MiB file, a text, a folder, an encrypted folder.
vm "sudo mkdir -p '$OP/sub' '$OP/vault' && sudo head -c 3145728 /dev/urandom | sudo tee '$OP/big.bin' >/dev/null && echo 'hello' | sudo tee '$OP/hello.txt' >/dev/null && echo 'inside' | sudo tee '$OP/sub/in.txt' >/dev/null && echo '{\"v\":1}' | sudo tee '$OP/vault/.strataos-sync-e2e.json' >/dev/null && sudo chown -R alice: '$OP'"
NAS_SUM=$(vm "sudo md5sum '$OP/big.bin'" | cut -d' ' -f1)
NAS_PART=$(vm "sudo dd if='$OP/big.bin' bs=1 skip=2097152 count=100 2>/dev/null | md5sum" | cut -d' ' -f1)

check "turned on" "$(STRATA_LANG=en "$S" --config "$OC" ondemand on --path "$MNT" 2>&1)" "files on demand: true"
if [ "${ONDEMAND_DOCKER:-}" != 1 ] && command -v fusermount3 >/dev/null && ! grep -q '^NoNewPrivs:[[:space:]]*1' /proc/self/status; then
  "$S" --config "$OC" daemon >"$TMP/od-daemon.log" 2>&1 &
  OD_PID=$!
  odx() { sh -c "$1"; }
  od_stop() { kill "$OD_PID" 2>/dev/null; wait "$OD_PID" 2>/dev/null; fusermount3 -u -z "$MNT" 2>/dev/null; }
else
  docker rm -f strataos-od >/dev/null 2>&1
  docker run -d --name strataos-od --network host --device /dev/fuse --cap-add SYS_ADMIN --security-opt apparmor:unconfined \
    -v "$S:/usr/local/bin/strataos-sync:ro" -v "$TMP:$TMP" debian:trixie strataos-sync --config "$OC" daemon >/dev/null
  odx() { docker exec strataos-od sh -c "$1"; }
  od_stop() { # the container runs as root: give the settings folder back
    docker logs strataos-od >"$TMP/od-daemon.log" 2>&1
    docker exec strataos-od chown -R "$(id -u):$(id -g)" "$OC"
    docker rm -f strataos-od >/dev/null
  }
fi
wait_od() { # wait_od "shell test" (inside the mount)
  for _ in $(seq 1 60); do odx "$1" >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}
D="$MNT/Photos/OnDemand"
wait_od "test -f '$D/hello.txt'"
check "shared folders listed" "$(odx "ls '$MNT'")" "Photos"
check "folder contents" "$(odx "ls '$D' | tr '\n' ' '")" "big.bin hello.txt sub"
check "encrypted folder left out" "$(odx "ls -a '$D' | grep -c vault")" "0"
check "size without download, no space used" "$(odx "stat -c '%s %b' '$D/big.bin'")" "3145728 0"
check "part of a file" "$(odx "dd if='$D/big.bin' bs=1 skip=2097152 count=100 2>/dev/null | md5sum | cut -d' ' -f1")" "$NAS_PART"
sleep 2 # attributes are cached a second
check "only that part downloaded" "$(odx "test \$(stat -c %b '$D/big.bin') -gt 0 && test \$(stat -c %b '$D/big.bin') -lt 6144 && echo partial")" "partial"
check "whole file" "$(odx "md5sum '$D/big.bin' | cut -d' ' -f1")" "$NAS_SUM"
check "free up space" "$(odx "'$S' unpin '$D/big.bin' 2>&1; stat -c %b '$D/big.bin'")" "0"
odx "'$S' pin '$D/sub'"
wait_od "test \$(stat -c %b '$D/sub/in.txt') -gt 0"
check "keep a folder on this device" "$(odx "test \$(stat -c %b '$D/sub/in.txt') -gt 0 && echo kept")" "kept"

# Changes made here go to the NAS.
odx "echo 'from the mount' > '$D/new.txt'"
for _ in $(seq 1 20); do [ "$(vm "sudo cat '$OP/new.txt' 2>/dev/null")" = "from the mount" ] && break; sleep 1; done
check "new file sent" "$(vm "sudo cat '$OP/new.txt'")" "from the mount"
check "owned by the user" "$(vm "sudo stat -c %U '$OP/new.txt'")" "alice"
odx "echo 'world' >> '$D/hello.txt'"
for _ in $(seq 1 20); do [ "$(vm "sudo cat '$OP/hello.txt' | wc -l")" = 2 ] && break; sleep 1; done
check "edit sent" "$(vm "sudo cat '$OP/hello.txt' | tr '\n' ' '")" "hello world"
odx "printf 'rewritten\n' > '$D/.hello.txt.swp' && mv '$D/.hello.txt.swp' '$D/hello.txt'"
for _ in $(seq 1 20); do [ "$(vm "sudo cat '$OP/hello.txt'")" = "rewritten" ] && break; sleep 1; done
check "editor's save through a temporary file" "$(vm "sudo cat '$OP/hello.txt'; ls -a '$OP' | grep -c swp")" "rewritten"
odx "mkdir '$D/made' && mv '$D/new.txt' '$D/made/moved.txt'"
check "folder and move on the NAS" "$(vm "sudo cat '$OP/made/moved.txt'")" "from the mount"
odx "rm '$D/made/moved.txt' && rmdir '$D/made'"
check "deletions on the NAS" "$(vm "test -e '$OP/made' && echo there || echo gone")" "gone"

# A change on the NAS shows here (inotify on the NAS, long poll here).
vm "echo 'changed on the NAS' | sudo tee '$OP/sub/in.txt' >/dev/null"
wait_od "grep -q 'changed on the NAS' '$D/sub/in.txt'"
check "NAS change seen" "$(odx "cat '$D/sub/in.txt'")" "changed on the NAS"
wait_od "test \$(stat -c %b '$D/sub/in.txt') -gt 0"
check "kept file downloaded again" "$(odx "test \$(stat -c %b '$D/sub/in.txt') -gt 0 && echo kept")" "kept"
vm "echo 'brand new' | sudo tee '$OP/sub/later.txt' >/dev/null"
wait_od "test -f '$D/sub/later.txt'"
check "NAS new file in a kept folder" "$(odx "cat '$D/sub/later.txt'")" "brand new"

od_stop
check "turned off" "$(STRATA_LANG=en "$S" --config "$OC" ondemand off 2>&1)" "files on demand: false"
"$S" --config "$OC" unpair >/dev/null 2>&1
vm "sudo rm -rf '$OP'"
