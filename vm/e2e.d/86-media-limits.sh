# shellcheck shell=bash
# Media and surveillance follow-ups: the DLNA and music folders follow a
# renamed, moved or deleted share; a cast session survives a restart of the
# daemon (fake DLNA renderer, as in 85-media.sh) and is cleaned when the
# receiver played something else meanwhile; an H.265 camera (mediamtx fed by
# ffmpeg/libx265) is recorded, shown live and played back as fragmented MP4
# with H.264 video.
section "Media follow-ups (shares followed, casts across restarts, H.265 playback)"

MDIR=/srv/strata/tank/media
NEWDIR=/srv/strata/tank/e2e-media-moved
TMPDIR_SH=/srv/strata/tank/e2e-dlnatmp
relogin() { wait_api; c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null; }
# PUT a share with a new name and path, from its current settings.
share_move() { # old new path
  c PUT "/api/shares/$1" -d "$(c GET /api/shares | py "import json; s=[x for x in d if x['name']=='$1'][0]; s['name']='$2'; s['path']='$3'; print(json.dumps(s))")"
}
check "recorder component (ffmpeg) installed" "$(job "$(c POST /api/system/components/nvr/install)")" "success"
check "DLNA server installed" "$(job "$(c POST /api/system/components/dlna/install)")" "success"

# ---- DLNA and music folders follow the shares ----
check "temporary share" "$(c POST /api/shares -d "{\"name\":\"E2EDlnaTmp\",\"path\":\"$TMPDIR_SH\",\"smb\":true,\"browseable\":true}")" '"name":"E2EDlnaTmp"'
check "DLNA on Media and the temporary share" "$(c PUT /api/media/dlna -d '{"enabled":true,"name":"E2E NAS","folders":[{"share":"Media","type":"video"},{"share":"E2EDlnaTmp"}]}')" '"ok":true'
check "both folders published" "$(vm "grep media_dir /etc/strata-minidlna.conf | tr '\n' ' '")" "media_dir=$TMPDIR_SH"
check "OwnTone app installed" "$(job "$(c POST /api/apps/owntone/install -d '{"name":"owntone","values":{"CONFIG_DIR":"/srv/strata/main/apps/owntone/config","CACHE_DIR":"/srv/strata/main/apps/owntone/cache"}}')")" "success"
check "music folders applied" "$(job "$(c PUT /api/media/music -d '{"shares":["Media"],"users":false}')")" "success"
OTCONF=/srv/strata/main/apps/owntone/config/owntone.conf
check "OwnTone reads the Media share" "$(vm "sudo grep -E '^\s*directories' $OTCONF")" "\"$MDIR\""

check "Media share renamed and moved" "$(share_move Media E2EMedia "$NEWDIR")" '"name":"E2EMedia"'
check "DLNA settings follow the new name" "$(c GET /api/media/dlna | py 'print([f["share"] for f in d["config"]["folders"]])')" "['E2EMedia', 'E2EDlnaTmp']"
check "music settings follow the new name" "$(c GET /api/media/music | py 'print(d["config"]["shares"])')" "['E2EMedia']"
check "DLNA configuration has the new path" "$(vm "cat /etc/strata-minidlna.conf")" "media_dir=V,$NEWDIR"
check "DLNA configuration lost the old path" "$(vm "grep -c 'media_dir=V,$MDIR\$' /etc/strata-minidlna.conf || true")" "0"
for _ in $(seq 1 30); do vm "systemctl is-active strata-minidlna" | grep -qx active && break; sleep 1; done
check "DLNA server restarted" "$(vm "systemctl is-active strata-minidlna")" "active"
for _ in $(seq 1 60); do vm "sudo grep -E '^\s*directories' $OTCONF" | grep -q "$NEWDIR" && break; sleep 2; done
check "OwnTone directories updated" "$(vm "sudo grep -E '^\s*directories' $OTCONF")" "\"$NEWDIR\""

