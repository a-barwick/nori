"use client";

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Check, Copy, FileText } from "lucide-react";
import { InteractionCard, type OnInteractionResolved } from "@/components/assistant/InteractionCard";
import { DebugBlock } from "@/components/dev/DebugBlock";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import type { InlineInteractionEntry } from "@/lib/assistant/interactions";
import { clock } from "@/lib/time";
import { buildUnits, posOf, type Unit } from "@/lib/units";
import type { ReadPosition, TextItem, Thread as Conversation } from "@/lib/types";
import { cn, documentTitle, firstSentence } from "@/lib/utils";
import { Markdown } from "./Markdown";
import { BubbleUnit } from "./Message";

const TOP_MASK = 56;
type OpenDocument = (item: TextItem, body: string) => void;

function PageBlock({ item, body, onOpenDocument }: { item: TextItem; body: string; onOpenDocument: OpenDocument }) {
  const ref = useRef<HTMLDivElement>(null);
  const [tall, setTall] = useState(false);
  // A page you just watched being written stays open; older ones fold.
  const [expanded, setExpanded] = useState(() => Date.now() - item.at < 15000);
  const [copied, setCopied] = useState(false);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setTall(el.scrollHeight > window.innerHeight * 0.7);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const collapsed = tall && !expanded;
  const title = documentTitle(body);

  return (
    <article className="nori-document group/page relative mt-4 w-full" data-item-id={item.id} aria-label={`Nori: ${title}`}>
      <header className="flex min-h-8 flex-wrap items-center gap-1.5">
        <NoriMark size={16} state="idle" />
        <span className="text-meta font-medium text-ink">Nori</span>
        <span className="text-caption text-ink-3">· {clock(item.at)}</span>
        <div className="ml-auto flex items-center gap-1">
          <Button
            size="sm"
            variant="quiet"
            onClick={() => {
              void navigator.clipboard?.writeText(body);
              setCopied(true);
              setTimeout(() => setCopied(false), 1400);
            }}
          >
            {copied ? <Check size={15} strokeWidth={1.5} /> : <Copy size={15} strokeWidth={1.5} />}
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button size="sm" variant="quiet" onClick={() => onOpenDocument(item, body)}><FileText size={15} strokeWidth={1.5} />Open document</Button>
        </div>
      </header>
      <div className={cn("relative mt-1", collapsed && "max-h-[50vh] overflow-hidden")}>
        <div ref={ref} className="max-w-[68ch]">
          <Markdown variant="page">{body}</Markdown>
        </div>
        {collapsed && (
          <div className="page-fade absolute inset-x-0 bottom-0 flex h-32 items-end justify-center pb-2">
            <Button onClick={() => setExpanded(true)}>Keep reading</Button>
          </div>
        )}
      </div>
    </article>
  );
}

function Receipt({ state }: { state: "sending" | "saved" }) {
  return <p role="status" className="flex items-center gap-1 pr-1 text-caption text-ink-3">
    {state === "saved" && <Check size={12} strokeWidth={2} aria-hidden />}{state === "sending" ? "Sending…" : "Saved"}
  </p>;
}

function WorkLine({ label, onDetails }: { label: string; onDetails?: () => void }) {
  return (
    <div className="anim-fade mt-4 flex items-center gap-2 text-meta text-ink-2" role="status">
      <NoriMark size={16} state="thinking" />
      <span>{label}</span>
      {onDetails && <button type="button" className="font-medium text-accent-text hover:underline" onClick={onDetails}>Details</button>}
    </div>
  );
}

function UnitView({ unit, onOpenDocument }: { unit: Unit; onOpenDocument: OpenDocument }) {
  switch (unit.type) {
    case "sep":
      return <div className="my-6 text-center text-caption text-ink-3">{unit.label}</div>;
    case "page":
      return <><PageBlock onOpenDocument={onOpenDocument} item={unit.item} body={unit.body} />{unit.item.debug && <DebugBlock info={unit.item.debug} />}</>;
    case "group":
      return (
        <div className={cn("mt-4 flex flex-col gap-1", unit.sender === "me" ? "items-end" : "items-start")}>
          {unit.units.map((u, i) => (
            <div key={u.key} className={cn("flex w-full", unit.sender === "me" ? "justify-end" : "justify-start")}>
              <BubbleUnit unit={u} pos={posOf(i, unit.units.length)} sender={unit.sender} />
            </div>
          ))}
          {unit.sender === "nori" && unit.units.map((u) => u.item.debug ? <DebugBlock key={`${u.key}-debug`} info={u.item.debug} /> : null)}
          {unit.sender === "me" && unit.units.at(-1)?.item.receipt && <Receipt state={unit.units.at(-1)!.item.receipt!} />}
        </div>
      );
  }
}

