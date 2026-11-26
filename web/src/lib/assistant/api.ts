"use client";

import type {
  APIError as APIErrorBody,
  AutomationEditable,
  AutomationPreview,
  HomeConnector,
  HomeConnectors,
  HomeConnectorTestResponse,
  HomePairing,
  ModelKeyProvider,
  ModelKeysResponse,
  ModelProfile,
  ModelSettings,
  MutationReceipt,
} from "@/lib/contracts/assistant";
import type { ModelSelection, Session } from "@/lib/types";
import { SERVICE_UNAVAILABLE } from "@/lib/runtime/messages";
import { SIGNED_OUT_EVENT_KEY } from "@/lib/runtime/session";
import { beginReset, finishReset, requestResetCheck } from "./resetState";

export const assistantURL = (path: string) => `/api/v1${path}`;
export const ACCOUNT_DELETED_REDIRECT = "/signed-out?deleted=1";

export class APIError extends Error {
  constructor(message: string, readonly status: number, readonly body?: Partial<APIErrorBody> & Record<string, unknown>) { super(message); }
  get code() { return this.body?.code; }
  get accepted() { return !!this.body?.accepted; }
  get stale() { return this.status === 409; }
  get unavailable() { return this.status === 503 || this.code === "service_unavailable"; }
}

export function isUnavailableError(error: unknown): boolean {
  if (error instanceof APIError) return error.unavailable;
  return error instanceof Error && error.message === SERVICE_UNAVAILABLE;
}

export async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(assistantURL(path), { cache: "no-store", signal, redirect: "error" });
  return readJSON<T>(response);
}

export async function postJSON<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(assistantURL(path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    cache: "no-store",
    redirect: "error",
    signal,
  });
  return readJSON<T>(response);
}

