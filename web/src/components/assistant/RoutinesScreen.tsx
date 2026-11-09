"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { Pause, Play, Plus, TimerReset, Zap } from "lucide-react";
import { DropdownMenu } from "radix-ui";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { assistantURL } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { cadence, money, relative, when } from "@/lib/assistant/format";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { Automation, AutomationEditable, AutomationPreview, Connection, DeliveryDestination, Effect, MissedRun, Occurrence, PermissionRule, Preferences, Trigger } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { Confirm, Empty, Failure, Field, inputBase, inputClass, Loading, Notice, NotYet, Pill, RefreshButton, Row, Rows, Screen, Section, type Tone } from "./ui";

type List<T> = { items: T[] };

const STATUS: Record<Automation["status"], { tone: Tone; label: string }> = { enabled: { tone: "ok", label: "On" }, paused: { tone: "neutral", label: "Paused" }, cancelled: { tone: "neutral", label: "Cancelled" } };
const OCCURRENCE: Record<Occurrence["status"], { tone: Tone; label: string }> = {
  pending: { tone: "neutral", label: "Due" }, accepted: { tone: "neutral", label: "Starting" }, running: { tone: "accent", label: "Running" }, completed: { tone: "ok", label: "Done" },
  failed: { tone: "danger", label: "Failed" }, cancelled: { tone: "neutral", label: "Cancelled" }, expired: { tone: "warning", label: "Expired" }, skipped: { tone: "warning", label: "Skipped" },
};
const REASON: Record<NonNullable<Occurrence["reason"]>, string> = { missed_window: "Nori was offline past this run’s window.", paused: "The routine was paused.", superseded: "An edit replaced this run.", max_active_runs: "The previous run was still going." };
const DELIVERY: Record<string, string> = { pending: "Waiting to send", attempting: "Sending", accepted: "Handed to the provider", delivered: "Delivered", failed: "Not delivered", uncertain: "Delivery unknown" };

function snoozeOptions(): [string, string][] {
  const at = (days: number, hour: number) => { const d = new Date(); d.setDate(d.getDate() + days); d.setHours(hour, 0, 0, 0); return d.toISOString(); };
  const monday = (8 - new Date().getDay()) % 7 || 7;
  return [["For an hour", new Date(Date.now() + 3_600_000).toISOString()], ["Until tomorrow", at(1, 8)], ["Until next week", at(monday, 8)]];
}

function stateOf(item: Automation) {
  if (item.status === "enabled" && item.snoozed_until && Date.parse(item.snoozed_until) > Date.now()) return { tone: "neutral" as Tone, label: `Snoozed until ${when(item.snoozed_until)}` };
  return STATUS[item.status];
}

export function RoutinesScreen() {
  const { has } = useAssistant();
  const list = useResource<List<Automation>>(has("automations") ? "/automations" : null, { poll: 30_000 });
  if (!has("automations")) return <Screen title="Routines"><NotYet what="Routines and reminders" /></Screen>;
  const items = list.data?.items ?? [];
  const active = items.filter((item) => item.status !== "cancelled").sort((a, b) => Date.parse(a.next_at ?? "9999") - Date.parse(b.next_at ?? "9999"));
  const next = active.find((item) => item.status === "enabled" && item.next_at);
  return (
    <Screen title="Routines" line={next ? `Next: ${next.title}, ${relative(next.next_at)}.` : items.length ? "Nothing is scheduled." : undefined}
      actions={<Link href="/routines/new" className="inline-flex h-9 items-center gap-1.5 rounded-full bg-accent px-4 text-row-title font-medium text-on-accent hover:bg-accent-hover"><Plus size={15} aria-hidden />New routine</Link>}>
      {list.error && <div className="mb-4"><Failure error={list.error} onRetry={() => void list.reload()} /></div>}
      {list.loading ? <Loading /> : active.length ? <Rows label="Routines">{active.map((item) => {
        const state = stateOf(item);
        return <Row key={item.id} href={`/routines/${encodeURIComponent(item.id)}`} title={item.title}
          detail={[cadence(item.trigger), item.status === "enabled" && item.next_at ? `next ${relative(item.next_at)}` : "", item.last_occurrence?.status === "failed" ? "last run failed" : item.last_occurrence?.status === "skipped" ? "last run skipped" : ""].filter(Boolean).join(" · ")}
          trailing={<Pill tone={item.last_occurrence?.status === "failed" ? "danger" : state.tone}>{item.last_occurrence?.status === "failed" ? "Last run failed" : state.label}</Pill>} />;
      })}</Rows> : <Empty action={<Link href="/routines/new" className="text-meta font-medium text-accent-text hover:underline">New routine</Link>}>No routines yet. Ask Nori in Chat (“remind me before I leave”) or make one here.</Empty>}
    </Screen>
  );
}

