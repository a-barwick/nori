"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useMemo, useState } from "react";
import { Check, ChevronRight, Clock, X } from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { when } from "@/lib/assistant/format";
import { isActionableInteraction } from "@/lib/assistant/interactions";
import { usePendingInteractions, useWork } from "@/lib/assistant/sources";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { Automation, Interaction, WorkItem, WorkStep } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { InteractionCard } from "./InteractionCard";
import { LibraryContent } from "./LibraryScreen";
import { RoutinesContent } from "./RoutinesScreen";
import { Failure, Loading, Notice, Screen, Section, UnderlineTabs, type Tone } from "./ui";

export {
  groupOperations,
  operationOutcomePill,
  OperationRows,
  shownOperations,
  type OperationDisplayGroup,
  type OperationRowItem,
} from "./WorkOperations";
export { isRetryableWorkDetailError, WorkDetailScreen } from "./WorkDetailScreen";

type WorkTab = "artifacts" | "automations" | "tasks";

export function parseWorkTab(raw: string | null): WorkTab {
  if (raw === "artifacts" || raw === "library") return "artifacts";
  if (raw === "automations" || raw === "routines" || raw === "scheduled") return "automations";
  return "tasks";
}

export function isPartialWorkItem(item: WorkItem): boolean {
  return Boolean(item.partial || item.error?.code === "task_incomplete");
}

export function statusPill(item: WorkItem): { tone: Tone; label: string } {
  if (item.cancel_requested && item.status === "running") return { tone: "neutral", label: "Stopping" };
  if (item.bucket === "needs_you") return { tone: "warning", label: "Action needed" };
  if (item.status === "running") {
    return item.operations.uncertain ? { tone: "warning", label: "Checking" } : { tone: "working", label: "Working" };
  }
  if (item.status === "completed") {
    return item.operations.uncertain ? { tone: "warning", label: "Unconfirmed" } : { tone: "ok", label: "Done" };
  }
  if (isPartialWorkItem(item)) return { tone: "warning", label: "Partial" };
  if (item.status === "failed") return { tone: "danger", label: "Failed" };
  return { tone: "neutral", label: "Stopped" };
}

export type UnifiedTaskEntry =
  | {
      kind: "in_progress";
      id: string;
      taskId: string;
      title: string;
      steps: WorkStep[];
      cancelRequested: boolean;
    }
  | {
      kind: "todo";
      id: string;
      taskId?: string;
      title: string;
      replyHref?: string;
      cancelRequested?: boolean;
    }
  | {
      kind: "scheduled";
      id: string;
      automation: Automation;
      title: string;
      scheduleLabel: string;
    };

export function buildUnifiedTaskEntries({
  workItems,
  pendingInteractions,
  automations,
}: {
  workItems: WorkItem[];
  pendingInteractions: Interaction[];
  automations: Automation[];
}): {
  actionableInteractions: Interaction[];
  entries: UnifiedTaskEntry[];
} {
  const actionableInteractions = pendingInteractions.filter(isActionableInteraction);
  const chatWaitingQuestions = pendingInteractions.filter((i) => !isActionableInteraction(i));
  const actionableTaskIDs = new Set(actionableInteractions.map((i) => i.task_id).filter(Boolean));
  const chatWaitingTaskIDs = new Set(chatWaitingQuestions.map((i) => i.task_id).filter(Boolean));

  const runningEntries: UnifiedTaskEntry[] = [];
  const queuedEntries: UnifiedTaskEntry[] = [];
  const seenTaskIDs = new Set<string>();

  for (const item of workItems) {
    if (item.status !== "running" || item.cancel_requested) continue;
    if (actionableTaskIDs.has(item.task_id)) continue;
    if (item.bucket === "needs_you" || chatWaitingTaskIDs.has(item.task_id)) continue;
    seenTaskIDs.add(item.task_id);
    if (item.queued) {
      queuedEntries.push({
        kind: "todo",
        id: `task:${item.task_id}`,
        taskId: item.task_id,
        title: item.title,
        cancelRequested: item.cancel_requested,
      });
    } else {
      runningEntries.push({
        kind: "in_progress",
        id: `task:${item.task_id}`,
        taskId: item.task_id,
        title: item.title,
        steps: item.steps ?? [],
        cancelRequested: item.cancel_requested,
      });
    }
  }

  const waitingInChat: { taskId?: string; title: string }[] = [];
  const waitingSeen = new Set<string>();
  for (const q of chatWaitingQuestions) {
    const workItem = q.task_id ? workItems.find((item) => item.task_id === q.task_id) : undefined;
    const key = q.task_id || q.id;
    if (!waitingSeen.has(key)) {
      waitingSeen.add(key);
      waitingInChat.push({
        taskId: q.task_id,
        title: workItem?.title || q.task_title || "Waiting in Chat",
      });
    }
  }
  for (const item of workItems) {
    if (
      item.status === "running" &&
      !item.cancel_requested &&
      item.bucket === "needs_you" &&
      !actionableTaskIDs.has(item.task_id) &&
      !waitingSeen.has(item.task_id) &&
      !seenTaskIDs.has(item.task_id)
    ) {
      waitingSeen.add(item.task_id);
      waitingInChat.push({ taskId: item.task_id, title: item.title });
    }
  }

  const waitingEntries: UnifiedTaskEntry[] = waitingInChat.map((entry) => ({
    kind: "todo",
    id: `chat:${entry.taskId || entry.title}`,
    taskId: entry.taskId,
    title: entry.title,
    replyHref:
      waitingInChat.length > 1
        ? `/?compose=1&reply_title=${encodeURIComponent(entry.title)}`
        : "/?compose=1",
  }));

  const onceScheduledAt = (item: Automation): string | undefined =>
    item.next_at ?? (item.trigger.kind === "once" ? item.trigger.at : undefined);

  const scheduledEntries: UnifiedTaskEntry[] = automations
    .filter((item) => item.status === "enabled" && item.trigger.kind === "once" && Boolean(onceScheduledAt(item)))
    .sort((a, b) => Date.parse(onceScheduledAt(a) ?? "9999") - Date.parse(onceScheduledAt(b) ?? "9999"))
    .map((item) => ({
      kind: "scheduled",
      id: `auto:${item.id}`,
      automation: item,
      title: item.title,
      scheduleLabel: when(onceScheduledAt(item)),
    }));

  return {
    actionableInteractions,
    entries: [...runningEntries, ...waitingEntries, ...queuedEntries, ...scheduledEntries],
  };
}

