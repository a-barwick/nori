"use client";

import { useEffect, useState } from "react";
import { AlertDialog } from "radix-ui";
import { ArrowLeft, Check, Search, X } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import {
  APIError,
  errorText,
  getJSON,
  saveModelSettings,
  type HomeConnectors,
  type ModelProfile,
  type ModelSettings,
} from "@/lib/assistant/api";
import { announce } from "@/lib/assistant/announce";
import { hasSelectedHomeChat } from "@/lib/assistant/modelSetup";
import { useResource, useResourceInvalidation } from "@/lib/assistant/useResource";
import type { ModelSelection } from "@/lib/types";
import { cn } from "@/lib/utils";
import { ApiKeysSettings } from "./ApiKeysSettings";
import { ProviderBrandIcon } from "./BrandIcons";
import { HomeConnectorSettings } from "./HomeConnectorSettings";
import { Empty, Failure, inputClass, Loading, Notice, Row, Rows, Section, Switch } from "./ui";

export { hasSelectedHomeChat };

type PickerLane = "chat" | "task";

export function ModelsSettings() {
  const models = useResource<ModelSettings>("/settings/models", { poll: 10_000 });
  const home = useResource<HomeConnectors>("/settings/home-connectors", { poll: 30_000, versioned: false });
  const invalidate = useResourceInvalidation();
  const [savedProvider, setSavedProvider] = useState<{ id: string; label: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string>();
  const [activeLane, setActiveLane] = useState<PickerLane | null>(null);

  const commitSelection = async (next: ModelSelection) => {
    if (!models.data || saving) return;
    setSaving(true);
    setSaveError(undefined);
    try {
      const updated = await saveModelSettings(next, models.data.revision);
      models.apply(updated);
      invalidate("/settings/models");
      announce("Model settings saved");
    } catch (error) {
      if (error instanceof APIError && error.stale) {
        await models.reload();
        setSaveError("Model settings changed on another device. Try again.");
      } else {
        setSaveError(errorText(error));
      }
    } finally {
      setSaving(false);
    }
  };

  const hasPairedHomeDevice = !!(home.data?.enabled && home.data.devices.length > 0);
  const selection = models.data?.selection ?? {};
  const profiles = models.data?.model_profiles?.profiles ?? [];

  if (activeLane && models.data) {
    return (
      <ModelPickerView
        lane={activeLane}
        profiles={profiles}
        selection={selection}
        saving={saving}
        saveError={saveError}
        onBack={() => {
          setActiveLane(null);
          setSaveError(undefined);
        }}
        onCommit={(next) => void commitSelection(next)}
      />
    );
  }

  const chatProfile = profiles.find((p) => p.id === selection.chat_profile_id && p.lanes.includes("chat"));
  const workProfile = profiles.find((p) => p.id === selection.task_profile_id && p.lanes.includes("task"));
  const chatUnavailable = !!selection.chat_profile_id && !chatProfile;
  const workUnavailable = !!selection.task_profile_id && !workProfile;
  const showHomeOnly = !!home.data?.enabled || !!selection.home_only;

  const toggleHomeOnly = (nextHomeOnly: boolean) => {
    const nextSelection: ModelSelection = { ...selection, home_only: nextHomeOnly };
    if (nextHomeOnly) {
      if (chatProfile?.backend !== "home") {
        const homeChat = profiles.find((p) => p.backend === "home" && p.lanes.includes("chat"));
        if (homeChat) {
          nextSelection.chat_profile_id = homeChat.id;
          nextSelection.chat_reasoning = homeChat.default_reasoning;
        }
      }
      if (workProfile && workProfile.backend !== "home") {
        const homeWork = profiles.find((p) => p.backend === "home" && p.lanes.includes("task"));
        nextSelection.task_profile_id = homeWork?.id ?? "";
        nextSelection.task_reasoning = homeWork?.default_reasoning;
      }
    }
    void commitSelection(nextSelection);
  };

  return (
    <div className="space-y-6">
      <Section title="Default models">
        {models.loading && !models.data ? (
          <Loading />
        ) : !models.data ? (
          <Failure error={models.error ?? "Models are unavailable."} onRetry={() => void models.reload()} />
        ) : !profiles.length && !selection.chat_profile_id && !selection.task_profile_id ? (
          <Empty>Add a provider key or pair a home computer to choose models.</Empty>
        ) : (
          <div className="space-y-3">
            {saveError && <Notice tone="danger">{saveError}</Notice>}
            {(chatUnavailable || workUnavailable) && (
              <Notice tone="warning">One of your selected models is unavailable. Choose an available model.</Notice>
            )}
            <Rows label="Default models">
              <Row
                title="Chat"
                onClick={() => setActiveLane("chat")}
                trailing={
                  <span className={cn("max-w-44 truncate text-meta font-medium", chatUnavailable ? "text-warning" : "text-ink-2")}>
                    {chatProfile ? profileLabel(chatProfile) : chatUnavailable ? "Unavailable" : "Choose"}
                  </span>
                }
                chevron
              />
              <Row
                title="Work"
                onClick={() => setActiveLane("task")}
                trailing={
                  <span className={cn("max-w-44 truncate text-meta font-medium", workUnavailable ? "text-warning" : "text-ink-2")}>
                    {workProfile ? profileLabel(workProfile) : workUnavailable ? "Unavailable" : "None"}
                  </span>
                }
                chevron
              />
              {showHomeOnly && (
                <Row
                  title="Home models only"
                  trailing={
                    <Switch
                      checked={!!selection.home_only}
                      label="Home models only"
                      disabled={
                        saving ||
                        (!selection.home_only &&
                          !profiles.some((profile) => profile.backend === "home" && profile.lanes.includes("chat")))
                      }
                      onCheckedChange={toggleHomeOnly}
                    />
                  }
                />
              )}
            </Rows>
          </div>
        )}
      </Section>

      {savedProvider && (
        <RecommendedModelsPrompt provider={savedProvider} onClose={() => setSavedProvider(null)} />
      )}

      <ApiKeysSettings
        hasHomeChat={hasSelectedHomeChat(models.data) || hasPairedHomeDevice}
        onKeySaved={(provider) => setSavedProvider({ id: provider.provider, label: provider.label })}
      />

      <HomeConnectorSettings />
    </div>
  );
}

function ModelPickerView({
  lane,
  profiles,
  selection,
  saving,
  saveError,
  onBack,
  onCommit,
}: {
  lane: PickerLane;
  profiles: ModelProfile[];
  selection: ModelSelection;
  saving: boolean;
  saveError?: string;
  onBack: () => void;
  onCommit: (next: ModelSelection) => void;
}) {
  const [query, setQuery] = useState("");
  const isWork = lane === "task";
  const homeOnly = !!selection.home_only;
  const selectedId = isWork ? selection.task_profile_id : selection.chat_profile_id;
  const currentReasoning = isWork ? selection.task_reasoning : selection.chat_reasoning;

  const laneProfiles = profiles.filter((p) => p.lanes.includes(lane) && (!homeOnly || p.backend === "home"));
  const selected = laneProfiles.find((p) => p.id === selectedId);
  const efforts = selected?.supported_reasoning ?? [];

  const needle = query.trim().toLowerCase();
  const filtered = laneProfiles.filter(
    (p) =>
      !needle ||
      p.label.toLowerCase().includes(needle) ||
      p.model.toLowerCase().includes(needle) ||
      p.backend.toLowerCase().includes(needle),
  );
  const cloud = filtered.filter((p) => p.backend !== "home");
  const home = filtered.filter((p) => p.backend === "home");

  const pickProfile = (profile: ModelProfile | null) => {
    if (!profile) {
      if (!isWork) return;
      onCommit({ ...selection, task_profile_id: "", task_reasoning: undefined });
      return;
    }
    const nextEffort =
      currentReasoning && profile.supported_reasoning?.includes(currentReasoning)
        ? currentReasoning
        : profile.default_reasoning;
    if (isWork) {
      onCommit({ ...selection, task_profile_id: profile.id, task_reasoning: nextEffort });
    } else {
      onCommit({ ...selection, chat_profile_id: profile.id, chat_reasoning: nextEffort });
    }
  };

  const pickReasoning = (effort?: string) => {
    if (!selected) return;
    if (isWork) {
      onCommit({ ...selection, task_profile_id: selected.id, task_reasoning: effort });
    } else {
      onCommit({ ...selection, chat_profile_id: selected.id, chat_reasoning: effort });
    }
  };

  const renderProfileRow = (profile: ModelProfile) => {
    const active = profile.id === selectedId;
    return (
      <Row
        key={profile.id}
        onClick={() => pickProfile(profile)}
        leading={<ProviderBrandIcon provider={profile.backend} model={profile.model} backend={profile.backend} />}
        title={profileLabel(profile)}
        trailing={active ? <Check size={16} className="text-accent-text" aria-hidden /> : undefined}
        chevron={false}
      />
    );
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
          Models
        </button>
        <span className="text-meta font-semibold text-ink-2">{isWork ? "Work model" : "Chat model"}</span>
      </div>

      {saveError && <Notice tone="danger">{saveError}</Notice>}

      {efforts.length > 0 && (
        <Section title="Reasoning" className="mt-0!">
          <div
            role="radiogroup"
            aria-label={`${isWork ? "Work" : "Chat"} reasoning`}
            className="grid gap-1 rounded-full border border-card-border bg-fill-1 p-1"
            style={{ gridTemplateColumns: `repeat(${efforts.length + 1}, minmax(0, 1fr))` }}
          >
            {[undefined, ...efforts].map((effort) => {
              const active = (currentReasoning ?? undefined) === effort;
              const label = effort ? capitalize(effort) : "Default";
              return (
                <button
                  key={effort ?? "default"}
                  type="button"
                  role="radio"
                  aria-checked={active}
                  disabled={saving}
                  onClick={() => pickReasoning(effort)}
                  className={cn(
                    "h-8 rounded-full border text-meta font-semibold transition-colors",
                    active
                      ? "border-chip-selected-border bg-chip-selected-bg text-chip-selected-fg"
                      : "border-transparent text-ink-2 hover:text-ink",
                  )}
                >
                  {label}
                </button>
              );
            })}
          </div>
        </Section>
      )}

      <label className="relative block">
        <span className="sr-only">Search models</span>
        <Search size={16} className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-ink-3" aria-hidden />
        <input
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search models…"
          className={cn(inputClass, "h-11 pr-9 pl-10")}
        />
        {query && (
          <button
            type="button"
            aria-label="Clear search"
            onClick={() => setQuery("")}
            className="absolute top-1/2 right-2.5 -translate-y-1/2 rounded-full p-1 text-ink-3 hover:bg-fill-1 hover:text-ink"
          >
            <X size={15} aria-hidden />
          </button>
        )}
      </label>

      <div className="space-y-5">
        {!filtered.length ? (
          <Empty>{needle ? `No models match “${query.trim()}”.` : "No models available for this lane."}</Empty>
        ) : (
          <>
            {cloud.length > 0 && (
              <Section title="Cloud" className="mt-0!">
                <Rows label="Cloud models">{cloud.map(renderProfileRow)}</Rows>
              </Section>
            )}
            {home.length > 0 && (
              <Section title="Home computer">
                <Rows label="Home computer models">{home.map(renderProfileRow)}</Rows>
              </Section>
            )}
          </>
        )}
        {isWork && !needle && (selectedId || filtered.length > 0) && (
          <Rows label="Clear Work model">
            <Row
              title="No Work model"
              onClick={() => pickProfile(null)}
              trailing={!selectedId ? <Check size={16} className="text-accent-text" aria-hidden /> : undefined}
              chevron={false}
            />
          </Rows>
        )}
      </div>
    </div>
  );
}

function profileLabel(profile: ModelProfile): string {
  if (profile.availability && profile.availability !== "online") {
    return `${profile.label} · ${profile.availability === "offline" ? "Offline" : "Ollama unavailable"}`;
  }
  return profile.label;
}

export function RecommendedModelsPrompt({
  provider,
  onClose,
}: {
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
      .catch((reason) => {
        if (!controller.signal.aborted) setError(errorText(reason));
      });
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
      setError(
        reason instanceof APIError && reason.stale
          ? "Model settings changed on another device. Review the current settings and try again."
          : errorText(reason),
      );
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
              <dt className="text-ink-2">Chat</dt>
              <dd>{describe(recommendation.chat_profile_id, recommendation.chat_reasoning)}</dd>
              <dt className="text-ink-2">Work</dt>
              <dd>{describe(recommendation.task_profile_id, recommendation.task_reasoning)}</dd>
            </dl>
          )}
          {data && !recommendation && <p className="text-meta text-ink-2">Choose models in Models settings.</p>}
          <div className="flex flex-wrap justify-end gap-2">
            {error && !data && (
              <Button
                size="sm"
                variant="neutral"
                onClick={() => {
                  setError(undefined);
                  setAttempt((current) => current + 1);
                }}
              >
                Retry
              </Button>
            )}
            <AlertDialog.Cancel asChild>
              <Button size="sm" variant="quiet" disabled={saving}>
                Keep current models
              </Button>
            </AlertDialog.Cancel>
            {recommendation && (
              <Button size="sm" variant="primary" disabled={saving} onClick={() => void apply()}>
                {saving ? "Saving…" : "Use recommended models"}
              </Button>
            )}
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}

function capitalize(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
