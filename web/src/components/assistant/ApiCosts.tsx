"use client";

import { useRef, useState, type KeyboardEvent } from "react";
import { Check, ChevronLeft, ChevronRight, CircleHelp, X } from "lucide-react";
import { Dialog } from "radix-ui";
import { Button, IconButton } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import type {
  BillingTier,
  CostAmount,
  CostReport,
  EntitlementSummary,
  ModelSettings,
  Preferences,
  PreferencesEditable,
} from "@/lib/contracts/assistant";
import { money, plural } from "@/lib/assistant/format";
import { useAction, useResource } from "@/lib/assistant/useResource";
import { cn } from "@/lib/utils";
import { ProviderBrandIcon } from "./BrandIcons";
import { Failure, inputBase, Loading, Notice, Pill, Row, Rows, Section, Tabs, type Tone } from "./ui";

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

function modelProfileFor(model: string, settings?: ModelSettings) {
  return settings?.model_profiles.profiles.find((profile) => profile.model === model);
}

function modelLabel(model: string, settings?: ModelSettings) {
  return modelProfileFor(model, settings)?.label || model || "Unknown model";
}

function dateLabel(iso: string, timeZone: string, weekday = false) {
  return new Intl.DateTimeFormat("en-US", {
    timeZone,
    ...(weekday ? { weekday: "short" } : {}),
    month: "short",
    day: "numeric",
  }).format(new Date(iso));
}

function hourLabel(iso: string, timeZone: string, offset = false) {
  const date = new Date(iso);
  const minutes = new Intl.DateTimeFormat("en-US", { timeZone, hour: "numeric", minute: "2-digit" })
    .formatToParts(date)
    .find((part) => part.type === "minute")?.value;
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
  return (
    <Dialog.Root>
      <Dialog.Trigger asChild>
        <IconButton label="About recorded spend" size={28} tip={false}>
          <CircleHelp size={16} aria-hidden />
        </IconButton>
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <Dialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
          <div className="flex items-center justify-between gap-2">
            <Dialog.Title className="text-section font-semibold text-ink">About recorded spend</Dialog.Title>
            <Dialog.Close asChild>
              <IconButton label="Close" size={32} tip={false}>
                <X size={17} aria-hidden />
              </IconButton>
            </Dialog.Close>
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
    </Dialog.Root>
  );
}

export function ApiCosts() {
  const [range, setRange] = useState<Range>("30");
  const [model, setModel] = useState<string | null>(null);
  const report = useResource<CostReport>(`/settings/api-costs?days=${range}`, { poll: 30_000, versioned: false });
  const settings = useResource<ModelSettings>("/settings/models", { poll: 60_000 });
  const preferences = useResource<Preferences>("/preferences", { poll: 0 });
  const data = report.data?.days === Number(range) ? report.data : undefined;
  const activeModel = data && model !== null && data.models.some((item) => item.model === model) ? model : null;

  return (
    <>
      <Tabs<Range> label="Date range" value={range} onChange={setRange} items={RANGES} />
      {!data ? (
        report.error ? (
          <Failure error={report.error} onRetry={() => void report.reload()} />
        ) : (
          <Loading label="Loading API costs…" />
        )
      ) : (
        <>
          {report.error && (
            <div className="mb-3">
              <Notice
                tone="warning"
                action={
                  <Button size="sm" variant="neutral" onClick={() => void report.reload()}>
                    Try again
                  </Button>
                }
              >
                Couldn’t refresh usage right now. Showing last loaded figures.
              </Notice>
            </div>
          )}
          <CostDetails
            report={data}
            selectedModel={activeModel}
            setSelectedModel={setModel}
            settings={settings.data}
            preferences={preferences.data}
          />
        </>
      )}

      {preferences.data ? (
        <BudgetsSection preferences={preferences.data} onSaved={preferences.apply} />
      ) : preferences.error ? (
        <Section title="Budgets">
          <Failure error={preferences.error} onRetry={() => void preferences.reload()} />
        </Section>
      ) : null}
    </>
  );
}

function editablePreferences(preferences: Preferences): PreferencesEditable {
  const { revision: _revision, updated_at: _updated, ...value } = preferences;
  void _revision;
  void _updated;
  return value;
}

