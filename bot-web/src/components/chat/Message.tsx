"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { AnimatePresence, motion } from "motion/react";
import { DropdownMenu, Popover } from "radix-ui";
import { Copy, CornerUpLeft, Ellipsis, MessageSquareText, Pause, Play, SmilePlus, Trash2 } from "lucide-react";
import { IconButton } from "@/components/ui/primitives";
import { chooseChip } from "@/lib/nori/engine";
import { artifactTypeLabel, useNori } from "@/lib/store";
import { clock, duration } from "@/lib/time";
import { corners, type GroupUnit, type Pos } from "@/lib/units";
import type { Chip, ChipSet, ID, ImageItem, Reaction, Sender, ThreadItem, VoiceItem } from "@/lib/types";
import { cn, plainLine } from "@/lib/utils";
import { Markdown } from "./Markdown";
import { Thumbnail } from "./Thumbnail";

const SPRING = { duration: 0.32, ease: [0.15, 1, 0.4, 1] as const };
const QUICK_REACTIONS = ["👍", "❤️", "😂", "😮", "🙏", "👀"];

export function excerptOf(item: ThreadItem, artifacts: { id: ID; title: string }[]) {
  switch (item.kind) {
    case "text":
      return plainLine(item.text);
    case "voice":
      return item.transcript ? plainLine(item.transcript) : `Voice note, ${duration(item.durationMs)}`;
    case "image":
      return "Photo";
    case "attachment":
      return artifacts.find((a) => a.id === item.artifactId)?.title ?? "Attachment";
    case "approval":
      return "Approval";
  }
}

/* ── Hover actions: React · Reply · More, opposite side, centered ── */

function HoverActions({ threadId, item, sender, copyText }: { threadId: ID; item: ThreadItem; sender: Sender; copyText?: string }) {
  const toggleReaction = useNori((s) => s.toggleReaction);
  const set = useNori((s) => s.set);
  const removeItem = useNori((s) => s.removeItem);
  const [open, setOpen] = useState<"react" | "more" | null>(null);

  return (
    <div
      className={cn(
        "t-fast absolute top-1/2 z-10 flex -translate-y-1/2 items-center gap-0.5 transition-opacity",
        sender === "nori" ? "left-full pl-2" : "right-full flex-row-reverse pr-2",
        open ? "opacity-100" : "opacity-0 group-hover/msg:opacity-100 group-focus-within/msg:opacity-100",
      )}
    >
      <Popover.Root open={open === "react"} onOpenChange={(o) => setOpen(o ? "react" : null)}>
        <Popover.Trigger asChild>
          <IconButton size={28} label="React" className="text-ink-3">
            <SmilePlus size={16} strokeWidth={1.5} />
          </IconButton>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content side="top" sideOffset={6} className="menu z-[60] flex gap-0.5 rounded-full bg-surface p-1 shadow-pop">
            {QUICK_REACTIONS.map((e) => (
              <button
                key={e}
                type="button"
                aria-label={`React ${e}`}
                onClick={() => {
                  toggleReaction(threadId, item.id, e);
                  setOpen(null);
                }}
                className="t-fast grid size-9 place-items-center rounded-full text-[20px] transition-transform hover:scale-110 hover:bg-fill-1"
              >
                {e}
              </button>
            ))}
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      <IconButton
        size={28}
        label="Reply"
        className="text-ink-3"
        onClick={() => set({ replyTo: { threadId, itemId: item.id }, editingId: null, composerFocus: Date.now() })}
      >
        <CornerUpLeft size={16} strokeWidth={1.5} />
      </IconButton>
      <DropdownMenu.Root open={open === "more"} onOpenChange={(o) => setOpen(o ? "more" : null)}>
        <DropdownMenu.Trigger asChild>
          <IconButton size={28} label="More" className="text-ink-3">
            <Ellipsis size={16} strokeWidth={1.5} />
          </IconButton>
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content sideOffset={6} className="menu z-[60] min-w-40 rounded-card bg-surface p-1.5 shadow-pop">
            {copyText && (
              <DropdownMenu.Item
                onSelect={() => void navigator.clipboard?.writeText(copyText)}
                className="flex h-9 cursor-default items-center gap-2.5 rounded-chip px-2.5 text-row-title text-ink outline-none data-[highlighted]:bg-fill-1"
              >
                <Copy size={16} strokeWidth={1.5} className="text-ink-2" /> Copy text
              </DropdownMenu.Item>
            )}
            {sender === "me" && (
              <DropdownMenu.Item
                onSelect={() => removeItem(threadId, item.id)}
                className="flex h-9 cursor-default items-center gap-2.5 rounded-chip px-2.5 text-row-title text-danger outline-none data-[highlighted]:bg-danger-bg"
              >
                <Trash2 size={16} strokeWidth={1.5} /> Delete
              </DropdownMenu.Item>
            )}
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
    </div>
  );
}

