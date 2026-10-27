import type { ConversationTrace, Run } from "@/lib/poc/types";

/** Debug values come from saved provider/task records, never inferred reasoning. */
export type DebugInfo = {
  run?: Run;
  turns?: ConversationTrace[];
  turnId?: string;
  agentMode?: boolean;
};

export function recordedDebug(run: Run | undefined, turns?: ConversationTrace[], agentMode = false, turnId?: string): DebugInfo {
  return { run, turns, agentMode, turnId };
}
