export type Message = { id: string; run_id: string; role: "user" | "assistant"; text: string; created_at: string };
export type RunEvent = { kind: string; text: string; created_at: string };
export type Artifact = { id: string; kind: "screenshot" };
export type Run = {
  id: string;
  status: "running" | "completed" | "failed" | "cancelled";
  cancel_requested: boolean;
  temporal?: boolean;
  revision?: number;
  incorporated?: number;
  waiting?: string;
  /** Model USD from reported token usage, and the budget Austin has approved. */
  spent_usd?: number;
  /** Recorded spend is partial when at least one provider cost is unknown. */
  cost_unknown?: boolean;
  budget_usd?: number;
  started_at: string;
  /** Legacy inline request deadline; durable tasks have no wall-clock deadline. */
  deadline?: string;
  finished_at?: string;
  error?: string;
  events: RunEvent[];
  artifacts: Artifact[];
};
export type Session = {
  messages: Message[]; runs: Run[]; paused: boolean;
  dispatches?: { id: string; request: string; status: string; created_at: string }[];
  classification_hint?: { intent?: string; confidence?: number; available: boolean };
};
