"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { hasSubmission, messageTooLong } from "@/lib/submission";
import type { Attachment, DraftFile } from "@/lib/types";
import { useDraftFiles } from "@/lib/useDraftFiles";
import {
  CONTRACT, chatAPI, conversationPath, mergeMessages, request, stay,
  type Capabilities, type Conversation, type MessagePage, type Summary,
} from "./contract";

const newest = (rows: Summary[]) => [...rows].sort((a, b) => Date.parse(b.updatedAt) - Date.parse(a.updatedAt));

type Selection = {
  controller: AbortController;
  id?: string;
  conversation?: Conversation;
  creating?: Promise<string | undefined>;
  sending?: symbol;
  loadingPage?: boolean;
  timeline?: Timeline;
};

type Submission = { id: string; text: string; attachments: string[] };
type Timeline = { position: number; submission?: Submission };

function chatInAddress(id?: string) {
  const url = new URL(window.location.href);
  if (id) url.searchParams.set("chat", id); else url.searchParams.delete("chat");
  if (url.href !== window.location.href) window.history.replaceState(window.history.state, "", url);
}

async function chatList(signal?: AbortSignal) {
  const result = await request<{ conversations: Summary[] }>("/conversations", { signal });
  return newest(result.conversations.filter((row) => row.mode === "chat"));
}

