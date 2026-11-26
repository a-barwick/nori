import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { ResourceCacheContext, createResourceCache } from "@/lib/assistant/useResource";
import { installTestDOM, settleTestDOM } from "@/lib/assistant/test-dom";
import type { ModelSettings } from "@/lib/assistant/api";
import { ModelsSettings } from "./ModelsSettings";

describe("account model settings", () => {
  let restoreDOM: () => void;
  const originalFetch = globalThis.fetch;

  beforeEach(() => { restoreDOM = installTestDOM(); });
  afterEach(() => { globalThis.fetch = originalFetch; restoreDOM(); });

  test("offers a replacement for an unavailable model and saves both lanes with the current revision", async () => {
    let settings: ModelSettings = {
      model_profiles: { profiles: [{
        id: "openai-sol", version: "1", label: "OpenAI Sol", backend: "openai", model: "gpt-6.2-sol",
        lanes: ["chat", "task"], supported_reasoning: ["low", "high"], default_reasoning: "low", paid: true,
      }] },
      selection: { chat_profile_id: "removed-profile", chat_reasoning: "low", task_profile_id: "openai-sol", task_reasoning: "high" },
      revision: 7,
      recommendations: {},
    };
    const writes: unknown[] = [];
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith("/settings/model-keys")) {
        return Response.json({ byok_required: true, providers: [{ provider: "openai", label: "OpenAI", configured: true }] });
      }
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        writes.push(body);
        settings = { ...settings, selection: body.selection, revision: 8 };
      }
      return Response.json(settings);
    }) as typeof fetch;

    const target = document.createElement("div");
    document.body.append(target);
    const root = createRoot(target);
    try {
      await act(async () => root.render(
        <ResourceCacheContext.Provider value={createResourceCache()}><ModelsSettings /></ResourceCacheContext.Provider>,
      ));
      await settleTestDOM();
      const select = target.querySelector<HTMLSelectElement>('select[aria-label="Chat model"]');
      assert.ok(select);
      assert.match(target.textContent ?? "", /Unavailable — choose a model/);
      assert.match(target.textContent ?? "", /Choose an available model/);
      const save = [...target.querySelectorAll("button")].find((button) => button.textContent === "Save models");
      assert.ok(save?.disabled);

      await act(async () => {
        select.value = "openai-sol";
        select.dispatchEvent(new window.Event("change", { bubbles: true }));
      });
      assert.equal(writes.length, 0);
      assert.equal(save.disabled, false);
      await act(async () => { save.click(); });
      await settleTestDOM();
      assert.deepEqual(writes, [{
        expected_revision: 7,
        selection: { chat_profile_id: "openai-sol", chat_reasoning: "low", task_profile_id: "openai-sol", task_reasoning: "high" },
      }]);
      assert.equal(save.disabled, true);
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });

  test("allows disabling home-only policy when the connector feature is unavailable", async () => {
    let settings: ModelSettings = {
      model_profiles: { profiles: [{
        id: "openai-sol", version: "1", label: "OpenAI Sol", backend: "openai", model: "gpt-6.2-sol",
        lanes: ["chat", "task"], paid: true,
      }] },
      selection: { chat_profile_id: "home-gone", home_only: true },
      revision: 9,
      recommendations: {},
    };
    const writes: unknown[] = [];
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/settings/home-connectors")) return Response.json({ error: "Not found" }, { status: 404 });
      if (url.endsWith("/settings/model-keys")) return Response.json({ byok_required: true, providers: [{ provider: "openai", label: "OpenAI", configured: true }] });
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        writes.push(body);
        settings = { ...settings, selection: body.selection, revision: 10 };
      }
      return Response.json(settings);
    }) as typeof fetch;
    const target = document.createElement("div");
    document.body.append(target);
    const root = createRoot(target);
    try {
      await act(async () => root.render(
        <ResourceCacheContext.Provider value={createResourceCache()}><ModelsSettings /></ResourceCacheContext.Provider>,
      ));
      await settleTestDOM();
      const toggle = target.querySelector<HTMLInputElement>('input[type="checkbox"]');
      assert.ok(toggle?.checked);
      assert.equal(toggle.disabled, false);
      await act(async () => toggle.click());
      const select = target.querySelector<HTMLSelectElement>('select[aria-label="Chat model"]');
      assert.ok(select);
      await act(async () => {
        select.value = "openai-sol";
        select.dispatchEvent(new window.Event("change", { bubbles: true }));
      });
      const save = [...target.querySelectorAll("button")].find((button) => button.textContent === "Save models");
      assert.ok(save);
      await act(async () => save.click());
      await settleTestDOM();
      assert.deepEqual(writes, [{
        expected_revision: 9,
        selection: { chat_profile_id: "openai-sol", home_only: false },
      }]);
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });

});
