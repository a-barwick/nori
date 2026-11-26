"use client";

import { useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
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
  const [copied, setCopied] = useState(false);
  const pressTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pressOrigin = useRef<{ x: number; y: number } | null>(null);
  const copiedTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (pressTimer.current) clearTimeout(pressTimer.current);
      if (copiedTimer.current) clearTimeout(copiedTimer.current);
    };
  }, []);

  const triggerCopy = () => {
    if (!copyText) return;
    void navigator.clipboard?.writeText(copyText);
    setCopied(true);
    if (copiedTimer.current) clearTimeout(copiedTimer.current);
    copiedTimer.current = setTimeout(() => setCopied(false), 1400);
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
      {!mine && showAvatar && (
        <span className="mt-2.5 mr-3 w-7 shrink-0" data-active-avatar="true">
          <NoriMark size={28} state={markState} />
        </span>
      )}
      {standaloneCard ? (
        <div className="min-w-0 flex-1">
          <Parts parts={unit.item.parts!} />
        </div>
      ) : (
        <div className="relative min-w-0">
          <div
            className={cn(
              "min-w-0 px-4 py-2.5",
              mine
                ? "bg-accent text-on-accent"
                : "bg-bubble text-ink",
            )}
            style={{ borderRadius: corners(sender, pos) }}
          >
            {unit.markdown && <Markdown onAccent={mine}>{unit.markdown}</Markdown>}
            {unit.item.parts && <Parts parts={unit.item.parts} />}
          </div>
          {copyText && (
            <button
              type="button"
              onClick={triggerCopy}
              aria-label={copied ? "Copied message" : "Copy message"}
              className={cn(
                "t-fast absolute -top-2 inline-flex h-6 items-center gap-1 rounded-full border border-hairline bg-surface px-2 text-[11px] font-semibold text-ink-2 shadow-pill transition-opacity hover:text-ink focus-visible:opacity-100",
                mine ? "-left-2" : "-right-2",
                copied ? "opacity-100 text-accent-text" : "opacity-0 group-hover/msg:opacity-100",
              )}
            >
              {copied ? <Check size={11} strokeWidth={2.2} aria-hidden /> : <Copy size={11} strokeWidth={1.8} aria-hidden />}
              <span>{copied ? "Copied" : "Copy"}</span>
            </button>
          )}
        </div>
      )}
    </div>
  );
}
