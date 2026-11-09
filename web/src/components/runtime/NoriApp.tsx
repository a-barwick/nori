"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Dialog } from "radix-ui";
import { Activity, ArrowLeft, RefreshCw, Trash2 } from "lucide-react";
import { Thread } from "@/components/chat/Thread";
import { IconButton } from "@/components/ui/primitives";
import { chatThread } from "@/lib/chat/thread";
import { useAssistant } from "@/lib/assistant/context";
import { useWide } from "@/lib/hooks";
import { runtimePath } from "@/lib/runtime/paths";
import type { Message, Part, Thread as Conversation, TextItem } from "@/lib/types";
import { ChatList } from "./ChatList";
import { DocumentViewer } from "./DocumentViewer";
import { RuntimeComposer } from "./RuntimeComposer";
import { TaskPanel, type PanelTab } from "./TaskPanel";

function partURL(part: Part, agentMode: boolean) {
  if (part.type === "attachment" && part.id) return runtimePath(agentMode, `v1/uploads/${part.id}`);
  if ((part.type === "artifact" || part.type === "image") && part.id) return runtimePath(agentMode, `v1/results/${part.id}/file`);
  return part.url;
}

function partsOf(message: Pick<Message, "text" | "parts">, agentMode: boolean) {
  const parts: Part[] = message.parts?.length ? message.parts : [{ type: "text", text: message.text }];
  const text = parts.filter((part) => part.type === "text");
  return {
    text: text.map((part) => part.text ?? "").join("\n\n"), display: text.find((part) => part.display)?.display,
    parts: parts.filter((part) => part.type !== "text").map((part) => ({ ...part, url: partURL(part, agentMode) })),
  };
}

