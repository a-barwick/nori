"use client";

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { BookmarkPlus, Check, Copy, CornerUpLeft, Library } from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { retryLive } from "@/lib/nori/engine";
import { classify, extractHints } from "@/lib/hybrid";
import { useNori, useThread } from "@/lib/store";
import { clock } from "@/lib/time";
import { buildUnits, posOf, type Unit } from "@/lib/units";
import type { ID, TextItem } from "@/lib/types";
import { cn, firstSentence } from "@/lib/utils";
import { scrollWithin } from "@/lib/dom";
import { ApprovalCard } from "./ApprovalCard";
import { Markdown } from "./Markdown";
import { ArtifactUnit, BubbleUnit, ImageUnit, VoiceUnit } from "./Message";

/* ── Page block: not a bubble; full column width, paper and ink ── */

function PageBlock({ threadId, item, body }: { threadId: ID; item: TextItem; body: string }) {
  const name = useNori((s) => s.identity.name);
  const addArtifact = useNori((s) => s.addArtifact);
  const set = useNori((s) => s.set);
  const ref = useRef<HTMLDivElement>(null);
  const router = useRouter();
  const [tall, setTall] = useState(false);
  // A page you just watched being written stays open; older ones fold.
  const [expanded, setExpanded] = useState(() => Date.now() - item.at < 15000);
  const [copied, setCopied] = useState(false);
  const [savedId, setSavedId] = useState<ID | null>(item.artifactId ?? null);

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
    <article className="group/page relative mt-4 w-full" data-item-id={item.id} aria-label={`${name}: ${title}`}>
      <header className="flex h-8 items-center gap-1.5">
        <NoriMark size={16} state="idle" />
        <span className="text-meta font-medium text-ink">{name}</span>
        <span className="text-caption text-ink-3">· {clock(item.at)}</span>
        <div className="t-fast ml-auto flex items-center gap-1 opacity-0 transition-opacity group-focus-within/page:opacity-100 group-hover/page:opacity-100">
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
          {savedId ? (
            <Button
              size="sm"
              variant="quiet"
              onClick={() => {
                set({ viewerId: savedId, viewerReturn: "/", libraryFilter: "all" });
                router.push("/library");
              }}
            >
              <Library size={15} strokeWidth={1.5} />
              In Library
            </Button>
          ) : (
            <Button
              size="sm"
              variant="quiet"
              onClick={() => setSavedId(addArtifact({ title, type: "page", madeBy: "nori", body, thumb: /\|.*\|/.test(body) ? "table" : "page" }))}
            >
              <BookmarkPlus size={15} strokeWidth={1.5} />
              Save to Library
            </Button>
          )}
          <Button size="sm" variant="quiet" onClick={() => set({ replyTo: { threadId, itemId: item.id }, composerFocus: Date.now() })}>
            <CornerUpLeft size={15} strokeWidth={1.5} />
            Reply
          </Button>
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

function Typing() {
  return (
    <div className="arrive mt-4 flex" aria-label="Nori is typing">
      <div className="flex h-[44px] items-center gap-1.5 rounded-bubble bg-bubble px-[17px]">
        {[0, 1, 2].map((i) => (
          <span key={i} className="typing-dot size-1.5 rounded-full bg-ink-2" style={{ animationDelay: `${i * 0.18}s` }} />
        ))}
      </div>
    </div>
  );
}

/** The reply while it's being written: same shapes it will settle into, with a soft caret. */
function Draft({ threadId }: { threadId: ID }) {
  const text = useNori((s) => (s.stream?.threadId === threadId ? s.stream.text : null));
  const name = useNori((s) => s.identity.name);
  if (text == null) return null;
  const { body, hints } = extractHints(text);
  // A drawing is shown whole once it's done; until then it holds its place.
  const svgAt = body.indexOf("```svg");
  const words = (svgAt >= 0 ? body.slice(0, svgAt) : body).trim();
  if (!words && svgAt < 0) return null;
  const c = words ? classify(words, hints.mode) : null;
  const bubble = c?.mode === "page" ? c.cover : c?.body;

  return (
    <div className="arrive" aria-hidden>
      {bubble && (
        <div className="mt-4 flex">
          <div className={cn("max-w-[min(680px,100%)] rounded-bubble bg-bubble px-[15px] py-[11px] text-ink", c?.mode !== "page" && "streaming")}>
            <Markdown>{bubble}</Markdown>
          </div>
        </div>
      )}
      {c?.mode === "page" && (
        <article className="mt-4 w-full">
          <header className="flex h-8 items-center gap-1.5">
            <NoriMark size={16} state="writing" />
            <span className="text-meta font-medium text-ink">{name}</span>
          </header>
          <div className="streaming mt-1 max-w-[68ch]">
            <Markdown variant="page">{c.body}</Markdown>
          </div>
        </article>
      )}
      {svgAt >= 0 && <div className="skeleton mt-1 aspect-[4/3] w-[min(480px,100%)] rounded-tile bg-fill-1" />}
    </div>
  );
}

function greeting(now = new Date()) {
  const h = now.getHours();
  if (h < 5) return "Still up? I'm here.";
  if (h < 12) return "Morning. What's on today?";
  if (h < 17) return "Afternoon. What can I take off your plate?";
  if (h < 22) return "Evening. Anything for tomorrow?";
  return "Late one. What's on your mind?";
}

function WorkLine({ label, activityId }: { label: string; activityId?: ID }) {
  const openPanelAt = useNori((s) => s.openPanelAt);
  const setFocus = useNori((s) => s.setFocusActivity);
  return (
    <div className="anim-fade mt-4 flex items-center gap-2 text-meta text-ink-2" role="status">
      <NoriMark size={16} state="thinking" />
      <span>{label}</span>
      {activityId && (
        <button
          type="button"
          className="font-medium text-accent-text hover:underline"
          onClick={() => {
            openPanelAt("activity", null);
            requestAnimationFrame(() => setFocus(activityId));
          }}
        >
          Details
        </button>
      )}
    </div>
  );
}

function UnitView({ threadId, unit, animate }: { threadId: ID; unit: Unit; animate: (id: ID) => boolean }) {
  switch (unit.type) {
    case "sep":
      return <div className="my-6 text-center text-caption text-ink-3">{unit.label}</div>;
    case "page":
      return (
        <div className={cn(animate(unit.item.id) && "arrive")} style={{ animationDelay: "60ms" }}>
          <PageBlock threadId={threadId} item={unit.item} body={unit.body} />
        </div>
      );
    case "approval":
      return (
        <div className={cn("mt-1 max-w-[440px]", animate(unit.item.id) && "arrive")} data-item-id={unit.item.id}>
          <ApprovalCard approvalId={unit.item.approvalId} />
        </div>
      );
    case "group": {
      const n = unit.units.length;
      return (
        <div className={cn("mt-4 flex flex-col gap-1", unit.sender === "me" ? "items-end" : "items-start")}>
          {unit.units.map((u, i) => {
            const pos = posOf(i, n);
            const isNew = animate(u.item.id);
            const style = isNew && unit.sender === "nori" ? { animationDelay: `${i * 60}ms` } : undefined;
            const cls = cn("flex w-full", unit.sender === "me" ? "justify-end" : "justify-start", isNew && "arrive");
            return (
              <div key={u.key} className={cls} style={style}>
                {u.type === "bubble" && <BubbleUnit threadId={threadId} unit={u} pos={pos} sender={unit.sender} />}
                {u.type === "voice" && <VoiceUnit threadId={threadId} item={u.item} pos={pos} sender={unit.sender} />}
                {u.type === "image" && <ImageUnit threadId={threadId} item={u.item} pos={pos} sender={unit.sender} />}
                {u.type === "artifact" && <ArtifactUnit threadId={threadId} item={u.item} artifactId={u.artifactId} pos={pos} sender={unit.sender} />}
              </div>
            );
          })}
        </div>
      );
    }
  }
}

export function Thread({
  threadId,
  onUnread,
  presence,
}: {
  threadId: ID;
  onUnread: (count: number, jump: () => void) => void;
  /** The presence medallion floats at top center, so content fades out lower. */
  presence: boolean;
}) {
  const thread = useThread(threadId);
  const arrived = useNori((s) => s.arrived);
  const typing = useNori((s) => s.typing === threadId);
  const work = useNori((s) => (s.work?.threadId === threadId ? s.work : null));
  const liveError = useNori((s) => (s.liveError?.threadId === threadId ? s.liveError : null));
  const highlight = useNori((s) => s.highlight);
  const items = useMemo(() => thread?.items ?? [], [thread?.items]);
  const units = useMemo(() => buildUnits(items), [items]);
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const seen = useRef(items.length);
  const [firstUnread, setFirstUnread] = useState<ID | null>(null);
  const [unread, setUnread] = useState(0);
  const [announcement, setAnnouncement] = useState("");

  const scrollToBottom = (smooth = true) => {
    const el = scroller.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: smooth ? "smooth" : "auto" });
  };

  // Opens at the bottom (the thread remounts per chat, so this covers switching too).
  useLayoutEffect(() => {
    scrollToBottom(false);
  }, []);

  // New arrivals: follow along if you're at the bottom, otherwise count them.
  useEffect(() => {
    const added = items.slice(seen.current);
    seen.current = items.length;
    if (!added.length) return;
    const fromNori = added.filter((i) => i.sender === "nori");
    const lastNori = fromNori[fromNori.length - 1];
    if (lastNori && lastNori.kind === "text") setAnnouncement(`Nori: ${firstSentence(lastNori.text)}`);
    const mine = added.some((i) => i.sender === "me");
    if (atBottom.current || mine) {
      requestAnimationFrame(() => scrollToBottom(true));
    } else if (fromNori.length) {
      setUnread((n) => n + fromNori.length);
      setFirstUnread((f) => f ?? fromNori[0].id);
    }
  }, [items]);

  useEffect(() => {
    if ((typing || work) && atBottom.current) requestAnimationFrame(() => scrollToBottom(true));
  }, [typing, work]);

  // Anything that grows the thread (a reply being written, an image decoding)
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
      setUnread(0);
      setFirstUnread(null);
    };
    onUnread(unread, jump);
  }, [unread, firstUnread, onUnread]);

  // "Show in chat" from Activity: scroll to the message and flash it.
  useEffect(() => {
    if (!highlight) return;
    const el = scroller.current?.querySelector<HTMLElement>(`[data-item-id="${highlight.itemId}"]`);
    if (!el) return;
    scrollWithin(el, "center");
    const target = (el.querySelector("[style*='border-radius']") as HTMLElement | null) ?? el;
    target.classList.remove("flash");
    void target.offsetWidth;
    target.classList.add("flash");
  }, [highlight]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    if (atBottom.current && unread) {
      setUnread(0);
      setFirstUnread(null);
    }
  };

  if (!thread) return null;
  const animate = (id: ID) => !!arrived[id];

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
          {units.length === 0 && !typing && thread.kind === "side" && (
            <div className="grid min-h-[50vh] place-items-center text-center text-meta text-ink-3">Say hi to start this side chat.</div>
          )}
          {units.length === 0 && !typing && thread.kind !== "side" && (
            <div className="grid min-h-[calc(100dvh-320px)] place-items-center">
              <p className="arrive text-title text-ink-2" style={{ animationDelay: "250ms" }}>
                {greeting()}
              </p>
            </div>
          )}
          {units.map((u) => (
            <UnitView key={u.key} threadId={threadId} unit={u} animate={animate} />
          ))}
          <Draft threadId={threadId} />
          {typing && <Typing />}
          {work && <WorkLine label={work.label} activityId={work.activityId} />}
          {liveError && (
            <div className="anim-fade mt-4 flex items-center gap-2 text-meta text-ink-2" role="alert">
              <span>{liveError.message}</span>
              <button type="button" onClick={() => retryLive(threadId)} className="font-medium text-accent-text hover:underline">
                Retry
              </button>
            </div>
          )}
        </div>
        <div className="sr-only">{announcement}</div>
      </div>
    </div>
  );
}
