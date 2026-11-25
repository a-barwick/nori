import type { ReactNode } from "react";
import Link from "next/link";
import { NoriMark } from "@/components/mark/NoriMark";
import type { MarkState } from "@/lib/types";

/**
 * Full-bleed shell with Concept C's calm 60px top bar:
 * Nori mark + wordmark on the left, centered 3-pillar pill switcher, and clean right whitespace.
 */
export function AppFrame({
  children,
  nav,
  footer,
  state = "idle",
}: {
  children: ReactNode;
  nav?: ReactNode;
  footer?: ReactNode;
  state?: MarkState;
}) {
  return (
    <div className="nori-shell">
      <header className="nori-topbar">
        <div className="flex items-center">
          <Link
            href="/"
            aria-label="Nori home"
            className="-ml-2 inline-flex items-center gap-2.5 rounded-tile px-2 py-1.5 transition-colors hover:bg-fill-1"
          >
            <NoriMark size={30} state={state} />
            <span className="text-[17px] leading-none font-bold tracking-[-0.02em] text-ink">Nori</span>
          </Link>
        </div>

        <div className="hidden justify-center sm:flex">{nav}</div>

        <div aria-hidden />
      </header>

      <div className="flex min-h-0 flex-1 flex-col overflow-clip">{children}</div>
      {footer}
    </div>
  );
}
