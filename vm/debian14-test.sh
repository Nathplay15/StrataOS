#!/bin/bash
# Smoke test of StrataOS on Debian 14 (forky, testing): a VM from the daily
# forky cloud image, the package installed on it, then the first sections of
# the end-to-end test (setup, storage with RAID/Btrfs/LVM, users and SMB/NFS
# shares, file manager security, Docker apps, ZFS built by DKMS) and a few
# Debian 14 checks (Windows discovery, directory clients without sssd).
#   vm/debian14-test.sh [dist/strataos_<version>_amd64.deb]
# VM_DEBIAN=13 runs the same subset on Debian 13 for comparison. The VM is
# destroyed at the end unless KEEP_VM=1. See docs/DEBIAN-14.md.
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${1:-$(ls dist/strataos_*_amd64.deb 2>/dev/null | sort -V | tail -1)}
[ -f "$DEB" ] || { echo "missing .deb (make deb)"; exit 1; }
export VM_DEBIAN=${VM_DEBIAN:-14} VM_DISKS=${VM_DISKS:-13} VM_EXTRA_PORTS=${VM_EXTRA_PORTS-}
export STRATA_URL=${STRATA_URL:-http://localhost:${VM_WEB_PORT:-7070}}

./vm/vm.sh destroy >/dev/null 2>&1
./vm/vm.sh create || exit 1
[ "${KEEP_VM:-}" = 1 ] || trap './vm/vm.sh destroy >/dev/null 2>&1' EXIT
./vm/vm.sh ssh 'echo "== $(. /etc/os-release; echo $PRETTY_NAME), kernel $(uname -r)"'
./vm/vm.sh deploy "$DEB" || exit 1

# The end-to-end test up to (not including) its Notifications section, then
# the checks below, in one script so they share its helpers and session.
T=$(mktemp --suffix=.sh vm/.debian14-e2e-XXXX) # in vm/: the test cds to its parent folder
# Jobs may wait 30 min instead of 10: the ZFS module build is slower on 14.
sed -n '1,/^section "Notifications"/p' vm/e2e-test.sh | sed '$d' | sed 's/for _ in $(seq 1 300); do/for _ in $(seq 1 900); do/' > "$T"
cat >> "$T" <<'EOF'

section "Debian release"
check "Windows discovery service" "$(vm "systemctl is-active -q wsdd2.service || systemctl is-active -q wsdd-server.service && echo running")" "running"
check "directory clients install" "$(job "$(c POST /api/system/components/directory/install)")" "success"
check "Active Directory client present" "$(c GET /api/directory | py 'print(d["adInstalled"])')" "True"
check "Samba configuration valid" "$(vm "sudo testparm -s >/dev/null 2>/tmp/tp; echo rc=\$?")" "rc=0"
check "nftables address blocking loads" "$(vm "sudo nft list tables" | tr '\n' ' ')" "table"
echo "  (info) LDAP client: $(c GET /api/directory | py 'print(d["ldapInstalled"])'), rclone: $(vm 'command -v rclone || echo missing')"
echo "  (info) $(vm '/usr/sbin/smbd --version; docker --version; dpkg -l sssd-ldap rclone wsdd2 wsdd-server 2>/dev/null | awk "/^ii/ {print \$2, \$3}"; zfs version | head -1; systemctl --version | head -1; python3 --version' | tr '\n' ';')"

printf '\n%s passed, %s failed\n' "$PASSES" "$FAILS"
[ "$FAILS" -eq 0 ]
EOF
bash "$T"
rc=$?
rm -f "$T"
exit $rc
