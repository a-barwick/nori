"use client";

import { useState } from "react";
import { Button } from "@/components/ui/primitives";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { formatTime12h } from "@/lib/assistant/format";
import { useAction, useResource } from "@/lib/assistant/useResource";
import type {
  Preferences,
  PreferencesEditable,
  ProactivityPreferences,
  PushPolicy,
  QuietHours,
} from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { Failure, inputBase, Loading, Notice, Row, Rows, Section, Switch } from "./ui";

export const DEFAULT_QUIET_HOURS: QuietHours = {
  enabled: true,
  start: "21:00",
  end: "07:30",
};

export const DEFAULT_MORNING_BRIEF_TIME = "08:00";

export function isPushChatsEnabled(proactivity?: ProactivityPreferences): boolean {
  if (proactivity?.push_chats !== undefined) return proactivity.push_chats;
  return proactivity?.push_policy !== "quiet_only";
}

export function isPushUrgentAndApprovalsEnabled(proactivity?: ProactivityPreferences): boolean {
  if (proactivity?.push_urgent_and_approvals !== undefined) return proactivity.push_urgent_and_approvals;
  return proactivity?.push_policy !== "quiet_only";
}

export function isPushMorningBriefEnabled(proactivity?: ProactivityPreferences): boolean {
  if (proactivity?.push_morning_brief !== undefined) return proactivity.push_morning_brief;
  return proactivity?.push_policy === "include_brief";
}

export function derivePushPolicy({
  chats,
  urgent,
  brief,
}: {
  chats: boolean;
  urgent: boolean;
  brief: boolean;
}): PushPolicy {
  if (!chats && !urgent && !brief) return "quiet_only";
  if (brief) return "include_brief";
  return "urgent_and_approvals";
}

export function formatQuietHours(quietHours?: QuietHours): string {
  if (quietHours && quietHours.enabled === false) return "Off";
  const start = quietHours?.start || DEFAULT_QUIET_HOURS.start;
  const end = quietHours?.end || DEFAULT_QUIET_HOURS.end;
  return `${formatTime12h(start)} – ${formatTime12h(end)}`;
}

export function formatMorningBrief(proactivity?: ProactivityPreferences): string {
  if (proactivity?.morning_brief_enabled === false) return "Off";
  const time = proactivity?.morning_brief_time || DEFAULT_MORNING_BRIEF_TIME;
  return formatTime12h(time);
}

export function editablePreferences(preferences: Preferences): PreferencesEditable {
  const {
    revision: _revision,
    updated_at: _updated,
    data_reset_at: _resetAt,
    data_reset_pending: _resetPending,
    ...value
  } = preferences;
  void _revision;
  void _updated;
  void _resetAt;
  void _resetPending;
  return value;
}

export async function saveProactivityPreferences(
  current: Preferences,
  patch: Partial<Pick<PreferencesEditable, "quiet_hours" | "proactivity">>,
): Promise<Preferences> {
  const base = editablePreferences(current);
  const next: PreferencesEditable = {
    ...base,
    ...(patch.quiet_hours !== undefined ? { quiet_hours: patch.quiet_hours } : {}),
    ...(patch.proactivity !== undefined ? { proactivity: patch.proactivity } : {}),
  };
  const receipt = await mutate<Preferences>(
    "/preferences",
    {
      request_id: newRequestID(),
      expected_revision: current.revision,
      value: next,
    },
    "PUT",
  );
  return receipt.resource as Preferences;
}

