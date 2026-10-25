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
  budget_usd?: number;
  started_at: string;
  deadline: string;
  finished_at?: string;
  error?: string;
  events: RunEvent[];
  artifacts: Artifact[];
};
export type Session = {
  messages: Message[]; runs: Run[]; paused: boolean; expires_at: string;
  dispatches?: { id: string; request: string; status: string; created_at: string }[];
  classification_hint?: { intent?: string; confidence?: number; available: boolean };
};
