"use client";

import Link from "next/link";
import { useState } from "react";
import { Bell, Check, Pencil, X } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { APIError, getJSON, mutate, newRequestID } from "@/lib/assistant/api";
import { when } from "@/lib/assistant/format";
import { useResourceInvalidation, useAction, useResource } from "@/lib/assistant/useResource";
import type { Automation } from "@/lib/contracts/assistant";
import type { Part } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Notice, Pill } from "./ui";

export function formatReminderTime(part: Pick<Part, "text" | "scheduled_at">, now = Date.now()): string {
  if (part.scheduled_at) {
    const target = new Date(part.scheduled_at);
    if (!Number.isNaN(target.getTime())) {
      const current = new Date(now);
      const startOf = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
      const dayDiff = Math.round((startOf(target) - startOf(current)) / 86_400_000);
      const timeStr = target.toLocaleTimeString("en-US", { hour: "numeric", minute: "2-digit" });
      if (dayDiff === 0) return `Today at ${timeStr}`;
      if (dayDiff === 1) return `Tomorrow at ${timeStr}`;
      if (dayDiff === -1) return `Yesterday at ${timeStr}`;
    }
  }
  return part.text?.trim() || (part.scheduled_at ? when(part.scheduled_at) : "");
}

export async function cancelReminder(id: string, invalidate: (...prefixes: string[]) => void = () => {}): Promise<Automation> {
  const path = `/automations/${encodeURIComponent(id)}`;
  const latest = await getJSON<Automation>(path);
  if (latest.status === "cancelled") {
    invalidate("/automations", "/work", "/assistant/summary");
    return latest;
  }
  try {
    const receipt = await mutate<Automation>(`${path}/actions`, {
      request_id: newRequestID(),
      expected_revision: latest.revision,
      value: { action: "cancel" },
    });
    invalidate("/automations", "/work", "/assistant/summary");
    return (receipt.resource as Automation | undefined) ?? { ...latest, status: "cancelled", revision: latest.revision + 1 };
  } catch (error) {
    if (error instanceof APIError && error.stale && error.body?.current) {
      const current = error.body.current as Automation;
      if (current.status === "cancelled") {
        invalidate("/automations", "/work", "/assistant/summary");
        return current;
      }
    }
    throw error;
  }
}

export function ReminderCard({ part }: { part: Part }) {
  const invalidate = useResourceInvalidation();
  const live = useResource<Automation>(part.id ? `/automations/${encodeURIComponent(part.id)}` : null, { poll: 0 });
  const action = useAction();
  const [cancelledLocal, setCancelledLocal] = useState(false);

  const automation = live.data;
  const cancelled = cancelledLocal || automation?.status === "cancelled";
  const done = !cancelled && automation?.status === "enabled" && !automation.next_at && automation.trigger.kind === "once";
  const status = cancelled ? "cancelled" : done ? "done" : "set";
  const title = automation?.title || part.name || part.fallback || "Reminder";
  const schedule = formatReminderTime({ text: part.text, scheduled_at: automation?.next_at ?? part.scheduled_at });

  const handleCancel = () => {
    if (!part.id) return;
    void action.run(
      "cancel",
      async () => {
        const updated = await cancelReminder(part.id!, invalidate);
        live.apply(updated);
        setCancelledLocal(true);
      },
      "Reminder cancelled.",
    );
  };

  return (
    <article
      aria-label={`Reminder: ${title}`}
      data-reminder-card
      data-reminder-id={part.id}
      data-reminder-status={status}
      className={cn(
        "rounded-card border px-3.5 py-3",
        cancelled ? "border-card-border bg-sidebar" : "border-card-border bg-surface shadow-card",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className={cn("inline-flex items-center gap-1.5 text-meta font-semibold", cancelled ? "text-ink-2" : "text-ink")}>
          {done ? <Check size={14} className="text-ink-2" aria-hidden /> : <Bell size={14} className={cancelled ? "text-ink-3" : "text-accent-text"} aria-hidden />}
          {cancelled ? "Reminder cancelled" : done ? "Reminder sent" : "Reminder set"}
        </span>
        {schedule && !cancelled && (
          <span className="text-meta text-ink-2">· {schedule}</span>
        )}
        {cancelled && <Pill tone="neutral">Cancelled</Pill>}
        {done && <Pill tone="neutral">Done</Pill>}
      </div>
      <p className={cn("mt-1 text-row font-medium [overflow-wrap:anywhere]", cancelled ? "text-ink-2 line-through" : "text-ink")}>
        {title}
      </p>
      {part.id && !cancelled && !done && (
        <div className="mt-2.5 flex flex-wrap items-center gap-2">
          <Link
            href={`/routines/${encodeURIComponent(part.id)}/edit`}
            className="t-fast inline-flex h-8 shrink-0 items-center justify-center gap-1.5 rounded-full bg-surface px-3 text-meta font-medium text-ink shadow-[inset_0_0_0_1px_var(--hairline-strong)] hover:bg-fill-1"
          >
            <Pencil size={13} aria-hidden />
            Edit
          </Link>
          <Button
            size="sm"
            variant="quiet"
            className="text-danger hover:bg-danger-bg hover:text-danger"
            disabled={!!action.pending}
            onClick={handleCancel}
          >
            <X size={13} aria-hidden />
            {action.pending === "cancel" ? "Cancelling…" : "Cancel"}
          </Button>
        </div>
      )}
      {action.error && (
        <div className="mt-2">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}
    </article>
  );
}