EV0=$(c GET /api/system/events | grep -o 'E2EDlnaTmp was deleted: the DLNA server no longer publishes it' | wc -l)
check "share used by DLNA deleted" "$(c DELETE /api/shares/E2EDlnaTmp)" '"ok":true'
check "removed from the DLNA settings" "$(c GET /api/media/dlna | py 'print([f["share"] for f in d["config"]["folders"]])')" "['E2EMedia']"
check "removed from the DLNA configuration" "$(vm "grep -c '$TMPDIR_SH' /etc/strata-minidlna.conf || true")" "0"
check "event for the removed folder" "$(c GET /api/system/events | grep -o 'E2EDlnaTmp was deleted: the DLNA server no longer publishes it' | wc -l)" "$((EV0 + 1))"

check "Media share back" "$(share_move E2EMedia Media "$MDIR")" '"name":"Media"'
check "DLNA back on Media" "$(vm "cat /etc/strata-minidlna.conf")" "media_dir=V,$MDIR"
for _ in $(seq 1 60); do vm "sudo grep -E '^\s*directories' $OTCONF" | grep -q "\"$MDIR\"" && break; sleep 2; done
check "OwnTone back on Media" "$(vm "sudo grep -E '^\s*directories' $OTCONF")" "\"$MDIR\""
for _ in $(seq 1 60); do c GET /api/jobs | grep -q '"status":"running"' || break; sleep 2; done
c PUT /api/media/dlna -d '{"enabled":false,"name":"E2E NAS","folders":[{"share":"Media","type":"video"}]}' >/dev/null
c DELETE /api/docker/stacks/owntone >/dev/null
c PUT /api/media/music -d '{}' >/dev/null
vm "sudo rm -rf /srv/strata/main/apps/owntone $NEWDIR $TMPDIR_SH"

# ---- A cast session survives a restart of the daemon ----
CLIP=$MDIR/e2e-cast.mp4
vm "sudo ffmpeg -loglevel error -y -f lavfi -i testsrc=size=320x240:duration=20 -pix_fmt yuv420p $CLIP"
cat > "$TMP/renderer86.py" <<'PY'
# Fake DLNA renderer (85-media.sh) that also reports the URI it plays
# (TrackURI), read from /tmp/e2e-renderer.uri so the test can change it.
import socket, struct, threading, re, html
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
PORT = 18900
DESC = """<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType><friendlyName>E2E Renderer</friendlyName>
<manufacturer>StrataOS</manufacturer><modelName>Fake TV</modelName><UDN>uuid:e2e-renderer</UDN><serviceList>
<service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType><controlURL>/av</controlURL></service>
<service><serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType><controlURL>/rc</controlURL></service>
</serviceList></device></root>"""
state = {"t": "STOPPED"}
def uri():
    try:
        return open("/tmp/e2e-renderer.uri").read().strip()
    except OSError:
        return ""
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
        elif action == "GetPositionInfo": out = "<RelTime>0:00:01</RelTime><TrackDuration>0:00:20</TrackDuration><TrackURI>%s</TrackURI>" % html.escape(uri())
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
./vm/vm.sh ssh "cat > /tmp/e2e-renderer86.py" < "$TMP/renderer86.py" 2>/dev/null
vm "sudo systemctl stop e2e-renderer 2>/dev/null; sudo systemctl reset-failed e2e-renderer 2>/dev/null; sudo rm -f /tmp/e2e-renderer.log /tmp/e2e-renderer.uri; sudo systemd-run --unit=e2e-renderer python3 /tmp/e2e-renderer86.py >/dev/null 2>&1"
for _ in $(seq 1 10); do vm "ss -Hltn 'sport = :18900'" | grep -q LISTEN && break; sleep 1; done
RID=$(c GET '/api/media/cast/receivers?refresh=1' | py 'print([r["id"] for r in d["receivers"] if r["name"]=="E2E Renderer"][0])' 2>/dev/null)
check "fake renderer discovered" "$RID" "dlna:uuid:e2e-renderer"
CAST=$(c POST /api/media/cast -d "{\"path\":\"$CLIP\",\"receivers\":[\"$RID\"]}")
SID=$(echo "$CAST" | py 'print(d["id"])' 2>/dev/null)
check "cast started" "$(echo "$CAST" | py 'print(d["receivers"][0]["status"]["state"])')" "PLAYING"
URI=$(vm "cat /tmp/e2e-renderer.uri")
check "stream answers before the restart" "$(vm "curl -s -m 10 -o /dev/null -w '%{http_code}' -r 0-99 '$URI'")" "206"
check "sessions saved for a restart" "$(vm "sudo cat /var/lib/strata/cast-sessions.json")" "\"$SID\""
check "media key kept in a file" "$(vm "sudo stat -c '%a %s' /var/lib/strata/media.key")" "600 32"

