"use client";

import { useSearchParams } from "next/navigation";
import { useCallback, useEffect, useState } from "react";
import { Hand, Play } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, assistantURL, getJSON, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { relative } from "@/lib/assistant/format";
import { useWork } from "@/lib/assistant/sources";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { ComputerSession, WorkDetail } from "@/lib/contracts/assistant";
import { runtimePath } from "@/lib/runtime/paths";
import { useBlobURL } from "@/lib/useBlobURL";
import { Empty, Loading, Notice, NotYet, Pill, Row, Rows, Screen, Section, type Tone } from "./ui";

type List<T> = { items: T[] };
const AVAILABILITY: Record<ComputerSession["availability"], { tone: Tone; label: string }> = {
  stopped: { tone: "neutral", label: "Off" }, starting: { tone: "neutral", label: "Starting" }, ready: { tone: "ok", label: "Ready" },
  busy: { tone: "accent", label: "In use" }, stopping: { tone: "neutral", label: "Stopping" }, failed: { tone: "danger", label: "Failed" },
};
const BLOCKED: Record<string, string> = { sign_in: "A site needs you to sign in.", captcha: "A site wants a human check.", user_requested: "You asked to take over.", blocked: "Nori is stuck on a page." };

export function ComputerScreen() {
  const { has } = useAssistant();
  const params = useSearchParams();
  const sessions = useResource<List<ComputerSession>>(has("computer_sessions") ? "/computer/sessions" : null, { poll: 5_000 });
  if (!has("computer_sessions")) return <Screen title="Computer" back="/work"><NotYet what="Watching and taking over Nori’s computer" /></Screen>;
  if (sessions.loading) return <Screen title="Computer" back="/work"><Loading /></Screen>;
  const items = sessions.data?.items ?? [];
  const session = items.find((item) => item.id === params.get("session")) ?? items[0];
  if (!session) return <Screen title="Computer" back="/work"><Empty>Nori’s computer is off. It starts when a task needs a browser.</Empty></Screen>;
  return <Session key={session.id} initial={session} />;
}

