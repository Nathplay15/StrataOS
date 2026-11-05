import { createContext, useContext, type ReactNode } from "react";
import type { PageId } from "../pages";

export type Nav = { page: PageId; params?: Record<string, string> };

export const NavCtx = createContext<{ nav: Nav; go: (page: PageId, params?: Record<string, string>) => void; addNas: () => void }>({ nav: { page: "home" }, go: () => {}, addNas: () => {} });

/** Moves to another page of the window. */
export const useNav = () => useContext(NavCtx);

/** A page of the window. */
export type PageProps = { params: Record<string, string> };
export type PageComponent = (p: PageProps) => ReactNode;
