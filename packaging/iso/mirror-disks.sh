#!/bin/sh
# "System on two disks (mirror)" (partman/early_command of preseed-mirror.cfg),
# and with the argument "datadisks" "System on the data disks"
# (preseed-datadisks.cfg): two disks or more, each with a 20 GB system part
# of the mirror, the rest of each disk becoming a data partition after the
# installation (install.sh, GPT name "StrataOS data").
#
# Mirror:
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
mode=${1:-mirror}
q=strata/mirror-disks wrong=strata/mirror-wrong arg=strata_mirror_disks
[ "$mode" = datadisks ] && q=strata/datadisks-disks wrong=strata/datadisks-wrong arg=strata_datadisks_disks

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
  case "$w" in "$arg"=*) chosen=$(echo "${w#*=}" | sed 's|\([a-z0-9]*\)|/dev/\1|g; s|,| |g') ;; esac
done
while [ -z "$chosen" ]; do
  db_subst "$q" DEVS "$devs"
  db_subst "$q" CHOICES "$choices"
  db_fset "$q" seen false
  db_input critical "$q" || true
  db_go || exit 10
  db_get "$q"
  sel=$(echo "$RET" | sed 's/,/ /g')
  n=$(echo $sel | wc -w)
  if { [ "$mode" = mirror ] && [ "$n" -eq 2 ]; } || { [ "$mode" = datadisks ] && [ "$n" -ge 2 ] && [ "$n" -le 8 ]; }; then
    chosen=$sel
  else
    db_fset "$wrong" seen false
    db_input critical "$wrong" || true
    db_go || true
  fi
done
set -- $chosen
a=$1 b=$2
n=$#
log "$mode on $chosen"
p() { case "$1" in *[0-9]) echo "${1}p$2" ;; *) echo "$1$2" ;; esac; }

if [ -d /sys/firmware/efi ]; then
  boot='538 538 538 free $iflabel{ gpt } $reusemethod{ } method{ efi } format{ } .'
  bootdev=default
else
  boot='1 1 1 free $iflabel{ gpt } method{ biosgrub } .'
  bootdev="$chosen"
fi
# Swap: 2 GB; the system takes the rest (the smaller disk sets the size),
# or 20 GB on the data disks (the rest stays free for the data partition).
sys="8000 8000 -1"
[ "$mode" = datadisks ] && sys="20480 20480 20480"
recipe="strata-mirror :: $boot 2048 2048 2048 raid \$lvmignore{ } method{ raid } . $sys raid \$lvmignore{ } method{ raid } ."
roots="" swaps=""
for d in $chosen; do
  roots="${roots:+$roots#}$(p "$d" 3)"
  swaps="${swaps:+$swaps#}$(p "$d" 2)"
done

db_set partman-auto/disk "$chosen"
db_set partman-auto/method raid
db_set partman-auto/expert_recipe "$recipe"
db_set partman-auto-raid/recipe "1 $n 0 btrfs / $roots . 1 $n 0 swap - $swaps ."
db_set grub-installer/bootdev "$bootdev"
for q in partman-auto/disk partman-auto/method partman-auto/expert_recipe partman-auto-raid/recipe grub-installer/bootdev; do
  db_fset "$q" seen true
done
exit 0
