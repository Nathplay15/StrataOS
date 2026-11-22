#!/bin/bash
# The end-to-end test on a fresh VM, as `make vm-test` and CI run it:
#   vm/ci-e2e.sh full  [package.deb]   the whole suite (about an hour)
#   vm/ci-e2e.sh smoke [package.deb]   the smoke subset (E2E_SMOKE=1, about 25 minutes)
# The VM gets the current packages and kernel of Debian first (like a NAS
# kept up to date: the base image lags behind and the headers of its kernel
# leave the archive), then a restart, then the package. The output goes to
# vm/last-test.log. VM_* variables (ports, memory) are passed to vm/vm.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
MODE=${1:-full}
DEB=${2:-dist/strataos_$(cat VERSION)_amd64.deb}
case "$MODE" in
  full) unset E2E_SMOKE ;;
  smoke) export E2E_SMOKE=1 ;;
  *) echo "usage: $0 full|smoke [package.deb]"; exit 2 ;;
esac
[ -f "$DEB" ] || { echo "missing $DEB (make deb)"; exit 1; }
export VM_DISKS=13
./vm/vm.sh destroy || true
./vm/vm.sh create
./vm/vm.sh ssh 'sudo apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get -y -qq -o Dpkg::Options::=--force-confold full-upgrade'
./vm/vm.sh stop
./vm/vm.sh start
./vm/vm.sh deploy "$DEB"
./vm/e2e-test.sh | tee vm/last-test.log
