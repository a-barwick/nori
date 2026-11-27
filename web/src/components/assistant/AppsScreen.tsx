"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/primitives";
import { mutate, newRequestID, sendSecret } from "@/lib/assistant/api";
import { useAssistantConfig } from "@/lib/assistant/context";
import { useResourceInvalidation, useAction, useResource } from "@/lib/assistant/useResource";
import { useStripSearchParams } from "@/lib/browser";
import type {
  AuthAttempt,
  Connection,
  HealthCode,
  MCPCredentialInput,
  MCPServer,
  MCPServerTool as MCPTool,
} from "@/lib/contracts/assistant";
import { safeAuthorizeURL, startSignIn } from "@/lib/assistant/signIn";
import { useHydrated } from "@/lib/hooks";
import "@/lib/desktop";
import { cn } from "@/lib/utils";
import { AppBrandIcon } from "./BrandIcons";
import { GoogleConnection } from "./GoogleConnection";
import {
  Empty,
  Failure,
  Field,
  inputBase,
  inputClass,
  Loading,
  Notice,
  NotYet,
  Pill,
  Row,
  Rows,
  Screen,
  Section,
  Switch,
  type Tone,
} from "./ui";

type List<T> = { items: T[] };

const HEALTH: Record<HealthCode, { tone: Tone; label: string }> = {
  ready: { tone: "ok", label: "Ready" },
  needs_sign_in: { tone: "danger", label: "Needs sign-in" },
  needs_more_access: { tone: "warning", label: "Needs more access" },
  revoked: { tone: "danger", label: "Access removed" },
  rate_limited: { tone: "warning", label: "Slowed down by provider" },
  temporarily_failing: { tone: "warning", label: "Having trouble" },
  provider_unavailable: { tone: "warning", label: "Provider is down" },
  credential_store_unavailable: { tone: "danger", label: "Can’t save this account" },
};

function healthOf(connection: Connection): HealthCode | undefined {
  const code = connection.health.code;
  if (connection.status === "needs_attention" && (!code || code === "ready")) {
    return connection.missing_scopes?.length ? "needs_more_access" : "needs_sign_in";
  }
  return code;
}

export function connectionState(connection: Connection): { tone: Tone; label: string } {
  if (connection.status === "connecting") return { tone: "neutral", label: "Connecting" };
  if (connection.status === "disconnected" || connection.status === "not_connected") {
    return { tone: "neutral", label: "Not connected" };
  }
  if (connection.status === "disabled") return { tone: "neutral", label: "Off" };
  if (connection.status === "disconnecting") return { tone: "neutral", label: "Disconnecting" };
  return HEALTH[healthOf(connection) ?? "ready"] ?? { tone: "neutral", label: connection.status };
}

export { startSignIn };

const RETURN_PARAMS = ["result", "connection"] as const;

function useReturnedResult() {
  const params = useSearchParams();
  const [returned] = useState(() => ({ result: params.get("result"), connection: params.get("connection") }));
  useStripSearchParams(RETURN_PARAMS);
  return returned;
}

export function AppsScreen() {
  const { has } = useAssistantConfig();
  const params = useSearchParams();
  const router = useRouter();
  const signedInServer = params.get("mcp");
  const signInResult = params.get("result");
  useEffect(() => {
    if (signedInServer) {
      router.replace(`/apps/mcp/${encodeURIComponent(signedInServer)}?result=${encodeURIComponent(signInResult ?? "")}`);
    }
  }, [signedInServer, signInResult, router]);
  if (!has("mcp") && !has("connections")) {
    return (
      <Screen title="Apps" back="/more" backLabel="More">
        <NotYet what="Apps" />
      </Screen>
    );
  }
  return (
    <Screen title="Apps" back="/more" backLabel="More">
      {has("connections") && <GoogleConnection />}
      {has("mcp") ? <CustomApps /> : <NotYet what="Custom apps" />}
      {has("connections") && <ActiveConnections />}
    </Screen>
  );
}

