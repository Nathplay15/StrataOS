# shellcheck shell=bash
# SSH password authentication for backups (0.11.1): an rsync copy to an SSH
# account that only has a password (a local account reached over 127.0.0.1).
# The copy runs, the password is masked in the API and never appears on a
# command line or in the job log.
section "Backups to an SSH server with a password"

SPUSER=e2essh
SPPASS="Ssh-$(head -c 9 /dev/urandom | base64 | tr -d '/+=')"
SPSRC=/srv/strata/main/e2e-sshpass-src
vm "sudo useradd -m -s /bin/bash $SPUSER 2>/dev/null; echo '$SPUSER:$SPPASS' | sudo chpasswd
  command -v sshpass >/dev/null || sudo apt-get install -y -qq sshpass >/dev/null 2>&1
  printf 'Match Address 127.0.0.1 User $SPUSER\n  PasswordAuthentication yes\n  KbdInteractiveAuthentication yes\n' | sudo tee /etc/ssh/sshd_config.d/00-e2e-sshpass.conf >/dev/null
  sudo systemctl reload ssh
  sudo mkdir -p $SPSRC && for i in 1 2 3; do sudo dd if=/dev/urandom of=$SPSRC/f\$i bs=1M count=8 status=none; done"

SPT=$(c POST /api/backups -d "{\"name\":\"e2e-sshpass\",\"kind\":\"rsync\",\"sources\":[\"$SPSRC\"],\"destType\":\"ssh\",\"host\":\"127.0.0.1\",\"port\":22,\"user\":\"$SPUSER\",\"sshPassword\":\"$SPPASS\",\"destPath\":\"/home/$SPUSER/backup\",\"bwLimit\":\"4M\",\"cron\":\"\",\"enabled\":true}" | py 'print(d["id"])')
check "SSH password masked in the task list" "$(c GET /api/backups | py "print([b.get('sshPassword') for b in d if b['id']=='$SPT'][0])")" "********"
check "SSH password test" "$(c POST /api/backups/test -d "$(c GET /api/backups | py "import json as j; print(j.dumps([b for b in d if b['id']=='$SPT'][0]))")")" "SSH connection successful"
SPJ=$(c POST /api/backups/$SPT/run)
sleep 2
SPPS=$(vm "ps -C sshpass,ssh,rsync -o args=")
check "sshpass running during the copy" "$(echo "$SPPS" | grep -c '^sshpass -f')" "1"
check "password never on a command line" "$(echo "$SPPS" | grep -c -- "$SPPASS")" "0"
check "rsync over SSH with a password" "$(job "$SPJ")" "success"
check "files copied to the SSH account" "$(vm "sudo ls /home/$SPUSER/backup/e2e-sshpass-src | wc -l")" "3"
check "password not in the job log" "$(c GET /api/jobs/$(echo "$SPJ" | py 'print(d["id"])') | py "print('$SPPASS' in d.get('log',''))")" "False"
check "password kept on save (masked value sent back)" "$(c PUT /api/backups/$SPT -d "$(c GET /api/backups | py "import json as j; print(j.dumps([b for b in d if b['id']=='$SPT'][0]))")" >/dev/null; job "$(c POST /api/backups/$SPT/run)")" "success"

c DELETE /api/backups/$SPT >/dev/null
vm "sudo rm -f /etc/ssh/sshd_config.d/00-e2e-sshpass.conf; sudo systemctl reload ssh; sudo userdel -r $SPUSER 2>/dev/null; sudo rm -rf $SPSRC"
