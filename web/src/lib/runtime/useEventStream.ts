"use client";

import { useCallback, useEffect, useRef, useState, type Dispatch, type MutableRefObject, type SetStateAction } from "react";
import { getJSON } from "@/lib/assistant/api";
import { browserResetChanged, syncBrowserReset } from "@/lib/assistant/resetState";
import type { Preferences } from "@/lib/contracts/assistant";
import type { Message, PendingTaskCommand, ReadPosition, Run, SavedResult, Session } from "@/lib/types";
import { STREAM_RECONNECTING } from "./messages";
import {
  COMMAND_ACCEPTANCE_UNKNOWN,
  commandApplied,
  mergeById,
  mergeMessages,
  mergeReadPosition,
  mergeRuns,
  NO_RUNTIME_ERROR,
  STREAM_IDLE_MS,
  TURN_ACCEPTANCE_UNKNOWN,
  type Pending,
  type Presence,
  type RuntimeErrorState,
} from "./runtimeHelpers";

type UseEventStreamParams = {
  started: boolean;
  visible: boolean;
  busy: boolean;
  scope: string;
  streamAwake: boolean;
  setStreamAwake: Dispatch<SetStateAction<boolean>>;
  outageRef: MutableRefObject<boolean>;
  position: MutableRefObject<number>;
  appliedRevision: MutableRefObject<number>;
  requestSerial: MutableRefObject<number>;
  minimumSerial: MutableRefObject<number>;
  replacingSnapshot: MutableRefObject<boolean>;
  checkingReset: MutableRefObject<boolean>;
  controllers: MutableRefObject<Set<AbortController>>;
  setSession: Dispatch<SetStateAction<Session | undefined>>;
  setErrorState: Dispatch<SetStateAction<RuntimeErrorState>>;
  setLoading: Dispatch<SetStateAction<boolean>>;
  setLoadingOlder: Dispatch<SetStateAction<boolean>>;
  refresh: (signal?: AbortSignal) => Promise<Session | undefined>;
  updatePending: (update: (current: Pending[]) => Pending[]) => void;
  updateTaskCommands: (update: (current: PendingTaskCommand[]) => PendingTaskCommand[]) => void;
  invalidateUploads: () => void;
  invalidate: (...prefixes: string[]) => void;
  resetReceipts: () => void;
};

