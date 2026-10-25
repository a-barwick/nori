"use client";

import { usePathname, useRouter } from "next/navigation";
import { CalendarDays, Clock, Eye, Gift, Heart, Package, Pause, Pencil, Play, Printer, Receipt, Sun, type LucideIcon } from "lucide-react";
import { EmptyTab, IconButton, SectionHeader } from "@/components/ui/primitives";
import { useNow } from "@/lib/hooks";
import { useNori } from "@/lib/store";
import { nextRun, upcomingAt } from "@/lib/time";
import type { UpcomingCadence, UpcomingGlyph, UpcomingTask } from "@/lib/types";
import { cn } from "@/lib/utils";

const GLYPH: Record<UpcomingGlyph, LucideIcon> = {
  heart: Heart,
  eye: Eye,
  sun: Sun,
  calendar: CalendarDays,
  package: Package,
  printer: Printer,
  gift: Gift,
  receipt: Receipt,
};

const GROUPS: { key: UpcomingCadence; label: string }[] = [
  { key: "daily", label: "Daily" },
  { key: "week", label: "This week" },
  { key: "later", label: "Later" },
];

function Row({ task, now, at }: { task: UpcomingTask; now: number; at: number }) {
  const toggle = useNori((s) => s.togglePauseUpcoming);
  const set = useNori((s) => s.set);
  const router = useRouter();
  const pathname = usePathname();
  const Icon = GLYPH[task.glyph];
  const heartbeat = task.glyph === "heart";

  const edit = () => {
    set({
      activeThreadId: "main",
      overlay: false,
      drafts: { ...useNori.getState().drafts, main: `Change “${task.title}”: ` },
      composerFocus: Date.now(),
    });
    if (pathname !== "/") router.push("/");
  };

  return (
    <div className="group/row relative flex items-center gap-3 py-2">
      <span className={cn("grid size-10 shrink-0 place-items-center rounded-full bg-fill-1", heartbeat ? "text-heartbeat" : "text-ink")}>
        <Icon size={20} strokeWidth={1.5} aria-hidden />
      </span>
      <span className={cn("min-w-0 flex-1", task.paused && "opacity-60")}>
        <span className="block text-row-title font-medium text-ink">{task.title}</span>
        <span className="line-clamp-2 block text-meta text-ink-2">
          {task.paused ? `Paused · ${task.schedule}` : `${task.schedule} · next ${nextRun(at, now, / at |Once/.test(task.schedule))}`}
        </span>
      </span>
      <span className="t-fast flex gap-0.5 opacity-0 transition-opacity group-focus-within/row:opacity-100 group-hover/row:opacity-100">
        <IconButton size={32} label={task.paused ? "Resume" : "Pause"} onClick={() => toggle(task.id)}>
          {task.paused ? <Play size={16} strokeWidth={1.5} /> : <Pause size={16} strokeWidth={1.5} />}
        </IconButton>
        <IconButton size={32} label="Edit" onClick={edit}>
          <Pencil size={16} strokeWidth={1.5} />
        </IconButton>
      </span>
    </div>
  );
}

export function UpcomingTab() {
  const upcoming = useNori((s) => s.upcoming);
  const now = useNow();
  if (!upcoming.some((u) => upcomingAt(u, now) !== null)) return <EmptyTab icon={Clock} title="Upcoming" line="Nothing scheduled" />;
  return (
    <div>
      {GROUPS.map((g, i) => {
        const rows = upcoming
          .filter((u) => u.cadence === g.key)
          .map((task) => ({ task, at: upcomingAt(task, now) }))
          .filter((r): r is { task: UpcomingTask; at: number } => r.at !== null)
          .sort((a, b) => a.at - b.at);
        if (!rows.length) return null;
        return (
          <section key={g.key}>
            <SectionHeader first={i === 0}>{g.label}</SectionHeader>
            {rows.map(({ task, at }) => (
              <Row key={task.id} task={task} now={now} at={at} />
            ))}
          </section>
        );
      })}
    </div>
  );
}
