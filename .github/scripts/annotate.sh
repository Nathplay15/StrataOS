#!/bin/bash
# Runs a command; when it fails, also reports the end of its output as an
# annotation, readable without signing in (logs need a GitHub account).
log=$(mktemp)
"$@" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
if [ "$status" -ne 0 ]; then
  msg=$(tail -40 "$log" | sed 's/%/%25/g' | awk '{printf "%s%%0A", $0}')
  echo "::error title=$1 failed::$msg"
fi
exit "$status"
