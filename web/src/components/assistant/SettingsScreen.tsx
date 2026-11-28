"use client";

import { useState } from "react";
import {
  Activity,
  ArrowLeft,
  ArrowUpRight,
  BarChart3,
  Bell,
  Brain,
  History,
  Monitor,
  Moon,
  RotateCcw,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
  Sun,
  UserCircle,
  Wand2,
  X,
} from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { APPEARANCE_OPTIONS, useAppearance, type NoriAppearance } from "@/lib/appearance";
import {
  ACCOUNT_DELETED_REDIRECT,
  APIError,
  deleteAccount,
  errorText,
  mutate,
  newRequestID,
  resetUserData,
} from "@/lib/assistant/api";
import { useAssistant, useAssistantConfig } from "@/lib/assistant/context";
import { markBrowserReset } from "@/lib/assistant/resetState";
import { when } from "@/lib/assistant/format";
import { useResourceInvalidation, useAction, useResource } from "@/lib/assistant/useResource";
import type {
  BillingTier,
  CapabilitiesEntitlement,
  Connection,
  EntitlementSummary,
  OperationProjection as Operation,
  PermissionRule,
  Preferences,
  PreferencesEditable,
} from "@/lib/contracts/assistant";
import { PRIVACY_URL, TERMS_URL } from "@/lib/legal";
import { cn } from "@/lib/utils";
import { ApiCosts } from "./ApiCosts";
import { AppBrandIcon } from "./BrandIcons";
import { ModelsSettings } from "./ModelsSettings";
import { PersonalizationSettings } from "./PersonalizationSettings";
import { NotificationsSettings } from "./ProactivitySettings";
import { SignOutForm } from "./SignOutForm";
import { OperationRows, shownOperations } from "./WorkScreen";
import {
  Empty,
  Failure,
  Field,
  inputBase,
  inputClass,
  Loading,
  Notice,
  NotYet,
  Pill,
  Row,
  Rows,
  Screen,
  Section,
  Switch,
} from "./ui";

type List<T> = { items: T[] };
export type SettingsSection =
  | "notifications"
  | "permissions"
  | "personalization"
  | "preferences"
  | "api-costs"
  | "appearance"
  | "models"
  | "reset"
  | "account"
  | "history"
  | "diagnostics";

const SECTIONS: {
  id: SettingsSection | "memory";
  title: string;
  icon: typeof ShieldCheck;
  href?: string;
  back?: string;
  backLabel?: string;
  feature?: Parameters<ReturnType<typeof useAssistant>["has"]>[0];
}[] = [
  { id: "notifications", title: "Notifications", icon: Bell, feature: "preferences" },
  { id: "permissions", title: "Permissions", icon: ShieldCheck, feature: "permissions" },
  { id: "personalization", title: "Personalization", icon: Wand2, feature: "preferences" },
  { id: "preferences", title: "Preferences", icon: SlidersHorizontal, feature: "preferences" },
  { id: "api-costs", title: "Usage & Budgets", icon: BarChart3 },
  { id: "appearance", title: "Appearance", icon: Monitor },
  { id: "models", title: "Models", icon: Sparkles },
  { id: "reset", title: "Reset data", icon: RotateCcw, back: "/settings/account", backLabel: "Account" },
  { id: "memory", title: "Memory", icon: Brain, href: "/memory", feature: "memory" },
  { id: "account", title: "Account", icon: UserCircle },
  { id: "history", title: "Action History", icon: History, feature: "operations", back: "/settings/account", backLabel: "Account" },
  { id: "diagnostics", title: "Diagnostics", icon: Activity, back: "/settings/account", backLabel: "Account" },
];

export function SettingsIndex() {
  const { capabilities, capabilitiesError, has, refreshCapabilities } = useAssistantConfig();
  return (
    <Screen title="Settings" back="/more" backLabel="Back">
      {capabilitiesError && (
        <div className="mb-4">
          <Failure error={capabilitiesError} onRetry={refreshCapabilities} />
        </div>
      )}
      <Rows>
        {SECTIONS.map((section) => (
          <Row
            key={section.id}
            href={section.href ?? `/settings/${section.id}`}
            leading={<section.icon size={18} className="text-ink-2" aria-hidden />}
            title={section.title}
            trailing={capabilities && !capabilitiesError && section.feature && !has(section.feature) ? <Pill>Not yet</Pill> : undefined}
          />
        ))}
      </Rows>
    </Screen>
  );
}