vm "sudo systemctl restart strata"
relogin
for _ in $(seq 1 30); do c GET /api/media/cast/sessions | grep -q "\"$SID\"" && break; sleep 1; done
check "session listed after the restart" "$(c GET /api/media/cast/sessions | py 'print([(s["id"], s["name"], s["receivers"][0]["status"]["state"]) for s in d["sessions"]])')" "('$SID', 'e2e-cast.mp4', 'PLAYING')"
check "stream URL still answers 206" "$(vm "curl -s -m 10 -o /dev/null -w '%{http_code} %{content_type}' -r 0-99 '$URI'")" "206 video/mp4"
vm "sudo truncate -s 0 /tmp/e2e-renderer.log"
check "pause after the restart" "$(c POST /api/media/cast/sessions/$SID/control -d '{"action":"pause"}')" '"ok":true'
check "stop after the restart" "$(c POST /api/media/cast/sessions/$SID/control -d '{"action":"stop"}')" '"ok":true'
check "renderer got Pause and Stop" "$(vm "cat /tmp/e2e-renderer.log" | grep -E '^(Pause|Stop)$' | tr '\n' ' ')" "Pause Stop"

# The TV played something else while the daemon was down: the session ends.
vm "sudo systemctl stop strata; echo http://10.0.2.15:9999/other.mp4 | sudo tee /tmp/e2e-renderer.uri >/dev/null; sudo systemctl start strata"
relogin
for _ in $(seq 1 30); do c GET /api/media/cast/sessions | grep -q "\"$SID\"" || break; sleep 1; done
sleep 2
check "session ended while down is removed" "$(c GET /api/media/cast/sessions)" '"sessions":[]'
check "its stream URL is revoked" "$(vm "curl -s -m 10 -o /dev/null -w '%{http_code}' '$URI'")" "410"
check "saved sessions cleaned" "$(vm "sudo cat /var/lib/strata/cast-sessions.json")" "[]"
vm "sudo systemctl stop e2e-renderer 2>/dev/null; sudo systemctl reset-failed e2e-renderer 2>/dev/null; sudo rm -f /tmp/e2e-renderer86.py /tmp/e2e-renderer.log /tmp/e2e-renderer.uri $CLIP"

# ---- H.265 camera: recording, live view, playback converted to H.264 ----
check "ffmpeg has libx265" "$(vm "ffmpeg -hide_banner -encoders 2>/dev/null | grep -c ' libx265 '")" "1"
NV=/srv/strata/main/e2e-nvr265
vm "cat > /tmp/e2e-mtx265.yml <<'EOF'
rtspAddress: :8556
rtpAddress: :8010
rtcpAddress: :8011
rtmp: no
hls: no
webrtc: no
srt: no
api: no
metrics: no
pprof: no
playback: no
authInternalUsers:
  - user: pub
    pass: pubpass
    ips: []
    permissions:
      - action: publish
  - user: cam
    pass: campass
    ips: []
    permissions:
      - action: read
paths:
  all_others:
