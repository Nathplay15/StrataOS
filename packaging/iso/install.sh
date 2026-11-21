#!/bin/bash
# Runs inside the freshly installed system (preseed late_command).
exec > >(tee -a /var/log/strataos-install.log) 2>&1
set -eux
export DEBIAN_FRONTEND=noninteractive
# The installation CD is not mounted inside the target: its APT entry would make
# apt-get update fail.
sed -i -E 's/^(deb(-src)? cdrom:)/# \1/' /etc/apt/sources.list
apt-get update
apt-get install -y /root/strataos/strataos_*.deb
# System on the data disks: the free space after the system on each disk of
# the system mirror becomes a data partition (GPT name "StrataOS data").
if [ -e /root/strataos/datadisks ]; then
  root=$(findmnt -n -o SOURCE -T / | sed 's/\[.*//')
  for m in $(mdadm --detail "$root" | sed -n 's|.*active sync.* \(/dev/[^ ]*\)$|\1|p'); do
    disk=/dev/$(lsblk -n -d -o PKNAME "$m" | head -1)
    case "$disk" in *[0-9]) part4=${disk}p4 ;; *) part4=${disk}4 ;; esac
    if lsblk -n -d -o PARTLABEL "$part4" 2>/dev/null | grep -qx 'StrataOS data'; then
      continue
    fi
    if [ -b "$part4" ]; then
      # The installer left partition 4 unused for the data: name it.
      sgdisk -t 4:8300 -c 4:'StrataOS data' "$disk"
      wipefs -a "$part4" || true
    else
      sgdisk -n 4:0:0 -t 4:8300 -c 4:'StrataOS data' "$disk"
    fi
    partprobe "$disk" || true
  done
  udevadm settle || true
fi
# System on two disks (mirror): the second disk gets a copy of the EFI system
# partition and its own UEFI boot entry (the installer fills only the first).
/usr/lib/strata/scripts/esp-sync --setup || echo "warning: the EFI system partition was not copied (done again at first start)"
# Nobody knows root's install-time password: lock it for good.
passwd -l root
# The login prompts of the machine (tty2-6, serial; tty1 shows the StrataOS
# screen, see strata-console.service) show where to open the web interface
# (\4 is replaced by agetty with the IPv4 address).
cat > /etc/issue <<'ISSUE'

  StrataOS — \n

  Web interface: http://\4:7070   (HTTPS: https://\4:7443)
  Create the administrator account from your browser.
  The StrataOS screen (address, QR code, local actions) is on Alt+F1.

ISSUE
cp /etc/issue /etc/issue.net
rm -rf /root/strataos
