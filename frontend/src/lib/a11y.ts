import { useEffect, useRef, type KeyboardEvent as RKeyboardEvent } from "react";

/** Closes a popup on a click outside it or on Escape, while it is open. */
export function useDismiss(ref: React.RefObject<HTMLElement | null>, open: boolean, onClose: () => void) {
  const cb = useRef(onClose);
  cb.current = onClose;
  useEffect(() => {
    if (!open) return;
    const down = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && cb.current();
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        cb.current();
      }
    };
    document.addEventListener("mousedown", down);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", down);
      document.removeEventListener("keydown", key);
    };
  }, [ref, open]);
}

/** Arrow keys, Home and End move the focus between the items of a menu (role="menuitem"). */
export function menuKeys(e: RKeyboardEvent<HTMLElement>) {
  const items = [...e.currentTarget.querySelectorAll<HTMLElement>('[role^="menuitem"]:not([disabled])')];
  if (!items.length) return;
  const i = items.indexOf(document.activeElement as HTMLElement);
  const to = e.key === "ArrowDown" ? (i + 1) % items.length : e.key === "ArrowUp" ? (i - 1 + items.length) % items.length : e.key === "Home" ? 0 : e.key === "End" ? items.length - 1 : -1;
  if (to < 0) return;
  e.preventDefault();
  items[to].focus();
}

/** Focuses the first menu item of an element once it is shown. */
export function focusFirstItem(el: HTMLElement | null) {
  el?.querySelector<HTMLElement>('[role^="menuitem"]')?.focus();
}
