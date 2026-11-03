# shellcheck shell=bash
# Desktop personalization and global search (0.9.1): widgets, wallpaper and
# accent in /api/me/desktop (validation, partial updates, per user), the
# wallpaper upload (re-encoded JPEG, refused HTML/SVG/oversized files, served
# with a strict content type), the file search limit used by the global
# search, and the one-click bridge plan. Creating the bridge itself changes
# the VM's network: only with E2E_BRIDGE=1 (it is then put back as a plain
# DHCP interface managed by StrataOS).
section "Desktop preferences and search"

check "default prefs" "$(u GET /api/me/desktop)" '"widgets":null'
check "widgets saved" "$(u PUT /api/me/desktop -d '{"widgets":["network","status"],"accent":"#16a34a","wallpaper":"strata"}'; u GET /api/me/desktop)" '"widgets":["network","status"]'
check "partial update keeps the widgets" "$(u PUT /api/me/desktop -d '{"icons":{"files":[1,1]}}'; u GET /api/me/desktop | py 'print(d["widgets"], d["accent"], d["wallpaper"], d["icons"])')" "['network', 'status'] #16a34a strata {'files': [1, 1]}"
check "unknown widget refused" "$(u PUT /api/me/desktop -d '{"widgets":["nope"]}')" "unknown widget"
check "bad accent refused" "$(u PUT /api/me/desktop -d '{"accent":"red"}')" "invalid accent"
check "custom wallpaper needs an image" "$(u PUT /api/me/desktop -d '{"wallpaper":"custom"}')" "unknown wallpaper"
check "prefs are per user" "$(c GET /api/me/desktop)" '"widgets":null'

# A real PNG (1×1) made by Python, an HTML file and an SVG pretending to be images.
python3 -c "import base64,sys; sys.stdout.buffer.write(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='))" >"$TMP/w.png"
printf '<!doctype html><script>alert(1)</script>' >"$TMP/w.html"
printf '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>' >"$TMP/w.svg"
up() { curl -s -b "$J2" -X PUT -H 'X-Strata: 1' -H "Content-Type: $2" --data-binary "@$1" "$B/api/me/wallpaper"; }
check "HTML upload refused" "$(up "$TMP/w.html" image/png)" "JPEG, PNG or GIF"
check "SVG upload refused" "$(up "$TMP/w.svg" image/svg+xml)" "JPEG, PNG or GIF"
head -c 6000000 /dev/zero >"$TMP/w.big"
check "oversized upload refused" "$(up "$TMP/w.big" image/png)" "5 MB"
check "PNG upload accepted" "$(up "$TMP/w.png" image/png; u GET /api/me/desktop | py 'print(d["wallpaper"], d.get("customWallpaper",0) > 0)')" "custom True"
check "served as JPEG, nosniff" "$(curl -s -b "$J2" -D - -o "$TMP/w.out" "$B/api/me/wallpaper" | tr -d '\r' | grep -iE '^(content-type|x-content-type-options)' | sort | tr '\n' ' '; head -c 3 "$TMP/w.out" | od -An -tx1)" "content-type: image/jpeg x-content-type-options: nosniff  ff d8 ff"
check "other users do not see it" "$(curl -s -o /dev/null -w '%{http_code}' -b "$J" "$B/api/me/wallpaper")" "404"
check "wallpaper removed" "$(u DELETE /api/me/wallpaper; u GET /api/me/desktop | py 'print(repr(d["wallpaper"]), "customWallpaper" in d)')" "'' False"
u PUT /api/me/desktop -d '{"widgets":null,"accent":"","wallpaper":"","icons":{}}' >/dev/null

# File search with the limit of the global search.
SD="$P/SearchTest"
c POST /api/files/mkdir -d "{\"path\":\"$SD\"}" >/dev/null
for i in 1 2 3 4 5; do vm "sudo touch '$SD/needle-$i.txt'"; done
check "search limit" "$(c GET "/api/files/search?q=needle&limit=3&path=/srv/strata" | py 'print(len(d["entries"]), d["truncated"])')" "3 True"
check "search without limit" "$(c GET "/api/files/search?q=needle&path=$SD" | py 'print(len(d["entries"]))')" "5"
c POST /api/files/delete -d "{\"paths\":[\"$SD\"]}" >/dev/null

# Bridge plan: the main link and the configuration the bridge would take over.
PLAN=$(c GET /api/system/network/bridge-plan)
check "bridge plan" "$PLAN" '"bridge":"br0"'
check "bridge plan keeps DHCP" "$PLAN" '"mode":"dhcp"'
check "bridge plan admin only" "$(u GET /api/system/network/bridge-plan)" "administrator rights"
check "quick bridge only on the main link" "$(c POST /api/system/network/quick-bridge -d '{"link":"lo"}')" "main network interface"
if [ "${E2E_BRIDGE:-0}" = 1 ]; then
  LINK=$(echo "$PLAN" | py 'print(d["link"])')
  IP0=$(vm "ip -4 -br addr show dev $LINK" | awk '{print $3}')
  R=$(c POST /api/system/network/quick-bridge -d "{\"link\":\"$LINK\"}")
  TOKEN=$(echo "$R" | py 'print(d["token"])')
  check "quick bridge applied on trial" "$R" '"bridge":"br0"'
  OK=""
  for _ in $(seq 1 30); do
    sleep 3
    OK=$(curl -s -m 4 -X POST -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B/api/system/network/confirm" -d "{\"token\":\"$TOKEN\"}" -w '%{http_code}' -o /dev/null)
    [ "$OK" = 200 ] && break
  done
  check "bridge confirmed through the NAS's address" "$OK" "200"
  check "NAS address moved to the bridge" "$(vm "ip -4 -br addr show dev br0" | awk '{print $3}')" "$IP0"
  check "link is a bridge port" "$(vm "ip -br link show master br0")" "$LINK"
  check "VM wizards list the bridge" "$(c GET /api/system/network/bridge-plan)" '"bridges":["br0"]'
  # Back to a plain interface (managed by StrataOS, DHCP).
  R=$(c PUT /api/system/network/config -d "{\"config\":{\"interfaces\":[{\"name\":\"$LINK\",\"mode\":\"dhcp\"}],\"bonds\":[],\"bridges\":[],\"vlans\":[]},\"timeout\":120}")
  TOKEN=$(echo "$R" | py 'print(d["token"])')
  for _ in $(seq 1 30); do
    sleep 3
    OK=$(curl -s -m 4 -X POST -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B/api/system/network/confirm" -d "{\"token\":\"$TOKEN\"}" -w '%{http_code}' -o /dev/null)
    [ "$OK" = 200 ] && break
  done
  check "bridge removed again" "$OK" "200"
  check "address back on the link" "$(vm "ip -4 -br addr show dev $LINK" | awk '{print $3}')" "$IP0"
fi
