import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import React from "react";
import { store } from "@/lib/assistant/useResource";
import type { Run, Session } from "@/lib/types";
import { STREAM_RECONNECTING } from "./messages";
import { useRuntime } from "./useRuntime";

type Cleanup = void | (() => void);
type Dispatcher = {
  useState<S>(initial: S | (() => S)): [S, (next: S | ((prev: S) => S)) => void];
  useRef<T>(initial: T): { current: T };
  useCallback<T>(fn: T, deps?: readonly unknown[]): T;
  useMemo<T>(fn: () => T, deps?: readonly unknown[]): T;
  useEffect(create: () => Cleanup, deps?: readonly unknown[]): void;
  useSyncExternalStore<T>(subscribe: (listener: () => void) => () => void, getSnapshot: () => T): T;
};
type ReactClientInternals = { H: Dispatcher | null };

const internals = (React as unknown as {
  __CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE: ReactClientInternals;
}).__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE;

function depsChanged(prev: readonly unknown[] | undefined, next: readonly unknown[] | undefined) {
  if (!prev || !next || prev.length !== next.length) return true;
  for (let i = 0; i < next.length; i++) if (!Object.is(prev[i], next[i])) return true;
  return false;
}

function mountHook<T>(invoke: () => T) {
  const slots: unknown[] = [];
  const effects: { deps?: readonly unknown[]; cleanup?: Cleanup; pending?: () => Cleanup }[] = [];
  let mounted = true;
  let scheduled = false;
  let latest!: T;

  const schedule = () => {
    if (!mounted || scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (mounted) render();
    });
  };

  function render() {
    let idx = 0;
    const dispatcher: Dispatcher = {
      useState<S>(initial: S | (() => S)) {
        const slot = idx++;
        if (!(slot in slots)) {
          slots[slot] = typeof initial === "function" ? (initial as () => S)() : initial;
        }
        const setState = (next: S | ((prev: S) => S)) => {
          const prev = slots[slot] as S;
          const resolved = typeof next === "function" ? (next as (p: S) => S)(prev) : next;
          if (!Object.is(prev, resolved)) {
            slots[slot] = resolved;
            schedule();
          }
        };
        return [slots[slot] as S, setState];
      },
      useRef<R>(initial: R) {
        const slot = idx++;
        if (!(slot in slots)) slots[slot] = { current: initial };
        return slots[slot] as { current: R };
      },
      useCallback<F>(fn: F, deps?: readonly unknown[]) {
        const slot = idx++;
        const prev = slots[slot] as { fn: F; deps?: readonly unknown[] } | undefined;
        if (!prev || depsChanged(prev.deps, deps)) slots[slot] = { fn, deps };
        return (slots[slot] as { fn: F }).fn;
      },
      useMemo<M>(fn: () => M, deps?: readonly unknown[]) {
        const slot = idx++;
        const prev = slots[slot] as { value: M; deps?: readonly unknown[] } | undefined;
        if (!prev || depsChanged(prev.deps, deps)) slots[slot] = { value: fn(), deps };
        return (slots[slot] as { value: M }).value;
      },
      useEffect(create: () => Cleanup, deps?: readonly unknown[]) {
        const slot = idx++;
        const entry = effects[slot] ?? {};
        if (!effects[slot] || depsChanged(entry.deps, deps)) {
          entry.deps = deps;
          entry.pending = create;
          effects[slot] = entry;
        }
      },
      useSyncExternalStore<V>(_subscribe: (listener: () => void) => () => void, getSnapshot: () => V) {
        return getSnapshot();
      },
    };
    const prev = internals.H;
    internals.H = dispatcher;
    try {
      latest = invoke();
    } finally {
      internals.H = prev;
    }
    for (const effect of effects) {
      if (effect?.pending) {
        const create = effect.pending;
        effect.pending = undefined;
        effect.cleanup?.();
        effect.cleanup = create();
      }
    }
  }

  render();
  return {
    get current() { return latest; },
    unmount() {
      mounted = false;
      for (const effect of effects) effect?.cleanup?.();
    },
  };
}

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

  emitClosedError() {
    this.readyState = FakeEventSource.CLOSED;
    this.onerror?.();
  }

  close() {
    this.closed = true;
    this.readyState = FakeEventSource.CLOSED;
  }
}

