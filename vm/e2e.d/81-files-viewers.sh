# shellcheck shell=bash
# File viewers: the PDF requested exactly like the built-in viewer (pdf.js:
# fetch with the session cookie, X-Strata, Range) — the regression of
# {"error":"authentication required"}; Word/Excel files served raw for the
# browser previews; document previews (LibreOffice, about 400 MB downloaded
# once) converting an ODP, a PPTX and a DOCX to PDF; videos and audio of any
# codec played through remux/transcode with seeking; pictures converted;
# archives listed and extracted with traversal, bomb and permission checks.
section "File viewers (PDF, office, media, pictures, archives)"
FV=/srv/strata/main/Viewers
check "share for the viewers" "$(c POST /api/shares -d "{\"name\":\"Viewers\",\"path\":\"$FV\",\"smb\":true,\"browseable\":true,\"permissions\":{\"$USER_NAME\":\"rw\"}}")" '"name":"Viewers"'
vm "sudo chmod 770 $FV && sudo setfacl -m u:debian:rx $FV"  # the test's SSH user cds into it
# up <local file> [name]: uploads into the share as the user.
up() { curl -s -b "$J2" -H 'X-Strata: 1' -F "file=@$1;filename=${2:-$(basename "$1")}" "$B/api/files/upload?path=$FV"; }

# ---- PDF, as pdf.js requests it ----
python3 - "$TMP/doc.pdf" <<'PY'
import sys
objs = [b"<</Type/Catalog/Pages 2 0 R>>", b"<</Type/Pages/Kids[3 0 R]/Count 1>>",
        b"<</Type/Page/Parent 2 0 R/MediaBox[0 0 595 842]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>", None,
        b"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>"]
text = b"BT /F1 24 Tf 72 760 Td (StrataOS viewer test) Tj ET"
objs[3] = b"<</Length %d>>stream\n" % len(text) + text + b"\nendstream"
out = b"%PDF-1.7\n"; offs = []
for i, o in enumerate(objs, 1):
    offs.append(len(out)); out += b"%d 0 obj\n" % i + o + b"\nendobj\n"
x = len(out)
out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1) + b"".join(b"%010d 00000 n \n" % o for o in offs)
out += b"trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, x)
open(sys.argv[1], "wb").write(out)
PY
up "$TMP/doc.pdf" >/dev/null
vm "sudo chmod 600 $FV/doc.pdf"  # readable by root only: the daemon reads it for the user
PDFURL="$B/api/files/download?path=$FV/doc.pdf&inline=1"
check "PDF fetched like the viewer: 200 application/pdf" "$(curl -s -b "$J2" -H 'X-Strata: 1' -H 'Sec-Fetch-Mode: cors' -H 'Sec-Fetch-Site: same-origin' -o "$TMP/got.pdf" -w '%{http_code} %{content_type}' "$PDFURL"; head -c 5 "$TMP/got.pdf")" "200 application/pdf%PDF-"
check "PDF range chunk with the session: 206" "$(curl -s -b "$J2" -H 'X-Strata: 1' -r 0-99 -o /dev/null -w '%{http_code} %{content_type}' "$PDFURL")" "206 application/pdf"
check "no session: authentication required (never shown by the viewer)" "$(curl -s "$PDFURL")" "authentication required"
check "PDF viewer worker and data files served by the NAS" "$(curl -s -o /dev/null -w '%{http_code} ' "$B/pdfjs/standard_fonts/FoxitSerif.pfb"; curl -s -o /dev/null -w '%{http_code}' "$B/pdfjs/wasm/openjpeg.wasm")" "200 200"

# ---- Word and Excel files, read by the browser ----
python3 - "$TMP" <<'PY'
import sys, zipfile
d = sys.argv[1]
ct = '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>'
rels = '<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>'
doc = '<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>StrataOS docx test</w:t></w:r></w:p></w:body></w:document>'
with zipfile.ZipFile(d + "/report.docx", "w") as z:
    z.writestr("[Content_Types].xml", ct); z.writestr("_rels/.rels", rels); z.writestr("word/document.xml", doc)
