"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { ArrowUpRight } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { mutate, newRequestID } from "@/lib/assistant/api";
import { plural, relative } from "@/lib/assistant/format";
import type { useResource } from "@/lib/assistant/useResource";
import type { MemoryLog, MemoryLogEntry, MemoryLogEntryAction } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { MemorySearchControls, useMemorySearch } from "../MemorySearchControls";
import { Confirm, Empty, Failure, Loading, Notice, Pill, Row, Rows, Section } from "../ui";
import { MemoryFactForm, useVersionedMemoryMutation } from "./MemoryFactForm";
import {
  memoryHitHref,
  messageJumpHref,
  provenanceInfo,
  requiresReauthorForPromote,
  validatePromoteDraft,
} from "./memoryHelpers";

type List<T> = { items: T[] };

export function DailyLogsTab({
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
  const mutation = useVersionedMemoryMutation();
  const [reviewingKey, setReviewingKey] = useState<string>();
  const [promoteText, setPromoteText] = useState("");
  const [promoteSection, setPromoteSection] = useState("");
  const [promoteAbout, setPromoteAbout] = useState("");
  const [promotePinned, setPromotePinned] = useState(false);
  const [promoteReviewed, setPromoteReviewed] = useState(false);

  useEffect(() => {
    const targetId = selectedEntry ? `log-entry-${selectedEntry}` : selectedDate ? `log-date-${selectedDate}` : undefined;
    if (!targetId) return;
    const el = document.getElementById(targetId);
    el?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [selectedDate, selectedEntry, items.length]);

  const { query, setQuery, settled, layer, setLayer, search, clearSearch } = useMemorySearch(initialQuery);

  const runLogAction = (
    key: string,
    log: MemoryLog,
    entry: MemoryLogEntry,
    value: MemoryLogEntryAction["value"],
    done: string,
  ) =>
    mutation.run(
      key,
      () =>
        mutate(`/memory/logs/${encodeURIComponent(log.date)}/${encodeURIComponent(entry.id)}/actions`, {
          request_id: newRequestID(),
          expected_revision: log.revision,
          value,
        }),
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
      <MemorySearchControls query={query} setQuery={setQuery} layer={layer} setLayer={setLayer} clearSearch={clearSearch} />

      {mutation.error && <Notice tone="danger">{mutation.error}</Notice>}

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
                        clearSearch();
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
              <Button size="sm" variant="neutral" onClick={clearSearch}>
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
                        const selected = selectedEntry === entry.id && (!selectedDate || selectedDate === log.date);
                        const jumpToChat = messageJumpHref(entry.source_input_id, "input");
                        const busyThis = mutation.pending?.endsWith(`:${key}`);
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
                                      <Button size="sm" variant="danger-quiet" disabled={!!busyThis}>
                                        Forget
                                      </Button>
                                    }
                                  />
                                </div>
                              </div>

                              {reviewing && (
                                <MemoryFactForm
                                  className="mt-3 rounded-tile border border-hairline-strong bg-fill-1/40 p-3.5"
                                  banner={
                                    <Notice tone="warning">
                                      Task outcomes never enter Core Memory automatically. Review or re-author the text below to save it as an explicit memory.
                                    </Notice>
                                  }
                                  heading="Re-authored Core Memory text"
                                  text={promoteText}
                                  onTextChange={setPromoteText}
                                  textAriaLabel="Promote text to Core Memory"
                                  section={promoteSection}
                                  onSectionChange={setPromoteSection}
                                  sectionPlaceholder="Projects, Decisions…"
                                  about={promoteAbout}
                                  onAboutChange={setPromoteAbout}
                                  aboutPlaceholder="the user"
                                  pinned={promotePinned}
                                  onPinnedChange={setPromotePinned}
                                  pinnedLabel="Pin in Core Memory"
                                  extraFields={
                                    <label className="flex min-h-9 items-center gap-2 text-meta font-medium text-ink">
                                      <input
                                        type="checkbox"
                                        className="accent-[var(--accent)]"
                                        checked={promoteReviewed}
                                        onChange={(event) => setPromoteReviewed(event.target.checked)}
                                      />
                                      I have reviewed this fact and want Nori to keep it in Core Memory.
                                    </label>
                                  }
                                  submitLabel="Promote to Core"
                                  submittingLabel="Promoting…"
                                  submitting={mutation.pending === `promote:${key}`}
                                  submitDisabled={!reviewCheck?.ok || !!mutation.pending}
                                  onSubmit={() => void submitPromoteReview(log, entry)}
                                  onCancel={() => setReviewingKey(undefined)}
                                />
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
