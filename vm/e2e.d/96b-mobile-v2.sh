# shellcheck shell=bash
# Server side of the Android app 0.8.0: the web interface inside the app
# (device token traded for a one-time code, then an ordinary session cookie
# tied to the device and its restrictions) and the live video of a camera
# (fragmented MP4 remuxed from RTSP, one ffmpeg shared by the viewers and
# stopped after the last one). The test camera is the mediamtx container of
# the Surveillance section (its image is already pulled).
section "Mobile app 0.8.0 (web session, live video)"
mb() { local t=$1 m=$2 p=$3; shift 3; curl -s -X "$m" -H "Authorization: Bearer $t" -H 'Content-Type: application/json' "$B$p" "$@"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
WJ=$(mktemp)

# ---- Web interface session ----
PC=$(c POST /api/me/devices/pairing | py 'print(d["code"])')
TW=$(curl -s -X POST "$B/api/mobile/pair" -d "{\"pairingCode\":\"$PC\",\"deviceName\":\"Web e2e\",\"model\":\"Pixel 9\"}" | py 'print(d.get("token",""))')
check "admin phone paired" "$TW" "stdev_"
check "app announces the new features" "$(mb "$TW" GET /api/mobile/info | py 'print("websession" in d["features"], "livevideo" in d["features"])')" "True True"
WS=$(mb "$TW" POST /api/mobile/websession)
WP=$(echo "$WS" | py 'print(d["path"])')
check "one-time web code for the token" "$WP" "/api/mobile/websession/"
check "a browser session cannot ask for one" "$(c POST /api/mobile/websession)" "only the mobile app"
check "code exchanged for a session cookie" "$(curl -s -c "$WJ" -o /dev/null -w '%{http_code} %{redirect_url}' "$B$WP")" "303 $B/"
check "session cookie is HttpOnly" "$(grep -c '#HttpOnly_.*strata_session' "$WJ")" "1"
check "code single-use" "$(code "$B$WP")" "401"
check "web session acts as the device's user" "$(curl -s -b "$WJ" "$B/api/me" | py 'print(d["username"], d["role"])')" "$ADMIN_USER admin"
check "web interface served" "$(curl -s -b "$WJ" "$B/" | grep -c '<div id="root">')" "1"
check "administration works from it" "$(curl -s -b "$WJ" "$B/api/system/services" | py 'print(len(d) > 0)')" "True"
check "no password change from it" "$(curl -s -b "$WJ" -X POST -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B/api/me/password" -d '{"current":"x","password":"y"}')" "in a browser"
check "no device management from it" "$(code -b "$WJ" "$B/api/me/devices")" "403"
check "still CSRF-protected" "$(code -b "$WJ" -X POST "$B/api/system/services/ssh.service/restart")" "403"
check "opening logged" "$(c GET /api/system/events | grep -c 'Web interface opened from the mobile app (Web e2e)')" "1"
check "bad code refused" "$(code "$B/api/mobile/websession/$(printf 'a%.0s' $(seq 1 64))")" "401"
# Opening it again replaces the previous session of the device.
WJ2=$(mktemp)
curl -s -c "$WJ2" -o /dev/null "$B$(mb "$TW" POST /api/mobile/websession | py 'print(d["path"])')"
check "previous web session replaced" "$(code -b "$WJ" "$B/api/me")" "401"
check "new web session works" "$(code -b "$WJ2" "$B/api/me")" "200"
WID=$(c GET /api/me/devices | py 'print([x["id"] for x in d if x["name"]=="Web e2e"][0])')
c DELETE "/api/me/devices/$WID" >/dev/null
check "revoking the device ends its web session" "$(code -b "$WJ2" "$B/api/me")" "401"
rm -f "$WJ" "$WJ2"

# ---- Live video ----
NV=/srv/strata/main/e2e-live
CAMPASS='p@ss#1;x=y&z'
vm "cat > /tmp/e2e-mtx.yml <<'EOF'
rtmp: no
hls: no
webrtc: no
srt: no
authInternalUsers:
  - user: pub
    pass: pubpass
    ips: []
    permissions:
      - action: publish
  - user: cam
    pass: '$CAMPASS'
    ips: []
    permissions:
      - action: read
paths:
  all_others:
