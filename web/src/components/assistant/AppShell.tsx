"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useRef, type ReactNode } from "react";
import { MotionConfig } from "motion/react";
import { Tooltip } from "radix-ui";
import { ListChecks, MoreHorizontal } from "lucide-react";
import { NoriTabIcon } from "@/components/mark/NoriMark";
import { AppFrame } from "@/components/shell/AppFrame";
import { useAppearance } from "@/lib/appearance";
import { AssistantProvider, useAssistant } from "@/lib/assistant/context";
import { useSummary } from "@/lib/assistant/sources";
import { cn } from "@/lib/utils";
import { Badge } from "./ui";

type NavItem = {
  href: string;
  label: string;
  renderIcon: (active: boolean, size: number) => ReactNode;
  badge?: number;
  badgeLabel?: string;
  dot?: boolean;
  dotLabel?: string;
  match: (path: string) => boolean;
};

export function AppShell({ localMode, children }: { localMode: boolean; children: ReactNode }) {
  useAppearance();
  return (
    <Tooltip.Provider delayDuration={500}>
      <MotionConfig reducedMotion="user">
        <AssistantProvider localMode={localMode}>
          <Frame>{children}</Frame>
        </AssistantProvider>
      </MotionConfig>
    </Tooltip.Provider>
  );
}

function Frame({ children }: { children: ReactNode }) {
  const { runtime } = useAssistant();
  const summary = useSummary();
  const path = usePathname();
  const previous = useRef(path);

  useEffect(() => {
    if (previous.current === path) return;
    previous.current = path;
    const frame = requestAnimationFrame(focusTitle);
    return () => cancelAnimationFrame(frame);
  }, [path]);

  const pending = summary.needs_you ?? 0;
  const appsAttention = summary.apps_attention ?? 0;
  const { busy } = runtime;

  const items: NavItem[] = [
    {
      href: "/",
      label: "Chat",
      renderIcon: (active, size) => <NoriTabIcon active={active} size={size} />,
      match: (p) => p === "/",
    },
    {
      href: "/work",
      label: "Work",
      renderIcon: (_active, size) => <ListChecks size={size} strokeWidth={1.85} aria-hidden />,
      badge: pending,
      badgeLabel: "items needing action",
      match: (p) =>
        p.startsWith("/work") ||
        p.startsWith("/library") ||
        p.startsWith("/goals") ||
        p.startsWith("/routines"),
    },
    {
      href: "/more",
      label: "More",
      renderIcon: (_active, size) => <MoreHorizontal size={size} strokeWidth={1.85} aria-hidden />,
      dot: appsAttention > 0,
      dotLabel: `${appsAttention} apps require attention`,
      match: (p) =>
        p.startsWith("/more") ||
        p.startsWith("/apps") ||
        p.startsWith("/memory") ||
        p.startsWith("/settings"),
    },
  ];

  const desktopNav = (
    <nav aria-label="Main" className="nori-pill-nav">
      {items.map((item) => {
        const active = item.match(path);
        return (
          <Link
            key={item.href}
            href={item.href}
            aria-current={active ? "page" : undefined}
            className="nori-pill-nav-item"
          >
            {item.renderIcon(active, 18)}
            <span>{item.label}</span>
            {item.badge ? <Badge count={item.badge} label={item.badgeLabel ?? ""} /> : null}
            {!item.badge && item.dot ? (
              <span
                aria-label={item.dotLabel}
                className="size-[6.5px] shrink-0 rounded-full bg-accent"
              />
            ) : null}
          </Link>
        );
      })}
    </nav>
  );

  const mobileTabs = (
    <nav aria-label="Main" className="nori-tabbar flex shrink-0 border-t border-hairline bg-surface sm:hidden">
      {items.map((item) => {
        const active = item.match(path);
        return (
          <Link
            key={item.href}
            href={item.href}
            aria-current={active ? "page" : undefined}
            className={cn("nori-tab", active && "text-accent-text")}
          >
            <span className="relative">
              {item.renderIcon(active, 22)}
              {item.badge ? (
                <span className="absolute -top-1.5 -right-2.5">
                  <Badge count={item.badge} label={item.badgeLabel ?? ""} />
                </span>
              ) : item.dot ? (
                <span
                  aria-label={item.dotLabel}
                  className="absolute -top-0.5 -right-1.5 size-2 rounded-full bg-accent"
                />
              ) : null}
            </span>
            <span>{item.label}</span>
          </Link>
        );
      })}
    </nav>
  );

  return (
    <>
      <a
        href="#content"
        onClick={(event) => {
          event.preventDefault();
          skipToContent();
        }}
        className="sr-only z-50 rounded-full bg-surface px-4 py-2 text-row-title font-medium text-accent-text shadow-pill focus:not-sr-only focus:fixed focus:top-3 focus:left-3"
      >
        Skip to content
      </a>
      <AppFrame state={busy ? "thinking" : "idle"} nav={desktopNav} footer={mobileTabs}>
        {children}
        <div id="nori-announcer" role="status" aria-live="polite" className="sr-only" />
      </AppFrame>
    </>
  );
}

/** Skip past navigation: to the message box on Chat, otherwise to the screen's title. */
function skipToContent() {
  const composer = [...document.querySelectorAll<HTMLElement>('main textarea[aria-label="Message Nori"]')].find(
    (element) => element.getClientRects().length > 0,
  );
  if (composer) composer.focus();
  else focusTitle();
}

/** Focus the visible screen's title. Next can keep the previous route mounted but hidden. */
function focusTitle() {
  const heading = [...document.querySelectorAll<HTMLElement>("main h1")].find(
    (element) => element.offsetParent !== null || element.getClientRects().length > 0,
  );
  if (!heading) return;
  heading.tabIndex = -1;
  heading.focus({ preventScroll: true });
}