export function Thread({
  onUnread,
  conversation,
  status,
  emptyMessage,
  onOpenDocument,
  onStatusDetails,
  hasOlder,
  loadingOlder,
  onLoadOlder,
  onShown,
  readPosition,
  interactions,
  onInteractionResolved,
}: {
  onShown?: (messageIDs: string[]) => void;
  readPosition?: ReadPosition;
  hasOlder: boolean;
  loadingOlder: boolean;
  onLoadOlder: () => void;
  onStatusDetails?: () => void;
  onOpenDocument: OpenDocument;
  conversation: Conversation;
  status?: string;
  emptyMessage: string;
  onUnread: (count: number, jump: () => void) => void;
  interactions?: InlineInteractionEntry[];
  onInteractionResolved?: OnInteractionResolved;
}) {
  const thread = conversation;
  const items = thread.items;
  const units = useMemo(() => buildUnits(items), [items]);
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const seenIds = useRef(new Set(items.map((item) => item.id)));
  const previousFirst = useRef(items[0]?.id);
  const prependAnchor = useRef<{ id: string; top: number } | null>(null);
  const [unreadIDs, setUnreadIDs] = useState<string[]>([]);
  const readIndex = items.findIndex((item) => item.id === readPosition?.message_id);
  const unreadItems = unreadIDs.filter((id) => {
    const index = items.findIndex((item) => item.id === id);
    return index >= 0 && (readIndex >= 0 ? index > readIndex : !readPosition?.message_id || items[index].at > Date.parse(readPosition.through));
  });
  const unread = unreadItems.length;
  const firstUnread = unreadItems[0];
  const [announcement, setAnnouncement] = useState("");

  const scrollToBottom = (smooth = true) => {
    const el = scroller.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: smooth ? "smooth" : "auto" });
  };

  // Opens at the bottom (the thread remounts per chat, so this covers switching too).
  useLayoutEffect(() => {
    scrollToBottom(false);
  }, []);

  // Loading a reply into a visible tab isn't enough: it must enter the conversation's viewport. Keep intersection
  // state while hidden, then report only what is still visible when the tab is shown again.
  useEffect(() => {
    const root = scroller.current;
    if (!root || !onShown) return;
    const shown = new Set<Element>();
    const report = () => {
      if (document.hidden) return;
      const ids = new Set([...shown].map((node) => (node as HTMLElement).dataset.itemId));
      const replies = items.filter((item) => item.sender === "nori" && ids.has(item.id));
      if (replies.length) onShown(replies.map((item) => item.id));
    };
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting && entry.intersectionRect.height > 0) shown.add(entry.target);
        else shown.delete(entry.target);
      }
      report();
    }, { root, rootMargin: `-${TOP_MASK}px 0px 0px 0px` });
    root.querySelectorAll<HTMLElement>("[data-item-id]").forEach((node) => observer.observe(node));
    document.addEventListener("visibilitychange", report);
    return () => { observer.disconnect(); document.removeEventListener("visibilitychange", report); };
  }, [items, onShown]);


  useLayoutEffect(() => {
    const anchor = prependAnchor.current;
    const el = scroller.current;
    if (!anchor || !el || loadingOlder) return;
    const target = Array.from(el.querySelectorAll<HTMLElement>("[data-item-id]")).find((node) => node.dataset.itemId === anchor.id);
    if (target) el.scrollTop += target.getBoundingClientRect().top - anchor.top;
    prependAnchor.current = null;
  }, [items, loadingOlder]);

  useEffect(() => {
    const previousIndex = items.findIndex((item) => item.id === previousFirst.current);
    const prepended = previousIndex > 0;
    const added = items.filter((item, index) => !seenIds.current.has(item.id) && (!prepended || index >= previousIndex));
    seenIds.current = new Set(items.map((item) => item.id));
    previousFirst.current = items[0]?.id;
    if (!added.length) return;
    const fromNori = added.filter((i) => i.sender === "nori");
    const lastNori = fromNori[fromNori.length - 1];
    if (lastNori) setAnnouncement(`Nori: ${firstSentence(lastNori.text)}`);
    const mine = added.some((i) => i.sender === "me");
    if (atBottom.current || mine) {
      requestAnimationFrame(() => scrollToBottom(true));
    } else if (fromNori.length) {
      setUnreadIDs((ids) => [...ids, ...fromNori.map((item) => item.id)]);
    }
  }, [items]);

  const interactionCount = interactions?.length ?? 0;

  useEffect(() => {
    if ((status || interactionCount > 0) && atBottom.current) requestAnimationFrame(() => scrollToBottom(true));
  }, [status, interactionCount]);

  // Anything that grows the saved reply (fonts or document layout)
  // keeps you pinned to the bottom if that's where you were.
  useEffect(() => {
    const el = content.current;
    if (!el) return;
    let prev = el.offsetHeight;
    const ro = new ResizeObserver(() => {
      const h = el.offsetHeight;
      if (h > prev && atBottom.current && scroller.current) scroller.current.scrollTop = scroller.current.scrollHeight;
      prev = h;
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    const jump = () => {
      const el = firstUnread ? scroller.current?.querySelector<HTMLElement>(`[data-item-id="${firstUnread}"]`) : null;
      const root = scroller.current;
      if (root && el) {
        // Keep the outer shell still and place the message below the mask.
        const top = el.getBoundingClientRect().top - root.getBoundingClientRect().top + root.scrollTop - TOP_MASK - 16;
        root.scrollTo({ top: Math.max(0, top), behavior: "smooth" });
      } else scrollToBottom(true);
      setUnreadIDs([]);
    };
    onUnread(unread, jump);
  }, [unread, firstUnread, onUnread]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    if (atBottom.current && unread) {
      setUnreadIDs([]);
    }
  };

  return (
    <div
      ref={scroller}
      onScroll={onScroll}
      className="thread-mask scrollbar-quiet h-full overflow-y-auto"
      style={{ "--top-fade": `${TOP_MASK}px` } as React.CSSProperties}
    >
      <div
        ref={content}
        role="log"
        aria-live="polite"
        aria-label={`Conversation: ${thread.title}`}
        className="mx-auto max-w-[768px] px-[clamp(24px,calc(72px-(100vw-768px)/2),72px)] pt-16 pb-[148px]"
      >
        <div aria-live="off">
          {hasOlder && <div className="mb-4 text-center"><Button size="sm" variant="quiet" disabled={loadingOlder} onClick={() => {
            const first = scroller.current?.querySelector<HTMLElement>("[data-item-id]");
            if (first?.dataset.itemId) prependAnchor.current = { id: first.dataset.itemId, top: first.getBoundingClientRect().top };
            atBottom.current = false;
            onLoadOlder();
          }}>{loadingOlder ? "Loading older messages…" : "Load older messages"}</Button></div>}
          {units.length === 0 && !interactions?.length && (
            <div className="grid min-h-[calc(100dvh-320px)] place-items-center">
              <p className="arrive text-title text-ink-2" style={{ animationDelay: "250ms" }}>
                {emptyMessage}
              </p>
            </div>
          )}
          {units.map((u) => (
            <UnitView onOpenDocument={onOpenDocument} key={u.key} unit={u} />
          ))}
          {!!interactions?.length && (
            <section aria-label="Needs your approval or answer" data-inline-interactions className="mt-4 space-y-3">
              {interactions.map((entry) => (
                <InteractionCard
                  key={entry.interaction.id}
                  interaction={entry.interaction}
                  inline
                  rememberedRule={entry.rememberedRule}
                  onResolved={onInteractionResolved}
                />
              ))}
            </section>
          )}
          {status && <WorkLine label={status} onDetails={onStatusDetails} />}
        </div>
        <div className="sr-only">{announcement}</div>
      </div>
    </div>
  );
}
