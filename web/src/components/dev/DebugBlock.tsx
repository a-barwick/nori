"use client";

import { Fragment, useEffect, useState, type ReactNode } from "react";
import { clock } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { ConversationTrace, TaskStep, Usage } from "@/lib/types";
import type { DebugInfo } from "@/lib/types";

const n = (value: number) => value.toLocaleString("en-US");
const secs = (ms: number | undefined) => ms === undefined ? undefined : `${(ms / 1000).toFixed(1)} s`;
const usd = (value: number) => `$${value.toFixed(4)}`;
const ROW = "grid grid-cols-[8px_5ch_minmax(0,1fr)_auto] items-center gap-x-[1ch] px-1";
const SUMMARY = cn(ROW, "cursor-pointer list-none rounded-[3px] hover:bg-fill-1 [&::-webkit-details-marker]:hidden");
const DETAIL = "mb-1 ml-[calc(8px+5ch+2ch+4px)] border-l border-hairline py-0.5 pl-[1ch]";

/** One reply's recorded outer attempts and, on request, the linked task's steps. */
export function DebugBlock({ info }: { info: DebugInfo }) {
  const [expanded, setExpanded] = useState(false);
  const [steps, setSteps] = useState<TaskStep[]>();
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const run = info.run;
  const runId = run?.id;
  const runStatus = run?.status;
  const runChanges = `${run?.events?.length}:${run?.progress}:${run?.spent_usd}`;
  useEffect(() => {
    if (!expanded || !runId) return;
    const controller = new AbortController();
    async function load() {
      try {
        const response = await fetch(`${info.agentMode ? "/api/agent" : "/api"}/trace`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ run_id: runId }), cache: "no-store", signal: controller.signal,
        });
        const data = await response.json();
        if (!response.ok) throw new Error(data.error || "Task trace unavailable.");
        if (!controller.signal.aborted) { setSteps(data.steps ?? []); setError(""); }
      } catch (err) { if (!controller.signal.aborted) setError(err instanceof Error ? err.message : "Task trace unavailable."); }
    }
    void load();
    return () => controller.abort();
    // A saved change to the task reaches this component through the stream, and that is when the trace can have changed.
  }, [expanded, runId, runStatus, runChanges, info.agentMode, retry]);

  return (
    <div className="mt-2 w-full max-w-[640px] rounded-tile border border-hairline bg-surface/70 px-1.5 py-1.5 font-mono text-mono-label leading-[18px] tracking-normal text-ink-2 tabular-nums">
      {info.turns?.length ? info.turns.map((turn) => <TurnRow key={turn.id} turn={turn} />) : info.turnId || !run ? <div className="px-1 text-ink-3">Outer trace not recorded.</div> : null}
      {run && <details onToggle={(event) => setExpanded(event.currentTarget.open)}>
        <summary className={cn(SUMMARY, "mt-1 border-t border-hairline pt-1")}>
          <Chevron /><span>task</span>
          <span className="min-w-0 truncate"><Status status={run.status} /> <span title={run.id}>{run.id}</span>{run.revision !== undefined && <> · rev {run.revision}</>}</span>
          <span>{run.spent_usd !== undefined ? usd(run.spent_usd + (run.subtask_spent_usd ?? 0)) : "cost unknown"}{run.cost_unknown && run.spent_usd !== undefined && " + unknown"}</span>
        </summary>
        <div className={DETAIL}>
          <KV rows={[
            ["started", clock(Date.parse(run.started_at))],
            ...(run.position !== undefined ? [["commands", `${run.incorporated ?? 0} applied / ${run.position} accepted`] as [string, ReactNode]] : []),
            ...(run.finished_at ? [["finished", clock(Date.parse(run.finished_at))] as [string, ReactNode]] : []),
            ...(run.budget_usd !== undefined ? [["budget", usd(run.budget_usd)] as [string, ReactNode]] : []),
          ]} />
          {error ? <p role="status" className="text-danger">{error} <button className="text-accent-text hover:underline" onClick={() => setRetry((current) => current + 1)}>Retry trace</button></p>
            : steps === undefined ? <p>Loading recorded steps…</p>
            : steps.length ? <ol>{steps.map((step) => <li key={step.id}><StepRow step={step} /></li>)}</ol>
            : <p className="text-ink-3">Detailed steps not recorded.</p>}
          {!!run.events?.length && <details><summary className="cursor-pointer text-ink-3">Task events</summary><ol>{run.events.map((event, index) => <li key={index} className="flex gap-2"><span>{clock(Date.parse(event.created_at))}</span><span>{event.kind}: {event.text}</span></li>)}</ol></details>}
        </div>
      </details>}
    </div>
  );
}

