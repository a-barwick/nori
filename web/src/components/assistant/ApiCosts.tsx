"use client";

import { useRef, useState, type KeyboardEvent } from "react";
import { Check, ChevronLeft, ChevronRight, CircleHelp, X } from "lucide-react";
import { Dialog } from "radix-ui";
import { Button, IconButton } from "@/components/ui/primitives";
import type { ModelSettings } from "@/lib/assistant/api";
import { money, plural } from "@/lib/assistant/format";
import { useResource } from "@/lib/assistant/useResource";
import { cn } from "@/lib/utils";
import { Failure, Loading, Notice, Rows, Section, Tabs } from "./ui";

type CostAmount = {
  cost_usd: number;
  priced_calls: number;
  unpriced_calls: number;
  local_calls: number;
  pending_calls: number;
};

type ModelCost = CostAmount & { model: string };
type CostBucket = { start_at: string; end_at: string; totals: CostAmount; models: ModelCost[] };
type CostReport = {
  days: number;
  timezone: string;
  start_at: string;
  end_at: string;
  bucket_size: "hour" | "day";
  totals: CostAmount;
  models: ModelCost[];
  buckets: CostBucket[];
};
type Range = "1" | "7" | "30";

const NO_COST: CostAmount = { cost_usd: 0, priced_calls: 0, unpriced_calls: 0, local_calls: 0, pending_calls: 0 };
const RANGES: { id: Range; label: string }[] = [
  { id: "1", label: "1 day" },
  { id: "7", label: "7 days" },
  { id: "30", label: "30 days" },
];

function callCount(amount: CostAmount) {
  return amount.priced_calls + amount.unpriced_calls + amount.local_calls + amount.pending_calls;
}

function hasRecordedCost(amount: CostAmount) {
  return amount.priced_calls > 0 || (amount.local_calls > 0 && !amount.unpriced_calls && !amount.pending_calls);
}

function costLabel(amount: CostAmount, unavailable = "—") {
  return hasRecordedCost(amount) || !callCount(amount) ? money(amount.cost_usd) : unavailable;
}

function periodLabel(amount: CostAmount) {
  if (!amount.priced_calls && !amount.unpriced_calls && amount.pending_calls) return "In progress";
  return callCount(amount) ? costLabel(amount, "Cost unavailable") : "No calls";
}

function modelLabel(model: string, settings?: ModelSettings) {
  return settings?.model_profiles.profiles.find((profile) => profile.model === model)?.label || model || "Unknown model";
}

function dateLabel(iso: string, timeZone: string, weekday = false) {
  return new Intl.DateTimeFormat("en-US", { timeZone, ...(weekday ? { weekday: "short" } : {}), month: "short", day: "numeric" }).format(new Date(iso));
}

function hourLabel(iso: string, timeZone: string, offset = false) {
  const date = new Date(iso);
  const minutes = new Intl.DateTimeFormat("en-US", { timeZone, hour: "numeric", minute: "2-digit" })
    .formatToParts(date).find((part) => part.type === "minute")?.value;
  return new Intl.DateTimeFormat("en-US", {
    timeZone,
    hour: "numeric",
    ...(minutes === "00" ? {} : { minute: "2-digit" }),
    ...(offset ? { timeZoneName: "shortOffset" } : {}),
  }).format(date);
}

function bucketLabels(report: CostReport) {
  if (report.bucket_size === "day") {
    return report.buckets.map((bucket) => ({
      axis: dateLabel(bucket.start_at, report.timezone),
      detail: dateLabel(bucket.start_at, report.timezone, true),
    }));
  }
  const labels = report.buckets.map((bucket) => hourLabel(bucket.start_at, report.timezone));
  return labels.map((label, index) => {
    const unique = labels.indexOf(label) === labels.lastIndexOf(label);
    const value = unique ? label : hourLabel(report.buckets[index].start_at, report.timezone, true);
    return { axis: value, detail: value };
  });
}

function axisStep(maximum: number) {
  if (maximum <= 0) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(maximum / 3));
  const scaled = maximum / 3 / magnitude;
  return (scaled <= 1 ? 1 : scaled <= 2 ? 2 : scaled <= 5 ? 5 : 10) * magnitude;
}

