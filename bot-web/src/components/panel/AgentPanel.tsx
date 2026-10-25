"use client";

import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import { Dialog } from "radix-ui";
import { usePathname } from "next/navigation";
import { Clock, Fingerprint, List, Pencil, ShieldCheck, X, type LucideIcon } from "lucide-react";
import { Medallion } from "@/components/mark/NoriMark";
import { Tip } from "@/components/ui/primitives";
import { focusSelf } from "@/components/ui/layers";
import { pendingApprovals, useNori } from "@/lib/store";
import type { PanelTab } from "@/lib/types";
import { cn } from "@/lib/utils";
import { ActivityTab } from "./ActivityTab";
import { ApprovalsTab } from "./ApprovalsTab";
import { UpcomingTab } from "./UpcomingTab";
import { IdentityTab } from "./IdentityTab";

const TABS: { key: PanelTab; label: string; icon: LucideIcon }[] = [
  { key: "activity", label: "Activity", icon: List },
  { key: "approvals", label: "Approvals", icon: ShieldCheck },
  { key: "upcoming", label: "Upcoming", icon: Clock },
  { key: "identity", label: "Identity", icon: Fingerprint },
];

const SPRING = { duration: 0.32, ease: [0.15, 1, 0.4, 1] as const };

function useOnline() {
  return useSyncExternalStore(
    (cb) => {
      window.addEventListener("online", cb);
      window.addEventListener("offline", cb);
      return () => {
        window.removeEventListener("online", cb);
        window.removeEventListener("offline", cb);
      };
    },
    () => navigator.onLine,
    () => true,
  );
}

function StatusLine() {
  const busy = useNori((s) => (s.typing ? 1 : 0) + (s.work ? 1 : 0));
  const online = useOnline();
  if (!online) {
    return (
      <span className="flex items-center gap-2">
        <span className="size-2 rounded-full shadow-[inset_0_0_0_1.5px_var(--text-tertiary)]" aria-hidden />
        Offline, will catch up
      </span>
    );
  }
  if (busy) {
    return (
      <span className="flex items-center gap-2">
        <span
          className="pulse-dot size-2 rounded-full"
          style={{ background: "linear-gradient(90deg, var(--accent-signal) 50%, transparent 50%)", boxShadow: "inset 0 0 0 1.5px var(--accent-signal)" }}
          aria-hidden
        />
        Working on {busy === 1 ? "1 thing" : `${busy} things`}
      </span>
    );
  }
  return (
    <span className="flex items-center gap-2">
      <span className="size-2 rounded-full bg-accent-signal" aria-hidden />
      Online
    </span>
  );
}

function CloseButton({ instance }: { instance: string }) {
  const onChat = usePathname() === "/";
  const setDock = useNori((s) => s.setDock);
  const setOverlay = useNori((s) => s.setOverlay);
  const close = () => (instance === "dock" ? setDock(onChat ? "chat" : "pages", false) : setOverlay(false));
  return (
    <Tip label="Hide panel" shortcut="⌘.">
      <button
        type="button"
        aria-label="Hide agent panel"
        onClick={close}
        className="t-fast absolute top-3 right-0 grid size-9 place-items-center rounded-full text-ink-2 transition-colors hover:bg-fill-1 hover:text-ink"
      >
        <X size={18} strokeWidth={1.5} aria-hidden />
      </button>
    </Tip>
  );
}

function Header({ instance }: { instance: string }) {
  const name = useNori((s) => s.identity.name);
  const set = useNori((s) => s.set);
  return (
    <div className="relative flex flex-col items-center pt-12">
      <CloseButton instance={instance} />
      <Medallion size={96}>
        <Tip label="Edit name & look">
          <button
            type="button"
            aria-label="Edit name & look"
            onClick={() => set({ editLookOpen: true })}
            className="glass t-fast absolute -right-1 -bottom-1 grid size-8 place-items-center rounded-full text-ink-2 transition-colors hover:text-ink"
          >
            <Pencil size={15} strokeWidth={1.5} aria-hidden />
          </button>
        </Tip>
      </Medallion>
      <h2 className="mt-3 text-title font-medium text-ink">{name}</h2>
      <div className="mt-1 text-status text-ink-2" aria-live="polite">
        <StatusLine />
      </div>
    </div>
  );
}

