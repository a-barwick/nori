"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useMemo, useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight } from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { relative } from "@/lib/assistant/format";
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

export function statusPill(item: WorkItem): { tone: Tone; label: string } {
  if (item.cancel_requested && item.status === "running") return { tone: "neutral", label: "Stopping" };
  if (item.bucket === "needs_you") return { tone: "warning", label: "Action needed" };
  if (item.status === "running") {
    return item.operations.uncertain ? { tone: "warning", label: "Checking" } : { tone: "working", label: "Working" };
  }
  if (item.status === "completed") {
    return item.operations.uncertain ? { tone: "warning", label: "Unconfirmed" } : { tone: "ok", label: "Done" };
  }
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
  const pendingTaskIDs = useMemo(() => new Set(pendingInteractions.map((i) => i.task_id).filter(Boolean)), [pendingInteractions]);

  const orphanPendingTasks = useMemo(
    () => items.filter((item) => item.bucket === "needs_you" && !pendingTaskIDs.has(item.task_id)),
    [items, pendingTaskIDs],
  );
  const runningTasks = useMemo(
    () => items.filter((item) => item.status === "running" && item.bucket !== "needs_you" && !pendingTaskIDs.has(item.task_id)),
    [items, pendingTaskIDs],
  );
  const finishedTasks = useMemo(
    () =>
      items
        .filter((item) => item.status !== "running")
        .sort((a, b) => Date.parse(b.finished_at ?? b.started_at) - Date.parse(a.finished_at ?? a.started_at)),
    [items],
  );

  const pendingTotal = pendingInteractions.length + orphanPendingTasks.length;

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
            { id: "tasks", label: "Tasks", count: pendingTotal || undefined },
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
          pendingInteractions={pendingInteractions}
          orphanPendingTasks={orphanPendingTasks}
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
  pendingInteractions,
  orphanPendingTasks,
  runningTasks,
  finishedTasks,
}: {
  loading: boolean;
  error?: unknown;
  onRetry: () => void;
  pendingError?: unknown;
  pendingRefreshing: boolean;
  onRetryPending: () => void;
  pendingInteractions: Interaction[];
  orphanPendingTasks: WorkItem[];
  runningTasks: WorkItem[];
  finishedTasks: WorkItem[];
}) {
  const [showAllRecent, setShowAllRecent] = useState(false);
  const pendingTotal = pendingInteractions.length + orphanPendingTasks.length;
  const visibleRecent = showAllRecent ? finishedTasks : finishedTasks.slice(0, 5);

  if (loading && !error && !pendingError && !pendingTotal && !runningTasks.length && !finishedTasks.length) {
    return <Loading />;
  }

  return (
    <div className="space-y-6">
      {error ? <Failure error={error} onRetry={onRetry} /> : null}
      {pendingError ? <Failure error={pendingError} onRetry={onRetryPending} /> : null}

      {pendingTotal > 0 ? (
        <Section title={`Action needed · ${pendingTotal}`}>
          <div className="space-y-3">
            {pendingInteractions.map((interaction) => (
              <InteractionCard key={interaction.id} interaction={interaction} />
            ))}
            {orphanPendingTasks.length > 0 && (
              <Rows label="Tasks needing action">
                {orphanPendingTasks.map((item) => (
                  <WorkRow key={item.task_id} item={item} />
                ))}
              </Rows>
            )}
          </div>
        </Section>
      ) : !error && !pendingError && !loading && !pendingRefreshing ? (
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

      {runningTasks.length > 0 && (
        <Section title={runningTasks.length > 1 ? `Running · ${runningTasks.length}` : "Running"}>
          <div className="space-y-2.5">
            {runningTasks.map((item) => (
              <Link
                key={item.task_id}
                href={`/work/${encodeURIComponent(item.task_id)}`}
                className="nori-running-card flex items-center gap-3 px-4 py-3.5 transition-colors hover:border-hairline-strong hover:bg-accent-subtle"
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
                  <span className="mt-0.5 block line-clamp-2 text-meta text-ink-2">
                    {item.cancel_requested ? "Stopping…" : item.progress || `Started ${relative(item.started_at)}`}
                  </span>
                </span>
                {item.operations.uncertain > 0 && (
                  <AlertTriangle size={14} className="shrink-0 text-warning" aria-label="Has an unconfirmed action" />
                )}
                <ChevronRight size={15} className="shrink-0 text-ink-3" aria-hidden />
              </Link>
            ))}
          </div>
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
  return (
    <Row
      href={`/work/${encodeURIComponent(item.task_id)}`}
      title={item.title}
      detail={
        item.error?.message ||
        item.progress ||
        (item.finished_at ? `Finished ${relative(item.finished_at)}` : `Started ${relative(item.started_at)}`)
      }
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
