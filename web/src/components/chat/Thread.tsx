"use client";

import Link from "next/link";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { AlertCircle, Check, Copy, FileText, RefreshCw } from "lucide-react";
import { InteractionCard, type OnInteractionResolved } from "@/components/assistant/InteractionCard";
import { DebugBlock } from "@/components/dev/DebugBlock";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import type { InlineInteractionEntry } from "@/lib/assistant/interactions";
import { useCopyToClipboard } from "@/lib/browser";
import { clock } from "@/lib/time";
import { THINKING_WORDS } from "@/lib/thinkingWords";
import { buildUnits, posOf, type Unit } from "@/lib/units";
import type { ReadPosition, TextItem, Thread as Conversation } from "@/lib/types";
import { cn, documentTitle, firstSentence } from "@/lib/utils";
import { Markdown } from "./Markdown";
import { BubbleUnit } from "./Message";

const TOP_MASK = 24;
type OpenDocument = (item: TextItem, body: string) => void;

function PageBlock({
  item,
  title: explicitTitle,
  body,
  onOpenDocument,
}: {
  item: TextItem;
  title?: string;
  body: string;
  onOpenDocument: OpenDocument;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [tall, setTall] = useState(false);
  const [expanded, setExpanded] = useState(() => Date.now() - item.at < 15000);
  const { copied, copy } = useCopyToClipboard();

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setTall(el.scrollHeight > window.innerHeight * 0.65);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const collapsed = tall && !expanded;
  const title = explicitTitle?.trim() || documentTitle(body);

  return (
    <article
      className="nori-document group/page relative mt-4 w-full max-w-[680px] transition-[border-color,box-shadow] hover:border-hairline-strong"
      data-item-id={item.id}
      aria-label={`Nori: ${title}`}
    >
      <header className="flex min-h-8 items-center gap-2.5">
        <span className="grid size-7 shrink-0 place-items-center rounded-[8px] bg-accent-subtle text-accent-text">
          <FileText size={14} strokeWidth={1.9} aria-hidden />
        </span>
        <div className="min-w-0 flex-1">
          <span className="block truncate text-meta font-semibold text-ink">{title}</span>
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-1.5">
          <Button
            size="sm"
            variant="quiet"
            onClick={() => copy(body)}
          >
            {copied ? <Check size={14} strokeWidth={1.8} /> : <Copy size={14} strokeWidth={1.8} />}
            {copied ? "Copied" : "Copy"}
          </Button>
          <button
            type="button"
            onClick={() => onOpenDocument(item, body)}
            className="t-fast inline-flex h-8 items-center gap-1.5 rounded-full border border-lavender-border bg-accent-subtle px-3 text-meta font-semibold text-accent-text hover:bg-accent-muted"
          >
            <FileText size={14} strokeWidth={1.8} aria-hidden />
            Open
          </button>
        </div>
      </header>
      <div className={cn("relative mt-1", collapsed && "max-h-[48vh] overflow-hidden")}>
        <div ref={ref} className="max-w-[68ch]">
          <Markdown variant="page">{body}</Markdown>
        </div>
        {collapsed && (
          <div className="page-fade absolute inset-x-0 bottom-0 flex h-28 items-end justify-center pb-2">
            <Button onClick={() => setExpanded(true)}>Keep reading</Button>
          </div>
        )}
      </div>
    </article>
  );
}

function Receipt({ state }: { state: "sending" | "saved" }) {
  return (
    <p role="status" className="flex items-center gap-1 pr-1 text-caption text-ink-3">
      {state === "saved" && <Check size={12} strokeWidth={2} aria-hidden />}
      {state === "sending" ? "Sending…" : "Saved"}
    </p>
  );
}

function WorkLine({
  label,
  thinking,
  onDetails,
}: {
  label: string;
  thinking?: boolean;
  onDetails?: () => void;
}) {
  const [wordIndex, setWordIndex] = useState(0);

  useEffect(() => {
    if (!thinking) return;
    const timer = setInterval(() => {
      setWordIndex((current) => {
        if (THINKING_WORDS.length <= 1) return 0;
        const step = 1 + Math.floor(Math.random() * (THINKING_WORDS.length - 1));
        return (current + step) % THINKING_WORDS.length;
      });
    }, 2600);
    return () => clearInterval(timer);
  }, [thinking]);

  if (thinking) {
    const word = THINKING_WORDS[wordIndex % THINKING_WORDS.length];
    return (
      <div
        className="anim-fade mt-4 inline-flex items-center gap-2.5 py-1"
        role="status"
        data-thinking-indicator
      >
        <span className="w-7 shrink-0" data-active-avatar="true">
          <NoriMark size={26} state="thinking" />
        </span>
        <span className="nori-thinking-word text-[13.5px] font-medium">{word}</span>
      </div>
    );
  }

  return (
    <div className="anim-fade mt-4 flex items-center" role="status">
      <button
        type="button"
        onClick={onDetails}
        disabled={!onDetails}
        className="inline-flex items-center gap-2 rounded-full border border-hairline bg-surface px-3 py-1.5 text-caption font-medium text-ink-2 transition-colors hover:bg-fill-1 hover:text-ink disabled:cursor-default disabled:hover:bg-surface"
      >
        <span className="pulse-dot size-[7px] shrink-0 rounded-full bg-accent" aria-hidden />
        <span className="truncate">{label}</span>
      </button>
    </div>
  );
}

function ErrorCard({
  item,
  onRetry,
  retrying,
}: {
  item: TextItem;
  onRetry?: (item: TextItem) => void;
  retrying?: boolean;
}) {
  const message = item.error?.message || item.text || "Something went wrong. Please try again.";
  const retryable = item.error ? item.error.retryable : true;
  return (
    <article
      role="alert"
      data-item-id={item.id}
      data-message-error
      data-error-code={item.error?.code}
      className="mt-4 max-w-[min(680px,100%)] rounded-card border border-danger/25 bg-surface px-4 py-3.5 shadow-[0_1px_2px_rgba(25,9,54,0.05)]"
    >
      <div className="flex items-start gap-2.5">
        <AlertCircle size={18} className="mt-0.5 shrink-0 text-danger" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-meta font-semibold text-ink">Reply didn’t go through</span>
            <span className="text-caption text-ink-3">· {clock(item.at)}</span>
          </div>
          <p className="mt-1 whitespace-pre-line text-body text-ink-2 [overflow-wrap:anywhere]">{message}</p>
          {(item.error?.code === "model_key_required" || item.error?.code === "missing_api_key") && (
            <Link href="/settings/models" className="mt-2 inline-block text-meta font-semibold text-accent-text hover:underline">Open Models</Link>
          )}
          {retryable && onRetry && (
            <div className="mt-3">
              <Button size="sm" variant="primary" disabled={retrying} onClick={() => onRetry(item)}>
                <RefreshCw size={14} className={retrying ? "animate-spin motion-reduce:animate-none" : undefined} aria-hidden />
                {retrying ? "Retrying…" : "Retry"}
              </Button>
            </div>
          )}
        </div>
      </div>
    </article>
  );
}

function latestNoriMessageId(units: Unit[]): string | undefined {
  for (let i = units.length - 1; i >= 0; i -= 1) {
    const unit = units[i];
    if (unit.type === "group" && unit.sender === "nori") {
      const last = unit.units.at(-1);
      if (last) return last.item.id;
    }
  }
  return undefined;
}

function UnitView({
  unit,
  activeNoriMessageId,
  justSentIds,
  onOpenDocument,
  onRetryMessage,
  retryingMessageId,
  onInteractionResolved,
}: {
  unit: Unit;
  activeNoriMessageId?: string;
  justSentIds: ReadonlySet<string>;
  onOpenDocument: OpenDocument;
  onRetryMessage?: (item: TextItem) => void;
  retryingMessageId?: string;
  onInteractionResolved?: OnInteractionResolved;
}) {
  switch (unit.type) {
    case "sep":
      return <div className="my-6 text-center text-caption font-medium text-ink-3">{unit.label}</div>;
    case "page":
      return (
        <>
          <PageBlock onOpenDocument={onOpenDocument} item={unit.item} title={unit.title} body={unit.body} />
          {unit.item.debug && <DebugBlock info={unit.item.debug} />}
        </>
      );
    case "error":
      return (
        <>
          <ErrorCard
            item={unit.item}
            onRetry={onRetryMessage}
            retrying={retryingMessageId === unit.item.id || (!!unit.item.turnId && retryingMessageId === unit.item.turnId)}
          />
          {unit.item.debug && <DebugBlock info={unit.item.debug} />}
        </>
      );
    case "interaction":
      return (
        <div
          aria-label="Action needed"
          data-inline-interactions
          className="mt-4 w-full max-w-[680px]"
        >
          <InteractionCard
            interaction={unit.entry.interaction}
            inline
            rememberedRule={unit.entry.rememberedRule}
            onResolved={onInteractionResolved}
          />
        </div>
      );
    case "group":
      return (
        <div className={cn("mt-4 flex flex-col gap-1", unit.sender === "me" ? "items-end" : "items-start")}>
          {unit.units.map((u, i) => (
            <div key={u.key} className={cn("flex w-full", unit.sender === "me" ? "justify-end" : "justify-start")}>
              <BubbleUnit
                unit={u}
                pos={posOf(i, unit.units.length)}
                sender={unit.sender}
                showAvatar={unit.sender === "nori" && u.item.id === activeNoriMessageId}
                animateSend={unit.sender === "me" && (u.item.receipt === "sending" || justSentIds.has(u.item.id))}
              />
            </div>
          ))}
          {unit.sender === "nori" &&
            unit.units.map((u) => (u.item.debug ? <DebugBlock key={`${u.key}-debug`} info={u.item.debug} /> : null))}
          {unit.sender === "me" && unit.units.at(-1)?.item.receipt && (
            <Receipt state={unit.units.at(-1)!.item.receipt!} />
          )}
        </div>
      );
  }
}

export function Thread({
  onUnread,
  conversation,
  status,
  thinking = false,
  streamingText,
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
  onRetryMessage,
  retryingMessageId,
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
  thinking?: boolean;
  streamingText?: string;
  emptyMessage: string;
  onUnread: (count: number, jump: () => void) => void;
  interactions?: InlineInteractionEntry[];
  onInteractionResolved?: OnInteractionResolved;
  onRetryMessage?: (item: TextItem) => void;
  retryingMessageId?: string;
}) {
  const thread = conversation;
  const items = thread.items;
  const units = useMemo(() => buildUnits(items, interactions), [items, interactions]);
  const activeNoriMessageId = useMemo(() => (thinking ? undefined : latestNoriMessageId(units)), [thinking, units]);
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const seenIds = useRef(new Set(items.map((item) => item.id)));
  const previousFirst = useRef(items[0]?.id);
  const prependAnchor = useRef<{ id: string; top: number } | null>(null);
  const touchStartY = useRef<number | null>(null);
  const justSentTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [justSentIds, setJustSentIds] = useState<Set<string>>(() => new Set());
  useEffect(() => {
    return () => {
      if (justSentTimer.current) clearTimeout(justSentTimer.current);
    };
  }, []);
  const [unreadIDs, setUnreadIDs] = useState<string[]>([]);
  const readIndex = items.findIndex((item) => item.id === readPosition?.message_id);
  const unreadItems = unreadIDs.filter((id) => {
    const index = items.findIndex((item) => item.id === id);
    return (
      index >= 0 &&
      (readIndex >= 0 ? index > readIndex : !readPosition?.message_id || items[index].at > Date.parse(readPosition.through))
    );
  });
  const unread = unreadItems.length;
  const firstUnread = unreadItems[0];
  const [announcement, setAnnouncement] = useState("");

  const scrollToBottom = (smooth = true) => {
    const el = scroller.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: smooth ? "smooth" : "auto" });
  };

  useLayoutEffect(() => {
    scrollToBottom(false);
  }, []);

  useEffect(() => {
    const root = scroller.current;
    if (!root || !onShown) return;
    const shown = new Set<Element>();
    const report = () => {
      if (document.hidden) return;
      const ids = new Set([...shown].map((node) => (node as HTMLElement).dataset.itemId));
      const replies = items.filter((item) => item.sender === "nori" && item.status !== "failed" && ids.has(item.id));
      if (replies.length) onShown(replies.map((item) => item.id));
    };
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting && entry.intersectionRect.height > 0) shown.add(entry.target);
          else shown.delete(entry.target);
        }
        report();
      },
      { root, rootMargin: `-${TOP_MASK}px 0px 0px 0px` },
    );
    root.querySelectorAll<HTMLElement>("[data-item-id]").forEach((node) => observer.observe(node));
    document.addEventListener("visibilitychange", report);
    return () => {
      observer.disconnect();
      document.removeEventListener("visibilitychange", report);
    };
  }, [items, onShown]);

  useLayoutEffect(() => {
    const anchor = prependAnchor.current;
    const el = scroller.current;
    if (!anchor || !el || loadingOlder) return;
    const target = Array.from(el.querySelectorAll<HTMLElement>("[data-item-id]")).find(
      (node) => node.dataset.itemId === anchor.id,
    );
    if (target) el.scrollTop += target.getBoundingClientRect().top - anchor.top;
    prependAnchor.current = null;
  }, [items, loadingOlder]);

  useEffect(() => {
    const previousIndex = items.findIndex((item) => item.id === previousFirst.current);
    const prepended = previousIndex > 0;
    const added = items.filter(
      (item, index) => !seenIds.current.has(item.id) && (!prepended || index >= previousIndex),
    );
    seenIds.current = new Set(items.map((item) => item.id));
    previousFirst.current = items[0]?.id;
    if (!added.length) return;
    const fromNori = added.filter((i) => i.sender === "nori");
    const lastNori = fromNori[fromNori.length - 1];
    if (lastNori) setAnnouncement(`Nori: ${firstSentence(lastNori.text)}`);
    const mine = added.filter((i) => i.sender === "me");
    if (mine.length) {
      const addedIds = mine.map((m) => m.id);
      setJustSentIds((prev) => new Set([...[...prev].slice(-10), ...addedIds]));
      if (justSentTimer.current) clearTimeout(justSentTimer.current);
      justSentTimer.current = setTimeout(() => {
        justSentTimer.current = null;
        setJustSentIds(new Set());
      }, 600);
    }
    if (atBottom.current || mine.length > 0) {
      requestAnimationFrame(() => scrollToBottom(true));
    } else if (fromNori.length) {
      setUnreadIDs((ids) => [...ids, ...fromNori.map((item) => item.id)]);
    }
  }, [items]);

  useEffect(() => {
    const onScrollToBottom = () => {
      setUnreadIDs([]);
      scrollToBottom(true);
    };
    window.addEventListener("nori:chat-scroll-bottom", onScrollToBottom);
    return () => window.removeEventListener("nori:chat-scroll-bottom", onScrollToBottom);
  }, []);

  const interactionCount = interactions?.length ?? 0;

  useEffect(() => {
    if ((status || interactionCount > 0) && atBottom.current) requestAnimationFrame(() => scrollToBottom(true));
  }, [status, interactionCount]);

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
      const el = firstUnread
        ? scroller.current?.querySelector<HTMLElement>(`[data-item-id="${firstUnread}"]`)
        : null;
      const root = scroller.current;
      if (root && el) {
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
      onTouchStart={(event) => {
        touchStartY.current = event.touches[0]?.clientY ?? null;
      }}
      onTouchMove={(event) => {
        if (touchStartY.current === null) return;
        const dy = (event.touches[0]?.clientY ?? touchStartY.current) - touchStartY.current;
        if (dy > 18 && document.activeElement instanceof HTMLTextAreaElement) {
          document.activeElement.blur();
          touchStartY.current = null;
        }
      }}
      onTouchEnd={() => {
        touchStartY.current = null;
      }}
      className="scrollbar-quiet h-full overflow-y-auto overscroll-contain"
    >
      <div
        ref={content}
        role="log"
        aria-live="polite"
        aria-label={`Conversation: ${thread.title}`}
        className="nori-page-col pt-5 pb-8"
      >
        <div aria-live="off">
          {hasOlder && (
            <div className="mb-4 text-center">
              <Button
                size="sm"
                variant="quiet"
                disabled={loadingOlder}
                onClick={() => {
                  const first = scroller.current?.querySelector<HTMLElement>("[data-item-id]");
                  if (first?.dataset.itemId) {
                    prependAnchor.current = { id: first.dataset.itemId, top: first.getBoundingClientRect().top };
                  }
                  atBottom.current = false;
                  onLoadOlder();
                }}
              >
                {loadingOlder ? "Loading older messages…" : "Load older messages"}
              </Button>
            </div>
          )}
          {units.length === 0 && !interactions?.length && (
            <div className="flex min-h-[calc(100dvh-260px)] flex-col items-center justify-center gap-3 text-center">
              <NoriMark size={44} state="idle" />
              <p className="arrive text-section font-semibold text-ink" style={{ animationDelay: "180ms" }}>
                {emptyMessage}
              </p>
            </div>
          )}
          {units.map((u) => (
            <UnitView
              activeNoriMessageId={activeNoriMessageId}
              justSentIds={justSentIds}
              onOpenDocument={onOpenDocument}
              onRetryMessage={onRetryMessage}
              retryingMessageId={retryingMessageId}
              onInteractionResolved={onInteractionResolved}
              key={u.key}
              unit={u}
            />
          ))}
          {streamingText && (
            <div className="mt-4 flex max-w-[680px] items-start gap-2.5" data-streaming-reply>
              <NoriMark size={28} state="thinking" />
              <div className="min-w-0 flex-1 whitespace-pre-wrap text-body text-ink">
                {streamingText}
              </div>
            </div>
          )}
          {status && !streamingText && <WorkLine label={status} thinking={thinking} onDetails={onStatusDetails} />}
        </div>
        <div className="sr-only">{announcement}</div>
      </div>
    </div>
  );
}
