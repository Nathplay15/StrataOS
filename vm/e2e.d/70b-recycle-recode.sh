# Recycle bin item by item (0.12.1): files deleted over SMB (keeptree) and
# from the web come back at their original place, into the folders that
# still exist; other accounts' bins stay out of reach; video compression
# replaces a video only with a checked H.265 copy that keeps owner and ACL,
# the original going to the recycle bin.
section "Recycle bin: restore in place, video compression"
SP=/srv/strata/main/Photos
smb() { vm "command -v smbclient >/dev/null || sudo apt-get install -y -qq smbclient >/dev/null 2>&1; smbclient //localhost/Photos -U '$USER_NAME%$USER_PASS' -c '$1' 2>&1"; }
bin() { u GET "/api/files/recycle?share=$SP" | py "$1"; }
vm "printf one > /tmp/a.txt; printf two > /tmp/b.txt"
smb "mkdir e2e-docs; cd e2e-docs; put /tmp/a.txt a.txt; put /tmp/b.txt b.txt; mkdir sub; cd sub; put /tmp/a.txt c.txt" >/dev/null
smb "cd e2e-docs; del a.txt; cd sub; del c.txt; cd ..; rmdir sub" >/dev/null
check "SMB deletions listed one by one at their place" "$(bin 'print(sorted(i["origin"].split("/Photos/")[1] for i in d["items"] if "e2e-docs" in i["origin"]))')" "['e2e-docs/a.txt', 'e2e-docs/sub/c.txt']"
check "deletion date of today" "$(bin 'import time; print(all(time.time()-time.mktime(time.strptime(i["deleted"][:19],"%Y-%m-%dT%H:%M:%S"))<86400*2 for i in d["items"]))')" "True"
P=$(bin 'print([i["path"] for i in d["items"] if i["origin"].endswith("e2e-docs/a.txt")][0])')
check "restored into the existing folder" "$(u POST /api/files/recycle/restore -d "{\"paths\":[\"$P\"]}")" "$SP/e2e-docs/a.txt"
check "no copy of the folder made" "$(vm "sudo ls $SP | grep -c '^e2e-docs'; sudo cat $SP/e2e-docs/a.txt $SP/e2e-docs/b.txt")" "1
onetwo"
P=$(bin 'print([i["path"] for i in d["items"] if i["origin"].endswith("e2e-docs/sub/c.txt")][0])')
check "deleted subfolder recreated on restore" "$(u POST /api/files/recycle/restore -d "{\"paths\":[\"$P\"]}" >/dev/null; vm "sudo cat $SP/e2e-docs/sub/c.txt")" "one"
smb "cd e2e-docs; del b.txt" >/dev/null
vm "printf live | sudo tee $SP/e2e-docs/b.txt >/dev/null"
P=$(bin 'print([i["path"] for i in d["items"] if i["origin"].endswith("e2e-docs/b.txt")][0])')
check "a name taken again gets a new one" "$(u POST /api/files/recycle/restore -d "{\"paths\":[\"$P\"]}")" "e2e-docs/b (restored).txt"
check "the live file is kept" "$(vm "sudo cat $SP/e2e-docs/b.txt")" "live"

# A folder deleted from the web: its old files are dated by the deletion.
vm "sudo mkdir -p $SP/e2e-old && printf x | sudo tee $SP/e2e-old/old.txt >/dev/null && sudo touch -d 2020-01-01 $SP/e2e-old/old.txt && sudo chown -R $USER_NAME $SP/e2e-old"
u POST /api/files/trash -d "{\"paths\":[\"$SP/e2e-old\"]}" >/dev/null
check "files of a deleted folder dated by the deletion" "$(bin 'import time; print([time.time()-time.mktime(time.strptime(i["deleted"][:19],"%Y-%m-%dT%H:%M:%S"))<86400*2 for i in d["items"] if i["origin"].endswith("e2e-old/old.txt")])')" "[True]"
P=$(bin 'print([i["path"] for i in d["items"] if i["origin"].endswith("e2e-old/old.txt")][0])')
check "its own date kept on restore" "$(u POST /api/files/recycle/restore -d "{\"paths\":[\"$P\"]}" >/dev/null; vm "sudo date -r $SP/e2e-old/old.txt +%Y")" "2020"

# Another account's bin: neither listed nor reachable.
vm "printf adm | sudo tee $SP/e2e-adm.txt >/dev/null"
c POST /api/files/trash -d "{\"paths\":[\"$SP/e2e-adm.txt\"]}" >/dev/null
check "only my own bin listed in .recycle" "$(u GET "/api/files?path=$SP/.recycle&hidden=1" | py 'print([e["name"] for e in d["entries"]])')" "['$USER_NAME']"
check "another account's bin refused" "$(u GET "/api/files?path=$SP/.recycle/$ADMIN_USER&hidden=1")" "access denied"
check "another account's deleted file refused" "$(u GET "/api/files/download?path=$SP/.recycle/$ADMIN_USER/e2e-adm.txt")" "access denied"
c POST /api/files/recycle/empty -d "{\"share\":\"$SP\"}" >/dev/null
u POST /api/files/recycle/empty -d "{\"share\":\"$SP\"}" >/dev/null
vm "sudo rm -rf $SP/e2e-docs $SP/e2e-old /tmp/a.txt /tmp/b.txt"

# Video compression: a lossless H.264 clip owned by the user, with an ACL.
vm "command -v ffmpeg >/dev/null || sudo apt-get install -y -qq ffmpeg acl >/dev/null 2>&1; sudo mkdir -p $SP/e2e-vid && sudo ffmpeg -v error -y -f lavfi -i testsrc2=size=320x240:rate=25:duration=3 -c:v libx264 -qp 0 $SP/e2e-vid/clip.mp4 && sudo chown $USER_NAME:famille $SP/e2e-vid/clip.mp4 && sudo chmod 640 $SP/e2e-vid/clip.mp4 && sudo setfacl -m u:nobody:r $SP/e2e-vid/clip.mp4"
SUM=$(vm "sudo sha256sum $SP/e2e-vid/clip.mp4 | cut -c1-64")
# The shares have default ACLs: the group bits shown are the ACL mask.
META=$(vm "sudo stat -c '%U:%G %a' $SP/e2e-vid/clip.mp4")
check "video compression job" "$(job "$(c POST /api/storagetools/recode -d "{\"path\":\"$SP/e2e-vid\",\"minSize\":1}")")" "success"
check "video compressed" "$(c GET /api/storagetools/recode | py 'print([f["status"] for f in d["report"]["files"]])')" "['recoded']"
check "now H.265, same owner, mode and ACL" "$(vm "sudo ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 $SP/e2e-vid/clip.mp4; sudo stat -c '%U:%G %a' $SP/e2e-vid/clip.mp4; sudo getfacl -cp $SP/e2e-vid/clip.mp4 | grep nobody")" "hevc
$META
user:nobody:r--"
check "original kept intact in the recycle bin" "$(vm "sudo sha256sum $SP/.recycle/$ADMIN_USER/e2e-vid/clip.mp4 | cut -c1-64")" "$SUM"
c POST /api/files/recycle/empty -d "{\"share\":\"$SP\"}" >/dev/null
vm "sudo rm -rf $SP/e2e-vid"
