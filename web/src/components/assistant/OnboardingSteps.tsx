"use client";

import type { CSSProperties } from "react";
import { CheckSquare, KeyRound, Lightbulb, Lock, MessageSquare } from "lucide-react";
import { Dialog } from "radix-ui";
import { NoriMark, type MarkMood } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { APPEARANCE_OPTIONS, type NoriAppearance } from "@/lib/appearance";
import type { ModelKeyProvider } from "@/lib/assistant/api";
import type { MarkState } from "@/lib/types";
import { cn } from "@/lib/utils";
import { ModelsSettings, RecommendedModelsPrompt } from "./ModelsSettings";
import { Notice, Pill } from "./ui";

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

export type MascotProps = {
  mood?: MarkMood;
  moodKey: number;
  happy: boolean;
  sparkleKey: number;
};

export function MascotStage({
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
          <span key={`sparkle-${sparkleKey}`} className="pointer-events-none absolute -inset-3" aria-hidden>
            {SPARKLES.map((dot, idx) => (
              <span
                key={idx}
                className={cn("nori-sparkle-dot absolute size-1.5 rounded-full opacity-0", dot.className)}
                style={dot.style}
              />
            ))}
          </span>
        )}
        <span key={`mood-${moodKey}`}>
          <NoriMark size={size} state={state} mood={mood} happy={happy} />
        </span>
      </button>
    </div>
  );
}

