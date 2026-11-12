"use client";

import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
import { useChat } from "@/lib/chat/useChat";
import { useHydrated } from "@/lib/hooks";
import { useRuntime } from "@/lib/runtime/useRuntime";
import type { Capabilities, AssistantFeature } from "@/lib/contracts/assistant";
import { fixtureMode } from "./api";
import { useResource } from "./useResource";

type Runtime = ReturnType<typeof useRuntime>;
type Chat = ReturnType<typeof useChat>;
type Assistant = {
  agentMode: boolean;
  fixtures: boolean;
  runtime: Runtime;
  chat: Chat;
  capabilities?: Capabilities;
  /** True when the live core advertises this route family (or fixture mode is on). */
  has: (feature: AssistantFeature) => boolean;
  /** False until the core's capabilities have loaded (or failed), so screens don't claim a feature is missing. */
  ready: boolean;
  debug: boolean;
  setDebug: (on: boolean) => void;
};

const AssistantContext = createContext<Assistant | null>(null);
const DEBUG_KEY = "nori-diagnostics-debug";

export function AssistantProvider({ agentMode, children }: { agentMode: boolean; children: ReactNode }) {
  const runtime = useRuntime(agentMode);
  const chat = useChat();
  const hydrated = useHydrated();
  const fixtures = hydrated && fixtureMode();
  const { data: capabilities, error: capabilitiesError } = useResource<Capabilities>("/capabilities", { poll: 120_000 });
  const ready = fixtures || !!capabilities || !!capabilitiesError;
  const [debugOverride, setDebugState] = useState<boolean | undefined>();
  const debug = debugOverride ?? (hydrated && (() => { try { return localStorage.getItem(DEBUG_KEY) === "1"; } catch { return false; } })());
  const value = useMemo<Assistant>(() => ({
    agentMode, fixtures, runtime, chat, capabilities, debug, ready,
    has: (feature) => fixtures || !!capabilities?.assistant_features?.[feature],
    setDebug: (on) => { setDebugState(on); try { if (on) localStorage.setItem(DEBUG_KEY, "1"); else localStorage.removeItem(DEBUG_KEY); } catch { /* storage off */ } },
  }), [agentMode, fixtures, runtime, chat, capabilities, debug, ready]);
  return <AssistantContext.Provider value={value}>{children}</AssistantContext.Provider>;
}

export function useAssistant() {
  const value = useContext(AssistantContext);
  if (!value) throw new Error("useAssistant needs AssistantProvider");
  return value;
}
