import assert from "node:assert/strict";
import { afterEach, describe, test } from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import {
  googleDriveNudgeDismissedKey,
  isGoogleDriveConnected,
  needsByokSetup,
  needsExaSetup,
} from "@/lib/assistant/modelSetup";
import { AssistantProvider } from "@/lib/assistant/context";
import { installTestDOM, settleTestDOM } from "@/lib/assistant/test-dom";
import { createResourceCache, ResourceCacheContext } from "@/lib/assistant/useResource";
import type { Connection, Preferences } from "@/lib/contracts/assistant";
import { asFetch } from "@/lib/test-fetch";
import { MoreScreen } from "./MoreScreen";
import { PersonalizationEditor } from "./PersonalizationSettings";

describe("Settings key alerts, Personalization, and Google Drive nudge", () => {
  const origFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.fetch = origFetch;
  });

  test("needsExaSetup flags only when BYOK is allowed and neither user nor platform Exa is available", () => {
    assert.equal(needsExaSetup(undefined, undefined), false);
    assert.equal(
      needsExaSetup(
        { allow_byok: true, services: [{ service: "exa", configured: false, source: "none" }] },
        undefined,
      ),
      true,
    );
    assert.equal(
      needsExaSetup(
        { allow_byok: true, services: [{ service: "exa", configured: true, source: "byok" }] },
        undefined,
      ),
      false,
    );
    assert.equal(
      needsExaSetup(
        { allow_byok: true, services: [{ service: "exa", configured: false, source: "platform" }] },
        undefined,
      ),
      false,
    );
    assert.equal(
      needsExaSetup(
        { allow_byok: false, services: [{ service: "exa", configured: false, source: "none" }] },
        undefined,
      ),
      false,
    );
    assert.equal(
      needsExaSetup(
        { services: [{ service: "exa", configured: false, source: "none" }] },
        undefined,
      ),
      false,
    );
    assert.equal(
      needsByokSetup(
        {
          byok_required: true,
          providers: [{ provider: "vercel_ai_gateway", label: "Vercel AI Gateway", configured: false }],
        },
        false,
        { api_version: "1", entitlement: { allow_byok: false } } as never,
      ),
      false,
    );
    assert.equal(
      needsExaSetup(undefined, {
        api_version: "1",
        service_keys: { exa_configured: false, platform_exa_available: false },
      } as never),
      true,
    );
  });

  test("isGoogleDriveConnected checks active Google connection with Drive read/write capabilities", () => {
    assert.equal(isGoogleDriveConnected([]), false);
    const calendarOnly: Connection = {
      id: "conn-1",
      integration_id: "google",
      title: "Google",
      status: "connected",
      revision: 1,
      capabilities: ["calendar.read", "calendar.write"],
      account: { provider_id: "acct-1" },
    } as Connection;
    assert.equal(isGoogleDriveConnected([calendarOnly]), false);

    const withDrive: Connection = {
      ...calendarOnly,
      capabilities: ["calendar.read", "files.read", "files.write"],
    };
    assert.equal(isGoogleDriveConnected([withDrive]), true);
  });

  test("PersonalizationEditor renders response styles, custom instructions, about you, and persists updates", async () => {
    const dom = installTestDOM();
    try {
      const savedBodies: unknown[] = [];
      globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
        if (url === "/api/v1/preferences" && init?.method === "PUT") {
          const parsed = JSON.parse(String(init.body));
          savedBodies.push(parsed.value);
          return Response.json({
            status: "applied",
            resource: {
              ...parsed.value,
              revision: 2,
            },
          });
        }
        return Response.json({ error: "not found" }, { status: 404 });
      });

      const current: Preferences = {
        revision: 1,
        timezone: "America/Los_Angeles",
        working_hours: [],
        default_destination_ids: [],
        account_defaults: {},
        budgets: { task_usd: 5 },
        personalization: {
          name: "Austin",
          reply_style: "Concise & direct",
          custom_instructions: "Lead with the answer.",
          focus: ["Engineering"],
          working_on: "Nori",
        },
      };

      const updates: Preferences[] = [];
      const container = document.createElement("div");
      document.body.appendChild(container);
      const root = createRoot(container);

      await act(async () => {
        root.render(<PersonalizationEditor current={current} onSaved={(next) => updates.push(next)} />);
      });

      assert.match(container.textContent ?? "", /Response style/);
      assert.match(container.textContent ?? "", /Custom instructions/);
      assert.match(container.textContent ?? "", /About you/);
      assert.match(container.textContent ?? "", /Memory/);

      const bulletsBtn = [...container.querySelectorAll("button")].find((btn) =>
        btn.textContent?.includes("Bullets first"),
      );
      assert.ok(bulletsBtn);
      await act(async () => {
        bulletsBtn.click();
      });
      await settleTestDOM();

      assert.equal(savedBodies.length, 1);
      assert.equal(
        (savedBodies[0] as { personalization?: { reply_style?: string } }).personalization?.reply_style,
        "Bullets first",
      );
      assert.equal(updates.length, 1);

      await act(async () => {
        root.unmount();
      });
    } finally {
      dom();
    }
  });

  test("MoreScreen shows separate AI model key and Exa alerts plus dismissible Google Drive nudge", async () => {
    const dom = installTestDOM();
    const origEventSource = globalThis.EventSource;
    class StubEventSource {
      addEventListener() {}
      removeEventListener() {}
      close() {}
    }
    globalThis.EventSource = StubEventSource as unknown as typeof EventSource;
    try {
      const driveKey = googleDriveNudgeDismissedKey("local");
      window.localStorage.removeItem(driveKey);
      globalThis.fetch = asFetch(async (input: RequestInfo | URL) => {
        const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
        if (url.endsWith("/settings/model-keys")) {
          return Response.json({
            byok_required: true,
            providers: [{ provider: "vercel_ai_gateway", label: "Vercel AI Gateway", configured: false }],
          });
        }
        if (url.endsWith("/settings/service-keys")) {
          return Response.json({
            allow_byok: true,
            services: [{ service: "exa", label: "Exa Web Search", configured: false, source: "none" }],
          });
        }
        if (url.endsWith("/settings/models")) {
          return Response.json({
            revision: 1,
            selection: {},
            model_profiles: { profiles: [] },
            recommendations: {},
          });
        }
        if (url.endsWith("/connections")) {
          return Response.json({ items: [] });
        }
        if (url.endsWith("/integrations/google")) {
          return Response.json({ id: "google", availability: "available" });
        }
        if (url.endsWith("/capabilities")) {
          return Response.json({
            api_version: "1",
            assistant_features: { connections: true },
            service_keys: { exa_configured: false, platform_exa_available: false },
          });
        }
        if (url.endsWith("/assistant/summary")) {
          return Response.json({ needs_you: 0, working: 0, apps_attention: 0 });
        }
        if (url.includes("/snapshot")) {
          return Response.json({ messages: [], runs: [], tasks: [], results: [], conversations: [], memory: [] });
        }
        return Response.json({});
      });

      const cache = createResourceCache();
      cache.seed("/settings/model-keys", {
        byok_required: true,
        providers: [{ provider: "vercel_ai_gateway", label: "Vercel AI Gateway", configured: false }],
      }, false);
      cache.seed("/settings/service-keys", {
        allow_byok: true,
        services: [{ service: "exa", label: "Exa Web Search", configured: false, source: "none" }],
      }, false);
      cache.seed("/settings/models", {
        revision: 1,
        selection: {},
        model_profiles: { profiles: [] },
        recommendations: {},
      }, true);
      cache.seed("/connections", { items: [] }, true);
      cache.seed("/integrations/google", { id: "google", availability: "available" }, true);

      const container = document.createElement("div");
      document.body.appendChild(container);
      const root = createRoot(container);

      await act(async () => {
        root.render(
          <ResourceCacheContext.Provider value={cache}>
            <AssistantProvider localMode>
              <MoreScreen />
            </AssistantProvider>
          </ResourceCacheContext.Provider>,
        );
      });
      await settleTestDOM();

      const text = container.textContent ?? "";
      assert.match(text, /Connect an AI model to get started/);
      assert.match(text, /For the best experience, enable web search by adding a web search key in Models/);
      assert.match(text, /Connect Google Drive/);
      assert.match(text, /Personalization/);
      assert.match(text, /2 keys/);

      const dismissBtn = container.querySelector('button[aria-label="Dismiss Google Drive suggestion"]') as HTMLButtonElement | null;
      assert.ok(dismissBtn);
      await act(async () => {
        dismissBtn.click();
      });
      await settleTestDOM();

      assert.equal((container.textContent ?? "").includes("Connect Google Drive"), false);
      assert.equal(window.localStorage.getItem(driveKey), "1");

      await act(async () => {
        root.unmount();
      });
    } finally {
      globalThis.EventSource = origEventSource;
      dom();
    }
  });
});
