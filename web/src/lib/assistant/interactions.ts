import type { Interaction, InteractionResponse } from "@/lib/contracts/assistant";
import { getJSON, mutate, newRequestID } from "./api";
import { relative } from "./format";

export type ApprovalDecision = "allow_once" | "always_allow" | "deny";
export type ApprovalResponseValue = Extract<
  InteractionResponse["value"],
  { kind: "approve" | "decline"; action_digest: string }
>;

export type InlineInteractionEntry = {
  interaction: Interaction;
  rememberedRule?: boolean;
};

export function buildApprovalResponse(decision: ApprovalDecision, actionDigest: string): ApprovalResponseValue {
  switch (decision) {
    case "allow_once":
      return { kind: "approve", action_digest: actionDigest };
    case "always_allow":
      return { kind: "approve", action_digest: actionDigest, remember_rule: true };
    case "deny":
      return { kind: "decline", action_digest: actionDigest };
  }
}

export function buildReviewApprovalResponse(
  action: "send" | "skip",
  actionDigest: string,
  originalBody: string,
  currentBody: string,
  originalSubject?: string,
  currentSubject?: string,
): ApprovalResponseValue {
  if (action === "skip") {
    return { kind: "decline", action_digest: actionDigest };
  }
  const trimmedOriginalSubject = originalSubject?.trim();
  const trimmedCurrentSubject = currentSubject?.trim();
  const subjectModified =
    trimmedOriginalSubject !== undefined &&
    trimmedOriginalSubject !== "" &&
    trimmedCurrentSubject !== undefined &&
    trimmedCurrentSubject !== trimmedOriginalSubject;
  const bodyModified = currentBody !== originalBody;
  if (subjectModified || bodyModified) {
    const edited_fields: { subject?: string; body?: string } = {};
    if (subjectModified) {
      edited_fields.subject = trimmedCurrentSubject;
    }
    if (bodyModified) {
      edited_fields.body = currentBody;
    }
    return {
      kind: "approve",
      action_digest: actionDigest,
      edited_fields,
    };
  }
  return { kind: "approve", action_digest: actionDigest };
}

export function invalidationPrefixesForResponse(value: InteractionResponse["value"]): string[] {
  const prefixes = ["/interactions", "/work", "/assistant/summary", "/inbox"];
  if (value.kind === "approve" && value.remember_rule) {
    prefixes.push("/permissions");
  }
  return prefixes;
}

export function responseAnnouncement(value: InteractionResponse["value"]): string {
  switch (value.kind) {
    case "approve":
      return value.remember_rule ? "Approved and rule saved." : "Allowed once.";
    case "decline":
      return "Denied.";
    case "answer":
      return "Answer sent.";
    case "choose_account":
      return "Account chosen.";
    case "resume":
      return "Handed back to Nori.";
    case "cancel":
      return "Takeover cancelled.";
  }
}

export function formatInteractionExpiry(expiresAt?: string, now = Date.now()): string | undefined {
  if (!expiresAt) return undefined;
  const target = Date.parse(expiresAt);
  if (Number.isNaN(target)) return undefined;
  if (target <= now) return "Expired";
  return `Expires ${relative(expiresAt, now)}`;
}

