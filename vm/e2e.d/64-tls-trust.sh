# shellcheck shell=bash
# Self-signed servers (0.11.1): a WebDAV server and a restic REST server with
# a self-signed certificate (rclone serve, so no download is needed). The
# cloud sync and the backup fail with a clear message, work once the
# certificate is trusted, and fail with "certificate … changed" when the
# server gets another one; "ignore certificate errors" works too. The
# webhook notification follows the same rules.
section "Self-signed certificates (trust this certificate)"

TT=/tmp/e2e-tls
TTPASS=$(head -c 12 /dev/urandom | base64 | tr -d '/+=')
# Two certificates for 127.0.0.1 (A, then B for "the certificate changed").
vm "sudo rm -rf $TT && sudo mkdir -p $TT/dav $TT/rest && cd $TT && for k in a b; do sudo openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj /CN=e2e-nas -addext subjectAltName=IP:127.0.0.1 -keyout \$k.key -out \$k.crt 2>/dev/null; done"
FPA=$(vm "sudo openssl x509 -in $TT/a.crt -noout -fingerprint -sha256" | cut -d= -f2 | tr -d ':\r' | tr 'A-F' 'a-f')
# tt_serve <cert a|b>: (re)starts the WebDAV and REST servers with that certificate.
tt_serve() {
  vm "sudo systemctl stop e2e-tt-dav e2e-tt-rest 2>/dev/null; sudo systemctl reset-failed e2e-tt-dav e2e-tt-rest 2>/dev/null
    sudo systemd-run --unit=e2e-tt-dav rclone serve webdav $TT/dav --addr 127.0.0.1:18443 --cert $TT/$1.crt --key $TT/$1.key --user dav --pass '$TTPASS' >/dev/null 2>&1
    sudo systemd-run --unit=e2e-tt-rest rclone serve restic $TT/rest --addr 127.0.0.1:18444 --cert $TT/$1.crt --key $TT/$1.key >/dev/null 2>&1; sleep 2"
}
tt_serve a

# The certificate as the NAS sees it.
INS=$(c POST /api/tls/inspect -d '{"url":"https://127.0.0.1:18443/remote.php/dav"}')
check "inspect: not trusted by default, self-signed" "$(echo "$INS" | py 'print(d["trusted"], d["chain"][0]["selfSigned"], "self-signed" in d["problem"], d.get("nameMismatch",""))')" "False True True "
check "inspect: SHA-256 fingerprint" "$(echo "$INS" | py 'print(d["fingerprint"])')" "$FPA"
PEMA=$(echo "$INS" | py 'print(json.dumps(d["pem"]))')
check "inspect: plain HTTP refused" "$(c POST /api/tls/inspect -d '{"url":"http://127.0.0.1:18443"}' -o /dev/null -w '%{http_code}')" "400"
check "inspect: administrators only" "$(u POST /api/tls/inspect -d '{"url":"https://127.0.0.1:18443"}' -o /dev/null -w '%{http_code}')" "403"

# Cloud sync to the WebDAV server.
TTSRC=/srv/strata/main/e2e-tls-src
vm "sudo mkdir -p $TTSRC && echo hello | sudo tee $TTSRC/hello.txt >/dev/null"
tt_cloud() { # tt_cloud <extra json> -> the task, as saved
  echo "{\"name\":\"e2e-tls-cloud\",\"provider\":\"webdav\",\"params\":{\"url\":\"https://127.0.0.1:18443\",\"vendor\":\"other\",\"user\":\"dav\"},\"secrets\":{\"pass\":\"$TTPASS\"},\"localPath\":\"$TTSRC\",\"remotePath\":\"up\",\"direction\":\"upload\",\"cron\":\"\",\"enabled\":true$1}"
}
TTC=$(c POST /api/cloudsync -d "$(tt_cloud "")" | py 'print(d["id"])')
tt_last() { c GET /api/cloudsync | py "print([t['history'][0]['status']+' '+t['history'][0]['message'] for t in d['tasks'] if t['id']=='$TTC'][0])"; }
job "$(c POST /api/cloudsync/$TTC/run)" >/dev/null
check "cloud sync: self-signed server refused with advice" "$(tt_last)" "Trust this certificate"
check "cloud sync test: same advice" "$(c POST /api/cloudsync/test -d "$(tt_cloud "")")" "Trust this certificate"
c PUT /api/cloudsync/$TTC -d "$(tt_cloud ",\"tlsCert\":$PEMA,\"tlsFingerprint\":\"forged\"")" >/dev/null
check "cloud sync: fingerprint computed by the NAS" "$(c GET /api/cloudsync | py "print([t['tlsFingerprint'] for t in d['tasks'] if t['id']=='$TTC'][0])")" "$FPA"
check "cloud sync: works once trusted" "$(job "$(c POST /api/cloudsync/$TTC/run)")" "success"
check "cloud sync: file on the WebDAV server" "$(vm "sudo cat $TT/dav/up/hello.txt")" "hello"
check "cloud sync: rclone got the CA for this run only" "$(vm "sudo ls /var/lib/strata/tls/ | wc -l")" "1"
tt_serve b
job "$(c POST /api/cloudsync/$TTC/run)" >/dev/null
check "cloud sync: changed certificate reported" "$(tt_last)" "the certificate of 127.0.0.1 changed"
c PUT /api/cloudsync/$TTC -d "$(tt_cloud ",\"tlsInsecure\":true")" >/dev/null
check "cloud sync: ignore certificate errors" "$(job "$(c POST /api/cloudsync/$TTC/run)")" "success"
c DELETE /api/cloudsync/$TTC >/dev/null

