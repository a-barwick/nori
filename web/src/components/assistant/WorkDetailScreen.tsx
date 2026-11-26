"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Square } from "lucide-react";
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
import { OperationRows, shownOperations } from "./WorkOperations";
import { statusPill } from "./WorkScreen";
import { Empty, Failure, Field, inputBase, inputClass, Loading, Notice, Pill, RefreshButton, Row, Rows, Screen, Section } from "./ui";

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
        className="flex flex-wrap items-end gap-3 rounded-card border border-hairline bg-surface p-4"
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
  const commands = runtime.taskCommands.filter((command) => command.command.run_id === taskID);
  const [draft, setDraft] = useState("");
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
  const toolNames = new Set(detail.operations.map((operation) => operation.name));
  const activity = (detail.events ?? []).filter((event) => !toolNames.has(event.text) && event.text !== "capability_load");
  const tooLong = messageTooLong(draft.trim());
  const sending = commands.some((command) => command.command.kind === "steer" && !command.error);
  const line =
    item.status === "running" && item.cancel_requested
      ? "Stopping."
      : item.status === "running"
        ? pendingInteractions.length
          ? undefined
          : item.progress || "Working on it."
        : item.status === "completed"
          ? item.operations.uncertain
            ? "Finished, but one action isn’t confirmed yet."
            : "Done."
          : item.status === "failed"
            ? item.error?.message || "This task failed."
            : "Stopped.";

  return (
    <Screen
      title={item.title}
      back="/work"
      backLabel="Work"
      line={line}
      actions={
        <>
          <Pill tone={pill.tone}>{pill.label}</Pill>
          <RefreshButton onClick={() => void reload()} />
        </>
      }
    >
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
            className="space-y-2.5 rounded-card border border-hairline bg-surface p-4"
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
              <Button disabled={stopping} onClick={() => void runtime.commandTask(taskID, "stop")}>
                <Square size={12} fill="currentColor" aria-hidden />
                {stopping ? "Stopping…" : "Stop task"}
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
          <div className="rounded-card border border-hairline bg-surface p-4 text-body [overflow-wrap:anywhere]">
            <Markdown variant="page">{detail.result}</Markdown>
          </div>
        </Section>
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

      {shownOperations(detail.operations).length > 0 && (
        <Section title="Actions audit">
          <OperationRows operations={shownOperations(detail.operations)} />
        </Section>
      )}

      {activity.length > 0 && (
        <Section title="Activity timeline">
          <ol className="space-y-3 border-l border-hairline pl-4">
            {activity.map((event, index) => (
              <li key={`${event.created_at}-${index}`} className="relative">
                <span
                  aria-hidden
                  className="absolute top-1.5 -left-[21px] size-2.5 rounded-full border-2 border-surface bg-hairline-strong"
                />
                <p className="text-caption text-ink-3">{when(event.created_at)}</p>
                <p className="text-body text-ink [overflow-wrap:anywhere]">{event.text}</p>
              </li>
            ))}
          </ol>
        </Section>
      )}

      <div className="mt-7 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-tile border border-hairline bg-fill-1 px-3.5 py-2.5 text-caption text-ink-2">
        <span>Started {when(item.started_at)}</span>
        {item.finished_at && (
          <>
            <span>·</span>
            <span>Finished {when(item.finished_at)}</span>
          </>
        )}
        <span>·</span>
        <span>
          From{" "}
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
        </span>
        <span>·</span>
        <span>
          Spend{" "}
          <code className="font-mono text-ink">
            {item.cost.status === "known"
              ? money(item.cost.amount_usd)
              : item.cost.status === "unknown"
                ? item.cost.amount_usd
                  ? `At least ${money(item.cost.amount_usd)}`
                  : "Unknown"
                : "None"}
          </code>
          {item.cost.budget_usd ? ` / ${money(item.cost.budget_usd)}` : ""}
        </span>
        {continuedBy.length > 0 && (
          <>
            <span>·</span>
            {continuedBy.map((other) => (
              <Link
                key={other.task_id}
                className="font-medium text-accent-text hover:underline"
                href={`/work/${encodeURIComponent(other.task_id)}`}
              >
                Continued by later task
              </Link>
            ))}
          </>
        )}
        {item.origin.continues_task_id && (
          <>
            <span>·</span>
            <Link
              className="font-medium text-accent-text hover:underline"
              href={`/work/${encodeURIComponent(item.origin.continues_task_id)}`}
            >
              Continues earlier task
            </Link>
          </>
        )}
        {item.parent_task_id && (
          <>
            <span>·</span>
            <Link
              className="font-medium text-accent-text hover:underline"
              href={`/work/${encodeURIComponent(item.parent_task_id)}`}
            >
              Parent task
            </Link>
          </>
        )}
      </div>
    </Screen>
  );
}
