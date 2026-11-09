"use client";

import Link from "next/link";
import { useState } from "react";
import { Bell, Bug, Check, Copy, History, Brain, KeyRound, ShieldCheck, SlidersHorizontal } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { deviceLabel, pushSupported, subscribeThisDevice } from "@/lib/assistant/push";
import { useAssistant } from "@/lib/assistant/context";
import { EFFECT_LABEL, relative, when } from "@/lib/assistant/format";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { Connection, DeliveryDestination, MCPClient, MCPClientCreated, InboxItem, MemoryFact, OperationProjection as Operation, PermissionRule, Preferences, PreferencesEditable } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { OperationRows } from "./WorkScreen";
import { Confirm, Empty, NoriLine, Failure, Field, inputBase, inputClass, Loading, Notice, NotYet, Pill, Row, Rows, Screen, Section } from "./ui";

type List<T> = { items: T[] };
export type SettingsSection = "permissions" | "preferences" | "memory" | "notifications" | "history" | "clients" | "diagnostics";

const SECTIONS: { id: SettingsSection; title: string; icon: typeof Bell; feature?: Parameters<ReturnType<typeof useAssistant>["has"]>[0] }[] = [
  { id: "permissions", title: "Permissions", icon: ShieldCheck, feature: "permissions" },
  { id: "preferences", title: "Preferences", icon: SlidersHorizontal, feature: "preferences" },
  { id: "memory", title: "Memory", icon: Brain, feature: "memory" },
  { id: "notifications", title: "Notifications", icon: Bell, feature: "preferences" },
  { id: "history", title: "Action history", icon: History, feature: "operations" },
  { id: "clients", title: "Connected clients", icon: KeyRound, feature: "mcp" },
  { id: "diagnostics", title: "Diagnostics", icon: Bug },
];

export function SettingsIndex() {
  const { has } = useAssistant();
  return <Screen title="Settings">
    <Rows>{SECTIONS.map((section) => <Row key={section.id} href={`/settings/${section.id}`} leading={<section.icon size={18} className="text-ink-2" aria-hidden />} title={section.title}
      trailing={section.feature && !has(section.feature) ? <Pill>Not yet</Pill> : undefined} />)}</Rows>
  </Screen>;
}

export function SettingsScreen({ section }: { section: SettingsSection }) {
  const { has } = useAssistant();
  const meta = SECTIONS.find((item) => item.id === section)!;
  if (meta.feature && !has(meta.feature)) return <Screen title={meta.title} back="/settings"><NotYet what={meta.title} /></Screen>;
  return <Screen title={meta.title} back="/settings">
    {section === "permissions" ? <Permissions /> : section === "preferences" ? <PreferencesForm /> : section === "memory" ? <Memory /> : section === "notifications" ? <Notifications /> : section === "history" ? <ActionHistory /> : section === "clients" ? <Clients /> : <Diagnostics />}
  </Screen>;
}

