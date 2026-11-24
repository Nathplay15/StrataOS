# shellcheck shell=bash
# WiFi for real, with a simulated radio pair (mac80211_hwsim): hostapd runs a
# WPA2 access point on wlan1 whose 32-character password has a quote and a
# backslash; the NAS joins it on wlan0 through the API (trial apply, then
# confirm). A wrong password applied on trial and not confirmed must be
# rolled back for real: wpa_supplicant back on the right network.
section "WiFi with a simulated access point (mac80211_hwsim + hostapd)"
if ! vm "sudo modprobe mac80211_hwsim radios=2 && ip link show wlan1" >/dev/null; then
  ok "skipped: no mac80211_hwsim in this kernel"
else
  vm "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq hostapd >/dev/null 2>&1; sudo systemctl stop hostapd 2>/dev/null; true"
  PASS='Quote"Back\slash-32-chars-pass!!'  # exactly 32 characters
  printf 'interface=wlan1\ndriver=nl80211\nssid=E2E-AP\nhw_mode=g\nchannel=6\nwpa=2\nwpa_key_mgmt=WPA-PSK\nrsn_pairwise=CCMP\nwpa_passphrase=%s\n' "$PASS" > "$TMP/hostapd.conf"
  vm "cat > /tmp/e2e-hostapd.conf" < "$TMP/hostapd.conf"
  vm "sudo hostapd -B -P /run/e2e-hostapd.pid /tmp/e2e-hostapd.conf >/dev/null"
  check "the NAS lists the simulated radios" "$(c GET /api/system/network/wifi | py 'print(sorted(i["name"] for i in d["ifaces"]))')" "'wlan0', 'wlan1'"
  # apply <password> [confirm]: WiFi on wlan0 on trial (30 s), confirmed or not.
  apply() {
    # The current configuration, with only the WiFi entry changed.
    c GET /api/system/network | python3 -c 'import json,sys; c=json.load(sys.stdin)["config"]; c["wifi"]=[{"name":"wlan0","ssid":"E2E-AP","psk":sys.argv[1],"enabled":True}]; print(json.dumps({"config":c,"timeout":30}))' "$1" > "$TMP/wifi.json"
    local tok; tok=$(c PUT /api/system/network/config -d @"$TMP/wifi.json" | py 'print(d.get("token",""))')
    [ "${2:-}" = confirm ] || return 0
    for _ in $(seq 1 20); do
      sleep 3
      [ -z "$(c POST /api/system/network/confirm -d "{\"token\":\"$tok\"}" | grep -i error)" ] && return 0
    done
  }
  state() { vm "sudo wpa_cli -i wlan0 status 2>/dev/null | grep -E '^(wpa_state|ssid)='" | tr '\n' ' '; }
  waitstate() { for _ in $(seq 1 20); do s=$(state); case "$s" in *"$1"*) break ;; esac; sleep 2; done; echo "$s"; }
  apply "$PASS" confirm
  check "joined with the right password (quote, backslash, 32 characters)" "$(waitstate COMPLETED)" "wpa_state=COMPLETED"
  check "configuration where wpa_supplicant@wlan0 reads it, key not in clear" "$(vm "sudo head -1 /etc/wpa_supplicant/wpa_supplicant-wlan0.conf; sudo grep -c 'psk=\"' /etc/wpa_supplicant/wpa_supplicant-wlan0.conf; sudo stat -c %a /etc/wpa_supplicant/wpa_supplicant-wlan0.conf" | tr '\n' ' ')" "# Managed by StrataOS 0 600"
  apply "wrong-password-123"
  check "a wrong password on trial: not associated" "$(sleep 12; s=$(state); case "$s" in *COMPLETED*) echo "associated: $s" ;; *) echo "not associated: $s" ;; esac)" "not associated"
  check "…and rolled back after the trial: associated again" "$(sleep 30; waitstate COMPLETED)" "wpa_state=COMPLETED"
  check "the right key is back in the file" "$(vm "sudo grep -c '^	psk=' /etc/wpa_supplicant/wpa_supplicant-wlan0.conf")" "1"
  # Forget the network: wpa_supplicant stops.
  c GET /api/system/network | python3 -c 'import json,sys; c=json.load(sys.stdin)["config"]; c["wifi"]=[]; print(json.dumps({"config":c,"timeout":30}))' > "$TMP/wifi.json"
  tok=$(c PUT /api/system/network/config -d @"$TMP/wifi.json" | py 'print(d.get("token",""))')
  for _ in $(seq 1 20); do sleep 3; [ -z "$(c POST /api/system/network/confirm -d "{\"token\":\"$tok\"}" | grep -i error)" ] && break; done
  check "forgetting the network stops wpa_supplicant@wlan0" "$(vm "systemctl is-active wpa_supplicant@wlan0; ls /etc/wpa_supplicant/wpa_supplicant-wlan0.conf 2>&1 | grep -c 'No such'")" "inactive
1"
  vm "sudo kill \$(cat /run/e2e-hostapd.pid) 2>/dev/null; sleep 1; sudo rmmod mac80211_hwsim 2>/dev/null; true"
fi