function axisAmount(value: number) {
  if (value === 0) return "$0";
  if (value >= 1) return `$${Number(value.toFixed(2)).toLocaleString("en-US")}`;
  if (value >= 0.01) return `$${value.toFixed(2)}`;
  return `$${value.toLocaleString("en-US", { maximumSignificantDigits: 2, maximumFractionDigits: 16 })}`;
}

function CostInfo() {
  return <Dialog.Root>
    <Dialog.Trigger asChild>
      <IconButton label="About recorded cost" size={28} tip={false}>
        <CircleHelp size={16} aria-hidden />
      </IconButton>
    </Dialog.Trigger>
    <Dialog.Portal>
      <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
      <Dialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
        <div className="flex items-center justify-between gap-2">
          <Dialog.Title className="text-section font-semibold text-ink">About recorded cost</Dialog.Title>
          <Dialog.Close asChild><IconButton label="Close" size={32} tip={false}><X size={17} aria-hidden /></IconButton></Dialog.Close>
        </div>
        <Dialog.Description asChild>
          <div className="mt-2 space-y-3 text-body text-ink-2">
            <p>Amounts are in US dollars. They combine costs reported by model providers and costs Nori calculates from recorded usage.</p>
            <p>Calls with unavailable prices are counted separately. Local model calls have no API charge.</p>
            <p>Dates follow your Nori time zone. Today includes calls recorded so far.</p>
            <p>For the amount billed to your account, check your provider’s invoice.</p>
          </div>
        </Dialog.Description>
      </Dialog.Content>
    </Dialog.Portal>
  </Dialog.Root>;
}

export function ApiCosts() {
  const [range, setRange] = useState<Range>("30");
  const [model, setModel] = useState<string | null>(null);
  const report = useResource<CostReport>(`/settings/api-costs?days=${range}`, { poll: 30_000, versioned: false });
  const settings = useResource<ModelSettings>("/settings/models", { poll: 60_000 });
  const data = report.data?.days === Number(range) ? report.data : undefined;
  const activeModel = data && model !== null && data.models.some((item) => item.model === model) ? model : null;

  return <>
    <Tabs<Range> label="Date range" value={range} onChange={setRange} items={RANGES} />
    {report.error ? <Failure error={report.error} onRetry={() => void report.reload()} />
      : !data ? <Loading label="Loading API costs…" />
      : <CostDetails report={data} selectedModel={activeModel} setSelectedModel={setModel} settings={settings.data} />}
  </>;
}

