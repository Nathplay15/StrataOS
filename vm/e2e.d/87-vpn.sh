# WireGuard 0.6: devices per user, per-device policy (services, expiry),
# site-to-site with a router in a container, outgoing tunnel to a test
# WireGuard server in a container (routed Docker app and kill switch).
section "WireGuard: users, policies, sites, outgoing tunnel"

[ "$(c GET /api/vpn | py 'print(d["installed"])')" = True ] || job "$(c POST /api/system/components/vpn/install)" >/dev/null
VPNSET='"port":51820,"subnet":"10.8.0.0/24","endpoint":"172.17.0.1","lan":"10.0.2.0/24","defaultMode":"nas"'
check "VPN server on, user devices off" "$(c PUT /api/vpn -d "{\"enabled\":true,$VPNSET,\"userDevices\":false}")" '"ok":true'

# Test tools: one image with WireGuard, netcat and socat; scripts of the test containers.
vm "printf 'FROM alpine:3.20\nRUN apk add --no-cache wireguard-tools iproute2 netcat-openbsd socat\n' | sudo docker build -q -t strata-wgtest - >/dev/null; sudo mkdir -p /tmp/wgtest"
./vm/vm.sh ssh 'sudo tee /tmp/wgtest/echo.sh >/dev/null' 2>/dev/null <<'EOF'
# Tiny HTTP server answer: the address the request came from.
while read -r l; do l=$(printf '%s' "$l" | tr -d '\r'); [ -z "$l" ] && break; done
printf 'HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\n\r\n%s\n' "$SOCAT_PEERADDR"
EOF
./vm/vm.sh ssh 'sudo tee /tmp/wgtest/site-up.sh >/dev/null' 2>/dev/null <<'EOF'
# The other site: its network 192.168.77.0/24 behind a WireGuard router.
ip link add lan0 type dummy && ip addr add 192.168.77.1/24 dev lan0 && ip link set lan0 up
mkdir -p /etc/wireguard && cp /t/site.conf /etc/wireguard/wg0.conf && wg-quick up wg0
exec socat TCP-LISTEN:8080,fork,reuseaddr SYSTEM:'sh /t/echo.sh'
EOF
./vm/vm.sh ssh 'sudo tee /tmp/wgtest/srv-up.sh >/dev/null' 2>/dev/null <<'EOF'
# A VPN provider: WireGuard server, "Internet" address 203.0.113.1 behind it.
umask 077; mkdir -p /etc/wireguard
wg genkey > /tmp/s.key; wg pubkey < /tmp/s.key > /tmp/s.pub
wg genkey > /tmp/c.key; wg pubkey < /tmp/c.key > /tmp/c.pub
ip link add inet0 type dummy && ip addr add 203.0.113.1/32 dev inet0 && ip link set inet0 up
printf '[Interface]\nAddress = 10.66.0.1/24\nListenPort = 51821\nPrivateKey = %s\n\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.66.0.2/32\n' "$(cat /tmp/s.key)" "$(cat /tmp/c.pub)" > /etc/wireguard/wg0.conf
wg-quick up wg0
printf '# Provider file\n[Interface]\nPrivateKey = %s\nAddress = 10.66.0.2/32, fd66::2/128\nDNS = 10.66.0.1\nPostUp = touch /tmp/pwned\n\n[Peer]\nPublicKey = %s\nEndpoint = %s:51821\nAllowedIPs = 0.0.0.0/0, ::/0\n' "$(cat /tmp/c.key)" "$(cat /tmp/s.pub)" "$(hostname -i | cut -d' ' -f1)" > /tmp/provider.conf
touch /tmp/ready
exec socat TCP-LISTEN:8080,fork,reuseaddr SYSTEM:'sh /t/echo.sh'
EOF
# wgrun name conf: WireGuard client in a container, up once 10.8.0.1 answers.
wgrun() {
  ./vm/vm.sh ssh "cat > /tmp/$1.conf; sudo docker rm -f $1 >/dev/null 2>&1; sudo docker run -d --name $1 --cap-add NET_ADMIN -v /tmp/$1.conf:/etc/wireguard/wg0.conf:ro strata-wgtest sh -c 'wg-quick up wg0 && sleep 900' >/dev/null" < "$2" 2>/dev/null
  for _ in $(seq 1 30); do vm "sudo docker exec $1 ping -c1 -W1 10.8.0.1 >/dev/null 2>&1 && echo up" | grep -q up && break; sleep 1; done
}
peer() { c GET /api/vpn | py "p=[p for p in d['config']['peers'] if p['id']=='$1'][0]; print($2)"; }

