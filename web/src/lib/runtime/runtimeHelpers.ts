import * as Sentry from "@sentry/nextjs";
import type { Span } from "@sentry/nextjs";
import { isUnavailableError } from "@/lib/assistant/api";
import { parseModelSelection } from "@/lib/assistant/modelSelection";
import { messageTooLong } from "@/lib/submission";
import type { Attachment, DraftFile, ModelSelection, PendingTaskCommand, ReadPosition, Run, Session } from "@/lib/types";
import { taskControlKinds } from "@/lib/types";
import type { UploadContext } from "@/lib/useDraftFiles";

export const STREAM_IDLE_MS = 5 * 60_000;
export const MAX_PENDING = 20;
export const STORAGE_KEY = "nori-pending-turns-v1";
export const DRAFT_KEY = "nori-draft-assistant-v1";
export const TIMING_STORAGE_KEY = "nori-reply-timings-v1";
export const COMMAND_STORAGE_KEY = "nori-pending-task-commands-v1";
export const TURN_ACCEPTANCE_UNKNOWN = "Message acceptance could not be confirmed.";
export const COMMAND_ACCEPTANCE_UNKNOWN = "Task command acceptance could not be confirmed.";

export type Presence = { conversation: NonNullable<Session["conversation"]>; paused: boolean };
export type Pending = {
  id: string;
  text: string;
  started: number;
  error?: string;
  errorCode?: string;
  attachments?: Attachment[];
  model?: ModelSelection;
  retryManually?: boolean;
};
export type ReplyTiming = { started: number; span: Span; timer: ReturnType<typeof setTimeout> };
export type RuntimeErrorKind = "unavailable" | "error" | null;
export type RuntimeErrorState = { message: string; kind: RuntimeErrorKind };

export const NO_RUNTIME_ERROR: RuntimeErrorState = { message: "", kind: null };

export function toRuntimeError(err: unknown, fallback: string): RuntimeErrorState {
  const message = err instanceof Error && err.message ? err.message : fallback;
  const kind: RuntimeErrorKind = isUnavailableError(err) ? "unavailable" : "error";
  return { message, kind };
}

export function traceHeaders(span?: Span): Record<string, string> | undefined {
  if (!span) return undefined;
  const context = span.spanContext();
  if (/^0+$/.test(context.traceId) || /^0+$/.test(context.spanId)) return undefined;
  return {
    "sentry-trace": Sentry.spanToTraceHeader(span),
    traceparent: `00-${context.traceId}-${context.spanId}-${context.traceFlags & 1 ? "01" : "00"}`,
  };
}

export function uploadFile(item: DraftFile, { signal, progress }: UploadContext): Promise<Attachment> {
  const query = new URLSearchParams({ id: item.id, filename: item.name });
  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open("POST", `/api/v1/uploads?${query}`);
    request.setRequestHeader("Content-Type", item.file.type || "application/octet-stream");
    request.upload.onprogress = (event) => {
      if (event.lengthComputable) progress(event.loaded / event.total);
    };
    request.onload = () => {
      let data: ({ error?: string } & Partial<Attachment>) | null = null;
      try {
        data = JSON.parse(request.responseText);
      } catch {}
      if (request.status >= 200 && request.status < 300 && data) resolve(data as Attachment);
      else reject(new Error(data?.error || (request.status ? `Upload failed (${request.status}).` : "Upload failed. Try again.")));
    };
    request.onerror = () => reject(new Error("The upload was interrupted. Retry this file."));
    request.onabort = () => reject(new DOMException("Upload cancelled", "AbortError"));
    signal.addEventListener("abort", () => request.abort(), { once: true });
    request.send(item.file);
  });
}

export function saveTimings(scope: string, timings: Map<string, ReplyTiming>) {
  try {
    sessionStorage.setItem(
      `${TIMING_STORAGE_KEY}:${scope}`,
      JSON.stringify([...timings].map(([id, timing]) => ({ id, started: timing.started }))),
    );
  } catch {}
}

export function restoredTimings(scope: string): Pending[] {
  try {
    const values: unknown = JSON.parse(sessionStorage.getItem(`${TIMING_STORAGE_KEY}:${scope}`) || "[]");
    return Array.isArray(values)
      ? values
          .slice(-MAX_PENDING)
          .filter(
            (value) =>
              typeof value?.id === "string" &&
              /^[a-f0-9-]{36}$/.test(value.id) &&
              Number.isFinite(value.started) &&
              value.started <= Date.now(),
          )
          .map((value) => ({ id: value.id, started: value.started, text: "" }))
      : [];
  } catch {
    return [];
  }
}

