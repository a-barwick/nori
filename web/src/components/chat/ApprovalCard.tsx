"use client";

import { Check, ShieldCheck, X } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { resolveApproval } from "@/lib/nori/engine";
import { useNori } from "@/lib/store";
import { clock } from "@/lib/time";
import type { ID } from "@/lib/types";
import { cn } from "@/lib/utils";

/** Inline in the thread and in the Approvals tab: same anatomy (§5.8). */
export function ApprovalCard({ approvalId, compact, className }: { approvalId: ID; compact?: boolean; className?: string }) {
  const ap = useNori((s) => s.approvals.find((a) => a.id === approvalId));
  if (!ap) return null;
  const pending = ap.status === "pending";

  return (
    <div
      className={cn("rounded-card bg-surface shadow-[inset_0_0_0_1px_var(--hairline)]", compact ? "p-3.5" : "p-4", className)}
      role="group"
      aria-label={`Approval: ${ap.title}`}
    >
      <div className="flex items-center gap-2">
        <ShieldCheck size={20} strokeWidth={1.5} className="shrink-0 text-ink-2" aria-hidden />
        <span className="text-row-title font-medium text-ink">{ap.title}</span>
      </div>
      <p className="mt-2.5 border-l-2 border-hairline-strong pl-2.5 text-meta text-ink">{ap.what}</p>
      <p className="mt-2 text-meta text-ink-2">{ap.why}</p>
      <p className="text-meta text-ink-2">{ap.impact}</p>
      {pending ? (
        <div className="mt-3.5 flex gap-2">
          <Button variant="primary" size="sm" onClick={() => void resolveApproval(ap.id, true)}>
            Approve
          </Button>
          <Button size="sm" onClick={() => void resolveApproval(ap.id, false)}>
            Not now
          </Button>
        </div>
      ) : (
        <div className="mt-3 flex items-center gap-1.5 text-meta text-ink-2">
          {ap.status === "approved" ? (
            <Check size={16} strokeWidth={1.5} className="text-accent-signal" aria-hidden />
          ) : (
            <X size={16} strokeWidth={1.5} className="text-ink-3" aria-hidden />
          )}
          <span>
            {ap.outcome}
            {ap.resolvedAt && <span className="text-ink-3"> · {clock(ap.resolvedAt)}</span>}
          </span>
        </div>
      )}
    </div>
  );
}
