"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import * as Sentry from "@sentry/nextjs";
import type { Span } from "@sentry/nextjs";
import type { Attachment, DraftFile, Message, PendingTaskCommand, ReadPosition, Run, SavedResult, Session, TaskCommand, TaskCommandResponse } from "@/lib/types";
import { taskControlKinds } from "@/lib/types";
import { hasSubmission, messageTooLong } from "@/lib/submission";
import { useDraftFiles, type UploadContext } from "@/lib/useDraftFiles";
import { runtimePath } from "./paths";

class RequestError extends Error {
  constructor(message: string, readonly status: number, readonly details?: TaskCommandResponse) { super(message); }
}

async function readJSON<T>(response: Response, failure = "Request failed"): Promise<T> {
  const data = await response.json().catch(() => null);
  if (data === null) throw new RequestError("Nori returned an unreadable response.", response.status);
  if (!response.ok) throw new RequestError(data.error || `${failure} (${response.status}).`, response.status, data);
  return data as T;
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
  const response = await fetch(runtimePath(agentMode, operation), {
    method: "POST", headers, body: JSON.stringify(payload), cache: "no-store", signal,
  });
  return readJSON<T>(response);
}

async function snapshot(before: string | undefined, signal: AbortSignal | undefined, agentMode: boolean): Promise<Session> {
  const query = before ? `?before=${encodeURIComponent(before)}` : "";
  const response = await fetch(runtimePath(agentMode, `v1/snapshot${query}`), { cache: "no-store", signal });
  return readJSON<Session>(response);
}

// With nothing running, a visible tab drops its stream after this long, so an open tab doesn't hold the service awake.
const STREAM_IDLE_MS = 5 * 60_000;
type Presence = { conversation: NonNullable<Session["conversation"]>; paused: boolean };
type Pending = { id: string; text: string; started: number; error?: string; attachments?: Attachment[]; retryManually?: boolean };
const TURN_ACCEPTANCE_UNKNOWN = "Message acceptance could not be confirmed.";
const COMMAND_ACCEPTANCE_UNKNOWN = "Task command acceptance could not be confirmed.";

