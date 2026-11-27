"use client";

import Link from "next/link";
import { ChevronRight, RefreshCw, Square, X } from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { Pill, Rows, type Tone } from "@/components/assistant/ui";
import { Button, IconButton } from "@/components/ui/primitives";
import { relative } from "@/lib/assistant/format";
import type { PendingTaskCommand, Run, Session, TaskCommand } from "@/lib/types";
import { cn } from "@/lib/utils";

const RECENT_LIMIT = 5;

const isPartial = (run: Run) => run.status === "failed" && /^incomplete\b/i.test(run.error || "");

function taskTitle(run: Run, session?: Session): string {
  const explicit = run.title?.trim();
  if (explicit) return explicit;
  const fallback = run.request || session?.messages.find((message) => message.run_id === run.id && message.role === "user")?.text || "";
  const firstLine = fallback.trim().split("\n")[0]?.replace(/\s+/g, " ").trim() ?? "";
  if (!firstLine) return "Task";
  const sentenceEnd = firstLine.search(/[.!?]\s/);
  const concise = sentenceEnd >= 8 ? firstLine.slice(0, sentenceEnd) : firstLine;
  const cleaned = concise
    .replace(/\s*\((?:the\s+)?user['’]s\s+words?\)/gi, "")
    .replace(/,\s+the\s+user['’]s\s+[^.!?\n]+/gi, "")
    .replace(/\bfor\s+the\s+user\b/gi, "")
    .replace(/\s+/g, " ")
    .replace(/[.\s]+$/, "");
  const title = cleaned || concise;
  return title.length > 80 ? `${title.slice(0, 79).trimEnd()}…` : title;
}

function durationLabel(start?: string, end?: string): string | undefined {
  if (!start || !end) return undefined;
  const ms = Date.parse(end) - Date.parse(start);
  if (!Number.isFinite(ms) || ms < 1000) return undefined;
  const sec = Math.round(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const rem = sec % 60;
  return rem ? `${min}m ${rem}s` : `${min}m`;
}

function activePill(run: Run, stopping: boolean): { tone: Tone; label: string } {
  if (run.cancel_requested || stopping || (run.finishing && run.cancel_requested)) {
    return { tone: "neutral", label: "Stopping" };
  }
  if (run.question && !run.question.resolved_by) {
    return { tone: "warning", label: run.question.kind === "work" ? "Waiting" : "Waiting" };
  }
  if (!run.question && run.waiting) {
    return { tone: "warning", label: "Waiting" };
  }
  return { tone: "working", label: "Working" };
}

function activeDetail(run: Run): string {
  const started = run.started_at ? relative(run.started_at) : "";
  if (run.cleanup_error) return run.cleanup_error;
  if (run.cancel_requested) return "Stopping task…";
  if (run.finishing) return "Wrapping up…";
  if (run.question?.resolved_by) {
    return run.question.kind === "work" ? "Waiting for subtasks" : "Continuing with your answer";
  }
  if (run.question || run.waiting) {
    if (run.question?.kind === "work") return "Coordinating subtasks";
    return "Needs a reply in chat";
  }
  if (run.progress?.trim()) {
    return started ? `${run.progress.trim()} · ${started}` : run.progress.trim();
  }
  return started ? `Started ${started}` : "Working on it";
}

function recentPill(run: Run): { tone: Tone; label: string } {
  if (run.status === "completed") return { tone: "ok", label: "Done" };
  if (isPartial(run)) return { tone: "warning", label: "Partial" };
  if (run.status === "failed") return { tone: "danger", label: "Failed" };
  return { tone: "neutral", label: "Stopped" };
}

function recentDetail(run: Run): string {
  const finished = run.finished_at || run.started_at;
  const rel = finished ? relative(finished) : "";
  const dur = durationLabel(run.started_at, run.finished_at);
  return [rel, dur].filter(Boolean).join(" · ") || "Finished";
}

export function TaskPanel({
  session,
  pending,
  loading,
  error,
  unavailable,
  onRefresh,
  onClose,
  onReplyInChat,
  commands,
  onCommand,
  onRetryCommand,
  onDismissCommand,
}: {
  session?: Session;
  pending: boolean;
  loading: boolean;
  error: string;
  unavailable: boolean;
  onRefresh: () => void;
  onClose: () => void;
  onReplyInChat: () => void;
  commands: PendingTaskCommand[];
  onCommand: (id: string, kind: TaskCommand["kind"], text?: string, questionId?: string) => Promise<void>;
  onRetryCommand: (item: PendingTaskCommand) => Promise<void>;
  onDismissCommand: (id: string) => void;
}) {
  const runs = session?.runs ?? [];
  const commandsByRun = new Map<string, PendingTaskCommand[]>();
  for (const item of commands) {
    const runID = item.command.run_id;
    const existing = commandsByRun.get(runID);
    if (existing) existing.push(item);
    else commandsByRun.set(runID, [item]);
  }

  const activeTasks: Run[] = [];
  const finishedTasks: Run[] = [];
  for (const run of runs) {
    if (run.status === "running" || commandsByRun.has(run.id)) activeTasks.push(run);
    else finishedTasks.push(run);
  }
  finishedTasks.sort(
    (a, b) => Date.parse(b.finished_at ?? b.started_at) - Date.parse(a.finished_at ?? a.started_at),
  );
  const recentTasks = finishedTasks.slice(0, RECENT_LIMIT);
  const activeCount = activeTasks.filter((run) => run.status === "running").length;

  const headerStatus = error
    ? { tone: "danger" as Tone, label: "Reconnecting", pulse: false }
    : !session
      ? { tone: "neutral" as Tone, label: loading ? "Loading…" : "Offline", pulse: false }
      : unavailable
        ? { tone: "warning" as Tone, label: "Paused", pulse: false }
        : activeCount > 0
          ? { tone: "working" as Tone, label: `${activeCount} running`, pulse: true }
          : { tone: "neutral" as Tone, label: "All quiet", pulse: false };

  return (
    <div className="flex h-full flex-col bg-sheet text-ink">
      <header className="flex shrink-0 items-center justify-between gap-2 border-b border-hairline px-4 py-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <NoriMark size={24} state={activeCount > 0 || pending ? "thinking" : "idle"} />
          <h2 className="text-[15px] font-semibold tracking-[-0.01em] text-ink">Activity</h2>
          <span
            role="status"
            className={cn(
              "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-caption font-semibold",
              headerStatus.tone === "working" && "bg-working-bg text-working",
              headerStatus.tone === "warning" && "bg-warning-bg text-warning",
              headerStatus.tone === "danger" && "bg-danger-bg text-danger",
              headerStatus.tone === "neutral" && "bg-inactive-bg text-inactive",
            )}
          >
            {headerStatus.pulse && (
              <span aria-hidden className="pulse-dot size-1.5 rounded-full bg-accent-signal" />
            )}
            {headerStatus.label}
          </span>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <IconButton
            size={32}
            label="Refresh activity"
            tip={false}
            disabled={loading}
            onClick={onRefresh}
            className="bg-fill-1 text-ink-2 hover:bg-fill-2 hover:text-ink"
          >
            <RefreshCw size={14} strokeWidth={1.75} className={cn(loading && "animate-spin")} />
          </IconButton>
          <IconButton
            size={32}
            label="Close activity"
            tip={false}
            onClick={onClose}
            className="bg-fill-1 text-ink-2 hover:bg-fill-2 hover:text-ink"
          >
            <X size={15} strokeWidth={1.75} />
          </IconButton>
        </div>
      </header>

      <div className="scrollbar-quiet min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-4">
        <section aria-label="Active tasks">
          <div className="mb-2 flex items-center justify-between px-0.5">
            <h3 className="text-caption font-semibold text-ink-3">Active</h3>
            {activeTasks.length > 0 && (
              <span className="text-caption font-semibold text-ink-3">{activeTasks.length}</span>
            )}
          </div>

          {activeTasks.length === 0 ? (
            <div className="rounded-card border border-card-border bg-surface px-4 py-3.5 text-meta text-ink-2">
              {pending ? "Handling your latest message…" : "No background tasks running."}
            </div>
          ) : (
            <Rows label="Active tasks">
              {activeTasks.map((run) => {
                const label = taskTitle(run, session);
                const taskCommands = commandsByRun.get(run.id) ?? [];
                const stopping = taskCommands.some((item) => item.command.kind === "stop");
                const pill = activePill(run, stopping);
                const waitingForChatReply =
                  run.status === "running" &&
                  !run.cancel_requested &&
                  !stopping &&
                  ((run.question?.kind === "input" && !run.question.resolved_by) ||
                    (!run.question && Boolean(run.waiting)));
                const failedCommands = taskCommands.filter((item) => !item.sending && item.error);

                return (
                  <li key={run.id} className="transition-colors">
                    <div className="flex items-center gap-2 px-4 py-3 hover:bg-fill-1">
                      <Link
                        href={`/work/${encodeURIComponent(run.id)}`}
                        onClick={onClose}
                        className="min-w-0 flex-1 text-left focus-visible:outline-none"
                      >
                        <span className="block truncate text-row font-semibold text-ink">
                          {run.parent_id ? `Subtask · ${label}` : label}
                        </span>
                        <span className="mt-0.5 line-clamp-2 block text-meta text-ink-2 [overflow-wrap:anywhere]">
                          {activeDetail(run)}
                        </span>
                      </Link>
                      <div className="flex shrink-0 items-center gap-1.5">
                        <Pill tone={pill.tone}>{pill.label}</Pill>
                        {run.status === "running" && !run.cancel_requested && !stopping && (
                          <button
                            type="button"
                            aria-label={`Stop ${label}`}
                            title="Stop task"
                            disabled={unavailable}
                            onClick={() => void onCommand(run.id, "stop")}
                            className="grid size-7 shrink-0 place-items-center rounded-full text-ink-3 transition-colors hover:bg-danger-bg hover:text-danger disabled:opacity-40"
                          >
                            <Square size={11} fill="currentColor" aria-hidden />
                          </button>
                        )}
                        <Link
                          href={`/work/${encodeURIComponent(run.id)}`}
                          onClick={onClose}
                          aria-label={`Open ${label}`}
                          className="grid size-6 shrink-0 place-items-center text-ink-3 hover:text-ink"
                        >
                          <ChevronRight size={15} aria-hidden />
                        </Link>
                      </div>
                    </div>

                    {waitingForChatReply && (
                      <div className="px-4 pb-3">
                        <button
                          type="button"
                          onClick={onReplyInChat}
                          className="inline-flex items-center gap-1 rounded-full bg-accent-subtle px-3 py-1 text-caption font-semibold text-accent-text transition-colors hover:brightness-105"
                        >
                          Reply in chat →
                        </button>
                      </div>
                    )}

                    {failedCommands.map((command) => (
                      <div
                        key={command.command.id}
                        role="alert"
                        className="flex items-center justify-between gap-2 px-4 pb-3 text-caption text-danger"
                      >
                        <span className="min-w-0 truncate">{command.error}</span>
                        <div className="flex shrink-0 items-center gap-1.5">
                          {!command.rejected && (
                            <Button
                              size="sm"
                              variant="quiet"
                              disabled={loading}
                              onClick={() => void onRetryCommand(command)}
                            >
                              Retry
                            </Button>
                          )}
                          <Button
                            size="sm"
                            variant="quiet"
                            onClick={() => onDismissCommand(command.command.id)}
                          >
                            Dismiss
                          </Button>
                        </div>
                      </div>
                    ))}
                  </li>
                );
              })}
            </Rows>
          )}
        </section>

        {recentTasks.length > 0 && (
          <section aria-label="Recent tasks">
            <div className="mb-2 flex items-center justify-between px-0.5">
              <h3 className="text-caption font-semibold text-ink-3">Recent</h3>
            </div>
            <Rows label="Recent tasks">
              {recentTasks.map((run) => {
                const label = taskTitle(run, session);
                const pill = recentPill(run);
                return (
                  <li key={run.id}>
                    <Link
                      href={`/work/${encodeURIComponent(run.id)}`}
                      onClick={onClose}
                      className="flex min-h-14 items-center gap-3 px-4 py-3 transition-colors hover:bg-fill-1"
                    >
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-row font-semibold text-ink">{label}</span>
                        <span className="mt-0.5 block text-meta text-ink-2">{recentDetail(run)}</span>
                      </span>
                      <Pill tone={pill.tone}>{pill.label}</Pill>
                      <ChevronRight size={15} className="shrink-0 text-ink-3" aria-hidden />
                    </Link>
                  </li>
                );
              })}
            </Rows>
          </section>
        )}
      </div>

      <footer className="flex shrink-0 items-center justify-between gap-3 border-t border-hairline px-4 py-3">
        <span className="text-caption text-ink-3">Click any task for details &amp; steering</span>
        <Link
          href="/work"
          onClick={onClose}
          className="text-meta font-semibold text-accent-text hover:underline"
        >
          Open Work →
        </Link>
      </footer>
    </div>
  );
}
