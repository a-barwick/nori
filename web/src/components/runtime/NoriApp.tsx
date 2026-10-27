"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { MotionConfig } from "motion/react";
import { Dialog, Tooltip } from "radix-ui";
import { Activity, Bug, RefreshCw } from "lucide-react";
import { Thread } from "@/components/chat/Thread";
import { NovaShell } from "@/components/nova/NovaShell";
import { IconButton } from "@/components/ui/primitives";
import { useHydrated, useWide } from "@/lib/hooks";
import { useRuntime } from "@/lib/poc/useRuntime";
import { recordedDebug } from "@/components/dev/debug";
import type { Thread as Conversation, ThreadItem } from "@/lib/types";
import { DocumentViewer } from "./DocumentViewer";
import { RuntimeComposer } from "./RuntimeComposer";
import { TaskPanel } from "./TaskPanel";

export function NoriApp({ agentMode = false }: { agentMode?: boolean }) {
  const hydrated = useHydrated();
  if (!hydrated) return <div className="fixed inset-0 bg-canvas" />;
  return (
    <Tooltip.Provider delayDuration={500}>
      <MotionConfig reducedMotion="user">
        <RuntimeChat agentMode={agentMode} />
      </MotionConfig>
    </Tooltip.Provider>
  );
}

function RuntimeChat({ agentMode }: { agentMode: boolean }) {
  const runtime = useRuntime(agentMode);
  const [debug, setDebug] = useState(false);
  const {
    session: liveSession,
    pending,
    running,
    activeId,
    selected,
    busy,
    loading,
    error,
    stopIntent,
    stopPending,
  } = runtime;
  const session = liveSession;
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
  const stopping =
    stopPending || (!!activeId && stopIntent === activeId) || !!running?.cancel_requested;
  const stopDisabled =
    stopPending ||
    (!error && !!activeId && stopIntent === activeId) ||
    !!running?.cancel_requested;
  const conversation = useMemo<Conversation>(() => {
    const generatedRequests = new Set(session?.dispatches?.map((dispatch) => `${dispatch.id}-user`));
    const items: ThreadItem[] = (session?.messages ?? []).filter((message) => !(message.run_id && message.id === `${message.run_id}-user` && message.turn_id) && !generatedRequests.has(message.id)).map((message) => ({
      id: message.id,
      kind: "text",
      sender: message.role === "user" ? "me" : "nori",
      at: Date.parse(message.created_at),
      text: message.text,
      debug: debug && message.role === "assistant"
        ? recordedDebug(session?.runs.find((run) => run.id === message.run_id), session?.traces?.filter((trace) => trace.turn_id === (message.turn_id || message.id.replace(/-assistant$/, ""))), agentMode, message.turn_id)
        : undefined,
    }));
    for (const item of pending) {
      if (!session?.messages.some((message) => message.id === `${item.id}-user`)) items.push({
        id: `${item.id}-user`, kind: "text", sender: "me", at: item.started, text: item.text,
      });
    }
    items.sort((a, b) => a.at - b.at);
    return { id: "runtime", title: "Nori", kind: "main", createdAt: items[0]?.at ?? 0, items };
  }, [session?.messages, session?.runs, session?.dispatches, session?.traces, pending, debug, agentMode]);

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
      busy={!!running}
      loading={loading}
      error={error}
      unavailable={runtime.tasksUnavailable}
      stopping={stopping}
      stopDisabled={stopDisabled}
      onSelect={runtime.setSelectedId}
      onStop={runtime.stop}
      onRefresh={() => void runtime.refreshHistory()}
      onClose={closePanel}
      artifactEndpoint={agentMode ? "/api/agent/artifact" : undefined}
      onUpdate={runtime.updateTask}
    />
  );
  const notice = error;
  const work = runtime.conversationBusy ? "Nori is thinking…" : pending.some((item) => !item.error) ? "Sending your message…" : running
    ? stopping ? "Stop requested. Waiting for the runtime to confirm."
      : runtime.tasksUnavailable ? "Task execution is paused. You can still chat with Nori."
      : running.waiting ? `Nori is waiting for your answer: ${running.waiting}` : "Nori is working."
    : undefined;

  return (
    <NovaShell
      state={busy ? "thinking" : runtime.draft.trim() ? "attentive" : "idle"}
      status={loading ? "Connecting…" : error || !session ? "Disconnected" : running?.waiting ? "Waiting for you" : busy ? "Working…" : "Ready when you are"}
      activityOpen={panelOpen}
      onToggleActivity={(event) => { activityButton.current = event.currentTarget; togglePanel(); }}
      actions={
      <>
        <IconButton label={debug ? "Hide debug info" : "Show debug info"} size={32} onClick={() => setDebug((on) => !on)}>
          <Bug size={16} strokeWidth={1.5} className={debug ? "text-accent-text" : undefined} />
        </IconButton>
        <IconButton label={loading ? "Loading history" : "Refresh history"} size={32} disabled={loading} onClick={() => void runtime.refreshHistory()}>
          <RefreshCw size={16} strokeWidth={1.5} />
        </IconButton>
        <button ref={activityButton} type="button" className="nova-activity-button" aria-label="Activity" aria-expanded={panelOpen} onClick={(event) => { activityButton.current = event.currentTarget; togglePanel(); }}>
          <Activity size={16} /><span>Activity</span>{busy && <span className="nova-working-dot" />}
        </button>
      </>
    }>
    <div className="nova-live-body flex min-h-0 flex-1 overflow-clip text-ink">
      <main className="relative flex min-w-0 flex-1 flex-col overflow-clip" aria-label="Chat">
        <div className="min-h-0 flex-1">
          <Thread
            conversation={conversation}
            onStatusDetails={() =>
              wide ? setDockOpen(true) : setOverlayOpen(true)
            }
            onOpenDocument={(item, body) => setDocument({ body, at: item.at })}
            status={work}
            onUnread={onUnread}
            presence={false}
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
        {pending.filter((item) => item.error).map((item) => (
          <div key={item.id} role="alert" className="mx-6 mb-2 rounded-tile bg-surface p-3 text-meta text-ink-2">
            Could not confirm “{item.text.slice(0, 80)}{item.text.length > 80 ? "…" : ""}”. {item.error}{" "}
            <button type="button" className="font-medium text-accent-text hover:underline" onClick={() => void runtime.retry(item)}>Retry message</button>
          </div>
        ))}
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
          busy={busy}
          canUpdate={runtime.canUpdate}
          canChat={runtime.canChat}
          stopping={stopping && !error}
          stopDisabled={stopDisabled}
          onSend={() => void runtime.send()}
          onStop={running ? runtime.stop : undefined}
          unread={unread.count}
          onJump={unread.jump}
        />
      </main>
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
    </NovaShell>
  );
}
