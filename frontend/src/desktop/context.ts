import { createContext } from "react";

/** True when rendered inside a desktop window (pages adapt their header). */
export const InWindow = createContext(false);

/** Opens a classic route as a desktop window; null outside the desktop. */
export const OpenPath = createContext<((path: string) => boolean) | null>(null);
