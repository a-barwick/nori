"use client";

import { useState } from "react";
import { useResource } from "@/lib/assistant/useResource";
import type { BriefItem, Interaction } from "@/lib/contracts/assistant";
import type { Part } from "@/lib/types";
import { ReviewApprovalSheet } from "./ReviewApprovalSheet";
import { Pill } from "./ui";

export type BriefActionStatus = "pending" | "sent" | "skipped";

export function briefItemStatus(
  interaction?: Interaction,
  localOverride?: "sent" | "skipped",
): BriefActionStatus {
  if (localOverride) return localOverride;
  if (!interaction || interaction.status === "pending") return "pending";
  if (interaction.status === "expired") return "skipped";
  if (interaction.status === "superseded") {
    return interaction.superseded_by ? "sent" : "skipped";
  }
  const kind = interaction.resolution?.kind;
  if (kind === "decline" || kind === "cancel" || kind === "expired") {
    return "skipped";
  }
  return "sent";
}

export function day1BodyText(part: Pick<Part, "text" | "fallback">): string {
  const explicit = part.text?.trim();
  if (explicit) return explicit;
  const fallback = part.fallback?.trim();
  if (fallback) {
    const lines = fallback.split("\n").map((line) => line.trim()).filter(Boolean);
    if (lines.length > 1) return lines.slice(1).join(" ");
  }
  return "I'll check in like this each morning. What's on your mind today?";
}

function BriefRow({
  item,
  providedInteraction,
  localOverride,
  onReview,
}: {
  item: BriefItem;
  providedInteraction?: Interaction;
  localOverride?: "sent" | "skipped";
  onReview: (interactionId: string, interaction?: Interaction) => void;
}) {
  const shouldFetch = !!item.interaction_id && !providedInteraction;
  const live = useResource<Interaction>(
    shouldFetch ? `/interactions/${encodeURIComponent(item.interaction_id!)}` : null,
    { poll: 20_000 },
  );
  const interaction = providedInteraction ?? live.data;
  const status = item.interaction_id ? briefItemStatus(interaction, localOverride) : undefined;

  return (
    <li
      data-brief-row
      className="flex min-h-[48px] items-center justify-between gap-3 px-4 py-[11px]"
    >
      <span className="min-w-0 flex-1 truncate text-row font-semibold text-ink">
        {item.title}
      </span>
      {item.interaction_id ? (
        status === "sent" ? (
          <Pill tone="ok">Sent</Pill>
        ) : status === "skipped" ? (
          <Pill tone="neutral">Skipped</Pill>
        ) : (
          <button
            type="button"
            onClick={() => onReview(item.interaction_id!, interaction)}
            className="shrink-0 rounded-full bg-accent-subtle px-3 py-[5px] text-[12.5px] font-semibold text-accent-text transition-colors hover:bg-fill-2"
          >
            {item.action_label?.trim() || "Review"}
          </button>
        )
      ) : item.trailing ? (
        <span className="shrink-0 whitespace-nowrap text-[12.5px] font-medium text-ink-2">
          {item.trailing}
        </span>
      ) : null}
    </li>
  );
}

function ReviewSheetHost({
  interactionId,
  initialInteraction,
  onClose,
  onResolved,
}: {
  interactionId: string;
  initialInteraction?: Interaction;
  onClose: () => void;
  onResolved: (status: "sent" | "skipped", resolved: Interaction) => void;
}) {
  const live = useResource<Interaction>(
    initialInteraction ? null : `/interactions/${encodeURIComponent(interactionId)}`,
    { poll: 0 },
  );
  const interaction = initialInteraction ?? live.data;

  return (
    <ReviewApprovalSheet
      open
      interaction={interaction}
      loading={!interaction && live.loading}
      error={live.error?.message}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      onResolved={(status, resolved) => {
        live.apply(resolved);
        onResolved(status, resolved);
      }}
    />
  );
}

export function BriefCard({
  part,
  interactions,
}: {
  part: Part;
  interactions?: Record<string, Interaction>;
}) {
  const [activeReview, setActiveReview] = useState<{ id: string; interaction?: Interaction } | null>(null);
  const [localOverrides, setLocalOverrides] = useState<Record<string, "sent" | "skipped">>({});

  const greeting = part.greeting?.trim() || "Good morning";
  const meta = part.meta?.trim();
  const items = part.items ?? [];

  return (
    <article
      aria-label={greeting}
      data-brief-card
      className="overflow-hidden rounded-[20px] border border-card-border bg-wash-top"
    >
      <header className="flex items-baseline justify-between gap-2 px-[18px] pt-4 pb-3.5">
        <h3 className="min-w-0 truncate text-[17px] font-semibold tracking-[-0.015em] text-ink">
          {greeting}
        </h3>
        {meta && (
          <span className="shrink-0 whitespace-nowrap text-[12.5px] font-medium text-ink-2">
            {meta}
          </span>
        )}
      </header>

      {items.length > 0 ? (
        <ul
          aria-label="Morning brief items"
          className="divide-y divide-hairline border-t border-hairline bg-surface"
        >
          {items.map((item, index) => (
            <BriefRow
              key={item.interaction_id ?? `${item.title}-${index}`}
              item={item}
              providedInteraction={item.interaction_id ? interactions?.[item.interaction_id] : undefined}
              localOverride={item.interaction_id ? localOverrides[item.interaction_id] : undefined}
              onReview={(id, interaction) => setActiveReview({ id, interaction })}
            />
          ))}
        </ul>
      ) : (
        <p data-brief-empty-body className="px-[18px] pb-4 text-[15px] leading-normal text-ink-2">
          {day1BodyText(part)}
        </p>
      )}

      {activeReview && (
        <ReviewSheetHost
          interactionId={activeReview.id}
          initialInteraction={activeReview.interaction}
          onClose={() => setActiveReview(null)}
          onResolved={(status) => {
            setLocalOverrides((prev) => ({ ...prev, [activeReview.id]: status }));
          }}
        />
      )}
    </article>
  );
}