function CostDetails({ report, selectedModel, setSelectedModel, settings }: {
  report: CostReport;
  selectedModel: string | null;
  setSelectedModel: (model: string | null) => void;
  settings?: ModelSettings;
}) {
  const modelButtons = useRef(new Map<string, HTMLButtonElement>());
  const filteredModel = selectedModel === null ? undefined : report.models.find((item) => item.model === selectedModel);
  const total = selectedModel === null ? report.totals : filteredModel ?? NO_COST;
  const currentModels = report.models;
  const first = report.buckets[0];
  const last = report.buckets.at(-1);
  const start = first ? dateLabel(first.start_at, report.timezone) : dateLabel(report.start_at, report.timezone);
  const end = last ? dateLabel(last.start_at, report.timezone) : start;
  const rangeLabel = start === end ? start : `${start} – ${end}`;
  const hasCalls = callCount(total) > 0;

  function clearModel() {
    const previous = selectedModel;
    setSelectedModel(null);
    if (previous !== null) requestAnimationFrame(() => modelButtons.current.get(previous)?.focus());
  }

  return <>
    <section aria-label="Recorded cost" className="rounded-card border border-card-border bg-surface px-4 pt-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="-my-1 flex items-center gap-1 text-meta font-semibold text-ink-2">
            Recorded cost <CostInfo />
          </div>
          <p className="mt-1 text-[32px] leading-9 font-bold tracking-[-0.03em] tabular-nums" aria-live="polite">{costLabel(total)}</p>
        </div>
        <div className="flex flex-col items-end gap-2 text-right">
          <p className="text-meta text-ink-2">{rangeLabel}</p>
          {selectedModel !== null && <Button variant="tonal" size="sm" onClick={clearModel} aria-label={`Clear model filter: ${modelLabel(selectedModel, settings)}`}>
            <span className="max-w-48 truncate">{modelLabel(selectedModel, settings)}</span><X size={14} aria-hidden />
          </Button>}
        </div>
      </div>
      {hasCalls && report.buckets.length ? <CostChart key={JSON.stringify([report.days, selectedModel])} report={report} selectedModel={selectedModel} />
        : <div className="py-12 text-center text-meta text-ink-2">No model calls in this range</div>}
    </section>
    {total.unpriced_calls > 0 && <div className="mt-3"><Notice tone="warning">
      <span className="flex items-baseline gap-2">
        <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-warning" />
        <span>{hasRecordedCost(total)
          ? `${plural(total.unpriced_calls, "call")} with unavailable cost. The total includes priced calls only.`
          : `Cost is unavailable for ${plural(total.unpriced_calls, "call")} in this view.`}</span>
      </span>
    </Notice></div>}
    {total.pending_calls > 0 && <div className="mt-3"><Notice>{plural(total.pending_calls, "call")} in progress.</Notice></div>}
    {currentModels.length > 0 && <Section title="By model">
      <Rows label="By model">
        {currentModels.map((item) => {
          const selected = selectedModel === item.model;
          const details = [plural(callCount(item), "call")];
          if (item.local_calls === callCount(item)) details.push("Local");
          else if (item.local_calls > 0) details.push(`${item.local_calls} local`);
          if (item.unpriced_calls > 0) details.push(`${item.unpriced_calls} without cost`);
          if (item.pending_calls > 0) details.push(`${item.pending_calls} in progress`);
          return <li key={item.model}>
            <button type="button" aria-pressed={selected} onClick={() => setSelectedModel(selected ? null : item.model)}
              ref={(button) => { if (button) modelButtons.current.set(item.model, button); else modelButtons.current.delete(item.model); }}
              className={cn("flex min-h-14 w-full items-center gap-3 px-4 py-3 text-left hover:bg-fill-1 focus-visible:bg-fill-1", selected && "bg-fill-1")}>
              <span className="min-w-0 flex-1">
                <span className={cn("block text-row font-semibold [overflow-wrap:anywhere] text-ink", selected && "text-accent-text")}>{modelLabel(item.model, settings)}</span>
                <span className="mt-0.5 block text-meta text-ink-2">{details.join(" · ")}</span>
              </span>
              <span className={cn("shrink-0 text-row font-semibold tabular-nums", !hasRecordedCost(item) && "text-meta font-medium text-ink-2")}>{costLabel(item, item.unpriced_calls ? "Cost unavailable" : "In progress")}</span>
              {selected && <Check size={15} className="shrink-0 text-accent-text" aria-hidden />}
            </button>
          </li>;
        })}
      </Rows>
    </Section>}
  </>;
}

