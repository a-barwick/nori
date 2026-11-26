"use client";

import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
import { useHydrated } from "@/lib/hooks";
import { useRuntime } from "@/lib/runtime/useRuntime";
import type { Capabilities, AssistantFeature } from "@/lib/contracts/assistant";
import { createResourceCache, ResourceCacheContext, useResource } from "./useResource";


type Runtime = ReturnType<typeof useRuntime>;
type Assistant = {
  localMode: boolean;
  scope: string;
  runtime: Runtime;
  capabilities?: Capabilities;
  capabilitiesError?: Error;
  refreshCapabilities: () => Promise<void>;
  has: (feature: AssistantFeature) => boolean;
  ready: boolean;
  debug: boolean;
  setDebug: (on: boolean) => void;
};

const AssistantContext = createContext<Assistant | null>(null);
const DEBUG_KEY = "nori-diagnostics-debug";

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
  const hydrated = useHydrated();
  const { data: capabilities, error: capabilitiesError, reload: refreshCapabilities } = useResource<Capabilities>("/capabilities", { poll: 120_000 });
  const ready = !!capabilities || !!capabilitiesError;
  const [debugOverride, setDebugState] = useState<boolean | undefined>();
  const debug = debugOverride ?? (hydrated && (() => { try { return localStorage.getItem(DEBUG_KEY) === "1"; } catch { return false; } })());
  const runtime = useRuntime(localMode, scope, refreshCapabilities);
  const value = useMemo<Assistant>(() => ({
    localMode, scope, runtime, capabilities, capabilitiesError, refreshCapabilities, debug, ready,
    has: (feature) => !!capabilities?.assistant_features?.[feature],
    setDebug: (on) => { setDebugState(on); try { if (on) localStorage.setItem(DEBUG_KEY, "1"); else localStorage.removeItem(DEBUG_KEY); } catch {  } },
  }), [localMode, scope, runtime, capabilities, capabilitiesError, refreshCapabilities, debug, ready]);
  return <AssistantContext.Provider value={value}>{children}</AssistantContext.Provider>;
}

export function useAssistant() {
  const value = useContext(AssistantContext);
  if (!value) throw new Error("useAssistant needs AssistantProvider");
  return value;
}
