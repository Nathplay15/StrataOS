import { useEffect, useRef } from "react";

// What a page should show once opened from elsewhere (the global search): a
// file in Files, a VM, a user… The intent waits until the page consumes it,
// so it works whether the page is already open or still loading.

export type Intent =
  | { page: "files"; dir: string; file?: import("./types").FileEntry }
  | { page: "vms"; kind: "vm" | "lxc"; name: string }
  | { page: "docker"; name: string }
  | { page: "users"; name: string }
  // Opens the disk replacement wizard (Storage → Disks) on a disk or a degraded array.
  | { page: "disks"; replace: { disk?: string; group?: string; member?: string } };

const pending = new Map<Intent["page"], Intent>();

export function postIntent(i: Intent) {
  pending.set(i.page, i);
  window.dispatchEvent(new CustomEvent("strata:intent", { detail: i.page }));
}

/** Calls handler with the intents posted for page, now and later. */
export function useIntent<P extends Intent["page"]>(page: P, handler: (i: Extract<Intent, { page: P }>) => void) {
  const h = useRef(handler);
  h.current = handler;
  useEffect(() => {
    const take = () => {
      const i = pending.get(page);
      if (!i) return;
      pending.delete(page);
      h.current(i as Extract<Intent, { page: P }>);
    };
    take();
    const on = (e: Event) => (e as CustomEvent).detail === page && take();
    window.addEventListener("strata:intent", on);
    return () => window.removeEventListener("strata:intent", on);
  }, [page]);
}

/** Scrolls to an element of a page being opened (waits for it a few seconds) and highlights it. */
export function revealElement(id: string) {
  let tries = 0;
  const t = setInterval(() => {
    const el = document.getElementById(id);
    if (el) {
      clearInterval(t);
      el.scrollIntoView({ behavior: "smooth", block: "start" });
      el.classList.add("search-flash");
      setTimeout(() => el.classList.remove("search-flash"), 1600);
    } else if (++tries > 50) clearInterval(t);
  }, 100);
}

/** Asks the account menu (classic or desktop) to open one of its dialogs. */
export type AccountDialog = "appearance" | "password" | "2fa" | "passkeys" | "devices" | "widgets";
export function openAccountDialog(d: AccountDialog) {
  window.dispatchEvent(new CustomEvent("strata:account", { detail: d }));
}
export function useAccountDialog(handler: (d: AccountDialog) => void) {
  const h = useRef(handler);
  h.current = handler;
  useEffect(() => {
    const on = (e: Event) => h.current((e as CustomEvent).detail as AccountDialog);
    window.addEventListener("strata:account", on);
    return () => window.removeEventListener("strata:account", on);
  }, []);
}