const PLAN_LABELS: Record<BillingTier, string> = {
  trial: "Trial",
  byok: "BYOK",
  standard: "Standard",
  power: "Power",
};

function EntitlementNotice({ entitlement }: { entitlement?: CapabilitiesEntitlement | EntitlementSummary }) {
  if (!entitlement) return null;
  if (entitlement.suspended) {
    return (
      <div className="mb-4">
        <Notice tone="danger">
          {entitlement.suspended_reason || "This account has been suspended. Your history remains read-only."}
        </Notice>
      </div>
    );
  }
  if (entitlement.status === "past_due" || entitlement.status === "expired") {
    return (
      <div className="mb-4">
        <Notice tone="warning">
          Your subscription is inactive. Renew your plan to resume chatting and running tasks.
        </Notice>
      </div>
    );
  }
  if (entitlement.credit_grant_usd > 0 && entitlement.credit_remaining_usd <= 0 && !entitlement.allow_byok) {
    return (
      <div className="mb-4">
        <Notice tone="warning">
          Your trial credits are used up. Upgrade your plan to continue.
        </Notice>
      </div>
    );
  }
  return null;
}

export function SettingsScreen({ section, userEmail }: { section: SettingsSection; userEmail?: string }) {
  const { has, capabilities } = useAssistantConfig();
  const meta = SECTIONS.find((item) => item.id === section)!;
  const back = meta.back ?? "/more";
  const backLabel = meta.backLabel ?? "Settings";
  if (meta.feature && !has(meta.feature)) {
    return (
      <Screen title={meta.title} back={back} backLabel={backLabel}>
        <NotYet what={meta.title} />
      </Screen>
    );
  }
  return (
    <Screen title={meta.title} back={back} backLabel={backLabel}>
      {section !== "api-costs" && <EntitlementNotice entitlement={capabilities?.entitlement} />}
      {section === "notifications" ? (
        <NotificationsSettings pushAvailable={capabilities?.assistant_features?.push_notifications} />
      ) : section === "permissions" ? (
        <Permissions />
      ) : section === "personalization" ? (
        <PersonalizationSettings />
      ) : section === "preferences" ? (
        <PreferencesForm />
      ) : section === "api-costs" ? (
        <ApiCosts />
      ) : section === "appearance" ? (
        <AppearanceSettings />
      ) : section === "models" ? (
        <ModelsSettings />
      ) : section === "reset" ? (
        <ResetData />
      ) : section === "account" ? (
        <AccountSettings userEmail={userEmail} />
      ) : section === "history" ? (
        <ActionHistory />
      ) : (
        <Diagnostics />
      )}
    </Screen>
  );
}

