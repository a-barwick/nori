import { classify, extractHints } from "./hybrid";
import { separatorLabel, minutes } from "./time";
import type { ApprovalItem, AttachmentItem, ChipSet, ID, ImageItem, Sender, TextItem, ThreadItem, VoiceItem } from "./types";

/**
 * Turns thread items into what the reader sees: time separators, sender
 * groups with pinched corners (§5.1), and page blocks / approval cards that
 * stand on their own.
 */

export type Pos = "single" | "first" | "middle" | "last";

export type GroupUnit =
  | { type: "bubble"; key: string; item: TextItem | VoiceItem; markdown: string; chips?: ChipSet; transcript?: boolean }
  | { type: "voice"; key: string; item: VoiceItem }
  | { type: "image"; key: string; item: ImageItem }
  | { type: "artifact"; key: string; item: TextItem | AttachmentItem; artifactId: ID };

export type Unit =
  | { type: "sep"; key: string; label: string }
  | { type: "group"; key: string; sender: Sender; units: GroupUnit[] }
  | { type: "page"; key: string; item: TextItem; body: string; reasons: string[] }
  | { type: "approval"; key: string; item: ApprovalItem };

const GROUP_WINDOW = minutes(3);
const SEPARATOR_GAP = minutes(15);

export function buildUnits(items: ThreadItem[], now = Date.now()): Unit[] {
  const out: Unit[] = [];
  let group: Extract<Unit, { type: "group" }> | null = null;
  let prevAt = -Infinity;
  let lastGroupAt = -Infinity;

  const close = () => {
    group = null;
  };

  const openGroup = (sender: Sender, item: ThreadItem, suffix = "") => {
    const g: Extract<Unit, { type: "group" }> = { type: "group", key: `g-${item.id}${suffix}`, sender, units: [] };
    out.push(g);
    group = g;
    return g;
  };

  const groupFor = (item: ThreadItem) => {
    const g = group as Extract<Unit, { type: "group" }> | null;
    if (g && g.sender === item.sender && item.at - lastGroupAt <= GROUP_WINDOW) return g;
    return openGroup(item.sender, item);
  };

  for (const item of items) {
    if (item.at - prevAt > SEPARATOR_GAP) {
      out.push({ type: "sep", key: `s-${item.id}`, label: separatorLabel(item.at, now) });
      close();
    }
    prevAt = item.at;

    switch (item.kind) {
      case "text": {
        if (item.sender === "me") {
          groupFor(item).units.push({ type: "bubble", key: item.id, item, markdown: item.text });
          break;
        }
        const { body, hints } = extractHints(item.text);
        const c = classify(body, hints.mode);
        if (c.mode === "page") {
          groupFor(item).units.push({ type: "bubble", key: `${item.id}-cover`, item, markdown: c.cover! });
          close();
          out.push({ type: "page", key: `${item.id}-page`, item, body: c.body, reasons: c.reasons });
          lastGroupAt = -Infinity;
          // Choices answer what the page ends on, so they come after it.
          if (item.chips) {
            openGroup("nori", item, "-chips").units.push({ type: "bubble", key: `${item.id}-chips`, item, markdown: "", chips: item.chips });
            close();
          }
          continue;
        }
        const g = groupFor(item);
        if (item.artifactId) {
          g.units.push({ type: "bubble", key: item.id, item, markdown: c.body });
          g.units.push({ type: "artifact", key: `${item.id}-art`, item, artifactId: item.artifactId });
          if (item.chips) g.units.push({ type: "bubble", key: `${item.id}-chips`, item, markdown: "", chips: item.chips });
          break;
        }
        g.units.push({ type: "bubble", key: item.id, item, markdown: c.body, chips: item.chips });
        break;
      }
      case "voice": {
        const g = groupFor(item);
        g.units.push({ type: "voice", key: item.id, item });
        if (item.transcript) {
          g.units.push({ type: "bubble", key: `${item.id}-tr`, item, markdown: item.transcript, transcript: true });
        }
        break;
      }
      case "image":
        groupFor(item).units.push({ type: "image", key: item.id, item });
        break;
      case "attachment":
        groupFor(item).units.push({ type: "artifact", key: item.id, item, artifactId: item.artifactId });
        break;
      case "approval":
        close();
        out.push({ type: "approval", key: item.id, item });
        lastGroupAt = -Infinity;
        continue;
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

/** Corner radii TL TR BR BL; the sender's side pinches where items touch. */
export function corners(sender: Sender, pos: Pos, r = 22, pinch = 6) {
  if (pos === "single") return `${r}px`;
  const R = `${r}px`;
  const P = `${pinch}px`;
  if (sender === "nori") {
    if (pos === "first") return `${R} ${R} ${R} ${P}`;
    if (pos === "middle") return `${P} ${R} ${R} ${P}`;
    return `${P} ${R} ${R} ${R}`;
  }
  if (pos === "first") return `${R} ${R} ${P} ${R}`;
  if (pos === "middle") return `${R} ${P} ${P} ${R}`;
  return `${R} ${P} ${R} ${R}`;
}