# ---- Devices per user ----
u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null
check "user refused when user devices are off" "$(u POST /api/vpn/my/devices -d '{"name":"Alice phone"}')" "does not let users"
check "users cannot use the admin VPN API" "$(u GET /api/vpn)" "administrator rights required"
c PUT /api/vpn -d "{\"enabled\":true,$VPNSET,\"userDevices\":true}" >/dev/null
u POST /api/vpn/my/devices -d '{"name":"Alice phone"}' | py 'print(d["config"])' > "$TMP/alice.conf"
check "alice adds her own device" "$(cat "$TMP/alice.conf")" "Endpoint = 172.17.0.1:51820"
ALICE_DEV=$(u GET /api/vpn/my | py 'print(d["devices"][0]["id"])')
check "device owned by alice, default access" "$(peer "$ALICE_DEV" "p['owner'], p['mode']")" "('alice', 'nas')"
check "alice sees only her devices, no keys" "$(u GET /api/vpn/my | py 'print(len(d["devices"]), "privateKey" in json.dumps(d))')" "1 False"
ADMIN_DEV=$(c POST /api/vpn/peers -d '{"name":"Admin laptop","mode":"lan"}' | py 'print(d["peer"]["id"])')
check "alice cannot read another device" "$(u GET /api/vpn/my/devices/$ADMIN_DEV/config)" "device not found"
check "alice cannot revoke another device" "$(u DELETE /api/vpn/my/devices/$ADMIN_DEV)" "device not found"
ALICE_PUB=$(peer "$ALICE_DEV" "p['publicKey']")
check "alice's device in WireGuard" "$(vm "sudo wg show strata peers")" "$ALICE_PUB"
# ubody True|False: the account as it is, enabled or disabled.
ubody() { c GET /api/users | py "u=[x for x in d if x['username']=='$USER_NAME'][0]; print(json.dumps({'fullName':u['fullName'],'email':u['email'],'role':u['role'],'disabled':$1,'groups':u['groups'],'ssh':u['ssh'],'docker':u.get('docker',False)}))"; }
c PUT /api/users/$USER_NAME -d "$(ubody True)" >/dev/null
for _ in $(seq 1 10); do vm "sudo wg show strata peers" | grep -q "$ALICE_PUB" || break; sleep 1; done
check "disabling the user disables her devices" "$(vm "sudo wg show strata peers" | grep -c "$ALICE_PUB") $(c GET /api/vpn | py "print(d['inactive'].get('$ALICE_DEV'))")" "0 user"
c PUT /api/users/$USER_NAME -d "$(ubody False)" >/dev/null
for _ in $(seq 1 10); do vm "sudo wg show strata peers" | grep -q "$ALICE_PUB" && break; sleep 1; done
check "devices back with the account" "$(vm "sudo wg show strata peers")" "$ALICE_PUB"
u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null
check "alice revokes her device" "$(u DELETE /api/vpn/my/devices/$ALICE_DEV)" '"ok":true'
check "revoked device gone" "$(vm "sudo wg show strata peers" | grep -c "$ALICE_PUB")" "0"