function Permissions() {
  const invalidate = useResourceInvalidation();
  const rules = useResource<List<PermissionRule>>("/permissions", { poll: 30_000 });
  const action = useAction();
  const [confirmId, setConfirmId] = useState<string | null>(null);

  if (rules.loading && !rules.data) return <Loading />;

  const items = rules.data?.items ?? [];
  const allowed = items.filter((rule) => rule.effect === "allow");
  const ask = items.filter((rule) => rule.effect === "ask");
  const blocked = items.filter((rule) => rule.effect === "deny");

  const revoke = (rule: PermissionRule) =>
    action.run(rule.id, async () => {
      await mutate(`/permissions/${encodeURIComponent(rule.id)}/actions`, {
        request_id: newRequestID(),
        expected_revision: rule.revision,
        value: { action: "revoke" },
      });
      setConfirmId(null);
      invalidate("/permissions", "/automations");
    });

  const renderRuleRow = (rule: PermissionRule) => {
    const confirming = confirmId === rule.id;
    const dotClass = rule.effect === "deny" ? "bg-danger" : rule.effect === "ask" ? "bg-warning" : "bg-ok";
    return (
      <li key={rule.id} id={rule.id} className="group flex items-center gap-3 px-4 py-3.5">
        <span aria-hidden className={cn("size-2 shrink-0 rounded-full", dotClass)} />
        <span className="min-w-0 flex-1">
          <span className="block text-body text-ink [overflow-wrap:anywhere]">
            {rule.summary ?? rule.title ?? "Permission"}
          </span>
          {rule.expires_at && (
            <span className="mt-0.5 block text-caption text-ink-3">Until {when(rule.expires_at)}</span>
          )}
        </span>
        {confirming ? (
          <div className="flex shrink-0 items-center gap-1.5">
            <Button size="sm" variant="danger" disabled={action.pending === rule.id} onClick={() => void revoke(rule)}>
              {action.pending === rule.id ? "Removing…" : "Remove"}
            </Button>
            <Button size="sm" variant="quiet" disabled={action.pending === rule.id} onClick={() => setConfirmId(null)}>
              Keep
            </Button>
          </div>
        ) : (
          <button
            type="button"
            aria-label="Remove permission"
            onClick={() => setConfirmId(rule.id)}
            className="grid size-7 shrink-0 place-items-center rounded-full text-ink-3 opacity-60 transition-colors hover:bg-danger-bg hover:text-danger hover:opacity-100 focus-visible:opacity-100 sm:opacity-35 sm:group-hover:opacity-100"
          >
            <X size={14} aria-hidden />
          </button>
        )}
      </li>
    );
  };

  return (
    <div className="space-y-6">
      {rules.error && <Failure error={rules.error} onRetry={() => void rules.reload()} />}
      {action.error && <Notice tone="danger">{action.error}</Notice>}
      {!items.length ? (
        !rules.error && <Empty>Nori asks before making changes or sending messages.</Empty>
      ) : (
        <>
          {allowed.length > 0 && (
            <Section title="Allowed">
              <Rows label="Allowed">{allowed.map(renderRuleRow)}</Rows>
            </Section>
          )}
          {ask.length > 0 && (
            <Section title="Ask first">
              <Rows label="Ask first">{ask.map(renderRuleRow)}</Rows>
            </Section>
          )}
          {blocked.length > 0 && (
            <Section title="Blocked">
              <Rows label="Blocked">{blocked.map(renderRuleRow)}</Rows>
            </Section>
          )}
        </>
      )}
    </div>
  );
}

const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const DAY_ORDER = [1, 2, 3, 4, 5, 6, 0];

function summarizeWorkingHours(hours: Preferences["working_hours"]): string {
  if (!hours.length) return "Not set";
  const ordered = DAY_ORDER.map((day) => hours.find((entry) => entry.day === day)).filter(
    (entry): entry is NonNullable<typeof entry> => Boolean(entry),
  );
  if (!ordered.length) return "Not set";
  const sameTimes = ordered.every((entry) => entry.start === ordered[0].start && entry.end === ordered[0].end);
  const days = ordered.map((entry) => entry.day);
  const isWeekdays = days.length === 5 && days.join(",") === "1,2,3,4,5";
  const dayLabel = isWeekdays ? "Mon – Fri" : `${ordered.length} days`;
  return sameTimes ? `${dayLabel} · ${ordered[0].start}–${ordered[0].end}` : dayLabel;
}

function PreferencesForm() {
  const preferences = useResource<Preferences>("/preferences", { poll: 0 });
  if (preferences.loading && !preferences.data) return <Loading />;
  if (!preferences.data) {
    return <Failure error={preferences.error ?? "Preferences unavailable."} onRetry={() => void preferences.reload()} />;
  }
  return <PreferencesEditor current={preferences.data} onSaved={preferences.apply} />;
}

function editable(preferences: Preferences): PreferencesEditable {
  const { revision: _revision, updated_at: _updated, ...value } = preferences;
  void _revision;
  void _updated;
  return value;
}