export function useChat() {
  const [chats, setChats] = useState<Summary[]>([]);
  const [current, setCurrent] = useState<Conversation | null>(null);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [connected, setConnected] = useState(true);
  const [sending, setSending] = useState(false);
  const [opening, setOpening] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const active = useRef<Selection | null>(null);
  const timelines = useRef(new Map<string, Timeline>());
  const { files, attach: attachFiles, retry: retryUpload, remove: removeFile, clear: clearFiles, invalidateUploads } = useDraftFiles();

  const show = useCallback((selection: Selection, snapshot: Conversation, update: "merge" | "snapshot" | "rewind" = "merge") => {
    if (active.current !== selection || selection.controller.signal.aborted || snapshot.id !== selection.id) return;
    const previous = selection.conversation;
    if (update === "rewind") {
      // A restored store can reuse positions. Old requests cannot update the replacement timeline.
      selection.timeline = { position: snapshot.position, submission: selection.timeline?.submission };
      timelines.current.set(snapshot.id, selection.timeline);
      selection.sending = undefined;
      selection.loadingPage = false;
      setSending(false);
      setLoadingOlder(false);
      setError("");
      invalidateUploads();
    } else if (previous && snapshot.position < previous.position) return;
    // Ordinary updates cover only recent or changed messages. Keep earlier pages until the stream resets.
    const conversation = previous && update === "merge"
      ? { ...snapshot, messages: mergeMessages(previous.messages, snapshot.messages), nextBefore: previous.nextBefore }
      : snapshot;
    if (selection.timeline) selection.timeline.position = conversation.position;
    selection.conversation = conversation;
    setCurrent(conversation);
    setChats((rows) => {
      const row = rows.find((chat) => chat.id === conversation.id);
      if (row && row.title === conversation.title && row.activeTurn === conversation.activeTurn && row.updatedAt === conversation.updatedAt) return rows;
      const { id, title, mode, activeTurn, updatedAt } = conversation;
      return newest([{ id, title, mode, activeTurn, updatedAt: updatedAt ?? row?.updatedAt ?? new Date().toISOString() }, ...rows.filter((chat) => chat.id !== id)]);
    });
  }, [invalidateUploads]);

  const connect = useCallback((selection: Selection, snapshot: Conversation) => {
    if (snapshot.id !== selection.id) throw new Error("The service returned a different conversation.");
    selection.timeline = timelines.current.get(snapshot.id) ?? { position: snapshot.position };
    timelines.current.set(snapshot.id, selection.timeline);
    show(selection, snapshot, snapshot.position < selection.timeline.position ? "rewind" : "snapshot");
    chatInAddress(snapshot.id);
    setConnected(true);
    void stay(snapshot, selection.controller.signal, {
      update: (header, messages) => {
        if (selection.conversation) show(selection, { ...selection.conversation, ...header, messages });
      },
      recover: (latest, recovery) => show(selection, latest, recovery),
      connected: (value) => { if (active.current === selection) setConnected(value); },
    });
  }, [show]);

  const choose = useCallback((nextOpen: boolean, id?: string) => {
    active.current?.controller.abort();
    const selection: Selection = { controller: new AbortController(), id };
    active.current = selection;
    setCurrent(null);
    setOpen(nextOpen);
    setOpening(!!id);
    setSending(false);
    setLoadingOlder(false);
    setConnected(true);
    setError("");
    clearFiles();
    if (!id) chatInAddress();
    return selection;
  }, [clearFiles]);

  const select = useCallback(async (id: string) => {
    if (active.current?.id === id && active.current.conversation) return;
    const selection = choose(true, id);
    try {
      const snapshot = await request<Conversation>(conversationPath(id), { signal: selection.controller.signal });
      if (active.current === selection) connect(selection, snapshot);
    } catch (error) {
      if (active.current === selection) setError(String(error));
    } finally {
      if (active.current === selection) setOpening(false);
    }
  }, [choose, connect]);

  const refresh = useCallback(async () => {
    try { setChats(await chatList()); }
    catch (error) { setError(String(error)); }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    const startingSelection = active.current;
    void (async () => {
      try {
        const capabilities = await request<Capabilities>("/capabilities", { signal: controller.signal });
        if (capabilities.contract !== CONTRACT) throw new Error("The service uses an incompatible chat contract.");
        const rows = await chatList(controller.signal);
        if (controller.signal.aborted) return;
        setChats(rows);
        if (active.current !== startingSelection) return;
        const saved = new URL(window.location.href).searchParams.get("chat");
        if (saved && rows.some((chat) => chat.id === saved)) await select(saved);
        else if (saved) chatInAddress();
      } catch (error) {
        if (!controller.signal.aborted) setError(String(error));
      }
    })();
    return () => {
      controller.abort();
      active.current?.controller.abort();
      active.current = null;
    };
  }, [select]);

  const startNew = () => choose(true);
  const close = () => choose(false);

  const UNLOADED = "This chat didn't load. Open it again from the list.";

  // Start a persisted chat only when it has a message or an upload.
  async function ensure(selection: Selection) {
    if (selection.conversation) return selection.conversation.id;
    if (selection.id) throw new Error(UNLOADED);
    selection.creating ??= (async () => {
      const created = await request<Conversation>("/conversations", { method: "POST", body: "{}", signal: selection.controller.signal });
      if (active.current !== selection) return;
      selection.id = created.id;
      connect(selection, created);
      return created.id;
    })().finally(() => { selection.creating = undefined; });
    return selection.creating;
  }

  async function send() {
    const selection = active.current;
    const text = draft.trim();
    if (!selection || selection.sending || selection.conversation?.activeTurn || !hasSubmission(text, files)) return;
    if (messageTooLong(text)) { setError("Please keep the message within 8,192 UTF-8 bytes."); return; }
    if (selection.id && !selection.conversation) { setError(UNLOADED); return; }
    const sending = Symbol();
    selection.sending = sending;
    setSending(true);
    setError("");
    try {
      const id = await ensure(selection);
      const timeline = selection.timeline;
      if (!id || !timeline || active.current !== selection || selection.sending !== sending) return;
      const attachments = files.map((file) => file.id);
      const previous = timeline.submission;
      // A dropped response is ambiguous, so retry the same payload under the same ID.
      const submission = previous && previous.text === text && previous.attachments.join(",") === attachments.join(",")
        ? previous : { id: crypto.randomUUID(), text, attachments };
      timeline.submission = submission;
      const body = JSON.stringify({ id: submission.id, text, ...(attachments.length ? { attachments } : {}) });
      const saved = await request<{ conversation: Conversation }>(`${conversationPath(id)}/messages`, { method: "POST", body });
      if (timelines.current.get(id) !== timeline) return;
      if (timeline.submission === submission) timeline.submission = undefined;
      // The text is saved even if the user switched chats mid-send; don't leave it to be sent twice.
      setDraft((current) => (current.trim() === text ? "" : current));
      if (active.current !== selection) return;
      clearFiles();
      show(selection, saved.conversation);
    } catch (error) {
      if (active.current === selection && selection.sending === sending) setError(`${String(error)}. Send again to retry the same message.`);
    } finally {
      if (selection.sending === sending) {
        selection.sending = undefined;
        if (active.current === selection) setSending(false);
      }
    }
  }

  async function loadOlder() {
    const selection = active.current;
    const shown = selection?.conversation;
    if (!selection || !shown?.nextBefore || selection.loadingPage) return;
    const timeline = selection.timeline;
    const currentTimeline = () => active.current === selection && selection.timeline === timeline;
    selection.loadingPage = true;
    setLoadingOlder(true);
    setError("");
    try {
      const page = await request<MessagePage>(`${conversationPath(shown.id)}/messages?before=${shown.nextBefore}`, { signal: selection.controller.signal });
      const latest = selection.conversation;
      if (!currentTimeline() || !latest || latest.nextBefore !== shown.nextBefore) return;
      const conversation = { ...latest, nextBefore: page.nextBefore, messages: mergeMessages(page.messages, latest.messages) };
      selection.conversation = conversation;
      setCurrent(conversation);
    } catch (error) {
      if (currentTimeline()) setError(String(error));
    } finally {
      if (currentTimeline()) {
        selection.loadingPage = false;
        setLoadingOlder(false);
      }
    }
  }

  async function stop() {
    const selection = active.current;
    const shown = selection?.conversation;
    if (!selection || !shown?.activeTurn) return;
    const timeline = selection.timeline;
    try {
      const latest = await request<Conversation>(`${conversationPath(shown.id)}/cancel`, { method: "POST", body: JSON.stringify({ turnId: shown.activeTurn }) });
      if (selection.timeline === timeline) show(selection, latest);
    } catch (error) {
      if (active.current === selection && selection.timeline === timeline) setError(String(error));
    }
  }

  async function remove() {
    const selection = active.current;
    const shown = selection?.conversation;
    if (!selection || !shown || shown.activeTurn) return;
    if (!window.confirm(`Delete “${shown.title}” and its files? This can't be undone.`)) return;
    const timeline = selection.timeline;
    try {
      await request(conversationPath(shown.id), { method: "DELETE" });
      timelines.current.delete(shown.id);
      setChats((rows) => rows.filter((chat) => chat.id !== shown.id));
      if (active.current?.id === shown.id) close();
    } catch (error) {
      if (active.current === selection && selection.timeline === timeline) setError(String(error));
    }
  }

  async function upload(item: DraftFile) {
    const selection = active.current;
    const timeline = selection?.timeline;
    const id = selection && await ensure(selection);
    if (!id || active.current !== selection || (timeline && timeline !== selection.timeline)) throw new Error("Chat changed before the file could be uploaded.");
    const query = new URLSearchParams({ id: item.id, filename: item.name });
    const response = await fetch(chatAPI(`${conversationPath(id)}/uploads?${query}`), {
      method: "POST", headers: { "Content-Type": item.file.type || "text/csv" }, body: item.file, redirect: "error",
      signal: AbortSignal.any([selection.controller.signal, AbortSignal.timeout(30_000)]),
    });
    const result = await response.json();
    if (!response.ok) throw new Error(result?.error || `Upload failed (${response.status})`);
    return result as Attachment;
  }

  return {
    chats, current, open, draft, setDraft, files, error, connected, sending, opening,
    refresh, select, startNew, close, send, stop, remove,
    attach: (picked: File[]) => attachFiles(picked, upload, setError),
    retryFile: (item: DraftFile) => retryUpload(item, upload),
    loadOlder, loadingOlder, removeFile,
  };
}
