"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useMemo, useRef, useState } from "react";
import { Calendar, Check, Circle, CircleSlash, Folder, KeyRound, ListTodo, Mail, MessageSquare, Plug, Plus, Search, Server, Upload } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, assistantURL, getJSON, mutate, newRequestID, sendSecret } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { EFFECT_LABEL, plural, relative } from "@/lib/assistant/format";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { AuthAttempt, Connection, HealthCode, Integration, MCPServer, Preferences, Resource } from "@/lib/contracts/assistant";
import type { CatalogEntry, Installation, InstallationReview, MCPCredentialInput, MCPServerTool as MCPTool } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { safeAuthorizeURL } from "./InteractionCard";
import { SkillsList } from "./SkillsScreen";
import { Confirm, Empty, Failure, Field, inputBase, inputClass, Loading, Notice, NotYet, Pill, RefreshButton, Row, Rows, Screen, Section, Tabs, type Tone } from "./ui";

type List<T> = { items: T[] };
const ICONS: Record<string, typeof Plug> = { calendar: Calendar, mail: Mail, list: ListTodo, folder: Folder, message: MessageSquare };

export function AppIcon({ icon, size = 40 }: { icon?: string; size?: number }) {
  const Icon = (icon && ICONS[icon]) || Plug;
  return <span className="grid shrink-0 place-items-center rounded-tile bg-accent-subtle text-accent-text" style={{ width: size, height: size }}><Icon size={size * 0.5} strokeWidth={1.75} aria-hidden /></span>;
}

const HEALTH: Record<HealthCode, { tone: Tone; label: string }> = {
  ready: { tone: "ok", label: "Ready" }, needs_sign_in: { tone: "danger", label: "Needs sign-in" }, needs_more_access: { tone: "warning", label: "Needs more access" },
  revoked: { tone: "danger", label: "Access removed" }, rate_limited: { tone: "warning", label: "Slowed down by provider" }, temporarily_failing: { tone: "warning", label: "Having trouble" },
  provider_unavailable: { tone: "warning", label: "Provider is down" }, credential_store_unavailable: { tone: "danger", label: "Can’t save this account" },
};

const SCOPES: Record<string, string> = {
  "calendar.readonly": "read calendars", "calendar.events": "change events", calendar: "manage calendars",
  "gmail.readonly": "read mail", "gmail.compose": "write drafts", "gmail.send": "send mail", "gmail.modify": "label and archive mail",
  "contacts.readonly": "read contacts", "contacts.other.readonly": "read other contacts", "drive.readonly": "read Drive files", "drive.file": "change Drive files Nori created", openid: "", email: "", profile: "",
};
/** A provider scope in plain words; unknown scopes keep their last path segment. */
export function scopeLabel(scope: string) {
  const short = scope.replace(/^https:\/\/www\.googleapis\.com\/auth\//, "");
  return SCOPES[short] ?? short;
}
const scopeList = (scopes: string[]) => [...new Set(scopes.map(scopeLabel).filter(Boolean))].join(", ");

export function connectionState(connection: Connection): { tone: Tone; label: string } {
  if (connection.status === "connecting") return { tone: "neutral", label: "Connecting" };
  if (connection.status === "disconnected" || connection.status === "not_connected") return { tone: "neutral", label: "Not connected" };
  if (connection.status === "disabled") return { tone: "neutral", label: "Off" };
  if (connection.status === "disconnecting") return { tone: "neutral", label: "Disconnecting" };
  return HEALTH[healthOf(connection) ?? "ready"] ?? { tone: "neutral", label: connection.status };
}

/** The connection's health, also read from its status: an account can be marked as needing attention while its last call worked. */
function healthOf(connection: Connection): HealthCode | undefined {
  const code = connection.health.code;
  if (connection.status === "needs_attention" && (!code || code === "ready")) return connection.missing_scopes?.length ? "needs_more_access" : "needs_sign_in";
  return code;
}

export function needsAttention(connection: Connection) {
  const code = healthOf(connection);
  return connection.status !== "disconnected" && connection.status !== "not_connected" && !!code && code !== "ready";
}

/** What "Allow more access" asks for: everything granted plus the capabilities that need the missing permissions. */
function wantedCapabilities(connection: Connection, integration: Integration) {
  const missing = new Set(connection.missing_scopes ?? []);
  const more = integration.capabilities.filter((capability) => capability.supported && capability.scopes?.some((scope) => missing.has(scope))).map((capability) => capability.id);
  return [...new Set([...(connection.capabilities ?? []), ...more])];
}

function recoveryOf(connection: Connection) {
  if (connection.status === "not_connected" || connection.status === "disconnected") return "connect";
  if (connection.status === "needs_attention" && (!connection.health.recovery || connection.health.recovery === "none")) return connection.missing_scopes?.length ? "request_access" : "repair";
  return connection.health.recovery;
}

/** Start a server-issued sign-in and send the browser to it. */
async function startSignIn(connection: Pick<Connection, "id" | "revision">, action: "connect" | "repair", returnTo: string, capabilities?: string[]) {
  const receipt = await mutate<Connection>(`/connections/${encodeURIComponent(connection.id)}/actions`, {
    request_id: newRequestID(), expected_revision: connection.revision, value: { action, return_to: returnTo, ...(capabilities?.length ? { capabilities } : {}) },
  }) as { auth?: AuthAttempt };
  const url = receipt.auth && safeAuthorizeURL(receipt.auth.authorize_url);
  if (!url) throw new Error("Nori didn’t return a sign-in link. Try again.");
  window.location.assign(url);
}

export function AppsScreen() {
  const { has } = useAssistant();
  const params = useSearchParams();
  const router = useRouter();
  const tab = (params.get("tab") as "apps" | "plugins" | "skills" | "mcp") || "apps";
  const setTab = (next: string) => router.replace(next === "apps" ? "/apps" : `/apps?tab=${next}`, { scroll: false });
  if (!has("integrations")) return <Screen title="Apps"><NotYet what="Connecting apps" /></Screen>;
  return (
    <Screen title="Apps" wide>
      <Tabs label="Apps" value={tab} onChange={setTab} items={[{ id: "apps", label: "Apps" }, { id: "plugins", label: "Plugins" }, { id: "skills", label: "Skills" }, { id: "mcp", label: "MCP servers" }]} />
      {tab === "apps" ? <Catalog /> : tab === "plugins" ? (has("plugins") ? <Plugins /> : <NotYet what="Plugins" />) : tab === "skills" ? <SkillsList /> : has("mcp") ? <MCPServers /> : <NotYet what="MCP servers" />}
    </Screen>
  );
}

function Catalog() {
  const integrations = useResource<List<Integration>>("/integrations", { poll: 60_000 });
  const connections = useResource<List<Connection>>("/connections", { poll: 30_000 });
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState("All");
  const items = integrations.data?.items;
  const categories = useMemo(() => ["All", ...new Set((items ?? []).flatMap((item) => item.categories))], [items]);
  const shown = (items ?? []).filter((item) => (category === "All" || item.categories.includes(category)) && (!query || `${item.title} ${item.description} ${item.categories.join(" ")}`.toLowerCase().includes(query.toLowerCase())));
  const attention = (connections.data?.items ?? []).filter(needsAttention);
  const byIntegration = (id: string) => (connections.data?.items ?? []).filter((connection) => connection.integration_id === id && connection.status !== "disconnected" && !(connection.status === "not_connected" && !connection.account?.provider_id));
  if (integrations.loading) return <Loading />;
  if (integrations.error && !items) return <Failure error={integrations.error} onRetry={() => void integrations.reload()} />;
  return <>
    {attention.length > 0 && <Section title="Needs attention"><Rows>{attention.map((connection) => {
      const state = connectionState(connection);
      const integration = items?.find((item) => item.id === connection.integration_id);
      return <Row key={connection.id} href={`/apps/${encodeURIComponent(connection.integration_id)}`} leading={<AppIcon icon={integration?.icon} size={36} />} title={[integration?.title ?? connection.title, connection.account?.label].filter(Boolean).join(" · ")} detail={connection.health.message} trailing={<Pill tone={state.tone}>{state.label}</Pill>} />;
    })}</Rows></Section>}
    <Section title={attention.length ? "All apps" : undefined}>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <label className="relative min-w-[220px] flex-1"><span className="sr-only">Search apps</span>
          <Search size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-ink-3" aria-hidden />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search apps" className={cn(inputClass, "pl-9")} type="search" />
        </label>
        <div className="flex flex-wrap gap-1" role="group" aria-label="Category">
          {categories.map((name) => <button key={name} type="button" aria-pressed={category === name} onClick={() => setCategory(name)} className={cn("h-9 rounded-full px-3 text-meta font-medium first-letter:uppercase", category === name ? "bg-ink text-surface" : "bg-fill-1 text-ink-2 hover:text-ink")}>{name}</button>)}
        </div>
      </div>
      {shown.length ? <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">{shown.map((item) => {
        const accounts = byIntegration(item.id);
        const trouble = accounts.some(needsAttention);
        return <li key={item.id}><Link href={`/apps/${encodeURIComponent(item.id)}`} className="flex h-full flex-col gap-3 rounded-card border border-hairline bg-surface p-4 hover:border-hairline-strong hover:bg-fill-1">
          <div className="flex items-center gap-3"><AppIcon icon={item.icon} /><span className="min-w-0 flex-1"><span className="block truncate text-row font-semibold">{item.title}</span><span className="block truncate text-caption text-ink-3 first-letter:uppercase">{item.categories.join(" · ")}</span></span></div>
          <p className="line-clamp-2 flex-1 text-meta text-ink-2">{item.description}</p>
          <div>{item.availability !== "available" ? <Pill>{item.availability === "blocked" ? "Not set up" : "Unavailable"}</Pill>
            : accounts.length ? <Pill tone={trouble ? "warning" : "ok"}>{trouble ? "Needs attention" : accounts.length === 1 ? "Connected" : `${accounts.length} accounts`}</Pill>
              : <Pill tone="accent">Connect</Pill>}</div>
        </Link></li>;
      })}</ul> : <Empty action={<Button onClick={() => { setQuery(""); setCategory("All"); }}>Show all apps</Button>}>No apps match “{query}”.</Empty>}
    </Section>
  </>;
}

