"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useMemo, useState } from "react";
import { Notice } from "@/components/assistant/ui";
import { Thread } from "@/components/chat/Thread";
import type { ModelKeysResponse, ModelSettings } from "@/lib/assistant/api";
import { useAssistant, useChatDraft } from "@/lib/assistant/context";
import { isChatInlineInteraction, mergeInlineInteractions, type InlineInteractionEntry } from "@/lib/assistant/interactions";
import { hasSelectedHomeChat, needsByokSetup } from "@/lib/assistant/modelSetup";
import { usePendingInteractions } from "@/lib/assistant/sources";
import { useResource } from "@/lib/assistant/useResource";
import type { Interaction } from "@/lib/contracts/assistant";
import type { Message, Part, Thread as Conversation, TextItem } from "@/lib/types";
import { DocumentViewer } from "./DocumentViewer";
import { RuntimeComposer } from "./RuntimeComposer";

function partURL(part: Part) {
  if (part.type === "attachment" && part.id) return `/api/v1/uploads/${part.id}`;
  if ((part.type === "artifact" || part.type === "image") && part.id) return `/api/v1/results/${part.id}/file`;
  return part.url;
}

function partsOf(message: Pick<Message, "text" | "parts">) {
  const raw: Part[] = message.parts?.length ? message.parts : [{ type: "text", text: message.text }];
  const mapped = raw.map((part) => ({ ...part, url: partURL(part) }));
  const textParts = mapped.filter((part) => part.type === "text");
  const hasPage = mapped.some((part) => part.type === "page" || part.display === "page");
  const hasBrief = mapped.some((part) => part.type === "brief");
  return {
    text: textParts.map((part) => part.text ?? "").join("\n\n") || (hasPage || hasBrief ? "" : message.text),
    display: mapped.find((part) => part.display)?.display,
    parts: hasPage ? mapped : mapped.filter((part) => part.type !== "text"),
  };
}

function ConnectedRuntimeComposer({
  accept,
  disabled,
  unread,
  onJump,
}: {
  accept?: string;
  disabled: boolean;
  unread: number;
  onJump: () => void;
}) {
  const { runtime } = useAssistant();
  const { draft, draftFiles } = useChatDraft();
  return (
    <RuntimeComposer
      accept={accept}
      draft={draft}
      setDraft={runtime.setDraft}
      disabled={disabled}
      onSend={() => void runtime.send()}
      files={draftFiles}
      onAttach={runtime.attachFiles}
      onRetryFile={(file) => void runtime.retryFile(file)}
      onRemoveFile={runtime.removeFile}
      unread={unread}
      onJump={onJump}
    />
  );
}

