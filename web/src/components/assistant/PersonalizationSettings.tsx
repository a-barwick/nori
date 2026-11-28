"use client";

import { useEffect, useRef, useState } from "react";
import { Check } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { announce } from "@/lib/assistant/announce";
import { APIError, mutate, newRequestID } from "@/lib/assistant/api";
import { useAction, useResource } from "@/lib/assistant/useResource";
import type {
  PersonalizationPreferences,
  Preferences,
  PreferencesEditable,
} from "@/lib/contracts/assistant";
import { cn } from "@/lib/utils";
import { FOCUS_OPTIONS, REPLY_STYLE_OPTIONS } from "./OnboardingSteps";
import { editablePreferences } from "./ProactivitySettings";
import { Failure, Loading, Notice, Row, Rows, Section } from "./ui";

export const RESPONSE_STYLE_CHOICES: readonly { id: (typeof REPLY_STYLE_OPTIONS)[number]; detail: string }[] = [
  { id: "Concise & direct", detail: "Short, straight to the point, minimal filler" },
  { id: "Warm & conversational", detail: "Friendly, natural tone with a bit more context" },
  { id: "Bullets first", detail: "Structured takeaways up front for quick scanning" },
];

function normalizedPersonalization(p?: PersonalizationPreferences): {
  name: string;
  replyStyle: string;
  customInstructions: string;
  focus: string[];
  workingOn: string;
} {
  return {
    name: p?.name?.trim() ?? "",
    replyStyle: p?.reply_style?.trim() ?? "",
    customInstructions: p?.custom_instructions?.trim() ?? "",
    focus: [...(p?.focus ?? [])],
    workingOn: p?.working_on?.trim() ?? "",
  };
}

export function PersonalizationSettings() {
  const preferences = useResource<Preferences>("/preferences", { poll: 0 });
  if (preferences.loading && !preferences.data) return <Loading />;
  if (!preferences.data) {
    return (
      <Failure
        error={preferences.error ?? "Personalization settings unavailable."}
        onRetry={() => void preferences.reload()}
      />
    );
  }
  return <PersonalizationEditor current={preferences.data} onSaved={preferences.apply} />;
}