export function useEventStream({
  started,
  visible,
  busy,
  scope,
  streamAwake,
  setStreamAwake,
  outageRef,
  position,
  appliedRevision,
  requestSerial,
  minimumSerial,
  replacingSnapshot,
  checkingReset,
  controllers,
  setSession,
  setErrorState,
  setLoading,
  setLoadingOlder,
  refresh,
  updatePending,
  updateTaskCommands,
  invalidateUploads,
  invalidate,
  resetReceipts,
}: UseEventStreamParams) {
  const [streamError, setStreamError] = useState("");
  const [streamReconnecting, setStreamReconnecting] = useState(false);
  const [streamingDelta, setStreamingDelta] = useState<{ turn_id: string; text: string } | null>(null);
  const wasConversationBusy = useRef(false);

  // Idle timer: only schedule when awake and not busy (fixes self-retriggering loop when streamAwake flips false).
  useEffect(() => {
    if (busy) {
      setStreamAwake(true);
      return;
    }
    if (!streamAwake) return;
    const timer = setTimeout(() => setStreamAwake(false), STREAM_IDLE_MS);
    return () => clearTimeout(timer);
  }, [busy, streamAwake, setStreamAwake]);

  useEffect(() => {
    if (!started || !visible || !streamAwake) return;
    const controller = new AbortController();
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setTimeout>;
    let taskRefreshTimer: ReturnType<typeof setTimeout> | undefined;
    let failures = 0;
    let stopped = false;
    const completedTurns = new Set<string>();
    let pendingDelta: { turn_id: string; text: string } | null = null;
    let deltaRafId: number | null = null;

    const cancelPendingDelta = () => {
      pendingDelta = null;
      if (deltaRafId !== null && typeof cancelAnimationFrame === "function") {
        cancelAnimationFrame(deltaRafId);
        deltaRafId = null;
      }
    };

    const clearStreamingDelta = (turnId?: string) => {
      cancelPendingDelta();
      setStreamingDelta((current) => (!current || !turnId || current.turn_id === turnId ? null : current));
    };

    const applyPresence = (presence: Presence) => {
      const { conversation } = presence;
      if (conversation.revision < appliedRevision.current) return;
      appliedRevision.current = conversation.revision;
      setSession((current) => current && { ...current, conversation, paused: presence.paused });
      updatePending((current) => current.filter((item) => !conversation.input_ids.includes(item.id)));
      const now = conversation.pending || conversation.processing;
      if (!now) {
        cancelPendingDelta();
        setStreamingDelta(null);
      }
      if (wasConversationBusy.current && !now) void refresh(controller.signal).catch(() => undefined);
      wasConversationBusy.current = now;
    };

    const reload = () => {
      void refresh(controller.signal).then(
        () => {
          if (!stopped) {
            invalidate("/assistant/summary", "/work", "/interactions", "/library", "/memory");
            open();
          }
        },
        () => {
          if (!stopped) timer = setTimeout(reload, 5000);
        },
      );
    };

    function open() {
      if (replacingSnapshot.current) {
        reload();
        return;
      }
      let streamPosition = position.current;
      const stream = new EventSource(`/api/v1/events?after=${streamPosition}`);
      source = stream;

      const listen = <T,>(kind: string, apply: (value: T) => void) =>
        stream.addEventListener(kind, (event) => {
          if (stream !== source || stopped) return;
          const message = event as MessageEvent<string>;
          let parsed: T;
          try {
            parsed = JSON.parse(message.data) as T;
          } catch {
            return;
          }
          apply(parsed);
          if (kind !== "reset" && message.lastEventId) {
            const nextPos = Number(message.lastEventId);
            if (Number.isFinite(nextPos)) {
              streamPosition = Math.max(streamPosition, nextPos);
              position.current = Math.max(position.current, streamPosition);
            }
          }
          failures = 0;
        });

      stream.onopen = () => {
        if (stopped || stream !== source) return;
        const recovering = outageRef.current || failures > 0;
        failures = 0;
        setStreamError("");
        setStreamReconnecting(false);
        if (recovering) {
          void refresh(controller.signal).then(
            (data) => {
              if (data) setErrorState(NO_RUNTIME_ERROR);
            },
            () => undefined,
          );
          invalidate("/assistant/summary", "/work", "/interactions", "/memory");
        }
      };

      listen<{ message: Message }>("message", ({ message }) => {
        setSession(
          (current) =>
            current && {
              ...current,
              messages: mergeMessages(current.messages, [message]),
            },
        );
        if (message.role === "assistant") {
          if (message.turn_id && message.status !== "superseded") {
            completedTurns.add(message.turn_id);
            if (completedTurns.size > 200) {
              const oldest = completedTurns.values().next().value;
              if (oldest !== undefined) completedTurns.delete(oldest);
            }
          }
          clearStreamingDelta(message.turn_id);
        }
        updatePending((current) => current.filter((item) => message.id !== `${item.id}-user`));
      });

      listen<{ turn_id: string; text: string }>("assistant_delta", (delta) => {
        if (!delta || typeof delta.text !== "string") return;
        if (delta.text.length === 0) {
          clearStreamingDelta(delta.turn_id);
          return;
        }
        if (delta.turn_id && completedTurns.has(delta.turn_id)) return;
        if (typeof requestAnimationFrame !== "function") {
          setStreamingDelta(delta);
          return;
        }
        pendingDelta = delta;
        if (deltaRafId === null) {
          deltaRafId = requestAnimationFrame(() => {
            deltaRafId = null;
            const next = pendingDelta;
            pendingDelta = null;
            if (!stopped && next && (!next.turn_id || !completedTurns.has(next.turn_id))) {
              setStreamingDelta(next);
            }
          });
        }
      });

      listen<{ run: Run }>("task", ({ run }) => {
        setSession((current) => current && { ...current, runs: mergeRuns(current.runs, [run]) });
        updateTaskCommands((current) => current.filter((item) => !commandApplied(item, [run])));
        invalidate("/interactions");
        if (taskRefreshTimer === undefined) {
          taskRefreshTimer = setTimeout(() => {
            taskRefreshTimer = undefined;
            invalidate("/assistant/summary", "/work");
          }, 100);
        }
      });

      listen<{ result: SavedResult }>("result", ({ result }) => {
        setSession(
          (current) =>
            current && {
              ...current,
              results: mergeById(current.results ?? [], [result]).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)),
            },
        );
        invalidate("/library");
      });

      listen<unknown>("memory", () => {
        invalidate("/memory");
      });

      listen<{ read_position: ReadPosition }>("read_position", ({ read_position }) => {
        setSession((current) => current && { ...current, read_position: mergeReadPosition(current.read_position, read_position) });
      });

      listen<Presence>("conversation", applyPresence);
      listen<Presence>("presence", applyPresence);
      listen<unknown>("unknown", () => undefined);

      listen<{ position: number }>("reset", (value) => {
        checkingReset.current = true;
        source = undefined;
        stream.close();
        const checkReset = () => {
          void getJSON<Preferences>("/preferences", controller.signal).then(
            (preferences) => {
              if (stopped) return;
              if (browserResetChanged(scope, preferences.data_reset_at)) {
                for (const request of controllers.current) request.abort();
                syncBrowserReset(scope, preferences.data_reset_at);
                window.dispatchEvent(new Event("nori:data-reset"));
                return;
              }
              checkingReset.current = false;
              const rewind = value.position < streamPosition;
              if (rewind) {
                minimumSerial.current = requestSerial.current + 1;
                replacingSnapshot.current = true;
                wasConversationBusy.current = false;
                for (const request of controllers.current) request.abort();
                updatePending((items) => items.map((item) => ({ ...item, retryManually: true, error: TURN_ACCEPTANCE_UNKNOWN })));
                updateTaskCommands((items) =>
                  items.map((item) =>
                    item.rejected
                      ? item
                      : {
                          ...item,
                          sending: false,
                          accepted: false,
                          position: undefined,
                          retryManually: true,
                          error: COMMAND_ACCEPTANCE_UNKNOWN,
                        },
                  ),
                );
                resetReceipts();
                invalidateUploads();
                setLoadingOlder(false);
                setLoading(true);
              }
              position.current = rewind ? value.position : Math.max(position.current, value.position);
              reload();
            },
            () => {
              if (!stopped) timer = setTimeout(checkReset, 5000);
            },
          );
        };
        checkReset();
      });

      stream.onerror = () => {
        if (stream !== source || stopped) return;
        source = undefined;
        stream.close();
        failures++;
        if (failures >= 3) {
          setStreamError(STREAM_RECONNECTING);
          setStreamReconnecting(true);
        }
        const cap = Math.min(30_000, 1000 * 2 ** failures);
        const delay = Math.floor(Math.random() * cap);
        timer = setTimeout(open, delay);
      };
    }

    open();
    return () => {
      stopped = true;
      cancelPendingDelta();
      controller.abort();
      source?.close();
      clearTimeout(timer);
      clearTimeout(taskRefreshTimer);
    };
  }, [
    started,
    visible,
    streamAwake,
    scope,
    refresh,
    updatePending,
    updateTaskCommands,
    invalidateUploads,
    invalidate,
    appliedRevision,
    checkingReset,
    controllers,
    minimumSerial,
    outageRef,
    position,
    replacingSnapshot,
    requestSerial,
    resetReceipts,
    setErrorState,
    setLoading,
    setLoadingOlder,
    setSession,
  ]);

  const clearStreamError = useCallback(() => {
    setStreamError("");
    setStreamReconnecting(false);
  }, []);

  return { streamError, streamReconnecting, streamingDelta, clearStreamError };
}
