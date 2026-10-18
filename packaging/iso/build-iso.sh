#!/bin/bash
# Builds dist/strataos-<version>-<arch>.iso (ARCH=amd64 or arm64): the Debian 13
# netinst image with an automated "Install StrataOS" entry (amd64: BIOS and
# UEFI; arm64: UEFI). xorriso runs in a Debian container, so nothing needs to
# be installed on the build machine but Docker.
# The installed system defaults to English, a US keyboard and UTC; set for
# example ISO_LOCALE=fr_FR.UTF-8 ISO_KEYMAP=fr ISO_TIMEZONE=Europe/Paris to
# change them (the web interface has its own language setting).
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION=$(cat VERSION)
ARCH=${ARCH:-amd64}
LOCALE=${ISO_LOCALE:-en_US.UTF-8}
KEYMAP=${ISO_KEYMAP:-us}
TIMEZONE=${ISO_TIMEZONE:-Etc/UTC}
LANGUAGE=${LOCALE%%_*}
COUNTRY=${LOCALE#*_}; COUNTRY=${COUNTRY%%.*}
SRC=${SRC_ISO:-$(ls vm/images/debian-13.*-"$ARCH"-netinst.iso 2>/dev/null | sort -V | tail -1)}
DEB=dist/strataos_${VERSION}_${ARCH}.deb
OUT=dist/strataos-${VERSION}-${ARCH}.iso
case "$ARCH" in
  amd64) KDIR=install.amd ;;
  arm64) KDIR=install.a64 ;;
  *) echo "unsupported architecture $ARCH"; exit 1 ;;
esac
[ -n "$SRC" ] && [ -f "$SRC" ] || { echo "missing the Debian 13 $ARCH netinst ISO in vm/images (or SRC_ISO), see vm/README.md"; exit 1; }
[ -f "$DEB" ] || { echo "missing $DEB (run make deb)"; exit 1; }

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/strata"
cp packaging/iso/install.sh "$DEB" "$STAGE/strata/"
# Random root password, never stored: only its hash goes in the preseed.
ROOT_HASH=$(head -c 32 /dev/urandom | base64 | openssl passwd -6 -stdin)
sed -e "s|@ROOT_HASH@|$ROOT_HASH|" -e "s|@LANGUAGE@|$LANGUAGE|" -e "s|@COUNTRY@|$COUNTRY|" -e "s|@LOCALE@|$LOCALE|" \
  -e "s|@KEYMAP@|$KEYMAP|" -e "s|@TIMEZONE@|$TIMEZONE|" packaging/iso/preseed.cfg > "$STAGE/strata/preseed.cfg"
# Test images only: PARTITIONING=lvm installs the system on LVM (ext4), with
# free space left in the volume group, like a "guided LVM" Debian install.
if [ "${PARTITIONING:-}" = lvm ]; then
  sed -i -e 's|^d-i partman-auto/method string .*|d-i partman-auto/method string lvm|' \
    -e 's|^d-i partman/default_filesystem string .*|d-i partman/default_filesystem string ext4\nd-i partman-auto-lvm/guided_size string 60%|' "$STAGE/strata/preseed.cfg"
  OUT=dist/strataos-${VERSION}-${ARCH}-lvm-test.iso
fi
# arm64 machines often install on a serial console, where only English can be
# shown: another installer language would be refused and asked, stopping the
# automatic installation.
if [ "$ARCH" = arm64 ]; then
  sed -i -e 's|^d-i debian-installer/language string .*|d-i debian-installer/language string en|' "$STAGE/strata/preseed.cfg"
  LANGUAGE=en
fi
BOOT="auto=true priority=critical preseed/file=/cdrom/strata/preseed.cfg language=$LANGUAGE country=$COUNTRY locale=$LOCALE keymap=$KEYMAP"
echo "$BOOT" > "$STAGE/bootargs"

docker run --rm -v "$PWD:/w" -v "$STAGE:/stage" -w /w -e VERSION="$VERSION" -e SRC="$SRC" -e OUT="$OUT" -e ARCH="$ARCH" -e KDIR="$KDIR" \
  -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" debian:trixie bash -euo pipefail -c '
  apt-get update -qq >/dev/null && apt-get install -y -qq xorriso >/dev/null
  BOOT=$(cat /stage/bootargs)
  x() { xorriso -osirrox on -indev "$SRC" -extract "$1" "/stage/$2" 2>/dev/null; chmod u+w "/stage/$2"; }
  x /boot/grub/grub.cfg grub.cfg
  maps=()
  if [ "$ARCH" = amd64 ]; then
  x /isolinux/menu.cfg menu.cfg
  x /isolinux/gtk.cfg gtk.cfg
  x /isolinux/spkgtk.cfg spkgtk.cfg

  # BIOS (isolinux): our entry first and the only default. Debian arms a 30 s timer
  # that starts the speech-synthesis installer: make it start StrataOS after 10 s.
  sed -i "/^default installgui/d; /menu default/d" /stage/gtk.cfg
  sed -i "s/^timeout 300/timeout 100/; s|^ontimeout .*|ontimeout /install.amd/vmlinuz vga=788 initrd=/install.amd/initrd.gz ${BOOT} --- quiet|; s/^menu autoboot .*/menu autoboot Installing StrataOS in # second{,s}... (press a key to choose)/" /stage/spkgtk.cfg
  sed -i "s/^include gtk.cfg/include strata.cfg\ninclude gtk.cfg/; s/Debian GNU\/Linux installer menu/StrataOS ${VERSION} (Debian 13)/" /stage/menu.cfg
  printf "label strata\n\tmenu label ^Install StrataOS %s\n\tmenu default\n\tkernel /install.amd/vmlinuz\n\tappend vga=788 initrd=/install.amd/initrd.gz %s --- quiet\n" "$VERSION" "$BOOT" > /stage/strata.cfg
  maps=(-map /stage/menu.cfg /isolinux/menu.cfg -map /stage/strata.cfg /isolinux/strata.cfg -map /stage/gtk.cfg /isolinux/gtk.cfg -map /stage/spkgtk.cfg /isolinux/spkgtk.cfg)
  fi

  # UEFI (GRUB): same entry, default, 5 s.
  entry="set default=0\nset timeout=5\nmenuentry \"Install StrataOS ${VERSION}\" {\n    set background_color=black\n    linux    /${KDIR}/vmlinuz vga=788 ${BOOT} --- quiet\n    initrd   /${KDIR}/initrd.gz\n}"
  awk -v e="$entry" "!done && /^menuentry/ {gsub(/\\\\n/, \"\n\", e); print e; done=1} {print}" /stage/grub.cfg > /stage/grub.new
  grep -q "Install StrataOS" /stage/grub.new

  rm -f "$OUT"
  xorriso -indev "$SRC" -outdev "$OUT" \
    -map /stage/strata /strata \
    "${maps[@]}" \
    -map /stage/grub.new /boot/grub/grub.cfg \
    -volid "STRATAOS" \
    -boot_image any replay 2>&1 | grep -E "^xorriso : (UPDATE|NOTE).*(Writing|written)|FAILURE|SORRY" || true
  chown "$HOST_UID:$HOST_GID" "$OUT"
'
ls -lh "$OUT"
