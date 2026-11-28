"use client";

import { useState } from "react";
import { X } from "lucide-react";
import { Dialog } from "radix-ui";
import { Button } from "@/components/ui/primitives";
import { buildReviewApprovalResponse, submitInteractionResponse } from "@/lib/assistant/interactions";
import { useAction, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { ApprovalPayload, Interaction } from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { inputClass, Notice } from "./ui";

export type ReviewDraft = {
  to: string;
  subject: string;
  body: string;
};

export function parseReviewDraft(interaction?: Interaction): ReviewDraft {
  if (!interaction || interaction.kind !== "approval") {
    return { to: "", subject: "", body: "" };
  }
  const payload = interaction.payload as Partial<ApprovalPayload>;
  const summary = payload.summary ?? "";
  const lines = summary.split("\n");

  let to = "";
  let subject = "";
  let body = "";

  const headerEndIdx = lines.findIndex((line, idx) => idx > 0 && line.trim() === "");
  const headerLines = headerEndIdx > 0 ? lines.slice(0, headerEndIdx) : lines.slice(0, Math.min(lines.length, 6));
  const hasExplicitHeaders = headerLines.some((line) => /^(To|Subject):/i.test(line.trim()));

  if (hasExplicitHeaders) {
    for (const line of headerLines) {
      const toMatch = /^To:\s*(.+)$/i.exec(line.trim());
      if (toMatch) to = toMatch[1].trim();
      const subjMatch = /^Subject:\s*(.+)$/i.exec(line.trim());
      if (subjMatch) subject = subjMatch[1].trim();
    }
    if (headerEndIdx > 0) {
      body = lines.slice(headerEndIdx + 1).join("\n").trim();
    }
  } else {
    const firstNewline = summary.indexOf("\n");
    const firstLine = firstNewline >= 0 ? summary.slice(0, firstNewline) : summary;
    body = firstNewline >= 0 ? summary.slice(firstNewline + 1).trim() : "";

    const quotedSubject = /"([^"]+)"/.exec(firstLine);
    if (quotedSubject) subject = quotedSubject[1].trim();

    const toAddress = /\bto\s+([^:\n]+?)(?::|$)/i.exec(firstLine);
    if (toAddress) to = toAddress[1].trim();
  }

  if (!to && payload.recipients?.length) {
    to = payload.recipients.join(", ");
  }
  if (!subject) {
    subject = interaction.prompt?.trim() ?? "";
  }

  return { to, subject, body };
}

export function ReviewApprovalPanel({
  interaction,
  loading,
  error,
  onClose,
  onResolved,
}: {
  interaction?: Interaction;
  loading?: boolean;
  error?: string;
  onClose: () => void;
  onResolved: (status: "sent" | "skipped", resolved: Interaction) => void;
}) {
  const invalidate = useResourceInvalidation();
  const action = useAction();
  const draft = parseReviewDraft(interaction);
  const [subject, setSubject] = useState(draft.subject);
  const [body, setBody] = useState(draft.body);

  const busy = !!action.pending;
  const canSubmit = !!interaction && interaction.kind === "approval" && !!interaction.action_digest && interaction.status === "pending";
  const canSend = canSubmit && body.trim().length > 0;

  const handleSubmit = (choice: "send" | "skip") => {
    if (!interaction || !interaction.action_digest) return;
    if (choice === "send" && !canSend) return;
    const value = buildReviewApprovalResponse(
      choice,
      interaction.action_digest,
      draft.body,
      body,
      draft.subject || undefined,
      draft.subject ? subject : undefined,
    );
    void action.run(
      choice,
      async () => {
        const result = await submitInteractionResponse(interaction, value, invalidate);
        if (!result.ok) {
          throw new Error(result.message);
        }
        onResolved(choice === "send" ? "sent" : "skipped", result.resolved);
        onClose();
      },
      choice === "send" ? "Reply sent." : "Reply skipped.",
    );
  };

  return (
    <div className="flex flex-col gap-3.5" data-review-approval-panel>
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-[17px] font-semibold tracking-[-0.015em] text-ink">Review reply</h2>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="grid size-7 shrink-0 place-items-center rounded-full bg-fill-1 text-ink-2 transition-colors hover:bg-fill-2 hover:text-ink"
        >
          <X size={15} strokeWidth={2} aria-hidden />
        </button>
      </div>

      {loading && !interaction ? (
        <p className="py-4 text-meta text-ink-2">Loading draft…</p>
      ) : (
        <>
          {(draft.to || draft.subject) && (
            <div className="space-y-1 rounded-tile bg-sidebar px-3.5 py-2.5 text-[13.5px] text-ink-2">
              {draft.to && (
                <div className="truncate">
                  <strong className="font-semibold text-ink">To:</strong> {draft.to}
                </div>
              )}
              {draft.subject && (
                <div className="flex items-center gap-1.5">
                  <strong className="shrink-0 font-semibold text-ink">Subject:</strong>
                  <input
                    type="text"
                    aria-label="Subject"
                    value={subject}
                    onChange={(event) => setSubject(event.target.value)}
                    disabled={busy || !canSubmit}
                    className="min-w-0 flex-1 bg-transparent text-[13.5px] text-ink outline-none placeholder:text-ink-3"
                  />
                </div>
              )}
            </div>
          )}

          <textarea
            aria-label="Draft reply"
            value={body}
            onChange={(event) => setBody(event.target.value)}
            disabled={busy || !canSubmit}
            rows={5}
            className={cn(inputClass, "min-h-[120px] resize-y text-[14.5px] leading-relaxed")}
          />

          {(action.error || error) && <Notice tone="danger">{action.error ?? error}</Notice>}

          <div className="flex items-center justify-end gap-2">
            <Button
              variant="quiet"
              size="sm"
              disabled={busy || !canSubmit}
              onClick={() => handleSubmit("skip")}
            >
              {action.pending === "skip" ? "Skipping…" : "Skip"}
            </Button>
            <Button
              variant="primary"
              size="sm"
              disabled={busy || !canSend}
              onClick={() => handleSubmit("send")}
            >
              {action.pending === "send" ? "Sending…" : "Send reply"}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

export function ReviewApprovalSheet({
  open,
  interaction,
  loading,
  error,
  onOpenChange,
  onResolved,
}: {
  open: boolean;
  interaction?: Interaction;
  loading?: boolean;
  error?: string;
  onOpenChange: (open: boolean) => void;
  onResolved: (status: "sent" | "skipped", resolved: Interaction) => void;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <Dialog.Content
          aria-describedby={undefined}
          className="anim-pop fixed top-1/2 left-1/2 z-50 max-h-[85vh] w-[min(480px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-sheet border border-card-border bg-sheet p-5 text-ink shadow-pop focus-visible:outline-none"
        >
          <Dialog.Title className="sr-only">Review reply</Dialog.Title>
          <ReviewApprovalPanel
            key={interaction ? `${interaction.id}:${interaction.revision}` : "loading"}
            interaction={interaction}
            loading={loading}
            error={error}
            onClose={() => onOpenChange(false)}
            onResolved={onResolved}
          />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
