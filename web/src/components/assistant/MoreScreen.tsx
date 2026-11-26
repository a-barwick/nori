"use client";

import Link from "next/link";
import { Activity, BarChart3, Brain, History, KeyRound, LayoutGrid, Monitor, Moon, RotateCcw, ShieldCheck, SlidersHorizontal, Sun, UserCircle } from "lucide-react";
import { appearanceLabel, useAppearance, type NoriAppearance } from "@/lib/appearance";
import type { ModelKeysResponse, ModelSettings } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { money, plural } from "@/lib/assistant/format";
import { useSummary } from "@/lib/assistant/sources";
import { useResource } from "@/lib/assistant/useResource";
import type { Connection, MCPServer, MemoryFact, PermissionRule, Preferences, WorkingMemory } from "@/lib/contracts/assistant";
import { needsByokSetup } from "./ApiKeysSettings";
import { hasSelectedHomeChat } from "./ModelsSettings";
import { coreBudgetStats, formatByteBudget } from "./MemoryScreen";
import { Notice, Pill, Row, Rows, Screen, Section } from "./ui";

type List<T> = { items: T[] };

const APPEARANCE_ICON: Record<NoriAppearance, typeof Monitor> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
};

export function MoreScreen() {
  const { has, runtime, localMode } = useAssistant();
  const summary = useSummary();
  const [appearance] = useAppearance();

  const mcp = useResource<List<MCPServer>>(has("mcp") ? "/mcp" : null, { poll: 30_000 });
  const connections = useResource<List<Connection>>(has("connections") ? "/connections" : null, { poll: 30_000 });
  const memory = useResource<List<MemoryFact>>(has("memory") ? "/memory" : null, { poll: 30_000 });
  const working = useResource<WorkingMemory>(has("memory") ? "/memory/working" : null, { poll: 30_000 });
  const permissions = useResource<List<PermissionRule>>(has("permissions") ? "/permissions" : null, { poll: 30_000 });
  const preferences = useResource<Preferences>(has("preferences") ? "/preferences" : null, { poll: 60_000 });
  const modelKeys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 60_000, versioned: false });
  const models = useResource<ModelSettings>("/settings/models", { poll: 60_000 });

  const mcpItems = mcp.data?.items ?? [];
  const connItems = (connections.data?.items ?? []).filter((c) => c.status !== "disconnected");
  const connectedCount =
    mcpItems.filter((s) => s.observed.status === "connected").length +
    connItems.filter((c) => c.status === "connected").length;
  const totalApps = mcpItems.length + connItems.length;
  const appsAttention = summary.apps_attention ?? 0;

  const coreFacts = memory.data?.items ?? [];
  const budget = coreBudgetStats(coreFacts, working.data);

  const rulesCount = permissions.data?.items.length ?? 0;
  const prefs = preferences.data;

  const reconnecting = runtime.reconnecting;
  const offline = !!runtime.error && !reconnecting;
  const degradedPill = reconnecting
    ? { tone: "warning" as const, label: "Reconnecting" }
    : offline
      ? { tone: "danger" as const, label: "Offline" }
      : undefined;

  const AppearanceIcon = APPEARANCE_ICON[appearance];
  const configuredKeysCount = modelKeys.data?.providers.filter((p) => p.configured).length ?? 0;
  const requiresByok = needsByokSetup(modelKeys.data, hasSelectedHomeChat(models.data));

  return (
    <Screen title="More" hideHeaderTitle>
      {requiresByok && (
        <div className="mb-6">
          <Notice
            tone="warning"
            action={
              <Link href="/settings/models" className="font-semibold underline underline-offset-2">
                Choose model
              </Link>
            }
          >
            Choose a model to use Nori.
          </Notice>
        </div>
      )}
      <Section title="Workspace">
        <Rows label="Workspace">
          <Row
            href="/apps"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                <LayoutGrid size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Apps & MCP"
            detail={totalApps > 0 ? `${connectedCount} connected` : undefined}
            trailing={appsAttention > 0 ? <Pill tone="warning">{appsAttention} attention</Pill> : undefined}
          />
          <Row
            href="/memory"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                <Brain size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Memory"
            detail={has("memory") ? formatByteBudget(budget.used, budget.budget) : undefined}
          />
        </Rows>
      </Section>

      <Section title="Settings">
        <Rows label="Settings">
          <Row
            href="/settings/permissions"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <ShieldCheck size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Standing Permissions"
            detail={rulesCount > 0 ? plural(rulesCount, "rule") : undefined}
          />
          <Row
            href="/settings/preferences"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <SlidersHorizontal size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Preferences & Budgets"
            detail={prefs ? `${prefs.timezone} · ${money(prefs.budgets.task_usd)} / task` : undefined}
          />
          <Row
            href="/settings/api-costs"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <BarChart3 size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="API costs"
          />
          <Row
            href="/settings/appearance"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <AppearanceIcon size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Appearance"
            detail={appearanceLabel(appearance)}
          />
          <Row
            href="/settings/models"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <KeyRound size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Models"
            detail={configuredKeysCount > 0 ? plural(configuredKeysCount, "configured") : undefined}
            trailing={requiresByok ? <Pill tone="warning">Required</Pill> : undefined}
          />
          <Row
            href="/settings/reset"
            leading={<span className="grid size-9 place-items-center rounded-[10px] bg-danger-bg text-danger"><RotateCcw size={18} strokeWidth={1.85} aria-hidden /></span>}
            title="Reset data"
          />
        </Rows>
      </Section>

      <Section title="Account">
        <Rows label="Account">
          <Row
            href="/settings/account"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <UserCircle size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Account"
            detail={localMode ? "Local" : undefined}
            trailing={degradedPill ? <Pill tone={degradedPill.tone}>{degradedPill.label}</Pill> : undefined}
          />
          <Row
            href="/settings/history"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <History size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Action History"
          />
          <Row
            href="/settings/diagnostics"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <Activity size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Diagnostics"
            detail={degradedPill ? degradedPill.label : undefined}
          />
        </Rows>
      </Section>
    </Screen>
  );
}
