"use client";

import { useState } from "react";
import Link from "next/link";
import {
  BarChart3,
  Bell,
  Brain,
  LayoutGrid,
  Monitor,
  Moon,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
  Sun,
  UserCircle,
  Wand2,
  X,
} from "lucide-react";
import { useAppearance, type NoriAppearance } from "@/lib/appearance";
import type { ModelKeysResponse, ModelSettings, ServiceKeysResponse } from "@/lib/assistant/api";
import { useAssistant, useAssistantConfig } from "@/lib/assistant/context";
import {
  googleDriveNudgeDismissedKey,
  hasSelectedHomeChat,
  isGoogleDriveConnected,
  needsByokSetup,
  needsExaSetup,
} from "@/lib/assistant/modelSetup";
import { useSummary } from "@/lib/assistant/sources";
import { useResource } from "@/lib/assistant/useResource";
import type { Connection, Integration } from "@/lib/contracts/assistant";
import { useHydrated } from "@/lib/hooks";
import { AppBrandIcon } from "./BrandIcons";
import { Pill, Row, Rows, Screen, Section } from "./ui";

const APPEARANCE_ICON: Record<NoriAppearance, typeof Monitor> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
};

type List<T> = { items: T[] };

function readDriveNudgeDismissed(scope: string): boolean {
  if (typeof window === "undefined") return false;
  try {
    return window.localStorage.getItem(googleDriveNudgeDismissedKey(scope)) === "1";
  } catch {
    return false;
  }
}

export function MoreScreen({ userEmail: _userEmail }: { userEmail?: string } = {}) {
  void _userEmail;
  const { runtime } = useAssistant();
  const { capabilities, has, scope } = useAssistantConfig();
  const summary = useSummary();
  const [appearance] = useAppearance();
  const hydrated = useHydrated();
  const [driveDismissedOverride, setDriveDismissedOverride] = useState(false);

  const connectionsEnabled = has("connections");
  const modelKeys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 60_000, versioned: false });
  const serviceKeys = useResource<ServiceKeysResponse>("/settings/service-keys", { poll: 60_000, versioned: false });
  const models = useResource<ModelSettings>("/settings/models", { poll: 60_000 });
  const connections = useResource<List<Connection>>(connectionsEnabled ? "/connections" : null, { poll: 60_000 });
  const googleIntegration = useResource<Integration>(connectionsEnabled ? "/integrations/google" : null, { poll: 0 });

  const appsAttention = summary.apps_attention ?? 0;

  const reconnecting = runtime.reconnecting;
  const offline = !!runtime.error && !reconnecting;
  const degradedPill = reconnecting
    ? { tone: "warning" as const, label: "Reconnecting" }
    : offline
      ? { tone: "danger" as const, label: "Offline" }
      : undefined;

  const AppearanceIcon = APPEARANCE_ICON[appearance];
  const requiresByok = needsByokSetup(modelKeys.data, hasSelectedHomeChat(models.data), capabilities);
  const requiresExa = needsExaSetup(serviceKeys.data, capabilities);

  const modelsPillLabel =
    requiresByok && requiresExa
      ? "2 keys"
      : requiresByok
        ? "Model key"
        : requiresExa
          ? "Web search"
          : undefined;

  const driveDismissed = driveDismissedOverride || (hydrated && readDriveNudgeDismissed(scope));
  const googleAvailable = googleIntegration.data?.availability === "available";
  const driveConnected = isGoogleDriveConnected(connections.data?.items);
  const showDriveNudge =
    hydrated && connectionsEnabled && googleAvailable && !!connections.data && !driveConnected && !driveDismissed;

  const dismissDriveNudge = () => {
    setDriveDismissedOverride(true);
    try {
      window.localStorage.setItem(googleDriveNudgeDismissedKey(scope), "1");
    } catch {
    }
  };

  return (
    <Screen title="Settings" hideHeaderTitle>
      {(requiresByok || requiresExa) && (
        <div className="mb-5 space-y-2.5">
          {requiresByok && (
            <div
              role="status"
              className="flex flex-wrap items-center justify-between gap-3 rounded-card bg-warning-bg px-4 py-3.5 text-warning"
            >
              <div className="flex min-w-0 flex-1 items-start gap-2.5">
                <span aria-hidden className="mt-1.5 size-2 shrink-0 rounded-full bg-danger" />
                <div className="min-w-0">
                  <p className="text-row font-semibold">Connect an AI model to get started</p>
                  <p className="mt-0.5 text-meta opacity-90">
                    Add an AI model key in Models so Nori can chat and run tasks for you.
                  </p>
                </div>
              </div>
              <Link
                href="/settings/models"
                className="shrink-0 text-meta font-semibold underline underline-offset-2"
              >
                Add key
              </Link>
            </div>
          )}

          {requiresExa && (
            <div
              role="status"
              className="flex flex-wrap items-center justify-between gap-3 rounded-card bg-accent-subtle px-4 py-3.5 text-ink"
            >
              <div className="flex min-w-0 flex-1 items-start gap-2.5">
                <span aria-hidden className="mt-1.5 size-2 shrink-0 rounded-full bg-danger" />
                <div className="min-w-0">
                  <p className="text-row font-semibold">Enable web search</p>
                  <p className="mt-0.5 text-meta text-ink-2">
                    For the best experience, enable web search by adding a web search key in Models.
                  </p>
                </div>
              </div>
              <Link
                href="/settings/models#web-search"
                className="shrink-0 text-meta font-semibold text-accent-text underline underline-offset-2"
              >
                Add key
              </Link>
            </div>
          )}
        </div>
      )}

      {showDriveNudge && (
        <div className="mb-5 flex items-center gap-3 rounded-card border border-card-border bg-surface p-3.5 shadow-card dark:bg-fill-1 dark:shadow-none">
          <AppBrandIcon service="drive" />
          <div className="min-w-0 flex-1">
            <p className="text-row font-semibold text-ink">Connect Google Drive</p>
            <p className="mt-0.5 text-meta text-ink-2">
              Let Nori reference the docs and files you choose.
            </p>
          </div>
          <Link
            href="/apps"
            className="t-fast inline-flex h-8 shrink-0 items-center rounded-full bg-accent px-3.5 text-meta font-semibold text-on-accent hover:bg-accent-hover"
          >
            Connect
          </Link>
          <button
            type="button"
            onClick={dismissDriveNudge}
            aria-label="Dismiss Google Drive suggestion"
            className="t-fast grid size-7 shrink-0 cursor-pointer place-items-center rounded-full text-ink-3 hover:bg-fill-2 hover:text-ink"
          >
            <X size={15} aria-hidden />
          </button>
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
            href="/settings/personalization"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                <Wand2 size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Personalization"
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

      <Section title="General">
        <Rows label="General">
          <Row
            href="/settings/notifications"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <Bell size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Notifications"
          />
          <Row
            href="/settings/models"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <Sparkles size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Models"
            trailing={
              modelsPillLabel ? (
                <Pill tone="warning">
                  <span aria-hidden className="size-1.5 rounded-full bg-danger" />
                  {modelsPillLabel}
                </Pill>
              ) : undefined
            }
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
