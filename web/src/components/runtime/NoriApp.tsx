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
    unavailable,
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
    stopPending || stopIntent === activeId || !!running?.cancel_requested;
  const stopDisabled =
    stopPending ||
    (!error && stopIntent === activeId) ||
    !!running?.cancel_requested;
  const pendingNotPersisted =
    pending &&
    !session?.messages.some(
      (message) =>
        message.id === `${pending.id}-user` && message.role === "user",
    );
  const conversation = useMemo<Conversation>(() => {
    const generatedRequests = new Set(session?.dispatches?.map((dispatch) => `${dispatch.id}-user`));
    const items: ThreadItem[] = (session?.messages ?? []).filter((message) => !generatedRequests.has(message.id)).map((message) => ({
      id: message.id,
      kind: "text",
      sender: message.role === "user" ? "me" : "nori",
      at: Date.parse(message.created_at),
      text: message.text,
      debug:
        debug && message.role === "assistant"
          ? recordedDebug(session?.runs.find((run) => run.id === message.run_id))
          : undefined,
    }));
    if (pendingNotPersisted && pending)
      items.push({
        id: pending.id,
        kind: "text",
        sender: "me",
        at: pending.started,
        text: pending.text,
      });
    return {
      id: "runtime",
      title: "Nori",
      kind: "main",
      createdAt: items[0]?.at ?? 0,
      items,
    };
  }, [session?.messages, session?.runs, session?.dispatches, pending, pendingNotPersisted, debug]);

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
      pending={!!pending}
      busy={busy}
      loading={loading}
      error={error}
      unavailable={agentMode ? runtime.tasksUnavailable : unavailable}
      stopping={stopping}
      stopDisabled={stopDisabled}
      onSelect={runtime.setSelectedId}
      onStop={runtime.stop}
      onRefresh={() => void runtime.refreshHistory()}
      onClose={closePanel}
      artifactEndpoint={agentMode ? "/api/agent/artifact" : undefined}
      onUpdate={agentMode ? runtime.updateTask : undefined}
    />
  );
  const notice =
    error ||
    (unavailable
      ? "Task execution is paused. History remains available."
      : "");
  const work = agentMode && pending && pending.id === pending.runId ? "Nori is thinking…" : busy
    ? stopping
      ? "Stop requested. Waiting for the runtime to confirm."
      : pending
        ? pending.id === pending.runId
          ? "Sending task to Nori…"
          : "Sending your update…"
        : running?.waiting
          ? `Nori is waiting for your answer: ${running.waiting}`
          : "Nori is working."
    : undefined;

  return (
    <NovaShell
      state={busy ? "thinking" : runtime.draft.trim() ? "attentive" : "idle"}
      status={loading ? "Connecting…" : error || !session ? "Disconnected" : unavailable ? "Paused" : running?.waiting ? "Waiting for you" : busy ? "Working…" : "Ready when you are"}
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
            emptyMessage={
              loading
                ? "Loading your conversation…"
                : !session
                  ? "Your conversation will appear when Nori reconnects."
                  : unavailable
                    ? "Your saved conversation appears here."
                    : undefined
            }
          />
        </div>
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
          disabled={!session || unavailable || loading || (!!error && !agentMode)}
          busy={busy}
          canUpdate={runtime.canUpdate}
          canChat={runtime.canChat}
          stopping={stopping && !error}
          stopDisabled={stopDisabled}
          onSend={() => void runtime.send()}
          onStop={agentMode && !running ? undefined : runtime.stop}
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
