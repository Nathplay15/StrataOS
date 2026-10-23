# shellcheck shell=bash
# Appliance console: strata-console.service replaces the login prompt on
# tty1 (getty@tty1 masked), shows the address and health (checked with
# `strata console --render-once`), and asks the daemon for its local actions
# over a root-only unix socket. A recovery code taken from that socket resets
# the password of a test administrator on the web API (once), the network
# reset to DHCP is kept from the socket like the console does, and turning
# the screen off gives tty1 back to getty.
section "Appliance console (tty1)"
CS=/run/strata/console.sock
sock() { vm "sudo curl -s -m 20 --unix-socket $CS -X $1 http://console$2"; }
check "screen on by default" "$(c GET /api/system/console | py 'print(d["enabled"], d["recovery"], d["installed"])')" "True True True"
for _ in $(seq 1 15); do [ "$(vm "systemctl is-active strata-console.service")" = active ] && break; sleep 2; done
check "screen running on tty1, getty@tty1 masked" "$(vm "systemctl is-active strata-console.service getty@tty1.service; systemctl is-enabled getty@tty1.service" | tr '\n' ' ')" "active inactive masked"
check "only tty1 taken: other consoles and serial lines untouched" "$(vm "ls /etc/systemd/system | grep -c -e 'getty@tty[2-6]' -e serial-getty@")" "0"
SCREEN=$(vm "sudo strata console --render-once")
has() { echo "$SCREEN" | grep -q -e "$1" && echo -n y || echo -n " missing: $1 "; }
check "screen: name, version and address" "$(has "StrataOS $(curl -s $B/api/setup | py 'print(d["version"])')")$(has strata-vm)$(has 'http://[0-9.]*:7070')$(has 'https://[0-9.]*:7443')" "yyyy"
check "screen: setup done, health, QR code, menu" "$(has 'Manage the NAS from your browser')$(has Health)$(has 'Scan to open')$(has '3  Administrator recovery code')$(has '5  Login prompt')" "yyyyy"
check "socket refused to other users" "$(vm "curl -s -m 5 --unix-socket $CS http://console/status; echo rc=\$?")" "rc=7"
check "socket status for root" "$(sock GET /status | py 'print(d["setupDone"], d["recovery"], d["httpPort"])')" "True True 7070"

# Recovery code: a test administrator gets a new password.
RA=rescueadm RA_OLD=Rescue-Old-1 RA_NEW=Rescue-New-2
check "test administrator" "$(c POST /api/users -d "{\"username\":\"$RA\",\"password\":\"$RA_OLD\",\"role\":\"admin\"}")" '"ok":true'
RC=$(sock POST /recovery | py 'print(d["code"])')
check "code generated on the console" "$RC" "-"
check "wrong code refused" "$(curl -s -X POST -H 'Content-Type: application/json' $B/api/login/recovery/check -d '{"code":"AAAA-AAAA-AAAA"}')" "invalid or expired recovery code"
check "code lists the administrators" "$(curl -s -X POST -H 'Content-Type: application/json' $B/api/login/recovery/check -d "{\"code\":\"$RC\"}" | py 'print(sorted(a["username"] for a in d["admins"]), d["canCreate"])')" "['$ADMIN_USER', '$RA'] False"
check "weak password refused" "$(curl -s -X POST -H 'Content-Type: application/json' $B/api/login/recovery/reset -d "{\"code\":\"$RC\",\"username\":\"$RA\",\"password\":\"x\"}")" "error"
check "non-administrator refused" "$(curl -s -X POST -H 'Content-Type: application/json' $B/api/login/recovery/reset -d "{\"code\":\"$RC\",\"username\":\"$USER_NAME\",\"password\":\"$RA_NEW\"}")" "not an administrator"
check "password reset with the code" "$(curl -s -X POST -H 'Content-Type: application/json' $B/api/login/recovery/reset -d "{\"code\":\"$(echo "$RC" | tr 'A-Z-' 'a-z ')\",\"username\":\"$RA\",\"password\":\"$RA_NEW\"}")" '"ok":true'
check "code used once" "$(curl -s -X POST -H 'Content-Type: application/json' $B/api/login/recovery/reset -d "{\"code\":\"$RC\",\"username\":\"$RA\",\"password\":\"$RA_OLD\"}")" "invalid or expired recovery code"
check "old password refused" "$(curl -s -X POST -H 'Content-Type: application/json' $B/api/login -d "{\"username\":\"$RA\",\"password\":\"$RA_OLD\"}")" "invalid username or password"
check "new password signs in" "$(curl -s -X POST -H 'Content-Type: application/json' $B/api/login -d "{\"username\":\"$RA\",\"password\":\"$RA_NEW\"}")" '"role":"admin"'
check "Samba password changed too" "$(vm "smbclient -g -L localhost -U '$RA%$RA_NEW' 2>&1")" "Disk|Photos"
check "audited" "$(c GET /api/system/events | py 'm=[e["message"] for e in d[:30]]; print(any("recovery code generated on the NAS console" in x for x in m), any(x.startswith("Password of administrator '"$RA"' reset with a recovery code") for x in m))')" "True True"
check "codes can be forbidden" "$(c PUT /api/system/console -d '{"enabled":true,"recovery":false}'; sock POST /recovery)" "disabled in Settings"
c PUT /api/system/console -d '{"enabled":true,"recovery":true}' >/dev/null
c DELETE /api/users/$RA >/dev/null

# Network reset to DHCP, kept from the console (the daemon holds the token).
check "network reset on trial" "$(sock POST /network/reset | py 'print("deadline" in d)')" "True"
check "pending change visible" "$(c GET /api/system/network | py 'print(d["pending"] is not None)'; sock GET /status | py 'print("networkDeadline" in d)')" "$(printf 'True\nTrue')"
KEPT=""
for _ in $(seq 1 20); do KEPT=$(sock POST /network/keep); case "$KEPT" in *'"ok":true'*) break ;; esac; sleep 2; done
check "reset kept" "$KEPT" '"ok":true'
check "every interface on DHCP" "$(c GET /api/system/network | py 'c=d["config"]; print(d["pending"], len(c["interfaces"]) > 0 and all(i["mode"]=="dhcp" for i in c["interfaces"]), c["bonds"], c["vlans"])')" "None True [] []"
for _ in $(seq 1 20); do curl -sf -m 3 "$B/api/setup" >/dev/null && break; sleep 2; done

# Off: getty back on tty1.
check "screen turned off" "$(c PUT /api/system/console -d '{"enabled":false,"recovery":true}')" '"ok":true'
check "getty back on tty1" "$(vm "systemctl is-active strata-console.service getty@tty1.service; systemctl is-enabled getty@tty1.service | grep -c masked" | tr '\n' ' ')" "inactive active 0"
check "setting kept" "$(c GET /api/system/console | py 'print(d["enabled"], d["active"])')" "False False"
check "screen back on" "$(c PUT /api/system/console -d '{"enabled":true,"recovery":true}'; vm "systemctl is-active strata-console.service")" "active"
