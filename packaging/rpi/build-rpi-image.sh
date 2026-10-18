#!/bin/bash
# Builds dist/strataos-<version>-raspberrypi.img.xz: a ready-to-flash image
# for Raspberry Pi 4, 400 and Compute Module 4 (Debian 13 arm64 with the
# Raspberry Pi firmware and StrataOS preinstalled). On first boot the system
# partition grows to the whole card or SSD, SSH host keys are generated, and
# the web interface at http://<address>:7070 asks for the administrator.
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
SIZE=${SIZE:-4G}
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
[ -f "$DEB" ] || { echo "missing $DEB (make deb ARCH=arm64)"; exit 1; }
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

cat > "$ROOT/etc/apt/sources.list" <<EOF
deb $MIRROR $SUITE main contrib non-free-firmware
deb $MIRROR $SUITE-updates main contrib non-free-firmware
deb http://security.debian.org/debian-security $SUITE-security main contrib non-free-firmware
EOF
cat > "$ROOT/etc/fstab" <<'EOF'
LABEL=RASPIROOT /             ext4 rw,noatime,errors=remount-ro 0 1
LABEL=RASPIFIRM /boot/firmware vfat rw,noatime                 0 2
EOF
echo strataos > "$ROOT/etc/hostname"
printf '127.0.0.1\tlocalhost\n127.0.1.1\tstrataos\n::1\tlocalhost ip6-localhost ip6-loopback\n' > "$ROOT/etc/hosts"
# Kernel command line written by raspi-firmware on every kernel update.
mkdir -p "$ROOT/etc/default"
cat > "$ROOT/etc/default/raspi-firmware" <<'EOF'
ROOTPART=LABEL=RASPIROOT
CONSOLES="ttyS1,115200 tty0"
KERNEL_ARCH="arm64"
EOF

echo ">> Kernel, Raspberry Pi firmware, network, SSH"
in_root() { chroot "$ROOT" env DEBIAN_FRONTEND=noninteractive LC_ALL=C.UTF-8 "$@"; }
in_root apt-get update -qq
in_root apt-get install -y -qq --no-install-recommends \
  linux-image-arm64 raspi-firmware firmware-brcm80211 wireless-regdb \
  systemd-resolved systemd-timesyncd openssh-server sudo curl cloud-guest-utils \
  e2fsprogs dosfstools fdisk iproute2 iputils-ping less nano console-setup keyboard-configuration >/dev/null
# Copy the kernel, initramfs and device trees to the firmware partition (the
# hook may have run before raspi-firmware was configured).
for k in "$ROOT"/boot/vmlinuz-*; do in_root /etc/kernel/postinst.d/z50-raspi-firmware "${k##*/vmlinuz-}"; done
ls "$ROOT/boot/firmware" | grep -q '^config.txt$' || { echo "the firmware partition is incomplete"; exit 1; }
echo "fr_FR.UTF-8 UTF-8" >> "$ROOT/etc/locale.gen"; echo "en_US.UTF-8 UTF-8" >> "$ROOT/etc/locale.gen"
in_root locale-gen >/dev/null
echo 'LANG=fr_FR.UTF-8' > "$ROOT/etc/default/locale"
ln -sf /usr/share/zoneinfo/Europe/Paris "$ROOT/etc/localtime"
# DHCP on the wired interface until StrataOS manages the network.
mkdir -p "$ROOT/etc/systemd/network"
cat > "$ROOT/etc/systemd/network/99-default-dhcp.network" <<'EOF'
[Match]
Name=en* eth*

[Network]
DHCP=yes
EOF
in_root systemctl enable systemd-networkd systemd-resolved systemd-timesyncd ssh >/dev/null 2>&1

echo ">> StrataOS"
cp "$DEB" "$ROOT/tmp/strataos.deb"
in_root apt-get install -y -qq /tmp/strataos.deb >/dev/null
rm -f "$ROOT/tmp/strataos.deb"

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

  Interface web : http://\4:7070   (HTTPS : https://\4:7443)
  Créez le compte administrateur depuis votre navigateur.

EOF
cp "$ROOT/etc/issue" "$ROOT/etc/issue.net"
# Nobody knows a root password: locked; the administrator is created in the web interface.
in_root passwd -l root >/dev/null
rm -f "$ROOT/etc/ssh/ssh_host_"*
: > "$ROOT/etc/machine-id"
rm -f "$ROOT/var/lib/dbus/machine-id"
in_root apt-get clean
rm -rf "$ROOT/var/lib/apt/lists/"* "$ROOT/var/log/"*.log

echo ">> Compressing"
sync
cleanup
trap - EXIT
xz -T0 -6 "$OUT"
ls -lh "$OUT.xz"
