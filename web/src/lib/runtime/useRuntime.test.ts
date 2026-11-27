import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { act, createElement } from "react";
import { createResourceCache, ResourceCacheContext } from "@/lib/assistant/useResource";
import { installTestDOM, mountTestHook, settleTestDOM } from "@/lib/assistant/test-dom";
import { asFetch } from "@/lib/test-fetch";
import type { Run, Session } from "@/lib/types";
import { STREAM_RECONNECTING } from "./messages";
import { STREAM_IDLE_MS } from "./runtimeHelpers";
import { useRuntime } from "./useRuntime";

class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];

  readyState = FakeEventSource.CONNECTING;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  private listeners = new Map<string, Set<(event: MessageEvent<string>) => void>>();

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(kind: string, fn: (event: MessageEvent<string>) => void) {
    const set = this.listeners.get(kind) ?? new Set();
    set.add(fn);
    this.listeners.set(kind, set);
  }

  emitOpen() {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }

  emitEvent(kind: string, data: unknown, lastEventId = "") {
    const event = { data: JSON.stringify(data), lastEventId } as MessageEvent<string>;
    for (const fn of this.listeners.get(kind) ?? []) fn(event);
  }

  emitRaw(kind: string, rawData: string, lastEventId = "") {
    const event = { data: rawData, lastEventId } as MessageEvent<string>;
    for (const fn of this.listeners.get(kind) ?? []) fn(event);
  }

  emitClosedError() {
    this.readyState = FakeEventSource.CLOSED;
    this.onerror?.();
  }

  emitConnectingError() {
    this.readyState = FakeEventSource.CONNECTING;
    this.onerror?.();
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }
}

function baseSession(overrides: Partial<Session> = {}): Session {
  return {
    messages: [],
    runs: [],
    paused: false,
    position: 10,
    conversation: { pending: false, processing: false, input_ids: [], restart_count: 0, frozen: false, revision: 5, consumed: 0 },
    ...overrides,
  };
}

