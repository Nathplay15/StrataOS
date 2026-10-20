# Remote access: app domains (reverse proxy on 80/443), Tailscale and
# Cloudflare Tunnel (installed, no real account: "not logged in" / "not configured").
section "Remote access (app domains, Tailscale, Cloudflare Tunnel)"

# R name curl-args…: request a name served by the NAS itself.
R() { vm "curl -sk -m 10 --resolve $1:443:127.0.0.1 --resolve $1:80:127.0.0.1 $(printf '%q ' "${@:2}")"; }
check "ports 80 and 443 free" "$(vm "sudo ss -Hltn 'sport = :80 or sport = :443' | wc -l")" "0"

# A program on port 80: enabling must be refused and say who uses it.
vm "sudo systemd-run --unit=e2e-port80 python3 -m http.server 80 --bind 0.0.0.0 >/dev/null 2>&1"
for _ in $(seq 1 10); do vm "sudo ss -Hltn 'sport = :80'" | grep -q LISTEN && break; sleep 1; done
check "busy port refused, owner named" "$(c PUT /api/remote/domains/settings -d '{"enabled":true}')" "port 80 is already used by python3"
vm "sudo systemctl stop e2e-port80"
check "proxy enabled" "$(c PUT /api/remote/domains/settings -d '{"enabled":true}')" '"ok":true'
check "listening on 80 and 443" "$(vm "sudo ss -Hltnp 'sport = :80 or sport = :443'" | grep -c strata)" "2"

# Target: Uptime Kuma installed by the main suite, or a small web server.
KUMA=$(c GET /api/apps | py 'print(d["installed"].get("uptimekuma",""))')
if [ -n "$KUMA" ]; then
  APPDOM="{\"host\":\"kuma.strata.test\",\"kind\":\"app\",\"app\":\"$KUMA\"}"
else
  vm "sudo mkdir -p /tmp/e2e-web && echo 'Uptime Kuma stand-in' | sudo tee /tmp/e2e-web/index.html >/dev/null && sudo systemd-run --unit=e2e-web python3 -m http.server 18081 --bind 127.0.0.1 --directory /tmp/e2e-web >/dev/null 2>&1"
  APPDOM='{"host":"kuma.strata.test","kind":"url","target":"http://127.0.0.1:18081"}'
fi
check "invalid host name refused" "$(c POST /api/remote/domains -d '{"host":"bad_host","kind":"ui"}')" "host name"
check "Internet target refused" "$(c POST /api/remote/domains -d '{"host":"x.strata.test","kind":"url","target":"http://8.8.8.8:80"}')" "local network"
check "interface port refused as an address" "$(c POST /api/remote/domains -d '{"host":"x.strata.test","kind":"url","target":"http://127.0.0.1:7070"}')" "StrataOS interface"
DOM=$(c POST /api/remote/domains -d "$APPDOM" | py 'print(d["id"])')
check "duplicate name refused" "$(c POST /api/remote/domains -d "$APPDOM")" "already published"
UIDOM=$(c POST /api/remote/domains -d '{"host":"nas.strata.test","kind":"ui"}' | py 'print(d["id"])')
check "app served over HTTPS" "$(R kuma.strata.test -L https://kuma.strata.test/)" "Uptime Kuma"
check "self-signed certificate of the name" "$(vm "echo | openssl s_client -connect 127.0.0.1:443 -servername kuma.strata.test 2>/dev/null | openssl x509 -noout -subject")" "kuma.strata.test"
check "certificate state shown" "$(c GET /api/remote/domains | py 'print([(x["cert"]["source"], x["localName"]) for x in d["domains"] if x["host"]=="kuma.strata.test"])')" "('self-signed', True)"
check "HTTP redirected to HTTPS" "$(R kuma.strata.test -o /dev/null -w '%{http_code} %{redirect_url}' http://kuma.strata.test/x?y=1)" "308 https://kuma.strata.test/x?y=1"
check "unknown name answers 404" "$(R other.strata.test -o /dev/null -w '%{http_code}' https://other.strata.test/)" "404"
check "interface through its name" "$(R nas.strata.test https://nas.strata.test/api/setup)" '"needed":false'
if [ -n "$KUMA" ]; then
  check "WebSocket upgrade passed through" "$(R kuma.strata.test --http1.1 -o /dev/null -w '%{http_code}' -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' 'https://kuma.strata.test/socket.io/?EIO=4&transport=websocket')" "101"
