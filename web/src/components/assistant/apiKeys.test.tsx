import assert from "node:assert/strict";
import { afterEach, describe, test } from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import {
  APIError,
  deleteModelKey,
  deleteServiceKey,
  fetchModelKeys,
  fetchServiceKeys,
  saveModelKey,
  saveServiceKey,
  type ModelKeysResponse,
  type ServiceKeyStatus,
} from "@/lib/assistant/api";
import { installTestDOM, settleTestDOM } from "@/lib/assistant/test-dom";
import { asFetch } from "@/lib/test-fetch";
import { ApiKeysContent, ApiKeysSettings, needsByokSetup, resolveServiceKeys, ServiceKeysContent } from "./ApiKeysSettings";
import { createResourceCache, ResourceCacheContext } from "@/lib/assistant/useResource";
import type { Capabilities } from "@/lib/contracts/assistant";

describe("BYOK API Keys client and Settings UI", () => {
  const origFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.fetch = origFetch;
  });

  test("fetchModelKeys, saveModelKey, and deleteModelKey match the REST contract and surface server errors", async () => {
    const calls: { url: string; method: string; body?: string }[] = [];
    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      calls.push({
        url,
        method: init?.method ?? "GET",
        body: typeof init?.body === "string" ? init.body : undefined,
      });
      if (url === "/api/v1/settings/model-keys") {
        return Response.json({
          byok_required: false,
          providers: [
            {
              provider: "vercel_ai_gateway",
              label: "Vercel AI Gateway",
              configured: true,
              last4: "a1b2",
              updated_at: "2026-11-25T17:00:00Z",
              validated_at: "2026-11-25T17:00:00Z",
            },
          ],
        });
      }
      if (init?.method === "PUT" && init.body === JSON.stringify({ api_key: "bad-key" })) {
        return Response.json({ error: "API key was rejected by Vercel AI Gateway." }, { status: 422 });
      }
      if (init?.method === "PUT") {
        return Response.json({
          provider: "vercel_ai_gateway",
          label: "Vercel AI Gateway",
          configured: true,
          last4: "9z8y",
          updated_at: "2026-11-25T18:00:00Z",
        });
      }
      if (init?.method === "DELETE") {
        return Response.json({
          provider: "vercel_ai_gateway",
          label: "Vercel AI Gateway",
          configured: false,
        });
      }
      return Response.json({ error: "not found" }, { status: 404 });
    });

    const list = await fetchModelKeys();
    assert.equal(list.byok_required, false);
    assert.equal(list.providers[0]?.last4, "a1b2");

    const saved = await saveModelKey("vercel_ai_gateway", "vck_live_9z8y");
    assert.equal(saved.configured, true);
    assert.equal(saved.last4, "9z8y");

    await assert.rejects(
      () => saveModelKey("vercel_ai_gateway", "bad-key"),
      (error: unknown) =>
        error instanceof APIError &&
        error.status === 422 &&
        error.message === "API key was rejected by Vercel AI Gateway.",
    );

    const removed = await deleteModelKey("vercel_ai_gateway");
    assert.equal(removed.configured, false);

    assert.deepEqual(calls, [
      { url: "/api/v1/settings/model-keys", method: "GET", body: undefined },
      {
        url: "/api/v1/settings/model-keys/vercel_ai_gateway",
        method: "PUT",
        body: JSON.stringify({ api_key: "vck_live_9z8y" }),
      },
      {
        url: "/api/v1/settings/model-keys/vercel_ai_gateway",
        method: "PUT",
        body: JSON.stringify({ api_key: "bad-key" }),
      },
      { url: "/api/v1/settings/model-keys/vercel_ai_gateway", method: "DELETE", body: undefined },
    ]);
  });

  test("needsByokSetup flags only when byok_required is true and no provider is configured", () => {
    assert.equal(needsByokSetup(undefined), false);
    assert.equal(
      needsByokSetup({
        byok_required: false,
        providers: [{ provider: "vercel_ai_gateway", label: "Vercel AI Gateway", configured: false }],
      }),
      false,
    );
    assert.equal(
      needsByokSetup({
        byok_required: true,
        providers: [{ provider: "vercel_ai_gateway", label: "Vercel AI Gateway", configured: false }],
      }),
      true,
    );
    assert.equal(
      needsByokSetup({
        byok_required: true,
        providers: [{ provider: "vercel_ai_gateway", label: "Vercel AI Gateway", configured: true, last4: "a1b2" }],
      }),
      false,
    );
  });

  test("ApiKeysContent renders configured status, privacy note, BYOK required banner, and password input attributes", () => {
    const configuredData: ModelKeysResponse = {
      byok_required: false,
      providers: [
        {
          provider: "vercel_ai_gateway",
          label: "Vercel AI Gateway",
          configured: true,
          last4: "a1b2",
          updated_at: "2026-11-25T17:00:00Z",
        },
      ],
    };

    const configuredHtml = renderToStaticMarkup(
      <ApiKeysContent data={configuredData} onProviderUpdated={() => {}} />,
    );
    assert.match(configuredHtml, /Vercel AI Gateway/);
    assert.match(configuredHtml, /••••a1b2/);
    assert.equal(/Add your Vercel AI Gateway API key below/.test(configuredHtml), false);

    const requiredUnconfiguredData: ModelKeysResponse = {
      byok_required: true,
      providers: [
        {
          provider: "vercel_ai_gateway",
          label: "Vercel AI Gateway",
          configured: false,
        },
      ],
    };

    const requiredHtml = renderToStaticMarkup(
      <ApiKeysContent data={requiredUnconfiguredData} onProviderUpdated={() => {}} />,
    );
    assert.match(requiredHtml, /Add your Vercel AI Gateway API key below/);
    assert.equal(/Not set/.test(requiredHtml), false);

    const editingHtml = renderToStaticMarkup(
      <ApiKeysContent
        data={configuredData}
        onProviderUpdated={() => {}}
        initialEditingProvider="vercel_ai_gateway"
      />,
    );
    assert.match(editingHtml, /encrypted at rest/i);
    assert.match(editingHtml, /type="password"/);
    assert.match(editingHtml, /autoComplete="new-password"/i);
    assert.match(editingHtml, /data-1p-ignore/i);
    assert.match(editingHtml, /data-lpignore="true"/i);
    assert.match(editingHtml, /data-bwignore="true"/i);
    assert.match(editingHtml, /spellCheck="false"/i);
    assert.match(editingHtml, />Save</);
    assert.match(editingHtml, />Cancel</);
    assert.match(editingHtml, />Remove</);
  });

  test("fetchServiceKeys, saveServiceKey, and deleteServiceKey match the BYOK Exa contract", async () => {
    const calls: { url: string; method: string; body?: string }[] = [];
    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      calls.push({
        url,
        method: init?.method ?? "GET",
        body: typeof init?.body === "string" ? init.body : undefined,
      });
      if (url === "/api/v1/settings/service-keys") {
        return Response.json({
          allow_byok: true,
          services: [
            {
              service: "exa",
              configured: true,
              masked_key: "••••e9x1",
              source: "user",
              updated_at: "2026-11-26T10:00:00Z",
              validated_at: "2026-11-26T10:00:00Z",
            },
          ],
        });
      }
      if (url === "/api/v1/settings/service-keys/exa" && init?.method === "PUT") {
        if (init.body === JSON.stringify({ api_key: "bad-exa" })) {
          return Response.json({ error: "Exa API key was rejected." }, { status: 422 });
        }
        return Response.json({
          service: "exa",
          configured: true,
          masked_key: "••••77aa",
          source: "user",
        });
      }
      if (url === "/api/v1/settings/service-keys/exa" && init?.method === "DELETE") {
        return Response.json({ service: "exa", configured: false, source: "platform" });
      }
      return Response.json({ error: "not found" }, { status: 404 });
    });

    const list = await fetchServiceKeys();
    assert.equal(list.allow_byok, true);
    assert.equal(list.services[0]?.masked_key, "••••e9x1");

    const saved = await saveServiceKey("exa", "exa_live_77aa");
    assert.equal(saved.configured, true);
    assert.equal(saved.masked_key, "••••77aa");

    await assert.rejects(
      () => saveServiceKey("exa", "bad-exa"),
      (error: unknown) => error instanceof APIError && error.status === 422 && error.message === "Exa API key was rejected.",
    );

    const removed = await deleteServiceKey("exa");
    assert.equal(removed.configured, false);

    assert.deepEqual(calls, [
      { url: "/api/v1/settings/service-keys", method: "GET", body: undefined },
      { url: "/api/v1/settings/service-keys/exa", method: "PUT", body: JSON.stringify({ api_key: "exa_live_77aa" }) },
      { url: "/api/v1/settings/service-keys/exa", method: "PUT", body: JSON.stringify({ api_key: "bad-exa" }) },
      { url: "/api/v1/settings/service-keys/exa", method: "DELETE", body: undefined },
    ]);
  });

  test("ServiceKeysContent displays masked key when allowed and disables editing calmly on trial tier", () => {
    const services: ServiceKeyStatus[] = [
      { service: "exa", label: "Exa Web Search", configured: true, masked_key: "••••e9x1", source: "user" },
    ];

    const configuredHtml = renderToStaticMarkup(
      <ServiceKeysContent services={services} allowByok={true} onServiceUpdated={() => {}} />,
    );
    assert.match(configuredHtml, /Web search/);
    assert.match(configuredHtml, /Exa Web Search/);
    assert.match(configuredHtml, /••••e9x1/);

    const editingHtml = renderToStaticMarkup(
      <ServiceKeysContent
        services={services}
        allowByok={true}
        initialEditingService="exa"
        onServiceUpdated={() => {}}
      />,
    );
    assert.match(editingHtml, /type="password"/);
    assert.match(editingHtml, /autoComplete="new-password"/i);
    assert.match(editingHtml, /encrypted at rest/i);
    assert.match(editingHtml, />Save</);
    assert.match(editingHtml, />Remove</);

    const trialHtml = renderToStaticMarkup(
      <ServiceKeysContent
        services={[{ service: "exa", configured: false }]}
        allowByok={false}
        initialEditingService="exa"
        onServiceUpdated={() => {}}
      />,
    );
    assert.match(trialHtml, /Bring-your-own API keys are available on paid plans/i);
    assert.match(trialHtml, /Paid plans/);
    assert.equal(/type="password"/.test(trialHtml), false);
  });

  test("resolveServiceKeys maps CapabilitiesServiceKeys object to Exa fallback and accepts byok source", () => {
    const fallback = resolveServiceKeys(undefined, { exa_configured: true, platform_exa_available: true });
    assert.deepEqual(fallback, [
      { service: "exa", label: "Exa Web Search", configured: true, source: "byok" },
    ]);

    const platformFallback = resolveServiceKeys(undefined, { exa_configured: false, platform_exa_available: true });
    assert.deepEqual(platformFallback, [
      { service: "exa", label: "Exa Web Search", configured: false, source: "platform" },
    ]);

    const explicit = resolveServiceKeys(
      { services: [{ service: "exa", label: "Exa Web Search", configured: true, masked_key: "••••9876", source: "byok" }] },
      { exa_configured: false, platform_exa_available: false },
    );
    assert.equal(explicit.length, 1);
    assert.equal(explicit[0]?.source, "byok");
    assert.equal(explicit[0]?.masked_key, "••••9876");
  });

  test("ApiKeysSettings renders Exa fallback from CapabilitiesServiceKeys and deduplicates trial notices", async () => {
    const restoreDOM = installTestDOM();
    try {
      const activeTrialCaps: Capabilities = {
        contract: "nori-chat-2",
        service: "nori",
        conversationModes: ["assistant"],
        features: {},
        entitlement: {
          tier: "trial",
          status: "active",
          allow_byok: false,
          suspended: false,
          credit_remaining_usd: 3,
          credit_grant_usd: 5,
          storage_used_bytes: 0,
          storage_limit_bytes: 1000,
          max_routines: 3,
          max_concurrent_tasks: 2,
          max_desktop_ws: 1,
          max_checkpoints: 1,
        },
        service_keys: { exa_configured: false, platform_exa_available: true },
      };

      const renderWithCaps = async (caps: Capabilities) => {
        globalThis.fetch = asFetch(async (input: RequestInfo | URL) => {
          const url = String(input);
          if (url.endsWith("/settings/model-keys")) {
            return Response.json({
              byok_required: false,
              providers: [{ provider: "openai", label: "OpenAI", configured: false }],
            });
          }
          if (url.endsWith("/capabilities")) {
            return Response.json(caps);
          }
          // Simulate /settings/service-keys failing so Exa row must come from capabilities.service_keys fallback.
          return Response.json({ error: "Service keys unavailable" }, { status: 503 });
        });
        const target = document.createElement("div");
        document.body.append(target);
        const root = createRoot(target);
        try {
          await act(async () => {
            root.render(
              <ResourceCacheContext.Provider value={createResourceCache()}>
                <ApiKeysSettings />
              </ResourceCacheContext.Provider>,
            );
          });
          await settleTestDOM();
          return target.innerHTML;
        } finally {
          await act(async () => root.unmount());
          target.remove();
        }
      };

      const activeTrialHtml = await renderWithCaps(activeTrialCaps);
      assert.match(activeTrialHtml, /Exa Web Search/);
      assert.equal(
        (activeTrialHtml.match(/Bring-your-own API keys are available on paid plans/g) ?? []).length,
        1,
        "active trial should render the BYOK notice once across Providers and Web search",
      );

      const exhaustedTrialHtml = await renderWithCaps({
        ...activeTrialCaps,
        entitlement: { ...activeTrialCaps.entitlement!, credit_remaining_usd: 0 },
      });
      assert.match(exhaustedTrialHtml, /Exa Web Search/);
      assert.equal(
        (exhaustedTrialHtml.match(/Bring-your-own API keys are available on paid plans/g) ?? []).length,
        0,
        "exhausted trial should not stack redundant BYOK notices below EntitlementNotice",
      );
    } finally {
      restoreDOM();
    }
  });
});