function ActiveConnections() {
  const connections = useResource<List<Connection>>("/connections", { poll: 30_000 });
  const items = (connections.data?.items ?? []).filter(
    (connection) =>
      connection.integration_id !== "google" &&
      connection.status !== "disconnected" &&
      !(connection.status === "not_connected" && !connection.account?.provider_id),
  );
  if (!items.length) return null;
  return (
    <Section title="Connected accounts">
      <Rows>
        {items.map((connection) => {
          const state = connectionState(connection);
          return (
            <Row
              key={connection.id}
              leading={<AppBrandIcon name={connection.title} />}
              title={connection.title}
              trailing={state.tone !== "ok" ? <Pill tone={state.tone}>{state.label}</Pill> : undefined}
            />
          );
        })}
      </Rows>
    </Section>
  );
}

function CustomApps() {
  const servers = useResource<List<MCPServer>>("/mcp", { poll: 15_000 });
  const [adding, setAdding] = useState(false);
  if (servers.loading) return <Loading />;
  if (servers.error && !servers.data) {
    return <Failure error={servers.error} onRetry={() => void servers.reload()} />;
  }
  const items = servers.data?.items ?? [];
  return (
    <Section
      title="Custom apps"
      actions={
        !adding ? (
          <button
            type="button"
            onClick={() => setAdding(true)}
            className="text-meta font-semibold text-accent-text hover:underline"
          >
            + Add
          </button>
        ) : undefined
      }
    >
      {adding && (
        <div className="mb-4">
          <NewMCPServer onDone={() => setAdding(false)} />
        </div>
      )}
      {items.length ? (
        <Rows label="Custom apps">
          {items.map((server) => (
            <Row
              key={server.id}
              href={`/apps/mcp/${encodeURIComponent(server.id)}`}
              leading={<AppBrandIcon name={server.name} endpoint={server.endpoint} />}
              title={server.name}
              trailing={<MCPStatus server={server} />}
            />
          ))}
        </Rows>
      ) : (
        !adding && <Empty>No custom apps yet.</Empty>
      )}
    </Section>
  );
}

