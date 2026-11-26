"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { ArrowDownToLine, ArrowUpRight, Pencil, Pin, PinOff, Plus, Search } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistantConfig } from "@/lib/assistant/context";
import { plural, relative } from "@/lib/assistant/format";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { MemoryAction, MemoryFact, MemoryLog, WorkingMemory } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { DailyLogsTab } from "./memory/DailyLogsTab";
import { MemoryFactForm, useVersionedMemoryMutation } from "./memory/MemoryFactForm";
import {
  buildEditableValue,
  buildMemoryHref,
  coreBudgetStats,
  filterCoreMemories,
  formatByteBudget,
  messageJumpHref,
  parseMemoryTab,
  provenanceInfo,
  SECTION_SUGGESTIONS,
  validateMemoryText,
  type MemoryTab,
} from "./memory/memoryHelpers";
import { WorkingMemoryTab } from "./memory/WorkingMemoryTab";
import {
  Confirm,
  Empty,
  Failure,
  inputClass,
  Loading,
  Notice,
  NotYet,
  Pill,
  RefreshButton,
  Rows,
  Screen,
  UnderlineTabs,
} from "./ui";

export {
  buildMemoryHref,
  coreBudgetStats,
  DEFAULT_CORE_BUDGET_BYTES,
  filterCoreMemories,
  formatByteBudget,
  formatKB,
  formatWatermark,
  MAX_MEMORY_TEXT_BYTES,
  memoryHitHref,
  messageJumpHref,
  parseMemoryTab,
  provenanceInfo,
  requiresReauthorForPromote,
  utf8Bytes,
  validateMemoryText,
  validatePromoteDraft,
  workingMemoryWatermarks,
  type MemoryTab,
} from "./memory/memoryHelpers";

type List<T> = { items: T[] };

export function MemoryScreen() {
  const invalidate = useResourceInvalidation();
  const { has } = useAssistantConfig();
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
      <datalist id="memory-sections">
        {SECTION_SUGGESTIONS.map((option) => (
          <option key={option} value={option} />
        ))}
      </datalist>

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
  const mutation = useVersionedMemoryMutation();
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

  const createValidation = validateMemoryText(newText);
  const createFact = async () => {
    if (!createValidation.ok) return;
    const created = await mutation.run(
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
    const saved = await mutation.run(
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
    return mutation.run(
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

      {adding && (
        <MemoryFactForm
          className="rounded-card border border-hairline-strong bg-surface p-4"
          heading="Add memory"
          headingAs="h2"
          text={newText}
          onTextChange={setNewText}
          textLabel="Fact"
          textAriaLabel="New memory text"
          textPlaceholder="Prefers aisle seats and direct morning flights…"
          section={newSection}
          onSectionChange={setNewSection}
          about={newAbout}
          onAboutChange={setNewAbout}
          pinned={newPinned}
          onPinnedChange={setNewPinned}
          pinnedLabel="Pin this memory (never auto-demoted during consolidation)"
          validationError={newText.trim().length > 0 && !createValidation.ok ? createValidation.reason : undefined}
          submitLabel="Save memory"
          submittingLabel="Saving…"
          submitting={mutation.pending === "create"}
          submitDisabled={!createValidation.ok || !!mutation.pending}
          onSubmit={() => void createFact()}
          onCancel={() => setAdding(false)}
        />
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
      {mutation.error && <Notice tone="danger">{mutation.error}</Notice>}

      {core.loading ? (
        <Loading label="Loading Core Memory…" />
      ) : filtered.length ? (
        <Rows label="Core memories">
          {filtered.map((fact) => {
            const prov = provenanceInfo(fact.provenance);
            const selected = selectedId === fact.id;
            const jumpToChat = messageJumpHref(fact.origin.message_id, "message");
            const busyThis = mutation.pending?.endsWith(`:${fact.id}`);
            return (
              <li
                key={fact.id}
                id={`memory-${fact.id}`}
                className={cn("px-4 py-3.5", selected && "bg-accent-subtle/40 ring-1 ring-inset ring-accent")}
              >
                {editingId === fact.id ? (
                  <MemoryFactForm
                    heading="Edit memory"
                    text={editText}
                    onTextChange={setEditText}
                    textAriaLabel="Edit memory text"
                    section={editSection}
                    onSectionChange={setEditSection}
                    sectionLabel="Section"
                    sectionPlaceholder="Preferences"
                    about={editAbout}
                    onAboutChange={setEditAbout}
                    aboutLabel="About"
                    aboutPlaceholder="the user"
                    pinned={editPinned}
                    onPinnedChange={setEditPinned}
                    pinnedLabel="Pinned"
                    validationError={!editValidation.ok ? editValidation.reason : undefined}
                    submitLabel="Save"
                    submittingLabel="Saving…"
                    submitting={mutation.pending === `edit:${fact.id}`}
                    submitDisabled={!editValidation.ok || !!mutation.pending}
                    onSubmit={() => void saveEdit(fact)}
                    onCancel={() => setEditingId(undefined)}
                  />
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
                            <Button size="sm" variant="danger-quiet" disabled={!!busyThis}>
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
