import type { Part, Thread, TextItem } from "@/lib/types";
import { chatAPI, conversationPath } from "./contract";
import type { Conversation, Operation } from "./contract";

const label = (operation: Operation) => {
  const name = operation.name.replaceAll("_", " ");
  return `${name.charAt(0).toUpperCase()}${name.slice(1)} · ${operation.outcome}`;
};

export function chatThread(chat: Conversation | null): Thread {
  if (!chat) return { title: "New chat", items: [] };
  // Chat messages carry a sequence, but only the conversation carries a timestamp.
  const at = Date.parse(chat.updatedAt ?? "") || 0;
  const base = chatAPI(conversationPath(chat.id));
  const items: TextItem[] = [];
  for (const message of chat.messages) {
    const operations = message.operations ?? [];
    const parts: Part[] = (message.attachments ?? []).map((file) => ({ type: "attachment", ...file, url: `${base}/uploads/${file.id}` }));
    for (const operation of operations) {
      // A task started from this chat shows as a live task card that updates in place.
      const taskID = typeof operation.result?.task_id === "string" ? operation.result.task_id : undefined;
      if (taskID) parts.push({ type: "card", schema_version: 1, kind: "progress", resource: { kind: "task", id: taskID }, fallback: "Started a task. Its progress is in Work." });
      else if (operation.action) parts.push({ type: "action", name: label(operation), text: operation.action.text });
      else parts.push({ type: "note", text: label(operation) });
    }
    if (message.status === "cancelled") parts.push({ type: "note", text: "Stopped" });
    if (message.status === "failed") parts.push({ type: "note", text: "Reply interrupted. Send a follow-up to continue." });
    const text = message.text;
    if (!text && !parts.length) continue;
    items.push({ id: message.id, sender: message.role === "user" ? "me" : "nori", at, text, parts });
  }
  return { title: chat.title, items };
}

/** Marks the user's latest message: sending until the core saves it, then saved until a reply starts. */
export function withReceipt(thread: Thread, outgoing: { text: string; at: number } | undefined, replying: boolean): Thread {
  const items = thread.items.map((item) => ({ ...item }));
  if (outgoing) {
    items.push({ id: "outgoing", sender: "me", at: Math.max(outgoing.at, items.at(-1)?.at ?? 0), text: outgoing.text, receipt: "sending" });
  } else if (replying && items.at(-1)?.sender === "me") {
    items[items.length - 1].receipt = "saved";
  }
  return { ...thread, items };
}