function PreferencesEditor({ current, onSaved }: { current: Preferences; onSaved: (next: Preferences) => void }) {
  const invalidate = useResourceInvalidation();
  const action = useAction();
  const [editingField, setEditingField] = useState<"timezone" | "locale" | null>(null);
  const [draftText, setDraftText] = useState("");
  const [editingHours, setEditingHours] = useState(false);
  const [conflict, setConflict] = useState<Preferences>();

  const commit = (next: PreferencesEditable) =>
    action.run("save", async () => {
      try {
        const receipt = await mutate<Preferences>(
          "/preferences",
          {
            request_id: newRequestID(),
            expected_revision: current.revision,
            value: next,
          },
          "PUT",
        );
        onSaved(receipt.resource as Preferences);
        setEditingField(null);
        invalidate("/connections");
      } catch (error) {
        if (error instanceof APIError && error.stale && error.body?.current) {
          setConflict(error.body.current as Preferences);
        }
        throw error;
      }
    });

  const base = editable(current);

  if (editingHours) {
    return (
      <WorkingHoursEditor
        hours={base.working_hours}
        saving={!!action.pending}
        error={action.error}
        onBack={() => setEditingHours(false)}
        onChange={(working_hours) => void commit({ ...base, working_hours })}
      />
    );
  }

  const startEdit = (field: "timezone" | "locale") => {
    setEditingField(field);
    setDraftText(field === "timezone" ? base.timezone : base.locale ?? "");
  };

  const saveField = (field: "timezone" | "locale") => {
    const trimmed = draftText.trim();
    if (field === "timezone" && !trimmed) return;
    if (field === "timezone") {
      void commit({ ...base, timezone: trimmed });
    } else {
      void commit({ ...base, locale: trimmed || undefined });
    }
  };

  const renderTextRow = (field: "timezone" | "locale", label: string, value: string, placeholder?: string) => {
    if (editingField === field) {
      return (
        <li key={field} className="px-4 py-3">
          <form
            className="flex flex-wrap items-center gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              saveField(field);
            }}
          >
            <span className="min-w-0 flex-1 text-row font-semibold text-ink">{label}</span>
            <input
              aria-label={label}
              value={draftText}
              onChange={(event) => setDraftText(event.target.value)}
              list={field === "timezone" ? "nori-zones" : undefined}
              placeholder={placeholder}
              className={cn(inputBase, "h-9 w-48 py-0")}
              autoFocus
            />
            <Button type="submit" size="sm" variant="primary" disabled={!!action.pending || (field === "timezone" && !draftText.trim())}>
              {action.pending ? "Saving…" : "Save"}
            </Button>
            <Button type="button" size="sm" variant="quiet" disabled={!!action.pending} onClick={() => setEditingField(null)}>
              Cancel
            </Button>
          </form>
        </li>
      );
    }
    return (
      <Row
        key={field}
        title={label}
        onClick={() => startEdit(field)}
        trailing={<span className="text-meta font-medium text-ink-2">{value || "Not set"}</span>}
        chevron
      />
    );
  };

  return (
    <div className="space-y-6">
      <datalist id="nori-zones">
        {(Intl as unknown as { supportedValuesOf?: (key: string) => string[] })
          .supportedValuesOf?.("timeZone")
          .map((zone) => (
            <option key={zone} value={zone} />
          ))}
      </datalist>

      {conflict && (
        <Notice tone="warning" action={<Button size="sm" onClick={() => onSaved(conflict)}>Load latest</Button>}>
          These changed on another device. Load the latest to edit them.
        </Notice>
      )}
      {action.error && !conflict && <Notice tone="danger">{action.error}</Notice>}

      <Section title="Region">
        <Rows label="Region">
          {renderTextRow("timezone", "Time zone", base.timezone, "America/Los_Angeles")}
          {renderTextRow("locale", "Language & region", base.locale ?? "", "en-US")}
        </Rows>
      </Section>

      <Section title="Schedule">
        <Rows label="Schedule">
          <Row
            title="Working hours"
            onClick={() => setEditingHours(true)}
            trailing={<span className="text-meta font-medium tabular-nums text-ink-2">{summarizeWorkingHours(base.working_hours)}</span>}
            chevron
          />
        </Rows>
      </Section>

      <AccountDefaults
        defaults={base.account_defaults}
        onChange={(account_defaults) => void commit({ ...base, account_defaults })}
      />
    </div>
  );
}

