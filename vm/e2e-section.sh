#!/bin/bash
# Runs feature sections of the end-to-end test (vm/e2e.d/*.sh) against a VM
# that already went through vm/e2e-test.sh, to work on one feature without
# the whole suite:  vm/e2e-section.sh vm/e2e.d/85-media.sh [...]
set -uo pipefail
cd "$(dirname "$0")/.."
B=${STRATA_URL:-http://localhost:7070}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
USER_NAME=alice USER_PASS=Alice-Pass-1
J=$(mktemp) J2=$(mktemp) TMP=$(mktemp -d)
SINKS_PID=""
trap 'rm -rf "$J" "$J2" "$TMP"; [ -n "$SINKS_PID" ] && kill "$SINKS_PID" 2>/dev/null' EXIT
FAILS=0 PASSES=0

c() { local m=$1 p=$2; shift 2; curl -s -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
u() { local m=$1 p=$2; shift 2; curl -s -b "$J2" -c "$J2" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "${2:-}"; }
# check "description" "actual" "expected substring"
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:300}" ;; esac; }
vm() { ./vm/vm.sh ssh "$@" 2>/dev/null; }
job() { # waits for a job returned by the API and prints its final status
  local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null)
  [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 300); do
    st=$(c GET "/api/jobs/$id" | py 'print(d["status"])'); [ "$st" != running ] && break; sleep 2
  done
  c GET "/api/jobs/$id" | py 'print(d["status"], d.get("error",""))'
}
raid_health() { c GET /api/storage/raid | py 'print(d[0]["health"] if d else "none")' 2>/dev/null; }
wait_api() { for _ in $(seq 1 90); do curl -sf "$B/api/setup" >/dev/null && return; sleep 2; done; }
section() { printf '\n\033[1;36m== %s\033[0m\n' "$1"; }

P=/srv/strata/main/Photos
c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null
for f in "$@"; do
  # shellcheck source=/dev/null
  . "$f"
done
printf '\n%s passed, %s failed\n' "$PASSES" "$FAILS"
[ "$FAILS" -eq 0 ]
