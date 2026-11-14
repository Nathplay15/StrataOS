#!/bin/sh
# "Restore a StrataOS system backup" (late_command of preseed-restore.cfg, in
# the installer): once the regular installation is done, the newest system
# backup found on the other disks (a folder strataos-system-backup/ made by
# Storage → Disks → System disk → Back up the system) replaces the freshly
# installed system. The new disk keeps its own partitions and file system
# (fstab, boot loader); everything else comes from the backup.
# A failure leaves the fresh installation in place (see
# /var/log/strataos-restore.log on the installed system).
cat > /target/root/strata-restore.sh <<'SCRIPT'
#!/bin/bash
set -uo pipefail
# in-target sends the output of commands to the installer's syslog: log here.
exec >> /var/log/strataos-restore.log 2>&1
log() { echo "strata-restore: $*"; }
fail() { log "FAILED: $*"; printf '\n  StrataOS: the system backup could NOT be restored (%s).\n  This is a fresh installation; see /var/log/strataos-restore.log.\n\n' "$*" >> /etc/issue; exit 1; }
M=/mnt/strata-restore
mkdir -p "$M"
# Data disks, read-only: md arrays and LVM volume groups.
mdadm --assemble --scan --readonly >/dev/null 2>&1 || true
vgchange -ay --config 'global {metadata_read_only=1}' >/dev/null 2>&1 || true
udevadm settle 2>/dev/null || true
sysdisk=$(lsblk -nrpo PKNAME "$(findmnt -n -o SOURCE / | sed 's/\[.*//')" | head -1)
best="" bestdate="" n=0
mounted=""
while read -r dev fs; do
  case "$fs" in ext2|ext3|ext4|xfs|btrfs|vfat|exfat) ;; *) continue ;; esac
  # Never the disk just installed.
  for p in $(lsblk -nrpso PATH "$dev" 2>/dev/null); do [ "$p" = "$sysdisk" ] && continue 2; done
  findmnt -n "$dev" >/dev/null 2>&1 && continue
  n=$((n + 1)); mkdir -p "$M/$n"
  mount -o ro "$dev" "$M/$n" 2>/dev/null || { rmdir "$M/$n"; continue; }
  mounted="$mounted $M/$n"
  for man in $(find "$M/$n" -maxdepth 5 -path '*/strataos-system-backup/*/manifest.json' 2>/dev/null); do
    created=$(sed -n 's/.*"created": *"\([^"]*\)".*/\1/p' "$man")
    log "found $man ($created) on $dev"
    if [ -z "$best" ] || [[ "$created" > "$bestdate" ]]; then best=$man bestdate=$created; fi
  done
done < <(lsblk -nrpo PATH,FSTYPE)
cleanup() { for d in $mounted; do umount "$d" 2>/dev/null; done; }
trap cleanup EXIT
[ -n "$best" ] || fail "no system backup found on the connected disks"
dir=$(dirname "$best")
log "restoring $dir"
arch=$(sed -n 's/.*"arch": *"\([^"]*\)".*/\1/p' "$best")
[ "$arch" = "$(dpkg --print-architecture)" ] || fail "the backup is for $arch"
archive="$dir/$(sed -n 's/.*"archive": *"\([^"]*\)".*/\1/p' "$best")"
sum=$(sed -n 's/.*"sha256": *"\([^"]*\)".*/\1/p' "$best")
[ -f "$archive" ] || fail "archive missing"
log "checking $archive"
[ "$(sha256sum "$archive" | cut -d' ' -f1)" = "$sum" ] || fail "the archive is damaged (checksum)"
S=/.strata-restore
rm -rf "$S"; mkdir -p "$S"
case "$archive" in *.zst) comp=--zstd ;; *) comp=--gzip ;; esac
log "extracting"
tar -x $comp -f "$archive" --numeric-owner --acls --xattrs --xattrs-include='*' -C "$S" || fail "extraction failed"
[ -f "$S/etc/fstab" ] && [ -d "$S/usr/bin" ] || fail "the archive holds no system"
# fstab: the new disk's own lines (/, /boot, /boot/efi, settings subvolume,
# swap), the backup's other lines (data volumes).
cp /etc/fstab /root/fstab.fresh
{
  awk '!/^[[:space:]]*#/ && NF >= 3 && ($2 == "/" || $2 == "/boot" || $2 == "/boot/efi" || $2 == "/var/lib/strata" || $3 == "swap")' /root/fstab.fresh
  awk '/^[[:space:]]*#/ || NF < 3 {print; next} !($2 == "/" || $2 == "/boot" || $2 == "/boot/efi" || $2 == "/var/lib/strata" || $3 == "swap")' "$S/etc/fstab"
} > /root/fstab.restored
# The settings subvolume of the new system (@strata, made by the package's
# postinst) must receive the backup's settings, not the folder under it.
if awk '!/^[[:space:]]*#/ && $2 == "/var/lib/strata"' /etc/fstab | grep -q . && ! mountpoint -q /var/lib/strata; then
  mount /var/lib/strata || fail "the settings subvolume could not be mounted"
fi
log "copying the backup onto the new system"
rsync -aHAX --numeric-ids --delete --exclude=/.strata-restore --exclude=/proc --exclude=/sys --exclude=/dev --exclude=/run \
  --exclude=/tmp --exclude=/boot/efi --exclude=/mnt/strata-restore --exclude=/etc/fstab --exclude=/root/fstab.fresh --exclude=/root/fstab.restored \
  --exclude=/root/strata-restore.sh --exclude=/var/log/strataos-restore.log "$S/" / || fail "copy failed"
cp /root/fstab.restored /etc/fstab
rm -f /etc/default/grub.d/strataos-sysdisk-test.cfg
# Arrays: those of this machine now (data arrays), not the old system's.
if [ -f /etc/mdadm/mdadm.conf ]; then
  grep -v -E '^(ARRAY|HOMEHOST)|^# Arrays managed by StrataOS' /etc/mdadm/mdadm.conf > /root/mdadm.conf.new
  { echo "HOMEHOST <ignore>"; cat /root/mdadm.conf.new; echo; echo "# Arrays managed by StrataOS"; mdadm --detail --scan | sed 's/ spares=[0-9]*//'; } > /etc/mdadm/mdadm.conf
  rm -f /root/mdadm.conf.new
fi
log "boot loader and initramfs"
update-initramfs -u -k all || fail "initramfs"
if [ -d /sys/firmware/efi ]; then
  t=x86_64-efi; [ "$(dpkg --print-architecture)" = arm64 ] && t=arm64-efi
  grub-install --target=$t --efi-directory=/boot/efi --bootloader-id=debian --force-extra-removable || fail "grub-install"
else
  grub-install --target=i386-pc "$sysdisk" || fail "grub-install"
fi
update-grub || fail "update-grub"
rm -rf "$S"
sync
printf '\n  StrataOS: system backup of %s restored.\n\n' "$bestdate" >> /etc/issue
log "done"
SCRIPT
in-target bash /root/strata-restore.sh || true
rm -f /target/root/strata-restore.sh
exit 0
