"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { Button } from "@/components/ui/primitives";
import { mutate, newRequestID, previewAutomation } from "@/lib/assistant/api";
import { useAssistantConfig } from "@/lib/assistant/context";
import { cadence, localDateString, localTimeString, scheduleDate, scheduleDayEnd, toValidISOString, when } from "@/lib/assistant/format";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type {
  Automation,
  AutomationEditable,
  AutomationPreview,
  Connection,
  Effect,
  MissedRun,
  PermissionRule,
  Preferences,
  Trigger,
} from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { Failure, Field, inputBase, inputClass, Loading, Notice, NotYet, Screen } from "./ui";

type List<T> = { items: T[] };
type Frequency = "once" | "daily" | "weekdays" | "weekly" | "monthly";

const WEEKDAYS = [
  ["MO", "Mon"],
  ["TU", "Tue"],
  ["WE", "Wed"],
  ["TH", "Thu"],
  ["FR", "Fri"],
  ["SA", "Sat"],
  ["SU", "Sun"],
] as const;

const NO_DESTINATIONS: string[] = [];

export function triggerFrom(
  frequency: Frequency,
  date: string,
  time: string,
  days: string[],
  timezone: string,
): Trigger | undefined {
  if (!date || !time) return undefined;
  if (frequency === "once") {
    const at = toValidISOString(`${date}T${time}`);
    return at ? { kind: "once", at } : undefined;
  }
  const rrule =
    frequency === "daily"
      ? "FREQ=DAILY"
      : frequency === "weekdays"
        ? "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"
        : frequency === "weekly"
          ? `FREQ=WEEKLY;BYDAY=${(days.length ? days : ["MO"]).join(",")}`
          : "FREQ=MONTHLY";
  return { kind: "recurring", local_start: `${date}T${time}`, timezone, rrule };
}

export function frequencyOf(
  trigger?: Trigger,
  now: Date = new Date(),
): { frequency: Frequency; date: string; time: string; days: string[] } {
  const today = localDateString(now);
  if (!trigger || trigger.kind === "event_relative" || trigger.kind === "watch") {
    return { frequency: "weekdays", date: today, time: "08:00", days: [] };
  }
  if (trigger.kind === "once") {
    const d = new Date(trigger.at);
    if (!Number.isFinite(d.getTime())) {
      return { frequency: "once", date: today, time: "08:00", days: [] };
    }
    return { frequency: "once", date: localDateString(d), time: localTimeString(d), days: [] };
  }
  const [date = today, rawTime = "08:00"] = trigger.local_start.split("T");
  const rule = Object.fromEntries(trigger.rrule.split(";").map((part) => part.split("=")));
  const days = (rule.BYDAY as string | undefined)?.split(",") ?? [];
  const frequency: Frequency =
    rule.FREQ === "DAILY"
      ? "daily"
      : rule.FREQ === "MONTHLY"
        ? "monthly"
        : days.join() === "MO,TU,WE,TH,FR"
          ? "weekdays"
          : "weekly";
  return { frequency, date, time: rawTime.slice(0, 5), days };
}

export function RoutineEditorScreen({ id }: { id?: string }) {
  const { has } = useAssistantConfig();
  const existing = useResource<Automation>(id ? `/automations/${encodeURIComponent(id)}` : null, { poll: 0 });
  const rules = useResource<List<PermissionRule>>(id ? "/permissions" : null, { poll: 0 });
  if (!has("automations")) {
    return (
      <Screen title="New routine" back="/work?tab=routines" backLabel="Work">
        <NotYet what="Routines" />
      </Screen>
    );
  }
  if (id && (!existing.data || !rules.data)) {
    return (
      <Screen title="Edit routine" back={`/routines/${id}`}>
        {existing.error || rules.error ? <Failure error={existing.error ?? rules.error} /> : <Loading />}
      </Screen>
    );
  }
  const held = new Set(
    (rules.data?.items ?? [])
      .filter((rule) => rule.effect === "allow" && existing.data?.authority_rule_ids.includes(rule.id))
      .flatMap((rule) => rule.action_classes),
  );
  return <Editor key={existing.data?.revision ?? "new"} existing={existing.data} held={held} />;
}

