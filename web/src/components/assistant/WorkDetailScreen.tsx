"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Dialog } from "radix-ui";
import {
  AlertTriangle,
  ArrowUp,
  ArrowUpRight,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  Download,
  FileText,
  Maximize2,
  MessageSquare,
  Play,
  Search,
  Square,
  X,
} from "lucide-react";
import { Markdown } from "@/components/chat/Markdown";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button, IconButton } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { money, relative, when } from "@/lib/assistant/format";
import { useWork, useWorkDetail } from "@/lib/assistant/sources";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import { downloadBlob, useCopyToClipboard } from "@/lib/browser";
import type { LibraryItem, OperationProjection, RunEvent, WorkItem } from "@/lib/contracts/assistant";
import { MAX_MESSAGE_BYTES, messageTooLong } from "@/lib/submission";
import { cn } from "@/lib/utils";
import { InteractionCard } from "./InteractionCard";
import { groupOperations, operationOutcomePill, shownOperations, stepName, type OperationRowItem } from "./WorkOperations";
import { statusPill } from "./WorkScreen";
import { Empty, Failure, Loading, Notice, Pill, Row, Rows, Screen, Section } from "./ui";

const SPENDING_LIMIT = /spending limit/i;
const MAX_TRANSIENT_RETRIES = 3;
const CLAMP_HEIGHT_PX = 220;

export function isRetryableWorkDetailError(error: unknown): boolean {
  if (error instanceof APIError) {
    return error.status === 404 || error.status === 502 || error.status === 503 || error.status === 504 || error.status === 0;
  }
  return false;
}

export function isBudgetLimitedTask(item: Pick<WorkItem, "status" | "error" | "cost">): boolean {
  if (item.status !== "failed") return false;
  if (SPENDING_LIMIT.test(item.error?.message ?? "")) return true;
  const spent = item.cost.amount_usd;
  const budget = item.cost.budget_usd;
  return spent !== undefined && budget !== undefined && budget > 0 && spent >= budget;
}

export function budgetPresets(currentBudgetUSD?: number): number[] {
  const current = currentBudgetUSD && currentBudgetUSD > 0 ? currentBudgetUSD : 1;
  if (current < 10) {
    const standard = [2, 5, 10].filter((value) => value > current);
    if (standard.length === 3) return standard;
    const base = Math.ceil(current * 2);
    return Array.from(new Set([...standard, base, base * 2])).slice(0, 3);
  }
  return [Math.ceil(current * 1.5), Math.ceil(current * 2), Math.ceil(current * 3)];
}

export function selectPrimaryArtifact(saved: LibraryItem[], result?: string | null): LibraryItem | null {
  if (!result?.trim() || saved.length !== 1) return null;
  const candidate = saved[0];
  const isTextDocumentKind = candidate.kind === "document" || candidate.kind === "report" || candidate.kind === "answer";
  if (!isTextDocumentKind) return null;
  const mediaType = (candidate.media_type ?? "").toLowerCase();
  if (mediaType && !mediaType.startsWith("text/")) return null;
  if (mediaType.includes("csv")) return null;
  return candidate;
}

