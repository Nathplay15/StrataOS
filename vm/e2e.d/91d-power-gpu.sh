# Power rules (0.13): scheduled shutdowns, restarts and power-ons by the
# clock of the motherboard (QEMU's RTC has a wake alarm), the 0.11 schedule
# turned into rules, postponement settings; hardware transcoding without a
# graphics card (the test VM has none): the player says it uses the
# processor and a device that does not exist is refused.
section "Power rules and hardware transcoding"
RTC=$(c GET /api/energy | py 'print(d["rtcWake"])')
R='{"spindownMin":0,"schedule":{"enabled":false,"days":[]},"rules":[{"id":"off","enabled":true,"action":"shutdown","days":[0,1,2,3,4,5,6],"time":"03:10"},{"id":"on","enabled":true,"action":"wake","days":[0,1,2,3,4,5,6],"time":"06:40"},{"id":"re","enabled":false,"action":"restart","days":[1],"time":"04:00"}],"guard":{"users":true,"retryMin":10,"maxMin":60,"warnMin":5}}'
if [ "$RTC" = True ]; then
  check "rules saved" "$(c PUT /api/energy -d "$R" | py 'print("error" not in (d or {}))')" "True"
  check "next action and power-on" "$(c GET /api/energy | py 'print(d["nextAction"]["action"], d["nextAction"]["time"][11:16], d["nextPowerOn"][11:16])')" "shutdown 03:10 06:40"
  check "wake alarm programmed in the clock" "$(c GET /api/energy | py 'print(d.get("rtcAlarm","")[11:16])')" "06:40"
  check "alarm read back from the RTC" "$(vm 'cat /sys/class/rtc/rtc0/wakealarm | xargs -I{} date -d @{} +%H:%M')" "06:40"
else
  check "power-on refused without an RTC alarm" "$(c PUT /api/energy -d "$R")" "cannot power it on"
fi
check "guard kept" "$(c GET /api/energy | py 'g=d["config"].get("guard",{}); print(g.get("users"), g.get("maxMin"))')" "True 60"
check "unknown action refused" "$(c PUT /api/energy -d '{"spindownMin":0,"schedule":{"enabled":false,"days":[]},"rules":[{"id":"x","enabled":true,"action":"sleep","days":[1],"time":"01:00"}]}')" "unknown power action"
check "rule without day refused" "$(c PUT /api/energy -d '{"spindownMin":0,"schedule":{"enabled":false,"days":[]},"rules":[{"id":"x","enabled":true,"action":"shutdown","days":[],"time":"01:00"}]}')" "at least one day"
check "longest delay shorter than the retry refused" "$(c PUT /api/energy -d '{"spindownMin":0,"schedule":{"enabled":false,"days":[]},"rules":[],"guard":{"retryMin":30,"maxMin":10}}')" "must not be shorter"
# A file of 0.11 (configuration import) only has the schedule: turned into rules.
c PUT /api/energy -d '{"spindownMin":0,"schedule":{"enabled":true,"days":[1,2],"shutdown":"22:00","wake":""}}' >/dev/null
check "0.11 schedule turned into a rule" "$(c GET /api/energy | py 'r=d["config"]["rules"]; print(len(r), r[0]["action"], r[0]["time"], r[0]["days"], d["config"]["schedule"]["enabled"])')" "1 shutdown 22:00 [1, 2] False"
c PUT /api/energy -d '{"spindownMin":0,"schedule":{"enabled":false,"days":[]},"rules":[]}' >/dev/null
check "no rule, no action" "$(c GET /api/energy | py 'print("nextAction" in d, "nextPowerOn" in d)')" "False False"
[ "$RTC" = True ] && check "wake alarm cleared" "$(vm 'cat /sys/class/rtc/rtc0/wakealarm; echo end')" "end"
check "fleet wake of an unknown NAS" "$(c POST /api/fleet/nope/wake)" "unknown NAS"
check "power rules are for administrators" "$(u GET /api/energy)" "administrator rights required"

check "no graphics card in the test VM" "$(c GET /api/hardware/gpu | py 'print(len(d["gpus"]), d["ffmpeg"] in (True, False))')" "0 True"
check "player on the processor" "$(c GET /api/hardware/gpu | py 'print(d["status"].get("encoder") or "cpu")')" "cpu"
check "unknown graphics device refused" "$(c PUT /api/hardware/gpu -d '{"accel":"","device":"/dev/dri/renderD129"}')" "unknown graphics device"
check "acceleration off saved" "$(c PUT /api/hardware/gpu -d '{"accel":"off"}'; c GET /api/hardware/gpu | py 'print(d["settings"].get("accel"))')" "off"
c PUT /api/hardware/gpu -d '{"accel":""}' >/dev/null
check "graphics settings are for administrators" "$(u GET /api/hardware/gpu)" "administrator rights required"
