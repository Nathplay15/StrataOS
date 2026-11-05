#!/bin/bash
# Installs a built strataos .deb in a Debian container (no systemd) to catch
# dependency breakage on the next Debian release early:
#   packaging/test-install.sh <suite> [dist/strataos_<version>_amd64.deb]
# suite: trixie (Debian 13, the target), forky (Debian 14, testing), sid.
# Checks that the Depends resolve, that the Recommends missing from the suite
# are only those known to be missing (KNOWN_MISSING), that postinst runs, that
# the daemon starts and answers, and simulates the installation of every
# optional component (Settings > Components) with the suite's packages.
set -euo pipefail
cd "$(dirname "$0")/.."
SUITE=${1:?usage: $0 <suite> [deb]}
DEB=${2:-$(ls dist/strataos_*_amd64.deb 2>/dev/null | sort -V | tail -1)}
[ -f "$DEB" ] || { echo "missing .deb (make deb)"; exit 1; }
# Packages known to be missing from a suite (temporarily removed from Debian
# testing, see docs/DEBIAN-14.md): reported, not failed. Anything else fails.
KNOWN_MISSING=${KNOWN_MISSING:-$([ "$SUITE" = trixie ] && echo "" || echo "wsdd2 rclone sssd-ldap libnss-sss libpam-sss")}
# Images from Docker Hub (debian:<suite>); IMAGE overrides it (mirror, cache).
IMAGE=${IMAGE:-debian:$SUITE}

docker run --rm -i -e KNOWN_MISSING="$KNOWN_MISSING" -e DEBIAN_FRONTEND=noninteractive \
  -v "$PWD/$DEB:/strataos.deb:ro" -v "$PWD/packaging/scripts:/scripts:ro" "$IMAGE" bash -euo pipefail <<'EOF'
. /etc/os-release
echo ">> $PRETTY_NAME ($VERSION_CODENAME)"
# contrib for ZFS, as the installer and install-zfs.sh enable it.
sed -i -E '/^Components:/ { /contrib/! s/$/ contrib/ }' /etc/apt/sources.list.d/debian.sources
apt-get update -qq
fail=0
missing() { case " $KNOWN_MISSING " in *" $1 "*) echo "   known missing: $1" ;; *) echo "!! missing: $1"; fail=1 ;; esac; }
has() { [ -n "$(apt-cache policy "$1" 2>/dev/null | awk '/Candidate:/ && $2 != "(none)" { print $2 }')" ]; }

echo ">> Recommends of the package"
reco=$(dpkg-deb -f /strataos.deb Recommends)
IFS=, read -ra alts <<<"$reco"
for a in "${alts[@]}"; do
  ok=0
  IFS='|' read -ra opts <<<"$a"
  for o in "${opts[@]}"; do o=$(echo "$o" | awk '{print $1}'); has "$o" && ok=1 && break; done
  [ $ok = 1 ] || missing "$(echo "${opts[0]}" | awk '{print $1}')"
done

echo ">> Installing $(dpkg-deb -f /strataos.deb Package) $(dpkg-deb -f /strataos.deb Version) with its Recommends"
# No init system in the container: invoke-rc.d is told to start nothing.
printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod 755 /usr/sbin/policy-rc.d
apt-get install -y -qq /strataos.deb >/tmp/install.log 2>&1 || { tail -40 /tmp/install.log; echo "!! installation failed"; exit 1; }
grep -E "^(E|W): " /tmp/install.log || true
dpkg-query -W -f '${Status}\n' strataos | grep -q "install ok installed" || { echo "!! strataos not configured"; exit 1; }
tail -3 /tmp/install.log
for p in samba nfs-kernel-server mdadm nftables docker.io avahi-daemon restic; do
  printf '   %-20s %s\n' "$p" "$(dpkg-query -W -f '${Version}' "$p" 2>/dev/null || echo '-')"
done
[ -L /etc/systemd/system/multi-user.target.wants/strata.service ] || { echo "!! strata.service not enabled"; fail=1; }
[ -L /etc/systemd/system/samba-ad-dc.service ] || { echo "!! samba-ad-dc.service not masked"; fail=1; }
grep -q "^HOMEHOST <ignore>" /etc/mdadm/mdadm.conf || { echo "!! mdadm.conf not prepared"; fail=1; }

echo ">> Starting the daemon"
apt-get install -y -qq curl >/dev/null
strata -version
strata -https "" -http 127.0.0.1:7070 >/tmp/strata.log 2>&1 &
pid=$!
for i in $(seq 1 30); do
  out=$(curl -fsS http://127.0.0.1:7070/api/setup 2>/dev/null) && break
  sleep 1
done
kill $pid 2>/dev/null || true
if [ -n "${out:-}" ]; then echo "   GET /api/setup: $out"; else echo "!! the daemon does not answer"; tail -20 /tmp/strata.log; fail=1; fi

echo ">> Optional components (simulated, with the packages of their scripts)"
for s in /scripts/install-*.sh; do
  name=$(basename "$s" .sh); name=${name#install-}
  # The words of its "apt-get install" commands (with continuation lines).
  pkgs=$(awk '/apt-get install/ { on = 1; sub(/.*apt-get install/, "") } on { print; if ($0 !~ /\\$/) on = 0 }' "$s" | tr -d '\\' | tr ' ' '\n' |
    grep -E '^[a-z0-9][a-z0-9.+-]+$' | grep -vE '^(fi|then)$' | sort -u || true)
  # Vendor repositories (Tailscale, Cloudflare) are not added here.
  pkgs=$(for p in $pkgs; do case $p in tailscale | cloudflared) ;; *) echo "$p" ;; esac; done)
  # Packages chosen at run time by the script.
  [ "$name" = zfs ] && pkgs="$pkgs zfs-dkms linux-headers-amd64 dpkg-dev"
  [ "$name" = vms ] && pkgs="$pkgs qemu-system-x86 ovmf libvirt-daemon-driver-storage-rbd qemu-block-extra"
  [ "$name" = photos ] && pkgs="$pkgs libvips-tools libheif-plugin-libde265 ffmpeg libimage-exiftool-perl intel-media-va-driver mesa-va-drivers"
  [ -n "$pkgs" ] || continue
  avail="" ; for p in $pkgs; do if has "$p"; then avail="$avail $p"; else missing "$p"; fi; done
  [ -n "$avail" ] || continue
  if apt-get install -s -qq --no-install-recommends $avail >/tmp/sim.log 2>&1; then
    echo "   $name: ok"
  else
    echo "!! $name:"; grep -vE "^(Inst|Conf) " /tmp/sim.log | tail -10; fail=1
  fi
done
exit $fail
EOF