describe("useRuntime SSE reconnect and resource invalidation", () => {
  const g = globalThis as Record<string, unknown>;
  let savedGlobals: Record<string, unknown>;
  let scheduledTimers: Map<number, { fn: () => void; delay: number }>;
  let nextTimerId = 1;
  let restoreDOM: () => void;
  let subscriptions: (() => void)[];
  let cache: ReturnType<typeof createResourceCache>;
  function mountHook<T>(hook: () => T) {
    subscriptions = ["/assistant/summary", "/work", "/interactions", "/library", "/memory", "/memory/working"]
      .map((path) => cache.subscribe(path, () => {}, 0, path !== "/assistant/summary"));
    return mountTestHook(hook, (node) => createElement(ResourceCacheContext.Provider, { value: cache }, node));
  }

  beforeEach(() => {
    FakeEventSource.instances = [];
    scheduledTimers = new Map();
    nextTimerId = 1;
    restoreDOM = installTestDOM();
    subscriptions = [];
    cache = createResourceCache();
    savedGlobals = { EventSource: g.EventSource, fetch: g.fetch, setTimeout: g.setTimeout, clearTimeout: g.clearTimeout };
    g.EventSource = FakeEventSource;
    g.setTimeout = ((fn: () => void, delay = 0) => {
      const id = nextTimerId++;
      scheduledTimers.set(id, { fn, delay });
      return id;
    }) as unknown as typeof setTimeout;
    g.clearTimeout = ((id: number) => {
      scheduledTimers.delete(id);
    }) as unknown as typeof clearTimeout;
  });

  afterEach(() => {
    subscriptions.forEach((unsubscribe) => unsubscribe());
    for (const [key, value] of Object.entries(savedGlobals)) {
      if (value === undefined) delete g[key];
      else g[key] = value;
    }
    restoreDOM();
  });

  test("task and result SSE events update state and invalidate summary, work, interactions, and library", async () => {
    const fetched: string[] = [];
    const run1: Run = {
      id: "run-1",
      status: "running",
      cancel_requested: false,
      position: 1,
      started_at: "2026-06-15T10:00:00Z",
      events: [],
      artifacts: [],
    };
    const completedRun: Run = { ...run1, status: "completed", position: 2 };

    g.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.startsWith("/api/v1/snapshot")) return Response.json(baseSession({ runs: [run1] }));
      return Response.json({ ok: true });
    });

    const hook = mountHook(() => useRuntime());
    try {
      await settleTestDOM();
      assert.equal(FakeEventSource.instances.length, 1);
      assert.equal(FakeEventSource.instances[0].url, "/api/v1/events?after=10");

      fetched.length = 0;
      await act(async () => { FakeEventSource.instances[0].emitEvent("task", { run: completedRun }, "11"); });

      assert.equal(hook.current.session?.runs[0].status, "completed");
      assert.equal(fetched.filter((url) => url === "/api/v1/interactions").length, 1);
      assert.equal(fetched.includes("/api/v1/assistant/summary"), false);
      assert.equal(fetched.includes("/api/v1/work"), false);
      await act(async () => {
        FakeEventSource.instances[0].emitEvent("task", { run: run1 }, "12");
        FakeEventSource.instances[0].emitEvent("task", { run: completedRun }, "13");
      });
      assert.equal(hook.current.session?.runs[0].status, "completed");
      assert.ok(fetched.filter((url) => url === "/api/v1/interactions").length > 1);
      const firstTimers = [...scheduledTimers.entries()].filter(([, timer]) => timer.delay === 100);
      assert.equal(firstTimers.length, 1);
      scheduledTimers.delete(firstTimers[0][0]);
      await act(async () => { firstTimers[0][1].fn(); });
      assert.equal(fetched.filter((url) => url === "/api/v1/assistant/summary").length, 1);
      assert.equal(fetched.filter((url) => url === "/api/v1/work").length, 1);
      assert.equal(fetched.includes("/api/v1/library"), false);

      fetched.length = 0;
      await act(async () => { FakeEventSource.instances[0].emitEvent("task", { run: run1 }, "14"); });
      assert.equal(hook.current.session?.runs[0].status, "completed");
      assert.equal(fetched.filter((url) => url === "/api/v1/interactions").length, 1);
      assert.equal(fetched.includes("/api/v1/assistant/summary"), false);
      assert.equal(fetched.includes("/api/v1/work"), false);
      const laterTimers = [...scheduledTimers.entries()].filter(([, timer]) => timer.delay === 100);
      assert.equal(laterTimers.length, 1);
      scheduledTimers.delete(laterTimers[0][0]);
      await act(async () => { laterTimers[0][1].fn(); });
      assert.equal(fetched.filter((url) => url === "/api/v1/assistant/summary").length, 1);
      assert.equal(fetched.filter((url) => url === "/api/v1/work").length, 1);

      fetched.length = 0;
      await act(async () => {
        FakeEventSource.instances[0].emitEvent(
          "result",
          {
            result: {
              id: "res-1",
              root_id: "res-1",
              version: 1,
              run_id: "run-1",
              kind: "answer",
              title: "Summary",
              created_at: "2026-06-15T10:01:00Z",
            },
          },
          "15",
        );
      });
      assert.equal(hook.current.session?.results?.length, 1);
      assert.equal(fetched.includes("/api/v1/library"), true);
      assert.equal(fetched.includes("/api/v1/memory"), false);

      fetched.length = 0;
      await act(async () => { FakeEventSource.instances[0].emitEvent("memory", { revision: 4 }, "16"); });
      assert.equal(fetched.includes("/api/v1/memory"), true);
      assert.equal(fetched.includes("/api/v1/memory/working"), true);
      assert.equal(fetched.includes("/api/v1/library"), false);
      assert.equal(fetched.includes("/api/v1/work"), false);
    } finally {
      hook.unmount();
    }
  });

  test("malformed SSE JSON frame is ignored without breaking subsequent events", async () => {
    g.fetch = asFetch(async (input: RequestInfo | URL) =>
      String(input).startsWith("/api/v1/snapshot") ? Response.json(baseSession()) : Response.json({ ok: true }),
    );
    const hook = mountHook(() => useRuntime());
    try {
      await settleTestDOM();
      await act(async () => {
        FakeEventSource.instances[0].emitRaw("assistant_delta", "{not-valid-json");
        FakeEventSource.instances[0].emitEvent("assistant_delta", { turn_id: "t-1", text: "Hello" }, "11");
      });
      assert.deepEqual(hook.current.streamingDelta, { turn_id: "t-1", text: "Hello" });
    } finally {
      hook.unmount();
    }
  });

  test("idle stream timer sleeps once and does not re-arm after streamAwake becomes false", async () => {
    g.fetch = asFetch(async (input: RequestInfo | URL) =>
      String(input).startsWith("/api/v1/snapshot") ? Response.json(baseSession()) : Response.json({ ok: true }),
    );
    const hook = mountHook(() => useRuntime());
    try {
      await settleTestDOM();
      const idleTimers = [...scheduledTimers.entries()].filter(([, t]) => t.delay === STREAM_IDLE_MS);
      assert.equal(idleTimers.length, 1);
      scheduledTimers.delete(idleTimers[0][0]);

      await act(async () => {
        idleTimers[0][1].fn();
      });

      assert.equal(FakeEventSource.instances[0].closed, true);
      const afterSleep = [...scheduledTimers.entries()].filter(([, t]) => t.delay === STREAM_IDLE_MS);
      assert.equal(afterSleep.length, 0);
    } finally {
      hook.unmount();
    }
  });

  test("a queued task refresh is cancelled when the stream is closed", async () => {
    g.fetch = asFetch(async (input: RequestInfo | URL) =>
      String(input).startsWith("/api/v1/snapshot") ? Response.json(baseSession()) : Response.json({ ok: true }),
    );
    const hook = mountHook(() => useRuntime());
    await settleTestDOM();
    await act(async () => {
      FakeEventSource.instances[0].emitEvent("task", { run: { id: "run-1", position: 1, status: "running" } }, "11");
    });
    const queued = [...scheduledTimers.entries()].filter(([, timer]) => timer.delay === 100);
    assert.equal(queued.length, 1);
    hook.unmount();
    assert.equal(scheduledTimers.has(queued[0][0]), false);
  });

  test("SSE error backoff escalates, shows STREAM_RECONNECTING on 3rd failure, and resyncs/invalidates on recovery open", async () => {
    const fetched: string[] = [];
    g.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.startsWith("/api/v1/snapshot")) return Response.json(baseSession({ position: 10 }));
      return Response.json({ ok: true });
    });

    const hook = mountHook(() => useRuntime());
    try {
      await settleTestDOM();
      assert.equal(FakeEventSource.instances.length, 1);
      assert.equal(hook.current.error, "");
      assert.equal(hook.current.reconnecting, false);

      const popReconnectTimer = () => {
        const entry = [...scheduledTimers.entries()].find(([, t]) => t.delay <= 30_000);
        assert.ok(entry, "expected reconnect timer to be scheduled");
        scheduledTimers.delete(entry[0]);
        return entry[1];
      };

      await act(async () => { FakeEventSource.instances[0].emitConnectingError(); });
      assert.equal(FakeEventSource.instances[0].closed, true);
      assert.equal(hook.current.error, "");
      const t1 = popReconnectTimer();
      assert.ok(t1.delay >= 0 && t1.delay < 2000, `expected jittered delay in [0, 2000), got ${t1.delay}`);
      await act(async () => { t1.fn(); });
      assert.equal(FakeEventSource.instances.length, 2);

      await act(async () => { FakeEventSource.instances[1].emitClosedError(); });
      assert.equal(hook.current.error, "");
      const t2 = popReconnectTimer();
      assert.ok(t2.delay >= 0 && t2.delay < 4000, `expected jittered delay in [0, 4000), got ${t2.delay}`);
      await act(async () => { t2.fn(); });
      assert.equal(FakeEventSource.instances.length, 3);

      await act(async () => { FakeEventSource.instances[2].emitClosedError(); });
      assert.equal(hook.current.error, STREAM_RECONNECTING);
      assert.equal(hook.current.reconnecting, true);
      const t3 = popReconnectTimer();
      assert.ok(t3.delay >= 0 && t3.delay < 8000, `expected jittered delay in [0, 8000), got ${t3.delay}`);
      await act(async () => { t3.fn(); });
      assert.equal(FakeEventSource.instances.length, 4);

      fetched.length = 0;
      await act(async () => { FakeEventSource.instances[3].emitOpen(); });
      assert.equal(hook.current.error, "");
      assert.equal(hook.current.reconnecting, false);
      assert.equal(fetched.includes("/api/v1/snapshot"), true);
      assert.equal(fetched.includes("/api/v1/assistant/summary"), true);
      assert.equal(fetched.includes("/api/v1/work"), true);
      assert.equal(fetched.includes("/api/v1/interactions"), true);
      assert.equal(fetched.includes("/api/v1/memory"), true);
      assert.equal(fetched.includes("/api/v1/memory/working"), true);
    } finally {
      hook.unmount();
    }
  });

  test("SSE rewind reset replaces snapshot cleanly, invalidates all resource families, and reopens stream", async () => {
    const fetched: string[] = [];
    let snapshotRevision = 20;
    let snapshotPosition = 20;

    g.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.startsWith("/api/v1/snapshot")) {
        return Response.json(baseSession({
          position: snapshotPosition,
          conversation: { pending: false, processing: false, input_ids: [], restart_count: 0, frozen: false, revision: snapshotRevision, consumed: 0 },
        }));
      }
      return Response.json({ ok: true });
    });

    const hook = mountHook(() => useRuntime());
    try {
      await settleTestDOM();
      assert.equal(FakeEventSource.instances.length, 1);
      assert.equal(FakeEventSource.instances[0].url, "/api/v1/events?after=20");

      fetched.length = 0;
      snapshotRevision = 3;
      snapshotPosition = 4;
      await act(async () => { FakeEventSource.instances[0].emitEvent("reset", { position: 4 }); });

      assert.equal(FakeEventSource.instances[0].closed, true);
      assert.equal(FakeEventSource.instances.length, 2);
      assert.equal(FakeEventSource.instances[1].url, "/api/v1/events?after=4");
      assert.equal(hook.current.session?.conversation?.revision, 3);
      assert.equal(fetched.includes("/api/v1/snapshot"), true);
      assert.equal(fetched.includes("/api/v1/assistant/summary"), true);
      assert.equal(fetched.includes("/api/v1/work"), true);
      assert.equal(fetched.includes("/api/v1/interactions"), true);
      assert.equal(fetched.includes("/api/v1/library"), true);
      assert.equal(fetched.includes("/api/v1/memory"), true);
      assert.equal(fetched.includes("/api/v1/memory/working"), true);
    } finally {
      hook.unmount();
    }
  });

  test("commandTask posts to /api/command and invalidates summary, work, and interactions", async () => {
    const fetched: string[] = [];
    const run1: Run = {
      id: "run-cmd",
      status: "running",
      cancel_requested: false,
      position: 1,
      started_at: "2026-06-15T10:00:00Z",
      events: [],
      artifacts: [],
    };

    g.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      fetched.push(url);
      if (url.startsWith("/api/v1/snapshot")) return Response.json(baseSession({ runs: [run1] }));
      if (url === "/api/command") {
        const body = JSON.parse(String(init?.body ?? "{}")) as { id: string; run_id: string; kind: "answer" };
        return Response.json({
          command: { ...body, position: 2, revision: 2 },
          run: { ...run1, position: 2, incorporated: 2 },
        });
      }
      return Response.json({ ok: true });
    });

    const hook = mountHook(() => useRuntime());
    try {
      await settleTestDOM();
      fetched.length = 0;
      await act(async () => { await hook.current.commandTask("run-cmd", "answer", "Go ahead", "q-1"); });

      assert.equal(fetched.includes("/api/command"), true);
      assert.equal(fetched.includes("/api/v1/assistant/summary"), true);
      assert.equal(fetched.includes("/api/v1/work"), true);
      assert.equal(fetched.includes("/api/v1/interactions"), true);
      assert.equal(hook.current.session?.runs[0].incorporated, 2);
    } finally {
      hook.unmount();
    }
  });

  test("retryTurn posts to /api/retry with turn_id/message_id and applies updated session snapshot", async () => {
    const calls: { url: string; body?: Record<string, unknown> }[] = [];
    const failedMessage = {
      id: "turn-1-assistant",
      run_id: "",
      turn_id: "turn-1",
      role: "assistant" as const,
      status: "failed" as const,
      text: "Nori's model service is temporarily busy. Your message is saved — tap Retry to try again.",
      error: {
        code: "provider_overloaded",
        message: "Nori's model service is temporarily busy. Your message is saved — tap Retry to try again.",
        retryable: true,
      },
      created_at: "2026-11-25T07:01:18Z",
    };

    g.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ url, body });
      if (url.startsWith("/api/v1/snapshot")) {
        return Response.json(baseSession({ messages: [failedMessage], position: 10 }));
      }
      if (url === "/api/retry") {
        return Response.json(
          baseSession({
            position: 11,
            messages: [{ ...failedMessage, status: "superseded" }],
            conversation: { pending: true, processing: false, input_ids: [], restart_count: 0, frozen: false, revision: 6, consumed: 0 },
          }),
          { status: 202 },
        );
      }
      return Response.json({ ok: true });
    });

    const hook = mountHook(() => useRuntime());
    try {
      await settleTestDOM();
      assert.equal(hook.current.session?.messages[0].status, "failed");

      await act(async () => { await hook.current.retryTurn({ turn_id: "turn-1", message_id: "turn-1-assistant" }); });

      const retryCall = calls.find((call) => call.url === "/api/retry");
      assert.ok(retryCall);
      assert.deepEqual(retryCall.body, { turn_id: "turn-1", message_id: "turn-1-assistant" });
      assert.equal(hook.current.session?.messages[0].status, "superseded");
      assert.equal(hook.current.conversationBusy, true);
    } finally {
      hook.unmount();
    }
  });

  test("boot reuses freshly seeded /preferences in shared cache without redundant fetch", async () => {
    const fetched: string[] = [];
    cache.seed("/preferences", { timezone: "UTC", working_hours: [], default_destination_ids: [], account_defaults: {}, budgets: { task_usd: 5 }, revision: 1 }, false);
    g.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.startsWith("/api/v1/snapshot")) return Response.json(baseSession());
      return Response.json({ ok: true });
    });

    const hook = mountHook(() => useRuntime());
    try {
      await settleTestDOM();
      assert.equal(fetched.includes("/api/v1/preferences"), false);
      assert.equal(fetched.includes("/api/v1/snapshot"), true);
    } finally {
      hook.unmount();
    }
  });
});