export function RoutineDetailScreen({ id }: { id: string }) {
  const routine = useResource<Automation>(`/automations/${encodeURIComponent(id)}`, { poll: 15_000 });
  const occurrences = useResource<List<Occurrence>>(`/automations/${encodeURIComponent(id)}/occurrences`, { poll: 15_000 });
  const destinations = useResource<List<DeliveryDestination>>("/delivery-destinations", { poll: 0 });
  const action = useAction();
  const router = useRouter();
  const item = routine.data;
  if (routine.loading) return <Screen title="Routine" back="/routines"><Loading /></Screen>;
  if (!item) return <Screen title="Routine" back="/routines"><Failure error={routine.error ?? "This routine doesn’t exist."} /></Screen>;
  const act = (value: Record<string, unknown>, label: string) => action.run(label, async () => {
    try {
      const receipt = await mutate<Automation>(`/automations/${encodeURIComponent(id)}/actions`, { request_id: newRequestID(), expected_revision: item.revision, value });
      routine.apply(receipt.resource as Automation);
      invalidate("/automations", "/work", "/assistant/summary");
      if (receipt.status === "queued" && receipt.work?.task_id) router.push(`/work/${encodeURIComponent(receipt.work.task_id)}`);
    } catch (error) {
      if (error instanceof APIError && error.stale && error.body?.current) routine.apply(error.body.current as Automation);
      throw error;
    }
  });
  const state = stateOf(item);
  const destinationLabel = (destinationID: string) => destinations.data?.items.find((destination) => destination.id === destinationID)?.label ?? "a destination";
  return (
    <Screen title={item.title} back="/routines" line={item.status === "enabled" && item.next_at ? `Next run ${when(item.next_at, item.trigger.kind === "recurring" ? item.trigger.timezone : undefined)}.` : item.status === "paused" ? "Paused. Nothing runs until you resume it." : "Cancelled."}
      actions={<><Pill tone={state.tone}>{state.label}</Pill><RefreshButton onClick={() => { void routine.reload(); void occurrences.reload(); }} /></>}>
      {action.error && <div className="mb-4"><Notice tone="danger">{action.error}</Notice></div>}
      {item.status !== "cancelled" && <div className="flex flex-wrap gap-2">
        <Button variant="primary" disabled={!!action.pending} onClick={() => void act({ action: "run_now" }, "run")}><Zap size={15} aria-hidden />{action.pending === "run" ? "Starting…" : "Run now"}</Button>
        {item.status === "enabled" ? <Button disabled={!!action.pending} onClick={() => void act({ action: "pause" }, "pause")}><Pause size={14} aria-hidden />Pause</Button>
          : <Button disabled={!!action.pending} onClick={() => void act({ action: "resume" }, "resume")}><Play size={14} aria-hidden />Resume</Button>}
        {item.status === "enabled" && <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild><Button disabled={!!action.pending}><TimerReset size={14} aria-hidden />Snooze</Button></DropdownMenu.Trigger>
          <DropdownMenu.Portal><DropdownMenu.Content sideOffset={6} className="anim-pop z-50 min-w-48 rounded-tile bg-surface p-1 shadow-pop">
            {snoozeOptions().map(([label, until]) => <DropdownMenu.Item key={label} onSelect={() => void act({ action: "snooze", until }, "snooze")} className="flex h-10 cursor-pointer items-center rounded-chip px-3 text-row outline-none data-[highlighted]:bg-fill-1">{label}</DropdownMenu.Item>)}
          </DropdownMenu.Content></DropdownMenu.Portal>
        </DropdownMenu.Root>}
        <Link href={`/routines/${encodeURIComponent(id)}/edit`} className="inline-flex h-9 items-center rounded-full px-4 text-row-title font-medium text-ink shadow-[inset_0_0_0_1px_var(--hairline-strong)] hover:bg-fill-1">Edit</Link>
        <Confirm danger title="Cancel this routine?" body="It won’t run again. Its history stays here. Work already started keeps going until you stop it in Work." confirm="Cancel routine" onConfirm={() => void act({ action: "cancel" }, "cancel")}
          trigger={<Button variant="quiet" className="text-danger!" disabled={!!action.pending}>Cancel routine</Button>} />
      </div>}
      <Section title="What it does">
        <p className="rounded-card border border-hairline bg-surface p-4 text-body whitespace-pre-wrap">{item.instruction}</p>
        <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
          <dt className="text-ink-3">When</dt><dd>{cadence(item.trigger)}{item.trigger.kind === "recurring" ? ` (${item.trigger.timezone})` : ""}</dd>
          <dt className="text-ink-3">If missed</dt><dd>{{ latest: "Runs once when Nori is back", skip: "Skipped and shown below", all: "Every missed run happens" }[item.missed_run]}</dd>
          <dt className="text-ink-3">Sends to</dt><dd>{item.destination_ids.map(destinationLabel).join(", ") || "Nowhere; results stay in Library"}</dd>
          <dt className="text-ink-3">Budget</dt><dd>{money(item.budget_usd)} per run</dd>
          {item.end_at && <><dt className="text-ink-3">Ends</dt><dd>{when(item.end_at)}</dd></>}
          {item.goal_id && <><dt className="text-ink-3">For goal</dt><dd><Link className="text-accent-text hover:underline" href={`/goals/${encodeURIComponent(item.goal_id)}`}>Open goal</Link></dd></>}
        </dl>
      </Section>
      <Section title="May do without asking"><Authority ids={item.authority_rule_ids} /></Section>
      <Section title="History">
        {occurrences.loading ? <Loading /> : occurrences.data?.items.length ? <Rows label="Runs">{occurrences.data.items.map((occurrence) => {
          const status = OCCURRENCE[occurrence.status];
          const deliveries = (occurrence.deliveries ?? []).map((delivery) => `${destinationLabel(delivery.destination_id)}: ${DELIVERY[delivery.state] ?? delivery.state}${delivery.acknowledged_at ? ", seen" : ""}`);
          return <Row key={occurrence.id} href={occurrence.task_ids[0] ? `/work/${encodeURIComponent(occurrence.task_ids[0])}` : undefined} title={when(occurrence.scheduled_at)}
            detail={[occurrence.reason ? REASON[occurrence.reason] : occurrence.error?.message ?? occurrence.summary, ...deliveries].filter(Boolean).join(" · ")} trailing={<Pill tone={status.tone}>{status.label}</Pill>} />;
        })}</Rows> : <Empty>No runs yet.</Empty>}
      </Section>
    </Screen>
  );
}