export function AppDetailScreen({ id }: { id: string }) {
  const { has } = useAssistant();
  const params = useSearchParams();
  const integration = useResource<Integration>(has("integrations") ? `/integrations/${encodeURIComponent(id)}` : null, { poll: 60_000 });
  const connections = useResource<List<Connection>>(has("connections") ? "/connections" : null, { poll: 10_000 });
  const action = useAction();
  const [passwordForm, setPasswordForm] = useState(false);
  if (!has("integrations")) return <Screen title="App" back="/apps"><NotYet what="Connecting apps" /></Screen>;
  if (integration.loading) return <Screen title="App" back="/apps"><Loading /></Screen>;
  const item = integration.data;
  if (!item) return <Screen title="App" back="/apps"><Failure error={integration.error ?? "This app isn’t in the catalog."} onRetry={() => void integration.reload()} /></Screen>;
  const mine = (connections.data?.items ?? []).filter((connection) => connection.integration_id === item.id);
  // A connection that never finished sign-in isn't an account; it is reused by the next Connect.
  const accounts = mine.filter((connection) => connection.status !== "disconnected" && !(connection.status === "not_connected" && !connection.account?.provider_id));
  const unfinished = mine.find((connection) => connection.status === "not_connected" && !connection.account?.provider_id);
  const result = params.get("result");
  const add = () => action.run("add", async () => {
    const connection = unfinished ?? (await mutate<Connection>("/connections", { request_id: newRequestID(), expected_revision: 0, value: { integration_id: item.id, title: item.title, selected_resource_ids: [] } })).resource as Connection;
    await startSignIn(connection, "connect", `/apps/${encodeURIComponent(item.id)}`);
  });
  const line = item.availability !== "available" ? item.availability_reason : undefined;
  // With one signed-in account, a capability it hasn't granted can be added by signing in again for it.
  const single = item.auth === "oauth" && accounts.length === 1 && accounts[0].account?.provider_id ? accounts[0] : undefined;
  const canAllow = (capability: Integration["capabilities"][number]) => !!single && capability.supported && !(single.capabilities ?? []).includes(capability.id);
  return (
    <Screen title={item.title} back="/apps" line={line} actions={<AppIcon icon={item.icon} />}>
      {result && <div className="mb-6"><Notice tone={result === "connected" ? "ok" : result === "cancelled" ? "neutral" : "danger"}>{result === "connected" ? "Connected." : result === "cancelled" ? "Sign-in was cancelled. Nothing changed." : "Sign-in didn’t finish. Nothing was saved; try again."}</Notice></div>}
      {action.error && <div className="mb-6"><Notice tone="danger">{action.error}</Notice></div>}
      <Section title="Accounts" actions={item.availability === "available" && (accounts.length === 0 || item.multiple_accounts) ? <Button variant="primary" size="sm" disabled={!!action.pending} onClick={() => item.auth === "app_password" ? setPasswordForm(true) : void add()}><Plus size={15} aria-hidden />{action.pending ? "Opening sign-in…" : accounts.length ? "Add another account" : "Connect"}</Button> : undefined}>
        {passwordForm && <div className="mb-3 rounded-card border border-hairline bg-surface p-4"><PasswordSignIn integration={item} connection={unfinished} onDone={() => setPasswordForm(false)} /></div>}
        {connections.loading ? <Loading /> : accounts.length ? <div className="space-y-3">{accounts.map((connection) => <ConnectionCard key={connection.id} connection={connection} integration={item} />)}</div>
          : <Empty>{item.availability === "available" ? `No ${item.title} account yet.` : item.availability_reason ?? "Not available yet."}</Empty>}
      </Section>
      <Section title="What Nori can do">
        <Rows>{item.capabilities.map((capability) => <Row key={capability.id} leading={!capability.supported ? <CircleSlash size={18} className="text-ink-3" aria-hidden /> : canAllow(capability) ? <Circle size={18} className="text-ink-3" aria-hidden /> : <Check size={18} className="text-[#1c7a43]" aria-hidden />}
          title={capability.title} detail={capability.supported ? undefined : capability.reason}
          trailing={<>{canAllow(capability) && <Button size="sm" variant="quiet" className="text-accent-text!" disabled={!!action.pending} aria-label={`Allow: ${capability.title}`}
            onClick={() => void action.run("allow", () => startSignIn(single!, "repair", `/apps/${encodeURIComponent(item.id)}`, [...(single!.capabilities ?? []), capability.id]))}>{action.pending === "allow" ? "Opening sign-in…" : "Allow"}</Button>}
            <Pill tone={capability.effect === "sensitive_send" ? "warning" : "neutral"}>{EFFECT_LABEL[capability.effect]}</Pill></>} />)}</Rows>
      </Section>
    </Screen>
  );
}

