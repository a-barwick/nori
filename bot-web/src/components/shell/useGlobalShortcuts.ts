"use client";

import { useEffect } from "react";
import { usePathname, useRouter } from "next/navigation";
import { getNori } from "@/lib/store";

export const RAIL_ROUTES = ["/", null, "/ideas", "/goals", "/library"] as const;

/** §9 keyboard map. Mod is ⌘ on macOS, Ctrl elsewhere (both accepted). */
export function useGlobalShortcuts() {
  const router = useRouter();
  const pathname = usePathname();

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (!mod || e.altKey) return;
      const s = getNori();
      const wide = window.innerWidth >= 1280;
      switch (e.code) {
        case "KeyK":
          e.preventDefault();
          s.set({ searchOpen: !s.searchOpen });
          return;
        case "Slash":
          e.preventDefault();
          s.set({ shortcutsOpen: true, searchOpen: false });
          return;
        case "Comma":
          e.preventDefault();
          s.set({ searchOpen: false, drawerOpen: false });
          router.push("/settings");
          return;
        case "Period":
          e.preventDefault();
          s.togglePanel(pathname === "/" ? "chat" : "pages", wide);
          return;
        case "Digit1":
        case "Digit2":
        case "Digit3":
        case "Digit4":
        case "Digit5": {
          if (e.shiftKey) return;
          e.preventDefault();
          const index = Number(e.code.slice(-1)) - 1;
          const route = RAIL_ROUTES[index];
          if (route === null) {
            s.set({ searchOpen: true });
          } else {
            s.set({ searchOpen: false });
            router.push(route);
          }
          return;
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [router, pathname]);
}
