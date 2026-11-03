# shellcheck shell=bash
# UX of 0.10: language of the messages sent by the NAS (the first
# administrator's interface language by default, or the one chosen in the
# settings), health report in that language, notification centre (history,
# kinds, read and cleared per user), security advisor with backup tasks that
# all are disabled. The desktop itself is checked by the TypeScript build.
section "UX 0.10: message language, notification centre, advisor texts"
SET=$(c GET /api/settings)
check "PUT /api/me/lang: invalid language refused" "$(c PUT /api/me/lang -d '{"lang":"xx"}')" "invalid language"
c PUT /api/me/lang -d '{"lang":"fr"}' >/dev/null
check "interface language remembered" "$(c GET /api/me | py 'print(d.get("lang"))')" "fr"
u PUT /api/me/lang -d '{"lang":"de"}' >/dev/null
check "a user's language does not set the NAS language" "$(c GET /api/settings | py 'print(d["notifyLangAuto"])')" "fr"

# Automatic (""): the report follows the first administrator's language.
c PUT /api/settings -d "$(echo "$SET" | py 'd["notifyLang"]=""; print(json.dumps(d))')" >/dev/null
REP=$(c POST /api/settings/report -d '{"period":"weekly"}' | py 'print(d["report"])')
check "report in the administrator's language (automatic)" "$REP" "💾 Stockage"
check "report in French: backups" "$REP" "Sauvegardes (7 jours)"
check "report in French: security score" "$REP" "score de sécurité"
# Chosen in the settings: wins over the administrator's language.
c PUT /api/settings -d "$(echo "$SET" | py 'd["notifyLang"]="en"; print(json.dumps(d))')" >/dev/null
check "report in the chosen language" "$(c POST /api/settings/report -d '{"period":"monthly"}' | py 'print(d["report"])')" "📦 Backups (30 days)"
c PUT /api/me/lang -d '{"lang":"en"}' >/dev/null

# Notification centre: the reports just sent are in the history.
NOTES=$(c GET /api/notifications)
check "notification history: report listed with its kind" "$(echo "$NOTES" | py 'print([n["kind"] for n in d["items"]][:2])')" "['report', 'report']"
check "notification history: French subject while automatic" "$(echo "$NOTES" | py 'print(d["items"][1]["subject"])')" "Rapport hebdomadaire de"
check "notification history: unread count" "$(echo "$NOTES" | py 'print(d["unread"] >= 2)')" "True"
c POST /api/settings/test-notification -d '{}' >/dev/null
check "test notification listed" "$(c GET /api/notifications | py 'print(d["items"][0]["kind"], d["items"][0].get("action",""))')" "system"
c POST /api/notifications/read -d '{}' >/dev/null
check "mark all read" "$(c GET /api/notifications | py 'print(d["unread"], len(d["items"]) > 0)')" "0 True"
c POST /api/notifications/clear >/dev/null
check "clear all" "$(c GET /api/notifications | py 'print(len(d["items"]), d["unread"])')" "0 0"
check "users do not see the NAS notifications" "$(u GET /api/notifications | py 'print(len(d["items"]))')" "0"
check "users cannot clear them" "$(u POST /api/notifications/clear)" "admin"
c PUT /api/settings -d "$SET" >/dev/null

# Advisor: backup tasks that exist but are all disabled.
ENABLED=$(c GET /api/backups | py 'print(" ".join(b["id"] for b in d if b["enabled"]))')
for id in $ENABLED; do
  c PUT "/api/backups/$id" -d "$(c GET /api/backups | py "print(json.dumps(dict([b for b in d if b['id']=='$id'][0], enabled=False)))")" >/dev/null
done
UXB=$(c POST /api/backups -d '{"name":"ux-off","kind":"rsync","sources":["/srv/strata/main/Photos"],"destType":"local","destPath":"/srv/strata/main/ux-off","enabled":false}' | py 'print(d.get("id",""))')
CLOUDS=$(c GET /api/cloudsync | py 'print(sum(1 for x in d if x.get("enabled")))' 2>/dev/null)
if [ -n "$UXB" ] && [ "${CLOUDS:-0}" = 0 ]; then
  ITEM=$(c POST /api/security/advisor/run | py 'x=[i for i in d["items"] if i["id"]=="backup-none"]; print(x[0]["title"], "|", x[0]["detail"]) if x else print("none")')
  check "advisor: no active backup task" "$ITEM" "No active backup task"
  check "advisor: disabled tasks named" "$ITEM" "ux-off"
else
  check "disabled backup task created" "$UXB" "-"
fi
[ -n "$UXB" ] && c DELETE "/api/backups/$UXB" >/dev/null
for id in $ENABLED; do
  c PUT "/api/backups/$id" -d "$(c GET /api/backups | py "print(json.dumps(dict([b for b in d if b['id']=='$id'][0], enabled=True)))")" >/dev/null
done
c POST /api/security/advisor/run >/dev/null
