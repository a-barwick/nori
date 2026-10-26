"use client";

import { Fragment, type ReactNode } from "react";
import { clock } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { RunEvent } from "@/lib/poc/types";
import type { Classification, DebugInfo, DebugStep, OuterAction, OuterTurn } from "./debug";

const n = (v: number) => v.toLocaleString("en-US");
const secs = (ms: number) => `${(ms / 1000).toFixed(1)} s`;

// Every row is one line on the same grid: chevron | kind | id | summary | metrics.
// Hue means status only (ok / warn / failed). Kind is carried by the kind column,
// brightness (model rows ink, tool rows ink-3) and indentation (tools sit under their model call).
const ROW = "grid grid-cols-[8px_5ch_6ch_minmax(0,1fr)_auto] items-center gap-x-[1ch]";
const SUMMARY = cn(ROW, "cursor-pointer list-none rounded-[3px] px-1 hover:bg-fill-1 [&::-webkit-details-marker]:hidden");
// Detail lines start under the summary column: chevron + kind + id + three gaps.
const DETAIL = "mb-1 ml-[calc(8px+11ch+3ch+4px)] border-l border-hairline pl-[1ch] py-0.5";

// Fixed metric columns so numbers line up down the block.
const COLS = [
  { key: "in", w: "w-[5ch]" },
  { key: "cached", w: "w-[6ch]" },
  { key: "out", w: "w-[4ch]" },
  { key: "think", w: "w-[5ch]" },
  { key: "time", w: "w-[5ch]" },
] as const;

type Tone = "ok" | "warn" | "bad" | "idle";
const TONE: Record<Tone, string> = { ok: "text-accent-signal", warn: "text-warning", bad: "text-danger", idle: "text-ink-3" };

/** Shown under a Nori message when debug mode is on. Unrecorded fields say so. */
export function DebugBlock({ info }: { info: DebugInfo }) {
  const failed = info.status === "failed";
  const usd = (v: number) => `$${v.toFixed(4)}`;
  return (
    <div className="mt-2 w-full max-w-[640px] rounded-tile border border-hairline bg-surface/70 px-1.5 py-1.5 font-mono text-mono-label leading-[18px] tracking-normal text-ink-2 tabular-nums">
      {info.classification ? <ClassificationRow c={info.classification} /> : <MissingRow kind="jev" />}
      {info.turn ? <TurnRow turn={info.turn} /> : <MissingRow kind="turn" />}

      {info.runId && (
        <>
          <div className={cn(ROW, "mt-1 border-t border-hairline px-1 pt-1")}>
            <span />
            <span className="text-ink-3">task</span>
            <span className="truncate text-ink" title={info.runId}>{info.runId}</span>
            <span className="truncate">
              <Status tone={failed ? "bad" : info.status === "completed" ? "ok" : "idle"}>{info.status}</Status>
              {info.model ? <span className="ml-[2ch] text-ink">{info.model}</span> : <span className="ml-[2ch] text-ink-3">model not recorded</span>}
              {info.revision !== undefined && <span className="ml-[1ch] text-ink-3">rev {info.revision}</span>}
              {info.started && (
                <span className="ml-[2ch] text-ink-3">
                  {clock(Date.parse(info.started))}–{info.ended ? clock(Date.parse(info.ended)) : "now"}
                </span>
              )}
            </span>
            <span className="text-right">
              {info.tokens !== undefined ? (
                <>
                  {n(info.tokens)}
                  <Unit>tok</Unit>
                </>
              ) : (
                <span className="text-ink-3">tokens not recorded</span>
              )}
            </span>
          </div>

          <div className="mt-0.5 ml-[calc(4px+0.5ch)] border-l border-hairline pl-[0.5ch]">
            <div className={cn(ROW, "px-1 text-ink-3")}>
              <span />
              <span>kind</span>
              <span>id</span>
              <span>
                {info.cost !== undefined || info.costUnknown ? (
                  <>
                    budget <span className="text-ink">{info.cost !== undefined ? usd(info.cost) : "unknown"}</span>
                    {info.costUnknown && info.cost !== undefined && <span className="text-warning"> + unknown</span>}
                    {info.budget !== undefined && <> / ${info.budget.toFixed(2)}</>}
                  </>
                ) : (
                  "budget not recorded"
                )}
              </span>
              {info.steps ? <Cells values={COLS.map((c) => c.key)} /> : <span>time</span>}
            </div>
            <ol>
              {info.steps
                ? info.steps.map((step) => <li key={step.id}>{step.kind === "model" ? <ModelRow step={step} /> : <ToolRow step={step} />}</li>)
                : info.events?.map((event, i) => (
                    <li key={i}>
                      <EventRow event={event} index={i + 1} />
                    </li>
                  ))}
            </ol>
          </div>
        </>
      )}
    </div>
  );
}

