"use client";

import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown, Globe, List, Terminal, X } from "lucide-react";
import { Medallion } from "@/components/mark/NoriMark";
import { DebugBlock } from "@/components/dev/DebugBlock";
import { Button, IconButton, SectionHeader } from "@/components/ui/primitives";
import type { Artifact, PendingTaskCommand, Run, Schedule, Session, TaskCommand , SavedResult } from "@/lib/types";
import { clock, separatorLabel } from "@/lib/time";
import { cn } from "@/lib/utils";

const unverified = (run: Run) => run.status === "failed" && /^(unverified\b|Completion verification was unavailable)/i.test(run.error || "");
const incomplete = (run: Run) => run.status === "failed" && /^(incomplete\b|The completion checks still)/i.test(run.error || "");

function statusLabel(run: Run) {
  if (unverified(run)) return "Answer unverified";
  if (incomplete(run)) return "Incomplete";
  if (run.status === "running" && run.cancel_requested) return "Stopping…";
  if (run.status === "running" && run.question?.resolved_by) return run.question.kind === "budget" ? "Budget accepted" : run.question.kind === "approval" ? "Decision saved" : run.question.kind === "work" ? "Waiting for its subtasks" : "Answer accepted";
  if (run.status === "running" && (run.question || run.waiting)) return run.question?.kind === "budget" ? "Waiting for budget approval" : run.question?.kind === "approval" ? "Waiting for your approval" : run.question?.kind === "work" ? "Asking for separate work" : "Waiting for your answer";
  if (run.status === "running" && run.events?.at(-1)?.kind === "checking") return "Checking the answer";
  if (run.status === "running" && run.progress) return "Running a long job";
  return { running: "Working on your task", completed: "Completed", failed: "Failed", cancelled: "Stopped" }[run.status];
}

/** Each saved output and what's known about it. Reported and delivered are separate from the result existing. */
function SavedResults({ results, agentMode }: { results: SavedResult[]; agentMode?: boolean }) {
  if (!results.length) return null;
  const api = agentMode ? "/api/agent" : "/api";
  return <section className="mb-3 rounded-tile bg-fill-1 p-3 text-meta" aria-label="Saved results">
    <h4 className="font-medium">Saved results</h4>
    <ul className="mt-2 space-y-2">{results.map((result) => <li key={result.id} className="min-w-0">
      <p className="break-words">{result.title}{result.version > 1 ? ` · version ${result.version}` : ""}</p>
      <p className="text-caption text-ink-3">{[result.superseded_by ? "Replaced by a newer version" : "", result.checked_at ? "Checked" : "Not checked", result.delivered_at ? "Delivered" : result.reported_at ? "In a reply" : "Not reported yet"].filter(Boolean).join(" · ")}</p>
      {result.kind === "file" && <a className="text-caption underline" href={`${api}/v1/results/${result.id}/file`} download={result.name}>Download {result.name}</a>}
      {result.kind === "link" && result.url && /^https?:\/\//.test(result.url) && <a className="text-caption underline" href={result.url} target="_blank" rel="noreferrer noopener">Open link</a>}
    </li>)}</ul>
  </section>;
}

const kindLabel = { reminder: "Reminder", task: "Scheduled task", monitor: "Monitor" };

/** Open schedules, with the same pause, resume and cancel the user can ask for in chat. */
function Schedules({ schedules, agentMode, onChanged }: { schedules: Schedule[]; agentMode?: boolean; onChanged: () => void }) {
  const [sending, setSending] = useState<string>();
  const [error, setError] = useState("");
  if (!schedules.length) return null;
  const change = async (id: string, next: "pause" | "resume" | "cancel") => {
    setSending(id);
    setError("");
    try {
      const response = await fetch(`${agentMode ? "/api/agent" : "/api"}/schedule`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id, change: next }), cache: "no-store" });
      if (!response.ok && response.status !== 409) throw new Error();
      onChanged();
    } catch {
      setError("That didn't save. Try again, or ask Nori in the chat.");
    } finally {
      setSending(undefined);
    }
  };
  return <details className="mt-4">
    <summary className="cursor-pointer px-2 py-2 text-meta font-medium">Schedules ({schedules.length})</summary>
    {error && <p role="alert" className="px-2 text-caption">{error}</p>}
    <ul className="space-y-1">{schedules.map((schedule) => <li key={schedule.id} className="rounded-tile border border-hairline p-3">
      <p className="line-clamp-2 text-row-title font-medium">{schedule.text}</p>
      <p className="mt-1 text-caption text-ink-3">{kindLabel[schedule.kind] ?? "Schedule"} · {schedule.status === "paused" ? "Paused" : schedule.next_at ? `Next ${separatorLabel(Date.parse(schedule.snooze_at ?? schedule.next_at))}` : "No time set"}</p>
      <div className="mt-2 flex flex-wrap gap-2">
        <Button size="sm" variant="quiet" disabled={sending === schedule.id} onClick={() => void change(schedule.id, schedule.status === "paused" ? "resume" : "pause")}>{schedule.status === "paused" ? "Resume" : "Pause"}</Button>
        <Button size="sm" variant="quiet" disabled={sending === schedule.id} onClick={() => void change(schedule.id, "cancel")}>Cancel</Button>
      </div>
    </li>)}</ul>
  </details>;
}

