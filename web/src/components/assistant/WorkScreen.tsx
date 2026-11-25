"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { mutate, newRequestID } from "@/lib/assistant/api";
import { AlertTriangle, ChevronDown, ChevronRight, Search, Square } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { useAssistant } from "@/lib/assistant/context";
import { cadence, money, relative, when } from "@/lib/assistant/format";
import { usePendingInteractions, useWork, useWorkDetail } from "@/lib/assistant/sources";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { Automation, Interaction, LibraryItem, OperationProjection, WorkItem } from "@/lib/contracts/assistant";
import { MAX_MESSAGE_BYTES, messageTooLong } from "@/lib/submission";
import { cn } from "@/lib/utils";
import { Markdown } from "@/components/chat/Markdown";
import { InteractionCard } from "./InteractionCard";
import { Empty, Failure, Field, inputBase, inputClass, Loading, Notice, NotYet, Pill, RefreshButton, Row, Rows, Screen, Section, Tabs, type Tone } from "./ui";

type Bucket = "needs_you" | "working" | "scheduled" | "finished";

export function statusPill(item: WorkItem): { tone: Tone; label: string } {
  if (item.cancel_requested && item.status === "running") return { tone: "neutral", label: "Stopping" };
  if (item.bucket === "needs_you") return { tone: "accent", label: "Needs you" };
  if (item.status === "running") return item.operations.uncertain ? { tone: "warning", label: "Checking" } : { tone: "neutral", label: "Working" };
  if (item.status === "completed") return item.operations.uncertain ? { tone: "warning", label: "Unconfirmed" } : { tone: "ok", label: "Done" };
  if (item.status === "failed") return { tone: "danger", label: "Failed" };
  return { tone: "neutral", label: "Stopped" };
}

export function WorkScreen() {
  const params = useSearchParams();
  const router = useRouter();
  const work = useWork();
  const pending = usePendingInteractions();
  const { has } = useAssistant();
  const automations = useResource<{ items: Automation[] }>(has("automations") ? "/automations?status=enabled" : null, { poll: 60_000 });
  const items = work.items;
  const groups = useMemo(() => ({
    needs_you: (items ?? []).filter((item) => item.bucket === "needs_you"),
    working: (items ?? []).filter((item) => item.bucket === "working"),
    finished: (items ?? []).filter((item) => item.bucket === "finished").sort((a, b) => Date.parse(b.finished_at ?? b.started_at) - Date.parse(a.finished_at ?? a.started_at)),
  }), [items]);
  const scheduled = (automations.data?.items ?? []).filter((item) => item.status === "enabled" && item.next_at).sort((a, b) => Date.parse(a.next_at!) - Date.parse(b.next_at!));
  const [chosen, setChosen] = useState<Bucket | undefined>(params.get("tab") as Bucket || undefined);
  const select = (next: Bucket) => { setChosen(next); router.replace(`/work?tab=${next}`, { scroll: false }); };
  const unlinked = (pending.items ?? []).filter((interaction) => !interaction.task_id || !(items ?? []).some((item) => item.task_id === interaction.task_id));
  const tab: Bucket = chosen ?? (groups.needs_you.length + unlinked.length ? "needs_you" : groups.working.length || !groups.finished.length ? "working" : "finished");

  return (
    <Screen title="Work" actions={<RefreshButton onClick={() => void work.reload()} />}>
      <Tabs label="Work" value={tab} onChange={select} items={[
        { id: "needs_you", label: "Needs you", count: groups.needs_you.length + unlinked.length },
        { id: "working", label: "Working", count: groups.working.length },
        { id: "scheduled", label: "Scheduled", count: scheduled.length },
        { id: "finished", label: "Finished" },
      ]} />
      {work.error && <div className="mb-4"><Failure error={work.error} onRetry={() => void work.reload()} /></div>}
      {work.loading ? <Loading /> : tab === "needs_you" ? <NeedsYou tasks={groups.needs_you} interactions={pending.items ?? []} unlinked={unlinked} />
        : tab === "working" ? (groups.working.length ? <WorkRows items={groups.working} /> : <Empty>Nothing is running. Ask Nori for something in Chat.</Empty>)
          : tab === "scheduled" ? (!has("automations") ? <NotYet what="Scheduled work" /> : scheduled.length ? <Rows label="Scheduled">{scheduled.map((item) => <Row key={item.id} href={`/routines/${encodeURIComponent(item.id)}`} title={item.title} detail={cadence(item.trigger)} trailing={<span className="text-meta text-ink-2">{relative(item.next_at)}</span>} />)}</Rows> : <Empty action={<Link href="/routines/new" className="text-meta font-medium text-accent-text hover:underline">New automation</Link>}>No automations are scheduled.</Empty>)
            : groups.finished.length ? <WorkRows items={groups.finished} /> : <Empty>Finished tasks appear here.</Empty>}
    </Screen>
  );
}