function Editor({ existing, held }: { existing?: Automation; held?: Set<Effect> }) {
  const invalidate = useResourceInvalidation();
  const router = useRouter();
  const connections = useResource<List<Connection>>("/connections", { poll: 0 });
  const preferences = useResource<Preferences>("/preferences", { poll: 0 });
  const start = useMemo(() => frequencyOf(existing?.trigger), [existing?.trigger]);
  const [title, setTitle] = useState(existing?.title ?? "");
  const [instruction, setInstruction] = useState(existing?.instruction ?? "");
  const [frequency, setFrequency] = useState<Frequency>(start.frequency);
  const [date, setDate] = useState(start.date);
  const [time, setTime] = useState(start.time);
  const [days, setDays] = useState<string[]>(start.days);
  const [timezone, setTimezone] = useState(existing?.trigger.kind === "recurring" ? existing.trigger.timezone : "");
  const [accounts, setAccounts] = useState<string[]>(existing?.connection_ids ?? []);
  const [writes, setWrites] = useState(!!held?.has("reversible_write"));
  const [sends, setSends] = useState(!!held?.has("sensitive_send"));
  const [missed, setMissed] = useState<MissedRun>(existing?.missed_run ?? "latest");
  const [budget, setBudget] = useState(String(existing?.budget_usd ?? 0.5));
  const zone = timezone || preferences.data?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [endAt, setEndAt] = useState(() => scheduleDate(existing?.end_at, zone));
  const [preview, setPreview] = useState<AutomationPreview>();
  const [previewError, setPreviewError] = useState("");
  const action = useAction();
  const chosenTargets = existing?.destination_ids ?? preferences.data?.default_destination_ids ?? NO_DESTINATIONS;
  const originalTrigger = existing?.trigger;
  const special = originalTrigger?.kind === "event_relative" || originalTrigger?.kind === "watch";

  const value = useMemo<AutomationEditable | undefined>(() => {
    const trigger = special ? originalTrigger : triggerFrom(frequency, date, time, days, zone);
    const end = endAt ? scheduleDayEnd(endAt, zone) : undefined;
    if (!trigger || !missed || (endAt && !end)) return undefined;
    const classes: Effect[] = ["read"];
    if (writes) classes.push("reversible_write");
    if (sends) classes.push("sensitive_send");
    return {
      title: title.trim() || "Routine",
      instruction: instruction.trim(),
      trigger,
      connection_ids: accounts,
      requested_authority: { action_classes: classes },
      budget_usd: Number(budget) || 0,
      max_active_runs: 1,
      missed_run: missed,
      execution_window_seconds: 3600,
      destination_ids: chosenTargets,
      ...(end ? { end_at: end } : {}),
    };
  }, [special, originalTrigger, frequency, date, time, days, zone, endAt, missed, writes, sends, title, instruction, accounts, budget, chosenTargets]);

  useEffect(() => {
    if (!value || !value.instruction) return;
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      try {
        const data = await previewAutomation(value, controller.signal);
        if (!controller.signal.aborted) {
          setPreview(data);
          setPreviewError("");
        }
      } catch (error) {
        if (!controller.signal.aborted) setPreviewError(error instanceof Error ? error.message : "Preview unavailable.");
      }
    }, 400);
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [value]);

  const save = () =>
    action.run("save", async () => {
      if (!value) throw new Error("Choose when it runs and what happens if a run is missed.");
      const receipt = existing
        ? await mutate<Automation>(
            `/automations/${encodeURIComponent(existing.id)}`,
            { request_id: newRequestID(), expected_revision: existing.revision, value },
            "PUT",
          )
        : await mutate<Automation>("/automations", { request_id: newRequestID(), expected_revision: 0, value });
      invalidate("/automations", "/permissions");
      router.push(`/routines/${encodeURIComponent((receipt.resource as Automation).id)}`);
    });

  const writable = (connections.data?.items ?? []).filter((connection) => connection.status !== "disconnected");
  return (
    <Screen
      title={existing ? "Edit routine" : "New routine"}
      back={existing ? `/routines/${existing.id}` : "/work?tab=routines"}
      backLabel={existing ? "Routine" : "Work"}
    >
      <form
        className="space-y-6"
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <Field label="Name">
          <input value={title} onChange={(event) => setTitle(event.target.value)} className={inputClass} placeholder="Morning briefing" required />
        </Field>
        <Field label="What Nori does each time">
          <textarea
            value={instruction}
            onChange={(event) => setInstruction(event.target.value)}
            rows={3}
            className={cn(inputClass, "resize-y")}
            placeholder="Summarize today’s agenda, urgent mail and anything due."
            required
          />
        </Field>
        {special ? (
          <Notice>{cadence(existing!.trigger)}. Change this kind of trigger by asking Nori in Chat.</Notice>
        ) : (
          <fieldset>
            <legend className="mb-1.5 text-meta font-medium">When</legend>
            <div className="flex flex-wrap gap-2">
              <select
                aria-label="How often"
                value={frequency}
                onChange={(event) => setFrequency(event.target.value as Frequency)}
                className={cn(inputBase, "w-auto")}
              >
                <option value="once">Once</option>
                <option value="daily">Every day</option>
                <option value="weekdays">Weekdays</option>
                <option value="weekly">Certain days</option>
                <option value="monthly">Monthly</option>
              </select>
              <input
                aria-label={frequency === "once" ? "Date" : "Starting"}
                type="date"
                value={date}
                onChange={(event) => setDate(event.target.value)}
                className={cn(inputBase, "w-auto")}
              />
              <input
                aria-label="Time"
                type="time"
                value={time}
                onChange={(event) => setTime(event.target.value)}
                className={cn(inputBase, "w-auto")}
              />
              {frequency !== "once" && (
                <input
                  aria-label="Time zone"
                  value={zone}
                  onChange={(event) => setTimezone(event.target.value)}
                  className={cn(inputBase, "w-56")}
                />
              )}
            </div>
            {frequency === "weekly" && (
              <div className="mt-2 flex flex-wrap gap-1" role="group" aria-label="Days">
                {WEEKDAYS.map(([code, label]) => (
                  <button
                    key={code}
                    type="button"
                    aria-pressed={days.includes(code)}
                    onClick={() => setDays(days.includes(code) ? days.filter((day) => day !== code) : [...days, code])}
                    className={cn(
                      "h-11 min-w-11 rounded-full border px-3 text-meta font-medium",
                      days.includes(code)
                        ? "border-chip-selected-border bg-chip-selected-bg text-chip-selected-fg"
                        : "border-transparent bg-fill-1 text-ink-2",
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
            )}
          </fieldset>
        )}
        <fieldset>
          <legend className="mb-1.5 text-meta font-medium">Accounts it can use</legend>
          {writable.length ? (
            <div className="flex flex-wrap gap-2">
              {writable.map((connection) => (
                <label
                  key={connection.id}
                  className={cn(
                    "flex min-h-11 cursor-pointer items-center gap-2 rounded-full border px-3 text-meta",
                    accounts.includes(connection.id) ? "border-accent bg-accent-subtle" : "border-hairline",
                  )}
                >
                  <input
                    type="checkbox"
                    className="accent-[var(--accent)]"
                    checked={accounts.includes(connection.id)}
                    onChange={() =>
                      setAccounts(
                        accounts.includes(connection.id)
                          ? accounts.filter((other) => other !== connection.id)
                          : [...accounts, connection.id],
                      )
                    }
                  />
                  {[connection.title, connection.account?.label].filter(Boolean).join(" · ")}
                </label>
              ))}
            </div>
          ) : (
            <p className="text-meta text-ink-2">
              No connected accounts.{" "}
              <Link href="/apps" className="text-accent-text hover:underline">
                Connect one
              </Link>
              .
            </p>
          )}
        </fieldset>
        <fieldset className="space-y-2">
          <legend className="mb-1.5 text-meta font-medium">Without asking, it may</legend>
          <label className="flex min-h-11 items-center gap-3 text-body">
            <input type="checkbox" checked disabled className="accent-[var(--accent)]" />
            Read from those accounts
          </label>
          <label className="flex min-h-11 items-center gap-3 text-body">
            <input
              type="checkbox"
              checked={writes}
              onChange={(event) => setWrites(event.target.checked)}
              className="accent-[var(--accent)]"
            />
            Make changes it can undo, like drafts and calendar edits
          </label>
          <label className="flex min-h-11 items-center gap-3 text-body">
            <input
              type="checkbox"
              checked={sends}
              onChange={(event) => setSends(event.target.checked)}
              className="accent-[var(--accent)]"
            />
            Send messages and invitations
          </label>
        </fieldset>
        <fieldset>
          <legend className="mb-1.5 text-meta font-medium">If Nori misses a run</legend>
          <div className="space-y-1">
            {(
              [
                ["latest", "Run once when Nori is back"],
                ["skip", "Skip it"],
                ["all", "Run every missed one"],
              ] as const
            ).map(([option, label]) => (
              <label key={option} className="flex min-h-11 items-center gap-3 text-body">
                <input
                  type="radio"
                  name="missed"
                  value={option}
                  checked={missed === option}
                  onChange={() => setMissed(option)}
                  className="accent-[var(--accent)]"
                  required
                />
                {label}
              </label>
            ))}
          </div>
        </fieldset>
        <div className="flex flex-wrap gap-4">
          <Field label="Budget per run (USD)">
            <input
              type="number"
              min={0}
              step="any"
              value={budget}
              onChange={(event) => setBudget(event.target.value)}
              className={cn(inputBase, "w-32")}
            />
          </Field>
          <Field label="Stop after (optional)">
            <input type="date" value={endAt} onChange={(event) => setEndAt(event.target.value)} className={cn(inputBase, "w-auto")} />
          </Field>
        </div>
        {(preview || previewError) && (
          <section aria-label="Preview" aria-live="polite" className="rounded-card border border-hairline-strong bg-fill-1 p-4">
            {previewError ? (
              <p className="text-meta text-danger">{previewError}</p>
            ) : (
              preview && (
                <>
                  <h2 className="text-row font-semibold">Next runs</h2>
                  <ul className="mt-1 text-body text-ink">
                    {preview.next_runs.slice(0, 5).map((run) => (
                      <li key={run}>{when(run, preview.timezone)}</li>
                    ))}
                  </ul>
                  {preview.authority_summary && (
                    <p className="mt-3 text-meta text-ink-2">Standing permission: {preview.authority_summary}</p>
                  )}
                  {preview.missed_run_explanation && (
                    <p className="mt-1 text-meta text-ink-2">{preview.missed_run_explanation}</p>
                  )}
                  {preview.warnings.map((warning) => (
                    <p key={warning.code} className="mt-2 text-meta text-warning">
                      {warning.message}
                    </p>
                  ))}
                </>
              )
            )}
          </section>
        )}
        {action.error && <Notice tone="danger">{action.error}</Notice>}
        <div className="flex gap-2">
          <Button type="submit" variant="primary" disabled={!!action.pending || !value || !instruction.trim()}>
            {action.pending ? "Saving…" : existing ? "Save changes" : "Create routine"}
          </Button>
          <Button variant="quiet" onClick={() => router.back()}>
            Cancel
          </Button>
        </div>
      </form>
    </Screen>
  );
}