function Screenshot({
  runId,
  artifact,
  endpoint,
}: {
  runId: string;
  artifact: Artifact;
  endpoint: string;
}) {
  const [url, setUrl] = useState<string>();
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    let objectUrl: string | undefined;
    async function load() {
      try {
        const response = await fetch(endpoint, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ run_id: runId, artifact_id: artifact.id }),
          signal: controller.signal,
        });
        if (
          !response.ok ||
          !response.headers.get("content-type")?.startsWith("image/png")
        )
          throw new Error("Screenshot could not be loaded.");
        objectUrl = URL.createObjectURL(await response.blob());
        if (!controller.signal.aborted) setUrl(objectUrl);
      } catch (err) {
        if (!controller.signal.aborted)
          setError(
            err instanceof Error ? err.message : "Screenshot unavailable.",
          );
      }
    }
    void load();
    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [runId, artifact.id, endpoint]);
  if (error)
    return (
      <p role="status" className="text-meta text-ink-2">
        {error}
      </p>
    );
  if (!url) return <p className="text-meta text-ink-3">Loading screenshot…</p>;
  return (
    <a
      href={url}
      target="_blank"
      rel="noreferrer"
      className="block overflow-hidden rounded-tile bg-surface shadow-pill"
    >
      {/* PNG bytes arrive only from the authenticated artifact route. */}
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img
        src={url}
        alt="Screenshot captured by Nori during this task. Open full size."
        className="w-full"
      />
      <span className="block p-3 text-meta text-accent-text">
        Open screenshot ↗
      </span>
    </a>
  );
}

