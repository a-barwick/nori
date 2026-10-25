"use client";

import { useCallback, useState } from "react";
import { Composer } from "@/components/chat/Composer";
import { Thread } from "@/components/chat/Thread";
import { Medallion } from "@/components/mark/NoriMark";
import { useWide } from "@/lib/hooks";
import { useNori } from "@/lib/store";

function TopBar() {
  const wide = useWide();
  const name = useNori((s) => s.identity.name);
  const panelOpen = useNori((s) => (wide ? s.dock.chat : s.overlay));
  const togglePanel = useNori((s) => s.togglePanel);

  return (
    <div className="pointer-events-none absolute inset-x-0 top-3 z-10 flex items-start px-4">
      {/* Frosted band: what scrolls under Nori blurs away instead of ghosting through */}
      <div aria-hidden className="top-frost absolute inset-x-0 -top-3" style={{ height: panelOpen ? 44 : 92 }} />
      {!panelOpen && (
        <button
          type="button"
          onClick={() => togglePanel("chat", wide)}
          aria-label={`${name}: open agent panel`}
          className="anim-fade pointer-events-auto absolute left-1/2 flex -translate-x-1/2 flex-col items-center rounded-full"
        >
          <Medallion size={52} watch />
          <span className="glass -mt-2.5 rounded-full px-2.5 py-0.5 text-caption font-medium text-ink">{name}</span>
        </button>
      )}
    </div>
  );
}

export default function ChatPage() {
  const threadId = useNori((s) => s.activeThreadId);
  const wide = useWide();
  const presence = useNori((s) => !(wide ? s.dock.chat : s.overlay));
  const [unread, setUnread] = useState<{ count: number; jump: () => void }>({ count: 0, jump: () => {} });
  const onUnread = useCallback((count: number, jump: () => void) => setUnread({ count, jump }), []);

  return (
    <div className="relative h-full">
      <Thread key={threadId} threadId={threadId} onUnread={onUnread} presence={presence} />
      <TopBar />
      <Composer threadId={threadId} unread={unread.count} onJump={unread.jump} />
    </div>
  );
}
