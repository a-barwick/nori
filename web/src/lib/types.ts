export type Sender = "nori" | "me";
export type MarkState = "idle" | "attentive" | "thinking" | "writing" | "listening" | "pleased";

/** Saved runtime message; markdown structure selects bubble or document rendering. */
export interface TextItem {
  id: string;
  sender: Sender;
  at: number;
  kind: "text";
  text: string;
  debug?: DebugInfo;
}

export type ThreadItem = TextItem;
export interface Thread {
  id: string;
  title: string;
  kind: "main";
  createdAt: number;
  items: ThreadItem[];
}

// Wire types for the Go runtime API. Field names match its JSON exactly.

export type Message = { id: string; run_id: string; turn_id?: string; role: "user" | "assistant"; text: string; created_at: string };
export type RunEvent = { id?: string; sequence?: number; instruction_revision?: number; kind: string; text: string; created_at: string };
export type TaskQuestion = { id: string; kind: "input" | "budget"; text: string; call_id?: string; instruction_revision: number; resolved_by?: string };
export type TaskCommand = { id: string; run_id: string; kind: "steer" | "answer" | "continue" | "stop"; text?: string; question_id?: string };
export type TaskCommandReceipt = TaskCommand & { position: number; revision: number };
export type TaskCommandResponse = { command: TaskCommandReceipt; run: Run; accepted?: boolean; retryable?: boolean; error?: string };
export type PendingTaskCommand = { command: TaskCommand; started: number; sending?: boolean; error?: string; accepted?: boolean; position?: number; rejected?: boolean };
export type Artifact = { id: string; kind: "screenshot" };
export type Run = {
  id: string;
  status: "running" | "completed" | "failed" | "cancelled";
  cancel_requested: boolean;
  temporal?: boolean;
  protocol_version?: number;
  request?: string;
  position?: number;
  event_sequence?: number;
  question?: TaskQuestion;
  result?: string;
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
export type Usage = {
  id: string; input_id: string; provider: string; model: string;
  input_tokens?: number; output_tokens?: number; total_tokens?: number;
  cost_usd?: number; cost_status: string; request_status: string; created_at: string;
};
export type ClassificationHint = { intent?: string; intents?: string[]; confidence?: number; available: boolean };
export type ConversationTrace = {
  id: string; turn_id: string; attempt: number; input_ids: string[]; model: string;
  status: "running" | "completed" | "superseded" | "error";
  started_at: string; finished_at?: string; duration_ms?: number;
  classification_hint?: ClassificationHint;
  context_truncated?: boolean; context_turns?: number; context_bytes?: number;
  actions: { id: string; request: string; bytes: number; status: string; error?: string }[];
  usage: Usage[]; error?: string;
};
export type TaskStep = {
  id: string; kind: "model" | "tool"; status: "running" | "completed" | "failed"; revision: number;
  started_at: string; finished_at?: string; duration_ms?: number; model?: string; name?: string;
  input_tokens?: number; output_tokens?: number; cached_tokens?: number; thinking_tokens?: number;
  cost_usd?: number; cost_status: string; calls?: { name: string; arguments: string }[];
  arguments?: string; result?: string; error?: string;
};
export type Session = {
  messages: Message[]; runs: Run[]; paused: boolean;
  dispatches?: { id: string; request: string; status: string; created_at: string }[];
  classification_hint?: ClassificationHint;
  usage?: Usage[];
  conversation?: { introducing?: boolean; pending: boolean; processing: boolean; turn_id?: string; input_ids: string[]; restart_count: number; routing_blocked?: boolean; frozen: boolean; revision: number; consumed: number };
  traces?: ConversationTrace[];
  next_cursor?: string;
};

/** Debug values come from saved provider/task records, never inferred reasoning. */
export type DebugInfo = {
  run?: Run;
  turns?: ConversationTrace[];
  turnId?: string;
  agentMode?: boolean;
};
