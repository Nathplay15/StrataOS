#!/usr/bin/env python3
"""Notification test sinks for vm/e2e-test.sh (run on the host, reachable from the VM at 10.0.2.2).

  webhook  http://127.0.0.1:8720/  -> <dir>/webhook.log
  SMTP     127.0.0.1:8525 (plain)  -> <dir>/mail.log
  SMTPS    127.0.0.1:8465 (implicit TLS, with --cert/--key) -> <dir>/mail.log

Usage: test-sinks.py <dir> [--cert cert.pem --key key.pem]
"""
import argparse
import asyncio
import http.server
import os
import ssl
import threading

ap = argparse.ArgumentParser()
ap.add_argument("dir")
ap.add_argument("--cert")
ap.add_argument("--key")
args = ap.parse_args()
os.makedirs(args.dir, exist_ok=True)
WEBHOOK = os.path.join(args.dir, "webhook.log")
MAIL = os.path.join(args.dir, "mail.log")


class Hook(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        with open(WEBHOOK, "ab") as f:
            f.write(body + b"\n")
        self.send_response(204)
        self.end_headers()

    def log_message(self, *a):
        pass


threading.Thread(target=http.server.HTTPServer(("127.0.0.1", 8720), Hook).serve_forever, daemon=True).start()


def smtp_handler(label):
    async def handle(reader, writer):
        def w(s):
            writer.write((s + "\r\n").encode())

        w("220 sink ESMTP")
        await writer.drain()
        data_mode, buf = False, []
        while line := await reader.readline():
            t = line.decode(errors="replace")
            if data_mode:
                if t.rstrip("\r\n") == ".":
                    with open(MAIL, "a") as f:
                        f.write(f"[{label}]\n" + "".join(buf) + "\n=====\n")
                    data_mode, buf = False, []
                    w("250 queued")
                else:
                    buf.append(t)
            else:
                cmd = t[:4].upper()
                if cmd in ("EHLO", "HELO"):
                    w("250 sink")
                elif cmd == "DATA":
                    data_mode = True
                    w("354 go")
                elif cmd == "QUIT":
                    w("221 bye")
                    await writer.drain()
                    break
                else:
                    w("250 ok")
            await writer.drain()
        writer.close()

    return handle


async def main():
    servers = [await asyncio.start_server(smtp_handler("plain"), "127.0.0.1", 8525)]
    if args.cert and args.key:
        ctx = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH)
        ctx.load_cert_chain(args.cert, args.key)
        servers.append(await asyncio.start_server(smtp_handler("tls"), "127.0.0.1", 8465, ssl=ctx))
    print("sinks ready", flush=True)
    await asyncio.gather(*(s.serve_forever() for s in servers))


asyncio.run(main())
