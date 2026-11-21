# Configuration backup, sections added in 0.13: firewall (address sets, security
# groups), sign-in provider, two-factor policy, backup repository, S3 (bucket
# with versioning and object lock, a user's access key), smart photos settings,
# desktop preferences. Export with the secrets,
# remove or change everything, import, check it is back and that a second
# preview has nothing left to do. Buckets of this NAS and object lock stay.
section "Configuration backup (0.13 sections)"
C12_PHRASE=Cfg012-Passphrase-2026
# body <file> <passphrase> <sections as JSON> → import/preview request
c12body() { python3 -c "import json,sys; print(json.dumps({'document': json.load(open(sys.argv[1])), 'passphrase': sys.argv[2], 'sections': json.loads(sys.argv[3])}))" "$1" "$2" "$3" > "$TMP/c12-body.json"; }
# c12item <section> <key> → action of a preview item (from stdin)
c12item() { py "s=[i for x in d.get('sections',[]) if x['id']=='$1' for i in x['items'] if i['key']=='$2']; print(s[0]['action'] if s else 'absent', d.get('error',''))"; }
C12_SECTIONS='["firewall","oidc","twoFactor","datastores","s3","photoSmart","desktop"]'

# Setup. The desktop preferences are those of an account of its own (the
# later sections expect alice's defaults).
J3=$(mktemp)
dk() { local m=$1 p=$2; shift 2; curl -s -b "$J3" -c "$J3" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
c POST /api/users -d '{"username":"cfgdesk","password":"Cfg-Desk-Pass-1","role":"user"}' >/dev/null
dk POST /api/login -d '{"username":"cfgdesk","password":"Cfg-Desk-Pass-1"}' >/dev/null
for sh in CfgS3 CfgS3b; do
  c POST /api/shares -d "{\"name\":\"$sh\",\"path\":\"/srv/strata/main/$sh\",\"smb\":false,\"permissions\":{\"$USER_NAME\":\"rw\"}}" >/dev/null
done
FW=$(c GET /api/system/network/firewall | py 'c=d["config"]; c.setdefault("ipsets",[]).append({"name":"cfgset","entries":["198.51.100.0/24"]}); c.setdefault("groups",[]).append({"name":"cfgweb","rules":[{"dir":"in","action":"accept","proto":"tcp","ports":"80,443","source":"set:cfgset"}]}); print(json.dumps({"config":c,"timeout":60}))')
check "firewall: address set and group" "$(c PUT /api/system/network/firewall -d "$FW")" '"applied":true'
check "sign-in provider" "$(c POST /api/security/oidc -d '{"id":"cfgidp","name":"Cfg IdP","issuer":"https://id.example.com/","clientId":"strata","clientSecret":"Cfg-Client-Secret-1","enabled":true,"groupMap":{"nas-admins":"famille"}}')" '"id":"cfgidp"'
check "two-factor policy (grace only)" "$(c PUT /api/security/2fa-policy -d '{"require":false,"graceDays":5}')" '"ok":true'
DSR=$(c POST /api/datastores -d '{"name":"cfg repository","destType":"local","destPath":"/srv/strata/main/cfg-repo","password":"Cfg-Repo-Pass-1","verify":{}}')
C12_DS=$(echo "$DSR" | py 'print(d.get("id",""))' 2>/dev/null)
check "backup repository" "$([ -n "$C12_DS" ] && echo created || echo "${DSR:0:200}")" "created"
check "smart photos settings" "$(c PUT /api/photos/smart -d '{"enabled":false,"noFaces":true,"limits":{"threads":1}}')" '"ok":true'
check "desktop preferences" "$(dk PUT /api/me/desktop -d '{"widgets":["status","storage"],"accent":"#16a34a","icons":{"files":[2,3]}}')" '"ok":true'
C12_S3=$(job "$(c POST /api/system/components/s3/install)")
check "S3 component" "$C12_S3" "success"
check "S3 bucket with versioning and object lock" "$(c PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[{"name":"cfgs3","share":"CfgS3","versioning":"enabled","lock":true,"lockMode":"GOVERNANCE","lockDays":1}]}')" '"ok":true'
AK=$(u POST /api/me/s3/keys -d '{"name":"cfg"}')
C12_A=$(echo "$AK" | py 'print(d["access"])' 2>/dev/null) C12_K=$(echo "$AK" | py 'print(d["secret"])' 2>/dev/null)
check "user's S3 key" "$([ -n "$C12_A" ] && echo created || echo "${AK:0:200}")" "created"

# Export with the secrets: nothing secret in clear.
c POST /api/config/export -d "{\"secrets\":true,\"passphrase\":\"$C12_PHRASE\"}" > "$TMP/c12.json"
check "export holds the 0.12 sections" "$(py 'c=d["config"]; print(sorted(k for k in ("firewall","oidc","twoFactor","datastores","s3","photoSmart","desktop") if c.get(k)))' < "$TMP/c12.json")" "['datastores', 'desktop', 'firewall', 'oidc', 'photoSmart', 's3', 'twoFactor']"
check "bucket protection exported" "$(py 'print([(b["name"], b.get("versioning"), b.get("lock"), b.get("lockMode"), b.get("lockDays")) for b in d["config"]["s3"]["buckets"]])' < "$TMP/c12.json")" "('cfgs3', 'enabled', True, 'GOVERNANCE', 1)"
check "no secret in clear" "$(grep -c -e 'Cfg-Client-Secret-1' -e 'Cfg-Repo-Pass-1' -e "${C12_K:-none}" "$TMP/c12.json")" "0"

# Remove or change everything.
FW=$(c GET /api/system/network/firewall | py 'c=d["config"]; c["ipsets"]=[s for s in c.get("ipsets") or [] if s["name"]!="cfgset"]; c["groups"]=[g for g in c.get("groups") or [] if g["name"]!="cfgweb"]; print(json.dumps({"config":c,"timeout":60}))')
c PUT /api/system/network/firewall -d "$FW" >/dev/null
c DELETE /api/security/oidc/cfgidp >/dev/null
c PUT /api/security/2fa-policy -d '{"require":false,"graceDays":0}' >/dev/null
c DELETE "/api/datastores/$C12_DS" >/dev/null
c PUT /api/photos/smart -d '{"enabled":false}' >/dev/null
dk PUT /api/me/desktop -d '{"widgets":["network"],"accent":"#dc2626"}' >/dev/null
u DELETE "/api/me/s3/keys/$C12_A" >/dev/null
# A bucket that exists only here, next to the locked one.
# (and a longer default retention on the locked one, which the file brings back).
c PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[{"name":"cfgs3","share":"CfgS3","versioning":"enabled","lock":true,"lockMode":"GOVERNANCE","lockDays":3},{"name":"cfglocal","share":"CfgS3b"}]}' >/dev/null

