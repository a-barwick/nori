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
      if (operation.action) parts.push({ type: "action", name: label(operation), text: operation.action.text });
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
