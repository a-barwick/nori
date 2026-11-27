import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { act, createElement } from "react";
import { asFetch } from "@/lib/test-fetch";
import { createResourceCache, ResourceCacheContext, useAction, useResource } from "./useResource";
import { installTestDOM, mountTestHook, settleTestDOM } from "./test-dom";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe("useResource", () => {
  let restoreBrowser: () => void;
  let cache: ReturnType<typeof createResourceCache>;
  const mountHook = <T,>(hook: () => T) => mountTestHook(hook, (node) => createElement(ResourceCacheContext.Provider, { value: cache }, node));
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    restoreBrowser = installTestDOM();
    cache = createResourceCache();
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
    restoreBrowser();
  });

  test("useResource(null) returns loading: false without fetching", async () => {
    let fetches = 0;
    globalThis.fetch = asFetch(async () => {
      fetches++;
      return Response.json({ ok: true });
    });

    const hook = mountHook(() => useResource<{ ok: boolean }>(null, { poll: 0 }));
    try {
      await settleTestDOM();
      assert.equal(hook.current.loading, false);
      assert.equal(hook.current.refreshing, false);
      assert.equal(hook.current.data, undefined);
      assert.equal(hook.current.error, undefined);
      assert.equal(fetches, 0);
    } finally {
      hook.unmount();
    }
  });

  test("versioned: true drops out-of-order lower revision responses while versioned: false accepts derived projections", async () => {
    const tag = crypto.randomUUID();
    const versionedPath = `/work/test-versioned-${tag}`;
    const summaryPath = `/assistant/summary-unversioned-${tag}`;

    globalThis.fetch = asFetch(async () => Response.json({ revision: 100, attention: 1 }));

    const versioned = mountHook(() => useResource<{ revision: number; attention: number }>(versionedPath, { poll: 0 }));
    const unversioned = mountHook(() => useResource<{ revision: number; attention: number }>(summaryPath, { poll: 0, versioned: false }));
    try {
      await settleTestDOM();
      assert.deepEqual(versioned.current.data, { revision: 100, attention: 1 });
      assert.deepEqual(unversioned.current.data, { revision: 100, attention: 1 });

      await act(async () => {
        cache.store(versionedPath, { revision: 42, attention: 0 }, undefined, true);
        cache.store(summaryPath, { revision: 42, attention: 0 }, undefined, false);
      });

      assert.deepEqual(versioned.current.data, { revision: 100, attention: 1 });
      assert.deepEqual(unversioned.current.data, { revision: 42, attention: 0 });

      await act(async () => {
        cache.store(versionedPath, { revision: 101, attention: 0 }, undefined, true);
      });
      assert.deepEqual(versioned.current.data, { revision: 101, attention: 0 });

      await act(async () => {
        cache.store(summaryPath, { revision: 999, attention: 9 }, 1, false);
      });
      assert.deepEqual(unversioned.current.data, { revision: 42, attention: 0 });
    } finally {
      versioned.unmount();
      unversioned.unmount();
    }
  });

  test("subscribing with versioned: true upgrades an existing unversioned resource and protects its revision", async () => {
    const path = `/work/upgrade-${crypto.randomUUID()}`;
    let call = 0;
    globalThis.fetch = asFetch(async () => {
      call++;
      if (call === 1) return Response.json({ revision: 100, value: "initial" });
      if (call === 2) return Response.json({ revision: 42, value: "stale" });
      return Response.json({ revision: 101, value: "newer" });
    });

    const first = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0, versioned: false }));
    let second: ReturnType<typeof mountHook<{ data?: { revision: number; value: string } }>> | undefined;
    try {
      await settleTestDOM();
      assert.deepEqual(first.current.data, { revision: 100, value: "initial" });

      second = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0, versioned: true }));
      await settleTestDOM();

      await act(async () => {
        await cache.load(path, true);
      });
      assert.deepEqual(first.current.data, { revision: 100, value: "initial" });
      assert.deepEqual(second.current.data, { revision: 100, value: "initial" });

      await act(async () => {
        await cache.load(path, true);
      });
      assert.deepEqual(first.current.data, { revision: 101, value: "newer" });
      assert.deepEqual(second.current.data, { revision: 101, value: "newer" });
    } finally {
      second?.unmount();
      first.unmount();
    }
  });

  test("useAction cancels its focus-restore timer on unmount and ignores stale concurrent completions", async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const scheduled = new Map<number, () => void>();
    let nextId = 1;
    const origSetTimeout = globalThis.setTimeout;
    const origClearTimeout = globalThis.clearTimeout;
    const g = globalThis as Record<string, unknown>;
    g.setTimeout = (fn: () => void) => {
      const id = nextId++;
      scheduled.set(id, fn);
      return id;
    };
    g.clearTimeout = (id: number) => {
      scheduled.delete(id);
    };

    const hook = mountHook(() => useAction());
    try {
      let p1!: Promise<string | undefined>;
      let p2!: Promise<string | undefined>;
      await act(async () => {
        p1 = hook.current.run("first", () => first.promise);
        p2 = hook.current.run("second", () => second.promise);
      });
      assert.equal(hook.current.pending, "second");

      await act(async () => {
        first.reject(new Error("stale failure"));
        await p1;
      });
      assert.equal(hook.current.pending, "second");
      assert.equal(hook.current.error, undefined);

      await act(async () => {
        second.resolve("ok");
        await p2;
      });
      assert.equal(hook.current.pending, undefined);
      assert.equal(hook.current.error, undefined);
      assert.equal(scheduled.size, 1);

      hook.unmount();
      assert.equal(scheduled.size, 0);
    } finally {
      g.setTimeout = origSetTimeout;
      g.clearTimeout = origClearTimeout;
    }
  });

  test("mid-flight invalidate triggers a follow-up refetch so stale summary badge counts clear", async () => {
    const tag = crypto.randomUUID();
    const path = `/assistant/summary-midflight-${tag}`;
    const first = deferred<Response>();
    const second = deferred<Response>();
    const calls: string[] = [];

    globalThis.fetch = asFetch((input: RequestInfo | URL) => {
      calls.push(String(input));
      if (calls.length === 1) return first.promise;
      if (calls.length === 2) return second.promise;
      return Promise.resolve(Response.json({ revision: 99, attention: 0 }));
    });

    const hook = mountHook(() => useResource<{ revision: number; attention: number }>(path, { poll: 0, versioned: false }));
    try {
      await settleTestDOM();
      assert.equal(calls.length, 1);
      assert.equal(calls[0], `/api/v1${path}`);
      assert.equal(hook.current.loading, true);

      await act(async () => {
        cache.invalidate(`/assistant/summary-midflight-${tag}`);
      });
      assert.equal(calls.length, 1);

      await act(async () => {
        first.resolve(Response.json({ revision: 50, attention: 1 }));
      });

      assert.equal(calls.length, 2);

      await act(async () => {
        second.resolve(Response.json({ revision: 12, attention: 0 }));
      });

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

    globalThis.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      if (url.includes(`work-prefix-${tag}`) && failWork) {
        return Response.json({ error: "Upstream unavailable" }, { status: 503 });
      }
      return Response.json({ revision: fetched.length, ok: true });
    });

    const work = mountHook(() => useResource<{ revision: number; ok: boolean }>(workPath, { poll: 0 }));
    const library = mountHook(() => useResource<{ revision: number; ok: boolean }>(libraryPath, { poll: 0 }));
    try {
      await settleTestDOM();
      assert.equal(fetched.length, 2);
      const initialWork = work.current.data;
      assert.ok(initialWork);

      failWork = true;
      await act(async () => {
        cache.invalidate(`/work-prefix-${tag}`);
      });

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

  test("a stale refresh clears loading and a stale error preserves the mutation", async () => {
    const responses = [deferred<Response>(), deferred<Response>()];
    const path = "/work/stale";
    let requests = 0;
    globalThis.fetch = asFetch(() => responses[requests++]?.promise ?? Promise.resolve(Response.json({ revision: 100 })));
    const hook = mountHook(() => useResource<{ revision: number }>(path, { poll: 0 }));
    try {
      await act(async () => {
        cache.store(path, { revision: 100 });
        responses[0].resolve(Response.json({ revision: 99 }));
      });
      assert.equal(hook.current.refreshing, false);
      assert.deepEqual(hook.current.data, { revision: 100 });

      await act(async () => {
        cache.invalidate(path);
        cache.store(path, { revision: 101 });
        responses[1].reject(new Error("stale failure"));
      });
      assert.equal(hook.current.refreshing, false);
      assert.deepEqual(hook.current.data, { revision: 101 });
      assert.equal(hook.current.error, undefined);
    } finally {
      hook.unmount();
    }
    cache.invalidate(path);
    assert.equal(requests, 2);
  });

  test("unmount then remount within stale window serves cached data immediately with 0 extra fetches", async () => {
    let clock = 1_000;
    cache = createResourceCache({ now: () => clock, staleTimeMs: 10_000, ttlMs: 300_000 });
    const path = `/work/cache-hit-${crypto.randomUUID()}`;
    let fetches = 0;
    globalThis.fetch = asFetch(async () => {
      fetches++;
      return Response.json({ revision: fetches, value: `v${fetches}` });
    });

    const first = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0 }));
    await settleTestDOM();
    assert.equal(fetches, 1);
    assert.deepEqual(first.current.data, { revision: 1, value: "v1" });
    assert.equal(first.current.loading, false);
    first.unmount();

    clock += 5_000;
    const second = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0 }));
    try {
      assert.equal(second.current.loading, false);
      assert.equal(second.current.refreshing, false);
      assert.deepEqual(second.current.data, { revision: 1, value: "v1" });
      await settleTestDOM();
      assert.equal(fetches, 1);
    } finally {
      second.unmount();
    }
  });

  test("unmount then remount after staleTimeMs serves cached data immediately and revalidates in background; expires after ttlMs", async () => {
    let clock = 1_000;
    cache = createResourceCache({ now: () => clock, staleTimeMs: 10_000, ttlMs: 60_000 });
    const path = `/work/cache-swr-${crypto.randomUUID()}`;
    const secondResp = deferred<Response>();
    let fetches = 0;
    globalThis.fetch = asFetch(() => {
      fetches++;
      if (fetches === 1) return Promise.resolve(Response.json({ revision: 1, value: "v1" }));
      if (fetches === 2) return secondResp.promise;
      return Promise.resolve(Response.json({ revision: 3, value: "v3" }));
    });

    const first = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0 }));
    await settleTestDOM();
    assert.equal(fetches, 1);
    first.unmount();

    clock += 15_000;
    const second = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0 }));
    try {
      assert.equal(second.current.loading, false);
      assert.deepEqual(second.current.data, { revision: 1, value: "v1" });
      assert.equal(fetches, 2);
      assert.equal(second.current.refreshing, true);

      await act(async () => {
        secondResp.resolve(Response.json({ revision: 2, value: "v2" }));
      });
      assert.deepEqual(second.current.data, { revision: 2, value: "v2" });
      assert.equal(second.current.refreshing, false);
    } finally {
      second.unmount();
    }

    clock += 61_000;
    const third = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0 }));
    try {
      await settleTestDOM();
      assert.equal(fetches, 3);
      assert.deepEqual(third.current.data, { revision: 3, value: "v3" });
    } finally {
      third.unmount();
    }
  });

  test("refreshVisible skips unmounted cached entries and only refetches active subscribers", async () => {
    let clock = 1_000;
    cache = createResourceCache({ now: () => clock, staleTimeMs: 10_000 });
    const activePath = `/work/active-${crypto.randomUUID()}`;
    const idlePath = `/work/idle-${crypto.randomUUID()}`;
    const fetched: string[] = [];
    globalThis.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      fetched.push(url);
      return Response.json({ revision: fetched.length });
    });

    const activeHook = mountHook(() => useResource<{ revision: number }>(activePath, { poll: 0 }));
    const idleHook = mountHook(() => useResource<{ revision: number }>(idlePath, { poll: 0 }));
    try {
      await settleTestDOM();
      assert.equal(fetched.length, 2);

      idleHook.unmount();
      clock += 2_000;

      await act(async () => {
        window.dispatchEvent(new window.Event("focus"));
      });
      await settleTestDOM();

      assert.equal(fetched.filter((u) => u.endsWith(activePath)).length, 2);
      assert.equal(fetched.filter((u) => u.endsWith(idlePath)).length, 1);
    } finally {
      activeHook.unmount();
    }
  });

  test("invalidate marks unmounted cached entries stale without immediate fetch and revalidates lazily on remount", async () => {
    let clock = 1_000;
    cache = createResourceCache({ now: () => clock, staleTimeMs: 60_000 });
    const path = `/work/lazy-inval-${crypto.randomUUID()}`;
    let fetches = 0;
    globalThis.fetch = asFetch(async () => {
      fetches++;
      return Response.json({ revision: fetches, value: `v${fetches}` });
    });

    const first = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0 }));
    await settleTestDOM();
    assert.equal(fetches, 1);
    first.unmount();

    cache.invalidate("/work/lazy-inval");
    await settleTestDOM();
    assert.equal(fetches, 1);

    clock += 1_000;
    const second = mountHook(() => useResource<{ revision: number; value: string }>(path, { poll: 0 }));
    try {
      assert.equal(second.current.loading, false);
      assert.deepEqual(second.current.data, { revision: 1, value: "v1" });
      await settleTestDOM();
      assert.equal(fetches, 2);
      assert.deepEqual(second.current.data, { revision: 2, value: "v2" });
    } finally {
      second.unmount();
    }
  });

  test("LRU cap evicts oldest idle entries while retaining recently used ones", async () => {
    const clock = 1_000;
    cache = createResourceCache({ now: () => clock, maxEntries: 2, staleTimeMs: 60_000 });
    const fetches: string[] = [];
    globalThis.fetch = asFetch(async (input: RequestInfo | URL) => {
      const url = String(input);
      fetches.push(url);
      return Response.json({ revision: fetches.length, url });
    });

    const p1 = `/work/lru-1-${crypto.randomUUID()}`;
    const p2 = `/work/lru-2-${crypto.randomUUID()}`;
    const p3 = `/work/lru-3-${crypto.randomUUID()}`;

    const h1 = mountHook(() => useResource<{ url: string }>(p1, { poll: 0 }));
    await settleTestDOM();
    h1.unmount();

    const h2 = mountHook(() => useResource<{ url: string }>(p2, { poll: 0 }));
    await settleTestDOM();
    h2.unmount();

    // Touch p1 so p2 becomes least-recently-used.
    const h1Again = mountHook(() => useResource<{ url: string }>(p1, { poll: 0 }));
    await settleTestDOM();
    h1Again.unmount();
    assert.equal(fetches.filter((u) => u.endsWith(p1)).length, 1);

    // Adding p3 should evict p2 (LRU) and keep p1 + p3.
    const h3 = mountHook(() => useResource<{ url: string }>(p3, { poll: 0 }));
    await settleTestDOM();
    h3.unmount();

    assert.ok(cache.snapshot(p1));
    assert.equal(cache.snapshot(p2), undefined);
    assert.ok(cache.snapshot(p3));
  });

  test("clear() removes cached entries and isolates separate cache scopes", async () => {
    const path = `/work/scope-${crypto.randomUUID()}`;
    let fetches = 0;
    globalThis.fetch = asFetch(async () => {
      fetches++;
      return Response.json({ revision: fetches, account: fetches === 1 ? "acct-a" : "acct-b" });
    });

    const first = mountHook(() => useResource<{ account: string }>(path, { poll: 0 }));
    await settleTestDOM();
    assert.deepEqual(first.current.data, { revision: 1, account: "acct-a" });
    first.unmount();
    assert.ok(cache.snapshot(path));

    cache.clear();
    assert.equal(cache.snapshot(path), undefined);

    // Fresh cache (as created when AssistantProvider scope changes) must not see prior scope data.
    cache = createResourceCache();
    assert.equal(cache.snapshot(path), undefined);
    const second = mountHook(() => useResource<{ account: string }>(path, { poll: 0 }));
    try {
      await settleTestDOM();
      assert.equal(fetches, 2);
      assert.deepEqual(second.current.data, { revision: 2, account: "acct-b" });
    } finally {
      second.unmount();
    }
  });

  test("seed() populates cache before first subscriber and prevents redundant fetch within stale window", async () => {
    let clock = 1_000;
    cache = createResourceCache({ now: () => clock, staleTimeMs: 10_000 });
    const path = "/preferences";
    let fetches = 0;
    globalThis.fetch = asFetch(async () => {
      fetches++;
      return Response.json({ timezone: "UTC", revision: 2 });
    });

    cache.seed(path, { timezone: "America/Los_Angeles", revision: 1 }, false);
    assert.equal(cache.isFresh(path), true);

    const hook = mountHook(() => useResource<{ timezone: string; revision: number }>(path, { poll: 0, versioned: false }));
    try {
      assert.equal(hook.current.loading, false);
      assert.deepEqual(hook.current.data, { timezone: "America/Los_Angeles", revision: 1 });
      await settleTestDOM();
      assert.equal(fetches, 0);
    } finally {
      hook.unmount();
    }

    clock += 15_000;
    assert.equal(cache.isFresh(path), false);
  });
});