function Authority({ ids }: { ids: string[] }) {
  const rules = useResource<List<PermissionRule>>(ids.length ? "/permissions" : null, { poll: 0 });
  if (!ids.length) return <p className="text-body text-ink-2">Nothing. It asks you before every action.</p>;
  const mine = (rules.data?.items ?? []).filter((rule) => ids.includes(rule.id));
  return rules.loading ? <Loading /> : <Rows>{mine.map((rule) => <Row key={rule.id} href={`/settings/permissions#${rule.id}`} title={rule.summary ?? rule.title ?? "Permission"} trailing={<Pill tone={rule.effect === "deny" ? "danger" : "neutral"}>{rule.effect === "deny" ? "Never" : "Allowed"}</Pill>} />)}</Rows>;
}

// ── Editor ──────────────────────────────────────────────────────

type Frequency = "once" | "daily" | "weekdays" | "weekly" | "monthly";
const WEEKDAYS = [["MO", "Mon"], ["TU", "Tue"], ["WE", "Wed"], ["TH", "Thu"], ["FR", "Fri"], ["SA", "Sat"], ["SU", "Sun"]] as const;

function triggerFrom(frequency: Frequency, date: string, time: string, days: string[], timezone: string): Trigger | undefined {
  if (!date || !time) return undefined;
  if (frequency === "once") return { kind: "once", at: new Date(`${date}T${time}`).toISOString() };
  const rrule = frequency === "daily" ? "FREQ=DAILY" : frequency === "weekdays" ? "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR" : frequency === "weekly" ? `FREQ=WEEKLY;BYDAY=${(days.length ? days : ["MO"]).join(",")}` : "FREQ=MONTHLY";
  return { kind: "recurring", local_start: `${date}T${time}`, timezone, rrule };
}

