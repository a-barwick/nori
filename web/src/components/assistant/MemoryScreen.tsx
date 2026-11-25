"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { ArrowDownToLine, ArrowUpRight, Pencil, Pin, PinOff, Plus, Search, Sparkles, X } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { plural, relative, when } from "@/lib/assistant/format";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type {
  MemoryAction,
  MemoryFact,
  MemoryFactEditable,
  MemoryLog,
  MemoryLogEntry,
  MemoryLogEntryAction,
  MemoryProvenance,
  MemorySearchHit,
  MemorySearchResponse,
  WorkingMemory,
  WorkingMemoryItem,
  WorkingMemoryItemAction,
} from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import {
  Confirm,
  Empty,
  Failure,
  Field,
  inputClass,
  Loading,
  Notice,
  NotYet,
  Pill,
  RefreshButton,
  Row,
  Rows,
  Screen,
  Section,
  UnderlineTabs,
  type Tone,
} from "./ui";

type List<T> = { items: T[] };
export type MemoryTab = "core" | "logs" | "working";
export type SearchLayerFilter = "all" | "log" | "core";

export const MAX_MEMORY_TEXT_BYTES = 2000;
export const DEFAULT_CORE_BUDGET_BYTES = 24 * 1024;
const SECTION_SUGGESTIONS = ["Preferences", "People", "Projects", "Decisions", "Context"];

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
  const used = working && typeof working.core_bytes === "number"
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

export const PROVENANCE_META: Record<MemoryProvenance, { label: string; tone: Tone }> = {
  user_explicit: { label: "Explicit", tone: "accent" },
  pre_compact_flush: { label: "Auto-saved", tone: "neutral" },
  summary_evicted: { label: "Archived", tone: "neutral" },
  task_outcome: { label: "Task outcome", tone: "warning" },
  dream_curated: { label: "Curated", tone: "ok" },
};

export function provenanceInfo(provenance?: MemoryProvenance): { label: string; tone: Tone } {
  return PROVENANCE_META[provenance ?? "user_explicit"] ?? PROVENANCE_META.user_explicit;
}

