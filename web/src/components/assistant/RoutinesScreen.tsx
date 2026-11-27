"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Clock, Pause, Play, Plus, TimerReset, X, Zap } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistantConfig } from "@/lib/assistant/context";
import { cadence, compactCadence, money, when } from "@/lib/assistant/format";
import { RULE_EFFECT_META } from "@/lib/assistant/permissions";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { Automation, Occurrence, PermissionRule } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import {
  Confirm,
  Empty,
  Failure,
  Loading,
  Notice,
  NotYet,
  Pill,
  Row,
  Rows,
  Screen,
  Section,
  type Tone,
} from "./ui";

export { frequencyOf, RoutineEditorScreen, triggerFrom } from "./RoutineEditor";

type List<T> = { items: T[] };

const STATUS: Record<Automation["status"], { tone: Tone; label: string }> = {
  enabled: { tone: "ok", label: "On" },
  paused: { tone: "neutral", label: "Paused" },
  cancelled: { tone: "neutral", label: "Cancelled" },
};
const OCCURRENCE: Record<Occurrence["status"], { tone: Tone; label: string }> = {
  pending: { tone: "neutral", label: "Due" },
  accepted: { tone: "neutral", label: "Starting" },
  running: { tone: "accent", label: "Running" },
  completed: { tone: "ok", label: "Done" },
  failed: { tone: "danger", label: "Failed" },
  cancelled: { tone: "neutral", label: "Cancelled" },
  expired: { tone: "warning", label: "Expired" },
  skipped: { tone: "warning", label: "Skipped" },
};
const REASON: Record<NonNullable<Occurrence["reason"]>, string> = {
  missed_window: "Nori was offline past this run’s window.",
  paused: "The routine was paused.",
  superseded: "An edit replaced this run.",
  max_active_runs: "The previous run was still going.",
};
const DELIVERY: Record<string, string> = {
  pending: "Waiting to send",
  attempting: "Sending",
  accepted: "Sent",
  delivered: "Delivered",
  failed: "Not delivered",
  uncertain: "Delivery unknown",
};

function snoozeOptions(): [string, string][] {
  const at = (days: number, hour: number) => {
    const d = new Date();
    d.setDate(d.getDate() + days);
    d.setHours(hour, 0, 0, 0);
    return d.toISOString();
  };
  const monday = (8 - new Date().getDay()) % 7 || 7;
  return [
    ["For an hour", new Date(Date.now() + 3_600_000).toISOString()],
    ["Until tomorrow", at(1, 8)],
    ["Until next week", at(monday, 8)],
  ];
}

const spent = (item: Automation) => item.status === "enabled" && !item.next_at && item.trigger.kind === "once";

function stateOf(item: Automation, long = false) {
  if (spent(item)) return { tone: "neutral" as Tone, label: "Done" };
  if (item.status === "enabled" && item.snoozed_until && Date.parse(item.snoozed_until) > Date.now()) {
    return { tone: "neutral" as Tone, label: long ? `Snoozed until ${when(item.snoozed_until)}` : "Snoozed" };
  }
  return STATUS[item.status];
}