function frequencyOf(trigger?: Trigger): { frequency: Frequency; date: string; time: string; days: string[] } {
  const today = new Date().toISOString().slice(0, 10);
  if (!trigger || trigger.kind === "event_relative" || trigger.kind === "watch") return { frequency: "weekdays", date: today, time: "08:00", days: [] };
  if (trigger.kind === "once") { const d = new Date(trigger.at); return { frequency: "once", date: d.toISOString().slice(0, 10), time: d.toTimeString().slice(0, 5), days: [] }; }
  const [date, time] = trigger.local_start.split("T");
  const rule = Object.fromEntries(trigger.rrule.split(";").map((part) => part.split("=")));
  const days = (rule.BYDAY as string | undefined)?.split(",") ?? [];
  const frequency: Frequency = rule.FREQ === "DAILY" ? "daily" : rule.FREQ === "MONTHLY" ? "monthly" : days.join() === "MO,TU,WE,TH,FR" ? "weekdays" : "weekly";
  return { frequency, date, time: time.slice(0, 5), days };
}

export function RoutineEditorScreen({ id }: { id?: string }) {
  const { has } = useAssistant();
  const existing = useResource<Automation>(id ? `/automations/${encodeURIComponent(id)}` : null, { poll: 0 });
  const rules = useResource<List<PermissionRule>>(id ? "/permissions" : null, { poll: 0 });
  if (!has("automations")) return <Screen title="New routine" back="/routines"><NotYet what="Routines" /></Screen>;
  if (id && (!existing.data || !rules.data)) return <Screen title="Edit routine" back={`/routines/${id}`}>{existing.error || rules.error ? <Failure error={existing.error ?? rules.error} /> : <Loading />}</Screen>;
  // Editing starts from the standing permission the routine already holds, so saving never narrows or widens it silently.
  const held = new Set((rules.data?.items ?? []).filter((rule) => rule.effect === "allow" && existing.data?.authority_rule_ids.includes(rule.id)).flatMap((rule) => rule.action_classes));
  return <Editor key={existing.data?.revision ?? "new"} existing={existing.data} held={held} />;
}

