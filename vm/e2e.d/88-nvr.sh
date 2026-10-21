# shellcheck shell=bash
# Surveillance: a test RTSP camera (mediamtx in Docker, with a reader password
# full of special characters, fed by ffmpeg with a test pattern) recorded by
# the built-in recorder in 10-second segments: connection test, recording,
# live picture, timeline, Range playback, clip, retention, credentials kept
# out of the API and the process list, offline/back-online alerts, Frigate
# configuration generation, camera removal. E2E_FRIGATE=1 also installs
# Frigate (large image) and checks it starts with the generated configuration.
section "Surveillance (NVR)"
NV=/srv/strata/main/e2e-nvr
CAMPASS='p@ss#1;x=y&z'
check "recorder component installed" "$(job "$(c POST /api/system/components/nvr/install)")" "success"
check "recorder reported installed" "$(c GET /api/nvr | py 'print(d["installed"], len(d["cameras"]))')" "True 0"
check "nothing recorded before setup" "$(vm "pgrep -fc '[f]fmpeg.*ffconcat' || true")" "0"

# Test camera: mediamtx (RTSP on 8554, readers must sign in) and a publisher.
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
publish() { vm "sudo systemctl reset-failed e2e-nvr-pub 2>/dev/null; sudo systemd-run --unit=e2e-nvr-pub ffmpeg -nostdin -loglevel error -re -f lavfi -i testsrc=size=640x360:rate=10 -c:v libx264 -profile:v baseline -pix_fmt yuv420p -g 10 -f rtsp -rtsp_transport tcp rtsp://pub:pubpass@127.0.0.1:8554/cam >/dev/null 2>&1"; }
for _ in $(seq 1 60); do vm "timeout 2 bash -c '</dev/tcp/127.0.0.1/8554' 2>/dev/null" && break; sleep 2; done
publish
sleep 4
check "test camera publishing" "$(vm "sudo systemctl is-active e2e-nvr-pub")" "active"

# Settings: the folder must be on a volume; 10-second segments for the test.
check "folder outside the volumes refused" "$(c PUT /api/nvr/settings -d '{"folder":"/etc/nvr"}')" "on a NAS volume"
check "folder set" "$(c PUT /api/nvr/settings -d '{"folder":"'$NV'","alertMinutes":1,"segmentSeconds":10}')" '"ok":true'
check "local file stream refused" "$(c POST /api/nvr/cameras -d '{"name":"bad","url":"file:///etc/shadow"}')" "must start with rtsp://"
check "quote in the stream path refused" "$(c POST /api/nvr/cameras -d "{\"name\":\"bad\",\"url\":\"rtsp://127.0.0.1:8554/a'b\"}")" "invalid characters"
URL=rtsp://127.0.0.1:8554/cam
CAMJ() { python3 -c 'import json,sys; print(json.dumps({"name":sys.argv[1],"url":sys.argv[2],"subUrl":sys.argv[2],"username":"cam","password":sys.argv[3],"keepDays":1,"engine":sys.argv[4]}))' "$@"; }
magic() { od -An -tx1 -N2 | tr -d ' \n'; }
check "connection test with a wrong password fails" "$(c POST /api/nvr/test -d "$(CAMJ t $URL wrong-pass '')")" "connection failed"
check "connection test returns a JPEG" "$(c POST /api/nvr/test -d "$(CAMJ t $URL "$CAMPASS" '')" | magic)" "ffd8"