function BudgetsSection({
  preferences,
  onSaved,
}: {
  preferences: Preferences;
  onSaved: (next: Preferences) => void;
}) {
  const action = useAction();
  const [editing, setEditing] = useState<"task" | "monthly" | null>(null);
  const [draft, setDraft] = useState("");
  const [validationError, setValidationError] = useState<string>();

  const startEdit = (field: "task" | "monthly") => {
    setEditing(field);
    setValidationError(undefined);
    setDraft(
      field === "task"
        ? String(preferences.budgets.task_usd)
        : preferences.budgets.monthly_usd !== undefined
          ? String(preferences.budgets.monthly_usd)
          : "",
    );
  };

  const saveField = (field: "task" | "monthly") => {
    const trimmed = draft.trim();
    if (field === "task") {
      const parsed = trimmed === "" ? Number.NaN : Number(trimmed);
      if (!Number.isFinite(parsed) || parsed < 0) {
        setValidationError("Enter a valid per-task limit (0 or more).");
        return;
      }
      const base = editablePreferences(preferences);
      const next: PreferencesEditable = {
        ...base,
        budgets: { ...base.budgets, task_usd: parsed },
      };
      void commit(next);
    } else {
      const parsed = trimmed === "" ? undefined : Number(trimmed);
      if (parsed !== undefined && (!Number.isFinite(parsed) || parsed < 0)) {
        setValidationError("Enter a valid monthly limit (0 or more), or leave blank for no limit.");
        return;
      }
      const base = editablePreferences(preferences);
      const nextBudgets: PreferencesEditable["budgets"] = { task_usd: base.budgets.task_usd };
      if (parsed !== undefined) nextBudgets.monthly_usd = parsed;
      void commit({ ...base, budgets: nextBudgets });
    }
  };

  const commit = (candidate: PreferencesEditable) =>
    action.run("save-budget", async () => {
      try {
        const receipt = await mutate<Preferences>(
          "/preferences",
          {
            request_id: newRequestID(),
            expected_revision: preferences.revision,
            value: candidate,
          },
          "PUT",
        );
        onSaved(receipt.resource as Preferences);
        setEditing(null);
        setValidationError(undefined);
      } catch (error) {
        if (error instanceof APIError && error.stale && error.body?.current) {
          onSaved(error.body.current as Preferences);
        }
        throw error;
      }
    });

  const renderBudgetRow = (field: "task" | "monthly", label: string, displayValue: string) => {
    if (editing === field) {
      return (
        <li key={field} className="px-4 py-3">
          <form
            className="flex flex-wrap items-center gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              saveField(field);
            }}
          >
            <span className="min-w-0 flex-1 text-row font-semibold text-ink">{label}</span>
            <input
              aria-label={label}
              type="number"
              min={0}
              step="any"
              value={draft}
              onChange={(event) => {
                setDraft(event.target.value);
                setValidationError(undefined);
              }}
              placeholder={field === "monthly" ? "No limit" : "0.00"}
              className={cn(inputBase, "h-9 w-28 py-0 tabular-nums")}
              autoFocus
            />
            <Button type="submit" size="sm" variant="primary" disabled={!!action.pending}>
              {action.pending ? "Saving…" : "Save"}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="quiet"
              disabled={!!action.pending}
              onClick={() => {
                setEditing(null);
                setValidationError(undefined);
              }}
            >
              Cancel
            </Button>
          </form>
        </li>
      );
    }
    return (
      <Row
        key={field}
        title={label}
        onClick={() => startEdit(field)}
        trailing={<span className="text-meta font-medium tabular-nums text-ink-2">{displayValue}</span>}
        chevron
      />
    );
  };

  return (
    <Section title="Budgets">
      {validationError && (
        <div className="mb-3">
          <Notice tone="danger">{validationError}</Notice>
        </div>
      )}
      {action.error && (
        <div className="mb-3">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}
      <Rows label="Budgets">
        {renderBudgetRow("task", "Per-task limit", money(preferences.budgets.task_usd))}
        {renderBudgetRow(
          "monthly",
          "Monthly limit",
          preferences.budgets.monthly_usd !== undefined ? money(preferences.budgets.monthly_usd) : "No limit",
        )}
      </Rows>
    </Section>
  );
}

