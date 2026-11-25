"use client";

import Link from "next/link";
import { useState } from "react";
import { Brain, Bug, History, ShieldCheck, SlidersHorizontal } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { EFFECT_LABEL, when } from "@/lib/assistant/format";
import {
  buildPermissionRuleEditable,
  countRulesByEffect,
  describePermissionRulePreview,
  groupPermissionRules,
  revokeRuleConfirmationBody,
  RULE_EFFECT_META,
  RULE_EFFECT_ORDER,
  RULE_PRECEDENCE_NOTE,
  type RuleEffect,
  type RuleFilter,
} from "@/lib/assistant/permissions";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { Connection, MCPServer, OperationProjection as Operation, PermissionRule, Preferences, PreferencesEditable } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { OperationRows, shownOperations } from "./WorkScreen";
import { Confirm, Empty, Failure, Field, inputBase, inputClass, Loading, Notice, NotYet, Pill, Row, Rows, Screen, Section, Tabs } from "./ui";

type List<T> = { items: T[] };
export type SettingsSection = "permissions" | "preferences" | "history" | "diagnostics";

const SECTIONS: { id: SettingsSection | "memory"; title: string; icon: typeof ShieldCheck; href?: string; feature?: Parameters<ReturnType<typeof useAssistant>["has"]>[0] }[] = [
  { id: "permissions", title: "Permissions", icon: ShieldCheck, feature: "permissions" },
  { id: "preferences", title: "Preferences", icon: SlidersHorizontal, feature: "preferences" },
  { id: "memory", title: "Memory", icon: Brain, href: "/memory", feature: "memory" },
  { id: "history", title: "Action history", icon: History, feature: "operations" },
  { id: "diagnostics", title: "Diagnostics", icon: Bug },
];

export function SettingsIndex() {
  const { has } = useAssistant();
  return <Screen title="Settings" back="/more" backLabel="More">
    <Rows>{SECTIONS.map((section) => <Row key={section.id} href={section.href ?? `/settings/${section.id}`} leading={<section.icon size={18} className="text-ink-2" aria-hidden />} title={section.title}
      trailing={section.feature && !has(section.feature) ? <Pill>Not yet</Pill> : undefined} />)}</Rows>
  </Screen>;
}

export function SettingsScreen({ section }: { section: SettingsSection }) {
  const { has } = useAssistant();
  const meta = SECTIONS.find((item) => item.id === section)!;
  if (meta.feature && !has(meta.feature)) return <Screen title={meta.title} back="/more" backLabel="More"><NotYet what={meta.title} /></Screen>;
  return <Screen title={meta.title} back="/more" backLabel="More">
    {section === "permissions" ? <Permissions /> : section === "preferences" ? <PreferencesForm /> : section === "history" ? <ActionHistory /> : <Diagnostics />}
  </Screen>;
}

