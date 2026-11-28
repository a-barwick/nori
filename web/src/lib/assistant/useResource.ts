"use client";

import { createContext, useCallback, useContext, useRef, useState, useEffect, useSyncExternalStore } from "react";
import { announce } from "./announce";
import { getJSON } from "./api";

type Snapshot = { data?: unknown; error?: Error; loading: boolean; serial: number; revision: number };
type Subscription = { listener: () => void; poll: number };
type Resource = {
  snapshot?: Snapshot;
  subscriptions: Set<Subscription>;
  versioned: boolean;
  controller?: AbortController;
  inflight?: Promise<void>;
  refetch: boolean;
  timer?: ReturnType<typeof setInterval>;
  fetchedAt: number;
  idleSince?: number;
};

export type ResourceCacheOptions = {
  maxEntries?: number;
  ttlMs?: number;
  staleTimeMs?: number;
  now?: () => number;
};

const DEFAULT_MAX_ENTRIES = 100;
const DEFAULT_IDLE_TTL_MS = 5 * 60_000;
const DEFAULT_STALE_TIME_MS = 10_000;

export function createResourceCache({
  maxEntries = DEFAULT_MAX_ENTRIES,
  ttlMs = DEFAULT_IDLE_TTL_MS,
  staleTimeMs = DEFAULT_STALE_TIME_MS,
  now = () => Date.now(),
}: ResourceCacheOptions = {}) {
  const resources = new Map<string, Resource>();
  const invalidationListeners = new Set<(prefixes: string[]) => void>();
  let serial = 0;
  let activeSubscriptions = 0;

  function touch(path: string, resource: Resource) {
    resources.delete(path);
    resources.set(path, resource);
  }

  function isExpiredIdle(resource: Resource) {
    return resource.subscriptions.size === 0 && resource.idleSince !== undefined && now() - resource.idleSince > ttlMs;
  }

  function evictIdle() {
    if (resources.size <= maxEntries) return;
    for (const [key, entry] of resources) {
      if (resources.size <= maxEntries) break;
      if (entry.subscriptions.size === 0) resources.delete(key);
    }
  }

  function attachListeners() {
    window.addEventListener("focus", refreshVisible);
    window.addEventListener("online", refreshVisible);
    document.addEventListener("visibilitychange", refreshVisible);
  }

  function detachListeners() {
    window.removeEventListener("focus", refreshVisible);
    window.removeEventListener("online", refreshVisible);
    document.removeEventListener("visibilitychange", refreshVisible);
  }

  function update(resource: Resource, snapshot: Snapshot) {
    resource.snapshot = snapshot;
    for (const { listener } of resource.subscriptions) listener();
  }

  function store(path: string, data: unknown, request = ++serial, versioned = true) {
    const resource = resources.get(path);
    if (!resource) return;
    const current = resource.snapshot;
    const revision = versioned && data && typeof data === "object" && "revision" in data && typeof data.revision === "number"
      ? data.revision : -1;
    if (current && (revision >= 0 ? revision < current.revision || (revision === current.revision && request < current.serial) : request < current.serial)) return;
    resource.fetchedAt = now();
    update(resource, { data, serial: request, revision, loading: !!resource.inflight });
  }

  function load(path: string, force = false): Promise<void> {
    const resource = resources.get(path);
    if (!resource) return Promise.resolve();
    if (resource.inflight) {
      resource.refetch ||= force;
      return resource.inflight;
    }
    const request = ++serial;
    const controller = new AbortController();
    resource.controller = controller;
    const previous = resource.snapshot ?? { serial: 0, revision: -1, loading: false };
    const run = getJSON<unknown>(path, controller.signal).then((data) => {
      if (!controller.signal.aborted) store(path, data, request, resource.versioned);
    }).catch((error: Error) => {
      const latest = resource.snapshot ?? previous;
      if (!controller.signal.aborted && latest.serial <= request) update(resource, { ...latest, error });
    }).finally(() => {
      if (controller.signal.aborted) return;
      resource.inflight = undefined;
      resource.controller = undefined;
      const refetch = resource.refetch;
      resource.refetch = false;
      if (refetch) void load(path);
      else if (resource.snapshot?.loading) update(resource, { ...resource.snapshot, loading: false });
    });
    resource.inflight = run;
    update(resource, { ...previous, loading: true });
    return run;
  }

  function invalidate(...prefixes: string[]) {
    for (const [path, resource] of resources) {
      if (prefixes.some((prefix) => path.startsWith(prefix))) {
        if (resource.subscriptions.size > 0) void load(path, true);
        else resource.fetchedAt = 0;
      }
    }
    for (const listener of invalidationListeners) {
      listener(prefixes);
    }
  }

  function onInvalidate(listener: (prefixes: string[]) => void) {
    invalidationListeners.add(listener);
    return () => {
      invalidationListeners.delete(listener);
    };
  }

  function refreshVisible() {
    if (document.visibilityState === "visible") {
      for (const [path, resource] of resources) {
        if (resource.subscriptions.size > 0) void load(path);
      }
    }
  }

  function schedule(path: string, resource: Resource) {
    clearInterval(resource.timer);
    resource.timer = undefined;
    const poll = Math.min(...[...resource.subscriptions].map((item) => item.poll).filter((ms) => ms > 0));
    if (Number.isFinite(poll)) {
      resource.timer = setInterval(() => {
        if (document.visibilityState === "visible") void load(path);
      }, poll);
    }
  }

  function subscribe(path: string, listener: () => void, poll: number, versioned: boolean) {
    if (activeSubscriptions === 0) attachListeners();
    activeSubscriptions++;

    let existing = resources.get(path);
    if (existing && isExpiredIdle(existing)) {
      resources.delete(path);
      existing = undefined;
    }

    const wasActive = !!existing && existing.subscriptions.size > 0;
    const resource: Resource = existing ?? { subscriptions: new Set(), refetch: false, versioned, fetchedAt: 0 };
    if (existing) {
      resource.idleSince = undefined;
      resource.versioned = resource.versioned || versioned;
      if (
        resource.versioned &&
        resource.snapshot &&
        resource.snapshot.revision < 0 &&
        resource.snapshot.data &&
        typeof resource.snapshot.data === "object" &&
        "revision" in resource.snapshot.data &&
        typeof resource.snapshot.data.revision === "number"
      ) {
        resource.snapshot = { ...resource.snapshot, revision: resource.snapshot.data.revision };
      }
    }
    const subscription = { listener, poll };
    resource.subscriptions.add(subscription);
    touch(path, resource);
    schedule(path, resource);

    if (!existing) {
      void load(path);
    } else if (!wasActive) {
      const stale = resource.fetchedAt === 0 || now() - resource.fetchedAt > staleTimeMs || !!resource.snapshot?.error;
      if (stale) void load(path);
    }

    return () => {
      if (!resource.subscriptions.delete(subscription)) return;
      activeSubscriptions = Math.max(0, activeSubscriptions - 1);
      if (resource.subscriptions.size) {
        schedule(path, resource);
      } else {
        clearInterval(resource.timer);
        resource.timer = undefined;
        resource.controller?.abort();
        resource.controller = undefined;
        resource.inflight = undefined;
        resource.refetch = false;
        if (resources.get(path) === resource) {
          if (resource.snapshot && resource.snapshot.data !== undefined) {
            if (resource.snapshot.loading) {
              resource.snapshot = { ...resource.snapshot, loading: false };
            }
            resource.idleSince = now();
            touch(path, resource);
            evictIdle();
          } else {
            resources.delete(path);
          }
        }
      }
      if (activeSubscriptions === 0) detachListeners();
    };
  }

  function clear() {
    for (const resource of resources.values()) {
      clearInterval(resource.timer);
      resource.timer = undefined;
      resource.controller?.abort();
      resource.controller = undefined;
      resource.inflight = undefined;
      resource.refetch = false;
      resource.subscriptions.clear();
    }
    resources.clear();
    if (activeSubscriptions > 0) {
      activeSubscriptions = 0;
      detachListeners();
    }
  }

  function seed(path: string, data: unknown, versioned = false) {
    let resource = resources.get(path);
    if (!resource) {
      resource = { subscriptions: new Set(), refetch: false, versioned, fetchedAt: 0, idleSince: now() };
      resources.set(path, resource);
    }
    store(path, data, undefined, versioned);
    if (resource.subscriptions.size === 0) {
      resource.idleSince = now();
      touch(path, resource);
      evictIdle();
    }
  }

  function isFresh(path: string): boolean {
    const resource = resources.get(path);
    if (!resource || isExpiredIdle(resource)) return false;
    if (!resource.snapshot || resource.snapshot.data === undefined || resource.snapshot.error) return false;
    return resource.fetchedAt > 0 && now() - resource.fetchedAt <= staleTimeMs;
  }

  function snapshot(path: string): Snapshot | undefined {
    const resource = resources.get(path);
    if (!resource || isExpiredIdle(resource)) return undefined;
    return resource.snapshot;
  }

  return { subscribe, load, store, seed, isFresh, invalidate, onInvalidate, clear, snapshot };
}

