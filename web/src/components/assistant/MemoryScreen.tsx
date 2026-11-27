"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, Search, X } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { getJSON, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistantConfig } from "@/lib/assistant/context";
import { useResource } from "@/lib/assistant/useResource";
import type { MemoryFact, MemoryLog, MemorySearchResponse } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { useVersionedMemoryMutation } from "./memory/MemoryFactForm";
import { formatLogDate, groupMemoriesByTopic, normalizeMemoryTopic } from "./memory/memoryHelpers";
import { Empty, Failure, inputClass, Loading, Notice, NotYet, Row, Rows, Screen, Section } from "./ui";

export {
  buildMemoryHref,
  coreBudgetStats,
  DEFAULT_CORE_BUDGET_BYTES,
  filterCoreMemories,
  formatByteBudget,
  formatKB,
  formatWatermark,
  groupMemoriesByTopic,
  MAX_MEMORY_TEXT_BYTES,
  memoryHitHref,
  messageJumpHref,
  normalizeMemoryTopic,
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
  const { has } = useAssistantConfig();
  const params = useSearchParams();
  const router = useRouter();
  const enabled = has("memory");

  const initialQuery = params.get("q") ?? "";
  const selectedTopicParam = params.get("topic") ?? undefined;
  const selectedId = params.get("id") ?? undefined;
  const selectedDate = params.get("date") ?? undefined;
  const selectedEntry = params.get("entry") ?? undefined;

  const [query, setQuery] = useState(initialQuery);
  const [settledQuery, setSettledQuery] = useState(initialQuery.trim());
  const [confirmId, setConfirmId] = useState<string | null>(null);

  useEffect(() => {
    const timer = setTimeout(() => setSettledQuery(query.trim()), 200);
    return () => clearTimeout(timer);
  }, [query]);

  const core = useResource<List<MemoryFact>>(enabled ? "/memory" : null, { poll: 30_000 });
  const searchPath =
    enabled && settledQuery ? `/memory/search?q=${encodeURIComponent(settledQuery)}&limit=20` : null;
  const searchRes = useResource<MemorySearchResponse>(searchPath, { poll: 0, versioned: false });
  const deepDateLog = useResource<MemoryLog>(
    enabled && selectedDate ? `/memory/logs/${encodeURIComponent(selectedDate)}` : null,
    { poll: 0 },
  );
  const mutation = useVersionedMemoryMutation();

  const facts = useMemo(() => core.data?.items ?? [], [core.data?.items]);
  const topics = useMemo(() => groupMemoriesByTopic(facts), [facts]);

  const resolvedTopic = useMemo(() => {
    if (selectedTopicParam) return selectedTopicParam;
    if (selectedId) {
      const matched = facts.find((fact) => fact.id === selectedId);
      if (matched) return normalizeMemoryTopic(matched.section);
    }
    return undefined;
  }, [selectedTopicParam, selectedId, facts]);

  useEffect(() => {
    if (!selectedId) return;
    const el = document.getElementById(`memory-${selectedId}`);
    el?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [selectedId, resolvedTopic, facts.length]);

  const forgetFact = async (fact: MemoryFact) => {
    const ok = await mutation.run(
      `forget:${fact.id}`,
      () =>
        mutate(`/memory/${encodeURIComponent(fact.id)}/actions`, {
          request_id: newRequestID(),
          expected_revision: fact.revision,
          value: { action: "forget" },
        }),
      "Memory forgotten.",
    );
    if (ok) setConfirmId(null);
  };

  const forgetLogEntry = async (date: string, entryId: string, knownRevision?: number) => {
    const ok = await mutation.run(
      `forget-log:${entryId}`,
      async () => {
        const revision =
          knownRevision ?? (await getJSON<MemoryLog>(`/memory/logs/${encodeURIComponent(date)}`)).revision;
        return mutate(`/memory/logs/${encodeURIComponent(date)}/${encodeURIComponent(entryId)}/actions`, {
          request_id: newRequestID(),
          expected_revision: revision,
          value: { action: "forget" },
        });
      },
      "Memory forgotten.",
    );
    if (ok) {
      setConfirmId(null);
      if (searchPath) void searchRes.reload();
      if (selectedDate === date) void deepDateLog.reload();
    }
  };

  if (!enabled) {
    return (
      <Screen title="Memory" back="/more" backLabel="More">
        <NotYet what="Memory" />
      </Screen>
    );
  }

  const openTopic = (topic: string) => {
    setConfirmId(null);
    router.replace(`/memory?topic=${encodeURIComponent(topic)}`, { scroll: false });
  };

  const closeSubView = () => {
    setConfirmId(null);
    router.replace("/memory", { scroll: false });
  };

  if (selectedDate && !query.trim()) {
    const log = deepDateLog.data;
    const entries = log
      ? selectedEntry
        ? log.entries.filter((item) => item.id === selectedEntry)
        : log.entries
      : [];
    const shownEntries = entries.length ? entries : (log?.entries ?? []);
    return (
      <Screen title={formatLogDate(selectedDate)}>
        <button
          type="button"
          onClick={closeSubView}
          className="mb-4 inline-flex h-8 items-center gap-1.5 rounded-full bg-fill-1 px-3 text-meta font-semibold text-ink transition-colors hover:bg-fill-2"
        >
          <ArrowLeft size={14} aria-hidden />
          Memory
        </button>
        {deepDateLog.error && <Failure error={deepDateLog.error} onRetry={() => void deepDateLog.reload()} />}
        {mutation.error && (
          <div className="mb-4">
            <Notice tone="danger">{mutation.error}</Notice>
          </div>
        )}
        {deepDateLog.loading && !log ? (
          <Loading label="Loading past chat notes…" />
        ) : shownEntries.length && log ? (
          <Rows label="Past chats">
            {shownEntries.map((entry) => {
              const rowId = `log:${entry.id}`;
              return (
                <ForgettableRow
                  key={entry.id}
                  id={rowId}
                  text={entry.text}
                  highlighted={selectedEntry === entry.id}
                  confirming={confirmId === rowId}
                  busy={mutation.pending === `forget-log:${entry.id}`}
                  onStartConfirm={() => setConfirmId(rowId)}
                  onCancel={() => setConfirmId(null)}
                  onConfirm={() => void forgetLogEntry(log.date, entry.id, log.revision)}
                />
              );
            })}
          </Rows>
        ) : (
          <Empty>No notes found for {formatLogDate(selectedDate)}.</Empty>
        )}
      </Screen>
    );
  }

  if (resolvedTopic && !query.trim()) {
    const topicFacts = facts.filter((fact) => normalizeMemoryTopic(fact.section) === resolvedTopic);
    return (
      <Screen title={resolvedTopic}>
        <button
          type="button"
          onClick={closeSubView}
          className="mb-4 inline-flex h-8 items-center gap-1.5 rounded-full bg-fill-1 px-3 text-meta font-semibold text-ink transition-colors hover:bg-fill-2"
        >
          <ArrowLeft size={14} aria-hidden />
          Memory
        </button>
        {mutation.error && (
          <div className="mb-4">
            <Notice tone="danger">{mutation.error}</Notice>
          </div>
        )}
        {core.loading && !core.data ? (
          <Loading label="Loading memories…" />
        ) : topicFacts.length ? (
          <Rows label={resolvedTopic}>
            {topicFacts.map((fact) => (
              <ForgettableRow
                key={fact.id}
                id={fact.id}
                text={fact.text}
                highlighted={selectedId === fact.id}
                confirming={confirmId === fact.id}
                busy={mutation.pending === `forget:${fact.id}`}
                onStartConfirm={() => setConfirmId(fact.id)}
                onCancel={() => setConfirmId(null)}
                onConfirm={() => void forgetFact(fact)}
              />
            ))}
          </Rows>
        ) : (
          <Empty>Nothing left in {resolvedTopic}.</Empty>
        )}
      </Screen>
    );
  }

  const trimmed = query.trim();
  const needle = trimmed.toLowerCase();
  const matchingFacts = trimmed
    ? facts.filter(
        (fact) =>
          fact.text.toLowerCase().includes(needle) ||
          normalizeMemoryTopic(fact.section).toLowerCase().includes(needle),
      )
    : [];
  const logHits = trimmed ? (searchRes.data?.items ?? []).filter((hit) => hit.layer === "log") : [];

  return (
    <Screen title="Memory" back="/more" backLabel="More">
      <div className="space-y-4">
        <label className="relative block">
          <span className="sr-only">Search everything Nori remembers</span>
          <Search
            size={16}
            className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-ink-3"
            aria-hidden
          />
          <input
            type="search"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setConfirmId(null);
            }}
            placeholder="Search everything Nori remembers…"
            className={cn(inputClass, "h-11 pr-9 pl-10")}
          />
          {query && (
            <button
              type="button"
              aria-label="Clear search"
              onClick={() => {
                setQuery("");
                setSettledQuery("");
                setConfirmId(null);
              }}
              className="absolute top-1/2 right-2.5 -translate-y-1/2 rounded-full p-1 text-ink-3 hover:bg-fill-1 hover:text-ink"
            >
              <X size={15} aria-hidden />
            </button>
          )}
        </label>

        {core.error && <Failure error={core.error} onRetry={() => void core.reload()} />}
        {mutation.error && <Notice tone="danger">{mutation.error}</Notice>}

        {trimmed ? (
          <div className="space-y-5">
            {searchRes.error && <Failure error={searchRes.error} onRetry={() => void searchRes.reload()} />}

            {matchingFacts.length > 0 && (
              <Section title="Saved">
                <Rows label="Saved">
                  {matchingFacts.map((fact) => (
                    <ForgettableRow
                      key={fact.id}
                      id={fact.id}
                      text={fact.text}
                      detail={normalizeMemoryTopic(fact.section)}
                      confirming={confirmId === fact.id}
                      busy={mutation.pending === `forget:${fact.id}`}
                      onStartConfirm={() => setConfirmId(fact.id)}
                      onCancel={() => setConfirmId(null)}
                      onConfirm={() => void forgetFact(fact)}
                    />
                  ))}
                </Rows>
              </Section>
            )}

            {logHits.length > 0 && (
              <Section title="Past chats">
                <Rows label="Past chats">
                  {logHits.map((hit, idx) => {
                    const rowId = `log:${hit.entry_id ?? hit.log_date ?? idx}`;
                    const canForget = Boolean(hit.log_date && hit.entry_id);
                    return (
                      <ForgettableRow
                        key={rowId}
                        id={rowId}
                        text={hit.snippet}
                        detail={hit.log_date ? formatLogDate(hit.log_date) : undefined}
                        confirming={confirmId === rowId}
                        busy={mutation.pending === `forget-log:${hit.entry_id}`}
                        canForget={canForget}
                        onStartConfirm={() => setConfirmId(rowId)}
                        onCancel={() => setConfirmId(null)}
                        onConfirm={() => {
                          if (hit.log_date && hit.entry_id) {
                            void forgetLogEntry(hit.log_date, hit.entry_id);
                          }
                        }}
                      />
                    );
                  })}
                </Rows>
              </Section>
            )}

            {!matchingFacts.length && !logHits.length && !searchRes.error && (
              searchRes.loading || settledQuery !== trimmed ? (
                <Loading label="Searching memory…" />
              ) : (
                <Empty>Nothing matches “{trimmed}”.</Empty>
              )
            )}
          </div>
        ) : core.loading && !core.data ? (
          <Loading label="Loading memory…" />
        ) : topics.length ? (
          <Rows label="Memory topics">
            {topics.map((group) => (
              <Row
                key={group.topic}
                onClick={() => openTopic(group.topic)}
                title={group.topic}
                trailing={
                  <span className="text-meta font-medium tabular-nums text-ink-3">
                    {group.items.length}
                  </span>
                }
                chevron
              />
            ))}
          </Rows>
        ) : (
          <Empty>Nori will remember helpful details as you chat.</Empty>
        )}
      </div>
    </Screen>
  );
}

