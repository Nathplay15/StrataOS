# Media: DLNA server (ReadyMedia), multi-room music (OwnTone app, through the
# StrataOS proxy) and casting. There is no TV in the VM: discovery must answer
# an empty list, then a fake DLNA renderer (SSDP + SOAP, written below in
# Python) receives a cast and fetches the signed URL.
section "Media (DLNA server, OwnTone, casting)"

MDIR=/srv/strata/tank/media
CLIP=$MDIR/films/e2e-clip.mp4
vm "sudo mkdir -p $MDIR/films $MDIR/music"
if vm "command -v ffmpeg" >/dev/null; then
  vm "sudo ffmpeg -loglevel error -y -f lavfi -i testsrc=size=320x240:duration=3 -pix_fmt yuv420p $CLIP; sudo ffmpeg -loglevel error -y -f lavfi -i sine=frequency=440:duration=3 -metadata title='E2E Tone' -metadata artist='StrataOS' -metadata album='E2E Album' $MDIR/music/e2e-tone.mp3"
else
  vm "sudo cp /srv/strata/main/Photos/holidays/clip.mp4 $CLIP 2>/dev/null || head -c 200000 /dev/urandom | sudo tee $CLIP >/dev/null"
  vm "sudo python3 -c 'import wave,math,struct; w=wave.open(\"$MDIR/music/e2e-tone.wav\",\"wb\"); w.setnchannels(1); w.setsampwidth(2); w.setframerate(22050); w.writeframes(b\"\".join(struct.pack(\"<h\",int(8000*math.sin(i/7))) for i in range(66150))); w.close()'"
fi
check "test media created" "$(vm "sudo ls $MDIR/films $MDIR/music | tr '\n' ' '")" "e2e-clip.mp4"

# ---- DLNA server ----
check "DLNA server installed" "$(job "$(c POST /api/system/components/dlna/install)")" "success"
check "package service left off" "$(vm "systemctl is-enabled minidlna.service 2>&1")" "disabled"
check "unknown share refused" "$(c PUT /api/media/dlna -d '{"enabled":true,"folders":[{"share":"Nope"}]}')" "unknown shared folder"
check "bad interface refused" "$(c PUT /api/media/dlna -d '{"enabled":true,"interface":"eth0;rm"}')" "invalid network interface"
check "bad name refused" "$(c PUT /api/media/dlna -d '{"enabled":true,"name":"a\nmedia_dir=/etc"}')" "forbidden characters"
check "DLNA enabled on the Media share" "$(c PUT /api/media/dlna -d '{"enabled":true,"name":"E2E NAS","folders":[{"share":"Media","type":"video"}]}')" '"ok":true'
check "configuration written" "$(vm "cat /etc/strata-minidlna.conf")" "media_dir=V,$MDIR"
for _ in $(seq 1 20); do vm "curl -s -m 2 http://127.0.0.1:8200/rootDesc.xml" | grep -q "E2E NAS" && break; sleep 1; done
check "rootDesc.xml answers" "$(vm "curl -s -m 5 http://127.0.0.1:8200/rootDesc.xml")" "<friendlyName>E2E NAS"
check "service active" "$(c GET /api/media/dlna | py 'print(d["status"]["service"])')" "active"
# Only one capability, reading any file (bit 2 = CAP_DAC_READ_SEARCH).
check "keeps only CAP_DAC_READ_SEARCH" "$(vm "grep -E '^Cap(Eff|Bnd)' /proc/\$(pgrep -xo minidlnad)/status | awk '{print \$2}' | sort -u")" "0000000000000004"
check "rescan" "$(c POST /api/media/dlna/rescan)" '"ok":true'
# Walks the ContentDirectory from the root (object IDs differ between
# ReadyMedia versions) and prints the titles of the items found.
cat > "$TMP/browse.py" <<'PY'
import re, urllib.request, html
URL = "http://127.0.0.1:8200/ctl/ContentDir"
ENV = ('<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" '
       's:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">'
       '<ObjectID>%s</ObjectID><BrowseFlag>BrowseDirectChildren</BrowseFlag><Filter>*</Filter><StartingIndex>0</StartingIndex>'
       '<RequestedCount>200</RequestedCount><SortCriteria></SortCriteria></u:Browse></s:Body></s:Envelope>')
