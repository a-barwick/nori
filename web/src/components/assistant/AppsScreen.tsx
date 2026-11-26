"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Plus, Server } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { mutate, newRequestID, sendSecret } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { plural, relative } from "@/lib/assistant/format";
import { useResourceInvalidation, useAction, useResource } from "@/lib/assistant/useResource";
import type { AuthAttempt, Connection, HealthCode, MCPCredentialInput, MCPServer, MCPServerTool as MCPTool } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { safeAuthorizeURL } from "./InteractionCard";
import { Empty, Failure, Field, inputBase, inputClass, Loading, Notice, NotYet, Pill, RefreshButton, Row, Rows, Screen, Section, type Tone } from "./ui";

type List<T> = { items: T[] };

const HEALTH: Record<HealthCode, { tone: Tone; label: string }> = {
  ready: { tone: "ok", label: "Ready" }, needs_sign_in: { tone: "danger", label: "Needs sign-in" }, needs_more_access: { tone: "warning", label: "Needs more access" },
  revoked: { tone: "danger", label: "Access removed" }, rate_limited: { tone: "warning", label: "Slowed down by provider" }, temporarily_failing: { tone: "warning", label: "Having trouble" },
  provider_unavailable: { tone: "warning", label: "Provider is down" }, credential_store_unavailable: { tone: "danger", label: "Can’t save this account" },
};

function healthOf(connection: Connection): HealthCode | undefined {
  const code = connection.health.code;
  if (connection.status === "needs_attention" && (!code || code === "ready")) return connection.missing_scopes?.length ? "needs_more_access" : "needs_sign_in";
  return code;
}

export function connectionState(connection: Connection): { tone: Tone; label: string } {
  if (connection.status === "connecting") return { tone: "neutral", label: "Connecting" };
  if (connection.status === "disconnected" || connection.status === "not_connected") return { tone: "neutral", label: "Not connected" };
  if (connection.status === "disabled") return { tone: "neutral", label: "Off" };
  if (connection.status === "disconnecting") return { tone: "neutral", label: "Disconnecting" };
  return HEALTH[healthOf(connection) ?? "ready"] ?? { tone: "neutral", label: connection.status };
}

export async function startSignIn(connection: Pick<Connection, "id" | "revision">, action: "connect" | "repair", returnTo: string, capabilities?: string[]) {
  const receipt = await mutate<Connection>(`/connections/${encodeURIComponent(connection.id)}/actions`, {
    request_id: newRequestID(), expected_revision: connection.revision, value: { action, return_to: returnTo, ...(capabilities?.length ? { capabilities } : {}) },
  }) as { auth?: AuthAttempt };
  const url = receipt.auth && safeAuthorizeURL(receipt.auth.authorize_url);
  if (!url) throw new Error("Nori didn’t return a sign-in link. Try again.");
  window.location.assign(url);
}

function useReturnedResult() {
  const params = useSearchParams();
  const [returned] = useState(() => ({ result: params.get("result"), connection: params.get("connection") }));
  useEffect(() => {
    const url = new URL(window.location.href);
    if (!url.searchParams.has("result") && !url.searchParams.has("connection")) return;
    url.searchParams.delete("result"); url.searchParams.delete("connection");
    window.history.replaceState(window.history.state, "", url);
  }, []);
  return returned;
}

export function AppsScreen() {
  const { has } = useAssistant();
  const params = useSearchParams();
  const router = useRouter();
  const signedInServer = params.get("mcp");
  const signInResult = params.get("result");
  useEffect(() => {
    if (signedInServer) router.replace(`/apps/mcp/${encodeURIComponent(signedInServer)}?result=${encodeURIComponent(signInResult ?? "")}`);
  }, [signedInServer, signInResult, router]);
  if (!has("mcp") && !has("connections")) return <Screen title="Apps & MCP" back="/more" backLabel="More"><NotYet what="MCP servers" /></Screen>;
  return (
    <Screen title="Apps & MCP" back="/more" backLabel="More">
      {has("mcp") ? <MCPServers /> : <NotYet what="MCP servers" />}
      {has("connections") && <ActiveConnections />}
    </Screen>
  );
}

