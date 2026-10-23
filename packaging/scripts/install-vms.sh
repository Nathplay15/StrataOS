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
# libvirtd's socket can start the daemon during the install, before the
# package has created its libvirt-qemu user: it then fails in a loop and hits
# systemd's start limit. Clear that and start it now that everything exists.
systemctl reset-failed libvirtd.service libvirtd.socket libvirtd-ro.socket libvirtd-admin.socket 2>/dev/null || true
systemctl enable libvirtd.service
for i in 1 2 3; do
  systemctl restart libvirtd.service && break
  sleep 3
  systemctl reset-failed libvirtd.service 2>/dev/null || true
done
systemctl is-active --quiet libvirtd.service || { echo ">> libvirtd does not start"; systemctl status libvirtd.service --no-pager || true; exit 1; }
virsh --connect qemu:///system net-autostart default || true
virsh --connect qemu:///system net-start default || true
[ -e /dev/kvm ] && echo ">> KVM acceleration available" || echo ">> warning: /dev/kvm missing (enable virtualisation in the BIOS)"
echo ">> Virtualisation is ready"