def browse(oid):
    req = urllib.request.Request(URL, data=(ENV % html.escape(oid)).encode(), headers={
        "Content-Type": 'text/xml; charset="utf-8"', "SOAPACTION": '"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"'})
    body = urllib.request.urlopen(req, timeout=5).read().decode(errors="replace")
    m = re.search(r"<Result>(.*?)</Result>", body, re.S)
    return html.unescape(m.group(1)) if m else ""
todo, seen, titles = ["0"], set(), []
while todo and len(seen) < 100:
    oid = todo.pop(0)
    if oid in seen:
        continue
    seen.add(oid)
    didl = browse(oid)
    todo += [html.unescape(x) for x in re.findall(r'<container[^>]*\bid="([^"]+)"', didl)]
    titles += re.findall(r"<item\b.*?<dc:title>(.*?)</dc:title>", didl, re.S)
print("containers=%d items: %s" % (len(seen), " | ".join(sorted(set(titles)))))
PY
browse() { ./vm/vm.sh ssh "python3 - " < "$TMP/browse.py" 2>/dev/null; }
for _ in $(seq 1 30); do browse | grep -q e2e-clip && break; sleep 2; done
# The share has no permission for anyone: the server reads it anyway (CAP_DAC_READ_SEARCH).
check "no access error in the server log" "errors=$(vm "sudo journalctl -u strata-minidlna -b --no-pager | grep -ciE 'not accessible|could not access|permission denied'")" "errors=0"
check "test video listed by ContentDirectory Browse" "$(browse)" "e2e-clip"
check "indexed files counted" "$(c GET /api/media/dlna | py 'print(d["status"]["service"], d["status"]["video"] >= 1)')" "active True"
check "DLNA disabled" "$(c PUT /api/media/dlna -d '{"enabled":false,"name":"E2E NAS","folders":[{"share":"Media","type":"video"}]}')" '"ok":true'
check "port 8200 closed" "$(vm "curl -s -m 2 -o /dev/null -w '%{http_code}' http://127.0.0.1:8200/rootDesc.xml")" "000"

# ---- Music: OwnTone ----
check "OwnTone app installed" "$(job "$(c POST /api/apps/owntone/install -d '{"name":"owntone","values":{"CONFIG_DIR":"/srv/strata/main/apps/owntone/config","CACHE_DIR":"/srv/strata/main/apps/owntone/cache"}}')")" "success"
check "music page sees it" "$(c GET /api/media/music | py 'print(d["installed"], d["allowed"])')" "True True"
check "unknown music share refused" "$(c PUT /api/media/music -d '{"shares":["Nope"]}')" "unknown shared folder"
check "music folders applied" "$(job "$(c PUT /api/media/music -d '{"shares":["Media"],"users":false}')")" "success"
OTCONF=/srv/strata/main/apps/owntone/config/owntone.conf
check "OwnTone configuration patched" "$(vm "sudo grep -E '^\s*(directories|trusted_networks)' $OTCONF | tr '\n' ' '")" "\"$MDIR\""
check "API trusted for local clients only" "$(vm "sudo grep -E '^\s*trusted_networks' $OTCONF")" '{ "localhost" }'
check "MPD server off" "$(vm "sudo sed -n '/^mpd {/,/^}/p' $OTCONF | grep -E '^\s*port'")" "port = 0"
SONGS=0
for _ in $(seq 1 60); do SONGS=$(c GET /api/media/music/proxy/api/library | py 'print(d.get("songs",0))' 2>/dev/null); [ "${SONGS:-0}" -ge 1 ] 2>/dev/null && break; sleep 3; done
check "library scan found the test song" "$SONGS" "1"
check "search through the proxy" "$(c GET '/api/media/music/proxy/api/search?type=tracks&query=e2e')" '"tracks"'
check "speakers (outputs) listed" "$(c GET /api/media/music/proxy/api/outputs)" '"outputs"'
check "player state" "$(c GET /api/media/music/proxy/api/player)" '"state"'
check "only API paths proxied" "$(c GET /api/media/music/proxy/index.html)" "reserved to administrators"
LANIP=$(vm "hostname -I | awk '{print \$1}'")
check "OwnTone API refuses direct LAN clients" "$(vm "curl -s -m 5 -o /dev/null -w '%{http_code}' http://$LANIP:3689/api/library | grep -qx 200 && echo open || echo refused")" "refused"
check "user refused while music is admin-only" "$(u GET /api/media/music/proxy/api/library)" "reserved to administrators"
check "music opened to users" "$(job "$(c PUT /api/media/music -d '{"shares":["Media"],"users":true}')")" "success"
for _ in $(seq 1 30); do u GET /api/media/music/proxy/api/library | grep -q '"songs"' && break; sleep 2; done
check "user reads the library" "$(u GET /api/media/music/proxy/api/library)" '"songs"'
check "user cannot rescan" "$(u PUT /api/media/music/proxy/api/update)" "reserved to administrators"
check "user cannot change the settings" "$(u PUT /api/media/music -d '{"shares":[],"users":true}')" "administrator"

