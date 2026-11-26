"use client";

const RESET_MARKER_PREFIX = "nori.data-reset-at:";

export function resetMarkerKey(scope: string) {
  return `${RESET_MARKER_PREFIX}${scope}`;
}

function clearLocalData(scope: string) {
  try {
    for (const key of [
      `nori.onboarding.done:${scope}`,
      `nori-draft-assistant-v1:${scope}`,
      `nori-diagnostics-model-selection-v1:${scope}`,
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
    if (resetAt && sessionStorage.getItem(resetMarkerKey(scope)) !== resetAt) clearSessionData(scope);
    sessionStorage.setItem(resetMarkerKey(scope), resetAt ?? "");
  } catch { }
}

export function browserResetChanged(scope: string, resetAt?: string) {
  try { return (sessionStorage.getItem(resetMarkerKey(scope)) ?? "") !== (resetAt ?? ""); } catch { return !!resetAt; }
}
