import assert from "node:assert/strict";
import { afterEach, describe, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import {
  APIError,
  deleteModelKey,
  fetchModelKeys,
  saveModelKey,
  type ModelKeysResponse,
} from "@/lib/assistant/api";
import { asFetch } from "@/lib/test-fetch";
import { ApiKeysContent, needsByokSetup } from "./ApiKeysSettings";

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
});