# ---- Per-device restriction and expiry ----
SMB=$(c POST /api/vpn/peers -d '{"name":"SMB only","mode":"nas","services":["smb"]}')
SMB_ID=$(echo "$SMB" | py 'print(d["peer"]["id"])'); echo "$SMB" | py 'print(d["config"])' > "$TMP/smb.conf"
check "invalid service refused" "$(c PUT /api/vpn/peers/$SMB_ID -d '{"mode":"nas","services":["tcp/80;drop"]}')" "unknown service"
wgrun wgr "$TMP/smb.conf"
check "SMB port reachable from the restricted device" "$(vm "sudo docker exec wgr nc -z -w3 10.8.0.1 445 && echo open || echo closed")" "open"
check "web interface port blocked for it" "$(vm "sudo docker exec wgr nc -z -w3 10.8.0.1 7070 && echo open || echo closed")" "closed"
check "handshake, transfer and endpoint reported" "$(c GET /api/vpn | py "s=d['status'].get('$(peer "$SMB_ID" "p['publicKey']")',{}); print(s.get('handshake',0)>0, s.get('rx',0)>0, bool(s.get('endpoint')))")" "True True True"
c PUT /api/vpn/peers/$SMB_ID -d '{"name":"SMB only","mode":"nas","services":["smb"],"expires":"2020-01-01"}' >/dev/null
check "expiry in the past disables the device" "$(peer "$SMB_ID" "p['disabled'], p.get('expired'), d['inactive'].get(p['id'])")" "(True, True, 'expired')"
check "expired device cut off" "$(vm "sudo docker exec wgr ping -c1 -W2 10.8.0.1 >/dev/null 2>&1 && echo up || echo down")" "down"
check "expiry event" "$(c GET /api/system/events | py '[print(e["message"]) for e in d[:20]]')" "VPN device SMB only expired and was disabled"
check "enabling an expired device refused" "$(c PUT /api/vpn/peers/$SMB_ID -d '{"name":"SMB only","mode":"nas","services":["smb"],"expires":"2020-01-01","disabled":false}')" "expiry date has passed"

# ---- Site to site ----
SITE=$(c POST /api/vpn/peers -d '{"name":"Branch","kind":"site","subnets":["192.168.77.0/24"]}')
SITE_ID=$(echo "$SITE" | py 'print(d["peer"]["id"])'); echo "$SITE" | py 'print(d["config"])' > "$TMP/site.conf"
check "router configuration" "$(cat "$TMP/site.conf")" "AllowedIPs = 10.8.0.0/24, 10.0.2.0/24"
check "StrataOS site code" "$(c GET "/api/vpn/peers/$SITE_ID/config?format=strataos")" "strataos-site:"
check "overlapping site network refused" "$(c POST /api/vpn/peers -d '{"name":"Bad","kind":"site","subnets":["10.0.2.0/25"]}')" "overlaps"
check "own site code refused (same networks)" "$(c POST /api/vpn/links -d "{\"code\":\"$(c GET "/api/vpn/peers/$SITE_ID/config?format=strataos" | py 'print(d["config"])')\"}")" "overlaps"
./vm/vm.sh ssh "sudo tee /tmp/wgtest/site.conf >/dev/null; sudo docker rm -f wgsite >/dev/null 2>&1; sudo docker run -d --name wgsite --cap-add NET_ADMIN -v /tmp/wgtest:/t:ro strata-wgtest sh /t/site-up.sh >/dev/null" < "$TMP/site.conf" 2>/dev/null
for _ in $(seq 1 30); do vm "ping -c1 -W1 192.168.77.1 >/dev/null 2>&1 && echo up" | grep -q up && break; sleep 1; done
check "route to the remote network" "$(vm "ip route get 192.168.77.1")" "dev strata"
check "remote network answers through the tunnel" "$(vm "ping -c1 -W2 192.168.77.1 >/dev/null && echo up || echo down")" "up"
check "HTTP to the remote network, from the NAS VPN address" "$(vm "curl -s -m 5 http://192.168.77.1:8080/")" "10.8.0.1"
check "forwarding on" "$(vm "cat /proc/sys/net/ipv4/ip_forward; test -f /etc/sysctl.d/90-strata-vpn.conf && echo dropin")" "dropin"
c DELETE /api/vpn/peers/$SITE_ID >/dev/null
check "site removed, its route too" "$(vm "ip route show 192.168.77.0/24 | wc -l")" "0"

