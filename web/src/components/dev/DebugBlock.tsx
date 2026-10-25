"use client";

import { Check, X } from "lucide-react";
import { clock } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { DebugInfo, DebugStep } from "./debug";

const n = (v: number) => v.toLocaleString("en-US");
const secs = (ms: number) => `${(ms / 1000).toFixed(1)} s`;

/** Shown under a Nori message when debug mode is on. */
export function DebugBlock({ info }: { info: DebugInfo }) {
  const modelCalls = info.steps.filter((s) => s.kind === "model").length;
  const toolCalls = info.steps.length - modelCalls;
  return (
    <div className="mt-2 w-full max-w-[640px] rounded-tile border border-hairline bg-surface/70 px-3 py-2 font-mono text-mono-data text-ink-2">
      <div className="flex flex-wrap gap-x-4 gap-y-1">
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