function Segmented({ instance }: { instance: string }) {
  const tab = useNori((s) => s.panelTab);
  const setTab = useNori((s) => s.setPanelTab);
  const pending = useNori((s) => pendingApprovals(s).length);
  const refs = useRef<(HTMLButtonElement | null)[]>([]);

  const onKey = (e: React.KeyboardEvent, i: number) => {
    const dir = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
    if (!dir) return;
    e.preventDefault();
    const next = (i + dir + TABS.length) % TABS.length;
    setTab(TABS[next].key);
    refs.current[next]?.focus();
  };

  return (
    <LayoutGroup id={`seg-${instance}`}>
      <div role="tablist" aria-label="Agent panel" className="mt-6 flex h-9 rounded-full bg-fill-1 p-1">
        {TABS.map((t, i) => {
          const active = t.key === tab;
          const divider = i > 0 && !active && TABS[i - 1].key !== tab;
          const Icon = t.icon;
          return (
            <Tip key={t.key} label={t.label}>
              <button
                ref={(el) => {
                  refs.current[i] = el;
                }}
                type="button"
                role="tab"
                id={`tab-${instance}-${t.key}`}
                aria-selected={active}
                aria-controls={`tabpanel-${instance}`}
                aria-label={pending && t.key === "approvals" ? `${t.label}, ${pending} pending` : t.label}
                tabIndex={active ? 0 : -1}
                onClick={() => setTab(t.key)}
                onKeyDown={(e) => onKey(e, i)}
                className={cn("relative grid h-7 flex-1 place-items-center rounded-full", active ? "text-ink" : "t-fast text-ink-2 transition-colors hover:text-ink")}
              >
                {divider && <span className="absolute top-1/2 left-0 h-4 w-px -translate-y-1/2 bg-hairline" aria-hidden />}
                {active && <motion.span layoutId="pill" className="absolute inset-0 rounded-full bg-surface shadow-pill" transition={SPRING} />}
                <span className="relative">
                  <Icon size={18} strokeWidth={1.5} aria-hidden />
                  {t.key === "approvals" && pending > 0 && (
                    <span className="anim-pop absolute -top-1.5 -right-2 grid h-3.5 min-w-3.5 place-items-center rounded-full bg-accent px-1 text-[10px] leading-none font-medium text-on-accent">
                      {pending}
                    </span>
                  )}
                </span>
              </button>
            </Tip>
          );
        })}
      </div>
    </LayoutGroup>
  );
}

function Content({ instance }: { instance: string }) {
  const tab = useNori((s) => s.panelTab);
  const index = TABS.findIndex((t) => t.key === tab);
  const [prev, setPrev] = useState(index);
  const [dir, setDir] = useState(0);
  if (prev !== index) {
    setDir(index > prev ? 1 : -1);
    setPrev(index);
  }
  return (
    <div
      role="tabpanel"
      id={`tabpanel-${instance}`}
      aria-labelledby={`tab-${instance}-${tab}`}
      className="scrollbar-quiet -mx-4 min-h-0 flex-1 overflow-x-hidden overflow-y-auto px-4 pb-8"
    >
      <AnimatePresence mode="wait" initial={false} custom={dir}>
        <motion.div
          key={tab}
          custom={dir}
          initial={{ opacity: 0, x: dir * 8 }}
          animate={{ opacity: 1, x: 0 }}
          exit={{ opacity: 0, x: dir * -8 }}
          transition={{ duration: 0.15, ease: [0.4, 0, 0.2, 1] }}
        >
          {tab === "activity" && <ActivityTab />}
          {tab === "approvals" && <ApprovalsTab />}
          {tab === "upcoming" && <UpcomingTab />}
          {tab === "identity" && <IdentityTab />}
        </motion.div>
      </AnimatePresence>
    </div>
  );
}

export function AgentPanel({ instance }: { instance: string }) {
  return (
    <div className="flex h-full flex-col bg-canvas px-4">
      <Header instance={instance} />
      <Segmented instance={instance} />
      <Content instance={instance} />
    </div>
  );
}

/** 1024–1279px: the panel becomes an overlay sheet from the right. */
export function PanelOverlay() {
  const open = useNori((s) => s.overlay);
  const setOverlay = useNori((s) => s.setOverlay);
  useEffect(() => () => setOverlay(false), [setOverlay]);
  return (
    <Dialog.Root open={open} onOpenChange={setOverlay}>
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-40 bg-overlay" />
        <Dialog.Content
          aria-describedby={undefined}
          onOpenAutoFocus={focusSelf}
          className="sheet-right fixed inset-y-0 right-0 z-40 shadow-pop"
          style={{ width: "var(--panel-w)" }}
        >
          <Dialog.Title className="sr-only">Agent panel</Dialog.Title>
          <AgentPanel instance="overlay" />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
