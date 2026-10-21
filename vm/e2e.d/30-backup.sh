# shellcheck shell=bash
# Backups 0.6: pulls from another NAS (rsync+SSH, rsync daemon, SMB) with
# snapshots of the destination, ZFS pull replication, two-way sync over SFTP
# and Time Machine destinations. The VM itself plays the "other NAS".
section "Backup: other NAS, ZFS pull, two-way sync, Time Machine"

KEY=$(c GET /api/backups/ssh-key | py 'print(d["publicKey"])')
check "StrataOS backup key" "$KEY" "ssh-ed25519 "
# The VM plays the remote NAS at 127.0.0.1: forget the host key a previous
# run may have recorded (a re-created test VM has new host keys).
vm "sudo ssh-keygen -R 127.0.0.1 -f /var/lib/strata/ssh/known_hosts >/dev/null 2>&1; sudo rm -f /var/lib/strata/ssh/known_hosts.old"
# A local account trusting the StrataOS key stands for the remote NAS.
vm "sudo useradd -m -s /bin/bash e2ebk 2>/dev/null; sudo install -d -m 700 -o e2ebk -g e2ebk /home/e2ebk/.ssh; echo '$KEY' | sudo tee /home/e2ebk/.ssh/authorized_keys >/dev/null; sudo chown e2ebk: /home/e2ebk/.ssh/authorized_keys; sudo chmod 600 /home/e2ebk/.ssh/authorized_keys"
vm "sudo -u e2ebk mkdir -p /home/e2ebk/data/docs && echo 'from ssh' | sudo -u e2ebk tee /home/e2ebk/data/docs/a.txt >/dev/null"

# rsync over SSH, versions kept on the Btrfs volume "fast".
PSJ='"name":"pull-ssh","kind":"rsync","sourceType":"ssh","host":"127.0.0.1","port":22,"user":"e2ebk","sources":["/home/e2ebk/data/docs"],"destType":"local","destPath":"/srv/strata/fast/pulls/ssh","versions":true,"keepLast":2,"cron":"","enabled":true'
check "pull over SSH: connection test" "$(c POST /api/backups/test -d "{$PSJ}")" "Connection successful"
check "pull over SSH: password refused (key only)" "$(c POST /api/backups -d "{$PSJ,\"remotePassword\":\"x\"}" | py 'print(repr(d.get("remotePassword","")))')" "''"
PS=$(c GET /api/backups | py 'print([b["id"] for b in d if b["name"]=="pull-ssh"][0])')
check "pull over SSH" "$(job "$(c POST /api/backups/$PS/run)")" "success"
check "pulled file" "$(vm "sudo cat /srv/strata/fast/pulls/ssh/docs/a.txt")" "from ssh"
check "new destination is a Btrfs sub-volume" "$(vm "sudo stat -c %i /srv/strata/fast/pulls/ssh")" "256"
check "snapshot of the destination on fast" "$(vm "sudo ls /srv/strata/fast/.snapshots")" "pulls_ssh@pull-$PS-"
c PUT /api/backups/$PS -d "{$PSJ,\"delete\":true}" >/dev/null
vm "sudo rm /home/e2ebk/data/docs/a.txt; echo b | sudo -u e2ebk tee /home/e2ebk/data/docs/b.txt >/dev/null"
sleep 1.1
check "mirror pull" "$(job "$(c POST /api/backups/$PS/run)")" "success"
check "deleted at the source, gone from the copy" "$(vm "sudo ls /srv/strata/fast/pulls/ssh/docs | tr '\n' ' '")" "b.txt "
check "... but kept in the previous snapshot" "$(vm "sudo cat /srv/strata/fast/.snapshots/pulls_ssh@pull-$PS-*/docs/a.txt 2>/dev/null | head -1")" "from ssh"
sleep 1.1
job "$(c POST /api/backups/$PS/run)" >/dev/null
check "snapshot retention keeps 2" "$(vm "sudo ls /srv/strata/fast/.snapshots | grep -c 'pulls_ssh@pull-$PS-'")" "2"
check "history message" "$(c GET /api/backups | py "print([b['history'][0]['message'] for b in d if b['id']=='$PS'][0])")" "snapshot taken"

