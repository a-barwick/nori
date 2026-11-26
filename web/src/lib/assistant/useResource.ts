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
};

export function createResourceCache() {
  const resources = new Map<string, Resource>();
  let serial = 0;

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
    for (const path of resources.keys()) {
      if (prefixes.some((prefix) => path.startsWith(prefix))) void load(path, true);
    }
  }

  function refreshVisible() {
    if (document.visibilityState === "visible") {
      for (const path of resources.keys()) void load(path);
    }
  }

  function schedule(path: string, resource: Resource) {
    clearInterval(resource.timer);
    const poll = Math.min(...[...resource.subscriptions].map((item) => item.poll).filter((ms) => ms > 0));
    if (Number.isFinite(poll)) {
      resource.timer = setInterval(() => {
        if (document.visibilityState === "visible") void load(path);
      }, poll);
    }
  }

  function subscribe(path: string, listener: () => void, poll: number, versioned: boolean) {
    if (resources.size === 0) {
      window.addEventListener("focus", refreshVisible);
      window.addEventListener("online", refreshVisible);
      document.addEventListener("visibilitychange", refreshVisible);
    }
    const existing = resources.get(path);
    const resource: Resource = existing ?? { subscriptions: new Set(), refetch: false, versioned };
    if (existing) {
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
    resources.set(path, resource);
    schedule(path, resource);
    if (!existing) void load(path);
    return () => {
      resource.subscriptions.delete(subscription);
      if (resource.subscriptions.size) schedule(path, resource);
      else {
        clearInterval(resource.timer);
        resource.controller?.abort();
        resources.delete(path);
      }
      if (resources.size === 0) {
        window.removeEventListener("focus", refreshVisible);
        window.removeEventListener("online", refreshVisible);
        document.removeEventListener("visibilitychange", refreshVisible);
      }
    };
  }

  return { subscribe, load, store, invalidate, snapshot: (path: string) => resources.get(path)?.snapshot };
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