function Permissions() {
  const rules = useResource<List<PermissionRule>>("/permissions", { poll: 30_000 });
  const connections = useResource<List<Connection>>("/connections", { poll: 0 });
  const action = useAction();
  const [adding, setAdding] = useState(false);
  if (rules.loading) return <Loading />;
  const items = rules.data?.items ?? [];
  const revoke = (rule: PermissionRule) => action.run(rule.id, async () => {
    await mutate(`/permissions/${encodeURIComponent(rule.id)}/actions`, { request_id: newRequestID(), expected_revision: rule.revision, value: { action: "revoke" } });
    invalidate("/permissions", "/automations");
  });
  const account = (id: string) => connections.data?.items.find((connection) => connection.id === id);
  const groups = [
    { title: "Never", rules: items.filter((rule) => rule.effect === "deny") },
    { title: "Allowed without asking", rules: items.filter((rule) => rule.effect === "allow" && !rule.automation_id && !rule.goal_id) },
    { title: "For routines and goals", rules: items.filter((rule) => rule.effect === "allow" && (rule.automation_id || rule.goal_id)) },
  ];
  return <>
    <NoriLine>I ask before anything not covered here. When you ask me directly, that request is enough.</NoriLine>
    {rules.error && <div className="mt-4"><Failure error={rules.error} onRetry={() => void rules.reload()} /></div>}
    {action.error && <div className="mt-4"><Notice tone="danger">{action.error}</Notice></div>}
    {groups.map((group) => group.rules.length > 0 && <Section key={group.title} title={group.title}><Rows>{group.rules.map((rule) => <li key={rule.id} id={rule.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
      <span className="min-w-0 flex-1">
        <span className="block text-row font-medium">{rule.summary ?? rule.title ?? "Permission"}</span>
        <span className="mt-0.5 block text-meta text-ink-2">{[rule.action_classes.map((effect) => EFFECT_LABEL[effect]).join(", "), rule.connection_ids.map((id) => id === "*" ? "any account" : account(id)?.title ?? id).join(", "), rule.recipients?.length ? `to ${rule.recipients.join(", ")}` : "", rule.expires_at ? `until ${when(rule.expires_at)}` : "", rule.max_operations ? `up to ${rule.max_operations} times` : ""].filter(Boolean).join(" · ")}</span>
        {rule.automation_id && <Link href={`/routines/${encodeURIComponent(rule.automation_id)}`} className="text-meta text-accent-text hover:underline">Routine</Link>}
        {rule.goal_id && <Link href={`/goals/${encodeURIComponent(rule.goal_id)}`} className="text-meta text-accent-text hover:underline">Goal</Link>}
      </span>
      <Confirm danger title="Remove this permission?" body={rule.automation_id ? "The routine will ask you before doing this from now on." : "Nori will ask you before doing this from now on."} confirm="Remove" onConfirm={() => void revoke(rule)}
        trigger={<Button size="sm" variant="quiet" disabled={action.pending === rule.id}>{action.pending === rule.id ? "Removing…" : "Remove"}</Button>} />
    </li>)}</Rows></Section>)}
    {!items.length && <div className="mt-6"><Empty>No standing permissions. Nori asks before each change or send.</Empty></div>}
    <Section>{adding ? <NewRule connections={connections.data?.items ?? []} onDone={() => setAdding(false)} /> : <Button onClick={() => setAdding(true)}>Add a permission</Button>}</Section>
  </>;
}

function NewRule({ connections, onDone }: { connections: Connection[]; onDone: () => void }) {
  const action = useAction();
  const [effect, setEffect] = useState<"allow" | "deny">("allow");
  const [account, setAccount] = useState(connections[0]?.id ?? "");
  const [classes, setClasses] = useState<PermissionRule["action_classes"]>(["reversible_write"]);
  const [recipients, setRecipients] = useState("");
  const [expires, setExpires] = useState("");
  const save = () => action.run("save", async () => {
    await mutate("/permissions", { request_id: newRequestID(), expected_revision: 0, value: {
      effect, tool_ids: ["*"], connection_ids: [account], resource_ids: ["*"], action_classes: classes,
      ...(classes.includes("sensitive_send") ? { recipients: recipients.split(",").map((value) => value.trim()).filter(Boolean) } : {}),
      ...(expires ? { expires_at: new Date(`${expires}T23:59:59`).toISOString() } : {}),
    } });
    invalidate("/permissions"); onDone();
  });
  const selected = connections.find((connection) => connection.id === account);
  const example = `${effect === "allow" ? "Nori may" : "Nori will never"} ${classes.map((effectClass) => ({ read: "read", reversible_write: "make undoable changes", sensitive_send: "send", unknown: "use unknown tools", control: "" }[effectClass])).join(" or ")} with ${selected?.account?.label ?? "this account"}${classes.includes("sensitive_send") && recipients ? ` to ${recipients}` : ""}${effect === "allow" ? " without asking" : ""}.`;
  return <form className="space-y-4 rounded-card border border-hairline-strong p-4" onSubmit={(event) => { event.preventDefault(); void save(); }}>
    <div className="flex flex-wrap gap-3">
      <Field label="Rule"><select value={effect} onChange={(event) => setEffect(event.target.value as "allow" | "deny")} className={cn(inputBase, "w-auto")}><option value="allow">Allow without asking</option><option value="deny">Never allow</option></select></Field>
      <Field label="Account"><select value={account} onChange={(event) => setAccount(event.target.value)} className={cn(inputBase, "w-auto")}>{connections.map((connection) => <option key={connection.id} value={connection.id}>{[connection.title, connection.account?.label].filter(Boolean).join(" · ")}</option>)}</select></Field>
    </div>
    <fieldset><legend className="mb-1.5 text-meta font-medium">Actions</legend><div className="flex flex-wrap gap-2">{(["read", "reversible_write", "sensitive_send"] as const).map((effectClass) => <label key={effectClass} className={cn("flex min-h-11 items-center gap-2 rounded-full border px-3 text-meta", classes.includes(effectClass) ? "border-accent bg-accent-subtle" : "border-hairline")}>
      <input type="checkbox" className="accent-[var(--accent)]" checked={classes.includes(effectClass)} onChange={() => setClasses(classes.includes(effectClass) ? classes.filter((other) => other !== effectClass) : [...classes, effectClass])} />{EFFECT_LABEL[effectClass]}</label>)}</div></fieldset>
    {classes.includes("sensitive_send") && <Field label="Only to"><input value={recipients} onChange={(event) => setRecipients(event.target.value)} placeholder="sam@example.com, family@example.com" className={inputClass} /></Field>}
    <Field label="Until (optional)"><input type="date" value={expires} onChange={(event) => setExpires(event.target.value)} className={cn(inputBase, "w-auto")} /></Field>
    <p className="rounded-tile bg-fill-1 px-3 py-2 text-meta text-ink">{example}</p>
    {action.error && <Notice tone="danger">{action.error}</Notice>}
    <div className="flex gap-2"><Button type="submit" variant="primary" disabled={!account || !classes.length || (classes.includes("sensitive_send") && effect === "allow" && !recipients.trim()) || !!action.pending}>Save</Button><Button variant="quiet" onClick={onDone}>Cancel</Button></div>
  </form>;
}

const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

function PreferencesForm() {
  const preferences = useResource<Preferences>("/preferences", { poll: 0 });
  if (preferences.loading) return <Loading />;
  if (!preferences.data) return <Failure error={preferences.error ?? "Preferences unavailable."} onRetry={() => void preferences.reload()} />;
  return <PreferencesEditor key={preferences.data.revision} current={preferences.data} onSaved={preferences.apply} />;
}

function editable(preferences: Preferences): PreferencesEditable {
  const { revision: _revision, updated_at: _updated, ...value } = preferences;
  void _revision; void _updated;
  return value;
}

function PreferencesEditor({ current, onSaved }: { current: Preferences; onSaved: (next: Preferences) => void }) {
  const action = useAction();
  const [value, setValue] = useState<PreferencesEditable>(editable(current));
  const [conflict, setConflict] = useState<Preferences>();
  const dirty = JSON.stringify(value) !== JSON.stringify(editable(current));
  const save = () => action.run("save", async () => {
    try {
      const receipt = await mutate<Preferences>("/preferences", { request_id: newRequestID(), expected_revision: current.revision, value }, "PUT");
      onSaved(receipt.resource as Preferences); invalidate("/connections");
    } catch (error) {
      if (error instanceof APIError && error.stale && error.body?.current) setConflict(error.body.current as Preferences);
      throw error;
    }
  });
  const hours = (day: number) => value.working_hours.find((entry) => entry.day === day);
  const setHours = (day: number, next?: { start: string; end: string }) => setValue({ ...value, working_hours: [...value.working_hours.filter((entry) => entry.day !== day), ...(next ? [{ day, ...next }] : [])].sort((a, b) => a.day - b.day) });
  return <form className="space-y-6" onSubmit={(event) => { event.preventDefault(); void save(); }}>
    {conflict && <Notice tone="warning" action={<Button size="sm" onClick={() => onSaved(conflict)}>Load latest</Button>}>These changed on another device. Load the latest to edit them.</Notice>}
    <div className="flex flex-wrap gap-4">
      <Field label="Time zone"><input value={value.timezone} onChange={(event) => setValue({ ...value, timezone: event.target.value })} className={cn(inputBase, "w-64")} list="zones" /></Field>
      <datalist id="zones">{(Intl as unknown as { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf?.("timeZone").map((zone) => <option key={zone} value={zone} />)}</datalist>
      <Field label="Language and region"><input value={value.locale ?? ""} onChange={(event) => setValue({ ...value, locale: event.target.value })} className={cn(inputBase, "w-32")} placeholder="en-GB" /></Field>
    </div>
    <fieldset><legend className="mb-1.5 text-meta font-medium">Working hours</legend>
      <ul className="space-y-1">{[1, 2, 3, 4, 5, 6, 0].map((day) => { const entry = hours(day); return <li key={day} className="flex min-h-11 items-center gap-3">
        <label className="flex w-20 shrink-0 items-center gap-2 text-body"><input type="checkbox" className="accent-[var(--accent)]" checked={!!entry} onChange={(event) => setHours(day, event.target.checked ? { start: "09:00", end: "17:30" } : undefined)} />{DAYS[day]}</label>
        {entry && <span className="flex min-w-0 items-center gap-2"><input aria-label={`${DAYS[day]} start`} type="time" value={entry.start} onChange={(event) => setHours(day, { start: event.target.value, end: entry.end })} className={cn(inputBase, "w-[7.5rem] min-w-0 px-2 py-1.5")} /><span className="text-ink-3" aria-hidden>–</span><input aria-label={`${DAYS[day]} end`} type="time" value={entry.end} onChange={(event) => setHours(day, { start: entry.start, end: event.target.value })} className={cn(inputBase, "w-[7.5rem] min-w-0 px-2 py-1.5")} /></span>}
      </li>; })}</ul>
    </fieldset>
    <div className="flex flex-wrap gap-4">
      <Field label="Budget per task (USD)"><input type="number" min={0} step="any" value={value.budgets.task_usd} onChange={(event) => setValue({ ...value, budgets: { ...value.budgets, task_usd: Number(event.target.value) } })} className={cn(inputBase, "w-32")} /></Field>
      <Field label="Monthly limit (USD)"><input type="number" min={0} step="any" value={value.budgets.monthly_usd ?? ""} onChange={(event) => setValue({ ...value, budgets: { ...value.budgets, monthly_usd: event.target.value ? Number(event.target.value) : undefined } })} className={cn(inputBase, "w-32")} /></Field>
      <Field label="Dictation language"><input value={value.input?.dictation_language ?? ""} onChange={(event) => setValue({ ...value, input: { ...value.input, dictation_language: event.target.value || undefined } })} className={cn(inputBase, "w-32")} placeholder="en-GB" /></Field>
    </div>
    <AccountDefaults defaults={value.account_defaults} onChange={(account_defaults) => setValue({ ...value, account_defaults })} />
    {action.error && !conflict && <Notice tone="danger">{action.error}</Notice>}
    <div className="flex gap-2"><Button type="submit" variant="primary" disabled={!dirty || !!action.pending}>{action.pending ? "Saving…" : "Save"}</Button>{dirty && <Button variant="quiet" onClick={() => setValue(editable(current))}>Undo changes</Button>}</div>
  </form>;
}

function AccountDefaults({ defaults, onChange }: { defaults: Record<string, string>; onChange: (next: Record<string, string>) => void }) {
  const connections = useResource<List<Connection>>("/connections", { poll: 0 });
  const purposes = Object.keys(defaults);
  if (!purposes.length) return null;
  const label: Record<string, string> = { "calendar.create": "New events go to", "calendar.read": "Read events from", "mail.send": "Send mail as" };
  return <fieldset><legend className="mb-1.5 text-meta font-medium">Default accounts</legend>
    <ul className="space-y-2">{purposes.map((purpose) => <li key={purpose} className="flex flex-wrap items-center gap-3"><span className="w-40 text-body">{label[purpose] ?? purpose}</span>
      <select aria-label={label[purpose] ?? purpose} value={defaults[purpose]} onChange={(event) => onChange({ ...defaults, [purpose]: event.target.value })} className={cn(inputBase, "w-auto")}>
        {(connections.data?.items ?? []).filter((connection) => connection.status !== "disconnected").map((connection) => <option key={connection.id} value={connection.id}>{[connection.title, connection.account?.label].filter(Boolean).join(" · ")}</option>)}
      </select></li>)}</ul>
  </fieldset>;
}

function Memory() {
  const memory = useResource<List<MemoryFact>>("/memory", { poll: 0 });
  const action = useAction();
  const [editing, setEditing] = useState<string>();
  const [text, setText] = useState("");
  if (memory.loading) return <Loading />;
  const items = memory.data?.items ?? [];
  const forget = (fact: MemoryFact) => action.run(fact.id, async () => { await mutate(`/memory/${encodeURIComponent(fact.id)}/actions`, { request_id: newRequestID(), expected_revision: fact.revision, value: { action: "forget" } }); invalidate("/memory"); });
  const save = (fact: MemoryFact) => action.run(fact.id, async () => { await mutate(`/memory/${encodeURIComponent(fact.id)}`, { request_id: newRequestID(), expected_revision: fact.revision, value: { text: text.trim() } }, "PUT"); setEditing(undefined); invalidate("/memory"); });
  return <>
    {action.error && <div className="mb-4"><Notice tone="danger">{action.error}</Notice></div>}
    {items.length ? <Rows label="Remembered">{items.map((fact) => <li key={fact.id} className="px-4 py-3">
      {editing === fact.id ? <form className="flex flex-col gap-2 sm:flex-row" onSubmit={(event) => { event.preventDefault(); if (text.trim()) void save(fact); }}>
        <input aria-label="Remembered fact" value={text} onChange={(event) => setText(event.target.value)} className={inputClass} autoFocus />
        <div className="flex gap-2"><Button type="submit" variant="primary" size="sm" disabled={!text.trim()}>Save</Button><Button size="sm" variant="quiet" onClick={() => setEditing(undefined)}>Cancel</Button></div>
      </form> : <div className="flex flex-wrap items-center gap-3">
        <span className="min-w-0 flex-1"><span className="block text-body">{fact.text}</span>
          <span className="block text-meta text-ink-3">Saved {relative(fact.created_at)}{fact.origin.conversation_id ? " from chat" : fact.origin.task_id ? " during a task" : ""}{fact.scope === "chat" ? " · this chat only" : ""}</span></span>
        <Button size="sm" variant="quiet" onClick={() => { setEditing(fact.id); setText(fact.text); }}>Edit</Button>
        <Confirm title="Forget this?" body="Nori stops using it. Chats where it came up stay as they are." confirm="Forget" onConfirm={() => void forget(fact)} trigger={<Button size="sm" variant="quiet" className="text-danger!" disabled={action.pending === fact.id}>Forget</Button>} />
      </div>}
    </li>)}</Rows> : <Empty>Nori remembers only what you ask it to. Say “remember that…” in Chat.</Empty>}
  </>;
}

function Notifications() {
  const destinations = useResource<List<DeliveryDestination>>("/delivery-destinations", { poll: 0 });
  const inbox = useResource<List<InboxItem>>("/inbox", { poll: 30_000 });
  const preferences = useResource<Preferences>("/preferences", { poll: 0 });
  const action = useAction();
  const acknowledge = (item: InboxItem) => action.run(item.id, async () => { await mutate(`/inbox/${encodeURIComponent(item.id)}/actions`, { request_id: newRequestID(), expected_revision: item.revision, value: { action: "acknowledge" } }); invalidate("/inbox", "/assistant/summary"); });
  const hrefFor = (link: InboxItem["link"]) => ({ interaction: "/work?tab=needs_you", task: `/work/${encodeURIComponent(link.id)}`, result: `/library/${encodeURIComponent(link.id)}`, automation: `/routines/${encodeURIComponent(link.id)}`, goal: `/goals/${encodeURIComponent(link.id)}`, connection: "/apps", installation: "/apps?tab=plugins", computer_session: `/computer?session=${encodeURIComponent(link.id)}` } as Record<string, string>)[link.kind] ?? "/";
  return <>
    <Section title="Where Nori reaches you">
      {destinations.loading ? <Loading /> : <Rows>{(destinations.data?.items ?? []).map((destination) => <Destination key={destination.id} destination={destination} />)}</Rows>}
      {preferences.data?.quiet_hours && <p className="mt-2 text-meta text-ink-2">Quiet hours {preferences.data.quiet_hours.start}–{preferences.data.quiet_hours.end}: notifications wait; work still runs.</p>}
      <AddDestination />
    </Section>
    <Section title="Inbox">{inbox.loading ? <Loading /> : inbox.data?.items.length ? <Rows>{inbox.data.items.map((item) => <Row key={item.id} href={hrefFor(item.link)} title={item.title} detail={`${item.body} · ${relative(item.created_at)}`}
      trailing={item.acknowledged_at ? <span className="text-meta text-ink-3">Seen</span> : <Button size="sm" variant="quiet" onClick={() => void acknowledge(item)}>Mark seen</Button>} chevron={false} />)}</Rows> : <Empty>Nothing yet.</Empty>}</Section>
  </>;
}

const CHANNEL: Record<DeliveryDestination["channel"], string> = { in_app: "In Nori", push: "Push notification", email: "Email" };
const DESTINATION_STATUS: Record<DeliveryDestination["status"], { tone: "ok" | "neutral" | "danger"; label: string }> = {
  verified: { tone: "ok", label: "On" }, pending_verification: { tone: "neutral", label: "Not verified" }, disabled: { tone: "neutral", label: "Off" }, failed: { tone: "danger", label: "Failing" },
};

/** One destination: verify with a code, re-enroll an expired push subscription, or remove it. */
function Destination({ destination }: { destination: DeliveryDestination }) {
  const { capabilities } = useAssistant();
  const action = useAction();
  const [codeSent, setCodeSent] = useState(false);
  const [code, setCode] = useState("");
  const [expired, setExpired] = useState(destination.channel === "push" && destination.status === "failed");
  const status = DESTINATION_STATUS[destination.status] ?? { tone: "neutral" as const, label: destination.status };
  const act = (value: Record<string, unknown>, label: string, done?: string) => action.run(label, async () => {
    try {
      await mutate(`/delivery-destinations/${encodeURIComponent(destination.id)}/actions`, { request_id: newRequestID(), expected_revision: destination.revision, value });
    } catch (error) {
      if (error instanceof APIError && error.code === "subscription_expired") setExpired(true);
      if (error instanceof APIError && error.code === "provider_unavailable") throw new Error(destination.channel === "email" ? "Nori can’t send email yet: no mail service is set up." : "Nori can’t send notifications yet: push isn’t set up.");
      throw error;
    }
    invalidate("/delivery-destinations");
  }, done);
  const reenroll = () => action.run("reenroll", async () => {
    if (!capabilities?.push_public_key) throw new Error("Push isn’t set up on this Nori.");
    const subscription = await subscribeThisDevice(capabilities.push_public_key);
    await mutate(`/delivery-destinations/${encodeURIComponent(destination.id)}`, { request_id: newRequestID(), expected_revision: destination.revision, value: { channel: "push", label: destination.label, subscription, ...(destination.quiet_hours ? { quiet_hours: destination.quiet_hours } : {}) } }, "PUT");
    setExpired(false); invalidate("/delivery-destinations");
  }, "This device is enrolled again.");
  const managed = destination.channel !== "in_app";
  return <li className="px-4 py-3">
    <div className="flex flex-wrap items-center gap-3">
      <span className="min-w-0 flex-1"><span className="block text-row font-medium">{destination.label}</span>
        <span className="block text-meta text-ink-2">{[CHANNEL[destination.channel], destination.quiet_hours ? `quiet ${destination.quiet_hours.start}–${destination.quiet_hours.end}` : "", destination.supports_receipts === false ? "can’t confirm you saw it" : ""].filter(Boolean).join(" · ")}</span></span>
      <Pill tone={status.tone}>{expired ? "Expired" : status.label}</Pill>
      {managed && destination.status === "pending_verification" && !codeSent && <Button size="sm" disabled={!!action.pending} onClick={() => void act({ action: "send_verification" }, "send", "Code sent.").then(() => setCodeSent(true))}>{action.pending === "send" ? "Sending…" : "Send code"}</Button>}
      {expired && <Button size="sm" variant="primary" disabled={!!action.pending} onClick={() => void reenroll()}>{action.pending === "reenroll" ? "Enrolling…" : "Re-enroll this device"}</Button>}
      {managed && <Confirm danger title={`Remove ${destination.label}?`} body="Nori stops sending here. Routines that used it keep running and deliver to your other destinations." confirm="Remove" onConfirm={() => void act({ action: "remove" }, "remove", "Removed.")}
        trigger={<Button size="sm" variant="quiet" className="text-danger!" disabled={!!action.pending}>Remove</Button>} />}
    </div>
    {codeSent && destination.status === "pending_verification" && <form className="mt-3 flex flex-wrap items-center gap-2" onSubmit={(event) => { event.preventDefault(); if (/^\d{6}$/.test(code)) void act({ action: "verify", code }, "verify", "Verified."); }}>
      <input aria-label="Verification code" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(event) => setCode(event.target.value.replace(/\D/g, ""))} className={cn(inputBase, "w-32 tracking-[0.3em]")} placeholder="123456" />
      <Button type="submit" size="sm" variant="primary" disabled={code.length !== 6 || !!action.pending}>{action.pending === "verify" ? "Checking…" : "Verify"}</Button>
      <Button size="sm" variant="quiet" disabled={!!action.pending} onClick={() => void act({ action: "send_verification" }, "send", "New code sent.")}>Send a new code</Button>
    </form>}
    {action.error && <p role="alert" className="mt-2 text-meta text-danger">{action.error}</p>}
  </li>;
}

/** Add an email address, or enroll this browser for push when the core has a push key. */
function AddDestination() {
  const { capabilities } = useAssistant();
  const action = useAction();
  const [adding, setAdding] = useState<"email" | undefined>();
  const [label, setLabel] = useState("");
  const [address, setAddress] = useState("");
  const create = (value: Record<string, unknown>, done: string) => action.run("create", async () => {
    await mutate("/delivery-destinations", { request_id: newRequestID(), expected_revision: 0, value });
    setAdding(undefined); setAddress(""); setLabel(""); invalidate("/delivery-destinations");
  }, done);
  const enrollThisDevice = () => action.run("push", async () => {
    const subscription = await subscribeThisDevice(capabilities!.push_public_key!);
    await mutate("/delivery-destinations", { request_id: newRequestID(), expected_revision: 0, value: { channel: "push", label: deviceLabel(), subscription } });
    invalidate("/delivery-destinations");
  }, "This device is added. Send a code to verify it.");
  const canPush = !!capabilities?.push_public_key && pushSupported();
  return <div className="mt-3">
    {adding === "email" ? <form className="flex flex-wrap items-end gap-3 rounded-card border border-hairline-strong p-4" onSubmit={(event) => { event.preventDefault(); void create({ channel: "email", label: label.trim() || address.trim(), address: address.trim() }, "Email added. Send a code to verify it."); }}>
      <Field label="Email address"><input type="email" required value={address} onChange={(event) => setAddress(event.target.value)} className={cn(inputBase, "w-64 max-w-full")} placeholder="you@example.com" /></Field>
      <Field label="Name (optional)"><input value={label} onChange={(event) => setLabel(event.target.value)} className={cn(inputBase, "w-48")} placeholder="Personal email" /></Field>
      <Button type="submit" variant="primary" disabled={!address.trim() || !!action.pending}>Add</Button><Button variant="quiet" onClick={() => setAdding(undefined)}>Cancel</Button>
    </form> : <div className="flex flex-wrap gap-2">
      <Button size="sm" onClick={() => setAdding("email")}>Add an email address</Button>
      {canPush ? <Button size="sm" disabled={!!action.pending} onClick={() => void enrollThisDevice()}>{action.pending === "push" ? "Asking…" : "Notify this device"}</Button>
        : <span className="self-center text-meta text-ink-3">{!capabilities?.push_public_key ? "Push notifications aren’t set up on this Nori." : "This browser can’t receive notifications."}</span>}
    </div>}
    {action.error && <p role="alert" className="mt-2 text-meta text-danger">{action.error}</p>}
  </div>;
}

function ActionHistory() {
  const connections = useResource<List<Connection>>("/connections", { poll: 0 });
  const [account, setAccount] = useState("");
  const operations = useResource<List<Operation>>(`/operations${account ? `?connection_id=${encodeURIComponent(account)}` : ""}`, { poll: 30_000 });
  return <>
    <label className="mb-4 flex items-center gap-2 text-meta"><span className="text-ink-2">Account</span>
      <select value={account} onChange={(event) => setAccount(event.target.value)} className={cn(inputBase, "w-auto")}><option value="">All accounts</option>{(connections.data?.items ?? []).map((connection) => <option key={connection.id} value={connection.id}>{[connection.title, connection.account?.label].filter(Boolean).join(" · ")}</option>)}</select></label>
    {operations.loading ? <Loading /> : operations.error ? <Failure error={operations.error} onRetry={() => void operations.reload()} /> : operations.data?.items.length ? <>
      <OperationRows operations={operations.data.items} />
    </> : <Empty>No actions yet.</Empty>}
  </>;
}

/** Other apps (MCP clients) that reach this Nori with a token. The token is shown once and never stored here. */
function Clients() {
  const clients = useResource<List<MCPClient>>("/mcp-clients", { poll: 30_000 });
  const action = useAction();
  const [name, setName] = useState("");
  const [mutateScope, setMutateScope] = useState(false);
  const [created, setCreated] = useState<{ name: string; token: string }>();
  const [copied, setCopied] = useState(false);
  const create = () => action.run("create", async () => {
    const receipt = await mutate<MCPClient>("/mcp-clients", { request_id: newRequestID(), expected_revision: 0, value: { name: name.trim(), scopes: mutateScope ? ["read", "mutate"] : ["read"] } }) as unknown as Partial<MCPClientCreated> & { resource: MCPClient };
    if (receipt.token) setCreated({ name: receipt.resource.name, token: receipt.token });
    setName(""); setMutateScope(false); invalidate("/mcp-clients");
  }, "Client added.");
  const revoke = (client: MCPClient) => action.run(client.id, async () => {
    await mutate(`/mcp-clients/${encodeURIComponent(client.id)}/actions`, { request_id: newRequestID(), expected_revision: client.revision, value: { action: "revoke" } });
    invalidate("/mcp-clients");
  }, "Access removed.");
  const active = (clients.data?.items ?? []).filter((client) => !client.revoked_at);
  return <>
    <NoriLine>Apps you add here can reach your Nori over MCP with their own token.</NoriLine>
    {created && <div className="mt-6 rounded-card border border-hairline-strong bg-fill-1 p-4" role="status">
      <p className="text-row font-semibold">Token for {created.name}</p>
      <p className="mt-1 text-meta text-ink-2">Copy it into the app now. Nori won’t show it again.</p>
      <div className="mt-3 flex flex-col gap-2 sm:flex-row">
        <code className="min-w-0 flex-1 rounded-tile bg-surface px-3 py-2 font-mono text-mono-data break-all">{created.token}</code>
        <Button size="sm" onClick={() => { void navigator.clipboard?.writeText(created.token); setCopied(true); }}>{copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}{copied ? "Copied" : "Copy"}</Button>
        <Button size="sm" variant="quiet" onClick={() => { setCreated(undefined); setCopied(false); }}>Done</Button>
      </div>
    </div>}
    <Section title="Add a client"><form className="flex flex-wrap items-end gap-3" onSubmit={(event) => { event.preventDefault(); if (name.trim()) void create(); }}>
      <Field label="Name"><input value={name} onChange={(event) => setName(event.target.value)} maxLength={100} className={cn(inputBase, "w-64 max-w-full")} placeholder="Laptop editor" /></Field>
      <label className="flex min-h-11 items-center gap-2 text-body"><input type="checkbox" className="accent-[var(--accent)]" checked={mutateScope} onChange={(event) => setMutateScope(event.target.checked)} />Can start work and answer questions</label>
      <Button type="submit" variant="primary" disabled={!name.trim() || !!action.pending}>{action.pending === "create" ? "Adding…" : "Add"}</Button>
    </form></Section>
    {action.error && <div className="mt-4"><Notice tone="danger">{action.error}</Notice></div>}
    <Section title="Clients">{clients.loading ? <Loading /> : active.length ? <Rows>{active.map((client) => <li key={client.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
      <span className="min-w-0 flex-1"><span className="block text-row font-medium">{client.name}</span>
        <span className="block text-meta text-ink-2">{client.scopes.includes("mutate") ? "Reads and starts work" : "Reads only"} · {client.last_used_at ? `last used ${relative(client.last_used_at)}` : "not used yet"}</span></span>
      <Confirm danger title={`Remove access for ${client.name}?`} body="Its token stops working right away." confirm="Remove access" onConfirm={() => void revoke(client)}
        trigger={<Button size="sm" variant="quiet" className="text-danger!" disabled={action.pending === client.id}>Remove access</Button>} />
    </li>)}</Rows> : <Empty>No clients yet.</Empty>}</Section>
  </>;
}

function Diagnostics() {
  const { capabilities, debug, setDebug, fixtures, agentMode } = useAssistant();
  const features = capabilities?.assistant_features ?? {};
  return <>
    <label className="flex min-h-11 items-center gap-3 text-body"><input type="checkbox" className="accent-[var(--accent)]" checked={debug} onChange={(event) => setDebug(event.target.checked)} />Show model and tool details in Chat</label>
    <Section title="This Nori">
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
        <dt className="text-ink-3">Runtime</dt><dd>{agentMode ? "Local" : "Hosted"}{fixtures ? " · fixture data" : ""}</dd>
        <dt className="text-ink-3">Contracts</dt><dd className="font-mono text-mono-data">{[capabilities?.contract, capabilities?.conversationContract, capabilities?.assistant_contract].filter(Boolean).join(" · ") || "unknown"}</dd>
        <dt className="text-ink-3">Available</dt><dd>{Object.entries(features).filter(([, on]) => on).map(([name]) => name).join(", ") || "Chat and tasks only"}</dd>
        {capabilities?.upload_accept && <><dt className="text-ink-3">Uploads</dt><dd>{capabilities.upload_accept.media_types.join(", ")}</dd></>}
      </dl>
    </Section>
  </>;
}