# restic backup to the REST server (rclone serve restic).
tt_serve a
tt_restic() {
  echo "{\"name\":\"e2e-tls-restic\",\"kind\":\"restic\",\"sources\":[\"$TTSRC\"],\"destType\":\"rest\",\"destPath\":\"https://127.0.0.1:18444/\",\"password\":\"Tls-Test-Pass\",\"cron\":\"\",\"enabled\":true$1}"
}
TTR=$(c POST /api/backups -d "$(tt_restic "")" | py 'print(d["id"])')
tt_blast() { c GET /api/backups | py "print([b['history'][0]['status']+' '+b['history'][0]['message'] for b in d if b['id']=='$TTR'][0])"; }
job "$(c POST /api/backups/$TTR/run)" >/dev/null
check "restic REST: self-signed server refused with advice" "$(tt_blast)" "Trust this certificate"
c PUT /api/backups/$TTR -d "$(tt_restic ",\"tlsCert\":$PEMA")" >/dev/null
check "restic REST: test destination once trusted" "$(c POST /api/backups/test -d "$(c GET /api/backups | py "import json as j; b=[b for b in d if b['id']=='$TTR'][0]; print(j.dumps(b))")")" "reachable"
check "restic REST: backup works once trusted" "$(job "$(c POST /api/backups/$TTR/run)")" "success"
tt_serve b
job "$(c POST /api/backups/$TTR/run)" >/dev/null
check "restic REST: changed certificate reported" "$(tt_blast)" "the certificate of 127.0.0.1 changed"
c PUT /api/backups/$TTR -d "$(tt_restic ",\"tlsInsecure\":true")" >/dev/null
check "restic REST: ignore certificate errors" "$(job "$(c POST /api/backups/$TTR/run)")" "success"
c DELETE /api/backups/$TTR >/dev/null

# Webhook notification to the HTTPS server (WebDAV refuses the POST, but
# only once the TLS handshake succeeded).
tt_serve a
SET=$(c GET /api/settings)
tt_settings() { echo "$SET" | py "s=d; s.pop('notifyLangAuto',None); s['webhookUrl']='https://127.0.0.1:18443/'; s['notifyChannels']={}; s.update($1); print(json.dumps(s))"; }
c PUT /api/settings -d "$(tt_settings "{}")" >/dev/null
check "webhook: self-signed server refused with advice" "$(c POST /api/settings/test-notification)" "Trust this certificate"
c PUT /api/settings -d "$(tt_settings "{'webhookTls':{'tlsCert':$PEMA}}")" >/dev/null
check "webhook: trusted certificate passes TLS" "$(c POST /api/settings/test-notification | grep -c 'certificate')" "0"
c PUT /api/settings -d "$(echo "$SET" | py 'd.pop("notifyLangAuto",None); print(json.dumps(d))')" >/dev/null

# Sign-in providers never ignore certificate errors.
check "OIDC: ignoring certificate errors refused" "$(c POST /api/security/oidc -d '{"id":"e2e-tls","name":"x","issuer":"https://127.0.0.1:18443","clientId":"x","enabled":false,"tlsInsecure":true}')" "cannot be ignored"

vm "sudo systemctl stop e2e-tt-dav e2e-tt-rest 2>/dev/null; sudo rm -rf $TT $TTSRC /var/lib/strata/tls"
