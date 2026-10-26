import type { Run, RunEvent } from "@/lib/poc/types";

// Debug info shown under a Nori message when debug mode is on. Real messages
// show only what the runtime records (task status, revision, times, spend and
// event names).

export type DebugStep =
  | {
      kind: "model";
      id: string;
      ms: number;
      tokens: { input: number; cached: number; output: number; thinking: number };
      tools: string[];
      thinking: string;
      finish: string;
    }
  | { kind: "tool"; id: string; name: string; ms: number; args: string; result: string; ok: boolean };

/** Jev's answer for the owner's message (`classification_hint`). */
export type Classification = {
  intent: "converse" | "ask" | "request_work" | "steer" | "provide_input" | "decide" | "inspect" | "control" | "update_memory" | "mixed";
  confidence: number;
  available: boolean;
  /** Set when Jev split a mixed message. */
  parts?: { intent: string; text: string; replaces_work?: boolean }[];
};

export type OuterAction =
  | { type: "respond"; text: string }
  | { type: "dispatch"; request: string; task_id: string; status: "accepted" | "rejected"; error?: string }
  | { type: "update_dispatch"; task_id: string; text: string; instruction_rev: number }
  | { type: "clarify"; question: string };

export type OuterTurn = {
  id: string;
  phase: "thinking" | "writing" | "done" | "superseded" | "failed";
  context: { label: string; tokens: number }[];
  actions: OuterAction[];
  ms: number;
};

/** Fields left undefined were not recorded and render as such. */
export type DebugInfo = {
  classification?: Classification;
  turn?: OuterTurn;
  runId?: string;
  status?: string;
  model?: string;
  revision?: number;
  started?: string;
  ended?: string;
  tokens?: number;
  cost?: number;
  costUnknown?: boolean;
  budget?: number;
  /** Optional detailed provider steps; absent when the runtime did not record them. */
  steps?: DebugStep[];
  /** Recorded task events: kind and name only. */
  events?: RunEvent[];
};

/** Recorded fields only, for real messages. */
export function recordedDebug(run: Run | undefined): DebugInfo {
  if (!run) return {};
  return {
    runId: run.id,
    status: run.status,
    revision: run.revision,
    started: run.started_at,
    ended: run.finished_at,
    cost: run.spent_usd,
    costUnknown: run.cost_unknown,
    budget: run.budget_usd,
    events: run.events,
  };
}
