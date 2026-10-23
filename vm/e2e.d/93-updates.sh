# shellcheck shell=bash
# Update channel and automatic updates. A local signed APT repository is built
# in the VM with packaging/make-repo.sh (two suites, like the one on GitHub
# Pages) from two fake "strataos" packages, never installed: 98.0.0 (final:
# stable and testing) and 99.0.0~beta.1 (pre-release: testing only). The
# package's own sources file (absent from test builds) points to it for the
# test. Automatic updates run through the hidden test hook, as simulations:
# apt-get update and apt-get -s (the Debian mirrors must be reachable, as in
# the grouped updates section).
section "Updates: channel and automatic updates"
R=/var/tmp/strata-e2e-repo
SRC=/etc/apt/sources.list.d/strataos.sources
vm "sudo test -f $SRC && sudo cp $SRC /var/tmp/strataos.sources.e2e-orig; sudo rm -rf $R; sudo mkdir -p $R/packaging $R/dist"
vm "command -v apt-ftparchive >/dev/null && command -v gpg >/dev/null || sudo apt-get install -y -qq apt-utils gnupg >/dev/null 2>&1"
vm "echo $(base64 -w0 packaging/make-repo.sh) | base64 -d | sudo tee $R/packaging/make-repo.sh >/dev/null; sudo chmod 755 $R/packaging/make-repo.sh"
for v in 98.0.0 99.0.0~beta.1; do
  vm "d=\$(mktemp -d); mkdir \$d/DEBIAN; printf 'Package: strataos\nVersion: $v\nArchitecture: all\nMaintainer: e2e <e2e@strata.test>\nDescription: e2e test package\n' > \$d/DEBIAN/control; sudo dpkg-deb --root-owner-group -b \$d $R/dist/strataos_${v/\~/-}_all.deb >/dev/null; rm -rf \$d"
done
check "local repository built with make-repo.sh" "$(vm "sudo $R/packaging/make-repo.sh 2>&1 | tail -1")" "stable 1 package(s), testing 2"
check "pre-release only in testing" "$(vm "grep -h '^Version' $R/dist/apt/dists/stable/main/binary-amd64/Packages | tr '\n' ' '; printf '| '; grep -h '^Version' $R/dist/apt/dists/testing/main/binary-amd64/Packages | tr '\n' ' '")" "Version: 98.0.0 | Version: 98.0.0 Version: 99.0.0~beta.1"
vm "printf 'Types: deb\nURIs: file:$R/dist/apt\nSuites: stable\nComponents: main\nSigned-By: $R/packaging/keys/strataos-archive.gpg\n' | sudo tee $SRC >/dev/null"
INSTALLED=$(vm "dpkg-query -W -f '\${Version}' strataos")

check "lists refreshed with the local repository" "$(job "$(c POST /api/system/updates/refresh)")" "success"
check "stable channel by default" "$(c GET /api/system/updates/auto | py 'print(d["channel"], d["repository"], d["beta"])')" "stable True False"
check "stable offers the final release" "$(c GET /api/system/updates/strata | py 'print(d["candidate"])')" "98.0.0"
check "invalid channel refused" "$(c PUT /api/system/updates/channel -d '{"channel":"nightly"}')" "invalid update channel"

check "beta channel chosen (lists refreshed)" "$(job "$(c PUT /api/system/updates/channel -d '{"channel":"beta"}')")" "success"
check "beta sources: same repository, suite testing" "$(vm "grep -E '^(URIs|Suites|Signed-By):' /etc/apt/sources.list.d/strataos-beta.sources | tr '\n' ' '")" "URIs: file:$R/dist/apt Suites: testing Signed-By: $R/packaging/keys/strataos-archive.gpg"
check "package sources (conffile) untouched" "$(vm "grep '^Suites:' $SRC")" "Suites: stable"
check "beta offers the pre-release" "$(c GET /api/system/updates/strata | py 'print(d["candidate"])')" "99.0.0~beta.1"
check "channel shown" "$(c GET /api/system/updates/auto | py 'print(d["channel"])')" "beta"
check "channel change audited" "$(c GET /api/system/events | py 'print(any(e["message"]=="Update channel set to Beta" for e in d[:20]))')" "True"

check "back to the stable channel" "$(job "$(c PUT /api/system/updates/channel -d '{"channel":"stable"}')")" "success"
check "beta sources removed" "$(vm "test -e /etc/apt/sources.list.d/strataos-beta.sources && echo present || echo gone")" "gone"
check "stable offers the final release again" "$(c GET /api/system/updates/strata | py 'print(d["candidate"])')" "98.0.0"
check "no downgrade from a final release" "$(c POST /api/system/updates/downgrade -d '{"version":"98.0.0"}')" "not the latest stable version older"
check "downgrade only to a final version" "$(c POST /api/system/updates/downgrade -d '{"version":"1.0.0;id"}')" "invalid version"

