"use client";

import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, Search } from "lucide-react";
import { relative } from "@/lib/assistant/format";
import type { OperationProjection } from "@/lib/contracts/assistant";
import { Pill, Row, Rows, type Tone } from "./ui";

const OUTCOME: Record<string, { tone: Tone; label: string }> = {
  confirmed: { tone: "ok", label: "Confirmed" },
  submitted: { tone: "neutral", label: "Sent, not yet confirmed" },
  uncertain: { tone: "warning", label: "Unconfirmed" },
  failed: { tone: "danger", label: "Failed" },
  attempted: { tone: "neutral", label: "In progress" },
  pending: { tone: "neutral", label: "Waiting" },
  ready: { tone: "neutral", label: "Ready" },
  skipped: { tone: "neutral", label: "Skipped" },
  voided: { tone: "neutral", label: "Cancelled before running" },
};

const CLEARED: Record<string, { tone: Tone; label: string }> = {
  did_not_run: { tone: "neutral", label: "Didn’t run" },
  read_only: { tone: "neutral", label: "Only read" },
  answered: { tone: "neutral", label: "Settled by your answer" },
  contained: { tone: "warning", label: "Plugin failed safely" },
};

const RECOVERY: Record<string, string> = {
  reconciling: "checking with the provider",
  needs_review: "pending review",
  resolved: "resolved",
};

const BUILTIN: Record<string, string> = {
  web_search: "Searched the web",
  upload_open: "Opened an attached file",
  shell: "Ran a command",
  job_start: "Started a long-running command",
  browser_navigate: "Opened a web page",
  browser_screenshot: "Took a screenshot",
  screenshot_inspect: "Looked at a screenshot",
};

export const stepName = (name: string) =>
  BUILTIN[name] ?? name.charAt(0).toUpperCase() + name.slice(1).replace(/_/g, " ");

export const shownOperations = <T extends { name: string }>(operations: T[]) =>
  operations.filter((operation) => operation.name !== "capability_load");

export type OperationRowItem = OperationProjection & { recovery?: string; task_title?: string };

export type OperationDisplayGroup =
  | { kind: "single"; operation: OperationRowItem }
  | { kind: "search_group"; id: string; operations: OperationRowItem[] };

export function operationOutcomePill(
  operation: Pick<OperationProjection, "effect_class" | "outcome" | "cleared">,
): { tone: Tone; label: string } | undefined {
  if (operation.cleared && CLEARED[operation.cleared]) return CLEARED[operation.cleared];
  if (operation.outcome === "confirmed") return undefined;
  return OUTCOME[operation.outcome] ?? { tone: "neutral", label: operation.outcome };
}

export function groupOperations(operations: OperationRowItem[]): OperationDisplayGroup[] {
  const groups: OperationDisplayGroup[] = [];
  let searchRun: OperationRowItem[] = [];

  const flushSearchRun = () => {
    if (searchRun.length === 0) return;
    if (searchRun.length === 1) {
      groups.push({ kind: "single", operation: searchRun[0] });
    } else {
      groups.push({ kind: "search_group", id: `search-group-${searchRun[0].id}`, operations: searchRun });
    }
    searchRun = [];
  };

  for (const operation of operations) {
    if (operation.name === "web_search") {
      searchRun.push(operation);
    } else {
      flushSearchRun();
      groups.push({ kind: "single", operation });
    }
  }
  flushSearchRun();
  return groups;
}

function operationDetail(operation: OperationRowItem): string {
  const isException = operation.outcome !== "confirmed" || !!operation.recovery || !!operation.cleared;
  return [
    isException && operation.account_summary,
    operation.recovery && RECOVERY[operation.recovery],
    operation.created_at && relative(operation.created_at),
  ]
    .filter(Boolean)
    .join(" · ");
}

function SearchGroupRow({ operations }: { operations: OperationRowItem[] }) {
  const [expanded, setExpanded] = useState(false);
  const failedCount = operations.filter((op) => op.outcome === "failed").length;
  const activeCount = operations.filter((op) => op.outcome === "attempted" || op.outcome === "pending").length;
  const latestTimestamp = operations
    .map((op) => op.created_at)
    .filter(Boolean)
    .sort()
    .at(-1);
  const badge =
    failedCount > 0
      ? { tone: "danger" as Tone, label: failedCount === operations.length ? "Failed" : `${failedCount} failed` }
      : activeCount > 0
        ? { tone: "neutral" as Tone, label: "In progress" }
        : undefined;

  return (
    <li data-search-group>
      <button
        type="button"
        aria-expanded={expanded}
        onClick={() => setExpanded((open) => !open)}
        className="flex min-h-14 w-full items-center gap-3 px-4 py-3 text-left hover:bg-fill-1 focus-visible:bg-fill-1"
      >
        <span className="shrink-0 text-ink-3" aria-hidden>
          <Search size={16} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-row font-medium text-ink">
            Searched the web ({operations.length} {operations.length === 1 ? "query" : "queries"})
          </span>
          {latestTimestamp && <span className="mt-0.5 block text-meta text-ink-2">{relative(latestTimestamp)}</span>}
        </span>
        {badge && <Pill tone={badge.tone}>{badge.label}</Pill>}
        <span className="shrink-0 text-ink-3" aria-hidden>
          {expanded ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
        </span>
      </button>
      {expanded && (
        <ul className="divide-y divide-hairline border-t border-hairline bg-fill-1/40 pl-10" aria-label="Web search queries">
          {operations.map((operation) => {
            const outcome = operationOutcomePill(operation);
            const detail = operationDetail(operation);
            return (
              <li key={operation.id} className="flex min-h-11 items-center gap-3 px-4 py-2.5">
                <span className="min-w-0 flex-1">
                  <span className="block text-meta font-medium text-ink [overflow-wrap:anywhere]">
                    {operation.action_summary || stepName(operation.name)}
                  </span>
                  {detail && <span className="mt-0.5 block text-caption text-ink-3">{detail}</span>}
                </span>
                {outcome && <Pill tone={outcome.tone}>{outcome.label}</Pill>}
              </li>
            );
          })}
        </ul>
      )}
    </li>
  );
}

export function OperationRows({ operations }: { operations: OperationRowItem[] }) {
  const grouped = useMemo(() => groupOperations(operations), [operations]);
  return (
    <Rows label="Actions">
      {grouped.map((entry) => {
        if (entry.kind === "search_group") {
          return <SearchGroupRow key={entry.id} operations={entry.operations} />;
        }
        const operation = entry.operation;
        const outcome = operationOutcomePill(operation);
        return (
          <Row
            key={operation.id}
            title={operation.action_summary || stepName(operation.name)}
            detail={operationDetail(operation)}
            trailing={
              <>
                {operation.receipt_url && /^https:\/\//.test(operation.receipt_url) && (
                  <a
                    href={operation.receipt_url}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="text-meta font-semibold text-accent-text hover:underline"
                  >
                    Open
                  </a>
                )}
                {outcome && <Pill tone={outcome.tone}>{outcome.label}</Pill>}
              </>
            }
          />
        );
      })}
    </Rows>
  );
}