const TIER_LABELS: Record<BillingTier, string> = {
  trial: "Trial",
  byok: "BYOK",
  standard: "Standard",
  power: "Power",
};

function tierLabel(tier: BillingTier): string {
  return TIER_LABELS[tier] ?? tier;
}

function entitlementExceptionPill(entitlement: EntitlementSummary): { label: string; tone: Tone } | undefined {
  if (entitlement.suspended) return { label: "Suspended", tone: "danger" };
  if (entitlement.status === "past_due") return { label: "Past due", tone: "warning" };
  if (entitlement.status === "expired") return { label: "Expired", tone: "warning" };
  if (entitlement.status === "billing_retry") return { label: "Billing retry", tone: "warning" };
  if (entitlement.status === "canceled") return { label: "Canceling", tone: "neutral" };
  return undefined;
}

function PlanCreditsCard({ entitlement, timeZone }: { entitlement: EntitlementSummary; timeZone: string }) {
  const pill = entitlementExceptionPill(entitlement);
  const grant = Math.max(0, entitlement.credit_grant_usd);
  const remaining = Math.max(0, entitlement.credit_remaining_usd);
  const usedPct = grant > 0 ? Math.min(100, Math.max(0, ((grant - remaining) / grant) * 100)) : 0;
  const barColor = remaining <= 0 ? "bg-danger" : usedPct >= 80 ? "bg-warning" : "bg-accent";
  const periodEndLabel = entitlement.period_end ? dateLabel(entitlement.period_end, timeZone) : undefined;
  const creditsExhausted = grant > 0 && remaining <= 0;
  const inactive = entitlement.status === "past_due" || entitlement.status === "expired";

  return (
    <section aria-label="Plan and credits" className="mb-4 rounded-card border border-card-border bg-surface p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <p className="text-meta font-semibold text-ink-2">Plan</p>
          <p className="mt-0.5 text-section font-semibold text-ink">{tierLabel(entitlement.tier)}</p>
          {entitlement.pending_tier && (
            <p className="mt-0.5 text-caption text-ink-3">
              Switching to {tierLabel(entitlement.pending_tier)}
              {periodEndLabel ? ` on ${periodEndLabel}` : ""}
            </p>
          )}
        </div>
        <div className="flex flex-col items-end gap-1 text-right">
          {pill && <Pill tone={pill.tone}>{pill.label}</Pill>}
          {periodEndLabel && !entitlement.pending_tier && (
            <p className="text-caption text-ink-3">
              {entitlement.status === "canceled" ? `Ends ${periodEndLabel}` : `Resets ${periodEndLabel}`}
            </p>
          )}
        </div>
      </div>

      {grant > 0 && (
        <div className="mt-3">
          <div
            role="progressbar"
            aria-label="Hosted credits used"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(usedPct)}
            className="h-1.5 w-full overflow-hidden rounded-full bg-fill-1"
          >
            <div className={cn("h-full rounded-full transition-[width]", barColor)} style={{ width: `${usedPct}%` }} />
          </div>
          <p className="mt-1.5 text-caption tabular-nums text-ink-3">
            {money(remaining)} remaining of {money(grant)} hosted credits
            {entitlement.credit_pending_usd > 0 ? ` · ${money(entitlement.credit_pending_usd)} pending` : ""}
          </p>
        </div>
      )}

      {entitlement.suspended ? (
        <div className="mt-3">
          <Notice tone="danger">
            {entitlement.suspended_reason || "This account has been suspended. Your history remains read-only."}
          </Notice>
        </div>
      ) : inactive ? (
        <div className="mt-3">
          <Notice tone="warning">
            Your subscription is inactive. Renew your plan to resume chatting and running tasks.
          </Notice>
        </div>
      ) : creditsExhausted ? (
        <div className="mt-3">
          <Notice tone="warning">
            {entitlement.allow_byok
              ? "Your hosted credits are used up. Add a provider API key or upgrade your plan to continue."
              : "Your trial credits are used up. Upgrade your plan to continue."}
          </Notice>
        </div>
      ) : null}
    </section>
  );
}

