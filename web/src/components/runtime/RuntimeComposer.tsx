"use client";

import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { ArrowDown, ArrowUp, Paperclip, RotateCw, X } from "lucide-react";
import { useBlobURL } from "@/lib/useBlobURL";
import { hasSubmission, MAX_FILE_BYTES, MAX_MESSAGE_BYTES } from "@/lib/submission";
import type { DraftFile } from "@/lib/types";

const ACCEPT = "image/png,image/jpeg,image/webp,application/pdf,text/csv,.csv";

/** A small preview of an attached image, released when the chip goes away. */
function Thumbnail({ file }: { file: File }) {
  const load = useCallback(async () => file as Blob, [file]);
  const preview = useBlobURL(load);
  // eslint-disable-next-line @next/next/no-img-element -- blob URL
  return preview?.url ? <img src={preview.url} alt="" className="-ml-1 size-6 shrink-0 rounded-full object-cover" /> : null;
}

/** True when the picker's accept list (MIME types, wildcards and extensions) admits the file. */
function accepted(file: File, accept: string) {
  const name = file.name.toLowerCase();
  return accept.split(",").map((entry) => entry.trim().toLowerCase()).filter(Boolean).some((entry) =>
    entry.startsWith(".") ? name.endsWith(entry) : entry.endsWith("/*") ? file.type.startsWith(entry.slice(0, -1)) : file.type === entry);
}

export function RuntimeComposer({
  accept = ACCEPT,
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
  accept?: string;
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
  const [notice, setNotice] = useState("");
  const [dragging, setDragging] = useState(false);
  const take = (picked: File[]) => {
    const ok = picked.filter((file) => accepted(file, accept));
    const refused = picked.filter((file) => !accepted(file, accept));
    setNotice(refused.length ? `${refused.map((file) => file.name).join(", ")} ${refused.length === 1 ? "isn’t a type" : "aren’t types"} Nori can read here.` : "");
    if (ok.length) onAttach(ok);
  };
  const area = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const canSend = !disabled && hasSubmission(draft, files);
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
    <div className="nori-composer relative z-10 shrink-0 pb-5">
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
            if (canSend) onSend();
          }}
          onDragOver={(e) => { if (disabled || !e.dataTransfer.types.includes("Files")) return; e.preventDefault(); setDragging(true); }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => { if (disabled) return; e.preventDefault(); setDragging(false); take([...e.dataTransfer.files]); }}
          className={`glass t-fast rounded-[28px] p-3 transition-[border-color] focus-within:border-hairline-strong ${dragging ? "border-accent! bg-accent-subtle" : ""}`}
        >
          {notice && <p role="alert" className="mb-2 pl-2 text-meta text-danger">{notice}</p>}
          {files.length > 0 && (
            <ul aria-label="Attached files" className="mb-2 flex flex-wrap gap-1.5 pl-2">
              {files.map((file) => (
                <li key={file.id} className="flex max-w-full items-center gap-1 rounded-full border border-hairline bg-surface px-2.5 py-1 text-meta text-ink-2">
                  {file.file.type.startsWith("image/") && <Thumbnail file={file.file} />}
                  <span className="min-w-0 truncate">{file.name}</span>
                  {file.status === "uploading" && <span className="shrink-0 text-ink-3" role="status">{file.progress !== undefined ? `${Math.round(file.progress * 100)}%` : "Uploading…"}</span>}
                  {file.status === "error" && <span className="min-w-0 truncate text-danger" role="alert">{file.error}</span>}
                  {file.status === "error" && file.size <= MAX_FILE_BYTES && <button type="button" aria-label={`Retry ${file.name}`} onClick={() => onRetryFile(file)}><RotateCw size={13} aria-hidden /></button>}
                  <button type="button" aria-label={file.status === "uploading" ? `Cancel uploading ${file.name}` : `Remove ${file.name}`} onClick={() => onRemoveFile(file.id)} className="grid size-6 place-items-center rounded-full hover:bg-fill-1"><X size={13} aria-hidden /></button>
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
                take([...(e.target.files ?? [])]);
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
              maxLength={MAX_MESSAGE_BYTES}
              disabled={disabled}
              onChange={(e) => setDraft(e.target.value)}
              onPaste={(e) => {
                const pasted = [...e.clipboardData.files];
                if (!pasted.length) return;
                e.preventDefault();
                take(pasted);
              }}
              onKeyDown={(e) => {
                if (
                  e.key === "Enter" &&
                  !e.shiftKey &&
                  !e.nativeEvent.isComposing
                ) {
                  e.preventDefault();
                  if (canSend) onSend();
                }
              }}
              placeholder="Ask Nori anything…"
              aria-label="Message Nori"
              className="scrollbar-quiet max-h-[196px] min-h-8 min-w-0 flex-1 resize-none bg-transparent py-1 text-input text-ink transition-[height] duration-150 ease-out placeholder:text-ink-3 focus-visible:outline-none disabled:opacity-60"
            />
            <button
              type="submit"
              aria-label="Send"
              disabled={!canSend}
              className="nori-send grid size-8 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-40"
            >
              <ArrowUp size={18} strokeWidth={2} />
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
