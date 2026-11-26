"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Dialog } from "radix-ui";
import { byokPromptLabel, needsByokSetup } from "@/components/assistant/ApiKeysSettings";
import { Notice } from "@/components/assistant/ui";
import { Thread } from "@/components/chat/Thread";
import type { ModelKeysResponse } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { mergeInlineInteractions, type InlineInteractionEntry } from "@/lib/assistant/interactions";
import { usePendingInteractions } from "@/lib/assistant/sources";
import { useResource } from "@/lib/assistant/useResource";
import type { Interaction } from "@/lib/contracts/assistant";
import { SERVICE_UNAVAILABLE, STREAM_RECONNECTING } from "@/lib/runtime/messages";
import type { Message, Part, Thread as Conversation, TextItem } from "@/lib/types";
import { DocumentViewer } from "./DocumentViewer";
import { RuntimeComposer } from "./RuntimeComposer";
import { TaskPanel } from "./TaskPanel";

function partURL(part: Part) {
  if (part.type === "attachment" && part.id) return `/api/v1/uploads/${part.id}`;
  if ((part.type === "artifact" || part.type === "image") && part.id) return `/api/v1/results/${part.id}/file`;
  return part.url;
}

function partsOf(message: Pick<Message, "text" | "parts">) {
  const parts: Part[] = message.parts?.length ? message.parts : [{ type: "text", text: message.text }];
  const text = parts.filter((part) => part.type === "text");
  return {
    text: text.map((part) => part.text ?? "").join("\n\n"),
    display: text.find((part) => part.display)?.display,
    parts: parts.filter((part) => part.type !== "text").map((part) => ({ ...part, url: partURL(part) })),
  };
}