function ThemeSwatch({ mode }: { mode: NoriAppearance }) {
  const bg =
    mode === "light"
      ? "bg-[var(--swatch-light-bg)]"
      : mode === "dark"
        ? "bg-[var(--swatch-dark-bg)]"
        : "bg-[linear-gradient(135deg,var(--swatch-light-bg)_50%,var(--swatch-dark-bg)_50%)]";
  const bubble = mode === "light" ? "bg-[var(--swatch-light-bubble)]" : "bg-[var(--swatch-dark-bubble)]";
  const line =
    mode === "light"
      ? "bg-[var(--swatch-light-line)]"
      : mode === "dark"
        ? "bg-[var(--swatch-dark-line)]"
        : "bg-[var(--swatch-system-line)]";

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

export function WelcomeStep({ mascot, onTap, onNext }: { mascot: MascotProps; onTap: () => void; onNext: () => void }) {
  return (
    <section className="anim-fade flex flex-col">
      <MascotStage size={76} {...mascot} onTap={onTap} />
      <h1 className="text-center text-[28px] font-bold tracking-tight text-ink">Hi, I’m Nori</h1>

      <div className="mt-6 flex flex-col gap-2.5">
        <div className="flex items-center gap-3.5 rounded-card border border-card-border bg-surface px-4 py-3.5">
          <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
            <MessageSquare size={18} strokeWidth={1.9} aria-hidden />
          </span>
          <span className="text-row font-semibold text-ink">Chat through ideas, drafts, and questions</span>
        </div>

        <div className="flex items-center gap-3.5 rounded-card border border-card-border bg-surface px-4 py-3.5">
          <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-[var(--scheduled-bg)] text-[var(--scheduled-text)]">
            <CheckSquare size={18} strokeWidth={1.9} aria-hidden />
          </span>
          <span className="text-row font-semibold text-ink">Hand off tasks and approve before anything sends</span>
        </div>

        <div className="flex items-center gap-3.5 rounded-card border border-card-border bg-surface px-4 py-3.5">
          <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-[var(--ok-bg)] text-[var(--ok-text)]">
            <Lightbulb size={18} strokeWidth={1.9} aria-hidden />
          </span>
          <span className="text-row font-semibold text-ink">Remembers your preferences and context</span>
        </div>
      </div>

      <div className="mt-6">
        <Button variant="primary" className="h-[46px] w-full text-[15px]" onClick={onNext}>
          Get started
        </Button>
      </div>
    </section>
  );
}

export function AppearanceStep({
  mascot,
  appearance,
  onTap,
  onPick,
  onNext,
}: {
  mascot: MascotProps;
  appearance: NoriAppearance;
  onTap: () => void;
  onPick: (next: NoriAppearance) => void;
  onNext: () => void;
}) {
  return (
    <section className="anim-fade flex flex-col">
      <MascotStage size={72} {...mascot} onTap={onTap} />
      <h1 className="text-center text-[28px] font-bold tracking-tight text-ink">Appearance</h1>

      <div className="mt-6 grid grid-cols-3 gap-2.5" role="radiogroup" aria-label="Appearance">
        {APPEARANCE_OPTIONS.map((option) => {
          const selected = appearance === option.id;
          return (
            <button
              key={option.id}
              type="button"
              role="radio"
              aria-checked={selected}
              onClick={() => onPick(option.id)}
              className={cn(
                "t-fast flex cursor-pointer flex-col items-center gap-2.5 rounded-card border p-3 text-ink",
                selected ? "border-accent-signal bg-fill-2" : "border-card-border bg-surface hover:bg-fill-1",
              )}
            >
              <ThemeSwatch mode={option.id} />
              <span className="text-meta font-semibold">{option.label}</span>
            </button>
          );
        })}
      </div>

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
        <Button variant="primary" className="h-[46px] w-full text-[15px]" onClick={onNext}>
          Continue
        </Button>
      </div>
    </section>
  );
}

export function ModelSetupStep({
  mascot,
  markState,
  homeEnabled,
  providers,
  primaryProvider,
  keyConfigured,
  validatingKey,
  draftKey,
  keyError,
  savedProvider,
  showModels,
  canContinue,
  onTap,
  onSelectProvider,
  onDraftKeyChange,
  onKeyFocusChange,
  onVerifyKey,
  onStartReplaceKey,
  onCloseSavedProvider,
  onShowModelsChange,
  onNext,
}: {
  mascot: MascotProps;
  markState: MarkState;
  homeEnabled: boolean;
  providers: ModelKeyProvider[];
  primaryProvider?: ModelKeyProvider;
  keyConfigured: boolean;
  validatingKey: boolean;
  draftKey: string;
  keyError: string | null;
  savedProvider: { id: string; label: string } | null;
  showModels: boolean;
  canContinue: boolean;
  onTap: () => void;
  onSelectProvider: (provider: string) => void;
  onDraftKeyChange: (value: string) => void;
  onKeyFocusChange: (focused: boolean) => void;
  onVerifyKey: () => void;
  onStartReplaceKey: () => void;
  onCloseSavedProvider: () => void;
  onShowModelsChange: (open: boolean) => void;
  onNext: () => void;
}) {
  return (
    <section className="anim-fade flex flex-col">
      <MascotStage size={72} state={markState} {...mascot} onTap={onTap} />
      <h1 className="text-center text-[28px] font-bold tracking-tight text-ink">
        {homeEnabled ? "Choose your AI model" : "Add your API key"}
      </h1>

      <div className="mt-6 rounded-card border border-card-border bg-surface p-4">
        <div className="mb-3.5 flex items-center justify-between gap-2.5">
          <div className="flex items-center gap-2.5">
            <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-accent-subtle text-accent-text">
              <KeyRound size={17} strokeWidth={1.9} aria-hidden />
            </span>
            <label className="sr-only" htmlFor="onboarding-provider">
              Model provider
            </label>
            <select
              id="onboarding-provider"
              value={primaryProvider?.provider ?? ""}
              disabled={validatingKey || !providers.length}
              onChange={(event) => onSelectProvider(event.target.value)}
              className="rounded-[10px] border border-card-border bg-fill-1 px-3 py-2 text-row font-semibold text-ink"
            >
              {!primaryProvider && <option value="">Providers unavailable</option>}
              {providers.map((provider) => (
                <option key={provider.provider} value={provider.provider}>
                  {provider.label}
                </option>
              ))}
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
                onFocus={() => onKeyFocusChange(true)}
                onBlur={() => onKeyFocusChange(false)}
                onChange={(e) => onDraftKeyChange(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    onVerifyKey();
                  }
                }}
                placeholder="Paste API key"
                autoComplete="off"
                className="h-11 w-full rounded-[12px] border border-card-border bg-fill-1 pr-24 pl-3.5 font-mono text-[13px] text-ink placeholder:text-ink-3 focus:border-accent-signal focus:outline-none"
              />
              <button
                type="button"
                disabled={validatingKey}
                onClick={onVerifyKey}
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
              onClick={onStartReplaceKey}
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
          <RecommendedModelsPrompt provider={savedProvider} onClose={onCloseSavedProvider} />
        </div>
      )}

      {homeEnabled && (
        <div className="mt-3">
          <Button variant="tonal" onClick={() => onShowModelsChange(true)}>
            Use a home AI server
          </Button>
        </div>
      )}
      <Dialog.Root open={showModels} onOpenChange={onShowModelsChange}>
        <Dialog.Portal>
          <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
          <Dialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 max-h-[85vh] w-[min(650px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 space-y-4 overflow-y-auto rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
            <Dialog.Title className="text-section font-semibold text-ink">Models</Dialog.Title>
            <Dialog.Description className="text-meta text-ink-2">
              Connect a computer and choose its model for Chat.
            </Dialog.Description>
            <ModelsSettings />
            <Dialog.Close asChild>
              <Button>Done</Button>
            </Dialog.Close>
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
          disabled={!canContinue || validatingKey}
          className="h-[46px] w-full text-[15px]"
          onClick={onNext}
        >
          Continue
        </Button>
      </div>
    </section>
  );
}