ns = 'xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:presentation="urn:oasis:names:tc:opendocument:xmlns:presentation:1.0" xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0"'
content = f'<?xml version="1.0" encoding="UTF-8"?><office:document-content {ns} office:version="1.2"><office:body><office:presentation><draw:page draw:name="p1"><draw:frame svg:width="20cm" svg:height="3cm" svg:x="2cm" svg:y="2cm"><draw:text-box><text:p>StrataOS slide test</text:p></draw:text-box></draw:frame></draw:page></office:presentation></office:body></office:document-content>'
manifest = '<?xml version="1.0" encoding="UTF-8"?><manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.2"><manifest:file-entry manifest:full-path="/" manifest:media-type="application/vnd.oasis.opendocument.presentation"/><manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/></manifest:manifest>'
with zipfile.ZipFile(d + "/slides.odp", "w") as z:
    z.writestr(zipfile.ZipInfo("mimetype"), "application/vnd.oasis.opendocument.presentation")  # first, stored
    z.writestr("content.xml", content, zipfile.ZIP_DEFLATED); z.writestr("META-INF/manifest.xml", manifest, zipfile.ZIP_DEFLATED)
PY
printf 'name,size\nalpha,1\nbeta,2\n' > "$TMP/table.csv"
for f in report.docx slides.odp table.csv; do up "$TMP/$f" >/dev/null; done
check "docx served raw to the browser preview" "$(curl -s -b "$J2" -H 'X-Strata: 1' -o "$TMP/raw.docx" -w '%{http_code}' "$B/api/files/download?path=$FV/report.docx"; head -c 2 "$TMP/raw.docx")" "200PK"
check "spreadsheet served raw (csv)" "$(curl -s -b "$J2" -H 'X-Strata: 1' "$B/api/files/download?path=$FV/table.csv")" "alpha,1"
check "viewer capabilities for users" "$(u GET /api/files/viewers | py 'print(d["admin"], "docPreview" in d, "media" in d)')" "False True True"

# ---- Document previews (LibreOffice) ----
check "document previews installed" "$(job "$(c POST /api/system/components/docpreview/install)")" "success"
check "component listed" "$(c GET /api/system/components | py 'print(d["docpreview"])')" "True"
vm "cd /tmp && sudo rm -rf e2e-lo && sudo mkdir e2e-lo && sudo cp $FV/slides.odp e2e-lo/ && cd e2e-lo && sudo HOME=/tmp/e2e-lo soffice --headless --convert-to pptx slides.odp >/dev/null 2>&1; sudo cp slides.pptx $FV/deck.pptx; sudo chown $USER_NAME $FV/deck.pptx"
for f in slides.odp deck.pptx report.docx; do
  check "$f converted to PDF" "$(curl -s -m 180 -b "$J2" -H 'X-Strata: 1' -o "$TMP/conv.pdf" -w '%{http_code} %{content_type}' "$B/api/files/docpreview?path=$FV/$f"; head -c 5 "$TMP/conv.pdf")" "200 application/pdf%PDF-"
done
check "conversion cached (second request fast)" "$(curl -s -b "$J2" -H 'X-Strata: 1' -o /dev/null -w '%{http_code} %{time_total}' "$B/api/files/docpreview?path=$FV/slides.odp" | awk '{print $1, ($2 < 2 ? "fast" : "slow " $2)}')" "200 fast"
check "cache folder private" "$(vm "sudo stat -c '%a %U' /var/cache/strata/previews; sudo find /var/cache/strata/previews -maxdepth 1 -name '*.pdf' -printf '%u %m\n' | sort -u")" "711 root
root 600"
check "LibreOffice ran unprivileged and is gone" "$(vm "id -u strata-preview >/dev/null && echo user; pgrep -c -x soffice.bin || true")" "user
0"
check "no preview of a file the user cannot read" "$(u GET "/api/files/docpreview?path=/srv/strata/tank/media/x.odt")" "error"

