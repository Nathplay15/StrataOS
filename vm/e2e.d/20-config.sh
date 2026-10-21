# Configuration backup: export (with and without secrets), import with preview,
# encrypted secrets, accounts that sign in again after a re-import.
section "Configuration backup"
CFG_USER=cfguser CFG_PASS=Cfg-User-Pass-1 CFG_PHRASE=Cfg-Passphrase-2026
J3=$(mktemp)
# body <file> <passphrase> <sections as a python list> → import/preview request
body() { python3 -c "import json,sys; print(json.dumps({'document': json.load(open(sys.argv[1])), 'passphrase': sys.argv[2], 'sections': json.loads(sys.argv[3])}))" "$1" "$2" "$3" > "$TMP/cfg-body.json"; }
# item <section> <key> → action and detail of a preview item
item() { py "s=[i for x in d.get('sections',[]) if x['id']=='$1' for i in x['items'] if i['key']=='$2']; print(s[0]['action'], s[0].get('detail','') if s else '') if s else print('absent', d.get('error',''))"; }

check "test user" "$(c POST /api/users -d "{\"username\":\"$CFG_USER\",\"password\":\"$CFG_PASS\",\"role\":\"user\",\"groups\":[\"famille\"]}")" '"ok":true'
check "test share" "$(c POST /api/shares -d '{"name":"CfgShare","path":"/srv/strata/main/CfgShare","smb":true,"browseable":true,"permissions":{"cfguser":"rw"}}')" '"name":"CfgShare"'
SCHED=$(c POST /api/schedules -d '{"name":"CfgScrub","type":"scrub","cron":"0 3 * * 0","enabled":false,"targetKind":"zfs","target":"tank"}' | py 'print(d.get("id",""))')
check "test schedule" "$([ -n "$SCHED" ] && echo created)" "created"
c GET /api/settings | py 'd["serverString"]="Cfg test NAS"; print(json.dumps(d))' > "$TMP/cfg-set.json"
check "test setting" "$(c PUT /api/settings -d @"$TMP/cfg-set.json")" '"ok":true'

c GET /api/config/export > "$TMP/cfg-plain.json"
check "plain export" "$(py 'print(d["format"], d["version"], "secrets" in d, [s["name"] for s in d["config"]["shares"]])' < "$TMP/cfg-plain.json")" "strataos-config 1 False"
check "plain export holds the share" "$(cat "$TMP/cfg-plain.json")" '"CfgShare"'
check "plain export has no password hash" "$(grep -c '\$2a\$\|\$y\$' "$TMP/cfg-plain.json")" "0"
c POST /api/config/export -d "{\"secrets\":true,\"passphrase\":\"$CFG_PHRASE\"}" > "$TMP/cfg-secret.json"
check "encrypted export" "$(py 'print(d["secrets"]["cipher"], d["secrets"]["kdf"])' < "$TMP/cfg-secret.json")" "aes-256-gcm argon2id"
check "short passphrase refused" "$(c POST /api/config/export -d '{"secrets":true,"passphrase":"short"}')" "at least 8"

# Change and delete things, then bring them back from the plain file.
c DELETE /api/shares/CfgShare >/dev/null
c DELETE "/api/schedules/$SCHED" >/dev/null
c GET /api/settings | py 'd["serverString"]="Changed"; print(json.dumps(d))' > "$TMP/cfg-set.json"
c PUT /api/settings -d @"$TMP/cfg-set.json" >/dev/null
body "$TMP/cfg-plain.json" "" '[]'
c POST /api/config/preview -d @"$TMP/cfg-body.json" > "$TMP/cfg-prev.json"
check "preview: share to create" "$(item shares CfgShare < "$TMP/cfg-prev.json")" "create"
check "preview: schedule to create" "$(item schedules "$SCHED" < "$TMP/cfg-prev.json")" "create"
check "preview: settings changed" "$(py "print([i['changes'] for x in d['sections'] if x['id']=='settings' for i in x['items']])" < "$TMP/cfg-prev.json")" "serverString"
check "preview: unchanged share" "$(item shares Photos < "$TMP/cfg-prev.json")" "same"
check "preview: volume found" "$(item volumes /srv/strata/main < "$TMP/cfg-prev.json")" "Found here"
body "$TMP/cfg-plain.json" "" '["settings","shares","schedules"]'
check "plain import job" "$(job "$(c POST /api/config/import -d @"$TMP/cfg-body.json")")" "success"
check "share is back" "$(c GET /api/shares | py 'print([s["permissions"] for s in d if s["name"]=="CfgShare"])')" "'cfguser': 'rw'"
check "share published again" "$(vm "sudo testparm -s 2>/dev/null | grep -c '\[CfgShare\]'")" "1"
check "schedule is back" "$(c GET /api/schedules | py 'print([s["id"] for s in d if s["name"]=="CfgScrub"])')" "$SCHED"
check "setting is back" "$(c GET /api/settings | py 'print(d["serverString"])')" "Cfg test NAS"
c POST /api/config/preview -d @"$TMP/cfg-body.json" > "$TMP/cfg-prev.json"
check "second preview: nothing left" "$(item shares CfgShare < "$TMP/cfg-prev.json")" "same"