function Reactions({ reactions, sender }: { reactions: Reaction[]; sender: Sender }) {
  return (
    <div className={cn("absolute -bottom-2 z-[1] flex", sender === "nori" ? "-right-2" : "-left-2 flex-row-reverse")} aria-label="Reactions">
      {reactions.map((r, i) => (
        <span
          key={`${r.emoji}-${r.by}`}
          className={cn("anim-pop grid size-6 place-items-center rounded-full bg-surface text-[13px] shadow-[0_0_0_1px_var(--hairline)]", i > 0 && (sender === "nori" ? "-ml-1.5" : "-mr-1.5"))}
          title={r.by === "me" ? "You" : "Nori"}
        >
          {r.emoji}
        </span>
      ))}
    </div>
  );
}

/** Shell shared by every grouped unit: alignment, hover actions, reactions. */
function UnitFrame({
  threadId,
  item,
  sender,
  reactions,
  copyText,
  children,
  className,
}: {
  threadId: ID;
  item: ThreadItem;
  sender: Sender;
  reactions?: Reaction[];
  copyText?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("group/msg relative flex", sender === "me" ? "max-w-[min(560px,85%)]" : "max-w-[min(680px,100%)]", reactions?.length && "mb-2.5", className)} data-item-id={item.id}>
      {children}
      <HoverActions threadId={threadId} item={item} sender={sender} copyText={copyText} />
      {reactions?.length ? <Reactions reactions={reactions} sender={sender} /> : null}
    </div>
  );
}

/* ── Chips: inside Nori's last bubble, dashed = possible, not yet said ── */

function Chips({ threadId, itemId, chips }: { threadId: ID; itemId: ID; chips: ChipSet }) {
  const chosen = chips.options.find((c) => c.id === chips.chosenId);
  const busy = useNori((s) => s.typing === threadId || s.work?.threadId === threadId);
  return (
    <>
      <AnimatePresence initial={false}>
        {!chosen && (
          <motion.div key="chips" className="mt-2.5 flex flex-col gap-2 first:mt-0" exit={{ opacity: 0, transition: { duration: 0.15 } }}>
            {chips.options.map((c: Chip) => (
              <motion.button
                key={c.id}
                layoutId={`chip-${c.id}`}
                type="button"
                disabled={busy}
                onClick={() => void chooseChip(threadId, itemId, c)}
                transition={SPRING}
                className="t-fast w-full rounded-chip border border-dashed border-chip-border px-2.5 py-2 text-left text-input text-ink transition-[background-color,border-color] hover:border-solid hover:border-accent-signal hover:bg-accent-subtle disabled:opacity-60"
              >
                {c.label}
              </motion.button>
            ))}
          </motion.div>
        )}
      </AnimatePresence>
      {chosen && (
        <motion.p initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ duration: 0.2, delay: 0.1 }} className="mt-2 text-meta text-ink-2">
          You chose: {chosen.label.replace(/[.!?]$/, "")}.
        </motion.p>
      )}
    </>
  );
}

/* ── Units ─────────────────────────────────────────────────── */

export function BubbleUnit({
  threadId,
  unit,
  pos,
  sender,
}: {
  threadId: ID;
  unit: Extract<GroupUnit, { type: "bubble" }>;
  pos: Pos;
  sender: Sender;
}) {
  const quoted = useNori((s) => {
    if (unit.item.kind !== "text" || !unit.item.replyToId) return null;
    const t = s.threads.find((x) => x.id === threadId);
    const q = t?.items.find((i) => i.id === unit.item.replyToId);
    return q ? excerptOf(q, s.artifacts) : null;
  });
  const mine = sender === "me";
  const fromChip = unit.item.kind === "text" ? unit.item.fromChip : undefined;
  // Reactions sit on the item's last bubble: its chips bubble if it has one.
  const hasChips = unit.item.kind === "text" && !!unit.item.chips;
  const carriesReactions = unit.transcript || unit.key.endsWith("-chips") || (unit.item.kind === "text" && !hasChips);
  const reactions = carriesReactions ? unit.item.reactions : undefined;
  const edited = unit.item.kind === "text" && unit.item.edited;

  return (
    <UnitFrame threadId={threadId} item={unit.item} sender={sender} reactions={reactions} copyText={unit.markdown}>
      <motion.div
        layoutId={fromChip ? `chip-${fromChip}` : undefined}
        transition={SPRING}
        className={cn("min-w-0 px-[15px] py-[11px]", mine ? "bg-accent text-on-accent" : "bg-bubble text-ink")}
        style={{ borderRadius: corners(sender, pos) }}
      >
        <motion.div layout={fromChip ? "position" : false}>
          {quoted && (
            <div className={cn("mb-1.5 line-clamp-1 border-l-2 pl-2 text-meta", mine ? "border-on-accent/60 text-on-accent/85" : "border-hairline-strong text-ink-2")}>
              {quoted}
            </div>
          )}
          {unit.markdown && <Markdown onAccent={mine}>{unit.markdown}</Markdown>}
          {edited && <span className={cn("text-caption", mine ? "text-on-accent/75" : "text-ink-3")}> · edited</span>}
          {unit.chips && <Chips threadId={threadId} itemId={unit.item.id} chips={unit.chips} />}
        </motion.div>
      </motion.div>
    </UnitFrame>
  );
}

