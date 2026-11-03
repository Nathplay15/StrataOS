# shellcheck shell=bash
# Health (0.9.1): SMART history and SSD wear endpoints, security advisor
# (checks, ignore, one-click fixes), weekly health report. The virtual disks
# give no SMART data, so a history file is put in place for one of them
# before anything loads it; the trend and wear logic itself is covered by
# the Go tests of internal/health.
section "Health: SMART history, security advisor, report"
HD=$(c GET /api/storage/disks | py 'x=[x for x in d if x["serial"]][0]; print(x["path"], x["serial"], x["model"].replace(" ", "_"))')
read -r HDEV HSER HMOD <<<"$HD"
HAVAIL=$(c GET "/api/storage/disks/smart?device=$HDEV" | py 'print(d.get("available"))')
if [ "$HAVAIL" = "False" ]; then
  NOW=$(date +%s)
  HFILE="/var/lib/strata/health/disks/$(echo "$HSER" | sed 's/[^A-Za-z0-9._-]\+/_/g').json"
  vm "sudo mkdir -p /var/lib/strata/health/disks && echo '{\"key\":\"$HSER\",\"points\":[{\"t\":$((NOW - 86400)),\"re\":24,\"tp\":35},{\"t\":$NOW,\"re\":30,\"tp\":37}]}' | sudo tee $HFILE >/dev/null"
  check "SMART history read from its file" "$(c GET "/api/storage/disks/history?device=$HDEV" | py 'print([p.get("re") for p in d["points"]])')" "[24, 30]"
  vm "sudo rm -f $HFILE"
else
  check "SMART history recorded" "$(c GET "/api/storage/disks/history?device=$HDEV" | py 'print(len(d["points"]) > 0)')" "True"
fi
check "SMART history of an unknown disk" "$(c GET '/api/storage/disks/history?device=/dev/nope')" "not found"
check "SSD wear list" "$(c GET /api/storage/ssd-wear | py 'print(type(d).__name__)')" "list"

ADV=$(c GET /api/security/advisor)
check "advisor gives a score" "$(echo "$ADV" | py 'print(0 <= d["score"] <= 100, len(d["items"]) > 0)')" "True True"
check "advisor items explained" "$(echo "$ADV" | py 'print(all(i["title"] and i["detail"] and i["severity"] in ("high","medium","low","info") for i in d["items"]))')" "True"

# Time synchronization: off → item with a fix → fixed.
vm "sudo timedatectl set-ntp false"
check "advisor sees NTP off" "$(c POST /api/security/advisor/run | py 'print([i["fix"] for i in d["items"] if i["id"]=="ntp"])')" "['ntp']"
check "NTP fix" "$(c POST /api/security/advisor/fix/ntp | py 'print("ntp" in [i["id"] for i in d["items"]])')" "False"
check "NTP on again" "$(vm "timedatectl show -p NTP --value")" "yes"

# SMB 2.0 → fixed to 2.1 (the default).
SET=$(c GET /api/settings)
c PUT /api/settings -d "$(echo "$SET" | py 'd["smbMinProtocol"]="SMB2"; print(json.dumps(d))')" >/dev/null
check "advisor sees SMB 2.0" "$(c POST /api/security/advisor/run | py 'print([i["id"] for i in d["items"]])')" "smb-proto"
c POST /api/security/advisor/fix/smb-proto >/dev/null
check "SMB fix applied" "$(vm "grep 'server min protocol' /etc/samba/smb.conf")" "SMB2_10"

# Address blocking: fix, then the previous settings back.
GUARD=$(c GET /api/guard | py 'print(json.dumps(d["config"]))')
if [ "$(echo "$GUARD" | py 'print(d["enabled"])')" = "False" ]; then
  c POST /api/security/advisor/fix/guard >/dev/null
  check "guard fix enables blocking, never the LAN" "$(c GET /api/guard | py 'print(d["config"]["enabled"], d["config"]["neverLan"])')" "True True"
  c PUT /api/guard -d "$GUARD" >/dev/null
fi
check "fix without automatic fix refused" "$(c POST /api/security/advisor/fix/backup-none)" "no automatic fix"

# Ignore and watch again (the first item not of the "info" kind).
read -r IID S1 <<<"$(c POST /api/security/advisor/run | py 'x=[i["id"] for i in d["items"] if i["severity"]!="info"]; print(x[0] if x else "none", d["score"])')"
if [ "$IID" != none ]; then
  check "ignore an item" "$(c PUT /api/security/advisor/ignore -d "{\"id\":\"$IID\",\"ignored\":true}" | py "print([i['ignored'] for i in d['items'] if i['id']=='$IID'], d['score'] >= $S1)")" "[True] True"
  check "ignored item persists" "$(c POST /api/security/advisor/run | py "print([i.get('ignored', False) for i in d['items'] if i['id']=='$IID'])")" "[True]"
  check "watch it again" "$(c PUT /api/security/advisor/ignore -d "{\"id\":\"$IID\",\"ignored\":false}" | py 'print(d["score"])')" "$S1"
fi

# Weekly report: settings and a test send (no channel configured: a warning).
SET=$(c GET /api/settings)
check "report period saved" "$(c PUT /api/settings -d "$(echo "$SET" | py 'd["report"]="weekly"; print(json.dumps(d))')" >/dev/null; c GET /api/settings | py 'print(d["report"])')" "weekly"
check "invalid report period refused" "$(c PUT /api/settings -d "$(echo "$SET" | py 'd["report"]="daily"; print(json.dumps(d))')")" "invalid report period"
REP=$(c POST /api/settings/report -d '{"period":"weekly"}' | py 'print(d["report"])')
for want in "💾 Storage" "Backups (7 days)" "🔄 Updates" "security score:" "📋 Events" "running for"; do
  check "weekly report: $want" "$REP" "$want"
done
c PUT /api/settings -d "$SET" >/dev/null
