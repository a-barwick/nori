"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useHydrated } from "@/lib/hooks";
import { useRuntime } from "@/lib/runtime/useRuntime";
import type { Capabilities, AssistantFeature, ModelProfileCatalog } from "@/lib/contracts/assistant";
import type { ModelSelection } from "@/lib/types";
import { useResource } from "./useResource";

type Runtime = ReturnType<typeof useRuntime>;
type Assistant = {
  localMode: boolean;
  runtime: Runtime;
  capabilities?: Capabilities;
  /** True when the live core advertises this route family. */
  has: (feature: AssistantFeature) => boolean;
  /** False until the core's capabilities have loaded (or failed), so screens don't claim a feature is missing. */
  ready: boolean;
  debug: boolean;
  setDebug: (on: boolean) => void;
  modelSelection: ModelSelection;
  setModelSelection: (next: ModelSelection) => void;
};

const AssistantContext = createContext<Assistant | null>(null);
const DEBUG_KEY = "nori-diagnostics-debug";
const MODEL_SELECTION_KEY = "nori-diagnostics-model-selection-v1";
const EMPTY_SELECTION: ModelSelection = {};

function readStoredModelSelection(): ModelSelection {
  try {
    const raw = localStorage.getItem(MODEL_SELECTION_KEY);
    if (!raw) return EMPTY_SELECTION;
    const parsed = JSON.parse(raw) as ModelSelection;
    if (!parsed || typeof parsed !== "object") return EMPTY_SELECTION;
    return {
      ...(typeof parsed.chat_profile_id === "string" && parsed.chat_profile_id ? { chat_profile_id: parsed.chat_profile_id } : {}),
      ...(typeof parsed.chat_reasoning === "string" && parsed.chat_reasoning ? { chat_reasoning: parsed.chat_reasoning } : {}),
      ...(typeof parsed.task_profile_id === "string" && parsed.task_profile_id ? { task_profile_id: parsed.task_profile_id } : {}),
      ...(typeof parsed.task_reasoning === "string" && parsed.task_reasoning ? { task_reasoning: parsed.task_reasoning } : {}),
    };
  } catch {
    return EMPTY_SELECTION;
  }
}

function pruneModelSelection(sel: ModelSelection, catalog?: ModelProfileCatalog): ModelSelection {
  if (!catalog) return sel;
  if (!catalog.profiles?.length) return EMPTY_SELECTION;
  const out: ModelSelection = {};
  const chatProfile = catalog.profiles.find(
    (p) => p.lanes.includes("chat") && p.id === (sel.chat_profile_id || catalog.default_chat_profile_id),
  );
  if (sel.chat_profile_id && catalog.profiles.some((p) => p.lanes.includes("chat") && p.id === sel.chat_profile_id)) {
    out.chat_profile_id = sel.chat_profile_id;
  }
  if (sel.chat_reasoning && chatProfile?.supported_reasoning?.includes(sel.chat_reasoning)) {
    out.chat_reasoning = sel.chat_reasoning;
  }
  const taskProfile = catalog.profiles.find(
    (p) => p.lanes.includes("task") && p.id === (sel.task_profile_id || catalog.default_task_profile_id),
  );
  if (sel.task_profile_id && catalog.profiles.some((p) => p.lanes.includes("task") && p.id === sel.task_profile_id)) {
    out.task_profile_id = sel.task_profile_id;
  }
  if (sel.task_reasoning && taskProfile?.supported_reasoning?.includes(sel.task_reasoning)) {
    out.task_reasoning = sel.task_reasoning;
  }
  return out;
}

export function AssistantProvider({ localMode, children }: { localMode: boolean; children: ReactNode }) {
  const hydrated = useHydrated();
  const { data: capabilities, error: capabilitiesError } = useResource<Capabilities>("/capabilities", { poll: 120_000 });
  const ready = !!capabilities || !!capabilitiesError;
  const [debugOverride, setDebugState] = useState<boolean | undefined>();
  const debug = debugOverride ?? (hydrated && (() => { try { return localStorage.getItem(DEBUG_KEY) === "1"; } catch { return false; } })());
  const [rawSelection, setRawSelection] = useState<ModelSelection>(EMPTY_SELECTION);
  useEffect(() => {
    if (!hydrated) return;
    setRawSelection(readStoredModelSelection());
  }, [hydrated]);
  const modelSelection = useMemo(
    () => (capabilities ? pruneModelSelection(rawSelection, capabilities.model_profiles) : rawSelection),
    [rawSelection, capabilities],
  );
  const setModelSelection = useCallback((next: ModelSelection) => {
    setRawSelection(next);
    try {
      const hasAny = !!(next.chat_profile_id || next.chat_reasoning || next.task_profile_id || next.task_reasoning);
      if (hasAny) localStorage.setItem(MODEL_SELECTION_KEY, JSON.stringify(next));
      else localStorage.removeItem(MODEL_SELECTION_KEY);
    } catch { /* storage off */ }
  }, []);
  const runtime = useRuntime(localMode, modelSelection);
  const value = useMemo<Assistant>(() => ({
    localMode, runtime, capabilities, debug, ready,
    has: (feature) => !!capabilities?.assistant_features?.[feature],
    setDebug: (on) => { setDebugState(on); try { if (on) localStorage.setItem(DEBUG_KEY, "1"); else localStorage.removeItem(DEBUG_KEY); } catch { /* storage off */ } },
    modelSelection,
    setModelSelection,
  }), [localMode, runtime, capabilities, debug, ready, modelSelection, setModelSelection]);
  return <AssistantContext.Provider value={value}>{children}</AssistantContext.Provider>;
}

export function useAssistant() {
  const value = useContext(AssistantContext);
  if (!value) throw new Error("useAssistant needs AssistantProvider");
  return value;
}