export function AutomationRows({
  items,
  confirmingId,
  onAskConfirm,
  onCancelConfirm,
  onRemove,
}: {
  items: Automation[];
  confirmingId: string | null;
  onAskConfirm: (id: string) => void;
  onCancelConfirm: () => void;
  onRemove: (item: Automation) => void;
}) {
  return (
    <ul
      aria-label="Automations"
      className="divide-y divide-hairline overflow-hidden rounded-card border border-card-border bg-surface"
    >
      {items.map((item) => {
        const isEnabled = item.status === "enabled" && !spent(item);
        const isConfirming = confirmingId === item.id;
        return (
          <li key={item.id} data-automation-row={item.id}>
            <div className="group flex min-h-[50px] items-center gap-3 px-4 py-3 transition-colors hover:bg-fill-1">
              <span className="grid size-5 shrink-0 place-items-center">
                <Clock size={15} strokeWidth={2} className="text-scheduled" aria-hidden />
              </span>
              <Link
                href={`/routines/${encodeURIComponent(item.id)}`}
                className={cn(
                  "min-w-0 flex-1 truncate text-row font-semibold hover:opacity-80",
                  isEnabled ? "text-ink" : "text-ink-2",
                )}
              >
                {item.title}
              </Link>
              <div className="flex shrink-0 items-center gap-2" onClick={(event) => event.stopPropagation()}>
                {isConfirming ? (
                  <>
                    <button
                      type="button"
                      onClick={() => onRemove(item)}
                      className="inline-flex h-7 items-center rounded-full bg-danger-bg px-2.5 text-caption font-semibold text-danger transition-opacity hover:opacity-90"
                    >
                      Remove
                    </button>
                    <button
                      type="button"
                      onClick={onCancelConfirm}
                      className="inline-flex h-7 items-center rounded-full bg-fill-1 px-2.5 text-caption font-semibold text-ink-2 hover:bg-fill-2 hover:text-ink"
                    >
                      Keep
                    </button>
                  </>
                ) : (
                  <>
                    <span className="text-caption font-medium whitespace-nowrap text-scheduled">
                      {compactCadence(item.trigger)}
                    </span>
                    <button
                      type="button"
                      title="Remove automation"
                      aria-label={`Remove ${item.title}`}
                      onClick={() => onAskConfirm(item.id)}
                      className="grid size-[26px] place-items-center rounded-full text-ink-3 opacity-60 transition-[opacity,background-color,color] hover:bg-danger-bg hover:text-danger hover:opacity-100 focus-visible:opacity-100 sm:opacity-0 sm:group-hover:opacity-100"
                    >
                      <X size={14} strokeWidth={2.1} aria-hidden />
                    </button>
                  </>
                )}
              </div>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

export function RoutinesContent() {
  const invalidate = useResourceInvalidation();
  const { has } = useAssistantConfig();
  const list = useResource<List<Automation>>(has("automations") ? "/automations" : null, { poll: 30_000 });
  const action = useAction();
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  const [dismissedIds, setDismissedIds] = useState<Set<string>>(() => new Set());

  if (!has("automations")) return <NotYet what="Automations" />;

  const items = list.data?.items ?? [];
  const active = items
    .filter((item) => item.status !== "cancelled" && item.trigger.kind !== "once" && !dismissedIds.has(item.id))
    .sort((a, b) => Date.parse(a.next_at ?? "9999") - Date.parse(b.next_at ?? "9999"));

  const removeAutomation = (item: Automation) => {
    setConfirmingId(null);
    setDismissedIds((prev) => new Set(prev).add(item.id));
    void action.run(
      `cancel:${item.id}`,
      async () => {
        try {
          await mutate<Automation>(`/automations/${encodeURIComponent(item.id)}/actions`, {
            request_id: newRequestID(),
            expected_revision: item.revision,
            value: { action: "cancel" },
          });
          invalidate("/automations", "/work", "/assistant/summary");
        } catch (err) {
          setDismissedIds((prev) => {
            const next = new Set(prev);
            next.delete(item.id);
            return next;
          });
          throw err;
        }
      },
      `Removed ${item.title}.`,
    );
  };

  return (
    <div className="space-y-4" onClick={() => confirmingId && setConfirmingId(null)}>
      <div className="flex items-center justify-end">
        <Link
          href="/routines/new"
          className="inline-flex h-8 items-center gap-1.5 rounded-full bg-accent px-3.5 text-meta font-semibold text-on-accent transition-colors hover:bg-accent-hover"
        >
          <Plus size={14} aria-hidden />
          New automation
        </Link>
      </div>

      {list.error && <Failure error={list.error} onRetry={() => void list.reload()} />}
      {action.error && <Notice tone="danger">{action.error}</Notice>}

      {list.loading ? (
        <Loading />
      ) : active.length ? (
        <AutomationRows
          items={active}
          confirmingId={confirmingId}
          onAskConfirm={setConfirmingId}
          onCancelConfirm={() => setConfirmingId(null)}
          onRemove={removeAutomation}
        />
      ) : (
        <Empty title="No automations yet">
          Ask Nori in Chat to set up recurring briefs, monitors, or scheduled checks.
        </Empty>
      )}
    </div>
  );
}

export function RoutinesScreen() {
  return (
    <Screen title="Automations" back="/work?tab=automations" backLabel="Work">
      <RoutinesContent />
    </Screen>
  );
}

export function RoutineDetailScreen({ id }: { id: string }) {
  const invalidate = useResourceInvalidation();
  const routine = useResource<Automation>(`/automations/${encodeURIComponent(id)}`, { poll: 15_000 });
  const occurrences = useResource<List<Occurrence>>(`/automations/${encodeURIComponent(id)}/occurrences`, { poll: 15_000 });
  const action = useAction();
  const router = useRouter();
  const [started, setStarted] = useState(false);
  const item = routine.data;

  if (routine.loading) {
    return (
      <Screen title="Routine" back="/work?tab=routines" backLabel="Work">
        <Loading />
      </Screen>
    );
  }
  if (!item) {
    return (
      <Screen title="Routine" back="/work?tab=routines" backLabel="Work">
        <Failure error={routine.error ?? "This routine doesn’t exist."} />
      </Screen>
    );
  }

  const act = (value: Record<string, unknown>, label: string) =>
    action.run(
      label,
      async () => {
        try {
          const receipt = await mutate<Automation>(`/automations/${encodeURIComponent(id)}/actions`, {
            request_id: newRequestID(),
            expected_revision: item.revision,
            value,
          });
          routine.apply(receipt.resource as Automation);
          invalidate("/automations", "/work", "/assistant/summary");
          if (receipt.status === "queued" && receipt.work?.task_id) {
            router.push(`/work/${encodeURIComponent(receipt.work.task_id)}`);
          } else if (value.action === "run_now") {
            setStarted(true);
            void occurrences.reload();
          }
        } catch (error) {
          if (error instanceof APIError && error.stale && error.body?.current) {
            routine.apply(error.body.current as Automation);
          }
          throw error;
        }
      },
      ({
        pause: "Routine paused.",
        resume: "Routine resumed.",
        snooze: "Routine snoozed.",
        cancel: "Routine cancelled.",
        run_now: "Run started.",
      } as Record<string, string>)[value.action as string],
    );

  const state = stateOf(item, true);
  return (
    <Screen
      title={item.title}
      back="/work?tab=routines"
      backLabel="Work"
      line={
        item.status === "enabled" && item.next_at
          ? `Next run ${when(item.next_at, item.trigger.kind === "recurring" ? item.trigger.timezone : undefined)}.`
          : spent(item)
            ? "This one-time routine has run. It won’t run again."
            : item.status === "paused"
              ? "Paused. Nothing runs until you resume it."
              : "Cancelled."
      }
      actions={
        state.label !== "On" ? <Pill tone={state.tone}>{state.label}</Pill> : undefined
      }
    >
      {action.error && (
        <div className="mb-4">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}
      {started && !action.error && (
        <div className="mb-4">
          <Notice tone="accent">Starting a run now. It shows in History and Work when it begins.</Notice>
        </div>
      )}
      {item.status !== "cancelled" && (
        <div className="flex flex-wrap gap-2">
          <Button variant="primary" disabled={!!action.pending} onClick={() => void act({ action: "run_now" }, "run")}>
            <Zap size={15} aria-hidden />
            {action.pending === "run" ? "Starting…" : "Run now"}
          </Button>
          {spent(item) ? null : item.status === "enabled" ? (
            <Button disabled={!!action.pending} onClick={() => void act({ action: "pause" }, "pause")}>
              <Pause size={14} aria-hidden />
              Pause
            </Button>
          ) : (
            <Button disabled={!!action.pending} onClick={() => void act({ action: "resume" }, "resume")}>
              <Play size={14} aria-hidden />
              Resume
            </Button>
          )}
          {item.status === "enabled" && !spent(item) && (
            <DropdownMenu.Root>
              <DropdownMenu.Trigger asChild>
                <Button disabled={!!action.pending}>
                  <TimerReset size={14} aria-hidden />
                  Snooze
                </Button>
              </DropdownMenu.Trigger>
              <DropdownMenu.Portal>
                <DropdownMenu.Content sideOffset={6} className="anim-pop z-50 min-w-48 rounded-tile bg-surface p-1 shadow-pop">
                  {snoozeOptions().map(([label, until]) => (
                    <DropdownMenu.Item
                      key={label}
                      onSelect={() => void act({ action: "snooze", until }, "snooze")}
                      className="flex h-10 cursor-pointer items-center rounded-chip px-3 text-row outline-none data-[highlighted]:bg-fill-1"
                    >
                      {label}
                    </DropdownMenu.Item>
                  ))}
                </DropdownMenu.Content>
              </DropdownMenu.Portal>
            </DropdownMenu.Root>
          )}
          <Link
            href={`/routines/${encodeURIComponent(id)}/edit`}
            className="inline-flex h-9 items-center rounded-full px-4 text-row-title font-medium text-ink shadow-[inset_0_0_0_1px_var(--hairline-strong)] hover:bg-fill-1"
          >
            Edit
          </Link>
          <Confirm
            danger
            title="Cancel this routine?"
            body="It won’t run again. Its history stays here. Work already started keeps going until you stop it in Work."
            confirm="Cancel routine"
            onConfirm={() => void act({ action: "cancel" }, "cancel")}
            trigger={
              <Button variant="danger-quiet" disabled={!!action.pending}>
                Cancel routine
              </Button>
            }
          />
        </div>
      )}
      <Section title="What it does">
        <p className="rounded-card border border-card-border bg-surface p-4 text-body whitespace-pre-wrap">{item.instruction}</p>
      </Section>
      <Section title="May do without asking">
        <Authority ids={item.authority_rule_ids} />
      </Section>
      <Section title="History">
        {occurrences.loading ? (
          <Loading />
        ) : occurrences.data?.items.length ? (
          <Rows label="Runs">
            {occurrences.data.items.map((occurrence) => {
              const status = OCCURRENCE[occurrence.status];
              const showStatusPill = occurrence.status !== "completed";
              const deliveries = (occurrence.deliveries ?? []).map(
                (delivery) => `${DELIVERY[delivery.state] ?? delivery.state}${delivery.acknowledged_at ? ", seen" : ""}`,
              );
              return (
                <Row
                  key={occurrence.id}
                  href={occurrence.task_ids[0] ? `/work/${encodeURIComponent(occurrence.task_ids[0])}` : undefined}
                  title={when(occurrence.scheduled_at)}
                  detail={[
                    occurrence.reason ? REASON[occurrence.reason] : occurrence.error?.message ?? occurrence.summary,
                    ...deliveries,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                  trailing={showStatusPill ? <Pill tone={status.tone}>{status.label}</Pill> : undefined}
                />
              );
            })}
          </Rows>
        ) : (
          <Empty>No runs yet.</Empty>
        )}
      </Section>
      <Section title="Details">
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
          <dt className="text-ink-3">When</dt>
          <dd>
            {cadence(item.trigger)}
            {item.trigger.kind === "recurring" ? ` (${item.trigger.timezone})` : ""}
          </dd>
          <dt className="text-ink-3">If missed</dt>
          <dd>
            {{ latest: "Runs once when Nori is back", skip: "Skipped and shown above", all: "Every missed run happens" }[item.missed_run]}
          </dd>
          <dt className="text-ink-3">Budget</dt>
          <dd>{money(item.budget_usd)} per run</dd>
          {item.end_at && (
            <>
              <dt className="text-ink-3">Ends</dt>
              <dd>{when(item.end_at)}</dd>
            </>
          )}
        </dl>
      </Section>
    </Screen>
  );
}

function Authority({ ids }: { ids: string[] }) {
  const rules = useResource<List<PermissionRule>>(ids.length ? "/permissions" : null, { poll: 0 });
  if (!ids.length) return <p className="text-body text-ink-2">Asks before every action.</p>;
  const mine = (rules.data?.items ?? []).filter((rule) => ids.includes(rule.id));
  return rules.loading ? (
    <Loading />
  ) : (
    <Rows>
      {mine.map((rule) => {
        const effectMeta = RULE_EFFECT_META[rule.effect];
        return (
          <Row
            key={rule.id}
            href={`/settings/permissions#${rule.id}`}
            title={rule.summary ?? rule.title ?? "Permission"}
            trailing={<Pill tone={effectMeta.tone}>{effectMeta.label}</Pill>}
          />
        );
      })}
    </Rows>
  );
}
