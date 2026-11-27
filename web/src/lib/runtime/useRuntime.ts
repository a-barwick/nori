"use client";

import { useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import * as Sentry from "@sentry/nextjs";
import { fetchSnapshot, getJSON, postRuntimeOperation } from "@/lib/assistant/api";
import { browserResetChanged, syncBrowserReset } from "@/lib/assistant/resetState";
import { ResourceCacheContext, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { Preferences } from "@/lib/contracts/assistant";
import { hasSubmission, messageTooLong } from "@/lib/submission";
import type { DraftFile, PendingTaskCommand, Session, TaskCommand } from "@/lib/types";
import { useDraftFiles, type UploadContext } from "@/lib/useDraftFiles";
import {
  acknowledged,
  commandApplied,
  DRAFT_KEY,
  MAX_PENDING,
  mergeById,
  mergeMessages,
  mergeReadPosition,
  mergeRuns,
  NO_RUNTIME_ERROR,
  restoredTimings,
  restorePending,
  restoreTaskCommands,
  toRuntimeError,
  uploadFile,
  type Pending,
  type RuntimeErrorState,
} from "./runtimeHelpers";
import { useEventStream } from "./useEventStream";
import { usePendingTurns } from "./usePendingTurns";
import { useReadDeliveryReceipts } from "./useReadDeliveryReceipts";

const subscribeHydration = () => () => {};

function readSavedDraft(scope: string): string {
  try {
    return localStorage.getItem(`${DRAFT_KEY}:${scope}`) ?? "";
  } catch {
    return "";
  }
}

export function useRuntime(localMode = false, scope = "local", refreshCapabilities?: () => void) {
  const cache = useContext(ResourceCacheContext);
  const invalidate = useResourceInvalidation();
  const [session, setSession] = useState<Session>();
  const hydrated = useSyncExternalStore(subscribeHydration, () => true, () => false);
  const [draftOverride, setDraftOverride] = useState<{ scope: string; value: string } | null>(null);
  const draft = draftOverride && draftOverride.scope === scope ? draftOverride.value : hydrated ? readSavedDraft(scope) : "";
  const { files: draftFiles, attach, retry, clear: clearFiles, remove: removeFile, invalidateUploads } = useDraftFiles();
  const draftValue = useRef(draft);
  const draftFilesRef = useRef(draftFiles);
  useEffect(() => {
    draftValue.current = draft;
  }, [draft]);
  useEffect(() => {
    draftFilesRef.current = draftFiles;
  }, [draftFiles]);

  const setDraft = useCallback(
    (text: string) => {
      draftValue.current = text;
      setDraftOverride({ scope, value: text });
      try {
        if (text) localStorage.setItem(`${DRAFT_KEY}:${scope}`, text);
        else localStorage.removeItem(`${DRAFT_KEY}:${scope}`);
      } catch {}
    },
    [scope],
  );

  const [errorState, setErrorState] = useState<RuntimeErrorState>(NO_RUNTIME_ERROR);
  const outageRef = useRef(false);
  useEffect(() => {
    outageRef.current = errorState.kind === "unavailable";
  }, [errorState.kind]);

  const [streamAwake, setStreamAwake] = useState(true);
  const [loading, setLoading] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [retryingMessageId, setRetryingMessageId] = useState<string>();
  const [taskDrafts, setTaskDrafts] = useState<Record<string, string>>({});
  const [selectedId, setSelectedId] = useState<string>();
  const [visible, setVisible] = useState(true);

  const hydratedStorage = useRef(false);
  const position = useRef(0);
  const controllers = useRef(new Set<AbortController>());
  const requestSerial = useRef(0);
  const minimumSerial = useRef(0);
  const replacingSnapshot = useRef(false);
  const checkingReset = useRef(false);
  const appliedSerial = useRef(0);
  const appliedRevision = useRef(-1);

  const pendingOpsRef = useRef<{
    updatePending: (update: (current: Pending[]) => Pending[]) => void;
    updateTaskCommands: (update: (current: PendingTaskCommand[]) => PendingTaskCommand[]) => void;
  } | null>(null);

  const applySnapshot = useCallback((data: Session, serial: number) => {
    if (serial < minimumSerial.current) return;
    const revision = data.conversation?.revision ?? 0;
    const replace = replacingSnapshot.current;
    if (!replace && (revision < appliedRevision.current || (revision === appliedRevision.current && serial < appliedSerial.current))) {
      return;
    }
    replacingSnapshot.current = false;
    appliedSerial.current = serial;
    appliedRevision.current = revision;
    position.current = replace ? (data.position ?? position.current) : Math.max(position.current, data.position ?? 0);
    setSession((current) =>
      replace
        ? data
        : {
            ...data,
            messages: mergeMessages(current?.messages ?? [], data.messages),
            runs: mergeRuns(current?.runs ?? [], data.runs),
            traces: mergeById(current?.traces ?? [], data.traces ?? []),
            read_position: mergeReadPosition(current?.read_position, data.read_position),
            next_cursor: current ? current.next_cursor : data.next_cursor,
          },
    );
    if (replace) setLoading(false);
    pendingOpsRef.current?.updatePending((current) => current.filter((item) => !acknowledged(data, item.id)));
    pendingOpsRef.current?.updateTaskCommands((current) => current.filter((item) => !commandApplied(item, data.runs)));
  }, []);

  const clearStreamErrorRef = useRef<() => void>(() => {});

  const refresh = useCallback(
    async (signal?: AbortSignal) => {
      const serial = ++requestSerial.current;
      const data = await fetchSnapshot(undefined, signal).catch((error) => {
        if (serial >= minimumSerial.current) throw error;
      });
      if (!data || signal?.aborted || serial < minimumSerial.current) return;
      applySnapshot(data, serial);
      clearStreamErrorRef.current();
      return data;
    },
    [applySnapshot],
  );

  const {
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
    submit,
    submitTaskCommand,
    dismissPending,
    dismissTaskCommand,
  } = usePendingTurns({
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
    invalidate,
  });

  useEffect(() => {
    pendingOpsRef.current = { updatePending, updateTaskCommands };
  }, [updatePending, updateTaskCommands]);

  const activeTasks = useMemo(() => session?.runs.filter((run) => run.status === "running") ?? [], [session?.runs]);
  const selected = useMemo(
    () => session?.runs.find((run) => run.id === selectedId) || activeTasks[0] || session?.runs[0],
    [session?.runs, selectedId, activeTasks],
  );
  const tasksUnavailable = !!session?.paused;
  const conversationBusy = !!(session?.conversation?.pending || session?.conversation?.processing);
  const taskActive =
    activeTasks.some((run) => run.cancel_requested) ||
    (activeTasks.length > 0 && !tasksUnavailable) ||
    taskCommands.some((item) => !item.rejected && !item.retryManually);
  const busy = taskActive || conversationBusy || pending.some((item) => !item.error);
  const started = !!session;

  const { markShown, resetReceipts, retryReceipts } = useReadDeliveryReceipts({
    session,
    setSession,
    visible,
    replacingSnapshot,
  });

  const { streamError, streamReconnecting, streamingDelta, clearStreamError } = useEventStream({
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
  });

  useEffect(() => {
    clearStreamErrorRef.current = clearStreamError;
  }, [clearStreamError]);

  const refreshAndRetry = useCallback(
    async (signal: AbortSignal, { reuseCachedPreferences = false }: { reuseCachedPreferences?: boolean } = {}) => {
      const cachedPrefs =
        reuseCachedPreferences && cache.isFresh("/preferences")
          ? (cache.snapshot("/preferences")?.data as Preferences | undefined)
          : undefined;
      const preferences = cachedPrefs ?? (await getJSON<Preferences>("/preferences", signal));
      if (signal.aborted) return;
      if (!cachedPrefs) cache.seed("/preferences", preferences, false);
      if (browserResetChanged(scope, preferences.data_reset_at)) {
        for (const request of controllers.current) request.abort();
        syncBrowserReset(scope, preferences.data_reset_at);
        window.dispatchEvent(new Event("nori:data-reset"));
        return;
      }
      checkingReset.current = false;
      const wasReplacing = replacingSnapshot.current;
      const epoch = minimumSerial.current;
      const data = await refresh(signal);
      if (!data || signal.aborted || checkingReset.current || wasReplacing || epoch !== minimumSerial.current) return;
      for (const item of pendingRef.current) if (!item.retryManually && !acknowledged(data, item.id)) void submit(item);
      for (const item of taskCommandsRef.current) {
        if (!item.retryManually && !item.rejected && !commandApplied(item, data.runs)) void submitTaskCommand(item);
      }
    },
    [cache, scope, refresh, submit, submitTaskCommand, pendingRef, taskCommandsRef],
  );

  useEffect(() => {
    if (!hydratedStorage.current) {
      hydratedStorage.current = true;
      const savedPending = restorePending(scope);
      if (savedPending.length) {
        pendingRef.current = savedPending;
        setPending(savedPending);
      }
      const savedCommands = restoreTaskCommands(scope);
      if (savedCommands.length) {
        taskCommandsRef.current = savedCommands;
        setTaskCommands(savedCommands);
      }
    }
    const currentControllers = controllers.current;
    const currentTimings = replyTimings.current;
    for (const item of [...restoredTimings(scope), ...pendingRef.current]) beginTiming(item);
    const controller = new AbortController();
    void refreshAndRetry(controller.signal, { reuseCachedPreferences: true })
      .catch((err) => {
        if (!controller.signal.aborted) setErrorState(toRuntimeError(err, "Unable to reach Nori."));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    const reconnect = () => {
      setVisible(!document.hidden);
      if (document.hidden) return;
      setStreamAwake(true);
      retryReceipts();
      void refreshAndRetry(controller.signal)
        .then(() => {
          if (!controller.signal.aborted) setErrorState(NO_RUNTIME_ERROR);
        })
        .catch((err) => {
          if (!controller.signal.aborted) setErrorState(toRuntimeError(err, "Unable to reach Nori."));
        });
    };
    const wake = () => setStreamAwake(true);
    document.addEventListener("visibilitychange", reconnect);
    window.addEventListener("online", reconnect);
    window.addEventListener("focus", wake);
    return () => {
      controller.abort();
      for (const active of currentControllers) active.abort();
      currentControllers.clear();
      for (const timing of currentTimings.values()) {
        clearTimeout(timing.timer);
        timing.span.setAttribute("nori.outcome", "unobserved");
        timing.span.end();
      }
      currentTimings.clear();
      document.removeEventListener("visibilitychange", reconnect);
      window.removeEventListener("online", reconnect);
      window.removeEventListener("focus", wake);
    };
  }, [refreshAndRetry, beginTiming, scope, retryReceipts, pendingRef, taskCommandsRef, replyTimings, setPending, setTaskCommands]);

  const refreshHistory = useCallback(async () => {
    setErrorState(NO_RUNTIME_ERROR);
    clearStreamErrorRef.current();
    setLoading(true);
    const controller = new AbortController();
    controllers.current.add(controller);
    try {
      await refreshAndRetry(controller.signal);
    } catch (err) {
      if (!controller.signal.aborted) setErrorState(toRuntimeError(err, "History unavailable."));
    } finally {
      controllers.current.delete(controller);
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [refreshAndRetry]);

  const loadOlder = useCallback(async () => {
    const cursor = session?.next_cursor;
    if (replacingSnapshot.current || !cursor || loadingOlder) return;
    setLoadingOlder(true);
    const controller = new AbortController();
    controllers.current.add(controller);
    try {
      const data = await fetchSnapshot(cursor, controller.signal);
      if (!controller.signal.aborted) {
        setSession((current) =>
          current
            ? {
                ...current,
                messages: mergeMessages(data.messages, current.messages),
                runs: mergeRuns(data.runs, current.runs),
                traces: mergeById(data.traces ?? [], current.traces ?? []),
                next_cursor: data.next_cursor,
              }
            : data,
        );
      }
    } catch (err) {
      if (!controller.signal.aborted) setErrorState(toRuntimeError(err, "Older history unavailable."));
    } finally {
      controllers.current.delete(controller);
      if (!controller.signal.aborted) setLoadingOlder(false);
    }
  }, [loadingOlder, session?.next_cursor]);

  const send = useCallback(async () => {
    const text = draftValue.current.trim();
    const files = draftFilesRef.current;
    if (replacingSnapshot.current || !hasSubmission(text, files) || !session || loading || session.conversation?.introducing) {
      return;
    }
    if (messageTooLong(text)) {
      setErrorState({ message: "Please keep the message within 8,192 UTF-8 bytes.", kind: "error" });
      return;
    }
    if (pendingRef.current.length >= MAX_PENDING) {
      setErrorState({ message: "Reconnect or retry your pending messages before sending more.", kind: "error" });
      return;
    }
    const item: Pending = {
      id: crypto.randomUUID(),
      text,
      started: Date.now(),
      ...(files.length ? { attachments: files.map(({ id, name, media_type, size }) => ({ id, name, media_type, size })) } : {}),
    };
    beginTiming(item);
    Sentry.metrics.count("nori.submissions", 1, { attributes: { mode: localMode ? "local" : "hosted" } });
    Sentry.addBreadcrumb({ category: "nori", message: "turn submitted" });
    updatePending((current) => [...current, item]);
    setDraft("");
    setErrorState(NO_RUNTIME_ERROR);
    clearFiles();
    await submit(item, true);
  }, [beginTiming, clearFiles, loading, localMode, pendingRef, session, setDraft, submit, updatePending]);

  const upload = useCallback((item: DraftFile, context: UploadContext) => uploadFile(item, context), []);

  const attachFiles = useCallback(
    (files: File[]) => attach(files, upload, (message) => setErrorState(message ? { message, kind: "error" } : NO_RUNTIME_ERROR)),
    [attach, upload],
  );
  const retryFile = useCallback((item: DraftFile) => retry(item, upload), [retry, upload]);

  const commandTask = useCallback(
    async (runId: string, kind: TaskCommand["kind"], text?: string, questionId?: string) => {
      if (replacingSnapshot.current) return;
      if (taskCommandsRef.current.some((item) => item.command.run_id === runId && (kind !== "stop" || item.command.kind === "stop"))) {
        return;
      }
      if (text !== undefined && (!text.trim() || messageTooLong(text.trim()))) return;
      const command: TaskCommand = {
        id: crypto.randomUUID(),
        run_id: runId,
        kind,
        ...(text !== undefined ? { text: text.trim() } : {}),
        ...(questionId ? { question_id: questionId } : {}),
      };
      const item: PendingTaskCommand = { command, started: Date.now() };
      updateTaskCommands((current) => [...current, item]);
      if (text !== undefined) setTaskDrafts((current) => ({ ...current, [runId]: "" }));
      await submitTaskCommand(item);
    },
    [submitTaskCommand, taskCommandsRef, updateTaskCommands],
  );

  const retryTurn = useCallback(
    async (opts?: { turn_id?: string; message_id?: string }) => {
      if (replacingSnapshot.current) return;
      const key = opts?.message_id ?? opts?.turn_id ?? "retry";
      setRetryingMessageId(key);
      const controller = new AbortController();
      controllers.current.add(controller);
      const serial = ++requestSerial.current;
      try {
        const payload: Record<string, string> = {};
        if (opts?.turn_id) payload.turn_id = opts.turn_id;
        if (opts?.message_id) payload.message_id = opts.message_id;
        const data = await postRuntimeOperation<Session>("retry", payload, { signal: controller.signal });
        if (!controller.signal.aborted) {
          applySnapshot(data, serial);
          setErrorState(NO_RUNTIME_ERROR);
        }
      } catch (err) {
        if (!controller.signal.aborted) {
          setErrorState(toRuntimeError(err, "Could not retry that message."));
        }
      } finally {
        controllers.current.delete(controller);
        setRetryingMessageId((current) => (current === key ? undefined : current));
      }
    },
    [applySnapshot],
  );

  const setTaskDraft = useCallback((id: string, text: string) => setTaskDrafts((current) => ({ ...current, [id]: text })), []);

  const error = errorState.message || streamError;
  const reconnecting = errorState.kind === "unavailable" || (!errorState.message && streamReconnecting);

  const core = useMemo(
    () => ({
      session,
      setDraft,
      attachFiles,
      retryFile,
      removeFile,
      error,
      reconnecting,
      loading,
      loadingOlder,
      retryingMessageId,
      pending,
      setSelectedId,
      activeTasks,
      selected,
      tasksUnavailable,
      busy,
      conversationBusy,
      taskCommands,
      taskDrafts,
      setTaskDraft,
      refreshHistory,
      loadOlder,
      send,
      retry: submit,
      retryTurn,
      dismissPending,
      commandTask,
      retryTaskCommand: submitTaskCommand,
      dismissTaskCommand,
      markShown,
    }),
    [
      session,
      setDraft,
      attachFiles,
      retryFile,
      removeFile,
      error,
      reconnecting,
      loading,
      loadingOlder,
      retryingMessageId,
      pending,
      activeTasks,
      selected,
      tasksUnavailable,
      busy,
      conversationBusy,
      taskCommands,
      taskDrafts,
      setTaskDraft,
      refreshHistory,
      loadOlder,
      send,
      submit,
      retryTurn,
      dismissPending,
      commandTask,
      submitTaskCommand,
      dismissTaskCommand,
      markShown,
    ],
  );

  const draftState = useMemo(
    () => ({
      draft,
      draftFiles,
    }),
    [draft, draftFiles],
  );

  const live = useMemo(
    () => ({
      draft,
      draftFiles,
      streamingDelta,
    }),
    [draft, draftFiles, streamingDelta],
  );

  return useMemo(() => ({ ...core, ...live, core, live, draftState }), [core, live, draftState]);
}

export type RuntimeCore = ReturnType<typeof useRuntime>["core"];
export type RuntimeDraft = ReturnType<typeof useRuntime>["draftState"];
export type RuntimeLive = ReturnType<typeof useRuntime>["live"];
export type StreamingDelta = ReturnType<typeof useRuntime>["streamingDelta"];
