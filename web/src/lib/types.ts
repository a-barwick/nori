export type Sender = "nori" | "me";

export type Part = { type: string; text?: string; display?: "page" | "bubble"; fallback?: string; id?: string; name?: string; media_type?: string; size?: number; url?: string; scheduled_at?: string;
  schema_version?: number; kind?: string; resource?: { kind: string; id: string; revision?: number }; actions?: string[] };
export type SavedResult = {
  id: string; root_id: string; version: number; revises?: string; superseded_by?: string; run_id: string; kind: "answer" | "file" | "screenshot" | "link";
  title: string; name?: string; media_type?: string; size?: number; url?: string; unmet?: string[];
  created_at: string; checked_at?: string; reported_at?: string; delivered_at?: string; message_id?: string;
};
export type Attachment = { id: string; name: string; media_type: string; size: number };
export type DraftFile = Attachment & { status: "uploading" | "ready" | "error"; error?: string; file: File;  progress?: number };
export type MarkState = "idle" | "attentive" | "thinking";

type MessageStatus = "completed" | "failed" | "cancelled" | "superseded";
type MessageError = { code: string; message: string; retryable: boolean };

export interface TextItem {
  id: string;
  sender: Sender;
  at: number;
  text: string;
  status?: MessageStatus;
  error?: MessageError;
  turnId?: string;
  display?: Part["display"];
  parts?: Part[];
  debug?: DebugInfo;
  receipt?: "sending" | "saved";
}

export interface Thread {
  title: string;
  items: TextItem[];
}


export type Message = { id: string; run_id: string; turn_id?: string; role: "user" | "assistant"; status?: MessageStatus; text: string; parts?: Part[]; error?: MessageError; result_ids?: string[]; created_at: string };
type RunEvent = { id?: string; sequence?: number; instruction_revision?: number; kind: string; text: string; created_at: string };
type TaskQuestion = { id: string; kind: "input" | "work"; text: string; call_id?: string; instruction_revision: number; resolved_by?: string };
type TaskHold = { id: string; task_id: string; question: string; status: "pending" | "ready" | "answered" | "failed"; answered_turn_id?: string; created_at: string };
type RunStatus = "running" | "completed" | "failed" | "cancelled";
export const taskControlKinds = ["steer", "answer", "stop", "continue", "approve", "decline"] as const;
type TaskControlKind = typeof taskControlKinds[number];
export type TaskCommand = { id: string; run_id: string; kind: TaskControlKind; text?: string; question_id?: string };
type TaskCommandReceipt = TaskCommand & { position: number; revision: number };
export type TaskCommandResponse = { command: TaskCommandReceipt; run: Run; accepted?: boolean; retryable?: boolean; error?: string };
export type PendingTaskCommand = { command: TaskCommand; started: number; sending?: boolean; error?: string; accepted?: boolean; position?: number; rejected?: boolean; retryManually?: boolean };
export type ModelSelection = {
  home_only?: boolean;
  chat_profile_id?: string;
  chat_profile_version?: string;
  chat_reasoning?: string;
  task_profile_id?: string;
  task_profile_version?: string;
  task_reasoning?: string;
};
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
  spent_usd?: number;
  cost_unknown?: boolean;
  budget_usd?: number;
  model_selection?: ModelSelection;
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
  served_model?: string; model_selection?: ModelSelection;
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
  position?: number;
  read_position?: ReadPosition;
};

export type DebugInfo = {
  run?: Run;
  turns?: ConversationTrace[];
  turnId?: string;
};
