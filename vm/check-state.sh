#!/bin/bash
# Prints the storage/service state of the test VM, used to compare before/after a reboot.
cd "$(dirname "$0")/.."
./vm/vm.sh ssh 'echo "## md"; cat /proc/mdstat | grep -E "^md|blocks"; echo "## mounts"; findmnt -rn -o TARGET,SOURCE,FSTYPE | grep /srv/strata | sort; echo "## zfs"; sudo zpool list -H -o name,health 2>/dev/null; sudo zfs list -H -o name,mounted 2>/dev/null; echo "## services"; for s in strata smbd nmbd nfs-server docker wsdd2 zfs-zed; do echo "$s $(systemctl is-active $s)"; done; echo "## shares"; sudo testparm -s 2>/dev/null | grep "^\[" ; sudo exportfs | tr -s "\t " " "; echo "## containers"; sudo docker ps --format "{{.Names}} {{.Status}}" | sed "s/Up .*/Up/"' 2>&1
