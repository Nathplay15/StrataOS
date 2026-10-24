#!/bin/bash
# Installs LXC for system containers (Virtual machines app): the lxc tools
# with their "download" template (images checked with GPG), lxcfs (the
# container sees its own limits in /proc), the subordinate id tools for
# unprivileged containers, and the NAT network of lxc-net (lxcbr0).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends lxc lxcfs uidmap dnsmasq-base gnupg dirmngr wget ca-certificates xz-utils zstd \
  apparmor nftables acl e2fsprogs
# Private network behind the NAS (10.0.3.0/24, DHCP and DNS by dnsmasq).
if [ ! -s /etc/default/lxc-net ] || ! grep -q '^USE_LXC_BRIDGE=' /etc/default/lxc-net; then
  cat > /etc/default/lxc-net <<'EOF'
# Written by StrataOS: NAT network of the containers.
USE_LXC_BRIDGE="true"
LXC_BRIDGE="lxcbr0"
LXC_ADDR="10.0.3.1"
LXC_NETMASK="255.255.255.0"
LXC_NETWORK="10.0.3.0/24"
LXC_DHCP_RANGE="10.0.3.2,10.0.3.254"
LXC_DHCP_MAX="253"
EOF
fi
# StrataOS starts the containers itself (strata-lxc@<name>.service, ordered);
# lxc.service would start the ones with lxc.start.auto from /var/lib/lxc only.
systemctl enable --now lxcfs.service || true
systemctl enable lxc-net.service
systemctl restart lxc-net.service
mkdir -p /var/lib/strata/lxc /var/lib/strata/lxc-cache
chmod 750 /var/lib/strata/lxc
ip link show lxcbr0 >/dev/null 2>&1 && echo ">> NAT network lxcbr0 ready" || echo ">> warning: lxcbr0 is missing (systemctl status lxc-net)"
[ -f /sys/fs/cgroup/cgroup.controllers ] || echo ">> warning: cgroup v2 is not mounted: limits will not apply"
echo ">> Containers (LXC) are ready"