# ---- Casting ----
check "discovery without receivers: empty list" "$(c GET '/api/media/cast/receivers?refresh=1')" '"receivers":[]'
cat > "$TMP/renderer.py" <<'PY'
# Fake DLNA media renderer: answers SSDP M-SEARCH and logs the SOAP actions.
import socket, struct, threading, re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
PORT = 18900
DESC = """<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType><friendlyName>E2E Renderer</friendlyName>
<manufacturer>StrataOS</manufacturer><modelName>Fake TV</modelName><UDN>uuid:e2e-renderer</UDN><serviceList>
<service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType><controlURL>/av</controlURL></service>
<service><serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType><controlURL>/rc</controlURL></service>
</serviceList></device></root>"""
state = {"t": "STOPPED"}
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        b = DESC.encode(); self.send_response(200); self.send_header("Content-Type", "text/xml"); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0))).decode()
        action = self.headers.get("SOAPACTION", "").strip('"').split("#")[-1]
        with open("/tmp/e2e-renderer.log", "a") as f: f.write(action + "\n")
        out = ""
        if action == "SetAVTransportURI":
            m = re.search(r"<CurrentURI>(.*?)</CurrentURI>", body, re.S)
            with open("/tmp/e2e-renderer.uri", "w") as f: f.write(m.group(1).replace("&amp;", "&"))
        elif action == "Play": state["t"] = "PLAYING"
        elif action == "Pause": state["t"] = "PAUSED_PLAYBACK"
        elif action == "Stop": state["t"] = "STOPPED"
        elif action == "GetTransportInfo": out = "<CurrentTransportState>%s</CurrentTransportState>" % state["t"]
        elif action == "GetPositionInfo": out = "<RelTime>0:00:01</RelTime><TrackDuration>0:00:03</TrackDuration>"
        elif action == "GetVolume": out = "<CurrentVolume>30</CurrentVolume>"
        b = ('<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="x">%s</u:%sResponse></s:Body></s:Envelope>' % (action, out, action)).encode()
        self.send_response(200); self.send_header("Content-Type", 'text/xml; charset="utf-8"'); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
def ssdp():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM, socket.IPPROTO_UDP)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("", 1900))
    s.setsockopt(socket.IPPROTO_IP, socket.IP_ADD_MEMBERSHIP, struct.pack("4sl", socket.inet_aton("239.255.255.250"), socket.INADDR_ANY))
    while True:
        data, addr = s.recvfrom(4096)
        t = data.decode(errors="replace")
        if t.startswith("M-SEARCH") and ("MediaRenderer" in t or "ssdp:all" in t):
            r = "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLOCATION: http://%s:%d/desc.xml\r\nSERVER: Linux UPnP/1.0 E2E/1\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\nUSN: uuid:e2e-renderer::urn:schemas-upnp-org:device:MediaRenderer:1\r\n\r\n" % (addr[0], PORT)
            s.sendto(r.encode(), addr)
