import assert from "node:assert/strict";
import { afterEach, beforeEach, test } from "node:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { SearchParamsContext } from "next/dist/shared/lib/hooks-client-context.shared-runtime";
import { AssistantProvider } from "@/lib/assistant/context";
import { ResourceCacheContext, createResourceCache } from "@/lib/assistant/useResource";
import { installTestDOM, settleTestDOM } from "@/lib/assistant/test-dom";
import { asFetch } from "@/lib/test-fetch";
import type { Interaction } from "@/lib/contracts/assistant";
import { GoogleConnection } from "./GoogleConnection";
import { InteractionCard } from "./InteractionCard";

let restoreDOM: () => void;
const originalFetch = globalThis.fetch;

beforeEach(() => { restoreDOM = installTestDOM(); });
afterEach(() => {
  globalThis.fetch = originalFetch;
  restoreDOM();
});

test("Google connection requests only the services the user selects", async () => {
  const calls: { url: string; method: string; body?: Record<string, unknown> }[] = [];
  globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined;
    calls.push({ url, method, body });
    if (url === "/api/v1/integrations/google") return Response.json({ id: "google", availability: "available", capabilities: [] });
    if (url === "/api/v1/connections" && method === "GET") return Response.json({ items: [] });
    if (url === "/api/v1/connections" && method === "POST") return Response.json({ resource: { id: "conn_google", revision: 1, capabilities: [] } });
    if (url === "/api/v1/connections/conn_google/actions") return Response.json({ auth: { authorize_url: "javascript:invalid" } });
    throw new Error(`Unexpected request ${method} ${url}`);
  });
  const target = document.createElement("div");
  document.body.append(target);
  const root = createRoot(target);
  try {
    await act(async () => root.render(
      <SearchParamsContext.Provider value={new URLSearchParams()}><ResourceCacheContext.Provider value={createResourceCache()}><GoogleConnection /></ResourceCacheContext.Provider></SearchParamsContext.Provider>,
    ));
    await settleTestDOM();
    const switches = [...target.querySelectorAll('button[role="switch"]')] as HTMLButtonElement[];
    assert.equal(switches.length, 3);
    assert.equal(switches.every((sw) => sw.getAttribute("aria-checked") === "false"), true);
    await act(async () => switches[2].click());
    await settleTestDOM();
    const cont = [...document.querySelectorAll("button")].find((button) => button.textContent === "Continue");
    assert.ok(cont);
    await act(async () => cont.click());
    await settleTestDOM();
    const create = calls.find((call) => call.url === "/api/v1/connections" && call.method === "POST");
    const signIn = calls.find((call) => call.url.endsWith("/actions"));
    assert.deepEqual(create?.body?.value, { integration_id: "google", title: "Google", selected_resource_ids: [] });
    assert.deepEqual(signIn?.body?.value, { action: "connect", return_to: "/apps", capabilities: ["mail.send"] });
    assert.match(target.textContent ?? "", /didn’t return a sign-in link/);
  } finally {
    await act(async () => root.unmount());
    target.remove();
  }
});

test("Drive file selection starts through the server and lists authorized files", async () => {
  const calls: { url: string; method: string; body?: Record<string, unknown> }[] = [];
  globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined;
    calls.push({ url, method, body });
    if (url === "/api/v1/integrations/google") return Response.json({ id: "google", availability: "available", capabilities: [] });
    if (url === "/api/v1/connections") return Response.json({ items: [{ id: "conn_google", revision: 2, integration_id: "google", account: { provider_id: "123", label: "person@example.com" }, status: "connected", capabilities: ["files.read", "files.write"], health: { code: "ready" } }] });
    if (url.endsWith("/resources")) return Response.json({ items: [{ kind: "drive_file", title: "Trip plan", ref: { provider_id: "file123" } }] });
    if (url.endsWith("/google-drive/picker")) return Response.json({ authorize_url: "javascript:invalid" });
    throw new Error(`Unexpected request ${method} ${url}`);
  });
  const target = document.createElement("div");
  document.body.append(target);
  const root = createRoot(target);
  try {
    await act(async () => root.render(
      <SearchParamsContext.Provider value={new URLSearchParams()}><ResourceCacheContext.Provider value={createResourceCache()}><GoogleConnection /></ResourceCacheContext.Provider></SearchParamsContext.Provider>,
    ));
    await settleTestDOM();
    assert.match(target.textContent ?? "", /Trip plan/);
    const choose = [...target.querySelectorAll("button")].find((button) => button.textContent?.includes("Choose Drive files"));
    assert.ok(choose);
    await act(async () => choose.click());
    await settleTestDOM();
    const request = calls.find((call) => call.url.endsWith("/google-drive/picker"));
    assert.deepEqual(request?.body, { return_to: "/apps" });
    assert.match(target.textContent ?? "", /didn’t return a Google Drive link/);
  } finally {
    await act(async () => root.unmount());
    target.remove();
  }
});

test("an expired Google task sign-in requests the missing and already granted capabilities", async () => {
  const calls: { url: string; body?: Record<string, unknown> }[] = [];
  const driveScope = "https://www.googleapis.com/auth/drive.file";
  const mailScope = "https://www.googleapis.com/auth/gmail.send";
  globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined;
    calls.push({ url, body });
    if (url === "/api/v1/capabilities") return Response.json({ assistant_features: {} });
    if (url === "/api/v1/integrations/google") return Response.json({ id: "google", title: "Google", capabilities: [
      { id: "files.read", supported: true, scopes: [driveScope] },
      { id: "files.write", supported: true, scopes: [driveScope] },
      { id: "mail.send", supported: true, scopes: [mailScope] },
    ] });
    if (url === "/api/v1/connections/conn_google") return Response.json({ id: "conn_google", revision: 2, integration_id: "google", account: { provider_id: "123", label: "person@example.com" }, status: "connected", capabilities: ["files.read", "files.write"], granted_scopes: [driveScope], health: {} });
    if (url.endsWith("/actions")) return Response.json({ auth: { authorize_url: "javascript:invalid" } });
    throw new Error(`Unexpected request ${url}`);
  });
  const interaction: Interaction = {
    id: "sign_in_1", revision: 1, task_id: "task_1", kind: "sign_in", status: "pending", prompt: "Connect Gmail",
    created_at: "2026-01-01T00:00:00Z", payload: {
      auth: "oauth", integration_id: "google", connection_id: "conn_google", authorize_url: "https://accounts.google.com/expired",
      expires_at: "2026-01-01T00:00:00Z", requested_scopes: [mailScope],
    },
  };
  const target = document.createElement("div");
  document.body.append(target);
  const root = createRoot(target);
  try {
    await act(async () => root.render(<AssistantProvider localMode><InteractionCard interaction={interaction} /></AssistantProvider>));
    await settleTestDOM();
    assert.match(target.textContent ?? "", /selected AI model provider/);
    const signIn = [...target.querySelectorAll("button")].find((button) => button.textContent === "Sign in");
    assert.ok(signIn);
    await act(async () => signIn.click());
    await settleTestDOM();
    const request = calls.find((call) => call.url.endsWith("/actions"));
    assert.deepEqual(request?.body?.value, { action: "repair", return_to: "/work/task_1", capabilities: ["files.read", "files.write", "mail.send"] });
  } finally {
    await act(async () => root.unmount());
    target.remove();
  }
});