function ActiveConnections() {
  const connections = useResource<List<Connection>>("/connections", { poll: 30_000 });
  const items = (connections.data?.items ?? []).filter((connection) => connection.status !== "disconnected" && !(connection.status === "not_connected" && !connection.account?.provider_id));
  if (!items.length) return null;
  return (
    <Section title="Connections">
      <Rows>{items.map((connection) => {
        const state = connectionState(connection);
        return <Row key={connection.id} title={[connection.title, connection.account?.label].filter(Boolean).join(" · ")}
          detail={connection.health.message || (connection.health.last_success_at ? `Last used ${relative(connection.health.last_success_at)}` : undefined)}
          trailing={<Pill tone={state.tone}>{state.label}</Pill>} />;
      })}</Rows>
    </Section>
  );
}

function MCPServers() {
  const servers = useResource<List<MCPServer>>("/mcp", { poll: 15_000 });
  const [adding, setAdding] = useState(false);
  if (servers.loading) return <Loading />;
  if (servers.error && !servers.data) return <Failure error={servers.error} onRetry={() => void servers.reload()} />;
  const items = servers.data?.items ?? [];
  return (
    <Section title="MCP servers" actions={!adding ? <Button size="sm" variant="primary" onClick={() => setAdding(true)}><Plus size={15} aria-hidden />Add server</Button> : undefined}>
      {adding && <div className="mb-4"><NewMCPServer onDone={() => setAdding(false)} /></div>}
      {items.length ? <Rows>{items.map((server) => <Row key={server.id} href={`/apps/mcp/${encodeURIComponent(server.id)}`} leading={<span className="grid size-9 place-items-center rounded-tile bg-fill-1"><Server size={18} aria-hidden /></span>}
        title={server.name} detail={`${server.transport === "http" ? "Remote" : "Local stdio"}${server.observed.tool_count !== undefined ? ` · ${plural(server.observed.tool_count, "tool")}` : ""}${server.observed.error ? ` · ${server.observed.error.message}` : ""}`}
        trailing={<MCPStatus server={server} />} />)}</Rows> : !adding && <Empty>No MCP servers yet.</Empty>}
    </Section>
  );
}

function isValidMCPEndpoint(raw: string) {
  const value = raw.trim();
  if (!value || /\s/.test(value)) return false;
  try {
    const parsed = new URL(value);
    if (parsed.protocol === "https:") return true;
    return parsed.protocol === "http:" && (parsed.hostname === "127.0.0.1" || parsed.hostname === "localhost" || parsed.hostname === "[::1]");
  } catch {
    return false;
  }
}

