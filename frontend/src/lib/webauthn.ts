// WebAuthn helpers: the server speaks the JSON form of the options (binary
// fields in base64url), the browser API wants ArrayBuffers.

export function passkeysSupported(): boolean {
  return typeof window !== "undefined" && window.isSecureContext && !!window.PublicKeyCredential && !!navigator.credentials;
}

function fromB64(s: string): ArrayBuffer {
  const b = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
  const out = new Uint8Array(b.length);
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i);
  return out.buffer;
}

function toB64(buf: ArrayBuffer | null | undefined): string {
  if (!buf) return "";
  const b = new Uint8Array(buf);
  let s = "";
  for (const c of b) s += String.fromCharCode(c);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

const descriptors = (l?: any[]) => (l ?? []).map((c) => ({ ...c, id: fromB64(c.id) }));

/** navigator.credentials.create() with the options of /api/me/passkeys/begin. */
export async function createPasskey(pk: any): Promise<unknown> {
  const publicKey: PublicKeyCredentialCreationOptions = {
    ...pk,
    challenge: fromB64(pk.challenge),
    user: { ...pk.user, id: fromB64(pk.user.id) },
    excludeCredentials: descriptors(pk.excludeCredentials),
  };
  const cred = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null;
  if (!cred) throw new Error("cancelled");
  const r = cred.response as AuthenticatorAttestationResponse;
  return {
    id: cred.id, rawId: toB64(cred.rawId), type: cred.type, authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    response: { clientDataJSON: toB64(r.clientDataJSON), attestationObject: toB64(r.attestationObject), transports: r.getTransports?.() ?? [] },
    clientExtensionResults: cred.getClientExtensionResults(),
  };
}

/** navigator.credentials.get() with the options of /api/login/passkey/begin. */
export async function getPasskey(pk: any): Promise<unknown> {
  const publicKey: PublicKeyCredentialRequestOptions = {
    ...pk,
    challenge: fromB64(pk.challenge),
    allowCredentials: descriptors(pk.allowCredentials),
  };
  const cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null;
  if (!cred) throw new Error("cancelled");
  const r = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id, rawId: toB64(cred.rawId), type: cred.type, authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    response: { clientDataJSON: toB64(r.clientDataJSON), authenticatorData: toB64(r.authenticatorData), signature: toB64(r.signature), userHandle: toB64(r.userHandle) },
    clientExtensionResults: cred.getClientExtensionResults(),
  };
}

/** The browser's "cancelled" / "not allowed" errors are not worth an error box. */
export function cancelled(e: unknown): boolean {
  return e instanceof DOMException ? e.name === "NotAllowedError" || e.name === "AbortError" : e instanceof Error && e.message === "cancelled";
}