EOF
sudo docker rm -f e2e-mtx >/dev/null 2>&1; sudo docker run -d --name e2e-mtx --network host -v /tmp/e2e-mtx.yml:/mediamtx.yml:ro bluenviron/mediamtx:latest >/dev/null"
for _ in $(seq 1 60); do vm "timeout 2 bash -c '</dev/tcp/127.0.0.1/8554' 2>/dev/null" && break; sleep 2; done
vm "sudo systemctl reset-failed e2e-live-pub 2>/dev/null; sudo systemd-run --unit=e2e-live-pub ffmpeg -nostdin -loglevel error -re -f lavfi -i testsrc=size=640x360:rate=10 -c:v libx264 -profile:v baseline -pix_fmt yuv420p -g 20 -f rtsp -rtsp_transport tcp rtsp://pub:pubpass@127.0.0.1:8554/cam >/dev/null 2>&1"
sleep 4
OLDF=$(c GET /api/nvr | py 'print(d["folder"])')
c PUT /api/nvr/settings -d '{"folder":"'$NV'","alertMinutes":5}' >/dev/null
CAM=$(c POST /api/nvr/cameras -d "$(python3 -c 'import json,sys; print(json.dumps({"name":"E2E live","url":"rtsp://127.0.0.1:8554/cam","username":"cam","password":sys.argv[1],"keepDays":1}))' "$CAMPASS")")
CID=$(echo "$CAM" | py 'print(d["id"])')
check "camera for the live video added" "$CAM" '"name":"E2E live"' 
# boxes <file>: the top-level MP4 boxes, and whether a fragment starts on a key frame.
boxes() { python3 -I - "$1" <<'PYEOF'
import struct, sys
b = open(sys.argv[1], "rb").read()
i, out = 0, []
while i + 8 <= len(b):
    n, t = struct.unpack(">I4s", b[i:i + 8])
    if n < 8 or i + n > len(b):
        break
    out.append(t.decode())
    i += n
print(" ".join(dict.fromkeys(out)), out.count("moof"))
PYEOF
}
curl -s -b "$J" --max-time 6 "$B/api/nvr/cameras/$CID/live.mp4" -o "$TMP/live1.mp4" &
L1=$!
sleep 1
curl -s -b "$J" --max-time 4 "$B/api/nvr/cameras/$CID/live.mp4" -o "$TMP/live2.mp4" &
L2=$!
sleep 2
check "one ffmpeg shared by two viewers" "$(vm "pgrep -fc '[/]run/strata/nvr/$CID/stream-main' || true")" "1"
wait "$L1" "$L2" 2>/dev/null
check "live video is fragmented MP4" "$(boxes "$TMP/live1.mp4")" "ftyp moov moof mdat"
check "live video keeps coming (several fragments)" "$(boxes "$TMP/live1.mp4" | awk '{print ($NF>=4)?"yes":"no: "$NF}')" "yes"
check "second viewer gets its own header" "$(boxes "$TMP/live2.mp4")" "ftyp moov moof"
check "stream copied (H.264 track, not transcoded)" "$(python3 -I -c 'import sys; print(b"avc1" in open(sys.argv[1], "rb").read(4096))' "$TMP/live1.mp4")" "True"
sleep 8
check "ffmpeg stopped after the last viewer" "$(vm "pgrep -fc '[/]run/strata/nvr/$CID/stream-' || true")" "0"
check "credentials not on the command line" "$(curl -s -b "$J" --max-time 3 "$B/api/nvr/cameras/$CID/live.mp4" -o /dev/null; vm "ps -eo args | grep stream-main | grep -v grep | grep -c 'rtsp://\|w/rd' || true")" "0"
check "user cannot watch" "$(code -b "$J2" "$B/api/nvr/cameras/$CID/live.mp4")" "403"
check "device token can watch" "$(TL=$(curl -s -X POST "$B/api/mobile/pair" -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"deviceName\":\"Live e2e\"}" | py 'print(d["token"])'); curl -s -H "Authorization: Bearer $TL" --max-time 3 "$B/api/nvr/cameras/$CID/live.mp4" -o "$TMP/live3.mp4"; mb "$TL" DELETE /api/mobile/device >/dev/null; boxes "$TMP/live3.mp4")" "ftyp moov"
check "unknown stream refused" "$(c GET "/api/nvr/cameras/$CID/live.mp4?stream=sub")" "no sub-stream"
vm "sudo systemctl stop e2e-live-pub"
sleep 2
check "unreachable camera reported" "$(curl -s -b "$J" --max-time 30 "$B/api/nvr/cameras/$CID/live.mp4" | head -c 300)" "error"

# Clean up.
c DELETE "/api/nvr/cameras/$CID?recordings=1" >/dev/null
[ -n "$OLDF" ] && c PUT /api/nvr/settings -d '{"folder":"'"$OLDF"'","alertMinutes":5}' >/dev/null
vm "sudo systemctl reset-failed e2e-live-pub 2>/dev/null; sudo docker rm -f e2e-mtx >/dev/null; sudo rm -rf $NV /tmp/e2e-mtx.yml"
