"use client";

import { useLayoutEffect, useRef } from "react";
import { ArrowDown, ArrowUp, Paperclip, RotateCw, Square, X } from "lucide-react";
import type { DraftFile } from "@/lib/types";

const ACCEPT = "image/png,image/jpeg,image/webp,application/pdf,text/csv,.csv";

/** Chat composer: text, plus images, PDFs and CSV files. Each file uploads when it's picked. */
export function RuntimeComposer({
  accept = ACCEPT,
  onStop,
  draft,
  setDraft,
  disabled,
  onSend,
  unread,
  onJump,
  files,
  onAttach,
  onRetryFile,
  onRemoveFile,
}: {
  /** File types the picker offers. */
  accept?: string;
  /** While a reply streams, the send button stops it instead. */
  onStop?: () => void;
  draft: string;
  setDraft: (draft: string) => void;
  disabled: boolean;
  onSend: () => void;
  unread: number;
  onJump: () => void;
  files: DraftFile[];
  onAttach: (files: File[]) => void;
  onRetryFile: (file: DraftFile) => void;
  onRemoveFile: (id: string) => void;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const canSend = !disabled && files.every((file) => file.status === "ready") && (!!draft.trim() || files.length > 0);
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    const fit = () => {
      el.style.height = "auto";
      el.style.height = `${Math.min(el.scrollHeight, 196)}px`;
    };
    fit();
    let width = el.offsetWidth;
    const observer = new ResizeObserver(() => {
      if (el.offsetWidth === width) return;
      width = el.offsetWidth;
      fit();
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [draft]);

  return (
    <div className="nova-composer relative z-10 shrink-0 pb-5">
      <div className="pointer-events-auto relative mx-auto max-w-[768px] px-[clamp(24px,calc(72px-(100vw-768px)/2),72px)]">
        {unread > 0 && (
          <button
            type="button"
            onClick={onJump}
            className="anim-pop absolute -top-12 left-1/2 flex h-8 -translate-x-1/2 items-center gap-1 rounded-full bg-accent px-3.5 text-meta font-medium text-on-accent shadow-float"
          >
            {unread} new <ArrowDown size={14} aria-hidden />
          </button>
        )}
        <form
          onSubmit={(e) => {
            e.preventDefault();
            onSend();
          }}
          className="glass t-fast rounded-[28px] p-3 transition-[border-color] focus-within:border-hairline-strong"
        >
          {files.length > 0 && (
            <ul aria-label="Attached files" className="mb-2 flex flex-wrap gap-1.5 pl-2">
              {files.map((file) => (
                <li key={file.id} className="flex max-w-full items-center gap-1 rounded-full border border-hairline bg-surface px-2.5 py-1 text-meta text-ink-2">
                  <span className="min-w-0 truncate">{file.name}</span>
                  {file.status === "uploading" && <span className="shrink-0 text-ink-3">Uploading…</span>}
                  {file.status === "error" && <span className="min-w-0 truncate text-danger" role="alert">{file.error}</span>}
                  {file.status === "error" && file.size <= 10 * 1024 * 1024 && <button type="button" aria-label={`Retry ${file.name}`} onClick={() => onRetryFile(file)}><RotateCw size={13} aria-hidden /></button>}
                  <button type="button" aria-label={`Remove ${file.name}`} onClick={() => onRemoveFile(file.id)}><X size={13} aria-hidden /></button>
                </li>
              ))}
            </ul>
          )}
          <div className="flex items-end gap-2 pl-2">
            <input
              ref={picker}
              type="file"
              multiple
              hidden
              accept={accept}
              aria-label="Choose files to attach"
              onChange={(e) => {
                onAttach([...(e.target.files ?? [])]);
                e.target.value = "";
              }}
            />
            <button
              type="button"
              aria-label="Attach a file"
              disabled={disabled}
              onClick={() => picker.current?.click()}
              className="grid size-8 shrink-0 place-items-center rounded-full text-ink-2 transition-colors hover:bg-surface disabled:opacity-40"
            >
              <Paperclip size={18} strokeWidth={2} aria-hidden />
            </button>
            <textarea
              ref={area}
              rows={1}
              value={draft}
              maxLength={8192}
              disabled={disabled}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (
                  e.key === "Enter" &&
                  !e.shiftKey &&
                  !e.nativeEvent.isComposing
                ) {
                  e.preventDefault();
                  onSend();
                }
              }}
              placeholder="Ask Nori anything…"
              aria-label="Message Nori"
              className="scrollbar-quiet max-h-[196px] min-h-8 min-w-0 flex-1 resize-none bg-transparent py-1 text-input text-ink transition-[height] duration-150 ease-out placeholder:text-ink-3 focus-visible:outline-none disabled:opacity-60"
            />
            {onStop ? (
              <button
                type="button"
                aria-label="Stop reply"
                onClick={onStop}
                className="nova-send grid size-8 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition-colors hover:bg-accent-hover"
              >
                <Square size={14} strokeWidth={2} fill="currentColor" />
              </button>
            ) : (
              <button
                type="submit"
                aria-label="Send"
                disabled={!canSend}
                className="nova-send grid size-8 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-40"
              >
                <ArrowUp size={18} strokeWidth={2} />
              </button>
            )}
          </div>
        </form>
      </div>
    </div>
  );
}