# Automatic updates, through the test hook (a window opens now, simulation).
auto_run() {
  local before st=""
  before=$(c GET /api/system/updates/auto | py 'print(d.get("last", {}).get("started", ""))')
  vm "sudo mkdir -p /run/strata && echo dry-run | sudo tee /run/strata/auto-update-test >/dev/null"
  for _ in $(seq 1 100); do
    st=$(c GET /api/system/updates/auto | py 'l = d.get("last", {}); print(l.get("status", "") if l.get("started", "") != "'"$before"'" else "")')
    case "$st" in success | failed | skipped) break ;; esac
    sleep 3
  done
  echo "$st"
}
RP=$(c GET /api/system/restore-points | py 'print(len(d["points"]))')
check "invalid mode refused" "$(c PUT /api/system/updates/auto -d '{"security":"notify"}')" "invalid automatic update mode"
check "invalid window refused" "$(c PUT /api/system/updates/auto -d '{"system":"install","hour":24}')" "invalid maintenance window"
# A window day that is neither today nor yesterday: no real run during the test.
DAY=$(( ($(vm "date +%w" | tr -dc 0-9) + 3) % 7 ))
check "automatic updates saved" "$(c PUT /api/system/updates/auto -d '{"strata":"install","security":"install","system":"notify","days":['"$DAY"'],"hour":4,"reboot":true}')" '"ok":true'
check "settings and next window" "$(c GET /api/system/updates/auto | py 'print(d["strata"], d["security"], d["system"], d["days"], d["hour"], d["reboot"], "T04:00:00" in d.get("nextWindow", ""))')" "install install notify [$DAY] 4 True True"
check "automatic run (simulation) finished" "$(auto_run)" "success"
LAST=$(c GET /api/system/updates/auto | py 'print(d["last"]["job"])')
check "simulated run recorded" "$(c GET /api/system/updates/auto | py 'l = d["last"]; print(l["dryRun"], l.get("strataTo"), l.get("restart", "") != "done")')" "True 98.0.0 True"
LOG=$(c GET "/api/jobs/$LAST" | py 'print(d["title"], d["status"]); print(d["log"])')
check "simulation job" "$LOG" "Automatic update (simulation) success"
check "no restore point in a simulation" "$LOG" "no restore point is taken"
check "security updates from their own sources and lists" "$LOG" "-o Dir::Etc::SourceList=/dev/null -o Dir::Etc::SourceParts=/var/cache/strata-apt-security/sources.d -o Dir::State::Lists=/var/cache/strata-apt-security/lists"
check "security upgrade simulated" "$(echo "$LOG" | grep -c 'apt-get -s .*Dir::Etc::SourceParts=/var/cache/strata-apt-security/sources.d.* upgrade --with-new-pkgs')" "1"
check "only security suites in the security sources" "$(vm "f=/var/cache/strata-apt-security/sources.d/security.sources; grep -q '^Suites: .*-security' \$f && ! grep '^Suites:' \$f | grep -qv -- '-security' && echo only-security")" "only-security"
check "StrataOS update simulated" "$LOG" "install --only-upgrade strataos"
check "restore points unchanged" "$(c GET /api/system/restore-points | py 'print(len(d["points"]))')" "$RP"
check "nothing installed" "$(vm "dpkg-query -W -f '\${Version}' strataos")" "$INSTALLED"
check "result logged" "$(c GET /api/system/events | py 'print(any(e["message"].startswith("Automatic updates simulated: ") for e in d[:20]))')" "True"

check "notify only" "$(c PUT /api/system/updates/auto -d '{"strata":"notify","days":['"$DAY"'],"hour":4}')" '"ok":true'
check "automatic run (notify only) finished" "$(auto_run)" "success"
check "StrataOS offered, not installed" "$(c GET /api/system/updates/auto | py 'l = d["last"]; print(l.get("offered"), l.get("strataTo"))')" "98.0.0 None"
check "availability notified" "$(c GET /api/system/events | py 'print(any(e["message"]=="StrataOS 98.0.0 is available: install it from Updates" for e in d[:20]))')" "True"

# Clean up: automatic updates off, the package's sources as they were.
c PUT /api/system/updates/auto -d '{}' >/dev/null
vm "sudo rm -f /etc/apt/sources.list.d/strataos-beta.sources $SRC; sudo test -f /var/tmp/strataos.sources.e2e-orig && sudo mv /var/tmp/strataos.sources.e2e-orig $SRC; sudo rm -rf $R /var/cache/strata-apt-security"
check "lists refreshed after the cleanup" "$(job "$(c POST /api/system/updates/refresh)")" "success"
check "automatic updates off" "$(c GET /api/system/updates/auto | py 'print(d["strata"] == d["security"] == d["system"] == "", "nextWindow" in d)')" "True False"
