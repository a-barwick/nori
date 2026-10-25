"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Popover } from "radix-ui";
import { ArrowDown, ArrowUp, FileUp, ImagePlus, LibraryBig, Mic, Plus, X } from "lucide-react";
import { IconButton } from "@/components/ui/primitives";
import { imageItem, sendItems, sendVoice } from "@/lib/nori/engine";
import { artifactTypeLabel, getNori, useNori } from "@/lib/store";
import { duration } from "@/lib/time";
import type { ID, ThreadItem } from "@/lib/types";
import { cn, uid } from "@/lib/utils";
import { excerptOf, Waveform } from "./Message";

type Attachment =
  | { id: ID; kind: "image"; src: string; name: string; w: number; h: number }
  | { id: ID; kind: "library"; artifactId: ID; title: string }
  | { id: ID; kind: "file"; name: string; size: number };

const ATTACH_ROWS = [
  { kind: "image", label: "Image", icon: ImagePlus },
  { kind: "file", label: "File", icon: FileUp },
  { kind: "library", label: "From Library", icon: LibraryBig },
] as const;

const LINE = 24;
const MAX_LINES = 8;

const MAX_IMAGE_SIDE = 1280;

/** Attached photos are shrunk to 1280px JPEG so a phone photo is ~200 KB, not 5 MB. */
function readImage(file: File): Promise<Attachment> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      const scale = Math.min(1, MAX_IMAGE_SIDE / Math.max(img.naturalWidth, img.naturalHeight));
      const w = Math.round(img.naturalWidth * scale);
      const h = Math.round(img.naturalHeight * scale);
      const canvas = document.createElement("canvas");
      canvas.width = w;
      canvas.height = h;
      canvas.getContext("2d")!.drawImage(img, 0, 0, w, h);
      URL.revokeObjectURL(url);
      resolve({ id: uid("att"), kind: "image", src: canvas.toDataURL("image/jpeg", 0.82), name: file.name || "Pasted image", w, h });
    };
    img.onerror = (e) => {
      URL.revokeObjectURL(url);
      reject(e);
    };
    img.src = url;
  });
}

/* ── Voice recording: live waveform, timer, cancel, send ─────── */

interface SpeechRecognitionLike {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((e: { results: ArrayLike<ArrayLike<{ transcript: string }>> }) => void) | null;
  start: () => void;
  stop: () => void;
}

function useRecorder(threadId: ID) {
  const [state, setState] = useState<{ startedAt: number; elapsed: number; levels: number[] } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const parts = useRef<{
    stream: MediaStream;
    ctx: AudioContext;
    rec: MediaRecorder;
    chunks: Blob[];
    timer: ReturnType<typeof setInterval>;
    levels: number[];
    transcript: string;
    recog?: SpeechRecognitionLike;
  } | null>(null);

  const start = async () => {
    setError(null);
    setPending(true);
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      setPending(false);
      const ctx = new AudioContext();
      const analyser = ctx.createAnalyser();
      analyser.fftSize = 512;
      ctx.createMediaStreamSource(stream).connect(analyser);
      const rec = new MediaRecorder(stream);
      const chunks: Blob[] = [];
      rec.ondataavailable = (e) => e.data.size && chunks.push(e.data);
      rec.start();
      const data = new Uint8Array(analyser.fftSize);
      const levels: number[] = [];
      const startedAt = Date.now();
      const timer = setInterval(() => {
        analyser.getByteTimeDomainData(data);
        let sum = 0;
        for (const v of data) sum += ((v - 128) / 128) ** 2;
        levels.push(Math.min(1, Math.sqrt(sum / data.length) * 5));
        setState({ startedAt, elapsed: Date.now() - startedAt, levels: levels.slice(-90) });
      }, 90);
      const SR = (window as unknown as { SpeechRecognition?: new () => SpeechRecognitionLike; webkitSpeechRecognition?: new () => SpeechRecognitionLike })
        .SpeechRecognition ?? (window as unknown as { webkitSpeechRecognition?: new () => SpeechRecognitionLike }).webkitSpeechRecognition;
      let recog: SpeechRecognitionLike | undefined;
      parts.current = { stream, ctx, rec, chunks, timer, levels, transcript: "" };
      if (SR) {
        recog = new SR();
        recog.continuous = true;
        recog.interimResults = true;
        recog.lang = "en-US";
        recog.onresult = (e) => {
          if (parts.current) parts.current.transcript = Array.from(e.results).map((r) => r[0].transcript).join(" ").trim();
        };
        try {
          recog.start();
          parts.current.recog = recog;
        } catch {
          /* recognition is optional */
        }
      }
      getNori().set({ markState: "listening" });
      setState({ startedAt, elapsed: 0, levels: [] });
    } catch {
      setPending(false);
      setError("Microphone is blocked");
      setTimeout(() => setError(null), 3000);
    }
  };

  const stop = async (send: boolean) => {
    const p = parts.current;
    if (!p) return;
    parts.current = null;
    clearInterval(p.timer);
    const elapsed = Date.now() - (state?.startedAt ?? Date.now());
    setState(null);
    getNori().set({ markState: "idle" });
    const stopped = new Promise<void>((r) => (p.rec.onstop = () => r()));
    p.rec.stop();
    p.recog?.stop();
    await stopped;
    p.stream.getTracks().forEach((t) => t.stop());
    void p.ctx.close();
    if (!send || elapsed < 600) return;
    await new Promise((r) => setTimeout(r, 500)); // let the last transcript result land
    const blob = new Blob(p.chunks, { type: p.rec.mimeType });
    const bars = 36;
    const step = Math.max(1, p.levels.length / bars);
    const waveform = Array.from({ length: bars }, (_, i) => {
      const slice = p.levels.slice(Math.floor(i * step), Math.floor((i + 1) * step));
      const v = slice.length ? Math.max(...slice) : 0.1;
      return Math.max(0.12, Math.min(1, v));
    });
    await sendVoice(threadId, {
      durationMs: elapsed,
      waveform,
      transcript: p.transcript || undefined,
      audioUrl: URL.createObjectURL(blob),
    });
  };

  return { recording: state, pending, error, start, stop };
}