export function stripLeadingMatchingHeading(markdown: string, title: string): string {
  const trimmedTitle = title.trim().toLowerCase();
  if (!trimmedTitle) return markdown;
  const lines = markdown.split("\n");
  let firstNonEmpty = 0;
  while (firstNonEmpty < lines.length && !lines[firstNonEmpty].trim()) {
    firstNonEmpty += 1;
  }
  const line = lines[firstNonEmpty]?.trim() ?? "";
  const headingMatch = line.match(/^#{1,6}\s+(.+?)\s*#*$/);
  if (!headingMatch) return markdown;
  if (headingMatch[1].trim().toLowerCase() !== trimmedTitle) return markdown;
  return lines.slice(firstNonEmpty + 1).join("\n").replace(/^\s*\n/, "");
}

export function isLongMarkdown(markdown: string): boolean {
  const trimmed = markdown.trim();
  if (trimmed.length > 440) return true;
  const lines = trimmed.split("\n").filter((line) => line.trim().length > 0);
  return lines.length > 8;
}

export function formatDuration(startISO?: string, endISO?: string, now = Date.now()): string {
  if (!startISO) return "";
  const start = Date.parse(startISO);
  const end = endISO ? Date.parse(endISO) : now;
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "";
  const totalSeconds = Math.max(0, Math.round((end - start) / 1000));
  if (totalSeconds < 60) return `${totalSeconds}s`;
  if (totalSeconds < 3600) {
    const mins = Math.floor(totalSeconds / 60);
    const secs = totalSeconds % 60;
    return secs === 0 ? `${mins}m` : `${mins}m ${secs}s`;
  }
  const hours = Math.floor(totalSeconds / 3600);
  const mins = Math.floor((totalSeconds % 3600) / 60);
  return mins === 0 ? `${hours}h` : `${hours}h ${mins}m`;
}

export function externalChangeOperations(operations: OperationProjection[]): OperationProjection[] {
  return shownOperations(operations).filter(
    (op) =>
      op.effect_class === "reversible_write" ||
      op.effect_class === "sensitive_send" ||
      (op.effect_class !== "read" && (op.outcome === "uncertain" || op.outcome === "failed")),
  );
}

export type TaskStepItem =
  | {
      kind: "search_group";
      id: string;
      title: string;
      status: "done" | "active" | "warn" | "err";
      time?: string;
      sortMs: number;
      order: number;
      queries: { id: string; text: string; time?: string }[];
    }
  | {
      kind: "step";
      id: string;
      title: string;
      status: "done" | "active" | "warn" | "err";
      time?: string;
      sortMs: number;
      order: number;
    };

function stepStatusFromOperation(op: OperationProjection): "done" | "active" | "warn" | "err" {
  if (op.outcome === "failed") return "err";
  if (op.outcome === "uncertain") return "warn";
  if (op.outcome === "attempted" || op.outcome === "pending" || op.outcome === "submitted") return "active";
  return "done";
}

function parseSortMs(iso?: string): number {
  if (!iso) return Number.MAX_SAFE_INTEGER;
  const parsed = Date.parse(iso);
  return Number.isFinite(parsed) ? parsed : Number.MAX_SAFE_INTEGER;
}

export function buildTaskSteps(operations: OperationProjection[], events: RunEvent[] = []): TaskStepItem[] {
  const visibleOps = shownOperations(operations);
  const grouped = groupOperations(visibleOps as OperationRowItem[]);
  const toolNames = new Set(operations.map((op) => op.name));
  const opSummaries = new Set(
    visibleOps.map((op) => (op.action_summary || stepName(op.name)).trim().toLowerCase()),
  );

  const items: TaskStepItem[] = [];
  let order = 0;

  for (const entry of grouped) {
    if (entry.kind === "search_group") {
      const hasFailed = entry.operations.some((op) => op.outcome === "failed");
      const hasActive = entry.operations.some((op) => op.outcome === "attempted" || op.outcome === "pending");
      const firstTs = entry.operations.find((op) => op.created_at)?.created_at;
      items.push({
        kind: "search_group",
        id: entry.id,
        title: `Searched the web (${entry.operations.length} ${entry.operations.length === 1 ? "query" : "queries"})`,
        status: hasFailed ? "err" : hasActive ? "active" : "done",
        time: firstTs,
        sortMs: parseSortMs(firstTs),
        order: order++,
        queries: entry.operations.map((op) => ({
          id: op.id,
          text: op.action_summary || stepName(op.name),
          time: op.created_at,
        })),
      });
    } else {
      const op = entry.operation;
      items.push({
        kind: "step",
        id: `op-${op.id}`,
        title: op.action_summary || stepName(op.name),
        status: stepStatusFromOperation(op),
        time: op.created_at,
        sortMs: parseSortMs(op.created_at),
        order: order++,
      });
    }
  }

  for (let i = 0; i < events.length; i += 1) {
    const event = events[i];
    const text = event.text.trim();
    if (!text || toolNames.has(text) || text === "capability_load") continue;
    if (opSummaries.has(text.toLowerCase())) continue;
    items.push({
      kind: "step",
      id: `evt-${event.created_at}-${i}`,
      title: text,
      status: "done",
      time: event.created_at,
      sortMs: parseSortMs(event.created_at),
      order: order++,
    });
  }

  items.sort((a, b) => (a.sortMs !== b.sortMs ? a.sortMs - b.sortMs : a.order - b.order));
  return items;
}

function formatStepClock(iso?: string): string {
  if (!iso) return "";
  const date = new Date(iso);
  if (!Number.isFinite(date.getTime())) return "";
  return date.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

function RecoveryCard({ item }: { item: WorkItem }) {
  const invalidate = useResourceInvalidation();
  const router = useRouter();
  const action = useAction();
  const limited = isBudgetLimitedTask(item);
  const presets = useMemo(() => budgetPresets(item.cost.budget_usd), [item.cost.budget_usd]);
  const [selectedBudget, setSelectedBudget] = useState<number>(() => presets[0] ?? 2);

  const go = () =>
    action.run(
      "continue",
      async () => {
        const receipt = await mutate(`/work/${encodeURIComponent(item.task_id)}/actions`, {
          request_id: newRequestID(),
          expected_revision: 0,
          value: { action: "continue", ...(limited && selectedBudget > 0 ? { budget_usd: selectedBudget } : {}) },
        });
        invalidate("/work", "/assistant/summary");
        if (receipt.status === "queued" && receipt.work?.task_id) {
          router.push(`/work/${encodeURIComponent(receipt.work.task_id)}`);
        }
      },
      "Continuing in a new task.",
    );

  const heading = limited
    ? item.cost.budget_usd
      ? `Paused at ${money(item.cost.budget_usd)} spending limit`
      : "Paused at spending limit"
    : item.error?.message || "Task stopped before finishing";

  return (
    <Section>
      <div className="rounded-card border border-card-border bg-accent-subtle p-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <p className="text-[15px] font-semibold text-ink">{heading}</p>
            <p className="mt-1 text-meta text-ink-2">Completed actions won’t be repeated. Saved work carries over.</p>
          </div>
          {limited && (
            <div className="flex items-center gap-1.5" role="group" aria-label="New spending limit">
              <span className="mr-0.5 text-caption text-ink-3">Limit</span>
              {presets.map((preset) => {
                const selected = selectedBudget === preset;
                return (
                  <button
                    key={preset}
                    type="button"
                    aria-pressed={selected}
                    onClick={() => setSelectedBudget(preset)}
                    className={cn(
                      "inline-flex h-7 items-center rounded-full border px-2.5 font-mono text-caption font-semibold transition-colors",
                      selected
                        ? "border-chip-selected-border bg-chip-selected-bg text-chip-selected-fg"
                        : "border-card-border bg-surface text-ink-2 hover:bg-fill-1 hover:text-ink",
                    )}
                  >
                    ${preset}
                  </button>
                );
              })}
            </div>
          )}
        </div>
        <div className="mt-3">
          <Button type="button" size="sm" variant="primary" disabled={!!action.pending} onClick={() => void go()}>
            <Play size={12} fill="currentColor" aria-hidden />
            {action.pending ? "Starting…" : limited ? `Continue (${money(selectedBudget)})` : "Continue"}
          </Button>
        </div>
      </div>
      {action.error && (
        <div className="mt-2">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}
    </Section>
  );
}

function ResultReaderModal({
  open,
  title,
  body,
  onClose,
}: {
  open: boolean;
  title: string;
  body: string;
  onClose: () => void;
}) {
  const { copied, copy } = useCopyToClipboard();

  const download = () => {
    const safeName = title.replace(/[^\p{L}\p{N} -]/gu, "").trim().slice(0, 80) || "Nori document";
    downloadBlob(body, `${safeName}.md`);
  };

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-40 bg-overlay sm:top-[60px]" />
        <Dialog.Content
          aria-describedby={undefined}
          className="sheet-right fixed inset-y-0 right-0 z-50 flex w-[min(640px,100vw)] flex-col border-l border-hairline bg-sheet text-ink shadow-sheet focus-visible:outline-none sm:top-[60px]"
        >
          <header className="flex h-[54px] shrink-0 items-center gap-3 border-b border-hairline bg-wash-top px-5">
            <Dialog.Title className="min-w-0 flex-1 truncate text-row font-semibold text-ink">{title}</Dialog.Title>
            <div className="flex shrink-0 items-center gap-1.5">
              <Button size="sm" variant="quiet" onClick={() => copy(body)}>
                {copied ? <Check size={14} strokeWidth={1.8} aria-hidden /> : <Copy size={14} strokeWidth={1.8} aria-hidden />}
                {copied ? "Copied" : "Copy"}
              </Button>
              <IconButton size={32} label="Export Markdown" onClick={download}>
                <Download size={15} strokeWidth={1.8} />
              </IconButton>
              <IconButton size={32} label="Close" onClick={onClose}>
                <X size={16} strokeWidth={1.8} />
              </IconButton>
            </div>
          </header>
          <div className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto px-6 py-6 sm:px-7">
            <div className="max-w-[68ch]">
              <Markdown variant="page">{body}</Markdown>
            </div>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function OutcomeCard({
  title,
  hasArtifact,
  markdown,
}: {
  title: string;
  hasArtifact: boolean;
  markdown: string;
}) {
  const { copied, copy } = useCopyToClipboard();
  const displayMarkdown = useMemo(() => stripLeadingMatchingHeading(markdown, title), [markdown, title]);
  const bodyRef = useRef<HTMLDivElement>(null);
  const [measuredTall, setMeasuredTall] = useState(() => isLongMarkdown(displayMarkdown));
  const [readerOpen, setReaderOpen] = useState(false);

  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    const measure = () => setMeasuredTall(el.scrollHeight > CLAMP_HEIGHT_PX);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [displayMarkdown]);

  return (
    <Section>
      <div className="rounded-card border border-card-border bg-surface p-4">
        <div className="mb-2.5 flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            {hasArtifact && (
              <span className="grid size-7 shrink-0 place-items-center rounded-[8px] bg-accent-subtle text-accent-text">
                <FileText size={14} strokeWidth={1.9} aria-hidden />
              </span>
            )}
            <span
              className={cn(
                "truncate text-[14px] font-semibold",
                hasArtifact ? "text-ink" : "text-ink-2",
              )}
            >
              {title}
            </span>
          </div>
          <Button size="sm" variant="quiet" className="h-7 px-2.5 text-[12px]" onClick={() => copy(markdown)}>
            {copied ? <Check size={13} strokeWidth={2} aria-hidden /> : <Copy size={13} strokeWidth={1.8} aria-hidden />}
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>

        <div className={cn("relative", measuredTall && "max-h-[200px] overflow-hidden")}>
          <div ref={bodyRef} className="text-body [overflow-wrap:anywhere]">
            <Markdown variant="page">{displayMarkdown}</Markdown>
          </div>
          {measuredTall && (
            <div className="page-fade absolute inset-x-0 bottom-0 flex h-24 items-end justify-center pb-1">
              <Button size="sm" variant="tonal" onClick={() => setReaderOpen(true)}>
                <Maximize2 size={13} strokeWidth={2} aria-hidden />
                Expand
              </Button>
            </div>
          )}
        </div>
      </div>

      <ResultReaderModal open={readerOpen} title={title} body={displayMarkdown} onClose={() => setReaderOpen(false)} />
    </Section>
  );
}

function StepsDisclosure({ steps, running }: { steps: TaskStepItem[]; running: boolean }) {
  const [userExpanded, setUserExpanded] = useState<boolean | null>(null);
  const [openSearchGroups, setOpenSearchGroups] = useState<Record<string, boolean>>({});
  const expanded = userExpanded ?? running;

  if (!steps.length) return null;

  const toggleSearchGroup = (id: string) => {
    setOpenSearchGroups((prev) => ({ ...prev, [id]: !prev[id] }));
  };

  return (
    <Section>
      <div className="overflow-hidden rounded-card border border-card-border bg-surface">
        <button
          type="button"
          aria-expanded={expanded}
          onClick={() => setUserExpanded(!expanded)}
          className="flex min-h-12 w-full items-center justify-between gap-3 px-4 py-3 text-left transition-colors hover:bg-fill-1 focus-visible:bg-fill-1"
        >
          <span className="flex items-center gap-2">
            {running && <span className="pulse-dot size-2 shrink-0 rounded-full bg-accent" aria-hidden />}
            <span className="text-[14.5px] font-semibold text-ink">Steps</span>
            <span className="text-caption tabular-nums text-ink-3">{steps.length}</span>
          </span>
          <span className="shrink-0 text-ink-3" aria-hidden>
            {expanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
          </span>
        </button>

        {expanded && (
          <div className="border-t border-hairline px-4 py-3.5">
            <ol className="space-y-0" aria-label="Task steps">
              {steps.map((step, index) => {
                const isLast = index === steps.length - 1;
                const dotColor =
                  step.status === "err"
                    ? "bg-danger"
                    : step.status === "warn"
                      ? "bg-warning"
                      : step.status === "active"
                        ? "bg-accent"
                        : "bg-ok";

                if (step.kind === "search_group") {
                  const isOpen = !!openSearchGroups[step.id];
                  return (
                    <li key={step.id} className="grid grid-cols-[18px_1fr] gap-2.5">
                      <div className="flex flex-col items-center pt-1.5">
                        <span className={cn("size-2 shrink-0 rounded-full", dotColor)} aria-hidden />
                        {!isLast && <span className="mt-1 min-h-4 w-px flex-1 bg-hairline" aria-hidden />}
                      </div>
                      <div className={cn("min-w-0", !isLast && "pb-3.5")}>
                        <button
                          type="button"
                          aria-expanded={isOpen}
                          onClick={() => toggleSearchGroup(step.id)}
                          className="flex w-full items-center gap-1.5 text-left text-[13.5px] leading-snug text-ink hover:text-accent-text"
                        >
                          <Search size={13} className="shrink-0 text-ink-3" aria-hidden />
                          <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{step.title}</span>
                          <span className="shrink-0 text-ink-3" aria-hidden>
                            {isOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                          </span>
                        </button>
                        {step.time && <p className="mt-0.5 text-caption text-ink-3">{formatStepClock(step.time)}</p>}
                        {isOpen && (
                          <ul className="mt-2 space-y-1 border-l-2 border-hairline pl-3 text-caption text-ink-2">
                            {step.queries.map((query) => (
                              <li key={query.id} className="[overflow-wrap:anywhere]">
                                {query.text}
                              </li>
                            ))}
                          </ul>
                        )}
                      </div>
                    </li>
                  );
                }

                return (
                  <li key={step.id} className="grid grid-cols-[18px_1fr] gap-2.5">
                    <div className="flex flex-col items-center pt-1.5">
                      <span className={cn("size-2 shrink-0 rounded-full", dotColor)} aria-hidden />
                      {!isLast && <span className="mt-1 min-h-4 w-px flex-1 bg-hairline" aria-hidden />}
                    </div>
                    <div className={cn("min-w-0", !isLast && "pb-3.5")}>
                      <p className="text-[13.5px] leading-snug text-ink [overflow-wrap:anywhere]">{step.title}</p>
                      {step.time && <p className="mt-0.5 text-caption text-ink-3">{formatStepClock(step.time)}</p>}
                    </div>
                  </li>
                );
              })}
            </ol>
          </div>
        )}
      </div>
    </Section>
  );
}

function PlainDetailsSection({ item, continuedBy }: { item: WorkItem; continuedBy: WorkItem[] }) {
  const { copied, copy } = useCopyToClipboard();
  const duration = formatDuration(item.started_at, item.finished_at);
  const timeText = item.finished_at
    ? `${when(item.started_at)} – ${formatStepClock(item.finished_at)}${duration ? ` (${duration})` : ""}`
    : `${when(item.started_at)}${duration ? ` (${duration})` : ""}`;

  const spent = item.cost.amount_usd;
  const budget = item.cost.budget_usd;
  const nearCap = spent !== undefined && budget !== undefined && budget > 0 && spent >= budget * 0.95;
  const costText =
    item.cost.status === "known"
      ? `${money(spent ?? 0)}${budget ? ` / ${money(budget)}` : ""}`
      : item.cost.status === "unknown"
        ? spent
          ? `At least ${money(spent)}${budget ? ` / ${money(budget)}` : ""}`
          : "Unknown"
        : "None";

  return (
    <Section title="Details">
      <dl className="grid grid-cols-[88px_1fr] gap-x-4 gap-y-2 text-[13px] leading-relaxed">
        <dt className="text-ink-3">Origin</dt>
        <dd className="min-w-0 text-ink-2">
          {item.origin.kind === "automation" && item.origin.automation_id ? (
            <Link className="font-medium text-accent-text hover:underline" href={`/routines/${encodeURIComponent(item.origin.automation_id)}`}>
              Routine
            </Link>
          ) : item.origin.kind === "goal" && item.origin.goal_id ? (
            <Link className="font-medium text-accent-text hover:underline" href={`/goals/${encodeURIComponent(item.origin.goal_id)}`}>
              Goal
            </Link>
          ) : item.origin.kind === "external" ? (
            "Another app"
          ) : (
            <Link className="font-medium text-accent-text hover:underline" href="/">
              Chat
            </Link>
          )}
        </dd>

        <dt className="text-ink-3">Time</dt>
        <dd className="min-w-0 text-ink-2">{timeText}</dd>

        <dt className="text-ink-3">Cost</dt>
        <dd className={cn("min-w-0 font-mono text-[12.5px]", nearCap ? "text-danger" : "text-ink-2")}>{costText}</dd>

        <dt className="text-ink-3">Task ID</dt>
        <dd className="min-w-0">
          <button
            type="button"
            onClick={() => copy(item.task_id)}
            aria-label={`Copy Task ID ${item.task_id}`}
            className="inline-flex max-w-full items-center gap-1.5 font-mono text-[12.5px] text-ink-2 transition-colors hover:text-ink"
          >
            <span className="truncate">{item.task_id}</span>
            {copied ? (
              <Check size={12} className="shrink-0 text-accent-text" aria-hidden />
            ) : (
              <Copy size={12} className="shrink-0 text-ink-3" aria-hidden />
            )}
          </button>
        </dd>

        {continuedBy.length > 0 && (
          <>
            <dt className="text-ink-3">Continued</dt>
            <dd className="flex flex-wrap gap-x-3 gap-y-1">
              {continuedBy.map((other) => (
                <Link
                  key={other.task_id}
                  className="font-medium text-accent-text hover:underline"
                  href={`/work/${encodeURIComponent(other.task_id)}`}
                >
                  Later task
                </Link>
              ))}
            </dd>
          </>
        )}

        {item.origin.continues_task_id && (
          <>
            <dt className="text-ink-3">Continues</dt>
            <dd>
              <Link
                className="font-medium text-accent-text hover:underline"
                href={`/work/${encodeURIComponent(item.origin.continues_task_id)}`}
              >
                Earlier task
              </Link>
            </dd>
          </>
        )}

        {item.parent_task_id && (
          <>
            <dt className="text-ink-3">Parent</dt>
            <dd>
              <Link
                className="font-medium text-accent-text hover:underline"
                href={`/work/${encodeURIComponent(item.parent_task_id)}`}
              >
                Parent task
              </Link>
            </dd>
          </>
        )}
      </dl>
    </Section>
  );
}

export function WorkDetailScreen({ taskID }: { taskID: string }) {
  const router = useRouter();
  const signInResult = useSearchParams().get("result");
  const { runtime, has } = useAssistant();
  const { detail, error, loading, reload, live } = useWorkDetail(taskID);
  const work = useWork();
  const continuedBy = (work.items ?? []).filter((other) => other.origin.continues_task_id === taskID);
  const item = detail?.item;
  const results = useResource<{ items: LibraryItem[] }>(
    has("library") && item?.result_ids.length ? `/library?task_id=${encodeURIComponent(taskID)}` : null,
    { poll: 0 },
  );
  const saved = useMemo(
    () => (results.data?.items ?? []).filter((result) => item?.result_ids.includes(result.id)),
    [results.data?.items, item?.result_ids],
  );
  const commands = runtime.taskCommands.filter((command) => command.command.run_id === taskID);
  const [draft, setDraft] = useState("");
  const [retryState, setRetryState] = useState<{ taskID: string; count: number }>({ taskID, count: 0 });
  const retries = retryState.taskID === taskID ? retryState.count : 0;
  const settling = !item && isRetryableWorkDetailError(error) && retries < MAX_TRANSIENT_RETRIES;

  useEffect(() => {
    if (!settling) return;
    const delayMs = 500 * 2 ** retries;
    const timer = setTimeout(() => {
      setRetryState((prev) => ({ taskID, count: prev.taskID === taskID ? prev.count + 1 : 1 }));
      void reload();
    }, delayMs);
    return () => clearTimeout(timer);
  }, [settling, retries, reload, taskID]);

  const changesMade = useMemo(
    () => (detail ? externalChangeOperations(detail.operations) : []),
    [detail],
  );

  const steps = useMemo(
    () => (detail ? buildTaskSteps(detail.operations, detail.events ?? []) : []),
    [detail],
  );

  if (loading || settling) {
    return (
      <Screen title="Task" back="/work" backLabel="Work">
        <Loading />
      </Screen>
    );
  }
  if (!item) {
    return (
      <Screen title="Task" back="/work" backLabel="Work">
        {error ? (
          <Failure error={error} onRetry={() => void reload()} />
        ) : (
          <Empty
            action={
              <Link href="/work" className="text-meta font-semibold text-accent-text hover:underline">
                Back to Work
              </Link>
            }
          >
            This task isn’t in your recent work.
          </Empty>
        )}
      </Screen>
    );
  }

  const pill = statusPill(item);
  const running = item.status === "running" && !item.cancel_requested;
  const stopping = commands.some((command) => command.command.kind === "stop") || item.cancel_requested;
  const pendingInteractions = detail.interactions.filter((interaction) => interaction.status === "pending");
  const actionableInteractions = pendingInteractions.filter((interaction) => interaction.kind !== "question");
  const pendingQuestions = pendingInteractions.filter((interaction) => interaction.kind === "question");
  const waitingInChat = running && actionableInteractions.length === 0 && (pendingQuestions.length > 0 || item.bucket === "needs_you");

  const headerTime = item.finished_at
    ? relative(item.finished_at)
    : item.started_at
      ? relative(item.started_at)
      : undefined;

  const primaryArtifact = selectPrimaryArtifact(saved, detail.result);
  const extraArtifacts = primaryArtifact ? saved.filter((entry) => entry.id !== primaryArtifact.id) : saved;
  const hasUncertainChangeRow = changesMade.some((op) => op.outcome === "uncertain");
  const tooLong = messageTooLong(draft.trim());
  const sending = commands.some((command) => command.command.kind === "steer" && !command.error);
  const showModelKeyShortcut =
    item.error?.code === "model_key_required" || (item.error?.message ?? "").includes("More → Models");

  const followUpInChat = () => {
    runtime.setDraft(`Following up on "${item.title}": `);
    router.push("/?compose=1");
  };

  return (
    <Screen
      title={item.title}
      back="/work"
      backLabel="Work"
      subtitle={headerTime}
      actions={
        <>
          <Pill tone={pill.tone}>{pill.label}</Pill>
          {running && (
            <Button
              size="sm"
              variant="danger"
              disabled={stopping}
              onClick={() => void runtime.commandTask(taskID, "stop")}
            >
              <Square size={11} fill="currentColor" aria-hidden />
              {stopping ? "Stopping…" : "Stop"}
            </Button>
          )}
        </>
      }
    >
      {signInResult && (
        <div className="mb-5">
          <Notice tone={signInResult === "connected" ? "ok" : signInResult === "cancelled" ? "neutral" : "danger"}>
            {signInResult === "connected"
              ? "Signed in. Nori is continuing this task."
              : signInResult === "cancelled"
                ? "Sign-in was cancelled. The task is still waiting."
                : "Sign-in didn’t finish. Try again from the card below."}
          </Notice>
        </div>
      )}

      {showModelKeyShortcut && (
        <div className="mb-5">
          <Notice
            tone="warning"
            action={
              <Link href="/settings/models" className="text-meta font-semibold text-accent-text hover:underline">
                Open Models
              </Link>
            }
          >
            Add a model key in Settings to continue this task.
          </Notice>
        </div>
      )}

      {actionableInteractions.length > 0 && (
        <Section title="Action needed">
          <div className="space-y-3">
            {actionableInteractions.map((interaction) => (
              <InteractionCard key={interaction.id} interaction={interaction} compact />
            ))}
          </div>
        </Section>
      )}

      {waitingInChat && (
        <Section>
          <div className="rounded-card border border-card-border bg-accent-subtle p-4">
            <div className="flex items-start gap-3">
              <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-surface text-accent-text">
                <NoriMark size={20} state="idle" />
              </span>
              <div className="min-w-0 flex-1">
                <p className="text-[14.5px] font-semibold text-ink">Nori asked a question in Chat</p>
                <p className="mt-1 text-meta text-ink-2">
                  {pendingQuestions[0]?.prompt || "Open Chat to reply so Nori can continue."}
                </p>
                <div className="mt-3 flex flex-wrap items-center gap-2">
                  <Button size="sm" variant="primary" onClick={() => router.push("/?compose=1")}>
                    <MessageSquare size={13} aria-hidden />
                    Reply in Chat
                  </Button>
                  <Button size="sm" variant="quiet" disabled={stopping} onClick={() => void runtime.commandTask(taskID, "stop")}>
                    {stopping ? "Stopping…" : "Stop task"}
                  </Button>
                </div>
              </div>
            </div>
          </div>
        </Section>
      )}

      {running && !waitingInChat && actionableInteractions.length === 0 && (
        <Section>
          <div className="rounded-card border border-card-border bg-accent-subtle p-4">
            <div className="mb-3 flex items-center gap-2">
              <span className="pulse-dot size-2 shrink-0 rounded-full bg-accent" aria-hidden />
              <span className="text-[14.5px] font-medium text-ink">{item.progress || "Working on it."}</span>
            </div>
            <form
              className="flex items-center gap-2 rounded-full border border-card-border bg-surface py-1.5 pr-1.5 pl-4 focus-within:border-accent-signal"
              onSubmit={(event) => {
                event.preventDefault();
                const text = draft.trim();
                if (!text || tooLong || sending) return;
                void runtime.commandTask(taskID, "steer", text).then(() => setDraft(""));
              }}
            >
              <input
                type="text"
                aria-label="Instruction for this task"
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                maxLength={MAX_MESSAGE_BYTES}
                placeholder="Add a note or adjust course…"
                className="min-w-0 flex-1 bg-transparent text-input text-ink placeholder:text-ink-3 focus:outline-none"
              />
              <button
                type="submit"
                aria-label="Send update"
                disabled={!draft.trim() || tooLong || sending}
                className="grid size-8 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition-colors hover:bg-accent-hover disabled:bg-disabled-bg disabled:text-disabled"
              >
                <ArrowUp size={15} strokeWidth={2.2} aria-hidden />
              </button>
            </form>
            {tooLong && (
              <p role="alert" className="mt-2 text-meta text-danger">
                Keep this within 8,192 bytes.
              </p>
            )}
          </div>
          {commands
            .filter((command) => command.error)
            .map((command) => (
              <div key={command.command.id} className="mt-2">
                <Notice
                  tone="danger"
                  action={
                    command.rejected ? (
                      <Button size="sm" onClick={() => runtime.dismissTaskCommand(command.command.id)}>
                        Dismiss
                      </Button>
                    ) : (
                      <Button size="sm" onClick={() => void runtime.retryTaskCommand(command)}>
                        Retry
                      </Button>
                    )
                  }
                >
                  {command.error}
                </Notice>
              </div>
            ))}
        </Section>
      )}

      {item.status === "running" && item.cancel_requested && (
        <Section>
          <Notice>Stopping. Work already done stays done; anything unconfirmed stays marked.</Notice>
        </Section>
      )}

      {live && item.status === "failed" && !continuedBy.length && <RecoveryCard item={item} />}

      {item.operations.uncertain > 0 && !hasUncertainChangeRow && (
        <Section>
          <Notice tone="warning">
            An action may or may not have gone through. Nori is checking with the provider and won’t repeat it.
          </Notice>
        </Section>
      )}

      {detail.result && (
        <OutcomeCard
          title={primaryArtifact?.title ?? (item.status === "failed" ? "Completed so far" : "Summary")}
          hasArtifact={Boolean(primaryArtifact)}
          markdown={detail.result}
        />
      )}

      {extraArtifacts.length > 0 && (
        <Section title="Artifacts">
          <Rows label="Artifacts">
            {extraArtifacts.map((result) => (
              <Row
                key={result.id}
                href={`/library/${encodeURIComponent(result.id)}`}
                leading={
                  <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                    <FileText size={16} strokeWidth={1.8} aria-hidden />
                  </span>
                }
                title={result.title}
              />
            ))}
          </Rows>
        </Section>
      )}

      {!has("library") && item.result_ids.length > 0 && (
        <Section title="Artifacts">
          <Rows label="Artifacts">
            {item.result_ids.map((id) => {
              const found = runtime.session?.results?.find((result) => result.id === id);
              return (
                <Row
                  key={id}
                  href={`/library/${encodeURIComponent(id)}`}
                  leading={
                    <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                      <FileText size={16} strokeWidth={1.8} aria-hidden />
                    </span>
                  }
                  title={found?.title ?? "Saved result"}
                />
              );
            })}
          </Rows>
        </Section>
      )}

      {changesMade.length > 0 && (
        <Section title="Changes made">
          <Rows label="Changes made">
            {changesMade.map((op) => {
              const isWarning = op.outcome === "uncertain" || op.outcome === "failed";
              const exceptionPill = op.outcome === "confirmed" && !op.cleared ? undefined : operationOutcomePill(op);
              const exceptionDetail =
                op.outcome === "uncertain"
                  ? "Checking with provider — won’t be repeated"
                  : op.outcome === "failed"
                    ? op.error?.message || "Action failed"
                    : undefined;
              return (
                <Row
                  key={op.id}
                  leading={
                    <span
                      className={cn(
                        "grid size-9 place-items-center rounded-[10px]",
                        isWarning ? "bg-warning-bg text-warning" : "bg-ok-bg text-ok",
                      )}
                    >
                      {isWarning ? (
                        <AlertTriangle size={16} strokeWidth={1.9} aria-hidden />
                      ) : (
                        <Check size={16} strokeWidth={2.2} aria-hidden />
                      )}
                    </span>
                  }
                  title={op.action_summary || stepName(op.name)}
                  detail={exceptionDetail}
                  wrapTitle
                  trailing={
                    <>
                      {exceptionPill && <Pill tone={exceptionPill.tone}>{exceptionPill.label}</Pill>}
                      {op.receipt_url && /^https:\/\//.test(op.receipt_url) && (
                        <a
                          href={op.receipt_url}
                          target="_blank"
                          rel="noreferrer noopener"
                          className="inline-flex items-center gap-0.5 text-meta font-semibold text-accent-text hover:underline"
                        >
                          Open
                          <ArrowUpRight size={13} aria-hidden />
                        </a>
                      )}
                    </>
                  }
                />
              );
            })}
          </Rows>
        </Section>
      )}

      {item.status !== "running" && (
        <Section>
          <Rows label="Follow up">
            <Row
              onClick={followUpInChat}
              leading={
                <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                  <MessageSquare size={16} strokeWidth={1.85} aria-hidden />
                </span>
              }
              title="Follow up in Chat"
              chevron
            />
          </Rows>
        </Section>
      )}

      <StepsDisclosure steps={steps} running={running} />

      <PlainDetailsSection item={item} continuedBy={continuedBy} />
    </Screen>
  );
}
