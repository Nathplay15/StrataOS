#!/bin/bash
# Installs KVM/libvirt for virtual machines.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
case "$(dpkg --print-architecture)" in
  arm64) emu="qemu-system-arm qemu-efi-aarch64" ;;
  *) emu="qemu-system-x86 ovmf" ;;
esac
apt-get install -y --no-install-recommends $emu qemu-utils libvirt-daemon-system libvirt-clients virtinst swtpm swtpm-tools dnsmasq-base xorriso pciutils
systemctl enable --now libvirtd.service
virsh --connect qemu:///system net-autostart default || true
virsh --connect qemu:///system net-start default || true
[ -e /dev/kvm ] && echo ">> KVM acceleration available" || echo ">> warning: /dev/kvm missing (enable virtualisation in the BIOS)"
echo ">> Virtualisation is ready"
