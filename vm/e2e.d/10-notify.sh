# shellcheck shell=bash
# Smarter notifications: per-channel switch and level, quiet hours, progress
# alerts notified once. Uses the webhook sink of vm/test-sinks.py (host side,
# 10.0.2.2:8720 from the VM). The once-per-operation logic of RAID/ZFS
# progress is unit-tested (the health check runs every 5 minutes: too slow here).
section "Notifications: channels, levels, quiet hours"
NS="$TMP/notify-sinks"
python3 -I vm/test-sinks.py "$NS" > "$TMP/notify-sinks.out" 2>&1 &
SINKS_PID=$!
for _ in $(seq 1 20); do grep -q ready "$TMP/notify-sinks.out" 2>/dev/null && break; sleep 0.5; done
# nset '<python statements on d>' -> PUT /api/settings result
nset() { c GET /api/settings | py "d.update(webhookUrl='http://10.0.2.2:8720/hook', smtpHost='', notifyEmail='', telegramToken='', telegramChatId='', ntfyUrl=''); $1; print(json.dumps(d))" | c PUT /api/settings -d @-; }
ntest() { c POST /api/settings/test-notification -d "{\"level\":\"$1\"}"; }
hooks() { wc -l < "$NS/webhook.log" 2>/dev/null || echo 0; }

check "webhook only, preferences reset" "$(nset "d.update(notifyChannels={}, quietHours={})")" '"ok":true'
check "zero preferences: enabled, every level" "$(c GET /api/settings | py 'print(d["notifyChannels"]["webhook"])')" "{}"
check "test reaches the webhook" "$(c POST /api/settings/test-notification)" '"sent":["webhook"]'
check "info event delivered by default" "$(ntest info)" '"sent":["webhook"]'
N=$(hooks)
check "two webhooks received" "$N" "2"

check "webhook set to errors only" "$(nset "d['notifyChannels']['webhook']={'minLevel':'error'}")" '"ok":true'
check "warning filtered out" "$(ntest warning)" '"sent":[]'
check "error delivered" "$(ntest error)" '"sent":["webhook"]'
check "only the error reached the sink" "$(hooks)" "$((N + 1))"
check "explicit test ignores the level" "$(c POST /api/settings/test-notification)" '"sent":["webhook"]'
check "unknown level refused" "$(nset "d['notifyChannels']['webhook']={'minLevel':'critical'}")" "invalid notification level"

FROM=$(vm "date -d '-1 hour' +%H:%M" | tr -d '\r\n') TO=$(vm "date -d '+1 hour' +%H:%M" | tr -d '\r\n')
check "quiet hours around now ($FROM-$TO)" "$(nset "d['notifyChannels']['webhook']={}; d['quietHours']={'enabled':True,'start':'$FROM','end':'$TO'}")" '"ok":true'
check "warning held during quiet hours" "$(ntest warning)" '"sent":[]'
check "error sent during quiet hours" "$(ntest error)" '"sent":["webhook"]'
check "silence everything" "$(nset "d['quietHours']['silenceAll']=True")" '"ok":true'
check "error held too" "$(ntest error)" '"sent":[]'
check "empty quiet window refused" "$(nset "d['quietHours']={'enabled':True,'start':'08:00','end':'08:00'}")" "must differ"
check "malformed time refused" "$(nset "d['quietHours']={'enabled':True,'start':'8h','end':'09:00'}")" "HH:MM"

N=$(hooks)
check "webhook disabled, URL kept" "$(nset "d['quietHours']={}; d['notifyChannels']['webhook']={'disabled':True}")" '"ok":true'
check "URL still saved" "$(c GET /api/settings | py 'print(d["webhookUrl"], d["notifyChannels"]["webhook"])')" "hook {'disabled': True}"
check "disabled channel: nothing sent" "$(c POST /api/settings/test-notification)" "disabled"
check "sink untouched" "$(hooks)" "$N"
check "re-enabled" "$(nset "d['notifyChannels']['webhook']={}")" '"ok":true'
check "delivered again" "$(ntest error)" '"sent":["webhook"]'

# The RAID 5 resync after its creation used to log one event per percentage.
check "no progress percentage in the event log" "$(c GET /api/system/events | py 'print(sum(1 for e in d if "is rebuilding (" in e["message"]))')" "0"

c GET /api/settings | py "d.update(webhookUrl='', notifyChannels={}, quietHours={}); print(json.dumps(d))" | c PUT /api/settings -d @- >/dev/null
kill "$SINKS_PID" 2>/dev/null; SINKS_PID=""