export function TaskPanel({
  session, selected, pending, loading, error, unavailable, onSelect, onRefresh, onClose,
  artifactEndpoint = "/api/artifact", commands, drafts, onDraft, onCommand, onRetryCommand, onDismissCommand, debug = false, agentMode = false,
}: {
  debug?: boolean;
  agentMode?: boolean;
  session?: Session;
  selected?: Run;
  pending: boolean;
  loading: boolean;
  error: string;
  unavailable: boolean;
  onSelect: (id: string) => void;
  onRefresh: () => void;
  onClose: () => void;
  artifactEndpoint?: string;
  commands: PendingTaskCommand[];
  drafts: Record<string, string>;
  onDraft: (id: string, text: string) => void;
  onCommand: (id: string, kind: TaskCommand["kind"], text?: string, questionId?: string) => Promise<void>;
  onRetryCommand: (item: PendingTaskCommand) => Promise<void>;
  onDismissCommand: (id: string) => void;
}) {
  const [collapsedId, setCollapsedId] = useState<string>();
  const selectedCard = useRef<HTMLElement>(null);
  const scrollTarget = useRef<string>(undefined);
  const answerInput = useRef<HTMLTextAreaElement>(null);
  const focusAnswer = useRef<string>(undefined);
  const expanded = !!selected && selected.id !== collapsedId;
  useEffect(() => {
    if (expanded && selected?.id === scrollTarget.current) {
      selectedCard.current?.scrollIntoView({ block: "start" });
      scrollTarget.current = undefined;
    }
    if (expanded && selected?.id === focusAnswer.current) {
      answerInput.current?.focus();
      focusAnswer.current = undefined;
    }
  }, [expanded, selected?.id]);
  function selectTask(id: string) {
    scrollTarget.current = selected?.id !== id || !expanded ? id : undefined;
    setCollapsedId(selected?.id === id && expanded ? id : undefined);
    onSelect(id);
  }
  function answerQuestion(id: string) {
    if (selected?.id === id && expanded) answerInput.current?.focus();
    else focusAnswer.current = id;
    setCollapsedId(undefined);
    onSelect(id);
  }
  const activeCount = session?.runs.filter((run) => run.status === "running").length ?? 0;
  const runs = session?.runs ?? [];
  const visibleTasks = runs.filter((run) => run.status === "running" || commands.some((item) => item.command.run_id === run.id));
  const recentTasks = runs.filter((run) => !visibleTasks.some((item) => item.id === run.id));
  const connection = error ? "Connection interrupted" : loading ? "Loading history…" : !session ? "Runtime unavailable"
    : unavailable ? "Task execution paused" : activeCount ? `Working on ${activeCount} ${activeCount === 1 ? "task" : "tasks"}` : "Ready when you are";
  const selectedCommand = commands.find((item) => item.command.run_id === selected?.id);
  const question = selected?.question;
  const answerable = question?.kind === "input" || (question?.kind === "work" && !question.resolved_by);
  const draft = selected ? drafts[selected.id] || "" : "";
  const draftTooLong = new TextEncoder().encode(draft).length > 8192;
  const updateDisabled = unavailable || !!selectedCommand || !draft.trim() || draftTooLong;
  const title = (run: Run) => run.request || session?.messages.find((message) => message.run_id === run.id && message.role === "user")?.text || "Task";
  const selectedDetails = selected && <section key={selected.id} id={`task-details-${selected.id}`} aria-label="Selected task details" className="mt-3 border-t border-hairline pt-3">
    <h4 className="mb-3 text-meta font-medium">Task details</h4>
    {selected.status === "running" && (question?.text || selected.waiting) && <p className="mb-3 whitespace-pre-wrap break-words text-meta text-ink-2 [overflow-wrap:anywhere]">{question?.text || selected.waiting}</p>}
    {selected.status === "running" && !question?.text && !selected.waiting && selected.progress && <p className="mb-3 whitespace-pre-wrap break-words text-meta text-ink-2 [overflow-wrap:anywhere]">{selected.progress}</p>}
    {selected.error && <p role="status" className={cn("mb-3 rounded-tile p-3 text-meta", selected.status === "failed" ? "bg-danger-bg text-danger" : "bg-fill-1 text-ink-2")}>{selected.error}</p>}
    {selected.status === "running" && !selected.cancel_requested && <form className="mb-4 space-y-2 px-2" onSubmit={(event) => {
      event.preventDefault();
      const text = drafts[selected.id]?.trim();
      if (!text || updateDisabled || (answerable && question?.resolved_by)) return;
      void onCommand(selected.id, answerable ? "answer" : "steer", text, answerable ? question?.id : undefined);
    }}>
      <label className="block text-meta font-medium" htmlFor={`update-${selected.id}`}>{answerable ? "Answer this question" : "Update this task"}</label>
      <textarea ref={answerInput} id={`update-${selected.id}`} value={draft} onChange={(event) => onDraft(selected.id, event.target.value)}
        maxLength={8192} rows={2} disabled={unavailable || !!selectedCommand}
        className="w-full resize-y rounded-tile bg-fill-1 p-3 text-meta text-ink" />
      {draftTooLong && <p role="alert" className="text-meta text-danger">Keep this within 8,192 UTF-8 bytes.</p>}
      <Button type="submit" size="sm" disabled={updateDisabled || (answerable && !!question?.resolved_by)}>
        {answerable ? "Send answer" : "Send task update"}
      </Button>
      {answerable && <Button type="button" size="sm" variant="quiet" disabled={updateDisabled} onClick={() => void onCommand(selected.id, "steer", draft)}>Send task update</Button>}
    </form>}
    {!!selected.holds?.length && <section className="mb-3 rounded-tile bg-fill-1 p-3 text-meta" aria-label="Held questions">
      <h4 className="font-medium">Follow-up questions</h4>
      <ul className="mt-2 space-y-2">{selected.holds.map((hold) => <li key={hold.id}>
        <p className="whitespace-pre-wrap break-words">{hold.question}</p>
        <p className="text-caption text-ink-3">{hold.status === "answered" ? "Answered in chat" : hold.status === "failed" ? "Chat reply unavailable" : hold.status === "ready" ? "Preparing a chat reply" : "Waiting for the task result"}</p>
      </li>)}</ul>
    </section>}
    {selected.result && <details className="mb-3 rounded-tile bg-fill-1 p-3 text-meta" open>
      <summary className="cursor-pointer font-medium">{unverified(selected) ? "Proposed answer · unverified" : incomplete(selected) ? "Partial answer" : "Task result"}</summary>
      <p className="mt-2 whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{selected.result}</p>
    </details>}
    <SavedResults results={(session?.results ?? []).filter((result) => result.run_id === selected.id)} agentMode={agentMode} />
    {!!selected.events?.length && <details className="mb-3" open>
      <summary className="cursor-pointer px-2 py-2 text-meta font-medium">Task activity</summary>
      <ol className="space-y-2" aria-label="Task events">{selected.events.map((event, index) => {
        const Icon = /browser|web/.test(event.kind) || /^(browser|web|screenshot)/.test(event.text) ? Globe : /shell|command/.test(event.kind) || /^(shell|job)/.test(event.text) ? Terminal : Check;
        return <li key={event.id || `${selected.id}-${index}`} className="flex items-start gap-3 rounded-tile px-2 py-2">
          <span className="grid size-10 shrink-0 place-items-center rounded-tile bg-fill-1 text-ink"><Icon size={20} strokeWidth={1.5} /></span>
          <div className="min-w-0 flex-1">
            <div className="flex items-center justify-between gap-2"><span className="text-row-title font-medium capitalize">{event.kind.replaceAll("_", " ")}</span><time className="text-caption text-ink-3">{clock(Date.parse(event.created_at))}</time></div>
            <p className="mt-1 whitespace-pre-wrap break-words text-meta text-ink-2 [overflow-wrap:anywhere]">{event.text}</p>
          </div>
        </li>;
      })}</ol>
    </details>}
    {debug && <DebugBlock key={selected.id} info={{ run: selected, agentMode }} />}
    {!!selected.artifacts?.length && <section className="space-y-3"><SectionHeader>Screenshots</SectionHeader>{selected.artifacts.map((artifact) => <Screenshot key={`${selected.id}-${artifact.id}`} runId={selected.id} artifact={artifact} endpoint={artifactEndpoint} />)}</section>}
  </section>;
  return (
    <div className="flex h-full flex-col bg-canvas px-4">
      <header className="relative flex shrink-0 flex-col items-center pt-12">
        <IconButton size={36} label="Hide agent panel" tip={false} onClick={onClose} className="absolute top-3 right-0">
          <X size={18} strokeWidth={1.5} />
        </IconButton>
        <Medallion size={96} state={activeCount ? "thinking" : "idle"} watch />
        <h2 className="mt-3 text-title font-medium text-ink">Nori</h2>
        <p className="mt-1 flex items-center gap-2 text-status text-ink-2" role="status">
          <span aria-hidden className={cn("size-2 rounded-full", error || unavailable || !session ? "bg-ink-3" : "bg-accent-signal", activeCount > 0 && "pulse-dot")} />
          {connection}
        </p>
        <div className="mt-6 mb-3 flex w-full items-center gap-2 rounded-full bg-fill-1 px-3 py-2">
          <List size={17} strokeWidth={1.5} /><h3 className="text-meta font-medium">Activity</h3>
        </div>
      </header>
      <div className="scrollbar-quiet -mx-4 min-h-0 flex-1 overflow-y-auto px-4 pb-6">
        {!runs.length ? <p className="px-2 py-5 text-meta text-ink-3">{pending ? "Sending your message to Nori…" : "Activity and screenshots from your tasks appear here."}</p> : (
          <section aria-label="Tasks" className="space-y-2">
            {visibleTasks.map((run) => {
              const taskCommands = commands.filter((item) => item.command.run_id === run.id);
              const stopping = taskCommands.some((item) => item.command.kind === "stop");
              return <article key={run.id} ref={selected?.id === run.id ? selectedCard : undefined} aria-label={title(run)} className={cn("scroll-mt-3 rounded-tile border border-hairline p-3", selected?.id === run.id && "bg-fill-1")}>
                <button type="button" aria-expanded={selected?.id === run.id && expanded} aria-controls={selected?.id === run.id && expanded ? `task-details-${run.id}` : undefined} onClick={() => selectTask(run.id)} className="flex w-full items-center gap-2 rounded-tile text-left">
                  <span className="min-w-0 flex-1">
                    <span className="line-clamp-2 text-row-title font-medium">{title(run)}</span>
                    <small className="mt-1 block text-caption text-ink-3">{run.parent_id ? "Subtask · " : ""}{statusLabel(run)}</small>
                    {run.status === "running" && (selected?.id !== run.id || !expanded) && (run.question?.text || run.waiting) && <span className="mt-2 line-clamp-3 whitespace-pre-wrap break-words text-meta text-ink-2 [overflow-wrap:anywhere]">{run.question?.text || run.waiting}</span>}
                    <span className="mt-2 block text-caption font-medium text-accent-text">{selected?.id === run.id && expanded ? "Hide task details" : "View task details"}</span>
                  </span>
                  <ChevronDown size={14} aria-hidden className={cn("shrink-0 text-ink-3", (selected?.id !== run.id || !expanded) && "-rotate-90")} />
                </button>
                {run.question?.resolved_by && run.status === "running" && <p className="mt-2 text-caption text-ink-3">{run.question.kind === "budget" ? "Budget accepted." : run.question.kind === "approval" ? "Decision saved." : run.question.kind === "work" ? "Subtasks are running." : "Answer accepted."} Waiting for the task to continue.</p>}
                {(run.status === "running" || taskCommands.length > 0) && <div className="mt-2 space-y-2">
                  {run.status === "running" && <div className="flex flex-wrap gap-2">
                    {(run.question?.kind === "input" || run.question?.kind === "work") && !run.question.resolved_by && !run.cancel_requested && <Button size="sm" disabled={unavailable || taskCommands.length > 0} onClick={() => answerQuestion(run.id)}>Answer question</Button>}
                    {run.question?.kind === "budget" && !run.question.resolved_by && <Button size="sm" disabled={unavailable || taskCommands.length > 0 || run.cancel_requested} onClick={() => void onCommand(run.id, "continue", undefined, run.question!.id)}>Continue task (+$1)</Button>}
                    {run.question?.kind === "approval" && !run.question.resolved_by && !run.cancel_requested && <>
                      <Button size="sm" disabled={unavailable || taskCommands.length > 0} onClick={() => void onCommand(run.id, "approve", undefined, run.question!.id)}>Approve</Button>
                      <Button size="sm" variant="quiet" disabled={unavailable || taskCommands.length > 0} onClick={() => void onCommand(run.id, "decline", undefined, run.question!.id)}>Decline</Button>
                    </>}
                    <Button size="sm" variant="quiet" disabled={stopping || run.cancel_requested} onClick={() => void onCommand(run.id, "stop")}>
                      {run.cancel_requested || stopping ? "Stopping…" : "Stop task"}
                    </Button>
                  </div>}
                  {run.cancel_requested && run.status === "running" && <p className="text-caption text-ink-3">Waiting for the computer to confirm it has stopped.</p>}
                  {taskCommands.map((command) => <div key={command.command.id} role={command.error ? "alert" : "status"} className="text-meta text-ink-2">
                    <p>{command.sending ? "Sending task command…" : command.error || "Waiting for command acceptance…"}</p>
                    {command.command.text && <p className="mt-1 line-clamp-3 whitespace-pre-wrap">{command.command.text}</p>}
                    {!command.sending && command.error && (command.rejected ? <Button size="sm" variant="quiet" onClick={() => {
                      if (command.command.text) onDraft(run.id, command.command.text);
                      onDismissCommand(command.command.id);
                    }}>Edit task command</Button> : <Button size="sm" variant="quiet" onClick={() => void onRetryCommand(command)}>Retry task command</Button>)}
                  </div>)}
                </div>}
                {selected?.id === run.id && expanded && selectedDetails}
              </article>;
            })}
          </section>
        )}

        {!!recentTasks.length && <details className="mt-4" open={recentTasks.some((run) => run.id === selected?.id)}>
          <summary className="cursor-pointer px-2 py-2 text-meta font-medium">Recent tasks ({recentTasks.length})</summary>
          {recentTasks.map((run) => <article key={run.id} ref={selected?.id === run.id ? selectedCard : undefined} aria-label={title(run)} className={cn("my-1 scroll-mt-3 rounded-tile border border-hairline p-3", selected?.id === run.id && "bg-fill-1")}>
            <button type="button" aria-expanded={selected?.id === run.id && expanded} aria-controls={selected?.id === run.id && expanded ? `task-details-${run.id}` : undefined} onClick={() => selectTask(run.id)}
              className="flex w-full items-center gap-2 rounded-tile text-left">
              <span className="min-w-0 flex-1"><span className="line-clamp-2 text-row-title font-medium">{title(run)}</span><small className="mt-1 block text-caption text-ink-3">{run.parent_id ? "Subtask · " : ""}{statusLabel(run)}</small></span>
              <ChevronDown size={14} aria-hidden className={cn("shrink-0 text-ink-3", (selected?.id !== run.id || !expanded) && "-rotate-90")} />
            </button>
            {selected?.id === run.id && expanded && selectedDetails}
          </article>)}
        </details>}
        <Schedules schedules={session?.schedules ?? []} agentMode={agentMode} onChanged={onRefresh} />
        <Button size="sm" variant="quiet" onClick={onRefresh} disabled={loading} className="mt-4 w-full">{loading ? "Loading…" : "Refresh history"}</Button>
      </div>
    </div>
  );
}
