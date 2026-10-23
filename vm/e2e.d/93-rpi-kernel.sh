# shellcheck shell=bash
# Switch of older Raspberry Pi installations to the Raspberry Pi kernels: the
# test VM is no Raspberry Pi, so the switch is not offered and refused, and
# nothing of it is written. The switch itself is tested on the 0.6.0 image
# in emulated machines (see docs/HARDWARE.md, "Older Raspberry Pi installations").
section "Raspberry Pi kernel switch (not a Raspberry Pi)"
st=$(c GET /api/system/rpi-kernel)
check "status answers" "$st" '"applicable":false'
check "no board model" "$(echo "$st" | py 'print(repr(d["model"]))')" "''"
check "switch refused" "$(c POST /api/system/rpi-kernel/switch)" "not a Raspberry Pi"
check "undo refused" "$(c POST /api/system/rpi-kernel/undo)" "nothing to undo"
check "no Raspberry Pi source added" "$(vm 'ls /etc/apt/sources.list.d/raspberrypi.sources /etc/apt/preferences.d/raspberrypi 2>&1 | grep -c "No such"')" "2"
check "non-admin refused" "$(u GET /api/system/rpi-kernel)" "administrator rights required"
