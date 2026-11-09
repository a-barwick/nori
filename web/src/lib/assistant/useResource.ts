"use client";

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { APIError, getJSON } from "./api";

type Entry = { data?: unknown; error?: APIError | Error; loading: boolean; serial: number; revision: number };
const cache = new Map<string, Entry>();
const listeners = new Map<string, Set<() => void>>();
let serials = 0;

const revisionOf = (data: unknown) => (data && typeof data === "object" && typeof (data as { revision?: unknown }).revision === "number" ? (data as { revision: number }).revision : -1);

function emit(path: string) { listeners.get(path)?.forEach((listener) => listener()); }
function set(path: string, entry: Entry) { cache.set(path, entry); emit(path); }

/**
 * Store a newer value for a path. A response carrying a lower resource revision than the one
 * already shown is dropped, so a slow GET never overwrites a newer mutation result.
 */
export function store(path: string, data: unknown, serial = ++serials) {
  const current = cache.get(path);
  const revision = revisionOf(data);
  if (current?.data !== undefined && revision >= 0 && revision < current.revision) return;
  if (current && revision < 0 && serial < current.serial) return;
  set(path, { data, loading: false, serial, revision: Math.max(revision, revision >= 0 ? revision : -1) });
}

const inflight = new Map<string, Promise<void>>();
async function load(path: string) {
  if (inflight.has(path)) return inflight.get(path);
  const serial = ++serials;
  const current = cache.get(path);
  set(path, { ...(current ?? { serial: 0, revision: -1 }), loading: true });
  const run = getJSON<unknown>(path).then((data) => store(path, data, serial)).catch((error: APIError | Error) => {
    const latest = cache.get(path);
    set(path, { ...(latest ?? { serial, revision: -1 }), loading: false, error });
  }).finally(() => inflight.delete(path));
  inflight.set(path, run);
  return run;
}

/** Re-fetch every cached path that starts with one of the prefixes (after a mutation or a stream event). */
export function invalidate(...prefixes: string[]) {
  for (const path of cache.keys()) if (prefixes.some((prefix) => path.startsWith(prefix))) void load(path);
}

/**
 * Reads a management resource. Fetches on mount, on focus, when the network returns and on
 * an interval while the tab is visible. Returns the last good value with any newer error.
 */
export function useResource<T>(path: string | null, { poll = 15_000 }: { poll?: number } = {}) {
  const subscribe = useCallback((listener: () => void) => {
    if (!path) return () => {};
    const set = listeners.get(path) ?? new Set();
    set.add(listener);
    listeners.set(path, set);
    return () => { set.delete(listener); };
  }, [path]);
  const entry = useSyncExternalStore(subscribe, () => (path ? cache.get(path) : undefined), () => undefined);
  const reload = useCallback(() => (path ? load(path) : Promise.resolve()), [path]);

  useEffect(() => {
    if (!path) return;
    void load(path);
    const onFocus = () => { if (document.visibilityState === "visible") void load(path); };
    window.addEventListener("focus", onFocus);
    window.addEventListener("online", onFocus);
    document.addEventListener("visibilitychange", onFocus);
    const timer = poll > 0 ? setInterval(() => { if (document.visibilityState === "visible") void load(path); }, poll) : undefined;
    return () => {
      window.removeEventListener("focus", onFocus);
      window.removeEventListener("online", onFocus);
      document.removeEventListener("visibilitychange", onFocus);
      if (timer) clearInterval(timer);
    };
  }, [path, poll]);

  const apply = useCallback((data: T) => { if (path) store(path, data); }, [path]);
  return { data: entry?.data as T | undefined, error: entry?.error, loading: !entry || (entry.loading && entry.data === undefined), refreshing: !!entry?.loading, reload, apply };
}

/** Run an async action and keep its pending/error state. */
export function useAction() {
  const [state, setState] = useState<{ pending?: string; error?: string }>({});
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const run = useCallback(async <T,>(name: string, action: () => Promise<T>): Promise<T | undefined> => {
    setState({ pending: name });
    try {
      const result = await action();
      if (mounted.current) setState({});
      return result;
    } catch (error) {
      if (mounted.current) setState({ error: error instanceof Error ? error.message : "Something went wrong." });
      return undefined;
    }
  }, []);
  const clear = useCallback(() => setState({}), []);
  return { ...state, run, clear };
}