# rsync daemon on a high port, module with a secret.
RPASS=$(head -c 12 /dev/urandom | base64 | tr -d '/+=')
vm "sudo mkdir -p /tmp/e2e-rsyncd/mod/sub && echo 'from daemon' | sudo tee /tmp/e2e-rsyncd/mod/sub/d.txt >/dev/null && printf 'port = 18873\nuse chroot = no\npid file = /tmp/e2e-rsyncd/pid\n[e2emod]\n  path = /tmp/e2e-rsyncd/mod\n  read only = yes\n  uid = root\n  gid = root\n  auth users = e2euser\n  secrets file = /tmp/e2e-rsyncd/secrets\n' | sudo tee /tmp/e2e-rsyncd/rsyncd.conf >/dev/null && echo 'e2euser:$RPASS' | sudo tee /tmp/e2e-rsyncd/secrets >/dev/null && sudo chmod 600 /tmp/e2e-rsyncd/secrets && sudo systemd-run --unit=e2e-rsyncd rsync --daemon --no-detach --config=/tmp/e2e-rsyncd/rsyncd.conf >/dev/null 2>&1; sleep 1"
RDJ='"name":"pull-daemon","kind":"rsync","sourceType":"rsyncd","host":"127.0.0.1","port":18873,"user":"e2euser","sources":["e2emod/sub"],"destType":"local","destPath":"/srv/strata/fast/pulls/daemon","cron":"","enabled":true'
check "rsync daemon: connection test" "$(c POST /api/backups/test -d "{$RDJ,\"remotePassword\":\"$RPASS\"}")" "Connection successful"
check "rsync daemon: wrong password refused" "$(c POST /api/backups/test -d "{$RDJ,\"remotePassword\":\"wrong\"}")" "error"
RD=$(c POST /api/backups -d "{$RDJ,\"remotePassword\":\"$RPASS\"}" | py 'print(d["id"])')
check "rsync daemon password masked" "$(c GET /api/backups | py "print([b['remotePassword'] for b in d if b['id']=='$RD'][0])")" "********"
check "pull from rsync daemon" "$(job "$(c POST /api/backups/$RD/run)")" "success"
check "file from the module" "$(vm "sudo cat /srv/strata/fast/pulls/daemon/sub/d.txt")" "from daemon"
check "password file removed after the run" "$(vm "sudo ls -A /run/strata/backup 2>/dev/null | wc -l")" "0"
vm "sudo systemctl stop e2e-rsyncd 2>/dev/null; sudo rm -rf /tmp/e2e-rsyncd"

# SMB: the share Photos with alice's account, mounted read-only.
vm "command -v mount.cifs >/dev/null || sudo apt-get install -y -qq cifs-utils >/dev/null 2>&1"
SMJ='"name":"pull-smb","kind":"rsync","sourceType":"smb","host":"127.0.0.1","user":"'$USER_NAME'","sources":["Photos/2026"],"destType":"local","destPath":"/srv/strata/fast/pulls/smb","versions":true,"keepLast":3,"cron":"","enabled":true'
check "SMB: connection test" "$(c POST /api/backups/test -d "{$SMJ,\"remotePassword\":\"$USER_PASS\"}")" "Connection successful"
check "SMB: wrong password refused" "$(c POST /api/backups/test -d "{$SMJ,\"remotePassword\":\"wrong\"}")" "error"
SM=$(c POST /api/backups -d "{$SMJ,\"remotePassword\":\"$USER_PASS\"}" | py 'print(d["id"])')
check "pull from an SMB share" "$(job "$(c POST /api/backups/$SM/run)")" "success"
check "file from the share" "$(vm "sudo cat /srv/strata/fast/pulls/smb/2026/plage.txt")" "souvenir"
check "share unmounted, credentials removed" "$(vm "findmnt -n -t cifs | wc -l; sudo ls -A /run/strata/backup | wc -l" | tr '\n' ' ')" "0 0"
check "snapshot after the SMB pull" "$(vm "sudo ls /srv/strata/fast/.snapshots | grep -c 'pulls_smb@pull-$SM-'")" "1"

