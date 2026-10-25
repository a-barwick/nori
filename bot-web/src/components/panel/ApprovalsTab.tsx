"use client";

import { ShieldCheck } from "lucide-react";
import { ApprovalCard } from "@/components/chat/ApprovalCard";
import { EmptyTab, SectionHeader } from "@/components/ui/primitives";
import { useNori } from "@/lib/store";

export function ApprovalsTab() {
  const approvals = useNori((s) => s.approvals);
  const pending = approvals.filter((a) => a.status === "pending").sort((a, b) => a.at - b.at);
  const recent = approvals.filter((a) => a.status !== "pending").sort((a, b) => (b.resolvedAt ?? 0) - (a.resolvedAt ?? 0));

  return (
    <div>
      {pending.length ? (
        <section>
          <SectionHeader first>Waiting on you</SectionHeader>
          <div className="mt-2 flex flex-col gap-2">
            {pending.map((a) => (
              <ApprovalCard key={a.id} approvalId={a.id} compact />
            ))}
          </div>
        </section>
      ) : (
        <EmptyTab icon={ShieldCheck} title="Approvals" line="Nothing waiting on you" />
      )}
      {recent.length > 0 && (
        <section>
          <SectionHeader first={false}>Recent</SectionHeader>
          <ul className="mt-1">
            {recent.map((a) => (
              <li key={a.id} className="border-b border-hairline py-2.5 last:border-0">
                <div className="text-row-title font-medium text-ink">{a.title}</div>
                <div className="text-meta text-ink-2">{a.outcome}</div>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}
