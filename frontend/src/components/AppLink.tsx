import { useContext, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { OpenPath } from "../desktop/context";

/** A link to another page: a route in classic mode, a window on the desktop. */
export function AppLink({ to, className, children }: { to: string; className?: string; children: ReactNode }) {
  const openPath = useContext(OpenPath);
  if (openPath) {
    return (
      <a href={to} className={className} onClick={(e) => { if (openPath(to)) e.preventDefault(); }}>
        {children}
      </a>
    );
  }
  return <Link to={to} className={className}>{children}</Link>;
}
