"use client";

import { corners, type GroupUnit, type Pos } from "@/lib/units";
import type { Sender } from "@/lib/types";
import { cn } from "@/lib/utils";
import { NoriMark } from "@/components/mark/NoriMark";
import { Markdown } from "./Markdown";
import { Parts } from "./Parts";

export function BubbleUnit({ unit, pos, sender }: { unit: GroupUnit; pos: Pos; sender: Sender }) {
  const mine = sender === "me";
  const standaloneCard = !mine && !unit.markdown && !!unit.item.parts?.length && unit.item.parts.every((part) => part.type === "reminder" || part.type === "card");
  return (
    <div
      className={cn(
        "nori-message relative flex items-start",
        mine ? "max-w-[min(560px,75%)]" : "max-w-[min(680px,100%)]",
      )}
      data-item-id={unit.item.id}
    >
      {!mine && (
        <span className="mt-2.5 mr-3 shrink-0">
          <NoriMark size={28} state="idle" />
        </span>
      )}
      {standaloneCard ? (
        <div className="min-w-0 flex-1">
          <Parts parts={unit.item.parts!} />
        </div>
      ) : (
        <div
          className={cn(
            "min-w-0 px-4 py-2.5",
            mine
              ? "bg-accent text-on-accent shadow-[0_2px_10px_rgba(80,0,255,0.14)]"
              : "bg-bubble text-ink",
          )}
          style={{ borderRadius: corners(sender, pos) }}
        >
          {unit.markdown && <Markdown onAccent={mine}>{unit.markdown}</Markdown>}
          {unit.item.parts && <Parts parts={unit.item.parts} />}
        </div>
      )}
    </div>
  );
}