function CostDetails({
  report,
  selectedModel,
  setSelectedModel,
  settings,
  preferences,
}: {
  report: CostReport;
  selectedModel: string | null;
  setSelectedModel: (model: string | null) => void;
  settings?: ModelSettings;
  preferences?: Preferences;
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

  const monthlyLimit = preferences?.budgets.monthly_usd;
  const showBudgetBar =
    report.days === 30 &&
    selectedModel === null &&
    monthlyLimit !== undefined &&
    monthlyLimit > 0 &&
    hasRecordedCost(total);
  const pct = showBudgetBar ? Math.min(100, (total.cost_usd / monthlyLimit!) * 100) : 0;
  const barColor = pct >= 100 ? "bg-danger" : pct >= 80 ? "bg-warning" : "bg-accent";

  function clearModel() {
    const previous = selectedModel;
    setSelectedModel(null);
    if (previous !== null) requestAnimationFrame(() => modelButtons.current.get(previous)?.focus());
  }

  const hasBillingBreakdown =
    selectedModel === null &&
    !!report.by_billing &&
    callCount(report.by_billing.hosted) + callCount(report.by_billing.byok) + callCount(report.by_billing.local) > 0;
  const hasCategoryBreakdown =
    selectedModel === null &&
    !!report.by_category &&
    callCount(report.by_category.llm) + callCount(report.by_category.search) + callCount(report.by_category.computer) > 0;

  return (
    <>
      {report.entitlement && <PlanCreditsCard entitlement={report.entitlement} timeZone={report.timezone} />}
      <section aria-label="Recorded spend" className="rounded-card border border-card-border bg-surface px-4 pt-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="-my-1 flex items-center gap-1 text-meta font-semibold text-ink-2">
              Recorded spend <CostInfo />
            </div>
            <p className="mt-1 text-[32px] leading-9 font-bold tracking-[-0.03em] tabular-nums" aria-live="polite">
              {costLabel(total)}
            </p>
          </div>
          <div className="flex flex-col items-end gap-2 text-right">
            <p className="text-meta text-ink-2">{rangeLabel}</p>
            {selectedModel !== null && (
              <Button
                variant="tonal"
                size="sm"
                onClick={clearModel}
                aria-label={`Clear model filter: ${modelLabel(selectedModel, settings)}`}
              >
                <span className="max-w-48 truncate">{modelLabel(selectedModel, settings)}</span>
                <X size={14} aria-hidden />
              </Button>
            )}
          </div>
        </div>
        {showBudgetBar && (
          <div className="mt-3">
            <div className="h-1.5 w-full overflow-hidden rounded-full bg-fill-1">
              <div className={cn("h-full rounded-full transition-[width]", barColor)} style={{ width: `${pct}%` }} />
            </div>
            <p className="mt-1.5 text-caption tabular-nums text-ink-3">
              {Math.round(pct)}% of {money(monthlyLimit!)} monthly limit
            </p>
          </div>
        )}
        {hasCalls && report.buckets.length ? (
          <CostChart key={JSON.stringify([report.days, selectedModel])} report={report} selectedModel={selectedModel} />
        ) : (
          <div className="py-12 text-center text-meta text-ink-2">No model calls in this range</div>
        )}
      </section>
      {total.unpriced_calls > 0 && (
        <div className="mt-3">
          <Notice tone="warning">
            <span className="flex items-baseline gap-2">
              <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-warning" />
              <span>
                {hasRecordedCost(total)
                  ? `${plural(total.unpriced_calls, "call")} with unavailable cost. The total includes priced calls only.`
                  : `Cost is unavailable for ${plural(total.unpriced_calls, "call")} in this view.`}
              </span>
            </span>
          </Notice>
        </div>
      )}
      {total.pending_calls > 0 && (
        <div className="mt-3">
          <Notice>{plural(total.pending_calls, "call")} in progress.</Notice>
        </div>
      )}

      {hasBillingBreakdown && report.by_billing && (
        <Section title="By billing">
          <Rows label="By billing">
            <Row
              title="Hosted"
              trailing={<span className="text-row font-semibold tabular-nums">{costLabel(report.by_billing.hosted)}</span>}
            />
            <Row
              title="Your API keys"
              trailing={<span className="text-row font-semibold tabular-nums">{costLabel(report.by_billing.byok)}</span>}
            />
            <Row
              title="Local"
              trailing={
                <span className="text-meta font-medium tabular-nums text-ink-2">
                  {report.by_billing.local.local_calls > 0 && !report.by_billing.local.priced_calls
                    ? "Local"
                    : costLabel(report.by_billing.local)}
                </span>
              }
            />
          </Rows>
        </Section>
      )}

      {hasCategoryBreakdown && report.by_category && (
        <Section title="By category">
          <Rows label="By category">
            <Row
              title="Models"
              trailing={<span className="text-row font-semibold tabular-nums">{costLabel(report.by_category.llm)}</span>}
            />
            <Row
              title="Web search"
              trailing={<span className="text-row font-semibold tabular-nums">{costLabel(report.by_category.search)}</span>}
            />
            <Row
              title="Computer"
              trailing={<span className="text-row font-semibold tabular-nums">{costLabel(report.by_category.computer)}</span>}
            />
          </Rows>
        </Section>
      )}

      {currentModels.length > 0 && (
        <Section title="By model">
          <Rows label="By model">
            {currentModels.map((item) => {
              const selected = selectedModel === item.model;
              const profile = modelProfileFor(item.model, settings);
              const isLocalOnly = item.local_calls > 0 && item.local_calls === callCount(item);
              const trailingLabel = isLocalOnly
                ? "Local"
                : costLabel(item, item.unpriced_calls ? "Cost unavailable" : "In progress");
              return (
                <li key={item.model}>
                  <button
                    type="button"
                    aria-pressed={selected}
                    onClick={() => setSelectedModel(selected ? null : item.model)}
                    ref={(button) => {
                      if (button) modelButtons.current.set(item.model, button);
                      else modelButtons.current.delete(item.model);
                    }}
                    className={cn(
                      "flex min-h-14 w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-fill-1 focus-visible:bg-fill-1",
                      selected && "bg-fill-1",
                    )}
                  >
                    <ProviderBrandIcon
                      provider={profile?.backend}
                      model={item.model}
                      backend={isLocalOnly ? "home" : profile?.backend}
                    />
                    <span
                      className={cn(
                        "min-w-0 flex-1 text-row font-semibold [overflow-wrap:anywhere] text-ink",
                        selected && "text-accent-text",
                      )}
                    >
                      {modelLabel(item.model, settings)}
                    </span>
                    <span
                      className={cn(
                        "shrink-0 text-row font-semibold tabular-nums",
                        (!hasRecordedCost(item) || isLocalOnly) && "text-meta font-medium text-ink-2",
                      )}
                    >
                      {trailingLabel}
                    </span>
                    {selected && <Check size={15} className="shrink-0 text-accent-text" aria-hidden />}
                  </button>
                </li>
              );
            })}
          </Rows>
        </Section>
      )}
    </>
  );
}

function CostChart({ report, selectedModel }: { report: CostReport; selectedModel: string | null }) {
  const [selected, setSelected] = useState<string | null>(null);
  const bars = useRef<HTMLDivElement>(null);
  const labels = bucketLabels(report);
  const values = report.buckets.map((bucket) =>
    selectedModel === null ? bucket.totals : bucket.models.find((item) => item.model === selectedModel) ?? NO_COST,
  );
  const found = report.buckets.findIndex((bucket) => bucket.start_at === selected);
  const active = found < 0 ? report.buckets.length - 1 : found;
  const peak = Math.max(...values.map((value) => value.cost_usd), 0);
  const step = axisStep(peak);
  const max = step * 3;
  const tickIndices = Array.from(
    new Set([0, Math.round((values.length - 1) / 3), Math.round(((values.length - 1) * 2) / 3), values.length - 1]),
  );

  function select(index: number, focus = false) {
    const next = Math.min(Math.max(index, 0), values.length - 1);
    setSelected(report.buckets[next].start_at);
    if (focus) bars.current?.querySelectorAll<HTMLButtonElement>("button")[next]?.focus();
  }

  function handleKeys(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next =
      event.key === "ArrowLeft"
        ? index - 1
        : event.key === "ArrowRight"
          ? index + 1
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? values.length - 1
              : undefined;
    if (next === undefined) return;
    event.preventDefault();
    select(next, true);
  }

  return (
    <>
      <div
        className="mt-6 grid grid-cols-[auto_minmax(0,1fr)] gap-x-2"
        role="group"
        aria-label={`${report.bucket_size === "hour" ? "Hourly" : "Daily"} recorded spend`}
      >
        <div aria-hidden className="flex h-32 flex-col justify-between text-right text-caption tabular-nums text-ink-3 sm:h-36">
          {[3, 2, 1, 0].map((tick) => (
            <span key={tick} className={cn("leading-none", !peak && tick > 0 && "invisible")}>
              {axisAmount(tick * step)}
            </span>
          ))}
        </div>
        <div className="relative h-32 sm:h-36">
          <div aria-hidden className="pointer-events-none absolute inset-x-0 inset-y-1.5 flex flex-col justify-between">
            {[3, 2, 1, 0].map((tick) => (
              <div key={tick} className={cn("border-t border-hairline", !peak && tick > 0 && "invisible")} />
            ))}
          </div>
          <div
            ref={bars}
            className="absolute inset-x-0 inset-y-1.5 grid"
            style={{ gridTemplateColumns: `repeat(${values.length}, minmax(0, 1fr))` }}
          >
            {values.map((value, index) => {
              const text = periodLabel(value);
              const status = value.unpriced_calls ? `, ${plural(value.unpriced_calls, "call")} without cost` : "";
              const pending = value.pending_calls ? `, ${plural(value.pending_calls, "call")} being recorded` : "";
              return (
                <button
                  key={report.buckets[index].start_at}
                  type="button"
                  aria-label={`${labels[index].detail}, ${text}${status}${pending}`}
                  aria-pressed={index === active}
                  tabIndex={index === active ? 0 : -1}
                  onClick={() => select(index)}
                  onKeyDown={(event) => handleKeys(event, index)}
                  className={cn(
                    "relative flex h-full min-w-0 items-end justify-center rounded-t focus-visible:outline-2 focus-visible:outline-accent-signal",
                    index === active && "bg-accent-text/5",
                  )}
                >
                  <span
                    className={cn(
                      "w-[65%] max-w-16 rounded-t-sm",
                      value.cost_usd > 0 && "min-h-[2px]",
                      index === active ? "bg-accent-text" : "bg-accent-text/30",
                    )}
                    style={{ height: `${Math.max(0, (value.cost_usd / max) * 100)}%` }}
                  />
                  {value.unpriced_calls > 0 && <span aria-hidden className="absolute -bottom-2.5 size-1.5 rounded-full bg-warning" />}
                </button>
              );
            })}
          </div>
        </div>
        <div />
        <div aria-hidden className="relative mt-3 h-5 text-caption text-ink-3">
          {tickIndices.map((index, i) => (
            <span
              key={index}
              className={cn("absolute whitespace-nowrap", i === 0 ? "left-0" : i === tickIndices.length - 1 ? "right-0" : "-translate-x-1/2")}
              style={i === 0 || i === tickIndices.length - 1 ? undefined : { left: `${((index + 0.5) / values.length) * 100}%` }}
            >
              {labels[index].axis}
            </span>
          ))}
        </div>
      </div>
      <div className="mt-3 flex min-h-13 items-center justify-between gap-2 border-t border-hairline text-meta">
        <div aria-live="polite" className="flex flex-wrap items-baseline gap-x-2">
          <span className="text-ink-2">{labels[active].detail}</span>
          <strong className="tabular-nums">{periodLabel(values[active])}</strong>
          {values[active].unpriced_calls > 0 && (
            <span className="text-warning">{plural(values[active].unpriced_calls, "call")} without cost</span>
          )}
          {values[active].pending_calls > 0 && periodLabel(values[active]) !== "In progress" && (
            <span className="text-ink-2">{plural(values[active].pending_calls, "call")} in progress</span>
          )}
        </div>
        <div className="-mr-2 flex shrink-0">
          <IconButton label="Previous period" size={36} tip={false} disabled={active === 0} onClick={() => select(active - 1)}>
            <ChevronLeft size={18} aria-hidden />
          </IconButton>
          <IconButton
            label="Next period"
            size={36}
            tip={false}
            disabled={active === values.length - 1}
            onClick={() => select(active + 1)}
          >
            <ChevronRight size={18} aria-hidden />
          </IconButton>
        </div>
      </div>
    </>
  );
}
