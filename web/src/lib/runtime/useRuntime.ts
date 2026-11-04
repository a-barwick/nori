"use client";

import { taskControlKinds } from "../types";

import { useCallback, useEffect, useRef, useState } from "react";
import * as Sentry from "@sentry/nextjs";
import type { Span } from "@sentry/nextjs";
import type { Attachment, DraftFile, Message, PendingTaskCommand, ReadPosition, Run, SavedResult, Session, TaskCommand, TaskCommandResponse } from "@/lib/types";

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

/** `GET /api/v1/snapshot`: the state to start from, and the event log position to follow the stream from. */
async function snapshot(before: string | undefined, signal: AbortSignal | undefined, agentMode: boolean): Promise<Session> {
  const query = before ? `?before=${encodeURIComponent(before)}` : "";
  const response = await fetch(`${agentMode ? "/api/agent" : "/api"}/v1/snapshot${query}`, { cache: "no-store", signal });
  const data = await response.json().catch(() => ({ error: "Nori returned an unreadable response." }));
  if (!response.ok) throw new RequestError(data.error || `Request failed (${response.status}).`, response.status, data);
  return data as Session;
}

// With nothing running, a visible tab drops its stream after this long, so an open tab doesn't hold the service awake.
const STREAM_IDLE_MS = 5 * 60_000;
type Presence = { conversation: NonNullable<Session["conversation"]>; paused: boolean };
type Pending = { id: string; text: string; started: number; error?: string; attachments?: Attachment[] };

const MAX_FILE_BYTES = 10 * 1024 * 1024;
const MAX_FILES = 10;
async function uploadFile(item: DraftFile, agentMode: boolean): Promise<Attachment> {
  const query = new URLSearchParams({ id: item.id, filename: item.name });
  const response = await fetch(`${agentMode ? "/api/agent" : "/api"}/v1/uploads?${query}`, {
    method: "POST", body: item.file, cache: "no-store", headers: { "Content-Type": item.file.type || "application/octet-stream" },
  });
  const data = await response.json().catch(() => ({ error: "Nori returned an unreadable response." }));
  if (!response.ok) throw new Error(data.error || `Upload failed (${response.status}).`);
  return data as Attachment;
}
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
      new TextEncoder().encode(item.text).length <= 8192 && typeof item.started === "number" && (item.attachments === undefined || Array.isArray(item.attachments))).slice(0, MAX_PENDING);
  } catch { return []; }
}
const acknowledged = (data: Session, id: string) => data.conversation?.input_ids.includes(id) || data.messages.some((message) => message.id === `${id}-user`);
function mergeById<T extends { id: string }>(older: T[], newer: T[]): T[] {
  return [...new Map([...older, ...newer].map((item) => [item.id, item])).values()];
}

