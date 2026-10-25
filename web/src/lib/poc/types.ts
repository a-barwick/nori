export type Message = { id: string; run_id: string; role: "user" | "assistant"; text: string; created_at: string };
export type RunEvent = { kind: string; text: string; created_at: string };
export type Artifact = { id: string; kind: "screenshot" };
export type Run = {
  id: string;
  status: "running" | "completed" | "failed" | "cancelled";
  cancel_requested: boolean;
  started_at: string;
  deadline: string;
  finished_at?: string;
  error?: string;
  events: RunEvent[];
  artifacts: Artifact[];
};
export type Session = { messages: Message[]; runs: Run[]; paused: boolean; expires_at: string };
