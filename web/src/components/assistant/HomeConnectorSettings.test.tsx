import assert from "node:assert/strict";
import { afterEach, beforeEach, test } from "node:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { ResourceCacheContext, createResourceCache } from "@/lib/assistant/useResource";
import { installTestDOM, settleTestDOM } from "@/lib/assistant/test-dom";
import { HomePairingForm } from "./HomeConnectorSettings";

let restoreDOM: () => void;
const originalFetch = globalThis.fetch;

beforeEach(() => { restoreDOM = installTestDOM(); });
afterEach(() => { globalThis.fetch = originalFetch; restoreDOM(); });

test("pairing checks the code and requires explicit approval before sending it", async () => {
  const calls: { url: string; body: unknown }[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, body: JSON.parse(String(init?.body)) });
    if (url.endsWith("/lookup")) {
      return Response.json({ name: "Home Mac", user_code: "ABCD-EFGH", expires_at: "2026-12-01T00:00:00Z" });
    }
    return new Response(null, { status: 204 });
  }) as typeof fetch;
  const target = document.createElement("div");
  document.body.append(target);
  const root = createRoot(target);
  try {
    await act(async () => root.render(
      <ResourceCacheContext.Provider value={createResourceCache()}><HomePairingForm initialCode="ABCD-EFGH" /></ResourceCacheContext.Provider>,
    ));
    await act(async () => target.querySelector("form")?.dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true })));
    await settleTestDOM();
    assert.equal(calls.length, 1);
    assert.match(target.textContent ?? "", /Pair Home Mac/);
    assert.match(target.textContent ?? "", /ABCD-EFGH/);
    const approve = [...target.querySelectorAll("button")].find((button) => button.textContent === "Pair device");
    assert.ok(approve);
    await act(async () => approve.click());
    await settleTestDOM();
    assert.deepEqual(calls, [
      { url: "/api/v1/settings/home-connectors/pairing/lookup", body: { user_code: "ABCD-EFGH" } },
      { url: "/api/v1/settings/home-connectors/pairing/approve", body: { user_code: "ABCD-EFGH" } },
    ]);
    assert.match(target.textContent ?? "", /Computer paired/);
  } finally {
    await act(async () => root.unmount());
    target.remove();
  }
});