export function WorkScreen() {
  const params = useSearchParams();
  const router = useRouter();
  const { has } = useAssistant();
  const work = useWork();
  const pending = usePendingInteractions();
  const automationsRes = useResource<{ items: Automation[] }>(has("automations") ? "/automations" : null, {
    poll: 30_000,
  });

  const tab = parseWorkTab(params.get("tab"));
  const selectTab = (next: WorkTab) => {
    router.replace(next === "tasks" ? "/work" : `/work?tab=${next}`, { scroll: false });
  };

  const { actionableInteractions, entries } = useMemo(
    () =>
      buildUnifiedTaskEntries({
        workItems: work.items ?? [],
        pendingInteractions: pending.items ?? [],
        automations: automationsRes.data?.items ?? [],
      }),
    [automationsRes.data?.items, pending.items, work.items],
  );

  const actionableTotal = actionableInteractions.length;

  return (
    <Screen
      title="Work"
      hideHeaderTitle
      tabs={
        <UnderlineTabs<WorkTab>
          label="Work"
          value={tab}
          onChange={selectTab}
          items={[
            { id: "artifacts", label: "Artifacts" },
            { id: "automations", label: "Automations" },
            { id: "tasks", label: "Tasks", count: actionableTotal || undefined },
          ]}
        />
      }
    >
      {tab === "artifacts" && <LibraryContent />}
      {tab === "automations" && <RoutinesContent />}
      {tab === "tasks" && (
        <TasksHub
          loading={work.loading || pending.loading}
          error={work.error}
          onRetry={() => void work.reload()}
          pendingError={pending.error}
          pendingRefreshing={pending.refreshing}
          onRetryPending={() => void pending.reload()}
          actionableInteractions={actionableInteractions}
          entries={entries}
          liveWork={work.live}
        />
      )}
    </Screen>
  );
}

function CircularProgressRing({ steps }: { steps: WorkStep[] }) {
  const radius = 7.4;
  const circumference = 2 * Math.PI * radius;
  const doneCount = steps.filter((step) => step.status === "done").length;
  const indeterminate = steps.length === 0 || doneCount === 0;
  const fraction = indeterminate ? 0.28 : Math.min(0.95, doneCount / steps.length);
  const offset = circumference * (1 - fraction);
  const label = steps.length > 0 ? `${doneCount} of ${steps.length} steps` : "In progress";

  return (
    <svg
      className={cn(
        "block size-[19px] -rotate-90",
        indeterminate && "animate-[spin_1.8s_linear_infinite] motion-reduce:animate-none",
      )}
      viewBox="0 0 20 20"
      role="img"
      aria-label={label}
    >
      <circle cx="10" cy="10" r={radius} fill="none" stroke="var(--fill-2)" strokeWidth="2.4" />
      <circle
        cx="10"
        cy="10"
        r={radius}
        fill="none"
        stroke="var(--accent)"
        strokeWidth="2.4"
        strokeLinecap="round"
        strokeDasharray={circumference.toFixed(2)}
        strokeDashoffset={offset.toFixed(2)}
        className="transition-[stroke-dashoffset] duration-200"
      />
    </svg>
  );
}