# ZFS pull: the remote account only has delegated ZFS rights on its dataset.
vm "sudo zfs create tank/pullsrc && echo zdata | sudo tee /srv/strata/tank/pullsrc/z.txt >/dev/null && sudo zfs allow e2ebk snapshot,send,hold,destroy,mount tank/pullsrc"
ZPJ='"name":"pull-zfs","kind":"zfs","sourceType":"ssh","host":"127.0.0.1","user":"e2ebk","sources":["tank/pullsrc"],"destType":"local","destPath":"tank/pulled/src","keepLast":2,"cron":"","enabled":true'
check "ZFS pull: connection test" "$(c POST /api/backups/test -d "{$ZPJ}")" "dataset tank/pullsrc found"
ZP=$(c POST /api/backups -d "{$ZPJ}" | py 'print(d["id"])')
check "ZFS pull (full)" "$(job "$(c POST /api/backups/$ZP/run)")" "success"
vm "echo more | sudo tee /srv/strata/tank/pullsrc/z2.txt >/dev/null"
sleep 1.1
check "ZFS pull (incremental)" "$(job "$(c POST /api/backups/$ZP/run)")" "success"
sleep 1.1
job "$(c POST /api/backups/$ZP/run)" >/dev/null
check "ZFS pull retention, both sides" "$(vm "sudo zfs list -H -t snapshot -o name -d 1 tank/pulled/src | grep -c strata-pull-; sudo zfs list -H -t snapshot -o name -d 1 tank/pullsrc | grep -c strata-pull-" | tr '\n' ' ')" "2 2"
check "pulled dataset holds the data" "$(vm "sudo zfs mount tank/pulled/src 2>/dev/null; sudo cat \$(sudo zfs get -H -o value mountpoint tank/pulled/src)/z2.txt")" "more"

# Two-way sync between a folder of fast and the e2ebk account, through SFTP.
vm "sudo mkdir -p /srv/strata/fast/twoway && for f in local f1 f2 f3; do echo here | sudo tee /srv/strata/fast/twoway/\$f.txt >/dev/null; done; sudo -u e2ebk mkdir -p /home/e2ebk/twoway && echo there | sudo -u e2ebk tee /home/e2ebk/twoway/remote.txt >/dev/null"
SYJ='"name":"twoway","kind":"sync","sources":["/srv/strata/fast/twoway"],"host":"127.0.0.1","user":"e2ebk","destPath":"/home/e2ebk/twoway","keepLast":3,"cron":"","enabled":true'
check "two-way sync: connection test" "$(c POST /api/backups/test -d "{$SYJ}")" "Connection successful"
SY=$(c POST /api/backups -d "{$SYJ}" | py 'print(d["id"])')
check "first sync merges" "$(job "$(c POST /api/backups/$SY/run)")" "success"
check "both files on both sides" "$(vm "echo \$(sudo ls /srv/strata/fast/twoway) '|' \$(sudo ls /home/e2ebk/twoway)")" "f1.txt f2.txt f3.txt local.txt remote.txt | f1.txt f2.txt f3.txt local.txt remote.txt"
check "sync marked initialised" "$(c GET /api/backups | py "print([b.get('synced') for b in d if b['id']=='$SY'][0])")" "True"
vm "sleep 1; echo mine | sudo tee /srv/strata/fast/twoway/local.txt >/dev/null; echo theirs-longer | sudo -u e2ebk tee /home/e2ebk/twoway/local.txt >/dev/null"
check "sync with a conflict" "$(job "$(c POST /api/backups/$SY/run)")" "success"
check "conflict kept as two files (local)" "$(vm "sudo ls /srv/strata/fast/twoway | grep -c '^local\.txt'")" "2"
check "conflict kept as two files (remote)" "$(vm "sudo ls /home/e2ebk/twoway | grep -c '^local\.txt'")" "2"
vm "sudo -u e2ebk rm /home/e2ebk/twoway/remote.txt"
check "deletion sync" "$(job "$(c POST /api/backups/$SY/run)")" "success"
check "deletion propagated" "$(vm "sudo ls /srv/strata/fast/twoway/remote.txt 2>&1")" "No such file"
check "recoverable from the snapshot taken before" "$(vm "sudo cat /srv/strata/fast/.snapshots/root@sync-$SY-*/twoway/remote.txt 2>/dev/null | head -1")" "there"
check "resync" "$(job "$(c POST /api/backups/$SY/resync)")" "success"

