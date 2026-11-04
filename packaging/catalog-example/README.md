# Example community catalog

A small catalog in the format of [docs/CATALOG-FORMAT.md](../../docs/CATALOG-FORMAT.md), to show how one is written and to try the App Center's community catalogs. It is not maintained for real use.

- `catalog.json`: the index (four apps: two plain ones, one with a folder of the NAS, one that needs an approval for the Docker socket).
- `catalog.json.sig`: its Ed25519 signature, in base64.
- `public-key.txt`: the public key to give the NAS (`ed25519:…`).
- `example-signing-key.pem`: the private key that signed it. **It is public on purpose** (anyone can re-sign the example after a change); a real catalog keeps its key secret.

To add it to a NAS: App Center → Community catalogs → address `https://raw.githubusercontent.com/Nathplay15/StrataOS/main/packaging/catalog-example/catalog.json` and the key of `public-key.txt` (or no key, to pin it by checksum).

After a change, sign again (a Go test checks the signature):

```bash
openssl pkeyutl -sign -inkey example-signing-key.pem -rawin -in catalog.json | base64 -w0 > catalog.json.sig
```
