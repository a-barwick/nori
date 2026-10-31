"use client";

import { taskControlKinds } from "../types";

import { useCallback, useEffect, useRef, useState } from "react";
import * as Sentry from "@sentry/nextjs";
import type { Span } from "@sentry/nextjs";
import type { PendingTaskCommand, Run, Session, TaskCommand, TaskCommandResponse } from "@/lib/types";

class RequestError extends Error {
  constructor(message: string, readonly status: number, readonly details?: TaskCommandResponse) { super(message); }
}

async function post<T>(operation: string, payload: object, signal?: AbortSignal, agentMode = false, span?: Span): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (span) {
    const context = span.spanContext();
    if (!/^0+$/.test(context.traceId) && !/^0+$/.test(context.spanId)) {
      headers["sentry-trace"] = Sentry.spanToTraceHeader(span);
      headers.traceparent = `00-${context.traceId}-${context.spanId}-${context.traceFlags & 1 ? "01" : "00"}`;
    }
  }
  const response = await fetch(`${agentMode ? "/api/agent" : "/api"}/${operation}`, {
    method: "POST", headers, body: JSON.stringify(payload), cache: "no-store", signal,
  });
  const data = await response.json().catch(() => ({ error: "Nori returned an unreadable response." }));
  if (!response.ok) throw new RequestError(data.error || `Request failed (${response.status}).`, response.status, data);
  return data as T;
}

type Pending = { id: string; text: string; started: number; error?: string };
type ReplyTiming = { started: number; span: Span; timer: ReturnType<typeof setTimeout> };
const STORAGE_KEY = "nori-pending-turns-v1";
const MAX_PENDING = 20;
const TIMING_STORAGE_KEY = "nori-reply-timings-v1";
function saveTimings(timings: Map<string, ReplyTiming>) {
  try { sessionStorage.setItem(TIMING_STORAGE_KEY, JSON.stringify([...timings].map(([id, timing]) => ({ id, started: timing.started })))); } catch { /* Best effort in private browsing. */ }
}
function restoredTimings(): Pending[] {
  try {
    const values: unknown = JSON.parse(sessionStorage.getItem(TIMING_STORAGE_KEY) || "[]");
    return Array.isArray(values) ? values.slice(-MAX_PENDING).filter((value) => typeof value?.id === "string" && /^[a-f0-9-]{36}$/.test(value.id) && Number.isFinite(value.started) && value.started <= Date.now()).map((value) => ({ id: value.id, started: value.started, text: "" })) : [];
  } catch { return []; }
}

const COMMAND_STORAGE_KEY = "nori-pending-task-commands-v1";
function restoreTaskCommands(): PendingTaskCommand[] {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(COMMAND_STORAGE_KEY) || "[]");
    if (!Array.isArray(value)) return [];
    return value.filter((item): item is PendingTaskCommand => item && typeof item.started === "number" &&
      typeof item.command?.id === "string" && /^[a-f0-9-]{36}$/.test(item.command.id) &&
      typeof item.command.run_id === "string" && taskControlKinds.includes(item.command.kind) &&
      (item.command.text === undefined || (typeof item.command.text === "string" && new TextEncoder().encode(item.command.text).length <= 8192)) &&
      (item.command.question_id === undefined || typeof item.command.question_id === "string"))
      .map((item) => ({ ...item, sending: false }));
  } catch { return []; }
}
function commandApplied(item: PendingTaskCommand, runs: Run[]) {
  const run = runs.find((run) => run.id === item.command.run_id);
  return !!run && ((item.position !== undefined && ((run.incorporated ?? 0) >= item.position || (run.status !== "running" && (run.position ?? 0) >= item.position))) ||
    (item.command.kind === "stop" && run.status !== "running"));
}
function restorePending(): Pending[] {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(STORAGE_KEY) || "[]");
    if (!Array.isArray(value)) return [];
    return value.filter((item): item is Pending => item && typeof item.id === "string" &&
      /^[a-f0-9-]{36}$/.test(item.id) && typeof item.text === "string" &&
      new TextEncoder().encode(item.text).length <= 8192 && typeof item.started === "number").slice(0, MAX_PENDING);
  } catch { return []; }
}
const acknowledged = (data: Session, id: string) => data.conversation?.input_ids.includes(id) || data.messages.some((message) => message.id === `${id}-user`);
function mergeById<T extends { id: string }>(older: T[], newer: T[]): T[] {
  return [...new Map([...older, ...newer].map((item) => [item.id, item])).values()];
}

