import type { Share } from "./types";

// The Linux group every StrataOS user belongs to (accounts.BaseGroup).
export const ALL_USERS = "@strata";

export const emptyShare: Share = { name: "", path: "", description: "", smb: true, nfs: false, guest: false, browseable: true, recycleBin: true, timeMachine: false, permissions: {}, nfsClients: "", nfsOptions: "" };

/** A new share as the Shares page proposes it: in base/name, its creator
 * can write while the other users read (without any permission everyone,
 * the creator included, only reads). */
export function newShareDefaults(name: string, base: string, me?: string): Share {
  return { ...emptyShare, name, path: base ? `${base}/${name || "share"}` : "", permissions: me ? { [me]: "rw", [ALL_USERS]: "ro" } : {} };
}
