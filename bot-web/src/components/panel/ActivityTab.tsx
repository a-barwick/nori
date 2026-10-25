"use client";

import { useEffect, useRef, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { Bell, Check, Globe, List, Pencil, Search, type LucideIcon } from "lucide-react";
import { EmptyTab, SectionHeader } from "@/components/ui/primitives";
import { useNow } from "@/lib/hooks";
import { useNori } from "@/lib/store";
import { clock, dayLabel } from "@/lib/time";
import type { ActivityEntry, ActivityType } from "@/lib/types";
import { cn } from "@/lib/utils";
import { scrollWithin } from "@/lib/dom";

export const ACTIVITY_ICON: Record<ActivityType, LucideIcon> = {
  done: Check,
  web: Globe,
  memory: Search,
  drafted: Pencil,
  notified: Bell,
};

function groupByDay<T extends { at: number }>(rows: T[], now: number) {
  const groups: { label: string; rows: T[] }[] = [];
  for (const row of rows) {
    const label = dayLabel(row.at, now);
    const last = groups[groups.length - 1];
    if (last?.label === label) last.rows.push(row);
    else groups.push({ label, rows: [row] });
  }
  return groups;
}

function KeyValues({ data }: { data: Record<string, string> }) {
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
      {Object.entries(data).map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="font-mono text-mono-data text-ink-3">{k}</dt>
          <dd className="min-w-0 font-mono text-mono-data [overflow-wrap:anywhere] text-ink">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Row({ entry, focused }: { entry: ActivityEntry; focused: boolean }) {
  const [open, setOpen] = useState(focused);
  const [wasFocused, setWasFocused] = useState(focused);
  if (focused !== wasFocused) {
    setWasFocused(focused);
    if (focused) setOpen(true);
  }
  const ref = useRef<HTMLDivElement>(null);
  const router = useRouter();
  const pathname = usePathname();
  const set = useNori((s) => s.set);
  const highlightItem = useNori((s) => s.highlightItem);
  const Icon = ACTIVITY_ICON[entry.type];

  useEffect(() => {
    if (focused && ref.current) scrollWithin(ref.current, "center");
  }, [focused]);

  const showInChat = () => {
    if (!entry.messageId || !entry.threadId) return;
    set({ activeThreadId: entry.threadId, overlay: false });
    if (pathname !== "/") router.push("/");
    setTimeout(() => highlightItem(entry.messageId!), pathname === "/" ? 0 : 250);
  };

  return (
    <div ref={ref} className={cn("rounded-tile", focused && "flash")}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="t-fast -mx-2 flex w-[calc(100%+16px)] items-start gap-3 rounded-tile px-2 py-2 text-left transition-colors hover:bg-fill-1/60"
      >
        <span className="grid size-10 shrink-0 place-items-center rounded-tile bg-fill-1 text-ink">
          <Icon size={20} strokeWidth={1.5} aria-hidden />
        </span>
        <span className="min-w-0 flex-1 pt-0.5">
          <span className="block text-row-title font-medium text-ink">{entry.title}</span>
          <span className="line-clamp-2 block text-meta text-ink-2">{entry.summary}</span>
        </span>
        <span className="shrink-0 pt-0.5 text-caption text-ink-3">{clock(entry.at)}</span>
      </button>
      {open && (
        <div className="anim-fade mb-2 ml-[52px] flex flex-col gap-3 rounded-tile bg-surface p-3 shadow-[inset_0_0_0_1px_var(--hairline)]">
          {entry.inputs && (
            <div>
              <div className="mb-1 font-mono text-mono-label text-ink-3 uppercase">Input</div>
              <KeyValues data={entry.inputs} />
            </div>
          )}
          {entry.outputs && (
            <div>
              <div className="mb-1 font-mono text-mono-label text-ink-3 uppercase">Output</div>
              <KeyValues data={entry.outputs} />
            </div>
          )}
          {entry.messageId && (
            <button type="button" onClick={showInChat} className="self-start text-meta font-medium text-accent-text hover:underline">
              Show in chat
            </button>
          )}
        </div>
      )}
    </div>
  );
}

export function ActivityTab() {
  const activity = useNori((s) => s.activity);
  const focusId = useNori((s) => s.focusActivityId);
  const now = useNow();
  const rows = [...activity].sort((a, b) => b.at - a.at);
  if (!rows.length) return <EmptyTab icon={List} title="Activity" line="What I do shows up here" />;
  return (
    <div>
      {groupByDay(rows, now).map((g, i) => (
        <section key={g.label}>
          <SectionHeader first={i === 0}>{g.label}</SectionHeader>
          {g.rows.map((entry) => (
            <Row key={entry.id} entry={entry} focused={entry.id === focusId} />
          ))}
        </section>
      ))}
    </div>
  );
}

export { groupByDay };
