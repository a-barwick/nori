"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { MarkMood } from "@/components/mark/NoriMark";
import { useAppearance, type NoriAppearance } from "@/lib/appearance";
import {
  errorText,
  mutate,
  newRequestID,
  saveModelKey,
  type HomeConnectors,
  type ModelKeyProvider,
  type ModelKeysResponse,
  type ModelSettings,
} from "@/lib/assistant/api";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { MemoryFact, Preferences, PreferencesEditable } from "@/lib/contracts/assistant";
import type { MarkState } from "@/lib/types";
import { cn } from "@/lib/utils";
import { hasSelectedHomeChat } from "./ModelsSettings";
import {
  AboutYouStep,
  AppearanceStep,
  FOCUS_OPTIONS,
  ModelSetupStep,
  REPLY_STYLE_OPTIONS,
  WelcomeStep,
} from "./OnboardingSteps";

export { FOCUS_OPTIONS, REPLY_STYLE_OPTIONS };

export type OnboardingStep = 1 | 2 | 3 | 4;

export interface OnboardingMemorySeed {
  section: "Preferences" | "Context";
  text: string;
  pinned?: boolean;
}

export function buildOnboardingMemoryFacts(input: {
  name: string;
  replyStyle: string;
  focus: readonly string[];
  workingOn: string;
}): OnboardingMemorySeed[] {
  const facts: OnboardingMemorySeed[] = [];
  const name = input.name.trim();
  const style = input.replyStyle.trim();
  const workingOn = input.workingOn.trim();

  if (name) {
    facts.push({ section: "Preferences", text: `The user's name is ${name}.` });
  }
  if (style) {
    facts.push({ section: "Preferences", text: `Prefers ${style.toLowerCase()} replies.`, pinned: true });
  }
  if (input.focus.length > 0) {
    facts.push({
      section: "Context",
      text: `Uses Nori for ${input.focus.map((item) => item.toLowerCase()).join(", ")}.`,
    });
  }
  if (workingOn) {
    facts.push({ section: "Context", text: `Currently working on: ${workingOn}` });
  }
  return facts;
}

export function shouldShowOnboarding(params: {
  forceReplay: boolean;
  preferences?: Preferences;
  preferencesLoaded: boolean;
  messageCount: number;
  sessionLoaded: boolean;
  localCompleted: boolean;
}): boolean {
  if (params.forceReplay) return true;
  if (params.localCompleted) return false;
  if (!params.preferencesLoaded || !params.sessionLoaded) return false;
  if (params.preferences?.onboarding_completed_at) return false;
  if (params.preferences?.data_reset_at) return true;
  if (params.messageCount > 0) return false;
  return true;
}

