import type { InlineInteractionEntry } from "./assistant/interactions";
import { separatorLabel, minutes } from "./time";
import type { Part, Sender, TextItem } from "./types";

export type Pos = "single" | "first" | "middle" | "last";
export type GroupUnit = { key: string; item: TextItem; markdown: string };
export type Unit =
  | { type: "sep"; key: string; label: string }
  | { type: "group"; key: string; sender: Sender; units: GroupUnit[] }
  | { type: "page"; key: string; item: TextItem; title?: string; body: string }
  | { type: "error"; key: string; item: TextItem }
  | { type: "interaction"; key: string; entry: InlineInteractionEntry };

const GROUP_WINDOW = minutes(3);
const SEPARATOR_GAP = minutes(15);

function interactionTime(entry: InlineInteractionEntry): number | undefined {
  const raw = entry.interaction.created_at ?? entry.interaction.resolved_at;
  if (!raw) return undefined;
  const parsed = Date.parse(raw);
  return Number.isNaN(parsed) ? undefined : parsed;
}

function isPagePart(part: Part): boolean {
  return part.type === "page" || (part.type === "text" && part.display === "page");
}

export function buildUnits(items: TextItem[], interactions: InlineInteractionEntry[] = []): Unit[] {
  const now = Date.now();
  const out: Unit[] = [];

  const datedInteractions: { at: number; index: number; entry: InlineInteractionEntry }[] = [];
  const undatedInteractions: InlineInteractionEntry[] = [];
  interactions.forEach((entry, index) => {
    const at = interactionTime(entry);
    if (at !== undefined) datedInteractions.push({ at, index, entry });
    else undatedInteractions.push(entry);
  });
  datedInteractions.sort((a, b) => a.at - b.at || a.index - b.index);
  let interactionCursor = 0;

  let group: Extract<Unit, { type: "group" }> | undefined;
  let prevAt = -Infinity;
  let lastGroupAt = -Infinity;

  const flushInteractionsBefore = (at: number) => {
    while (interactionCursor < datedInteractions.length && datedInteractions[interactionCursor].at < at) {
      const entry = datedInteractions[interactionCursor].entry;
      out.push({ type: "interaction", key: `int-${entry.interaction.id}`, entry });
      group = undefined;
      interactionCursor += 1;
    }
  };

  const pushBubble = (sender: Sender, at: number, key: string, item: TextItem, markdown: string) => {
    if (!group || group.sender !== sender || at - lastGroupAt > GROUP_WINDOW) {
      group = { type: "group", key: `g-${key}`, sender, units: [] };
      out.push(group);
    }
    group.units.push({ key, item, markdown });
  };

  for (const item of items) {
    flushInteractionsBefore(item.at);
    if (item.at - prevAt > SEPARATOR_GAP) {
      out.push({ type: "sep", key: `s-${item.id}`, label: separatorLabel(item.at, now) });
      group = undefined;
    }
    prevAt = item.at;
    if (item.sender === "nori" && item.status === "failed") {
      out.push({ type: "error", key: `e-${item.id}`, item });
      group = undefined;
      lastGroupAt = item.at;
      continue;
    }

    // Engine-segmented assistant messages with page parts render parts in exact server order.
    const orderedParts = item.sender === "nori" ? item.parts : undefined;
    if (orderedParts?.some(isPagePart)) {
      const trailingNonText: Part[] = [];
      let lastDebugTarget: { kind: "page"; unit: Extract<Unit, { type: "page" }> } | { kind: "group"; unit: GroupUnit } | undefined;

      orderedParts.forEach((part, index) => {
        if (isPagePart(part)) {
          const pageUnit: Extract<Unit, { type: "page" }> = {
            type: "page",
            key: `${item.id}-page-${index}`,
            item: { ...item, parts: undefined, debug: undefined },
            title: part.title,
            body: part.text ?? "",
          };
          out.push(pageUnit);
          group = undefined;
          lastDebugTarget = { kind: "page", unit: pageUnit };
        } else if (part.type === "text") {
          const text = (part.text ?? "").trim();
          if (!text) return;
          const unitItem: TextItem = { ...item, text, parts: undefined, debug: undefined };
          const key = index === 0 ? item.id : `${item.id}-text-${index}`;
          pushBubble(item.sender, item.at, key, unitItem, text);
          lastDebugTarget = { kind: "group", unit: group!.units[group!.units.length - 1] };
        } else {
          trailingNonText.push(part);
        }
      });

      if (trailingNonText.length > 0) {
        const partsItem: TextItem = { ...item, text: "", parts: trailingNonText, debug: undefined };
        pushBubble(item.sender, item.at, `${item.id}-parts`, partsItem, "");
        lastDebugTarget = { kind: "group", unit: group!.units[group!.units.length - 1] };
      }

      if (item.debug && lastDebugTarget) {
        lastDebugTarget.unit.item = { ...lastDebugTarget.unit.item, debug: item.debug };
      }
      lastGroupAt = item.at;
      continue;
    }

    if (item.sender === "nori" && item.display === "page") {
      out.push({ type: "page", key: `${item.id}-page`, item, body: item.text });
      group = undefined;
      lastGroupAt = item.at;
      continue;
    }

    pushBubble(item.sender, item.at, item.id, item, item.text);
    lastGroupAt = item.at;
  }

  while (interactionCursor < datedInteractions.length) {
    const entry = datedInteractions[interactionCursor].entry;
    out.push({ type: "interaction", key: `int-${entry.interaction.id}`, entry });
    interactionCursor += 1;
  }
  for (const entry of undatedInteractions) {
    out.push({ type: "interaction", key: `int-${entry.interaction.id}`, entry });
  }

  return out;
}

export function posOf(index: number, length: number): Pos {
  if (length === 1) return "single";
  if (index === 0) return "first";
  if (index === length - 1) return "last";
  return "middle";
}

export function corners(sender: Sender, pos: Pos) {
  const R = "22px";
  const P = "6px";
  if (pos === "single") return R;
  if (sender === "nori") {
    if (pos === "first") return `${R} ${R} ${R} ${P}`;
    if (pos === "middle") return `${P} ${R} ${R} ${P}`;
    return `${P} ${R} ${R} ${R}`;
  }
  if (pos === "first") return `${R} ${R} ${P} ${R}`;
  if (pos === "middle") return `${R} ${P} ${P} ${R}`;
  return `${R} ${P} ${R} ${R}`;
}