function ForgettableRow({
  id,
  text,
  detail,
  highlighted = false,
  confirming,
  busy,
  canForget = true,
  onStartConfirm,
  onCancel,
  onConfirm,
}: {
  id: string;
  text: string;
  detail?: string;
  highlighted?: boolean;
  confirming: boolean;
  busy?: boolean;
  canForget?: boolean;
  onStartConfirm: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <li
      id={`memory-${id}`}
      className={cn(
        "group flex items-start gap-3 px-4 py-3.5",
        highlighted && "bg-accent-subtle/40 ring-1 ring-inset ring-accent",
      )}
    >
      <span className="min-w-0 flex-1">
        <span className="block whitespace-pre-wrap text-body leading-relaxed text-ink [overflow-wrap:anywhere]">
          {text}
        </span>
        {detail && <span className="mt-1 block text-caption text-ink-3">{detail}</span>}
      </span>
      {canForget && (
        confirming ? (
          <div className="flex shrink-0 items-center gap-1.5">
            <Button size="sm" variant="danger" disabled={busy} onClick={onConfirm}>
              {busy ? "Forgetting…" : "Forget"}
            </Button>
            <Button size="sm" variant="quiet" disabled={busy} onClick={onCancel}>
              Keep
            </Button>
          </div>
        ) : (
          <button
            type="button"
            aria-label="Forget"
            onClick={onStartConfirm}
            className="grid size-7 shrink-0 place-items-center rounded-full text-ink-3 opacity-60 transition-colors hover:bg-danger-bg hover:text-danger hover:opacity-100 focus-visible:opacity-100 sm:opacity-35 sm:group-hover:opacity-100"
          >
            <X size={14} aria-hidden />
          </button>
        )
      )}
    </li>
  );
}