c12body "$TMP/c12.json" "$C12_PHRASE" '[]'
c POST /api/config/preview -d @"$TMP/c12-body.json" > "$TMP/c12-prev.json"
check "preview: address set to create" "$(c12item firewall ipset:cfgset < "$TMP/c12-prev.json")" "create"
check "preview: security group to create" "$(c12item firewall group:cfgweb < "$TMP/c12-prev.json")" "create"
check "preview: NAS firewall unchanged" "$(c12item firewall host < "$TMP/c12-prev.json")" "same"
check "preview: provider to create" "$(c12item oidc cfgidp < "$TMP/c12-prev.json")" "create"
check "preview: two-factor policy to update" "$(c12item twoFactor twoFactor < "$TMP/c12-prev.json")" "update"
check "preview: repository to open again" "$(c12item datastores "$C12_DS" < "$TMP/c12-prev.json")" "create"
check "preview: S3 settings to update" "$(c12item s3 settings < "$TMP/c12-prev.json")" "update"
check "preview: S3 key to create" "$(c12item s3 "key:$C12_A" < "$TMP/c12-prev.json")" "create"
check "preview: smart photos to update" "$(c12item photoSmart photoSmart < "$TMP/c12-prev.json")" "update"
check "preview: desktop preferences to update" "$(c12item desktop cfgdesk < "$TMP/c12-prev.json")" "update"

