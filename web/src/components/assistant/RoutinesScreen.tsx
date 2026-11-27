"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Pause, Play, Plus, TimerReset, Zap } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistantConfig } from "@/lib/assistant/context";
import { cadence, money, relative, when } from "@/lib/assistant/format";
import { RULE_EFFECT_META } from "@/lib/assistant/permissions";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { Automation, Occurrence, PermissionRule } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import {
  Confirm,
  Empty,
  Failure,
  Loading,
  NoriLine,
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

export function RoutinesContent() {
  const invalidate = useResourceInvalidation();
  const { has } = useAssistantConfig();
  const list = useResource<List<Automation>>(has("automations") ? "/automations" : null, { poll: 30_000 });
  const action = useAction();

  if (!has("automations")) return <NotYet what="Automations" />;

  const items = list.data?.items ?? [];
  const active = items
    .filter((item) => item.status !== "cancelled" && item.trigger.kind !== "once")
    .sort((a, b) => Date.parse(a.next_at ?? "9999") - Date.parse(b.next_at ?? "9999"));
  const next = active.find((item) => item.status === "enabled" && item.next_at);

  const quickAct = (item: Automation, kind: "run_now" | "pause" | "resume") =>
    action.run(
      `${kind}:${item.id}`,
      async () => {
        await mutate<Automation>(`/automations/${encodeURIComponent(item.id)}/actions`, {
          request_id: newRequestID(),
          expected_revision: item.revision,
          value: { action: kind },
        });
        invalidate("/automations", "/work", "/assistant/summary");
      },
      kind === "run_now" ? `Started ${item.title}.` : kind === "pause" ? `Paused ${item.title}.` : `Resumed ${item.title}.`,
    );

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        {next ? (
          <NoriLine>Next up: {next.title}, {relative(next.next_at)}.</NoriLine>
        ) : active.length ? (
          <NoriLine>All automations are paused.</NoriLine>
        ) : (
          <div />
        )}
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
        <Rows label="Automations">
          {active.map((item) => {
            const state = stateOf(item);
            const isEnabled = item.status === "enabled" && !spent(item);
            const busy = action.pending?.endsWith(`:${item.id}`);
            return (
              <li key={item.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <Link href={`/routines/${encodeURIComponent(item.id)}`} className="min-w-0 flex-1 hover:opacity-80">
                  <div className="flex items-center gap-2">
                    <span className={cn("truncate text-row font-semibold", isEnabled ? "text-ink" : "text-ink-2")}>
                      {item.title}
                    </span>
                    {item.last_occurrence?.status === "failed" ? (
                      <Pill tone="danger">Last run failed</Pill>
                    ) : !isEnabled ? (
                      <Pill tone={state.tone}>{state.label}</Pill>
                    ) : null}
                  </div>
                  <p className="mt-0.5 text-meta text-ink-2">{cadence(item.trigger)}</p>
                  {isEnabled && item.next_at && (
                    <p className="mt-0.5 text-caption font-medium text-accent-text">Next {relative(item.next_at)}</p>
                  )}
                </Link>
                <div className="flex shrink-0 items-center gap-1.5">
                  <button
                    type="button"
                    disabled={!!busy}
                    onClick={() => void quickAct(item, "run_now")}
                    className="t-fast inline-flex h-8 items-center gap-1.5 rounded-full bg-fill-2 px-3 text-caption font-semibold text-accent-text hover:bg-accent-muted disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled"
                  >
                    <Play size={10} fill="currentColor" aria-hidden />
                    Run
                  </button>
                  {!spent(item) && (
                    <button
                      type="button"
                      disabled={!!busy}
                      onClick={() => void quickAct(item, isEnabled ? "pause" : "resume")}
                      className="t-fast inline-flex h-8 items-center rounded-full bg-fill-1 px-3 text-caption font-medium text-ink-2 hover:bg-fill-2 hover:text-ink disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled"
                    >
                      {isEnabled ? "Pause" : "Resume"}
                    </button>
                  )}
                </div>
              </li>
            );
          })}
        </Rows>
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
