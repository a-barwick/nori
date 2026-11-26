"use client";

import Link from "next/link";
import { useState } from "react";
import { AlertTriangle, Check, ChevronRight, Clock, KeyRound, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { EFFECT_LABEL, relative } from "@/lib/assistant/format";
import {
  buildApprovalResponse,
  formatInteractionExpiry,
  resolvedStatusPill,
  resolvedSummaryText,
  resolveInteractionLocally,
  responseAnnouncement,
  submitInteractionResponse,
} from "@/lib/assistant/interactions";
import { useResourceInvalidation, useAction, useResource } from "@/lib/assistant/useResource";
import type {
  AccountSelectionPayload,
  ApprovalPayload,
  AuthAttempt,
  Connection,
  Integration,
  Interaction,
  InteractionResponse,
  MCPServer,
  QuestionPayload,
  SignInPayload,
} from "@/lib/contracts/assistant";
import { MAX_MESSAGE_BYTES } from "@/lib/submission";
import { useHydrated } from "@/lib/hooks";
import "@/lib/desktop";
import { cn } from "@/lib/utils";
import { startSignIn } from "./AppsScreen";
import { inputClass, Notice, Pill } from "./ui";

type Value = InteractionResponse["value"];
export type OnInteractionResolved = (resolved: Interaction, meta?: { rememberRule?: boolean }) => void;

export function safeAuthorizeURL(url: string) {
  if (url.startsWith("/") && !url.startsWith("//") && !url.startsWith("/\\") && !url.includes("\\")) return url;
  try {
    return new URL(url).protocol === "https:" ? url : undefined;
  } catch {
    return undefined;
  }
}

function useRespond(interaction: Interaction, onResolved?: OnInteractionResolved) {
  const { runtime } = useAssistant();
  const invalidate = useResourceInvalidation();
  const action = useAction();
  const [current, setCurrent] = useState<Interaction>();
  const legacy = !!interaction.legacy_question_id && interaction.id.startsWith("legacy:");
  async function respond(value: Value, label: string) {
    await action.run(
      label,
      async () => {
        if (legacy) {
          if (value.kind !== "answer" || !interaction.task_id) throw new Error("This question takes a written answer.");
          await runtime.commandTask(interaction.task_id, "answer", value.text, interaction.legacy_question_id);
          onResolved?.(resolveInteractionLocally(interaction, value));
          return;
        }
        try {
          const result = await submitInteractionResponse(interaction, value, invalidate);
          if (!result.ok) {
            setCurrent(result.current);
            throw new Error(result.message);
          }
          onResolved?.(result.resolved, { rememberRule: result.rememberRule });
        } catch (error) {
          if (error instanceof APIError && error.stale && error.body?.current) {
            setCurrent(error.body.current as Interaction);
          }
          throw error;
        }
      },
      responseAnnouncement(value),
    );
  }
  return { respond, ...action, current, legacy };
}

function isInternalScheduleApproval(payload: Partial<ApprovalPayload>) {
  return !!payload.tool_id?.startsWith("nori/") && !payload.account?.connection_id && !payload.account?.label?.trim();
}

function heading(interaction: Interaction) {
  const payload = interaction.payload as Partial<ApprovalPayload>;
  if (interaction.kind !== "approval" || interaction.prompt !== payload.summary) return interaction.prompt;
  if (payload.tool_id === "nori/assistant/automation-create") return "Create this routine?";
  return (
    ({
      sensitive_send: "Send this?",
      reversible_write: "Make this change?",
      read: "Allow this?",
    } as Record<string, string>)[payload.effect ?? ""] ?? "Approve this action?"
  );
}

export function InteractionCard({
  interaction,
  compact,
  inline,
  rememberedRule,
  onResolved,
}: {
  interaction: Interaction;
  compact?: boolean;
  inline?: boolean;
  rememberedRule?: boolean;
  onResolved?: OnInteractionResolved;
}) {
  const [override, setOverride] = useState<{ interaction: Interaction; rememberedRule?: boolean }>();
  const useOverride =
    override && override.interaction.id === interaction.id && override.interaction.revision >= interaction.revision;
  const shown = useOverride ? override.interaction : interaction;
  const effectiveRememberedRule = rememberedRule ?? (useOverride ? override.rememberedRule : undefined);
  const resolved = shown.status !== "pending";
  const expiryLabel = !resolved ? formatInteractionExpiry(shown.expires_at) : undefined;
  const statusPill = resolved ? resolvedStatusPill(shown) : undefined;

  const handleResolved: OnInteractionResolved = (next, meta) => {
    setOverride({ interaction: next, rememberedRule: meta?.rememberRule });
    onResolved?.(next, meta);
  };

  return (
    <article
      aria-label={heading(shown)}
      data-interaction-id={shown.id}
      data-interaction-status={shown.status}
      className={cn(
        "rounded-[20px] transition-[border-color,background-color]",
        resolved
          ? "border border-card-border bg-sidebar px-4 py-3"
          : "border border-card-border bg-surface p-4 shadow-card",
      )}
    >
      <div className="flex items-start gap-2.5">
        <div className="min-w-0 flex-1">
          <h3 className={cn("font-bold text-ink", inline ? "text-[15px] leading-snug" : "text-[15.5px] leading-snug")}>
            {heading(shown)}
          </h3>
          {!compact && shown.task_title && shown.task_id && (
            <Link
              href={`/work/${encodeURIComponent(shown.task_id)}`}
              className="mt-1 inline-flex items-center gap-0.5 text-[12.5px] font-semibold text-accent-text hover:underline"
            >
              <span>{shown.task_title}</span>
              <ChevronRight size={13} aria-hidden />
            </Link>
          )}
        </div>
        {statusPill ? (
          <Pill tone={statusPill.tone}>{statusPill.label}</Pill>
        ) : expiryLabel ? (
          <Pill tone="warning">
            <Clock size={12} className="mr-1 shrink-0" aria-hidden />
            {expiryLabel}
          </Pill>
        ) : (
          <Pill tone="accent">Waiting</Pill>
        )}
      </div>

      <div className={cn(inline && resolved ? "mt-2" : "mt-3")}>
        {resolved ? (
          <Resolved interaction={shown} rememberedRule={effectiveRememberedRule} />
        ) : (
          <Body
            interaction={shown}
            inline={inline}
            onReplaced={(next) => setOverride({ interaction: next })}
            onResolved={handleResolved}
          />
        )}
      </div>
    </article>
  );
}

function Resolved({ interaction, rememberedRule }: { interaction: Interaction; rememberedRule?: boolean }) {
  const r = interaction.resolution;
  if (interaction.status === "superseded") {
    return (
      <p className="text-meta text-ink-2">
        A newer version replaced this.{interaction.superseded_by ? " Review it in Work." : ""}
      </p>
    );
  }
  if (!r) return null;
  const text = resolvedSummaryText(interaction, rememberedRule);
  return (
    <p className="flex items-center gap-1.5 text-meta text-ink-2">
      <Check size={14} className="text-ok" aria-hidden />
      <span>
        {text}
        {r.at ? ` · ${relative(r.at)}` : ""}
      </span>
    </p>
  );
}

function Body({
  interaction,
  inline,
  onReplaced,
  onResolved,
}: {
  interaction: Interaction;
  inline?: boolean;
  onReplaced: (next: Interaction) => void;
  onResolved: OnInteractionResolved;
}) {
  const state = useRespond(interaction, onResolved);
  const replaced = state.current && (
    <Notice tone="warning" action={<Button size="sm" onClick={() => onReplaced(state.current!)}>Show current</Button>}>
      {state.error}
    </Notice>
  );
  const error = !state.current && state.error && <Notice tone="danger">{state.error}</Notice>;
  const extra = (
    <div className="mt-3 space-y-2 empty:hidden">
      {replaced}
      {error}
    </div>
  );
  switch (interaction.kind) {
    case "question":
      return (
        <>
          <Question payload={interaction.payload} state={state} />
          {extra}
        </>
      );
    case "approval":
      return (
        <>
          <Approval interaction={interaction} payload={interaction.payload} state={state} inline={inline} />
          {extra}
        </>
      );
    case "account_selection":
      return (
        <>
          <AccountChoice payload={interaction.payload} state={state} />
          {extra}
        </>
      );
    case "sign_in":
      return (
        <>
          <SignIn interaction={interaction} payload={interaction.payload} state={state} />
          {extra}
        </>
      );
  }
}

type State = ReturnType<typeof useRespond>;

function Question({ payload, state }: { payload: QuestionPayload; state: State }) {
  const [text, setText] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const busy = !!state.pending;
  if (payload.input === "confirm") {
    return (
      <div className="flex gap-2">
        <Button variant="primary" disabled={busy} onClick={() => void state.respond({ kind: "answer", text: "Yes" }, "yes")}>
          Yes
        </Button>
        <Button disabled={busy} onClick={() => void state.respond({ kind: "answer", text: "No" }, "no")}>
          No
        </Button>
      </div>
    );
  }
  if ((payload.input === "choice" || payload.input === "multi_choice") && payload.options?.length) {
    const multi = payload.input === "multi_choice";
    return (
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!picked.length) return;
          void state.respond(
            {
              kind: "answer",
              text: payload.options!.filter((o) => picked.includes(o.id)).map((o) => o.label).join(", "),
              option_ids: picked,
            },
            "answer",
          );
        }}
      >
        <fieldset className="space-y-1.5">
          <legend className="sr-only">Choose</legend>
          {payload.options.map((option) => (
            <label
              key={option.id}
              className={cn(
                "flex min-h-11 cursor-pointer items-center gap-3 rounded-tile border px-3 py-2 text-row font-medium transition-colors",
                picked.includes(option.id) ? "border-accent bg-accent-subtle text-ink" : "border-hairline hover:bg-fill-1",
              )}
            >
              <input
                type={multi ? "checkbox" : "radio"}
                name="option"
                className="accent-[var(--accent)]"
                checked={picked.includes(option.id)}
                onChange={() =>
                  setPicked(
                    multi
                      ? picked.includes(option.id)
                        ? picked.filter((id) => id !== option.id)
                        : [...picked, option.id]
                      : [option.id],
                  )
                }
              />
              {option.label}
            </label>
          ))}
        </fieldset>
        <Button type="submit" variant="primary" className="mt-3" disabled={busy || !picked.length}>
          {busy ? "Sending…" : "Send answer"}
        </Button>
      </form>
    );
  }
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (text.trim()) void state.respond({ kind: "answer", text: text.trim() }, "answer");
      }}
      className="space-y-2"
    >
      {payload.input === "date" ? (
        <input type="date" value={text} onChange={(event) => setText(event.target.value)} className={inputClass} aria-label="Answer" />
      ) : (
        <textarea
          value={text}
          onChange={(event) => setText(event.target.value)}
          rows={2}
          maxLength={payload.constraints?.max_length ?? MAX_MESSAGE_BYTES}
          className={cn(inputClass, "resize-y")}
          aria-label="Answer"
          placeholder="Your answer"
        />
      )}
      <Button type="submit" variant="primary" disabled={busy || !text.trim()}>
        {busy ? "Sending…" : "Send answer"}
      </Button>
    </form>
  );
}

