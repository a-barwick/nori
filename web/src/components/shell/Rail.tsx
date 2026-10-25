"use client";

import { useRouter } from "next/navigation";
import { Popover } from "radix-ui";
import { Bug, Keyboard, Lightbulb, Library, Menu, MessageCircle, Search, Settings, Target, type LucideIcon } from "lucide-react";
import { Tip } from "@/components/ui/primitives";
import { useNori } from "@/lib/store";
import { cn } from "@/lib/utils";

interface Item {
  label: string;
  icon: LucideIcon;
  href?: string;
  shortcut: string;
}

const ITEMS: Item[] = [
  { label: "Chat", icon: MessageCircle, href: "/", shortcut: "⌘1" },
  { label: "Search", icon: Search, shortcut: "⌘K" },
  { label: "Ideas", icon: Lightbulb, href: "/ideas", shortcut: "⌘3" },
  { label: "Goals", icon: Target, href: "/goals", shortcut: "⌘4" },
  { label: "Library", icon: Library, href: "/library", shortcut: "⌘5" },
];

const itemClass = (active: boolean) =>
  cn(
    "t-fast grid size-11 place-items-center rounded-full transition-colors",
    active ? "bg-fill-2 text-ink" : "text-ink-2 hover:bg-fill-1 hover:text-ink",
  );

/** The rail is hidden for now; only its menu button stays, floating bottom-left. */
export function Rail() {
  return (
    <nav aria-label="Primary" className="absolute bottom-5 left-4 z-20">
      <MenuButton />
    </nav>
  );
}

function MenuButton() {
  const router = useRouter();
  const set = useNori((s) => s.set);
  // With the rail hidden, its destinations live at the top of the menu.
  const nav = ITEMS.map((item) => ({
    label: item.label,
    icon: item.icon,
    shortcut: item.shortcut,
    run: () => (item.href ? (set({ searchOpen: false, drawerOpen: false }), router.push(item.href)) : set({ searchOpen: true, drawerOpen: false })),
  }));
  const rows: ({ label: string; icon: LucideIcon; shortcut?: string; run: () => void } | null)[] = [
    ...nav,
    null,
    { label: "Keyboard shortcuts", icon: Keyboard, shortcut: "⌘/", run: () => set({ shortcutsOpen: true }) },
    { label: "Report an issue", icon: Bug, run: () => set({ reportOpen: true }) },
    null,
    { label: "Settings", icon: Settings, shortcut: "⌘,", run: () => router.push("/settings") },
  ];
  return (
    <Popover.Root>
      <Tip label="Menu" side="top">
        <Popover.Trigger asChild>
          <button type="button" aria-label="Menu" className={itemClass(false)}>
            <Menu size={24} strokeWidth={1.5} aria-hidden />
          </button>
        </Popover.Trigger>
      </Tip>
      <Popover.Portal>
        <Popover.Content
          side="top"
          align="start"
          sideOffset={8}
          className="menu z-[60] w-60 rounded-card bg-surface p-1.5 shadow-pop"
          onOpenAutoFocus={(e) => e.preventDefault()}
        >
          <div role="menu" aria-label="Menu">
            {rows.map((row, i) =>
              row ? (
                <Popover.Close asChild key={row.label}>
                  <button
                    type="button"
                    role="menuitem"
                    onClick={row.run}
                    className="t-fast flex h-9 w-full items-center gap-2.5 rounded-chip px-2.5 text-left text-row-title text-ink transition-colors hover:bg-fill-1"
                  >
                    <row.icon size={18} strokeWidth={1.5} className="text-ink-2" aria-hidden />
                    <span className="flex-1">{row.label}</span>
                    {row.shortcut && <span className="text-caption text-ink-3">{row.shortcut}</span>}
                  </button>
                </Popover.Close>
              ) : (
                <div key={`d${i}`} role="separator" className="mx-2.5 my-1.5 h-px bg-hairline" />
              ),
            )}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