export function AboutYouStep({
  mascot,
  name,
  replyStyle,
  focus,
  workingOn,
  finishing,
  finishError,
  onTap,
  onNameChange,
  onReplyStyleChange,
  onToggleFocus,
  onWorkingOnChange,
  onFinish,
  onRetry,
}: {
  mascot: MascotProps;
  name: string;
  replyStyle: string;
  focus: readonly string[];
  workingOn: string;
  finishing: boolean;
  finishError: string | null;
  onTap: () => void;
  onNameChange: (value: string) => void;
  onReplyStyleChange: (value: string) => void;
  onToggleFocus: (item: string) => void;
  onWorkingOnChange: (value: string) => void;
  onFinish: () => void;
  onRetry: () => void;
}) {
  return (
    <section className="anim-fade flex flex-col">
      <MascotStage size={72} {...mascot} onTap={onTap} />
      <h1 className="text-center text-[28px] font-bold tracking-tight text-ink">About you</h1>

      <div className="mt-5 space-y-4 rounded-card border border-card-border bg-surface p-4">
        <div>
          <label htmlFor="onboarding-name" className="mb-1.5 block text-meta font-semibold text-ink-2">
            Name
          </label>
          <input
            id="onboarding-name"
            type="text"
            value={name}
            onChange={(e) => onNameChange(e.target.value)}
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
                  onClick={() => onReplyStyleChange(option)}
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
                  onClick={() => onToggleFocus(option)}
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
            onChange={(e) => onWorkingOnChange(e.target.value)}
            placeholder="e.g. Building a mobile app"
            className="h-11 w-full rounded-[12px] border border-card-border bg-fill-1 px-3.5 text-body text-ink placeholder:text-ink-3 focus:border-accent-signal focus:outline-none"
          />
        </div>
      </div>

      {finishError && (
        <div className="mt-4">
          <Notice
            tone="danger"
            action={
              <Button size="sm" variant="neutral" disabled={finishing} onClick={onRetry}>
                Retry
              </Button>
            }
          >
            {finishError}
          </Notice>
        </div>
      )}

      <div className="mt-6">
        <Button
          variant="primary"
          disabled={finishing}
          className="h-[46px] w-full text-[15px]"
          onClick={onFinish}
        >
          {finishing ? "Starting…" : "Start chatting"}
        </Button>
      </div>
    </section>
  );
}