function Permissions() {
  const { has } = useAssistant();
  const rules = useResource<List<PermissionRule>>("/permissions", { poll: 30_000 });
  const connections = useResource<List<Connection>>(has("connections") ? "/connections" : null, { poll: 0 });
  const servers = useResource<List<MCPServer>>(has("mcp") ? "/mcp" : null, { poll: 0 });
  const action = useAction();
  const [adding, setAdding] = useState(false);
  const [filter, setFilter] = useState<RuleFilter>("all");
  if (rules.loading) return <Loading />;
  const items = rules.data?.items ?? [];
  const counts = countRulesByEffect(items);
  const revoke = (rule: PermissionRule) => action.run(rule.id, async () => {
    await mutate(`/permissions/${encodeURIComponent(rule.id)}/actions`, { request_id: newRequestID(), expected_revision: rule.revision, value: { action: "revoke" } });
    invalidate("/permissions", "/automations");
  });
  const account = (id: string) => connections.data?.items.find((connection) => connection.id === id);
  const server = (id: string) => servers.data?.items.find((item) => item.id === id);
  const groups = groupPermissionRules(items, filter);
  const shownCount = groups.reduce((total, group) => total + group.rules.length, 0);
  return <>
    <Notice tone="neutral">{RULE_PRECEDENCE_NOTE}</Notice>
    {rules.error && <div className="mt-4"><Failure error={rules.error} onRetry={() => void rules.reload()} /></div>}
    {action.error && <div className="mt-4"><Notice tone="danger">{action.error}</Notice></div>}
    {items.length > 0 && (
      <div className="mt-6">
        <Tabs<RuleFilter>
          label="Filter permission rules"
          value={filter}
          onChange={setFilter}
          items={[
            { id: "all", label: "All", count: items.length },
            ...RULE_EFFECT_ORDER.map((effect) => ({
              id: effect,
              label: RULE_EFFECT_META[effect].label,
              count: counts[effect],
            })),
          ]}
        />
      </div>
    )}
    {groups.map((group) => group.rules.length > 0 && <Section key={group.id} title={group.title}><Rows>{group.rules.map((rule) => {
      const effectMeta = RULE_EFFECT_META[rule.effect];
      return <li key={rule.id} id={rule.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
        <span className="min-w-0 flex-1">
          <span className="block text-row font-medium">{rule.summary ?? rule.title ?? "Permission"}</span>
          <span className="mt-0.5 block text-meta text-ink-2">{[rule.action_classes.map((effect) => EFFECT_LABEL[effect]).join(", "), rule.connection_ids.map((id) => id === "*" ? "all tools & MCP servers" : account(id)?.title ?? server(id)?.name ?? id).join(", "), rule.recipients?.length ? `to ${rule.recipients.join(", ")}` : "", rule.expires_at ? `until ${when(rule.expires_at)}` : "", rule.max_operations ? `up to ${rule.max_operations} times` : ""].filter(Boolean).join(" · ")}</span>
          {rule.automation_id && <Link href={`/routines/${encodeURIComponent(rule.automation_id)}`} className="text-meta text-accent-text hover:underline">Automation</Link>}
          {rule.goal_id && <Link href={`/goals/${encodeURIComponent(rule.goal_id)}`} className="text-meta text-accent-text hover:underline">Goal</Link>}
        </span>
        <Pill tone={effectMeta.tone}>{effectMeta.label}</Pill>
        <Confirm danger title="Remove this permission?" body={revokeRuleConfirmationBody(rule)} confirm="Remove" onConfirm={() => void revoke(rule)}
          trigger={<Button size="sm" variant="quiet" disabled={action.pending === rule.id}>{action.pending === rule.id ? "Removing…" : "Remove"}</Button>} />
      </li>;
    })}</Rows></Section>)}
    {!items.length && <div className="mt-6"><Empty>No standing permissions. Nori asks before each change or send.</Empty></div>}
    {items.length > 0 && shownCount === 0 && <div className="mt-4"><Empty>No {RULE_EFFECT_META[filter as RuleEffect].label.toLowerCase()} rules yet.</Empty></div>}
    <Section>{adding ? <NewRule connections={connections.data?.items ?? []} servers={servers.data?.items ?? []} onDone={() => setAdding(false)} /> : <Button onClick={() => setAdding(true)}>Add a permission</Button>}</Section>
  </>;
}

export function NewRule({ connections, servers, onDone }: { connections: Connection[]; servers: MCPServer[]; onDone: () => void }) {
  const action = useAction();
  const [effect, setEffect] = useState<RuleEffect>("allow");
  const [account, setAccount] = useState(connections[0]?.id ?? "*");
  const [toolIds, setToolIds] = useState("");
  const [classes, setClasses] = useState<PermissionRule["action_classes"]>(["reversible_write"]);
  const [recipients, setRecipients] = useState("");
  const [expires, setExpires] = useState("");
  const onTargetChange = (next: string) => {
    setAccount(next);
    if (servers.some((item) => item.id === next) && !classes.includes("unknown")) {
      setClasses((prev) => [...prev, "unknown"]);
    }
  };
  const save = () => action.run("save", async () => {
    await mutate("/permissions", {
      request_id: newRequestID(),
      expected_revision: 0,
      value: buildPermissionRuleEditable({ effect, account, classes, toolIds, recipients, expires }),
    });
    invalidate("/permissions"); onDone();
  });
  const selected = connections.find((connection) => connection.id === account);
  const selectedServer = servers.find((item) => item.id === account);
  const targetLabel = account === "*" ? "all tools & MCP servers" : selected?.account?.label ?? selected?.title ?? selectedServer?.name ?? "this account";
  const example = describePermissionRulePreview({ effect, classes, targetLabel, recipients });
  return <form className="space-y-4 rounded-card border border-hairline-strong p-4" onSubmit={(event) => { event.preventDefault(); void save(); }}>
    <div className="flex flex-wrap gap-3">
      <Field label="Rule"><select value={effect} onChange={(event) => setEffect(event.target.value as RuleEffect)} className={cn(inputBase, "w-auto")}>
        <option value="allow">{RULE_EFFECT_META.allow.label}</option>
        <option value="ask">{RULE_EFFECT_META.ask.label}</option>
        <option value="deny">{RULE_EFFECT_META.deny.label}</option>
      </select></Field>
      <Field label="Target"><select value={account} onChange={(event) => onTargetChange(event.target.value)} className={cn(inputBase, "w-auto")}><option value="*">All tools &amp; MCP servers</option>{connections.map((connection) => <option key={connection.id} value={connection.id}>{[connection.title, connection.account?.label].filter(Boolean).join(" · ")}</option>)}{servers.map((item) => <option key={item.id} value={item.id}>{item.name} (MCP)</option>)}</select></Field>
    </div>
    <fieldset><legend className="mb-1.5 text-meta font-medium">Actions</legend><div className="flex flex-wrap gap-2">{(["read", "reversible_write", "sensitive_send", "unknown"] as const).map((effectClass) => <label key={effectClass} className={cn("flex min-h-11 items-center gap-2 rounded-full border px-3 text-meta", classes.includes(effectClass) ? "border-accent bg-accent-subtle" : "border-hairline")}>
      <input type="checkbox" className="accent-[var(--accent)]" checked={classes.includes(effectClass)} onChange={() => setClasses(classes.includes(effectClass) ? classes.filter((other) => other !== effectClass) : [...classes, effectClass])} />{EFFECT_LABEL[effectClass]}</label>)}</div></fieldset>
    <Field label="Tool ID (optional)"><input value={toolIds} onChange={(event) => setToolIds(event.target.value)} placeholder="* (any tool), or mcp/server_id/tool_name" className={inputClass} /></Field>
    {classes.includes("sensitive_send") && <Field label={effect === "allow" ? "Only to" : "Only to (optional)"}><input value={recipients} onChange={(event) => setRecipients(event.target.value)} placeholder={effect === "allow" ? "sam@example.com, family@example.com" : "Any recipient, or sam@example.com"} className={inputClass} /></Field>}
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
        <label className="flex w-16 shrink-0 items-center gap-2 text-body"><input type="checkbox" className="accent-[var(--accent)]" checked={!!entry} onChange={(event) => setHours(day, event.target.checked ? { start: "09:00", end: "17:30" } : undefined)} />{DAYS[day]}</label>
        {entry && <span className="flex min-w-0 flex-1 items-center gap-2"><input aria-label={`${DAYS[day]} start`} type="time" value={entry.start} onChange={(event) => setHours(day, { start: event.target.value, end: entry.end })} className={cn(inputBase, "w-full max-w-[8.75rem] min-w-0 flex-1 px-2 py-1.5")} /><span className="text-ink-3" aria-hidden>–</span><input aria-label={`${DAYS[day]} end`} type="time" value={entry.end} onChange={(event) => setHours(day, { start: entry.start, end: event.target.value })} className={cn(inputBase, "w-full max-w-[8.75rem] min-w-0 flex-1 px-2 py-1.5")} /></span>}
      </li>; })}</ul>
    </fieldset>
    <div className="flex flex-wrap gap-4">
      <Field label="Budget per task (USD)"><input type="number" min={0} step="any" value={value.budgets.task_usd} onChange={(event) => setValue({ ...value, budgets: { ...value.budgets, task_usd: Number(event.target.value) } })} className={cn(inputBase, "w-32")} /></Field>
      <Field label="Monthly limit (USD)"><input type="number" min={0} step="any" value={value.budgets.monthly_usd ?? ""} onChange={(event) => setValue({ ...value, budgets: { ...value.budgets, monthly_usd: event.target.value ? Number(event.target.value) : undefined } })} className={cn(inputBase, "w-32")} /></Field>
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

