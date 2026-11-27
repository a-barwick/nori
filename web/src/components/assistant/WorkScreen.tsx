"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useMemo, useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { relative } from "@/lib/assistant/format";
import { isActionableInteraction } from "@/lib/assistant/interactions";
import { usePendingInteractions, useWork } from "@/lib/assistant/sources";
import type { Interaction, WorkItem } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { GoalsContent } from "./GoalsScreen";
import { InteractionCard } from "./InteractionCard";
import { LibraryContent } from "./LibraryScreen";
import { RoutinesContent } from "./RoutinesScreen";
import { Empty, Failure, Loading, Pill, Row, Rows, Screen, Section, UnderlineTabs, type Tone } from "./ui";

export {
  groupOperations,
  operationOutcomePill,
  OperationRows,
  shownOperations,
  type OperationDisplayGroup,
  type OperationRowItem,
} from "./WorkOperations";
export { isRetryableWorkDetailError, WorkDetailScreen } from "./WorkDetailScreen";

type WorkTab = "tasks" | "artifacts" | "goals" | "routines";

function parseWorkTab(raw: string | null): WorkTab {
  if (raw === "artifacts" || raw === "library") return "artifacts";
  if (raw === "goals") return "goals";
  if (raw === "routines" || raw === "scheduled") return "routines";
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

export function WorkScreen() {
  const params = useSearchParams();
  const router = useRouter();
  const work = useWork();
  const pending = usePendingInteractions();

  const tab = parseWorkTab(params.get("tab"));
  const selectTab = (next: WorkTab) => {
    router.replace(next === "tasks" ? "/work" : `/work?tab=${next}`, { scroll: false });
  };

  const items = useMemo(() => work.items ?? [], [work.items]);
  const pendingInteractions = useMemo(() => pending.items ?? [], [pending.items]);
  const actionableInteractions = useMemo(
    () => pendingInteractions.filter(isActionableInteraction),
    [pendingInteractions],
  );
  const chatWaitingQuestions = useMemo(
    () => pendingInteractions.filter((i) => !isActionableInteraction(i)),
    [pendingInteractions],
  );
  const actionableTaskIDs = useMemo(
    () => new Set(actionableInteractions.map((i) => i.task_id).filter(Boolean)),
    [actionableInteractions],
  );
  const chatWaitingTaskIDs = useMemo(
    () => new Set(chatWaitingQuestions.map((i) => i.task_id).filter(Boolean)),
    [chatWaitingQuestions],
  );

  const waitingInChatTasks = useMemo(() => {
    const byTaskID = new Map<string, { taskId?: string; title: string }>();
    for (const q of chatWaitingQuestions) {
      const workItem = q.task_id ? items.find((item) => item.task_id === q.task_id) : undefined;
      const key = q.task_id || q.id;
      if (!byTaskID.has(key)) {
        byTaskID.set(key, {
          taskId: q.task_id,
          title: workItem?.title || q.task_title || "Waiting in Chat",
        });
      }
    }
    for (const item of items) {
      if (item.bucket === "needs_you" && !actionableTaskIDs.has(item.task_id) && !byTaskID.has(item.task_id)) {
        byTaskID.set(item.task_id, { taskId: item.task_id, title: item.title });
      }
    }
    return [...byTaskID.values()];
  }, [actionableTaskIDs, chatWaitingQuestions, items]);

  const runningTasks = useMemo(
    () =>
      items.filter(
        (item) =>
          item.status === "running" &&
          item.bucket !== "needs_you" &&
          !actionableTaskIDs.has(item.task_id) &&
          !chatWaitingTaskIDs.has(item.task_id),
      ),
    [actionableTaskIDs, chatWaitingTaskIDs, items],
  );
  const finishedTasks = useMemo(
    () =>
      items
        .filter((item) => item.status !== "running")
        .sort((a, b) => Date.parse(b.finished_at ?? b.started_at) - Date.parse(a.finished_at ?? a.started_at)),
    [items],
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
            { id: "tasks", label: "Tasks", count: actionableTotal || undefined },
            { id: "artifacts", label: "Artifacts" },
            { id: "goals", label: "Goals" },
            { id: "routines", label: "Routines" },
          ]}
        />
      }
    >
      {tab === "tasks" && (
        <TasksHub
          loading={work.loading || pending.loading}
          error={work.error}
          onRetry={() => void work.reload()}
          pendingError={pending.error}
          pendingRefreshing={pending.refreshing}
          onRetryPending={() => void pending.reload()}
          actionableInteractions={actionableInteractions}
          waitingInChatTasks={waitingInChatTasks}
          runningTasks={runningTasks}
          finishedTasks={finishedTasks}
        />
      )}
      {tab === "artifacts" && <LibraryContent />}
      {tab === "goals" && <GoalsContent />}
      {tab === "routines" && <RoutinesContent />}
    </Screen>
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
  waitingInChatTasks,
  runningTasks,
  finishedTasks,
}: {
  loading: boolean;
  error?: unknown;
  onRetry: () => void;
  pendingError?: unknown;
  pendingRefreshing: boolean;
  onRetryPending: () => void;
  actionableInteractions: Interaction[];
  waitingInChatTasks: { taskId?: string; title: string }[];
  runningTasks: WorkItem[];
  finishedTasks: WorkItem[];
}) {
  const [showAllRecent, setShowAllRecent] = useState(false);
  const actionableTotal = actionableInteractions.length;
  const activeTotal = waitingInChatTasks.length + runningTasks.length;
  const visibleRecent = showAllRecent ? finishedTasks : finishedTasks.slice(0, 5);

  if (loading && !error && !pendingError && !actionableTotal && !activeTotal && !finishedTasks.length) {
    return <Loading />;
  }

  return (
    <div className="space-y-6">
      {error ? <Failure error={error} onRetry={onRetry} /> : null}
      {pendingError ? <Failure error={pendingError} onRetry={onRetryPending} /> : null}

      {actionableTotal > 0 ? (
        <Section title={actionableTotal > 1 ? `Action needed · ${actionableTotal}` : "Action needed"}>
          <div className="space-y-3">
            {actionableInteractions.map((interaction) => (
              <InteractionCard key={interaction.id} interaction={interaction} />
            ))}
          </div>
        </Section>
      ) : activeTotal === 0 && !error && !pendingError && !loading && !pendingRefreshing ? (
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

      {activeTotal > 0 && (
        <Section title="Active">
          <Rows label="Active tasks">
            {waitingInChatTasks.map((entry) => {
              const replyHref =
                waitingInChatTasks.length > 1
                  ? `/?compose=1&reply_title=${encodeURIComponent(entry.title)}`
                  : "/?compose=1";
              return (
                <div
                  key={entry.taskId || entry.title}
                  className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-fill-1"
                >
                  {entry.taskId ? (
                    <Link href={`/work/${encodeURIComponent(entry.taskId)}`} className="min-w-0 flex-1">
                      <span className="block truncate text-row font-semibold text-ink">{entry.title}</span>
                      <span className="mt-0.5 block truncate text-meta text-ink-2">Waiting in Chat</span>
                    </Link>
                  ) : (
                    <div className="min-w-0 flex-1">
                      <span className="block truncate text-row font-semibold text-ink">{entry.title}</span>
                      <span className="mt-0.5 block truncate text-meta text-ink-2">Waiting in Chat</span>
                    </div>
                  )}
                  <Link
                    href={replyHref}
                    className="shrink-0 rounded-full bg-accent-subtle px-3 py-1 text-caption font-semibold text-accent-text transition-colors hover:bg-fill-2"
                  >
                    Reply
                  </Link>
                </div>
              );
            })}
            {runningTasks.map((item) => (
              <Link
                key={item.task_id}
                href={`/work/${encodeURIComponent(item.task_id)}`}
                className="flex items-center gap-3 px-4 py-3 transition-colors hover:bg-fill-1"
              >
                <span
                  className={cn(
                    "size-[7.5px] shrink-0 rounded-full",
                    item.operations.uncertain > 0 ? "bg-warning" : "bg-accent",
                    !item.cancel_requested && "pulse-dot",
                  )}
                />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-row font-semibold text-ink">{item.title}</span>
                  <span className="mt-0.5 block truncate text-meta text-ink-2">
                    {item.cancel_requested ? "Stopping…" : item.progress || `Started ${relative(item.started_at)}`}
                  </span>
                </span>
                {item.operations.uncertain > 0 && (
                  <AlertTriangle size={14} className="shrink-0 text-warning" aria-label="Has an unconfirmed action" />
                )}
                <ChevronRight size={15} className="shrink-0 text-ink-3" aria-hidden />
              </Link>
            ))}
          </Rows>
        </Section>
      )}

      {finishedTasks.length > 0 ? (
        <Section title="Recent">
          <Rows label="Recent tasks">
            {visibleRecent.map((item) => (
              <WorkRow key={item.task_id} item={item} />
            ))}
          </Rows>
          {finishedTasks.length > 5 && (
            <button
              type="button"
              onClick={() => setShowAllRecent((open) => !open)}
              className="mt-2 flex w-full items-center justify-center gap-1 py-2 text-meta font-semibold text-ink-2 hover:text-ink"
            >
              <span>{showAllRecent ? "Show fewer" : `Show all (${finishedTasks.length})`}</span>
              <ChevronDown size={14} className={cn("transition-transform", showAllRecent && "rotate-180")} aria-hidden />
            </button>
          )}
        </Section>
      ) : !runningTasks.length ? (
        <Section title="Recent">
          <Empty>Finished tasks appear here.</Empty>
        </Section>
      ) : null}
    </div>
  );
}

function WorkRow({ item }: { item: WorkItem }) {
  const pill = statusPill(item);
  const detailText =
    item.error?.message ||
    item.progress ||
    (item.finished_at ? `Finished ${relative(item.finished_at)}` : `Started ${relative(item.started_at)}`);
  return (
    <Row
      href={`/work/${encodeURIComponent(item.task_id)}`}
      title={item.title}
      detail={<span className="line-clamp-2">{detailText}</span>}
      trailing={
        <>
          {item.operations.uncertain > 0 && (
            <AlertTriangle size={14} className="text-warning" aria-label="Has an unconfirmed action" />
          )}
          <Pill tone={pill.tone}>{pill.label}</Pill>
        </>
      }
    />
  );
}
