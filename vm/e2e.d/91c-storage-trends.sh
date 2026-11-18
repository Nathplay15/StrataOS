# Storage trends (0.12.1, PR #55): shares measured by du once a day (the last
# run kept on disk, so a restart does not measure again), accounts from the
# user quotas.
section "Storage trends"
check "trends are for administrators" "$(u GET /api/storage/trends)" "administrator rights required"
TF=$(vm "sudo find /var/lib/strata -maxdepth 2 -name storage-trends.json" | head -1)
TF=${TF:-/var/lib/strata/storage-trends.json}
# Measure now: forget the last run and restart (the health check starts it).
vm "sudo rm -f $TF; sudo systemctl restart strata"
wait_api
c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null
for _ in $(seq 1 60); do vm "sudo test -s $TF" && break; sleep 3; done
check "last run kept on disk" "$(vm "sudo test -s $TF && echo kept")" "kept"
for _ in $(seq 1 120); do [ "$(vm 'pgrep -c -x du || true')" = 0 ] && break; sleep 5; done
check "shares measured (one point an hour at most)" "$(c GET "/api/storage/trends/history?name=share:/srv/strata/main/Photos" | py 'import time; p=d["points"][-1]; print(time.time()-p["t"] < 7200, p["used"] > 0)')" "True True"
check "shares listed by name" "$(c GET /api/storage/trends | py 'print([s["name"] for s in d["shares"] if s["name"]=="Photos"])')" "['Photos']"
check "account names without prefix" "$(c GET /api/storage/trends | py 'print(any(x["name"].startswith("user:") for x in d["users"]))')" "False"
M=$(vm "sudo stat -c %Y $TF")
vm "sudo systemctl restart strata"
wait_api
sleep 45
check "a restart does not measure again" "$(vm "sudo stat -c %Y $TF")" "${M:-missing}"
c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null
check "volume history" "$(c GET "/api/storage/trends/history?name=main" | py 'print(len(d["points"]) > 0)')" "True"
check "no du left running" "$(vm 'pgrep -c -x du || true')" "0"