# ---- Video and audio of any codec ----
check "media tools present" "$(job "$(c POST /api/system/components/photos/install)")" "success"
printf '1\n00:00:01,000 --> 00:00:03,500\nHello subtitles\n\n2\n00:00:05,000 --> 00:00:07,000\nSecond line\n' > "$TMP/s.srt"
up "$TMP/s.srt" e2e.srt >/dev/null
vm "cd $FV && (sudo ffmpeg -loglevel error -y -f lavfi -i testsrc=size=320x240:rate=25:duration=8 -f lavfi -i sine=f=440:duration=8 -i e2e.srt -map 0 -map 1 -map 2 -c:v libx265 -pix_fmt yuv420p -c:a ac3 -c:s srt clip.mkv 2>/dev/null || sudo ffmpeg -loglevel error -y -f lavfi -i testsrc=size=320x240:rate=25:duration=8 -f lavfi -i sine=f=440:duration=8 -c:v mpeg4 -c:a mp3 clip.avi); sudo ffmpeg -loglevel error -y -f lavfi -i sine=f=330:duration=5 -c:a wmav2 tone.wma; sudo chown $USER_NAME clip.* tone.wma"
CLIP=$(vm "ls $FV/clip.mkv $FV/clip.avi 2>/dev/null | head -1" | tr -d '\r')
check "test video generated" "$CLIP" "$FV/clip."
check "probe: transcoded for a browser without HEVC" "$(u GET "/api/files/probe?path=$CLIP" | py 'p=d["plan"]; print(d["installed"], p["mode"], round(d["duration"]))')" "True transcode 8"
# fetch <url> <file>: the first part of a stream (the client then goes away).
fetch() { curl -s -m 30 -b "$J2" -H 'X-Strata: 1' "$1" 2>/dev/null | head -c 300000 > "$2"; }
mp4ok() { python3 -c "import sys; b=open(sys.argv[1],'rb').read(); print('ftyp' if b[4:8]==b'ftyp' else 'not-mp4', 'moof' if b'moof' in b else 'no-fragment', len(b) > 1000)" "$1"; }
fetch "$B/api/files/play?path=$CLIP" "$TMP/p0.mp4"
check "transcoded stream: fragmented MP4" "$(mp4ok "$TMP/p0.mp4")" "ftyp moof True"
check "stream content type" "$(curl -s -m 30 -b "$J2" -H 'X-Strata: 1' -o /dev/null -w '%{content_type}' "$B/api/files/play?path=$CLIP&start=6")" "video/mp4"
curl -s -m 60 -b "$J2" -H 'X-Strata: 1' -o "$TMP/full0.mp4" "$B/api/files/play?path=$CLIP"
curl -s -m 60 -b "$J2" -H 'X-Strata: 1' -o "$TMP/full6.mp4" "$B/api/files/play?path=$CLIP&start=6"
check "seek with ?start: shorter stream from 6 s" "$(python3 -c "import os,sys; a,b=os.path.getsize(sys.argv[1]),os.path.getsize(sys.argv[2]); print('seek-ok' if 0 < b < a * 0.6 else f'{a} {b}')" "$TMP/full0.mp4" "$TMP/full6.mp4")" "seek-ok"
check "seek: still MP4" "$(mp4ok "$TMP/full6.mp4")" "ftyp moof True"
sleep 2
check "ffmpeg stopped when the client left, ran unprivileged" "$(vm "pgrep -c -x -u strata-preview ffmpeg || true")" "0"
if [ "${CLIP##*.}" = mkv ]; then
  check "HEVC browser: remux only (stream copy)" "$(u GET "/api/files/probe?path=$CLIP&hevc=1" | py 'p=d["plan"]; print(p["mode"], p["copyVideo"])')" "transcode True"
  check "subtitle track listed" "$(u GET "/api/files/probe?path=$CLIP" | py 'print([s["codec"] for s in d["subtitles"]])')" "subrip"
  SUB=$(u GET "/api/files/probe?path=$CLIP" | py 'print(d["subtitles"][0]["index"])')
  check "subtitles as WebVTT" "$(curl -s -b "$J2" -H 'X-Strata: 1' "$B/api/files/subtitle?path=$CLIP&index=$SUB")" "Hello subtitles"
  check "subtitles shifted for a stream started at 4 s" "$(curl -s -b "$J2" -H 'X-Strata: 1' "$B/api/files/subtitle?path=$CLIP&index=$SUB&start=4" | tr '\n' ' ')" "00:00:01.000 --> 00:00:03.000 Second line"
