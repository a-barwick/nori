"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Check, Copy, MessageSquare, Square } from "lucide-react";
import { Markdown } from "@/components/chat/Markdown";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { money, when } from "@/lib/assistant/format";
import { useWork, useWorkDetail } from "@/lib/assistant/sources";
import { useAction, useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { LibraryItem, WorkItem } from "@/lib/contracts/assistant";
import { MAX_MESSAGE_BYTES, messageTooLong } from "@/lib/submission";
import { cn } from "@/lib/utils";
import { InteractionCard } from "./InteractionCard";
import { OperationRows, shownOperations, stepName } from "./WorkOperations";
import { statusPill } from "./WorkScreen";
import { Empty, Failure, Field, inputBase, inputClass, Loading, Notice, Pill, Row, Rows, Screen, Section } from "./ui";

const SPENDING_LIMIT = /spending limit/i;
const MAX_TRANSIENT_RETRIES = 3;

export function isRetryableWorkDetailError(error: unknown): boolean {
  if (error instanceof APIError) {
    return error.status === 404 || error.status === 502 || error.status === 503 || error.status === 504 || error.status === 0;
  }
  return false;
}

function Continue({ item }: { item: WorkItem }) {
  const invalidate = useResourceInvalidation();
  const router = useRouter();
  const action = useAction();
  const limited = SPENDING_LIMIT.test(item.error?.message ?? "");
  const [budget, setBudget] = useState(String(Math.max(1, (item.cost.budget_usd ?? 1) * 2)));
  const go = () =>
    action.run(
      "continue",
      async () => {
        const receipt = await mutate(`/work/${encodeURIComponent(item.task_id)}/actions`, {
          request_id: newRequestID(),
          expected_revision: 0,
          value: { action: "continue", ...(limited && Number(budget) > 0 ? { budget_usd: Number(budget) } : {}) },
        });
        invalidate("/work", "/assistant/summary");
        if (receipt.status === "queued" && receipt.work?.task_id) {
          router.push(`/work/${encodeURIComponent(receipt.work.task_id)}`);
        }
      },
      "Continuing in a new task.",
    );
  return (
    <Section title="Continue">
      <form
        className="flex flex-wrap items-end gap-3 rounded-card border border-card-border bg-surface p-4"
        onSubmit={(event) => {
          event.preventDefault();
          void go();
        }}
      >
        {limited && (
          <Field label="New spending limit (USD)">
            <input
              type="number"
              min={0.01}
              step="any"
              value={budget}
              onChange={(event) => setBudget(event.target.value)}
              className={cn(inputBase, "w-32")}
            />
          </Field>
        )}
        <Button type="submit" variant="primary" disabled={!!action.pending || (limited && !(Number(budget) > 0))}>
          {action.pending ? "Starting…" : "Continue this task"}
        </Button>
      </form>
      {action.error && (
        <div className="mt-2">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}
    </Section>
  );
}

export function WorkDetailScreen({ taskID }: { taskID: string }) {
  const signInResult = useSearchParams().get("result");
  const { runtime, has } = useAssistant();
  const { detail, error, loading, reload, live } = useWorkDetail(taskID);
  const work = useWork();
  const continuedBy = (work.items ?? []).filter((other) => other.origin.continues_task_id === taskID);
  const item = detail?.item;
  const results = useResource<{ items: LibraryItem[] }>(
    has("library") && item?.result_ids.length ? `/library?task_id=${encodeURIComponent(taskID)}` : null,
    { poll: 0 },
  );
  const saved = (results.data?.items ?? []).filter((result) => item?.result_ids.includes(result.id));
  const router = useRouter();
  const commands = runtime.taskCommands.filter((command) => command.command.run_id === taskID);
  const [draft, setDraft] = useState("");
  const [copiedResult, setCopiedResult] = useState(false);
  const [copiedTaskID, setCopiedTaskID] = useState(false);
  const [retryState, setRetryState] = useState<{ taskID: string; count: number }>({ taskID, count: 0 });
  const retries = retryState.taskID === taskID ? retryState.count : 0;
  const settling = !item && isRetryableWorkDetailError(error) && retries < MAX_TRANSIENT_RETRIES;

  useEffect(() => {
    if (!settling) return;
    const delayMs = 500 * 2 ** retries;
    const timer = setTimeout(() => {
      setRetryState((prev) => ({ taskID, count: prev.taskID === taskID ? prev.count + 1 : 1 }));
      void reload();
    }, delayMs);
    return () => clearTimeout(timer);
  }, [settling, retries, reload, taskID]);

  if (loading || settling) {
    return (
      <Screen title="Task" back="/work" backLabel="Work">
        <Loading />
      </Screen>
    );
  }
  if (!item) {
    return (
      <Screen title="Task" back="/work" backLabel="Work">
        {error ? (
          <Failure error={error} onRetry={() => void reload()} />
        ) : (
          <Empty
            action={
              <Link href="/work" className="text-meta font-semibold text-accent-text hover:underline">
                Back to Work
              </Link>
            }
          >
            This task isn’t in your recent work.
          </Empty>
        )}
      </Screen>
    );
  }

  const pill = statusPill(item);
  const running = item.status === "running" && !item.cancel_requested;
  const stopping = commands.some((command) => command.command.kind === "stop") || item.cancel_requested;
  const pendingInteractions = detail.interactions.filter((interaction) => interaction.status === "pending");
  const actionablePending = pendingInteractions.filter((interaction) => interaction.kind !== "question");
  const waitingInChat =
    item.status === "running" &&
    !item.cancel_requested &&
    (item.bucket === "needs_you" || pendingInteractions.some((interaction) => interaction.kind === "question")) &&
    actionablePending.length === 0;
  const visibleOperations = shownOperations(detail.operations);
  const coveredLabels = new Set(
    detail.operations.flatMap((operation) => [
      operation.name.trim().toLowerCase(),
      stepName(operation.name).trim().toLowerCase(),
      ...(operation.action_summary ? [operation.action_summary.trim().toLowerCase()] : []),
    ]),
  );
  const extraSteps = (detail.events ?? []).filter((event) => {
    const normalized = event.text.trim().toLowerCase();
    return normalized !== "" && normalized !== "capability_load" && !coveredLabels.has(normalized);
  });
  const hasSteps = visibleOperations.length > 0 || extraSteps.length > 0;
  const tooLong = messageTooLong(draft.trim());
  const sending = commands.some((command) => command.command.kind === "steer" && !command.error);
  const needsModelKey =
    item.error?.code === "model_key_required" ||
    item.error?.code === "missing_api_key" ||
    item.error?.message?.includes("More → Models");
  const line =
    item.status === "running" && !item.cancel_requested && !pendingInteractions.length && item.progress
      ? item.progress
      : item.status === "completed" && item.operations.uncertain
        ? "Finished, but one action isn’t confirmed yet."
        : item.status === "failed" && item.error?.message
          ? item.error.message
          : undefined;

  const copyText = (value: string, setFlag: (v: boolean) => void) => {
    if (typeof navigator === "undefined" || !navigator.clipboard) return;
    void navigator.clipboard.writeText(value).then(() => {
      setFlag(true);
      setTimeout(() => setFlag(false), 1800);
    });
  };

  const followUpInChat = () => {
    runtime.setDraft(`Follow up on "${item.title}": `);
    router.push("/?compose=1");
  };

  return (
    <Screen
      title={item.title}
      back="/work"
      backLabel="Work"
      line={line}
      actions={<Pill tone={pill.tone}>{pill.label}</Pill>}
    >
      {needsModelKey && (
        <div className="mb-4">
          <Link
            href="/settings/models"
            className="inline-flex h-8 items-center rounded-full bg-fill-1 px-3.5 text-meta font-semibold text-ink transition-colors hover:bg-fill-2"
          >
            Open Models
          </Link>
        </div>
      )}

      {signInResult && (
        <div className="mb-5">
          <Notice tone={signInResult === "connected" ? "ok" : signInResult === "cancelled" ? "neutral" : "danger"}>
            {signInResult === "connected"
              ? "Signed in. Nori is continuing this task."
              : signInResult === "cancelled"
                ? "Sign-in was cancelled. The task is still waiting."
                : "Sign-in didn’t finish. Try again from the card below."}
          </Notice>
        </div>
      )}

      {pendingInteractions.length > 0 && (
        <Section title="Action needed">
          <div className="space-y-3">
            {pendingInteractions.map((interaction) => (
              <InteractionCard key={interaction.id} interaction={interaction} compact />
            ))}
          </div>
        </Section>
      )}

      {waitingInChat && (
        <div className="mt-4">
          <Link
            href={`/?compose=1&reply_title=${encodeURIComponent(item.title)}`}
            className="inline-flex h-9 items-center gap-1.5 rounded-full bg-accent px-4 text-row-title font-medium text-on-accent transition-colors hover:bg-accent-hover"
          >
            <MessageSquare size={14} aria-hidden />
            Reply in Chat
          </Link>
        </div>
      )}

      {item.operations.uncertain > 0 && (
        <div className="mt-5">
          <Notice tone="warning">
            An action may or may not have gone through. Nori is checking with the provider and won’t repeat it.
          </Notice>
        </div>
      )}

      {running && (
        <Section title="Steer this task">
          <form
            className="space-y-2.5 rounded-card border border-card-border bg-surface p-4"
            onSubmit={(event) => {
              event.preventDefault();
              const text = draft.trim();
              if (!text || tooLong) return;
              void runtime.commandTask(taskID, "steer", text).then(() => setDraft(""));
            }}
          >
            <textarea
              aria-label="Instruction for this task"
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              rows={2}
              maxLength={MAX_MESSAGE_BYTES}
              className={cn(inputClass, "resize-y")}
              placeholder="Also check Monday, use the work calendar…"
            />
            {tooLong && (
              <p role="alert" className="text-meta text-danger">
                Keep this within 8,192 bytes.
              </p>
            )}
            <div className="flex flex-wrap gap-2">
              <Button type="submit" variant="primary" disabled={!draft.trim() || tooLong || sending}>
                {sending ? "Sending…" : "Send update"}
              </Button>
              <Button variant="danger" disabled={stopping} onClick={() => void runtime.commandTask(taskID, "stop")}>
                <Square size={12} fill="currentColor" aria-hidden />
                {stopping ? "Stopping…" : "Stop"}
              </Button>
            </div>
          </form>
          {commands
            .filter((command) => command.error)
            .map((command) => (
              <div key={command.command.id} className="mt-2">
                <Notice
                  tone="danger"
                  action={
                    command.rejected ? (
                      <Button size="sm" onClick={() => runtime.dismissTaskCommand(command.command.id)}>
                        Dismiss
                      </Button>
                    ) : (
                      <Button size="sm" onClick={() => void runtime.retryTaskCommand(command)}>
                        Retry
                      </Button>
                    )
                  }
                >
                  {command.error}
                </Notice>
              </div>
            ))}
        </Section>
      )}

      {item.status === "running" && item.cancel_requested && (
        <div className="mt-5">
          <Notice>Stopping. Work already done stays done; anything unconfirmed stays marked.</Notice>
        </div>
      )}

      {live && item.status === "failed" && !continuedBy.length && <Continue item={item} />}

      {detail.result && (
        <Section title={item.status === "failed" ? "Partial answer" : "Result"}>
          <div className="rounded-card border border-card-border bg-surface p-4 text-body [overflow-wrap:anywhere]">
            <Markdown variant="page">{detail.result}</Markdown>
          </div>
        </Section>
      )}

      {item.status !== "running" && (
        <div className="mt-4 flex flex-wrap gap-2">
          {detail.result && (
            <Button variant="neutral" size="sm" onClick={() => copyText(detail.result!, setCopiedResult)}>
              {copiedResult ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
              {copiedResult ? "Copied" : "Copy"}
            </Button>
          )}
          <Button variant="tonal" size="sm" onClick={followUpInChat}>
            <MessageSquare size={14} aria-hidden />
            Follow up in Chat
          </Button>
        </div>
      )}

      {saved.length > 0 && (
        <Section title="Saved artifacts">
          <Rows>
            {saved.map((result) => (
              <Row
                key={result.id}
                href={`/library/${encodeURIComponent(result.id)}`}
                title={result.title}
                detail={result.version > 1 ? `Version ${result.version}` : undefined}
              />
            ))}
          </Rows>
        </Section>
      )}

      {!has("library") && item.result_ids.length > 0 && (
        <Section title="Saved artifacts">
          <Rows>
            {item.result_ids.map((id) => {
              const found = runtime.session?.results?.find((result) => result.id === id);
              return <Row key={id} href={`/library/${encodeURIComponent(id)}`} title={found?.title ?? "Saved result"} />;
            })}
          </Rows>
        </Section>
      )}

      {hasSteps && (
        <Section title="Steps">
          <div className="space-y-3">
            {visibleOperations.length > 0 && <OperationRows operations={visibleOperations} />}
            {extraSteps.length > 0 && (
              <Rows label="Task milestones">
                {extraSteps.map((event, index) => (
                  <Row
                    key={`${event.created_at}-${index}`}
                    title={event.text}
                    detail={event.created_at ? when(event.created_at) : undefined}
                    wrapTitle
                  />
                ))}
              </Rows>
            )}
          </div>
        </Section>
      )}

      <Section title="Details">
        <dl className="grid grid-cols-[auto_1fr] items-baseline gap-x-6 gap-y-2 text-meta">
          <dt className="text-ink-3">Origin</dt>
          <dd className="text-ink">
            {item.origin.kind === "automation" && item.origin.automation_id ? (
              <Link className="font-medium text-accent-text hover:underline" href={`/routines/${encodeURIComponent(item.origin.automation_id)}`}>
                Routine
              </Link>
            ) : item.origin.kind === "goal" && item.origin.goal_id ? (
              <Link className="font-medium text-accent-text hover:underline" href={`/goals/${encodeURIComponent(item.origin.goal_id)}`}>
                Goal
              </Link>
            ) : item.origin.kind === "external" ? (
              "Another app"
            ) : (
              <Link className="font-medium text-accent-text hover:underline" href="/">
                Chat
              </Link>
            )}
          </dd>
          <dt className="text-ink-3">Started</dt>
          <dd className="text-ink">{when(item.started_at)}</dd>
          {item.finished_at && (
            <>
              <dt className="text-ink-3">Finished</dt>
              <dd className="text-ink">{when(item.finished_at)}</dd>
            </>
          )}
          <dt className="text-ink-3">Cost</dt>
          <dd className="text-ink">
            <code className="font-mono">
              {item.cost.status === "known"
                ? money(item.cost.amount_usd)
                : item.cost.status === "unknown"
                  ? item.cost.amount_usd
                    ? `At least ${money(item.cost.amount_usd)}`
                    : "Unknown"
                  : "None"}
            </code>
            {item.cost.budget_usd ? ` / ${money(item.cost.budget_usd)}` : ""}
          </dd>
          {item.origin.continues_task_id && (
            <>
              <dt className="text-ink-3">Continues</dt>
              <dd>
                <Link
                  className="font-medium text-accent-text hover:underline"
                  href={`/work/${encodeURIComponent(item.origin.continues_task_id)}`}
                >
                  Earlier task
                </Link>
              </dd>
            </>
          )}
          {continuedBy.length > 0 && (
            <>
              <dt className="text-ink-3">Continued by</dt>
              <dd className="flex flex-wrap gap-2">
                {continuedBy.map((other) => (
                  <Link
                    key={other.task_id}
                    className="font-medium text-accent-text hover:underline"
                    href={`/work/${encodeURIComponent(other.task_id)}`}
                  >
                    {other.title || "Later task"}
                  </Link>
                ))}
              </dd>
            </>
          )}
          {item.parent_task_id && (
            <>
              <dt className="text-ink-3">Parent</dt>
              <dd>
                <Link
                  className="font-medium text-accent-text hover:underline"
                  href={`/work/${encodeURIComponent(item.parent_task_id)}`}
                >
                  Parent task
                </Link>
              </dd>
            </>
          )}
          <dt className="text-ink-3">Task ID</dt>
          <dd className="flex min-w-0 items-center gap-2">
            <code className="truncate font-mono text-caption text-ink-2">{item.task_id}</code>
            <button
              type="button"
              onClick={() => copyText(item.task_id, setCopiedTaskID)}
              className="inline-flex shrink-0 items-center gap-1 text-caption font-medium text-accent-text hover:underline"
              aria-label={`Copy Task ID ${item.task_id}`}
            >
              {copiedTaskID ? <Check size={12} aria-hidden /> : <Copy size={12} aria-hidden />}
              {copiedTaskID ? "Copied" : "Copy"}
            </button>
          </dd>
        </dl>
      </Section>
    </Screen>
  );
}
