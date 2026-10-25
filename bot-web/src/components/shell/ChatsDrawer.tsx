"use client";

import { useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { Dialog } from "radix-ui";
import { MessagesSquare, Plus, Search } from "lucide-react";
import { Medallion } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { focusSelf } from "@/components/ui/layers";
import { excerptOf } from "@/components/chat/Message";
import { useNow } from "@/lib/hooks";
import { MAIN_THREAD } from "@/lib/seed";
import { useNori } from "@/lib/store";
import { relative } from "@/lib/time";
import type { Thread } from "@/lib/types";
import { cn } from "@/lib/utils";

function preview(t: Thread, artifacts: { id: string; title: string }[]) {
  const last = t.items[t.items.length - 1];
  if (!last) return "No messages yet";
  return `${last.sender === "me" ? "You: " : ""}${excerptOf(last, artifacts)}`;
}

function ChatRow({
  t,
  pinned,
  active,
  title,
  preview,
  when,
  onOpen,
}: {
  t: Thread;
  pinned?: boolean;
  active: boolean;
  title: string;
  preview: string;
  when: string;
  onOpen: (id: string) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => onOpen(t.id)}
      aria-current={active ? "true" : undefined}
      className={cn("t-fast flex w-full items-center gap-3 rounded-chip px-2.5 py-2 text-left transition-colors", active ? "bg-fill-2" : "hover:bg-fill-1")}
    >
      {pinned && <Medallion size={24} />}
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span className="flex-1 truncate text-row-title font-medium text-ink">{title}</span>
          <span className={cn("shrink-0 text-caption", active ? "text-ink-2" : "text-ink-3")}>{when}</span>
        </span>
        <span className="block truncate text-meta text-ink-2">{preview}</span>
      </span>
    </button>
  );
}

/** Slides from the rail edge over main (doesn't push). */
export function ChatsDrawer() {
  const open = useNori((s) => s.drawerOpen);
  const threads = useNori((s) => s.threads);
  const artifacts = useNori((s) => s.artifacts);
  const active = useNori((s) => s.activeThreadId);
  const name = useNori((s) => s.identity.name);
  const set = useNori((s) => s.set);
  const setActive = useNori((s) => s.setActiveThread);
  const newSideChat = useNori((s) => s.newSideChat);
  const router = useRouter();
  const pathname = usePathname();
  const now = useNow();
  const [q, setQ] = useState("");

  const main = threads.find((t) => t.id === MAIN_THREAD)!;
  const side = threads
    .filter((t) => t.kind === "side")
    .filter((t) => !q || `${t.title} ${preview(t, artifacts)}`.toLowerCase().includes(q.toLowerCase()))
    .sort((a, b) => (b.items.at(-1)?.at ?? b.createdAt) - (a.items.at(-1)?.at ?? a.createdAt));

  const rowProps = (t: Thread) => ({
    active: active === t.id,
    title: t.title,
    preview: preview(t, artifacts),
    when: relative(t.items.at(-1)?.at ?? t.createdAt, now),
    onOpen: go,
  });

  function go(id: string) {
    setActive(id);
    set({ drawerOpen: false });
    if (pathname !== "/") router.push("/");
  }

  return (
    <Dialog.Root open={open} onOpenChange={(o) => set({ drawerOpen: o })} modal={false}>
      <Dialog.Portal>
        <Dialog.Content
          aria-describedby={undefined}
          onOpenAutoFocus={focusSelf}
          onInteractOutside={(e) => {
            // A layer opening on top (search, a menu) shouldn't close the drawer underneath.
            const target = e.target as HTMLElement | null;
            if (target?.closest("[role=dialog], [data-radix-popper-content-wrapper]")) e.preventDefault();
          }}
          className="sheet-left fixed inset-y-0 z-40 flex flex-col bg-sidebar shadow-[inset_-1px_0_0_var(--hairline),var(--shadow-pop)]"
          style={{ left: "var(--rail-w)", width: "var(--drawer-w)" }}
        >
          <Dialog.Title className="sr-only">Chats</Dialog.Title>
          <div className="px-3 pt-4 pb-2">
            <label className="flex h-9 items-center gap-2 rounded-full bg-fill-1 px-3 text-ink-3 shadow-[inset_0_0_0_1px_var(--hairline)] focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-focus">
              <Search size={16} strokeWidth={1.5} aria-hidden />
              <input
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder="Search chats"
                aria-label="Search chats"
                className="min-w-0 flex-1 bg-transparent text-input text-ink placeholder:text-ink-3 focus-visible:outline-none"
              />
            </label>
          </div>
          <div className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto px-1.5">
            <ChatRow t={main} pinned {...rowProps(main)} title={name} />
            <div className="mt-4 mb-1 px-2.5 text-section font-medium text-ink">Side chats</div>
            {side.length ? (
              side.map((t) => <ChatRow key={t.id} t={t} {...rowProps(t)} />)
            ) : (
              <div className="flex flex-col items-center px-6 py-10 text-center">
                <MessagesSquare size={28} strokeWidth={1.5} className="text-ink-3" aria-hidden />
                <div className="mt-3 text-row-title font-medium text-ink">Start a side chat</div>
                <div className="mt-0.5 text-meta text-ink-2">Side chats keep a topic in its own thread.</div>
              </div>
            )}
          </div>
          <div className="p-3">
            <Button
              className="w-full"
              onClick={() => {
                newSideChat();
                if (pathname !== "/") router.push("/");
                set({ composerFocus: Date.now() });
              }}
            >
              <Plus size={16} strokeWidth={1.5} aria-hidden />
              New side chat
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
