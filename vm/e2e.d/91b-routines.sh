# Routines (0.12.1, PR #52): "if → then" rules evaluated every minute; a
# webhook reaches the local network, with a timeout and no redirect to
# another host.
section "Routines"
# (pid files: pkill -f would also match the ssh command that runs it)
vm "kill \$(cat /tmp/hook.pids 2>/dev/null) 2>/dev/null; rm -f /tmp/hook.pids /tmp/hook.log /tmp/hook-away.log; cat > /tmp/e2e-routine-hook.py <<'PY'
import http.server, sys
port, log, to = int(sys.argv[1]), sys.argv[2], (sys.argv[3] if len(sys.argv) > 3 else '')
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        open(log, 'ab').write(self.path.encode() + b' ' + body + b'\n')
        if to and self.path == '/away':
            self.send_response(307); self.send_header('Location', to); self.end_headers(); return
        self.send_response(200); self.end_headers()
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', port), H).serve_forever()
PY
setsid nohup timeout 600 python3 /tmp/e2e-routine-hook.py 18991 /tmp/hook.log http://127.0.0.1:18992/stolen >/dev/null 2>&1 < /dev/null & echo \$! >> /tmp/hook.pids
setsid nohup timeout 600 python3 /tmp/e2e-routine-hook.py 18992 /tmp/hook-away.log >/dev/null 2>&1 < /dev/null & echo \$! >> /tmp/hook.pids; sleep 1"
check "routines are for administrators" "$(u GET /api/routines)" "administrator rights required"
check "bad webhook URL refused" "$(c POST /api/routines -d '{"name":"x","trigger":"cpu","value":90,"actions":[{"kind":"webhook","url":"file:///etc/passwd"}]}')" "http(s)"
# The test volumes are nearly empty: fill main to 2 % if no volume reaches 1 %.
FILL=$(c GET /api/storage/trends | py 'v=[x for x in d["volumes"] if x["name"]=="main"][0]; print(0 if max(int(100*x["used"]/x["total"]) for x in d["volumes"] if x.get("total")) >= 1 else v["total"]//50)')
[ "$FILL" -gt 0 ] && vm "sudo fallocate -l $FILL /srv/strata/main/e2e-routine-fill"
RT=$(c POST /api/routines -d '{"name":"E2E volume","enabled":true,"trigger":"volume-full","value":1,"actions":[{"kind":"webhook","url":"http://127.0.0.1:18991/hook"},{"kind":"webhook","url":"http://127.0.0.1:18991/away"}],"cooldown":60}' | py 'print(d["id"])')
check "routine created" "$(c GET /api/routines | py 'print([r["name"] for r in d])')" "E2E volume"
for _ in $(seq 1 50); do vm "grep -q /hook /tmp/hook.log 2>/dev/null" && break; sleep 3; done
check "routine fired within a minute: webhook on the local network" "$(vm 'cat /tmp/hook.log')" '"routine":"E2E volume"'
check "trigger detail sent" "$(vm 'cat /tmp/hook.log')" "full (threshold 1%)"
check "run stamped" "$(c GET /api/routines | py 'print([r.get("lastFired","")[:2] for r in d if r["id"]=="'"$RT"'"])')" "['20']"
check "redirect to another host not followed" "$(vm 'cat /tmp/hook-away.log 2>/dev/null | wc -l')" "0"
N=$(vm 'grep -c /hook /tmp/hook.log')
sleep 70
check "cooldown: not fired again" "$(vm 'grep -c /hook /tmp/hook.log')" "$N"
c POST /api/routines/$RT/fire >/dev/null; sleep 2
check "test run fires at once" "$(vm 'grep -c /hook /tmp/hook.log')" "$((N + 1))"
check "routine edited" "$(c PUT /api/routines/$RT -d '{"name":"E2E renamed","enabled":false,"trigger":"volume-full","value":99,"actions":[{"kind":"notify"}]}' | py 'print(d["name"], d["enabled"])')" "E2E renamed False"
check "routine deleted" "$(c DELETE /api/routines/$RT >/dev/null; c GET /api/routines | py 'print(len([r for r in d if r["id"]=="'"$RT"'"]))')" "0"
vm "kill \$(cat /tmp/hook.pids) 2>/dev/null; rm -f /tmp/hook.pids /tmp/e2e-routine-hook.py /tmp/hook.log /tmp/hook-away.log; sudo rm -f /srv/strata/main/e2e-routine-fill"
