import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { ResourceCacheContext, createResourceCache } from "@/lib/assistant/useResource";
import { installTestDOM, settleTestDOM } from "@/lib/assistant/test-dom";
import { ApiCosts } from "./ApiCosts";

const amount = (cost_usd = 0, priced_calls = 0, unpriced_calls = 0, local_calls = 0, pending_calls = 0) => ({
  cost_usd, priced_calls, unpriced_calls, local_calls, pending_calls,
});
const model = (name: string, ...counts: Parameters<typeof amount>) => ({ model: name, ...amount(...counts) });
const settings = { model_profiles: { profiles: [{ model: "paid-model", label: "Paid model" }] } };

const reports = {
  30: {
    days: 30, timezone: "America/Los_Angeles", start_at: "2026-10-03T07:00:00Z", end_at: "2026-11-02T08:00:00Z", bucket_size: "day",
    totals: amount(1.25, 1, 1),
    models: [model("paid-model", 1.25, 1), model("", 0, 0, 1)],
    buckets: [
      { start_at: "2026-10-31T07:00:00Z", end_at: "2026-11-01T07:00:00Z", totals: amount(1.25, 1), models: [model("paid-model", 1.25, 1)] },
      { start_at: "2026-11-01T07:00:00Z", end_at: "2026-11-02T08:00:00Z", totals: amount(0, 0, 1), models: [model("", 0, 0, 1)] },
    ],
  },
  1: {
    days: 1, timezone: "America/Los_Angeles", start_at: "2026-11-01T07:00:00Z", end_at: "2026-11-01T10:00:00Z", bucket_size: "hour",
    totals: amount(0, 0, 1, 0, 1), models: [model("", 0, 0, 1, 0, 1)],
    buckets: [
      { start_at: "2026-11-01T08:00:00Z", end_at: "2026-11-01T09:00:00Z", totals: amount(0, 0, 1), models: [model("", 0, 0, 1)] },
      { start_at: "2026-11-01T09:00:00Z", end_at: "2026-11-01T10:00:00Z", totals: amount(0, 0, 0, 0, 1), models: [model("", 0, 0, 0, 0, 1)] },
    ],
  },
  7: {
    days: 7, timezone: "America/Los_Angeles", start_at: "2026-10-26T07:00:00Z", end_at: "2026-11-02T08:00:00Z", bucket_size: "day",
    totals: amount(0.34, 1), models: [model("paid-model", 0.34, 1)],
    buckets: [{ start_at: "2026-11-01T07:00:00Z", end_at: "2026-11-02T08:00:00Z", totals: amount(0.34, 1), models: [model("paid-model", 0.34, 1)] }],
  },
};

describe("API costs", () => {
  let restoreDOM: () => void;
  const originalFetch = globalThis.fetch;

  beforeEach(() => { restoreDOM = installTestDOM(); });
  afterEach(() => { globalThis.fetch = originalFetch; restoreDOM(); });

  test("keeps an available model across range changes and distinguishes repeated hours", async () => {
    let resolveToday: ((response: Response) => void) | undefined;
    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/settings/models")) return Response.json(settings);
      if (url.endsWith("days=30")) return Response.json(reports[30]);
      if (url.endsWith("days=7")) return Response.json(reports[7]);
      if (url.endsWith("days=1")) return new Promise<Response>((resolve) => { resolveToday = resolve; });
      throw new Error(`Unexpected request: ${url}`);
    }) as typeof fetch;

    const target = document.createElement("div");
    document.body.append(target);
    const root = createRoot(target);
    const button = (label: string) => [...target.querySelectorAll("button")].find((item) => item.textContent?.trim() === label);
    try {
      await act(async () => root.render(<ResourceCacheContext.Provider value={createResourceCache()}><ApiCosts /></ResourceCacheContext.Provider>));
      await settleTestDOM();
      assert.match(target.textContent ?? "", /\$1\.25/);
      const unknown = [...target.querySelectorAll("button")].find((item) => item.textContent?.includes("Unknown model"));
      assert.ok(unknown);
      await act(async () => unknown.click());
      assert.equal(unknown.getAttribute("aria-pressed"), "true");
      assert.match(target.textContent ?? "", /Cost is unavailable for 1 call/);
      assert.ok(target.querySelector('[aria-label="Clear model filter: Unknown model"]'));

      const today = button("1 day");
      assert.ok(today);
      await act(async () => today.click());
      assert.doesNotMatch(target.textContent ?? "", /\$1\.25/);
      assert.match(target.textContent ?? "", /Loading API costs/);
      assert.ok(resolveToday);
      await act(async () => resolveToday!(Response.json(reports[1])));
      await settleTestDOM();
      assert.ok(target.querySelector('[aria-label="Clear model filter: Unknown model"]'));
      assert.ok(target.querySelector('[aria-label^="1 AM GMT-7,"]'));
      assert.ok(target.querySelector('[aria-label^="1 AM GMT-8,"]'));
      assert.match(target.textContent ?? "", /In progress/);
      assert.match(target.textContent ?? "", /1 call in progress/);

      const seven = button("7 days");
      assert.ok(seven);
      await act(async () => seven.click());
      await settleTestDOM();
      assert.equal(target.querySelector('[aria-label="Clear model filter: Unknown model"]'), null);
      assert.match(target.textContent ?? "", /\$0\.34/);
    } finally {
      await act(async () => root.unmount());
      target.remove();
    }
  });
});
