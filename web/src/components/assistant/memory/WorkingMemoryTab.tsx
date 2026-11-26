"use client";

import Link from "next/link";
import { ArrowUpRight, Sparkles } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { mutate, newRequestID } from "@/lib/assistant/api";
import type { useResource } from "@/lib/assistant/useResource";
import type { WorkingMemory, WorkingMemoryItem, WorkingMemoryItemAction } from "@/lib/contracts/assistant";
import { Empty, Failure, Loading, Notice, Pill, Rows, Section, type Tone } from "../ui";
import { useVersionedMemoryMutation } from "./MemoryFactForm";
import { formatByteBudget, messageJumpHref, WORKING_KIND_META, workingMemoryWatermarks } from "./memoryHelpers";

export function WorkingMemoryTab({ working }: { working: ReturnType<typeof useResource<WorkingMemory>> }) {
  const mutation = useVersionedMemoryMutation();
  const data = working.data;

  const consolidate = () =>
    mutation.run(
      "consolidate",
      () =>
        mutate("/memory/actions", {
          request_id: newRequestID(),
          expected_revision: 0,
          value: { action: "consolidate" },
        }),
      "Memory consolidation requested.",
    );

  const itemAction = (item: WorkingMemoryItem, kind: WorkingMemoryItemAction["value"]["action"]) => {
    if (!data) return;
    return mutation.run(
      `${kind}:${item.id}`,
      () =>
        mutate(`/memory/working/${encodeURIComponent(item.id)}/actions`, {
          request_id: newRequestID(),
          expected_revision: data.revision,
          value: { action: kind },
        }),
      kind === "promote" ? "Promoted to Core Memory." : "Item dismissed from Working Memory.",
    );
  };

  if (working.loading && !data) return <Loading label="Loading Working Memory…" />;
  if (!data) return <Failure error={working.error ?? "Working memory unavailable."} onRetry={() => void working.reload()} />;

  const watermarks = workingMemoryWatermarks(data);

  return (
    <div className="space-y-6">
      {working.error && <Failure error={working.error} onRetry={() => void working.reload()} />}
      {mutation.error && <Notice tone="danger">{mutation.error}</Notice>}

      <Section
        title="Session gist"
        actions={
          <Button
            size="sm"
            variant="neutral"
            disabled={!!mutation.pending}
            onClick={() => void consolidate()}
          >
            <Sparkles size={14} aria-hidden />
            {mutation.pending === "consolidate" ? "Consolidating…" : "Consolidate now"}
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
              const busyThis = mutation.pending?.endsWith(`:${item.id}`);
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
