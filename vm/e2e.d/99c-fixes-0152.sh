# shellcheck shell=bash
# 0.15.2 fixes: symlinks planted in a share never make the daemon read or
# write outside it (subtitle sidecars, photo editor), the automatic upload
# never overwrites, the cinema scans the shares of the volumes and filters by
# what each account reads (age limits server-side), the HDMI kiosk acts as a
# chosen account on the cinema routes only, CalDAV/CardDAV with real
# discovery, VCALENDAR objects, ETags and If-Match, and a migration mirror is
# refused into a share root.
section "0.15.2 fixes (symlinks, upload, cinema, kiosk, CalDAV)"
F=/srv/strata/main/Fix152
check "share for the fixes" "$(c POST /api/shares -d "{\"name\":\"Fix152\",\"path\":\"$F\",\"smb\":true,\"browseable\":true,\"permissions\":{\"$USER_NAME\":\"rw\"}}")" '"name":"Fix152"'
vm "sudo chmod 770 $F"
SHADOW=$(vm "sudo md5sum /etc/shadow | cut -c1-32")

# ---- Subtitle sidecars ----
vm "sudo sh -c 'printf video > \"$F/m.mkv\"; ln -sf /etc/shadow \"$F/m.en.vtt\"; printf \"1\n00:00:01,000 --> 00:00:02,000\nhello\n\" > \"$F/m.srt\"; ln -sf /dev/zero \"$F/m.fr.vtt\"'"
out=$(u GET "/api/files/subtitle?path=$F/m.mkv&index=1000")
check "a sidecar linked to /etc/shadow is not read" "$(echo "$out" | grep -c 'root:') $out" "0 {\"error\""
out=$(timeout 20 curl -s -b "$J2" -H 'X-Strata: 1' "$B/api/files/subtitle?path=$F/m.mkv&index=1001")
check "a sidecar linked to /dev/zero is refused at once" "$out" "error"
check "a real sidecar is served as WebVTT" "$(u GET "/api/files/subtitle?path=$F/m.mkv&index=1002")" "00:00:01.000"

# ---- Photo editor ----
vm "sudo ln -sf /etc/shadow $F/pic.jpg"
check "photo editor refuses a link out of the share" "$(u POST /api/photos/edit -d "{\"path\":\"$F/pic.jpg\",\"rotate\":90}")" "error"
check "/etc/shadow untouched" "$(vm "sudo md5sum /etc/shadow | cut -c1-32")" "$SHADOW"

# ---- Automatic upload: never overwrites ----
printf 'first photo' > "$TMP/IMG_1.jpg"
upk() { curl -s -b "$J2" -H 'X-Strata: 1' -F "file=@$1;filename=IMG_1.jpg" "$B/api/files/upload?keep=1&path=$F"; }
upk "$TMP/IMG_1.jpg" >/dev/null
check "same photo again: skipped" "$(upk "$TMP/IMG_1.jpg")" '"skipped":1'
printf 'second photo' > "$TMP/IMG_1b.jpg"
upk "$TMP/IMG_1b.jpg" >/dev/null
check "another photo with the same name: kept beside it" "$(vm "sudo cat '$F/IMG_1.jpg'; echo; sudo cat '$F/IMG_1 (2).jpg'" | tr '\n' '|')" "first photo|second photo"

# ---- Cinema: shares of the volumes, filtered per account ----
P2=/srv/strata/main/Private152
check "share alice cannot read" "$(c POST /api/shares -d "{\"name\":\"Private152\",\"path\":\"$P2\",\"smb\":true,\"permissions\":{}}")" '"name":"Private152"'
vm "sudo sh -c 'printf x > \"$F/Heat (1995).mkv\"; printf x > \"$P2/Secret (2001).mkv\"'"
c POST /api/cinema/scan >/dev/null
for _ in $(seq 1 30); do [ "$(c GET /api/cinema | py 'print(d["running"])')" = False ] && break; sleep 2; done
check "the scan finds the videos of shares on the volume" "$(c GET /api/cinema | py 'print(sorted(i["path"].split("/")[-1] for i in d["items"] if "152/" in i["path"]))')" "['Heat (1995).mkv', 'Secret (2001).mkv']"
check "alice only sees the videos of her shares" "$(u GET /api/cinema | py 'print([i["path"].split("/")[-1] for i in d["items"] if "152/" in i["path"]])')" "['Heat (1995).mkv']"
check "resume point refused for a video she cannot see" "$(u PUT /api/cinema/position -d "{\"path\":\"$P2/Secret (2001).mkv\",\"at\":5,\"duration\":100}")" "not in the library"

