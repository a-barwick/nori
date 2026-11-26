"use client";

import type { APIError as APIErrorBody, MutationReceipt } from "@/lib/contracts/assistant";

export const assistantURL = (path: string) => `/api/v1${path}`;

export class APIError extends Error {
  constructor(message: string, readonly status: number, readonly body?: Partial<APIErrorBody> & Record<string, unknown>) { super(message); }
  get code() { return this.body?.code; }
  get accepted() { return !!this.body?.accepted; }
  get stale() { return this.status === 409; }
}

export async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(assistantURL(path), { cache: "no-store", signal, redirect: "error" });
  return readJSON<T>(response);
}

async function readJSON<T>(response: Response): Promise<T> {
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    const message = data?.error || (response.status === 404 ? "This is no longer available." : response.status >= 500 ? "Nori could not be reached." : `Request failed (${response.status}).`);
    throw new APIError(message, response.status, data ?? undefined);
  }
  if (data === null) throw new APIError("Nori returned an unreadable response.", response.status);
  return data as T;
}

const PENDING_KEY = "nori-pending-mutations-v1";
type PendingMutation = { request_id: string; path: string; method: string; body: unknown; started: number };

function pendingMutations(): PendingMutation[] {
  try { return JSON.parse(sessionStorage.getItem(PENDING_KEY) || "[]"); } catch { return []; }
}
function savePending(list: PendingMutation[]) {
  try { if (list.length) sessionStorage.setItem(PENDING_KEY, JSON.stringify(list.slice(-50))); else sessionStorage.removeItem(PENDING_KEY); } catch {  }
}

export const newRequestID = () => crypto.randomUUID();

export async function mutate<T = unknown>(path: string, input: { request_id: string } & Record<string, unknown>, method = "POST"): Promise<MutationReceipt & { resource: T }> {
  const { request_id: _fresh, ...change } = input;
  void _fresh;
  const withoutID = (value: unknown) => JSON.stringify(Object.fromEntries(Object.entries(value as Record<string, unknown>).filter(([key]) => key !== "request_id")));
  const same = pendingMutations().find((item) => item.path === path && item.method === method && withoutID(item.body) === JSON.stringify(change));
  const body = same ? { ...input, request_id: same.request_id } : input;
  savePending([...pendingMutations().filter((item) => item.request_id !== body.request_id), { request_id: body.request_id, path, method, body, started: Date.now() }]);
  try {
    const response = await fetch(assistantURL(path), {
      method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), cache: "no-store", redirect: "error",
    });
    const receipt = await readJSON<MutationReceipt & { resource: T }>(response);
    savePending(pendingMutations().filter((item) => item.request_id !== body.request_id));
    return receipt;
  } catch (error) {
    if (error instanceof APIError) {
      if (error.accepted && error.body?.receipt) {
        savePending(pendingMutations().filter((item) => item.request_id !== body.request_id));
        return error.body.receipt as MutationReceipt & { resource: T };
      }
      if (error.status < 500 && error.status !== 429) savePending(pendingMutations().filter((item) => item.request_id !== body.request_id));
      throw error;
    }
    const saved = await receiptFor<T>(body.request_id).catch(() => undefined);
    if (saved) {
      savePending(pendingMutations().filter((item) => item.request_id !== body.request_id));
      return saved;
    }
    throw new APIError("Nori did not confirm this change. Retrying sends the same request, so it cannot apply twice.", 0, { retryable: true });
  }
}

export async function sendSecret<T>(path: string, body: { request_id: string } & Record<string, unknown>): Promise<MutationReceipt & { resource: T }> {
  const response = await fetch(assistantURL(path), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), cache: "no-store", redirect: "error" });
  return readJSON<MutationReceipt & { resource: T }>(response);
}

async function receiptFor<T>(requestID: string) {
  return getJSON<MutationReceipt & { resource: T }>(`/mutations/${encodeURIComponent(requestID)}`);
}
