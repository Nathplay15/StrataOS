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
# WebSocket through the proxy, against a tiny server we control: it answers
# the handshake (101 + Sec-WebSocket-Accept) only when Connection/Upgrade
# reached it, echoes one text frame and reports the X-Forwarded-* it saw.
cat > "$TMP/e2e-ws.py" <<'EOF'
import base64, hashlib, socket, threading
GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
def handle(c):
    try:
        data = b""
        while b"\r\n\r\n" not in data:
            chunk = c.recv(4096)
            if not chunk:
                return
            data += chunk
        head, rest = data.split(b"\r\n\r\n", 1)
        h = {}
        for line in head.decode("latin-1").split("\r\n")[1:]:
            k, _, v = line.partition(":")
            h[k.strip().lower()] = v.strip()
        seen = "X-Seen: for=%s proto=%s host=%s\r\n" % (h.get("x-forwarded-for"), h.get("x-forwarded-proto"), h.get("x-forwarded-host"))
        if "upgrade" not in h.get("connection", "").lower() or h.get("upgrade", "").lower() != "websocket":
            c.sendall(("HTTP/1.1 200 OK\r\nContent-Length: 10\r\nConnection: close\r\n" + seen + "\r\nno-upgrade").encode())
            return
        acc = base64.b64encode(hashlib.sha1((h["sec-websocket-key"] + GUID).encode()).digest()).decode()
        c.sendall(("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acc + "\r\n" + seen + "\r\n").encode())
        # Echo the first (masked, short) client frame back unmasked.
        while len(rest) < 2:
            rest += c.recv(4096)
        n = rest[1] & 0x7F
        while len(rest) < 6 + n:
            rest += c.recv(4096)
        mask, payload = rest[2:6], rest[6:6 + n]
        text = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        c.sendall(bytes([0x81, len(text)]) + text)
    finally:
        c.close()
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 18082))
s.listen(8)
while True:
    conn, _ = s.accept()
    threading.Thread(target=handle, args=(conn,), daemon=True).start()
EOF
./vm/vm.sh ssh 'cat > /tmp/e2e-ws.py' < "$TMP/e2e-ws.py" 2>/dev/null
vm "sudo systemd-run --unit=e2e-ws python3 /tmp/e2e-ws.py >/dev/null 2>&1"
for _ in $(seq 1 10); do vm "ss -Hltn 'sport = :18082'" | grep -q LISTEN && break; sleep 1; done
WSDOM=$(c POST /api/remote/domains -d '{"host":"ws.strata.test","kind":"url","target":"http://127.0.0.1:18082"}' | py 'print(d["id"])')
WSH=(--http1.1 -m 3 -D - -o /dev/null -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==')
WSOUT=$(R ws.strata.test "${WSH[@]}" https://ws.strata.test/socket | tr -d '\r')
check "WebSocket upgrade passed through" "$WSOUT" "HTTP/1.1 101"
check "WebSocket handshake answered by the app" "$WSOUT" "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
check "app sees X-Forwarded-* headers" "$WSOUT" "X-Seen: for=127.0.0.1 proto=https host=ws.strata.test"
# One frame each way through the proxy (a masked "hello" from the client).
cat > "$TMP/e2e-wsc.py" <<'EOF'
import socket, ssl, sys
port = int(sys.argv[1]) if len(sys.argv) > 1 else 443
ctx = ssl.create_default_context()
ctx.check_hostname, ctx.verify_mode = False, ssl.CERT_NONE
ctx.set_alpn_protocols(["http/1.1"])
s = ctx.wrap_socket(socket.create_connection(("127.0.0.1", port), timeout=5), server_hostname="ws.strata.test")
s.sendall(b"GET /socket HTTP/1.1\r\nHost: ws.strata.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
buf = b""
while b"\r\n\r\n" not in buf:
    buf += s.recv(4096)
status = buf.split(b"\r\n", 1)[0].decode()
mask = b"\x01\x02\x03\x04"
s.sendall(bytes([0x81, 0x80 | 5]) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(b"hello")))
rest = buf.split(b"\r\n\r\n", 1)[1]
while len(rest) < 7:
    rest += s.recv(4096)
print(status, "echo:" + rest[2:7].decode())
EOF
./vm/vm.sh ssh 'cat > /tmp/e2e-wsc.py' < "$TMP/e2e-wsc.py" 2>/dev/null
check "WebSocket frames relayed" "$(vm "python3 /tmp/e2e-wsc.py")" "101 Switching Protocols echo:hello"
c DELETE /api/remote/domains/$WSDOM >/dev/null
vm "sudo systemctl stop e2e-ws; rm -f /tmp/e2e-ws.py /tmp/e2e-wsc.py"
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