/** Apple's help page for creating an app-specific password. */
const APP_PASSWORD_HELP: Record<string, string> = { icloud: "https://support.apple.com/en-us/102654" };

/**
 * Sign-in for apps that use an account name and an app-specific password (iCloud). The pair is sent once
 * and kept nowhere in the browser; the server never returns it.
 */
function PasswordSignIn({ integration, connection, onDone }: { integration: Integration; connection?: Connection; onDone: () => void }) {
  const action = useAction();
  const apple = integration.id === "icloud";
  const repairing = !!connection?.account?.provider_id;
  const [username, setUsername] = useState(repairing ? connection?.account?.label ?? "" : "");
  const [password, setPassword] = useState("");
  const help = APP_PASSWORD_HELP[integration.id];
  const submit = () => action.run("save", async () => {
    const pair = { username: username.trim(), password: password.trim() };
    setPassword("");
    const target = connection ?? (await mutate<Connection>("/connections", { request_id: newRequestID(), expected_revision: 0, value: { integration_id: integration.id, title: integration.title, selected_resource_ids: [] } })).resource as Connection;
    await sendSecret<Connection>(`/connections/${encodeURIComponent(target.id)}/actions`, {
      request_id: newRequestID(), expected_revision: target.revision, value: { action: repairing ? "repair" : "connect", ...pair, return_to: `/apps/${encodeURIComponent(integration.id)}` },
    }).catch((error) => {
      if (error instanceof APIError && error.code === "validation_failed") throw new Error(`${apple ? "Apple" : integration.title} didn’t accept that ${apple ? "email" : "username"} and app-specific password. Nothing was saved.`);
      throw error;
    });
    invalidate("/connections", "/integrations", "/assistant/summary");
    onDone();
  }, repairing ? "Signed in again." : `${integration.title} connected.`);
  return <form className="max-w-md space-y-3" onSubmit={(event) => { event.preventDefault(); if (username.trim() && password.trim()) void submit(); }}>
    <p className="text-meta text-ink-2">{apple ? "Use an app-specific password, not your Apple Account password." : "Use an app-specific password, not your account password."}{help && <> <a href={help} target="_blank" rel="noreferrer noopener" className="font-medium text-accent-text hover:underline">How to create one</a></>}</p>
    <Field label={apple ? "Apple Account email" : "Username"}><input value={username} onChange={(event) => setUsername(event.target.value)} type={apple ? "email" : "text"} autoComplete="username" spellCheck={false} className={inputClass} disabled={repairing} /></Field>
    <Field label="App-specific password"><input value={password} onChange={(event) => setPassword(event.target.value)} type="password" autoComplete="off" spellCheck={false} className={inputClass} placeholder="xxxx-xxxx-xxxx-xxxx" /></Field>
    {action.error && <p role="alert" className="text-meta text-danger">{action.error}</p>}
    <div className="flex gap-2"><Button type="submit" variant="primary" disabled={!username.trim() || !password.trim() || !!action.pending}>{action.pending ? "Checking…" : repairing ? "Sign in again" : "Connect"}</Button><Button variant="quiet" onClick={() => { setPassword(""); onDone(); }}>Cancel</Button></div>
  </form>;
}

function ConnectionCard({ connection, integration }: { connection: Connection; integration: Integration }) {
  const action = useAction();
  const state = connectionState(connection);
  const [open, setOpen] = useState(false);
  const [passwordForm, setPasswordForm] = useState(false);
  const recovery = recoveryOf(connection);
  const returnTo = `/apps/${encodeURIComponent(integration.id)}`;
  const act = (value: Record<string, unknown>, label: string) => action.run(label, async () => {
    await mutate(`/connections/${encodeURIComponent(connection.id)}/actions`, { request_id: newRequestID(), expected_revision: connection.revision, value });
    invalidate("/connections", "/integrations", "/assistant/summary");
  });
  return (
    <article className="rounded-card border border-hairline bg-surface" aria-label={connection.account?.label ?? integration.title}>
      <div className="flex flex-wrap items-center gap-3 p-4">
        <span className="min-w-0 flex-1"><span className="block text-row font-semibold">{connection.account?.label ?? integration.title}</span>
          <span className="block text-meta text-ink-2">{connection.health.message || (connection.health.last_success_at ? `Last used ${relative(connection.health.last_success_at)}` : connection.title)}</span></span>
        <Pill tone={state.tone}>{state.label}</Pill>
      </div>
      {(recovery === "repair" || recovery === "connect" || recovery === "request_access") && <div className="flex flex-wrap items-center gap-2 border-t border-hairline px-4 py-3">
        <Button variant="primary" size="sm" disabled={!!action.pending} onClick={() => integration.auth === "app_password" ? setPasswordForm(true) : void action.run("repair", () => startSignIn(connection, recovery === "connect" ? "connect" : "repair", returnTo, recovery === "request_access" ? wantedCapabilities(connection, integration) : undefined))}>
          <KeyRound size={14} aria-hidden />{action.pending === "repair" ? "Opening sign-in…" : recovery === "request_access" ? "Allow more access" : connection.account ? "Sign in again" : "Sign in"}</Button>
        {!!connection.missing_scopes?.length && <span className="text-meta text-ink-2">Still needs permission to {scopeList(connection.missing_scopes)}</span>}
      </div>}
      {passwordForm && <div className="border-t border-hairline p-4"><PasswordSignIn integration={integration} connection={connection} onDone={() => setPasswordForm(false)} /></div>}
      {recovery === "wait" && connection.health.retry_after && <p className="border-t border-hairline px-4 py-3 text-meta text-ink-2">Nori will try again {relative(connection.health.retry_after)}.</p>}
      {action.error && <div className="px-4 pb-3"><Notice tone="danger">{action.error}</Notice></div>}
      <div className="flex flex-wrap items-center gap-2 border-t border-hairline px-4 py-2">
        <Button variant="quiet" size="sm" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? "Hide details" : "Calendars, defaults and access"}</Button>
        <span className="flex-1" />
        <Button variant="quiet" size="sm" disabled={!!action.pending} onClick={() => void act({ action: connection.status === "disabled" ? "enable" : "disable" }, "toggle")}>{connection.status === "disabled" ? "Turn on" : "Pause"}</Button>
        <Disconnect connection={connection} onDisconnect={(value) => void act(value, "disconnect")} busy={action.pending === "disconnect"} />
      </div>
      {open && <div className="border-t border-hairline p-4"><ConnectionDetails connection={connection} integration={integration} /></div>}
    </article>
  );
}

