# shellcheck shell=bash
# Smart photos (0.12): GPS places, memories, the photoml component (ONNX
# Runtime and models downloaded and checked by SHA-256, ~190 MB), faces
# grouped into people per user, things, search by description, and the
# rights of a user who cannot read one of the folders. Sample photos:
# vm/testdata/photos (public domain / CC0, see LICENSE.md there).
section "Smart photos"
SP="$P/SmartSamples"
SV=/srv/strata/main/SmartVault
up() { curl -s -b "$J" -H 'X-Strata: 1' -F "file=@$1" "$B/api/files/upload?path=$2" >/dev/null; }
c POST /api/files/mkdir -d "{\"path\":\"$SP\"}" >/dev/null
c POST /api/shares -d "{\"name\":\"SmartVault\",\"path\":\"$SV\",\"smb\":true,\"permissions\":{}}" >/dev/null
for f in vm/testdata/photos/*.jpg; do up "$f" "$SP"; done
up vm/testdata/photos/biden-portrait.jpg "$SV"
up vm/testdata/photos/biden-2013.jpg "$SV"
check "media tools" "$(job "$(c POST /api/system/components/photos/install)")" "success"
# A picture taken on this day three years ago, for the memories.
vm "sudo cp $SP/dog.jpg $SP/dog-memory.jpg && sudo exiftool -q -overwrite_original -DateTimeOriginal='$(date -d '-3 years' '+%Y:%m:%d 12:00:00')' $SP/dog-memory.jpg"
OLD=$(c GET /api/photos | py 'print(d.get("folders", []))')
check "photo folders" "$(job "$(c PUT /api/photos/settings -d "{\"folders\":[\"$P\",\"$SV\"]}")")" "success"

check "places from the GPS positions" "$(c GET /api/photos/places | py 'print(sorted((p["city"], p["country"], p["count"]) for p in d))')" "('Washington', 'US', 4)"
check "a town of Guam for the beach" "$(c GET /api/photos/places | py 'print([p["country"] for p in d])')" "GU"
PARIS=$(c GET /api/photos/places | py 'print([p["id"] for p in d if p["city"]=="Paris"][0])')
check "photos of a place" "$(c GET "/api/photos?place=$PARIS" | py 'print([i["path"].split("/")[-1] for i in d["items"]])')" "['car.jpg']"
check "memories: on this day" "$(c GET /api/photos/memories | py 'print([(m["yearsAgo"], m["count"]) for m in d])')" "(3, 1)"

check "smart features refused before the install" "$(c PUT /api/photos/smart -d '{"enabled":true}')" "install the smart photos component first"
check "runtime and models installed (checksums)" "$(job "$(c POST /api/system/components/photoml/install)")" "success"
check "component listed" "$(c GET /api/system/components | py 'print(d["photoml"])')" "True"
check "invalid hours refused" "$(c PUT /api/photos/smart -d '{"enabled":true,"limits":{"hours":"25:00-02:00"}}')" "hours must be like"
check "smart features enabled" "$(c PUT /api/photos/smart -d '{"enabled":true,"limits":{"anyLoad":true,"threads":2}}')" "ok"
for _ in $(seq 1 120); do
  S=$(c GET /api/photos/smart)
  [ "$(echo "$S" | py 'print(d["counts"]["photos"] >= 12 and not d["running"])')" = True ] && break
  sleep 5
done
check "every photo analysed" "$(echo "$S" | py 'print(d["counts"]["photos"], d["counts"]["failed"], d["provider"])')" "12 0 CPU"
check "faces found" "$(echo "$S" | py 'print(d["counts"]["faces"] >= 9)')" "True"
check "worker runs as the sandbox user, niced" "$(vm "ps -o user=,ni= -C python3 | head -1")" "strata-p"

# People of the administrator: Obama in 4 photos, Biden in 3 (one is profile).
PEOPLE=$(c GET /api/photos/people)
check "a person in several photos" "$(echo "$PEOPLE" | py 'print(max(p["count"] for p in d) >= 4)')" "True"
OB=$(echo "$PEOPLE" | py 'print(max(d, key=lambda p: p["count"])["id"])')
check "photos of the person" "$(c GET "/api/photos?person=$OB" | py 'print(sorted(i["path"].split("/")[-1] for i in d["items"]))')" "obama-desk.jpg"
check "person named" "$(c PUT "/api/photos/people/$OB" -d '{"name":"Barack"}')" "ok"
check "name kept" "$(c GET /api/photos/people | py "print([p['name'] for p in d if p['id']==$OB])")" "['Barack']"
check "face thumbnail" "$(curl -s -b "$J" -o /dev/null -w '%{http_code} %{content_type}' "$B/api/photos/face?id=$(echo "$PEOPLE" | py "print([p['cover']['id'] for p in d if p['id']==$OB][0])")")" "200 image/jpeg"
FACES=$(c GET "/api/photos/people/$OB/faces" | py 'print(",".join(str(f["id"]) for f in d))')
F1=${FACES%%,*}
NEWP=$(c POST /api/photos/faces/move -d "{\"faces\":[$F1],\"to\":0,\"name\":\"Twin\"}" | py 'print(d["person"])')
check "face split off into a new person" "$(c GET /api/photos/people | py "print([p['name'] for p in d if p['id']==$NEWP])")" "['Twin']"
check "merged back" "$(c POST "/api/photos/people/$OB/merge" -d "{\"from\":[$NEWP]}")" "ok"
check "one person again" "$(c GET /api/photos/people | py "print(len([p for p in d if p['id'] in ($OB, $NEWP)]), [p['count'] for p in d if p['id']==$OB][0] >= 4)")" "1 True"
check "person hidden" "$(c PUT "/api/photos/people/$OB" -d '{"hidden":true}' >/dev/null; c GET /api/photos/people | py "print($OB in [p['id'] for p in d])")" "False"
c PUT "/api/photos/people/$OB" -d '{"hidden":false}' >/dev/null

# Things and search by description.
check "things recognised" "$(c GET /api/photos/tags | py 'print(sorted(t["tag"] for t in d))')" "'dog'"
check "beach recognised" "$(c GET /api/photos/tags | py 'print([t["tag"] for t in d])')" "beach"
check "photos of a thing" "$(c GET '/api/photos?tag=car' | py 'print([i["path"].split("/")[-1] for i in d["items"]])')" "car.jpg"
check "search: dog first" "$(c GET '/api/photos/search?q=a%20golden%20dog' | py 'print(d["smart"], d["items"][0]["path"].split("/")[-1])')" "True dog"
check "search: beach first" "$(c GET '/api/photos/search?q=sand%20and%20sea' | py 'print(d["items"][0]["path"].split("/")[-1])')" "beach.jpg"
check "search: nothing for an absent thing" "$(c GET '/api/photos/search?q=giraffe' | py 'print(len(d["items"]))')" "0"

# A user: only the photos of the shares they can read, and their own people.
check "user sees no vault photo" "$(u GET /api/photos | py 'print(any("SmartVault" in i["path"] for i in d["items"]))')" "False"
UPEOPLE=$(u GET /api/photos/people)
check "user has people of their own, without the admin's names" "$(echo "$UPEOPLE" | py 'print(len(d) > 0, any(p["name"] for p in d))')" "True False"
check "user's people only in readable photos" "$(echo "$UPEOPLE" | py 'print(sum(p["count"] for p in d) <= 9)')" "True"
VF=$(vm "sudo python3 -c \"import sqlite3; print(sqlite3.connect('/var/lib/strata/photos/ml.db').execute(\\\"select id from faces where path like '%SmartVault%' limit 1\\\").fetchone()[0])\"" | tr -d '\r')
check "face of a vault photo refused to the user" "$(u GET "/api/photos/face?id=$VF")" "face not found"
check "someone else's person cannot be renamed" "$(u PUT "/api/photos/people/$OB" -d '{"name":"x"}')" "person not found"
check "vault photos never in the user's search" "$(u GET '/api/photos/search?q=a%20man%20in%20a%20suit' | py 'print(any("SmartVault" in i["path"] for i in d["items"]))')" "False"
check "settings for administrators only" "$(u PUT /api/photos/smart -d '{"enabled":false}')" "administrator"

# Clean up.
c PUT /api/photos/smart -d '{"enabled":false}' >/dev/null
c POST /api/photos/smart/reset >/dev/null
c POST /api/files/delete -d "{\"paths\":[\"$SP\"]}" >/dev/null
c DELETE /api/shares/SmartVault >/dev/null
vm "sudo rm -rf $SV"
c PUT /api/photos/settings -d "{\"folders\":$(echo "$OLD" | tr "'" '"')}" >/dev/null
