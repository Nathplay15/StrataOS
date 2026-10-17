# TOTP code generator for vm/e2e-test.sh (RFC 6238, SHA1, 30 s, 6 digits).
import base64, hmac, hashlib, struct, sys, time
def code(secret, t=None):
    key = base64.b32decode(secret + "=" * (-len(secret) % 8))
    c = int((t or time.time()) // 30)
    h = hmac.new(key, struct.pack(">Q", c), hashlib.sha1).digest()
    o = h[-1] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o+4])[0] & 0x7fffffff) % 1000000)
print(code(sys.argv[1], float(sys.argv[2]) if len(sys.argv) > 2 else None))
