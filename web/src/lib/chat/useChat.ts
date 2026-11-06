"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { DraftFile } from "@/lib/types";
import { CONTRACT, request, stay, type Capabilities, type Conversation, type Summary } from "./contract";

const MAX_FILE_BYTES = 10 * 1024 * 1024;
const newest = (rows: Summary[]) => [...rows].sort((a, b) => Date.parse(b.updatedAt) - Date.parse(a.updatedAt));

/** The open chat lives in the address as `?chat=<id>`, so a reload returns to it. */
function chatInAddress(id?: string) {
  const url = new URL(window.location.href);
  if (id) url.searchParams.set("chat", id); else url.searchParams.delete("chat");
  if (url.href !== window.location.href) window.history.replaceState(window.history.state, "", url);
}

/**
 * Independent chats: separate conversations with their own saved history, streamed replies,
 * CSV files and exact approvals. They share the Go service with the assistant but not its history.
 */
export function useChat() {
  const [chats, setChats] = useState<Summary[]>([]);
  const [current, setCurrent] = useState<Conversation | null>(null);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [files, setFiles] = useState<DraftFile[]>([]);
  const [error, setError] = useState("");
  const [connected, setConnected] = useState(true);
  const [sending, setSending] = useState(false);
  const currentRef = useRef<Conversation | null>(null);
  const stream = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const creating = useRef<Promise<string> | null>(null);
  const retry = useRef<{ id: string; message: string; conversation: string; attachments: string[] } | null>(null);

  const show = useCallback((c: Conversation) => {
    const shown = currentRef.current;
    if (shown?.id === c.id && c.position < shown.position) return;
    currentRef.current = c;
    setCurrent(c);
    setChats((rows) => {
      const row = rows.find((chat) => chat.id === c.id);
      if (row && row.title === c.title && row.activeTurn === c.activeTurn && row.updatedAt === c.updatedAt) return rows;
      return newest([{ id: c.id, title: c.title, mode: c.mode, activeTurn: c.activeTurn, updatedAt: c.updatedAt ?? new Date().toISOString() }, ...rows.filter((chat) => chat.id !== c.id)]);
    });
  }, []);

  const disconnect = useCallback(() => {
    generation.current++;
    stream.current?.abort();
    stream.current = null;
  }, []);

  const select = useCallback(async (id: string) => {
    // Files picked in a new chat stay: they belong to the chat being created.
    if (currentRef.current && currentRef.current.id !== id) setFiles([]);
    disconnect();
    const selected = generation.current;
    setError("");
    setOpen(true);
    try {
      const snapshot = await request<Conversation>(`/conversations/${id}`);
      if (selected !== generation.current) return;
      currentRef.current = snapshot;
      setCurrent(snapshot);
      chatInAddress(id);
      const controller = new AbortController();
      stream.current = controller;
      setConnected(true);
      void stay(snapshot, controller.signal, (c) => { if (selected === generation.current) show(c); }, (on) => { if (selected === generation.current) setConnected(on); });
    } catch (e) { if (selected === generation.current) setError(String(e)); }
  }, [disconnect, show]);

  const refresh = useCallback(async () => {
    try {
      const result = await request<{ conversations: Summary[] }>("/conversations");
      setChats(newest(result.conversations.filter((c) => c.mode === "chat")));
    } catch (e) { setError(String(e)); }
  }, []);

  useEffect(() => {
    void (async () => {
      try {
        const capabilities = await request<Capabilities>("/capabilities");
        if (capabilities.contract !== CONTRACT) throw new Error("The service uses an incompatible chat contract.");
        const result = await request<{ conversations: Summary[] }>("/conversations");
        const rows = newest(result.conversations.filter((c) => c.mode === "chat"));
        setChats(rows);
        const saved = new URL(window.location.href).searchParams.get("chat");
        if (saved && rows.some((c) => c.id === saved)) await select(saved);
        else if (saved) chatInAddress();
      } catch (e) { setError(String(e)); }
    })();
    return disconnect;
  }, [disconnect, select]);

  /** A new chat is created when its first message or file is sent, so opening one never leaves empty chats. */
  const startNew = useCallback(() => {
    disconnect();
    currentRef.current = null;
    setCurrent(null);
    setFiles([]);
    setError("");
    retry.current = null;
    setOpen(true);
    chatInAddress();
  }, [disconnect]);

  const close = useCallback(() => {
    disconnect();
    currentRef.current = null;
    setCurrent(null);
    setFiles([]);
    setError("");
    setOpen(false);
    chatInAddress();
  }, [disconnect]);

  /** The open chat's ID, creating it first when this is a new chat. */
  async function ensure() {
    if (currentRef.current) return currentRef.current.id;
    creating.current ??= (async () => {
      const created = await request<Conversation>("/conversations", { method: "POST", body: "{}" });
      await select(created.id);
      return created.id;
    })().finally(() => { creating.current = null; });
    return creating.current;
  }

  async function send() {
    const shown = currentRef.current;
    const message = draft.trim();
    if (shown?.activeTurn || sending || !message || files.some((file) => file.status !== "ready")) return;
    setSending(true);
    setError("");
    try {
      const id = await ensure();
      if (currentRef.current?.id !== id) return;
      const attachments = files.map((file) => file.id);
      const last = retry.current;
      if (!last || last.message !== message || last.conversation !== id || JSON.stringify(last.attachments) !== JSON.stringify(attachments))
        retry.current = { id: crypto.randomUUID(), message, conversation: id, attachments };
      const saved = await request<{ conversation: Conversation }>(`/conversations/${id}/messages`, { method: "POST", body: JSON.stringify({ id: retry.current!.id, text: message, ...(attachments.length ? { attachments } : {}) }) });
      retry.current = null;
      setDraft("");
      setFiles([]);
      if (currentRef.current?.id === id) show(saved.conversation);
    } catch (e) { setError(`${String(e)}. Send again to retry the same message.`); }
    finally { setSending(false); }
  }

  async function stop() {
    const shown = currentRef.current;
    if (!shown?.activeTurn) return;
    try { show(await request<Conversation>(`/conversations/${shown.id}/cancel`, { method: "POST", body: JSON.stringify({ turnId: shown.activeTurn }) })); }
    catch (e) { setError(String(e)); }
  }

  async function remove() {
    const shown = currentRef.current;
    if (!shown || shown.activeTurn) return;
    if (!window.confirm(`Delete “${shown.title}” and its files? This can't be undone.`)) return;
    try {
      await request(`/conversations/${shown.id}`, { method: "DELETE" });
      setChats((rows) => rows.filter((chat) => chat.id !== shown.id));
      close();
    } catch (e) { setError(String(e)); }
  }

  // A retry keeps the same ID, so a file that did reach the service isn't stored twice.
  async function upload(item: DraftFile) {
    setFiles((rows) => rows.map((file) => file.id === item.id ? { ...file, status: "uploading", error: undefined } : file));
    try {
      const id = await ensure();
      const query = new URLSearchParams({ id: item.id, filename: item.name });
      const response = await fetch(`/api/v1/conversations/${id}/uploads?${query}`, { method: "POST", headers: { "Content-Type": item.file.type || "text/csv" }, body: item.file, redirect: "error", signal: AbortSignal.timeout(30000) });
      const result = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(result.error || `Upload failed (${response.status})`);
      setFiles((rows) => currentRef.current?.id === id
        ? rows.map((file) => file.id === item.id ? { ...file, ...result, status: "ready" } : file)
        : rows.filter((file) => file.id !== item.id));
    } catch (e) {
      setFiles((rows) => rows.map((file) => file.id === item.id ? { ...file, status: "error", error: e instanceof Error ? e.message : "Upload failed. Try again." } : file));
    }
  }

  function attach(picked: File[]) {
    for (const file of picked) {
      const item: DraftFile = { id: crypto.randomUUID(), name: file.name, media_type: file.type, size: file.size, status: "uploading", file };
      if (file.size > MAX_FILE_BYTES) { setFiles((rows) => [...rows, { ...item, status: "error", error: "A file can be at most 10 MB." }]); continue; }
      setFiles((rows) => [...rows, item]);
      void upload(item);
    }
  }

  const waiting = !!current?.activeTurn && current.messages.at(-1)?.operations?.at(-1)?.outcome === "prepared";
  return {
    chats, current, open, draft, setDraft, files, error, connected, sending, waiting,
    refresh, select, startNew, close, send, stop, remove, attach, retryFile: upload,
    removeFile: (id: string) => setFiles((rows) => rows.filter((file) => file.id !== id)),
  };
}
