# shellcheck shell=bash
# 0.15.4: server texts in the caller's language (whole texts only), errors
# of the interface in the NAS log, the diagnostic report (secrets masked),
# release notes, names that look like options refused, the post-update
# self-check (marker confirmed, rollback reported).
section "0.15.4 fixes (translations, interface errors, report, self-check)"

# ---- Server texts by Accept-Language ----
out=$(curl -s -b "$J" -X PUT -H 'X-Strata: 1' -H 'Content-Type: application/json' -H 'Accept-Language: fr-FR,fr;q=0.9' "$B/api/users/nobody-here-0154" -d '{}')
check "an error in French with Accept-Language: fr" "$out" "introuvable"
out=$(curl -s -b "$J" -X PUT -H 'X-Strata: 1' -H 'Content-Type: application/json' -H 'Accept-Language: en' "$B/api/users/nobody-here-0154" -d '{}')
check "the same error in English" "$out" "not found"
out=$(curl -s -b "$J" -X POST -H 'X-Strata: 1' -H 'Content-Type: application/json' -H 'Accept-Language: de' "$B/api/users" -d '{"username":"-oops","password":"Long-enough-pass-1"}')
check "a user name starting with - is refused" "$out" '"error"'
check "no Linux account -oops" "$(vm "getent passwd -- -oops || echo none")" "none"

# ---- Errors of the interface go to the NAS log ----
check "interface error accepted" "$(c POST /api/client-errors -d '{"page":"/files?path=/private","message":"e2e boom 0154","stack":"at x","kind":"render"}')" '"ok":true'
check "interface error in the log, without the query" "$(c GET /api/system/events | py 'print([e["message"] for e in d if "e2e boom 0154" in e["message"]])')" "Interface error on /files: e2e boom 0154"
for _ in $(seq 1 12); do c POST /api/client-errors -d '{"page":"/","message":"flood","kind":"error"}' >/dev/null; done
check "interface errors rate-limited" "$(c GET /api/system/events | py 'print(sum(1 for e in d if e["message"].endswith(": flood")))')" "9"

# ---- Diagnostic report ----
code=$(curl -s -b "$J" -o "$TMP/report.tgz" -w '%{http_code}' "$B/api/system/support/report")
check "diagnostic report downloaded" "$code" "200"
check "report files" "$(tar tzf "$TMP/report.tgz" 2>&1 | sort | tr '\n' ' ')" "strataos-report/events.txt strataos-report/hardware.txt strataos-report/journal.txt"
HASH=$(vm "sudo grep -o '\"passwordHash\": *\"[^\"]*' /var/lib/strata/state.json | head -1 | sed 's/.*\"//' | cut -c1-20")
check "no password hash in the report" "$(tar xzOf "$TMP/report.tgz" | grep -cF "${HASH:-no-hash}")" "0"
check "report download audited" "$(c GET /api/system/events | py 'print(any(e["message"]=="Diagnostic report downloaded" for e in d))')" "True"
check "report refused to a user" "$(curl -s -o /dev/null -w '%{http_code}' -b "$J2" "$B/api/system/support/report")" "403"

# ---- Release notes of the available version ----
check "release notes route answers" "$(c GET /api/system/updates/strata/notes)" '"notes"'

# ---- Post-update self-check ----
V=$(c GET /api/system/updates/strata | py 'print(d["running"])')
vm "printf 'FROM=0.0.1\nTO=$V\nSERVICES=smbd\n' | sudo tee /var/lib/strata/update-check.env >/dev/null; sudo systemctl restart strata"
wait_api
for _ in $(seq 1 30); do vm "test -f /var/lib/strata/update-check.env" || break; sleep 3; done
check "a healthy update confirms itself" "$(vm "test -f /var/lib/strata/update-check.env && echo still || echo removed")" "removed"
check "self-check logged" "$(c GET /api/system/events | py 'print(any("checked: the interface and the services answer" in e["message"] for e in d))')" "True"
vm "printf 'FROM=$V\nTO=9.9.9\nSERVICES=\nROLLEDBACK=1\n' | sudo tee /var/lib/strata/update-check.env >/dev/null; sudo systemctl restart strata"
wait_api
sleep 3
check "a rollback is reported" "$(c GET /api/system/events | py 'print(any("did not pass its checks" in e["message"] for e in d))')" "True"
check "rollback marker removed" "$(vm "test -f /var/lib/strata/update-check.env && echo still || echo removed")" "removed"