function Disconnect({ connection, onDisconnect, busy }: { connection: Connection; onDisconnect: (value: Record<string, unknown>) => void; busy: boolean }) {
  const [deleteImported, setDeleteImported] = useState(false);
  return <Confirm danger title={`Disconnect ${connection.account?.label ?? connection.title}?`} confirm="Disconnect" onConfirm={() => onDisconnect({ action: "disconnect", purge_cache: true, delete_imported_content: deleteImported })}
    trigger={<Button variant="quiet" size="sm" disabled={busy} className="text-danger!">{busy ? "Disconnecting…" : "Disconnect"}</Button>}
    body={<div className="space-y-3">
      <p>Nori stops using this account right away. Results you saved stay in Library, marked as copies. Work already sent can’t be undone.</p>
      <label className="flex items-start gap-2 text-ink"><input type="checkbox" className="mt-1 accent-[var(--accent)]" checked={deleteImported} onChange={(event) => setDeleteImported(event.target.checked)} />Also delete content imported from this account</label>
    </div>} />;
}

function ConnectionDetails({ connection, integration }: { connection: Connection; integration: Integration }) {
  const { has } = useAssistant();
  const resources = useResource<List<Resource>>(`/connections/${encodeURIComponent(connection.id)}/resources`, { poll: 0 });
  const preferences = useResource<Preferences>(has("preferences") ? "/preferences" : null, { poll: 0 });
  const action = useAction();
  const [selected, setSelected] = useState<string[] | undefined>();
  const current = selected ?? connection.selected_resource_ids;
  const dirty = selected !== undefined && selected.join() !== connection.selected_resource_ids.join();
  const save = () => action.run("save", async () => {
    await mutate(`/connections/${encodeURIComponent(connection.id)}`, { request_id: newRequestID(), expected_revision: connection.revision, value: { integration_id: connection.integration_id, title: connection.title, selected_resource_ids: current } }, "PUT");
    setSelected(undefined); invalidate("/connections");
  });
  const setDefault = (purpose: string) => action.run(`default:${purpose}`, async () => {
    const latest = await getJSON<Preferences>("/preferences");
    const { revision: _revision, updated_at: _updated, ...value } = latest;
    void _revision; void _updated;
    await mutate("/preferences", { request_id: newRequestID(), expected_revision: latest.revision, value: { ...value, account_defaults: { ...latest.account_defaults, [purpose]: connection.id } } }, "PUT");
    invalidate("/preferences", "/connections");
  });
  return <div className="space-y-6">
    <div>
      <h4 className="mb-2 text-meta font-semibold">Use these</h4>
      {resources.loading ? <Loading /> : resources.data?.items.length ? <ul className="space-y-1">{resources.data.items.map((resource) => <li key={resource.ref.provider_id}>
        <label className="flex min-h-11 items-center gap-3 rounded-tile px-2 hover:bg-fill-1"><input type="checkbox" className="accent-[var(--accent)]" checked={current.includes(resource.ref.provider_id)} onChange={(event) => setSelected(event.target.checked ? [...current, resource.ref.provider_id] : current.filter((id) => id !== resource.ref.provider_id))} />
          <span className="flex-1 text-row">{resource.title}</span>{resource.writable === false && <span className="text-caption text-ink-3">Read only</span>}</label>
      </li>)}</ul> : <p className="text-meta text-ink-2">This account has nothing to choose.</p>}
      {dirty && <Button size="sm" variant="primary" className="mt-2" disabled={!!action.pending} onClick={() => void save()}>{action.pending === "save" ? "Saving…" : "Save"}</Button>}
    </div>
    {integration.purposes.length > 0 && <div>
      <h4 className="mb-2 text-meta font-semibold">Default account for</h4>
      <ul className="space-y-1">{integration.purposes.map((purpose) => {
        const isDefault = preferences.data?.account_defaults[purpose.id] === connection.id || connection.defaults.includes(purpose.id);
        return <li key={purpose.id} className="flex min-h-11 items-center gap-3 px-2"><span className="flex-1 text-row">{purpose.title}</span>
          {isDefault ? <Pill tone="accent">Default</Pill> : <Button size="sm" disabled={!!action.pending || !has("preferences")} onClick={() => void setDefault(purpose.id)}>Make default</Button>}</li>;
      })}</ul>
    </div>}
    <div>
      <h4 className="mb-2 text-meta font-semibold">Granted access</h4>
      <p className="text-meta text-ink-2 [overflow-wrap:anywhere]">{scopeList(connection.granted_scopes) || "None"}</p>
    </div>
    {action.error && <Notice tone="danger">{action.error}</Notice>}
  </div>;
}

