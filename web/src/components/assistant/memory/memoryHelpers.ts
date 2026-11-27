import { relative, when } from "@/lib/assistant/format";
import type {
  MemoryFact,
  MemoryFactEditable,
  MemoryLogEntry,
  MemoryProvenance,
  MemorySearchHit,
  WorkingMemory,
  WorkingMemoryItem,
} from "@/lib/contracts/assistant";
import type { Tone } from "../ui";

export type MemoryTab = "core" | "logs" | "working";

export const MAX_MEMORY_TEXT_BYTES = 2000;
export const DEFAULT_CORE_BUDGET_BYTES = 24 * 1024;
export const SECTION_SUGGESTIONS = ["Preferences", "People", "Projects", "Decisions", "Context"] as const;

const encoder = new TextEncoder();

export function utf8Bytes(text: string): number {
  return encoder.encode(text).length;
}

export function formatKB(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0";
  const kb = bytes / 1024;
  if (kb < 0.1) return "<0.1";
  if (Number.isInteger(kb)) return String(kb);
  if (kb < 10) return kb.toFixed(1).replace(/\.0$/, "");
  return String(Math.round(kb));
}

export function formatByteBudget(usedBytes: number, budgetBytes = DEFAULT_CORE_BUDGET_BYTES): string {
  const limit = budgetBytes > 0 ? budgetBytes : DEFAULT_CORE_BUDGET_BYTES;
  return `${formatKB(usedBytes)} / ${formatKB(limit)} KB`;
}

export function formatLogDate(date: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date.trim());
  if (!match) return date;
  const parsed = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])));
  if (!Number.isFinite(parsed.getTime())) return date;
  const sameYear = parsed.getUTCFullYear() === new Date().getUTCFullYear();
  return parsed.toLocaleDateString("en-US", {
    weekday: "short",
    month: "short",
    day: "numeric",
    ...(sameYear ? {} : { year: "numeric" }),
    timeZone: "UTC",
  });
}

export function formatWatermark(iso: string | undefined, emptyLabel: string, now?: number, timeZone?: string): string {
  if (!iso) return emptyLabel;
  return `${when(iso, timeZone)} (${relative(iso, now)})`;
}

export function workingMemoryWatermarks(
  working: Pick<WorkingMemory, "extracted_through" | "flushed_through" | "through" | "last_dream_at">,
  now?: number,
  timeZone?: string,
) {
  return {
    extractedThrough: formatWatermark(working.extracted_through, "Not extracted yet", now, timeZone),
    flushedThrough: formatWatermark(working.flushed_through, "Not flushed yet", now, timeZone),
    windowThrough: formatWatermark(working.through, "—", now, timeZone),
    lastDream: formatWatermark(working.last_dream_at, "Not yet", now, timeZone),
  };
}

export function coreBudgetStats(
  facts: Pick<MemoryFact, "text">[],
  working?: Pick<WorkingMemory, "core_bytes" | "core_budget_bytes">,
): { used: number; budget: number; ratio: number; tone: Tone } {
  const used =
    working && typeof working.core_bytes === "number"
      ? Math.max(0, working.core_bytes)
      : facts.reduce((sum, fact) => sum + utf8Bytes(fact.text), 0);
  const budget = working && working.core_budget_bytes > 0 ? working.core_budget_bytes : DEFAULT_CORE_BUDGET_BYTES;
  const ratio = Math.min(1, budget > 0 ? used / budget : 0);
  const tone: Tone = ratio >= 0.98 ? "danger" : ratio >= 0.85 ? "warning" : "neutral";
  return { used, budget, ratio, tone };
}

export function parseMemoryTab(raw?: string | null, date?: string | null, entry?: string | null): MemoryTab {
  if (raw === "core" || raw === "logs" || raw === "working") return raw;
  if (date || entry) return "logs";
  return "core";
}

export function buildMemoryHref(opts: {
  tab?: MemoryTab;
  id?: string;
  date?: string;
  entry?: string;
  q?: string;
}): string {
  const params = new URLSearchParams();
  if (opts.tab && opts.tab !== "core") params.set("tab", opts.tab);
  else if (opts.tab === "core" && opts.id) params.set("tab", "core");
  if (opts.id) params.set("id", opts.id);
  if (opts.date) params.set("date", opts.date);
  if (opts.entry) params.set("entry", opts.entry);
  if (opts.q?.trim()) params.set("q", opts.q.trim());
  const query = params.toString();
  return query ? `/memory?${query}` : "/memory";
}