const WORKING_KIND_META: Record<WorkingMemoryItem["kind"], { label: string; tone: Tone }> = {
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

export function validateMemoryText(text: string): { ok: true; text: string; bytes: number } | { ok: false; reason: string; bytes: number } {
  const trimmed = text.trim();
  const bytes = utf8Bytes(trimmed);
  if (!trimmed) return { ok: false, reason: "Enter something to remember.", bytes };
  if (bytes > MAX_MEMORY_TEXT_BYTES) {
    return { ok: false, reason: `Keep memory within ${MAX_MEMORY_TEXT_BYTES.toLocaleString()} UTF-8 bytes.`, bytes };
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

function buildEditableValue(input: { text: string; about: string; section: string; pinned: boolean }): MemoryFactEditable {
  const value: MemoryFactEditable = { text: input.text.trim() };
  if (input.about.trim()) value.about = input.about.trim();
  if (input.section.trim()) value.section = input.section.trim();
  if (input.pinned) value.pinned = true;
  return value;
}

export function MemoryScreen() {
  const { has } = useAssistant();
  const params = useSearchParams();
  const router = useRouter();
  const enabled = has("memory");

  const tab = parseMemoryTab(params.get("tab"), params.get("date"), params.get("entry"));
  const selectedId = params.get("id") ?? undefined;
  const selectedDate = params.get("date") ?? undefined;
  const selectedEntry = params.get("entry") ?? undefined;
  const initialQuery = params.get("q") ?? "";

  const [adding, setAdding] = useState(false);

  const core = useResource<List<MemoryFact>>(enabled ? "/memory" : null, { poll: 30_000 });
  const working = useResource<WorkingMemory>(enabled ? "/memory/working" : null, { poll: 30_000 });
  const logs = useResource<List<MemoryLog>>(enabled ? "/memory/logs?limit=30" : null, { poll: 30_000 });

  const hasSelectedDateInList = useMemo(
    () => !selectedDate || (logs.data?.items ?? []).some((log) => log.date === selectedDate),
    [logs.data?.items, selectedDate],
  );
  const deepDateLog = useResource<MemoryLog>(
    enabled && tab === "logs" && selectedDate && !hasSelectedDateInList
      ? `/memory/logs/${encodeURIComponent(selectedDate)}`
      : null,
    { poll: 0 },
  );

  const coreItems = useMemo(() => core.data?.items ?? [], [core.data?.items]);
  const mergedLogs = useMemo(() => {
    const items = logs.data?.items ?? [];
    if (!deepDateLog.data || items.some((item) => item.date === deepDateLog.data!.date)) return items;
    return [deepDateLog.data, ...items].sort((a, b) => b.date.localeCompare(a.date));
  }, [logs.data?.items, deepDateLog.data]);
  const logEntryCount = useMemo(() => mergedLogs.reduce((sum, log) => sum + log.entries.length, 0), [mergedLogs]);
  const workingItems = useMemo(() => working.data?.items ?? [], [working.data?.items]);
  const budget = useMemo(() => coreBudgetStats(coreItems, working.data), [coreItems, working.data]);

  const selectTab = (next: MemoryTab) => {
    router.replace(buildMemoryHref({ tab: next }), { scroll: false });
  };

  if (!enabled) {
    return (
      <Screen title="Memory" back="/more" backLabel="More">
        <NotYet what="Memory" />
      </Screen>
    );
  }

  const refreshAll = () => {
    invalidate("/memory");
  };

  const openAdd = () => {
    setAdding(true);
    if (tab !== "core") selectTab("core");
  };

  const summaryLine = coreItems.length
    ? `${plural(coreItems.length, "core memory", "core memories")} · ${formatByteBudget(budget.used, budget.budget)} used.`
    : "What Nori remembers across chats, daily logs, and your active session.";

  return (
    <Screen
      title="Memory"
      back="/more"
      backLabel="More"
      line={summaryLine}
      actions={
        <>
          <Button variant="primary" size="sm" onClick={openAdd}>
            <Plus size={15} aria-hidden />
            Add memory
          </Button>
          <RefreshButton onClick={refreshAll} busy={core.refreshing || logs.refreshing || working.refreshing} />
        </>
      }
      tabs={
        <UnderlineTabs
          label="Memory layers"
          value={tab}
          onChange={selectTab}
          items={[
            { id: "core", label: "Core", count: coreItems.length || undefined },
            { id: "logs", label: "Daily Logs", count: logEntryCount || undefined },
            { id: "working", label: "Working", count: workingItems.length || undefined },
          ]}
        />
      }
    >

      {tab === "core" && (
        <CoreMemoryTab
          key={`core:${initialQuery}`}
          core={core}
          items={coreItems}
          budget={budget}
          selectedId={selectedId}
          initialQuery={initialQuery}
          adding={adding}
          setAdding={setAdding}
        />
      )}

      {tab === "logs" && (
        <DailyLogsTab
          key={`logs:${initialQuery}`}
          logs={logs}
          deepDateLog={deepDateLog}
          items={mergedLogs}
          selectedDate={selectedDate}
          selectedEntry={selectedEntry}
          initialQuery={initialQuery}
          onJump={(href) => router.replace(href, { scroll: false })}
        />
      )}

      {tab === "working" && <WorkingMemoryTab working={working} />}
    </Screen>
  );
}

function ByteCounter({ text }: { text: string }) {
  const bytes = utf8Bytes(text.trim());
  const over = bytes > MAX_MEMORY_TEXT_BYTES;
  return (
    <span className={cn("font-mono text-caption", over ? "text-danger" : "text-ink-3")}>
      {bytes.toLocaleString()} / {MAX_MEMORY_TEXT_BYTES.toLocaleString()} B
    </span>
  );
}

function CoreMemoryTab({
  core,
  items,
  budget,
  selectedId,
  initialQuery,
  adding,
  setAdding,
}: {
  core: ReturnType<typeof useResource<List<MemoryFact>>>;
  items: MemoryFact[];
  budget: ReturnType<typeof coreBudgetStats>;
  selectedId?: string;
  initialQuery: string;
  adding: boolean;
  setAdding: (open: boolean) => void;
}) {
  const action = useAction();
  const [query, setQuery] = useState(initialQuery);
  const [editingId, setEditingId] = useState<string>();
  const [editText, setEditText] = useState("");
  const [editAbout, setEditAbout] = useState("");
  const [editSection, setEditSection] = useState("");
  const [editPinned, setEditPinned] = useState(false);

  const [newText, setNewText] = useState("");
  const [newAbout, setNewAbout] = useState("");
  const [newSection, setNewSection] = useState("");
  const [newPinned, setNewPinned] = useState(false);

  const filtered = useMemo(() => filterCoreMemories(items, query), [items, query]);
  const pinnedCount = useMemo(() => items.filter((item) => item.pinned).length, [items]);

  useEffect(() => {
    if (!selectedId) return;
    const el = document.getElementById(`memory-${selectedId}`);
    el?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [selectedId, items.length]);

  const runCoreMutation = async <T,>(
    name: string,
    fn: () => Promise<T>,
    done: string,
  ) =>
    action.run(
      name,
      async () => {
        try {
          const result = await fn();
          invalidate("/memory");
          return result;
        } catch (error) {
          if (error instanceof APIError && error.stale) invalidate("/memory");
          throw error;
        }
      },
      done,
    );

  const createValidation = validateMemoryText(newText);
  const createFact = async () => {
    if (!createValidation.ok) return;
    const created = await runCoreMutation(
      "create",
      () =>
        mutate<MemoryFact>("/memory", {
          request_id: newRequestID(),
          expected_revision: 0,
          value: buildEditableValue({ text: createValidation.text, about: newAbout, section: newSection, pinned: newPinned }),
        }),
      "Memory saved.",
    );
    if (created) {
      setNewText("");
      setNewAbout("");
      setNewSection("");
      setNewPinned(false);
      setAdding(false);
    }
  };

  const startEdit = (fact: MemoryFact) => {
    setEditingId(fact.id);
    setEditText(fact.text);
    setEditAbout(fact.about ?? "");
    setEditSection(fact.section ?? "");
    setEditPinned(!!fact.pinned);
  };

  const editValidation = validateMemoryText(editText);
  const saveEdit = async (fact: MemoryFact) => {
    if (!editValidation.ok) return;
    const saved = await runCoreMutation(
      `edit:${fact.id}`,
      () =>
        mutate<MemoryFact>(
          `/memory/${encodeURIComponent(fact.id)}`,
          {
            request_id: newRequestID(),
            expected_revision: fact.revision,
            value: {
              text: editValidation.text,
              ...(editAbout.trim() ? { about: editAbout.trim() } : {}),
              ...(editSection.trim() ? { section: editSection.trim() } : {}),
              pinned: editPinned,
            },
          },
          "PUT",
        ),
      "Memory updated.",
    );
    if (saved) setEditingId(undefined);
  };

  const factAction = (fact: MemoryFact, kind: Extract<MemoryAction["value"]["action"], "pin" | "unpin" | "demote" | "forget">) => {
    const label: Record<typeof kind, string> = {
      pin: "Pinned to Core Memory.",
      unpin: "Unpinned.",
      demote: "Moved to Daily Logs.",
      forget: "Memory forgotten.",
    };
    return runCoreMutation(
      `${kind}:${fact.id}`,
      () =>
        mutate(`/memory/${encodeURIComponent(fact.id)}/actions`, {
          request_id: newRequestID(),
          expected_revision: fact.revision,
          value: { action: kind },
        }),
      label[kind],
    );
  };

  const barColor = budget.tone === "danger" ? "bg-danger" : budget.tone === "warning" ? "bg-warning" : "bg-accent";

  return (
    <div className="space-y-4">
      <div className="rounded-card border border-hairline bg-surface p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="text-meta font-medium text-ink">Core budget</span>
            <Pill tone={budget.tone}>{formatByteBudget(budget.used, budget.budget)}</Pill>
          </div>
          <span className="text-caption text-ink-3">
            {plural(items.length, "fact")}
            {pinnedCount ? ` · ${pinnedCount} pinned` : ""}
          </span>
        </div>
        <div
          className="mt-2.5 h-1.5 w-full overflow-hidden rounded-full bg-fill-1"
          role="progressbar"
          aria-label="Core Memory budget usage"
          aria-valuenow={budget.used}
          aria-valuemin={0}
          aria-valuemax={budget.budget}
        >
          <div className={cn("h-full rounded-full transition-[width]", barColor)} style={{ width: `${Math.round(budget.ratio * 100)}%` }} />
        </div>
        <p className="mt-2 text-caption text-ink-3">
          Always loaded into chat context. Pinned items stay in Core; unpinned overflow moves to Daily Logs automatically.
        </p>
      </div>

      <datalist id="memory-sections">
        {SECTION_SUGGESTIONS.map((option) => (
          <option key={option} value={option} />
        ))}
      </datalist>

      {adding && (
        <form
          className="space-y-3 rounded-card border border-hairline-strong bg-surface p-4"
          onSubmit={(event) => {
            event.preventDefault();
            void createFact();
          }}
        >
          <div className="flex items-center justify-between gap-2">
            <h2 className="text-section font-semibold text-ink">Add memory</h2>
            <ByteCounter text={newText} />
          </div>
          <Field label="Fact">
            <textarea
              aria-label="New memory text"
              rows={3}
              value={newText}
              onChange={(event) => setNewText(event.target.value)}
              placeholder="Prefers aisle seats and direct morning flights…"
              className={cn(inputClass, "resize-y")}
              autoFocus
            />
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Section (optional)">
              <input
                list="memory-sections"
                value={newSection}
                onChange={(event) => setNewSection(event.target.value)}
                placeholder="Preferences, People, Projects…"
                className={inputClass}
              />
            </Field>
            <Field label="About (optional)">
              <input
                value={newAbout}
                onChange={(event) => setNewAbout(event.target.value)}
                placeholder="the user, Nori, travel…"
                className={inputClass}
              />
            </Field>
          </div>
          <label className="flex min-h-9 items-center gap-2 text-meta text-ink">
            <input
              type="checkbox"
              className="accent-[var(--accent)]"
              checked={newPinned}
              onChange={(event) => setNewPinned(event.target.checked)}
            />
            Pin this memory (never auto-demoted during consolidation)
          </label>
          {newText.trim().length > 0 && !createValidation.ok && <Notice tone="danger">{createValidation.reason}</Notice>}
          <div className="flex flex-wrap gap-2">
            <Button type="submit" variant="primary" size="sm" disabled={!createValidation.ok || !!action.pending}>
              {action.pending === "create" ? "Saving…" : "Save memory"}
            </Button>
            <Button size="sm" variant="quiet" onClick={() => setAdding(false)}>
              Cancel
            </Button>
          </div>
        </form>
      )}

      <label className="relative block">
        <span className="sr-only">Filter core memories</span>
        <Search size={17} className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-ink-3" aria-hidden />
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Filter core memories by text, section, or topic"
          className={cn(inputClass, "h-11 pl-10")}
        />
      </label>

      {core.error && <Failure error={core.error} onRetry={() => void core.reload()} />}
      {action.error && <Notice tone="danger">{action.error}</Notice>}

      {core.loading ? (
        <Loading label="Loading Core Memory…" />
      ) : filtered.length ? (
        <Rows label="Core memories">
          {filtered.map((fact) => {
            const prov = provenanceInfo(fact.provenance);
            const selected = selectedId === fact.id;
            const jumpToChat = messageJumpHref(fact.origin.message_id, "message");
            const busyThis = action.pending?.endsWith(`:${fact.id}`);
            return (
              <li
                key={fact.id}
                id={`memory-${fact.id}`}
                className={cn("px-4 py-3.5", selected && "bg-accent-subtle/40 ring-1 ring-inset ring-accent")}
              >
                {editingId === fact.id ? (
                  <form
                    className="space-y-3"
                    onSubmit={(event) => {
                      event.preventDefault();
                      void saveEdit(fact);
                    }}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-meta font-medium text-ink">Edit memory</span>
                      <ByteCounter text={editText} />
                    </div>
                    <textarea
                      aria-label="Edit memory text"
                      rows={3}
                      value={editText}
                      onChange={(event) => setEditText(event.target.value)}
                      className={cn(inputClass, "resize-y")}
                      autoFocus
                    />
                    <div className="grid gap-3 sm:grid-cols-2">
                      <Field label="Section">
                        <input
                          list="memory-sections"
                          value={editSection}
                          onChange={(event) => setEditSection(event.target.value)}
                          placeholder="Preferences"
                          className={inputClass}
                        />
                      </Field>
                      <Field label="About">
                        <input
                          value={editAbout}
                          onChange={(event) => setEditAbout(event.target.value)}
                          placeholder="the user"
                          className={inputClass}
                        />
                      </Field>
                    </div>
                    <label className="flex min-h-9 items-center gap-2 text-meta text-ink">
                      <input
                        type="checkbox"
                        className="accent-[var(--accent)]"
                        checked={editPinned}
                        onChange={(event) => setEditPinned(event.target.checked)}
                      />
                      Pinned
                    </label>
                    {!editValidation.ok && <Notice tone="danger">{editValidation.reason}</Notice>}
                    <div className="flex flex-wrap gap-2">
                      <Button type="submit" variant="primary" size="sm" disabled={!editValidation.ok || !!action.pending}>
                        {action.pending === `edit:${fact.id}` ? "Saving…" : "Save"}
                      </Button>
                      <Button size="sm" variant="quiet" onClick={() => setEditingId(undefined)}>
                        Cancel
                      </Button>
                    </div>
                  </form>
                ) : (
                  <div className="space-y-2">
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <p className="min-w-0 flex-1 whitespace-pre-wrap text-body text-ink [overflow-wrap:anywhere]">{fact.text}</p>
                      <div className="flex flex-wrap items-center gap-1.5">
                        {fact.pinned && <Pill tone="accent">Pinned</Pill>}
                        <Pill tone={prov.tone}>{prov.label}</Pill>
                        {fact.section && <Pill>{fact.section}</Pill>}
                      </div>
                    </div>

                    {fact.source_text && (
                      <blockquote className="border-l-2 border-hairline-strong pl-2.5 text-caption text-ink-2 [overflow-wrap:anywhere]">
                        “{fact.source_text}”
                      </blockquote>
                    )}

                    <div className="flex flex-wrap items-center justify-between gap-2 pt-0.5">
                      <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption text-ink-3">
                        {fact.about && <span>About {fact.about}</span>}
                        <span>Saved {relative(fact.created_at)}</span>
                        {fact.updated_at && fact.updated_at !== fact.created_at && (
                          <span>· updated {relative(fact.updated_at)}</span>
                        )}
                        {fact.scope === "chat" && <span>· this chat only</span>}
                        {jumpToChat && (
                          <Link href={jumpToChat} className="inline-flex items-center gap-0.5 text-accent-text hover:underline">
                            Source message
                            <ArrowUpRight size={12} aria-hidden />
                          </Link>
                        )}
                        {fact.origin.task_id && (
                          <Link
                            href={`/work/${encodeURIComponent(fact.origin.task_id)}`}
                            className="inline-flex items-center gap-0.5 text-accent-text hover:underline"
                          >
                            Source task
                            <ArrowUpRight size={12} aria-hidden />
                          </Link>
                        )}
                      </span>

                      <div className="flex flex-wrap items-center gap-1">
                        <Button
                          size="sm"
                          variant="quiet"
                          disabled={!!busyThis}
                          onClick={() => void factAction(fact, fact.pinned ? "unpin" : "pin")}
                        >
                          {fact.pinned ? <PinOff size={14} aria-hidden /> : <Pin size={14} aria-hidden />}
                          {fact.pinned ? "Unpin" : "Pin"}
                        </Button>
                        <Button size="sm" variant="quiet" disabled={!!busyThis} onClick={() => startEdit(fact)}>
                          <Pencil size={14} aria-hidden />
                          Edit
                        </Button>
                        <Button
                          size="sm"
                          variant="quiet"
                          disabled={!!busyThis}
                          onClick={() => void factAction(fact, "demote")}
                        >
                          <ArrowDownToLine size={14} aria-hidden />
                          Demote to log
                        </Button>
                        <Confirm
                          danger
                          title="Forget this memory?"
                          body="Nori stops using it. Chats where it came up stay as they are."
                          confirm="Forget"
                          onConfirm={() => void factAction(fact, "forget")}
                          trigger={
                            <Button size="sm" variant="quiet" className="text-danger!" disabled={!!busyThis}>
                              Forget
                            </Button>
                          }
                        />
                      </div>
                    </div>
                  </div>
                )}
              </li>
            );
          })}
        </Rows>
      ) : (
        <Empty
          action={
            query.trim() ? (
              <Button size="sm" variant="neutral" onClick={() => setQuery("")}>
                Clear filter
              </Button>
            ) : (
              <Button size="sm" variant="primary" onClick={() => setAdding(true)}>
                <Plus size={14} aria-hidden />
                Add memory
              </Button>
            )
          }
        >
          {query.trim() ? `No core memories match “${query.trim()}”.` : "Nothing in Core Memory yet."}
        </Empty>
      )}
    </div>
  );
}

function DailyLogsTab({
  logs,
  deepDateLog,
  items,
  selectedDate,
  selectedEntry,
  initialQuery,
  onJump,
}: {
  logs: ReturnType<typeof useResource<List<MemoryLog>>>;
  deepDateLog: ReturnType<typeof useResource<MemoryLog>>;
  items: MemoryLog[];
  selectedDate?: string;
  selectedEntry?: string;
  initialQuery: string;
  onJump: (href: string) => void;
}) {
  const action = useAction();
  const [query, setQuery] = useState(initialQuery);
  const [settled, setSettled] = useState(initialQuery.trim());
  const [layer, setLayer] = useState<SearchLayerFilter>("all");

  const [reviewingKey, setReviewingKey] = useState<string>();
  const [promoteText, setPromoteText] = useState("");
  const [promoteSection, setPromoteSection] = useState("");
  const [promoteAbout, setPromoteAbout] = useState("");
  const [promotePinned, setPromotePinned] = useState(false);
  const [promoteReviewed, setPromoteReviewed] = useState(false);

  useEffect(() => {
    const timer = setTimeout(() => setSettled(query.trim()), 250);
    return () => clearTimeout(timer);
  }, [query]);

  useEffect(() => {
    const targetId = selectedEntry ? `log-entry-${selectedEntry}` : selectedDate ? `log-date-${selectedDate}` : undefined;
    if (!targetId) return;
    const el = document.getElementById(targetId);
    el?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [selectedDate, selectedEntry, items.length]);

  const searchPath = settled
    ? `/memory/search?q=${encodeURIComponent(settled)}${layer !== "all" ? `&layer=${layer}` : ""}&limit=20`
    : null;
  const search = useResource<MemorySearchResponse>(searchPath, { poll: 0, versioned: false });

  const runLogAction = (
    key: string,
    log: MemoryLog,
    entry: MemoryLogEntry,
    value: MemoryLogEntryAction["value"],
    done: string,
  ) =>
    action.run(
      key,
      async () => {
        try {
          const result = await mutate(
            `/memory/logs/${encodeURIComponent(log.date)}/${encodeURIComponent(entry.id)}/actions`,
            {
              request_id: newRequestID(),
              expected_revision: log.revision,
              value,
            },
          );
          invalidate("/memory");
          return result;
        } catch (error) {
          if (error instanceof APIError && error.stale) invalidate("/memory");
          throw error;
        }
      },
      done,
    );

  const openPromoteReview = (logDate: string, entry: MemoryLogEntry) => {
    setReviewingKey(`${logDate}:${entry.id}`);
    setPromoteText(entry.text);
    setPromoteSection(entry.section ?? "");
    setPromoteAbout(entry.about ?? "");
    setPromotePinned(false);
    setPromoteReviewed(false);
  };

  const submitPromoteReview = async (log: MemoryLog, entry: MemoryLogEntry) => {
    const validated = validatePromoteDraft(entry, promoteText, promoteReviewed);
    if (!validated.ok) return;
    const value: MemoryLogEntryAction["value"] = {
      action: "promote",
      text: validated.text,
      ...(promoteAbout.trim() ? { about: promoteAbout.trim() } : {}),
      ...(promoteSection.trim() ? { section: promoteSection.trim() } : {}),
      ...(promotePinned ? { pinned: true } : {}),
    };
    const result = await runLogAction(`promote:${log.date}:${entry.id}`, log, entry, value, "Promoted to Core Memory.");
    if (result) setReviewingKey(undefined);
  };

  const quickPromote = (log: MemoryLog, entry: MemoryLogEntry) => {
    if (requiresReauthorForPromote(entry.provenance)) {
      openPromoteReview(log.date, entry);
      return;
    }
    const value: MemoryLogEntryAction["value"] = {
      action: "promote",
      ...(entry.about ? { about: entry.about } : {}),
      ...(entry.section ? { section: entry.section } : {}),
    };
    void runLogAction(`promote:${log.date}:${entry.id}`, log, entry, value, "Promoted to Core Memory.");
  };

  const forgetEntry = (log: MemoryLog, entry: MemoryLogEntry) =>
    runLogAction(`forget:${log.date}:${entry.id}`, log, entry, { action: "forget" }, "Log entry forgotten.");

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <label className="relative block flex-1">
          <span className="sr-only">Search memory</span>
          <Search size={17} className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-ink-3" aria-hidden />
          <input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search daily logs and core memories…"
            className={cn(inputClass, "h-11 pr-9 pl-10")}
          />
          {query && (
            <button
              type="button"
              aria-label="Clear search"
              onClick={() => {
                setQuery("");
                setSettled("");
              }}
              className="absolute top-1/2 right-2.5 -translate-y-1/2 rounded-full p-1 text-ink-3 hover:bg-fill-1 hover:text-ink"
            >
              <X size={15} aria-hidden />
            </button>
          )}
        </label>
        <div className="flex shrink-0 items-center gap-1" role="group" aria-label="Search layer">
          {(
            [
              { id: "all", label: "All" },
              { id: "log", label: "Logs" },
              { id: "core", label: "Core" },
            ] as const
          ).map((option) => (
            <button
              key={option.id}
              type="button"
              aria-pressed={layer === option.id}
              onClick={() => setLayer(option.id)}
              className={cn(
                "h-9 rounded-full border px-3 text-meta font-medium",
                layer === option.id
                  ? "border-chip-selected-border bg-chip-selected-bg text-chip-selected-fg"
                  : "border-transparent bg-fill-1 text-ink-2 hover:text-ink",
              )}
            >
              {option.label}
            </button>
          ))}
        </div>
      </div>

      {action.error && <Notice tone="danger">{action.error}</Notice>}

      {settled ? (
        search.loading ? (
          <Loading label="Searching memory…" />
        ) : search.error ? (
          <Failure error={search.error} onRetry={() => void search.reload()} />
        ) : search.data?.items.length ? (
          <Section title={`${plural(search.data.items.length, "search hit")}`}>
            <Rows label="Memory search results">
              {search.data.items.map((hit) => {
                const prov = provenanceInfo(hit.provenance);
                const href = memoryHitHref(hit);
                return (
                  <Row
                    key={hit.chunk_id}
                    onClick={() => {
                      if (hit.layer === "log") {
                        setQuery("");
                        setSettled("");
                      }
                      onJump(href);
                    }}
                    title={hit.snippet}
                    wrapTitle
                    detail={[
                      hit.section,
                      hit.about ? `About ${hit.about}` : "",
                      hit.log_date ?? "",
                      relative(hit.updated_at ?? hit.created_at),
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                    trailing={
                      <span className="flex flex-wrap items-center gap-1.5">
                        <Pill tone={hit.layer === "core" ? "accent" : "neutral"}>{hit.layer === "core" ? "Core" : "Log"}</Pill>
                        <Pill tone={prov.tone}>{prov.label}</Pill>
                      </span>
                    }
                    chevron
                  />
                );
              })}
            </Rows>
          </Section>
        ) : (
          <Empty
            action={
              <Button
                size="sm"
                variant="neutral"
                onClick={() => {
                  setQuery("");
                  setSettled("");
                }}
              >
                Clear search
              </Button>
            }
          >
            Nothing in memory matches “{settled}”.
          </Empty>
        )
      ) : (
        <>
          {logs.error && <Failure error={logs.error} onRetry={() => void logs.reload()} />}
          {deepDateLog.error && <Failure error={deepDateLog.error} onRetry={() => void deepDateLog.reload()} />}
          {logs.loading || (selectedDate && deepDateLog.loading) ? (
            <Loading label="Loading Daily Logs…" />
          ) : items.length ? (
            items.map((log) => (
              <Section
                key={log.date}
                title={log.date}
                actions={<span className="text-caption text-ink-3">{plural(log.entries.length, "entry", "entries")}</span>}
              >
                <div id={`log-date-${log.date}`}>
                  {log.entries.length ? (
                    <Rows label={`Entries for ${log.date}`}>
                      {log.entries.map((entry) => {
                        const key = `${log.date}:${entry.id}`;
                        const prov = provenanceInfo(entry.provenance);
                        const needsReview = requiresReauthorForPromote(entry.provenance);
                        const reviewing = reviewingKey === key;
                        const selected =
                          selectedEntry === entry.id && (!selectedDate || selectedDate === log.date);
                        const jumpToChat = messageJumpHref(entry.source_input_id, "input");
                        const busyThis = action.pending?.endsWith(`:${key}`);
                        const reviewCheck = reviewing ? validatePromoteDraft(entry, promoteText, promoteReviewed) : undefined;

                        return (
                          <li
                            key={entry.id}
                            id={`log-entry-${entry.id}`}
                            className={cn("px-4 py-3.5", selected && "bg-accent-subtle/40 ring-1 ring-inset ring-accent")}
                          >
                            <div className="space-y-2">
                              <div className="flex flex-wrap items-start justify-between gap-2">
                                <p className="min-w-0 flex-1 whitespace-pre-wrap text-body text-ink [overflow-wrap:anywhere]">
                                  {entry.text}
                                </p>
                                <div className="flex flex-wrap items-center gap-1.5">
                                  <Pill tone={prov.tone}>{prov.label}</Pill>
                                  {entry.section && <Pill>{entry.section}</Pill>}
                                </div>
                              </div>

                              {entry.source_text && (
                                <blockquote className="border-l-2 border-hairline-strong pl-2.5 text-caption text-ink-2 [overflow-wrap:anywhere]">
                                  “{entry.source_text}”
                                </blockquote>
                              )}

                              <div className="flex flex-wrap items-center justify-between gap-2 pt-0.5">
                                <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption text-ink-3">
                                  {entry.about && <span>About {entry.about}</span>}
                                  <span>{relative(entry.created_at)}</span>
                                  {jumpToChat && (
                                    <Link href={jumpToChat} className="inline-flex items-center gap-0.5 text-accent-text hover:underline">
                                      Source message
                                      <ArrowUpRight size={12} aria-hidden />
                                    </Link>
                                  )}
                                  {entry.run_id && (
                                    <Link
                                      href={`/work/${encodeURIComponent(entry.run_id)}`}
                                      className="inline-flex items-center gap-0.5 text-accent-text hover:underline"
                                    >
                                      Task run
                                      <ArrowUpRight size={12} aria-hidden />
                                    </Link>
                                  )}
                                </span>

                                <div className="flex flex-wrap items-center gap-1">
                                  <Button
                                    size="sm"
                                    variant="quiet"
                                    disabled={!!busyThis}
                                    onClick={() => quickPromote(log, entry)}
                                  >
                                    {needsReview ? "Review & promote" : "Promote to Core"}
                                  </Button>
                                  <Confirm
                                    danger
                                    title="Forget this log entry?"
                                    body="Removes this entry from the daily log and search index."
                                    confirm="Forget"
                                    onConfirm={() => void forgetEntry(log, entry)}
                                    trigger={
                                      <Button size="sm" variant="quiet" className="text-danger!" disabled={!!busyThis}>
                                        Forget
                                      </Button>
                                    }
                                  />
                                </div>
                              </div>

                              {reviewing && (
                                <form
                                  className="mt-3 space-y-3 rounded-tile border border-hairline-strong bg-fill-1/40 p-3.5"
                                  onSubmit={(event) => {
                                    event.preventDefault();
                                    void submitPromoteReview(log, entry);
                                  }}
                                >
                                  <Notice tone="warning">
                                    Task outcomes never enter Core Memory automatically. Review or re-author the text below to save it as an explicit memory.
                                  </Notice>
                                  <div className="flex items-center justify-between gap-2">
                                    <span className="text-meta font-medium text-ink">Re-authored Core Memory text</span>
                                    <ByteCounter text={promoteText} />
                                  </div>
                                  <textarea
                                    aria-label="Promote text to Core Memory"
                                    rows={3}
                                    value={promoteText}
                                    onChange={(event) => setPromoteText(event.target.value)}
                                    className={cn(inputClass, "resize-y")}
                                    autoFocus
                                  />
                                  <div className="grid gap-3 sm:grid-cols-2">
                                    <Field label="Section (optional)">
                                      <input
                                        value={promoteSection}
                                        onChange={(event) => setPromoteSection(event.target.value)}
                                        placeholder="Projects, Decisions…"
                                        className={inputClass}
                                      />
                                    </Field>
                                    <Field label="About (optional)">
                                      <input
                                        value={promoteAbout}
                                        onChange={(event) => setPromoteAbout(event.target.value)}
                                        placeholder="the user"
                                        className={inputClass}
                                      />
                                    </Field>
                                  </div>
                                  <label className="flex min-h-9 items-center gap-2 text-meta text-ink">
                                    <input
                                      type="checkbox"
                                      className="accent-[var(--accent)]"
                                      checked={promotePinned}
                                      onChange={(event) => setPromotePinned(event.target.checked)}
                                    />
                                    Pin in Core Memory
                                  </label>
                                  <label className="flex min-h-9 items-center gap-2 text-meta font-medium text-ink">
                                    <input
                                      type="checkbox"
                                      className="accent-[var(--accent)]"
                                      checked={promoteReviewed}
                                      onChange={(event) => setPromoteReviewed(event.target.checked)}
                                    />
                                    I have reviewed this fact and want Nori to keep it in Core Memory.
                                  </label>
                                  <div className="flex flex-wrap gap-2">
                                    <Button
                                      type="submit"
                                      variant="primary"
                                      size="sm"
                                      disabled={!reviewCheck?.ok || !!action.pending}
                                    >
                                      {action.pending === `promote:${key}` ? "Promoting…" : "Promote to Core"}
                                    </Button>
                                    <Button size="sm" variant="quiet" onClick={() => setReviewingKey(undefined)}>
                                      Cancel
                                    </Button>
                                  </div>
                                </form>
                              )}
                            </div>
                          </li>
                        );
                      })}
                    </Rows>
                  ) : (
                    <Empty>No entries left for {log.date}.</Empty>
                  )}
                </div>
              </Section>
            ))
          ) : (
            <Empty>
              No daily logs yet. As conversations compact or tasks finish, durable notes appear here by date.
            </Empty>
          )}
        </>
      )}
    </div>
  );
}

function WorkingMemoryTab({
  working,
}: {
  working: ReturnType<typeof useResource<WorkingMemory>>;
}) {
  const action = useAction();
  const data = working.data;

  const consolidate = () =>
    action.run(
      "consolidate",
      async () => {
        const result = await mutate("/memory/actions", {
          request_id: newRequestID(),
          expected_revision: 0,
          value: { action: "consolidate" },
        });
        invalidate("/memory");
        return result;
      },
      "Memory consolidation requested.",
    );

  const itemAction = (item: WorkingMemoryItem, kind: WorkingMemoryItemAction["value"]["action"]) => {
    if (!data) return;
    return action.run(
      `${kind}:${item.id}`,
      async () => {
        try {
          const result = await mutate(`/memory/working/${encodeURIComponent(item.id)}/actions`, {
            request_id: newRequestID(),
            expected_revision: data.revision,
            value: { action: kind },
          });
          invalidate("/memory");
          return result;
        } catch (error) {
          if (error instanceof APIError && error.stale) invalidate("/memory");
          throw error;
        }
      },
      kind === "promote" ? "Promoted to Core Memory." : "Item dismissed from Working Memory.",
    );
  };

  if (working.loading && !data) return <Loading label="Loading Working Memory…" />;
  if (!data) return <Failure error={working.error ?? "Working memory unavailable."} onRetry={() => void working.reload()} />;

  const watermarks = workingMemoryWatermarks(data);

  return (
    <div className="space-y-6">
      {working.error && <Failure error={working.error} onRetry={() => void working.reload()} />}
      {action.error && <Notice tone="danger">{action.error}</Notice>}

      <Section
        title="Session gist"
        actions={
          <Button
            size="sm"
            variant="neutral"
            disabled={!!action.pending}
            onClick={() => void consolidate()}
          >
            <Sparkles size={14} aria-hidden />
            {action.pending === "consolidate" ? "Consolidating…" : "Consolidate now"}
          </Button>
        }
      >
        {data.gist ? (
          <div className="rounded-card border border-hairline bg-surface p-4 text-body text-ink whitespace-pre-wrap [overflow-wrap:anywhere]">
            {data.gist}
          </div>
        ) : (
          <Empty>No active session summary yet. Nori builds one as your conversation grows.</Empty>
        )}
      </Section>

      <Section title={`Quote-verified items${data.items.length ? ` (${data.items.length})` : ""}`}>
        {data.items.length ? (
          <Rows label="Working memory items">
            {data.items.map((item) => {
              const kindMeta = WORKING_KIND_META[item.kind] ?? { label: item.kind, tone: "neutral" as Tone };
              const jumpToChat = messageJumpHref(item.input_id, "input");
              const busyThis = action.pending?.endsWith(`:${item.id}`);
              return (
                <li key={item.id} className="px-4 py-3.5">
                  <div className="space-y-2">
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <p className="min-w-0 flex-1 text-body text-ink [overflow-wrap:anywhere]">“{item.quote}”</p>
                      <Pill tone={kindMeta.tone}>{kindMeta.label}</Pill>
                    </div>
                    {item.note && <p className="text-meta text-ink-2 [overflow-wrap:anywhere]">{item.note}</p>}
                    <div className="flex flex-wrap items-center justify-between gap-2 pt-0.5">
                      <span className="flex flex-wrap items-center gap-x-2 text-caption text-ink-3">
                        <span className="font-mono">{item.input_id}</span>
                        {jumpToChat && (
                          <Link href={jumpToChat} className="inline-flex items-center gap-0.5 text-accent-text hover:underline">
                            Jump to message
                            <ArrowUpRight size={12} aria-hidden />
                          </Link>
                        )}
                      </span>
                      <div className="flex flex-wrap items-center gap-1">
                        <Button
                          size="sm"
                          variant="quiet"
                          disabled={!!busyThis}
                          onClick={() => void itemAction(item, "promote")}
                        >
                          Promote to Core
                        </Button>
                        <Button
                          size="sm"
                          variant="quiet"
                          disabled={!!busyThis}
                          onClick={() => void itemAction(item, "dismiss")}
                        >
                          Dismiss
                        </Button>
                      </div>
                    </div>
                  </div>
                </li>
              );
            })}
          </Rows>
        ) : (
          <Empty>No active quote-verified items in working memory.</Empty>
        )}
      </Section>

      <Section title="Watermarks & status">
        <div className="rounded-card border border-hairline bg-surface p-4">
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
            <dt className="text-ink-3">Turns</dt>
            <dd className="text-ink">{data.turns}</dd>

            <dt className="text-ink-3">Extracted through</dt>
            <dd className="text-ink">{watermarks.extractedThrough}</dd>

            <dt className="text-ink-3">Flushed through</dt>
            <dd className="text-ink">{watermarks.flushedThrough}</dd>

            <dt className="text-ink-3">Window through</dt>
            <dd className="text-ink">{watermarks.windowThrough}</dd>

            <dt className="text-ink-3">Last dream</dt>
            <dd className="text-ink">{watermarks.lastDream}</dd>

            <dt className="text-ink-3">Core budget</dt>
            <dd className="text-ink">{formatByteBudget(data.core_bytes, data.core_budget_bytes)}</dd>

            {data.model && (
              <>
                <dt className="text-ink-3">Model</dt>
                <dd className="font-mono text-mono-data text-ink">{data.model}</dd>
              </>
            )}
          </dl>
          <p className="mt-3 border-t border-hairline pt-2.5 text-caption text-ink-3">
            Nori extracts durable memories after 20 minutes idle (even on short 1–5 turn chats) and compacts older turns past the newest 5 verbatim turns.
          </p>
        </div>
      </Section>
    </div>
  );
}