export function ChatScreen() {
  const { runtime, debug, capabilities } = useAssistant();
  const accepted = capabilities?.upload_accept;
  const uploadAccept = accepted ? [...accepted.media_types, ...(accepted.extensions ?? [])].join(",") : undefined;
  const { session, pending, activeTasks, selected, loading, error } = runtime;
  const pendingInteractions = usePendingInteractions();
  const modelKeys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 60_000, versioned: false });
  const requiresByok = needsByokSetup(modelKeys.data);
  const [sessionResolved, setSessionResolved] = useState<Record<string, InlineInteractionEntry>>({});
  const handleInteractionResolved = useCallback((resolved: Interaction, meta?: { rememberRule?: boolean }) => {
    setSessionResolved((current) => ({
      ...current,
      [resolved.id]: { interaction: resolved, rememberedRule: meta?.rememberRule },
    }));
  }, []);
  const inlineInteractions = useMemo(
    () => mergeInlineInteractions(pendingInteractions.items ?? [], sessionResolved),
    [pendingInteractions.items, sessionResolved],
  );
  const [document, setDocument] = useState<{ body: string; at: number } | null>(null);
  const [activityOpen, setActivityOpen] = useState(false);
  const closePanel = useCallback(() => setActivityOpen(false), []);
  const togglePanel = useCallback(() => setActivityOpen((open) => !open), []);
  const [unread, setUnread] = useState<{ count: number; jump: () => void }>({
    count: 0,
    jump: () => {},
  });
  const onUnread = useCallback((count: number, jump: () => void) => setUnread({ count, jump }), []);

  const conversation = useMemo<Conversation>(() => {
    const generatedRequests = new Set(session?.dispatches?.map((dispatch) => `${dispatch.id}-user`));
    const items: TextItem[] = (session?.messages ?? [])
      .filter(
        (message) =>
          message.status !== "superseded" &&
          !(message.run_id && message.id === `${message.run_id}-user` && message.turn_id) &&
          !generatedRequests.has(message.id),
      )
      .map((message) => ({
        id: message.id,
        sender: message.role === "user" ? "me" : "nori",
        at: Date.parse(message.created_at),
        status: message.status,
        error: message.error,
        turnId: message.turn_id,
        ...partsOf(message),
        debug:
          debug && message.role === "assistant"
            ? {
                run: session?.runs.find((run) => run.id === message.run_id),
                turns: session?.traces?.filter(
                  (trace) => trace.turn_id === (message.turn_id || message.id.replace(/-assistant$/, "")),
                ),
                turnId: message.turn_id,
              }
            : undefined,
      }));
    const lastNori = items.findLast((item) => item.sender === "nori" && item.status !== "failed");
    const lastMine = items.findLast((item) => item.sender === "me");
    if (
      lastMine &&
      (!lastNori || lastNori.at < lastMine.at) &&
      (session?.conversation?.pending || session?.conversation?.processing)
    ) {
      lastMine.receipt = "saved";
    }
    for (const item of pending) {
      if (!session?.messages.some((message) => message.id === `${item.id}-user`)) {
        items.push({
          id: `${item.id}-user`,
          sender: "me",
          at: item.started,
          text: item.text,
          receipt: item.error ? undefined : "sending",
          parts: item.attachments?.map((file) => ({
            type: "attachment",
            ...file,
            url: `/api/v1/uploads/${file.id}`,
          })),
        });
      }
    }
    items.sort((a, b) => a.at - b.at);
    return { title: "Nori", items };
  }, [
    session?.messages,
    session?.runs,
    session?.dispatches,
    session?.traces,
    session?.conversation?.pending,
    session?.conversation?.processing,
    pending,
    debug,
  ]);

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
      commands={runtime.taskCommands}
      drafts={runtime.taskDrafts}
      onDraft={runtime.setTaskDraft}
      onCommand={runtime.commandTask}
      onRetryCommand={runtime.retryTaskCommand}
      onDismissCommand={runtime.dismissTaskCommand}
    />
  );

  const reconnecting = error === SERVICE_UNAVAILABLE || error === STREAM_RECONNECTING;
  const hasVisibleFailedCard = conversation.items.some((item) => item.sender === "nori" && item.status === "failed");
  const notice = reconnecting
    ? "Can't reach Nori right now. Reconnecting on its own…"
    : error ||
      (session?.conversation?.routing_blocked && !runtime.conversationBusy && !hasVisibleFailedCard
        ? "Some work is waiting for this message to be resolved. Send a follow-up to retry or clarify."
        : "");
  const waitingCount = activeTasks.filter((run) => (run.question ? !run.question.resolved_by : run.waiting)).length;
  const work = runtime.conversationBusy
    ? "Nori is thinking…"
    : activeTasks.length
      ? runtime.tasksUnavailable
        ? "Task execution is paused. You can still chat with Nori."
        : `${activeTasks.length} ${activeTasks.length === 1 ? "task" : "tasks"} running${waitingCount ? ` · ${waitingCount} needing action` : ""}`
      : undefined;

  return (
    <main className="relative flex min-h-0 flex-1 flex-col overflow-clip bg-canvas text-ink" aria-label="Chat">
      <h1 className="sr-only">Chat</h1>
      <div className="min-h-0 flex-1">
        <Thread
          conversation={conversation}
          onStatusDetails={() => setActivityOpen(true)}
          onOpenDocument={(item, body) => setDocument({ body, at: item.at })}
          status={work}
          thinking={runtime.conversationBusy}
          streamingText={runtime.streamingDelta?.text}
          interactions={inlineInteractions}
          onInteractionResolved={handleInteractionResolved}
          onRetryMessage={(item) => void runtime.retryTurn({ turn_id: item.turnId, message_id: item.id })}
          retryingMessageId={runtime.retryingMessageId}
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
                : "Ask Nori anything…"
          }
        />
      </div>

      {pending
        .filter((item) => item.error)
        .map((item) => {
          const label = item.text || item.attachments?.map((file) => file.name).join(", ") || "your message";
          return (
            <div key={item.id} className="nori-page-col shrink-0 pb-2">
              <div role="alert" className="max-w-[680px] rounded-tile border border-hairline bg-surface p-3 text-meta text-ink-2">
                Could not confirm “{label.slice(0, 80)}
                {label.length > 80 ? "…" : ""}”. {item.error}{" "}
                <button
                  type="button"
                  className="font-semibold text-accent-text hover:underline"
                  disabled={loading}
                  onClick={() => void runtime.retry(item)}
                >
                  Retry message
                </button>{" "}
                <button
                  type="button"
                  className="font-medium text-ink-2 hover:underline"
                  onClick={() => runtime.dismissPending(item.id)}
                >
                  Dismiss
                </button>
              </div>
            </div>
          );
        })}

      {requiresByok && (
        <div className="nori-page-col shrink-0 pb-2">
          <div className="max-w-[680px]">
            <Notice
              tone="warning"
              action={
                <Link href="/settings/api-keys" className="font-semibold underline underline-offset-2">
                  Add API key
                </Link>
              }
            >
              Add your {byokPromptLabel(modelKeys.data)} API key in Settings to use Nori.
            </Notice>
          </div>
        </div>
      )}

      {notice && (
        <div className="nori-page-col shrink-0 pb-2">
          <div
            role={error && !reconnecting ? "alert" : "status"}
            className="max-w-[680px] rounded-tile border border-hairline bg-wash-top px-3.5 py-2.5 text-meta text-ink-2"
          >
            {notice}{" "}
            {error && (
              <button
                type="button"
                className="font-semibold text-accent-text hover:underline"
                disabled={loading}
                onClick={() => void runtime.refreshHistory()}
              >
                {reconnecting ? "Try now" : "Refresh to reconnect"}
              </button>
            )}
          </div>
        </div>
      )}

      <RuntimeComposer
        accept={uploadAccept}
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

      <DocumentViewer document={document} onClose={() => setDocument(null)} />

      <Dialog.Root open={activityOpen} onOpenChange={setActivityOpen}>
        <Dialog.Portal>
          <Dialog.Overlay className="scrim fixed inset-0 z-40 bg-overlay sm:top-[60px]" />
          <Dialog.Content
            aria-describedby={undefined}
            className="sheet-right fixed inset-y-0 right-0 z-40 w-[min(460px,100vw)] border-l border-hairline bg-sheet shadow-sheet sm:top-[60px]"
          >
            <Dialog.Title className="sr-only">Task activity</Dialog.Title>
            {panel}
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </main>
  );
}
