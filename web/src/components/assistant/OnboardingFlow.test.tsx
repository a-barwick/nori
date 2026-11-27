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
      if (url === "/api/v1/settings/models") return Response.json({ revision: 1, selection: {} });
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

  test("hides Skip on Steps 1–2, hides Set up later once key is configured, and starts Step 4 Focus unselected", async () => {
    let configured = false;
    globalThis.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/v1/settings/model-keys") {
        return Response.json({
          byok_required: true,
          providers: [{ provider: "openai", label: "OpenAI", configured, last4: configured ? "1234" : undefined }],
        });
      }
      if (url === "/api/v1/settings/models") return Response.json({ revision: 1, selection: {} });
      if (url === "/api/v1/settings/home-connectors") return Response.json({ enabled: false, connectors: [] });
      return Response.json({ ok: true });
    });

    const target = document.createElement("div");
    document.body.append(target);
    const root = createRoot(target);
    try {
      await act(async () => {
        root.render(
          <ResourceCacheContext.Provider value={createResourceCache()}>
            <OnboardingFlow initialStep={1} onComplete={() => {}} />
          </ResourceCacheContext.Provider>,
        );
      });
      await settleTestDOM();

      const headerButtonsStep1 = [...target.querySelectorAll("header button:not([aria-hidden='true'])")];
      assert.equal(headerButtonsStep1.length, 0, "Step 1 header should not expose Back or Skip buttons");

      const getStarted = [...target.querySelectorAll("button")].find((b) => b.textContent?.includes("Get started"));
      assert.ok(getStarted);
      await act(async () => { getStarted.click(); });
      await settleTestDOM();

      const headerButtonsStep2 = [...target.querySelectorAll("header button:not([aria-hidden='true'])")].map((b) => b.textContent?.trim());
      assert.deepEqual(headerButtonsStep2, ["Back"], "Step 2 should only expose Back, not duplicate Skip");

      const continueStep2 = [...target.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Continue");
      assert.ok(continueStep2);
      await act(async () => { continueStep2.click(); });
      await settleTestDOM();

      const headerButtonsStep3Unconfigured = [...target.querySelectorAll("header button:not([aria-hidden='true'])")].map((b) => b.textContent?.trim());
      assert.deepEqual(headerButtonsStep3Unconfigured, ["Back", "Set up later"], "Step 3 should show Set up later when unconfigured");

      const setUpLater = [...target.querySelectorAll<HTMLButtonElement>("header button")].find((b) => b.textContent?.trim() === "Set up later");
      assert.ok(setUpLater);
      await act(async () => { setUpLater.click(); });
      await settleTestDOM();

      const focusButtons = [...target.querySelectorAll("[role='group'][aria-labelledby='onboarding-focus-label'] button")];
      assert.ok(focusButtons.length > 0, "expected focus option buttons");
      assert.ok(
        focusButtons.every((button) => button.getAttribute("aria-pressed") === "false"),
        "Step 4 focus options should start unselected",
      );

      configured = true;
      await act(async () => {
        root.render(
          <ResourceCacheContext.Provider value={createResourceCache()}>
            <OnboardingFlow key="step3-configured" initialStep={3} onComplete={() => {}} />
          </ResourceCacheContext.Provider>,
        );
      });
      await settleTestDOM();

      const headerButtonsStep3Configured = [...target.querySelectorAll("header button:not([aria-hidden='true'])")].map((b) => b.textContent?.trim());
      assert.deepEqual(headerButtonsStep3Configured, ["Back"], "Step 3 should hide Set up later once a key is configured");
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });
});