# Time Machine destination.
check "Time Machine destination created" "$(c POST /api/backups/timemachine -d '{"name":"TMtest","path":"/srv/strata/fast/TMtest","maxGb":200,"users":["'$USER_NAME'"]}')" '"timeMachine":true'
check "Samba: Time Machine with size limit" "$(vm "sudo sed -n '/^\[TMtest\]/,/^\[/p' /etc/samba/strata-shares.conf | grep 'fruit:time machine'" | tr '\n' ' ')" "fruit:time machine max size = 200000000000"
vm "sudo mkdir -p '/srv/strata/fast/TMtest/Alice Mac.sparsebundle/bands' && sudo dd if=/dev/zero of='/srv/strata/fast/TMtest/Alice Mac.sparsebundle/bands/0' bs=1M count=3 status=none && printf '<?xml version=\"1.0\"?><plist version=\"1.0\"><dict><key>com.apple.backupd.HostUUID</key><string>E2E-UUID</string><key>com.apple.backupd.ModelID</key><string>Mac14,2</string></dict></plist>' | sudo tee '/srv/strata/fast/TMtest/Alice Mac.sparsebundle/com.apple.TimeMachine.MachineID.plist' >/dev/null"
check "Mac listed with its size" "$(c GET /api/backups/timemachine | py 'print([(b["name"], b["model"], b["size"] >= 3<<20) for s in d if s["name"]=="TMtest" for b in s["backups"]])')" "('Alice Mac', 'Mac14,2', True)"
c PUT /api/backups/timemachine/TMtest -d '{"maxGb":0}' >/dev/null
check "size limit removed" "$(vm "sudo sed -n '/^\[TMtest\]/,/^\[/p' /etc/samba/strata-shares.conf | grep -c 'max size'")" "0"
check "Time Machine share over SMB" "$(vm "smbclient //localhost/TMtest -U '$USER_NAME%$USER_PASS' -c ls 2>&1")" "Alice Mac.sparsebundle"

# Clean up.
for id in $PS $RD $SM $ZP $SY; do c DELETE /api/backups/$id >/dev/null; done
c DELETE /api/shares/TMtest >/dev/null
vm "for s in /srv/strata/fast/.snapshots/pulls_*@pull-* /srv/strata/fast/.snapshots/root@sync-*; do sudo btrfs subvolume delete \"\$s\" >/dev/null 2>&1; done; for s in ssh daemon smb; do sudo btrfs subvolume delete /srv/strata/fast/pulls/\$s >/dev/null 2>&1; done; sudo rm -rf /srv/strata/fast/pulls /srv/strata/fast/twoway /srv/strata/fast/TMtest; sudo zfs destroy -r tank/pulled; sudo zfs destroy -r tank/pullsrc; sudo userdel -r e2ebk 2>/dev/null"
check "backup tasks cleaned up" "$(c GET /api/backups | py 'print(sum(1 for b in d if b["name"] in ("pull-ssh","pull-daemon","pull-smb","pull-zfs","twoway")))')" "0"
