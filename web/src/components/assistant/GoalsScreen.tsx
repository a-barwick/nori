"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Check, CheckCircle2, ChevronRight, Circle, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { day, relative } from "@/lib/assistant/format";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { Automation, Goal, GoalEditable, LibraryItem, WorkItem } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import {
  Confirm,
  Empty,
  Failure,
  Field,
  inputBase,
  inputClass,
  Loading,
  NoriLine,
  Notice,
  NotYet,
  Pill,
  RefreshButton,
  Row,
  Rows,
  Screen,
  Section,
  type Tone,
} from "./ui";

type List<T> = { items: T[] };
const STATUS: Record<Goal["status"], { tone: Tone; label: string }> = {
  active: { tone: "accent", label: "Active" },
  paused: { tone: "neutral", label: "Paused" },
  completed: { tone: "ok", label: "Done" },
  abandoned: { tone: "neutral", label: "Dropped" },
};

function met(goal: Goal) {
  const done = goal.completion_criteria.filter((criterion) => criterion.met).length;
  return `${done} of ${goal.completion_criteria.length}`;
}

/** Embeddable Goals view used inside Work -> Goals and standalone /goals. */
export function GoalsContent() {
  const { has } = useAssistant();
  const list = useResource<List<Goal>>(has("goals") ? "/goals" : null, { poll: 30_000 });
  if (!has("goals")) return <NotYet what="Goals" />;

  const items = list.data?.items ?? [];
  const open = items.filter((goal) => goal.status === "active" || goal.status === "paused");
  const closed = items.filter((goal) => goal.status === "completed" || goal.status === "abandoned");
  const blocked = open.filter((goal) => goal.blockers.length);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        {blocked.length ? (
          <NoriLine>
            {blocked.length === 1 ? "One goal is waiting on input." : `${blocked.length} goals are waiting on input.`}
          </NoriLine>
        ) : (
          <div />
        )}
        <Link
          href="/goals/new"
          className="inline-flex h-8 items-center gap-1.5 rounded-full bg-accent px-3.5 text-meta font-semibold text-on-accent transition-colors hover:bg-accent-hover"
        >
          <Plus size={14} aria-hidden />
          New goal
        </Link>
      </div>

      {list.error && <Failure error={list.error} onRetry={() => void list.reload()} />}

      {list.loading ? (
        <Loading />
      ) : !items.length ? (
        <Empty
          title="No goals yet"
          action={
            <Link
              href="/goals/new"
              className="inline-flex h-9 items-center gap-1.5 rounded-full bg-accent px-4 text-meta font-semibold text-on-accent hover:bg-accent-hover"
            >
              <Plus size={14} aria-hidden />
              New goal
            </Link>
          }
        >
          Tell Nori what you want done by when, and it tracks criteria and blockers here.
        </Empty>
      ) : (
        <>
          {open.length > 0 && (
            <div className="space-y-3.5">
              {open.map((goal) => {
                const doneCount = goal.completion_criteria.filter((c) => c.met).length;
                const total = goal.completion_criteria.length;
                const pct = total > 0 ? Math.round((doneCount / total) * 100) : 0;
                return (
                  <article key={goal.id} className="rounded-card border border-hairline bg-surface p-4">
                    <div className="flex items-start justify-between gap-3">
                      <Link
                        href={`/goals/${encodeURIComponent(goal.id)}`}
                        className="text-[15.5px] font-bold text-ink hover:text-accent-text"
                      >
                        {goal.title}
                      </Link>
                      <Pill tone={goal.blockers.length ? "warning" : STATUS[goal.status].tone}>
                        {goal.blockers.length ? "Blocked" : STATUS[goal.status].label}
                      </Pill>
                    </div>

                    {total > 0 && (
                      <div className="mt-3">
                        <div className="h-[4.5px] w-full overflow-hidden rounded-full bg-fill-2">
                          <div
                            className={cn("h-full rounded-full transition-[width]", pct === 100 ? "bg-ok" : "bg-accent")}
                            style={{ width: `${pct}%` }}
                          />
                        </div>
                        <p className="mt-1.5 text-caption text-ink-2">
                          {doneCount} of {total} criteria met
                          {goal.deadline ? ` · Due ${day(goal.deadline)}` : ""}
                        </p>
                      </div>
                    )}

                    {goal.blockers.map((blocker, idx) => (
                      <Link
                        key={idx}
                        href={blocker.interaction_id ? "/work?tab=tasks" : blocker.connection_id ? "/apps" : `/goals/${encodeURIComponent(goal.id)}`}
                        className="mt-3 flex items-center justify-between gap-3 rounded-[14px] border border-lavender-border bg-wash-top px-3 py-2.5 text-[12.5px] transition-colors hover:bg-accent-subtle"
                      >
                        <span className="min-w-0">
                          <span className="font-semibold text-accent-text">Blocked: </span>
                          <span className="text-ink">{blocker.text}</span>
                        </span>
                        <span className="inline-flex shrink-0 items-center gap-0.5 rounded-full border border-lavender-border bg-surface px-2.5 py-1 text-[12px] font-semibold text-accent-text">
                          Review
                          <ChevronRight size={12} aria-hidden />
                        </span>
                      </Link>
                    ))}

                    {total > 0 && (
                      <ul className="mt-3 space-y-1.5">
                        {goal.completion_criteria.slice(0, 4).map((criterion) => (
                          <li
                            key={criterion.id}
                            className={cn(
                              "flex items-start gap-2 text-[13.5px]",
                              criterion.met ? "text-ink-2 line-through decoration-ink-3" : "text-ink",
                            )}
                          >
                            {criterion.met ? (
                              <CheckCircle2 size={15} className="mt-0.5 shrink-0 text-ok" aria-hidden />
                            ) : (
                              <Circle size={15} className="mt-0.5 shrink-0 text-hairline-strong" aria-hidden />
                            )}
                            <span>{criterion.text}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </article>
                );
              })}
            </div>
          )}

          {closed.length > 0 && (
            <Section title="Closed">
              <Rows>
                {closed.map((goal) => (
                  <Row
                    key={goal.id}
                    href={`/goals/${encodeURIComponent(goal.id)}`}
                    title={goal.title}
                    detail={goal.completed_at ? `Finished ${relative(goal.completed_at)}` : undefined}
                    trailing={<Pill tone={STATUS[goal.status].tone}>{STATUS[goal.status].label}</Pill>}
                  />
                ))}
              </Rows>
            </Section>
          )}
        </>
      )}
    </div>
  );
}

export function GoalsScreen() {
  return (
    <Screen title="Goals" back="/work?tab=goals" backLabel="Work">
      <GoalsContent />
    </Screen>
  );
}

export function GoalDetailScreen({ id }: { id: string }) {
  const { has } = useAssistant();
  const goal = useResource<Goal>(`/goals/${encodeURIComponent(id)}`, { poll: 20_000 });
  const work = useResource<List<WorkItem>>(has("work") ? "/work" : null, { poll: 0 });
  const automations = useResource<List<Automation>>(has("automations") ? "/automations" : null, { poll: 0 });
  const library = useResource<List<LibraryItem>>(has("library") ? "/library" : null, { poll: 0 });
  const action = useAction();
  const [keepMonitors, setKeepMonitors] = useState(false);
  const item = goal.data;

  if (goal.loading) {
    return (
      <Screen title="Goal" back="/work?tab=goals" backLabel="Work">
        <Loading />
      </Screen>
    );
  }
  if (!item) {
    return (
      <Screen title="Goal" back="/work?tab=goals" backLabel="Work">
        <Failure error={goal.error ?? "This goal doesn’t exist."} />
      </Screen>
    );
  }

  const act = (value: Record<string, unknown>, label: string) =>
    action.run(
      label,
      async () => {
        try {
          const receipt = await mutate<Goal>(`/goals/${encodeURIComponent(id)}/actions`, {
            request_id: newRequestID(),
            expected_revision: item.revision,
            value,
          });
          goal.apply(receipt.resource as Goal);
          invalidate("/goals", "/automations");
        } catch (error) {
          if (error instanceof APIError && error.stale && error.body?.current) {
            goal.apply(error.body.current as Goal);
          }
          throw error;
        }
      },
      ({
        pause: "Goal paused.",
        resume: "Goal resumed.",
        complete: "Goal marked done.",
        abandon: "Goal dropped.",
      } as Record<string, string>)[value.action as string],
    );

  const open = item.status === "active" || item.status === "paused";
  const allMet = item.completion_criteria.every((criterion) => criterion.met);
  const tasks = item.linked.task_ids.map(
    (taskID) => work.data?.items.find((task) => task.task_id === taskID) ?? ({ task_id: taskID, title: "Task" } as Pick<WorkItem, "task_id" | "title">),
  );
  const routines = item.linked.automation_ids.map(
    (routineID) =>
      automations.data?.items.find((routine) => routine.id === routineID) ??
      ({ id: routineID, title: "Routine" } as Pick<Automation, "id" | "title">),
  );
  const results = item.linked.result_ids.map(
    (resultID) =>
      library.data?.items.find((result) => result.id === resultID) ??
      ({ id: resultID, title: "Saved result" } as Pick<LibraryItem, "id" | "title">),
  );

  return (
    <Screen
      title={item.title}
      back="/work?tab=goals"
      backLabel="Work"
      line={item.progress || undefined}
      actions={
        <>
          <Pill tone={STATUS[item.status].tone}>{STATUS[item.status].label}</Pill>
          <RefreshButton onClick={() => void goal.reload()} />
        </>
      }
    >
      <p className="text-body text-ink">
        {item.outcome}
        {item.deadline ? <span className="text-ink-2"> By {day(item.deadline)}.</span> : null}
      </p>
      {action.error && (
        <div className="mt-4">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}
      {item.blockers.length > 0 && (
        <Section title="Blocked by">
          <Rows>
            {item.blockers.map((blocker, index) => (
              <Row
                key={index}
                title={blocker.text}
                href={blocker.interaction_id ? "/work?tab=tasks" : blocker.connection_id ? "/apps" : undefined}
                trailing={
                  blocker.interaction_id ? (
                    <span className="text-meta font-semibold text-accent-text">Review</span>
                  ) : blocker.connection_id ? (
                    <span className="text-meta font-semibold text-accent-text">Fix</span>
                  ) : undefined
                }
                chevron={false}
              />
            ))}
          </Rows>
        </Section>
      )}
      <Section title={`Done when (${met(item)})`}>
        <ul className="space-y-1">
          {item.completion_criteria.map((criterion) => (
            <li key={criterion.id} className="flex min-h-10 items-start gap-3 py-1">
              {criterion.met ? (
                <Check size={18} className="mt-0.5 text-ok" aria-label="Met" />
              ) : (
                <Circle size={18} className="mt-0.5 text-ink-3" aria-label="Not met" />
              )}
              <span className={cn("text-body", criterion.met && "text-ink-2")}>
                {criterion.text}
                {criterion.evidence_result_ids?.map((resultID) => (
                  <Link
                    key={resultID}
                    href={`/library/${encodeURIComponent(resultID)}`}
                    className="ml-2 text-meta text-accent-text hover:underline"
                  >
                    evidence
                  </Link>
                ))}
              </span>
            </li>
          ))}
        </ul>
      </Section>
      {item.next_actions.length > 0 && (
        <Section title="Next">
          <ul className="list-disc space-y-1 pl-5 text-body">
            {item.next_actions.map((next) => (
              <li key={next}>{next}</li>
            ))}
          </ul>
        </Section>
      )}
      {(tasks.length > 0 || routines.length > 0 || results.length > 0) && (
        <Section title="Linked">
          <Rows>
            {tasks.map((task) => (
              <Row key={task.task_id} href={`/work/${encodeURIComponent(task.task_id)}`} title={task.title} detail="Task" />
            ))}
            {routines.map((routine) => (
              <Row
                key={routine.id}
                href={`/routines/${encodeURIComponent(routine.id)}`}
                title={routine.title}
                detail={item.follow_up === "monitor" ? "Checks for changes" : "Routine"}
              />
            ))}
            {results.map((result) => (
              <Row key={result.id} href={`/library/${encodeURIComponent(result.id)}`} title={result.title} detail="Artifact" />
            ))}
          </Rows>
        </Section>
      )}
      {open && (
        <Section title="Manage">
          <div className="flex flex-wrap items-center gap-2">
            <Link
              href={`/goals/${encodeURIComponent(id)}/edit`}
              className="inline-flex h-9 items-center rounded-full px-4 text-row-title font-medium shadow-[inset_0_0_0_1px_var(--hairline-strong)] hover:bg-fill-1"
            >
              Edit
            </Link>
            <Button disabled={!!action.pending} onClick={() => void act({ action: item.status === "active" ? "pause" : "resume" }, "toggle")}>
              {item.status === "active" ? "Pause" : "Resume"}
            </Button>
            <Confirm
              title="Mark this goal done?"
              confirm="Mark done"
              onConfirm={() => void act({ action: "complete", keep_monitors: keepMonitors }, "complete")}
              trigger={
                <Button variant={allMet ? "primary" : "neutral"} disabled={!!action.pending}>
                  Mark done
                </Button>
              }
              body={
                <div className="space-y-3">
                  {!allMet && <p>Some criteria aren’t met yet.</p>}
                  {item.linked.automation_ids.length > 0 && (
                    <label className="flex items-start gap-2 text-ink">
                      <input
                        type="checkbox"
                        className="mt-1 accent-[var(--accent)]"
                        checked={keepMonitors}
                        onChange={(event) => setKeepMonitors(event.target.checked)}
                      />
                      Keep its routines running
                    </label>
                  )}
                  {!item.linked.automation_ids.length && allMet && <p>Nori stops following up.</p>}
                </div>
              }
            />
            <Confirm
              danger
              title="Drop this goal?"
              confirm="Drop goal"
              body="Nori stops following up and its routines stop. Saved results stay in Artifacts."
              onConfirm={() => void act({ action: "abandon" }, "abandon")}
              trigger={
                <Button variant="quiet" className="text-danger!" disabled={!!action.pending}>
                  Drop
                </Button>
              }
            />
          </div>
        </Section>
      )}
    </Screen>
  );
}

export function GoalEditorScreen({ id }: { id?: string }) {
  const { has } = useAssistant();
  const existing = useResource<Goal>(id ? `/goals/${encodeURIComponent(id)}` : null, { poll: 0 });
  if (!has("goals")) {
    return (
      <Screen title="New goal" back="/work?tab=goals" backLabel="Work">
        <NotYet what="Goals" />
      </Screen>
    );
  }
  if (id && !existing.data) {
    return (
      <Screen title="Edit goal" back={`/goals/${id}`}>
        {existing.error ? <Failure error={existing.error} /> : <Loading />}
      </Screen>
    );
  }
  return <GoalEditor key={existing.data?.revision ?? "new"} existing={existing.data} />;
}

function GoalEditor({ existing }: { existing?: Goal }) {
  const router = useRouter();
  const action = useAction();
  const [title, setTitle] = useState(existing?.title ?? "");
  const [outcome, setOutcome] = useState(existing?.outcome ?? "");
  const [criteria, setCriteria] = useState<{ id?: string; text: string }[]>(
    existing?.completion_criteria.map(({ id, text }) => ({ id, text })) ?? [{ text: "" }],
  );
  const [deadline, setDeadline] = useState(existing?.deadline?.slice(0, 10) ?? "");
  const [followUp, setFollowUp] = useState<NonNullable<GoalEditable["follow_up"]>>(existing?.follow_up ?? "remind");

  const save = () =>
    action.run("save", async () => {
      const value: GoalEditable = {
        title: title.trim(),
        outcome: outcome.trim(),
        completion_criteria: criteria
          .filter((criterion) => criterion.text.trim())
          .map((criterion) => ({ ...criterion, text: criterion.text.trim() })),
        follow_up: followUp,
        ...(deadline ? { deadline: new Date(`${deadline}T23:59:59`).toISOString() } : {}),
      };
      const receipt = existing
        ? await mutate<Goal>(
            `/goals/${encodeURIComponent(existing.id)}`,
            { request_id: newRequestID(), expected_revision: existing.revision, value },
            "PUT",
          )
        : await mutate<Goal>("/goals", { request_id: newRequestID(), expected_revision: 0, value });
      invalidate("/goals");
      router.push(`/goals/${encodeURIComponent((receipt.resource as Goal).id)}`);
    });

  return (
    <Screen title={existing ? "Edit goal" : "New goal"} back={existing ? `/goals/${existing.id}` : "/work?tab=goals"} backLabel={existing ? "Goal" : "Work"}>
      <form
        className="space-y-6"
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <Field label="Goal">
          <input value={title} onChange={(event) => setTitle(event.target.value)} className={inputClass} placeholder="Book the dentist" required />
        </Field>
        <Field label="Outcome">
          <textarea
            value={outcome}
            onChange={(event) => setOutcome(event.target.value)}
            rows={2}
            className={cn(inputClass, "resize-y")}
            placeholder="An appointment is on my calendar before December."
            required
          />
        </Field>
        <fieldset>
          <legend className="mb-1.5 text-meta font-medium">Done when</legend>
          <ul className="space-y-2">
            {criteria.map((criterion, index) => (
              <li key={index} className="flex gap-2">
                <input
                  aria-label={`Criterion ${index + 1}`}
                  value={criterion.text}
                  onChange={(event) =>
                    setCriteria(criteria.map((other, n) => (n === index ? { ...other, text: event.target.value } : other)))
                  }
                  className={inputClass}
                  placeholder="Appointment exists on my calendar"
                />
                {criteria.length > 1 && (
                  <Button
                    variant="quiet"
                    aria-label={`Remove criterion ${index + 1}`}
                    onClick={() => setCriteria(criteria.filter((_, n) => n !== index))}
                  >
                    <Trash2 size={15} aria-hidden />
                  </Button>
                )}
              </li>
            ))}
          </ul>
          <Button size="sm" variant="quiet" className="mt-2" onClick={() => setCriteria([...criteria, { text: "" }])}>
            <Plus size={14} aria-hidden />
            Add
          </Button>
        </fieldset>
        <div className="flex flex-wrap gap-4">
          <Field label="By (optional)">
            <input type="date" value={deadline} onChange={(event) => setDeadline(event.target.value)} className={cn(inputBase, "w-auto")} />
          </Field>
          <Field label="Follow-up">
            <select
              value={followUp}
              onChange={(event) => setFollowUp(event.target.value as typeof followUp)}
              className={cn(inputBase, "w-auto")}
            >
              <option value="remind">Remind me</option>
              <option value="monitor">Watch for changes</option>
              <option value="none">Nothing</option>
            </select>
          </Field>
        </div>
        {action.error && <Notice tone="danger">{action.error}</Notice>}
        <div className="flex gap-2">
          <Button
            type="submit"
            variant="primary"
            disabled={!!action.pending || !title.trim() || !outcome.trim() || !criteria.some((criterion) => criterion.text.trim())}
          >
            {action.pending ? "Saving…" : existing ? "Save changes" : "Create goal"}
          </Button>
          <Button variant="quiet" onClick={() => router.back()}>
            Cancel
          </Button>
        </div>
      </form>
    </Screen>
  );
}