function NeedsYou({ tasks, interactions, unlinked }: { tasks: WorkItem[]; interactions: Interaction[]; unlinked: Interaction[] }) {
  if (!tasks.length && !unlinked.length) return <Empty>Nothing needs you right now.</Empty>;
  return <div className="space-y-6">
    {unlinked.map((interaction) => <InteractionCard key={interaction.id} interaction={interaction} />)}
    {tasks.map((task) => {
      const mine = interactions.filter((interaction) => interaction.task_id === task.task_id);
      return <section key={task.task_id} aria-label={task.title}>
        <Link href={`/work/${encodeURIComponent(task.task_id)}`} className="mb-2 flex items-baseline gap-2 hover:underline"><h2 className="text-section font-semibold">{task.title}</h2><span className="text-meta text-ink-3">{relative(task.started_at)}</span></Link>
        <div className="space-y-3">
          {mine.length ? mine.map((interaction) => <InteractionCard key={interaction.id} interaction={interaction} compact />) : <Notice action={<Link href={`/work/${encodeURIComponent(task.task_id)}`} className="font-medium text-accent-text hover:underline">Open</Link>}>{task.progress || "This task is waiting for you."}</Notice>}
        </div>
      </section>;
    })}
  </div>;
}

function WorkRows({ items }: { items: WorkItem[] }) {
  return <Rows label="Tasks">{items.map((item) => {
    const pill = statusPill(item);
    return <Row key={item.task_id} href={`/work/${encodeURIComponent(item.task_id)}`} title={item.title}
      detail={item.error?.message || item.progress || (item.finished_at ? `Finished ${relative(item.finished_at)}` : `Started ${relative(item.started_at)}`)}
      trailing={<>{item.operations.uncertain > 0 && <AlertTriangle size={15} className="text-warning" aria-label="Has an unconfirmed action" />}<Pill tone={pill.tone}>{pill.label}</Pill></>} />;
  })}</Rows>;
}

const OUTCOME: Record<string, { tone: Tone; label: string }> = {
  confirmed: { tone: "ok", label: "Confirmed" }, submitted: { tone: "neutral", label: "Sent, not yet confirmed" }, uncertain: { tone: "warning", label: "Unconfirmed" },
  failed: { tone: "danger", label: "Failed" }, attempted: { tone: "neutral", label: "In progress" }, pending: { tone: "neutral", label: "Waiting" }, ready: { tone: "neutral", label: "Ready" },
  skipped: { tone: "neutral", label: "Skipped" }, voided: { tone: "neutral", label: "Cancelled before running" },
};

/** Why an operation needs no further attention, in plain words. */
const CLEARED: Record<string, { tone: Tone; label: string }> = {
  did_not_run: { tone: "neutral", label: "Didn’t run" }, read_only: { tone: "neutral", label: "Only read" },
  answered: { tone: "neutral", label: "Settled by your answer" }, contained: { tone: "warning", label: "Plugin failed safely" },
};
const AUTHORITY: Record<string, string> = { user_request: "you asked", permission_rule: "a permission rule", automation: "an automation", approval: "your approval", internal: "Nori" };
const RECOVERY: Record<string, string> = { reconciling: "checking with the provider", needs_review: "needs your review", resolved: "resolved" };

/** Plain names for built-in steps that carry no action summary. */
const BUILTIN: Record<string, string> = {
  web_search: "Searched the web", upload_open: "Opened an attached file", shell: "Ran a command", job_start: "Started a long-running command",
  browser_navigate: "Opened a web page", browser_screenshot: "Took a screenshot", screenshot_inspect: "Looked at a screenshot",
};
/** A step's name in words: a plain name when known, else its tool name without underscores. */
const stepName = (name: string) => BUILTIN[name] ?? name.charAt(0).toUpperCase() + name.slice(1).replace(/_/g, " ");