export function NotificationsSettings({
  current,
  onSaved,
  pushAvailable,
}: {
  current?: Preferences;
  pushAvailable?: boolean;
  onSaved?: (next: Preferences) => void;
}) {
  const remote = useResource<Preferences>(current ? null : "/preferences", { poll: 0 });
  const prefs = current ?? remote.data;
  const applySaved = onSaved ?? remote.apply;

  const action = useAction();
  const [editing, setEditing] = useState<"quiet_hours" | "morning_brief" | null>(null);
  const [quietEnabled, setQuietEnabled] = useState(true);
  const [quietStart, setQuietStart] = useState(DEFAULT_QUIET_HOURS.start);
  const [quietEnd, setQuietEnd] = useState(DEFAULT_QUIET_HOURS.end);
  const [briefEnabled, setBriefEnabled] = useState(true);
  const [briefTime, setBriefTime] = useState(DEFAULT_MORNING_BRIEF_TIME);

  if (!current && remote.loading && !remote.data) return <Loading />;
  if (!prefs) {
    return (
      <Failure
        error={remote.error ?? "Notification settings unavailable."}
        onRetry={() => void remote.reload()}
      />
    );
  }

  const pushChats = isPushChatsEnabled(prefs.proactivity);
  const pushUrgent = isPushUrgentAndApprovalsEnabled(prefs.proactivity);
  const briefScheduleEnabled = prefs.proactivity?.morning_brief_enabled !== false;
  const pushBrief = briefScheduleEnabled && isPushMorningBriefEnabled(prefs.proactivity);
  const allPushOff = !pushChats && !pushUrgent && !pushBrief;

  const commit = (
    name: string,
    patch: Partial<Pick<PreferencesEditable, "quiet_hours" | "proactivity">>,
    announcement: string,
  ) => {
    void action.run(
      name,
      async () => {
        try {
          const updated = await saveProactivityPreferences(prefs, patch);
          applySaved(updated);
          setEditing(null);
        } catch (error) {
          if (error instanceof APIError && error.stale && error.body?.current) {
            applySaved(error.body.current as Preferences);
          }
          throw error;
        }
      },
      announcement,
    );
  };

  const togglePush = (
    key: "push_chats" | "push_urgent_and_approvals" | "push_morning_brief",
    nextValue: boolean,
    label: string,
  ) => {
    if (action.pending) return;
    const nextChats = key === "push_chats" ? nextValue : pushChats;
    const nextUrgent = key === "push_urgent_and_approvals" ? nextValue : pushUrgent;
    const nextBrief = key === "push_morning_brief" ? nextValue : isPushMorningBriefEnabled(prefs.proactivity);
    commit(
      key,
      {
        proactivity: {
          ...prefs.proactivity,
          push_chats: nextChats,
          push_urgent_and_approvals: nextUrgent,
          push_morning_brief: nextBrief,
          push_policy: derivePushPolicy({ chats: nextChats, urgent: nextUrgent, brief: nextBrief }),
        },
      },
      `${label} notifications ${nextValue ? "enabled" : "disabled"}.`,
    );
  };

  const openQuietEditor = () => {
    setQuietEnabled(prefs.quiet_hours?.enabled !== false);
    setQuietStart(prefs.quiet_hours?.start || DEFAULT_QUIET_HOURS.start);
    setQuietEnd(prefs.quiet_hours?.end || DEFAULT_QUIET_HOURS.end);
    setEditing("quiet_hours");
  };

  const openBriefEditor = () => {
    setBriefEnabled(prefs.proactivity?.morning_brief_enabled !== false);
    setBriefTime(prefs.proactivity?.morning_brief_time || DEFAULT_MORNING_BRIEF_TIME);
    setEditing("morning_brief");
  };

  return (
    <div className="space-y-6">
      {action.error && <Notice tone="danger">{action.error}</Notice>}

      <Section title="Push notifications">
        {pushAvailable === false ? (
          <p className="px-1 text-body text-ink-2">Push delivery is unavailable. Updates still appear in Nori.</p>
        ) : (
          <>
            <Rows label="Push notifications">
              <li className="flex items-center justify-between gap-3 px-4 py-3">
                <span className="min-w-0 flex-1 text-row font-semibold text-ink">Chats</span>
                <Switch
                  checked={pushChats}
                  disabled={!!action.pending}
                  label="Chats"
                  onCheckedChange={(next) => togglePush("push_chats", next, "Chats")}
                />
              </li>
              <li className="flex items-center justify-between gap-3 px-4 py-3">
                <span className="min-w-0 flex-1 text-row font-semibold text-ink">Urgent &amp; approvals</span>
                <Switch
                  checked={pushUrgent}
                  disabled={!!action.pending}
                  label="Urgent & approvals"
                  onCheckedChange={(next) => togglePush("push_urgent_and_approvals", next, "Urgent & approvals")}
                />
              </li>
              <li
                className={cn(
                  "flex items-center justify-between gap-3 px-4 py-3",
                  !briefScheduleEnabled && "opacity-50",
                )}
              >
                <span className="min-w-0 flex-1 text-row font-semibold text-ink">Morning brief</span>
                <Switch
                  checked={pushBrief}
                  disabled={!!action.pending || !briefScheduleEnabled}
                  label="Morning brief"
                  onCheckedChange={(next) => togglePush("push_morning_brief", next, "Morning brief")}
                />
              </li>
            </Rows>
            {allPushOff && (
              <p className="mt-2 px-1 text-caption text-ink-3">
                Push notifications are off. Updates still appear in Chat.
              </p>
            )}
          </>
        )}
      </Section>

      <Section title="Schedule">
        <Rows label="Schedule">
          {editing === "quiet_hours" ? (
            <li className="px-4 py-3">
              <form
                className="flex flex-wrap items-center gap-2"
                onSubmit={(event) => {
                  event.preventDefault();
                  commit(
                    "quiet_hours",
                    {
                      quiet_hours: {
                        enabled: quietEnabled,
                        start: quietStart,
                        end: quietEnd,
                      },
                    },
                    "Quiet hours updated.",
                  );
                }}
              >
                <span className="min-w-0 flex-1 text-row font-semibold text-ink">Quiet hours</span>
                <button
                  type="button"
                  onClick={() => setQuietEnabled((on) => !on)}
                  className={cn(
                    "inline-flex h-8 items-center rounded-full px-3 text-caption font-semibold transition-colors",
                    quietEnabled ? "bg-accent-subtle text-accent-text" : "bg-fill-1 text-ink-2",
                  )}
                >
                  {quietEnabled ? "On" : "Off"}
                </button>
                {quietEnabled && (
                  <span className="flex items-center gap-1.5">
                    <input
                      aria-label="Quiet hours start"
                      type="time"
                      value={quietStart}
                      disabled={!!action.pending}
                      onChange={(event) => setQuietStart(event.target.value)}
                      className={cn(inputBase, "h-9 w-[6.5rem] px-2 py-0 tabular-nums")}
                    />
                    <span className="text-ink-3" aria-hidden>
                      –
                    </span>
                    <input
                      aria-label="Quiet hours end"
                      type="time"
                      value={quietEnd}
                      disabled={!!action.pending}
                      onChange={(event) => setQuietEnd(event.target.value)}
                      className={cn(inputBase, "h-9 w-[6.5rem] px-2 py-0 tabular-nums")}
                    />
                  </span>
                )}
                <Button type="submit" size="sm" variant="primary" disabled={!!action.pending}>
                  {action.pending === "quiet_hours" ? "Saving…" : "Save"}
                </Button>
                <Button type="button" size="sm" variant="quiet" disabled={!!action.pending} onClick={() => setEditing(null)}>
                  Cancel
                </Button>
              </form>
            </li>
          ) : (
            <Row
              title="Quiet hours"
              onClick={openQuietEditor}
              chevron
              trailing={
                <span className="text-meta font-medium tabular-nums text-ink-2">
                  {formatQuietHours(prefs.quiet_hours)}
                </span>
              }
            />
          )}

          {editing === "morning_brief" ? (
            <li className="px-4 py-3">
              <form
                className="flex flex-wrap items-center gap-2"
                onSubmit={(event) => {
                  event.preventDefault();
                  commit(
                    "morning_brief",
                    {
                      proactivity: {
                        ...prefs.proactivity,
                        morning_brief_enabled: briefEnabled,
                        morning_brief_time: briefTime,
                      },
                    },
                    "Morning brief updated.",
                  );
                }}
              >
                <span className="min-w-0 flex-1 text-row font-semibold text-ink">Morning brief</span>
                <button
                  type="button"
                  onClick={() => setBriefEnabled((on) => !on)}
                  className={cn(
                    "inline-flex h-8 items-center rounded-full px-3 text-caption font-semibold transition-colors",
                    briefEnabled ? "bg-accent-subtle text-accent-text" : "bg-fill-1 text-ink-2",
                  )}
                >
                  {briefEnabled ? "On" : "Off"}
                </button>
                {briefEnabled && (
                  <input
                    aria-label="Morning brief time"
                    type="time"
                    value={briefTime}
                    disabled={!!action.pending}
                    onChange={(event) => setBriefTime(event.target.value)}
                    className={cn(inputBase, "h-9 w-[6.5rem] px-2 py-0 tabular-nums")}
                  />
                )}
                <Button type="submit" size="sm" variant="primary" disabled={!!action.pending}>
                  {action.pending === "morning_brief" ? "Saving…" : "Save"}
                </Button>
                <Button type="button" size="sm" variant="quiet" disabled={!!action.pending} onClick={() => setEditing(null)}>
                  Cancel
                </Button>
              </form>
            </li>
          ) : (
            <Row
              title="Morning brief"
              onClick={openBriefEditor}
              chevron
              trailing={
                <span className="text-meta font-medium tabular-nums text-ink-2">
                  {formatMorningBrief(prefs.proactivity)}
                </span>
              }
            />
          )}
        </Rows>
      </Section>
    </div>
  );
}
