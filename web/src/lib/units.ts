import { classify } from "./hybrid";
import { separatorLabel, minutes } from "./time";
import type { Sender, TextItem } from "./types";

export type Pos = "single" | "first" | "middle" | "last";
export type GroupUnit = { key: string; item: TextItem; markdown: string };
export type Unit =
  | { type: "sep"; key: string; label: string }
  | { type: "group"; key: string; sender: Sender; units: GroupUnit[] }
  | { type: "page"; key: string; item: TextItem; body: string }
  | { type: "error"; key: string; item: TextItem };

const GROUP_WINDOW = minutes(3);
const SEPARATOR_GAP = minutes(15);

export function buildUnits(items: TextItem[]): Unit[] {
  const now = Date.now();
  const out: Unit[] = [];
  let group: Extract<Unit, { type: "group" }> | undefined;
  let prevAt = -Infinity;
  let lastGroupAt = -Infinity;
  for (const item of items) {
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
    const classified = item.sender === "me" ? { mode: "bubble" as const, body: item.text } : classify(item.text, item.display);
    if (!group || group.sender !== item.sender || item.at - lastGroupAt > GROUP_WINDOW) {
      group = { type: "group", key: `g-${item.id}`, sender: item.sender, units: [] };
      out.push(group);
    }
    group.units.push({ key: item.id, item: classified.mode === "page" ? { ...item, debug: undefined } : item, markdown: classified.mode === "page" ? classified.cover : classified.body });
    if (classified.mode === "page") {
      out.push({ type: "page", key: `${item.id}-page`, item, body: classified.body });
      group = undefined;
    }
    lastGroupAt = item.at;
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
