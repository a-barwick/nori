"use client";

import { useEffect, useState } from "react";
import { Check, ChevronDown, Globe, List, Terminal, X } from "lucide-react";
import { Medallion } from "@/components/mark/NoriMark";
import { Button, IconButton, SectionHeader } from "@/components/ui/primitives";
import type { Artifact, Run, Session } from "@/lib/poc/types";
import { clock } from "@/lib/time";
import { cn } from "@/lib/utils";

export function statusLabel(run: Run) {
  return run.status === "running" && run.cancel_requested
    ? "Stopping…"
    : {
        running: "Working on your task",
        completed: "Completed",
        failed: "Failed",
        cancelled: "Cancelled",
      }[run.status];
}

function Screenshot({
  runId,
  artifact,
}: {
  runId: string;
  artifact: Artifact;
}) {
  const [url, setUrl] = useState<string>();
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    let objectUrl: string | undefined;
    async function load() {
      try {
        const response = await fetch("/api/artifact", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ run_id: runId, artifact_id: artifact.id }),
          signal: controller.signal,
        });
        if (
          !response.ok ||
          !response.headers.get("content-type")?.startsWith("image/png")
        )
          throw new Error("Screenshot could not be loaded.");
        objectUrl = URL.createObjectURL(await response.blob());
        if (!controller.signal.aborted) setUrl(objectUrl);
      } catch (err) {
        if (!controller.signal.aborted)
          setError(
            err instanceof Error ? err.message : "Screenshot unavailable.",
          );
      }
    }
    void load();
    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [runId, artifact.id]);
  if (error)
    return (
      <p role="status" className="text-meta text-ink-2">
        {error}
      </p>
    );
  if (!url) return <p className="text-meta text-ink-3">Loading screenshot…</p>;
  return (
    <a
      href={url}
      target="_blank"
      rel="noreferrer"
      className="block overflow-hidden rounded-tile bg-surface shadow-pill"
    >
      {/* PNG bytes arrive only from the authenticated artifact route. */}
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img
        src={url}
        alt="Screenshot captured by Nori during this task. Open full size."
        className="w-full"
      />
      <span className="block p-3 text-meta text-accent-text">
        Open screenshot ↗
      </span>
    </a>
  );
}

