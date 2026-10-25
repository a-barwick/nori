"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { DropdownMenu } from "radix-ui";
import { Briefcase, Check, ChevronRight, Ellipsis, Heart, ListChecks, Palette, Plus, Users, Wallet, type LucideIcon } from "lucide-react";
import { Page, PageTitle, Subtitle } from "@/components/ui/Page";
import { startGoalCategory } from "@/lib/nori/engine";
import { useNori } from "@/lib/store";
import type { Goal, GoalCategory, GoalStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

const CATEGORIES: { key: GoalCategory; label: string; icon: LucideIcon }[] = [
  { key: "health", label: "Health", icon: Heart },
  { key: "relationships", label: "Relationships", icon: Users },
  { key: "finance", label: "Finance", icon: Wallet },
  { key: "career", label: "Career", icon: Briefcase },
  { key: "interests", label: "Interests", icon: Palette },
  { key: "productivity", label: "Productivity", icon: ListChecks },
  { key: "other", label: "Something else", icon: Plus },
];

function Checkbox({ checked, onChange, label }: { checked: boolean; onChange: () => void; label: string }) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation();
        onChange();
      }}
      className={cn(
        "t-fast mt-0.5 grid size-[18px] shrink-0 place-items-center rounded-check transition-colors",
        checked ? "bg-accent text-on-accent" : "shadow-[inset_0_0_0_1.5px_var(--hairline-strong)] hover:shadow-[inset_0_0_0_1.5px_var(--accent-signal)]",
      )}
    >
      {checked && <Check size={13} strokeWidth={2.5} aria-hidden />}
    </button>
  );
}

function GoalRow({ goal }: { goal: Goal }) {
  const set = useNori((s) => s.set);
  const toggle = () =>
    useNori.setState((s) => ({
      goals: s.goals.map((g) => (g.id === goal.id ? { ...g, status: g.status === "done" ? "tracking" : "done" } : g)),
    }));
  return (
    <li className="group/goal flex items-start gap-3 border-b border-hairline py-3 last:border-0">
      <Checkbox checked={goal.status === "done"} onChange={toggle} label={`Mark “${goal.title}” ${goal.status === "done" ? "not done" : "done"}`} />
      <button type="button" onClick={() => set({ goalSheetId: goal.id })} className="min-w-0 flex-1 text-left">
        <span className={cn("block text-row font-medium", goal.status === "done" ? "text-ink-2" : "text-ink")}>{goal.title}</span>
        <span className="block text-meta text-ink-2">
          {goal.blocked ? (
            <>
              Blocked: {goal.blocked.reason}{" "}
              <span className="font-medium text-accent-text group-hover/goal:underline">{goal.blocked.action} →</span>
            </>
          ) : (
            goal.line
          )}
        </span>
      </button>
    </li>
  );
}

function Group({ status, goals }: { status: GoalStatus; goals: Goal[] }) {
  const [open, setOpen] = useState(status !== "done");
  if (!goals.length) return null;
  const label = status === "tracking" ? "Tracking" : status === "paused" ? "Paused" : "Done";
  const header = (
    <span className={cn("flex items-center gap-2 text-section font-medium", status === "tracking" ? "text-accent-text" : "text-ink-3")}>
      {status === "tracking" && <span className="size-2 rounded-full bg-accent-signal" aria-hidden />}
      {label}
      {status === "done" && <span className="text-meta font-normal">{goals.length}</span>}
    </span>
  );
  return (
    <section className="mt-8">
      {status === "done" ? (
        <button type="button" aria-expanded={open} onClick={() => setOpen((o) => !o)} className="flex items-center gap-1">
          {header}
          <ChevronRight size={16} strokeWidth={1.5} className={cn("t-fast text-ink-3 transition-transform", open && "rotate-90")} aria-hidden />
        </button>
      ) : (
        <h2>{header}</h2>
      )}
      {open && (
        <ul className="mt-1">
          {goals.map((g) => (
            <GoalRow key={g.id} goal={g} />
          ))}
        </ul>
      )}
    </section>
  );
}

export default function GoalsPage() {
  const goals = useNori((s) => s.goals);
  const router = useRouter();
  const start = (c: GoalCategory) => {
    router.push("/");
    void startGoalCategory(c);
  };
  return (
    <Page>
      <PageTitle
        aside={
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <button
                type="button"
                aria-label="Goal options"
                className="t-fast grid h-9 w-12 place-items-center rounded-full bg-surface text-ink-2 shadow-[inset_0_0_0_1px_var(--hairline-strong)] transition-colors hover:bg-fill-1 hover:text-ink"
              >
                <Ellipsis size={18} strokeWidth={1.5} />
              </button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content align="end" sideOffset={6} className="menu z-[60] min-w-48 rounded-card bg-surface p-1.5 shadow-pop">
                <DropdownMenu.Item
                  onSelect={() => start("other")}
                  className="flex h-9 cursor-default items-center gap-2.5 rounded-chip px-2.5 text-row-title text-ink outline-none data-[highlighted]:bg-fill-1"
                >
                  <Plus size={16} strokeWidth={1.5} className="text-ink-2" /> New goal
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        }
      >
        Goals
      </PageTitle>
      <Subtitle>What you&apos;re working toward. I keep track and nudge when it helps.</Subtitle>

      <Group status="tracking" goals={goals.filter((g) => g.status === "tracking")} />
      <Group status="paused" goals={goals.filter((g) => g.status === "paused")} />
      <Group status="done" goals={goals.filter((g) => g.status === "done")} />

      <section className="mt-10">
        <h2 className="mb-1 text-section font-medium text-ink">Create a goal</h2>
        <ul>
          {CATEGORIES.map((c) => (
            <li key={c.key} className="border-b border-hairline last:border-0">
              <button
                type="button"
                onClick={() => start(c.key)}
                className="t-fast -mx-3 flex w-[calc(100%+24px)] items-center gap-3 rounded-row px-3 py-3 text-left transition-colors hover:bg-fill-1"
              >
                <c.icon size={20} strokeWidth={1.5} className="text-ink-2" aria-hidden />
                <span className="flex-1 text-row text-ink">{c.label}</span>
                <ChevronRight size={18} strokeWidth={1.5} className="text-ink-3" aria-hidden />
              </button>
            </li>
          ))}
        </ul>
      </section>
    </Page>
  );
}