/** Steps worth showing: checking which tools are ready is bookkeeping, not an action. */
export const shownOperations = <T extends { name: string }>(operations: T[]) => operations.filter((operation) => operation.name !== "capability_load");

export type OperationRowItem = OperationProjection & { recovery?: string; task_title?: string };

export type OperationDisplayGroup =
  | { kind: "single"; operation: OperationRowItem }
  | { kind: "search_group"; id: string; operations: OperationRowItem[] };

/** Reserve the green "Confirmed" badge for non-read external changes rather than routine reads. */
export function operationOutcomePill(operation: Pick<OperationProjection, "effect_class" | "outcome" | "cleared">): { tone: Tone; label: string } | undefined {
  if (operation.cleared && CLEARED[operation.cleared]) return CLEARED[operation.cleared];
  if (operation.effect_class === "read" && operation.outcome === "confirmed") return undefined;
  return OUTCOME[operation.outcome] ?? { tone: "neutral", label: operation.outcome };
}

/** Group consecutive web_search operations (2+) into a single expandable row while preserving chronological order. */
export function groupOperations(operations: OperationRowItem[]): OperationDisplayGroup[] {
  const groups: OperationDisplayGroup[] = [];
  let searchRun: OperationRowItem[] = [];

  const flushSearchRun = () => {
    if (searchRun.length === 0) return;
    if (searchRun.length === 1) {
      groups.push({ kind: "single", operation: searchRun[0] });
    } else {
      groups.push({ kind: "search_group", id: `search-group-${searchRun[0].id}`, operations: searchRun });
    }
    searchRun = [];
  };

  for (const operation of operations) {
    if (operation.name === "web_search") {
      searchRun.push(operation);
    } else {
      flushSearchRun();
      groups.push({ kind: "single", operation });
    }
  }
  flushSearchRun();
  return groups;
}

function operationDetail(operation: OperationRowItem): string {
  const showAuthority = operation.authority_source && !(operation.name === "web_search" && operation.authority_source === "internal");
  return [
    operation.account_summary,
    showAuthority && AUTHORITY[operation.authority_source!] && `because ${AUTHORITY[operation.authority_source!]}`,
    operation.recovery && RECOVERY[operation.recovery],
    operation.created_at && relative(operation.created_at),
  ].filter(Boolean).join(" · ");
}

