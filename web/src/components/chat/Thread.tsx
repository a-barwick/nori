"use client";

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Check, Copy, FileText } from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { clock } from "@/lib/time";
import { buildUnits, posOf, type Unit } from "@/lib/units";
import type { ReadPosition, TextItem, Thread as Conversation } from "@/lib/types";
import { cn, firstSentence } from "@/lib/utils";
import { scrollWithin } from "@/lib/dom";
import { DebugBlock } from "@/components/dev/DebugBlock";
import { Markdown } from "./Markdown";
import { BubbleUnit } from "./Message";

/* ── Page block: not a bubble; full column width, paper and ink ── */

function PageBlock({ item, body, onOpenDocument }: { item: TextItem; body: string; onOpenDocument?: (item: TextItem, body: string) => void }) {
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
  const title = body.match(/^#{1,3}\s+(.+)$/m)?.[1] ?? firstSentence(body).slice(0, 48);

  return (
    <article className="nova-document group/page relative mt-4 w-full" data-item-id={item.id} aria-label={`Nori: ${title}`}>
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
          {onOpenDocument && <Button size="sm" variant="quiet" onClick={() => onOpenDocument(item, body)}><FileText size={15} strokeWidth={1.5} />Open document</Button>}
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

function WorkLine({ label, onDetails }: { label: string; onDetails?: () => void }) {
  return (
    <div className="anim-fade mt-4 flex items-center gap-2 text-meta text-ink-2" role="status">
      <NoriMark size={16} state="thinking" />
      <span>{label}</span>
      {onDetails && <button type="button" className="font-medium text-accent-text hover:underline" onClick={onDetails}>Details</button>}
    </div>
  );
}

function UnitView({ unit, onOpenDocument }: { unit: Unit; onOpenDocument?: (item: TextItem, body: string) => void }) {
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
        </div>
      );
  }
}

export function Thread({
  onUnread,
  presence,
  conversation,
  status,
  emptyMessage,
  onOpenDocument,
  onStatusDetails,
  hasOlder = false,
  loadingOlder = false,
  onLoadOlder,
  onRead,
  readPosition,
}: {
  onRead?: (messageID: string) => void;
  readPosition?: ReadPosition;
  hasOlder?: boolean;
  loadingOlder?: boolean;
  onLoadOlder?: () => void;
  onStatusDetails?: () => void;
  onOpenDocument?: (item: TextItem, body: string) => void;
  conversation: Conversation;
  status?: string;
  emptyMessage?: string;
  onUnread: (count: number, jump: () => void) => void;
  /** The presence medallion floats at top center, so content fades out lower. */
  presence: boolean;
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
    if (!root || !onRead) return;
    const shown = new Set<string>();
    const report = () => {
      if (document.hidden) return;
      const newest = items.findLast((item) => item.sender === "nori" && shown.has(item.id));
      if (newest) onRead(newest.id);
    };
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        const id = (entry.target as HTMLElement).dataset.itemId!;
        if (entry.isIntersecting && entry.intersectionRect.height > 0) shown.add(id);
        else shown.delete(id);
      }
      report();
    }, { root, rootMargin: `${presence ? -104 : -56}px 0px 0px 0px` });
    root.querySelectorAll<HTMLElement>("[data-item-id]").forEach((node) => observer.observe(node));
    document.addEventListener("visibilitychange", report);
    return () => { observer.disconnect(); document.removeEventListener("visibilitychange", report); };
  }, [items, onRead, presence]);


  useLayoutEffect(() => {
    const anchor = prependAnchor.current;
    const el = scroller.current;
    if (!anchor || !el || loadingOlder) return;
    const target = Array.from(el.querySelectorAll<HTMLElement>("[data-item-id]")).find((node) => node.dataset.itemId === anchor.id);
    if (target) el.scrollTop += target.getBoundingClientRect().top - anchor.top;
    prependAnchor.current = null;
  }, [items, loadingOlder]);

  // New arrivals: follow along if you're at the bottom, otherwise count them.
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

  useEffect(() => {
    if (status && atBottom.current) requestAnimationFrame(() => scrollToBottom(true));
  }, [status]);

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
      if (el) scrollWithin(el, "start");
      else scrollToBottom(true);
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
      style={{ "--top-fade": presence ? "104px" : "56px" } as React.CSSProperties}
    >
      <div
        ref={content}
        role="log"
        aria-live="polite"
        aria-label={`Conversation: ${thread.title}`}
        className={cn("mx-auto max-w-[768px] px-[clamp(24px,calc(72px-(100vw-768px)/2),72px)] pb-[148px] transition-[padding] duration-300", presence ? "pt-28" : "pt-16")}
      >
        <div aria-live="off">
          {hasOlder && <div className="mb-4 text-center"><Button size="sm" variant="quiet" disabled={loadingOlder} onClick={() => {
            const first = scroller.current?.querySelector<HTMLElement>("[data-item-id]");
            if (first?.dataset.itemId) prependAnchor.current = { id: first.dataset.itemId, top: first.getBoundingClientRect().top };
            atBottom.current = false;
            onLoadOlder?.();
          }}>{loadingOlder ? "Loading older messages…" : "Load older messages"}</Button></div>}
          {units.length === 0 && (
            <div className="grid min-h-[calc(100dvh-320px)] place-items-center">
              <p className="arrive text-title text-ink-2" style={{ animationDelay: "250ms" }}>
                {emptyMessage ?? "Nori is getting ready to say hello…"}
              </p>
            </div>
          )}
          {units.map((u) => (
            <UnitView onOpenDocument={onOpenDocument} key={u.key} unit={u} />
          ))}
          {status && <WorkLine label={status} onDetails={onStatusDetails} />}
        </div>
        <div className="sr-only">{announcement}</div>
      </div>
    </div>
  );
}