export function Composer({ threadId, unread, onJump }: { threadId: ID; unread: number; onJump: () => void }) {
  const draft = useNori((s) => s.drafts[threadId] ?? "");
  const setDraft = useNori((s) => s.setDraft);
  const replyTo = useNori((s) => (s.replyTo?.threadId === threadId || (s.replyTo && !s.replyTo.itemId) ? s.replyTo : null));
  const editingId = useNori((s) => s.editingId);
  const focusNonce = useNori((s) => s.composerFocus);
  const set = useNori((s) => s.set);
  const updateItem = useNori((s) => s.updateItem);
  const artifacts = useNori((s) => s.artifacts);
  const quote = useNori((s) => {
    if (!replyTo) return null;
    if (replyTo.artifactId && !replyTo.itemId) return s.artifacts.find((a) => a.id === replyTo.artifactId)?.title ?? null;
    const item = s.threads.find((t) => t.id === threadId)?.items.find((i) => i.id === replyTo.itemId);
    return item ? excerptOf(item, s.artifacts) : null;
  });
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [attachOpen, setAttachOpen] = useState(false);
  const [libraryPick, setLibraryPick] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  const imageInput = useRef<HTMLInputElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const recorder = useRecorder(threadId);
  const hasContent = draft.trim().length > 0 || attachments.length > 0;

  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    // Measure at auto, then ease from the old height to the new one.
    const prev = el.style.height;
    el.style.height = "auto";
    const next = `${Math.min(el.scrollHeight, LINE * MAX_LINES + 4)}px`;
    el.style.height = prev;
    void el.offsetHeight;
    el.style.height = next;
  }, [draft]);

  // Nori looks down at the composer while you write to it.
  useEffect(() => {
    const s = getNori();
    const writing = draft.trim().length > 0;
    if (writing && s.markState === "idle") s.set({ markState: "attentive" });
    if (!writing && s.markState === "attentive") s.set({ markState: "idle" });
  }, [draft]);

  useEffect(() => {
    if (!focusNonce) return;
    const el = area.current;
    if (!el) return;
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
  }, [focusNonce]);

  const send = async () => {
    const text = draft.trim();
    if (!text && !attachments.length) return;
    if (editingId) {
      updateItem(threadId, editingId, { text, edited: true });
      set({ editingId: null });
      setDraft(threadId, "");
      return;
    }
    const s = getNori();
    const items: ThreadItem[] = [];
    if (replyTo?.artifactId && !replyTo.itemId) {
      items.push({ id: uid("att"), kind: "attachment", sender: "me", at: Date.now(), artifactId: replyTo.artifactId });
    }
    for (const a of attachments) {
      if (a.kind === "image") items.push(imageItem(a.src, a.name, a.w, a.h));
      if (a.kind === "library") items.push({ id: uid("att"), kind: "attachment", sender: "me", at: Date.now(), artifactId: a.artifactId });
      if (a.kind === "file") {
        const artifactId = s.addArtifact({ title: a.name, type: "file", madeBy: "you", thumb: "file", sizeLabel: `${Math.max(1, Math.round(a.size / 1024))} KB` });
        items.push({ id: uid("att"), kind: "attachment", sender: "me", at: Date.now(), artifactId });
      }
    }
    if (text) items.push({ id: uid("m"), kind: "text", sender: "me", at: Date.now(), text, replyToId: replyTo?.itemId });
    setDraft(threadId, "");
    setAttachments([]);
    set({ replyTo: null });
    await sendItems(threadId, items);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void send();
      return;
    }
    if (e.key === "ArrowUp" && !draft) {
      const items = getNori().threads.find((t) => t.id === threadId)?.items ?? [];
      const last = [...items].reverse().find((i) => i.sender === "me" && i.kind === "text");
      if (last && last.kind === "text") {
        e.preventDefault();
        set({ editingId: last.id, replyTo: null });
        setDraft(threadId, last.text);
      }
      return;
    }
    if (e.key === "Escape" && (replyTo || editingId || attachments.length)) {
      e.preventDefault();
      e.stopPropagation();
      if (editingId) setDraft(threadId, "");
      set({ replyTo: null, editingId: null });
      setAttachments([]);
    }
  };

  const onPaste = async (e: React.ClipboardEvent) => {
    const files = Array.from(e.clipboardData.files).filter((f) => f.type.startsWith("image/"));
    if (!files.length) return;
    e.preventDefault();
    const read = await Promise.all(files.map(readImage));
    setAttachments((a) => [...a, ...read]);
  };

  const strip = editingId ? "Editing your message" : quote;

  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-0 z-10 pb-5">
      <div className="pointer-events-auto relative mx-auto max-w-[768px] px-[clamp(24px,calc(72px-(100vw-768px)/2),72px)]">
        {unread > 0 && (
          <div className="absolute -top-12 left-1/2 -translate-x-1/2">
            <button
              type="button"
              onClick={onJump}
              className="anim-pop flex h-8 items-center gap-1 rounded-full bg-accent px-3.5 text-meta font-medium text-on-accent shadow-float hover:bg-accent-hover"
            >
              {unread} new <ArrowDown size={14} strokeWidth={2} aria-hidden />
            </button>
          </div>
        )}

        <div className="glass t-fast rounded-[28px] p-3 transition-[border-color] focus-within:border-hairline-strong">
          {strip && (
            <div className="mb-2 flex items-center gap-2 pr-1 pl-2">
              <span className="h-5 w-0.5 shrink-0 rounded-full bg-accent" aria-hidden />
              <span className="min-w-0 flex-1 truncate text-meta text-ink-2">{strip}</span>
              <IconButton
                size={28}
                label={editingId ? "Cancel edit" : "Cancel reply"}
                onClick={() => {
                  if (editingId) setDraft(threadId, "");
                  set({ replyTo: null, editingId: null });
                }}
              >
                <X size={14} strokeWidth={1.5} />
              </IconButton>
            </div>
          )}

          {attachments.length > 0 && (
            <div className="mb-2 flex flex-wrap gap-2 px-1">
              {attachments.map((a) => (
                <div key={a.id} className="relative">
                  {a.kind === "image" ? (
                    // eslint-disable-next-line @next/next/no-img-element
                    <img src={a.src} alt={a.name} className="size-16 rounded-tile object-cover shadow-[inset_0_0_0_1px_var(--hairline)]" />
                  ) : (
                    <div className="flex h-16 max-w-48 items-center rounded-tile bg-surface px-3 text-meta text-ink shadow-[inset_0_0_0_1px_var(--hairline)]">
                      <span className="truncate">{a.kind === "library" ? a.title : a.name}</span>
                    </div>
                  )}
                  <button
                    type="button"
                    aria-label="Remove attachment"
                    onClick={() => setAttachments((list) => list.filter((x) => x.id !== a.id))}
                    className="absolute -top-1.5 -right-1.5 grid size-5 place-items-center rounded-full bg-ink text-surface"
                  >
                    <X size={11} strokeWidth={2} />
                  </button>
                </div>
              ))}
            </div>
          )}

          {recorder.recording ? (
            <div className="flex h-8 items-center gap-3">
              <IconButton size={32} label="Cancel recording" onClick={() => void recorder.stop(false)}>
                <X size={18} strokeWidth={1.5} />
              </IconButton>
              <div className="flex min-w-0 flex-1 justify-end overflow-hidden">
                <Waveform bars={recorder.recording.levels.map((v) => Math.max(0.1, v))} progress={1} height={22} />
              </div>
              <span className="font-mono text-mono-data text-ink-2 tabular-nums">{duration(recorder.recording.elapsed)}</span>
              <button
                type="button"
                aria-label="Send voice note"
                onClick={() => void recorder.stop(true)}
                className="grid size-8 place-items-center rounded-full bg-accent text-on-accent hover:bg-accent-hover"
              >
                <ArrowUp size={18} strokeWidth={2} />
              </button>
            </div>
          ) : (
            <div className="flex items-end gap-2">
              <Popover.Root
                open={attachOpen}
                onOpenChange={(o) => {
                  setAttachOpen(o);
                  if (!o) setLibraryPick(false);
                }}
              >
                <Popover.Trigger asChild>
                  <IconButton size={32} label="Attach">
                    <Plus size={20} strokeWidth={1.5} />
                  </IconButton>
                </Popover.Trigger>
                <Popover.Portal>
                  <Popover.Content side="top" align="start" sideOffset={10} className="menu z-[60] w-64 rounded-card bg-surface p-1.5 shadow-pop">
                    {libraryPick ? (
                      <div className="scrollbar-quiet max-h-72 overflow-y-auto">
                        {artifacts.map((a) => (
                          <button
                            key={a.id}
                            type="button"
                            onClick={() => {
                              setAttachments((list) => [...list, { id: uid("att"), kind: "library", artifactId: a.id, title: a.title }]);
                              setAttachOpen(false);
                              setLibraryPick(false);
                            }}
                            className="flex w-full flex-col rounded-chip px-2.5 py-2 text-left hover:bg-fill-1"
                          >
                            <span className="truncate text-row-title text-ink">{a.title}</span>
                            <span className="text-caption text-ink-3">{artifactTypeLabel[a.type]}</span>
                          </button>
                        ))}
                      </div>
                    ) : (
                      ATTACH_ROWS.map((row) => (
                        <button
                          key={row.label}
                          type="button"
                          onClick={() => {
                            if (row.kind === "library") return setLibraryPick(true);
                            (row.kind === "image" ? imageInput : fileInput).current?.click();
                            setAttachOpen(false);
                          }}
                          className="flex h-9 w-full items-center gap-2.5 rounded-chip px-2.5 text-left text-row-title text-ink hover:bg-fill-1"
                        >
                          <row.icon size={18} strokeWidth={1.5} className="text-ink-2" aria-hidden />
                          {row.label}
                        </button>
                      ))
                    )}
                  </Popover.Content>
                </Popover.Portal>
              </Popover.Root>
              <input
                ref={imageInput}
                type="file"
                accept="image/*"
                multiple
                hidden
                onChange={async (e) => {
                  const files = Array.from(e.target.files ?? []);
                  e.target.value = "";
                  const read = await Promise.all(files.map(readImage));
                  setAttachments((a) => [...a, ...read]);
                }}
              />
              <input
                ref={fileInput}
                type="file"
                multiple
                hidden
                onChange={(e) => {
                  const files = Array.from(e.target.files ?? []);
                  e.target.value = "";
                  setAttachments((a) => [...a, ...files.map((f) => ({ id: uid("att"), kind: "file" as const, name: f.name, size: f.size }))]);
                }}
              />
              <textarea
                ref={area}
                rows={1}
                value={draft}
                onChange={(e) => setDraft(threadId, e.target.value)}
                onKeyDown={onKeyDown}
                onPaste={onPaste}
                placeholder="Message Nori"
                aria-label="Message Nori"
                className="scrollbar-quiet max-h-[196px] min-h-8 flex-1 resize-none bg-transparent transition-[height] duration-150 ease-out py-1 text-input text-ink placeholder:text-ink-3 focus-visible:outline-none"
              />
              {(recorder.error || recorder.pending) && (
                <span className="self-center text-caption text-ink-3" role="status">
                  {recorder.error ?? "Waiting for the microphone…"}
                </span>
              )}
              {hasContent ? (
                <button
                  type="button"
                  aria-label="Send"
                  onClick={() => void send()}
                  className="anim-pop grid size-8 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition-colors hover:bg-accent-hover"
                >
                  <ArrowUp size={18} strokeWidth={2} />
                </button>
              ) : (
                <IconButton size={32} label="Record a voice note" onClick={() => void recorder.start()} className={cn("anim-pop")}>
                  <Mic size={20} strokeWidth={1.5} />
                </IconButton>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
