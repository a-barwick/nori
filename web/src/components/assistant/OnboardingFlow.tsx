"use client";

import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
import { CheckSquare, KeyRound, Lightbulb, Lock, MessageSquare } from "lucide-react";
import { Dialog } from "radix-ui";
import { NoriMark, type MarkMood } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { APPEARANCE_OPTIONS, useAppearance, type NoriAppearance } from "@/lib/appearance";
import {
  errorText,
  mutate,
  newRequestID,
  saveModelKey,
  type ModelKeyProvider,
  type ModelKeysResponse,
  type HomeConnectors,
  type ModelSettings,
} from "@/lib/assistant/api";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { MemoryFact, Preferences, PreferencesEditable } from "@/lib/contracts/assistant";
import type { MarkState } from "@/lib/types";
import { cn } from "@/lib/utils";
import { hasSelectedHomeChat, ModelsSettings, RecommendedModelsPrompt } from "./ModelsSettings";
import { Notice, Pill } from "./ui";

export type OnboardingStep = 1 | 2 | 3 | 4;

export interface OnboardingMemorySeed {
  section: "Preferences" | "Context";
  text: string;
}

export const REPLY_STYLE_OPTIONS = [
  "Concise & direct",
  "Warm & conversational",
  "Bullets first",
] as const;

export const FOCUS_OPTIONS = [
  "Engineering",
  "Daily planning",
  "Email & calendar",
  "Research",
  "Writing",
] as const;

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
    facts.push({ section: "Preferences", text: `Prefers ${style.toLowerCase()} replies.` });
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

const SPARKLES: readonly { className: string; style: CSSProperties }[] = [
  {
    className: "top-2 left-2 bg-accent-text",
    style: { "--tx": "-16px", "--ty": "-14px" } as CSSProperties,
  },
  {
    className: "top-1.5 right-2.5 bg-[var(--ok-text)]",
    style: { "--tx": "16px", "--ty": "-12px", animationDelay: "40ms" } as CSSProperties,
  },
  {
    className: "bottom-3 left-0.5 bg-accent",
    style: { "--tx": "-18px", "--ty": "4px", animationDelay: "80ms" } as CSSProperties,
  },
  {
    className: "bottom-2.5 right-1 bg-accent-text",
    style: { "--tx": "18px", "--ty": "6px", animationDelay: "60ms" } as CSSProperties,
  },
];

function MascotStage({
  size = 72,
  state = "idle",
  mood,
  moodKey,
  happy = false,
  sparkleKey = 0,
  onTap,
}: {
  size?: number;
  state?: MarkState;
  mood?: MarkMood;
  moodKey: number;
  happy?: boolean;
  sparkleKey?: number;
  onTap: () => void;
}) {
  return (
    <div className="relative mb-4 flex h-[84px] items-center justify-center">
      <button
        type="button"
        onClick={onTap}
        aria-label="Nori"
        className="relative grid cursor-pointer place-items-center rounded-full p-1 focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-accent-signal"
      >
        {sparkleKey > 0 && (
          <span key={sparkleKey} className="pointer-events-none absolute -inset-3" aria-hidden>
            {SPARKLES.map((dot, idx) => (
              <span
                key={idx}
                className={cn("nori-sparkle-dot absolute size-1.5 rounded-full opacity-0", dot.className)}
                style={dot.style}
              />
            ))}
          </span>
        )}
        <span key={moodKey}>
          <NoriMark size={size} state={state} mood={mood} happy={happy} />
        </span>
      </button>
    </div>
  );
}

function ThemeSwatch({ mode }: { mode: NoriAppearance }) {
  const bg =
    mode === "light"
      ? "bg-[#FFFFFF]"
      : mode === "dark"
        ? "bg-[#000000]"
        : "bg-[linear-gradient(135deg,#FFFFFF_50%,#000000_50%)]";
  const bubble = mode === "light" ? "bg-[#5000FF]" : "bg-[#7A3DF6]";
  const line =
    mode === "light"
      ? "bg-[#E7E2EF]"
      : mode === "dark"
        ? "bg-[#261F38]"
        : "bg-[#A98AFF]/60";

  return (
    <div
      className={cn(
        "flex h-[52px] w-full flex-col justify-between overflow-hidden rounded-[10px] border border-hairline-strong p-[7px]",
        bg,
      )}
      aria-hidden
    >
      <div className={cn("h-2 w-[44%] self-end rounded-full", bubble)} />
      <div className={cn("h-1.5 w-[62%] rounded-full", line)} />
    </div>
  );
}