export function TaskPanel({
  session,
  selected,
  pending,
  busy,
  loading,
  error,
  unavailable,
  stopping,
  stopDisabled,
  onSelect,
  onStop,
  onRefresh,
  onClose,
}: {
  session?: Session;
  selected?: Run;
  pending: boolean;
  busy: boolean;
  loading: boolean;
  error: string;
  unavailable: boolean;
  stopping: boolean;
  stopDisabled: boolean;
  onSelect: (id: string) => void;
  onStop: () => void;
  onRefresh: () => void;
  onClose: () => void;
}) {
  const connection = error
    ? "Connection interrupted"
    : loading
      ? "Loading history…"
      : !session
        ? "Runtime unavailable"
        : unavailable
          ? session.paused
            ? "Session paused"
            : "Session expired"
          : busy
            ? "Working on 1 thing"
            : "Ready when you are";
  return (
    <div className="flex h-full flex-col bg-canvas px-4">
      <header className="relative flex shrink-0 flex-col items-center pt-12">
        <IconButton
          size={36}
          label="Hide agent panel"
          onClick={onClose}
          className="absolute top-3 right-0"
        >
          <X size={18} strokeWidth={1.5} />
        </IconButton>
        <Medallion
          size={96}
          state={busy ? "thinking" : "idle"}
          look="onigiri"
          watch
        />
        <h2 className="mt-3 text-title font-medium text-ink">Nori</h2>
        <p
          className="mt-1 flex items-center gap-2 text-status text-ink-2"
          role="status"
        >
          <span
            aria-hidden
            className={cn(
              "size-2 rounded-full",
              error || unavailable || !session
                ? "bg-ink-3"
                : "bg-accent-signal",
              busy && "pulse-dot",
            )}
          />
          {connection}
        </p>
        <div className="mt-6 mb-3 flex w-full items-center justify-between rounded-full bg-fill-1 px-3 py-2">
          <h3 className="flex items-center gap-2 text-meta font-medium">
            <List size={17} strokeWidth={1.5} />
            Activity
          </h3>
          {busy && (
            <Button
              size="sm"
              variant="quiet"
              disabled={stopDisabled}
              onClick={onStop}
            >
              {stopping ? "Stopping…" : "Stop"}
            </Button>
          )}
        </div>
      </header>
      <div className="scrollbar-quiet -mx-4 min-h-0 flex-1 overflow-y-auto px-4 pb-6">
        {!selected ? (
          <p className="px-2 py-5 text-meta text-ink-3">
            {pending
              ? "Sending task to Nori…"
              : "Activity and screenshots from your tasks appear here."}
          </p>
        ) : (
          <>
            <p className="px-2 py-2 text-meta font-medium text-ink">
              {statusLabel(selected)}
            </p>
            {selected.cancel_requested && selected.status === "running" && (
              <p className="px-2 pb-3 text-meta text-ink-2">
                Waiting for Nori’s computer to confirm it has stopped.
              </p>
            )}
            {selected.error && (
              <p
                role="status"
                className="mb-3 rounded-tile bg-danger-bg p-3 text-meta text-danger"
              >
                {selected.error}
              </p>
            )}
            <ol className="space-y-2" aria-label="Task events">
              {selected.events?.map((event, index) => {
                const Icon = /browser|web/.test(event.kind)
                  ? Globe
                  : /shell|command/.test(event.kind)
                    ? Terminal
                    : Check;
                return (
                  <li
                    key={`${selected.id}-${index}`}
                    className="flex items-start gap-3 rounded-tile px-2 py-2"
                  >
                    <span className="grid size-10 shrink-0 place-items-center rounded-tile bg-fill-1 text-ink">
                      <Icon size={20} strokeWidth={1.5} />
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-row-title font-medium capitalize">
                          {event.kind.replaceAll("_", " ")}
                        </span>
                        <time className="text-caption text-ink-3">
                          {clock(Date.parse(event.created_at))}
                        </time>
                      </div>
                      <p className="mt-1 whitespace-pre-wrap break-words text-meta text-ink-2 [overflow-wrap:anywhere]">
                        {event.text}
                      </p>
                    </div>
                  </li>
                );
              })}
            </ol>
            {!!selected.artifacts?.length && (
              <section className="space-y-3">
                <SectionHeader>Screenshots</SectionHeader>
                {selected.artifacts.map((artifact) => (
                  <Screenshot
                    key={`${selected.id}-${artifact.id}`}
                    runId={selected.id}
                    artifact={artifact}
                  />
                ))}
              </section>
            )}
          </>
        )}
        {!!session?.runs.length && (
          <section>
            <SectionHeader>Recent tasks</SectionHeader>
            {session.runs.map((run) => (
              <button
                key={run.id}
                type="button"
                aria-pressed={selected?.id === run.id}
                onClick={() => onSelect(run.id)}
                className={cn(
                  "my-1 flex w-full items-center gap-2 rounded-tile p-3 text-left hover:bg-fill-1",
                  selected?.id === run.id && "bg-fill-1",
                )}
              >
                <span className="min-w-0 flex-1">
                  <span className="line-clamp-2 text-row-title font-medium">
                    {session.messages.find(
                      (message) =>
                        message.run_id === run.id && message.role === "user",
                    )?.text || "Task"}
                  </span>
                  <small className="mt-1 block text-caption text-ink-3">
                    {statusLabel(run)}
                  </small>
                </span>
                <ChevronDown
                  size={14}
                  className="shrink-0 -rotate-90 text-ink-3"
                />
              </button>
            ))}
          </section>
        )}
        <Button
          size="sm"
          variant="quiet"
          onClick={onRefresh}
          disabled={loading}
          className="mt-4 w-full"
        >
          {loading ? "Loading…" : "Refresh history"}
        </Button>
      </div>
    </div>
  );
}
