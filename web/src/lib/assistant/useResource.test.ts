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
});