function Approval({
  interaction,
  payload,
  state,
  inline,
}: {
  interaction: Interaction;
  payload: ApprovalPayload;
  state: State;
  inline?: boolean;
}) {
  const busy = !!state.pending;
  const digest = interaction.action_digest!;
  const send = payload.effect === "sensitive_send";
  const cut = send ? payload.summary.indexOf("\n") : -1;
  const summaryHeading = cut >= 0 ? payload.summary.slice(0, cut) : payload.summary;
  const message = cut >= 0 ? payload.summary.slice(cut + 1) : "";
  const rawWarning = payload.warning?.trim();
  const internalSchedule = isInternalScheduleApproval(payload);
  const warning = internalSchedule && rawWarning
    ? rawWarning.replace(/^This modifies external records\.?\s*/i, "").trim()
    : rawWarning;
  const accountLabel = payload.account?.label?.trim();
  const showMeta = !internalSchedule || !!accountLabel || !!payload.recipients?.length;
  const summaryHasRecipients = payload.tool_id === "google/mail/mail-send";
  const buttonSize = inline ? "sm" : "md";

  return (
    <div>
      <p className={cn("font-semibold whitespace-pre-line text-ink [overflow-wrap:anywhere]", inline ? "text-row-title" : "text-row")}>
        {summaryHeading}
      </p>
      {cut >= 0 && (
        <>
          <blockquote
            aria-label="Message to be sent"
            className="mt-2.5 rounded-tile border border-hairline bg-fill-1 px-3.5 py-2.5 text-[13.5px] leading-relaxed whitespace-pre-wrap text-ink [overflow-wrap:anywhere]"
          >
            {message || "(No message text)"}
          </blockquote>
          {message.trimEnd().endsWith("…") && (
            <p className="mt-1 text-caption text-ink-2">Only the start of the message is shown here. The rest is sent too.</p>
          )}
        </>
      )}
      {warning && (
        <div
          role="note"
          aria-label="Warning"
          data-approval-warning
          className="mt-3 flex items-start gap-2 rounded-tile bg-warning-bg px-3 py-2.5 text-meta font-medium text-warning"
        >
          <AlertTriangle size={15} className="mt-0.5 shrink-0" aria-hidden />
          <p className="min-w-0 flex-1 whitespace-pre-line [overflow-wrap:anywhere]">{warning}</p>
        </div>
      )}
      {showMeta && (
        <dl className="mt-3 grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-1.5 text-meta">
          {send && !summaryHasRecipients && !!payload.recipients?.length && (
            <>
              <dt className="text-ink-2">To</dt>
              <dd className="font-medium text-ink [overflow-wrap:anywhere]">{payload.recipients.join(", ")}</dd>
            </>
          )}
          {!!accountLabel && (
            <>
              <dt className="text-ink-2">{send ? "From" : "Account"}</dt>
              <dd className="font-medium text-ink">{accountLabel}</dd>
            </>
          )}
          {!send && !!payload.recipients?.length && (
            <>
              <dt className="text-ink-2">To</dt>
              <dd className="font-medium text-ink [overflow-wrap:anywhere]">{payload.recipients.join(", ")}</dd>
            </>
          )}
          <dt className="text-ink-2">Effect</dt>
          <dd>
            <Pill tone={payload.effect === "sensitive_send" ? "warning" : "neutral"}>{EFFECT_LABEL[payload.effect]}</Pill>
          </dd>
        </dl>
      )}
      {!!payload.material_effects?.length && (
        <ul className="mt-3 space-y-1 text-[12.5px] text-ink-2">
          {payload.material_effects.map((effect) => (
            <li key={effect} className="flex items-center gap-2">
              <ShieldCheck size={14} className="shrink-0 text-ink-2" aria-hidden />
              <span>{effect}</span>
            </li>
          ))}
        </ul>
      )}
      <div className={cn("flex flex-wrap gap-2", inline ? "mt-3.5" : "mt-4")}>
        <Button
          variant="primary"
          size={buttonSize}
          disabled={busy}
          onClick={() => void state.respond(buildApprovalResponse("allow_once", digest), "allow_once")}
        >
          {state.pending === "allow_once" ? "Allowing…" : "Allow once"}
        </Button>
        {!payload.approval_once && (
          <Button
            variant="neutral"
            size={buttonSize}
            disabled={busy}
            onClick={() => void state.respond(buildApprovalResponse("always_allow", digest), "always_allow")}
          >
            {state.pending === "always_allow" ? "Saving rule…" : "Always allow"}
          </Button>
        )}
        <Button
          variant="neutral"
          size={buttonSize}
          className="text-danger hover:bg-danger-bg"
          disabled={busy}
          onClick={() => void state.respond(buildApprovalResponse("deny", digest), "deny")}
        >
          {state.pending === "deny" ? "Denying…" : "Deny"}
        </Button>
      </div>
    </div>
  );
}

