"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { MotionConfig } from "motion/react";
import { Dialog, Tooltip } from "radix-ui";
import { FolderOpen, LayoutGrid, ListChecks, MessageCircle, MoreHorizontal, Monitor, Repeat, Settings, Target, X } from "lucide-react";
import { NovaShell } from "@/components/nova/NovaShell";
import { AssistantProvider, useAssistant } from "@/lib/assistant/context";
import { useSummary } from "@/lib/assistant/sources";
import { useHydrated } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { Badge } from "./ui";

type NavItem = { href: string; label: string; icon: typeof MessageCircle; badge?: number; badgeLabel?: string; match: (path: string) => boolean };

export function AppShell({ agentMode, children }: { agentMode: boolean; children: ReactNode }) {
  const hydrated = useHydrated();
  if (!hydrated) return <div className="fixed inset-0 bg-canvas" />;
  return (
    <Tooltip.Provider delayDuration={500}>
      <MotionConfig reducedMotion="user">
        <AssistantProvider agentMode={agentMode}>
          <Frame>{children}</Frame>
        </AssistantProvider>
      </MotionConfig>
    </Tooltip.Provider>
  );
}

function Frame({ children }: { children: ReactNode }) {
  const { runtime, fixtures } = useAssistant();
  const summary = useSummary();
  const path = usePathname();
  const [more, setMore] = useState(false);
  const previous = useRef(path);
  // After navigation, move focus to the new screen's title so keyboard and screen-reader users land on it.
  useEffect(() => {
    if (previous.current === path) return;
    previous.current = path;
    const frame = requestAnimationFrame(() => {
      const heading = document.querySelector<HTMLElement>("main h1");
      if (!heading) return;
      heading.tabIndex = -1;
      heading.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [path]);
  const waiting = summary.needs_you ?? 0;
  const { session, loading, error, busy } = runtime;
  const items: NavItem[] = [
    { href: "/", label: "Chat", icon: MessageCircle, match: (p) => p === "/" },
    { href: "/work", label: "Work", icon: ListChecks, badge: waiting, badgeLabel: "need you", match: (p) => p.startsWith("/work") || p.startsWith("/computer") },
    { href: "/apps", label: "Apps", icon: LayoutGrid, badge: summary.apps_attention, badgeLabel: "need attention", match: (p) => p.startsWith("/apps") },
    { href: "/library", label: "Library", icon: FolderOpen, match: (p) => p.startsWith("/library") },
    { href: "/routines", label: "Routines", icon: Repeat, match: (p) => p.startsWith("/routines") },
    { href: "/goals", label: "Goals", icon: Target, match: (p) => p.startsWith("/goals") },
    { href: "/settings", label: "Settings", icon: Settings, match: (p) => p.startsWith("/settings") },
  ];
  const status = loading ? "Connecting…" : error || !session ? "Disconnected" : waiting ? `${waiting} ${waiting === 1 ? "thing needs" : "things need"} you` : busy ? "Working…" : "Ready when you are";
  const mobileMain = items.slice(0, 4);
  const mobileMore = [...items.slice(4), { href: "/computer", label: "Computer", icon: Monitor, match: (p: string) => p.startsWith("/computer") }];

  return (
    <NovaShell state={busy ? "thinking" : "idle"} status={status} actions={fixtures ? <span className="rounded-full bg-warning-bg px-2.5 py-1 text-caption font-medium text-warning" title="Screens read contract fixtures, not your data">Fixtures</span> : undefined}>
      <div className="nova-live-body flex min-h-0 flex-1 overflow-clip text-ink">
        <nav aria-label="Main" className="nori-rail hidden shrink-0 flex-col gap-1 border-r border-hairline px-3 py-4 sm:flex">
          {items.map((item) => <RailLink key={item.href} item={item} active={item.match(path)} />)}
        </nav>
        <div className="flex min-w-0 flex-1 flex-col overflow-clip">{children}</div>
      </div>
      <nav aria-label="Main" className="nori-tabbar flex shrink-0 border-t border-hairline bg-surface sm:hidden">
        {mobileMain.map((item) => <TabLink key={item.href} item={item} active={item.match(path)} />)}
        <Dialog.Root open={more} onOpenChange={setMore}>
          <Dialog.Trigger asChild>
            <button type="button" className={cn("nori-tab", mobileMore.some((item) => item.match(path)) && "text-accent-text")}>
              <MoreHorizontal size={22} strokeWidth={1.75} aria-hidden /><span>More</span>
            </button>
          </Dialog.Trigger>
          <Dialog.Portal>
            <Dialog.Overlay className="scrim fixed inset-0 z-40 bg-overlay" />
            <Dialog.Content aria-describedby={undefined} className="anim-pop fixed inset-x-0 bottom-0 z-50 rounded-t-sheet bg-surface p-4 pb-[max(16px,env(safe-area-inset-bottom))] shadow-pop">
              <div className="mb-2 flex items-center justify-between"><Dialog.Title className="text-section font-semibold">More</Dialog.Title>
                <Dialog.Close className="grid size-11 place-items-center rounded-full text-ink-2" aria-label="Close"><X size={20} /></Dialog.Close></div>
              <ul className="grid grid-cols-2 gap-2">
                {mobileMore.map((item) => <li key={item.href}><Link href={item.href} onClick={() => setMore(false)} className={cn("flex h-14 items-center gap-3 rounded-tile bg-fill-1 px-4 text-row font-medium", item.match(path) && "text-accent-text")}><item.icon size={20} aria-hidden />{item.label}</Link></li>)}
              </ul>
            </Dialog.Content>
          </Dialog.Portal>
        </Dialog.Root>
      </nav>
    </NovaShell>
  );
}

function RailLink({ item, active }: { item: NavItem; active: boolean }) {
  const Icon = item.icon;
  return (
    <Link href={item.href} aria-current={active ? "page" : undefined}
      className={cn("relative flex h-10 items-center gap-3 rounded-tile px-3 text-row-title font-medium", active ? "bg-accent-subtle text-accent-text" : "text-ink-2 hover:bg-fill-1 hover:text-ink")}>
      <Icon size={18} strokeWidth={1.75} aria-hidden />
      <span className="nori-rail-label flex-1">{item.label}</span>
      {item.badge ? <span className="nori-rail-badge"><Badge count={item.badge} label={item.badgeLabel ?? ""} /></span> : null}
    </Link>
  );
}

function TabLink({ item, active }: { item: NavItem; active: boolean }) {
  const Icon = item.icon;
  return (
    <Link href={item.href} aria-current={active ? "page" : undefined} className={cn("nori-tab", active && "text-accent-text")}>
      <span className="relative"><Icon size={22} strokeWidth={1.75} aria-hidden />
        {item.badge ? <span className="absolute -top-1.5 -right-2.5"><Badge count={item.badge} label={item.badgeLabel ?? ""} /></span> : null}</span>
      <span>{item.label}</span>
    </Link>
  );
}

