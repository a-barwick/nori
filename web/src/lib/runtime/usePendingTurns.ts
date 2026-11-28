"use client";

import { useCallback, useEffect, useRef, useState, type Dispatch, type MutableRefObject, type SetStateAction } from "react";
import * as Sentry from "@sentry/nextjs";
import { APIError, billingAccessBlockFromError, postRuntimeOperation, type BillingAccessBlock } from "@/lib/assistant/api";
import type { PendingTaskCommand, Session, TaskCommandResponse } from "@/lib/types";
import {
  COMMAND_ACCEPTANCE_UNKNOWN,
  COMMAND_STORAGE_KEY,
  mergeRuns,
  NO_RUNTIME_ERROR,
  saveTimings,
  STORAGE_KEY,
  traceHeaders,
  TURN_ACCEPTANCE_UNKNOWN,
  type Pending,
  type ReplyTiming,
  type RuntimeErrorState,
} from "./runtimeHelpers";

type UsePendingTurnsParams = {
  localMode: boolean;
  scope: string;
  session: Session | undefined;
  setSession: Dispatch<SetStateAction<Session | undefined>>;
  setErrorState: Dispatch<SetStateAction<RuntimeErrorState>>;
  controllers: MutableRefObject<Set<AbortController>>;
  requestSerial: MutableRefObject<number>;
  replacingSnapshot: MutableRefObject<boolean>;
  applySnapshot: (data: Session, serial: number) => void;
  refresh: (signal?: AbortSignal) => Promise<Session | undefined>;
  refreshCapabilities?: () => void;
  onBillingAccessBlock?: (block: BillingAccessBlock | null) => void;
  invalidate: (...prefixes: string[]) => void;
};

