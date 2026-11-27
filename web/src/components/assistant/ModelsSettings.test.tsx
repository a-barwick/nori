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

  beforeEach(() => {
    restoreDOM = installTestDOM();
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
    restoreDOM();
  });

  test("opens searchable model picker with top reasoning control and filters lanes cleanly", async () => {
    const settings: ModelSettings = {
      model_profiles: {
        profiles: [
          { id: "home-chat", version: "1", label: "Chat model on Mac", backend: "home", model: "chat", lanes: ["chat"], paid: false },
          {
            id: "home-work",
            version: "1",
            label: "Work model on Mac",
            backend: "home",
            model: "work",
            lanes: ["chat", "task"],
            supported_reasoning: ["low", "high"],
            default_reasoning: "low",
            paid: false,
          },
        ],
      },
      selection: { chat_profile_id: "home-work", chat_reasoning: "low" },
      revision: 1,
      recommendations: {},
    };
    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/settings/home-connectors")) return Response.json({ enabled: true, devices: [] });
      if (url.endsWith("/settings/model-keys")) return Response.json({ byok_required: true, providers: [] });
      return Response.json(settings);
    }) as typeof fetch;

    const target = document.createElement("div");
    document.body.append(target);
    const root = createRoot(target);
    try {
      await act(async () =>
        root.render(
          <ResourceCacheContext.Provider value={createResourceCache()}>
            <ModelsSettings />
          </ResourceCacheContext.Provider>,
        ),
      );
      await settleTestDOM();
      const chatRow = [...target.querySelectorAll("button")].find((b) => b.textContent?.includes("Chat"));
      assert.ok(chatRow);
      await act(async () => chatRow.click());
      await settleTestDOM();

      // Reasoning control appears above the search input and model list
      const html = target.innerHTML;
      const reasoningIdx = html.indexOf("Reasoning");
      const searchIdx = html.indexOf("Search models");
      const listIdx = html.indexOf("Chat model on Mac");
      assert.ok(reasoningIdx > -1 && searchIdx > reasoningIdx && listIdx > searchIdx);
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });

  test("flags an unavailable model and auto-saves on picking an available replacement", async () => {
    let settings: ModelSettings = {
      model_profiles: {
        profiles: [
          {
            id: "openai-sol",
            version: "1",
            label: "OpenAI Sol",
            backend: "openai",
            model: "gpt-6.2-sol",
            lanes: ["chat", "task"],
            supported_reasoning: ["low", "high"],
            default_reasoning: "low",
            paid: true,
          },
        ],
      },
      selection: {
        chat_profile_id: "removed-profile",
        chat_reasoning: "low",
        task_profile_id: "openai-sol",
        task_reasoning: "high",
      },
      revision: 7,
      recommendations: {},
    };
    const writes: unknown[] = [];
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/settings/home-connectors")) return Response.json({ enabled: false, devices: [] });
      if (url.endsWith("/settings/model-keys")) {
        return Response.json({
          byok_required: true,
          providers: [{ provider: "openai", label: "OpenAI", configured: true }],
        });
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
      await act(async () =>
        root.render(
          <ResourceCacheContext.Provider value={createResourceCache()}>
            <ModelsSettings />
          </ResourceCacheContext.Provider>,
        ),
      );
      await settleTestDOM();
      assert.match(target.textContent ?? "", /Unavailable/);
      const chatRow = [...target.querySelectorAll("button")].find((b) => b.textContent?.includes("Chat"));
      assert.ok(chatRow);
      await act(async () => chatRow.click());
      await settleTestDOM();

      const solBtn = [...target.querySelectorAll("button")].find((b) => b.textContent?.includes("OpenAI Sol"));
      assert.ok(solBtn);
      await act(async () => solBtn.click());
      await settleTestDOM();

      assert.deepEqual(writes, [
        {
          expected_revision: 7,
          selection: {
            chat_profile_id: "openai-sol",
            chat_reasoning: "low",
            task_profile_id: "openai-sol",
            task_reasoning: "high",
          },
        },
      ]);
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });

  test("allows disabling home-only policy when the connector feature is unavailable", async () => {
    let settings: ModelSettings = {
      model_profiles: {
        profiles: [
          {
            id: "openai-sol",
            version: "1",
            label: "OpenAI Sol",
            backend: "openai",
            model: "gpt-6.2-sol",
            lanes: ["chat", "task"],
            paid: true,
          },
        ],
      },
      selection: { chat_profile_id: "home-gone", home_only: true },
      revision: 9,
      recommendations: {},
    };
    const writes: unknown[] = [];
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/settings/home-connectors")) return Response.json({ error: "Not found" }, { status: 404 });
      if (url.endsWith("/settings/model-keys")) {
        return Response.json({
          byok_required: true,
          providers: [{ provider: "openai", label: "OpenAI", configured: true }],
        });
      }
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
      await act(async () =>
        root.render(
          <ResourceCacheContext.Provider value={createResourceCache()}>
            <ModelsSettings />
          </ResourceCacheContext.Provider>,
        ),
      );
      await settleTestDOM();
      const toggle = target.querySelector<HTMLButtonElement>('button[role="switch"]');
      assert.equal(toggle?.getAttribute("aria-checked"), "true");
      assert.equal(toggle?.disabled, false);
      await act(async () => toggle?.click());
      await settleTestDOM();

      assert.deepEqual(writes, [
        {
          expected_revision: 9,
          selection: { chat_profile_id: "home-gone", home_only: false },
        },
      ]);
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });
});