fi
# Local network only: a visitor coming through a local tunnel (cloudflared
# passes its address in Cf-Connecting-IP) is refused.
c PUT /api/remote/domains/$DOM -d "$(echo "$APPDOM" | py 'd["lanOnly"]=True; print(json.dumps(d))')" >/dev/null
check "LAN-only: local client served" "$(R kuma.strata.test -L -o /dev/null -w '%{http_code}' https://kuma.strata.test/)" "200"
check "LAN-only: Internet visitor refused" "$(R kuma.strata.test -o /dev/null -w '%{http_code}' -H 'Cf-Connecting-IP: 203.0.113.5' https://kuma.strata.test/)" "403"
c PUT /api/remote/domains/$DOM -d "$APPDOM" >/dev/null
# Address blocking applies to proxied visitors.
c PUT /api/guard -d '{"enabled":true,"maxFailures":5,"windowMin":10,"blockMin":5,"neverLan":true,"allow":["10.0.2.2/32"],"geo":{"enabled":false}}' >/dev/null
c POST /api/guard/block -d '{"ip":"203.0.113.6","minutes":5}' >/dev/null
sleep 6
check "blocked visitor refused" "$(R kuma.strata.test -o /dev/null -w '%{http_code}' -H 'Cf-Connecting-IP: 203.0.113.6' https://kuma.strata.test/)" "403"
check "other visitor served" "$(R kuma.strata.test -L -o /dev/null -w '%{http_code}' -H 'Cf-Connecting-IP: 203.0.113.7' https://kuma.strata.test/)" "200"
c POST /api/guard/unblock -d '{"ip":"203.0.113.6"}' >/dev/null
c PUT /api/guard -d '{"enabled":false,"maxFailures":5,"windowMin":10,"blockMin":60,"neverLan":true,"geo":{"enabled":false}}' >/dev/null
check "no certificate request for a local name" "$(c POST /api/remote/domains/$DOM/certificate)" "public names"
c DELETE /api/remote/domains/$DOM >/dev/null; c DELETE /api/remote/domains/$UIDOM >/dev/null
check "proxy disabled" "$(c PUT /api/remote/domains/settings -d '{"enabled":false}')" '"ok":true'
check "ports 80 and 443 released" "$(vm "sudo ss -Hltn 'sport = :80 or sport = :443' | wc -l")" "0"
vm "sudo systemctl stop e2e-web 2>/dev/null; sudo rm -rf /tmp/e2e-web"

# Tailscale: installed from its repository, not signed in.
check "Tailscale installed" "$(job "$(c POST /api/system/components/tailscale/install)")" "success"
check "Tailscale repository signed" "$(vm "cat /etc/apt/sources.list.d/tailscale.list")" "signed-by=/usr/share/keyrings/tailscale-archive-keyring.gpg"
for _ in $(seq 1 15); do c GET /api/remote/tailscale | grep -q NeedsLogin && break; sleep 2; done
check "Tailscale: not signed in" "$(c GET /api/remote/tailscale | py 'print(d["installed"], d["status"]["loggedIn"], d["status"]["state"])')" "True False NeedsLogin"
check "Tailscale: bad auth key refused" "$(c POST /api/remote/tailscale/login -d '{"authKey":"nope; rm -rf /"}')" "tskey-"
check "Tailscale: options saved, SSH off" "$(c PUT /api/remote/tailscale -d '{"advertiseLan":true,"routes":"10.0.2.0/24","acceptRoutes":false,"exitNode":false,"ssh":false}'; c GET /api/remote/tailscale | py 'print(d["config"])')" "'routes': '10.0.2.0/24'"
check "Tailscale: sign-in address given" "$(c POST /api/remote/tailscale/login -d '{}')" "https://"
c POST /api/remote/tailscale/logout >/dev/null
c PUT /api/remote/tailscale -d '{}' >/dev/null

# Cloudflare Tunnel: installed, not configured; a well-formed fake token runs
# the service (it cannot connect) and stays masked.
check "cloudflared installed" "$(job "$(c POST /api/system/components/tunnel/install)")" "success"
check "tunnel: not configured" "$(c GET /api/remote/tunnel | py 'print(d["status"]["installed"], d["status"]["configured"], d["status"]["connected"])')" "True False False"
check "tunnel: invalid token refused" "$(c PUT /api/remote/tunnel -d '{"enabled":true,"token":"abc"}')" "invalid tunnel token"
TOK=$(python3 -c 'import base64,json; print(base64.b64encode(json.dumps({"a":"0123456789abcdef0123456789abcdef","t":"6f1b3c2a-1234-4abc-9def-0123456789ab","s":"c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0"}).encode()).decode())')
check "tunnel: token accepted" "$(c PUT /api/remote/tunnel -d "{\"enabled\":true,\"token\":\"cloudflared service install $TOK\"}")" '"ok":true'
check "tunnel: token masked, tunnel id shown" "$(c GET /api/remote/tunnel | py 'print(d["config"]["token"], d["status"]["tunnelId"], d["status"]["connected"])')" "******** 6f1b3c2a-1234-4abc-9def-0123456789ab False"
check "tunnel: token readable by root only" "$(vm "sudo stat -c %a /etc/strata/cloudflared.env")" "600"
check "tunnel: service started" "$(vm "systemctl show -p ExecStart strata-cloudflared.service")" "tunnel run"
check "tunnel: disabled" "$(c PUT /api/remote/tunnel -d '{"enabled":false,"token":""}'; vm "systemctl is-enabled strata-cloudflared.service 2>&1; test -e /etc/strata/cloudflared.env && echo env-left || echo env-gone")" "env-gone"