function isValidMCPEndpoint(raw: string) {
  const value = raw.trim();
  if (!value || /\s/.test(value)) return false;
  try {
    const parsed = new URL(value);
    if (parsed.protocol === "https:") return true;
    return (
      parsed.protocol === "http:" &&
      (parsed.hostname === "127.0.0.1" ||
        parsed.hostname === "localhost" ||
        parsed.hostname === "[::1]" ||
        parsed.hostname.endsWith(".local"))
    );
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
  const [auth, setAuth] = useState<"oauth" | "none" | "configured">("oauth");
  const [showSignInOptions, setShowSignInOptions] = useState(false);
  const validEndpoint = isValidMCPEndpoint(endpoint);
  const save = () =>
    action.run("save", async () => {
      const receipt = await mutate<MCPServer>("/mcp", {
        request_id: newRequestID(),
        expected_revision: 0,
        value: {
          name: name.trim(),
          transport: "http",
          endpoint: endpoint.trim(),
          auth,
          enabled: true,
          exposure: { default: "discoverable" },
        },
      });
      invalidate("/mcp", "/integrations");
      router.push(`/apps/mcp/${encodeURIComponent((receipt.resource as MCPServer).id)}`);
    });
  return (
    <form
      className="space-y-4 rounded-card border border-card-border bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (name.trim() && validEndpoint) void save();
      }}
    >
      <Field label="Name">
        <input
          value={name}
          onChange={(event) => setName(event.target.value)}
          className={inputClass}
          placeholder="Home notes"
          required
        />
      </Field>
      <Field label="Server URL">
        <input
          value={endpoint}
          onChange={(event) => setEndpoint(event.target.value)}
          className={inputClass}
          placeholder="https://mcp.example.com/mcp"
          inputMode="url"
          required
        />
      </Field>
      <div>
        <button
          type="button"
          onClick={() => setShowSignInOptions((current) => !current)}
          className="text-meta font-semibold text-accent-text hover:underline"
        >
          {showSignInOptions ? "Hide sign-in options" : "Sign-in options"}
        </button>
        {showSignInOptions && (
          <div className="mt-3">
            <Field label="Authentication">
              <select
                value={auth}
                onChange={(event) => setAuth(event.target.value as typeof auth)}
                className={cn(inputBase, "w-full sm:w-auto")}
              >
                <option value="oauth">OAuth</option>
                <option value="configured">Access token</option>
                <option value="none">None</option>
              </select>
            </Field>
          </div>
        )}
      </div>
      {endpoint && !validEndpoint && (
        <p role="alert" className="text-meta text-danger">
          Use an https:// address, or http://localhost / 127.0.0.1 / .local for a local server.
        </p>
      )}
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      <div className="flex gap-2">
        <Button type="submit" variant="primary" disabled={!name.trim() || !validEndpoint || !!action.pending}>
          {action.pending ? "Adding…" : "Add app"}
        </Button>
        <Button variant="quiet" onClick={onDone}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function Credential({ server, onSaved }: { server: MCPServer; onSaved: (next: MCPServer) => void }) {
  const invalidate = useResourceInvalidation();
  const action = useAction();
  const [token, setToken] = useState("");
  const [replacing, setReplacing] = useState(false);
  const present = server.observed.credential === "present";
  const save = () =>
    action.run("save", async () => {
      const value = token.trim();
      setToken("");
      const body: MCPCredentialInput = { request_id: newRequestID(), token: value };
      const receipt = await sendSecret<MCPServer>(`/mcp/${encodeURIComponent(server.id)}/credential`, body);
      onSaved(receipt.resource as MCPServer);
      setReplacing(false);
      invalidate(`/mcp/${encodeURIComponent(server.id)}/tools`);
    });
  if (present && !replacing) {
    return (
      <Rows>
        <Row
          title="Access token"
          trailing={
            <Button size="sm" variant="quiet" onClick={() => setReplacing(true)}>
              Replace
            </Button>
          }
        />
      </Rows>
    );
  }
  return (
    <form
      className="space-y-3 rounded-card border border-card-border bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (token.trim()) void save();
      }}
    >
      <Field label="Access token">
        <input
          aria-label="Token"
          type="password"
          autoComplete="off"
          spellCheck={false}
          value={token}
          onChange={(event) => setToken(event.target.value)}
          className={inputClass}
          placeholder={present ? "New token" : "Paste the app’s access token"}
        />
      </Field>
      <div className="flex gap-2">
        <Button type="submit" size="sm" variant="primary" disabled={!token.trim() || !!action.pending}>
          {action.pending ? "Saving…" : "Save"}
        </Button>
        {replacing && (
          <Button
            size="sm"
            variant="quiet"
            onClick={() => {
              setReplacing(false);
              setToken("");
            }}
          >
            Cancel
          </Button>
        )}
      </div>
      {action.error && (
        <p role="alert" className="text-meta text-danger">
          {/credential storage is not configured/i.test(action.error)
            ? "This Nori can’t store access tokens yet. Nothing was saved."
            : action.error}
        </p>
      )}
    </form>
  );
}

function MCPStatus({ server }: { server: MCPServer }) {
  if (!server.enabled) return <Pill tone="neutral">Off</Pill>;
  const status = server.observed.status;
  if (status === "connected") return null;
  const tone: Tone = status === "failed" || status === "needs_sign_in" ? "danger" : status === "connecting" ? "working" : "neutral";
  return (
    <Pill tone={tone}>
      {
        {
          connecting: "Connecting",
          disconnected: server.transport === "stdio" ? "Stopped" : "Not connected",
          needs_sign_in: "Needs sign-in",
          failed: "Failed",
          disabled: "Off",
        }[status]
      }
    </Pill>
  );
}