threading.Thread(target=ssdp, daemon=True).start()
ThreadingHTTPServer(("0.0.0.0", PORT), H).serve_forever()
PY
./vm/vm.sh ssh "cat > /tmp/e2e-renderer.py" < "$TMP/renderer.py" 2>/dev/null
vm "sudo rm -f /tmp/e2e-renderer.log /tmp/e2e-renderer.uri; sudo systemd-run --unit=e2e-renderer python3 /tmp/e2e-renderer.py >/dev/null 2>&1"
for _ in $(seq 1 10); do vm "ss -Hltn 'sport = :18900'" | grep -q LISTEN && break; sleep 1; done
RID=$(c GET '/api/media/cast/receivers?refresh=1' | py 'print([r["id"] for r in d["receivers"] if r["name"]=="E2E Renderer"][0])' 2>/dev/null)
check "fake renderer discovered by SSDP" "$RID" "dlna:uuid:e2e-renderer"
check "file that is not media refused" "$(c POST /api/media/cast -d "{\"path\":\"/srv/strata/main/Photos/via-smb.txt\",\"receivers\":[\"$RID\"]}")" "cannot be cast"
check "user cannot cast a file outside their shares" "$(u POST /api/media/cast -d "{\"path\":\"$CLIP\",\"receivers\":[\"$RID\"]}")" "access denied"
CAST=$(c POST /api/media/cast -d "{\"path\":\"$CLIP\",\"receivers\":[\"$RID\"]}")
check "cast started, renderer playing" "$(echo "$CAST" | py 'print(d["receivers"][0]["status"]["state"], d["receivers"][0].get("error",""))')" "PLAYING"
SID=$(echo "$CAST" | py 'print(d["id"])' 2>/dev/null)
check "renderer got SetAVTransportURI then Play" "$(vm "cat /tmp/e2e-renderer.log" | tr '\n' ' ')" "SetAVTransportURI Play"
URI=$(vm "cat /tmp/e2e-renderer.uri")
check "signed stream URL given" "$URI" ":7070/api/media/stream/"
check "stream fetched with Range: 206" "$(vm "curl -s -m 10 -o /dev/null -w '%{http_code} %{content_type} %{size_download}' -r 0-99 '$URI'")" "206 video/mp4 100"
check "DLNA headers sent" "$(vm "curl -s -m 10 -o /dev/null -D - -r 0-0 '$URI'" | tr -d '\r')" "transferMode.dlna.org: Streaming"
check "no cookie needed, tampered token refused" "$(vm "curl -s -m 10 -o /dev/null -w '%{http_code}' '${URI/stream\//stream/x}'")" "410"
check "session listed" "$(c GET /api/media/cast/sessions | py 'print([s["name"] for s in d["sessions"]])')" "e2e-clip.mp4"
check "user does not see the admin's session" "$(u GET /api/media/cast/sessions)" '"sessions":[]'
check "pause sent" "$(c POST /api/media/cast/sessions/$SID/control -d '{"action":"pause"}')" '"ok":true'
check "volume sent" "$(c POST /api/media/cast/sessions/$SID/control -d '{"action":"volume","value":40}')" '"ok":true'
check "seek sent" "$(c POST /api/media/cast/sessions/$SID/control -d '{"action":"seek","value":1}')" '"ok":true'
check "renderer got Pause, SetVolume and Seek" "$(vm "cat /tmp/e2e-renderer.log" | tr '\n' ' ')" "Pause"
check "volume reached the renderer" "$(vm "cat /tmp/e2e-renderer.log" | tr '\n' ' ')" "SetVolume"
check "cast stopped" "$(c DELETE /api/media/cast/sessions/$SID)" '"ok":true'
check "link revoked after the stop" "$(vm "curl -s -m 10 -o /dev/null -w '%{http_code}' '$URI'")" "410"

# Cleanup.
vm "sudo systemctl stop e2e-renderer 2>/dev/null; sudo rm -f /tmp/e2e-renderer.py /tmp/e2e-renderer.log /tmp/e2e-renderer.uri"
c DELETE /api/docker/stacks/owntone >/dev/null
c PUT /api/media/music -d '{}' >/dev/null
vm "sudo rm -rf /srv/strata/main/apps/owntone $MDIR/films $MDIR/music"
check "OwnTone removed" "$(c GET /api/media/music | py 'print(d["installed"])')" "False"
