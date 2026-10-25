"use client";

import { useRouter } from "next/navigation";
import { Dialog } from "radix-ui";
import { X } from "lucide-react";
import { Button, IconButton } from "@/components/ui/primitives";
import { Sheet } from "@/components/ui/layers";
import { talkAboutGoal } from "@/lib/nori/engine";
import { useNori } from "@/lib/store";
import { shortDate } from "@/lib/time";
import type { GoalStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

export function StatusPill({ status }: { status: GoalStatus }) {
  return (
    <span
      className={cn(
        "inline-flex h-6 items-center gap-1.5 rounded-full px-2.5 text-caption font-medium",
        status === "tracking" && "bg-accent-muted text-accent-text",
        status === "paused" && "bg-fill-1 text-ink-3",
        status === "done" && "bg-fill-1 text-ink-2",
      )}
    >
      {status === "tracking" && <span className="size-1.5 rounded-full bg-accent-signal" aria-hidden />}
      {status === "tracking" ? "Tracking" : status === "paused" ? "Paused" : "Done"}
    </span>
  );
}

export function GoalSheet() {
  const goalId = useNori((s) => s.goalSheetId);
  const goal = useNori((s) => s.goals.find((g) => g.id === s.goalSheetId));
  const set = useNori((s) => s.set);
  const router = useRouter();

  return (
    <Sheet
      open={!!goalId}
      onOpenChange={(o) => !o && set({ goalSheetId: null })}
      title={goal?.title ?? "Goal"}
      header={
        goal && (
          <div className="flex items-start gap-3 px-6 pt-6 pb-2">
            <div className="min-w-0 flex-1">
              <StatusPill status={goal.status} />
              <h2 className="mt-2 text-title font-medium text-ink">{goal.title}</h2>
              <p className={cn("mt-1 text-meta", goal.blocked ? "text-ink" : "text-ink-2")}>{goal.line}</p>
            </div>
            <Dialog.Close asChild>
              <IconButton label="Close" size={32} tip={false}>
                <X size={18} strokeWidth={1.5} />
              </IconButton>
            </Dialog.Close>
          </div>
        )
      }
    >
      {goal && (
        <div className="px-6 pb-8">
          <div className="mt-4 rounded-card bg-canvas p-4 shadow-[inset_0_0_0_1px_var(--hairline)]">
            <div className="text-row-title font-medium text-ink">Next step</div>
            <p className="mt-1 text-meta text-ink-2">{goal.nextStep}</p>
            <Button
              variant="primary"
              className="mt-3"
              onClick={() => {
                router.push("/");
                void talkAboutGoal(goal.id);
              }}
            >
              Talk about it
            </Button>
          </div>

          <h3 className="mt-7 mb-3 text-section font-medium text-ink">Progress</h3>
          <ol className="relative ml-1">
            {goal.progress.map((e, i) => (
              <li key={e.at} className="relative flex gap-4 pb-5 last:pb-0">
                {i < goal.progress.length - 1 && <span className="absolute top-3 left-[4px] h-full w-px bg-hairline-strong" aria-hidden />}
                <span className={cn("relative mt-1.5 size-[9px] shrink-0 rounded-full", i === 0 ? "bg-accent-signal" : "bg-fill-2 shadow-[inset_0_0_0_1px_var(--hairline-strong)]")} aria-hidden />
                <div className="min-w-0">
                  <div className="text-row-title text-ink">{e.text}</div>
                  <div className="text-caption text-ink-3">{shortDate(e.at)}</div>
                </div>
              </li>
            ))}
          </ol>

          <h3 className="mt-7 mb-1 text-section font-medium text-ink">History</h3>
          <ul>
            {goal.history.map((e) => (
              <li key={e.at} className="flex items-baseline justify-between gap-4 border-b border-hairline py-2.5 last:border-0">
                <span className="text-meta text-ink-2">{e.text}</span>
                <span className="shrink-0 text-caption text-ink-3">{shortDate(e.at)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </Sheet>
  );
}