function Plugins() {
  const catalog = useResource<List<CatalogEntry>>("/plugins/catalog", { poll: 0 });
  const installed = useResource<List<Installation>>("/plugins/installations", { poll: 30_000 });
  const [query, setQuery] = useState("");
  if (catalog.loading || installed.loading) return <Loading />;
  const installations = installed.data?.items ?? [];
  const entries = (catalog.data?.items ?? []).filter((entry) => !query || `${entry.title} ${entry.description} ${entry.categories.join(" ")}`.toLowerCase().includes(query.toLowerCase()));
  return <>
    {installations.length > 0 && <Section title="Installed"><Rows>{installations.map((installation) => {
      const entry = catalog.data?.items.find((item) => item.package.id === installation.package.id);
      return <Row key={installation.id} href={`/apps/plugins/${encodeURIComponent(installation.id)}`} leading={<AppIcon icon="list" size={36} />} title={entry?.title ?? installation.package.id}
        detail={`Version ${installation.package.version}${installation.pinned ? " · pinned" : ""}${installation.available_version && installation.available_version !== installation.package.version ? ` · ${installation.available_version} available` : ""}`}
        trailing={<Pill tone={installation.state === "failed" ? "danger" : installation.enabled && installation.state === "ready" ? "ok" : "neutral"}>{INSTALL_STATE[installation.state] ?? (installation.enabled ? "On" : "Off")}</Pill>} />;
    })}</Rows></Section>}
    <Section title="Catalog" actions={<PackageUpload onUploaded={() => void catalog.reload()} />}>
      <label className="relative mb-4 block"><span className="sr-only">Search plugins</span><Search size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-ink-3" aria-hidden />
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search plugins and skills" className={cn(inputClass, "pl-9")} type="search" /></label>
      <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2">{entries.map((entry) => <li key={entry.package.id}><PluginCard entry={entry} /></li>)}</ul>
    </Section>
  </>;
}

/** Upload a local package manifest; the core verifies it and adds it to the catalog. Installing is a separate step. */
function PackageUpload({ onUploaded }: { onUploaded: () => void }) {
  const picker = useRef<HTMLInputElement>(null);
  const action = useAction();
  const upload = (file: File) => action.run("upload", async () => {
    if (file.size > 256 * 1024) throw new Error("That package manifest is too large.");
    let manifest: unknown;
    try { manifest = JSON.parse(await file.text()); } catch { throw new Error("That file isn’t a package manifest."); }
    const response = await fetch(assistantURL("/plugins/packages"), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(manifest), cache: "no-store" });
    const data = await response.json().catch(() => null);
    if (!response.ok) throw new Error(data?.error ?? "Nori couldn’t read that package.");
    onUploaded();
  });
  return <span className="flex items-center gap-2">
    {action.error && <span role="alert" className="text-meta text-danger">{action.error}</span>}
    <input ref={picker} type="file" accept=".json,application/json" hidden aria-label="Choose a package manifest" onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) void upload(file); }} />
    <Button size="sm" onClick={() => picker.current?.click()} disabled={!!action.pending}><Upload size={14} aria-hidden />{action.pending ? "Checking…" : "Upload package"}</Button>
  </span>;
}

const INSTALL_STATE: Partial<Record<Installation["state"], string>> = { installing: "Installing", updating: "Updating", disabling: "Turning off", disabled: "Off", removing: "Removing", failed: "Failed" };

function PluginCard({ entry }: { entry: CatalogEntry }) {
  const action = useAction();
  const router = useRouter();
  const install = () => action.run("install", async () => {
    // The user approves exactly what the package requests, hosts and paths included.
    const receipt = await mutate<Installation>("/plugins/installations", { request_id: newRequestID(), expected_revision: 0, value: { package: { id: entry.package.id, version: entry.latest_version }, approved_access: entry.requested_access } });
    invalidate("/plugins", "/integrations");
    router.push(`/apps/plugins/${encodeURIComponent((receipt.resource as Installation).id)}`);
  });
  const c = entry.contributions_summary;
  const parts = [c.tools && plural(c.tools, "tool"), c.skills && `${c.skills} ${c.skills === 1 ? "skill" : "skills"}`, c.mcp_servers && "MCP server"].filter(Boolean).join(" · ");
  return <article className="flex h-full flex-col gap-2 rounded-card border border-hairline bg-surface p-4">
    <div className="flex items-start gap-2"><h3 className="min-w-0 flex-1 text-row font-semibold">{entry.title}</h3>{!entry.provenance.verified && <Pill tone="warning">Unverified</Pill>}</div>
    <p className="text-meta text-ink-2">{entry.description}</p>
    <p className="text-caption text-ink-3">{entry.publisher.name} · {entry.latest_version}{parts ? ` · ${parts}` : ""}</p>
    <p className="text-caption text-ink-3">{entry.requested_access.action_classes.length ? `Access: ${entry.requested_access.action_classes.map((effect) => EFFECT_LABEL[effect].toLowerCase()).join(", ")}${entry.requested_access.connection_types.length ? ` with ${entry.requested_access.connection_types.join(", ")}` : ""}` : "Instructions only; no account access."}</p>
    <div className="mt-auto pt-2">{entry.installation_id ? <Link href={`/apps/plugins/${encodeURIComponent(entry.installation_id)}`} className="text-meta font-medium text-accent-text hover:underline">Installed · manage</Link>
      : <Confirm title={`Install ${entry.title}?`} confirm="Install" onConfirm={() => void install()} trigger={<Button size="sm" variant="primary" disabled={!!action.pending}>{action.pending ? "Installing…" : "Install"}</Button>}
        body={<div className="space-y-2"><p>Installing adds what it contributes. It doesn’t let Nori act on your accounts until you connect them and allow it.</p>{entry.requested_access.network?.length ? <p>Reaches {entry.requested_access.network.join(", ")}.</p> : null}{entry.requested_access.filesystem?.length ? <p>Reads files in {entry.requested_access.filesystem.join(", ")}.</p> : null}{!entry.provenance.verified && <p className="text-warning">This package isn’t verified by its registry.</p>}</div>} />}
      {action.error && <p role="alert" className="mt-2 text-meta text-danger">{action.error}</p>}</div>
  </article>;
}

