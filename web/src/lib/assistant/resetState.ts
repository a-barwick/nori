"use client";

const RESET_MARKER_PREFIX = "nori.data-reset-at:";
const RESET_COMPLETION_PREFIX = "nori.data-reset-confirmation:";
export const RESET_CHECK_EVENT = "nori:check-data-reset";
export const RESET_CHECK_KEY = "nori.check-data-reset";
export const RESET_STARTED_EVENT = "nori:data-reset-started";
let resetsInProgress = 0;

export type ResetCheckOptions = { resetFailed?: boolean; suppressCompletion?: boolean };

export function requestResetCheck(options: ResetCheckOptions = {}) {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent(RESET_CHECK_EVENT, { detail: options }));
  try { localStorage.setItem(RESET_CHECK_KEY, `${Date.now()}-${Math.random()}`); } catch { }
}

export function beginReset() {
  resetsInProgress++;
  if (typeof window !== "undefined") window.dispatchEvent(new Event(RESET_STARTED_EVENT));
}

export function finishReset() {
  resetsInProgress = Math.max(0, resetsInProgress - 1);
}

export function resetInProgress() {
  return resetsInProgress > 0;
}

export function resetMarkerKey(scope: string) {
  return `${RESET_MARKER_PREFIX}${scope}`;
}

function clearLocalData(scope: string) {
  try {
    for (const key of [
      `nori.onboarding.done:${scope}`,
      `nori-draft-assistant-v1:${scope}`,
      "nori-diagnostics-debug",
      "nori.appearance",
    ]) localStorage.removeItem(key);
  } catch { }
}

function clearSessionData(scope: string) {
  try {
    for (const key of [
      `nori-pending-turns-v1:${scope}`,
      `nori-pending-task-commands-v1:${scope}`,
      `nori-reply-timings-v1:${scope}`,
      "nori-pending-mutations-v1",
    ]) sessionStorage.removeItem(key);
  } catch { }
}

export function markBrowserReset(scope: string) {
  clearLocalData(scope);
  clearSessionData(scope);
  const marker = `pending:${Date.now()}`;
  try { sessionStorage.setItem(resetMarkerKey(scope), marker); } catch { }
  try { localStorage.setItem(resetMarkerKey(scope), marker); } catch { }
}

export function syncBrowserReset(scope: string, resetAt?: string) {
  try {
    if (resetAt && localStorage.getItem(resetMarkerKey(scope)) !== resetAt) clearLocalData(scope);
    if (resetAt) localStorage.setItem(resetMarkerKey(scope), resetAt);
  } catch { }
  try {
    if (resetAt && sessionStorage.getItem(resetMarkerKey(scope)) !== resetAt) {
      clearSessionData(scope);
      sessionStorage.setItem(`${RESET_COMPLETION_PREFIX}${scope}`, resetAt);
    }
    sessionStorage.setItem(resetMarkerKey(scope), resetAt ?? "");
  } catch { }
}

export function resetCompletionPending(scope: string, resetAt?: string) {
  if (!resetAt) return false;
  try { return sessionStorage.getItem(`${RESET_COMPLETION_PREFIX}${scope}`) === resetAt; } catch { return false; }
}

export function acknowledgeResetCompletion(scope: string) {
  try { sessionStorage.removeItem(`${RESET_COMPLETION_PREFIX}${scope}`); } catch { }
}

export function browserResetChanged(scope: string, resetAt?: string) {
  try { return (sessionStorage.getItem(resetMarkerKey(scope)) ?? "") !== (resetAt ?? ""); } catch { return !!resetAt; }
}
