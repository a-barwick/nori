"use client";

import { useEffect, useRef, useState } from "react";
import { Dialog } from "radix-ui";
import { Check, Eye, Hand, Monitor, X } from "lucide-react";
import { Button, IconButton } from "@/components/ui/primitives";
import {
  buildDesktopWSURL,
  createComputerSession,
  endComputerSession,
  updateComputerSessionMode,
} from "@/lib/assistant/computer";
import { NoriRFBClient, type RFBConnectionState } from "@/lib/computer/rfbClient";
import type { CreateComputerSessionResponse } from "@/lib/contracts/assistant";
import { Notice, Pill } from "./ui";

export interface ComputerDesktopDrawerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initialMode?: "view" | "control";
  taskId?: string;
  interactionId?: string;
  prompt?: string;
  onDoneContinue?: () => Promise<void> | void;
}

export function ComputerDesktopDrawer({
  open,
  onOpenChange,
  initialMode = "view",
  taskId,
  interactionId,
  prompt,
  onDoneContinue,
}: ComputerDesktopDrawerProps) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-[var(--overlay-scrim)] backdrop-blur-[2px]" />
        {open && (
          <ComputerDesktopDrawerBody
            key={`${initialMode}:${taskId ?? ""}:${interactionId ?? ""}`}
            onOpenChange={onOpenChange}
            initialMode={initialMode}
            taskId={taskId}
            interactionId={interactionId}
            prompt={prompt}
            onDoneContinue={onDoneContinue}
          />
        )}
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function ComputerDesktopDrawerBody({
  onOpenChange,
  initialMode,
  taskId,
  interactionId,
  prompt,
  onDoneContinue,
}: Omit<ComputerDesktopDrawerProps, "open"> & { initialMode: "view" | "control" }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const clientRef = useRef<NoriRFBClient | null>(null);
  const sessionRef = useRef<CreateComputerSessionResponse | null>(null);

  const [mode, setMode] = useState<"view" | "control">(initialMode);
  const [connState, setConnState] = useState<RFBConnectionState>("connecting");
  const [error, setError] = useState<string | null>(null);
  const [switching, setSwitching] = useState(false);
  const [completing, setCompleting] = useState(false);

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      try {
        const session = await createComputerSession({
          mode: initialMode,
          task_id: taskId,
          interaction_id: interactionId,
        });
        if (cancelled) {
          void endComputerSession(session.id).catch(() => {});
          return;
        }
        sessionRef.current = session;
        const canvas = canvasRef.current;
        if (!canvas) return;
        const client = new NoriRFBClient({
          canvas,
          url: buildDesktopWSURL(session),
          viewOnly: initialMode === "view",
          onStateChange: (nextState, detail) => {
            if (!cancelled) {
              setConnState(nextState);
              if (nextState === "error" && detail) {
                setError(detail);
              }
            }
          },
        });
        clientRef.current = client;
        client.connect();
      } catch (err) {
        if (!cancelled) {
          setConnState("error");
          setError(err instanceof Error ? err.message : "Could not open your computer desktop.");
        }
      }
    })();

    return () => {
      cancelled = true;
      clientRef.current?.disconnect();
      clientRef.current = null;
      const active = sessionRef.current;
      sessionRef.current = null;
      if (active) {
        void endComputerSession(active.id).catch(() => {});
      }
    };
  }, [initialMode, taskId, interactionId]);

  async function toggleMode() {
    const active = sessionRef.current;
    if (!active || switching) return;
    const nextMode = mode === "view" ? "control" : "view";
    setSwitching(true);
    setError(null);
    try {
      await updateComputerSessionMode(active.id, nextMode);
      setMode(nextMode);
      clientRef.current?.setViewOnly(nextMode === "view");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not switch desktop control mode.");
    } finally {
      setSwitching(false);
    }
  }

  async function handleDoneContinue() {
    if (!onDoneContinue || completing) return;
    setCompleting(true);
    try {
      await onDoneContinue();
      onOpenChange(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not resume the task.");
    } finally {
      setCompleting(false);
    }
  }

  const statusTone =
    connState === "connected"
      ? mode === "control"
        ? "working"
        : "ok"
      : connState === "error"
        ? "danger"
        : "warning";

  const statusLabel =
    connState === "connected"
      ? mode === "control"
        ? "In control"
        : "Watching"
      : connState === "reconnecting"
        ? "Reconnecting…"
        : connState === "error"
          ? "Disconnected"
          : "Starting desktop…";

  return (
    <Dialog.Content className="fixed inset-4 z-[51] mx-auto flex max-w-6xl flex-col overflow-hidden rounded-[28px] border border-card-border bg-sheet-bg shadow-2xl outline-none">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-hairline px-5 py-3.5">
        <div className="flex items-center gap-2.5">
          <span className="grid size-8 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
            <Monitor size={16} strokeWidth={1.85} aria-hidden />
          </span>
          <div>
            <Dialog.Title className="text-row font-semibold text-ink">
              Nori Computer
            </Dialog.Title>
            {prompt && (
              <Dialog.Description className="text-meta text-ink-2">
                {prompt}
              </Dialog.Description>
            )}
          </div>
          <Pill tone={statusTone}>{statusLabel}</Pill>
        </div>

        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant={mode === "control" ? "neutral" : "primary"}
            disabled={switching || connState !== "connected"}
            onClick={() => void toggleMode()}
          >
            {mode === "control" ? (
              <>
                <Eye size={14} className="mr-1.5" aria-hidden />
                Hand back to Nori
              </>
            ) : (
              <>
                <Hand size={14} className="mr-1.5" aria-hidden />
                Take control
              </>
            )}
          </Button>
          {onDoneContinue && (
            <Button
              size="sm"
              variant="primary"
              disabled={completing}
              onClick={() => void handleDoneContinue()}
            >
              <Check size={14} className="mr-1.5" aria-hidden />
              {completing ? "Resuming…" : "Done, continue"}
            </Button>
          )}
          <IconButton
            label="Close desktop"
            onClick={() => onOpenChange(false)}
          >
            <X size={16} />
          </IconButton>
        </div>
      </div>

      {error && (
        <div className="px-5 pt-3">
          <Notice tone="danger">{error}</Notice>
        </div>
      )}

      <div className="relative flex flex-1 items-center justify-center overflow-auto bg-[#07070A] p-3">
        <canvas
          ref={canvasRef}
          width={1280}
          height={800}
          className="max-h-full max-w-full rounded-tile bg-black object-contain outline-none focus-visible:ring-2 focus-visible:ring-accent"
          aria-label="Live Nori computer desktop"
        />
      </div>

      <div className="flex items-center justify-between border-t border-hairline px-5 py-2.5 text-meta text-ink-2">
        <span>
          {mode === "control"
            ? "Click inside the screen to type, sign in, or interact directly. Your sign-ins stay saved on your computer."
            : "Read-only view. Choose Take control whenever you want to click or sign in."}
        </span>
        <span>Auto-sleeps when idle</span>
      </div>
    </Dialog.Content>
  );
}