export function restoreTaskCommands(scope: string): PendingTaskCommand[] {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(`${COMMAND_STORAGE_KEY}:${scope}`) || "[]");
    if (!Array.isArray(value)) return [];
    return value
      .filter(
        (item): item is PendingTaskCommand =>
          item &&
          typeof item.started === "number" &&
          typeof item.command?.id === "string" &&
          /^[a-f0-9-]{36}$/.test(item.command.id) &&
          typeof item.command.run_id === "string" &&
          taskControlKinds.includes(item.command.kind) &&
          (item.command.text === undefined || (typeof item.command.text === "string" && !messageTooLong(item.command.text))) &&
          (item.command.question_id === undefined || typeof item.command.question_id === "string"),
      )
      .map((item) => ({ ...item, sending: false }));
  } catch {
    return [];
  }
}

export function commandApplied(item: PendingTaskCommand, runs: Run[]) {
  const run = runs.find((candidate) => candidate.id === item.command.run_id);
  return (
    !!run &&
    ((item.position !== undefined &&
      ((run.incorporated ?? 0) >= item.position || (run.status !== "running" && (run.position ?? 0) >= item.position))) ||
      (item.command.kind === "stop" && run.status !== "running"))
  );
}

export function restorePending(scope: string): Pending[] {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(`${STORAGE_KEY}:${scope}`) || "[]");
    if (!Array.isArray(value)) return [];
    return value
      .filter(
        (item): item is Pending =>
          item &&
          typeof item.id === "string" &&
          /^[a-f0-9-]{36}$/.test(item.id) &&
          typeof item.text === "string" &&
          !messageTooLong(item.text) &&
          typeof item.started === "number" &&
          (item.attachments === undefined || Array.isArray(item.attachments)),
      )
      .slice(0, MAX_PENDING)
      .map((item) => ({
        ...item,
        model: item.model ? parseModelSelection(item.model) : undefined,
        ...(item.retryManually ? { error: TURN_ACCEPTANCE_UNKNOWN } : {}),
      }));
  } catch {
    return [];
  }
}

export const acknowledged = (data: Session, id: string) =>
  data.conversation?.input_ids.includes(id) || data.messages.some((message) => message.id === `${id}-user`);

export function mergeById<T extends { id: string }>(older: T[], newer: T[]): T[] {
  return [...new Map([...older, ...newer].map((item) => [item.id, item])).values()];
}

export function mergeMessages<T extends { id: string; created_at: string }>(older: T[], newer: T[]): T[] {
  if (newer.length === 0) return older;
  if (older.length === 0) {
    return newer.length === 1 ? [newer[0]] : [...newer].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));
  }
  if (newer.length === 1) {
    const incoming = newer[0];
    const last = older[older.length - 1];
    if (last.id === incoming.id && last.created_at === incoming.created_at) {
      return [...older.slice(0, -1), incoming];
    }
    const incomingAt = Date.parse(incoming.created_at);
    const lastAt = Date.parse(last.created_at);
    if (incomingAt >= lastAt && !older.some((item) => item.id === incoming.id)) {
      return [...older, incoming];
    }
  }
  return mergeById(older, newer).sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));
}

export function mergeReadPosition(older: ReadPosition | undefined, newer: ReadPosition | undefined) {
  return !older || (newer && newer.position > older.position) ? newer : older;
}

export function mergeRuns(older: Run[], newer: Run[]): Run[] {
  const runs = new Map(older.map((run) => [run.id, run]));
  for (const run of newer) {
    const previous = runs.get(run.id);
    if (
      previous &&
      ((previous.position ?? 0) > (run.position ?? 0) ||
        (previous.event_sequence ?? 0) > (run.event_sequence ?? 0) ||
        (previous.incorporated ?? 0) > (run.incorporated ?? 0) ||
        (previous.status !== "running" && run.status === "running") ||
        (previous.cancel_requested && !run.cancel_requested))
    ) {
      continue;
    }
    runs.set(run.id, run);
  }
  return [...runs.values()].sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));
}
