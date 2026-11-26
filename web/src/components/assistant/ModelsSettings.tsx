"use client";

import { useEffect, useState } from "react";
import { AlertDialog } from "radix-ui";
import { Button } from "@/components/ui/primitives";
import { APIError, errorText, getJSON, saveModelSettings, type HomeConnectors, type ModelProfile, type ModelSettings } from "@/lib/assistant/api";
import { announce } from "@/lib/assistant/announce";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { ModelSelection } from "@/lib/types";
import { ApiKeysSettings } from "./ApiKeysSettings";
import { HomeConnectorSettings } from "./HomeConnectorSettings";
import { Empty, Failure, Field, inputClass, Loading, Notice, Section } from "./ui";

export function ModelsSettings() {
  const models = useResource<ModelSettings>("/settings/models", { poll: 10_000 });
  const home = useResource<HomeConnectors>("/settings/home-connectors", { poll: 30_000, versioned: false });
  const invalidate = useResourceInvalidation();
  const [savedProvider, setSavedProvider] = useState<{ id: string; label: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string>();
  const [draft, setDraft] = useState<{ selection: ModelSelection; revision: number }>();

  const saveSelection = async () => {
    if (!draft || saving) return;
    setSaving(true);
    setSaveError(undefined);
    try {
      const updated = await saveModelSettings(draft.selection, draft.revision);
      models.apply(updated);
      setDraft(undefined);
      invalidate("/settings/models");
      announce("Model settings saved");
    } catch (error) {
      if (error instanceof APIError && error.stale) {
        await models.reload();
        setSaveError("Model settings changed on another device. Review your choices and try again.");
      } else {
        setSaveError(errorText(error));
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6">
      <Section title="Models and reasoning">
        {models.loading ? <Loading /> : !models.data ? (
          <Failure error={models.error ?? "Models are unavailable."} onRetry={() => void models.reload()} />
        ) : (
          <>
            {saveError && <div className="mb-3"><Notice tone="danger">{saveError}</Notice></div>}
            {draft && draft.revision !== models.data.revision && (
              <div className="mb-3">
                <Notice tone="warning" action={<button type="button" className="font-semibold underline" onClick={() => { setDraft(undefined); setSaveError(undefined); }}>Use latest</button>}>
                  Model settings changed on another device.
                </Notice>
              </div>
            )}
            <ModelSelectors
              data={models.data}
              selection={draft?.selection ?? models.data.selection ?? {}}
              disabled={saving}
              homeEnabled={!!home.data?.enabled}
              onChange={(next) => setDraft((current) => ({ selection: next, revision: current?.revision ?? models.data!.revision }))}
            />
            <div className="mt-3">
              <Button size="sm" variant="primary" disabled={!draft || saving || draft.revision !== models.data.revision} onClick={() => void saveSelection()}>
                {saving ? "Saving…" : "Save models"}
              </Button>
            </div>
          </>
        )}
      </Section>

      {savedProvider && (
        <RecommendedModelsPrompt
          provider={savedProvider}
          onClose={() => setSavedProvider(null)}
        />
      )}

      <HomeConnectorSettings />
      <ApiKeysSettings hasHomeChat={hasSelectedHomeChat(models.data) || !!home.data?.enabled} onKeySaved={(provider) => setSavedProvider({ id: provider.provider, label: provider.label })} />
    </div>
  );
}

export function hasSelectedHomeChat(data?: ModelSettings): boolean {
  return !!data?.selection.chat_profile_id && (data.selection.home_only === true || data.selection.chat_profile_id.startsWith("home-") || data.model_profiles?.profiles.some((profile) => profile.id === data.selection.chat_profile_id && profile.backend === "home"));
}

function ModelSelectors({ data, selection, disabled, homeEnabled, onChange }: {
  data: ModelSettings;
  selection: ModelSelection;
  disabled: boolean;
  homeEnabled: boolean;
  onChange: (next: ModelSelection) => void;
}) {
  const profiles = data.model_profiles?.profiles ?? [];
  if (!profiles.length && !selection.chat_profile_id && !selection.task_profile_id) {
    return <Empty>Add an API key or connect a home computer to choose models.</Empty>;
  }

  return (
    <div className="grid gap-4 rounded-card border border-card-border bg-surface p-4 sm:grid-cols-2">
      <LaneSelector
        label="Chat"
        profiles={profiles.filter((profile) => profile.lanes.includes("chat"))}
        selectedId={selection.chat_profile_id}
        reasoning={selection.chat_reasoning}
        homeOnly={!!selection.home_only}
        disabled={disabled}
        onChange={(id, effort) => onChange({ ...selection, chat_profile_id: id, chat_reasoning: effort })}
      />
      {(homeEnabled || selection.home_only) && <label className="flex items-start gap-3 sm:col-span-2">
        <input type="checkbox" className="mt-1 accent-[var(--accent)]" checked={!!selection.home_only} disabled={disabled || (!selection.home_only && profiles.find((profile) => profile.id === selection.chat_profile_id)?.backend !== "home")} onChange={(event) => onChange({ ...selection, home_only: event.target.checked })} />
        <span><span className="block text-row font-semibold">Use home models only</span><span className="text-meta text-ink-2">Model requests use your home computer. Nori still stores your conversations.</span></span>
      </label>}
      {selection.home_only && selection.task_profile_id && profiles.find((profile) => profile.id === selection.task_profile_id)?.backend !== "home" && <div className="sm:col-span-2"><Notice tone="warning">Choose a home Work model or clear the Work model before saving.</Notice></div>}
      <LaneSelector
        label="Work"
        profiles={profiles.filter((profile) => profile.lanes.includes("task"))}
        selectedId={selection.task_profile_id}
        reasoning={selection.task_reasoning}
        homeOnly={!!selection.home_only}
        disabled={disabled}
        onChange={(id, effort) => onChange({ ...selection, task_profile_id: id, task_reasoning: effort })}
      />
    </div>
  );
}

function LaneSelector({ label, profiles, selectedId, reasoning, disabled, homeOnly, onChange }: {
  label: string;
  profiles: ModelProfile[];
  selectedId?: string;
  reasoning?: string;
  disabled: boolean;
  homeOnly: boolean;
  onChange: (id: string, effort?: string) => void;
}) {
  const selected = profiles.find((profile) => profile.id === selectedId);
  const unavailable = !!selectedId && !selected;
  const efforts = selected?.supported_reasoning ?? [];

  return (
    <div className="space-y-3">
      <Field label={`${label} model`}>
        <select
          aria-label={`${label} model`}
          className={inputClass}
          disabled={disabled || (!profiles.length && (label !== "Work" || !selectedId))}
          value={selectedId ?? ""}
          onChange={(event) => {
            const next = profiles.find((profile) => profile.id === event.target.value);
            if (!next) { if (label === "Work") onChange(""); return; }
            const nextEffort = reasoning && next.supported_reasoning?.includes(reasoning)
              ? reasoning
              : next.default_reasoning;
            onChange(next.id, nextEffort);
          }}
        >
          {(label === "Work" || !selectedId) && <option value="">{label === "Work" ? "No Work model" : "Choose a model"}</option>}
          {unavailable && <option value={selectedId}>Unavailable — choose a model</option>}
          {profiles.map((profile) => <option key={profile.id} value={profile.id} disabled={homeOnly && profile.backend !== "home"}>{profile.label}{profile.availability && profile.availability !== "online" ? " · " + (profile.availability === "offline" ? "Offline" : "Ollama unavailable") : ""}</option>)}
        </select>
      </Field>
      {unavailable && <Notice tone="warning">Choose an available model.</Notice>}
      {efforts.length > 0 && (
        <Field label={`${label} reasoning`}>
          <select
            aria-label={`${label} reasoning`}
            className={inputClass}
            disabled={disabled}
            value={reasoning ?? ""}
            onChange={(event) => onChange(selected!.id, event.target.value || undefined)}
          >
            <option value="">Default{selected?.default_reasoning ? ` (${capitalize(selected.default_reasoning)})` : ""}</option>
            {efforts.map((effort) => <option key={effort} value={effort}>{capitalize(effort)}</option>)}
          </select>
        </Field>
      )}
    </div>
  );
}

export function RecommendedModelsPrompt({ provider, onClose }: {
  provider: { id: string; label: string };
  onClose: () => void;
}) {
  const invalidate = useResourceInvalidation();
  const [data, setData] = useState<ModelSettings>();
  const [error, setError] = useState<string>();
  const [saving, setSaving] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    getJSON<ModelSettings>("/settings/models", controller.signal)
      .then(setData)
      .catch((reason) => { if (!controller.signal.aborted) setError(errorText(reason)); });
    return () => controller.abort();
  }, [provider.id, attempt]);

  const recommendation = data?.recommendations?.[provider.id];
  const describe = (id?: string, effort?: string) => {
    const label = data?.model_profiles?.profiles.find((profile) => profile.id === id)?.label ?? id ?? "Choose a model";
    return effort ? `${label} · ${capitalize(effort)}` : label;
  };

  const apply = async () => {
    if (!data || !recommendation || saving) return;
    setSaving(true);
    setError(undefined);
    try {
      await saveModelSettings(recommendation, data.revision);
      invalidate("/settings/models");
      announce("Recommended models saved");
      onClose();
    } catch (reason) {
      setError(reason instanceof APIError && reason.stale
        ? "Model settings changed on another device. Review the current settings and try again."
        : errorText(reason));
      if (reason instanceof APIError && reason.stale) {
        setData(undefined);
        setAttempt((current) => current + 1);
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <AlertDialog.Root open onOpenChange={(open) => { if (!open && !saving) onClose(); }}>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <AlertDialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 space-y-4 rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
          <AlertDialog.Title className="text-section font-semibold text-ink">Use recommended models?</AlertDialog.Title>
          <AlertDialog.Description className="text-meta text-ink-2">{provider.label}</AlertDialog.Description>
          {error && <Notice tone="danger">{error}</Notice>}
          {!data && !error && <Loading />}
          {recommendation && (
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-meta">
              <dt className="text-ink-2">Chat</dt><dd>{describe(recommendation.chat_profile_id, recommendation.chat_reasoning)}</dd>
              <dt className="text-ink-2">Work</dt><dd>{describe(recommendation.task_profile_id, recommendation.task_reasoning)}</dd>
            </dl>
          )}
          {data && !recommendation && <p className="text-meta text-ink-2">Choose models in Models settings.</p>}
          <div className="flex flex-wrap justify-end gap-2">
            {error && !data && <Button size="sm" variant="neutral" onClick={() => { setError(undefined); setAttempt((current) => current + 1); }}>Retry</Button>}
            <AlertDialog.Cancel asChild><Button size="sm" variant="quiet" disabled={saving}>Keep current models</Button></AlertDialog.Cancel>
            {recommendation && <Button size="sm" variant="primary" disabled={saving} onClick={() => void apply()}>{saving ? "Saving…" : "Use recommended models"}</Button>}
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}

function capitalize(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