function WorkingHoursEditor({
  hours,
  saving,
  error,
  onBack,
  onChange,
}: {
  hours: Preferences["working_hours"];
  saving: boolean;
  error?: string;
  onBack: () => void;
  onChange: (next: Preferences["working_hours"]) => void;
}) {
  const entryFor = (day: number) => hours.find((entry) => entry.day === day);
  const updateDay = (day: number, next?: { start: string; end: string }) => {
    const updated = [...hours.filter((entry) => entry.day !== day), ...(next ? [{ day, ...next }] : [])].sort(
      (a, b) => a.day - b.day,
    );
    onChange(updated);
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <button
          type="button"
          onClick={onBack}
          className="inline-flex h-8 items-center gap-1.5 rounded-full bg-fill-1 px-3 text-meta font-semibold text-ink transition-colors hover:bg-fill-2"
        >
          <ArrowLeft size={14} aria-hidden />
          Preferences
        </button>
        <span className="text-meta font-semibold text-ink-2">Working hours</span>
      </div>
      {error && <Notice tone="danger">{error}</Notice>}
      <Rows label="Working hours">
        {DAY_ORDER.map((day) => {
          const entry = entryFor(day);
          return (
            <li key={day} className="flex min-h-14 flex-wrap items-center gap-3 px-4 py-3">
              <Switch
                checked={!!entry}
                label={DAYS[day]}
                disabled={saving}
                onCheckedChange={(on) => updateDay(day, on ? { start: "09:00", end: "17:30" } : undefined)}
              />
              <span className="min-w-12 flex-1 text-row font-semibold text-ink">{DAYS[day]}</span>
              {entry ? (
                <span className="flex items-center gap-2">
                  <input
                    aria-label={`${DAYS[day]} start`}
                    type="time"
                    value={entry.start}
                    disabled={saving}
                    onChange={(event) => updateDay(day, { start: event.target.value, end: entry.end })}
                    className={cn(inputBase, "h-9 w-[6.5rem] px-2 py-0 tabular-nums")}
                  />
                  <span className="text-ink-3" aria-hidden>
                    –
                  </span>
                  <input
                    aria-label={`${DAYS[day]} end`}
                    type="time"
                    value={entry.end}
                    disabled={saving}
                    onChange={(event) => updateDay(day, { start: entry.start, end: event.target.value })}
                    className={cn(inputBase, "h-9 w-[6.5rem] px-2 py-0 tabular-nums")}
                  />
                </span>
              ) : (
                <span className="text-meta text-ink-3">Off</span>
              )}
            </li>
          );
        })}
      </Rows>
    </div>
  );
}

const PURPOSE_LABELS: Record<string, string> = {
  "calendar.create": "New events",
  "calendar.read": "Read events",
  "calendar.write": "Update events",
  "mail.send": "Send mail",
  "mail.read": "Read mail",
  "files.read": "Read files",
  "files.write": "Save files",
};

function humanizePurpose(purpose: string): string {
  if (PURPOSE_LABELS[purpose]) return PURPOSE_LABELS[purpose];
  const words = purpose.replace(/[._-]+/g, " ").trim();
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : purpose;
}

function purposeService(purpose: string): "calendar" | "drive" | "gmail" | undefined {
  if (purpose.startsWith("calendar")) return "calendar";
  if (purpose.startsWith("mail")) return "gmail";
  if (purpose.startsWith("files")) return "drive";
  return undefined;
}

function AccountDefaults({
  defaults,
  onChange,
}: {
  defaults: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
}) {
  const connections = useResource<List<Connection>>("/connections", { poll: 0 });
  const purposes = Object.keys(defaults);
  if (!purposes.length) return null;
  const active = (connections.data?.items ?? []).filter((connection) => connection.status !== "disconnected");
  return (
    <Section title="Default accounts">
      <Rows label="Default accounts">
        {purposes.map((purpose) => {
          const displayLabel = humanizePurpose(purpose);
          const svc = purposeService(purpose);
          return (
            <li key={purpose} className="flex min-h-14 flex-wrap items-center gap-3 px-4 py-3">
              <AppBrandIcon service={svc} name={displayLabel} />
              <span className="min-w-0 flex-1 text-row font-semibold text-ink">{displayLabel}</span>
              <select
                aria-label={displayLabel}
                value={defaults[purpose]}
                onChange={(event) => onChange({ ...defaults, [purpose]: event.target.value })}
                className={cn(inputBase, "h-9 w-auto py-0")}
              >
                {active.map((connection) => (
                  <option key={connection.id} value={connection.id}>
                    {[connection.title, connection.account?.label].filter(Boolean).join(" · ")}
                  </option>
                ))}
              </select>
            </li>
          );
        })}
      </Rows>
    </Section>
  );
}