function startAudio(a: HTMLAudioElement, onProgress: (p: number) => void, onEnd: () => void) {
  a.currentTime = 0;
  a.ontimeupdate = () => onProgress(a.duration ? a.currentTime / a.duration : 0);
  a.onended = onEnd;
  void a.play();
}

function speak(text: string) {
  if (typeof speechSynthesis === "undefined") return;
  const u = new SpeechSynthesisUtterance(text);
  u.rate = 1.02;
  speechSynthesis.cancel();
  speechSynthesis.speak(u);
}

function useVoicePlayback(item: VoiceItem) {
  const [progress, setProgress] = useState(0);
  const [playing, setPlaying] = useState(false);
  const audio = useRef<HTMLAudioElement | null>(null);
  const raf = useRef(0);

  const stop = () => {
    cancelAnimationFrame(raf.current);
    audio.current?.pause();
    if (typeof speechSynthesis !== "undefined") speechSynthesis.cancel();
    setPlaying(false);
  };

  useEffect(
    () => () => {
      cancelAnimationFrame(raf.current);
      audio.current?.pause();
      if (typeof speechSynthesis !== "undefined") speechSynthesis.cancel();
    },
    [],
  );

  const play = () => {
    if (playing) return stop();
    setPlaying(true);
    if (item.audioUrl) {
      if (!audio.current) audio.current = new Audio(item.audioUrl);
      startAudio(audio.current, setProgress, () => {
        setPlaying(false);
        setProgress(0);
      });
      return;
    }
    // Seeded notes have no audio: read the transcript aloud and pace the waveform.
    const start = performance.now();
    if (item.transcript) speak(item.transcript);
    const tick = () => {
      const p = Math.min(1, (performance.now() - start) / item.durationMs);
      setProgress(p);
      if (p < 1) raf.current = requestAnimationFrame(tick);
      else {
        setPlaying(false);
        setProgress(0);
      }
    };
    raf.current = requestAnimationFrame(tick);
  };

  return { progress, playing, play };
}

export function Waveform({ bars, progress, onAccent, height = 24 }: { bars: number[]; progress: number; onAccent?: boolean; height?: number }) {
  const played = Math.round(progress * bars.length);
  return (
    <span className="flex items-center gap-[2px]" style={{ height }} aria-hidden>
      {bars.map((v, i) => (
        <span
          key={i}
          className="w-[2px] rounded-full"
          style={{
            height: Math.max(3, v * height),
            background: onAccent ? "var(--waveform-on-accent)" : "var(--text-primary)",
            opacity: i < played ? 1 : 0.4,
          }}
        />
      ))}
    </span>
  );
}

