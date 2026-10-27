"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { Run, Session } from "@/lib/poc/types";

async function post<T>(operation: string, payload: object, signal?: AbortSignal, agentMode = false): Promise<T> {
  const response = await fetch(`${agentMode ? "/api/agent" : "/api"}/${operation}`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload), cache: "no-store", signal,
  });
  const data = await response.json().catch(() => ({ error: "Nori returned an unreadable response." }));
  if (!response.ok) throw new Error(data.error || `Request failed (${response.status}).`);
  return data as T;
}

type Pending = { id: string; text: string; started: number; error?: string };
const STORAGE_KEY = "nori-pending-turns-v1";
const MAX_PENDING = 20;
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

export function useRuntime(agentMode = false) {
  const [session, setSession] = useState<Session>();
  const [draft, setDraftState] = useState("");
  const draftValue = useRef("");
  const setDraft = (text: string) => { draftValue.current = text; setDraftState(text); };
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [pending, setPending] = useState<Pending[]>(restorePending);
  const pendingRef = useRef(pending);
  const [stopPending, setStopPending] = useState(false);
  const [stopIntent, setStopIntent] = useState<string>();
  const [selectedId, setSelectedId] = useState<string>();
  const [visible, setVisible] = useState(true);
  const controllers = useRef(new Set<AbortController>());
  const submissions = useRef(new Set<string>());
  const taskUpdates = useRef(new Map<string, { id: string; message: string }>());
  const requestSerial = useRef(0);
  const appliedSerial = useRef(0);
  const appliedRevision = useRef(-1);
  const cancellationInFlight = useRef(false);
  const running = session?.runs.find((run) => run.status === "running");
  const activeId = running?.id;
  const selected = session?.runs.find((run) => run.id === selectedId) || session?.runs[0];
  const tasksUnavailable = !!session?.paused;
  const conversationBusy = !!(session?.conversation?.pending || session?.conversation?.processing);
  const taskPolling = !!running && !tasksUnavailable;
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

  const applySnapshot = useCallback((data: Session, serial: number) => {
    const revision = data.conversation?.revision ?? 0;
    if (revision < appliedRevision.current || (revision === appliedRevision.current && serial < appliedSerial.current)) return;
    appliedSerial.current = serial;
    appliedRevision.current = revision;
    setSession((current) => ({ ...data,
      messages: mergeById(current?.messages ?? [], data.messages).sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)),
      traces: mergeById(current?.traces ?? [], data.traces ?? []),
      // Preserve pages already loaded while refreshing the newest page.
      next_cursor: current ? current.next_cursor : data.next_cursor,
    }));
    updatePending((current) => current.filter((item) => !acknowledged(data, item.id)));
  }, [updatePending]);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const serial = ++requestSerial.current;
    const data = await post<Session>("session", {}, signal, agentMode);
    if (!signal?.aborted) applySnapshot(data, serial);
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
      const data = await post<Session>("turn", { id: item.id, message: item.text }, controller.signal, agentMode);
      if (!controller.signal.aborted) { applySnapshot(data, serial); setError(""); }
    } catch (err) {
      if (!controller.signal.aborted) {
        // A dropped acceptance response is ambiguous. Keep the same UUID for retry.
        updatePending((current) => current.map((entry) => entry.id === item.id ? { ...entry, error: err instanceof Error ? err.message : "Message acceptance could not be confirmed." } : entry));
        await refresh(controller.signal).catch(() => undefined);
      }
    } finally { controllers.current.delete(controller); submissions.current.delete(item.id); }
  }, [agentMode, applySnapshot, refresh, updatePending]);

  useEffect(() => {
    const currentControllers = controllers.current;
    const controller = new AbortController();
    currentControllers.add(controller);
    void refresh(controller.signal).then((data) => {
      if (!controller.signal.aborted) {
        for (const item of pendingRef.current) if (!acknowledged(data, item.id)) void submit(item);
      }
    }).catch((err) => { if (!controller.signal.aborted) setError(err.message); })
      .finally(() => { currentControllers.delete(controller); if (!controller.signal.aborted) setLoading(false); });
    const reconnect = () => {
      setVisible(!document.hidden);
      if (!document.hidden) void refresh().then((data) => {
        setError("");
        for (const item of pendingRef.current) if (!acknowledged(data, item.id)) void submit(item);
      }).catch((err) => setError(err.message));
    };
    document.addEventListener("visibilitychange", reconnect);
    window.addEventListener("online", reconnect);
    return () => {
      for (const active of currentControllers) active.abort();
      currentControllers.clear();
      document.removeEventListener("visibilitychange", reconnect);
      window.removeEventListener("online", reconnect);
    };
  }, [refresh, submit]);

  useEffect(() => {
    // Task pause does not pause the conversation. No continuous idle polling.
    if ((!taskPolling && !conversationBusy) || error || !visible) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const next = await refresh(controller.signal);
        if (!controller.signal.aborted && (next.conversation?.pending || next.conversation?.processing || (!next.paused && next.runs.some((run) => run.status === "running")))) timer = setTimeout(poll, 2000);
      } catch (err) { if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "History refresh failed."); }
    };
    timer = setTimeout(poll, 1000);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [taskPolling, conversationBusy, error, visible, refresh]);

  async function refreshHistory() {
    setError(""); setLoading(true);
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const data = await refresh(controller.signal);
      if (!controller.signal.aborted) for (const item of pendingRef.current) if (!acknowledged(data, item.id)) void submit(item);
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
        runs: mergeById(data.runs, current.runs),
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
    updatePending((current) => [...current, item]);
    setDraft(""); setError("");
    await submit(item);
  }

  async function updateTask(runId: string, message: string) {
    const text = message.trim();
    if (!text) return;
    const previous = taskUpdates.current.get(runId);
    const update = previous?.message === text ? previous : { id: crypto.randomUUID(), message: text };
    taskUpdates.current.set(runId, update);
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      await post<{ run: Run }>("input", { ...update, run_id: runId }, controller.signal, agentMode);
      await refresh(controller.signal);
      if (taskUpdates.current.get(runId) === update) taskUpdates.current.delete(runId);
    } finally { controllers.current.delete(controller); }
  }

  async function requestCancellation(id: string) {
    if (cancellationInFlight.current) return;
    cancellationInFlight.current = true; setStopPending(true); setError(""); setStopIntent(id);
    const controller = new AbortController(); controllers.current.add(controller);
    try {
      const { run } = await post<{ run: Run }>("cancel", { id }, controller.signal, agentMode);
      if (!controller.signal.aborted) setSession((current) => current ? { ...current, runs: [run, ...current.runs.filter((item) => item.id !== run.id)] } : current);
    } catch (err) { if (!controller.signal.aborted) { setStopIntent(undefined); setError(err instanceof Error ? err.message : "Stop could not be confirmed."); } }
    finally { controllers.current.delete(controller); cancellationInFlight.current = false; if (!controller.signal.aborted) setStopPending(false); }
  }

  return { session, draft, setDraft, error, loading, loadingOlder, pending, stopPending, stopIntent, selectedId, setSelectedId,
    running, activeId, selected, tasksUnavailable, busy, conversationBusy,
    canUpdate: false, canChat: true, refreshHistory, loadOlder, send, retry: submit,
    stop: () => { if (running && !running.cancel_requested) void requestCancellation(running.id); }, updateTask };
}