export function usePendingTurns({
  localMode,
  scope,
  session,
  setSession,
  setErrorState,
  controllers,
  requestSerial,
  replacingSnapshot,
  applySnapshot,
  refresh,
  refreshCapabilities,
  onBillingAccessBlock,
  invalidate,
}: UsePendingTurnsParams) {
  const [pending, setPending] = useState<Pending[]>([]);
  const pendingRef = useRef(pending);
  const [taskCommands, setTaskCommands] = useState<PendingTaskCommand[]>([]);
  const taskCommandsRef = useRef(taskCommands);
  const submissions = useRef(new Set<string>());
  const commandSubmissions = useRef(new Set<string>());
  const replyTimings = useRef(new Map<string, ReplyTiming>());

  const updatePending = useCallback(
    (update: (current: Pending[]) => Pending[]) => {
      const next = update(pendingRef.current);
      pendingRef.current = next;
      setPending(next);
      try {
        if (next.length) {
          sessionStorage.setItem(
            `${STORAGE_KEY}:${scope}`,
            JSON.stringify(
              next.map(({ id, text, started, attachments, model, retryManually }) => ({
                id,
                text,
                started,
                attachments,
                model,
                retryManually,
              })),
            ),
          );
        } else {
          sessionStorage.removeItem(`${STORAGE_KEY}:${scope}`);
        }
      } catch {}
    },
    [scope],
  );

  const updateTaskCommands = useCallback(
    (update: (current: PendingTaskCommand[]) => PendingTaskCommand[]) => {
      const next = update(taskCommandsRef.current);
      taskCommandsRef.current = next;
      setTaskCommands(next);
      try {
        if (next.length) {
          sessionStorage.setItem(
            `${COMMAND_STORAGE_KEY}:${scope}`,
            JSON.stringify(next.map((item) => ({ ...item, sending: false }))),
          );
        } else {
          sessionStorage.removeItem(`${COMMAND_STORAGE_KEY}:${scope}`);
        }
      } catch {}
    },
    [scope],
  );

  const finishTiming = useCallback(
    (id: string, outcome: "reply" | "error" | "timeout" | "unobserved") => {
      const timing = replyTimings.current.get(id);
      if (!timing) return;
      clearTimeout(timing.timer);
      timing.span.setAttribute("nori.outcome", outcome);
      const attributes = { mode: localMode ? "local" : "hosted", outcome };
      Sentry.withActiveSpan(timing.span, () => {
        Sentry.metrics.count("nori.reply.outcomes", 1, { attributes });
        Sentry.metrics.distribution(
          outcome === "reply" ? "nori.submit_to_reply" : "nori.submit_to_terminal",
          Math.max(0, Date.now() - timing.started),
          { unit: "millisecond", attributes },
        );
      });
      timing.span.end();
      replyTimings.current.delete(id);
      saveTimings(scope, replyTimings.current);
    },
    [localMode, scope],
  );

  const beginTiming = useCallback(
    (item: Pending) => {
      if (!Sentry.getClient() || replyTimings.current.has(item.id)) return;
      const span = Sentry.startNewTrace(() =>
        Sentry.startInactiveSpan({
          name: "nori.submit_to_reply",
          op: "nori.conversation",
          parentSpan: null,
          startTime: item.started / 1000,
          attributes: { "nori.mode": localMode ? "local" : "hosted" },
        }),
      );
      const timer = setTimeout(() => finishTiming(item.id, "timeout"), Math.max(0, 15 * 60_000 - (Date.now() - item.started)));
      replyTimings.current.set(item.id, { started: item.started, span, timer });
      saveTimings(scope, replyTimings.current);
    },
    [localMode, finishTiming, scope],
  );

  useEffect(() => {
    if (!session) return;
    for (const [id] of replyTimings.current) {
      const input = session.messages.find((message) => message.id === `${id}-user`);
      if (!input?.turn_id) continue;
      const attempt = session.traces
        ?.filter((trace) => trace.turn_id === input.turn_id)
        .sort((a, b) => b.attempt - a.attempt)[0];
      if (attempt?.status === "error") {
        finishTiming(id, "error");
        continue;
      }
      if (
        attempt?.status !== "completed" ||
        !session.messages.some(
          (message) =>
            message.role === "assistant" &&
            !message.run_id &&
            message.turn_id === input.turn_id &&
            message.status !== "failed" &&
            message.status !== "superseded",
        )
      ) {
        continue;
      }
      finishTiming(id, "reply");
      Sentry.addBreadcrumb({ category: "nori", message: "reply received" });
    }
  }, [finishTiming, session]);

  const submit = useCallback(
    async (item: Pending, fallbackOnUnavailableModel = false) => {
      if (replacingSnapshot.current || submissions.current.has(item.id)) return;
      submissions.current.add(item.id);
      const controller = new AbortController();
      controllers.current.add(controller);
      updatePending((current) =>
        current.map((entry) => (entry.id === item.id ? { ...entry, error: undefined, errorCode: undefined } : entry)),
      );
      const serial = ++requestSerial.current;
      try {
        const payload = { id: item.id, message: item.text, attachments: item.attachments?.map((file) => file.id) };
        const headers = traceHeaders(replyTimings.current.get(item.id)?.span);
        let data: Session;
        try {
          data = await postRuntimeOperation<Session>("turn", item.model ? { ...payload, model: item.model } : payload, {
            signal: controller.signal,
            headers,
          });
        } catch (error) {
          if (controller.signal.aborted || !(error instanceof APIError) || error.code !== "model_selection_unavailable") {
            throw error;
          }
          refreshCapabilities?.();
          if (!item.model || !fallbackOnUnavailableModel) throw error;
          updatePending((current) => current.map((entry) => (entry.id === item.id ? { ...entry, model: undefined } : entry)));
          data = await postRuntimeOperation<Session>("turn", payload, { signal: controller.signal, headers });
        }
        if (!controller.signal.aborted) {
          applySnapshot(data, serial);
          onBillingAccessBlock?.(null);
          setErrorState(NO_RUNTIME_ERROR);
        }
      } catch (err) {
        if (!controller.signal.aborted) {
          const block = billingAccessBlockFromError(err);
          if (block) {
            onBillingAccessBlock?.(block);
            refreshCapabilities?.();
          }
          const rejected = err instanceof APIError && err.status >= 400 && err.status < 500 && err.status !== 429;
          updatePending((current) =>
            current.map((entry) =>
              entry.id === item.id
                ? {
                    ...entry,
                    retryManually: entry.retryManually || rejected,
                    error: err instanceof Error ? err.message : TURN_ACCEPTANCE_UNKNOWN,
                    errorCode: err instanceof APIError ? err.code : undefined,
                  }
                : entry,
            ),
          );
          await refresh(controller.signal).catch(() => undefined);
        }
      } finally {
        controllers.current.delete(controller);
        submissions.current.delete(item.id);
      }
    },
    [applySnapshot, controllers, onBillingAccessBlock, refresh, refreshCapabilities, replacingSnapshot, requestSerial, setErrorState, updatePending],
  );

  const submitTaskCommand = useCallback(
    async (item: PendingTaskCommand) => {
      if (replacingSnapshot.current || commandSubmissions.current.has(item.command.id)) return;
      commandSubmissions.current.add(item.command.id);
      updateTaskCommands((current) =>
        current.map((entry) => (entry.command.id === item.command.id ? { ...entry, sending: true, error: undefined } : entry)),
      );
      const controller = new AbortController();
      controllers.current.add(controller);
      try {
        const data = await postRuntimeOperation<TaskCommandResponse>("command", item.command, { signal: controller.signal });
        if (!data.command || data.command.id !== item.command.id || data.run?.id !== item.command.run_id) {
          throw new Error(COMMAND_ACCEPTANCE_UNKNOWN);
        }
        if (!controller.signal.aborted) {
          setSession((current) => (current ? { ...current, runs: mergeRuns(current.runs, [data.run]) } : current));
          updateTaskCommands((current) => current.filter((entry) => entry.command.id !== item.command.id));
          onBillingAccessBlock?.(null);
          invalidate("/assistant/summary", "/work", "/interactions");
        }
      } catch (err) {
        if (!controller.signal.aborted) {
          const block = billingAccessBlockFromError(err);
          if (block) {
            onBillingAccessBlock?.(block);
            refreshCapabilities?.();
          }
          const receipt = err instanceof APIError ? (err.body as Partial<TaskCommandResponse> | undefined) : undefined;
          if (receipt?.accepted && receipt.run) {
            setSession((current) => (current ? { ...current, runs: mergeRuns(current.runs, [receipt.run!]) } : current));
          }
          updateTaskCommands((current) =>
            current.map((entry) =>
              entry.command.id === item.command.id
                ? {
                    ...entry,
                    sending: false,
                    accepted: receipt?.accepted || entry.accepted,
                    position: receipt?.command?.position ?? entry.position,
                    rejected: err instanceof APIError && err.status >= 400 && err.status < 500,
                    error: receipt?.accepted
                      ? "Accepted. Delivery to the task is still pending."
                      : err instanceof Error
                        ? err.message
                        : COMMAND_ACCEPTANCE_UNKNOWN,
                  }
                : entry,
            ),
          );
        }
      } finally {
        commandSubmissions.current.delete(item.command.id);
        if (!controller.signal.aborted) await refresh(controller.signal).catch(() => undefined);
        controllers.current.delete(controller);
      }
    },
    [controllers, invalidate, onBillingAccessBlock, refresh, refreshCapabilities, replacingSnapshot, setSession, updateTaskCommands],
  );

  const dismissPending = useCallback(
    (id: string) => {
      if (submissions.current.has(id)) return;
      updatePending((current) => current.filter((item) => item.id !== id || !item.error));
      finishTiming(id, "unobserved");
    },
    [finishTiming, updatePending],
  );

  const dismissTaskCommand = useCallback(
    (id: string) => {
      updateTaskCommands((current) => current.filter((item) => item.command.id !== id || !item.rejected));
    },
    [updateTaskCommands],
  );

  return {
    pending,
    pendingRef,
    setPending,
    updatePending,
    taskCommands,
    taskCommandsRef,
    setTaskCommands,
    updateTaskCommands,
    replyTimings,
    beginTiming,
    finishTiming,
    submit,
    submitTaskCommand,
    dismissPending,
    dismissTaskCommand,
  };
}
