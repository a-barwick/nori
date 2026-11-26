"use client";

import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { useHydrated } from "@/lib/hooks";
import { useRuntime, type RuntimeCore, type RuntimeLive } from "@/lib/runtime/useRuntime";
import type { AssistantFeature, Capabilities } from "@/lib/contracts/assistant";
import { createResourceCache, ResourceCacheContext, useResource } from "./useResource";

export type AssistantConfig = {
  localMode: boolean;
  scope: string;
  capabilities?: Capabilities;
  capabilitiesError?: Error;
  refreshCapabilities: () => Promise<void>;
  has: (feature: AssistantFeature) => boolean;
  ready: boolean;
  debug: boolean;
  setDebug: (on: boolean) => void;
};

type Assistant = AssistantConfig & {
  runtime: RuntimeCore;
};

const AssistantConfigContext = createContext<AssistantConfig | null>(null);
const AssistantContext = createContext<Assistant | null>(null);
const ChatLiveContext = createContext<RuntimeLive | null>(null);
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
  const debug =
    debugOverride ??
    (hydrated &&
      (() => {
        try {
          return localStorage.getItem(DEBUG_KEY) === "1";
        } catch {
          return false;
        }
      })());

  const has = useCallback((feature: AssistantFeature) => !!capabilities?.assistant_features?.[feature], [capabilities]);
  const setDebug = useCallback((on: boolean) => {
    setDebugState(on);
    try {
      if (on) localStorage.setItem(DEBUG_KEY, "1");
      else localStorage.removeItem(DEBUG_KEY);
    } catch {}
  }, []);

  const runtime = useRuntime(localMode, scope, refreshCapabilities);

  const configValue = useMemo<AssistantConfig>(
    () => ({
      localMode,
      scope,
      capabilities,
      capabilitiesError,
      refreshCapabilities,
      has,
      ready,
      debug,
      setDebug,
    }),
    [localMode, scope, capabilities, capabilitiesError, refreshCapabilities, has, ready, debug, setDebug],
  );

  const assistantValue = useMemo<Assistant>(
    () => ({
      ...configValue,
      runtime: runtime.core,
    }),
    [configValue, runtime.core],
  );

  return (
    <AssistantConfigContext.Provider value={configValue}>
      <AssistantContext.Provider value={assistantValue}>
        <ChatLiveContext.Provider value={runtime.live}>{children}</ChatLiveContext.Provider>
      </AssistantContext.Provider>
    </AssistantConfigContext.Provider>
  );
}

export function useAssistantConfig() {
  const value = useContext(AssistantConfigContext);
  if (!value) throw new Error("useAssistantConfig needs AssistantProvider");
  return value;
}

export function useAssistant() {
  const value = useContext(AssistantContext);
  if (!value) throw new Error("useAssistant needs AssistantProvider");
  return value;
}

export function useChatLive() {
  const value = useContext(ChatLiveContext);
  if (!value) throw new Error("useChatLive needs AssistantProvider");
  return value;
}