export function memoryHitHref(hit: Pick<MemorySearchHit, "layer" | "memory_id" | "log_date" | "entry_id">): string {
  if (hit.layer === "log") {
    return buildMemoryHref({ tab: "logs", date: hit.log_date, entry: hit.entry_id });
  }
  return buildMemoryHref({ tab: "core", id: hit.memory_id });
}

export function messageJumpHref(id?: string, kind: "message" | "input" = "message"): string | undefined {
  const trimmed = id?.trim();
  if (!trimmed || trimmed === "ui") return undefined;
  const anchor = kind === "input" && !trimmed.endsWith("-user") ? `${trimmed}-user` : trimmed;
  return `/#${encodeURIComponent(anchor)}`;
}

const PROVENANCE_META: Record<MemoryProvenance, { label: string; tone: Tone }> = {
  user_explicit: { label: "Explicit", tone: "accent" },
  pre_compact_flush: { label: "Auto-saved", tone: "neutral" },
  summary_evicted: { label: "Archived", tone: "neutral" },
  task_outcome: { label: "Task outcome", tone: "warning" },
  dream_curated: { label: "Curated", tone: "ok" },
};

export function provenanceInfo(provenance?: MemoryProvenance): { label: string; tone: Tone } {
  return PROVENANCE_META[provenance ?? "user_explicit"] ?? PROVENANCE_META.user_explicit;
}

export const WORKING_KIND_META: Record<WorkingMemoryItem["kind"], { label: string; tone: Tone }> = {
  constraint: { label: "Constraint", tone: "warning" },
  correction: { label: "Correction", tone: "danger" },
  preference: { label: "Preference", tone: "accent" },
  approval: { label: "Approval", tone: "ok" },
  open_question: { label: "Open question", tone: "neutral" },
  commitment: { label: "Commitment", tone: "accent" },
  decision: { label: "Decision", tone: "ok" },
  fact: { label: "Fact", tone: "neutral" },
};

export function requiresReauthorForPromote(provenance?: MemoryProvenance): boolean {
  return provenance === "task_outcome";
}

export function validateMemoryText(
  text: string,
): { ok: true; text: string; bytes: number } | { ok: false; reason: string; bytes: number } {
  const trimmed = text.trim();
  const bytes = utf8Bytes(trimmed);
  if (!trimmed) return { ok: false, reason: "Enter something to remember.", bytes };
  if (bytes > MAX_MEMORY_TEXT_BYTES) {
    return { ok: false, reason: `Keep memory within ${MAX_MEMORY_TEXT_BYTES.toLocaleString()} characters.`, bytes };
  }
  return { ok: true, text: trimmed, bytes };
}

export function validatePromoteDraft(
  entry: Pick<MemoryLogEntry, "provenance" | "text">,
  draftText: string,
  reviewed: boolean,
): { ok: true; text: string } | { ok: false; reason: string } {
  const checked = validateMemoryText(draftText);
  if (!checked.ok) return { ok: false, reason: checked.reason };
  if (requiresReauthorForPromote(entry.provenance) && !reviewed) {
    return { ok: false, reason: "Confirm that you have reviewed this task outcome before promoting it to Core Memory." };
  }
  return { ok: true, text: checked.text };
}

export function filterCoreMemories(facts: MemoryFact[], query: string): MemoryFact[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return facts;
  return facts.filter((fact) => {
    const haystack = [
      fact.text,
      fact.about,
      fact.section,
      fact.provenance,
      provenanceInfo(fact.provenance).label,
      fact.source_text,
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    return haystack.includes(needle);
  });
}

export function buildEditableValue(input: { text: string; about: string; section: string; pinned: boolean }): MemoryFactEditable {
  const value: MemoryFactEditable = { text: input.text.trim() };
  if (input.about.trim()) value.about = input.about.trim();
  if (input.section.trim()) value.section = input.section.trim();
  if (input.pinned) value.pinned = true;
  return value;
}