export function VoiceUnit({ threadId, item, pos, sender }: { threadId: ID; item: VoiceItem; pos: Pos; sender: Sender }) {
  const { progress, playing, play } = useVoicePlayback(item);
  const mine = sender === "me";
  return (
    <UnitFrame threadId={threadId} item={item} sender={sender} reactions={item.transcript ? undefined : item.reactions}>
      <div
        className={cn("flex h-[52px] items-center gap-2.5 px-3 py-2.5", mine ? "bg-accent text-on-accent" : "bg-bubble text-ink")}
        style={{ borderRadius: corners(sender, pos, 26) }}
      >
        <button
          type="button"
          onClick={play}
          aria-label={playing ? "Pause voice note" : "Play voice note"}
          className={cn("grid size-7 shrink-0 place-items-center rounded-full", mine ? "bg-surface text-accent" : "bg-ink text-surface")}
        >
          {playing ? <Pause size={13} strokeWidth={2} fill="currentColor" /> : <Play size={13} strokeWidth={2} fill="currentColor" className="ml-px" />}
        </button>
        <Waveform bars={item.waveform} progress={progress} onAccent={mine} />
        <span className={cn("font-mono text-mono-data tabular-nums", mine ? "text-on-accent/85" : "text-ink-2")}>
          {duration(playing ? item.durationMs * progress : item.durationMs)}
        </span>
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild>
            <button
              type="button"
              aria-label="Voice note options"
              className={cn("grid size-7 place-items-center rounded-full", mine ? "text-on-accent/85 hover:bg-accent-hover" : "text-ink-2 hover:bg-fill-2")}
            >
              <Ellipsis size={16} strokeWidth={1.5} />
            </button>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content sideOffset={6} className="menu z-[60] min-w-44 rounded-card bg-surface p-1.5 shadow-pop">
              <DropdownMenu.Item
                onSelect={() => item.transcript && void navigator.clipboard?.writeText(item.transcript)}
                className="flex h-9 cursor-default items-center gap-2.5 rounded-chip px-2.5 text-row-title text-ink outline-none data-[highlighted]:bg-fill-1"
              >
                <MessageSquareText size={16} strokeWidth={1.5} className="text-ink-2" /> Copy transcript
              </DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Portal>
        </DropdownMenu.Root>
      </div>
    </UnitFrame>
  );
}

export function ImageUnit({ threadId, item, pos, sender }: { threadId: ID; item: ImageItem; pos: Pos; sender: Sender }) {
  const set = useNori((s) => s.set);
  const ratio = item.w / item.h;
  // Nori's drawings carry small labels, so they get room to be read; photos stay thumbnails.
  const drawing = item.src.startsWith("data:image/svg+xml");
  const box = drawing ? 480 : 240;
  const w = ratio >= 1 ? box : Math.round(box * ratio);
  const h = ratio >= 1 ? Math.round(box / ratio) : box;
  return (
    <UnitFrame threadId={threadId} item={item} sender={sender} reactions={item.reactions}>
      <button
        type="button"
        onClick={() => item.src && set({ lightbox: { src: item.src, alt: item.alt } })}
        aria-label={`Open image: ${item.alt}`}
        className="overflow-hidden bg-fill-1 shadow-[inset_0_0_0_1px_var(--hairline)]"
        style={{ borderRadius: corners(sender, pos, 16), width: w, maxWidth: "100%", aspectRatio: `${w} / ${h}` }}
      >
        {item.src ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img src={item.src} alt={item.alt} width={w} height={h} className={cn("size-full", drawing ? "object-contain" : "object-cover")} draggable={false} />
        ) : (
          <span className="grid size-full place-items-center text-caption text-ink-3">Image unavailable</span>
        )}
      </button>
    </UnitFrame>
  );
}

export function ArtifactUnit({
  threadId,
  item,
  artifactId,
  pos,
  sender,
}: {
  threadId: ID;
  item: ThreadItem;
  artifactId: ID;
  pos: Pos;
  sender: Sender;
}) {
  const artifact = useNori((s) => s.artifacts.find((a) => a.id === artifactId));
  const set = useNori((s) => s.set);
  const router = useRouter();
  if (!artifact) return null;
  const open = () => {
    set({ viewerId: artifact.id, libraryFilter: "all" });
    router.push("/library");
  };
  return (
    <UnitFrame threadId={threadId} item={item} sender={sender} reactions={item.kind === "attachment" ? item.reactions : undefined}>
      <div
        className="group/tile relative w-[280px] overflow-hidden bg-surface shadow-[inset_0_0_0_1px_var(--hairline)]"
        style={{ borderRadius: corners(sender, pos, 16) }}
      >
        <button type="button" onClick={open} className="block w-full text-left" aria-label={`Open ${artifact.title}`}>
          <Thumbnail artifact={artifact} />
          <div className="px-3 pt-2.5 pb-3">
            <div className="truncate text-row-title font-medium text-ink">{artifact.title}</div>
            <div className="text-caption text-ink-3">
              {artifactTypeLabel[artifact.type]} · {clock(artifact.createdAt)}
            </div>
          </div>
        </button>
        <button
          type="button"
          onClick={() => set({ replyTo: { threadId, itemId: item.id, artifactId: artifact.id }, composerFocus: Date.now() })}
          className="glass t-fast absolute top-2.5 right-2.5 h-8 rounded-full px-3 text-meta font-medium text-ink opacity-0 transition-opacity group-hover/tile:opacity-100 focus-visible:opacity-100"
        >
          Discuss
        </button>
      </div>
    </UnitFrame>
  );
}
