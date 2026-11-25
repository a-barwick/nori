"use client";

import Link from "next/link";
import { Brain, Bug, History, LayoutGrid, ShieldCheck, SlidersHorizontal } from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { useAssistant } from "@/lib/assistant/context";
import { money, plural } from "@/lib/assistant/format";
import { useSummary } from "@/lib/assistant/sources";
import { useResource } from "@/lib/assistant/useResource";
import type { Connection, MCPServer, MemoryFact, PermissionRule, Preferences, WorkingMemory } from "@/lib/contracts/assistant";
import { SERVICE_UNAVAILABLE, STREAM_RECONNECTING } from "@/lib/runtime/messages";
import { cn } from "@/lib/utils";
import { coreBudgetStats, formatByteBudget } from "./MemoryScreen";
import { HeroCard, Pill, Row, Rows, Screen, Section } from "./ui";

type List<T> = { items: T[] };

export function MoreScreen() {
  const { has, runtime, localMode } = useAssistant();
  const summary = useSummary();

  const mcp = useResource<List<MCPServer>>(has("mcp") ? "/mcp" : null, { poll: 30_000 });
  const connections = useResource<List<Connection>>(has("connections") ? "/connections" : null, { poll: 30_000 });
  const memory = useResource<List<MemoryFact>>(has("memory") ? "/memory" : null, { poll: 30_000 });
  const working = useResource<WorkingMemory>(has("memory") ? "/memory/working" : null, { poll: 30_000 });
  const permissions = useResource<List<PermissionRule>>(has("permissions") ? "/permissions" : null, { poll: 30_000 });
  const preferences = useResource<Preferences>(has("preferences") ? "/preferences" : null, { poll: 60_000 });

  const mcpItems = mcp.data?.items ?? [];
  const connItems = (connections.data?.items ?? []).filter((c) => c.status !== "disconnected");
  const connectedCount =
    mcpItems.filter((s) => s.observed.status === "connected").length +
    connItems.filter((c) => c.status === "connected").length;
  const totalApps = mcpItems.length + connItems.length;
  const appsAttention = summary.apps_attention ?? 0;

  const coreFacts = memory.data?.items ?? [];
  const pinnedCount = coreFacts.filter((f) => f.pinned).length;
  const budget = coreBudgetStats(coreFacts, working.data);
  const memPct = Math.round(budget.ratio * 100);
  const memBarColor =
    budget.tone === "danger" ? "bg-danger" : budget.tone === "warning" ? "bg-warning" : "bg-accent";

  const rulesCount = permissions.data?.items.length ?? 0;
  const prefs = preferences.data;

  const reconnecting = runtime.error === SERVICE_UNAVAILABLE || runtime.error === STREAM_RECONNECTING;
  const offline = !!runtime.error && !reconnecting;
  const statusPill = reconnecting
    ? { tone: "warning" as const, label: "Reconnecting" }
    : offline || !runtime.session
      ? { tone: "danger" as const, label: runtime.loading ? "Connecting" : "Offline" }
      : { tone: "ok" as const, label: "Live" };

  return (
    <Screen title="More">
      <HeroCard className="flex items-center gap-4">
        <div className="grid size-[52px] shrink-0 place-items-center rounded-[15px] border border-hairline bg-surface shadow-pill">
          <NoriMark size={32} state={runtime.busy ? "thinking" : "idle"} />
        </div>
        <div className="min-w-0 flex-1">
          <p className="text-[17px] font-bold text-ink">Your Nori</p>
          <p className="truncate text-[13.5px] text-ink-2">
            {localMode ? "Local runtime · Connected" : "Hosted runtime · Synced"}
          </p>
        </div>
        <Pill tone={statusPill.tone}>{statusPill.label}</Pill>
      </HeroCard>

      <Section title="Apps & Memory">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Link
            href="/apps"
            className="flex min-h-[134px] flex-col justify-between gap-3 rounded-card border border-hairline bg-surface p-4 transition-[border-color,background-color,box-shadow] hover:border-hairline-strong hover:bg-wash-top hover:shadow-pill"
          >
            <div className="flex items-center justify-between gap-2">
              <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                <LayoutGrid size={18} strokeWidth={1.85} aria-hidden />
              </span>
              {appsAttention > 0 && <Pill tone="accent">{appsAttention} attention</Pill>}
            </div>
            <div>
              <p className="text-[15.5px] font-semibold text-ink">Apps &amp; MCP</p>
              <p className="mt-0.5 text-meta text-ink-2">
                {totalApps > 0
                  ? `${connectedCount} connected · ${plural(mcpItems.length, "MCP server")}`
                  : has("mcp") || has("connections")
                    ? "Connect MCP servers & accounts"
                    : "Not enabled on this Nori"}
              </p>
            </div>
          </Link>

          <Link
            href="/memory"
            className="flex min-h-[134px] flex-col justify-between gap-3 rounded-card border border-hairline bg-surface p-4 transition-[border-color,background-color,box-shadow] hover:border-hairline-strong hover:bg-wash-top hover:shadow-pill"
          >
            <div className="flex items-center justify-between gap-2">
              <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                <Brain size={18} strokeWidth={1.85} aria-hidden />
              </span>
              {has("memory") && (
                <span className="font-mono text-caption font-semibold text-ink-2">
                  {formatByteBudget(budget.used, budget.budget)}
                </span>
              )}
            </div>
            <div className="w-full">
              <p className="text-[15.5px] font-semibold text-ink">Memory</p>
              <p className="mt-0.5 text-meta text-ink-2">
                {has("memory")
                  ? `${plural(coreFacts.length, "core fact")}${pinnedCount ? ` · ${pinnedCount} pinned` : ""}`
                  : "Not enabled on this Nori"}
              </p>
              {has("memory") && (
                <div className="mt-2.5 h-[4.5px] w-full overflow-hidden rounded-full bg-fill-2">
                  <div className={cn("h-full rounded-full transition-[width]", memBarColor)} style={{ width: `${memPct}%` }} />
                </div>
              )}
            </div>
          </Link>
        </div>
      </Section>

      <Section title="Controls & Account">
        <Rows>
          <Row
            href="/settings/permissions"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <ShieldCheck size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Standing Permissions"
            detail={
              has("permissions")
                ? `${plural(rulesCount, "rule")} · deny-wins precedence`
                : "Not enabled on this Nori"
            }
          />
          <Row
            href="/settings/preferences"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <SlidersHorizontal size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Preferences & Budgets"
            detail={
              prefs
                ? `${prefs.timezone} · ${money(prefs.budgets.task_usd)} / task`
                : has("preferences")
                  ? "Timezone, working hours & spending caps"
                  : "Not enabled on this Nori"
            }
          />
          <Row
            href="/settings/history"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <History size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Action History"
            detail={has("operations") ? "Audit log of external actions" : "Not enabled on this Nori"}
          />
          <Row
            href="/settings/diagnostics"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <Bug size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Account & Diagnostics"
            detail="Runtime status, contracts & debug tools"
          />
        </Rows>
      </Section>
    </Screen>
  );
}