function NewMCPServer({ onDone }: { onDone: () => void }) {
  const invalidate = useResourceInvalidation();
  const router = useRouter();
  const action = useAction();
  const [name, setName] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [auth, setAuth] = useState<"none" | "configured">("none");
  const [exposure, setExposure] = useState<"direct" | "discoverable">("discoverable");
  const validEndpoint = isValidMCPEndpoint(endpoint);
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
    {endpoint && !validEndpoint && <p role="alert" className="text-meta text-danger">Use an https:// address, or http://localhost / http://127.0.0.1 for a local server.</p>}
    {action.error && <Notice tone="danger">{action.error}</Notice>}
    <div className="flex gap-2"><Button type="submit" variant="primary" disabled={!name.trim() || !validEndpoint || !!action.pending}>{action.pending ? "Adding…" : "Add server"}</Button><Button variant="quiet" onClick={onDone}>Cancel</Button></div>
  </form>;
}

function Credential({ server, onSaved }: { server: MCPServer; onSaved: (next: MCPServer) => void }) {
  const invalidate = useResourceInvalidation();
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
  const invalidate = useResourceInvalidation();
  const server = useResource<MCPServer>(`/mcp/${encodeURIComponent(id)}`, { poll: 5_000 });
  const tools = useResource<List<MCPTool>>(`/mcp/${encodeURIComponent(id)}/tools`, { poll: 0 });
  const action = useAction();
  const { result } = useReturnedResult();
  const item = server.data;
  if (server.loading) return <Screen title="MCP server" back="/apps"><Loading /></Screen>;
  if (!item) return <Screen title="MCP server" back="/apps"><Failure error={server.error ?? "Not found."} /></Screen>;
  const act = (value: Record<string, unknown>, label: string) => action.run(label, async () => {
    const receipt = await mutate<MCPServer>(`/mcp/${encodeURIComponent(id)}/actions`, { request_id: newRequestID(), expected_revision: item.revision, value });
    server.apply(receipt.resource as MCPServer); invalidate(`/mcp/${encodeURIComponent(id)}/tools`);
  });
  const signIn = () => action.run("sign_in", async () => {
    const receipt = await mutate<MCPServer>(`/mcp/${encodeURIComponent(id)}/actions`, { request_id: newRequestID(), expected_revision: item.revision, value: { action: "sign_in" } }) as { auth?: AuthAttempt };
    const url = receipt.auth && safeAuthorizeURL(receipt.auth.authorize_url);
    if (!url) throw new Error("Nori didn’t return a sign-in link. Try again.");
    window.location.assign(url);
  });
  const observed = item.observed;
  return <Screen title={item.name} back="/apps" line={observed.error?.message ?? (observed.status === "connected" ? `Connected${observed.last_success_at ? `, last call ${relative(observed.last_success_at)}` : ""}.` : undefined)}
    actions={<><MCPStatus server={item} /><RefreshButton onClick={() => void server.reload()} /></>}>
    {result && <div className="mb-4"><Notice tone={result === "connected" ? "ok" : "danger"}>{result === "connected" ? "Signed in." : "Sign-in didn’t finish. Nothing was saved; try again."}</Notice></div>}
    {action.error && <div className="mb-4"><Notice tone="danger">{action.error}</Notice></div>}
    <Section title="Connection"><dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
      <dt className="text-ink-3">Transport</dt><dd>{item.transport === "http" ? "Streamable HTTP" : "Managed process (stdio)"}</dd>
      {item.endpoint && <><dt className="text-ink-3">Endpoint</dt><dd className="[overflow-wrap:anywhere]">{item.endpoint}</dd></>}
      <dt className="text-ink-3">From</dt><dd>Added by you</dd>
      {observed.protocol_version && <><dt className="text-ink-3">Protocol</dt><dd>{observed.protocol_version}</dd></>}
      {!!observed.capabilities?.length && <><dt className="text-ink-3">Supports</dt><dd>{observed.capabilities.join(", ")}</dd></>}
    </dl>
      <div className="mt-4 flex flex-wrap gap-2">
        {item.transport === "stdio" ? <>
          <Button size="sm" variant="primary" disabled={!!action.pending} onClick={() => void act({ action: observed.status === "connected" ? "restart" : "start" }, "start")}>{observed.status === "connected" ? "Restart" : "Start"}</Button>
          {observed.status === "connected" && <Button size="sm" disabled={!!action.pending} onClick={() => void act({ action: "stop" }, "stop")}>Stop</Button>}
        </> : <Button size="sm" variant="primary" disabled={!!action.pending} onClick={() => void act({ action: "reconnect" }, "reconnect")}>Reconnect</Button>}
        {(observed.status === "needs_sign_in" || item.auth === "oauth" && observed.status !== "connected") && <Button size="sm" disabled={!!action.pending} onClick={() => void signIn()}>{action.pending === "sign_in" ? "Opening sign-in…" : "Sign in"}</Button>}
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
