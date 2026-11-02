export type Sender = "nori" | "me";

/** One piece of a message. A client that doesn't know the type shows `fallback`. */
export type Part = { type: string; text?: string; display?: "page" | "bubble"; fallback?: string; id?: string; name?: string; media_type?: string; size?: number; url?: string };
/** A saved output of a task. Created, checked, reported and delivered are separate facts. */
export type SavedResult = {
  id: string; root_id: string; version: number; revises?: string; superseded_by?: string; run_id: string; kind: "answer" | "file" | "screenshot" | "link";
  title: string; name?: string; media_type?: string; size?: number; url?: string; unmet?: string[];
  created_at: string; checked_at?: string; reported_at?: string; delivered_at?: string; message_id?: string;
};
export type Attachment = { id: string; name: string; media_type: string; size: number };
/** A file picked in the composer, from the upload until the message is sent. */
export type DraftFile = Attachment & { status: "uploading" | "ready" | "error"; error?: string; file: File };
export type MarkState = "idle" | "attentive" | "thinking" | "writing" | "listening" | "pleased";

/** Saved runtime message; markdown structure selects bubble or document rendering. */
export interface TextItem {
  id: string;
  sender: Sender;
  at: number;
  kind: "text";
  text: string;
  /** Text-part layout requested by the service, when it set one. */
  display?: Part["display"];
  /** Non-text parts, shown under the text. */
  parts?: Part[];
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

export type Message = { id: string; run_id: string; turn_id?: string; role: "user" | "assistant"; text: string; parts?: Part[]; result_ids?: string[]; created_at: string };
export type RunEvent = { id?: string; sequence?: number; instruction_revision?: number; kind: string; text: string; created_at: string };
export type TaskQuestion = { id: string; kind: "input" | "budget" | "approval" | "work"; text: string; call_id?: string; instruction_revision: number; resolved_by?: string };
export type TaskHold = { id: string; task_id: string; question: string; status: "pending" | "ready" | "answered" | "failed"; answered_turn_id?: string; created_at: string };
export type RunStatus = "running" | "completed" | "failed" | "cancelled";
export const taskControlKinds = ["steer", "answer", "continue", "approve", "decline", "stop"] as const;
export type TaskControlKind = typeof taskControlKinds[number];
export type CommandKind = TaskControlKind | "start" | "hold";
export type TaskCommand = { interrupt?: boolean; id: string; run_id: string; kind: TaskControlKind; text?: string; question_id?: string };
export type TaskCommandReceipt = TaskCommand & { position: number; revision: number };
export type TaskCommandResponse = { command: TaskCommandReceipt; run: Run; accepted?: boolean; retryable?: boolean; error?: string };
export type PendingTaskCommand = { command: TaskCommand; started: number; sending?: boolean; error?: string; accepted?: boolean; position?: number; rejected?: boolean };
export type Artifact = { id: string; kind: "screenshot" };
export type Run = {
  id: string;
  status: RunStatus;
  cancel_requested: boolean;
  temporal?: boolean;
  protocol_version?: number;
  request?: string;
  holds?: TaskHold[];
  position?: number;
  event_sequence?: number;
  question?: TaskQuestion;
  result?: string;
  revision?: number;
  incorporated?: number;
  waiting?: string;
  /** Latest saved state of a running long job. */
  progress?: string;
  /** Model USD from reported token usage, and the budget Austin has approved. */
  spent_usd?: number;
  /** Recorded spend is partial when at least one provider cost is unknown. */
  cost_unknown?: boolean;
  budget_usd?: number;
  /** The task this one works for, and the cost of this task's own subtasks. */
  parent_id?: string;
  subtask_spent_usd?: number;
  started_at: string;
  /** Legacy inline request deadline; durable tasks have no wall-clock deadline. */
  deadline?: string;
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
  results?: SavedResult[];
  /** Event log position this snapshot reflects. */
  position?: number;
};

/** Debug values come from saved provider/task records, never inferred reasoning. */
export type DebugInfo = {
  run?: Run;
  turns?: ConversationTrace[];
  turnId?: string;
  agentMode?: boolean;
};