# ---- Outgoing tunnel ----
vm "sudo docker rm -f wgsrv vpntest vpnfree >/dev/null 2>&1; sudo docker run -d --name wgsrv --cap-add NET_ADMIN -v /tmp/wgtest:/t:ro strata-wgtest sh /t/srv-up.sh >/dev/null"
for _ in $(seq 1 30); do vm "sudo docker exec wgsrv test -f /tmp/ready && echo ok" | grep -q ok && break; sleep 1; done
vm "sudo docker exec wgsrv cat /tmp/provider.conf" > "$TMP/provider.conf"
vm "sudo docker run -d --name vpntest strata-wgtest sleep 900 >/dev/null; sudo docker run -d --name vpnfree strata-wgtest sleep 900 >/dev/null"
check "Internet reachable without the tunnel (control)" "$(vm "sudo docker exec vpnfree wget -q -T 8 -O /dev/null http://deb.debian.org/ && echo up || echo down")" "up"
check "invalid provider file refused" "$(c PUT /api/vpn/client -d '{"enabled":true,"conf":"[Interface]\nPrivateKey = x\n"}')" "invalid private key"
BODY=$(python3 -c 'import json,sys; print(json.dumps({"enabled":True,"name":"Test provider","scope":"apps","apps":["vpntest"],"checkUrl":"http://203.0.113.1:8080/","conf":open(sys.argv[1]).read()}))' "$TMP/provider.conf")
check "provider file imported, scripts ignored" "$(c PUT /api/vpn/client -d "$BODY")" "ignored: postup"
for _ in $(seq 1 20); do [ "$(c GET /api/vpn/client | py 'print((d["status"] or {}).get("handshake",0) > 0)')" = True ] && break; sleep 1; done
check "tunnel connected" "$(c GET /api/vpn/client | py 'print((d["status"] or {}).get("handshake",0) > 0, d["routing"], "privateKey" in d["config"] and d["config"]["privateKey"] != "********")')" "True True False"
check "provider scripts never run" "$(vm "test -e /tmp/pwned && echo ran || echo clean")" "clean"
check "routed app on the tunnel network" "$(vm "sudo docker inspect -f '{{range \$k, \$v := .NetworkSettings.Networks}}{{\$k}} {{end}}' vpntest")" "strata-vpn"
check "routed app reaches the Internet through the tunnel" "$(vm "sudo docker exec vpntest wget -q -T 8 -O - http://203.0.113.1:8080/")" "10.66.0.2"
check "other containers do not use the tunnel" "$(vm "sudo docker exec vpnfree wget -q -T 4 -O - http://203.0.113.1:8080/ 2>/dev/null || echo unreachable")" "unreachable"
check "public address seen through the tunnel" "$(c POST /api/vpn/client/check)" "10.66.0.2"
check "kill switch test" "$(c POST /api/vpn/client/killswitch)" '"blocked":true'
check "tunnel back after the test" "$(vm "sudo docker exec vpntest wget -q -T 8 -O - http://203.0.113.1:8080/")" "10.66.0.2"
vm "sudo ip link set strata-out down"
check "tunnel down: no way out for the routed app" "$(vm "sudo docker exec vpntest wget -q -T 5 -O /dev/null http://deb.debian.org/ && echo LEAK || echo blocked")" "blocked"
check "kill switch rule in place" "$(vm "ip rule show pref 5303")" "blackhole"
check "reconnect" "$(c POST /api/vpn/client/reconnect)" '"ok":true'
check "routed app back through the tunnel" "$(vm "sudo docker exec vpntest wget -q -T 8 -O - http://203.0.113.1:8080/")" "10.66.0.2"
check "tunnel removed" "$(c DELETE /api/vpn/client)" '"ok":true'
check "app given its network back" "$(vm "sudo docker inspect -f '{{range \$k, \$v := .NetworkSettings.Networks}}{{\$k}} {{end}}' vpntest")" "bridge"
check "routing and tunnel cleaned up" "$(vm "ip rule show | grep -c '^53'; ip link show strata-out >/dev/null 2>&1 && echo iface; sudo docker network ls --format '{{.Name}}' | grep -c strata-vpn")" "$(printf '0\n0')"

# ---- Clean up ----
vm "sudo docker rm -f wgr wgsite wgsrv vpntest vpnfree >/dev/null 2>&1; sudo docker rmi strata-wgtest >/dev/null 2>&1; sudo rm -rf /tmp/wgtest /tmp/wgr.conf"
c DELETE /api/vpn/peers/$SMB_ID >/dev/null; c DELETE /api/vpn/peers/$ADMIN_DEV >/dev/null
c PUT /api/vpn -d "{\"enabled\":false,$VPNSET,\"userDevices\":false}" >/dev/null
check "VPN off, firewall table removed" "$(vm "sudo nft list tables | grep -c strata_wg; test -f /etc/sysctl.d/90-strata-vpn.conf && echo dropin")" "0"