export function OnboardingFlow({
  preferences,
  onComplete,
  initialStep = 1,
}: {
  preferences?: Preferences;
  onComplete: () => void;
  initialStep?: OnboardingStep;
}) {
  const [step, setStep] = useState<OnboardingStep>(initialStep);
  const [appearance, setAppearance] = useAppearance();
  const keys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 0, versioned: false });
  const models = useResource<ModelSettings>("/settings/models", { poll: 0 });
  const home = useResource<HomeConnectors>("/settings/home-connectors", { poll: 0, versioned: false });
  const [showModels, setShowModels] = useState(false);
  const invalidate = useResourceInvalidation();

  const [mood, setMood] = useState<MarkMood | undefined>("hello");
  const [moodKey, setMoodKey] = useState(1);
  const [happy, setHappy] = useState(false);
  const [sparkleKey, setSparkleKey] = useState(1);
  const happyTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const [draftKey, setDraftKey] = useState("");
  const [isReplacingKey, setIsReplacingKey] = useState(false);
  const [validatingKey, setValidatingKey] = useState(false);
  const [keyError, setKeyError] = useState<string | null>(null);
  const [keyFocused, setKeyFocused] = useState(false);
  const [selectedProvider, setSelectedProvider] = useState("");
  const [savedProvider, setSavedProvider] = useState<{ id: string; label: string } | null>(null);

  const [name, setName] = useState("");
  const [replyStyle, setReplyStyle] = useState<string>(REPLY_STYLE_OPTIONS[0]);
  const [focus, setFocus] = useState<string[]>([]);
  const [workingOn, setWorkingOn] = useState("");
  const [finishing, setFinishing] = useState(false);
  const [finishError, setFinishError] = useState<string | null>(null);
  const lastSaveAboutYou = useRef(true);
  const savedFacts = useRef(new Set<string>());
  const savedPreferences = useRef(false);

  useEffect(() => {
    return () => {
      if (happyTimer.current) clearTimeout(happyTimer.current);
    };
  }, []);

  const triggerMood = useCallback(
    (nextMood: MarkMood, opts?: { happy?: boolean; sparkles?: boolean; durationMs?: number }) => {
      setMood(nextMood);
      setMoodKey((k) => k + 1);
      if (opts?.sparkles) setSparkleKey((k) => k + 1);
      if (opts?.happy) {
        setHappy(true);
        if (happyTimer.current) clearTimeout(happyTimer.current);
        happyTimer.current = setTimeout(() => setHappy(false), opts.durationMs ?? 700);
      }
    },
    [],
  );

  const primaryProvider: ModelKeyProvider | undefined =
    keys.data?.providers.find((provider) => provider.provider === selectedProvider) ??
    keys.data?.providers.find((provider) => provider.configured) ??
    keys.data?.providers[0];
  const byokRequired = !!keys.data?.byok_required;
  const keyConfigured = !!primaryProvider?.configured && !isReplacingKey;
  const canContinueStep3 =
    (!byokRequired ||
      ((!!keys.data?.providers.some((provider) => provider.configured) || hasSelectedHomeChat(models.data)) &&
        !isReplacingKey)) &&
    !savedProvider;

  const goStep = (next: OnboardingStep) => {
    setStep(next);
    if (next === 1) triggerMood("hello", { sparkles: true });
    else if (next === 2 || next === 4) triggerMood("nod");
  };

  const handleAppearancePick = (next: NoriAppearance) => {
    setAppearance(next);
    triggerMood("hop", { happy: true, durationMs: 600 });
  };

  const handleVerifyKey = async () => {
    const provider = primaryProvider;
    if (!provider) {
      setKeyError("Model providers are unavailable. Try again shortly.");
      return;
    }
    const trimmed = draftKey.trim();
    if (!trimmed) {
      setKeyError("Enter an API key.");
      triggerMood("shake");
      return;
    }
    setKeyError(null);
    setValidatingKey(true);
    try {
      const updated = await saveModelKey(provider.provider, trimmed);
      setDraftKey("");
      setIsReplacingKey(false);
      if (keys.data) {
        keys.apply({
          ...keys.data,
          providers: keys.data.providers.map((item) => (item.provider === updated.provider ? updated : item)),
        });
      }
      invalidate("/settings/model-keys", "/settings/models", "/capabilities");
      setSavedProvider({ id: provider.provider, label: provider.label });
      triggerMood("hop", { happy: true, sparkles: true, durationMs: 850 });
    } catch (err) {
      setKeyError(errorText(err));
      triggerMood("shake");
    } finally {
      setValidatingKey(false);
    }
  };

  const toggleFocus = (item: string) => {
    setFocus((prev) => (prev.includes(item) ? prev.filter((v) => v !== item) : [...prev, item]));
    triggerMood("nod");
  };

  const finishOnboarding = async (saveAboutYou: boolean) => {
    if (finishing) return;
    lastSaveAboutYou.current = saveAboutYou;
    setFinishing(true);
    setFinishError(null);

    const failures: string[] = [];
    const tasks: Promise<void>[] = [];

    if (saveAboutYou) {
      const facts = buildOnboardingMemoryFacts({ name, replyStyle, focus, workingOn });
      for (const fact of facts) {
        const key = `${fact.section}:${fact.text}`;
        if (savedFacts.current.has(key)) continue;
        tasks.push(
          mutate<MemoryFact>("/memory", {
            request_id: newRequestID(),
            expected_revision: 0,
            value: {
              text: fact.text,
              section: fact.section,
              pinned: fact.pinned ?? false,
            },
          })
            .then(() => {
              savedFacts.current.add(key);
            })
            .catch((err) => {
              failures.push(errorText(err));
            }),
        );
      }
    }

    if (!savedPreferences.current) {
      const detectedZone = (() => {
        try {
          return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
        } catch {
          return undefined;
        }
      })();

      const currentPrefs = preferences;
      const editablePrefs: PreferencesEditable = {
        timezone: detectedZone || currentPrefs?.timezone || "UTC",
        locale: currentPrefs?.locale,
        working_hours: currentPrefs?.working_hours ?? [
          { day: 1, start: "09:00", end: "17:00" },
          { day: 2, start: "09:00", end: "17:00" },
          { day: 3, start: "09:00", end: "17:00" },
          { day: 4, start: "09:00", end: "17:00" },
          { day: 5, start: "09:00", end: "17:00" },
        ],
        quiet_hours: currentPrefs?.quiet_hours,
        default_destination_ids: currentPrefs?.default_destination_ids ?? [],
        account_defaults: currentPrefs?.account_defaults ?? {},
        budgets: currentPrefs?.budgets ?? { task_usd: 5 },
        input: currentPrefs?.input,
        onboarding_completed_at: new Date().toISOString(),
      };

      tasks.push(
        mutate<Preferences>(
          "/preferences",
          {
            request_id: newRequestID(),
            expected_revision: currentPrefs?.revision ?? 0,
            value: editablePrefs,
          },
          "PUT",
        )
          .then(() => {
            savedPreferences.current = true;
          })
          .catch((err) => {
            failures.push(errorText(err));
          }),
      );
    }

    await Promise.all(tasks);

    if (failures.length > 0) {
      setFinishError(failures[0] || "Could not save your setup. Try again.");
      setFinishing(false);
      triggerMood("shake");
      return;
    }

    triggerMood("celebrate", { happy: true, sparkles: true, durationMs: 850 });
    invalidate("/preferences", "/memory", "/settings/model-keys", "/capabilities");
    onComplete();
  };

  const step3MarkState: MarkState = validatingKey ? "thinking" : !keyConfigured && keyFocused ? "attentive" : "idle";
  const mascot = { mood, moodKey, happy, sparkleKey };
  const showRightAction = (step === 3 && !keyConfigured) || step === 4;

  return (
    <div className="fixed inset-0 z-40 flex flex-col bg-canvas text-ink">
      <header className="mx-auto grid w-full max-w-[600px] shrink-0 grid-cols-[1fr_auto_1fr] items-center px-5 pt-5 pb-2">
        <button
          type="button"
          disabled={step === 1}
          aria-hidden={step === 1}
          tabIndex={step === 1 ? -1 : undefined}
          onClick={() => step > 1 && goStep((step - 1) as OnboardingStep)}
          className={cn(
            "t-fast justify-self-start rounded-full px-2.5 py-1.5 text-meta font-medium text-ink-2 hover:bg-fill-1 hover:text-ink",
            step === 1 && "invisible pointer-events-none",
          )}
        >
          Back
        </button>

        <div
          role="progressbar"
          aria-valuenow={step}
          aria-valuemin={1}
          aria-valuemax={4}
          aria-label={`Step ${step} of 4`}
          className="flex items-center gap-1.5"
        >
          {([1, 2, 3, 4] as const).map((idx) => (
            <span
              key={idx}
              className={cn(
                "h-[5px] rounded-full transition-all duration-200",
                idx === step
                  ? "w-8 bg-accent"
                  : idx < step
                    ? "w-[18px] bg-accent-text/35"
                    : "w-[18px] bg-fill-1",
              )}
            />
          ))}
        </div>

        <button
          type="button"
          disabled={finishing || !showRightAction}
          aria-hidden={!showRightAction}
          tabIndex={!showRightAction ? -1 : undefined}
          onClick={() => {
            if (step === 3) goStep(4);
            else if (step === 4) void finishOnboarding(false);
          }}
          className={cn(
            "t-fast justify-self-end rounded-full px-2.5 py-1.5 text-meta font-medium text-ink-2 hover:bg-fill-1 hover:text-ink",
            !showRightAction && "invisible pointer-events-none",
          )}
        >
          {step === 3 ? "Set up later" : "Skip"}
        </button>
      </header>

      <main className="flex flex-1 flex-col items-center overflow-y-auto px-5 pt-3 pb-10">
        <div className="my-auto w-full max-w-[480px]">
          {step === 1 && (
            <WelcomeStep
              mascot={mascot}
              onTap={() => triggerMood("hop", { happy: true, sparkles: true })}
              onNext={() => goStep(2)}
            />
          )}

          {step === 2 && (
            <AppearanceStep
              mascot={mascot}
              appearance={appearance}
              onTap={() => triggerMood("hop", { happy: true })}
              onPick={handleAppearancePick}
              onNext={() => goStep(3)}
            />
          )}

          {step === 3 && (
            <ModelSetupStep
              mascot={mascot}
              markState={step3MarkState}
              homeEnabled={!!home.data?.enabled}
              byokRequired={byokRequired}
              providers={keys.data?.providers ?? []}
              primaryProvider={primaryProvider}
              keyConfigured={keyConfigured}
              validatingKey={validatingKey}
              draftKey={draftKey}
              keyError={keyError}
              savedProvider={savedProvider}
              showModels={showModels}
              canContinue={canContinueStep3}
              onTap={() => triggerMood("hop", { happy: true })}
              onSelectProvider={(provider) => {
                setSelectedProvider(provider);
                setIsReplacingKey(false);
                setDraftKey("");
                setKeyError(null);
                setSavedProvider(null);
              }}
              onDraftKeyChange={(value) => {
                setDraftKey(value);
                if (keyError) setKeyError(null);
              }}
              onKeyFocusChange={setKeyFocused}
              onVerifyKey={() => void handleVerifyKey()}
              onStartReplaceKey={() => {
                setIsReplacingKey(true);
                setKeyError(null);
              }}
              onCloseSavedProvider={() => setSavedProvider(null)}
              onShowModelsChange={setShowModels}
              onNext={() => goStep(4)}
            />
          )}

          {step === 4 && (
            <AboutYouStep
              mascot={mascot}
              name={name}
              replyStyle={replyStyle}
              focus={focus}
              workingOn={workingOn}
              finishing={finishing}
              finishError={finishError}
              onTap={() => triggerMood("hop", { happy: true })}
              onNameChange={setName}
              onReplyStyleChange={(option) => {
                setReplyStyle(option);
                triggerMood("nod");
              }}
              onToggleFocus={toggleFocus}
              onWorkingOnChange={setWorkingOn}
              onFinish={() => void finishOnboarding(true)}
              onRetry={() => void finishOnboarding(lastSaveAboutYou.current)}
            />
          )}
        </div>
      </main>
    </div>
  );
}
