"use client";

import { useMemo } from "react";
import type { AssistantSummary, Interaction, LibraryItem, WorkDetail, WorkItem } from "@/lib/contracts/assistant";
import type { Run, SavedResult, Session } from "@/lib/types";
import { useAssistant } from "./context";
import { useResource } from "./useResource";

type List<T> = { items: T[]; next_cursor?: string };

const legacyTitle = (run: Run, session?: Session) =>
  run.request || session?.messages.find((message) => message.run_id === run.id && message.role === "user")?.text || "Task";

function legacyInteraction(run: Run): Interaction | undefined {
  const question = run.question;
  if (!question || run.status !== "running" || question.resolved_by || question.kind !== "input") return undefined;
  return {
    id: `legacy:${question.id}`, revision: question.instruction_revision, task_id: run.id, instruction_revision: question.instruction_revision,
    conversation_id: "assistant", kind: "question", status: "pending", prompt: question.text, payload: { input: "text" }, legacy_question_id: question.id,
  };
}

function legacyWorkItem(run: Run, session?: Session): WorkItem {
  const waiting = run.status === "running" && !!((run.question && !run.question.resolved_by) || run.waiting);
  return {
    task_id: run.id, title: legacyTitle(run, session), status: run.status,
    bucket: run.status !== "running" ? "finished" : waiting ? "needs_you" : "working",
    instruction_revision: run.revision ?? 0, origin: { kind: "conversation", conversation_id: "assistant" },
    interaction_ids: legacyInteraction(run) ? [`legacy:${run.question!.id}`] : [],
    progress: run.question?.text || run.waiting || run.progress || undefined,
    operations: { total: 0, uncertain: 0, failed: 0, submitted: 0 },
    result_ids: (session?.results ?? []).filter((result) => result.run_id === run.id).map((result) => result.id),
    cost: { status: run.spent_usd === undefined ? "unknown" : run.cost_unknown ? "unknown" : "known", amount_usd: run.spent_usd, budget_usd: run.budget_usd },
    parent_task_id: run.parent_id, started_at: run.started_at, finished_at: run.finished_at, cancel_requested: run.cancel_requested,
    ...(run.error ? { error: { code: run.status, message: run.error, retryable: false } } : {}),
  };
}

function legacyLibraryItem(result: SavedResult): LibraryItem {
  return {
    id: result.id, revision: 0, root_id: result.root_id, version: result.version, revises: result.revises, superseded_by: result.superseded_by,
    kind: result.kind, title: result.title, name: result.name, media_type: result.media_type, size: result.size,
    url: result.kind === "link" ? result.url : result.kind === "file" || result.kind === "screenshot" ? `/api/v1/results/${result.id}/file` : undefined,
    task_id: result.run_id, conversation_id: "assistant", provenance: { content: "generated" }, created_at: result.created_at,
  };
}

export function useWork() {
  const { has, runtime } = useAssistant();
  const live = has("work");
  const remote = useResource<List<WorkItem>>(live ? "/work" : null, { poll: 20_000 });
  const session = runtime.session;
  const legacy = useMemo(() => (session?.runs ?? []).map((run) => legacyWorkItem(run, session)), [session]);
  return live
    ? { items: remote.data?.items, error: remote.error, loading: remote.loading, reload: remote.reload, live: true }
    : { items: session ? legacy : undefined, error: runtime.error ? new Error(runtime.error) : undefined, loading: runtime.loading && !session, reload: runtime.refreshHistory, live: false };
}

export function useWorkDetail(taskID: string) {
  const { has, runtime } = useAssistant();
  const live = has("work");
  const remote = useResource<WorkDetail>(live ? `/work/${encodeURIComponent(taskID)}` : null, { poll: 5_000 });
  const session = runtime.session;
  const run = session?.runs.find((item) => item.id === taskID);
  const legacy = useMemo<(WorkDetail) | undefined>(() => run && {
    item: legacyWorkItem(run, session), operations: [],
    interactions: legacyInteraction(run) ? [legacyInteraction(run)!] : [],
    result: run.result, events: (run.events ?? []).map((event) => ({ kind: event.kind, text: event.text, created_at: event.created_at })),
  }, [run, session]);
  return live
    ? { detail: remote.data, error: remote.error, loading: remote.loading, reload: remote.reload, apply: remote.apply, live, run: undefined }
    : { detail: legacy, error: !session && runtime.error ? new Error(runtime.error) : undefined, loading: runtime.loading && !session, reload: runtime.refreshHistory, apply: undefined, live, run };
}

export function usePendingInteractions() {
  const { has, runtime } = useAssistant();
  const live = has("interactions");
  const remote = useResource<List<Interaction>>(live ? "/interactions?status=pending" : null, { poll: 20_000 });
  const runs = runtime.session?.runs;
  const legacy = useMemo(() => (runs ?? []).map(legacyInteraction).filter((item): item is Interaction => !!item), [runs]);
  return live ? { items: remote.data?.items, reload: remote.reload, live } : { items: runs ? legacy : undefined, reload: runtime.refreshHistory, live };
}

export function useLibrary(query = "") {
  const { has, runtime } = useAssistant();
  const live = has("library");
  const remote = useResource<List<LibraryItem>>(live ? `/library${query}` : null, { poll: 30_000 });
  const results = runtime.session?.results;
  const legacy = useMemo(() => (results ?? []).filter((result) => !result.superseded_by).map((result) => legacyLibraryItem(result)).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)), [results]);
  return live ? { items: remote.data?.items, error: remote.error, loading: remote.loading, reload: remote.reload, live } : { items: runtime.session ? legacy : undefined, error: undefined, loading: runtime.loading && !runtime.session, reload: runtime.refreshHistory, live };
}

export function useSummary(): Partial<AssistantSummary> {
  const { has, runtime } = useAssistant();
  const live = has("work") && has("interactions");
  const remote = useResource<AssistantSummary>(live ? "/assistant/summary" : null, { poll: 30_000, versioned: false });
  const runs = runtime.session?.runs;
  return useMemo(() => {
    if (live) return remote.data ?? {};
    const active = (runs ?? []).filter((run) => run.status === "running");
    return { needs_you: active.filter((run) => (run.question && !run.question.resolved_by) || run.waiting).length, working: active.length };
  }, [live, remote.data, runs]);
}