function SearchGroupRow({ operations }: { operations: OperationRowItem[] }) {
  const [expanded, setExpanded] = useState(false);
  const failedCount = operations.filter((op) => op.outcome === "failed").length;
  const activeCount = operations.filter((op) => op.outcome === "attempted" || op.outcome === "pending").length;
  const latestTimestamp = operations.map((op) => op.created_at).filter(Boolean).sort().at(-1);
  const badge = failedCount > 0
    ? { tone: "danger" as Tone, label: failedCount === operations.length ? "Failed" : `${failedCount} failed` }
    : activeCount > 0
      ? { tone: "neutral" as Tone, label: "In progress" }
      : undefined;

  return (
    <li data-search-group>
      <button
        type="button"
        aria-expanded={expanded}
        onClick={() => setExpanded((open) => !open)}
        className="flex min-h-14 w-full items-center gap-3 px-4 py-3 text-left hover:bg-fill-1 focus-visible:bg-fill-1"
      >
        <span className="shrink-0 text-ink-3" aria-hidden><Search size={16} /></span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-row font-medium text-ink">
            Searched the web ({operations.length} {operations.length === 1 ? "query" : "queries"})
          </span>
          {latestTimestamp && <span className="mt-0.5 block text-meta text-ink-2">{relative(latestTimestamp)}</span>}
        </span>
        {badge && <Pill tone={badge.tone}>{badge.label}</Pill>}
        <span className="shrink-0 text-ink-3" aria-hidden>
          {expanded ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
        </span>
      </button>
      {expanded && (
        <ul className="divide-y divide-hairline border-t border-hairline bg-fill-1/40 pl-10" aria-label="Web search queries">
          {operations.map((operation) => {
            const outcome = operationOutcomePill(operation);
            const detail = operationDetail(operation);
            return (
              <li key={operation.id} className="flex min-h-11 items-center gap-3 px-4 py-2.5">
                <span className="min-w-0 flex-1">
                  <span className="block text-meta font-medium text-ink [overflow-wrap:anywhere]">{operation.action_summary || stepName(operation.name)}</span>
                  {detail && <span className="mt-0.5 block text-caption text-ink-3">{detail}</span>}
                </span>
                {outcome && <Pill tone={outcome.tone}>{outcome.label}</Pill>}
              </li>
            );
          })}
        </ul>
      )}
    </li>
  );
}

export function OperationRows({ operations }: { operations: OperationRowItem[] }) {
  const grouped = useMemo(() => groupOperations(operations), [operations]);
  return <Rows label="Actions">{grouped.map((entry) => {
    if (entry.kind === "search_group") {
      return <SearchGroupRow key={entry.id} operations={entry.operations} />;
    }
    const operation = entry.operation;
    const outcome = operationOutcomePill(operation);
    return <Row key={operation.id} title={operation.action_summary || stepName(operation.name)}
      detail={operationDetail(operation)}
      trailing={<>{operation.receipt_url && /^https:\/\//.test(operation.receipt_url) && <a href={operation.receipt_url} target="_blank" rel="noreferrer noopener" className="text-meta font-medium text-accent-text hover:underline">Open</a>}{outcome && <Pill tone={outcome.tone}>{outcome.label}</Pill>}</>} />;
  })}</Rows>;
}

const SPENDING_LIMIT = /spending limit/i;

/** A task that ended before finishing can be continued as a new task; saved results carry over and completed actions aren't repeated. */
function Continue({ item }: { item: WorkItem }) {
  const router = useRouter();
  const action = useAction();
  const limited = SPENDING_LIMIT.test(item.error?.message ?? "");
  const [budget, setBudget] = useState(String(Math.max(1, (item.cost.budget_usd ?? 1) * 2)));
  const go = () => action.run("continue", async () => {
    const receipt = await mutate(`/work/${encodeURIComponent(item.task_id)}/actions`, { request_id: newRequestID(), expected_revision: 0, value: { action: "continue", ...(limited && Number(budget) > 0 ? { budget_usd: Number(budget) } : {}) } });
    invalidate("/work", "/assistant/summary");
    if (receipt.status === "queued" && receipt.work?.task_id) router.push(`/work/${encodeURIComponent(receipt.work.task_id)}`);
  }, "Continuing in a new task.");
  return <Section title="Continue">
    <form className="flex flex-wrap items-end gap-3" onSubmit={(event) => { event.preventDefault(); void go(); }}>
      {limited && <Field label="New spending limit (USD)"><input type="number" min={0.01} step="any" value={budget} onChange={(event) => setBudget(event.target.value)} className={cn(inputBase, "w-32")} /></Field>}
      <Button type="submit" variant="primary" disabled={!!action.pending || (limited && !(Number(budget) > 0))}>{action.pending ? "Starting…" : "Continue this task"}</Button>
    </form>
    {action.error && <div className="mt-2"><Notice tone="danger">{action.error}</Notice></div>}
  </Section>;
}

export function WorkDetailScreen({ taskID }: { taskID: string }) {
  // Back from signing in for this task (the server returns here with ?result=…).
  const signInResult = useSearchParams().get("result");
  const { runtime, has } = useAssistant();
  const { detail, error, loading, reload, live } = useWorkDetail(taskID);
  const work = useWork();
  // The reverse of origin.continues_task_id: later tasks that continue this one.
  const continuedBy = (work.items ?? []).filter((other) => other.origin.continues_task_id === taskID);
  const item = detail?.item;
  const results = useResource<{ items: LibraryItem[] }>(has("library") && item?.result_ids.length ? `/library?task_id=${encodeURIComponent(taskID)}` : null, { poll: 0 });
  // The list is filtered here as well: the server may return more than this task saved.
  const saved = (results.data?.items ?? []).filter((result) => item?.result_ids.includes(result.id));
  const commands = runtime.taskCommands.filter((command) => command.command.run_id === taskID);
  const [draft, setDraft] = useState("");
  // A task opened right after it starts (Run now, a chat hand-off) can take a moment to appear in Work.
  const [retries, setRetries] = useState(0);
  const settling = !item && !!error && retries < 12;
  useEffect(() => {
    if (!settling) return;
    const timer = setTimeout(() => { setRetries((count) => count + 1); void reload(); }, 1_500);
    return () => clearTimeout(timer);
  }, [settling, retries, reload]);
  if (loading || settling) return <Screen title="Task" back="/work"><Loading /></Screen>;
  if (!item) return <Screen title="Task" back="/work">{error ? <Failure error={error} onRetry={() => void reload()} /> : <Empty action={<Link href="/work" className="text-meta font-medium text-accent-text hover:underline">Back to Work</Link>}>This task isn’t in your recent work.</Empty>}</Screen>;
  const pill = statusPill(item);
  const running = item.status === "running" && !item.cancel_requested;
  const stopping = commands.some((command) => command.command.kind === "stop") || item.cancel_requested;
  const pendingInteractions = detail.interactions.filter((interaction) => interaction.status === "pending");
  // Tool calls already appear under Actions with their summaries; Activity keeps the rest.
  const toolNames = new Set(detail.operations.map((operation) => operation.name));
  const activity = (detail.events ?? []).filter((event) => !toolNames.has(event.text) && event.text !== "capability_load");
  const tooLong = messageTooLong(draft.trim());
  const sending = commands.some((command) => command.command.kind === "steer" && !command.error);
  const line = item.status === "running" && item.cancel_requested ? "Stopping." : item.status === "running" ? (pendingInteractions.length ? undefined : item.progress || "Working on it.") : item.status === "completed" ? (item.operations.uncertain ? "Finished, but one action isn’t confirmed yet." : "Done.") : item.status === "failed" ? item.error?.message || "This task failed." : "Stopped.";

  return (
    <Screen title={item.title} back="/work" line={line} actions={<><Pill tone={pill.tone}>{pill.label}</Pill><RefreshButton onClick={() => void reload()} /></>}>
      {signInResult && <div className="mb-6"><Notice tone={signInResult === "connected" ? "ok" : signInResult === "cancelled" ? "neutral" : "danger"}>{signInResult === "connected" ? "Signed in. Nori is continuing this task." : signInResult === "cancelled" ? "Sign-in was cancelled. The task is still waiting." : "Sign-in didn’t finish. Try again from the card below."}</Notice></div>}
      {pendingInteractions.length > 0 && <Section title="Needs you"><div className="space-y-3">{pendingInteractions.map((interaction) => <InteractionCard key={interaction.id} interaction={interaction} compact />)}</div></Section>}
      {item.operations.uncertain > 0 && <div className="mt-6"><Notice tone="warning">An action may or may not have gone through. Nori is checking with the provider and won’t repeat it.</Notice></div>}
      {running && <Section title="Change this task">
        <form className="space-y-2" onSubmit={(event) => { event.preventDefault(); const text = draft.trim(); if (!text || tooLong) return; void runtime.commandTask(taskID, "steer", text).then(() => setDraft("")); }}>
          <textarea aria-label="Instruction for this task" value={draft} onChange={(event) => setDraft(event.target.value)} rows={2} maxLength={MAX_MESSAGE_BYTES} className={cn(inputClass, "resize-y")} placeholder="Also check Monday, use the work calendar…" />
          {tooLong && <p role="alert" className="text-meta text-danger">Keep this within 8,192 bytes.</p>}
          <div className="flex flex-wrap gap-2">
            <Button type="submit" variant="primary" disabled={!draft.trim() || tooLong || sending}>{sending ? "Sending…" : "Send to this task"}</Button>
            <Button disabled={stopping} onClick={() => void runtime.commandTask(taskID, "stop")}><Square size={13} fill="currentColor" aria-hidden />{stopping ? "Stopping…" : "Stop task"}</Button>
          </div>
        </form>
        {commands.filter((command) => command.error).map((command) => <div key={command.command.id} className="mt-2"><Notice tone="danger" action={command.rejected ? <Button size="sm" onClick={() => runtime.dismissTaskCommand(command.command.id)}>Dismiss</Button> : <Button size="sm" onClick={() => void runtime.retryTaskCommand(command)}>Retry</Button>}>{command.error}</Notice></div>)}
      </Section>}
      {item.status === "running" && item.cancel_requested && <div className="mt-6"><Notice>Stopping. Work already done stays done; anything unconfirmed stays marked.</Notice></div>}
      {live && item.status === "failed" && !continuedBy.length && <Continue item={item} />}
      {detail.result && <Section title={item.status === "failed" ? "Partial answer" : "Result"}><div className="rounded-card border border-hairline bg-surface p-4 text-body [overflow-wrap:anywhere]"><Markdown variant="page">{detail.result}</Markdown></div></Section>}
      {saved.length > 0 && <Section title="Saved"><Rows>{saved.map((result) => <Row key={result.id} href={`/library/${encodeURIComponent(result.id)}`} title={result.title} detail={result.version > 1 ? `Version ${result.version}` : undefined} />)}</Rows></Section>}
      {!has("library") && item.result_ids.length > 0 && <Section title="Saved"><Rows>{item.result_ids.map((id) => { const saved = runtime.session?.results?.find((result) => result.id === id); return <Row key={id} href={`/library/${encodeURIComponent(id)}`} title={saved?.title ?? "Saved result"} />; })}</Rows></Section>}
      {shownOperations(detail.operations).length > 0 && <Section title="Actions"><OperationRows operations={shownOperations(detail.operations)} /></Section>}
      {activity.length > 0 && <Section title="Activity">
        <ol className="space-y-3 border-l border-hairline pl-4">{activity.map((event, index) => <li key={`${event.created_at}-${index}`} className="relative">
          <span aria-hidden className="absolute top-1.5 -left-[21px] size-2.5 rounded-full border-2 border-surface bg-hairline-strong" />
          <p className="text-meta text-ink-3">{when(event.created_at)}</p>
          <p className="text-body text-ink [overflow-wrap:anywhere]">{event.text}</p>
        </li>)}</ol>
      </Section>}
      <Section title="Details">
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
          <dt className="text-ink-3">Started</dt><dd>{when(item.started_at)}</dd>
          {item.finished_at && <><dt className="text-ink-3">Finished</dt><dd>{when(item.finished_at)}</dd></>}
          <dt className="text-ink-3">From</dt><dd>{item.origin.kind === "automation" && item.origin.automation_id ? <Link className="text-accent-text hover:underline" href={`/routines/${encodeURIComponent(item.origin.automation_id)}`}>An automation</Link> : item.origin.kind === "goal" && item.origin.goal_id ? <Link className="text-accent-text hover:underline" href={`/goals/${encodeURIComponent(item.origin.goal_id)}`}>A goal</Link> : item.origin.kind === "external" ? "Another app" : <Link className="text-accent-text hover:underline" href="/">Chat</Link>}</dd>
          {continuedBy.length > 0 && <><dt className="text-ink-3">Continued by</dt><dd className="flex flex-col">{continuedBy.map((other) => <Link key={other.task_id} className="text-accent-text hover:underline" href={`/work/${encodeURIComponent(other.task_id)}`}>{other.status === "running" ? "A task in progress" : `A later task (${statusPill(other).label.toLowerCase()})`}</Link>)}</dd></>}
          {item.origin.continues_task_id && <><dt className="text-ink-3">Continues</dt><dd><Link className="text-accent-text hover:underline" href={`/work/${encodeURIComponent(item.origin.continues_task_id)}`}>The earlier task</Link></dd></>}
          <dt className="text-ink-3">Cost</dt><dd>{[item.cost.status === "known" ? money(item.cost.amount_usd) : item.cost.status === "unknown" ? (item.cost.amount_usd ? `At least ${money(item.cost.amount_usd)}` : "Not known yet") : "None", item.cost.budget_usd ? `budget ${money(item.cost.budget_usd)}` : ""].filter(Boolean).join(" · ")}</dd>
          {item.parent_task_id && <><dt className="text-ink-3">Part of</dt><dd><Link className="text-accent-text hover:underline" href={`/work/${encodeURIComponent(item.parent_task_id)}`}>Parent task</Link></dd></>}
        </dl>
      </Section>
    </Screen>
  );
}
