import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { createResourceCache, ResourceCacheContext } from "@/lib/assistant/useResource";
import { installTestDOM, settleTestDOM } from "@/lib/assistant/test-dom";
import { asFetch } from "@/lib/test-fetch";
import { OnboardingFlow } from "./OnboardingFlow";

describe("OnboardingFlow save-failure handling", () => {
  let restoreDOM: () => void;
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    restoreDOM = installTestDOM();
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
    restoreDOM();
  });

  test("surfaces PUT /preferences failure with Retry and does not duplicate saved memory facts on retry", async () => {
    let failPreferences = true;
    const calls: { url: string; method: string; body?: unknown }[] = [];

    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ url, method, body });

      if (url === "/api/v1/settings/model-keys") return Response.json({ byok_required: false, providers: [] });
      if (url === "/api/v1/settings/models") return Response.json({ revision: 1, roles: {} });
      if (url === "/api/v1/settings/home-connectors") return Response.json({ enabled: false, connectors: [] });
      if (url === "/api/v1/memory" && method === "POST") {
        return Response.json({ status: "applied", resource: { id: `mem_${calls.length}`, revision: 1 } });
      }
      if (url === "/api/v1/preferences" && method === "PUT") {
        if (failPreferences) {
          return Response.json({ error: "Could not save preferences" }, { status: 503 });
        }
        return Response.json({ status: "applied", resource: { revision: 2 } });
      }
      return Response.json({ ok: true });
    });

    let completed = 0;
    const target = document.createElement("div");
    document.body.append(target);
    const root = createRoot(target);
    try {
      await act(async () => {
        root.render(
          <ResourceCacheContext.Provider value={createResourceCache()}>
            <OnboardingFlow initialStep={4} onComplete={() => { completed++; }} />
          </ResourceCacheContext.Provider>,
        );
      });
      await settleTestDOM();

      const startButton = [...target.querySelectorAll("button")].find((button) => button.textContent?.includes("Start chatting"));
      assert.ok(startButton, "expected Start chatting button");

      await act(async () => {
        startButton.click();
      });
      await settleTestDOM();

      assert.equal(completed, 0, "must not complete onboarding when preferences save fails");
      assert.match(target.textContent ?? "", /Could not save preferences/);

      const memoryPostsAfterFirstTry = calls.filter((call) => call.url === "/api/v1/memory" && call.method === "POST").length;
      assert.ok(memoryPostsAfterFirstTry > 0, "expected initial memory facts to be saved");

      const retryButton = [...target.querySelectorAll("button")].find((button) => button.textContent?.trim() === "Retry");
      assert.ok(retryButton, "expected Retry button in error notice");

      failPreferences = false;
      await act(async () => {
        retryButton.click();
      });
      await settleTestDOM();

      assert.equal(completed, 1, "should complete onboarding once retry succeeds");
      const memoryPostsAfterRetry = calls.filter((call) => call.url === "/api/v1/memory" && call.method === "POST").length;
      assert.equal(memoryPostsAfterRetry, memoryPostsAfterFirstTry, "already-saved memory facts must not be re-posted on retry");
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });
});
