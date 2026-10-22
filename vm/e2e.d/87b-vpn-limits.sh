# WireGuard 0.6.1: IPv6 through the outgoing tunnel (or blocked), kill
# switch of the whole NAS, app recreated → back on the routed network at
# once, expiry warnings. Same test provider as 87-vpn.sh, with IPv6.
section "WireGuard: IPv6, whole-NAS kill switch, expiry warnings"
# The test echo server answers the peer address as socat prints it ([expanded
# IPv6], IPv4-mapped for IPv4 clients): compare the canonical form.
ipn() { python3 -c 'import ipaddress,sys
t=sys.stdin.read().strip().strip("[]")
try:
    a=ipaddress.ip_address(t); print(a.ipv4_mapped or a)
except ValueError: print(t)'; }

[ "$(c GET /api/vpn | py 'print(d["installed"])')" = True ] || job "$(c POST /api/system/components/vpn/install)" >/dev/null
VPNSET='"port":51820,"subnet":"10.8.0.0/24","endpoint":"172.17.0.1","lan":"10.0.2.0/24","defaultMode":"nas"'
check "VPN server on" "$(c PUT /api/vpn -d "{\"enabled\":true,$VPNSET}")" '"ok":true'

vm "printf 'FROM alpine:3.20\nRUN apk add --no-cache wireguard-tools iproute2 netcat-openbsd socat\n' | sudo docker build -q -t strata-wgtest - >/dev/null; sudo mkdir -p /tmp/wgtest6"
./vm/vm.sh ssh 'sudo tee /tmp/wgtest6/echo.sh >/dev/null' 2>/dev/null <<'EOF'
while read -r l; do l=$(printf '%s' "$l" | tr -d '\r'); [ -z "$l" ] && break; done
printf 'HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\n\r\n%s\n' "$SOCAT_PEERADDR"
EOF
./vm/vm.sh ssh 'sudo tee /tmp/wgtest6/srv-up.sh >/dev/null' 2>/dev/null <<'EOF'
# A VPN provider with IPv6: "Internet" addresses 203.0.113.1 and 2001:db8:66::1 behind it.
umask 077; mkdir -p /etc/wireguard
wg genkey > /tmp/s.key; wg pubkey < /tmp/s.key > /tmp/s.pub
wg genkey > /tmp/c.key; wg pubkey < /tmp/c.key > /tmp/c.pub
ip link add inet0 type dummy && ip addr add 203.0.113.1/32 dev inet0 && ip -6 addr add 2001:db8:66::1/128 dev inet0 && ip link set inet0 up
printf '[Interface]\nAddress = 10.66.0.1/24, fd66::1/64\nListenPort = 51821\nPrivateKey = %s\n\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.66.0.2/32, fd66::2/128\n' "$(cat /tmp/s.key)" "$(cat /tmp/c.pub)" > /etc/wireguard/wg0.conf
wg-quick up wg0
EP="$(hostname -i | tr ' ' '\n' | grep -m1 '\.'):51821"
printf '[Interface]\nPrivateKey = %s\nAddress = 10.66.0.2/32, fd66::2/128\n\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = 0.0.0.0/0, ::/0\n' "$(cat /tmp/c.key)" "$(cat /tmp/s.pub)" "$EP" > /tmp/provider6.conf
printf '[Interface]\nPrivateKey = %s\nAddress = 10.66.0.2/32\n\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = 0.0.0.0/0\n' "$(cat /tmp/c.key)" "$(cat /tmp/s.pub)" "$EP" > /tmp/provider4.conf
touch /tmp/ready
exec socat TCP6-LISTEN:8080,fork,reuseaddr,ipv6only=0 SYSTEM:'sh /t/echo.sh'
EOF
vm "sudo docker rm -f wgsrv6 vpntest6 wgdev6 >/dev/null 2>&1; sudo docker run -d --name wgsrv6 --cap-add NET_ADMIN --sysctl net.ipv6.conf.all.disable_ipv6=0 -v /tmp/wgtest6:/t:ro strata-wgtest sh /t/srv-up.sh >/dev/null"
for _ in $(seq 1 30); do vm "sudo docker exec wgsrv6 test -f /tmp/ready && echo ok" | grep -q ok && break; sleep 1; done
vm "sudo docker exec wgsrv6 cat /tmp/provider6.conf" > "$TMP/provider6.conf"
vm "sudo docker exec wgsrv6 cat /tmp/provider4.conf" > "$TMP/provider4.conf"
vm "sudo docker run -d --name vpntest6 strata-wgtest sleep 900 >/dev/null"
# client body: scope, conf file, extra JSON fields.
client() {
  local extra=${3:-}
  [ -n "$extra" ] || extra='{}'
  python3 -c 'import json,sys; b={"enabled":True,"name":"Test provider 6","scope":sys.argv[1],"apps":["vpntest6"],"checkUrl":"http://203.0.113.1:8080/","conf":open(sys.argv[2]).read()}; b.update(json.loads(sys.argv[3])); print(json.dumps(b))' "$1" "$2" "$extra"
}
handshake() { for _ in $(seq 1 20); do [ "$(c GET /api/vpn/client | py 'print((d["status"] or {}).get("handshake",0) > 0)')" = True ] && break; sleep 1; done; }
nets() { vm "sudo docker inspect -f '{{range \$k, \$v := .NetworkSettings.Networks}}{{\$k}} {{end}}' $1"; }

# ---- IPv6 of the routed apps ----
check "NAS has IPv6" "$(c GET /api/vpn/client | py 'print(d["hostIpv6"])')" "True"
R=$(c PUT /api/vpn/client -d "$(client apps "$TMP/provider6.conf")")
check "IPv6 provider file imported, no IPv6 warning" "$(echo "$R" | grep -q 'IPv6 is blocked' && echo warned || echo "$R")" '"warnings":[]'
handshake
check "tunnel up, IPv6 through it" "$(c GET /api/vpn/client | py 'print(d["routing"], d["ipv6"])')" "True True"
check "routed network has IPv6" "$(vm "sudo docker network inspect -f '{{.EnableIPv6}} {{range .IPAM.Config}}{{.Subnet}} {{end}}' strata-vpn")" "true 172.31.250.0/24 fd5f:cb20:250::/64"
check "routed app: IPv6 default route through the routed network" "$(vm "sudo docker exec vpntest6 ip -6 route show default")" "via fd5f:cb20:250::1"
check "routed app: no other IPv6 default route" "$(vm "sudo docker exec vpntest6 ip -6 route show default" | grep -vc 'fd5f:cb20:250::1')" "0"
check "routed app reaches IPv6 through the tunnel" "$(vm "sudo docker exec vpntest6 wget -q -T 8 -O - 'http://[2001:db8:66::1]:8080/'" | ipn)" "fd66::2"
check "and IPv4" "$(vm "sudo docker exec vpntest6 wget -q -T 8 -O - http://203.0.113.1:8080/" | ipn)" "10.66.0.2"
check "IPv6 kill switch rule" "$(vm "ip -6 rule show pref 5303")" "fd5f:cb20:250::/64 blackhole"
check "kill switch test (IPv4 and IPv6)" "$(c POST /api/vpn/client/killswitch | py 'print(d["blocked"], bool(d.get("downRoute6")))')" "True True"
check "IPv6 back after the test" "$(vm "sudo docker exec vpntest6 wget -q -T 8 -O - 'http://[2001:db8:66::1]:8080/'" | ipn)" "fd66::2"

# ---- App recreated (update): back on the routed network at once ----
vm "sudo docker rm -f vpntest6 >/dev/null; sudo docker run -d --name vpntest6 strata-wgtest sleep 900 >/dev/null"
check "recreated app back on the routed network within 5 s" "$(vm "for i in \$(seq 1 20); do sudo docker inspect -f '{{range \$k, \$v := .NetworkSettings.Networks}}{{\$k}} {{end}}' vpntest6 | grep -q strata-vpn && { echo attached; break; }; sleep 0.25; done")" "attached"
check "recreated app through the tunnel" "$(vm "sudo docker exec vpntest6 wget -q -T 8 -O - 'http://[2001:db8:66::1]:8080/'" | ipn)" "fd66::2"

# ---- Tunnel without IPv6: IPv6 of the routed apps blocked ----
check "IPv4-only provider file: IPv6 blocked warning" "$(c PUT /api/vpn/client -d "$(client apps "$TMP/provider4.conf")")" "IPv6 is blocked"
handshake
check "status: no IPv6 in the tunnel" "$(c GET /api/vpn/client | py 'print(d["routing"], d["ipv6"])')" "True False"
check "no IPv6 tunnel route" "$(vm "ip -6 route show table 52000 | wc -l")" "0"
check "routed app: IPv6 blocked" "$(vm "sudo docker exec vpntest6 wget -q -T 5 -O - 'http://[2001:db8:66::1]:8080/' 2>/dev/null || echo blocked")" "blocked"
check "host: no IPv6 way out for the routed network" "$(vm "ip -6 route get 2606:4700:4700::1111 from fd5f:cb20:250::1 2>&1 | grep -q ' dev ' && echo LEAK || echo blocked")" "blocked"
check "routed app: IPv4 still through the tunnel" "$(vm "sudo docker exec vpntest6 wget -q -T 8 -O - http://203.0.113.1:8080/" | ipn)" "10.66.0.2"

# ---- Whole NAS: kill switch ----
c PUT /api/vpn/client -d "$(client all "$TMP/provider6.conf")" >/dev/null
handshake
check "whole NAS through the tunnel (IPv4)" "$(vm "curl -s -m 8 http://203.0.113.1:8080/" | ipn)" "10.66.0.2"
check "whole NAS through the tunnel (IPv6)" "$(vm "curl -s -g -m 8 'http://[2001:db8:66::1]:8080/'" | ipn)" "fd66::2"
check "kill switch rules and boot unit" "$(vm "ip rule show pref 5305; ip -6 rule show pref 5305; ip rule show pref 5298 | grep -c 'lookup main'; systemctl is-enabled strata-vpn-killswitch")" "enabled"
check "kill switch rules: IPv4 and IPv6 blackholes" "$(vm "ip rule show pref 5305; ip -6 rule show pref 5305" | grep -c blackhole)" "2"
check "routed apps network gone in whole-NAS mode" "$(vm "sudo docker network ls --format '{{.Name}}' | grep -c '^strata-vpn$'")" "0"
check "kill switch test, whole NAS" "$(c POST /api/vpn/client/killswitch | py 'print(d["blocked"], bool(d.get("localRoute")), bool(d.get("serverRoute")))')" "True True True"
check "tunnel back after the test" "$(vm "curl -s -m 8 http://203.0.113.1:8080/" | ipn)" "10.66.0.2"
vm "sudo ip link set strata-out down"
check "tunnel down: no Internet for the NAS" "$(vm "curl -s -m 5 -o /dev/null http://deb.debian.org/ && echo LEAK || echo blocked")" "blocked"
check "tunnel down: no IPv6 Internet either" "$(vm "ip -6 route get 2606:4700:4700::1111 2>&1 | grep -q ' dev ' && echo LEAK || echo blocked")" "blocked"
check "tunnel down: the local network still answers (API, route, DNS)" "$(c GET /api/vpn/client | py 'print("api")'; vm "ip route get 10.0.2.2 | grep -q 'dev strata-out' && echo tunnel || echo local; getent hosts deb.debian.org >/dev/null && echo dns")" "$(printf 'api\nlocal\ndns')"
DEV=$(c POST /api/vpn/peers -d '{"name":"Kill switch phone","mode":"nas"}')
DEV_ID=$(echo "$DEV" | py 'print(d["peer"]["id"])'); echo "$DEV" | py 'print(d["config"])' > "$TMP/ks.conf"
./vm/vm.sh ssh "cat > /tmp/wgdev6.conf; sudo docker run -d --name wgdev6 --cap-add NET_ADMIN -v /tmp/wgdev6.conf:/etc/wireguard/wg0.conf:ro strata-wgtest sh -c 'wg-quick up wg0 && sleep 900' >/dev/null" < "$TMP/ks.conf" 2>/dev/null
for _ in $(seq 1 20); do vm "sudo docker exec wgdev6 ping -c1 -W1 10.8.0.1 >/dev/null 2>&1 && echo up" | grep -q up && break; sleep 1; done
check "tunnel down: the WireGuard server still works" "$(vm "sudo docker exec wgdev6 ping -c1 -W2 10.8.0.1 >/dev/null 2>&1 && echo up || echo down")" "up"
check "updates blocked by default" "$(vm "sudo -u _apt curl -s -m 5 -o /dev/null http://deb.debian.org/debian/ && echo reached || echo blocked")" "blocked"
c PUT /api/vpn/client -d "$(client all "$TMP/provider6.conf" '{"allowUpdates":true}')" >/dev/null
check "updates allowed: APT rule" "$(vm "ip rule show pref 5304")" "uidrange"
check "updates allowed: APT reaches its repository" "$(vm "sudo -u _apt curl -s -m 8 -o /dev/null http://deb.debian.org/debian/ && echo reached || echo blocked")" "reached"
check "updates allowed: the rest still blocked" "$(vm "curl -s -m 5 -o /dev/null http://deb.debian.org/ && echo LEAK || echo blocked")" "blocked"
check "reconnect" "$(c POST /api/vpn/client/reconnect)" '"ok":true'
check "whole NAS back through the tunnel" "$(vm "curl -s -m 8 http://203.0.113.1:8080/" | ipn)" "10.66.0.2"
check "tunnel removed" "$(c DELETE /api/vpn/client)" '"ok":true'
check "kill switch removed with the tunnel" "$(vm "ip rule show | grep -cE '^5(298|30[0-5]):'; ip -6 rule show | grep -cE '^5(298|30[0-5]):'; test -e /etc/systemd/system/strata-vpn-killswitch.service && echo unit")" "$(printf '0\n0')"
check "Internet back without the tunnel" "$(vm "curl -s -m 8 -o /dev/null http://deb.debian.org/ && echo up || echo down")" "up"
check "app given its network back" "$(nets vpntest6)" "bridge"

# ---- Expiry warning ----
TOMORROW=$(date -d tomorrow +%F)
EXP=$(c POST /api/vpn/peers -d "{\"name\":\"Expiring tablet\",\"mode\":\"nas\",\"expires\":\"$TOMORROW\"}" | py 'print(d["peer"]["id"])')
check "warning event for a device expiring tomorrow" "$(c GET /api/system/events | py '[print(e["level"], e["message"]) for e in d[:30]]')" "warning VPN device Expiring tablet expires on $TOMORROW"
c PUT /api/vpn/peers/$EXP -d "{\"name\":\"Expiring tablet\",\"mode\":\"nas\",\"expires\":\"$TOMORROW\"}" >/dev/null
check "warned once" "$(c GET /api/system/events | py 'print(sum("Expiring tablet expires" in e["message"] for e in d[:50]))')" "1"
check "warning delay in the settings" "$(c GET /api/vpn | py 'print(d["warnDays"])')" "7"

# ---- Clean up ----
c DELETE /api/vpn/peers/$EXP >/dev/null; c DELETE /api/vpn/peers/$DEV_ID >/dev/null
vm "sudo docker rm -f wgsrv6 vpntest6 wgdev6 >/dev/null 2>&1; sudo docker rmi strata-wgtest >/dev/null 2>&1; sudo rm -rf /tmp/wgtest6 /tmp/wgdev6.conf"
c PUT /api/vpn -d "{\"enabled\":false,$VPNSET,\"userDevices\":false}" >/dev/null
check "VPN off, firewall table removed" "$(vm "sudo nft list tables | grep -c strata_wg; test -f /etc/sysctl.d/90-strata-vpn.conf && echo dropin")" "0"