// Responses can arrive out of order; keep newer task progress.
function mergeRuns(older: Run[], newer: Run[]): Run[] {
  const runs = new Map(older.map((run) => [run.id, run]));
  for (const run of newer) {
    const previous = runs.get(run.id);
    if (previous && ((previous.position ?? 0) > (run.position ?? 0) ||
      (previous.event_sequence ?? 0) > (run.event_sequence ?? 0) ||
      (previous.incorporated ?? 0) > (run.incorporated ?? 0) ||
      (previous.status !== "running" && run.status === "running") ||
      (previous.cancel_requested && !run.cancel_requested))) continue;
    runs.set(run.id, run);
  }
  return [...runs.values()].sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));
}

export function useRuntime(agentMode = false) {
  const [session, setSession] = useState<Session>();
  const [draft, setDraftState] = useState("");
  const draftValue = useRef("");
  const setDraft = (text: string) => { draftValue.current = text; setDraftState(text); };
  const [error, setError] = useState("");
  const [pollError, setPollError] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [pending, setPending] = useState<Pending[]>(restorePending);
  const pendingRef = useRef(pending);
  const [taskCommands, setTaskCommands] = useState<PendingTaskCommand[]>(restoreTaskCommands);
  const taskCommandsRef = useRef(taskCommands);
  const [taskDrafts, setTaskDrafts] = useState<Record<string, string>>({});
  const [selectedId, setSelectedId] = useState<string>();
  const [visible, setVisible] = useState(true);
  const controllers = useRef(new Set<AbortController>());
  const submissions = useRef(new Set<string>());
  const replyTimings = useRef(new Map<string, ReplyTiming>());
  const commandSubmissions = useRef(new Set<string>());
  const requestSerial = useRef(0);
  const appliedSerial = useRef(0);
  const appliedRevision = useRef(-1);
  const activeTasks = session?.runs.filter((run) => run.status === "running") ?? [];
  const selected = session?.runs.find((run) => run.id === selectedId) || activeTasks[0] || session?.runs[0];
  const tasksUnavailable = !!session?.paused;
  const conversationBusy = !!(session?.conversation?.pending || session?.conversation?.processing);
  const taskPolling = activeTasks.some((run) => run.cancel_requested) || (activeTasks.length > 0 && !tasksUnavailable) || taskCommands.some((item) => !item.rejected);
  const busy = taskPolling || conversationBusy || pending.some((item) => !item.error);

  const updatePending = useCallback((update: (current: Pending[]) => Pending[]) => {
    const next = update(pendingRef.current);
    pendingRef.current = next;
    setPending(next);
    try {
      if (next.length) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next.map(({ id, text, started }) => ({ id, text, started }))));
      else sessionStorage.removeItem(STORAGE_KEY);
    } catch { /* Private browsing may disable storage; current-tab retries still work. */ }
  }, []);

  const updateTaskCommands = useCallback((update: (current: PendingTaskCommand[]) => PendingTaskCommand[]) => {
    const next = update(taskCommandsRef.current);
    taskCommandsRef.current = next;
    setTaskCommands(next);
    try {
      if (next.length) sessionStorage.setItem(COMMAND_STORAGE_KEY, JSON.stringify(next.map((item) => ({ ...item, sending: false }))));
      else sessionStorage.removeItem(COMMAND_STORAGE_KEY);
    } catch { /* Current-tab retries remain available without storage. */ }
  }, []);

  const applySnapshot = useCallback((data: Session, serial: number) => {
    const revision = data.conversation?.revision ?? 0;
    if (revision < appliedRevision.current || (revision === appliedRevision.current && serial < appliedSerial.current)) return;
    appliedSerial.current = serial;
    appliedRevision.current = revision;
    setSession((current) => ({ ...data,
      messages: mergeById(current?.messages ?? [], data.messages).sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)),
      runs: mergeRuns(current?.runs ?? [], data.runs),
      traces: mergeById(current?.traces ?? [], data.traces ?? []),
      // Preserve pages already loaded while refreshing the newest page.
      next_cursor: current ? current.next_cursor : data.next_cursor,
    }));
    updatePending((current) => current.filter((item) => !acknowledged(data, item.id)));
    updateTaskCommands((current) => current.filter((item) => !commandApplied(item, data.runs)));
  }, [updatePending, updateTaskCommands]);

  const finishTiming = useCallback((id: string, outcome: "reply" | "error" | "timeout" | "unobserved") => {
    const timing = replyTimings.current.get(id);
    if (!timing) return;
    clearTimeout(timing.timer);
    timing.span.setAttribute("nori.outcome", outcome);
    const attributes = { mode: agentMode ? "local" : "hosted", outcome };
    Sentry.withActiveSpan(timing.span, () => {
      Sentry.metrics.count("nori.reply.outcomes", 1, { attributes });
      Sentry.metrics.distribution(outcome === "reply" ? "nori.submit_to_reply" : "nori.submit_to_terminal", Math.max(0, Date.now() - timing.started), { unit: "millisecond", attributes });
    });
    timing.span.end();
    replyTimings.current.delete(id);
    saveTimings(replyTimings.current);
  }, [agentMode]);

  const beginTiming = useCallback((item: Pending) => {
    if (!Sentry.getClient() || replyTimings.current.has(item.id)) return;
    const span = Sentry.startNewTrace(() => Sentry.startInactiveSpan({ name: "nori.submit_to_reply", op: "nori.conversation", parentSpan: null,
      startTime: item.started / 1000, attributes: { "nori.mode": agentMode ? "local" : "hosted" } }));
    const timer = setTimeout(() => finishTiming(item.id, "timeout"), Math.max(0, 15 * 60_000 - (Date.now() - item.started)));
    replyTimings.current.set(item.id, { started: item.started, span, timer });
    saveTimings(replyTimings.current);
  }, [agentMode, finishTiming]);

  useEffect(() => {
    if (!session) return;
    for (const [id] of replyTimings.current) {
      const input = session.messages.find((message) => message.id === `${id}-user`);
      if (!input?.turn_id) continue;
      const attempt = session.traces?.filter((trace) => trace.turn_id === input.turn_id).sort((a,b) => b.attempt - a.attempt)[0];
      if (attempt?.status === "error") { finishTiming(id, "error"); continue; }
      if (attempt?.status !== "completed" || !session.messages.some((message) => message.role === "assistant" && !message.run_id && message.turn_id === input.turn_id)) continue;
      finishTiming(id, "reply");
      Sentry.addBreadcrumb({ category: "nori", message: "reply received" });
    }
  }, [finishTiming, session]);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const serial = ++requestSerial.current;
    const data = await post<Session>("session", {}, signal, agentMode);
    if (!signal?.aborted) { applySnapshot(data, serial); setPollError(""); }
    return data;
  }, [agentMode, applySnapshot]);

  const submit = useCallback(async (item: Pending) => {
    if (submissions.current.has(item.id)) return;
    submissions.current.add(item.id);
    const controller = new AbortController();
    controllers.current.add(controller);
    updatePending((current) => current.map((entry) => entry.id === item.id ? { ...entry, error: undefined } : entry));
    const serial = ++requestSerial.current;
    try {
      const data = await post<Session>("turn", { id: item.id, message: item.text }, controller.signal, agentMode, replyTimings.current.get(item.id)?.span);
      if (!controller.signal.aborted) { applySnapshot(data, serial); setError(""); }
    } catch (err) {
      if (!controller.signal.aborted) {
        // A dropped acceptance response is ambiguous. Keep the same UUID for retry.
        updatePending((current) => current.map((entry) => entry.id === item.id ? { ...entry, error: err instanceof Error ? err.message : "Message acceptance could not be confirmed." } : entry));
        await refresh(controller.signal).catch(() => undefined);
      }
    } finally { controllers.current.delete(controller); submissions.current.delete(item.id); }
  }, [agentMode, applySnapshot, refresh, updatePending]);

  const submitTaskCommand = useCallback(async (item: PendingTaskCommand) => {
    if (commandSubmissions.current.has(item.command.id)) return;
    commandSubmissions.current.add(item.command.id);
    updateTaskCommands((current) => current.map((entry) => entry.command.id === item.command.id ? { ...entry, sending: true, error: undefined } : entry));
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const data = await post<TaskCommandResponse>("command", item.command, controller.signal, agentMode);
      if (!data.command || data.command.id !== item.command.id || data.run?.id !== item.command.run_id) throw new Error("Task command acceptance could not be confirmed.");
      if (!controller.signal.aborted) {
        setSession((current) => current ? { ...current, runs: mergeRuns(current.runs, [data.run]) } : current);
        updateTaskCommands((current) => current.filter((entry) => entry.command.id !== item.command.id));
      }
    } catch (err) {
      if (!controller.signal.aborted) {
        const receipt = err instanceof RequestError ? err.details : undefined;
        if (receipt?.accepted && receipt.run) setSession((current) => current ? { ...current, runs: mergeRuns(current.runs, [receipt.run]) } : current);
        updateTaskCommands((current) => current.map((entry) => entry.command.id === item.command.id ? {
          ...entry, sending: false, accepted: receipt?.accepted || entry.accepted,
          position: receipt?.command?.position ?? entry.position,
          rejected: err instanceof RequestError && err.status >= 400 && err.status < 500,
          error: receipt?.accepted ? "Accepted. Delivery to the task is still pending." : err instanceof Error ? err.message : "Task command could not be confirmed.",
        } : entry));
      }
    } finally {
      controllers.current.delete(controller); commandSubmissions.current.delete(item.command.id);
      if (!controller.signal.aborted) await refresh(controller.signal).catch(() => undefined);
    }
  }, [agentMode, refresh, updateTaskCommands]);

  useEffect(() => {
    const currentControllers = controllers.current;
    const currentTimings = replyTimings.current;
    for (const item of [...restoredTimings(), ...pendingRef.current]) beginTiming(item);
    const controller = new AbortController();
    currentControllers.add(controller);
    void refresh(controller.signal).then((data) => {
      if (!controller.signal.aborted) {
        for (const item of pendingRef.current) if (!acknowledged(data, item.id)) void submit(item);
        for (const item of taskCommandsRef.current) if (!item.rejected && !commandApplied(item, data.runs)) void submitTaskCommand(item);
      }
    }).catch((err) => { if (!controller.signal.aborted) setError(err.message); })
      .finally(() => { currentControllers.delete(controller); if (!controller.signal.aborted) setLoading(false); });
    const reconnect = () => {
      setVisible(!document.hidden);
      if (!document.hidden) void refresh().then((data) => {
        setError("");
        for (const item of pendingRef.current) if (!acknowledged(data, item.id)) void submit(item);
        for (const item of taskCommandsRef.current) if (!item.rejected && !commandApplied(item, data.runs)) void submitTaskCommand(item);
      }).catch((err) => setError(err.message));
    };
    document.addEventListener("visibilitychange", reconnect);
    window.addEventListener("online", reconnect);
    return () => {
      for (const active of currentControllers) active.abort();
      currentControllers.clear();
      for (const timing of currentTimings.values()) { clearTimeout(timing.timer); timing.span.setAttribute("nori.outcome", "unobserved"); timing.span.end(); }
      currentTimings.clear();
      document.removeEventListener("visibilitychange", reconnect);
      window.removeEventListener("online", reconnect);
    };
  }, [refresh, submit, submitTaskCommand, beginTiming, finishTiming]);

  useEffect(() => {
    // Only refresh saved history here; never resubmit messages or task commands.
    // A transient read failure must not permanently strand an active task's UI.
    if ((!taskPolling && !conversationBusy) || !visible) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let failures = 0;
    const poll = async () => {
      try {
        const next = await refresh(controller.signal);
        if (controller.signal.aborted) return;
        failures = 0;
        setPollError("");
        if (next.conversation?.pending || next.conversation?.processing || next.runs.some((run) => run.status === "running" && (!next.paused || run.cancel_requested)) || taskCommandsRef.current.some((item) => !item.rejected)) timer = setTimeout(poll, 2000);
      } catch (err) {
        if (controller.signal.aborted) return;
        setPollError(err instanceof Error ? err.message : "History refresh failed.");
        const transient = err instanceof TypeError || (err instanceof RequestError && [429, 502, 503, 504].includes(err.status));
        if (transient && failures < 3) timer = setTimeout(poll, 2000 * 2 ** failures++);
      }
    };
    timer = setTimeout(poll, 1000);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [taskPolling, conversationBusy, visible, refresh]);

  async function refreshHistory() {
    setError(""); setPollError(""); setLoading(true);
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const data = await refresh(controller.signal);
      if (!controller.signal.aborted) {
        for (const item of pendingRef.current) if (!acknowledged(data, item.id)) void submit(item);
        for (const item of taskCommandsRef.current) if (!item.rejected && !commandApplied(item, data.runs)) void submitTaskCommand(item);
      }
    }
    catch (err) { if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "History unavailable."); }
    finally { controllers.current.delete(controller); if (!controller.signal.aborted) setLoading(false); }
  }

  async function loadOlder() {
    if (!session?.next_cursor || loadingOlder) return;
    setLoadingOlder(true);
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const data = await post<Session>("session", { before: session.next_cursor }, controller.signal, agentMode);
      if (!controller.signal.aborted) setSession((current) => current ? { ...current,
        messages: mergeById(data.messages, current.messages).sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)),
        runs: mergeRuns(data.runs, current.runs),
        traces: mergeById(data.traces ?? [], current.traces ?? []), next_cursor: data.next_cursor,
      } : data);
    } catch (err) { if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "Older history unavailable."); }
    finally { controllers.current.delete(controller); if (!controller.signal.aborted) setLoadingOlder(false); }
  }

  async function send() {
    const text = draftValue.current.trim();
    if (!text || !session || loading || session.conversation?.introducing) return;
    if (new TextEncoder().encode(text).length > 8192) { setError("Please keep the message within 8,192 UTF-8 bytes."); return; }
    if (pendingRef.current.length >= MAX_PENDING) { setError("Reconnect or retry your pending messages before sending more."); return; }
    const item = { id: crypto.randomUUID(), text, started: Date.now() };
    beginTiming(item);
    Sentry.metrics.count("nori.submissions", 1, { attributes: { mode: agentMode ? "local" : "hosted" } });
    Sentry.addBreadcrumb({ category: "nori", message: "turn submitted" });
    updatePending((current) => [...current, item]);
    setDraft(""); setError("");
    await submit(item);
  }

  async function commandTask(runId: string, kind: TaskCommand["kind"], text?: string, questionId?: string) {
    if (taskCommandsRef.current.some((item) => item.command.run_id === runId && (kind !== "stop" || item.command.kind === "stop"))) return;
    if (text !== undefined && (!text.trim() || new TextEncoder().encode(text.trim()).length > 8192)) return;
    const command: TaskCommand = { id: crypto.randomUUID(), run_id: runId, kind,
      ...(text !== undefined ? { text: text.trim() } : {}), ...(questionId ? { question_id: questionId } : {}) };
    const item: PendingTaskCommand = { command, started: Date.now() };
    updateTaskCommands((current) => [...current, item]);
    if (text !== undefined) setTaskDrafts((current) => ({ ...current, [runId]: "" }));
    await submitTaskCommand(item);
  }

  function dismissTaskCommand(id: string) {
    updateTaskCommands((current) => current.filter((item) => item.command.id !== id || !item.rejected));
  }

  return { session, draft, setDraft, error: error || pollError, loading, loadingOlder, pending, selectedId, setSelectedId,
    activeTasks, selected, tasksUnavailable, busy, conversationBusy, taskCommands, taskDrafts,
    setTaskDraft: (id: string, text: string) => setTaskDrafts((current) => ({ ...current, [id]: text })),
    refreshHistory, loadOlder, send, retry: submit, commandTask, retryTaskCommand: submitTaskCommand, dismissTaskCommand };
}
