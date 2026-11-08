"use client";

import { Plus } from "lucide-react";
import type { Summary } from "@/lib/chat/contract";
import { separatorLabel } from "@/lib/time";
import { cn } from "@/lib/utils";

export function ChatList({ chats, openId, onOpen, onNew }: {
  chats: Summary[];
  openId?: string;
  onOpen: (id: string) => void;
  onNew: () => void;
}) {
  return (
    <div className="space-y-1">
      <button type="button" onClick={onNew} className="flex w-full items-center gap-2 rounded-tile px-3 py-2.5 text-left text-row-title font-medium text-accent-text hover:bg-fill-1">
        <Plus size={16} strokeWidth={1.75} aria-hidden />New chat
      </button>
      {!chats.length && <p className="px-3 py-4 text-meta text-ink-3">No chats yet.</p>}
      <nav aria-label="Chats" className="space-y-1">
        {chats.map((chat) => (
          <button key={chat.id} type="button" data-conversation-id={chat.id} aria-current={chat.id === openId ? "page" : undefined} onClick={() => onOpen(chat.id)}
            className={cn("flex w-full items-center gap-2 rounded-tile px-3 py-2.5 text-left hover:bg-fill-1", chat.id === openId && "bg-fill-1")}>
            <span className="min-w-0 flex-1">
              <span className="block truncate text-row-title text-ink">{chat.title}</span>
              <small className="block text-caption text-ink-3">{separatorLabel(Date.parse(chat.updatedAt))}</small>
            </span>
            {chat.activeTurn && <span aria-label="Replying" className="pulse-dot size-2 shrink-0 rounded-full bg-accent-signal" />}
          </button>
        ))}
      </nav>
    </div>
  );
}
