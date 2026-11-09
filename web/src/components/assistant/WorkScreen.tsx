"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useMemo, useState } from "react";
import { mutate, newRequestID } from "@/lib/assistant/api";
import { AlertTriangle, Square } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { useAssistant } from "@/lib/assistant/context";
import { cadence, money, plural, relative, when } from "@/lib/assistant/format";
import { usePendingInteractions, useWork, useWorkDetail } from "@/lib/assistant/sources";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { Automation, ComputerSession, Interaction, LibraryItem, OperationProjection, WorkItem } from "@/lib/contracts/assistant";
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
  const line = !items ? undefined : groups.needs_you.length + unlinked.length
    ? `${plural(groups.needs_you.length + unlinked.length, "thing needs", "things need")} you.`
    : groups.working.length ? `Working on ${plural(groups.working.length, "task")}.` : "Nothing needs you right now.";

  return (
    <Screen title="Work" line={line} actions={<RefreshButton onClick={() => void work.reload()} />}>
      <ComputerBanner />
      <Tabs label="Work" value={tab} onChange={select} items={[
        { id: "needs_you", label: "Needs you", count: groups.needs_you.length + unlinked.length },
        { id: "working", label: "Working", count: groups.working.length },
        { id: "scheduled", label: "Scheduled", count: scheduled.length },
        { id: "finished", label: "Finished" },
      ]} />
      {work.error && <div className="mb-4"><Failure error={work.error} onRetry={() => void work.reload()} /></div>}
      {work.loading ? <Loading /> : tab === "needs_you" ? <NeedsYou tasks={groups.needs_you} interactions={pending.items ?? []} unlinked={unlinked} />
        : tab === "working" ? (groups.working.length ? <WorkRows items={groups.working} /> : <Empty>Nothing is running. Ask Nori for something in Chat.</Empty>)
          : tab === "scheduled" ? (!has("automations") ? <NotYet what="Scheduled work" /> : scheduled.length ? <Rows label="Scheduled">{scheduled.map((item) => <Row key={item.id} href={`/routines/${encodeURIComponent(item.id)}`} title={item.title} detail={cadence(item.trigger)} trailing={<span className="text-meta text-ink-2">{relative(item.next_at)}</span>} />)}</Rows> : <Empty action={<Link href="/routines/new" className="text-meta font-medium text-accent-text hover:underline">New routine</Link>}>No routines are scheduled.</Empty>)
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

function ComputerBanner() {
  const { has } = useAssistant();
  const { data } = useResource<{ items: ComputerSession[] }>(has("computer_sessions") ? "/computer/sessions" : null, { poll: 10_000 });
  const session = data?.items.find((item) => item.controller === "user" || item.blocked_reason);
  if (!session) return null;
  return <div className="mb-5"><Notice tone="accent" action={<Link href={`/computer?session=${encodeURIComponent(session.id)}`} className="font-medium hover:underline">Open computer</Link>}>
    {session.controller === "user" ? "You have the computer." : "The computer needs you."}{session.task_title ? ` ${session.task_title}.` : ""}
  </Notice></div>;
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
const AUTHORITY: Record<string, string> = { user_request: "you asked", permission_rule: "a permission rule", automation: "a routine", approval: "your approval", internal: "Nori" };
const RECOVERY: Record<string, string> = { reconciling: "checking with the provider", needs_review: "needs your review", resolved: "resolved" };

/** Plain names for built-in steps that carry no action summary. */
const BUILTIN: Record<string, string> = { capability_load: "Checked which apps and tools are ready" };

export function OperationRows({ operations }: { operations: (OperationProjection & { recovery?: string; task_title?: string })[] }) {
  return <Rows label="Actions">{operations.map((operation) => {
    const outcome = (operation.cleared && CLEARED[operation.cleared]) || OUTCOME[operation.outcome] || { tone: "neutral" as Tone, label: operation.outcome };
    return <Row key={operation.id} title={operation.action_summary || BUILTIN[operation.name] || operation.name}
      detail={[operation.account_summary, operation.authority_source && `because ${AUTHORITY[operation.authority_source]}`, operation.recovery && RECOVERY[operation.recovery], operation.created_at && relative(operation.created_at)].filter(Boolean).join(" · ")}
      trailing={<>{operation.receipt_url && /^https:\/\//.test(operation.receipt_url) && <a href={operation.receipt_url} target="_blank" rel="noreferrer noopener" className="text-meta font-medium text-accent-text hover:underline">Open</a>}<Pill tone={outcome.tone}>{outcome.label}</Pill></>} />;
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
  if (loading) return <Screen title="Task" back="/work"><Loading /></Screen>;
  if (!item) return <Screen title="Task" back="/work">{error ? <Failure error={error} onRetry={() => void reload()} /> : <Empty action={<Link href="/work" className="text-meta font-medium text-accent-text hover:underline">Back to Work</Link>}>This task isn’t in your recent work.</Empty>}</Screen>;
  const pill = statusPill(item);
  const running = item.status === "running" && !item.cancel_requested;
  const stopping = commands.some((command) => command.command.kind === "stop") || item.cancel_requested;
  const pendingInteractions = detail.interactions.filter((interaction) => interaction.status === "pending");
  const tooLong = messageTooLong(draft.trim());
  const sending = commands.some((command) => command.command.kind === "steer" && !command.error);
  const line = item.status === "running" && item.cancel_requested ? "Stopping." : item.status === "running" ? item.progress || "Working on it." : item.status === "completed" ? (item.operations.uncertain ? "Finished, but one action isn’t confirmed yet." : "Done.") : item.status === "failed" ? item.error?.message || "This task failed." : "Stopped.";

  return (
    <Screen title={item.title} back="/work" line={line} actions={<><Pill tone={pill.tone}>{pill.label}</Pill><RefreshButton onClick={() => void reload()} /></>}>
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
      {detail.operations.length > 0 && <Section title="Actions"><OperationRows operations={detail.operations} /></Section>}
      {!!detail.events?.length && <Section title="Activity">
        <ol className="space-y-3 border-l border-hairline pl-4">{detail.events.map((event, index) => <li key={`${event.created_at}-${index}`} className="relative">
          <span aria-hidden className="absolute top-1.5 -left-[21px] size-2.5 rounded-full border-2 border-surface bg-hairline-strong" />
          <p className="text-meta text-ink-3">{when(event.created_at)}</p>
          <p className="text-body text-ink [overflow-wrap:anywhere]">{event.text}</p>
        </li>)}</ol>
      </Section>}
      <Section title="Details">
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
          <dt className="text-ink-3">Started</dt><dd>{when(item.started_at)}</dd>
          {item.finished_at && <><dt className="text-ink-3">Finished</dt><dd>{when(item.finished_at)}</dd></>}
          <dt className="text-ink-3">From</dt><dd>{item.origin.kind === "automation" && item.origin.automation_id ? <Link className="text-accent-text hover:underline" href={`/routines/${encodeURIComponent(item.origin.automation_id)}`}>A routine</Link> : item.origin.kind === "goal" && item.origin.goal_id ? <Link className="text-accent-text hover:underline" href={`/goals/${encodeURIComponent(item.origin.goal_id)}`}>A goal</Link> : item.origin.kind === "external" ? "Another app" : <Link className="text-accent-text hover:underline" href={item.origin.conversation_id && item.origin.conversation_id !== "assistant" ? `/?chat=${encodeURIComponent(item.origin.conversation_id)}` : "/"}>Chat</Link>}</dd>
          {continuedBy.length > 0 && <><dt className="text-ink-3">Continued by</dt><dd className="flex flex-col">{continuedBy.map((other) => <Link key={other.task_id} className="text-accent-text hover:underline" href={`/work/${encodeURIComponent(other.task_id)}`}>{other.status === "running" ? "A task in progress" : `A later task (${statusPill(other).label.toLowerCase()})`}</Link>)}</dd></>}
          {item.origin.continues_task_id && <><dt className="text-ink-3">Continues</dt><dd><Link className="text-accent-text hover:underline" href={`/work/${encodeURIComponent(item.origin.continues_task_id)}`}>The earlier task</Link></dd></>}
          <dt className="text-ink-3">Cost</dt><dd>{[item.cost.status === "known" ? money(item.cost.amount_usd) : item.cost.status === "unknown" ? (item.cost.amount_usd ? `At least ${money(item.cost.amount_usd)}` : "Not known yet") : "None", item.cost.budget_usd ? `budget ${money(item.cost.budget_usd)}` : ""].filter(Boolean).join(" · ")}</dd>
          {item.parent_task_id && <><dt className="text-ink-3">Part of</dt><dd><Link className="text-accent-text hover:underline" href={`/work/${encodeURIComponent(item.parent_task_id)}`}>Parent task</Link></dd></>}
        </dl>
      </Section>
    </Screen>
  );
}