/** The main conversation and side chats. Runtime state lives in AssistantProvider so it survives navigation. */
export function ChatScreen() {
  const { runtime, chat, agentMode, debug } = useAssistant();
  const [tab, setTab] = useState<PanelTab>(() => new URL(window.location.href).searchParams.has("chat") ? "chats" : "activity");
  const {
    session,
    pending,
    activeTasks,
    selected,
    loading,
    error,
  } = runtime;
  const wide = useWide();
  const [document, setDocument] = useState<{ body: string; at: number } | null>(
    null,
  );
  const [dockOpen, setDockOpen] = useState(false);
  const [overlayOpen, setOverlayOpen] = useState(false);
  const activityButton = useRef<HTMLButtonElement>(null);
  const panelOpen = wide ? dockOpen : overlayOpen;
  const closePanel = useCallback(
    () => (wide ? setDockOpen(false) : setOverlayOpen(false)),
    [wide],
  );
  const togglePanel = useCallback(
    () =>
      wide ? setDockOpen((open) => !open) : setOverlayOpen((open) => !open),
    [wide],
  );
  const [unread, setUnread] = useState<{ count: number; jump: () => void }>({
    count: 0,
    jump: () => {},
  });
  const onUnread = useCallback(
    (count: number, jump: () => void) => setUnread({ count, jump }),
    [],
  );
  const conversation = useMemo<Conversation>(() => {
    const generatedRequests = new Set(session?.dispatches?.map((dispatch) => `${dispatch.id}-user`));
    const items: TextItem[] = (session?.messages ?? []).filter((message) => !(message.run_id && message.id === `${message.run_id}-user` && message.turn_id) && !generatedRequests.has(message.id)).map((message) => ({
      id: message.id,
      sender: message.role === "user" ? "me" : "nori",
      at: Date.parse(message.created_at),
      ...partsOf(message, agentMode),
      debug: debug && message.role === "assistant"
        ? { run: session?.runs.find((run) => run.id === message.run_id), turns: session?.traces?.filter((trace) => trace.turn_id === (message.turn_id || message.id.replace(/-assistant$/, ""))), agentMode, turnId: message.turn_id }
        : undefined,
    }));
    for (const item of pending) {
      if (!session?.messages.some((message) => message.id === `${item.id}-user`)) items.push({
        id: `${item.id}-user`, sender: "me", at: item.started, text: item.text,
        parts: item.attachments?.map((file) => ({ type: "attachment", ...file, url: runtimePath(agentMode, `v1/uploads/${file.id}`) })),
      });
    }
    items.sort((a, b) => a.at - b.at);
    return { title: "Nori", items };
  }, [session?.messages, session?.runs, session?.dispatches, session?.traces, pending, debug, agentMode]);

  const openChat = chat.current;
  const chatConversation = useMemo(() => chatThread(openChat), [openChat]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key === ".") {
        event.preventDefault();
        togglePanel();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [togglePanel]);

  const panel = (
    <TaskPanel
      tab={tab}
      onTab={(next) => { setTab(next); if (next === "chats") void chat.refresh(); }}
      chats={<ChatList chats={chat.chats} openId={chat.open ? chat.current?.id : undefined}
        onOpen={(id) => { void chat.select(id); if (!wide) setOverlayOpen(false); }}
        onNew={() => { chat.startNew(); if (!wide) setOverlayOpen(false); }} />}
      session={session}
      selected={selected}
      pending={pending.length > 0}
      loading={loading}
      error={error}
      unavailable={runtime.tasksUnavailable}
      onSelect={runtime.setSelectedId}
      onRefresh={() => void runtime.refreshHistory()}
      onClose={closePanel}
      debug={debug}
      agentMode={agentMode}
      commands={runtime.taskCommands}
      drafts={runtime.taskDrafts}
      onDraft={runtime.setTaskDraft}
      onCommand={runtime.commandTask}
      onRetryCommand={runtime.retryTaskCommand}
      onDismissCommand={runtime.dismissTaskCommand}
    />
  );
  const notice = error || (session?.conversation?.routing_blocked && !runtime.conversationBusy
    ? "Some work is waiting for this message to be resolved. Send a follow-up to retry or clarify."
    : "");
  const waitingCount = activeTasks.filter((run) => run.question ? !run.question.resolved_by : run.waiting).length;
  const work = runtime.conversationBusy ? "Nori is thinking…" : pending.some((item) => !item.error) ? "Sending your message…" : activeTasks.length
    ? runtime.tasksUnavailable ? "Task execution is paused. You can still chat with Nori."
      : `${activeTasks.length} ${activeTasks.length === 1 ? "task" : "tasks"} in progress${waitingCount ? ` · ${waitingCount} waiting for you` : ""}`
    : undefined;

  return (
    <>
    <div className="nori-chat-toolbar flex shrink-0 items-center justify-end gap-2 px-4 pt-3">
      <IconButton label={loading ? "Loading history" : "Refresh history"} size={32} disabled={loading} onClick={() => void runtime.refreshHistory()}>
        <RefreshCw size={16} strokeWidth={1.5} />
      </IconButton>
      <button ref={activityButton} type="button" className="nova-activity-button" aria-label="Activity" aria-expanded={panelOpen} onClick={(event) => { activityButton.current = event.currentTarget; togglePanel(); }}>
        <Activity size={16} /><span>Activity</span>{runtime.busy && <span className="nova-working-dot" />}
      </button>
    </div>
    <div className="nova-live-body flex min-h-0 flex-1 overflow-clip text-ink">
      {chat.open ? <main className="relative flex min-w-0 flex-1 flex-col overflow-clip" aria-label="Chat">
        <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-4 py-2">
          <IconButton label="Back to Nori" size={32} onClick={chat.close}><ArrowLeft size={16} strokeWidth={1.5} /></IconButton>
          <h1 className="min-w-0 flex-1 truncate text-row-title font-medium text-ink">{chat.current?.title ?? "New chat"}</h1>
          {chat.current && <IconButton label="Delete chat" size={32} disabled={!!chat.current.activeTurn} onClick={() => void chat.remove()}><Trash2 size={16} strokeWidth={1.5} /></IconButton>}
        </div>
        <div className="min-h-0 flex-1">
          <Thread
            key={chat.current?.id ?? "new"}
            conversation={chatConversation}
            onOpenDocument={(item, body) => setDocument({ body, at: item.at })}
            status={chat.current?.activeTurn ? "Responding…" : undefined}
            onUnread={onUnread}
            emptyMessage={chat.opening ? "Loading your conversation…" : "What would you like to talk about?"}
            hasOlder={!!chat.current?.nextBefore}
            loadingOlder={chat.loadingOlder}
            onLoadOlder={() => void chat.loadOlder()}
          />
        </div>
        {(chat.error || !chat.connected) && (
          <div className="nova-runtime-notice w-full shrink-0 self-center px-6 pb-3">
            <div role={chat.error ? "alert" : "status"} className="rounded-tile bg-surface p-3 text-meta text-ink-2 shadow-pill">{chat.error || "Reconnecting…"}</div>
          </div>
        )}
        <RuntimeComposer
          accept=".csv,text/csv"
          draft={chat.draft}
          setDraft={chat.setDraft}
          disabled={chat.sending || chat.opening}
          onSend={() => void chat.send()}
          onStop={chat.current?.activeTurn ? () => void chat.stop() : undefined}
          files={chat.files}
          onAttach={chat.attach}
          onRetryFile={(file) => void chat.retryFile(file)}
          onRemoveFile={chat.removeFile}
          unread={unread.count}
          onJump={unread.jump}
        />
      </main> : <main className="relative flex min-w-0 flex-1 flex-col overflow-clip" aria-label="Chat">
        <div className="min-h-0 flex-1">
          <Thread
            conversation={conversation}
            onStatusDetails={() =>
              wide ? setDockOpen(true) : setOverlayOpen(true)
            }
            onOpenDocument={(item, body) => setDocument({ body, at: item.at })}
            status={work}
            onUnread={onUnread}
            onShown={runtime.markShown}
            readPosition={session?.read_position}
            hasOlder={!!session?.next_cursor}
            loadingOlder={runtime.loadingOlder}
            onLoadOlder={() => void runtime.loadOlder()}
            emptyMessage={
              loading
                ? "Loading your conversation…"
                : !session
                  ? "Your conversation will appear when Nori reconnects."
                  : "Nori is getting ready to say hello…"
            }
          />
        </div>
        {pending.filter((item) => item.error).map((item) => {
          const label = item.text || item.attachments?.map((file) => file.name).join(", ") || "your message";
          return (
          <div key={item.id} role="alert" className="mx-6 mb-2 rounded-tile bg-surface p-3 text-meta text-ink-2">
            Could not confirm “{label.slice(0, 80)}{label.length > 80 ? "…" : ""}”. {item.error}{" "}
            <button type="button" className="font-medium text-accent-text hover:underline" disabled={loading} onClick={() => void runtime.retry(item)}>Retry message</button>{" "}
            <button type="button" className="font-medium text-ink-2 hover:underline" onClick={() => runtime.dismissPending(item.id)}>Dismiss</button>
          </div>
          );
        })}
        {notice && (
          <div className="nova-runtime-notice w-full shrink-0 self-center px-6 pb-3">
            <div
              role={error ? "alert" : "status"}
              className="rounded-tile bg-surface p-3 text-meta text-ink-2 shadow-pill"
            >
              {notice}{" "}
              {error && (
                <button
                  type="button"
                  className="font-medium text-accent-text hover:underline"
                  disabled={loading}
                  onClick={() => void runtime.refreshHistory()}
                >
                  Refresh history to reconnect
                </button>
              )}
            </div>
          </div>
        )}
        <RuntimeComposer
          draft={runtime.draft}
          setDraft={runtime.setDraft}
          disabled={!session || loading || !!session.conversation?.introducing}
          onSend={() => void runtime.send()}
          files={runtime.draftFiles}
          onAttach={runtime.attachFiles}
          onRetryFile={(file) => void runtime.retryFile(file)}
          onRemoveFile={runtime.removeFile}
          unread={unread.count}
          onJump={unread.jump}
        />
      </main>}
      {wide && dockOpen && (
        <aside
          aria-label="Agent panel"
          className="hairline-l h-full shrink-0 overflow-clip"
          style={{ width: "var(--panel-w)" }}
        >
          {panel}
        </aside>
      )}
      <DocumentViewer document={document} onClose={() => setDocument(null)} />
      {!wide && (
        <Dialog.Root open={overlayOpen} onOpenChange={setOverlayOpen}>
          <Dialog.Portal>
            <Dialog.Overlay className="scrim fixed inset-0 z-40 bg-overlay" />
            <Dialog.Content
              aria-describedby={undefined}
              onCloseAutoFocus={(event) => {
                event.preventDefault();
                activityButton.current?.focus();
              }}
              className="sheet-right fixed inset-y-0 right-0 z-40 max-w-full shadow-pop"
              style={{ width: "var(--panel-w)" }}
            >
              <Dialog.Title className="sr-only">Agent panel</Dialog.Title>
              {panel}
            </Dialog.Content>
          </Dialog.Portal>
        </Dialog.Root>
      )}
    </div>
    </>
  );
}