fi
check "audio (WMA): transcoded to AAC" "$(u GET "/api/files/probe?path=$FV/tone.wma" | py 'p=d["plan"]; print(p["mode"], p["audioOnly"], p["type"])')" "transcode True audio/mp4"
fetch "$B/api/files/play?path=$FV/tone.wma" "$TMP/a.mp4"
check "audio stream is MP4" "$(mp4ok "$TMP/a.mp4")" "ftyp"
vm "sudo ffmpeg -loglevel error -y -f lavfi -i testsrc=size=320x240:duration=2 -pix_fmt yuv420p -c:v libx264 $FV/native.mp4; sudo chown $USER_NAME $FV/native.mp4"
check "H.264 MP4: played natively (Range)" "$(u GET "/api/files/probe?path=$FV/native.mp4" | py 'print(d["plan"]["mode"])'; curl -s -b "$J2" -H 'X-Strata: 1' -r 0-9 -o /dev/null -w '%{http_code}' "$B/api/files/play?path=$FV/native.mp4")" "native
206"
check "not a media file: clear error" "$(u GET "/api/files/probe?path=$FV/table.csv")" "error"

# ---- Pictures ----
vm "cd $FV && sudo ffmpeg -loglevel error -y -f lavfi -i testsrc=size=1200x800 -frames:v 1 pic.tiff && sudo ffmpeg -loglevel error -y -f lavfi -i testsrc=size=640x480 -frames:v 1 pic.ppm; sudo chown $USER_NAME pic.*"
check "TIFF converted (WebP keeps transparency)" "$(curl -s -m 60 -b "$J2" -H 'X-Strata: 1' -o "$TMP/pic" -w '%{http_code} %{content_type}' "$B/api/files/image?path=$FV/pic.tiff&size=600"; python3 -c "import sys; print(' RIFF' if open(sys.argv[1],'rb').read(4)==b'RIFF' else ' ?')" "$TMP/pic")" "200 image/webp RIFF"
check "PPM converted to JPEG" "$(curl -s -m 60 -b "$J2" -H 'X-Strata: 1' -o "$TMP/pic" -w '%{http_code} %{content_type}' "$B/api/files/image?path=$FV/pic.ppm"; python3 -c "import sys; print(' JPEG' if open(sys.argv[1],'rb').read(2)==b'\xff\xd8' else ' ?')" "$TMP/pic")" "200 image/jpeg JPEG"
printf '<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><script>alert(1)</script><rect width="10" height="10"/></svg>' > "$TMP/x.svg"
up "$TMP/x.svg" >/dev/null
check "SVG inline as a picture, scripts sandboxed" "$(curl -s -b "$J2" -D - -o /dev/null "$B/api/files/download?path=$FV/x.svg&inline=1" | tr -d '\r' | grep -iE '^(content-type|content-security-policy):' | sort | tr '\n' ' ')" "Content-Security-Policy: sandbox"

# ---- Details and text ----
check "details: owner, type, text sniffed" "$(u GET "/api/files/info?path=$FV/e2e.srt" | py 'print(d["owner"], d["text"], d["mime"].split(";")[0])')" "$USER_NAME True text/plain"
check "SHA-256 on demand" "$(u GET "/api/files/hash?path=$FV/e2e.srt" | py 'print(d["sha256"])')" "$(sha256sum "$TMP/s.srt" | cut -d' ' -f1)"
vm "sudo seq 1 3000000 | sudo tee $FV/big.log >/dev/null"
check "large log: tail from a whole line" "$(u GET "/api/files/tail?path=$FV/big.log&bytes=4096" | py 'c=d["content"]; print(c.split("\n")[0].isdigit(), c.rstrip().split("\n")[-1], d["offset"] > 0)')" "True 3000000 True"

