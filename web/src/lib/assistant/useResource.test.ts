import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import React from "react";
import { invalidate, store, useResource } from "./useResource";

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
  const stores: { subscribe?: (listener: () => void) => () => void; unsubscribe?: () => void }[] = [];
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
      useSyncExternalStore<V>(subscribe: (listener: () => void) => () => void, getSnapshot: () => V) {
        const slot = idx++;
        const entry = stores[slot] ?? {};
        if (entry.subscribe !== subscribe) {
          entry.unsubscribe?.();
          entry.subscribe = subscribe;
          entry.unsubscribe = subscribe(schedule);
          stores[slot] = entry;
        }
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
    rerender: render,
    unmount() {
      mounted = false;
      for (const storeEntry of stores) storeEntry?.unsubscribe?.();
      for (const effect of effects) effect?.cleanup?.();
    },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

async function flush(turns = 6) {
  for (let i = 0; i < turns; i++) await Promise.resolve();
}

function installBrowserStubs() {
  const g = globalThis as Record<string, unknown>;
  const prevWindow = g.window;
  const prevDocument = g.document;
  const noop = () => undefined;
  g.window = { addEventListener: noop, removeEventListener: noop };
  g.document = { visibilityState: "visible", addEventListener: noop, removeEventListener: noop };
  return () => {
    if (prevWindow === undefined) delete g.window;
    else g.window = prevWindow;
    if (prevDocument === undefined) delete g.document;
    else g.document = prevDocument;
  };
}

describe("useResource", () => {
  let restoreBrowser: () => void;
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    restoreBrowser = installBrowserStubs();
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
    restoreBrowser();
  });

  test("versioned: true drops out-of-order lower revision responses while versioned: false accepts derived projections", async () => {
    const tag = crypto.randomUUID();
    const versionedPath = `/work/test-versioned-${tag}`;
    const summaryPath = `/assistant/summary-unversioned-${tag}`;

    globalThis.fetch = (async () => Response.json({ revision: 100, attention: 1 })) as unknown as typeof fetch;

    const versioned = mountHook(() => useResource<{ revision: number; attention: number }>(versionedPath, { poll: 0 }));
    const unversioned = mountHook(() => useResource<{ revision: number; attention: number }>(summaryPath, { poll: 0, versioned: false }));
    try {
      await flush();
      assert.deepEqual(versioned.current.data, { revision: 100, attention: 1 });
      assert.deepEqual(unversioned.current.data, { revision: 100, attention: 1 });

      // Stale/lower revision is dropped for versioned entities, but accepted for versioned: false summaries.
      store(versionedPath, { revision: 42, attention: 0 }, undefined, true);
      store(summaryPath, { revision: 42, attention: 0 }, undefined, false);
      await flush();

      assert.deepEqual(versioned.current.data, { revision: 100, attention: 1 });
      assert.deepEqual(unversioned.current.data, { revision: 42, attention: 0 });

      // Higher revision updates versioned entity.
      store(versionedPath, { revision: 101, attention: 0 }, undefined, true);
      await flush();
      assert.deepEqual(versioned.current.data, { revision: 101, attention: 0 });

      // For unversioned resources, an older serial never overwrites a newer serial.
      store(summaryPath, { revision: 999, attention: 9 }, 1, false);
      await flush();
      assert.deepEqual(unversioned.current.data, { revision: 42, attention: 0 });
    } finally {
      versioned.unmount();
      unversioned.unmount();
    }
  });

  test("mid-flight invalidate triggers a follow-up refetch so stale summary badge counts clear", async () => {
    const tag = crypto.randomUUID();
    const path = `/assistant/summary-midflight-${tag}`;
    const first = deferred<Response>();
    const second = deferred<Response>();
    const calls: string[] = [];

    globalThis.fetch = ((input: RequestInfo | URL) => {
      calls.push(String(input));
      if (calls.length === 1) return first.promise;
      if (calls.length === 2) return second.promise;
      return Promise.resolve(Response.json({ revision: 99, attention: 0 }));
    }) as unknown as typeof fetch;

    const hook = mountHook(() => useResource<{ revision: number; attention: number }>(path, { poll: 0, versioned: false }));
    try {
      await flush();
      assert.equal(calls.length, 1);
      assert.equal(calls[0], `/api/v1${path}`);
      assert.equal(hook.current.loading, true);

      // A task finishes while the first summary GET is still in flight and invalidates the summary.
      invalidate(`/assistant/summary-midflight-${tag}`);
      await flush();
      // Still coalesced onto the in-flight request — second fetch must not overlap.
      assert.equal(calls.length, 1);

      // First request completes with the stale "1 thing needs you" payload.
      first.resolve(Response.json({ revision: 50, attention: 1 }));
      await flush(10);

      // Because invalidate arrived mid-flight, a follow-up fetch starts automatically.
      assert.equal(calls.length, 2);

      // Second request resolves with lower revision (derived projection) and zero attention.
      second.resolve(Response.json({ revision: 12, attention: 0 }));
      await flush(10);

      assert.deepEqual(hook.current.data, { revision: 12, attention: 0 });
      assert.equal(hook.current.loading, false);
      assert.equal(hook.current.refreshing, false);
    } finally {
      hook.unmount();
    }
  });

  test("invalidate matches prefixes only and preserves last good data when a refresh fails", async () => {
    const tag = crypto.randomUUID();
    const workPath = `/work-prefix-${tag}/1`;
    const libraryPath = `/library-prefix-${tag}/1`;
    const fetched: string[] = [];
    let failWork = false;

    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.includes(`work-prefix-${tag}`) && failWork) {
        return Response.json({ error: "Upstream unavailable" }, { status: 503 });
      }
      return Response.json({ revision: fetched.length, ok: true });
    }) as unknown as typeof fetch;

    const work = mountHook(() => useResource<{ revision: number; ok: boolean }>(workPath, { poll: 0 }));
    const library = mountHook(() => useResource<{ revision: number; ok: boolean }>(libraryPath, { poll: 0 }));
    try {
      await flush();
      assert.equal(fetched.length, 2);
      const initialWork = work.current.data;
      assert.ok(initialWork);

      failWork = true;
      invalidate(`/work-prefix-${tag}`);
      await flush(10);

      assert.equal(fetched.filter((u) => u.includes(`work-prefix-${tag}`)).length, 2);
      assert.equal(fetched.filter((u) => u.includes(`library-prefix-${tag}`)).length, 1);
      assert.deepEqual(work.current.data, initialWork);
      assert.ok(work.current.error);
      assert.equal(work.current.error?.message, "Upstream unavailable");
    } finally {
      work.unmount();
      library.unmount();
    }
  });
});
