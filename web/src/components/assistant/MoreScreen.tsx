"use client";

import Link from "next/link";
import { BarChart3, Brain, LayoutGrid, Monitor, Moon, ShieldCheck, SlidersHorizontal, Sparkles, Sun, UserCircle } from "lucide-react";
import { useAppearance, type NoriAppearance } from "@/lib/appearance";
import type { ModelKeysResponse, ModelSettings } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { useSummary } from "@/lib/assistant/sources";
import { useResource } from "@/lib/assistant/useResource";
import { needsByokSetup } from "./ApiKeysSettings";
import { hasSelectedHomeChat } from "./ModelsSettings";
import { Notice, Pill, Row, Rows, Screen, Section } from "./ui";

const APPEARANCE_ICON: Record<NoriAppearance, typeof Monitor> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
};

export function MoreScreen({ userEmail: _userEmail }: { userEmail?: string } = {}) {
  void _userEmail;
  const { runtime } = useAssistant();
  const summary = useSummary();
  const [appearance] = useAppearance();

  const modelKeys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 60_000, versioned: false });
  const models = useResource<ModelSettings>("/settings/models", { poll: 60_000 });

  const appsAttention = summary.apps_attention ?? 0;

  const reconnecting = runtime.reconnecting;
  const offline = !!runtime.error && !reconnecting;
  const degradedPill = reconnecting
    ? { tone: "warning" as const, label: "Reconnecting" }
    : offline
      ? { tone: "danger" as const, label: "Offline" }
      : undefined;

  const AppearanceIcon = APPEARANCE_ICON[appearance];
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

      <Section title="Nori">
        <Rows label="Nori">
          <Row
            href="/apps"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                <LayoutGrid size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Apps"
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
          />
          <Row
            href="/settings/permissions"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                <ShieldCheck size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Permissions"
          />
        </Rows>
      </Section>

      <Section title="Settings">
        <Rows label="Settings">
          <Row
            href="/settings/models"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <Sparkles size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Models"
            trailing={requiresByok ? <Pill tone="warning">Required</Pill> : undefined}
          />
          <Row
            href="/settings/api-costs"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <BarChart3 size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Usage & Budgets"
          />
          <Row
            href="/settings/preferences"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <SlidersHorizontal size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Preferences"
          />
          <Row
            href="/settings/appearance"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <AppearanceIcon size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Appearance"
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
            trailing={degradedPill ? <Pill tone={degradedPill.tone}>{degradedPill.label}</Pill> : undefined}
          />
        </Rows>
      </Section>
    </Screen>
  );
}