export function ChatScreen() {
  const router = useRouter();
  const { runtime, debug, capabilities } = useAssistant();
  const accepted = capabilities?.upload_accept;
  const uploadAccept = accepted ? [...accepted.media_types, ...(accepted.extensions ?? [])].join(",") : undefined;
  const { session, pending, activeTasks, loading, error, reconnecting, retryTurn, loadOlder } = runtime;
  const pendingInteractions = usePendingInteractions();
  const modelKeys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 60_000, versioned: false });
  const models = useResource<ModelSettings>("/settings/models", { poll: 60_000 });
  const requiresByok = needsByokSetup(modelKeys.data, hasSelectedHomeChat(models.data));
  const [sessionResolved, setSessionResolved] = useState<Record<string, InlineInteractionEntry>>({});
  const handleInteractionResolved = useCallback((resolved: Interaction, meta?: { rememberRule?: boolean }) => {
    setSessionResolved((current) => ({
      ...current,
      [resolved.id]: { interaction: resolved, rememberedRule: meta?.rememberRule },
    }));
  }, []);
  const inlineInteractions = useMemo(
    () => mergeInlineInteractions((pendingInteractions.items ?? []).filter(isChatInlineInteraction), sessionResolved),
    [pendingInteractions.items, sessionResolved],
  );
  const [document, setDocument] = useState<{ body: string; at: number; title?: string } | null>(null);
  const [unread, setUnread] = useState<{ count: number; jump: () => void }>({
    count: 0,
    jump: () => {},
  });
  const onUnread = useCallback((count: number, jump: () => void) => setUnread({ count, jump }), []);
  const handleStatusDetails = useCallback(() => router.push("/work"), [router]);
  const handleOpenDocument = useCallback(
    (item: TextItem, body: string, title?: string) => setDocument({ body, at: item.at, title }),
    [],
  );
  const handleRetryMessage = useCallback(
    (item: TextItem) => void retryTurn({ turn_id: item.turnId, message_id: item.id }),
    [retryTurn],
  );
  const handleLoadOlder = useCallback(() => void loadOlder(), [loadOlder]);

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

  const hasVisibleFailedCard = conversation.items.some((item) => item.sender === "nori" && item.status === "failed");
  const notice = reconnecting
    ? "Can't reach Nori right now. Reconnecting on its own…"
    : error ||
      (session?.conversation?.routing_blocked && !runtime.conversationBusy && !hasVisibleFailedCard
        ? "Some work is waiting for this message to be resolved. Send a follow-up to retry or clarify."
        : "");
  const workingTasks = activeTasks.filter((run) => !(run.question ? !run.question.resolved_by : run.waiting));
  const singleWorkingLabel =
    workingTasks.length === 1
      ? workingTasks[0].progress?.trim() || "Working in the background…"
      : `${workingTasks.length} working`;
  const work = runtime.conversationBusy
    ? "Nori is thinking…"
    : workingTasks.length
      ? runtime.tasksUnavailable
        ? "Task execution is paused. You can still chat with Nori."
        : singleWorkingLabel
      : undefined;

  return (
    <main className="relative flex min-h-0 flex-1 flex-col overflow-clip bg-canvas text-ink" aria-label="Chat">
      <h1 className="sr-only">Chat</h1>
      <div className="min-h-0 flex-1">
        <Thread
          conversation={conversation}
          onStatusDetails={handleStatusDetails}
          onOpenDocument={handleOpenDocument}
          status={work}
          thinking={runtime.conversationBusy}
          interactions={inlineInteractions}
          onInteractionResolved={handleInteractionResolved}
          onRetryMessage={handleRetryMessage}
          retryingMessageId={runtime.retryingMessageId}
          onUnread={onUnread}
          onShown={runtime.markShown}
          readPosition={session?.read_position}
          hasOlder={!!session?.next_cursor}
          loadingOlder={runtime.loadingOlder}
          onLoadOlder={handleLoadOlder}
          emptyMessage={
            loading
              ? "Loading conversation…"
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
              <div role="alert" className="max-w-[680px] rounded-tile border border-card-border bg-surface p-3 text-meta text-ink-2">
                Could not confirm “{label.slice(0, 80)}
                {label.length > 80 ? "…" : ""}”. {item.error}{" "}
                {(item.errorCode === "model_key_required" || item.errorCode === "missing_api_key") && (
                  <><Link href="/settings/models" className="font-semibold text-accent-text hover:underline">Open Models</Link>{" "}</>
                )}
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
                <Link href="/settings/models" className="font-semibold underline underline-offset-2">
                  Choose model
                </Link>
              }
            >
              Choose a model in Settings to use Nori.
            </Notice>
          </div>
        </div>
      )}

      {notice && (
        <div className="nori-page-col shrink-0 pb-2">
          <div
            role={error && !reconnecting ? "alert" : "status"}
            className="max-w-[680px] rounded-tile border border-card-border bg-wash-top px-3.5 py-2.5 text-meta text-ink-2"
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

      <ConnectedRuntimeComposer
        accept={uploadAccept}
        disabled={!session || loading || !!session.conversation?.introducing}
        unread={unread.count}
        onJump={unread.jump}
      />

      <DocumentViewer document={document} onClose={() => setDocument(null)} />
    </main>
  );
}