/** Uploads with progress and cancel (XMLHttpRequest reports upload progress; fetch does not). */
function uploadFile(item: DraftFile, agentMode: boolean, { signal, progress }: UploadContext): Promise<Attachment> {
  const query = new URLSearchParams({ id: item.id, filename: item.name });
  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open("POST", runtimePath(agentMode, `v1/uploads?${query}`));
    request.setRequestHeader("Content-Type", item.file.type || "application/octet-stream");
    request.upload.onprogress = (event) => { if (event.lengthComputable) progress(event.loaded / event.total); };
    request.onload = () => {
      let data: { error?: string } & Partial<Attachment> | null = null;
      try { data = JSON.parse(request.responseText); } catch { /* handled below */ }
      if (request.status >= 200 && request.status < 300 && data) resolve(data as Attachment);
      else reject(new Error(data?.error || (request.status ? `Upload failed (${request.status}).` : "Upload failed. Try again.")));
    };
    request.onerror = () => reject(new Error("The upload was interrupted. Retry this file."));
    request.onabort = () => reject(new DOMException("Upload cancelled", "AbortError"));
    signal.addEventListener("abort", () => request.abort(), { once: true });
    request.send(item.file);
  });
}
type ReplyTiming = { started: number; span: Span; timer: ReturnType<typeof setTimeout> };
const STORAGE_KEY = "nori-pending-turns-v1";
const DRAFT_KEY = "nori-draft-assistant-v1";
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
      (item.command.text === undefined || (typeof item.command.text === "string" && !messageTooLong(item.command.text))) &&
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
      !messageTooLong(item.text) && typeof item.started === "number" && (item.attachments === undefined || Array.isArray(item.attachments)))
      .slice(0, MAX_PENDING)
      .map((item) => item.retryManually ? { ...item, error: TURN_ACCEPTANCE_UNKNOWN } : item);
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
  // The unsent draft survives reloads and reconnects; it is cleared when the message is accepted for sending.
  const [draft, setDraftState] = useState(() => { try { return localStorage.getItem(DRAFT_KEY) ?? ""; } catch { return ""; } });
  const { files: draftFiles, attach, retry, clear: clearFiles, remove: removeFile, invalidateUploads } = useDraftFiles();
  const draftValue = useRef(draft);
  const setDraft = (text: string) => {
    draftValue.current = text; setDraftState(text);
    try { if (text) localStorage.setItem(DRAFT_KEY, text); else localStorage.removeItem(DRAFT_KEY); } catch { /* storage off */ }
  };
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
  const [shownMessageIDs, setShownMessageIDs] = useState<Set<string>>(() => new Set());
  const shownMessageID = session?.messages.findLast((message) => message.role === "assistant" && shownMessageIDs.has(message.id))?.id ?? "";
  const [deliveryRetry, setDeliveryRetry] = useState(0);
  const [readRetry, setReadRetry] = useState(0);
  const controllers = useRef(new Set<AbortController>());
  const submissions = useRef(new Set<string>());
  const replyTimings = useRef(new Map<string, ReplyTiming>());
  const commandSubmissions = useRef(new Set<string>());
  const requestSerial = useRef(0);
  const minimumSerial = useRef(0);
  const replacingSnapshot = useRef(false);
  const appliedSerial = useRef(0);
  const appliedRevision = useRef(-1);
  const activeTasks = session?.runs.filter((run) => run.status === "running") ?? [];
  const selected = session?.runs.find((run) => run.id === selectedId) || activeTasks[0] || session?.runs[0];
  const tasksUnavailable = !!session?.paused;
  const conversationBusy = !!(session?.conversation?.pending || session?.conversation?.processing);
  const taskActive = activeTasks.some((run) => run.cancel_requested) || (activeTasks.length > 0 && !tasksUnavailable) || taskCommands.some((item) => !item.rejected && !item.retryManually);
  const busy = taskActive || conversationBusy || pending.some((item) => !item.error);
  const started = !!session;

  const updatePending = useCallback((update: (current: Pending[]) => Pending[]) => {
    const next = update(pendingRef.current);
    pendingRef.current = next;
    setPending(next);
    try {
      if (next.length) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next.map(({ id, text, started, attachments, retryManually }) => ({ id, text, started, attachments, retryManually }))));
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
    if (serial < minimumSerial.current) return;
    const revision = data.conversation?.revision ?? 0;
    const replace = replacingSnapshot.current;
    if (!replace && (revision < appliedRevision.current || (revision === appliedRevision.current && serial < appliedSerial.current))) return;
    replacingSnapshot.current = false;
    appliedSerial.current = serial;
    appliedRevision.current = revision;
    position.current = replace ? data.position ?? position.current : Math.max(position.current, data.position ?? 0);
    setSession((current) => replace ? data : ({ ...data,
      messages: mergeById(current?.messages ?? [], data.messages).sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)),
      runs: mergeRuns(current?.runs ?? [], data.runs),
      traces: mergeById(current?.traces ?? [], data.traces ?? []),
      read_position: mergeReadPosition(current?.read_position, data.read_position),
      // Preserve pages already loaded while refreshing the newest page.
      next_cursor: current ? current.next_cursor : data.next_cursor,
    }));
    if (replace) setLoading(false);
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
    const data = await snapshot(undefined, signal, agentMode).catch((error) => {
      if (serial >= minimumSerial.current) throw error;
    });
    if (!data || signal?.aborted || serial < minimumSerial.current) return;
    applySnapshot(data, serial);
    setStreamError("");
    return data;
  }, [agentMode, applySnapshot]);

  const submit = useCallback(async (item: Pending) => {
    if (replacingSnapshot.current || submissions.current.has(item.id)) return;
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
        const rejected = err instanceof RequestError && err.status >= 400 && err.status < 500 && err.status !== 429;
        updatePending((current) => current.map((entry) => entry.id === item.id ? {
          ...entry, retryManually: entry.retryManually || rejected,
          error: err instanceof Error ? err.message : TURN_ACCEPTANCE_UNKNOWN,
        } : entry));
        await refresh(controller.signal).catch(() => undefined);
      }
    } finally { controllers.current.delete(controller); submissions.current.delete(item.id); }
  }, [agentMode, applySnapshot, refresh, updatePending]);

  const submitTaskCommand = useCallback(async (item: PendingTaskCommand) => {
    if (replacingSnapshot.current || commandSubmissions.current.has(item.command.id)) return;
    commandSubmissions.current.add(item.command.id);
    updateTaskCommands((current) => current.map((entry) => entry.command.id === item.command.id ? { ...entry, sending: true, error: undefined } : entry));
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const data = await post<TaskCommandResponse>("command", item.command, controller.signal, agentMode);
      if (!data.command || data.command.id !== item.command.id || data.run?.id !== item.command.run_id) throw new Error(COMMAND_ACCEPTANCE_UNKNOWN);
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
          error: receipt?.accepted ? "Accepted. Delivery to the task is still pending." : err instanceof Error ? err.message : COMMAND_ACCEPTANCE_UNKNOWN,
        } : entry));
      }
    } finally {
      commandSubmissions.current.delete(item.command.id);
      if (!controller.signal.aborted) await refresh(controller.signal).catch(() => undefined);
      controllers.current.delete(controller);
    }
  }, [agentMode, refresh, updateTaskCommands]);

  const refreshAndRetry = useCallback(async (signal: AbortSignal) => {
    const wasReplacing = replacingSnapshot.current;
    const epoch = minimumSerial.current;
    const data = await refresh(signal);
    if (!data || signal.aborted || wasReplacing || epoch !== minimumSerial.current) return;
    for (const item of pendingRef.current) if (!item.retryManually && !acknowledged(data, item.id)) void submit(item);
    for (const item of taskCommandsRef.current) if (!item.retryManually && !item.rejected && !commandApplied(item, data.runs)) void submitTaskCommand(item);
  }, [refresh, submit, submitTaskCommand]);

  useEffect(() => {
    const currentControllers = controllers.current;
    const currentTimings = replyTimings.current;
    for (const item of [...restoredTimings(), ...pendingRef.current]) beginTiming(item);
    const controller = new AbortController();
    void refreshAndRetry(controller.signal).catch((err) => { if (!controller.signal.aborted) setError(err.message); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    const reconnect = () => {
      setVisible(!document.hidden);
      if (document.hidden) return;
      setStreamAwake(true);
      setDeliveryRetry((attempt) => attempt + 1);
      void refreshAndRetry(controller.signal).then(() => { if (!controller.signal.aborted) setError(""); })
        .catch((err) => { if (!controller.signal.aborted) setError(err.message); });
    };
    const wake = () => setStreamAwake(true);
    document.addEventListener("visibilitychange", reconnect);
    window.addEventListener("online", reconnect);
    window.addEventListener("focus", wake);
    return () => {
      controller.abort();
      for (const active of currentControllers) active.abort();
      currentControllers.clear();
      for (const timing of currentTimings.values()) { clearTimeout(timing.timer); timing.span.setAttribute("nori.outcome", "unobserved"); timing.span.end(); }
      currentTimings.clear();
      document.removeEventListener("visibilitychange", reconnect);
      window.removeEventListener("online", reconnect);
      window.removeEventListener("focus", wake);
    };
  }, [refreshAndRetry, beginTiming]);

  useEffect(() => {
    if (busy) { setStreamAwake(true); return; }
    const timer = setTimeout(() => setStreamAwake(false), STREAM_IDLE_MS);
    return () => clearTimeout(timer);
  }, [busy, streamAwake]);

  // Remember every reply exposed in this tab, independently of the shared read position. Reading a newer
  // reply (here or elsewhere) does not prove that an older reply's results were shown.
  const markShown = useCallback((ids: string[]) => {
    if (document.hidden || replacingSnapshot.current) return;
    setShownMessageIDs((current) => ids.every((id) => current.has(id)) ? current : new Set([...current, ...ids]));
  }, []);

  const reported = useRef(new Set<string>());
  useEffect(() => {
    if (!session || !visible || document.hidden || !navigator.onLine || replacingSnapshot.current) return;
    const reports = reported.current;
    const shown = new Set(session.messages.filter((message) => message.role === "assistant" && shownMessageIDs.has(message.id)).flatMap((message) => message.result_ids ?? []));
    const ids = (session.results ?? []).filter((result) => shown.has(result.id) && !result.delivered_at && !reports.has(result.id)).map((result) => result.id);
    if (!ids.length) return;
    const controller = new AbortController();
    const requests = controllers.current;
    requests.add(controller);
    let delivered = false;
    let timer: ReturnType<typeof setTimeout>;
    for (const id of ids) reports.add(id);
    void post("delivered", { ids }, controller.signal, agentMode).then(() => {
      delivered = true;
    }).catch(() => {
      if (!controller.signal.aborted) timer = setTimeout(() => setDeliveryRetry((attempt) => attempt + 1), 5000);
    });
    return () => {
      controller.abort(); clearTimeout(timer);
      requests.delete(controller);
      if (!delivered) for (const id of ids) reports.delete(id);
    };
  }, [session, shownMessageIDs, visible, agentMode, deliveryRetry]);

  // The newest exposed reply advances the shared read position. Retries use the same message and the service's
  // mark only moves forward, including when another session has already read beyond this tab.
  useEffect(() => {
    if (!visible || document.hidden || !navigator.onLine || !shownMessageID || replacingSnapshot.current) return;
    const messages = session?.messages ?? [];
    const shownIndex = messages.findIndex((message) => message.id === shownMessageID && message.role === "assistant");
    if (shownIndex < 0) return;
    const read = session?.read_position;
    const readIndex = messages.findIndex((message) => message.id === read?.message_id);
    if (readIndex >= shownIndex || (readIndex < 0 && read?.message_id && Date.parse(read.through) >= Date.parse(messages[shownIndex].created_at))) return;
    const controller = new AbortController();
    const requests = controllers.current;
    requests.add(controller);
    let timer: ReturnType<typeof setTimeout>;
    void post<{ read_position: ReadPosition }>("seen", { message_id: shownMessageID }, controller.signal, agentMode).then((data) => {
      if (!controller.signal.aborted) setSession((current) => current && { ...current, read_position: mergeReadPosition(current.read_position, data.read_position) });
    }).catch(() => {
      if (!controller.signal.aborted) timer = setTimeout(() => setReadRetry((attempt) => attempt + 1), 5000);
    });
    return () => { controller.abort(); clearTimeout(timer); requests.delete(controller); };
  }, [session?.messages, session?.read_position, shownMessageID, visible, agentMode, readRetry]);

  // Follow the event log while the tab is visible. The browser reconnects by itself with Last-Event-ID after a stream
  // ends. It gives up only when a response isn't a stream, and then this reopens it from the last event.
  useEffect(() => {
    if (!started || !visible || !streamAwake) return;
    const controller = new AbortController();
    let source: EventSource | undefined;
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
      if (wasConversationBusy.current && !now) void refresh(controller.signal).catch(() => undefined);
      wasConversationBusy.current = now;
    };
    const reload = () => {
      void refresh(controller.signal).then(() => {
        if (!stopped) open();
      }, () => { if (!stopped) timer = setTimeout(reload, 5000); });
    };
    function open() {
      if (replacingSnapshot.current) { reload(); return; }
      let streamPosition = position.current;
      const stream = new EventSource(runtimePath(agentMode, `v1/events?after=${streamPosition}`));
      source = stream;
      const listen = <T,>(kind: string, apply: (value: T) => void) => stream.addEventListener(kind, (event) => {
        if (stream !== source || stopped) return;
        const message = event as MessageEvent<string>;
        apply(JSON.parse(message.data) as T);
        if (kind !== "reset" && message.lastEventId) {
          streamPosition = Math.max(streamPosition, Number(message.lastEventId));
          position.current = Math.max(position.current, streamPosition);
        }
        failures = 0;
      });
      stream.onopen = () => { if (!stopped && stream === source) setStreamError(""); };
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
        const rewind = value.position < streamPosition;
        if (rewind) {
          // After a store rewind, a higher revision or command position may belong to the retired history.
          minimumSerial.current = requestSerial.current + 1;
          replacingSnapshot.current = true;
          wasConversationBusy.current = false;
          for (const request of controllers.current) request.abort();
          updatePending((items) => items.map((item) => ({ ...item, retryManually: true, error: TURN_ACCEPTANCE_UNKNOWN })));
          updateTaskCommands((items) => items.map((item) => item.rejected ? item : {
            ...item, sending: false, accepted: false, position: undefined, retryManually: true, error: COMMAND_ACCEPTANCE_UNKNOWN,
          }));
          reported.current = new Set();
          setShownMessageIDs(new Set());
          invalidateUploads();
          setLoadingOlder(false);
          setLoading(true);
        }
        position.current = rewind ? value.position : Math.max(position.current, value.position);
        source = undefined;
        stream.close();
        reload();
      });
      stream.onerror = () => {
        if (stream !== source || stopped || stream.readyState !== EventSource.CLOSED) return;
        failures++;
        if (failures >= 3) setStreamError("Live updates are reconnecting.");
        timer = setTimeout(open, Math.min(30_000, 1000 * 2 ** failures));
      };
    }
    open();
    return () => { stopped = true; controller.abort(); source?.close(); clearTimeout(timer); };
  }, [started, visible, streamAwake, agentMode, refresh, updatePending, updateTaskCommands, invalidateUploads]);

  async function refreshHistory() {
    setError(""); setStreamError(""); setLoading(true);
    const controller = new AbortController(); controllers.current.add(controller);
    try { await refreshAndRetry(controller.signal); }
    catch (err) { if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "History unavailable."); }
    finally { controllers.current.delete(controller); if (!controller.signal.aborted) setLoading(false); }
  }

  async function loadOlder() {
    if (replacingSnapshot.current || !session?.next_cursor || loadingOlder) return;
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
    if (replacingSnapshot.current || !hasSubmission(text, draftFiles) || !session || loading || session.conversation?.introducing) return;
    if (messageTooLong(text)) { setError("Please keep the message within 8,192 UTF-8 bytes."); return; }
    if (pendingRef.current.length >= MAX_PENDING) { setError("Reconnect or retry your pending messages before sending more."); return; }
    const item: Pending = { id: crypto.randomUUID(), text, started: Date.now(), ...(draftFiles.length ? { attachments: draftFiles.map(({ id, name, media_type, size }) => ({ id, name, media_type, size })) } : {}) };
    beginTiming(item);
    Sentry.metrics.count("nori.submissions", 1, { attributes: { mode: agentMode ? "local" : "hosted" } });
    Sentry.addBreadcrumb({ category: "nori", message: "turn submitted" });
    updatePending((current) => [...current, item]);
    setDraft(""); setError(""); clearFiles();
    await submit(item);
  }

  const upload = (item: DraftFile, context: UploadContext) => uploadFile(item, agentMode, context);

  async function commandTask(runId: string, kind: TaskCommand["kind"], text?: string, questionId?: string) {
    if (replacingSnapshot.current) return;
    if (taskCommandsRef.current.some((item) => item.command.run_id === runId && (kind !== "stop" || item.command.kind === "stop"))) return;
    if (text !== undefined && (!text.trim() || messageTooLong(text.trim()))) return;
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

  function dismissPending(id: string) {
    if (submissions.current.has(id)) return;
    updatePending((current) => current.filter((item) => item.id !== id || !item.error));
    finishTiming(id, "unobserved");
  }

  return { session, draft, setDraft, draftFiles,
    attachFiles: (files: File[]) => attach(files, upload, setError), retryFile: (item: DraftFile) => retry(item, upload),
    removeFile, error: error || streamError, loading, loadingOlder, pending, setSelectedId,
    activeTasks, selected, tasksUnavailable, busy, conversationBusy, taskCommands, taskDrafts,
    setTaskDraft: (id: string, text: string) => setTaskDrafts((current) => ({ ...current, [id]: text })),
    refreshHistory, loadOlder, send, retry: submit, dismissPending, commandTask, retryTaskCommand: submitTaskCommand, dismissTaskCommand, markShown };
}
