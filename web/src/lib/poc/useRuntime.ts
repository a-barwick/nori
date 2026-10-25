"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { Run, Session } from "@/lib/poc/types";

async function post<T>(
  operation: string,
  payload: object,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(`/api/${operation}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
    cache: "no-store",
    signal,
  });
  const data = await response
    .json()
    .catch(() => ({ error: "Nori returned an unreadable response." }));
  if (!response.ok)
    throw new Error(data.error || `Request failed (${response.status}).`);
  return data as T;
}

export function useRuntime() {
  const [session, setSession] = useState<Session>();
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState<{
    id: string;
    runId: string;
    text: string;
    started: number;
  }>();
  const [stopPending, setStopPending] = useState(false);
  const [stopIntent, setStopIntent] = useState<string>();
  const [selectedId, setSelectedId] = useState<string>();
  const [visible, setVisible] = useState(true);
  const [expired, setExpired] = useState(false);
  const controllers = useRef(new Set<AbortController>());
  const cancellationInFlight = useRef(false);
  const running = session?.runs.find((run) => run.status === "running");
  const activeId = running?.id || pending?.runId;
  const selected =
    session?.runs.find((run) => run.id === selectedId) ||
    (!pending ? session?.runs[0] : undefined);
  const unavailable = !!session?.paused || expired;
  const busy = !!activeId;
  const canUpdate =
    !!running?.temporal &&
    !running.cancel_requested &&
    !pending &&
    !stopPending &&
    stopIntent !== running.id;

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const data = await post<Session>("session", {}, signal);
    setSession(data);
    setExpired(!!data.expires_at && Date.parse(data.expires_at) <= Date.now());
    return data;
  }, []);

  const requestCancellation = useCallback(async (id: string) => {
    if (cancellationInFlight.current) return;
    cancellationInFlight.current = true;
    const controller = new AbortController();
    controllers.current.add(controller);
    setStopPending(true);
    try {
      const { run } = await post<{ run: Run }>(
        "cancel",
        { id },
        controller.signal,
      );
      if (controller.signal.aborted) return;
      setSession((current) =>
        current
          ? {
              ...current,
              runs: [run, ...current.runs.filter((r) => r.id !== run.id)],
            }
          : current,
      );
      setError("");
    } catch (err) {
      if (!controller.signal.aborted)
        setError(
          err instanceof Error ? err.message : "Stop could not be confirmed.",
        );
    } finally {
      controllers.current.delete(controller);
      cancellationInFlight.current = false;
      if (!controller.signal.aborted) setStopPending(false);
    }
  }, []);

  useEffect(() => {
    const currentControllers = controllers.current;
    const controller = new AbortController();
    currentControllers.add(controller);
    void post<Session>("session", {}, controller.signal)
      .then((data) => {
        if (!controller.signal.aborted) {
          setSession(data);
          setExpired(
            !!data.expires_at && Date.parse(data.expires_at) <= Date.now(),
          );
        }
      })
      .catch((err) => {
        if (!controller.signal.aborted) setError(err.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    const onVisibility = () => setVisible(!document.hidden);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      for (const active of currentControllers) active.abort();
      currentControllers.clear();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [refresh]);

  useEffect(() => {
    if (!session?.expires_at || expired) return;
    const timer = setTimeout(
      () => setExpired(true),
      Math.max(0, Date.parse(session.expires_at) - Date.now()),
    );
    return () => clearTimeout(timer);
  }, [session?.expires_at, expired]);

  useEffect(() => {
    // No idle requests. Pause in hidden tabs and after any connection error.
    if (!activeId || unavailable || error || !visible) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const stopAt = running?.deadline
      ? Date.parse(running.deadline) + 25_000
      : (pending?.started || Date.now()) + 145_000;
    const poll = async () => {
      if (session?.expires_at && Date.parse(session.expires_at) <= Date.now())
        return;
      if (Date.now() >= stopAt) {
        setError(
          "The task's deadline has passed. Refresh history to confirm its status.",
        );
        return;
      }
      try {
        const next = await refresh(controller.signal);
        // Stop can be clicked before Begin has durably stored the browser's UUID.
        // Keep that intent until session history acknowledges the running task.
        const toStop = next.runs.find(
          (run) =>
            run.id === stopIntent &&
            run.status === "running" &&
            !run.cancel_requested,
        );
        if (!controller.signal.aborted && toStop)
          await requestCancellation(toStop.id);
        if (
          !controller.signal.aborted &&
          !next.paused &&
          Date.parse(next.expires_at) > Date.now() &&
          (next.runs.some((run) => run.status === "running") ||
            (pending && !next.runs.some((run) => run.id === pending.runId)))
        ) {
          timer = setTimeout(poll, 2000);
        }
      } catch (err) {
        if (!controller.signal.aborted)
          setError(
            err instanceof Error ? err.message : "History refresh failed.",
          );
      }
    };
    timer = setTimeout(poll, 2000);
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [
    activeId,
    unavailable,
    error,
    visible,
    refresh,
    session?.expires_at,
    running?.deadline,
    pending,
    stopIntent,
    requestCancellation,
  ]);

  async function refreshHistory() {
    setError("");
    setLoading(true);
    const controller = new AbortController();
    controllers.current.add(controller);
    try {
      await refresh(controller.signal);
    } catch (err) {
      if (!controller.signal.aborted)
        setError(err instanceof Error ? err.message : "History unavailable.");
    } finally {
      controllers.current.delete(controller);
      if (!controller.signal.aborted) setLoading(false);
    }
  }

  const sending = useRef(false);

  async function send() {
    const text = draft.trim();
    if (
      !text ||
      (busy && !canUpdate) ||
      sending.current ||
      unavailable ||
      !session ||
      loading ||
      error
    )
      return;
    if (new TextEncoder().encode(text).length > 8192) {
      setError("Please keep the message within 8,192 UTF-8 bytes.");
      return;
    }
    sending.current = true;
    const id = crypto.randomUUID();
    const controller = new AbortController();
    controllers.current.add(controller);
    const runId = canUpdate && running ? running.id : id;
    setPending({ id, runId, text, started: Date.now() });
    setSelectedId(runId);
    setDraft("");
    setError("");
    try {
      const { run } = await post<{ run: Run }>(
        canUpdate ? "input" : "run",
        canUpdate
          ? { id, run_id: runId, message: text }
          : { id, message: text },
        controller.signal,
      );
      if (controller.signal.aborted) return;
      setSession((current) =>
        current
          ? {
              ...current,
              runs: [run, ...current.runs.filter((r) => r.id !== run.id)],
            }
          : current,
      );
      await refresh(controller.signal);
    } catch (err) {
      if (!controller.signal.aborted) {
        setError(
          err instanceof Error
            ? err.message
            : "Connection interrupted. Refresh history to confirm the task status.",
        );
      }
    } finally {
      controllers.current.delete(controller);
      sending.current = false;
      if (!controller.signal.aborted) setPending(undefined);
    }
  }

  function stop() {
    if (!activeId || stopPending || running?.cancel_requested) return;
    setError("");
    setStopIntent(activeId);
    if (running) void requestCancellation(activeId);
  }

  return {
    session,
    draft,
    setDraft,
    error,
    loading,
    pending,
    stopPending,
    stopIntent,
    selectedId,
    setSelectedId,
    running,
    activeId,
    selected,
    unavailable,
    busy,
    canUpdate,
    refreshHistory,
    send,
    stop,
  };
}