export function PersonalizationEditor({
  current,
  onSaved,
}: {
  current: Preferences;
  onSaved: (next: Preferences) => void;
}) {
  const action = useAction();
  const saved = normalizedPersonalization(current.personalization);

  const [name, setName] = useState(saved.name);
  const [replyStyle, setReplyStyle] = useState(saved.replyStyle);
  const [customInstructions, setCustomInstructions] = useState(saved.customInstructions);
  const [focus, setFocus] = useState<string[]>(saved.focus);
  const [workingOn, setWorkingOn] = useState(saved.workingOn);
  const [conflict, setConflict] = useState<Preferences>();
  const [justSaved, setJustSaved] = useState(false);
  const savedTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (savedTimer.current) clearTimeout(savedTimer.current);
    };
  }, []);

  const syncFromPreferences = (prefs: Preferences) => {
    const next = normalizedPersonalization(prefs.personalization);
    setName(next.name);
    setReplyStyle(next.replyStyle);
    setCustomInstructions(next.customInstructions);
    setFocus(next.focus);
    setWorkingOn(next.workingOn);
    setConflict(undefined);
  };

  const textDirty =
    name.trim() !== saved.name ||
    customInstructions.trim() !== saved.customInstructions ||
    workingOn.trim() !== saved.workingOn;

  const commit = (overrides?: Partial<PersonalizationPreferences>, onFailure?: () => void) => {
    if (action.pending) return;

    const nextPersonalization: PersonalizationPreferences = {
      name: (overrides?.name !== undefined ? overrides.name : name).trim() || undefined,
      reply_style: (overrides?.reply_style !== undefined ? overrides.reply_style : replyStyle).trim() || undefined,
      custom_instructions:
        (overrides?.custom_instructions !== undefined ? overrides.custom_instructions : customInstructions).trim() ||
        undefined,
      focus: (() => {
        const list = overrides?.focus !== undefined ? overrides.focus : focus;
        return list.length > 0 ? list : undefined;
      })(),
      working_on: (overrides?.working_on !== undefined ? overrides.working_on : workingOn).trim() || undefined,
    };

    const base: PreferencesEditable = editablePreferences(current);
    const nextValue: PreferencesEditable = {
      ...base,
      personalization: nextPersonalization,
    };

    void action.run("save", async () => {
      try {
        const receipt = await mutate<Preferences>(
          "/preferences",
          {
            request_id: newRequestID(),
            expected_revision: current.revision,
            value: nextValue,
          },
          "PUT",
        );
        const updated = receipt.resource as Preferences;
        onSaved(updated);
        syncFromPreferences(updated);
        setJustSaved(true);
        announce("Personalization saved");
        if (savedTimer.current) clearTimeout(savedTimer.current);
        savedTimer.current = setTimeout(() => setJustSaved(false), 1800);
      } catch (error) {
        onFailure?.();
        if (error instanceof APIError && error.stale && error.body?.current) {
          setConflict(error.body.current as Preferences);
        }
        throw error;
      }
    });
  };

  const selectStyle = (style: string) => {
    if (action.pending) return;
    const prev = replyStyle;
    const next = replyStyle === style ? "" : style;
    setReplyStyle(next);
    commit({ reply_style: next }, () => setReplyStyle(prev));
  };

  const toggleFocus = (item: string) => {
    if (action.pending) return;
    const prev = focus;
    const next = focus.includes(item) ? focus.filter((entry) => entry !== item) : [...focus, item];
    setFocus(next);
    commit({ focus: next }, () => setFocus(prev));
  };

  return (
    <div className="space-y-6">
      {conflict && (
        <Notice
          tone="warning"
          action={
            <Button
              size="sm"
              onClick={() => {
                onSaved(conflict);
                syncFromPreferences(conflict);
              }}
            >
              Load latest
            </Button>
          }
        >
          These changed on another device. Load the latest to edit them.
        </Notice>
      )}
      {action.error && !conflict && <Notice tone="danger">{action.error}</Notice>}

      <Section
        title="Response style"
        actions={
          textDirty ? (
            <Button
              size="sm"
              variant="primary"
              disabled={!!action.pending}
              onClick={() => commit()}
            >
              {action.pending ? "Saving…" : "Save"}
            </Button>
          ) : justSaved ? (
            <span className="text-caption font-semibold text-ok" role="status">
              Saved
            </span>
          ) : undefined
        }
      >
        <Rows label="Response style">
          {RESPONSE_STYLE_CHOICES.map((choice) => {
            const selected = replyStyle === choice.id;
            return (
              <Row
                key={choice.id}
                onClick={action.pending ? undefined : () => selectStyle(choice.id)}
                title={choice.id}
                detail={choice.detail}
                trailing={
                  selected ? (
                    <Check size={16} strokeWidth={2.2} className="text-accent-text" aria-hidden />
                  ) : undefined
                }
                chevron={false}
                className={action.pending ? "opacity-60" : undefined}
              />
            );
          })}
        </Rows>
      </Section>

      <Section title="Custom instructions">
        <Rows label="Custom instructions">
          <li className="px-4 py-3.5">
            <label htmlFor="personalization-custom-instructions" className="sr-only">
              Custom instructions
            </label>
            <textarea
              id="personalization-custom-instructions"
              rows={3}
              maxLength={4000}
              value={customInstructions}
              onChange={(event) => setCustomInstructions(event.target.value)}
              onKeyDown={(event) => {
                if ((event.metaKey || event.ctrlKey) && event.key === "Enter" && textDirty && !action.pending) {
                  event.preventDefault();
                  commit();
                }
              }}
              placeholder="How should Nori respond? Add formatting preferences, tone guidelines, or rules to always follow."
              className="block w-full resize-y bg-transparent text-body leading-relaxed text-ink placeholder:text-ink-3 focus:outline-none"
            />
          </li>
        </Rows>
        <p className="mt-2 px-1 text-caption text-ink-3">
          Applied to every conversation alongside your response style.
        </p>
      </Section>

      <Section title="About you">
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (textDirty && !action.pending) commit();
          }}
        >
          <Rows label="About you">
            <li className="flex min-h-14 items-center gap-3 px-4 py-3">
              <label htmlFor="personalization-name" className="shrink-0 text-row font-semibold text-ink">
                Name
              </label>
              <input
                id="personalization-name"
                type="text"
                maxLength={120}
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="What should Nori call you?"
                className="min-w-0 flex-1 bg-transparent text-right text-body text-ink placeholder:text-ink-3 focus:outline-none"
              />
            </li>
            <li className="flex min-h-14 items-center gap-3 px-4 py-3">
              <label htmlFor="personalization-working-on" className="shrink-0 text-row font-semibold text-ink">
                Working on
              </label>
              <input
                id="personalization-working-on"
                type="text"
                maxLength={240}
                value={workingOn}
                onChange={(event) => setWorkingOn(event.target.value)}
                placeholder="Current project or role"
                className="min-w-0 flex-1 bg-transparent text-right text-body text-ink placeholder:text-ink-3 focus:outline-none"
              />
            </li>
            <li className="px-4 py-3.5">
              <span id="personalization-focus-label" className="mb-2.5 block text-meta font-semibold text-ink-2">
                Focus
              </span>
              <div className="flex flex-wrap gap-2" role="group" aria-labelledby="personalization-focus-label">
                {FOCUS_OPTIONS.map((option) => {
                  const selected = focus.includes(option);
                  return (
                    <button
                      key={option}
                      type="button"
                      disabled={!!action.pending}
                      aria-pressed={selected}
                      onClick={() => toggleFocus(option)}
                      className={cn(
                        "t-fast cursor-pointer rounded-full border px-3.5 py-1.5 text-meta font-medium disabled:opacity-60",
                        selected
                          ? "border-chip-selected-border bg-chip-selected-bg font-semibold text-chip-selected-fg"
                          : "border-card-border bg-fill-1 text-ink-2 hover:text-ink",
                      )}
                    >
                      {option}
                    </button>
                  );
                })}
              </div>
            </li>
          </Rows>

          {textDirty && (
            <div className="mt-3 flex justify-end">
              <Button type="submit" size="sm" variant="primary" disabled={!!action.pending}>
                {action.pending ? "Saving…" : "Save changes"}
              </Button>
            </div>
          )}
        </form>
      </Section>

      <Section>
        <Rows label="Memory">
          <Row
            href="/memory"
            title="Memory"
            detail="Manage details Nori learns automatically from your chats"
          />
        </Rows>
      </Section>
    </div>
  );
}
