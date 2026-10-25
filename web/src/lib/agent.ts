"use client";

import { useCallback, useEffect, useRef, useState } from "react";

export type AgentMessage = {
  id: string;
  role: "user" | "assistant";
  text: string;
  created_at: string;
};

export type Dispatch = {
  id: string;
  request: string;
  status: "accepted";
  created_at: string;
};

export type AgentSession = {
  messages: AgentMessage[];
  dispatches: Dispatch[];
};

async function post(operation: "session" | "turn", body: object, signal: AbortSignal): Promise<AgentSession> {
  const response = await fetch(`/api/agent/${operation}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    cache: "no-store",
    signal,
  });
  const data = await response.json().catch(() => ({ error: "Nori returned an unreadable response." }));
  if (!response.ok) throw new Error(data.error || `Request failed (${response.status}).`);
  return data;
}

export function useAgent() {
  const [session, setSession] = useState<AgentSession>();
  const [draft, setDraft] = useState("");
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState<{ id: string; text: string; at: number }>();
  const [error, setError] = useState("");
  const controllers = useRef(new Set<AbortController>());
  const sending = useRef(false);
  const retry = useRef<{ id: string; text: string } | undefined>(undefined);

  useEffect(() => {
    const active = controllers.current;
    const controller = new AbortController();
    active.add(controller);
    void post("session", {}, controller.signal)
      .then((data) => { if (!controller.signal.aborted) setSession(data); })
      .catch((err) => { if (!controller.signal.aborted) setError(err.message); })
      .finally(() => {
        active.delete(controller);
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => {
      for (const item of active) item.abort();
      active.clear();
    };
  }, []);

  const refresh = useCallback(async () => {
    if (sending.current) return;
    const controller = new AbortController();
    controllers.current.add(controller);
    setLoading(true);
    setError("");
    try {
      const data = await post("session", {}, controller.signal);
      if (!controller.signal.aborted) setSession(data);
    } catch (err) {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "Could not load the conversation.");
    } finally {
      controllers.current.delete(controller);
      if (!controller.signal.aborted) setLoading(false);
    }
  }, []);

  async function send() {
    const text = draft.trim();
    if (!text || !session || loading || sending.current) return;
    if (new TextEncoder().encode(text).length > 8192) {
      setError("Please keep the message within 8,192 UTF-8 bytes.");
      return;
    }
    const id = retry.current?.text === text ? retry.current.id : crypto.randomUUID();
    const controller = new AbortController();
    controllers.current.add(controller);
    sending.current = true;
    setPending({ id, text, at: Date.now() });
    setDraft("");
    setError("");
    try {
      const data = await post("turn", { id, message: text }, controller.signal);
      if (!controller.signal.aborted) {
        setSession(data);
        retry.current = undefined;
      }
    } catch (err) {
      if (!controller.signal.aborted) {
        retry.current = { id, text };
        setDraft(text);
        setError(err instanceof Error ? err.message : "Could not send your message.");
      }
    } finally {
      controllers.current.delete(controller);
      sending.current = false;
      if (!controller.signal.aborted) setPending(undefined);
    }
  }

  return { session, draft, setDraft, loading, pending, error, refresh, send };
}
