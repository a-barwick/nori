"use client";

import { corners, type GroupUnit, type Pos } from "@/lib/units";
import type { Sender } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Medallion } from "@/components/mark/NoriMark";
import { Markdown } from "./Markdown";

export function BubbleUnit({ unit, pos, sender }: { unit: GroupUnit; pos: Pos; sender: Sender }) {
  const mine = sender === "me";
  return (
    <div className={cn("nova-message relative flex", mine ? "max-w-[min(560px,85%)]" : "max-w-[min(680px,100%)]")} data-item-id={unit.item.id}>
      {!mine && <Medallion size={32} className="mt-2 mr-3" />}
      <div className={cn("min-w-0 px-[15px] py-[11px]", mine ? "bg-accent text-on-accent" : "bg-bubble text-ink")} style={{ borderRadius: corners(sender, pos) }}>
        <Markdown onAccent={mine}>{unit.markdown}</Markdown>
      </div>
    </div>
  );
}