# ---- Archives ----
python3 - "$TMP" <<'PY'
import sys, zipfile
d = sys.argv[1]
with zipfile.ZipFile(d + "/t.zip", "w") as z:
    z.writestr("ok/a.txt", "from the archive"); z.writestr("../evil.txt", "escaped"); z.writestr("/abs-evil.txt", "escaped")
    i = zipfile.ZipInfo("link"); i.external_attr = 0o120777 << 16; z.writestr(i, "/etc/passwd")
with zipfile.ZipFile(d + "/bomb.zip", "w", zipfile.ZIP_DEFLATED) as z:
    with z.open("zeros", "w", force_zip64=True) as f:
        block = bytes(1 << 20)
        for _ in range(1300): f.write(block)
PY
up "$TMP/t.zip" >/dev/null
up "$TMP/bomb.zip" >/dev/null
check "zip listed, unsafe entries flagged" "$(u GET "/api/files/archive?path=$FV/t.zip" | py 'print(d["files"], sorted(e["skipped"] for e in d["entries"] if e.get("skipped")))')" "1 ['link', 'unsafe name', 'unsafe name']"
check "extract all: job succeeds" "$(job "$(u POST /api/files/archive/extract -d "{\"path\":\"$FV/t.zip\"}")")" "success"
check "extracted into a new folder, owned by the user" "$(vm "sudo cat $FV/t/ok/a.txt; echo; sudo stat -c %U $FV/t/ok/a.txt")" "from the archive
$USER_NAME"
check "traversal, absolute path and link refused" "$(vm "sudo ls $FV/t; sudo test -e /srv/strata/main/evil.txt && echo LEAK; sudo test -e /abs-evil.txt && echo LEAK; sudo test -e $FV/evil.txt && echo LEAK; echo done")" "ok
done"
check "one member into the folder" "$(job "$(u POST /api/files/archive/extract -d "{\"path\":\"$FV/t.zip\",\"entries\":[\"ok/a.txt\"]}")"; vm "sudo cat $FV/a.txt")" "from the archive"
check "archive bomb stopped and cleaned" "$(job "$(u POST /api/files/archive/extract -d "{\"path\":\"$FV/bomb.zip\"}")"; vm "sudo test -e $FV/bomb && echo LEFT; echo checked")" "failed"
check "bomb leaves nothing behind" "$(vm "sudo test -e $FV/bomb && echo LEFT; echo checked")" "checked"
vm "sudo mkdir -p /srv/strata/main/ViewersRO && echo ro | sudo tee /srv/strata/main/ViewersRO/r.txt >/dev/null && cd /srv/strata/main/ViewersRO && sudo python3 -c \"import zipfile; z=zipfile.ZipFile('r.zip','w'); z.writestr('r2.txt','x'); z.close()\""
c POST /api/shares -d "{\"name\":\"ViewersRO\",\"path\":\"/srv/strata/main/ViewersRO\",\"smb\":true,\"browseable\":true,\"permissions\":{\"$USER_NAME\":\"ro\"}}" >/dev/null
check "read-only share: extraction refused" "$(u POST /api/files/archive/extract -d '{"path":"/srv/strata/main/ViewersRO/r.zip"}')" "error"
check "archive outside the user's shares refused" "$(u GET "/api/files/archive?path=/srv/strata/tank/media/x.zip")" "error"
if vm "command -v 7z" >/dev/null; then
  vm "cd $FV && sudo 7z a -bd e2e.7z e2e.srt >/dev/null 2>&1; sudo chown $USER_NAME e2e.7z"
  check "7z archive listed through 7-Zip" "$(u GET "/api/files/archive?path=$FV/e2e.7z" | py 'print(d["format"], [e["name"] for e in d["entries"]])')" "7z ['e2e.srt']"
fi

c DELETE /api/shares/Viewers >/dev/null
c DELETE /api/shares/ViewersRO >/dev/null
vm "sudo rm -rf $FV /srv/strata/main/ViewersRO /tmp/e2e-lo"
