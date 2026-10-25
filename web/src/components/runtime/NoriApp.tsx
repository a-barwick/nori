"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { MotionConfig } from "motion/react";
import { Dialog, Tooltip } from "radix-ui";
import { RefreshCw } from "lucide-react";
import { Thread } from "@/components/chat/Thread";
import { Medallion } from "@/components/mark/NoriMark";
import { IconButton } from "@/components/ui/primitives";
import { useHydrated, useWide } from "@/lib/hooks";
import { useRuntime } from "@/lib/poc/useRuntime";
import type { Thread as Conversation, ThreadItem } from "@/lib/types";
import { DocumentViewer } from "./DocumentViewer";
import { RuntimeComposer } from "./RuntimeComposer";
import { TaskPanel } from "./TaskPanel";

export function NoriApp() {
  const hydrated = useHydrated();
  if (!hydrated) return <div className="fixed inset-0 bg-canvas" />;
  return (
    <Tooltip.Provider delayDuration={500}>
      <MotionConfig reducedMotion="user">
        <RuntimeChat />
      </MotionConfig>
    </Tooltip.Provider>
  );
}

function RuntimeChat() {
  const runtime = useRuntime();
  const {
    session,
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
  const wide = useWide();
  const [document, setDocument] = useState<{ body: string; at: number } | null>(
    null,
  );
  const [dockOpen, setDockOpen] = useState(false);
  const [overlayOpen, setOverlayOpen] = useState(false);
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
      (message) => message.run_id === pending.id && message.role === "user",
    );
  const conversation = useMemo<Conversation>(() => {
    const items: ThreadItem[] = (session?.messages ?? []).map((message) => ({
      id: message.id,
      kind: "text",
      sender: message.role === "user" ? "me" : "nori",
      at: Date.parse(message.created_at),
      text: message.text,
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
  }, [session?.messages, pending, pendingNotPersisted]);

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
      unavailable={unavailable}
      stopping={stopping}
      stopDisabled={stopDisabled}
      onSelect={runtime.setSelectedId}
      onStop={runtime.stop}
      onRefresh={() => void runtime.refreshHistory()}
      onClose={closePanel}
    />
  );
  const notice =
    error ||
    (unavailable
      ? `${session?.paused ? "This session is paused." : "This session has expired."} History remains available.`
      : "");
  const work = busy
    ? stopping
      ? "Stop requested. Waiting for the runtime to confirm."
      : pending && !running
        ? "Sending task to Nori…"
        : "Nori is working."
    : undefined;

  return (
    <div className="fixed inset-0 flex overflow-clip bg-canvas text-ink">
      <main className="relative min-w-0 flex-1 overflow-clip" aria-label="Chat">
        <div
          className={notice ? "absolute inset-x-0 top-0 bottom-20" : "h-full"}
        >
          <Thread
            threadId="runtime"
            conversation={conversation}
            readOnly
          onStatusDetails={() => wide ? setDockOpen(true) : setOverlayOpen(true)}
            onOpenDocument={(item, body) => setDocument({ body, at: item.at })}
            status={work}
            onUnread={onUnread}
            presence={!panelOpen}
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
          <div className="pointer-events-none absolute inset-x-0 top-3 z-10 flex items-start px-4">
            <div
              aria-hidden
              className="top-frost absolute inset-x-0 -top-3"
              style={{ height: panelOpen ? 44 : 92 }}
            />
            {!panelOpen && (
              <button
                type="button"
                onClick={togglePanel}
                aria-label="Nori: open agent panel"
                className="anim-fade pointer-events-auto absolute left-1/2 flex -translate-x-1/2 flex-col items-center rounded-full"
              >
                <Medallion
                  size={52}
                  state={
                    busy
                      ? "thinking"
                      : runtime.draft.trim()
                        ? "attentive"
                        : "idle"
                  }
                  look="onigiri"
                  watch
                />
                <span className="glass -mt-2.5 rounded-full px-2.5 py-0.5 text-caption font-medium text-ink">
                  Nori
                </span>
              </button>
            )}
          </div>
        </div>
        <div className="absolute top-3 right-3 z-20">
          <IconButton
            label={loading ? "Loading history" : "Refresh history"}
            size={32}
            disabled={loading}
            onClick={() => void runtime.refreshHistory()}
          >
            <RefreshCw size={16} strokeWidth={1.5} />
          </IconButton>
        </div>
        {notice && (
          <div className="absolute inset-x-0 bottom-[88px] z-10 mx-auto max-w-[768px] px-6">
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
          disabled={!session || unavailable || loading || !!error}
          busy={busy}
          stopping={stopping && !error}
          stopDisabled={stopDisabled}
          onSend={() => void runtime.send()}
          onStop={runtime.stop}
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
  );
}