export function OnboardingFlow({
  preferences,
  onComplete,
}: {
  preferences?: Preferences;
  onComplete: () => void;
}) {
  const [step, setStep] = useState<OnboardingStep>(1);
  const [appearance, setAppearance] = useAppearance();
  const keys = useResource<ModelKeysResponse>("/settings/model-keys", { poll: 0, versioned: false });
  const models = useResource<ModelSettings>("/settings/models", { poll: 0 });
  const home = useResource<HomeConnectors>("/settings/home-connectors", { poll: 0, versioned: false });
  const [showModels, setShowModels] = useState(false);
  const invalidate = useResourceInvalidation();

  // Mascot reaction state
  const [mood, setMood] = useState<MarkMood | undefined>("hello");
  const [moodKey, setMoodKey] = useState(1);
  const [happy, setHappy] = useState(false);
  const [sparkleKey, setSparkleKey] = useState(1);
  const happyTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Step 3 (API key)
  const [draftKey, setDraftKey] = useState("");
  const [isReplacingKey, setIsReplacingKey] = useState(false);
  const [validatingKey, setValidatingKey] = useState(false);
  const [keyError, setKeyError] = useState<string | null>(null);
  const [keyFocused, setKeyFocused] = useState(false);
  const [selectedProvider, setSelectedProvider] = useState("");
  const [savedProvider, setSavedProvider] = useState<{ id: string; label: string } | null>(null);

  // Step 4 (About you)
  const [name, setName] = useState("");
  const [replyStyle, setReplyStyle] = useState<string>(REPLY_STYLE_OPTIONS[0]);
  const [focus, setFocus] = useState<string[]>(["Engineering", "Daily planning"]);
  const [workingOn, setWorkingOn] = useState("");
  const [finishing, setFinishing] = useState(false);

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

  const primaryProvider: ModelKeyProvider | undefined = keys.data?.providers.find((provider) => provider.provider === selectedProvider)
    ?? keys.data?.providers.find((provider) => provider.configured)
    ?? keys.data?.providers[0];
  const byokRequired = !!keys.data?.byok_required;
  const keyConfigured = !!primaryProvider?.configured && !isReplacingKey;
  const canContinueStep3 = (!byokRequired || !!keys.data?.providers.some((provider) => provider.configured) || hasSelectedHomeChat(models.data)) && !savedProvider;

  const goStep = (next: OnboardingStep) => {
    setStep(next);
    if (next === 1) triggerMood("hello", { sparkles: true });
    else if (next === 2) triggerMood("nod");
    else if (next === 4) triggerMood("nod");
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
    setFinishing(true);
    triggerMood("celebrate", { happy: true, sparkles: true, durationMs: 850 });

    const tasks: Promise<unknown>[] = [];

    if (saveAboutYou) {
      const facts = buildOnboardingMemoryFacts({ name, replyStyle, focus, workingOn });
      for (const fact of facts) {
        tasks.push(
          mutate<MemoryFact>("/memory", {
            request_id: newRequestID(),
            expected_revision: 0,
            value: {
              text: fact.text,
              section: fact.section,
              pinned: false,
            },
          }).catch(() => undefined),
        );
      }
    }

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
      ).catch(() => undefined),
    );

    await Promise.allSettled(tasks);
    invalidate("/preferences", "/memory", "/settings/model-keys", "/capabilities");
    onComplete();
  };

  const step3MarkState: MarkState = validatingKey
    ? "thinking"
    : !keyConfigured && keyFocused
      ? "attentive"
      : "idle";

  return (
    <div className="fixed inset-0 z-40 flex flex-col bg-canvas text-ink">
      {/* Minimal Header */}
      <header className="mx-auto flex w-full max-w-[600px] shrink-0 items-center justify-between px-5 pt-5 pb-2">
        <button
          type="button"
          onClick={() => step > 1 && goStep((step - 1) as OnboardingStep)}
          className={cn(
            "t-fast rounded-lg px-2.5 py-1.5 text-meta font-medium text-ink-2 hover:bg-fill-1 hover:text-ink",
            step === 1 && "invisible pointer-events-none",
          )}
        >
          Back
        </button>

        <div className="flex items-center gap-1.5" aria-label={`Step ${step} of 4`}>
          {([1, 2, 3, 4] as const).map((idx) => (
            <span
              key={idx}
              className={cn(
                "h-[5px] rounded-full transition-all duration-200",
                idx === step
                  ? "w-8 bg-accent"
                  : idx < step
                    ? "w-[18px] bg-accent-subtle dark:bg-accent-text/35"
                    : "w-[18px] bg-fill-1",
              )}
            />
          ))}
        </div>

        <button
          type="button"
          disabled={finishing}
          onClick={() => {
            if (step < 4) goStep((step + 1) as OnboardingStep);
            else void finishOnboarding(false);
          }}
          className="t-fast rounded-lg px-2.5 py-1.5 text-meta font-medium text-ink-2 hover:bg-fill-1 hover:text-ink"
        >
          {step === 3 ? "Set up later" : "Skip"}
        </button>
      </header>

      {/* Main Content */}
      <main className="flex flex-1 flex-col items-center justify-center overflow-y-auto px-5 pt-3 pb-10">
        <div className="w-full max-w-[480px]">
          {step === 1 && (
            <section className="anim-fade flex flex-col">
              <MascotStage
                size={76}
                mood={mood}
                moodKey={moodKey}
                happy={happy}
                sparkleKey={sparkleKey}
                onTap={() => triggerMood("hop", { happy: true, sparkles: true })}
              />
              <h1 className="text-center text-[28px] font-bold tracking-tight text-ink">
                Hi, I’m Nori
              </h1>

              <div className="mt-6 flex flex-col gap-2.5">
                <div className="flex items-center gap-3.5 rounded-card border border-card-border bg-surface px-4 py-3.5">
                  <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                    <MessageSquare size={18} strokeWidth={1.9} aria-hidden />
                  </span>
                  <span className="text-row font-semibold text-ink">
                    Chat through ideas, drafts, and questions
                  </span>
                </div>

                <div className="flex items-center gap-3.5 rounded-card border border-card-border bg-surface px-4 py-3.5">
                  <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-[var(--scheduled-bg)] text-[var(--scheduled-text)]">
                    <CheckSquare size={18} strokeWidth={1.9} aria-hidden />
                  </span>
                  <span className="text-row font-semibold text-ink">
                    Hand off tasks and approve before anything sends
                  </span>
                </div>

                <div className="flex items-center gap-3.5 rounded-card border border-card-border bg-surface px-4 py-3.5">
                  <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-[var(--ok-bg)] text-[var(--ok-text)]">
                    <Lightbulb size={18} strokeWidth={1.9} aria-hidden />
                  </span>
                  <span className="text-row font-semibold text-ink">
                    Remembers your preferences and context
                  </span>
                </div>
              </div>

              <div className="mt-6">
                <Button variant="primary" className="h-[46px] w-full text-[15px]" onClick={() => goStep(2)}>
                  Get started
                </Button>
              </div>
            </section>
          )}

          {step === 2 && (
            <section className="anim-fade flex flex-col">
              <MascotStage
                size={72}
                mood={mood}
                moodKey={moodKey}
                happy={happy}
                sparkleKey={sparkleKey}
                onTap={() => triggerMood("hop", { happy: true })}
              />
              <h1 className="text-center text-[28px] font-bold tracking-tight text-ink">
                Appearance
              </h1>

              <div className="mt-6 grid grid-cols-3 gap-2.5" role="radiogroup" aria-label="Appearance">
                {APPEARANCE_OPTIONS.map((option) => {
                  const selected = appearance === option.id;
                  return (
                    <button
                      key={option.id}
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      onClick={() => handleAppearancePick(option.id)}
                      className={cn(
                        "t-fast flex cursor-pointer flex-col items-center gap-2.5 rounded-card border p-3 text-ink",
                        selected
                          ? "border-accent-signal bg-fill-2"
                          : "border-card-border bg-surface hover:bg-fill-1",
                      )}
                    >
                      <ThemeSwatch mode={option.id} />
                      <span className="text-meta font-semibold">{option.label}</span>
                    </button>
                  );
                })}
              </div>

              {/* Live Chat Preview */}
              <div
                className="mt-3.5 flex flex-col gap-2.5 rounded-card border border-card-border bg-surface p-4"
                aria-label="Appearance preview"
              >
                <div className="max-w-[82%] self-end rounded-[18px] bg-accent px-3.5 py-2 text-meta text-on-accent">
                  Summarize my calendar for tomorrow
                </div>
                <div className="flex items-start gap-2.5">
                  <span className="mt-0.5 shrink-0">
                    <NoriMark size={24} state="idle" />
                  </span>
                  <div className="space-y-1.5">
                    <p className="text-meta leading-relaxed text-ink">
                      You have 3 meetings starting at 10:00 AM, with an open block from 1:00–3:30 PM.
                    </p>
                    <Pill tone="ok">Ready</Pill>
                  </div>
                </div>
              </div>

              <div className="mt-6">
                <Button variant="primary" className="h-[46px] w-full text-[15px]" onClick={() => goStep(3)}>
                  Continue
                </Button>
              </div>
            </section>
          )}

          {step === 3 && (
            <section className="anim-fade flex flex-col">
              <MascotStage
                size={72}
                state={step3MarkState}
                mood={mood}
                moodKey={moodKey}
                happy={happy}
                sparkleKey={sparkleKey}
                onTap={() => triggerMood("hop", { happy: true })}
              />
              <h1 className="text-center text-[28px] font-bold tracking-tight text-ink">
                {home.data?.enabled ? "Choose your AI model" : "Add your API key"}
              </h1>

              <div className="mt-6 rounded-card border border-card-border bg-surface p-4">
                <div className="mb-3.5 flex items-center justify-between gap-2.5">
                  <div className="flex items-center gap-2.5">
                    <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
                      <KeyRound size={17} strokeWidth={1.9} aria-hidden />
                    </span>
                    <label className="sr-only" htmlFor="onboarding-provider">Model provider</label>
                    <select
                      id="onboarding-provider"
                      value={primaryProvider?.provider ?? ""}
                      disabled={validatingKey || !keys.data?.providers.length}
                      onChange={(event) => {
                        setSelectedProvider(event.target.value);
                        setIsReplacingKey(false);
                        setDraftKey("");
                        setKeyError(null);
                        setSavedProvider(null);
                      }}
                      className="rounded-[10px] border border-card-border bg-fill-1 px-3 py-2 text-row font-semibold text-ink"
                    >
                      {!primaryProvider && <option value="">Providers unavailable</option>}
                      {keys.data?.providers.map((provider) => <option key={provider.provider} value={provider.provider}>{provider.label}</option>)}
                    </select>
                  </div>
                  <Pill tone={validatingKey ? "working" : keyConfigured ? "ok" : "neutral"}>
                    {validatingKey ? "Saving" : keyConfigured ? "Configured" : "Not configured"}
                  </Pill>
                </div>

                {!keyConfigured ? (
                  <div>
                    <label htmlFor="onboarding-api-key" className="mb-1.5 block text-meta font-semibold text-ink-2">
                      API key
                    </label>
                    <div className="relative flex items-center">
                      <input
                        id="onboarding-api-key"
                        type="password"
                        value={draftKey}
                        disabled={validatingKey}
                        onFocus={() => setKeyFocused(true)}
                        onBlur={() => setKeyFocused(false)}
                        onChange={(e) => {
                          setDraftKey(e.target.value);
                          if (keyError) setKeyError(null);
                        }}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") {
                            e.preventDefault();
                            void handleVerifyKey();
                          }
                        }}
                        placeholder="Paste API key"
                        autoComplete="off"
                        className="h-11 w-full rounded-[12px] border border-card-border bg-fill-1 pr-24 pl-3.5 font-mono text-[13px] text-ink placeholder:text-ink-3 focus:border-accent-signal focus:outline-none"
                      />
                      <button
                        type="button"
                        disabled={validatingKey}
                        onClick={() => void handleVerifyKey()}
                        className="t-fast absolute right-2 rounded-lg bg-surface px-2.5 py-1.5 text-[12px] font-semibold text-accent-text hover:bg-fill-2 disabled:opacity-50"
                      >
                        {validatingKey ? "Saving…" : "Save"}
                      </button>
                    </div>
                  </div>
                ) : (
                  <div className="flex items-center justify-between rounded-[12px] bg-[var(--ok-bg)] px-3.5 py-2.5 text-meta text-[var(--ok-text)]">
                    <span>
                      Key saved
                      {primaryProvider?.last4 && (
                        <>
                          {" "}
                          · <span className="font-mono text-[12px]">••••{primaryProvider.last4}</span>
                        </>
                      )}
                    </span>
                    <button
                      type="button"
                      onClick={() => {
                        setIsReplacingKey(true);
                        setKeyError(null);
                      }}
                      className="text-[12.5px] font-semibold underline-offset-2 hover:underline"
                    >
                      Replace
                    </button>
                  </div>
                )}

                {keyError && (
                  <div className="mt-3">
                    <Notice tone="danger">{keyError}</Notice>
                  </div>
                )}
              </div>

              {savedProvider && (
                <div className="mt-3">
                  <RecommendedModelsPrompt provider={savedProvider} onClose={() => setSavedProvider(null)} />
                </div>
              )}

              {home.data?.enabled && <div className="mt-3"><Button variant="tonal" onClick={() => setShowModels(true)}>Use a home AI server</Button></div>}
              <Dialog.Root open={showModels} onOpenChange={setShowModels}>
                <Dialog.Portal>
                  <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
                  <Dialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 max-h-[85vh] w-[min(650px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 space-y-4 overflow-y-auto rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
                    <Dialog.Title className="text-section font-semibold text-ink">Models</Dialog.Title>
                    <Dialog.Description className="text-meta text-ink-2">Connect a computer and choose its model for Chat.</Dialog.Description>
                    <ModelsSettings />
                    <Dialog.Close asChild><Button>Done</Button></Dialog.Close>
                  </Dialog.Content>
                </Dialog.Portal>
              </Dialog.Root>

              <div className="mt-3">
                <Notice tone="neutral">
                  <span className="inline-flex items-center gap-2">
                    <Lock size={14} className="shrink-0" aria-hidden />
                    <span>Encrypted at rest and never displayed again after saving.</span>
                  </span>
                </Notice>
              </div>

              <div className="mt-6">
                <Button
                  variant="primary"
                  disabled={!canContinueStep3 || validatingKey}
                  className="h-[46px] w-full text-[15px]"
                  onClick={() => goStep(4)}
                >
                  Continue
                </Button>
              </div>
            </section>
          )}

          {step === 4 && (
            <section className="anim-fade flex flex-col">
              <MascotStage
                size={72}
                mood={mood}
                moodKey={moodKey}
                happy={happy}
                sparkleKey={sparkleKey}
                onTap={() => triggerMood("hop", { happy: true })}
              />
              <h1 className="text-center text-[28px] font-bold tracking-tight text-ink">
                About you
              </h1>

              <div className="mt-5 space-y-4 rounded-card border border-card-border bg-surface p-4">
                <div>
                  <label htmlFor="onboarding-name" className="mb-1.5 block text-meta font-semibold text-ink-2">
                    Name
                  </label>
                  <input
                    id="onboarding-name"
                    type="text"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="Your name"
                    className="h-11 w-full rounded-[12px] border border-card-border bg-fill-1 px-3.5 text-body text-ink placeholder:text-ink-3 focus:border-accent-signal focus:outline-none"
                  />
                </div>

                <div>
                  <span className="mb-1.5 block text-meta font-semibold text-ink-2">Reply style</span>
                  <div className="flex flex-wrap gap-2">
                    {REPLY_STYLE_OPTIONS.map((option) => {
                      const selected = replyStyle === option;
                      return (
                        <button
                          key={option}
                          type="button"
                          onClick={() => {
                            setReplyStyle(option);
                            triggerMood("nod");
                          }}
                          className={cn(
                            "t-fast rounded-full border px-3.5 py-1.5 text-meta font-medium",
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
                </div>

                <div>
                  <span className="mb-1.5 block text-meta font-semibold text-ink-2">Focus</span>
                  <div className="flex flex-wrap gap-2">
                    {FOCUS_OPTIONS.map((option) => {
                      const selected = focus.includes(option);
                      return (
                        <button
                          key={option}
                          type="button"
                          onClick={() => toggleFocus(option)}
                          className={cn(
                            "t-fast rounded-full border px-3.5 py-1.5 text-meta font-medium",
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
                </div>

                <div>
                  <label htmlFor="onboarding-working-on" className="mb-1.5 block text-meta font-semibold text-ink-2">
                    What are you working on?
                  </label>
                  <input
                    id="onboarding-working-on"
                    type="text"
                    value={workingOn}
                    onChange={(e) => setWorkingOn(e.target.value)}
                    placeholder="e.g. Building a mobile app"
                    className="h-11 w-full rounded-[12px] border border-card-border bg-fill-1 px-3.5 text-body text-ink placeholder:text-ink-3 focus:border-accent-signal focus:outline-none"
                  />
                </div>
              </div>

              <div className="mt-6">
                <Button
                  variant="primary"
                  disabled={finishing}
                  className="h-[46px] w-full text-[15px]"
                  onClick={() => void finishOnboarding(true)}
                >
                  {finishing ? "Starting…" : "Start chatting"}
                </Button>
              </div>
            </section>
          )}
        </div>
      </main>
    </div>
  );
}