function TurnRow({ turn }: { turn: ConversationTrace }) {
  const hint = turn.classification_hint;
  const usage = turn.usage ?? [];
  const knownCosts = usage.filter((item) => item.cost_usd !== undefined);
  const unknownCost = !usage.length || knownCosts.length < usage.length;
  return <details>
    <summary className={SUMMARY}>
      <Chevron /><span>outer</span>
      <span className="min-w-0 truncate"><Status status={turn.status} /> · attempt {turn.attempt} · {turn.model || "model not recorded"}</span>
      <span>{secs(turn.duration_ms)}</span>
    </summary>
    <div className={DETAIL}>
      {turn.context_truncated && <p className="mb-1 text-warning">The model used a limited portion of the conversation{turn.context_turns !== undefined ? ` (${n(turn.context_turns)} recent turns${turn.context_bytes !== undefined ? `, ${n(turn.context_bytes)} bytes` : ""})` : ""}. Older messages remain in history.</p>}
      <KV rows={[
        ["turn", turn.turn_id],
        ["inputs", turn.input_ids.join(", ")],
        ["Jev hint", hint ? `${hint.available ? "available" : "unavailable"}${hint.intents?.length ? ` · ${hint.intents.join(" + ")}` : hint.intent ? ` · ${hint.intent}` : ""}${hint.confidence !== undefined ? ` · ${hint.confidence.toFixed(2)}` : ""}` : "not recorded"],
        ["cost", `${knownCosts.length ? usd(knownCosts.reduce((sum, item) => sum + item.cost_usd!, 0)) : ""}${unknownCost ? knownCosts.length ? " + unknown" : "unknown" : ""}`],
        ...(turn.error ? [["error", turn.error] as [string, ReactNode]] : []),
      ]} />
      {usage.map((item) => <UsageRow key={item.id} usage={item} />)}
      {turn.actions?.map((action) => <details key={action.id} className="mt-1">
        <summary className="cursor-pointer"><Status status={action.status} /> dispatch · {n(action.bytes)} bytes · {action.id}</summary>
        <Code>{action.request}</Code>{action.error && <p className="text-danger">{action.error}</p>}
      </details>)}
    </div>
  </details>;
}

function UsageRow({ usage }: { usage: Usage }) {
  return <div className="mt-1 break-words text-ink-3">
    {usage.provider} · {usage.model} · {usage.request_status}
    {usage.input_tokens !== undefined && <> · {n(usage.input_tokens)} input</>}
    {usage.output_tokens !== undefined && <> · {n(usage.output_tokens)} output</>}
    {usage.total_tokens !== undefined && <> · {n(usage.total_tokens)} total</>}
    {usage.cost_usd !== undefined && <> · {usd(usage.cost_usd)} ({usage.cost_status})</>}
  </div>;
}

function StepRow({ step }: { step: TaskStep }) {
  return <details className="mt-1">
    <summary className={SUMMARY}>
      <Chevron /><span>{step.kind}</span><span className="min-w-0 truncate"><Status status={step.status} /> · {step.model || step.name || step.id}</span><span>{secs(step.duration_ms)}</span>
    </summary>
    <div className={DETAIL}>
      <KV rows={[
        ["id", step.id], ["revision", step.revision],
        ["started", clock(Date.parse(step.started_at))],
        ...(step.input_tokens !== undefined ? [["input", n(step.input_tokens)] as [string, ReactNode]] : []),
        ...(step.cached_tokens !== undefined ? [["cached", n(step.cached_tokens)] as [string, ReactNode]] : []),
        ...(step.output_tokens !== undefined ? [["output", n(step.output_tokens)] as [string, ReactNode]] : []),
        ...(step.thinking_tokens !== undefined ? [["thinking tokens", n(step.thinking_tokens)] as [string, ReactNode]] : []),
        ["cost", step.cost_usd !== undefined ? `${usd(step.cost_usd)} (${step.cost_status})` : step.cost_status === "not_applicable" ? "not applicable" : step.cost_status === "not_sent" ? "not sent" : "unknown"],
      ]} />
      {step.calls?.map((call, index) => <div key={index}>{call.name}<Code>{call.arguments}</Code></div>)}
      {step.arguments && <><span>Arguments</span><Code>{step.arguments}</Code></>}
      {step.result && <><span>Result</span><Code>{step.result}</Code></>}
      {step.error && <p className="text-danger">{step.error}</p>}
    </div>
  </details>;
}

function Status({ status }: { status: string }) {
  return <span className={/failed|error|rejected/.test(status) ? "text-danger" : /superseded|unknown/.test(status) ? "text-warning" : /completed|accepted/.test(status) ? "text-accent-signal" : "text-ink"}>{status}</span>;
}
function Chevron() { return <span className="text-center text-[8px] text-ink-3">▶</span>; }
function KV({ rows }: { rows: [string, ReactNode][] }) {
  return <div className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-[2ch] gap-y-0.5">{rows.map(([key, value], index) => <Fragment key={index}><span className="text-ink-3">{key}</span><div className="min-w-0 break-words [overflow-wrap:anywhere]">{value}</div></Fragment>)}</div>;
}
function Code({ children }: { children: string }) {
  return <pre className="my-0.5 max-h-64 overflow-auto rounded-[3px] bg-fill-1 px-[1ch] whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{typeof children === "string" ? children : JSON.stringify(children, null, 2)}</pre>;
}