function CostChart({ report, selectedModel }: { report: CostReport; selectedModel: string | null }) {
  const [selected, setSelected] = useState<string | null>(null);
  const bars = useRef<HTMLDivElement>(null);
  const labels = bucketLabels(report);
  const values = report.buckets.map((bucket) => selectedModel === null
    ? bucket.totals
    : bucket.models.find((item) => item.model === selectedModel) ?? NO_COST);
  const found = report.buckets.findIndex((bucket) => bucket.start_at === selected);
  const active = found < 0 ? report.buckets.length - 1 : found;
  const peak = Math.max(...values.map((value) => value.cost_usd), 0);
  const step = axisStep(peak);
  const max = step * 3;
  const tickIndices = Array.from(new Set([0, Math.round((values.length - 1) / 3), Math.round((values.length - 1) * 2 / 3), values.length - 1]));

  function select(index: number, focus = false) {
    const next = Math.min(Math.max(index, 0), values.length - 1);
    setSelected(report.buckets[next].start_at);
    if (focus) bars.current?.querySelectorAll<HTMLButtonElement>("button")[next]?.focus();
  }

  function handleKeys(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next = event.key === "ArrowLeft" ? index - 1 : event.key === "ArrowRight" ? index + 1
      : event.key === "Home" ? 0 : event.key === "End" ? values.length - 1 : undefined;
    if (next === undefined) return;
    event.preventDefault();
    select(next, true);
  }

  return <>
    <div className="mt-6 grid grid-cols-[auto_minmax(0,1fr)] gap-x-2" role="group" aria-label={`${report.bucket_size === "hour" ? "Hourly" : "Daily"} recorded cost`}>
      <div aria-hidden className="flex h-32 flex-col justify-between text-right text-caption tabular-nums text-ink-3 sm:h-36">
        {[3, 2, 1, 0].map((tick) => <span key={tick} className={cn("leading-none", !peak && tick > 0 && "invisible")}>{axisAmount(tick * step)}</span>)}
      </div>
      <div className="relative h-32 sm:h-36">
        <div aria-hidden className="pointer-events-none absolute inset-y-1.5 inset-x-0 flex flex-col justify-between">
          {[3, 2, 1, 0].map((tick) => <div key={tick} className={cn("border-t border-hairline", !peak && tick > 0 && "invisible")} />)}
        </div>
        <div ref={bars} className="absolute inset-y-1.5 inset-x-0 grid" style={{ gridTemplateColumns: `repeat(${values.length}, minmax(0, 1fr))` }}>
          {values.map((value, index) => {
            const text = periodLabel(value);
            const status = value.unpriced_calls ? `, ${plural(value.unpriced_calls, "call")} without cost` : "";
            const pending = value.pending_calls ? `, ${plural(value.pending_calls, "call")} being recorded` : "";
            return <button key={report.buckets[index].start_at} type="button" aria-label={`${labels[index].detail}, ${text}${status}${pending}`}
              aria-pressed={index === active} tabIndex={index === active ? 0 : -1}
              onClick={() => select(index)} onKeyDown={(event) => handleKeys(event, index)}
              className={cn("relative flex h-full min-w-0 items-end justify-center rounded-t focus-visible:outline-2 focus-visible:outline-accent-signal", index === active && "bg-accent-text/5")}>
              <span className={cn("w-[65%] max-w-16 rounded-t-sm", index === active ? "bg-accent-text" : "bg-accent-text/30")}
                style={{ height: `${Math.max(0, value.cost_usd / max * 100)}%` }} />
              {value.unpriced_calls > 0 && <span aria-hidden className="absolute -bottom-2.5 size-1.5 rounded-full bg-warning" />}
            </button>;
          })}
        </div>
      </div>
      <div />
      <div aria-hidden className="relative mt-3 h-5 text-caption text-ink-3">
        {tickIndices.map((index, i) => <span key={index} className={cn("absolute whitespace-nowrap", i === 0 ? "left-0" : i === tickIndices.length - 1 ? "right-0" : "-translate-x-1/2")}
          style={i === 0 || i === tickIndices.length - 1 ? undefined : { left: `${(index + 0.5) / values.length * 100}%` }}>{labels[index].axis}</span>)}
      </div>
    </div>
    <div className="mt-3 flex min-h-13 items-center justify-between gap-2 border-t border-hairline text-meta">
      <div aria-live="polite" className="flex flex-wrap items-baseline gap-x-2">
        <span className="text-ink-2">{labels[active].detail}</span>
        <strong className="tabular-nums">{periodLabel(values[active])}</strong>
        {values[active].unpriced_calls > 0 && <span className="text-warning">{plural(values[active].unpriced_calls, "call")} without cost</span>}
        {values[active].pending_calls > 0 && periodLabel(values[active]) !== "In progress" && <span className="text-ink-2">{plural(values[active].pending_calls, "call")} in progress</span>}
      </div>
      <div className="-mr-2 flex shrink-0">
        <IconButton label="Previous period" size={36} tip={false} disabled={active === 0} onClick={() => select(active - 1)}><ChevronLeft size={18} aria-hidden /></IconButton>
        <IconButton label="Next period" size={36} tip={false} disabled={active === values.length - 1} onClick={() => select(active + 1)}><ChevronRight size={18} aria-hidden /></IconButton>
      </div>
    </div>
  </>;
}