export function formatCompactExpiry(expiresAt?: string, now = Date.now()): string | undefined {
  if (!expiresAt) return undefined;
  const target = Date.parse(expiresAt);
  if (Number.isNaN(target)) return undefined;
  const diffMs = target - now;
  if (diffMs <= 0) return "Expired";
  const mins = Math.max(1, Math.round(diffMs / 60_000));
  if (mins < 60) return `${mins}m`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

function isInteractionResource(value: unknown): value is Interaction {
  return !!value && typeof value === "object" && typeof (value as { id?: unknown }).id === "string" && typeof (value as { status?: unknown }).status === "string";
}

export function resolveInteractionLocally(
  interaction: Interaction,
  value: InteractionResponse["value"],
  receiptResource?: unknown,
  nowISO = new Date().toISOString(),
): Interaction {
  if (isInteractionResource(receiptResource) && receiptResource.status !== "pending") {
    return receiptResource;
  }
  const resolution: NonNullable<Interaction["resolution"]> =
    value.kind === "answer"
      ? { kind: "answer", text: value.text, at: nowISO }
      : value.kind === "choose_account"
        ? { kind: "choose_account", connection_id: value.connection_id, at: nowISO }
        : value.kind === "resume" || value.kind === "cancel"
          ? { kind: value.kind, ...(value.text ? { text: value.text } : {}), at: nowISO }
          : { kind: value.kind, at: nowISO };
  return {
    ...interaction,
    status: "resolved",
    revision: interaction.revision + 1,
    resolved_at: nowISO,
    resolution,
  };
}

export async function submitInteractionResponse(
  interaction: Interaction,
  value: InteractionResponse["value"],
  invalidate: (...prefixes: string[]) => void = () => {},
): Promise<
  | { ok: true; resolved: Interaction; rememberRule?: boolean }
  | { ok: false; current: Interaction; message: string }
> {
  const latest = await getJSON<Interaction>(`/interactions/${encodeURIComponent(interaction.id)}`);
  if (
    latest.status !== "pending" ||
    latest.revision !== interaction.revision ||
    latest.action_digest !== interaction.action_digest ||
    latest.instruction_revision !== interaction.instruction_revision
  ) {
    invalidate("/interactions", "/work", "/assistant/summary");
    return {
      ok: false,
      current: latest,
      message:
        latest.status === "pending"
          ? "This changed since it was shown. Review the current version."
          : "This was already answered or replaced.",
    };
  }
  const receipt = await mutate<Interaction>(
    `/interactions/${encodeURIComponent(interaction.id)}/responses`,
    { request_id: newRequestID(), expected_revision: interaction.revision, value },
  );
  const resolved = resolveInteractionLocally(interaction, value, receipt.resource);
  const rememberRule = value.kind === "approve" ? !!value.remember_rule : undefined;
  invalidate(...invalidationPrefixesForResponse(value));
  return { ok: true, resolved, rememberRule };
}

export function resolvedStatusPill(interaction: Interaction): { tone: "ok" | "neutral" | "danger"; label: string } {
  if (interaction.status === "expired") return { tone: "neutral", label: "Expired" };
  if (interaction.status === "superseded") return { tone: "neutral", label: "Replaced" };
  const kind = interaction.resolution?.kind;
  if (kind === "approve") return { tone: "ok", label: "Approved" };
  if (kind === "decline") {
    return {
      tone: "neutral",
      label: interaction.kind === "sign_in" ? "Not now" : interaction.kind === "approval" ? "Denied" : "Declined",
    };
  }
  if (kind === "signed_in") return { tone: "ok", label: "Signed in" };
  if (kind === "resume") return { tone: "ok", label: "Resumed" };
  if (kind === "cancel") return { tone: "neutral", label: "Cancelled" };
  return { tone: "ok", label: "Answered" };
}

export function resolvedSummaryText(interaction: Interaction, rememberedRule?: boolean): string {
  const resolution = interaction.resolution;
  if (!resolution) return "";
  switch (resolution.kind) {
    case "approve":
      if (rememberedRule === true) return "Always allowed · Rule saved";
      if (rememberedRule === false) return "Allowed once";
      return "Approved";
    case "signed_in":
      return "Signed in";
    case "resume":
      return resolution.text ? `Resumed · ${resolution.text}` : "Handed back to Nori";
    case "cancel":
      return "Cancelled";
    case "decline":
      return interaction.kind === "sign_in" ? "Not now" : interaction.kind === "approval" ? "Denied" : "Declined";
    case "choose_account":
      return "Account chosen";
    case "expired":
      return "Expired";
    case "answer":
      return resolution.text ? `“${resolution.text}”` : "Answered";
  }
}

function interactionTimestamp(interaction: Interaction): number {
  const raw = interaction.created_at ?? interaction.resolved_at;
  if (!raw) return 0;
  const parsed = Date.parse(raw);
  return Number.isNaN(parsed) ? 0 : parsed;
}

export function isActionableInteraction(interaction: Interaction): boolean {
  return interaction.kind !== "question";
}

export function isChatInlineInteraction(interaction: Interaction): boolean {
  if (interaction.surface) return interaction.surface === "chat";
  if (interaction.kind !== "question") return true;
  return interaction.payload.input !== "text" && interaction.payload.input !== "date";
}

export function mergeInlineInteractions(
  pending: Interaction[],
  sessionResolved: Record<string, InlineInteractionEntry>,
): InlineInteractionEntry[] {
  const byID = new Map<string, InlineInteractionEntry>();

  for (const [id, entry] of Object.entries(sessionResolved)) {
    byID.set(id, entry);
  }

  for (const item of pending) {
    const existing = byID.get(item.id);
    if (!existing || item.revision > existing.interaction.revision) {
      byID.set(item.id, { interaction: item });
    }
  }

  return [...byID.values()].sort((a, b) => {
    const diff = interactionTimestamp(a.interaction) - interactionTimestamp(b.interaction);
    if (diff !== 0) return diff;
    return a.interaction.id.localeCompare(b.interaction.id);
  });
}
