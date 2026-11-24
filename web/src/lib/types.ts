export type Sender = "nori" | "me";

/** One piece of a message. A client that doesn't know the type shows `fallback`. */
export type Part = { type: string; text?: string; display?: "page" | "bubble"; fallback?: string; id?: string; name?: string; media_type?: string; size?: number; url?: string;
  /** Card parts (contract CardPart): a typed reference to server state. */
  schema_version?: number; kind?: string; resource?: { kind: string; id: string; revision?: number }; actions?: string[] };
/** A saved output of a task. Created, checked, reported and delivered are separate facts. */
export type SavedResult = {
  id: string; root_id: string; version: number; revises?: string; superseded_by?: string; run_id: string; kind: "answer" | "file" | "screenshot" | "link";
  title: string; name?: string; media_type?: string; size?: number; url?: string; unmet?: string[];
  created_at: string; checked_at?: string; reported_at?: string; delivered_at?: string; message_id?: string;
};
export type Attachment = { id: string; name: string; media_type: string; size: number };
export type DraftFile = Attachment & { status: "uploading" | "ready" | "error"; error?: string; file: File; /** 0–1 while uploading, when the browser reports it. */ progress?: number };
export type MarkState = "idle" | "attentive" | "thinking";

export interface TextItem {
  id: string;
  sender: Sender;
  at: number;
  text: string;
  display?: Part["display"];
  parts?: Part[];
  debug?: DebugInfo;
  /** The user's own latest message: sending until the core's receipt, then saved until Nori replies. */
  receipt?: "sending" | "saved";
}

export interface Thread {
  title: string;
  items: TextItem[];
}

// Wire types for the Go runtime API. Field names match its JSON exactly.

export type Message = { id: string; run_id: string; turn_id?: string; role: "user" | "assistant"; text: string; parts?: Part[]; result_ids?: string[]; created_at: string };
type RunEvent = { id?: string; sequence?: number; instruction_revision?: number; kind: string; text: string; created_at: string };
type TaskQuestion = { id: string; kind: "input" | "work"; text: string; call_id?: string; instruction_revision: number; resolved_by?: string };
type TaskHold = { id: string; task_id: string; question: string; status: "pending" | "ready" | "answered" | "failed"; answered_turn_id?: string; created_at: string };
type RunStatus = "running" | "completed" | "failed" | "cancelled";
export const taskControlKinds = ["steer", "answer", "stop"] as const;
type TaskControlKind = typeof taskControlKinds[number];
export type TaskCommand = { id: string; run_id: string; kind: TaskControlKind; text?: string; question_id?: string };
type TaskCommandReceipt = TaskCommand & { position: number; revision: number };
export type TaskCommandResponse = { command: TaskCommandReceipt; run: Run; accepted?: boolean; retryable?: boolean; error?: string };
export type PendingTaskCommand = { command: TaskCommand; started: number; sending?: boolean; error?: string; accepted?: boolean; position?: number; rejected?: boolean; retryManually?: boolean };
export type Artifact = { id: string; kind: "screenshot" };
export type Run = {
  id: string;
  status: RunStatus;
  cancel_requested: boolean;
  finishing?: { status: RunStatus };
  cleanup_pending?: boolean;
  cleanup_error?: string;
  request?: string;
  holds?: TaskHold[];
  position?: number;
  event_sequence?: number;
  question?: TaskQuestion;
  result?: string;
  revision?: number;
  incorporated?: number;
  waiting?: string;
  progress?: string;
  /** Model USD from reported token usage, and the configured task budget. */
  spent_usd?: number;
  /** Recorded spend is partial when at least one provider cost is unknown. */
  cost_unknown?: boolean;
  budget_usd?: number;
  parent_id?: string;
  subtask_spent_usd?: number;
  started_at: string;
  finished_at?: string;
  error?: string;
  events: RunEvent[] | null;
  artifacts: Artifact[];
};
export type Usage = {
  id: string; input_id: string; provider: string; model: string;
  input_tokens?: number; output_tokens?: number; total_tokens?: number;
  cost_usd?: number; cost_status: string; request_status: string; created_at: string;
};
type ClassificationHint = { intent?: string; intents?: string[]; confidence?: number; available: boolean };
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
export type ReadPosition = { message_id: string; through: string; position: number };
export type Session = {
  messages: Message[]; runs: Run[]; paused: boolean;
  dispatches?: { id: string; request: string; status: string; created_at: string }[];
  classification_hint?: ClassificationHint;
  usage?: Usage[];
  conversation?: { introducing?: boolean; pending: boolean; processing: boolean; turn_id?: string; input_ids: string[]; restart_count: number; routing_blocked?: boolean; frozen: boolean; revision: number; consumed: number };
  traces?: ConversationTrace[];
  next_cursor?: string;
  results?: SavedResult[];
  /** Event log position this snapshot reflects. */
  position?: number;
  /** The owner's read position, shared across sessions. */
  read_position?: ReadPosition;
};

export type DebugInfo = {
  run?: Run;
  turns?: ConversationTrace[];
  turnId?: string;
};
