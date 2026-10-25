"use client";

import { useLayoutEffect, useRef } from "react";
import { ArrowDown, ArrowUp, Square } from "lucide-react";
import { IconButton } from "@/components/ui/primitives";

/** Current chat composer, restricted to the runtime's text-only contract. */
export function RuntimeComposer({
  draft,
  setDraft,
  disabled,
  busy,
  canUpdate,
  stopping,
  stopDisabled,
  onSend,
  onStop,
  unread,
  onJump,
}: {
  draft: string;
  setDraft: (draft: string) => void;
  disabled: boolean;
  busy: boolean;
  canUpdate: boolean;
  stopping: boolean;
  stopDisabled: boolean;
  onSend: () => void;
  onStop: () => void;
  unread: number;
  onJump: () => void;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
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
    <div className="pointer-events-none absolute inset-x-0 bottom-0 z-10 pb-5">
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
          {canUpdate && (
            <p className="mb-1 px-2 text-caption text-ink-3">
              Your message updates the current task.
            </p>
          )}
          <div className="flex items-end gap-2 pl-2">
            <textarea
              ref={area}
              rows={1}
              value={draft}
              maxLength={8192}
              disabled={disabled || (busy && !canUpdate)}
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
              placeholder={
                canUpdate
                  ? "Answer or update this task"
                  : busy
                    ? "Nori is working…"
                    : "Message Nori"
              }
              aria-label="Message Nori"
              className="scrollbar-quiet max-h-[196px] min-h-8 min-w-0 flex-1 resize-none bg-transparent py-1 text-input text-ink transition-[height] duration-150 ease-out placeholder:text-ink-3 focus-visible:outline-none disabled:opacity-60"
            />
            {busy && (
              <IconButton
                size={32}
                label={stopping ? "Stopping…" : "Stop task"}
                disabled={stopDisabled}
                onClick={onStop}
                className="bg-fill-2 text-danger disabled:opacity-50"
              >
                <Square size={14} strokeWidth={2} fill="currentColor" />
              </IconButton>
            )}
            {(!busy || canUpdate) && (
              <button
                type="submit"
                aria-label={canUpdate ? "Send update" : "Send"}
                disabled={disabled || !draft.trim()}
                className="grid size-8 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-40"
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
