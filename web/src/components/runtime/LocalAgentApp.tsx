"use client";

import { useCallback, useMemo, useState } from "react";
import { MotionConfig } from "motion/react";
import { Dialog, Tooltip } from "radix-ui";
import { RefreshCw, X } from "lucide-react";
import { Thread } from "@/components/chat/Thread";
import { Medallion } from "@/components/mark/NoriMark";
import { IconButton } from "@/components/ui/primitives";
import { useAgent } from "@/lib/agent";
import { useHydrated, useWide } from "@/lib/hooks";
import type { Thread as Conversation, ThreadItem } from "@/lib/types";
import { DocumentViewer } from "./DocumentViewer";
import { RuntimeComposer } from "./RuntimeComposer";

export function LocalAgentApp() {
  const hydrated = useHydrated();
  if (!hydrated) return <div className="fixed inset-0 bg-canvas" />;
  return (
    <Tooltip.Provider delayDuration={500}>
      <MotionConfig reducedMotion="user"><AgentChat /></MotionConfig>
    </Tooltip.Provider>
  );
}

function AgentChat() {
  const agent = useAgent();
  const wide = useWide();
  const [panelOpen, setPanelOpen] = useState(false);
  const [document, setDocument] = useState<{ body: string; at: number } | null>(null);
  const [unread, setUnread] = useState({ count: 0, jump: () => {} });
  const onUnread = useCallback((count: number, jump: () => void) => setUnread({ count, jump }), []);
  const busy = !!agent.pending;
  const dispatches = agent.session?.dispatches ?? [];
  const conversation = useMemo<Conversation>(() => {
    const pending = agent.pending;
    const items: ThreadItem[] = (agent.session?.messages ?? []).map((message) => ({
      id: message.id, kind: "text", sender: message.role === "user" ? "me" : "nori",
      text: message.text, at: Date.parse(message.created_at),
    }));
    if (pending && !items.some((item) => item.id === `${pending.id}-user`)) {
      items.push({ id: `${pending.id}-user`, kind: "text", sender: "me", text: pending.text, at: pending.at });
    }
    return { id: "local-agent", title: "Nori", kind: "main", createdAt: items[0]?.at ?? 0, items };
  }, [agent.session?.messages, agent.pending]);
  const status = busy ? "Nori is thinking…" : dispatches.length
    ? `${dispatches.length} ${dispatches.length === 1 ? "request" : "requests"} accepted for later execution`
    : undefined;

  const panel = (
    <div className="flex h-full flex-col bg-canvas text-ink">
      <header className="hairline-b flex h-16 shrink-0 items-center justify-between px-5">
        <h2 className="text-section font-medium">Requests</h2>
        <IconButton label="Close requests" size={32} onClick={() => setPanelOpen(false)}><X size={16} /></IconButton>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto p-5">
        <p className="mb-5 text-meta text-ink-2">Accepted requests are saved here for later execution. They are not running yet.</p>
        {dispatches.length === 0 ? <p className="text-meta text-ink-3">No requests yet.</p> : (
          <ol className="space-y-5" aria-label="Accepted requests">
            {[...dispatches].reverse().map((dispatch) => (
              <li key={dispatch.id} className="hairline-b pb-5">
                <div className="mb-1 text-caption text-ink-3">Accepted</div>
                <p className="whitespace-pre-wrap break-words text-row-title">{dispatch.request}</p>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  );

  return (
    <div className="fixed inset-0 flex overflow-clip bg-canvas text-ink">
      <main className="relative min-w-0 flex-1 overflow-clip" aria-label="Chat">
        <Thread
          threadId="local-agent" conversation={conversation} readOnly onUnread={onUnread}
          presence={!(wide && panelOpen)} status={status} onStatusDetails={() => setPanelOpen(true)}
          onOpenDocument={(item, body) => setDocument({ body, at: item.at })}
          emptyMessage={agent.loading ? "Loading your conversation…" : !agent.session ? "Connect to Nori to begin." : undefined}
        />
        {!(wide && panelOpen) && (
          <div className="pointer-events-none absolute inset-x-0 top-3 z-10 flex justify-center">
            <div aria-hidden className="top-frost absolute inset-x-0 -top-3 h-[92px]" />
            <button
              type="button" aria-label="Nori: open requests" onClick={() => setPanelOpen(true)}
              className="pointer-events-auto relative flex flex-col items-center rounded-full"
            >
              <Medallion size={52} state={busy ? "thinking" : agent.draft.trim() ? "attentive" : "idle"} look="onigiri" watch />
              <span className="glass -mt-2.5 rounded-full px-2.5 py-0.5 text-caption font-medium">Nori</span>
            </button>
          </div>
        )}
        <div className="absolute top-3 right-3 z-20">
          <IconButton label="Refresh conversation" size={32} disabled={agent.loading || busy} onClick={() => void agent.refresh()}>
            <RefreshCw size={16} />
          </IconButton>
        </div>
        {agent.error && (
          <div className="absolute inset-x-0 bottom-[96px] z-20 mx-auto max-w-[768px] px-6">
            <div role="alert" className="rounded-tile bg-surface p-3 text-meta text-ink-2 shadow-pill">
              {agent.error}{" "}
              <button type="button" className="font-medium text-accent-text hover:underline" disabled={agent.loading || busy} onClick={() => void agent.refresh()}>
                Refresh conversation
              </button>
            </div>
          </div>
        )}
        <RuntimeComposer
          draft={agent.draft} setDraft={agent.setDraft} disabled={!agent.session || agent.loading}
          busy={busy} canUpdate={false} stopping={false} stopDisabled
          onSend={() => void agent.send()} unread={unread.count} onJump={unread.jump}
        />
      </main>
      {wide && panelOpen && <aside aria-label="Requests" className="hairline-l h-full shrink-0 overflow-clip" style={{ width: "var(--panel-w)" }}>{panel}</aside>}
      {!wide && (
        <Dialog.Root open={panelOpen} onOpenChange={setPanelOpen}>
          <Dialog.Portal>
            <Dialog.Overlay className="scrim fixed inset-0 z-40 bg-overlay" />
            <Dialog.Content aria-describedby={undefined} className="sheet-right fixed inset-y-0 right-0 z-40 max-w-full shadow-pop" style={{ width: "var(--panel-w)" }}>
              <Dialog.Title className="sr-only">Requests</Dialog.Title>
              {panel}
            </Dialog.Content>
          </Dialog.Portal>
        </Dialog.Root>
      )}
      <DocumentViewer document={document} onClose={() => setDocument(null)} />
    </div>
  );
}