function AccountChoice({ payload, state }: { payload: AccountSelectionPayload; state: State }) {
  const { data } = useResource<{ items: Connection[] }>("/connections", { poll: 0 });
  const [remember, setRemember] = useState(!!payload.remember_as_default);
  const busy = !!state.pending;
  const eligible = payload.eligible_connection_ids.map((id) => ({
    id,
    connection: data?.items.find((item) => item.id === id),
  }));
  const address = (connection?: Connection) => connection?.account?.label || "";
  const repeated = (connection?: Connection) =>
    !!address(connection) && eligible.filter((entry) => address(entry.connection) === address(connection)).length > 1;
  return (
    <div className="space-y-2">
      {eligible.map(({ id, connection }) => (
        <button
          key={id}
          type="button"
          disabled={busy}
          onClick={() =>
            void state.respond({ kind: "choose_account", connection_id: id, remember_as_default: remember }, id)
          }
          className="flex min-h-12 w-full items-center gap-3 rounded-tile border border-hairline px-3 py-2 text-left transition-colors hover:bg-fill-1 disabled:opacity-60"
        >
          <span className="min-w-0 flex-1">
            <span className="block text-row font-medium [overflow-wrap:anywhere]">
              {address(connection) || connection?.title || (data ? "An account that is no longer connected" : "Loading account…")}
            </span>
            {connection && (
              <span className="block text-meta text-ink-2">
                {[connection.title, repeated(connection) && `connected ${relative(connection.created_at)}`]
                  .filter(Boolean)
                  .join(", ")}
              </span>
            )}
          </span>
          {state.pending === id && <span className="text-meta text-ink-3">Choosing…</span>}
        </button>
      ))}
      <label className="flex min-h-11 items-center gap-2 text-meta text-ink-2">
        <input
          type="checkbox"
          className="accent-[var(--accent)]"
          checked={remember}
          onChange={(event) => setRemember(event.target.checked)}
        />
        Use this account next time
      </label>
    </div>
  );
}