CAM=$(c POST /api/nvr/cameras -d "$(CAMJ 'E2E door' $URL "$CAMPASS" '')")
check "camera added" "$CAM" '"name":"E2E door"'
CID=$(echo "$CAM" | py 'print(d["id"])')
check "password masked in the API" "$(c GET /api/nvr | py 'print([c["password"] for c in d["cameras"]])')" "['********']"
check "password not in the API at all" "$(c GET /api/nvr | grep -c 'w/rd')" "0"
camst() { c GET /api/nvr | py "print([(c['status']['state'], c['segments']) for c in d['cameras'] if c['id']=='$CID'][0])"; }
for _ in $(seq 1 30); do camst | grep -q "'recording', [1-9]" && break; sleep 2; done
check "recording within a minute" "$(camst)" "('recording',"
sleep 22
check "segments written (10 s each)" "$(vm "ls $NV/$CID | grep -cE '^[0-9]{8}-[0-9]{6}\.mp4$'" | awk '{print ($1>=2)?"several":"few: "$1}')" "several"
check "stream copied, not transcoded (H.264)" "$(vm "sudo ffprobe -v error -show_entries stream=codec_name -of csv=p=0 \$(ls $NV/$CID/*.mp4 | head -1)")" "h264"
check "credentials not in the process list" "$(vm "ps -eo args | grep ffconcat | grep -v grep | grep -c 'rtsp://\|w/rd\|w%2Frd' || true")" "0"
check "recorder input file root-only" "$(vm "sudo stat -c %a /run/strata/nvr/$CID/main.ffconcat")" "600"
check "credentials not in the logs" "$(vm "sudo journalctl -u strata --since '-10 min' | grep -c 'w/rd\|w%2Frd' || true")" "0"

check "live picture is a JPEG" "$(curl -s -b "$J" "$B/api/nvr/cameras/$CID/live.jpg" | magic)" "ffd8"
sleep 2
check "live picture refreshed" "$(curl -s -b "$J" "$B/api/nvr/cameras/$CID/live.jpg" | magic)" "ffd8"

REC=$(c GET "/api/nvr/cameras/$CID/recordings")
check "timeline lists today's segments" "$(echo "$REC" | py 'print(d["day"] == d["days"][0], len(d["segments"]) >= 2, d["segments"][-1].get("open", False))')" "True True True"
SEG=$(echo "$REC" | py 'print([s["name"] for s in d["segments"] if not s.get("open")][0])')
check "segment served with Range (206)" "$(curl -s -b "$J" -r 0-999 -o /dev/null -w '%{http_code} %{size_download} %{content_type}' "$B/api/nvr/cameras/$CID/recordings/$SEG")" "206 1000 video/mp4"
check "segment download" "$(curl -s -b "$J" -D - -o /dev/null "$B/api/nvr/cameras/$CID/recordings/$SEG?download=1" | grep -i content-disposition)" "attachment"
CLIP=$(curl -s -b "$J" "$B/api/nvr/cameras/$CID/recordings/$SEG/clip?from=0&to=5" -o "$TMP/clip.mp4" -w '%{http_code}')
check "clip export" "$CLIP $(stat -c %s "$TMP/clip.mp4" | awk '{print ($1>1000)?"big enough":"too small"}')" "200 big enough"
check "path traversal refused" "$(curl -s -b "$J" "$B/api/nvr/cameras/$CID/recordings/..%2F..%2Fetc%2Fpasswd")" "not found"
check "user cannot reach the cameras" "$(u GET /api/nvr)" "administrator rights required"
check "user cannot see the live view" "$(curl -s -b "$J2" "$B/api/nvr/cameras/$CID/live.jpg")" "administrator rights required"

# Retention: a fake 3-day-old segment goes, the recent ones stay.
vm "sudo head -c 4096 /dev/urandom | sudo tee $NV/$CID/20200101-000000.mp4 >/dev/null && sudo touch -d '3 days ago' $NV/$CID/20200101-000000.mp4"
BEFORE=$(vm "ls $NV/$CID | wc -l")
check "retention deletes the old segment" "$(c POST /api/nvr/cleanup | py 'print(d["deleted"])')" "1"
check "old segment gone, recent ones kept" "$(vm "test -e $NV/$CID/20200101-000000.mp4 && echo still || echo gone; ls $NV/$CID | wc -l" | tr '\n' ' ')" "gone $((BEFORE - 1))"

# Camera lost for more than a minute: alert, then back online.
vm "sudo systemctl stop e2e-nvr-pub"
for _ in $(seq 1 20); do camst | grep -q "'recording'" || break; sleep 2; done
check "camera offline detected" "$(camst)" "('offline',"
for _ in $(seq 1 45); do c GET /api/system/events | grep -q 'E2E door has been unreachable' && break; sleep 2; done
check "offline alert after the delay" "$(c GET /api/system/events)" "Camera E2E door has been unreachable"
check "offline error has no credentials" "$(c GET /api/nvr | grep -c 'w/rd\|cam:' || true)" "0"
publish
for _ in $(seq 1 45); do camst | grep -q "'recording'" && break; sleep 2; done
check "camera recording again (restart with backoff)" "$(camst)" "('recording',"
for _ in $(seq 1 15); do c GET /api/system/events | grep -q 'E2E door is back online' && break; sleep 2; done
check "back online notice" "$(c GET /api/system/events)" "Camera E2E door is back online"

# Frigate: a second camera given to Frigate (not installed: config only),
# with the default options (a previous run may have changed them).
c PUT /api/nvr/frigate -d '{"detector":"auto","objects":["person","car"],"notify":true,"notifyMinutes":10}' >/dev/null
F=$(c POST /api/nvr/cameras -d "$(CAMJ 'E2E garden' $URL "$CAMPASS" frigate)")
FID=$(echo "$F" | py 'print(d["id"])')
check "Frigate camera not recorded by the built-in recorder" "$(c GET /api/nvr | py "print([c['status']['state'] for c in d['cameras'] if c['id']=='$FID'])")" "['frigate']"
FC=$(c GET /api/nvr/frigate/config | py 'print(d["config"])')
check "Frigate config lists the camera" "$FC" "e2e_garden:"
check "Frigate config uses secret placeholders" "$FC" "rtsp://{FRIGATE_E2E_GARDEN_USER}:{FRIGATE_E2E_GARDEN_PASS}@127.0.0.1:8554/cam"
check "Frigate config without credentials" "$(echo "$FC" | grep -c 'w/rd\|w%2Frd\|cam:' || true)" "0"
check "Frigate config: no MQTT, objects tracked" "$(echo "$FC" | grep -A3 -E '^(mqtt|objects):' | grep -v -E '^(detectors|  cpu|--)' | tr -d '\n ')" "mqtt:enabled:falseobjects:track:-\"person\"-\"car\""
check "Frigate options validated" "$(c PUT /api/nvr/frigate -d '{"objects":["unicorn"]}')" "unknown object"
check "Frigate options saved" "$(c PUT /api/nvr/frigate -d '{"detector":"cpu","objects":["person","dog"],"notify":true,"notifyMinutes":5}')" '"ok":true'
check "objects in the generated config" "$(c GET /api/nvr/frigate/config | py 'print(d["config"])' | grep -A3 'track:' | tr -d '\n ')" 'track:-"person"-"dog"'
if [ "${E2E_FRIGATE:-}" = 1 ]; then
  check "Frigate installed" "$(job "$(c POST /api/nvr/frigate/install)")" "success"
  check "Frigate config written" "$(vm "sudo cat $NV/frigate/config/config.yml")" "e2e_garden:"
  check "Frigate secrets root-only" "$(vm "sudo stat -c %a /var/lib/strata/nvr/frigate-secrets/FRIGATE_E2E_GARDEN_PASS")" "600"
  for _ in $(seq 1 90); do vm "curl -sf http://127.0.0.1:5005/api/version" >/dev/null && break; sleep 5; done
  check "Frigate API answers" "$(vm "curl -s http://127.0.0.1:5005/api/config" | py 'print(list(d["cameras"]))')" "['e2e_garden']"
  check "Frigate reads the camera" "$(sleep 30; vm "curl -s http://127.0.0.1:5005/api/stats" | py 'print(d["cameras"]["e2e_garden"]["camera_fps"] > 0)')" "True"
  check "Frigate removed" "$(c DELETE '/api/docker/stacks/frigate?volumes=1')" '"ok":true'
fi

# Removal stops the recorder and removes its files.
check "camera deleted with its recordings" "$(c DELETE "/api/nvr/cameras/$CID?recordings=1")" '"ok":true'
sleep 3
check "recorder process stopped" "$(vm "pgrep -fc '[/]run/strata/nvr/$CID/' || true")" "0"
check "runtime files removed" "$(vm "sudo test -e /run/strata/nvr/$CID && echo left || echo removed")" "removed"
check "recordings removed" "$(vm "test -e $NV/$CID && echo left || echo removed")" "removed"
c DELETE "/api/nvr/cameras/$FID" >/dev/null
check "no camera left" "$(c GET /api/nvr | py 'print(len(d["cameras"]))')" "0"
vm "sudo systemctl stop e2e-nvr-pub 2>/dev/null; sudo systemctl reset-failed e2e-nvr-pub 2>/dev/null; sudo docker rm -f e2e-mtx >/dev/null; sudo rm -rf $NV /tmp/e2e-mtx.yml"