function MissingRow({ kind }: { kind: string }) {
  return (
    <div className={cn(ROW, "px-1")}>
      <span />
      <span className="text-ink-3">{kind}</span>
      <span />
      <span className="text-ink-3">not recorded</span>
      <span />
    </div>
  );
}

/** One recorded task event; the runtime keeps only its kind and name. */
function EventRow({ event, index }: { event: RunEvent; index: number }) {
  const kind = event.kind === "waiting" ? "wait" : event.kind;
  return (
    <div className={cn(ROW, "px-1", event.kind === "tool" && "pl-[2ch]")}>
      <span />
      <span className={event.kind === "tool" ? "text-ink-3" : "text-ink"}>{kind}</span>
      <span className="text-ink-3">e{index}</span>
      <span className="truncate text-ink" title={event.text}>{event.text}</span>
      <span className="text-right text-ink-3">{clock(Date.parse(event.created_at))}</span>
    </div>
  );
}

function ClassificationRow({ c }: { c: Classification }) {
  return (
    <details className="group">
      <summary className={SUMMARY}>
        <Chevron />
        <span className="text-ink-3">jev</span>
        <span />
        <span className="truncate">
          <span className="text-ink">{c.intent}</span>
          <span className={cn("ml-[1ch]", c.available ? "text-ink" : "text-warning")}>{c.confidence.toFixed(2)}</span>
          {c.parts && <span className="ml-[2ch] text-ink-3">{c.parts.map((p) => p.intent).join(" + ")}</span>}
        </span>
        <span>{!c.available && <Status tone="warn">unavailable · defaulted</Status>}</span>
      </summary>
      {c.parts && (
        <div className={DETAIL}>
          <KV
            rows={c.parts.map((p) => [
              p.intent,
              <>
                <span className="text-ink">{p.text}</span>
                {p.replaces_work && <Status tone="warn" className="ml-[1ch]">interrupt</Status>}
              </>,
            ])}
          />
        </div>
      )}
    </details>
  );
}

function TurnRow({ turn }: { turn: OuterTurn }) {
  const tokens = turn.context.reduce((sum, b) => sum + b.tokens, 0);
  const tone: Tone = turn.phase === "failed" ? "bad" : turn.phase === "superseded" ? "warn" : turn.phase === "done" ? "ok" : "idle";
  return (
    <details className="group">
      <summary className={SUMMARY}>
        <Chevron />
        <span className="text-ink-3">turn</span>
        <span className="text-ink-3">{turn.id}</span>
        <span className="truncate">
          <Status tone={tone}>{turn.phase}</Status>
          <span className="ml-[2ch] text-ink">{turn.actions.map((a) => a.type).join(" + ")}</span>
        </span>
        <span className="flex gap-x-[1ch]">
          <span>
            {n(tokens)}
            <Unit>ctx</Unit>
          </span>
          <span className={cn(COLS[4].w, "text-right")}>{secs(turn.ms)}</span>
        </span>
      </summary>
      <div className={DETAIL}>
        <KV
          rows={[
            [
              "context",
              <div key="ctx" className="grid w-fit grid-cols-[auto_6ch] gap-x-[2ch]">
                {turn.context.map((b) => (
                  <Fragment key={b.label}>
                    <span>{b.label}</span>
                    <span className="text-right text-ink">{n(b.tokens)}</span>
                  </Fragment>
                ))}
              </div>,
            ],
            ...turn.actions.map((a): [string, ReactNode] => [a.type, <Action key={a.type} a={a} />]),
          ]}
        />
      </div>
    </details>
  );
}