function SignIn({ interaction, payload, state }: { interaction: Interaction; payload: SignInPayload; state: State }) {
  const hydrated = useHydrated();
  const desktop = hydrated && !!window.noriDesktop;
  const browserContinuation = hydrated && new URLSearchParams(window.location.search).get("nori_desktop_continue") === "1";
  const mcp = payload.integration_id === "mcp";
  const integration = useResource<Integration>(mcp ? null : `/integrations/${encodeURIComponent(payload.integration_id)}`, {
    poll: 0,
  });
  const server = useResource<MCPServer>(mcp && payload.connection_id ? `/mcp/${encodeURIComponent(payload.connection_id)}` : null, {
    poll: 0,
  });
  const named = useResource<Connection>(
    !mcp && payload.connection_id ? `/connections/${encodeURIComponent(payload.connection_id)}` : null,
    { poll: 0 },
  );
  const all = useResource<{ items: Connection[] }>(mcp || payload.connection_id ? null : "/connections", { poll: 0 });
  const accounts = (all.data?.items ?? []).filter(
    (item) => item.integration_id === payload.integration_id && item.account?.provider_id && item.status !== "disconnected",
  );
  const connection = { data: named.data ?? (accounts.length === 1 ? accounts[0] : undefined) };
  const action = useAction();
  const [expired] = useState(() => !!payload.expires_at && Date.parse(payload.expires_at) < Date.now());
  const url = payload.auth === "oauth" && payload.authorize_url ? safeAuthorizeURL(payload.authorize_url) : null;
  const returnTo = interaction.task_id
    ? `/work/${encodeURIComponent(interaction.task_id)}`
    : mcp && payload.connection_id
      ? `/apps/mcp/${encodeURIComponent(payload.connection_id)}`
      : "/apps";
  const signIn = () => {
    if (desktop) {
      void action.run("sign_in", () => window.noriDesktop!.openInBrowser());
      return;
    }
    if (url && !expired && !browserContinuation) {
      window.location.assign(url);
      return;
    }
    if (mcp && server.data) {
      void action.run("sign_in", async () => {
        const receipt = (await mutate<MCPServer>(`/mcp/${encodeURIComponent(server.data!.id)}/actions`, {
          request_id: newRequestID(),
          expected_revision: server.data!.revision,
          value: { action: "sign_in" },
        })) as { auth?: AuthAttempt };
        const next = receipt.auth && safeAuthorizeURL(receipt.auth.authorize_url);
        if (!next) throw new Error("Nori didn’t return a sign-in link. Try again.");
        window.location.assign(next);
      });
    } else if (!mcp && connection.data) {
      void action.run("sign_in", async () => {
        let capabilities: string[] | undefined;
        if (payload.integration_id === "google") {
          if (!integration.data) throw new Error("Google permissions could not be loaded. Try again.");
          const requested = new Set(payload.requested_scopes ?? []);
          const scopes = new Set([...(connection.data!.granted_scopes ?? []), ...requested]);
          const matched = integration.data.capabilities
            .filter((capability) => capability.supported && capability.scopes?.length && capability.scopes.every((scope) => scopes.has(scope)));
          const covered = new Set(matched.flatMap((capability) => capability.scopes ?? []));
          if ([...requested].some((scope) => !covered.has(scope))) {
            throw new Error("Google permissions could not be refreshed. Try again from Apps.");
          }
          capabilities = [...new Set([
            ...(connection.data!.capabilities ?? []),
            ...matched.map((capability) => capability.id),
          ])];
        }
        await startSignIn(connection.data!, "repair", returnTo, capabilities);
      });
    } else {
      void action.run("sign_in", async () => {
        throw new Error(browserContinuation
          ? "Open Apps to connect this account, then return to this task."
          : "This sign-in link expired. Sign in again from Apps; the task continues once the account works.");
      });
    }
  };
  const blocked = !desktop && !browserContinuation && !!payload.authorize_url && !url;
  const title = mcp ? server.data?.name ?? "MCP server" : integration.data?.title ?? payload.integration_id;
  const wanted = askedFor(payload.requested_scopes, integration.data);
  return (
    <div className="space-y-3">
      <p className="text-meta text-ink-2">{[title, connection.data?.account?.label].filter(Boolean).join(" · ")}</p>
      {desktop && <p className="text-meta text-ink-2">Sign in to Nori with the same account in your browser, then continue the connection there.</p>}
      {wanted.length > 0 && (
        <p className="text-meta text-ink-2">
          {title} will ask you to allow: {wanted.join("; ")}.
        </p>
      )}
      {payload.integration_id === "google" && (
        <p className="text-meta text-ink-2">Nori uses the Google data you allow to carry out your requests and may share it with your selected AI model provider.</p>
      )}
      {blocked && (
        <Notice tone="danger">This sign-in link isn’t one Nori issued, so it won’t open.</Notice>
      )}
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      <div className="flex flex-wrap gap-2">
        <Button
          variant="primary"
          disabled={blocked || !!action.pending || state.pending === "decline"}
          onClick={signIn}
        >
          <KeyRound size={15} aria-hidden />
          {action.pending ? "Opening sign-in…" : desktop ? "Continue in browser" : "Sign in"}
        </Button>
        <Button disabled={!!state.pending} onClick={() => void state.respond({ kind: "decline" }, "decline")}>
          {state.pending === "decline" ? "Declining…" : "Not now"}
        </Button>
      </div>
    </div>
  );
}

function askedFor(scopes: string[] | undefined, integration: Integration | undefined) {
  const asked = new Set(scopes ?? []);
  const covered = (integration?.capabilities ?? []).filter(
    (capability) => capability.scopes?.length && capability.scopes.every((scope) => asked.has(scope)),
  );
  const within = (a: string[], b: string[]) => a.length < b.length && a.every((scope) => b.includes(scope));
  return covered
    .filter((capability) => !covered.some((other) => within(capability.scopes!, other.scopes!)))
    .map((capability) => capability.title);
}
