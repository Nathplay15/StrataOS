# Self-service restore (0.12.1, PR #53): a user browses the snapshots of a
# share and restores files and folders, owned by them, never overwriting; a
# link kept in the snapshot (A/link -> ../B) never leads into another share.
section "Self-service restore from snapshots"
FA=/srv/strata/fast/SnapA FB=/srv/strata/fast/SnapB
c POST /api/shares -d "{\"name\":\"SnapA\",\"path\":\"$FA\",\"smb\":true,\"browseable\":true,\"permissions\":{\"$USER_NAME\":\"rw\"}}" >/dev/null
c POST /api/shares -d "{\"name\":\"SnapB\",\"path\":\"$FB\",\"smb\":true,\"browseable\":true,\"permissions\":{}}" >/dev/null
vm "sudo mkdir -p $FA/docs/deep $FB && echo mine | sudo tee $FA/docs/a.txt >/dev/null && echo deep | sudo tee $FA/docs/deep/d.txt >/dev/null \
  && echo secret-of-b | sudo tee $FB/secret.txt >/dev/null && sudo ln -sfn ../SnapB $FA/link && sudo mkdir -p /srv/strata/fast/.snapshots \
  && sudo btrfs subvolume snapshot -r /srv/strata/fast /srv/strata/fast/.snapshots/root@\$(date +%Y%m%d-%H%M%S) >/dev/null \
  && sudo rm -rf $FA/docs/a.txt $FA/docs/deep"
SS=$(u GET "/api/files/snapshots?path=$FA" | py 'print(d["points"][0]["snapshot"] if d["points"] else "")')
check "snapshot timeline of the share" "$SS" "root@"
check "snapshot browsed" "$(u GET "/api/files/snapshots/browse?path=$FA/docs&snapshot=$SS" | py 'print([e["name"] for e in d["entries"]], d["exists"])')" "['deep', 'a.txt'] True"
check "link to another share not followed (browse)" "$(u GET "/api/files/snapshots/browse?path=$FA/link&snapshot=$SS")" "symbolic links are not followed"
check "link to another share not followed (download)" "$(curl -s -b "$J2" "$B/api/files/snapshots/download?path=$FA/link&snapshot=$SS&name=secret.txt")" "symbolic links are not followed"
check "link to another share not followed (single file)" "$(curl -s -b "$J2" "$B/api/files/versions/download?path=$FA/link/secret.txt&snapshot=$SS")" "symbolic links are not followed"
check "link to another share not restored" "$(u POST /api/files/snapshots/restore -d "{\"path\":\"$FA\",\"snapshot\":\"$SS\",\"names\":[\"link\"]}")" "symbolic links are not followed"
check "other share refused" "$(u GET "/api/files/snapshots/browse?path=$FB&snapshot=$SS")" "access denied"
check "file downloaded" "$(curl -s -b "$J2" "$B/api/files/snapshots/download?path=$FA/docs&snapshot=$SS&name=a.txt")" "mine"
check "file restored" "$(u POST /api/files/snapshots/restore -d "{\"path\":\"$FA/docs\",\"snapshot\":\"$SS\",\"names\":[\"a.txt\"]}" | py 'print(d["restored"])')" "['a.txt']"
check "restored file belongs to the user" "$(vm "sudo stat -c %U:%s $FA/docs/a.txt")" "$USER_NAME:5"
check "folder restored by a job" "$(job "$(u POST /api/files/snapshots/restore -d "{\"path\":\"$FA/docs\",\"snapshot\":\"$SS\",\"names\":[\"deep\"]}")")" "success"
check "every restored item belongs to the user" "$(vm "sudo stat -c %U $FA/docs/deep $FA/docs/deep/d.txt | sort -u")" "$USER_NAME"
job "$(u POST /api/files/snapshots/restore -d "{\"path\":\"$FA/docs\",\"snapshot\":\"$SS\",\"names\":[\"deep\"]}")" >/dev/null
job "$(u POST /api/files/snapshots/restore -d "{\"path\":\"$FA/docs\",\"snapshot\":\"$SS\",\"names\":[\"deep\"]}")" >/dev/null
check "folder restored again as complete copies" "$(vm "sudo find $FA/docs -name d.txt | wc -l; sudo ls $FA/docs | grep -c '^deep (version of .*) (2)\$'")" "3
1"
check "no write to a read-only share" "$(u POST /api/files/snapshots/restore -d "{\"path\":\"$FB\",\"snapshot\":\"$SS\",\"names\":[\"secret.txt\"]}")" "access denied"
vm "sudo btrfs subvolume delete /srv/strata/fast/.snapshots/$SS >/dev/null; sudo rm -rf $FA $FB"
c DELETE /api/shares/SnapA >/dev/null
c DELETE /api/shares/SnapB >/dev/null