# Newer format refused.
py 'd["version"]=99; d["strataos"]="9.9.9"; print(json.dumps(d))' < "$TMP/cfg-plain.json" > "$TMP/cfg-new.json"
body "$TMP/cfg-new.json" "" '[]'
check "newer format refused" "$(c POST /api/config/preview -d @"$TMP/cfg-body.json")" "newer StrataOS"

# A deleted user comes back with its passwords from the encrypted file.
check "user deleted" "$(c DELETE /api/users/$CFG_USER)" '"ok":true'
body "$TMP/cfg-secret.json" "Wrong-Passphrase-1" '[]'
check "wrong passphrase refused" "$(c POST /api/config/preview -d @"$TMP/cfg-body.json")" "wrong passphrase"
body "$TMP/cfg-secret.json" "$CFG_PHRASE" '[]'
c POST /api/config/preview -d @"$TMP/cfg-body.json" > "$TMP/cfg-prev.json"
check "right passphrase opens the secrets" "$(py 'print(d["encrypted"], d["secrets"])' < "$TMP/cfg-prev.json")" "True True"
check "preview: user restored with password" "$(item users $CFG_USER < "$TMP/cfg-prev.json")" "create Password and Samba password restored"
check "preview: own account untouched" "$(item users $ADMIN_USER < "$TMP/cfg-prev.json")" "skip"
body "$TMP/cfg-secret.json" "$CFG_PHRASE" '["users"]'
check "encrypted import job" "$(job "$(c POST /api/config/import -d @"$TMP/cfg-body.json")")" "success"
check "user is back, enabled" "$(c GET /api/users | py "print([(u['disabled'], u['groups']) for u in d if u['username']=='$CFG_USER'])")" "(False, ['famille'])"
check "restored user signs in" "$(curl -s -c "$J3" -X POST -H 'X-Strata: 1' "$B/api/login" -d "{\"username\":\"$CFG_USER\",\"password\":\"$CFG_PASS\"}")" '"role":"user"'
check "restored user on SMB" "$(vm "smbclient -L localhost -U $CFG_USER%$CFG_PASS 2>&1")" "CfgShare"

# Without secrets, a deleted account comes back disabled.
check "user deleted again" "$(c DELETE /api/users/$CFG_USER)" '"ok":true'
body "$TMP/cfg-plain.json" "" '["users"]'
check "plain user import" "$(job "$(c POST /api/config/import -d @"$TMP/cfg-body.json")")" "success"
check "user back disabled" "$(c GET /api/users | py "print([u['disabled'] for u in d if u['username']=='$CFG_USER'])")" "[True]"
check "disabled user cannot sign in" "$(curl -s -X POST -H 'X-Strata: 1' "$B/api/login" -d "{\"username\":\"$CFG_USER\",\"password\":\"$CFG_PASS\"}")" "invalid"

# Automatic backups.
check "auto backup settings" "$(c PUT /api/config/auto -d '{"enabled":true,"folder":"/srv/strata/main/CfgBackups","keep":2,"hour":3}')" '"ok":true'
check "auto backup outside volumes refused" "$(c PUT /api/config/auto -d '{"enabled":true,"folder":"/etc","keep":2,"hour":3}')" "mounted volume"
for _ in 1 2 3; do job "$(c POST /api/config/auto/run)" >/dev/null; sleep 1; done
check "auto backup keeps 2 copies" "$(c GET /api/config/auto | py 'print(len(d["files"]), d["config"]["lastResult"])')" "2 OK"
F=$(c GET /api/config/auto | py 'print(d["files"][0]["name"])')
check "auto backup downloadable" "$(curl -s -b "$J" "$B/api/config/auto/files/$F" | py 'print(d["format"])')" "strataos-config"

# Cleanup
c PUT /api/config/auto -d '{"enabled":false,"folder":"","keep":7,"hour":2}' >/dev/null
c DELETE /api/users/$CFG_USER >/dev/null
c DELETE /api/shares/CfgShare >/dev/null
c DELETE "/api/schedules/$SCHED" >/dev/null
vm "sudo rm -rf /srv/strata/main/CfgShare /srv/strata/main/CfgBackups"
rm -f "$J3"
