import type {
  ComputerCheckpoint,
  ComputerSessionSummary,
  ComputerStatus,
  CreateComputerSessionRequest,
  CreateComputerSessionResponse,
} from "@/lib/contracts/assistant";
import { APIError, assistantURL, getJSON } from "./api";

async function sendComputerJSON<T>(path: string, method: "POST" | "PATCH" | "DELETE", body?: unknown): Promise<T> {
  const response = await fetch(assistantURL(path), {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    cache: "no-store",
    redirect: "error",
  });
  if (response.status === 204) {
    return undefined as T;
  }
  const data = await response.json().catch(() => null);
  if (!response.ok) {
    throw new APIError(data?.error || "Could not reach your computer.", response.status, data ?? undefined);
  }
  return data as T;
}

export function getComputerStatus(): Promise<ComputerStatus> {
  return getJSON<ComputerStatus>("/computer");
}

export function wakeComputer(): Promise<ComputerStatus> {
  return sendComputerJSON<ComputerStatus>("/computer/wake", "POST");
}

export function sleepComputer(): Promise<void> {
  return sendComputerJSON<void>("/computer/sleep", "POST");
}

export function resetComputer(): Promise<ComputerStatus> {
  return sendComputerJSON<ComputerStatus>("/computer/reset", "POST");
}

export function listComputerCheckpoints(): Promise<ComputerCheckpoint[]> {
  return getJSON<ComputerCheckpoint[]>("/computer/checkpoints");
}

export function createComputerCheckpoint(label?: string): Promise<ComputerCheckpoint> {
  return sendComputerJSON<ComputerCheckpoint>("/computer/checkpoints", "POST", label ? { label } : {});
}

export function restoreComputerCheckpoint(checkpointId?: string): Promise<ComputerStatus> {
  return sendComputerJSON<ComputerStatus>("/computer/restore", "POST", checkpointId ? { checkpoint_id: checkpointId } : {});
}

export function createComputerSession(
  req: CreateComputerSessionRequest = {},
): Promise<CreateComputerSessionResponse> {
  return sendComputerJSON<CreateComputerSessionResponse>("/computer/sessions", "POST", req);
}

export function updateComputerSessionMode(
  sessionId: string,
  mode: "view" | "control",
): Promise<ComputerSessionSummary> {
  return sendComputerJSON<ComputerSessionSummary>(
    `/computer/sessions/${encodeURIComponent(sessionId)}`,
    "PATCH",
    { mode },
  );
}

export function endComputerSession(sessionId: string): Promise<void> {
  return sendComputerJSON<void>(`/computer/sessions/${encodeURIComponent(sessionId)}`, "DELETE");
}

export function buildDesktopWSURL(session: CreateComputerSessionResponse): string {
  const proto = typeof window !== "undefined" && window.location.protocol === "https:" ? "wss:" : "ws:";
  const host = typeof window !== "undefined" ? window.location.host : "127.0.0.1:3210";
  const path = session.ws_path.startsWith("/api") ? session.ws_path : `/api${session.ws_path}`;
  return `${proto}//${host}${path}?token=${encodeURIComponent(session.token)}`;
}
