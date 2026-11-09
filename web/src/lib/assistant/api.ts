"use client";

import type { APIError as APIErrorBody, MutationReceipt } from "@/lib/contracts/assistant";

// Fixture mode serves contracts/assistant/fixtures through the same client code paths
// (development only). It is a build aid, never integration evidence.
const FIXTURE_KEY = "nori-fixtures";

export function fixtureMode() {
  if (typeof window === "undefined" || process.env.NODE_ENV !== "development") return false;
  const query = new URL(window.location.href).searchParams.get("fixtures");
  try {
    if (query === "1") localStorage.setItem(FIXTURE_KEY, "1");
    if (query === "0") localStorage.removeItem(FIXTURE_KEY);
    return localStorage.getItem(FIXTURE_KEY) === "1";
  } catch { return query === "1"; }
}

export const assistantURL = (path: string) => `${fixtureMode() ? "/api/fixtures/v1" : "/api/v1"}${path}`;

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
  try { if (list.length) sessionStorage.setItem(PENDING_KEY, JSON.stringify(list.slice(-50))); else sessionStorage.removeItem(PENDING_KEY); } catch { /* current tab only */ }
}

export const newRequestID = () => crypto.randomUUID();

/**
 * Sends a management mutation. The request ID is kept until a receipt arrives, so a lost
 * response is resolved by retrying the identical request or reading /v1/mutations/{id};
 * it never becomes a second, different request.
 */
export async function mutate<T = unknown>(path: string, body: { request_id: string } & Record<string, unknown>, method = "POST"): Promise<MutationReceipt & { resource: T }> {
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
      // A definite rejection is final; an ambiguous failure keeps the ID for an identical retry.
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

/**
 * Sends a secret (an access token) once. Unlike mutate(), nothing is kept for retry:
 * the browser never stores credentials. A lost response is resolved by reading the
 * resource, which says only whether a credential is present.
 */
export async function sendSecret<T>(path: string, body: { request_id: string } & Record<string, string>): Promise<MutationReceipt & { resource: T }> {
  const response = await fetch(assistantURL(path), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), cache: "no-store", redirect: "error" });
  return readJSON<MutationReceipt & { resource: T }>(response);
}

export async function receiptFor<T>(requestID: string) {
  return getJSON<MutationReceipt & { resource: T }>(`/mutations/${encodeURIComponent(requestID)}`);
}

/** Retry a mutation with its original request ID and body. */
export function retryMutation<T>(requestID: string) {
  const saved = pendingMutations().find((item) => item.request_id === requestID);
  if (!saved) throw new APIError("That change is no longer pending.", 0);
  return mutate<T>(saved.path, saved.body as { request_id: string }, saved.method);
}

export function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Something went wrong.";
}
