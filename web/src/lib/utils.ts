export { clsx as cn } from "clsx";

let counter = 0;
export function uid(prefix = "id") {
  counter += 1;
  return `${prefix}_${Date.now().toString(36)}${counter.toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}

export const isMac = () =>
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

export function firstSentence(text: string) {
  const plain = text
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/[#*_`>|[\]]/g, "")
    .replace(/\(https?:[^)]*\)/g, "")
    .trim();
  const m = plain.match(/^[\s\S]*?[.!?](\s|$)/);
  return (m ? m[0] : plain).trim().slice(0, 180);
}

/** A message as one plain line: markdown and hints stripped. */
export function plainLine(text: string, max = 160) {
  return text
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/[#*_`>|]/g, "")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, max);
}

/** Deterministic pseudo-random waveform for seeded voice notes. */
export function waveform(seed: number, bars = 36) {
  let x = seed;
  const out: number[] = [];
  for (let i = 0; i < bars; i++) {
    x = (x * 9301 + 49297) % 233280;
    const r = x / 233280;
    const envelope = Math.sin((Math.PI * (i + 1)) / (bars + 1));
    out.push(Math.max(0.12, Math.min(1, 0.25 + r * 0.55 * envelope + envelope * 0.25)));
  }
  return out;
}