export function PluginDetailScreen({ id }: { id: string }) {
  const installation = useResource<Installation>(`/plugins/installations/${encodeURIComponent(id)}`, { poll: 10_000 });
  const catalog = useResource<List<CatalogEntry>>("/plugins/catalog", { poll: 0 });
  const action = useAction();
  const router = useRouter();
  const [review, setReview] = useState<{ version?: string; data: InstallationReview }>();
  const item = installation.data;
  if (installation.loading) return <Screen title="Plugin" back="/apps?tab=plugins"><Loading /></Screen>;
  if (!item) return <Screen title="Plugin" back="/apps?tab=plugins"><Failure error={installation.error ?? "Not installed."} /></Screen>;
  const entry = catalog.data?.items.find((candidate) => candidate.package.id === item.package.id);
  // The core may keep available_version equal to the installed one; only a different version is an update.
  const available = item.available_version && item.available_version !== item.package.version ? item.available_version : undefined;
  // Versions this installation ran before; the core keeps their immutable packages for rollback.
  const earlier = new Set((item.history ?? []).map((event) => event.version).filter((version): version is string => !!version && version !== item.package.version));
  const act = (value: Record<string, unknown>, label: string) => action.run(label, async () => {
    const receipt = await mutate<Installation>(`/plugins/installations/${encodeURIComponent(id)}/actions`, { request_id: newRequestID(), expected_revision: item.revision, value });
    invalidate("/plugins", "/integrations");
    if ((value as { action: string }).action === "uninstall") router.push("/apps?tab=plugins"); else installation.apply(receipt.resource as Installation);
    setReview(undefined);
  });
  const loadReview = (version?: string) => action.run("review", async () => setReview({ version, data: await getJSON<InstallationReview>(`/plugins/installations/${encodeURIComponent(id)}/review${version ? `?version=${encodeURIComponent(version)}` : ""}`) }));
  return <Screen title={entry?.title ?? item.package.id} back="/apps?tab=plugins" line={item.state === "failed" ? "This plugin failed. Its diagnostics are below." : available && !item.pinned ? `Version ${available} is available.` : undefined}
    actions={<Pill tone={item.enabled ? "ok" : "neutral"}>{item.enabled ? "On" : "Off"}</Pill>}>
    {action.error && <div className="mb-4"><Notice tone="danger">{action.error}</Notice></div>}
    <Section title="Version">
      <Rows><Row title={`${item.package.version}${item.pinned ? " (pinned)" : ""}`} detail={available ? `${available} available` : "Up to date"}
        trailing={<>{available && <Button size="sm" variant="primary" onClick={() => void loadReview(available)}>Review update</Button>}{!item.pinned && <Button size="sm" onClick={() => void act({ action: "pin", version: item.package.version }, "pin")}>Pin</Button>}</>} /></Rows>
      {review && <div className="mt-3 rounded-card border border-hairline-strong p-4">
        <h3 className="text-row font-semibold">{review.version ? `Changes in ${review.version}` : "Before you uninstall"}</h3>
        <ReviewList data={review.data} />
        <div className="mt-4 flex gap-2">{review.version ? <Button variant="primary" onClick={() => void act({ action: "update", version: review.version, approved_access: approvedFor(item.approved_access, review.data, entry && entry.latest_version === review.version ? entry.requested_access : undefined) }, "update")}>{action.pending === "update" ? "Updating…" : review.data.access_added.length ? "Allow and update" : "Update"}</Button>
          : <Button variant="primary" className="bg-danger! hover:bg-danger!" onClick={() => void act({ action: "uninstall" }, "uninstall")}>{action.pending === "uninstall" ? "Uninstalling…" : "Uninstall"}</Button>}
          <Button variant="quiet" onClick={() => setReview(undefined)}>Cancel</Button></div>
      </div>}
    </Section>
    {entry && <Section title="Adds"><p className="text-body text-ink-2">{[entry.contributions_summary.tools && plural(entry.contributions_summary.tools, "tool"), entry.contributions_summary.skills && `${entry.contributions_summary.skills} ${entry.contributions_summary.skills === 1 ? "skill" : "skills"}`, entry.contributions_summary.prompts && `${entry.contributions_summary.prompts} prompts`, entry.contributions_summary.mcp_servers && `${entry.contributions_summary.mcp_servers === 1 ? "an MCP server" : `${entry.contributions_summary.mcp_servers} MCP servers`}`].filter(Boolean).join(", ") || "Instructions only"}.</p>
      {entry.description && <p className="mt-1 text-meta text-ink-3">{entry.description}</p>}</Section>}
    <Section title="Access">
      <p className="text-body text-ink-2">{item.approved_access.action_classes.length ? `May ${item.approved_access.action_classes.map((effect) => EFFECT_LABEL[effect].toLowerCase()).join(", ")}` : "No account access"}{item.approved_access.connection_types.length ? ` with ${item.approved_access.connection_types.join(", ")} accounts you connect.` : "."}</p>
      {item.approved_access.network?.length ? <p className="mt-1 text-meta text-ink-2">Reaches {item.approved_access.network!.join(", ")}.</p> : null}
      {item.approved_access.filesystem?.length ? <p className="mt-1 text-meta text-ink-2">Reads files in {item.approved_access.filesystem!.join(", ")}.</p> : null}
      {item.required_connection_types?.length ? <p className="mt-1 text-meta text-ink-3">Needs a connected {item.required_connection_types.join(", ")} account.</p> : null}
    </Section>
    {!!item.diagnostics?.length && <Section title="Diagnostics"><pre className="overflow-x-auto rounded-card bg-fill-1 p-3 font-mono text-mono-data whitespace-pre-wrap">{item.diagnostics.join("\n")}</pre></Section>}
    {!!item.history?.length && <Section title="History"><Rows>{item.history.slice().reverse().map((event, index) => <Row key={index} title={`${event.event[0].toUpperCase()}${event.event.slice(1)}${event.version ? ` ${event.version}` : ""}`} detail={relative(event.at)}
      trailing={event.version && event.version !== item.package.version && !earlier.has(event.version) ? undefined : event.version && event.version !== item.package.version ? <Button size="sm" variant="quiet" disabled={!!action.pending} onClick={() => void action.run("rollback", async () => {
        const data = await getJSON<InstallationReview>(`/plugins/installations/${encodeURIComponent(id)}/review?version=${encodeURIComponent(event.version!)}`);
        const receipt = await mutate<Installation>(`/plugins/installations/${encodeURIComponent(id)}/actions`, { request_id: newRequestID(), expected_revision: item.revision, value: { action: "rollback", version: event.version, approved_access: approvedFor(item.approved_access, data, entry && entry.latest_version === event.version ? entry.requested_access : undefined) } });
        invalidate("/plugins", "/integrations"); installation.apply(receipt.resource as Installation);
      }, "Version changed.")}>Use this version</Button> : undefined} />)}</Rows></Section>}
    <Section title="Manage"><div className="flex flex-wrap gap-2">
      <Button onClick={() => void act({ action: item.enabled ? "disable" : "enable" }, "toggle")} disabled={!!action.pending}>{item.enabled ? "Turn off" : "Turn on"}</Button>
      <Button className="text-danger!" variant="quiet" onClick={() => void loadReview()} disabled={!!action.pending}>Uninstall…</Button>
    </div></Section>
  </Screen>;
}

type Access = Installation["approved_access"];