function makeStorage() {
  const map = new Map<string, string>();
  return {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v); },
    removeItem: (k: string) => { map.delete(k); },
  };
}

async function flush(turns = 10) {
  for (let i = 0; i < turns; i++) await Promise.resolve();
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

  beforeEach(() => {
    FakeEventSource.instances = [];
    scheduledTimers = new Map();
    nextTimerId = 1;
    savedGlobals = {
      window: g.window,
      document: g.document,
      navigator: g.navigator,
      localStorage: g.localStorage,
      sessionStorage: g.sessionStorage,
      EventSource: g.EventSource,
      fetch: g.fetch,
      setTimeout: g.setTimeout,
      clearTimeout: g.clearTimeout,
    };

    const noop = () => undefined;
    g.window = { addEventListener: noop, removeEventListener: noop };
    g.document = { hidden: false, visibilityState: "visible", addEventListener: noop, removeEventListener: noop };
    g.navigator = { onLine: true };
    g.localStorage = makeStorage();
    g.sessionStorage = makeStorage();
    g.EventSource = FakeEventSource;
    g.setTimeout = ((fn: () => void, delay = 0) => {
      const id = nextTimerId++;
      scheduledTimers.set(id, { fn, delay });
      return id;
    }) as unknown as typeof setTimeout;
    g.clearTimeout = ((id: number) => {
      scheduledTimers.delete(id);
    }) as unknown as typeof clearTimeout;

    // Seed assistant resource cache entries so invalidate() calls trigger observable GET fetches.
    store("/assistant/summary", { revision: 1, attention: 1 }, undefined, false);
    store("/work", { items: [] });
    store("/interactions", { items: [] });
    store("/library", { items: [] });
    store("/memory", { items: [] });
    store("/memory/working", { revision: 1, items: [], gist: "", turns: 0, core_bytes: 0, core_budget_bytes: 24576 });
  });

  afterEach(() => {
    for (const [key, value] of Object.entries(savedGlobals)) {
      if (value === undefined) delete g[key];
      else g[key] = value;
    }
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

    g.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.startsWith("/api/v1/snapshot")) return Response.json(baseSession({ runs: [run1] }));
      return Response.json({ ok: true });
    }) as unknown as typeof fetch;

    const hook = mountHook(() => useRuntime());
    try {
      await flush();
      assert.equal(FakeEventSource.instances.length, 1);
      assert.equal(FakeEventSource.instances[0].url, "/api/v1/events?after=10");

      fetched.length = 0;
      FakeEventSource.instances[0].emitEvent("task", { run: completedRun }, "11");
      await flush();

      assert.equal(hook.current.session?.runs[0].status, "completed");
      assert.equal(fetched.includes("/api/v1/assistant/summary"), true);
      assert.equal(fetched.includes("/api/v1/work"), true);
      assert.equal(fetched.includes("/api/v1/interactions"), true);
      assert.equal(fetched.includes("/api/v1/library"), false);

      // Out-of-order stale running event must NOT overwrite completed run.
      FakeEventSource.instances[0].emitEvent("task", { run: run1 }, "12");
      await flush();
      assert.equal(hook.current.session?.runs[0].status, "completed");

      // Result event invalidates /library.
      fetched.length = 0;
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
        "13",
      );
      await flush();
      assert.equal(hook.current.session?.results?.length, 1);
      assert.equal(fetched.includes("/api/v1/library"), true);
      assert.equal(fetched.includes("/api/v1/memory"), false);

      // Memory event invalidates /memory (including sub-resources like /memory/working).
      fetched.length = 0;
      FakeEventSource.instances[0].emitEvent("memory", { revision: 4 }, "14");
      await flush();
      assert.equal(fetched.includes("/api/v1/memory"), true);
      assert.equal(fetched.includes("/api/v1/memory/working"), true);
      assert.equal(fetched.includes("/api/v1/library"), false);
      assert.equal(fetched.includes("/api/v1/work"), false);
    } finally {
      hook.unmount();
    }
  });

  test("SSE error backoff escalates, shows STREAM_RECONNECTING on 3rd failure, and resyncs/invalidates on recovery open", async () => {
    const fetched: string[] = [];
    g.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.startsWith("/api/v1/snapshot")) return Response.json(baseSession({ position: 10 }));
      return Response.json({ ok: true });
    }) as unknown as typeof fetch;

    const hook = mountHook(() => useRuntime());
    try {
      await flush();
      assert.equal(FakeEventSource.instances.length, 1);
      assert.equal(hook.current.error, "");

      const popReconnectTimer = () => {
        const entry = [...scheduledTimers.entries()].find(([, t]) => t.delay <= 30_000);
        assert.ok(entry, "expected reconnect timer to be scheduled");
        scheduledTimers.delete(entry[0]);
        return entry[1];
      };

      // Failure 1 -> 2s backoff, no reconnecting banner yet
      FakeEventSource.instances[0].emitClosedError();
      await flush();
      assert.equal(hook.current.error, "");
      const t1 = popReconnectTimer();
      assert.equal(t1.delay, 2000);
      t1.fn();
      await flush();
      assert.equal(FakeEventSource.instances.length, 2);

      // Failure 2 -> 4s backoff, still no banner
      FakeEventSource.instances[1].emitClosedError();
      await flush();
      assert.equal(hook.current.error, "");
      const t2 = popReconnectTimer();
      assert.equal(t2.delay, 4000);
      t2.fn();
      await flush();
      assert.equal(FakeEventSource.instances.length, 3);

      // Failure 3 -> 8s backoff AND STREAM_RECONNECTING error shown
      FakeEventSource.instances[2].emitClosedError();
      await flush();
      assert.equal(hook.current.error, STREAM_RECONNECTING);
      const t3 = popReconnectTimer();
      assert.equal(t3.delay, 8000);
      t3.fn();
      await flush();
      assert.equal(FakeEventSource.instances.length, 4);

      // Recovery onopen clears error, refreshes snapshot, and invalidates summary/work/interactions/memory
      fetched.length = 0;
      FakeEventSource.instances[3].emitOpen();
      await flush();
      assert.equal(hook.current.error, "");
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

    g.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.startsWith("/api/v1/snapshot")) {
        return Response.json(baseSession({
          position: snapshotPosition,
          conversation: { pending: false, processing: false, input_ids: [], restart_count: 0, frozen: false, revision: snapshotRevision, consumed: 0 },
        }));
      }
      return Response.json({ ok: true });
    }) as unknown as typeof fetch;

    const hook = mountHook(() => useRuntime());
    try {
      await flush();
      assert.equal(FakeEventSource.instances.length, 1);
      assert.equal(FakeEventSource.instances[0].url, "/api/v1/events?after=20");

      // Simulate store rewind to an earlier position & lower conversation revision.
      fetched.length = 0;
      snapshotRevision = 3;
      snapshotPosition = 4;
      FakeEventSource.instances[0].emitEvent("reset", { position: 4 });
      await flush(15);

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

    g.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
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
    }) as unknown as typeof fetch;

    const hook = mountHook(() => useRuntime());
    try {
      await flush();
      fetched.length = 0;
      await hook.current.commandTask("run-cmd", "answer", "Go ahead", "q-1");
      await flush();

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

    g.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
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
    }) as unknown as typeof fetch;

    const hook = mountHook(() => useRuntime());
    try {
      await flush();
      assert.equal(hook.current.session?.messages[0].status, "failed");

      await hook.current.retryTurn({ turn_id: "turn-1", message_id: "turn-1-assistant" });
      await flush();

      const retryCall = calls.find((call) => call.url === "/api/retry");
      assert.ok(retryCall);
      assert.deepEqual(retryCall.body, { turn_id: "turn-1", message_id: "turn-1-assistant" });
      assert.equal(hook.current.session?.messages[0].status, "superseded");
      assert.equal(hook.current.conversationBusy, true);
    } finally {
      hook.unmount();
    }
  });
});
