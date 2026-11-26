"use client";

import { useCallback, useEffect, useRef, useState, type Dispatch, type MutableRefObject, type SetStateAction } from "react";
import { postRuntimeOperation } from "@/lib/assistant/api";
import type { ReadPosition, Session } from "@/lib/types";
import { mergeReadPosition } from "./runtimeHelpers";

const MAX_RECEIPT_RETRIES = 5;
const receiptDelayMs = (attempt: number) => Math.min(30_000, 2_000 * 2 ** Math.max(0, attempt - 1));

type UseReadDeliveryReceiptsParams = {
  session: Session | undefined;
  setSession: Dispatch<SetStateAction<Session | undefined>>;
  visible: boolean;
  replacingSnapshot: MutableRefObject<boolean>;
};

export function useReadDeliveryReceipts({
  session,
  setSession,
  visible,
  replacingSnapshot,
}: UseReadDeliveryReceiptsParams) {
  const [shownMessageIDs, setShownMessageIDs] = useState<Set<string>>(() => new Set());
  const [deliveryTick, setDeliveryTick] = useState(0);
  const [readTick, setReadTick] = useState(0);

  const reported = useRef(new Set<string>());
  const inFlightDelivery = useRef(new Set<string>());
  const deliveryController = useRef<AbortController | null>(null);
  const deliveryTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const deliveryAttempts = useRef(0);

  const inFlightSeenId = useRef<string | null>(null);
  const lastSeenAckedId = useRef<string | null>(null);
  const seenController = useRef<AbortController | null>(null);
  const seenTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const seenAttempts = useRef(0);

  const markShown = useCallback(
    (ids: string[]) => {
      if (document.hidden || replacingSnapshot.current) return;
      setShownMessageIDs((current) => (ids.every((id) => current.has(id)) ? current : new Set([...current, ...ids])));
    },
    [replacingSnapshot],
  );

  const resetReceipts = useCallback(() => {
    deliveryController.current?.abort();
    deliveryController.current = null;
    clearTimeout(deliveryTimer.current);
    deliveryTimer.current = undefined;
    deliveryAttempts.current = 0;
    inFlightDelivery.current.clear();
    reported.current = new Set();

    seenController.current?.abort();
    seenController.current = null;
    clearTimeout(seenTimer.current);
    seenTimer.current = undefined;
    seenAttempts.current = 0;
    inFlightSeenId.current = null;
    lastSeenAckedId.current = null;

    setShownMessageIDs(new Set());
  }, []);

  const retryReceipts = useCallback(() => {
    deliveryAttempts.current = 0;
    seenAttempts.current = 0;
    setDeliveryTick((tick) => tick + 1);
    setReadTick((tick) => tick + 1);
  }, []);

  useEffect(() => {
    return () => {
      deliveryController.current?.abort();
      clearTimeout(deliveryTimer.current);
      seenController.current?.abort();
      clearTimeout(seenTimer.current);
    };
  }, []);

  // Delivery receipts: never abort in-flight requests on unrelated SSE session updates.
  useEffect(() => {
    if (!session || !visible || document.hidden || !navigator.onLine || replacingSnapshot.current) return;
    if (deliveryController.current) return;
    const shown = new Set(
      session.messages
        .filter((message) => message.role === "assistant" && shownMessageIDs.has(message.id))
        .flatMap((message) => message.result_ids ?? []),
    );
    const ids = (session.results ?? [])
      .filter((result) => shown.has(result.id) && !result.delivered_at && !reported.current.has(result.id) && !inFlightDelivery.current.has(result.id))
      .map((result) => result.id);
    if (!ids.length) return;

    const controller = new AbortController();
    deliveryController.current = controller;
    for (const id of ids) inFlightDelivery.current.add(id);

    void postRuntimeOperation("delivered", { ids }, { signal: controller.signal })
      .then(() => {
        if (controller.signal.aborted) return;
        for (const id of ids) {
          inFlightDelivery.current.delete(id);
          reported.current.add(id);
        }
        deliveryAttempts.current = 0;
      })
      .catch(() => {
        for (const id of ids) inFlightDelivery.current.delete(id);
        if (controller.signal.aborted) return;
        deliveryAttempts.current += 1;
        if (deliveryAttempts.current <= MAX_RECEIPT_RETRIES) {
          clearTimeout(deliveryTimer.current);
          deliveryTimer.current = setTimeout(() => setDeliveryTick((tick) => tick + 1), receiptDelayMs(deliveryAttempts.current));
        }
      })
      .finally(() => {
        if (deliveryController.current === controller) {
          deliveryController.current = null;
        }
      });
  }, [session, shownMessageIDs, visible, deliveryTick, replacingSnapshot]);

  const shownMessageID =
    session?.messages.findLast(
      (message) => message.role === "assistant" && message.status !== "failed" && message.status !== "superseded" && shownMessageIDs.has(message.id),
    )?.id ?? "";

  // Read-position ("seen") receipts: never abort in-flight requests on unrelated SSE updates.
  useEffect(() => {
    if (!visible || document.hidden || !navigator.onLine || !shownMessageID || replacingSnapshot.current) return;
    if (inFlightSeenId.current === shownMessageID || lastSeenAckedId.current === shownMessageID) return;
    const messages = session?.messages ?? [];
    const shownIndex = messages.findIndex((message) => message.id === shownMessageID && message.role === "assistant");
    if (shownIndex < 0) return;
    const read = session?.read_position;
    const readIndex = messages.findIndex((message) => message.id === read?.message_id);
    if (readIndex >= shownIndex || (readIndex < 0 && read?.message_id && Date.parse(read.through) >= Date.parse(messages[shownIndex].created_at))) {
      return;
    }

    seenController.current?.abort();
    const controller = new AbortController();
    seenController.current = controller;
    inFlightSeenId.current = shownMessageID;

    void postRuntimeOperation<{ read_position: ReadPosition }>("seen", { message_id: shownMessageID }, { signal: controller.signal })
      .then((data) => {
        if (controller.signal.aborted) return;
        lastSeenAckedId.current = shownMessageID;
        seenAttempts.current = 0;
        setSession((current) => (current ? { ...current, read_position: mergeReadPosition(current.read_position, data.read_position) } : current));
      })
      .catch(() => {
        if (controller.signal.aborted) return;
        seenAttempts.current += 1;
        if (seenAttempts.current <= MAX_RECEIPT_RETRIES) {
          clearTimeout(seenTimer.current);
          seenTimer.current = setTimeout(() => setReadTick((tick) => tick + 1), receiptDelayMs(seenAttempts.current));
        }
      })
      .finally(() => {
        if (seenController.current === controller) {
          seenController.current = null;
          inFlightSeenId.current = null;
        }
      });
  }, [session?.messages, session?.read_position, shownMessageID, visible, readTick, replacingSnapshot, setSession]);

  return { markShown, resetReceipts, retryReceipts };
}
