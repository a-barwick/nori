"use client";

import { useEffect, useRef } from "react";
import { Check, Copy } from "lucide-react";
import { useCopyToClipboard } from "@/lib/browser";
import { corners, type GroupUnit, type Pos } from "@/lib/units";
import type { MarkState, Sender } from "@/lib/types";
import { cn } from "@/lib/utils";
import { NoriMark } from "@/components/mark/NoriMark";
import { Markdown } from "./Markdown";
import { Parts } from "./Parts";

const LONG_PRESS_MS = 450;
const MOVE_CANCEL_PX = 8;

export function BubbleUnit({
  unit,
  pos,
  sender,
  showAvatar = false,
  markState = "idle",
  animateSend = false,
}: {
  unit: GroupUnit;
  pos: Pos;
  sender: Sender;
  showAvatar?: boolean;
  markState?: MarkState;
  animateSend?: boolean;
}) {
  const mine = sender === "me";
  const standaloneCard =
    !mine &&
    !unit.markdown &&
    !!unit.item.parts?.length &&
    unit.item.parts.every((part) => part.type === "reminder" || part.type === "card");

  const copyText = (unit.item.text || unit.markdown || "").trim();
  const { copied, copy } = useCopyToClipboard();
  const pressTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pressOrigin = useRef<{ x: number; y: number } | null>(null);

  useEffect(() => {
    return () => {
      if (pressTimer.current) clearTimeout(pressTimer.current);
    };
  }, []);

  const triggerCopy = () => {
    if (!copyText) return;
    copy(copyText);
  };

  const cancelLongPress = () => {
    if (pressTimer.current) {
      clearTimeout(pressTimer.current);
      pressTimer.current = null;
    }
    pressOrigin.current = null;
  };

  const onPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (!copyText || event.pointerType === "mouse") return;
    cancelLongPress();
    pressOrigin.current = { x: event.clientX, y: event.clientY };
    pressTimer.current = setTimeout(() => {
      pressTimer.current = null;
      triggerCopy();
    }, LONG_PRESS_MS);
  };

  const onPointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const origin = pressOrigin.current;
    if (!origin || !pressTimer.current) return;
    if (Math.hypot(event.clientX - origin.x, event.clientY - origin.y) > MOVE_CANCEL_PX) {
      cancelLongPress();
    }
  };

  return (
    <div
      className={cn(
        "nori-message group/msg relative flex items-start",
        mine ? "max-w-[min(560px,75%)]" : "max-w-[min(680px,100%)]",
        mine && animateSend && "nori-send-arrive",
      )}
      data-item-id={unit.item.id}
      data-copy-message={copyText ? "true" : undefined}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={cancelLongPress}
      onPointerCancel={cancelLongPress}
    >
      {!mine && (
        <span
          className="mt-2.5 mr-3 w-7 shrink-0"
          data-active-avatar={showAvatar ? "true" : undefined}
          aria-hidden={!showAvatar || undefined}
        >
          {showAvatar ? <NoriMark size={26} state={markState} /> : null}
        </span>
      )}
      {standaloneCard ? (
        <div className="min-w-0 flex-1">
          <Parts parts={unit.item.parts!} />
        </div>
      ) : (
        <div className={cn("min-w-0", mine ? "flex flex-col items-end" : "flex-1")}>
          <div
            className={cn(
              "min-w-0",
              mine
                ? "bg-accent px-4 py-2.5 text-on-accent"
                : "bg-bubble px-0 py-1.5 text-ink",
            )}
            style={mine ? { borderRadius: corners(sender, pos) } : undefined}
          >
            {unit.markdown && <Markdown onAccent={mine}>{unit.markdown}</Markdown>}
            {unit.item.parts && <Parts parts={unit.item.parts} />}
          </div>
          {copyText && (
            <div className={cn("mt-0.5 flex", mine ? "justify-end" : "justify-start")}>
              <button
                type="button"
                onClick={triggerCopy}
                aria-label={copied ? "Copied message" : "Copy message"}
                title={copied ? "Copied" : "Copy"}
                className={cn(
                  "t-fast -ml-1.5 inline-flex size-6 items-center justify-center rounded-md text-ink-3 transition-opacity hover:bg-fill-1 hover:text-ink focus-visible:opacity-100",
                  copied ? "opacity-100 text-accent-text" : "opacity-0 group-hover/msg:opacity-100",
                )}
              >
                {copied ? <Check size={13} strokeWidth={2.2} aria-hidden /> : <Copy size={13} strokeWidth={1.8} aria-hidden />}
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
