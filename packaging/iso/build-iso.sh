#!/bin/bash
# Builds dist/strataos-<version>-amd64.iso: the Debian 13 netinst image with an
# automated "Install StrataOS" entry (BIOS and UEFI). xorriso runs in a Debian
# container, so nothing needs to be installed on the build machine but Docker.
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION=$(cat VERSION)
SRC=${SRC_ISO:-vm/images/debian-13.7.0-amd64-netinst.iso}
DEB=dist/strataos_${VERSION}_amd64.deb
OUT=dist/strataos-${VERSION}-amd64.iso
[ -f "$SRC" ] || { echo "missing $SRC (Debian netinst ISO, see vm/README.md)"; exit 1; }
[ -f "$DEB" ] || { echo "missing $DEB (run make deb)"; exit 1; }

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/strata"
cp packaging/iso/install.sh "$DEB" "$STAGE/strata/"
# Random root password, never stored: only its hash goes in the preseed.
ROOT_HASH=$(head -c 32 /dev/urandom | base64 | openssl passwd -6 -stdin)
sed "s|@ROOT_HASH@|$ROOT_HASH|" packaging/iso/preseed.cfg > "$STAGE/strata/preseed.cfg"
BOOT="auto=true priority=critical preseed/file=/cdrom/strata/preseed.cfg language=fr country=FR locale=fr_FR.UTF-8 keymap=fr"
echo "$BOOT" > "$STAGE/bootargs"

docker run --rm -v "$PWD:/w" -v "$STAGE:/stage" -w /w -e VERSION="$VERSION" -e SRC="$SRC" -e OUT="$OUT" \
  -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" debian:trixie bash -euo pipefail -c '
  apt-get update -qq >/dev/null && apt-get install -y -qq xorriso >/dev/null
  BOOT=$(cat /stage/bootargs)
  x() { xorriso -osirrox on -indev "$SRC" -extract "$1" "/stage/$2" 2>/dev/null; chmod u+w "/stage/$2"; }
  x /isolinux/menu.cfg menu.cfg
  x /boot/grub/grub.cfg grub.cfg
  x /isolinux/gtk.cfg gtk.cfg
  x /isolinux/spkgtk.cfg spkgtk.cfg

  # BIOS (isolinux): our entry first and the only default. Debian arms a 30 s timer
  # that starts the speech-synthesis installer: make it start StrataOS after 10 s.
  sed -i "/^default installgui/d; /menu default/d" /stage/gtk.cfg
  sed -i "s/^timeout 300/timeout 100/; s|^ontimeout .*|ontimeout /install.amd/vmlinuz vga=788 initrd=/install.amd/initrd.gz ${BOOT} --- quiet|; s/^menu autoboot .*/menu autoboot Installation de StrataOS dans # seconde{,s}... (une touche pour choisir)/" /stage/spkgtk.cfg
  sed -i "s/^include gtk.cfg/include strata.cfg\ninclude gtk.cfg/; s/Debian GNU\/Linux installer menu/StrataOS ${VERSION} (Debian 13)/" /stage/menu.cfg
  printf "label strata\n\tmenu label ^Installer StrataOS %s\n\tmenu default\n\tkernel /install.amd/vmlinuz\n\tappend vga=788 initrd=/install.amd/initrd.gz %s --- quiet\n" "$VERSION" "$BOOT" > /stage/strata.cfg

  # UEFI (GRUB): same entry, default, 5 s.
  entry="set default=0\nset timeout=5\nmenuentry \"Installer StrataOS ${VERSION}\" {\n    set background_color=black\n    linux    /install.amd/vmlinuz vga=788 ${BOOT} --- quiet\n    initrd   /install.amd/initrd.gz\n}"
  awk -v e="$entry" "!done && /^menuentry/ {gsub(/\\\\n/, \"\n\", e); print e; done=1} {print}" /stage/grub.cfg > /stage/grub.new
  grep -q "Installer StrataOS" /stage/grub.new

  rm -f "$OUT"
  xorriso -indev "$SRC" -outdev "$OUT" \
    -map /stage/strata /strata \
    -map /stage/menu.cfg /isolinux/menu.cfg \
    -map /stage/strata.cfg /isolinux/strata.cfg \
    -map /stage/gtk.cfg /isolinux/gtk.cfg \
    -map /stage/spkgtk.cfg /isolinux/spkgtk.cfg \
    -map /stage/grub.new /boot/grub/grub.cfg \
    -volid "STRATAOS" \
    -boot_image any replay 2>&1 | grep -E "^xorriso : (UPDATE|NOTE).*(Writing|written)|FAILURE|SORRY" || true
  chown "$HOST_UID:$HOST_GID" "$OUT"
'
ls -lh "$OUT"