function Session({ initial }: { initial: ComputerSession }) {
  const live = useResource<ComputerSession>(`/computer/sessions/${encodeURIComponent(initial.id)}`, { poll: 3_000 });
  const session = live.data ?? initial;
  const action = useAction();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 5_000); return () => clearInterval(timer); }, []);
  const mine = session.controller === "user" && session.lease?.holder === "user";
  // During a takeover the core moves the task that was using the computer into the waiting list.
  const handBackTo = session.task_id ?? session.waiting_task_ids?.[0];
  const work = useWork();
  const act = (value: Record<string, unknown>, label: string) => action.run(label, async () => {
    try {
      const receipt = await mutate<ComputerSession>(`/computer/sessions/${encodeURIComponent(session.id)}/actions`, { request_id: newRequestID(), expected_revision: session.revision, value });
      live.apply(receipt.resource as ComputerSession); void live.reload(); invalidate("/computer", "/work", "/interactions", "/assistant/summary");
    } catch (error) {
      if (error instanceof APIError && error.stale && error.body?.current) live.apply(error.body.current as ComputerSession);
      throw error;
    }
  });
  const resume = () => action.run("resume", async () => {
    if (!handBackTo || !session.lease) throw new Error("There’s no task waiting for this computer.");
    // Resume names the exact task revision; a stale lease or a changed task is rejected by the server.
    const detail = await getJSON<WorkDetail>(`/work/${encodeURIComponent(handBackTo)}`);
    const receipt = await mutate<ComputerSession>(`/computer/sessions/${encodeURIComponent(session.id)}/actions`, { request_id: newRequestID(), expected_revision: session.revision, value: { action: "resume", lease_id: session.lease.id, task_id: handBackTo, instruction_revision: detail.item.instruction_revision } });
    live.apply(receipt.resource as ComputerSession); void live.reload(); invalidate("/computer", "/work", "/interactions", "/assistant/summary");
  }, "Handed back to Nori.");
  const state = AVAILABILITY[session.availability] ?? { tone: "neutral" as Tone, label: session.controller === "user" ? "Yours" : "Unknown" };
  const expiresIn = session.lease ? Date.parse(session.lease.expires_at) - now : 0;
  const line = mine ? `You have control${session.lease ? `, ${relative(session.lease.expires_at).replace(/^in /, "for ")}` : ""}. Nori won’t touch the screen until you hand it back.`
    : session.controller === "agent" ? "Nori is using the computer." : session.blocked_reason ? BLOCKED[session.blocked_reason] ?? session.blocked_reason : "Nobody is using the computer.";
  return (
    <Screen title="Computer" back="/work" line={line} wide actions={<Pill tone={state.tone}>{state.label}</Pill>}>
      {action.error && <div className="mb-4"><Notice tone="danger">{action.error}</Notice></div>}
      <Screenshot session={session} />
      <div className="mt-4 flex flex-wrap items-center gap-2">
        {!mine && <Button variant="primary" disabled={!!action.pending || session.availability !== "ready" && session.availability !== "busy"} onClick={() => void act({ action: "take_over", reason: session.blocked_reason ?? "user_requested" }, "take")}><Hand size={15} aria-hidden />{action.pending === "take" ? "Taking over…" : "Take over"}</Button>}
        {mine && handBackTo && <Button variant="primary" disabled={!!action.pending} onClick={() => void resume()}><Play size={14} aria-hidden />{action.pending === "resume" ? "Handing back…" : "Done, let Nori continue"}</Button>}
        {mine && session.lease && <Button disabled={!!action.pending} onClick={() => void act({ action: "renew", lease_id: session.lease!.id }, "renew")}>More time</Button>}
        {mine && session.lease && <Button variant="quiet" disabled={!!action.pending} onClick={() => void act({ action: "release", lease_id: session.lease!.id }, "release")}>Give back without resuming</Button>}
        {mine && expiresIn > 0 && expiresIn < 120_000 && <span role="status" className="text-meta text-warning">Your control ends {relative(session.lease!.expires_at)}.</span>}
      </div>
      {session.task_id && <Section title="For"><Rows><Row href={`/work/${encodeURIComponent(session.task_id)}`} title={session.task_title ?? "The current task"} /></Rows></Section>}
      {!!session.waiting_task_ids?.length && <Section title="Waiting for the computer"><Rows>{session.waiting_task_ids.map((taskID) => <Row key={taskID} href={`/work/${encodeURIComponent(taskID)}`} title={work.items?.find((item) => item.task_id === taskID)?.title ?? "A task"} detail={mine ? "Continues when you hand the computer back" : "Continues when the computer is free"} />)}</Rows></Section>}
    </Screen>
  );
}

/**
 * The live screen when the core serves one (screen_url); otherwise the newest screenshot the
 * session's task captured, read through the existing task screenshot route.
 */
function Screenshot({ session }: { session: ComputerSession }) {
  const { runtime, agentMode } = useAssistant();
  const [frame, setFrame] = useState(0);
  useEffect(() => { const timer = setInterval(() => setFrame((value) => value + 1), 4_000); return () => clearInterval(timer); }, []);
  const taskID = session.task_id ?? session.waiting_task_ids?.[0];
  const run = taskID ? runtime.session?.runs.find((candidate) => candidate.id === taskID) : undefined;
  const artifact = run?.artifacts?.at(-1);
  const path = session.screen_url?.replace(/^\/api\/v1/, "") ?? "";
  const load = useCallback(async (signal: AbortSignal) => {
    const response = path
      ? await fetch(`${assistantURL(path)}${path.includes("?") ? "&" : "?"}frame=${frame}`, { cache: "no-store", signal })
      : await fetch(runtimePath(agentMode, "artifact"), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ run_id: run!.id, artifact_id: artifact!.id }), signal });
    if (!response.ok || !response.headers.get("content-type")?.startsWith("image/")) throw new Error("No screen yet.");
    return response.blob();
  }, [path, frame, agentMode, run, artifact]);
  const image = useBlobURL(session.availability !== "stopped" && (path || (run && artifact)) ? load : undefined);
  const empty = session.availability === "stopped" ? "The computer is off." : taskID ? "No screenshot from this task yet." : "Nothing is open on the computer.";
  return <figure className="grid aspect-[16/10] w-full place-items-center overflow-hidden rounded-card border border-hairline bg-fill-1">
    {/* eslint-disable-next-line @next/next/no-img-element -- blob URL */}
    {image?.url ? <img src={image.url} alt={path ? "Nori’s computer screen" : "The latest screenshot from this task"} className="h-full w-full object-contain" /> : <span className="text-meta text-ink-3">{empty}</span>}
  </figure>;
}