function ActionHistory() {
  const connections = useResource<List<Connection>>("/connections", { poll: 0 });
  const [account, setAccount] = useState("");
  const operations = useResource<List<Operation>>(
    `/operations${account ? `?connection_id=${encodeURIComponent(account)}` : ""}`,
    { poll: 30_000 },
  );
  const connectionItems = connections.data?.items ?? [];
  return (
    <>
      {connectionItems.length > 1 && (
        <label className="mb-4 flex items-center gap-2 text-meta">
          <span className="text-ink-2">Account</span>
          <select value={account} onChange={(event) => setAccount(event.target.value)} className={cn(inputBase, "w-auto")}>
            <option value="">All accounts</option>
            {connectionItems.map((connection) => (
              <option key={connection.id} value={connection.id}>
                {[connection.title, connection.account?.label].filter(Boolean).join(" · ")}
              </option>
            ))}
          </select>
        </label>
      )}
      {operations.loading ? (
        <Loading />
      ) : operations.error ? (
        <Failure error={operations.error} onRetry={() => void operations.reload()} />
      ) : operations.data && shownOperations(operations.data.items).length ? (
        <OperationRows operations={shownOperations(operations.data.items)} />
      ) : (
        <Empty>No actions yet.</Empty>
      )}
    </>
  );
}

const APPEARANCE_ICON: Record<NoriAppearance, typeof Monitor> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
};