EOF
sudo docker rm -f e2e-mtx265 >/dev/null 2>&1; sudo docker run -d --name e2e-mtx265 --network host -v /tmp/e2e-mtx265.yml:/mediamtx.yml:ro bluenviron/mediamtx:latest >/dev/null"
for _ in $(seq 1 60); do vm "timeout 2 bash -c '</dev/tcp/127.0.0.1/8556' 2>/dev/null" && break; sleep 2; done
vm "sudo systemctl reset-failed e2e-hevc-pub 2>/dev/null; sudo systemd-run --unit=e2e-hevc-pub ffmpeg -nostdin -loglevel error -re -f lavfi -i testsrc=size=640x360:rate=10 -c:v libx265 -preset ultrafast -x265-params keyint=10:log-level=error -pix_fmt yuv420p -f rtsp -rtsp_transport tcp rtsp://pub:pubpass@127.0.0.1:8556/hevc >/dev/null 2>&1"
sleep 4
check "H.265 test camera publishing" "$(vm "sudo systemctl is-active e2e-hevc-pub")" "active"
check "recordings folder set" "$(c PUT /api/nvr/settings -d '{"folder":"'$NV'","alertMinutes":1,"segmentSeconds":10}')" '"ok":true'
CAM=$(c POST /api/nvr/cameras -d '{"name":"E2E hevc","url":"rtsp://127.0.0.1:8556/hevc","username":"cam","password":"campass","keepDays":1}')
CID=$(echo "$CAM" | py 'print(d["id"])')
check "H.265 camera added" "$CAM" '"name":"E2E hevc"'
for _ in $(seq 1 30); do [ "$(vm "ls $NV/$CID 2>/dev/null | grep -c mp4")" -ge 2 ] 2>/dev/null && break; sleep 2; done
sleep 3
REC=$(c GET "/api/nvr/cameras/$CID/recordings")
SEG=$(echo "$REC" | py 'print([s["name"] for s in d["segments"] if not s.get("open")][0])')
check "closed H.265 segment" "$SEG" ".mp4"
check "segment recorded as H.265 (stream copy)" "$(vm "sudo ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 $NV/$CID/$SEG")" "hevc"
magic() { od -An -tx1 -N2 | tr -d ' \n'; }
check "live view of the H.265 camera is a JPEG" "$(curl -s -b "$J" "$B/api/nvr/cameras/$CID/live.jpg" | magic)" "ffd8"
check "segment info: not playable as is" "$(c GET "/api/nvr/cameras/$CID/recordings/$SEG/info" | py 'print(d["video"], d["direct"])')" "hevc False"
check "playback endpoint answers MP4" "$(curl -s -b "$J" -o "$TMP/play265.mp4" -w '%{http_code} %{content_type}' "$B/api/nvr/cameras/$CID/recordings/$SEG/play")" "200 video/mp4"
check "fragmented MP4 (moof boxes)" "$(grep -a -c moof "$TMP/play265.mp4" | awk '{print ($1>0)?"fragmented":"not fragmented"}')" "fragmented"
./vm/vm.sh ssh "cat > /tmp/e2e-play265.mp4" < "$TMP/play265.mp4" 2>/dev/null
check "converted to H.264" "$(vm "ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 /tmp/e2e-play265.mp4")" "h264"
check "playback from ?start= seconds" "$(curl -s -b "$J" -o "$TMP/play265b.mp4" -w '%{http_code}' "$B/api/nvr/cameras/$CID/recordings/$SEG/play?start=4"; grep -a -c moof "$TMP/play265b.mp4" | awk '{print ($1>0)?" fragmented":" empty"}')" "200 fragmented"
check "invalid start refused" "$(c GET "/api/nvr/cameras/$CID/recordings/$SEG/play?start=-1")" "invalid start"
check "user cannot play recordings" "$(u GET "/api/nvr/cameras/$CID/recordings/$SEG/info")" "administrator rights required"
check "Frigate list refused for a built-in camera" "$(c GET "/api/nvr/cameras/$CID/frigate/events")" "not recorded by Frigate"

check "H.265 camera deleted with its recordings" "$(c DELETE "/api/nvr/cameras/$CID?recordings=1")" '"ok":true'
vm "sudo systemctl stop e2e-hevc-pub 2>/dev/null; sudo systemctl reset-failed e2e-hevc-pub 2>/dev/null; sudo docker rm -f e2e-mtx265 >/dev/null; sudo rm -rf $NV /tmp/e2e-mtx265.yml /tmp/e2e-play265.mp4"
