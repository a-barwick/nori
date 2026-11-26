"use client";

import { useCallback, type ReactNode } from "react";
import { Button } from "@/components/ui/primitives";
import { APIError } from "@/lib/assistant/api";
import { useAction, useResourceInvalidation } from "@/lib/assistant/useResource";
import { cn } from "@/lib/utils";
import { Field, inputClass, Notice } from "../ui";
import { MAX_MEMORY_TEXT_BYTES, utf8Bytes } from "./memoryHelpers";

export function useVersionedMemoryMutation() {
  const invalidate = useResourceInvalidation();
  const action = useAction();

  const run = useCallback(
    <T,>(name: string, fn: () => Promise<T>, done: string) =>
      action.run(
        name,
        async () => {
          try {
            const result = await fn();
            invalidate("/memory");
            return result;
          } catch (error) {
            if (error instanceof APIError && error.stale) invalidate("/memory");
            throw error;
          }
        },
        done,
      ),
    [action, invalidate],
  );

  return { ...action, run };
}

export function ByteCounter({ text }: { text: string }) {
  const bytes = utf8Bytes(text.trim());
  const over = bytes > MAX_MEMORY_TEXT_BYTES;
  return (
    <span className={cn("font-mono text-caption", over ? "text-danger" : "text-ink-3")}>
      {bytes.toLocaleString()} / {MAX_MEMORY_TEXT_BYTES.toLocaleString()} B
    </span>
  );
}

export function MemoryFactForm({
  heading,
  headingAs = "span",
  banner,
  text,
  onTextChange,
  textLabel,
  textAriaLabel,
  textPlaceholder,
  section,
  onSectionChange,
  sectionLabel = "Section (optional)",
  sectionPlaceholder = "Preferences, People, Projects…",
  about,
  onAboutChange,
  aboutLabel = "About (optional)",
  aboutPlaceholder = "the user, Nori, travel…",
  pinned,
  onPinnedChange,
  pinnedLabel,
  extraFields,
  validationError,
  submitLabel,
  submittingLabel,
  submitting,
  submitDisabled,
  onSubmit,
  onCancel,
  className,
}: {
  heading: string;
  headingAs?: "h2" | "span";
  banner?: ReactNode;
  text: string;
  onTextChange: (value: string) => void;
  textLabel?: string;
  textAriaLabel: string;
  textPlaceholder?: string;
  section: string;
  onSectionChange: (value: string) => void;
  sectionLabel?: string;
  sectionPlaceholder?: string;
  about: string;
  onAboutChange: (value: string) => void;
  aboutLabel?: string;
  aboutPlaceholder?: string;
  pinned: boolean;
  onPinnedChange: (checked: boolean) => void;
  pinnedLabel: string;
  extraFields?: ReactNode;
  validationError?: string;
  submitLabel: string;
  submittingLabel: string;
  submitting: boolean;
  submitDisabled: boolean;
  onSubmit: () => void;
  onCancel: () => void;
  className?: string;
}) {
  const textarea = (
    <textarea
      aria-label={textAriaLabel}
      rows={3}
      value={text}
      onChange={(event) => onTextChange(event.target.value)}
      placeholder={textPlaceholder}
      className={cn(inputClass, "resize-y")}
      autoFocus
    />
  );

  return (
    <form
      className={cn("space-y-3", className)}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      {banner}
      <div className="flex items-center justify-between gap-2">
        {headingAs === "h2" ? (
          <h2 className="text-section font-semibold text-ink">{heading}</h2>
        ) : (
          <span className="text-meta font-medium text-ink">{heading}</span>
        )}
        <ByteCounter text={text} />
      </div>
      {textLabel ? <Field label={textLabel}>{textarea}</Field> : textarea}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={sectionLabel}>
          <input
            list="memory-sections"
            value={section}
            onChange={(event) => onSectionChange(event.target.value)}
            placeholder={sectionPlaceholder}
            className={inputClass}
          />
        </Field>
        <Field label={aboutLabel}>
          <input
            value={about}
            onChange={(event) => onAboutChange(event.target.value)}
            placeholder={aboutPlaceholder}
            className={inputClass}
          />
        </Field>
      </div>
      <label className="flex min-h-9 items-center gap-2 text-meta text-ink">
        <input
          type="checkbox"
          className="accent-[var(--accent)]"
          checked={pinned}
          onChange={(event) => onPinnedChange(event.target.checked)}
        />
        {pinnedLabel}
      </label>
      {extraFields}
      {validationError && <Notice tone="danger">{validationError}</Notice>}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" variant="primary" size="sm" disabled={submitDisabled}>
          {submitting ? submittingLabel : submitLabel}
        </Button>
        <Button size="sm" variant="quiet" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
