"use client";

import { Check, X } from "lucide-react";
import { clock } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { Classification, DebugInfo, DebugStep, OuterTurn } from "./debug";

const n = (v: number) => v.toLocaleString("en-US");
const secs = (ms: number) => `${(ms / 1000).toFixed(1)} s`;

/** Shown under a Nori message when debug mode is on. */
export function DebugBlock({ info }: { info: DebugInfo }) {
  const modelCalls = info.steps.filter((s) => s.kind === "model").length;
  const toolCalls = info.steps.length - modelCalls;
  return (
    <div className="mt-2 w-full max-w-[640px] rounded-tile border border-hairline bg-surface/70 px-3 py-2 font-mono text-mono-data text-ink-2">
      <ClassificationRow c={info.classification} />
      <TurnRows turn={info.turn} />
      <p className="mt-2 border-t border-hairline pt-2 text-ink-3">task</p>
      <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1">
        <span>{info.runId}</span>
        <span className={info.status === "failed" ? "text-danger" : undefined}>{info.status}</span>
        <span>{info.model}</span>
        <span>rev {info.revision}</span>
        <span>model {modelCalls}/6</span>
        <span>tools {toolCalls}/12</span>
        <span>{n(info.tokens)} tok</span>
        <span>${info.cost.toFixed(4)}</span>
        <span className="text-ink-3">
          {clock(Date.parse(info.started))}–{clock(Date.parse(info.ended))}
        </span>
      </div>
      <ol className="mt-2 border-t border-hairline pt-2">
        {info.steps.map((step) => (
          <li key={step.id}>{step.kind === "model" ? <ModelRow step={step} /> : <ToolRow step={step} />}</li>
        ))}
      </ol>
    </div>
  );
}

function ClassificationRow({ c }: { c: Classification }) {
  return (
    <details>
      <summary className="flex cursor-pointer items-center gap-3">
        <span className="w-12 shrink-0 text-ink-3">jev</span>
        <span className="text-ink">{c.intent}</span>
        <span>{c.confidence.toFixed(2)}</span>
        {!c.available && <span className="text-warning">unavailable · defaulted</span>}
      </summary>
      {c.parts && (
        <ul className="mt-1.5 ml-15 space-y-0.5">
          {c.parts.map((p) => (
            <li key={p.text} className="flex gap-3">
              <span className="w-24 shrink-0 text-ink-3">{p.intent}</span>
              <span className="font-sans text-meta">{p.text}</span>
              {p.replaces_work && <span className="text-warning">interrupt</span>}
            </li>
          ))}
        </ul>
      )}
    </details>
  );
}

function TurnRows({ turn }: { turn: OuterTurn }) {
  const tokens = turn.context.reduce((sum, b) => sum + b.tokens, 0);
  return (
    <details className="mt-1">
      <summary className="flex cursor-pointer items-center gap-3">
        <span className="w-12 shrink-0 text-ink-3">turn</span>
        <span className="text-ink">{turn.actions.map((a) => a.type).join(" + ")}</span>
        <span className={turn.phase === "failed" || turn.phase === "superseded" ? "text-danger" : undefined}>{turn.phase}</span>
        <span className="ml-auto text-ink-3">
          {n(tokens)} ctx · {secs(turn.ms)}
        </span>
      </summary>
      <div className="mt-1.5 ml-15 space-y-1.5">
        <ul className="space-y-0.5">
          {turn.context.map((b) => (
            <li key={b.label} className="flex gap-3">
              <span className="flex-1">{b.label}</span>
              <span className="text-ink-3">{n(b.tokens)}</span>
            </li>
          ))}
        </ul>
        {turn.actions.map((a, i) => (
          <Code key={i} label={a.type} bad={"status" in a && a.status === "rejected"}>
            {actionText(a)}
          </Code>
        ))}
      </div>
    </details>
  );
}

function actionText(a: OuterTurn["actions"][number]) {
  switch (a.type) {
    case "respond":
      return a.text;
    case "dispatch":
      return `${a.status} · ${a.task_id}${a.error ? ` · ${a.error}` : ""}\n${a.request}`;
    case "update_dispatch":
      return `${a.task_id} · rev ${a.instruction_rev}\n${a.text}`;
    case "clarify":
      return a.question;
  }
}

function ModelRow({ step }: { step: Extract<DebugStep, { kind: "model" }> }) {
  const t = step.tokens;
  return (
    <details className="py-1">
      <summary className="flex cursor-pointer items-center gap-3">
        <span className="w-12 shrink-0 text-ink-3">{step.id}</span>
        <span className="text-ink">model</span>
        <span className="ml-auto text-ink-3">
          {n(t.input)} in · {n(t.cached)} cached · {n(t.output)} out · {n(t.thinking)} think · {secs(step.ms)}
        </span>
      </summary>
      <div className="mt-1.5 ml-15 space-y-1.5">
        <div className="flex flex-wrap gap-1">
          {step.tools.map((tool) => (
            <span key={tool} className="rounded-check bg-fill-1 px-1.5">
              {tool}
            </span>
          ))}
        </div>
        <p className="font-sans text-meta text-ink-2 italic">{step.thinking}</p>
        <p className="text-ink-3">finish: {step.finish}</p>
      </div>
    </details>
  );
}

function ToolRow({ step }: { step: Extract<DebugStep, { kind: "tool" }> }) {
  return (
    <details className="py-1">
      <summary className="flex cursor-pointer items-center gap-3">
        <span className="w-12 shrink-0 text-ink-3">{step.id}</span>
        {step.ok ? <Check size={12} className="text-accent-signal" /> : <X size={12} className="text-danger" />}
        <span className="text-ink">{step.name}</span>
        <span className="ml-auto text-ink-3">{secs(step.ms)}</span>
      </summary>
      <div className="mt-1.5 ml-15 space-y-1.5">
        <Code label="args">{step.args}</Code>
        <Code label="result" bad={!step.ok}>
          {step.result}
        </Code>
      </div>
    </details>
  );
}

function Code({ label, bad, children }: { label: string; bad?: boolean; children: string }) {
  return (
    <div className="flex gap-3">
      <span className="w-12 shrink-0 text-ink-3">{label}</span>
      <pre className={cn("min-w-0 flex-1 rounded-check bg-fill-1 px-2 py-1 whitespace-pre-wrap", bad && "text-danger")}>{children}</pre>
    </div>
  );
}
