"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useHydrated } from "@/lib/hooks";
import { useRuntime, type RuntimeCore, type RuntimeDraft, type RuntimeLive, type StreamingDelta } from "@/lib/runtime/useRuntime";
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

const ScopedResourceCacheContext = createContext(false);
const AssistantConfigContext = createContext<AssistantConfig | null>(null);
const AssistantContext = createContext<Assistant | null>(null);
const ChatDraftContext = createContext<RuntimeDraft | null>(null);
const StreamingDeltaContext = createContext<StreamingDelta | undefined>(undefined);
const DEBUG_KEY = "nori-diagnostics-debug";

export function ResourceCacheProvider({ scope = "local", children }: { scope?: string; children: ReactNode }) {
  return <ScopedResourceCacheProvider key={scope}>{children}</ScopedResourceCacheProvider>;
}

function ScopedResourceCacheProvider({ children }: { children: ReactNode }) {
  const [cache] = useState(() => createResourceCache());
  useEffect(() => () => cache.clear(), [cache]);
  return (
    <ScopedResourceCacheContext.Provider value={true}>
      <ResourceCacheContext.Provider value={cache}>{children}</ResourceCacheContext.Provider>
    </ScopedResourceCacheContext.Provider>
  );
}

export function AssistantProvider({ localMode, scope = "local", children }: { localMode: boolean; scope?: string; children: ReactNode }) {
  return <ScopedAssistantProvider key={scope} localMode={localMode} scope={scope}>{children}</ScopedAssistantProvider>;
}

function ScopedAssistantProvider({ localMode, scope, children }: { localMode: boolean; scope: string; children: ReactNode }) {
  const hasScopedCache = useContext(ScopedResourceCacheContext);
  if (hasScopedCache) {
    return <AssistantSession localMode={localMode} scope={scope}>{children}</AssistantSession>;
  }
  return (
    <ScopedResourceCacheProvider>
      <AssistantSession localMode={localMode} scope={scope}>{children}</AssistantSession>
    </ScopedResourceCacheProvider>
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
        <ChatDraftContext.Provider value={runtime.draftState}>
          <StreamingDeltaContext.Provider value={runtime.streamingDelta}>{children}</StreamingDeltaContext.Provider>
        </ChatDraftContext.Provider>
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

export function useChatDraft(): RuntimeDraft {
  const value = useContext(ChatDraftContext);
  if (!value) throw new Error("useChatDraft needs AssistantProvider");
  return value;
}

export function useStreamingDelta(): StreamingDelta {
  const value = useContext(StreamingDeltaContext);
  if (value === undefined) throw new Error("useStreamingDelta needs AssistantProvider");
  return value;
}

export function useStreamingDeltaOptional(): StreamingDelta {
  return useContext(StreamingDeltaContext) ?? null;
}

export function useChatLive(): RuntimeLive {
  const draftState = useChatDraft();
  const streamingDelta = useStreamingDelta();
  return useMemo(() => ({ ...draftState, streamingDelta }), [draftState, streamingDelta]);
}