/** The access an update or rollback needs approved: what the installation has, plus what the review says is new. */
function approvedFor(current: Access, review: InstallationReview, target?: Access): Access {
  // When the catalog knows the target version's request, approve all of it: an installation made before hosts
  // were recorded has an approval that lists none, so the review's additions alone fall short.
  const base = { connection_types: [...current.connection_types, ...(target?.connection_types ?? [])], action_classes: [...current.action_classes, ...(target?.action_classes ?? [])],
    network: [...(current.network ?? []), ...(target?.network ?? [])], filesystem: [...(current.filesystem ?? []), ...(target?.filesystem ?? [])] };
  const next = base;
  const removed = new Set(review.access_removed);
  for (const entry of review.access_added) {
    const [kind, value] = entry.includes(":") ? [entry.slice(0, entry.indexOf(":")), entry.slice(entry.indexOf(":") + 1)] : ["", entry];
    if (kind === "network") next.network.push(value);
    else if (kind === "filesystem") next.filesystem.push(value);
    else if (kind === "connection") next.connection_types.push(value);
    else if (kind === "action") next.action_classes.push(value as Installation["approved_access"]["action_classes"][number]);
  }
  const keep = (kind: string) => (value: string) => !removed.has(`${kind}:${value}`);
  return { connection_types: [...new Set(next.connection_types.filter(keep("connection")))], action_classes: [...new Set(next.action_classes.filter(keep("action")))],
    network: [...new Set(next.network.filter(keep("network")))], filesystem: [...new Set(next.filesystem.filter(keep("filesystem")))] };
}

function ReviewList({ data }: { data: InstallationReview }) {
  const rows = [
    ["New access", data.access_added], ["Access removed", data.access_removed], ["New tools", data.tools_added], ["Tools removed", data.tools_removed], ["Changed tools", data.schemas_changed],
  ] as const;
  const dependents = data.dependents.automation_ids.length + data.dependents.goal_ids.length;
  return <dl className="mt-2 space-y-2 text-meta">
    {rows.filter(([, list]) => list.length).map(([label, list]) => <div key={label}><dt className={cn("font-medium", label === "New access" && "text-warning")}>{label}</dt><dd className="text-ink-2">{list.join(", ")}</dd></div>)}
    {dependents > 0 && <div><dt className="font-medium">Affects</dt><dd className="text-ink-2">{data.dependents.automation_ids.map((id) => <Link key={id} className="mr-2 text-accent-text hover:underline" href={`/routines/${encodeURIComponent(id)}`}>an automation</Link>)}{data.dependents.goal_ids.map((id) => <Link key={id} className="mr-2 text-accent-text hover:underline" href={`/goals/${encodeURIComponent(id)}`}>a goal</Link>)}</dd></div>}
    {!rows.some(([, list]) => list.length) && !dependents && <p className="text-ink-2">Nothing else changes.</p>}
  </dl>;
}

function MCPServers() {
  const servers = useResource<List<MCPServer>>("/mcp", { poll: 15_000 });
  const [adding, setAdding] = useState(false);
  if (servers.loading) return <Loading />;
  const items = servers.data?.items ?? [];
  return <>
    <Section actions={!adding ? <Button size="sm" variant="primary" onClick={() => setAdding(true)}><Plus size={15} aria-hidden />Add server</Button> : undefined}>
      {adding && <div className="mb-4"><NewMCPServer onDone={() => setAdding(false)} /></div>}
      {items.length ? <Rows>{items.map((server) => <Row key={server.id} href={`/apps/mcp/${encodeURIComponent(server.id)}`} leading={<span className="grid size-9 place-items-center rounded-tile bg-fill-1"><Server size={18} aria-hidden /></span>}
        title={server.name} detail={`${server.transport === "http" ? "Remote" : "Runs on Nori’s computer"}${server.observed.tool_count !== undefined ? ` · ${plural(server.observed.tool_count, "tool")}` : ""}${server.observed.error ? ` · ${server.observed.error.message}` : ""}`}
        trailing={<MCPStatus server={server} />} />)}</Rows> : !adding && <Empty>No MCP servers yet.</Empty>}
    </Section>
  </>;
}

function NewMCPServer({ onDone }: { onDone: () => void }) {
  const router = useRouter();
  const action = useAction();
  const [name, setName] = useState("");
  const [endpoint, setEndpoint] = useState("");
  // OAuth sign-in to MCP servers isn't implemented by the core yet, so it isn't offered.
  const [auth, setAuth] = useState<"none" | "configured">("none");
  const [exposure, setExposure] = useState<"direct" | "discoverable">("discoverable");
  const validEndpoint = /^https:\/\/[^\s]+$/.test(endpoint.trim());
  const save = () => action.run("save", async () => {
    const receipt = await mutate<MCPServer>("/mcp", { request_id: newRequestID(), expected_revision: 0, value: { name: name.trim(), transport: "http", endpoint: endpoint.trim(), auth, enabled: true, exposure: { default: exposure } } });
    invalidate("/mcp", "/integrations");
    router.push(`/apps/mcp/${encodeURIComponent((receipt.resource as MCPServer).id)}`);
  });
  return <form className="space-y-4 rounded-card border border-hairline-strong p-4" onSubmit={(event) => { event.preventDefault(); if (name.trim() && validEndpoint) void save(); }}>
    <div className="flex flex-wrap gap-3">
      <Field label="Name"><input value={name} onChange={(event) => setName(event.target.value)} className={cn(inputBase, "w-56")} placeholder="Home notes" required /></Field>
      <Field label="Server URL"><input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} className={cn(inputBase, "w-96 max-w-full")} placeholder="https://mcp.example.com/mcp" inputMode="url" required /></Field>
    </div>
    <div className="flex flex-wrap gap-3">
      <Field label="Sign-in"><select value={auth} onChange={(event) => setAuth(event.target.value as typeof auth)} className={cn(inputBase, "w-auto")}><option value="none">None</option><option value="configured">Access token</option></select></Field>
      <Field label="Tools"><select value={exposure} onChange={(event) => setExposure(event.target.value as typeof exposure)} className={cn(inputBase, "w-auto")}><option value="discoverable">When relevant</option><option value="direct">Always available</option></select></Field>
    </div>
    {endpoint && !validEndpoint && <p role="alert" className="text-meta text-danger">Use an https:// address.</p>}
    {action.error && <Notice tone="danger">{action.error}</Notice>}
    <div className="flex gap-2"><Button type="submit" variant="primary" disabled={!name.trim() || !validEndpoint || !!action.pending}>{action.pending ? "Adding…" : "Add server"}</Button><Button variant="quiet" onClick={onDone}>Cancel</Button></div>
  </form>;
}