function AppearanceSettings() {
  const [appearance, setAppearance] = useAppearance();
  return (
    <div className="rounded-card border border-card-border bg-surface p-4 sm:p-5">
      <div className="space-y-3 rounded-tile border border-card-border bg-canvas p-4">
        <div className="flex justify-end">
          <span className="rounded-full bg-accent px-3.5 py-2 text-[13px] font-medium text-on-accent">
            What do I have tomorrow?
          </span>
        </div>
        <div className="flex items-start gap-2.5">
          <NoriMark size={22} state="idle" />
          <p className="pt-0.5 text-[13px] leading-relaxed text-ink">
            Two morning meetings and a reminder at noon.
          </p>
        </div>
      </div>

      <div
        role="radiogroup"
        aria-label="Appearance"
        className="mt-4 grid grid-cols-3 gap-1 rounded-full border border-card-border bg-fill-1 p-1"
      >
        {APPEARANCE_OPTIONS.map((option) => {
          const Icon = APPEARANCE_ICON[option.id];
          const selected = appearance === option.id;
          return (
            <button
              key={option.id}
              type="button"
              role="radio"
              aria-checked={selected}
              onClick={() => setAppearance(option.id)}
              className={cn(
                "t-fast flex h-9 items-center justify-center gap-1.5 rounded-full text-[13px] font-semibold transition-[background-color,color,box-shadow]",
                selected
                  ? "border border-chip-selected-border bg-chip-selected-bg text-chip-selected-fg"
                  : "border border-transparent text-ink-2 hover:text-ink",
              )}
            >
              <Icon size={14} strokeWidth={2} aria-hidden />
              <span>{option.label}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

function AccountSettings({ userEmail }: { userEmail?: string }) {
  const { localMode, runtime, has, capabilities } = useAssistant();
  const [showDelete, setShowDelete] = useState(false);
  const reconnecting = runtime.reconnecting;
  const offline = !!runtime.error && !reconnecting;
  const statusPill = reconnecting
    ? { tone: "warning" as const, label: "Reconnecting" }
    : offline
      ? { tone: "danger" as const, label: "Offline" }
      : undefined;
  const entitlement = capabilities?.entitlement;

  if (showDelete) {
    return (
      <div className="space-y-4">
        <button
          type="button"
          onClick={() => setShowDelete(false)}
          className="inline-flex h-8 items-center gap-1.5 rounded-full bg-fill-1 px-3 text-meta font-semibold text-ink transition-colors hover:bg-fill-2"
        >
          <ArrowLeft size={14} aria-hidden />
          Account
        </button>
        <DeleteAccountForm />
      </div>
    );
  }

  return (
    <>
      <Rows label="Account">
        <Row
          leading={
            <span className="grid size-9 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
              <UserCircle size={18} strokeWidth={1.85} aria-hidden />
            </span>
          }
          title={userEmail || (localMode ? "Local account" : "Signed in")}
          trailing={statusPill ? <Pill tone={statusPill.tone}>{statusPill.label}</Pill> : undefined}
        />
        {entitlement && (
          <Row
            href="/settings/api-costs"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <BarChart3 size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Plan"
            trailing={<span className="text-meta font-medium text-ink-2">{PLAN_LABELS[entitlement.tier] ?? entitlement.tier}</span>}
          />
        )}
      </Rows>

      <Section title="System">
        <Rows label="System">
          {has("operations") && (
            <Row
              href="/settings/history"
              leading={
                <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                  <History size={18} strokeWidth={1.85} aria-hidden />
                </span>
              }
              title="Action History"
            />
          )}
          <Row
            href="/settings/diagnostics"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-fill-1 text-ink-2">
                <Activity size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Diagnostics"
            trailing={statusPill ? <Pill tone={statusPill.tone}>{statusPill.label}</Pill> : undefined}
          />
        </Rows>
      </Section>

      <Section title="Legal">
        <Rows label="Legal">
          <Row
            href={PRIVACY_URL}
            title="Privacy Policy"
            chevron={false}
            trailing={<ArrowUpRight size={15} className="text-ink-3" aria-hidden />}
          />
          <Row
            href={TERMS_URL}
            title="Terms of Service"
            chevron={false}
            trailing={<ArrowUpRight size={15} className="text-ink-3" aria-hidden />}
          />
        </Rows>
      </Section>

      <Section title="Data">
        <Rows label="Data">
          <Row
            href="/settings/reset"
            leading={
              <span className="grid size-9 place-items-center rounded-[10px] bg-danger-bg text-danger">
                <RotateCcw size={18} strokeWidth={1.85} aria-hidden />
              </span>
            }
            title="Reset data"
          />
          {!localMode && (
            <Row
              onClick={() => setShowDelete(true)}
              leading={
                <span className="grid size-9 place-items-center rounded-[10px] bg-danger-bg text-danger">
                  <UserCircle size={18} strokeWidth={1.85} aria-hidden />
                </span>
              }
              title="Delete account"
              chevron
            />
          )}
        </Rows>
      </Section>

      {!localMode && (
        <Section>
          <SignOutForm />
        </Section>
      )}
    </>
  );
}

function DeleteAccountForm() {
  const [confirmation, setConfirmation] = useState("");
  const [pending, setPending] = useState(false);
  const [deletePending, setDeletePending] = useState(false);
  const [error, setError] = useState("");

  const submit = async (resumePending: boolean) => {
    const email = confirmation.trim();
    if (!email || pending) return;
    setPending(true);
    setError("");
    try {
      await deleteAccount(email, resumePending);
      window.location.replace(ACCOUNT_DELETED_REDIRECT);
    } catch (reason) {
      if (reason instanceof APIError && reason.code === "account_delete_pending") {
        setDeletePending(true);
      }
      setError(
        reason instanceof APIError
          ? errorText(reason)
          : "Could not reach Nori to confirm account deletion. Try again.",
      );
      setPending(false);
    }
  };

  return (
    <form
      className="space-y-4 rounded-card border border-card-border bg-surface p-4 sm:p-5"
      onSubmit={(event) => {
        event.preventDefault();
        void submit(deletePending);
      }}
    >
      <p className="text-body text-ink-2">
        Permanently deletes your account and all Nori data, including chats, memories, work, files, schedules, preferences, and saved API keys.
      </p>
      {deletePending && (
        <Notice tone="warning">
          Your account deletion was interrupted. Retry below to finish removing your account.
        </Notice>
      )}
      <Field label="Type your account email to confirm">
        <input
          type="email"
          autoComplete="off"
          placeholder="you@example.com"
          className={inputClass}
          value={confirmation}
          onChange={(event) => setConfirmation(event.target.value)}
          disabled={pending}
        />
      </Field>
      {error && <Notice tone="danger">{error}</Notice>}
      <Button type="submit" variant="danger" disabled={!confirmation.trim() || pending}>
        {pending ? "Deleting…" : deletePending ? "Finish deleting account" : "Delete account"}
      </Button>
    </form>
  );
}

function ResetData() {
  const { scope } = useAssistantConfig();
  const [confirmation, setConfirmation] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");

  const reset = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (confirmation !== "RESET" || pending) return;
    setPending(true);
    setError("");
    try {
      await resetUserData();
      markBrowserReset(scope);
      window.location.replace("/");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "The reset could not be completed.");
      setPending(false);
    }
  };

  return (
    <form onSubmit={(event) => void reset(event)} className="space-y-5 rounded-card border border-card-border bg-surface p-4 sm:p-5">
      <p className="text-body text-ink-2">
        This permanently clears your chats, memories, work, files, schedules, preferences, saved API keys, and model choices from Nori and disconnects your apps. You’ll stay signed in with Google and start onboarding again.
      </p>
      <Field label="Type RESET to confirm">
        <input
          autoComplete="off"
          className={inputClass}
          value={confirmation}
          onChange={(event) => setConfirmation(event.target.value)}
          disabled={pending}
        />
      </Field>
      {error && <Notice tone="danger">{error}</Notice>}
      <Button type="submit" variant="danger" disabled={confirmation !== "RESET" || pending}>
        {pending ? "Resetting…" : "Reset data"}
      </Button>
    </form>
  );
}

function Diagnostics() {
  const { capabilities, debug, setDebug, localMode, refreshCapabilities } = useAssistantConfig();
  const features = capabilities?.assistant_features ?? {};
  const [checking, setChecking] = useState(false);
  const [healthResult, setHealthResult] = useState<{
    ok: boolean;
    latencyMs: number;
    checkedAt: string;
    error?: string;
  }>();

  const checkHealth = async () => {
    if (checking) return;
    setChecking(true);
    const start = performance.now();
    try {
      const response = await fetch("/api/v1/capabilities", { cache: "no-store" });
      const latencyMs = Math.max(1, Math.round(performance.now() - start));
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      await refreshCapabilities();
      setHealthResult({ ok: true, latencyMs, checkedAt: new Date().toISOString() });
    } catch (reason) {
      const latencyMs = Math.max(1, Math.round(performance.now() - start));
      setHealthResult({
        ok: false,
        latencyMs,
        checkedAt: new Date().toISOString(),
        error: reason instanceof Error ? reason.message : "Server unreachable",
      });
    } finally {
      setChecking(false);
    }
  };

  return (
    <>
      <Rows label="Chat debug">
        <Row
          title="Show model and tool details in Chat"
          trailing={
            <Switch
              checked={debug}
              label="Show model and tool details in Chat"
              onCheckedChange={setDebug}
            />
          }
        />
      </Rows>
      <Section
        title="This Nori"
        actions={
          <button
            type="button"
            disabled={checking}
            onClick={() => void checkHealth()}
            className="text-meta font-semibold text-accent-text hover:underline disabled:opacity-50"
          >
            {checking ? "Checking…" : "Check health"}
          </button>
        }
      >
        <Rows label="This Nori">
          <Row title="Runtime" trailing={<span className="text-meta text-ink-2">{localMode ? "Local" : "Hosted"}</span>} />
          {healthResult && (
            <Row
              title="Health"
              trailing={
                <span className={cn("text-meta font-medium", healthResult.ok ? "text-ok" : "text-danger")}>
                  {healthResult.ok ? `Healthy · ${healthResult.latencyMs} ms` : `${healthResult.error ?? "Unreachable"} · ${healthResult.latencyMs} ms`}
                </span>
              }
            />
          )}
          <Row
            title="Provider"
            trailing={
              <span className="font-mono text-mono-data text-ink-2">
                {[capabilities?.provider, capabilities?.model].filter(Boolean).join(" · ") || "unknown"}
              </span>
            }
          />
          <Row
            title="Contracts"
            trailing={
              <span className="font-mono text-mono-data text-ink-2">
                {[capabilities?.contract, capabilities?.conversationContract, capabilities?.assistant_contract]
                  .filter(Boolean)
                  .join(" · ") || "unknown"}
              </span>
            }
          />
          <Row
            title="Available"
            detail={
              Object.entries(features)
                .filter(([, on]) => on)
                .map(([name]) => name)
                .join(", ") || "Chat and tasks only"
            }
          />
        </Rows>
      </Section>
    </>
  );
}