function Editor({ existing, held }: { existing?: Automation; held?: Set<Effect> }) {
  const router = useRouter();
  const connections = useResource<List<Connection>>("/connections", { poll: 0 });
  const destinations = useResource<List<DeliveryDestination>>("/delivery-destinations", { poll: 0 });
  const preferences = useResource<Preferences>("/preferences", { poll: 0 });
  const start = frequencyOf(existing?.trigger);
  const [title, setTitle] = useState(existing?.title ?? "");
  const [instruction, setInstruction] = useState(existing?.instruction ?? "");
  const [frequency, setFrequency] = useState<Frequency>(start.frequency);
  const [date, setDate] = useState(start.date);
  const [time, setTime] = useState(start.time);
  const [days, setDays] = useState<string[]>(start.days);
  const [timezone, setTimezone] = useState(existing?.trigger.kind === "recurring" ? existing.trigger.timezone : "");
  const [accounts, setAccounts] = useState<string[]>(existing?.connection_ids ?? []);
  const [writes, setWrites] = useState(!!held?.has("reversible_write"));
  // D2 is pending: unattended sends stay off unless the user turns this on for this routine.
  const [sends, setSends] = useState(!!held?.has("sensitive_send"));
  // D3 is pending: the user chooses; there is no hidden default.
  const [missed, setMissed] = useState<MissedRun | "">(existing?.missed_run ?? "");
  const [targets, setTargets] = useState<string[] | undefined>(existing?.destination_ids);
  const [budget, setBudget] = useState(String(existing?.budget_usd ?? 0.5));
  const [endAt, setEndAt] = useState(existing?.end_at?.slice(0, 10) ?? "");
  const [preview, setPreview] = useState<AutomationPreview>();
  const [previewError, setPreviewError] = useState("");
  const action = useAction();
  const zone = timezone || preferences.data?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone;
  const chosenTargets = targets ?? preferences.data?.default_destination_ids ?? [];
  const trigger = triggerFrom(frequency, date, time, days, zone);
  const special = existing && (existing.trigger.kind === "event_relative" || existing.trigger.kind === "watch");
  const classes: Effect[] = ["read", ...(writes ? ["reversible_write" as const] : []), ...(sends ? ["sensitive_send" as const] : [])];
  const triggerKey = JSON.stringify(trigger);
  const targetsKey = chosenTargets.join();
  const value = useMemo<AutomationEditable | undefined>(() => (special ? existing!.trigger : trigger) && missed ? {
    title: title.trim() || "Routine", instruction: instruction.trim(), trigger: special ? existing!.trigger : trigger!, connection_ids: accounts,
    requested_authority: { action_classes: classes }, budget_usd: Number(budget) || 0, max_active_runs: 1, missed_run: missed,
    execution_window_seconds: 3600, destination_ids: chosenTargets, ...(endAt ? { end_at: new Date(`${endAt}T23:59:59`).toISOString() } : {}),
  } : undefined, [title, instruction, triggerKey, accounts, writes, sends, missed, budget, endAt, targetsKey]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!value || !value.instruction) return;
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      try {
        const response = await fetch(assistantURL("/automations/preview"), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value), signal: controller.signal });
        const data = await response.json();
        if (!response.ok) throw new Error(data?.error ?? "Preview unavailable.");
        setPreview(data); setPreviewError("");
      } catch (error) { if (!controller.signal.aborted) setPreviewError(error instanceof Error ? error.message : "Preview unavailable."); }
    }, 400);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [value]);

  const save = () => action.run("save", async () => {
    if (!value) throw new Error("Choose when it runs and what happens if a run is missed.");
    const receipt = existing
      ? await mutate<Automation>(`/automations/${encodeURIComponent(existing.id)}`, { request_id: newRequestID(), expected_revision: existing.revision, value }, "PUT")
      : await mutate<Automation>("/automations", { request_id: newRequestID(), expected_revision: 0, value });
    invalidate("/automations", "/permissions");
    router.push(`/routines/${encodeURIComponent((receipt.resource as Automation).id)}`);
  });

  const writable = (connections.data?.items ?? []).filter((connection) => connection.status !== "disconnected");
  return (
    <Screen title={existing ? "Edit routine" : "New routine"} back={existing ? `/routines/${existing.id}` : "/routines"}>
      <form className="space-y-6" onSubmit={(event) => { event.preventDefault(); void save(); }}>
        <Field label="Name"><input value={title} onChange={(event) => setTitle(event.target.value)} className={inputClass} placeholder="Morning briefing" required /></Field>
        <Field label="What Nori does each time"><textarea value={instruction} onChange={(event) => setInstruction(event.target.value)} rows={3} className={cn(inputClass, "resize-y")} placeholder="Summarize today’s agenda, urgent mail and anything due." required /></Field>
        {special ? <Notice>{cadence(existing!.trigger)}. Change this kind of trigger by asking Nori in Chat.</Notice> : <fieldset>
          <legend className="mb-1.5 text-meta font-medium">When</legend>
          <div className="flex flex-wrap gap-2">
            <select aria-label="How often" value={frequency} onChange={(event) => setFrequency(event.target.value as Frequency)} className={cn(inputBase, "w-auto")}>
              <option value="once">Once</option><option value="daily">Every day</option><option value="weekdays">Weekdays</option><option value="weekly">Certain days</option><option value="monthly">Monthly</option>
            </select>
            <input aria-label={frequency === "once" ? "Date" : "Starting"} type="date" value={date} onChange={(event) => setDate(event.target.value)} className={cn(inputBase, "w-auto")} />
            <input aria-label="Time" type="time" value={time} onChange={(event) => setTime(event.target.value)} className={cn(inputBase, "w-auto")} />
            {frequency !== "once" && <input aria-label="Time zone" value={zone} onChange={(event) => setTimezone(event.target.value)} className={cn(inputBase, "w-56")} />}
          </div>
          {frequency === "weekly" && <div className="mt-2 flex flex-wrap gap-1" role="group" aria-label="Days">{WEEKDAYS.map(([code, label]) => <button key={code} type="button" aria-pressed={days.includes(code)} onClick={() => setDays(days.includes(code) ? days.filter((day) => day !== code) : [...days, code])}
            className={cn("h-11 min-w-11 rounded-full px-3 text-meta font-medium", days.includes(code) ? "bg-ink text-surface" : "bg-fill-1 text-ink-2")}>{label}</button>)}</div>}
        </fieldset>}
        <fieldset>
          <legend className="mb-1.5 text-meta font-medium">Accounts it can use</legend>
          {writable.length ? <div className="flex flex-wrap gap-2">{writable.map((connection) => <label key={connection.id} className={cn("flex min-h-11 cursor-pointer items-center gap-2 rounded-full border px-3 text-meta", accounts.includes(connection.id) ? "border-accent bg-accent-subtle" : "border-hairline")}>
            <input type="checkbox" className="accent-[var(--accent)]" checked={accounts.includes(connection.id)} onChange={() => setAccounts(accounts.includes(connection.id) ? accounts.filter((other) => other !== connection.id) : [...accounts, connection.id])} />{[connection.title, connection.account?.label].filter(Boolean).join(" · ")}</label>)}</div>
            : <p className="text-meta text-ink-2">No connected accounts. <Link href="/apps" className="text-accent-text hover:underline">Connect one</Link>.</p>}
        </fieldset>
        <fieldset className="space-y-2">
          <legend className="mb-1.5 text-meta font-medium">Without asking, it may</legend>
          <label className="flex min-h-11 items-center gap-3 text-body"><input type="checkbox" checked disabled className="accent-[var(--accent)]" />Read from those accounts</label>
          <label className="flex min-h-11 items-center gap-3 text-body"><input type="checkbox" checked={writes} onChange={(event) => setWrites(event.target.checked)} className="accent-[var(--accent)]" />Make changes it can undo, like drafts and calendar edits</label>
          <label className="flex min-h-11 items-center gap-3 text-body"><input type="checkbox" checked={sends} onChange={(event) => setSends(event.target.checked)} className="accent-[var(--accent)]" />Send messages and invitations</label>
        </fieldset>
        <fieldset>
          <legend className="mb-1.5 text-meta font-medium">If Nori misses a run</legend>
          <div className="space-y-1">{([["latest", "Run once when Nori is back"], ["skip", "Skip it"], ["all", "Run every missed one"]] as const).map(([option, label]) => <label key={option} className="flex min-h-11 items-center gap-3 text-body">
            <input type="radio" name="missed" value={option} checked={missed === option} onChange={() => setMissed(option)} className="accent-[var(--accent)]" required />{label}</label>)}</div>
        </fieldset>
        <fieldset>
          <legend className="mb-1.5 text-meta font-medium">Send results to</legend>
          <div className="flex flex-wrap gap-2">{(destinations.data?.items ?? []).filter((destination) => destination.status !== "disabled").map((destination) => <label key={destination.id} className={cn("flex min-h-11 cursor-pointer items-center gap-2 rounded-full border px-3 text-meta", chosenTargets.includes(destination.id) ? "border-accent bg-accent-subtle" : "border-hairline")}>
            <input type="checkbox" className="accent-[var(--accent)]" checked={chosenTargets.includes(destination.id)} onChange={() => setTargets(chosenTargets.includes(destination.id) ? chosenTargets.filter((other) => other !== destination.id) : [...chosenTargets, destination.id])} />{destination.label}{destination.status !== "verified" ? " (not verified)" : ""}</label>)}</div>
        </fieldset>
        <div className="flex flex-wrap gap-4">
          <Field label="Budget per run (USD)"><input type="number" min={0} step={0.1} value={budget} onChange={(event) => setBudget(event.target.value)} className={cn(inputBase, "w-32")} /></Field>
          <Field label="Stop after (optional)"><input type="date" value={endAt} onChange={(event) => setEndAt(event.target.value)} className={cn(inputBase, "w-auto")} /></Field>
        </div>
        {(preview || previewError) && <section aria-label="Preview" aria-live="polite" className="rounded-card border border-hairline-strong bg-fill-1 p-4">
          {previewError ? <p className="text-meta text-danger">{previewError}</p> : preview && <>
            <h2 className="text-row font-semibold">Next runs</h2>
            <ul className="mt-1 text-body text-ink">{preview.next_runs.slice(0, 5).map((run) => <li key={run}>{when(run, preview.timezone)}</li>)}</ul>
            {preview.authority_summary && <p className="mt-3 text-meta text-ink-2">Standing permission: {preview.authority_summary}</p>}
            <p className="mt-1 text-meta text-ink-2">{preview.missed_run_explanation}</p>
            {preview.warnings.map((warning) => <p key={warning.code} className="mt-2 text-meta text-warning">{warning.message}</p>)}
          </>}
        </section>}
        {action.error && <Notice tone="danger">{action.error}</Notice>}
        <div className="flex gap-2">
          <Button type="submit" variant="primary" disabled={!!action.pending || !value || !instruction.trim()}>{action.pending ? "Saving…" : existing ? "Save changes" : "Create routine"}</Button>
          <Button variant="quiet" onClick={() => router.back()}>Cancel</Button>
        </div>
      </form>
    </Screen>
  );
}
