"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { Artifact, Run, Session } from "@/lib/poc/types";
import "./poc.css";

async function post<T>(operation: string, payload: object, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api/${operation}`, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload), cache: "no-store", signal,
  });
  const data = await response.json().catch(() => ({ error: "Nori returned an unreadable response." }));
  if (!response.ok) throw new Error(data.error || `Request failed (${response.status}).`);
  return data as T;
}

function statusLabel(run: Run) {
  return run.status === "running" && run.cancel_requested ? "Stopping…" : run.status === "running" && run.waiting ? "Waiting for you" : {
    running: "Running", completed: "Completed", failed: "Failed", cancelled: "Cancelled",
  }[run.status];
}

function Screenshot({ runId, artifact }: { runId: string; artifact: Artifact }) {
  const [url, setUrl] = useState<string>();
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    let objectUrl: string | undefined;
    async function load() {
      try {
        const response = await fetch("/api/artifact", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ run_id: runId, artifact_id: artifact.id }), signal: controller.signal,
        });
        if (!response.ok || !response.headers.get("content-type")?.startsWith("image/png")) {
          throw new Error("Screenshot could not be loaded.");
        }
        objectUrl = URL.createObjectURL(await response.blob());
        if (!controller.signal.aborted) setUrl(objectUrl);
      } catch (err) {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "Screenshot unavailable.");
      }
    }
    void load();
    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [runId, artifact.id]);
  if (error) return <p className="poc-muted">{error}</p>;
  if (!url) return <p className="poc-muted">Loading screenshot…</p>;
  return <a href={url} target="_blank" rel="noreferrer" className="poc-screenshot">
    {/* Protected PNGs are fetched with POST and displayed through a local blob URL. */}
    {/* eslint-disable-next-line @next/next/no-img-element */}
    <img src={url} alt="Screenshot captured by Nori during this task. Open full size." />
    <span>Open screenshot ↗</span>
  </a>;
}

export function NoriApp() {
  const [session, setSession] = useState<Session>();
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState<{ id: string; text: string; started: number }>();
  const [stopPending, setStopPending] = useState(false);
  const [stopIntent, setStopIntent] = useState<string>();
  const [selectedId, setSelectedId] = useState<string>();
  const [visible, setVisible] = useState(true);
  const [expired, setExpired] = useState(false);
  const controllers = useRef(new Set<AbortController>());
  const cancellationInFlight = useRef(false);
  const messagesEnd = useRef<HTMLDivElement>(null);
  const running = session?.runs.find((run) => run.status === "running");
  const activeId = running?.id || pending?.id;
  const selected = session?.runs.find((run) => run.id === selectedId) || (!pending ? session?.runs[0] : undefined);
  const unavailable = !!session?.paused || expired;
  const busy = !!activeId;
  const canUpdate = !!running?.temporal && !running.cancel_requested && !pending;

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
      const { run } = await post<{ run: Run }>("cancel", { id }, controller.signal);
      if (controller.signal.aborted) return;
      setSession((current) => current ? { ...current, runs: [run, ...current.runs.filter((r) => r.id !== run.id)] } : current);
      setError("");
    } catch (err) {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "Stop could not be confirmed.");
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
          setExpired(!!data.expires_at && Date.parse(data.expires_at) <= Date.now());
        }
      })
      .catch((err) => { if (!controller.signal.aborted) setError(err.message); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
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
    const timer = setTimeout(() => setExpired(true), Math.max(0, Date.parse(session.expires_at) - Date.now()));
    return () => clearTimeout(timer);
  }, [session?.expires_at, expired]);

  useEffect(() => {
    // No idle requests. Pause in hidden tabs and after any connection error.
    if (!activeId || unavailable || error || !visible) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const stopAt = running?.deadline ? Date.parse(running.deadline) + 25_000 : (pending?.started || Date.now()) + 145_000;
    const poll = async () => {
      if (session?.expires_at && Date.parse(session.expires_at) <= Date.now()) return;
      if (Date.now() >= stopAt) {
        setError("The task's deadline has passed. Refresh history to confirm its status.");
        return;
      }
      try {
        const next = await refresh(controller.signal);
        // Stop can be clicked before Begin has durably stored the browser's UUID.
        // Keep that intent until session history acknowledges the running task.
        const toStop = next.runs.find((run) => run.id === stopIntent && run.status === "running" && !run.cancel_requested);
        if (!controller.signal.aborted && toStop) await requestCancellation(toStop.id);
        if (!controller.signal.aborted && !next.paused &&
          Date.parse(next.expires_at) > Date.now() &&
          (next.runs.some((run) => run.status === "running") ||
            (pending && !next.runs.some((run) => run.id === pending.id)))) {
          timer = setTimeout(poll, 2000);
        }
      } catch (err) {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "History refresh failed.");
      }
    };
    timer = setTimeout(poll, 2000);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [activeId, unavailable, error, visible, refresh, session?.expires_at, running?.deadline, pending, stopIntent, requestCancellation]);

  useEffect(() => {
    messagesEnd.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [session?.messages.length, pending?.id]);

  async function refreshHistory() {
    setError("");
    setLoading(true);
    const controller = new AbortController();
    controllers.current.add(controller);
    try { await refresh(controller.signal); }
    catch (err) { if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "History unavailable."); }
    finally { controllers.current.delete(controller); if (!controller.signal.aborted) setLoading(false); }
  }

  async function send() {
    const text = draft.trim();
    if (!text || (busy && !canUpdate) || unavailable || !session) return;
    if (new TextEncoder().encode(text).length > 8192) {
      setError("Please keep the message within 8,192 UTF-8 bytes.");
      return;
    }
    const id = crypto.randomUUID();
    const controller = new AbortController();
    controllers.current.add(controller);
    setPending({ id, text, started: Date.now() });
    setSelectedId(canUpdate ? running?.id : id);
    setDraft("");
    setError("");
    try {
      const { run } = await post<{ run: Run }>(canUpdate ? "input" : "run", canUpdate ? { id, run_id: running?.id, message: text } : { id, message: text }, controller.signal);
      if (controller.signal.aborted) return;
      setSession((current) => current ? { ...current, runs: [run, ...current.runs.filter((r) => r.id !== run.id)] } : current);
      await refresh(controller.signal);
    } catch (err) {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : "Connection interrupted. Refresh history to confirm the task status.");
      }
    } finally {
      controllers.current.delete(controller);
      if (!controller.signal.aborted) setPending(undefined);
    }
  }

  function stop() {
    if (!activeId || stopPending || running?.cancel_requested) return;
    setError("");
    setStopIntent(activeId);
    if (running) void requestCancellation(activeId);
  }

  const pendingNotPersisted = pending && !session?.messages.some((message) => message.run_id === pending.id && message.role === "user");
  return <main className="poc-app">
    <header className="poc-header">
      <div className="poc-brand"><span className="poc-mark" aria-hidden="true">n</span><div><h1>Nori</h1><p>Your place to get things done.</p></div></div>
      <button className="poc-button poc-secondary" onClick={refreshHistory} disabled={loading}>{loading ? "Loading…" : "Refresh history"}</button>
    </header>
    <div className="poc-layout">
      <section className="poc-conversation" aria-label="Conversation">
        <div className="poc-messages" aria-live="polite" aria-relevant="additions">
          {!loading && !session?.messages.length && !pending && <div className="poc-welcome"><span className="poc-eyebrow">A LITTLE HELP, A LOT POSSIBLE</span><h2>What are we doing today?</h2><p>Ask Nori to work on something. You’ll see what happens here as it goes.</p></div>}
          {session?.messages.map((message) => <article key={message.id} className={`poc-message poc-message-${message.role}`}><span className="poc-message-author">{message.role === "user" ? "You" : "Nori"}</span><div>{message.text}</div></article>)}
          {pendingNotPersisted && <article className="poc-message poc-message-user"><span className="poc-message-author">You · sending</span><div>{pending.text}</div></article>}
          <div ref={messagesEnd} />
        </div>
        <div className="poc-compose-area">
          {error && <div role="alert" className="poc-error">{error}<span>Automatic refresh is paused. Refresh history to reconnect.</span></div>}
          {unavailable && <p className="poc-notice">{session?.paused ? "This session is paused." : "This session has expired."} History remains available.</p>}
          <form className="poc-composer" onSubmit={(event) => { event.preventDefault(); void send(); }}>
            <label htmlFor="message" className="poc-sr-only">Message Nori</label>
            <textarea id="message" placeholder="Ask Nori…" value={draft} rows={2} maxLength={8192} onChange={(event) => setDraft(event.target.value)} disabled={!session || unavailable || (busy && !canUpdate)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); void send(); } }} />
            <div className="poc-composer-footer"><span>{canUpdate ? "Send a correction or answer for the current task." : busy ? "One task at a time. You can stop it below." : "Enter to send · Shift + Enter for a new line"}</span><button type="submit" className="poc-button" disabled={!draft.trim() || !session || unavailable || (busy && !canUpdate)}>{canUpdate ? "Update ↑" : "Send ↑"}</button></div>
          </form>
        </div>
      </section>
      <aside className="poc-task-panel" aria-label="Task activity">
        <div className="poc-panel-heading"><h2>Task activity</h2>{busy && <button className="poc-button poc-stop" disabled={stopPending || (!error && stopIntent === activeId) || running?.cancel_requested} onClick={stop}>{error && stopIntent === activeId ? "Retry stop" : stopPending || stopIntent === activeId || running?.cancel_requested ? "Stopping…" : "Stop"}</button>}</div>
        {stopIntent === activeId && pending && !running && <p className="poc-muted">Stop requested. Waiting for the runtime to receive the task before confirming it has stopped.</p>}
        {!selected ? <div className="poc-idle"><span className="poc-dot" />{loading ? "Loading history…" : !session ? "Runtime unavailable." : pending ? "Sending task to Nori…" : "Ready when you are."}<p>Activity and screenshots from your tasks will appear here.</p></div> : <>
          <div className={`poc-status poc-status-${selected.status}`}><span className="poc-dot" />{statusLabel(selected)}</div>
          {selected.cancel_requested && selected.status === "running" && <p className="poc-muted">Stop was requested. Waiting for Nori’s computer to confirm it has stopped.</p>}
          {selected.waiting && selected.status === "running" && <p className="poc-notice">{selected.waiting}</p>}
          {selected.error && <p className="poc-error" role="status">{selected.error}</p>}
          <ol className="poc-events">{selected.events?.map((event, index) => <li key={`${event.created_at}-${index}`}><span className="poc-event-kind">{event.kind.replaceAll("_", " ")}</span><p>{event.text}</p></li>)}</ol>
          {!!selected.artifacts?.length && <div className="poc-artifacts"><h3>Screenshots</h3>{selected.artifacts.map((artifact) => <Screenshot key={`${selected.id}-${artifact.id}`} runId={selected.id} artifact={artifact} />)}</div>}
        </>}
        {!!session?.runs.length && <div className="poc-history"><h3>Recent tasks</h3>{session.runs.map((run) => <button key={run.id} aria-pressed={selected?.id === run.id} onClick={() => setSelectedId(run.id)}><span>{session.messages.find((message) => message.run_id === run.id && message.role === "user")?.text || "Task"}</span><small>{statusLabel(run)}</small></button>)}</div>}
      </aside>
    </div>
  </main>;
}
