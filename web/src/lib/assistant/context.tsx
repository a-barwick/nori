"use client";

import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { useHydrated } from "@/lib/hooks";
import { useRuntime } from "@/lib/runtime/useRuntime";
import type { Capabilities, AssistantFeature } from "@/lib/contracts/assistant";
import type { ModelSelection } from "@/lib/types";
import { EMPTY_SELECTION, normalizeModelSelection, parseModelSelection } from "./modelSelection";
import { createResourceCache, ResourceCacheContext, useResource } from "./useResource";


type Runtime = ReturnType<typeof useRuntime>;
type Assistant = {
  localMode: boolean;
  runtime: Runtime;
  capabilities?: Capabilities;
  has: (feature: AssistantFeature) => boolean;
  ready: boolean;
  debug: boolean;
  setDebug: (on: boolean) => void;
  modelSelection: ModelSelection;
  setModelSelection: (next: ModelSelection) => void;
};

const AssistantContext = createContext<Assistant | null>(null);
const DEBUG_KEY = "nori-diagnostics-debug";
const MODEL_SELECTION_KEY = "nori-diagnostics-model-selection-v1";

function readStoredModelSelection(key: string): ModelSelection {
  try {
    return parseModelSelection(JSON.parse(localStorage.getItem(key) ?? "null"));
  } catch {
    return EMPTY_SELECTION;
  }
}

export function AssistantProvider({ localMode, scope = "local", children }: { localMode: boolean; scope?: string; children: ReactNode }) {
  return <ScopedAssistantProvider key={scope} localMode={localMode} scope={scope}>{children}</ScopedAssistantProvider>;
}

function ScopedAssistantProvider({ localMode, scope, children }: { localMode: boolean; scope: string; children: ReactNode }) {
  const [cache] = useState(createResourceCache);
  return (
    <ResourceCacheContext.Provider value={cache}>
      <AssistantSession localMode={localMode} scope={scope}>{children}</AssistantSession>
    </ResourceCacheContext.Provider>
  );
}

function AssistantSession({ localMode, scope, children }: { localMode: boolean; scope: string; children: ReactNode }) {
  const modelKey = `${MODEL_SELECTION_KEY}:${scope}`;
  const hydrated = useHydrated();
  const { data: capabilities, error: capabilitiesError, reload: refreshCapabilities } = useResource<Capabilities>("/capabilities", { poll: 120_000 });
  const ready = !!capabilities || !!capabilitiesError;
  const [debugOverride, setDebugState] = useState<boolean | undefined>();
  const debug = debugOverride ?? (hydrated && (() => { try { return localStorage.getItem(DEBUG_KEY) === "1"; } catch { return false; } })());
  const storedSelection = useMemo(() => (hydrated ? readStoredModelSelection(modelKey) : EMPTY_SELECTION), [hydrated, modelKey]);
  const [modelOverride, setModelOverride] = useState<ModelSelection | undefined>();
  const rawSelection = modelOverride ?? storedSelection;
  const modelSelection = useMemo(
    () => normalizeModelSelection(rawSelection, capabilitiesError ? undefined : capabilities?.model_profiles),
    [rawSelection, capabilities, capabilitiesError],
  );
  const setModelSelection = useCallback((next: ModelSelection) => {
    setModelOverride(next);
    try {
      const hasAny = !!(next.chat_profile_id || next.chat_reasoning || next.task_profile_id || next.task_reasoning);
      if (hasAny) localStorage.setItem(modelKey, JSON.stringify(next));
      else localStorage.removeItem(modelKey);
    } catch {  }
  }, [modelKey]);
  const runtime = useRuntime(localMode, modelSelection, scope, refreshCapabilities);
  const value = useMemo<Assistant>(() => ({
    localMode, runtime, capabilities, debug, ready,
    has: (feature) => !!capabilities?.assistant_features?.[feature],
    setDebug: (on) => { setDebugState(on); try { if (on) localStorage.setItem(DEBUG_KEY, "1"); else localStorage.removeItem(DEBUG_KEY); } catch {  } },
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