function Credential({ server, onSaved }: { server: MCPServer; onSaved: (next: MCPServer) => void }) {
  const action = useAction();
  const [token, setToken] = useState("");
  const [replacing, setReplacing] = useState(false);
  const present = server.observed.credential === "present";
  const save = () => action.run("save", async () => {
    const value = token.trim();
    setToken("");
    const body: MCPCredentialInput = { request_id: newRequestID(), token: value };
    const receipt = await sendSecret<MCPServer>(`/mcp/${encodeURIComponent(server.id)}/credential`, body);
    onSaved(receipt.resource as MCPServer); setReplacing(false); invalidate(`/mcp/${encodeURIComponent(server.id)}/tools`);
  });
  if (present && !replacing) return <div className="flex flex-wrap items-center gap-3"><Pill tone="ok">Saved</Pill><Button size="sm" variant="quiet" onClick={() => setReplacing(true)}>Replace</Button></div>;
  return <form className="flex flex-col gap-2 sm:flex-row" onSubmit={(event) => { event.preventDefault(); if (token.trim()) void save(); }}>
    <input aria-label="Token" type="password" autoComplete="off" spellCheck={false} value={token} onChange={(event) => setToken(event.target.value)} className={inputClass} placeholder={present ? "New token" : "Paste the server’s access token"} />
    <div className="flex gap-2"><Button type="submit" variant="primary" disabled={!token.trim() || !!action.pending}>{action.pending ? "Saving…" : "Save"}</Button>{replacing && <Button variant="quiet" onClick={() => { setReplacing(false); setToken(""); }}>Cancel</Button>}</div>
    {action.error && <p role="alert" className="text-meta text-danger">{/credential storage is not configured/i.test(action.error) ? "This Nori can’t store access tokens yet. Nothing was saved." : action.error}</p>}
  </form>;
}

function MCPStatus({ server }: { server: MCPServer }) {
  const status = server.observed.status;
  const tone: Tone = status === "connected" ? "ok" : status === "failed" || status === "needs_sign_in" ? "danger" : "neutral";
  return <Pill tone={tone}>{{ connected: "Connected", connecting: "Connecting", disconnected: server.transport === "stdio" ? "Stopped" : "Not connected", needs_sign_in: "Needs sign-in", failed: "Failed", disabled: "Off" }[status]}</Pill>;
}

export function MCPDetailScreen({ id }: { id: string }) {
  const server = useResource<MCPServer>(`/mcp/${encodeURIComponent(id)}`, { poll: 5_000 });
  const tools = useResource<List<MCPTool>>(`/mcp/${encodeURIComponent(id)}/tools`, { poll: 0 });
  const action = useAction();
  const item = server.data;
  if (server.loading) return <Screen title="MCP server" back="/apps?tab=mcp"><Loading /></Screen>;
  if (!item) return <Screen title="MCP server" back="/apps?tab=mcp"><Failure error={server.error ?? "Not found."} /></Screen>;
  const act = (value: Record<string, unknown>, label: string) => action.run(label, async () => {
    const receipt = await mutate<MCPServer>(`/mcp/${encodeURIComponent(id)}/actions`, { request_id: newRequestID(), expected_revision: item.revision, value });
    server.apply(receipt.resource as MCPServer); invalidate(`/mcp/${encodeURIComponent(id)}/tools`);
  });
  const observed = item.observed;
  return <Screen title={item.name} back="/apps?tab=mcp" line={observed.error?.message ?? (observed.status === "connected" ? `Connected${observed.last_success_at ? `, last call ${relative(observed.last_success_at)}` : ""}.` : undefined)}
    actions={<><MCPStatus server={item} /><RefreshButton onClick={() => void server.reload()} /></>}>
    {action.error && <div className="mb-4"><Notice tone="danger">{action.error}</Notice></div>}
    <Section title="Connection"><dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
      <dt className="text-ink-3">Transport</dt><dd>{item.transport === "http" ? "Streamable HTTP" : "Managed process (stdio)"}</dd>
      {item.endpoint && <><dt className="text-ink-3">Endpoint</dt><dd className="[overflow-wrap:anywhere]">{item.endpoint}</dd></>}
      <dt className="text-ink-3">From</dt><dd>{item.source.kind === "installation" ? <Link className="text-accent-text hover:underline" href={`/apps/plugins/${encodeURIComponent(item.source.installation_id)}`}>A plugin</Link> : "Added by you"}</dd>
      {observed.protocol_version && <><dt className="text-ink-3">Protocol</dt><dd>{observed.protocol_version}</dd></>}
      {!!observed.capabilities?.length && <><dt className="text-ink-3">Supports</dt><dd>{observed.capabilities.join(", ")}</dd></>}
    </dl>
      <div className="mt-4 flex flex-wrap gap-2">
        {item.transport === "stdio" ? <>
          <Button size="sm" variant="primary" disabled={!!action.pending} onClick={() => void act({ action: observed.status === "connected" ? "restart" : "start" }, "start")}>{observed.status === "connected" ? "Restart" : "Start"}</Button>
          {observed.status === "connected" && <Button size="sm" disabled={!!action.pending} onClick={() => void act({ action: "stop" }, "stop")}>Stop</Button>}
        </> : <Button size="sm" variant="primary" disabled={!!action.pending} onClick={() => void act({ action: "reconnect" }, "reconnect")}>Reconnect</Button>}
        {observed.status === "needs_sign_in" && <Button size="sm" onClick={() => void act({ action: "sign_in" }, "sign_in")}>Sign in</Button>}
      </div></Section>
    {item.auth === "configured" && <Section title="Access token"><Credential server={item} onSaved={(next) => server.apply(next)} /></Section>}
    <Section title="Tools">
      {tools.loading ? <Loading /> : tools.data?.items.length ? <Rows>{tools.data.items.map((tool) => <li key={tool.name} className="flex min-h-14 flex-wrap items-center gap-3 px-4 py-2">
        <span className="min-w-0 flex-1"><span className="block text-row font-medium">{tool.title}</span><span className="block font-mono text-mono-data text-ink-3">{tool.name}</span></span>
        <label className="text-meta text-ink-2"><span className="sr-only">How Nori sees {tool.title}</span>
          <select value={tool.exposure} onChange={(event) => void act({ action: "set_exposure", tool: tool.name, exposure: event.target.value }, `exposure:${tool.name}`)} className="h-9 rounded-tile border border-hairline bg-surface px-2">
            <option value="direct">Always available</option><option value="discoverable">When relevant</option><option value="disabled">Off</option>
          </select></label>
      </li>)}</Rows> : <Empty>No tools discovered.</Empty>}
    </Section>
  </Screen>;
}
