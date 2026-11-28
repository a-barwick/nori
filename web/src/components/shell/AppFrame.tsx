"use client";

import { useEffect, useRef, type ReactNode } from "react";
import Link from "next/link";
import { NoriMark } from "@/components/mark/NoriMark";
import type { MarkState } from "@/lib/types";

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
  const shell = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const viewport = window.visualViewport;
    const frame = shell.current;
    if (!viewport || !frame || !window.matchMedia("(pointer: coarse)").matches) return;

    // Safari can pan the visible page as well as shrink it for the keyboard.
    const fitKeyboard = () => {
      const editing = document.activeElement?.matches("input, textarea, [contenteditable=true]");
      const fit = editing && Math.abs(viewport.scale - 1) < 0.01;
      frame.style.top = fit ? `${viewport.offsetTop}px` : "";
      frame.style.height = fit ? `${viewport.height}px` : "";
    };

    viewport.addEventListener("resize", fitKeyboard);
    viewport.addEventListener("scroll", fitKeyboard);
    document.addEventListener("focusin", fitKeyboard);
    document.addEventListener("focusout", fitKeyboard);
    fitKeyboard();
    return () => {
      viewport.removeEventListener("resize", fitKeyboard);
      viewport.removeEventListener("scroll", fitKeyboard);
      document.removeEventListener("focusin", fitKeyboard);
      document.removeEventListener("focusout", fitKeyboard);
    };
  }, []);

  return (
    <div ref={shell} className="nori-shell">
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

      <div id="content" className="flex min-h-0 flex-1 flex-col overflow-clip">{children}</div>
      {footer}
    </div>
  );
}
