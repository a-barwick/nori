import type { Part, Thread, ThreadItem } from "@/lib/types";
import type { Conversation, Operation } from "./contract";

const label = (operation: Operation) => {
  const name = operation.name.replaceAll("_", " ");
  return `${name.charAt(0).toUpperCase()}${name.slice(1)} · ${operation.outcome}`;
};

/**
 * A chat's saved messages as thread items. Files, tool steps and a stopped or
 * interrupted reply become parts under the text. Messages carry no time, so the chat's own does.
 */
export function chatThread(chat: Conversation | null): Thread {
  if (!chat) return { id: "new-chat", title: "New chat", kind: "main", createdAt: 0, items: [] };
  const at = Date.parse(chat.updatedAt ?? "") || 0;
  const base = `/api/v1/conversations/${chat.id}`;
  const items: ThreadItem[] = [];
  for (const message of chat.messages) {
    const operations = message.operations ?? [];
    const parts: Part[] = (message.attachments ?? []).map((file) => ({ type: "attachment", ...file, url: `${base}/uploads/${file.id}` }));
    for (const operation of operations) {
      if (operation.action) parts.push({ type: "action", name: label(operation), text: operation.action.text });
      else parts.push({ type: "note", text: label(operation) });
    }
    if (message.status === "cancelled") parts.push({ type: "note", text: "Stopped" });
    if (message.status === "failed") parts.push({ type: "note", text: "Reply interrupted" });
    const text = message.text;
    if (!text && !parts.length) continue;
    items.push({ id: message.id, kind: "text", sender: message.role === "user" ? "me" : "nori", at, text, parts });
  }
  return { id: chat.id, title: chat.title, kind: "main", createdAt: at, items };
}
