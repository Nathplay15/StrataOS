#!/bin/sh
# "System on two disks (mirror)" (partman/early_command of preseed-mirror.cfg):
# asks for the two system disks, then preseeds partman with a RAID 1 recipe
# that gives both disks the same layout:
#   1  EFI system partition (UEFI) or BIOS boot partition (BIOS)
#   2  swap, RAID 1 member (md "swap")
#   3  system, RAID 1 member (md "root", Btrfs with the @rootfs subvolume)
# The two ESPs are kept identical by /usr/lib/strata/scripts/esp-sync once the
# system runs; in BIOS mode GRUB goes on both disks.
. /usr/share/debconf/confmodule
set -u
log() { logger -t strata-mirror "$*"; }

debconf-loadtemplate strata /cdrom/strata/mirror.templates

# Disks, without the installation medium (a USB stick shows up as a disk).
media=$(sed -n 's|^\(/dev/[^ ]*\) /cdrom .*|\1|p' /proc/mounts | head -1)
media=${media#/dev/}
devs="" choices=""
for d in $(list-devices disk); do
  n=${d#/dev/}
  case "$media" in "$n"|"$n"[0-9]*|"$n"p[0-9]*) continue ;; esac
  size=$(( $(cat "/sys/block/$n/size" 2>/dev/null || echo 0) / 2097152 ))
  [ "$size" -ge 8 ] || continue
  model=$(cat "/sys/block/$n/device/model" 2>/dev/null | sed 's/ *$//; s/,/ /g')
  devs="${devs:+$devs, }$d"
  choices="${choices:+$choices, }$d ($size GB${model:+, $model})"
done
log "disks: $devs"

chosen=""
# Test images only: the disks can be given on the kernel command line.
for w in $(cat /proc/cmdline); do
  case "$w" in strata_mirror_disks=*) chosen=$(echo "${w#*=}" | sed 's|\([a-z0-9]*\)|/dev/\1|g; s|,| |g') ;; esac
done
while [ -z "$chosen" ]; do
  db_subst strata/mirror-disks DEVS "$devs"
  db_subst strata/mirror-disks CHOICES "$choices"
  db_fset strata/mirror-disks seen false
  db_input critical strata/mirror-disks || true
  db_go || exit 10
  db_get strata/mirror-disks
  sel=$(echo "$RET" | sed 's/,/ /g')
  if [ "$(echo $sel | wc -w)" -eq 2 ]; then
    chosen=$sel
  else
    db_fset strata/mirror-wrong seen false
    db_input critical strata/mirror-wrong || true
    db_go || true
  fi
done
set -- $chosen
a=$1 b=$2
log "mirror on $a and $b"
p() { case "$1" in *[0-9]) echo "${1}p$2" ;; *) echo "$1$2" ;; esac; }

if [ -d /sys/firmware/efi ]; then
  boot='538 538 538 free $iflabel{ gpt } $reusemethod{ } method{ efi } format{ } .'
  bootdev=default
else
  boot='1 1 1 free $iflabel{ gpt } method{ biosgrub } .'
  bootdev="$a $b"
fi
# Swap: 2 GB; the system takes the rest (the smaller disk sets the size).
recipe="strata-mirror :: $boot 2048 2048 2048 raid \$lvmignore{ } method{ raid } . 8000 8000 -1 raid \$lvmignore{ } method{ raid } ."

db_set partman-auto/disk "$a $b"
db_set partman-auto/method raid
db_set partman-auto/expert_recipe "$recipe"
db_set partman-auto-raid/recipe "1 2 0 btrfs / $(p "$a" 3)#$(p "$b" 3) . 1 2 0 swap - $(p "$a" 2)#$(p "$b" 2) ."
db_set grub-installer/bootdev "$bootdev"
for q in partman-auto/disk partman-auto/method partman-auto/expert_recipe partman-auto-raid/recipe grub-installer/bootdev; do
  db_fset "$q" seen true
done
exit 0