export async function postRuntimeOperation<T>(
  operation: string,
  payload: object,
  options?: { signal?: AbortSignal; headers?: Record<string, string> },
): Promise<T> {
  const response = await fetch(`/api/${operation}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...options?.headers },
    body: JSON.stringify(payload),
    cache: "no-store",
    redirect: "error",
    signal: options?.signal,
  });
  return readJSON<T>(response);
}

export function fetchSnapshot(before?: string, signal?: AbortSignal): Promise<Session> {
  const query = before ? `?before=${encodeURIComponent(before)}` : "";
  return getJSON<Session>(`/snapshot${query}`, signal);
}

export function previewAutomation(value: AutomationEditable, signal?: AbortSignal): Promise<AutomationPreview> {
  return postJSON<AutomationPreview>("/automations/preview", value, signal);
}

export function startGoogleDrivePicker(connectionId: string, returnTo = "/apps"): Promise<{ authorize_url?: string }> {
  return postJSON<{ authorize_url?: string }>(
    `/connections/${encodeURIComponent(connectionId)}/google-drive/picker`,
    { return_to: returnTo },
  );
}

export async function fetchLibraryText(itemId: string, live: boolean, signal?: AbortSignal): Promise<string | undefined> {
  const path = live ? assistantURL(`/library/${encodeURIComponent(itemId)}/file`) : assistantURL(`/results/${encodeURIComponent(itemId)}`);
  const response = await fetch(path, { cache: "no-store", redirect: "error", signal });
  if (!response.ok) return undefined;
  const type = response.headers.get("content-type") ?? "";
  const body = type.includes("json") ? (await response.json().catch(() => null))?.text : await response.text();
  return typeof body === "string" ? body : undefined;
}

export async function fetchFileBlob(url: string, mediaType: string | undefined, signal?: AbortSignal): Promise<Blob> {
  const response = await fetch(url, { cache: "no-store", redirect: "error", signal });
  if (!response.ok) throw new APIError("This file is no longer available.", response.status);
  return new Blob([await response.arrayBuffer()], { type: mediaType });
}

export async function deleteAccount(confirmation: string, resumePending = false): Promise<void> {
  const response = await fetch(assistantURL("/settings/account/delete"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ confirmation, ...(resumePending ? { resume_pending: true } : {}) }),
    cache: "no-store",
    redirect: "error",
  });
  if (response.status === 204 || response.status === 200) {
    try { window.localStorage.setItem(SIGNED_OUT_EVENT_KEY, String(Date.now())); } catch {}
    return;
  }
  await readJSON(response, false);
}

async function readJSON<T>(response: Response, signalReset = true): Promise<T> {
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    if (signalReset && data?.code === "data_reset_pending") requestResetCheck();
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
export function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Something went wrong.";
}

export type {
  HomeConnector,
  HomeConnectors,
  HomePairing,
  ModelKeyProvider,
  ModelKeysResponse,
  ModelProfile,
  ModelSettings,
};

async function homeConnectorRequest<T>(path: string, method: string, body?: unknown): Promise<T> {
  const response = await fetch(assistantURL(path), {
    method,
    ...(body === undefined ? {} : { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }),
    cache: "no-store",
    redirect: "error",
  });
  if (response.ok && response.status === 204) return undefined as T;
  return readJSON<T>(response);
}

export const lookupHomePairing = (userCode: string) =>
  homeConnectorRequest<HomePairing>("/settings/home-connectors/pairing/lookup", "POST", { user_code: userCode });
export const approveHomePairing = (userCode: string) =>
  homeConnectorRequest<void>("/settings/home-connectors/pairing/approve", "POST", { user_code: userCode });
export const testHomeConnector = (id: string) =>
  homeConnectorRequest<HomeConnectorTestResponse>(`/settings/home-connectors/${encodeURIComponent(id)}/test`, "POST");
export const revokeHomeConnector = (id: string) =>
  homeConnectorRequest<void>(`/settings/home-connectors/${encodeURIComponent(id)}`, "DELETE");

export async function saveModelSettings(selection: ModelSelection, expectedRevision: number): Promise<ModelSettings> {
  const response = await fetch(assistantURL("/settings/models"), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ selection, expected_revision: expectedRevision }),
    cache: "no-store",
    redirect: "error",
  });
  return readJSON<ModelSettings>(response);
}

export function fetchModelKeys(signal?: AbortSignal): Promise<ModelKeysResponse> {
  return getJSON<ModelKeysResponse>("/settings/model-keys", signal);
}

/**
 * Saves or replaces a provider API key. Never persists the secret in browser storage.
 */
export async function saveModelKey(provider: string, apiKey: string): Promise<ModelKeyProvider> {
  const response = await fetch(assistantURL(`/settings/model-keys/${encodeURIComponent(provider)}`), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ api_key: apiKey }),
    cache: "no-store",
    redirect: "error",
  });
  return readJSON<ModelKeyProvider>(response);
}

export async function deleteModelKey(provider: string): Promise<ModelKeyProvider> {
  const response = await fetch(assistantURL(`/settings/model-keys/${encodeURIComponent(provider)}`), {
    method: "DELETE",
    cache: "no-store",
    redirect: "error",
  });
  return readJSON<ModelKeyProvider>(response);
}

export async function resetUserData(resumePending = false): Promise<void> {
  let failure: unknown;
  beginReset();
  try {
    const response = await fetch(assistantURL("/settings/reset"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ confirmation: "RESET", ...(resumePending ? { resume_pending: true } : {}) }),
      cache: "no-store",
      redirect: "error",
    });
    if (!response.ok) await readJSON(response, false);
  } catch (error) {
    failure = error instanceof APIError ? error : new APIError("Nori could not confirm the reset. Try again.", 0);
  } finally {
    finishReset();
  }
  if (failure) {
    const noLongerPending = failure instanceof APIError && failure.code === "data_reset_not_pending";
    requestResetCheck({ resetFailed: !resumePending && !noLongerPending, suppressCompletion: noLongerPending });
    throw failure;
  }
}
