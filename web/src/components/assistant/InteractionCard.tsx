"use client";

import Link from "next/link";
import { useState } from "react";
import { Check, KeyRound, Monitor, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, getJSON, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { EFFECT_LABEL, relative } from "@/lib/assistant/format";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { AccountSelectionPayload, ApprovalPayload, AuthAttempt, Connection, ComputerTakeoverPayload, Integration, Interaction, InteractionResponse, MCPServer, QuestionPayload, SignInPayload } from "@/lib/contracts/assistant";
import { MAX_MESSAGE_BYTES } from "@/lib/submission";
import { cn } from "@/lib/utils";
import { PasswordSignIn, startSignIn } from "./AppsScreen";
import { inputClass, Notice, Pill } from "./ui";

type Value = InteractionResponse["value"];

/** Allow only server-issued https authorize URLs or same-origin paths. */
export function safeAuthorizeURL(url: string) {
  if (url.startsWith("/") && !url.startsWith("//")) return url;
  try { return new URL(url).protocol === "https:" ? url : undefined; } catch { return undefined; }
}

/**
 * Answers one pending interaction. Before acting it re-reads the interaction; if the
 * task revision, action digest or status changed, it shows the current version instead.
 */
function useRespond(interaction: Interaction, onDone?: () => void) {
  const { runtime } = useAssistant();
  const action = useAction();
  const [current, setCurrent] = useState<Interaction>();
  const legacy = !!interaction.legacy_question_id && interaction.id.startsWith("legacy:");
  async function respond(value: Value, label: string) {
    await action.run(label, async () => {
      if (legacy) {
        if (value.kind !== "answer" || !interaction.task_id) throw new Error("This question takes a written answer.");
        await runtime.commandTask(interaction.task_id, "answer", value.text, interaction.legacy_question_id);
        return;
      }
      const latest = await getJSON<Interaction>(`/interactions/${encodeURIComponent(interaction.id)}`);
      if (latest.status !== "pending" || latest.revision !== interaction.revision || latest.action_digest !== interaction.action_digest || latest.instruction_revision !== interaction.instruction_revision) {
        setCurrent(latest);
        invalidate("/interactions", "/work", "/assistant/summary");
        throw new Error(latest.status === "pending" ? "This changed since it was shown. Review the current version." : "This was already answered or replaced.");
      }
      try {
        await mutate(`/interactions/${encodeURIComponent(interaction.id)}/responses`, { request_id: newRequestID(), expected_revision: interaction.revision, value });
      } catch (error) {
        if (error instanceof APIError && error.stale && error.body?.current) setCurrent(error.body.current as Interaction);
        throw error;
      }
      invalidate("/interactions", "/work", "/assistant/summary", "/inbox");
      onDone?.();
    }, { approve: "Approved.", decline: "Declined.", answer: "Answer sent.", choose_account: "Account chosen.", resume_computer: "Handed back to Nori." }[value.kind]);
  }
  return { respond, ...action, current, legacy };
}

export function InteractionCard({ interaction, compact }: { interaction: Interaction; compact?: boolean }) {
  const [shown, setShown] = useState(interaction);
  const resolved = shown.status !== "pending";
  return (
    <article aria-label={shown.prompt} className={cn("rounded-card border bg-surface p-4", resolved ? "border-hairline" : "border-hairline-strong shadow-[0_1px_0_var(--hairline)]")}>
      <div className="flex items-start gap-2">
        <h3 className="min-w-0 flex-1 text-row font-semibold text-ink">{shown.prompt}</h3>
        {resolved ? <Pill tone={shown.status === "resolved" ? "ok" : "neutral"}>{shown.status === "resolved" ? "Answered" : shown.status === "expired" ? "Expired" : "Replaced"}</Pill>
          : shown.expires_at ? <Pill tone="warning">Expires {relative(shown.expires_at)}</Pill> : null}
      </div>
      {!compact && shown.task_title && shown.task_id && <Link href={`/work/${encodeURIComponent(shown.task_id)}`} className="mt-0.5 inline-block text-meta text-ink-2 hover:underline">{shown.task_title}</Link>}
      <div className="mt-3">
        {resolved ? <Resolved interaction={shown} /> : <Body interaction={shown} onReplaced={setShown} />}
      </div>
    </article>
  );
}

function Resolved({ interaction }: { interaction: Interaction }) {
  const r = interaction.resolution;
  if (interaction.status === "superseded") return <p className="text-meta text-ink-2">A newer version replaced this.{interaction.superseded_by ? " It’s in Needs you." : ""}</p>;
  if (!r) return null;
  const text = r.kind === "approve" ? "Approved" : r.kind === "signed_in" ? "Signed in" : r.kind === "decline" ? (interaction.kind === "sign_in" ? "Not now" : "Declined") : r.kind === "choose_account" ? "Account chosen" : r.text ? `“${r.text}”` : "Answered";
  return <p className="flex items-center gap-1.5 text-meta text-ink-2"><Check size={14} aria-hidden />{text}{r.at ? ` · ${relative(r.at)}` : ""}</p>;
}

function Body({ interaction, onReplaced }: { interaction: Interaction; onReplaced: (next: Interaction) => void }) {
  const state = useRespond(interaction);
  const replaced = state.current && <Notice tone="warning" action={<Button size="sm" onClick={() => onReplaced(state.current!)}>Show current</Button>}>{state.error}</Notice>;
  const error = !state.current && state.error && <Notice tone="danger">{state.error}</Notice>;
  const extra = <div className="mt-3 space-y-2 empty:hidden">{replaced}{error}</div>;
  switch (interaction.kind) {
    case "question": return <><Question payload={interaction.payload} state={state} />{extra}</>;
    case "approval": return <><Approval interaction={interaction} payload={interaction.payload} state={state} />{extra}</>;
    case "account_selection": return <><AccountChoice payload={interaction.payload} state={state} />{extra}</>;
    case "sign_in": return <><SignIn interaction={interaction} payload={interaction.payload} state={state} />{extra}</>;
    case "computer_takeover": return <Takeover payload={interaction.payload} />;
  }
}

type State = ReturnType<typeof useRespond>;

function Question({ payload, state }: { payload: QuestionPayload; state: State }) {
  const [text, setText] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const busy = !!state.pending;
  if (payload.input === "confirm") return <div className="flex gap-2">
    <Button variant="primary" disabled={busy} onClick={() => void state.respond({ kind: "answer", text: "Yes" }, "yes")}>Yes</Button>
    <Button disabled={busy} onClick={() => void state.respond({ kind: "answer", text: "No" }, "no")}>No</Button>
  </div>;
  if ((payload.input === "choice" || payload.input === "multi_choice") && payload.options?.length) {
    const multi = payload.input === "multi_choice";
    return <form onSubmit={(event) => { event.preventDefault(); if (!picked.length) return; void state.respond({ kind: "answer", text: payload.options!.filter((o) => picked.includes(o.id)).map((o) => o.label).join(", "), option_ids: picked }, "answer"); }}>
      <fieldset className="space-y-1.5"><legend className="sr-only">Choose</legend>
        {payload.options.map((option) => <label key={option.id} className={cn("flex min-h-11 cursor-pointer items-center gap-3 rounded-tile border px-3 py-2 text-row", picked.includes(option.id) ? "border-accent bg-accent-subtle" : "border-hairline hover:bg-fill-1")}>
          <input type={multi ? "checkbox" : "radio"} name="option" className="accent-[var(--accent)]" checked={picked.includes(option.id)} onChange={() => setPicked(multi ? (picked.includes(option.id) ? picked.filter((id) => id !== option.id) : [...picked, option.id]) : [option.id])} />
          {option.label}
        </label>)}
      </fieldset>
      <Button type="submit" variant="primary" className="mt-3" disabled={busy || !picked.length}>{busy ? "Sending…" : "Send answer"}</Button>
    </form>;
  }
  return <form onSubmit={(event) => { event.preventDefault(); if (text.trim()) void state.respond({ kind: "answer", text: text.trim() }, "answer"); }} className="space-y-2">
    {payload.input === "date" ? <input type="date" value={text} onChange={(event) => setText(event.target.value)} className={inputClass} aria-label="Answer" />
      : <textarea value={text} onChange={(event) => setText(event.target.value)} rows={2} maxLength={payload.constraints?.max_length ?? MAX_MESSAGE_BYTES} className={cn(inputClass, "resize-y")} aria-label="Answer" placeholder="Your answer" />}
    <Button type="submit" variant="primary" disabled={busy || !text.trim()}>{busy ? "Sending…" : "Send answer"}</Button>
  </form>;
}

function Approval({ interaction, payload, state }: { interaction: Interaction; payload: ApprovalPayload; state: State }) {
  const busy = !!state.pending;
  const digest = interaction.action_digest!;
  return <div>
    <p className="text-body text-ink">{payload.summary}</p>
    <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-meta">
      <dt className="text-ink-3">Account</dt><dd className="text-ink">{payload.account.label}</dd>
      {!!payload.recipients?.length && <><dt className="text-ink-3">To</dt><dd className="text-ink [overflow-wrap:anywhere]">{payload.recipients.join(", ")}</dd></>}
      <dt className="text-ink-3">Effect</dt><dd><Pill tone={payload.effect === "sensitive_send" ? "warning" : "neutral"}>{EFFECT_LABEL[payload.effect]}</Pill></dd>
    </dl>
    {!!payload.material_effects?.length && <ul className="mt-3 space-y-1 text-meta text-ink-2">{payload.material_effects.map((effect) => <li key={effect} className="flex gap-2"><ShieldCheck size={14} className="mt-0.5 shrink-0 text-ink-3" aria-hidden />{effect}</li>)}</ul>}
    <div className="mt-4 flex flex-wrap gap-2">
      <Button variant="primary" disabled={busy} onClick={() => void state.respond({ kind: "approve", action_digest: digest }, "approve")}>{state.pending === "approve" ? "Approving…" : "Approve"}</Button>
      <Button disabled={busy} onClick={() => void state.respond({ kind: "decline", action_digest: digest }, "decline")}>{state.pending === "decline" ? "Declining…" : "Decline"}</Button>
    </div>
  </div>;
}

function AccountChoice({ payload, state }: { payload: AccountSelectionPayload; state: State }) {
  const { data } = useResource<{ items: Connection[] }>("/connections", { poll: 0 });
  const [remember, setRemember] = useState(!!payload.remember_as_default);
  const busy = !!state.pending;
  const eligible = payload.eligible_connection_ids.map((id) => data?.items.find((item) => item.id === id) ?? { id, title: id, account: { label: "" } } as Pick<Connection, "id" | "title" | "account">);
  return <div className="space-y-2">
    {eligible.map((connection) => <button key={connection.id} type="button" disabled={busy} onClick={() => void state.respond({ kind: "choose_account", connection_id: connection.id, remember_as_default: remember }, connection.id)}
      className="flex min-h-12 w-full items-center gap-3 rounded-tile border border-hairline px-3 py-2 text-left hover:bg-fill-1 disabled:opacity-60">
      <span className="min-w-0 flex-1"><span className="block text-row font-medium">{connection.title}</span>{connection.account?.label && <span className="block text-meta text-ink-2">{connection.account.label}</span>}</span>
      {state.pending === connection.id && <span className="text-meta text-ink-3">Choosing…</span>}
    </button>)}
    <label className="flex min-h-11 items-center gap-2 text-meta text-ink-2"><input type="checkbox" className="accent-[var(--accent)]" checked={remember} onChange={(event) => setRemember(event.target.checked)} />Use this account next time</label>
  </div>;
}

/**
 * Repair from the card (r2.11). The task resumes on its own once the account works again: the server resolves this
 * interaction as signed_in when the connection, or for integration "mcp" the MCP server, becomes usable, whether
 * the user signs in here or from Apps. "Not now" sends {kind: "decline"} with no action digest.
 */
function SignIn({ interaction, payload, state }: { interaction: Interaction; payload: SignInPayload; state: State }) {
  const mcp = payload.integration_id === "mcp";
  const integration = useResource<Integration>(mcp ? null : `/integrations/${encodeURIComponent(payload.integration_id)}`, { poll: 0 });
  const server = useResource<MCPServer>(mcp && payload.connection_id ? `/mcp/${encodeURIComponent(payload.connection_id)}` : null, { poll: 0 });
  const named = useResource<Connection>(!mcp && payload.connection_id ? `/connections/${encodeURIComponent(payload.connection_id)}` : null, { poll: 0 });
  const all = useResource<{ items: Connection[] }>(mcp || payload.connection_id ? null : "/connections", { poll: 0 });
  // Without a named connection, the integration's one signed-in account is the one to repair.
  const accounts = (all.data?.items ?? []).filter((item) => item.integration_id === payload.integration_id && item.account?.provider_id && item.status !== "disconnected");
  const connection = { data: named.data ?? (accounts.length === 1 ? accounts[0] : undefined) };
  const action = useAction();
  const [form, setForm] = useState(false);
  const [expired] = useState(() => !!payload.expires_at && Date.parse(payload.expires_at) < Date.now());
  const url = payload.auth === "oauth" && payload.authorize_url ? safeAuthorizeURL(payload.authorize_url) : null;
  const returnTo = interaction.task_id ? `/work/${encodeURIComponent(interaction.task_id)}` : mcp && payload.connection_id
    ? `/apps/mcp/${encodeURIComponent(payload.connection_id)}` : `/apps/${encodeURIComponent(payload.integration_id)}`;
  const signIn = () => {
    if (payload.auth === "app_password") { setForm(true); return; }
    if (url && !expired) { window.location.assign(url); return; }
    // The link expired: ask for a fresh one. An MCP server's sign-in returns to the server's page, not the task.
    if (mcp && server.data) void action.run("sign_in", async () => {
      const receipt = await mutate<MCPServer>(`/mcp/${encodeURIComponent(server.data!.id)}/actions`, { request_id: newRequestID(), expected_revision: server.data!.revision, value: { action: "sign_in" } }) as { auth?: AuthAttempt };
      const next = receipt.auth && safeAuthorizeURL(receipt.auth.authorize_url);
      if (!next) throw new Error("Nori didn’t return a sign-in link. Try again.");
      window.location.assign(next);
    });
    else if (!mcp && connection.data) void action.run("sign_in", () => startSignIn(connection.data!, "repair", returnTo));
    else void action.run("sign_in", async () => { throw new Error("This sign-in link expired. Sign in again from Apps; the task continues once the account works."); });
  };
  if (form && integration.data) return <PasswordSignIn integration={integration.data} connection={connection.data}
    onDone={() => { setForm(false); invalidate("/interactions", "/work", "/connections", "/assistant/summary"); }} />;
  const blocked = !!payload.authorize_url && !url;
  const title = mcp ? server.data?.name ?? "MCP server" : integration.data?.title ?? payload.integration_id;
  return <div className="space-y-3">
    <p className="text-meta text-ink-2">{[title, connection.data?.account?.label].filter(Boolean).join(" · ")}</p>
    {payload.authorize_url && !url && <Notice tone="danger">This sign-in link isn’t one Nori issued, so it won’t open.</Notice>}
    {action.error && <Notice tone="danger">{action.error}</Notice>}
    <div className="flex flex-wrap gap-2">
      <Button variant="primary" disabled={blocked || !!action.pending || state.pending === "decline"} onClick={signIn}><KeyRound size={15} aria-hidden />{action.pending ? "Opening sign-in…" : "Sign in"}</Button>
      <Button disabled={!!state.pending} onClick={() => void state.respond({ kind: "decline" }, "decline")}>{state.pending === "decline" ? "Declining…" : "Not now"}</Button>
    </div>
  </div>;
}

function Takeover({ payload }: { payload: ComputerTakeoverPayload }) {
  const reason = { sign_in: "A site needs you to sign in.", captcha: "A site wants a human check.", user_requested: "You asked to take over.", blocked: "Nori is stuck on a page." }[payload.reason];
  return <div>
    <p className="mb-3 text-meta text-ink-2">{reason}</p>
    <Link href={`/computer?session=${encodeURIComponent(payload.session_id)}`} className="inline-flex h-9 items-center gap-1.5 rounded-full bg-accent px-4 text-row-title font-medium text-on-accent hover:bg-accent-hover"><Monitor size={15} aria-hidden />Open computer</Link>
  </div>;
}