export function MCPDetailScreen({ id }: { id: string }) {
  const hydrated = useHydrated();
  const desktop = hydrated && !!window.noriDesktop;
  const invalidate = useResourceInvalidation();
  const server = useResource<MCPServer>(`/mcp/${encodeURIComponent(id)}`, { poll: 5_000 });
  const tools = useResource<List<MCPTool>>(`/mcp/${encodeURIComponent(id)}/tools`, { poll: 0 });
  const action = useAction();
  const { result } = useReturnedResult();
  const [editing, setEditing] = useState(false);
  const item = server.data;
  if (server.loading) {
    return (
      <Screen title="App" back="/apps" backLabel="Apps">
        <Loading />
      </Screen>
    );
  }
  if (!item) {
    return (
      <Screen title="App" back="/apps" backLabel="Apps">
        <Failure error={server.error ?? "Not found."} />
      </Screen>
    );
  }

  const act = (value: Record<string, unknown>, label: string) =>
    action.run(label, async () => {
      const receipt = await mutate<MCPServer>(`/mcp/${encodeURIComponent(id)}/actions`, {
        request_id: newRequestID(),
        expected_revision: item.revision,
        value,
      });
      server.apply(receipt.resource as MCPServer);
      invalidate("/mcp", `/mcp/${encodeURIComponent(id)}/tools`);
    });

  const toggleEnabled = (nextEnabled: boolean) =>
    action.run("toggle-enabled", async () => {
      const receipt = await mutate<MCPServer>(
        `/mcp/${encodeURIComponent(id)}`,
        {
          request_id: newRequestID(),
          expected_revision: item.revision,
          value: {
            name: item.name,
            transport: item.transport,
            ...(item.transport === "http" && item.endpoint ? { endpoint: item.endpoint } : {}),
            auth: item.auth ?? "oauth",
            enabled: nextEnabled,
            exposure: item.exposure,
          },
        },
        "PUT",
      );
      server.apply(receipt.resource as MCPServer);
      invalidate("/mcp", `/mcp/${encodeURIComponent(id)}/tools`);
    });

  const signIn = () =>
    action.run("sign_in", async () => {
      if (window.noriDesktop) {
        await window.noriDesktop.openInBrowser();
        return;
      }
      const receipt = (await mutate<MCPServer>(`/mcp/${encodeURIComponent(id)}/actions`, {
        request_id: newRequestID(),
        expected_revision: item.revision,
        value: { action: "sign_in" },
      })) as { auth?: AuthAttempt };
      const url = receipt.auth && safeAuthorizeURL(receipt.auth.authorize_url);
      if (!url) throw new Error("Nori didn’t return a sign-in link. Try again.");
      window.location.assign(url);
    });

  const observed = item.observed;
  const needsSignIn =
    observed.status === "needs_sign_in" || (item.auth === "oauth" && observed.status !== "connected");

  return (
    <Screen title={item.name} back="/apps" backLabel="Apps" actions={<MCPStatus server={item} />}>
      {result && (
        <div className="mb-4">
          <Notice tone={result === "connected" ? "ok" : "danger"}>
            {result === "connected" ? "Signed in." : "Sign-in didn’t finish. Nothing was saved; try again."}
          </Notice>
        </div>
      )}
      {needsSignIn && (
        <div className="mb-4">
          <Notice
            tone="danger"
            action={
              <Button size="sm" variant="primary" disabled={!!action.pending} onClick={() => void signIn()}>
                {action.pending === "sign_in" ? "Opening…" : desktop ? "Continue in browser" : "Sign in"}
              </Button>
            }
          >
            {observed.error?.message || `Sign in to reconnect ${item.name}.`}
          </Notice>
        </div>
      )}
      {!needsSignIn && observed.error?.message && (
        <div className="mb-4">
          <Notice tone="danger">{observed.error.message}</Notice>
        </div>
      )}
      {action.error && (
        <div className="mb-4">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}

      {editing ? (
        <EditMCPServer
          server={item}
          onCancel={() => setEditing(false)}
          onSaved={(next) => {
            server.apply(next);
            invalidate("/mcp", `/mcp/${encodeURIComponent(id)}/tools`);
            setEditing(false);
          }}
        />
      ) : (
        <Rows label="App controls">
          {item.transport === "http" && (
            <Row
              title="Enabled"
              trailing={
                <Switch
                  checked={item.enabled}
                  label={`Enable ${item.name}`}
                  disabled={!!action.pending}
                  onCheckedChange={(next) => void toggleEnabled(next)}
                />
              }
            />
          )}
          {item.transport === "stdio" ? (
            <>
              <Row
                title={observed.status === "connected" ? "Restart" : "Start"}
                onClick={() => void act({ action: observed.status === "connected" ? "restart" : "start" }, "start")}
                chevron
              />
              {observed.status === "connected" && (
                <Row title="Stop" onClick={() => void act({ action: "stop" }, "stop")} chevron />
              )}
            </>
          ) : (
            <Row
              title={action.pending === "reconnect" ? "Reconnecting…" : "Reconnect"}
              onClick={() => void act({ action: "reconnect" }, "reconnect")}
              chevron
            />
          )}
          {item.transport === "http" && <Row title="Settings" onClick={() => setEditing(true)} chevron />}
        </Rows>
      )}

      {item.auth === "configured" && (
        <Section title="Access token">
          <Credential server={item} onSaved={(next) => server.apply(next)} />
        </Section>
      )}

      <Section title="Actions">
        {tools.loading ? (
          <Loading />
        ) : tools.data?.items.length ? (
          <Rows label="Actions">
            {tools.data.items.map((tool) => {
              const isOn = tool.exposure !== "disabled";
              return (
                <Row
                  key={tool.name}
                  title={tool.title}
                  trailing={
                    <Switch
                      checked={isOn}
                      label={tool.title}
                      disabled={!!action.pending}
                      onCheckedChange={(next) =>
                        void act(
                          { action: "set_exposure", tool: tool.name, exposure: next ? "discoverable" : "disabled" },
                          `exposure:${tool.name}`,
                        )
                      }
                    />
                  }
                />
              );
            })}
          </Rows>
        ) : (
          <Empty>No actions discovered yet.</Empty>
        )}
      </Section>
    </Screen>
  );
}

function EditMCPServer({
  server,
  onSaved,
  onCancel,
}: {
  server: MCPServer;
  onSaved: (next: MCPServer) => void;
  onCancel: () => void;
}) {
  const action = useAction();
  const [name, setName] = useState(server.name);
  const [endpoint, setEndpoint] = useState(server.endpoint ?? "");
  const [auth, setAuth] = useState<"oauth" | "none" | "configured">(server.auth ?? "oauth");
  const validEndpoint = server.transport !== "http" || isValidMCPEndpoint(endpoint);
  const save = () =>
    action.run("save", async () => {
      const receipt = await mutate<MCPServer>(
        `/mcp/${encodeURIComponent(server.id)}`,
        {
          request_id: newRequestID(),
          expected_revision: server.revision,
          value: {
            name: name.trim(),
            transport: server.transport,
            ...(server.transport === "http" ? { endpoint: endpoint.trim() } : {}),
            auth,
            enabled: server.enabled,
            exposure: server.exposure,
          },
        },
        "PUT",
      );
      onSaved(receipt.resource as MCPServer);
    });
  return (
    <form
      className="space-y-4 rounded-card border border-card-border bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (name.trim() && validEndpoint) void save();
      }}
    >
      <Field label="Name">
        <input
          value={name}
          onChange={(event) => setName(event.target.value)}
          className={inputClass}
          required
        />
      </Field>
      {server.transport === "http" && (
        <Field label="Server URL">
          <input
            value={endpoint}
            onChange={(event) => setEndpoint(event.target.value)}
            className={inputClass}
            inputMode="url"
            required
          />
        </Field>
      )}
      <Field label="Authentication">
        <select
          value={auth}
          onChange={(event) => setAuth(event.target.value as typeof auth)}
          className={cn(inputBase, "w-full sm:w-auto")}
        >
          <option value="oauth">OAuth</option>
          <option value="configured">Access token</option>
          <option value="none">None</option>
        </select>
      </Field>
      {server.transport === "http" && endpoint && !validEndpoint && (
        <p role="alert" className="text-meta text-danger">
          Use an https:// address, or http://localhost / 127.0.0.1 / .local for a local server.
        </p>
      )}
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      <div className="flex gap-2">
        <Button type="submit" size="sm" variant="primary" disabled={!name.trim() || !validEndpoint || !!action.pending}>
          {action.pending ? "Saving…" : "Save"}
        </Button>
        <Button size="sm" variant="quiet" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