# ---- HDMI kiosk ----
check "kiosk account created by the package" "$(vm "getent passwd strata-kiosk | cut -d: -f1,7")" "strata-kiosk:/usr/sbin/nologin"
vm "sudo sh -c 'test -s /var/lib/strata/hdmi.token || (od -An -tx1 -N32 /dev/urandom | tr -d \" \n\" > /var/lib/strata/hdmi.token; chmod 600 /var/lib/strata/hdmi.token)'"
kiosk() { vm "T=\$(sudo cat /var/lib/strata/hdmi.token); curl -sk -o /dev/null -w '%{http_code}' -H \"Authorization: Bearer hdmi:\$T\" 'https://127.0.0.1:7443$1'"; }
check "kiosk without an account chosen: nothing" "$(kiosk /api/cinema)" "403"
c PUT /api/system/hdmi -d "{\"enabled\":false,\"account\":\"$USER_NAME\"}" >/dev/null
check "kiosk reads the cinema of the chosen account" "$(kiosk /api/cinema)" "200"
check "kiosk sees that account's library only" "$(vm "T=\$(sudo cat /var/lib/strata/hdmi.token); curl -sk -H \"Authorization: Bearer hdmi:\$T\" https://127.0.0.1:7443/api/cinema" | py 'print([i["path"].split("/")[-1] for i in d["items"] if "152/" in i["path"]])')" "['Heat (1995).mkv']"
check "kiosk cannot list files, read settings or users" "$(kiosk '/api/files?path=/srv/strata'; echo; kiosk /api/settings; echo; kiosk /api/users)" "403
403
403"
check "kiosk token refused from the network" "$(curl -sk -o /dev/null -w '%{http_code}' -H "Authorization: Bearer hdmi:$(vm 'sudo cat /var/lib/strata/hdmi.token')" "$B/api/cinema")" "401"
check "TV remote refused to an account not allowed" "$(u POST /api/hdmi/remote/key -d '{"key":"ok"}')" "not allowed"
c PUT /api/system/hdmi -d '{"enabled":false,"account":"","remote":[]}' >/dev/null

# ---- CalDAV / CardDAV ----
c GET /api/fileservices | py 'x=d["config"]; x["calDAV"]=True; print(json.dumps(x))' > "$TMP/fs.json"
c PUT /api/fileservices -d @"$TMP/fs.json" >/dev/null
dav() { local m=$1 p=$2; shift 2; curl -s -u "$USER_NAME:$USER_PASS" -X "$m" "$B$p" "$@"; }
check "well-known redirects to the CalDAV root" "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "$B/.well-known/caldav")" "301 $B/caldav/"
check "discovery: the account's principal" "$(dav PROPFIND /caldav/ -H 'Depth: 0' -H 'Content-Type: application/xml' --data '<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>')" "/caldav/$USER_NAME/"
EV="BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//e2e//EN\r\nBEGIN:VTIMEZONE\r\nTZID:Europe/Paris\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\nBEGIN:VEVENT\r\nUID:e2e-event-1\r\nDTSTAMP:20261120T100000Z\r\nDTSTART;TZID=Europe/Paris:20261201T100000\r\nDTEND;TZID=Europe/Paris:20261201T110000\r\nSUMMARY:Dentist\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
CAL=/caldav/$USER_NAME/calendars/default
check "PUT an event under a client-chosen name" "$(printf "$EV" | dav PUT "$CAL/7F3A-client-name.ics" -H 'Content-Type: text/calendar' --data-binary @- -o /dev/null -w '%{http_code}')" "201"
got=$(dav GET "$CAL/7F3A-client-name.ics" -D "$TMP/h.txt")
check "GET returns the VCALENDAR with its VTIMEZONE" "$(echo "$got" | grep -c 'BEGIN:VCALENDAR\|BEGIN:VTIMEZONE\|SUMMARY:Dentist')" "3"
ETAG=$(grep -i '^etag:' "$TMP/h.txt" | tr -d '\r' | cut -d' ' -f2)
check "a stale If-Match is refused" "$(printf "$EV" | dav PUT "$CAL/7F3A-client-name.ics" -H 'Content-Type: text/calendar' -H 'If-Match: "0000"' --data-binary @- -o /dev/null -w '%{http_code}')" "412"
check "the current ETag is accepted" "$(printf "$EV" | sed 's/Dentist/Dentist 2/' | dav PUT "$CAL/7F3A-client-name.ics" -H 'Content-Type: text/calendar' -H "If-Match: $ETAG" --data-binary @- -o /dev/null -w '%{http_code}')" "201"
check "time-range REPORT finds it" "$(dav REPORT "$CAL/" -H 'Depth: 1' -H 'Content-Type: application/xml' --data '<?xml version="1.0"?><c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/></d:prop><c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"><c:time-range start="20261130T000000Z" end="20261202T000000Z"/></c:comp-filter></c:comp-filter></c:filter></c:calendar-query>')" "7F3A-client-name.ics"
check "another account cannot read it" "$(curl -s -u "$ADMIN_USER:$ADMIN_PASS" -o /dev/null -w '%{http_code}' "$B$CAL/7F3A-client-name.ics")" "403"
check "CardDAV: a contact under a client-chosen name" "$(printf 'BEGIN:VCARD\r\nVERSION:3.0\r\nUID:e2e-c1\r\nFN:Ada Lovelace\r\nN:Lovelace;Ada;;;\r\nEND:VCARD\r\n' | dav PUT "/carddav/$USER_NAME/addressbooks/default/abc-42.vcf" -H 'Content-Type: text/vcard' --data-binary @- -o /dev/null -w '%{http_code}')" "201"
check "CardDAV REPORT returns address-data in the CardDAV namespace" "$(dav REPORT "/carddav/$USER_NAME/addressbooks/default/" -H 'Depth: 1' -H 'Content-Type: application/xml' --data '<?xml version="1.0"?><card:addressbook-query xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><card:address-data/></d:prop></card:addressbook-query>')" "Ada Lovelace"

# ---- Migration ----
check "mirror (--delete) into a whole share refused" "$(c POST /api/migrations -d '{"name":"m152","kind":"rsync","host":"10.0.2.2","share":"mod","user":"u","dest":"/Fix152","delete":true}')" "inside the share"
vm "sudo rm -rf $F $P2"
c DELETE /api/shares/Fix152 >/dev/null
c DELETE /api/shares/Private152 >/dev/null
