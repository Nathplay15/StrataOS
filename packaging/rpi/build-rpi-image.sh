#!/bin/bash
# Builds dist/strataos-<version>-raspberrypi.img.xz: one ready-to-flash image
# for Raspberry Pi 5, 500, Compute Module 5, Raspberry Pi 4, 400, Compute
# Module 4 and Raspberry Pi 3 B / 3 B+. Debian 13 arm64 with StrataOS
# preinstalled; only the kernels, the boot firmware and the initramfs tools
# come from the Raspberry Pi archive (Debian's kernel has no driver for the
# RP1 chip of the Pi 5: no Ethernet, no USB). Like Raspberry Pi OS, the image
# holds two kernel flavours, v8 for the Pi 3 and 4 and 2712 for the Pi 5, and
# the firmware starts the right one. On first boot the system partition grows
# to the whole card or SSD, SSH host keys are generated, and the web
# interface at http://<address>:7070 asks for the administrator.
#
# Runs as root on Debian or Ubuntu: natively on arm64, or on another
# architecture with qemu-user-static (binfmt) installed.
#   sudo packaging/rpi/build-rpi-image.sh [strataos_<version>_arm64.deb]
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION=$(cat VERSION)
DEB=$(realpath "${1:-dist/strataos_${VERSION}_arm64.deb}")
OUT=dist/strataos-${VERSION}-raspberrypi.img
SUITE=${SUITE:-trixie}
MIRROR=${MIRROR:-http://deb.debian.org/debian}
RPI_MIRROR=${RPI_MIRROR:-https://archive.raspberrypi.com/debian}
SIZE=${SIZE:-3G}
# Raspberry Pi Archive Signing Key, CF8A 1AF5 02A2 AA2D 763B AE7E 82B1 2992 7FA3 303E:
# the keyring of the raspberrypi-archive-keyring 2025.1+rpt1 package, the same
# file as keydir/ in github.com/raspberrypi/rpi-image-gen. Its key bindings
# use SHA-512 (raspberrypi.gpg.key on the archive still has SHA-1 ones, which
# apt rejects since February 2026).
RPI_KEY=packaging/rpi/raspberrypi-archive-keyring.pgp
RPI_KEY_SHA256=3a29901549ae65a910de13a32551e6eea7f45ba1fd70f0cb67e5c70d98488071
# The APT source and pin, config.txt and cmdline.txt of packaging/rpi/ are
# shared with the switch of older installations to these kernels (StrataOS
# ships them in /usr/lib/strata/rpi). The pin's first Package line lists the
# only packages taken from the Raspberry Pi archive (apt and awk regex).
RPI_PACKAGES=$(sed -n 's|^Package: \(/.*/\)$|\1|p' packaging/rpi/raspberrypi.pref | head -1)
[ -n "$RPI_PACKAGES" ] || { echo "no package list in packaging/rpi/raspberrypi.pref"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
[ -f "$DEB" ] || { echo "missing $DEB (make deb ARCH=arm64)"; exit 1; }
echo "$RPI_KEY_SHA256  $RPI_KEY" | sha256sum -c --quiet - || { echo "unexpected $RPI_KEY"; exit 1; }
for b in debootstrap parted mkfs.vfat mkfs.ext4 losetup xz; do
  command -v "$b" >/dev/null || { echo "$b is required (debootstrap parted dosfstools e2fsprogs util-linux xz-utils)"; exit 1; }
done
FOREIGN=""
if [ "$(dpkg --print-architecture)" != arm64 ]; then
  [ -e /proc/sys/fs/binfmt_misc/qemu-aarch64 ] || { echo "qemu-user-static (binfmt) is required to build on $(dpkg --print-architecture)"; exit 1; }
  FOREIGN=--foreign
fi

WORK=$(mktemp -d)
ROOT=$WORK/root
LOOP=""
cleanup() {
  set +e
  for m in dev/pts dev proc sys boot/firmware; do mountpoint -q "$ROOT/$m" && umount -l "$ROOT/$m"; done
  mountpoint -q "$ROOT" && umount -l "$ROOT"
  [ -n "$LOOP" ] && losetup -d "$LOOP"
  rm -rf "$WORK"
}
trap cleanup EXIT

echo ">> Disk image ($SIZE): firmware partition (FAT, 512 MiB) + system (ext4)"
mkdir -p dist
rm -f "$OUT" "$OUT.xz"
truncate -s "$SIZE" "$OUT"
parted -s "$OUT" mklabel msdos mkpart primary fat32 4MiB 516MiB mkpart primary ext4 516MiB 100%
LOOP=$(losetup -f --show -P "$OUT")
mkfs.vfat -F 32 -n RASPIFIRM "${LOOP}p1" >/dev/null
mkfs.ext4 -q -L RASPIROOT "${LOOP}p2"
mkdir -p "$ROOT"
mount "${LOOP}p2" "$ROOT"
mkdir -p "$ROOT/boot/firmware"
mount "${LOOP}p1" "$ROOT/boot/firmware"

echo ">> Debian $SUITE arm64"
KEYRING=""
[ -f /usr/share/keyrings/debian-archive-keyring.gpg ] && KEYRING=--keyring=/usr/share/keyrings/debian-archive-keyring.gpg
debootstrap --arch=arm64 $FOREIGN $KEYRING --variant=minbase --components=main,non-free-firmware \
  --include=ca-certificates,locales,systemd,systemd-sysv,dbus,udev,kmod "$SUITE" "$ROOT" "$MIRROR"
[ -n "$FOREIGN" ] && chroot "$ROOT" /debootstrap/debootstrap --second-stage

for m in proc sys dev dev/pts; do mkdir -p "$ROOT/$m"; done
mount -t proc proc "$ROOT/proc"; mount -t sysfs sys "$ROOT/sys"
mount --bind /dev "$ROOT/dev"; mount --bind /dev/pts "$ROOT/dev/pts"

# No documentation, manual pages or translations of the command line tools
# (the web interface has its own), like Debian's minimal container images.
# Copyright files stay.
cat > "$ROOT/etc/dpkg/dpkg.cfg.d/strataos-minimal" <<'EOF'
# StrataOS Raspberry Pi image: no documentation, manual pages or translations.
path-exclude=/usr/share/doc/*
path-include=/usr/share/doc/*/copyright
path-exclude=/usr/share/man/*
path-exclude=/usr/share/info/*
path-exclude=/usr/share/locale/*
path-include=/usr/share/locale/locale.alias
path-exclude=/usr/share/lintian/*
EOF
find "$ROOT/usr/share/doc" -mindepth 1 ! -type d ! -name copyright -delete
find "$ROOT/usr/share/man" "$ROOT/usr/share/info" "$ROOT/usr/share/lintian" -mindepth 1 -delete 2>/dev/null || true
find "$ROOT/usr/share/locale" -mindepth 1 ! -name locale.alias -delete 2>/dev/null || true

cat > "$ROOT/etc/apt/sources.list" <<EOF
deb $MIRROR $SUITE main contrib non-free-firmware
deb $MIRROR $SUITE-updates main contrib non-free-firmware
deb http://security.debian.org/debian-security $SUITE-security main contrib non-free-firmware
EOF
# The Raspberry Pi archive: its key is trusted for this source only, and the
# pin keeps everything but the kernels, boot firmware, initramfs tools and
# Wi-Fi firmware on Debian. Kernel updates arrive with the system updates.
install -d "$ROOT/etc/apt/keyrings"
install -m 644 "$RPI_KEY" "$ROOT/etc/apt/keyrings/raspberrypi-archive-keyring.pgp"
sed -e "s|^URIs: .*|URIs: $RPI_MIRROR|" -e "s|^Suites: .*|Suites: $SUITE|" packaging/rpi/raspberrypi.sources \
  > "$ROOT/etc/apt/sources.list.d/raspberrypi.sources"
install -m 644 packaging/rpi/raspberrypi.pref "$ROOT/etc/apt/preferences.d/raspberrypi"
cat > "$ROOT/etc/fstab" <<'EOF'
LABEL=RASPIROOT /             ext4 rw,noatime,errors=remount-ro 0 1
LABEL=RASPIFIRM /boot/firmware vfat rw,noatime                 0 2
EOF
echo strataos > "$ROOT/etc/hostname"
printf '127.0.0.1\tlocalhost\n127.0.1.1\tstrataos\n::1\tlocalhost ip6-localhost ip6-loopback\n' > "$ROOT/etc/hosts"

echo ">> Raspberry Pi kernels and firmware, network, SSH"
in_root() { chroot "$ROOT" env DEBIAN_FRONTEND=noninteractive LC_ALL=C.UTF-8 "$@"; }
APT=(apt-get install -y -qq -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold)
in_root apt-get update -qq
# The drivers of the system disk (SD card, USB, NVMe) and ext4 are built into
# the Raspberry Pi kernels: a small initramfs (root by label, fsck) is enough.
mkdir -p "$ROOT/etc/initramfs-tools/conf.d"
echo "MODULES=list" > "$ROOT/etc/initramfs-tools/conf.d/strataos-rpi.conf"
in_root "${APT[@]}" --no-install-recommends raspi-firmware initramfs-tools
in_root "${APT[@]}" --no-install-recommends \
  linux-image-rpi-v8 linux-image-rpi-2712 firmware-brcm80211 wireless-regdb \
  systemd-resolved systemd-timesyncd openssh-server sudo curl cloud-guest-utils \
  e2fsprogs dosfstools fdisk iproute2 iputils-ping less nano console-setup keyboard-configuration

# Boot configuration, read by the firmware of every model: the Pi 5 starts
# kernel_2712.img (its own flavour, 16K pages), the others kernel8.img, each
# with the initramfs of the same name (auto_initramfs).
install -m 644 packaging/rpi/config.txt packaging/rpi/cmdline.txt "$ROOT/boot/firmware/"
# Kernels, device trees and initramfs on the firmware partition (the hooks of
# raspi-firmware keep them up to date on kernel updates).
for k in "$ROOT"/boot/vmlinuz-*; do
  in_root env DEB_MAINT_PARAMS=configure /etc/kernel/postinst.d/z50-raspi-firmware "${k##*/vmlinuz-}" "/boot/${k##*/}"
done
in_root update-initramfs -u -k all
for f in config.txt cmdline.txt bootcode.bin start.elf start4.elf kernel8.img initramfs8 kernel_2712.img initramfs_2712 \
  bcm2710-rpi-3-b.dtb bcm2710-rpi-3-b-plus.dtb bcm2711-rpi-4-b.dtb bcm2712-rpi-5-b.dtb bcm2712d0-rpi-5-b.dtb overlays/vc4-kms-v3d.dtbo; do
  [ -s "$ROOT/boot/firmware/$f" ] || { echo "the firmware partition has no $f"; exit 1; }
done

echo "en_US.UTF-8 UTF-8" >> "$ROOT/etc/locale.gen"
in_root locale-gen >/dev/null
echo 'LANG=en_US.UTF-8' > "$ROOT/etc/default/locale"
ln -sf /usr/share/zoneinfo/Etc/UTC "$ROOT/etc/localtime"
# DHCP on the wired interface until StrataOS manages the network (usb*:
# USB Ethernet adapters driven by cdc_ether).
mkdir -p "$ROOT/etc/systemd/network"
cat > "$ROOT/etc/systemd/network/99-default-dhcp.network" <<'EOF'
[Match]
Name=en* eth* usb*

[Network]
DHCP=yes
EOF
in_root systemctl enable systemd-networkd systemd-resolved systemd-timesyncd ssh >/dev/null 2>&1

echo ">> StrataOS"
# With its recommended packages but Docker (a quarter of the image), rclone
# and the UPS tools: the interface installs them on demand (App Center →
# Install Docker, Settings → Components).
cp "$DEB" "$ROOT/tmp/strataos.deb"
in_root "${APT[@]}" /tmp/strataos.deb docker.io- docker-compose- rclone- nut-client- nut-server- >/dev/null
rm -f "$ROOT/tmp/strataos.deb"
# Nothing else may come from the Raspberry Pi archive: installed versions
# that it publishes must be on the list.
other=$(in_root dpkg-query -W -f '${Package} ${Version}\n' | awk '
  FILENAME != "-" && /^Package: / { p = $2 } FILENAME != "-" && /^Version: / { rpi[p " " $2] = 1 }
  FILENAME == "-" && ($0 in rpi) && $1 !~ '"$RPI_PACKAGES"' { print $1 }' "$ROOT"/var/lib/apt/lists/*raspberrypi.com*_Packages -)
[ -z "$other" ] || { echo "packages from the Raspberry Pi archive: $other"; exit 1; }

echo ">> First boot: grow the system partition, new SSH keys and machine id"
cat > "$ROOT/usr/local/sbin/strata-firstboot" <<'EOF'
#!/bin/sh
set -e
root=$(findmnt -n -o SOURCE /)
disk=/dev/$(lsblk -no PKNAME "$root")
part=$(cat /sys/class/block/$(basename "$root")/partition)
growpart "$disk" "$part" || true
resize2fs "$root"
rm -f /etc/ssh/ssh_host_*
ssh-keygen -A
systemctl disable strata-firstboot.service
EOF
chmod 755 "$ROOT/usr/local/sbin/strata-firstboot"
cat > "$ROOT/etc/systemd/system/strata-firstboot.service" <<'EOF'
[Unit]
Description=StrataOS first boot (grow the system partition, SSH keys)
Before=ssh.service strata.service
ConditionPathExists=/usr/local/sbin/strata-firstboot

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/strata-firstboot
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF
in_root systemctl enable strata-firstboot.service >/dev/null 2>&1
cat > "$ROOT/etc/issue" <<'EOF'

  StrataOS — \n (Raspberry Pi)

  Web interface: http://\4:7070   (HTTPS: https://\4:7443)
  Create the administrator account from your browser.

EOF
cp "$ROOT/etc/issue" "$ROOT/etc/issue.net"
# Nobody knows a root password: locked; the administrator is created in the web interface.
in_root passwd -l root >/dev/null
rm -f "$ROOT/etc/ssh/ssh_host_"*
: > "$ROOT/etc/machine-id"
rm -f "$ROOT/var/lib/dbus/machine-id"
in_root apt-get clean
rm -rf "$ROOT/var/lib/apt/lists/"* "$ROOT/var/log/"*.log "$ROOT/var/cache/debconf/"*-old "$ROOT/var/lib/dpkg/"*-old
echo ">> Used: $(df -h --output=used "$ROOT" | tail -1) system, $(df -h --output=used "$ROOT/boot/firmware" | tail -1) firmware partition"

echo ">> Compressing"
# The blocks freed during the build (downloaded packages, package lists:
# about 300 MB) still hold data. Filled with zeros, they compress to nothing
# (fstrim does not reach the image file through every loop device).
for m in "$ROOT/boot/firmware" "$ROOT"; do
  dd if=/dev/zero of="$m/zero" bs=4M status=none 2>/dev/null || true
  sync -f "$m/zero"
  rm -f "$m/zero"
done
sync
cleanup
trap - EXIT
# Raspberry Pi Imager and balenaEtcher read .img.xz (multithreaded blocks too).
xz -T0 -9 "$OUT"
ls -lh "$OUT.xz"
