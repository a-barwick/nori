"use client";

import { useLayoutEffect, useRef } from "react";
import { ArrowDown, ArrowUp } from "lucide-react";

/** Current chat composer, restricted to the runtime's text-only contract. */
export function RuntimeComposer({
  draft,
  setDraft,
  disabled,
  onSend,
  unread,
  onJump,
}: {
  draft: string;
  setDraft: (draft: string) => void;
  disabled: boolean;
  onSend: () => void;
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
          <div className="flex items-end gap-2 pl-2">
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
            <button
              type="submit"
              aria-label="Send"
              disabled={disabled || !draft.trim()}
              className="grid size-8 shrink-0 place-items-center rounded-full bg-accent text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-40"
            >
              <ArrowUp size={18} strokeWidth={2} />
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