c12body "$TMP/c12.json" "$C12_PHRASE" "$C12_SECTIONS"
check "import job" "$(job "$(c POST /api/config/import -d @"$TMP/c12-body.json")")" "success"
check "firewall back" "$(c GET /api/system/network/firewall | py 'c=d["config"]; print([s["entries"] for s in c.get("ipsets") or [] if s["name"]=="cfgset"], [g["rules"][0]["ports"] for g in c.get("groups") or [] if g["name"]=="cfgweb"])')" "[['198.51.100.0/24']] ['80,443']"
check "provider back, secret restored" "$(c GET /api/security/oidc | py 'p=[x for x in d["providers"] if x["id"]=="cfgidp"]; print(len(p), p[0]["enabled"] if p else "-", p[0].get("groupMap") if p else "-")')" "1 True {'nas-admins': 'famille'}"
check "provider secret is the exported one" "$(vm "sudo grep -c 'Cfg-Client-Secret-1' /var/lib/strata/state.json")" "1"
check "two-factor policy back" "$(c GET /api/security/2fa-policy | py 'print(d["graceDays"])')" "5"
check "repository back" "$(c GET /api/datastores | py "print([(x['name'], x['destPath']) for x in d if x['id']=='$C12_DS'])")" "('cfg repository', '/srv/strata/main/cfg-repo')"
check "repository opens with its password" "$(job "$(c POST "/api/datastores/$C12_DS/verify" -d '{}')")" "success"
check "smart photos settings back" "$(c GET /api/photos/smart | py 's=d.get("settings",{}); print(s.get("noFaces"), s.get("limits",{}).get("threads"))')" "True 1"
check "desktop preferences back" "$(dk GET /api/me/desktop | py 'print(d["widgets"], d["accent"], d["icons"])')" "['status', 'storage'] #16a34a {'files': [2, 3]}"
check "S3 key back" "$(u GET /api/me/s3 | py 'print([k["access"] for k in d["keys"]])')" "$C12_A"
check "S3: locked bucket kept, bucket of this NAS kept" "$(c GET /api/s3 | py 'print(sorted((b["name"], b.get("versioning",""), bool(b.get("lock")), b.get("lockDays",0)) for b in d["buckets"]))')" "[('cfglocal', '', False, 0), ('cfgs3', 'enabled', True, 1)]"
if [ -n "$C12_K" ]; then
  vm "command -v rclone >/dev/null || sudo apt-get install -y -qq rclone >/dev/null 2>&1"
  for _ in $(seq 1 20); do vm "curl -sk -o /dev/null https://localhost:7380/" && break; sleep 1; done
  sleep 2
  check "restored key signs S3 requests" "$(vm "RCLONE_CONFIG_N_TYPE=s3 RCLONE_CONFIG_N_PROVIDER=Other RCLONE_CONFIG_N_ENDPOINT=https://localhost:7380 RCLONE_CONFIG_N_ACCESS_KEY_ID=$C12_A RCLONE_CONFIG_N_SECRET_ACCESS_KEY=$C12_K RCLONE_CONFIG_N_FORCE_PATH_STYLE=true rclone -q --no-check-certificate lsf N:cfgs3 2>&1 && echo listed")" "listed"
fi

# Importing again changes nothing.
c12body "$TMP/c12.json" "$C12_PHRASE" '[]'
check "second preview: nothing left to do" "$(c POST /api/config/preview -d @"$TMP/c12-body.json" | py "print(sorted({x['id']+'/'+i['key'] for x in d['sections'] if x['id'] in $C12_SECTIONS for i in x['items'] if i['action'] in ('create','update')}))")" "[]"

# Cleanup (the locked bucket needs the confirmation).
c PUT /api/s3 -d '{"enabled":false,"port":7380,"buckets":[],"confirm":true}' >/dev/null
u DELETE "/api/me/s3/keys/$C12_A" >/dev/null
FW=$(c GET /api/system/network/firewall | py 'c=d["config"]; c["ipsets"]=[s for s in c.get("ipsets") or [] if s["name"]!="cfgset"]; c["groups"]=[g for g in c.get("groups") or [] if g["name"]!="cfgweb"]; print(json.dumps({"config":c,"timeout":60}))')
c PUT /api/system/network/firewall -d "$FW" >/dev/null
c DELETE /api/security/oidc/cfgidp >/dev/null
c PUT /api/security/2fa-policy -d '{"require":false,"graceDays":0}' >/dev/null
c DELETE "/api/datastores/$C12_DS" >/dev/null
c PUT /api/photos/smart -d '{"enabled":false}' >/dev/null
for sh in CfgS3 CfgS3b; do c DELETE "/api/shares/$sh" >/dev/null; done
vm "sudo rm -rf /srv/strata/main/CfgS3 /srv/strata/main/CfgS3b /srv/strata/main/cfg-repo /srv/strata/main/.strata-s3-versions/cfgs3"
c DELETE /api/users/cfgdesk >/dev/null
rm -f "$J3"