// A snapshot or POST response can arrive after a newer read-position event.
function mergeReadPosition(older: ReadPosition | undefined, newer: ReadPosition | undefined) {
  return !older || (newer && newer.position > older.position) ? newer : older;
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
  const [draftFiles, setDraftFiles] = useState<DraftFile[]>([]);
  const draftValue = useRef("");
  const setDraft = (text: string) => { draftValue.current = text; setDraftState(text); };
  const [error, setError] = useState("");
  const [streamError, setStreamError] = useState("");
  const [streamAwake, setStreamAwake] = useState(true);
  const position = useRef(0);
  const wasConversationBusy = useRef(false);
  const [loading, setLoading] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [pending, setPending] = useState<Pending[]>(restorePending);
  const pendingRef = useRef(pending);
  const [taskCommands, setTaskCommands] = useState<PendingTaskCommand[]>(restoreTaskCommands);
  const taskCommandsRef = useRef(taskCommands);
  const [taskDrafts, setTaskDrafts] = useState<Record<string, string>>({});
  const [selectedId, setSelectedId] = useState<string>();
  const [visible, setVisible] = useState(true);
  const [shownMessageID, setShownMessageID] = useState("");
  const [readRetry, setReadRetry] = useState(0);
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
  const taskActive = activeTasks.some((run) => run.cancel_requested) || (activeTasks.length > 0 && !tasksUnavailable) || taskCommands.some((item) => !item.rejected);
  const busy = taskActive || conversationBusy || pending.some((item) => !item.error);
  const started = !!session;

  const updatePending = useCallback((update: (current: Pending[]) => Pending[]) => {
    const next = update(pendingRef.current);
    pendingRef.current = next;
    setPending(next);
    try {
      if (next.length) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next.map(({ id, text, started, attachments }) => ({ id, text, started, attachments }))));
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
    position.current = Math.max(position.current, data.position ?? 0);
    setSession((current) => ({ ...data,
      messages: mergeById(current?.messages ?? [], data.messages).sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)),
      runs: mergeRuns(current?.runs ?? [], data.runs),
      traces: mergeById(current?.traces ?? [], data.traces ?? []),
      read_position: mergeReadPosition(current?.read_position, data.read_position),
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
    const data = await snapshot(undefined, signal, agentMode);
    if (!signal?.aborted) { applySnapshot(data, serial); setStreamError(""); }
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
      const data = await post<Session>("turn", { id: item.id, message: item.text, attachments: item.attachments?.map((file) => file.id) }, controller.signal, agentMode, replyTimings.current.get(item.id)?.span);
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
      if (!document.hidden) setStreamAwake(true);
      if (!document.hidden) void refresh().then((data) => {
        setError("");
        for (const item of pendingRef.current) if (!acknowledged(data, item.id)) void submit(item);
        for (const item of taskCommandsRef.current) if (!item.rejected && !commandApplied(item, data.runs)) void submitTaskCommand(item);
      }).catch((err) => setError(err.message));
    };
    const wake = () => setStreamAwake(true);
    document.addEventListener("visibilitychange", reconnect);
    window.addEventListener("online", reconnect);
    window.addEventListener("focus", wake);
    return () => {
      for (const active of currentControllers) active.abort();
      currentControllers.clear();
      for (const timing of currentTimings.values()) { clearTimeout(timing.timer); timing.span.setAttribute("nori.outcome", "unobserved"); timing.span.end(); }
      currentTimings.clear();
      document.removeEventListener("visibilitychange", reconnect);
      window.removeEventListener("online", reconnect);
      window.removeEventListener("focus", wake);
    };
  }, [refresh, submit, submitTaskCommand, beginTiming, finishTiming]);

  useEffect(() => {
    if (busy) { setStreamAwake(true); return; }
    const timer = setTimeout(() => setStreamAwake(false), STREAM_IDLE_MS);
    return () => clearTimeout(timer);
  }, [busy, streamAwake]);

  // A browser that shows a reply reports its results as delivered. Only a visible tab counts.
  const reported = useRef(new Set<string>());
  useEffect(() => {
    if (!session || !visible) return;
    const shown = new Set(session.messages.flatMap((message) => message.result_ids ?? []));
    const ids = (session.results ?? []).filter((result) => shown.has(result.id) && !result.delivered_at && !reported.current.has(result.id)).map((result) => result.id);
    if (!ids.length) return;
    for (const id of ids) reported.current.add(id);
    void post("delivered", { ids }, undefined, agentMode).catch(() => { for (const id of ids) reported.current.delete(id); });
  }, [session, visible, agentMode]);

  // Thread reports replies that entered its viewport. Keep the newest one pending across failed requests; retries
  // use the same message and the service's mark only moves forward.
  const markRead = useCallback((id: string) => {
    const messages = session?.messages ?? [];
    setShownMessageID((current) => messages.findIndex((message) => message.id === id) > messages.findIndex((message) => message.id === current) ? id : current);
  }, [session?.messages]);
  useEffect(() => {
    if (!visible || document.hidden || !navigator.onLine || !shownMessageID) return;
    const messages = session?.messages ?? [];
    const shownIndex = messages.findIndex((message) => message.id === shownMessageID && message.role === "assistant");
    if (shownIndex < 0) return;
    const read = session?.read_position;
    const readIndex = messages.findIndex((message) => message.id === read?.message_id);
    if (readIndex >= shownIndex || (readIndex < 0 && read?.message_id && Date.parse(read.through) >= Date.parse(messages[shownIndex].created_at))) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    void post<{ read_position: ReadPosition }>("seen", { message_id: shownMessageID }, controller.signal, agentMode).then((data) => {
      if (!controller.signal.aborted) setSession((current) => current && { ...current, read_position: mergeReadPosition(current.read_position, data.read_position) });
    }).catch(() => {
      if (!controller.signal.aborted) timer = setTimeout(() => setReadRetry((attempt) => attempt + 1), 5000);
    });
    return () => { controller.abort(); clearTimeout(timer); };
  }, [session?.messages, session?.read_position, shownMessageID, visible, agentMode, readRetry]);

  // Follow the event log while the tab is visible. The browser reconnects by itself with Last-Event-ID after a stream
  // ends. It gives up only when a response isn't a stream, and then this reopens it from the last event.
  useEffect(() => {
    if (!started || !visible || !streamAwake) return;
    let source: EventSource;
    let timer: ReturnType<typeof setTimeout>;
    let failures = 0;
    let stopped = false;
    const applyPresence = (presence: Presence) => {
      const { conversation } = presence;
      if (conversation.revision < appliedRevision.current) return;
      appliedRevision.current = conversation.revision;
      setSession((current) => current && { ...current, conversation, paused: presence.paused });
      updatePending((current) => current.filter((item) => !conversation.input_ids.includes(item.id)));
      const now = conversation.pending || conversation.processing;
      // A finished turn's traces are in the snapshot. Read it once at the end of a turn, and never on a reconnect.
      if (wasConversationBusy.current && !now) void refresh().catch(() => undefined);
      wasConversationBusy.current = now;
    };
    const open = () => {
      source = new EventSource(`${agentMode ? "/api/agent" : "/api"}/v1/events?after=${position.current}`);
      const listen = <T,>(kind: string, apply: (value: T) => void) => source.addEventListener(kind, (event) => {
        const message = event as MessageEvent<string>;
        apply(JSON.parse(message.data) as T);
        if (message.lastEventId) position.current = Number(message.lastEventId);
        failures = 0;
      });
      source.onopen = () => setStreamError("");
      listen<{ message: Message }>("message", ({ message }) => {
        setSession((current) => current && { ...current, messages: mergeById(current.messages, [message]).sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)) });
        updatePending((current) => current.filter((item) => message.id !== `${item.id}-user`));
      });
      listen<{ run: Run }>("task", ({ run }) => {
        setSession((current) => current && { ...current, runs: mergeRuns(current.runs, [run]) });
        updateTaskCommands((current) => current.filter((item) => !commandApplied(item, [run])));
      });
      listen<{ result: SavedResult }>("result", ({ result }) => {
        setSession((current) => current && { ...current, results: mergeById(current.results ?? [], [result]).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)) });
      });
      listen<{ read_position: ReadPosition }>("read_position", ({ read_position }) => {
        setSession((current) => current && { ...current, read_position: mergeReadPosition(current.read_position, read_position) });
      });
      listen<Presence>("conversation", applyPresence);
      listen<Presence>("presence", applyPresence);
      listen<unknown>("unknown", () => undefined);
      // The log can't resume from this position, so start again from a snapshot.
      listen<{ position: number }>("reset", (value) => {
        position.current = value.position;
        source.close();
        const reload = () => refresh().then(() => { if (!stopped) open(); }, () => { if (!stopped) timer = setTimeout(reload, 5000); });
        void reload();
      });
      source.onerror = () => {
        if (source.readyState !== EventSource.CLOSED) return;
        failures++;
        if (failures >= 3) setStreamError("Live updates are reconnecting.");
        timer = setTimeout(open, Math.min(30_000, 1000 * 2 ** failures));
      };
    };
    open();
    return () => { stopped = true; source.close(); clearTimeout(timer); };
  }, [started, visible, streamAwake, agentMode, refresh, updatePending, updateTaskCommands]);

  async function refreshHistory() {
    setError(""); setStreamError(""); setLoading(true);
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
      const data = await snapshot(session.next_cursor, controller.signal, agentMode);
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
    const ready = draftFiles.filter((file) => file.status === "ready");
    if ((!text && !ready.length) || draftFiles.some((file) => file.status !== "ready") || !session || loading || session.conversation?.introducing) return;
    if (new TextEncoder().encode(text).length > 8192) { setError("Please keep the message within 8,192 UTF-8 bytes."); return; }
    if (pendingRef.current.length >= MAX_PENDING) { setError("Reconnect or retry your pending messages before sending more."); return; }
    const item: Pending = { id: crypto.randomUUID(), text, started: Date.now(), ...(ready.length ? { attachments: ready.map(({ id, name, media_type, size }) => ({ id, name, media_type, size })) } : {}) };
    beginTiming(item);
    Sentry.metrics.count("nori.submissions", 1, { attributes: { mode: agentMode ? "local" : "hosted" } });
    Sentry.addBreadcrumb({ category: "nori", message: "turn submitted" });
    updatePending((current) => [...current, item]);
    setDraft(""); setError(""); setDraftFiles([]);
    await submit(item);
  }

  // A retry keeps the same ID, so a file that did reach the service isn't stored twice.
  const sendFile = useCallback(async (item: DraftFile) => {
    setDraftFiles((current) => current.map((file) => file.id === item.id ? { ...file, status: "uploading", error: undefined } : file));
    try {
      const saved = await uploadFile(item, agentMode);
      setDraftFiles((current) => current.map((file) => file.id === item.id ? { ...file, ...saved, status: "ready" } : file));
    } catch (err) {
      setDraftFiles((current) => current.map((file) => file.id === item.id ? { ...file, status: "error", error: err instanceof Error ? err.message : "Upload failed. Try again." } : file));
    }
  }, [agentMode]);

  function attachFiles(files: File[]) {
    const room = MAX_FILES - draftFiles.length;
    if (files.length > room) setError(`A message can have at most ${MAX_FILES} files.`);
    for (const file of files.slice(0, Math.max(0, room))) {
      const item: DraftFile = { id: crypto.randomUUID(), name: file.name, media_type: file.type, size: file.size, status: "uploading", file };
      if (file.size > MAX_FILE_BYTES) { setDraftFiles((current) => [...current, { ...item, status: "error", error: "A file can be at most 10 MB." }]); continue; }
      setDraftFiles((current) => [...current, item]);
      void sendFile(item);
    }
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

  return { session, draft, setDraft, draftFiles, attachFiles, retryFile: sendFile,
    removeFile: (id: string) => setDraftFiles((current) => current.filter((file) => file.id !== id)), error: error || streamError, loading, loadingOlder, pending, selectedId, setSelectedId,
    activeTasks, selected, tasksUnavailable, busy, conversationBusy, taskCommands, taskDrafts,
    setTaskDraft: (id: string, text: string) => setTaskDrafts((current) => ({ ...current, [id]: text })),
    refreshHistory, loadOlder, send, retry: submit, commandTask, retryTaskCommand: submitTaskCommand, dismissTaskCommand, markRead };
}