function ActionHistory() {
  const connections = useResource<List<Connection>>("/connections", { poll: 0 });
  const [account, setAccount] = useState("");
  const operations = useResource<List<Operation>>(`/operations${account ? `?connection_id=${encodeURIComponent(account)}` : ""}`, { poll: 30_000 });
  return <>
    <label className="mb-4 flex items-center gap-2 text-meta"><span className="text-ink-2">Account</span>
      <select value={account} onChange={(event) => setAccount(event.target.value)} className={cn(inputBase, "w-auto")}><option value="">All accounts</option>{(connections.data?.items ?? []).map((connection) => <option key={connection.id} value={connection.id}>{[connection.title, connection.account?.label].filter(Boolean).join(" · ")}</option>)}</select></label>
    {operations.loading ? <Loading /> : operations.error ? <Failure error={operations.error} onRetry={() => void operations.reload()} /> : operations.data && shownOperations(operations.data.items).length ? <>
      <OperationRows operations={shownOperations(operations.data.items)} />
    </> : <Empty>No actions yet.</Empty>}
  </>;
}

function Diagnostics() {
  const { capabilities, debug, setDebug, localMode } = useAssistant();
  const features = capabilities?.assistant_features ?? {};
  return <>
    <label className="flex min-h-11 items-center gap-3 text-body"><input type="checkbox" className="accent-[var(--accent)]" checked={debug} onChange={(event) => setDebug(event.target.checked)} />Show model and tool details in Chat</label>
    <Section title="This Nori">
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
        <dt className="text-ink-3">Runtime</dt><dd>{localMode ? "Local" : "Hosted"}</dd>
        <dt className="text-ink-3">Contracts</dt><dd className="font-mono text-mono-data">{[capabilities?.contract, capabilities?.conversationContract, capabilities?.assistant_contract].filter(Boolean).join(" · ") || "unknown"}</dd>
        <dt className="text-ink-3">Available</dt><dd>{Object.entries(features).filter(([, on]) => on).map(([name]) => name).join(", ") || "Chat and tasks only"}</dd>
        {capabilities?.upload_accept && <><dt className="text-ink-3">Uploads</dt><dd>{capabilities.upload_accept.media_types.join(", ")}</dd></>}
      </dl>
    </Section>
  </>;
}