function StepMarker({ status }: { status: WorkStep["status"] }) {
  if (status === "done") {
    return (
      <span className="mt-0.5 grid size-[14px] shrink-0 place-items-center rounded-full bg-ok-bg text-ok">
        <Check size={9} strokeWidth={3} aria-hidden />
      </span>
    );
  }
  if (status === "active") {
    return (
      <span className="mt-0.5 grid size-[14px] shrink-0 place-items-center">
        <span className="pulse-dot size-[6.5px] rounded-full bg-accent" />
      </span>
    );
  }
  return (
    <span className="mt-0.5 grid size-[14px] shrink-0 place-items-center">
      <span className="size-[6.5px] rounded-full border-[1.5px] border-ink-3/60" />
    </span>
  );
}

function TasksHub({
  loading,
  error,
  onRetry,
  pendingError,
  pendingRefreshing,
  onRetryPending,
  actionableInteractions,
  entries,
  liveWork,
}: {
  loading: boolean;
  error?: unknown;
  onRetry: () => void;
  pendingError?: unknown;
  pendingRefreshing: boolean;
  onRetryPending: () => void;
  actionableInteractions: Interaction[];
  entries: UnifiedTaskEntry[];
  liveWork: boolean;
}) {
  const { runtime } = useAssistant();
  const invalidate = useResourceInvalidation();
  const action = useAction();
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  const [dismissedIds, setDismissedIds] = useState<Set<string>>(() => new Set());

  const visibleEntries = useMemo(
    () => entries.filter((entry) => !dismissedIds.has(entry.id)),
    [dismissedIds, entries],
  );
  const actionableTotal = actionableInteractions.length;

  const cancelEntry = (entry: UnifiedTaskEntry) => {
    setConfirmingId(null);
    setDismissedIds((prev) => new Set(prev).add(entry.id));
    if (expandedId === entry.id) setExpandedId(null);

    if (entry.kind === "scheduled") {
      void action.run(
        `cancel:${entry.id}`,
        async () => {
          try {
            await mutate<Automation>(`/automations/${encodeURIComponent(entry.automation.id)}/actions`, {
              request_id: newRequestID(),
              expected_revision: entry.automation.revision,
              value: { action: "cancel" },
            });
            invalidate("/automations", "/work", "/assistant/summary");
          } catch (err) {
            setDismissedIds((prev) => {
              const next = new Set(prev);
              next.delete(entry.id);
              return next;
            });
            throw err;
          }
        },
        `Cancelled ${entry.title}.`,
      );
      return;
    }

    const taskId = entry.taskId;
    if (!taskId) return;
    void action.run(
      `cancel:${entry.id}`,
      async () => {
        try {
          if (liveWork) {
            await mutate(`/work/${encodeURIComponent(taskId)}/actions`, {
              request_id: newRequestID(),
              expected_revision: 0,
              value: { action: "stop" },
            });
            invalidate("/work", "/interactions", "/assistant/summary");
          } else {
            await runtime.commandTask(taskId, "stop");
          }
        } catch (err) {
          setDismissedIds((prev) => {
            const next = new Set(prev);
            next.delete(entry.id);
            return next;
          });
          throw err;
        }
      },
      `Cancelled ${entry.title}.`,
    );
  };

  if (loading && !error && !pendingError && !actionableTotal && !visibleEntries.length) {
    return <Loading />;
  }

  return (
    <div className="space-y-5" onClick={() => confirmingId && setConfirmingId(null)}>
      {error ? <Failure error={error} onRetry={onRetry} /> : null}
      {pendingError ? <Failure error={pendingError} onRetry={onRetryPending} /> : null}
      {action.error ? <Notice tone="danger">{action.error}</Notice> : null}

      {actionableTotal > 0 ? (
        <Section title={actionableTotal > 1 ? `Action needed · ${actionableTotal}` : "Action needed"}>
          <div className="space-y-3">
            {actionableInteractions.map((interaction) => (
              <InteractionCard key={interaction.id} interaction={interaction} />
            ))}
          </div>
        </Section>
      ) : visibleEntries.length === 0 && !error && !pendingError && !loading && !pendingRefreshing ? (
        <div
          data-all-clear
          className="flex items-center gap-3 rounded-card border border-card-border bg-surface px-4 py-3"
        >
          <span className="grid size-8 shrink-0 place-items-center rounded-[10px] bg-fill-1">
            <NoriMark size={22} state="idle" />
          </span>
          <p className="text-[15px] font-semibold text-ink">All clear</p>
        </div>
      ) : null}

      {visibleEntries.length > 0 && (
        <ul
          aria-label="Tasks"
          className="divide-y divide-hairline overflow-hidden rounded-card border border-card-border bg-surface"
        >
          {visibleEntries.map((entry) => {
            const hasSteps = entry.kind === "in_progress" && entry.steps.length > 0;
            const isExpanded = hasSteps && expandedId === entry.id;
            const isConfirming = confirmingId === entry.id;
            const canCancel = entry.kind === "scheduled" || Boolean(entry.taskId);

            return (
              <li key={entry.id} data-task-row={entry.id}>
                <div
                  role={hasSteps ? "button" : undefined}
                  tabIndex={hasSteps ? 0 : undefined}
                  aria-expanded={hasSteps ? isExpanded : undefined}
                  onClick={() => {
                    if (hasSteps) setExpandedId((cur) => (cur === entry.id ? null : entry.id));
                  }}
                  onKeyDown={(event) => {
                    if (hasSteps && (event.key === "Enter" || event.key === " ")) {
                      event.preventDefault();
                      setExpandedId((cur) => (cur === entry.id ? null : entry.id));
                    }
                  }}
                  className={cn(
                    "group flex min-h-[50px] items-center gap-3 px-4 py-3 transition-colors hover:bg-fill-1",
                    hasSteps && "cursor-pointer",
                  )}
                >
                  <span className="grid size-5 shrink-0 place-items-center">
                    {entry.kind === "in_progress" ? (
                      <CircularProgressRing steps={entry.steps} />
                    ) : entry.kind === "todo" ? (
                      <span className="size-[15px] rounded-full border-[1.75px] border-hairline-strong" aria-hidden />
                    ) : (
                      <Clock size={15} strokeWidth={2} className="text-scheduled" aria-hidden />
                    )}
                  </span>

                  <span className="min-w-0 flex-1">
                    <span
                      className={cn(
                        "block truncate text-row",
                        entry.kind === "scheduled" ? "font-medium text-ink-2" : "font-semibold text-ink",
                      )}
                    >
                      {entry.title}
                    </span>
                  </span>

                  <div className="flex shrink-0 items-center gap-2" onClick={(event) => event.stopPropagation()}>
                    {isConfirming ? (
                      <>
                        <button
                          type="button"
                          onClick={() => cancelEntry(entry)}
                          className="inline-flex h-7 items-center rounded-full bg-danger-bg px-2.5 text-caption font-semibold text-danger transition-opacity hover:opacity-90"
                        >
                          Cancel
                        </button>
                        <button
                          type="button"
                          onClick={() => setConfirmingId(null)}
                          className="inline-flex h-7 items-center rounded-full bg-fill-1 px-2.5 text-caption font-semibold text-ink-2 hover:bg-fill-2 hover:text-ink"
                        >
                          Keep
                        </button>
                      </>
                    ) : (
                      <>
                        {entry.kind === "todo" && entry.replyHref && (
                          <Link
                            href={entry.replyHref}
                            className="shrink-0 rounded-full bg-accent-subtle px-3 py-1 text-caption font-semibold text-accent-text transition-colors hover:bg-fill-2"
                          >
                            Reply
                          </Link>
                        )}
                        {entry.kind === "scheduled" && entry.scheduleLabel && (
                          <span className="text-caption font-medium whitespace-nowrap text-scheduled">
                            {entry.scheduleLabel}
                          </span>
                        )}
                        {canCancel && (
                          <button
                            type="button"
                            title="Cancel task"
                            aria-label={`Cancel ${entry.title}`}
                            onClick={() => setConfirmingId(entry.id)}
                            className="grid size-[26px] place-items-center rounded-full text-ink-3 opacity-60 transition-[opacity,background-color,color] hover:bg-danger-bg hover:text-danger hover:opacity-100 focus-visible:opacity-100 sm:opacity-0 sm:group-hover:opacity-100"
                          >
                            <X size={14} strokeWidth={2.1} aria-hidden />
                          </button>
                        )}
                        {hasSteps && (
                          <ChevronRight
                            size={14}
                            className={cn(
                              "text-ink-3 transition-transform duration-150",
                              isExpanded && "rotate-90",
                            )}
                            aria-hidden
                          />
                        )}
                      </>
                    )}
                  </div>
                </div>

                {isExpanded && entry.kind === "in_progress" && (
                  <div className="px-4 pb-3.5 pl-12">
                    <ul className="space-y-1.5" aria-label={`Steps for ${entry.title}`}>
                      {entry.steps.map((step, index) => (
                        <li
                          key={`${entry.id}-step-${index}`}
                          className={cn(
                            "flex items-start gap-2.5 text-[13px] leading-snug",
                            step.status === "done"
                              ? "text-ink-3"
                              : step.status === "active"
                                ? "font-semibold text-ink"
                                : "text-ink-2",
                          )}
                        >
                          <StepMarker status={step.status} />
                          <span className="min-w-0 flex-1">{step.text}</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