export const ResourceCacheContext = createContext(createResourceCache());

export function useResourceInvalidation() {
  return useContext(ResourceCacheContext).invalidate;
}

export function useResource<T>(path: string | null, { poll = 15_000, versioned = true }: { poll?: number; versioned?: boolean } = {}) {
  const cache = useContext(ResourceCacheContext);
  const subscribe = useCallback((listener: () => void) => path ? cache.subscribe(path, listener, poll, versioned) : () => {}, [cache, path, poll, versioned]);
  const entry = useSyncExternalStore(subscribe, () => path ? cache.snapshot(path) : undefined, () => undefined);
  const reload = useCallback(() => path ? cache.load(path, true) : Promise.resolve(), [cache, path]);
  const apply = useCallback((data: T) => { if (path) cache.store(path, data, undefined, versioned); }, [cache, path, versioned]);
  return { data: entry?.data as T | undefined, error: entry?.error, loading: path === null ? false : !entry || (entry.loading && entry.data === undefined), refreshing: !!entry?.loading, reload, apply };
}

export function useAction() {
  const [state, setState] = useState<{ pending?: string; error?: string }>({});
  const mounted = useRef(true);
  const activeRun = useRef(0);
  const restoreTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      if (restoreTimer.current !== undefined) {
        clearTimeout(restoreTimer.current);
        restoreTimer.current = undefined;
      }
    };
  }, []);
  const run = useCallback(async <T,>(name: string, action: () => Promise<T>, done?: string): Promise<T | undefined> => {
    const runID = ++activeRun.current;
    if (restoreTimer.current !== undefined) {
      clearTimeout(restoreTimer.current);
      restoreTimer.current = undefined;
    }
    const control = typeof document !== "undefined" && document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : undefined;
    const parent = control?.parentElement;
    const index = parent ? [...parent.children].indexOf(control!) : -1;
    const restore = () => {
      if (restoreTimer.current !== undefined) clearTimeout(restoreTimer.current);
      restoreTimer.current = setTimeout(() => {
        restoreTimer.current = undefined;
        if (!mounted.current || activeRun.current !== runID) return;
        if (document.activeElement && document.activeElement !== document.body) return;
        const target = control?.isConnected ? control : parent?.isConnected ? parent.children[index] : undefined;
        if (target instanceof HTMLElement) target.focus({ preventScroll: true });
      }, 50);
    };
    setState({ pending: name });
    try {
      const result = await action();
      if (done) announce(done);
      if (mounted.current && activeRun.current === runID) { setState({}); restore(); }
      return result;
    } catch (error) {
      if (mounted.current && activeRun.current === runID) {
        setState({ error: error instanceof Error ? error.message : "Something went wrong." });
        restore();
      }
      return undefined;
    }
  }, []);
  const clear = useCallback(() => setState({}), []);
  return { ...state, run, clear };
}