function Action({ a }: { a: OuterAction }) {
  switch (a.type) {
    case "respond":
      return <Code>{a.text}</Code>;
    case "dispatch":
      return (
        <>
          <Status tone={a.status === "rejected" ? "bad" : "ok"}>{a.status}</Status>
          <span className="ml-[1ch] text-ink">→ {a.task_id}</span>
          {a.error && <span className="ml-[1ch] text-danger">{a.error}</span>}
          <Code>{a.request}</Code>
        </>
      );
    case "update_dispatch":
      return (
        <>
          <span className="text-ink">→ {a.task_id}</span>
          <span className="ml-[1ch] text-ink-3">rev {a.instruction_rev}</span>
          <Code>{a.text}</Code>
        </>
      );
    case "clarify":
      return <Code>{a.question}</Code>;
  }
}

function ModelRow({ step }: { step: Extract<DebugStep, { kind: "model" }> }) {
  const t = step.tokens;
  return (
    <details className="group">
      <summary className={SUMMARY}>
        <Chevron />
        <span className="text-ink">model</span>
        <span className="text-ink">{step.id}</span>
        <span className="truncate">
          <Status tone={step.finish === "STOP" ? "idle" : "warn"}>{step.finish}</Status>
          <span className="ml-[1ch] text-ink-3">{step.thinking}</span>
        </span>
        <Cells values={[n(t.input), n(t.cached), n(t.output), n(t.thinking), secs(step.ms)]} />
      </summary>
      <div className={DETAIL}>
        <KV
          rows={[
            ["tools", <span key="tools" className="text-ink">{step.tools.join("  ")}</span>],
            ["thinking", <span key="thinking" className="text-ink">{step.thinking}</span>],
            ["finish", step.finish],
          ]}
        />
      </div>
    </details>
  );
}

function ToolRow({ step }: { step: Extract<DebugStep, { kind: "tool" }> }) {
  return (
    <details className="group">
      <summary className={cn(SUMMARY, "pl-[2ch]")}>
        <Chevron />
        <span className="text-ink-3">tool</span>
        <span className="text-ink-3">{step.id}</span>
        <span className="truncate">
          <Status tone={step.ok ? "ok" : "bad"}>{step.ok ? "✓" : "✗"}</Status>
          <span className={cn("ml-[1ch]", step.ok ? "text-ink" : "text-danger")}>{step.name}</span>
          <span className="ml-[1ch] text-ink-3">{step.args}</span>
        </span>
        <span className={cn(COLS[4].w, "text-right")}>{secs(step.ms)}</span>
      </summary>
      <div className={cn(DETAIL, "ml-[calc(8px+13ch+3ch+4px)]")}>
        <KV
          rows={[
            ["args", <Code key="args">{step.args}</Code>],
            [
              "result",
              <Code key="result" bad={!step.ok}>
                {step.result}
              </Code>,
            ],
          ]}
        />
      </div>
    </details>
  );
}

function Chevron() {
  return <span className="h-2 text-center text-[8px] leading-2 text-ink-3 transition-transform group-open:rotate-90">▶</span>;
}

function Cells({ values }: { values: readonly string[] }) {
  return (
    <span className="flex gap-x-[1ch]">
      {values.map((v, i) => (
        <span key={COLS[i].key} className={cn(COLS[i].w, "text-right", v === "0" && "text-ink-3")}>
          {v}
        </span>
      ))}
    </span>
  );
}

function KV({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <div className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-[2ch] gap-y-0.5">
      {rows.map(([k, v], i) => (
        <Fragment key={i}>
          <span className="text-ink-3">{k}</span>
          <div className="min-w-0">{v}</div>
        </Fragment>
      ))}
    </div>
  );
}

function Status({ tone, className, children }: { tone: Tone; className?: string; children: ReactNode }) {
  return <span className={cn(TONE[tone], className)}>{children}</span>;
}

function Unit({ children }: { children: ReactNode }) {
  return <span className="ml-[0.5ch] text-ink-3">{children}</span>;
}

function Code({ bad, children }: { bad?: boolean; children: string }) {
  return (
    <pre className={cn("my-0.5 rounded-[3px] bg-fill-1 px-[1ch] whitespace-pre-wrap [overflow-wrap:anywhere]", bad ? "text-danger" : "text-ink")}>{children}</pre>
  );
}
