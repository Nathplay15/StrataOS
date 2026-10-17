#!/bin/bash
# Installs KVM/libvirt for virtual machines.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients virtinst ovmf swtpm swtpm-tools dnsmasq-base
systemctl enable --now libvirtd.service
virsh --connect qemu:///system net-autostart default || true
virsh --connect qemu:///system net-start default || true
[ -e /dev/kvm ] && echo ">> KVM acceleration available" || echo ">> warning: /dev/kvm missing (enable virtualisation in the BIOS)"
echo ">> Virtualisation is ready"
