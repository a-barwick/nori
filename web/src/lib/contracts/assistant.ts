export type Trigger = ({
  kind: "once";
  at: Instant;
} | {
  kind: "recurring";
  local_start: string;
  timezone: Timezone;
  rrule: string;
} | {
  kind: "event_relative";
  event: ResourceRef;
  offset_seconds: number;
} | {
  kind: "watch";
  source: ResourceRef;
  predicate_ref: string;
  min_interval_seconds: number;
});
export type MissedRun = "skip" | "latest" | "all";
export type Automation = {
  id: ID;
  revision: Revision;
  title: string;
  instruction: string;
  status: "enabled" | "paused" | "cancelled";
  trigger: Trigger;
  connection_ids: ID[];
  budget_usd: number;
  max_active_runs: number;
  missed_run: MissedRun;
  execution_window_seconds: number;
  destination_ids: ID[];
  next_at?: Instant;
  end_at?: Instant;
  max_occurrences?: number;
  goal_id?: ID;
  last_occurrence_id?: string;
  created_at?: Instant;
  updated_at?: Instant;
  authority_rule_ids: ID[];
  snoozed_until?: Instant;
  last_occurrence?: {
    id: string;
    status: OccurrenceStatus;
    scheduled_at: Instant;
    summary?: string;
  };
};
export type AutomationEditable = {
  title: string;
  instruction: string;
  trigger: Trigger;
  connection_ids: ID[];
  requested_authority?: {
    action_classes: Effect[];
    tool_ids?: string[];
    resource_ids?: string[];
    recipients?: string[];
    max_operations?: number;
  };
  budget_usd: number;
  max_active_runs: number;
  missed_run?: MissedRun;
  execution_window_seconds: number;
  destination_ids: ID[];
  end_at?: Instant;
  max_occurrences?: number;
  goal_id?: ID;
};
export type AutomationChange = {
  request_id: UUID;
  expected_revision: Revision;
  value: AutomationEditable;
};
export type AutomationAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: ({
    action: "pause" | "resume" | "cancel" | "run_now";
  } | {
    action: "snooze";
    until: Instant;
  });
};
export type Occurrence = {
  id: string;
  automation_id: ID;
  automation_revision: Revision;
  generation?: number;
  scheduled_at: Instant;
  source_revision?: string;
  status: OccurrenceStatus;
  task_ids: string[];
  delivery_ids: string[];
  started_at?: Instant;
  finished_at?: Instant;
  summary?: string;
  error?: ErrorDetail;
  reason?: "missed_window" | "paused" | "superseded" | "max_active_runs";
  deliveries?: Delivery[];
};
export type Delivery = {
  id: string;
  occurrence_id?: string;
  result_ids?: string[];
  task_id?: string;
  destination_id: ID;
  state: "pending" | "attempting" | "accepted" | "delivered" | "failed" | "uncertain";
  attempt_id?: string;
  receipt?: string;
  acknowledged_at?: Instant;
  scheduled_for?: Instant;
  created_at?: Instant;
  updated_at?: Instant;
  error?: ErrorDetail;
};
export type Goal = {
  id: ID;
  revision: Revision;
  title: string;
  outcome: string;
  completion_criteria: {
    id: string;
    text: string;
    met: boolean;
    evidence_result_ids?: string[];
  }[];
  status: "active" | "paused" | "completed" | "abandoned";
  progress: string;
  next_actions: string[];
  blockers: {
    text: string;
    interaction_id?: string;
    connection_id?: string;
  }[];
  linked: {
    task_ids: string[];
    automation_ids: string[];
    result_ids: string[];
  };
  deadline?: Instant;
  follow_up?: "none" | "remind" | "monitor";
  created_at?: Instant;
  updated_at?: Instant;
  completed_at?: Instant;
  authority_rule_ids?: ID[];
};
export type GoalEditable = {
  title: string;
  outcome: string;
  completion_criteria: {
    id?: string;
    text: string;
  }[];
  deadline?: Instant;
  follow_up?: "none" | "remind" | "monitor";
};
export type GoalChange = {
  request_id: UUID;
  expected_revision: Revision;
  value: GoalEditable;
};
export type GoalAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: {
    action: "pause" | "resume" | "complete" | "abandon";
    keep_monitors?: boolean;
  };
};
export type OccurrenceStatus = "pending" | "accepted" | "running" | "completed" | "failed" | "cancelled" | "expired" | "skipped";
export type AutomationPreview = {
  next_runs: Instant[];
  timezone: Timezone;
  proposed_rules: PermissionRuleEditable[];
  authority_summary?: string;
  missed_run_explanation: string;
  warnings: ({
    code: "dst_shift" | "outside_working_hours" | "needs_connection" | "needs_permission" | "unattended_send_requires_rule";
    message: string;
  })[];
};

export type ID = string;
export type UUID = string;
export type Revision = number;
export type Instant = string;
export type LocalDate = string;
export type Timezone = string;
export type JSON = unknown;
export type Effect = "read" | "reversible_write" | "sensitive_send" | "unknown" | "control";
export type Outcome = "pending" | "ready" | "attempted" | "submitted" | "confirmed" | "failed" | "uncertain" | "skipped" | "voided";
export type DefinitionRef = {
  id: string;
  version: string;
  digest: string;
};
export type ResourceRef = {
  connection_id: ID;
  kind: string;
  provider_id: string;
  provider_revision?: string;
};
export type Change = {
  request_id: UUID;
  expected_revision: Revision;
  value: JSON;
};
export type ActionRequest = {
  request_id: UUID;
  expected_revision: Revision;
  value: Record<string, unknown>;
};
export type WorkRef = {
  task_id: string;
  operation_id?: string;
};
export type MutationReceipt = ({
  request_id: UUID;
  accepted: true;
  status: "applied";
  resource: JSON;
  auth?: AuthAttempt;
} | {
  request_id: UUID;
  accepted: true;
  status: "queued";
  resource: JSON;
  work?: WorkRef;
  auth?: AuthAttempt;
});
export type APIError = {
  error: string;
  code: string;
  retryable: boolean;
  accepted: boolean;
  request_id?: UUID;
  receipt?: MutationReceipt;
  current?: unknown;
};
export type ErrorDetail = {
  code: string;
  message: string;
  retryable: boolean;
};
export type ListResponse = {
  items: unknown[];
  next_cursor?: string;
};
export type Cost = {
  amount_usd?: number;
  status: "known" | "unknown" | "not_applicable";
};
export type AuthAttempt = {
  id: ID;
  authorize_url: string;
  expires_at: Instant;
};
export type AssistantSummary = {
  revision: Revision;
  needs_you: number;
  working: number;
  apps_attention: number;
  inbox_unread: number;
  next_scheduled_at?: Instant;
};
export type AssistantCapabilities = {
  assistant_contract?: "nori-assistant-1";
  assistant_features: {
    integrations?: boolean;
    connections?: boolean;
    mcp?: boolean;
    interactions?: boolean;
    work?: boolean;
    automations?: boolean;
    goals?: boolean;
    library?: boolean;
    search?: boolean;
    permissions?: boolean;
    preferences?: boolean;
    memory?: boolean;
    inbox?: boolean;
    operations?: boolean;
  };
  upload_accept?: UploadAccept;
};
export type Capabilities = {
  contract: "nori-chat-2";
  conversationContract?: "nori-conversations-1";
  service: "nori";
  persistence?: string;
  provider?: string;
  model?: string;
  conversationModes: "assistant"[];
  features: {
    backgroundTasks?: boolean;
    jev?: boolean;
  };
  assistant_contract?: "nori-assistant-1";
  assistant_features?: {
    integrations?: boolean;
    connections?: boolean;
    mcp?: boolean;
    interactions?: boolean;
    work?: boolean;
    automations?: boolean;
    goals?: boolean;
    library?: boolean;
    search?: boolean;
    permissions?: boolean;
    preferences?: boolean;
    memory?: boolean;
    inbox?: boolean;
    operations?: boolean;
  };
  upload_accept?: UploadAccept;
  model_profiles?: {
    default_chat_profile_id?: string;
    default_chat_reasoning?: string;
    default_task_profile_id?: string;
    default_task_reasoning?: string;
    profiles: ({
      id: string;
      version: string;
      label: string;
      backend: "local" | "gateway";
      model: string;
      lanes: ("chat" | "task")[];
      supported_reasoning?: string[];
      default_reasoning?: string;
      paid: boolean;
    })[];
  };
};
export type WorkingHours = {
  day: number;
  start: string;
  end: string;
}[];
export type Preferences = {
  revision: Revision;
  timezone: Timezone;
  locale?: string;
  working_hours: WorkingHours;
  quiet_hours?: {
    start: string;
    end: string;
  };
  default_destination_ids: ID[];
  account_defaults: Record<string, ID>;
  budgets: {
    task_usd: number;
    monthly_usd?: number;
  };
  input?: {
    dictation_language?: string;
  };
  onboarding_completed_at?: Instant;
  data_reset_at?: Instant;
  updated_at?: Instant;
};
export type PreferencesEditable = {
  timezone: Timezone;
  locale?: string;
  working_hours: WorkingHours;
  quiet_hours?: {
    start: string;
    end: string;
  };
  default_destination_ids: ID[];
  account_defaults: Record<string, ID>;
  budgets: {
    task_usd: number;
    monthly_usd?: number;
  };
  input?: {
    dictation_language?: string;
  };
  onboarding_completed_at?: Instant;
};
export type PreferencesChange = {
  request_id: UUID;
  expected_revision: Revision;
  value: PreferencesEditable;
};
export type ErrorCode = "revision_conflict" | "idempotency_conflict" | "interaction_stale" | "validation_failed" | "not_found" | "credential_store_unavailable" | "provider_unavailable";

export type ConnectionStatus = "not_connected" | "connecting" | "connected" | "needs_attention" | "disabled" | "disconnecting" | "disconnected";
export type Health = {
  checked_at?: Instant;
  code?: HealthCode;
  message?: string;
  last_success_at?: Instant;
  recovery?: Recovery;
  affected_capability_ids?: string[];
  retry_after?: Instant;
};
export type Connection = {
  id: ID;
  revision: Revision;
  integration_id: string;
  title: string;
  account?: {
    provider_id: string;
    label: string;
  };
  status: ConnectionStatus;
  granted_scopes: string[];
  selected_resource_ids: string[];
  health: Health;
  defaults: string[];
  created_at?: Instant;
  updated_at?: Instant;
  missing_scopes?: string[];
  capabilities?: string[];
};
export type ConnectionEditable = {
  integration_id: string;
  title: string;
  selected_resource_ids: string[];
  configuration?: Record<string, unknown>;
};
export type ConnectionChange = {
  request_id: UUID;
  expected_revision: Revision;
  value: ConnectionEditable;
};
export type ConnectionAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: ({
    action: "connect";
    capabilities?: string[];
    return_to?: string;
    username?: string;
    password?: string;
  } | {
    action: "repair";
    return_to?: string;
    capabilities?: string[];
    username?: string;
    password?: string;
  } | {
    action: "disconnect";
    purge_cache?: boolean;
    delete_imported_content?: boolean;
  } | {
    action: "enable" | "disable";
  });
};
export type Resource = {
  ref: ResourceRef;
  title: string;
  kind: string;
  selected?: boolean;
  writable?: boolean;
};
export type HealthCode = "ready" | "needs_sign_in" | "needs_more_access" | "revoked" | "rate_limited" | "temporarily_failing" | "provider_unavailable" | "credential_store_unavailable";
export type Recovery = "connect" | "repair" | "request_access" | "wait" | "none";
export type OAuthCallback = {
  state: string;
  code?: string;
  error?: string;
};
export type Integration = {
  id: string;
  title: string;
  description: string;
  categories: string[];
  icon?: string;
  source: "native" | "plugin" | "mcp";
  installation_id?: ID;
  auth: "oauth" | "app_password" | "api_key" | "none";
  multiple_accounts: boolean;
  purposes: {
    id: string;
    title: string;
  }[];
  capabilities: {
    id: string;
    title: string;
    effect: Effect;
    supported: boolean;
    reason?: string;
    scopes?: string[];
  }[];
  availability: "available" | "unavailable" | "blocked";
  availability_reason?: string;
  connection_ids: ID[];
};

export type CalendarTime = ({
  kind: "all_day";
  start_date: LocalDate;
  end_date: LocalDate;
} | {
  kind: "timed";
  start: Instant;
  end: Instant;
  timezone: Timezone;
});
export type RecurrenceScope = "instance" | "following" | "series";
export type Attendee = {
  email: string;
  name?: string;
  role: "organizer" | "required" | "optional" | "resource";
  response: "accepted" | "declined" | "tentative" | "needs_action" | "unknown";
  self?: boolean;
};
export type CalendarEvent = {
  ref: ResourceRef;
  calendar: ResourceRef;
  title: string;
  time: CalendarTime;
  location?: string;
  meeting_url?: string;
  description?: string;
  attendees?: Attendee[];
  recurrence?: {
    rrule?: string;
    series_id?: string;
    occurrence_id?: string;
    exception?: boolean;
  };
  status?: "confirmed" | "tentative" | "cancelled";
  source_url?: string;
  imported_from?: "live" | "ics";
  supports: {
    update: boolean;
    cancel: boolean;
    invite: boolean;
    rsvp: boolean;
    recurrence_scopes: RecurrenceScope[];
  };
};
export type AvailabilitySlot = {
  start: Instant;
  end: Instant;
  status: "free" | "busy" | "tentative" | "unknown";
  sources?: ResourceRef[];
};
export type MailAddress = {
  email: string;
  name?: string;
};
export type MailMessage = {
  ref: ResourceRef;
  thread: ResourceRef;
  from: MailAddress;
  to: MailAddress[];
  cc?: MailAddress[];
  subject: string;
  received_at: Instant;
  content: {
    kind: "snippet" | "full";
    text?: string;
    result_id?: string;
  };
  attachments?: {
    id: string;
    name: string;
    media_type: string;
    size: number;
    upload_id?: string;
  }[];
  labels?: string[];
  folder?: string;
  unread?: boolean;
  source_url?: string;
  supports?: {
    labels?: boolean;
    folders?: boolean;
    archive?: boolean;
    mark_read?: boolean;
  };
};
export type MailDraft = {
  ref: ResourceRef;
  sender: MailAddress;
  to: MailAddress[];
  cc: MailAddress[];
  bcc: MailAddress[];
  subject: string;
  body_text: string;
  body_html_result_id?: string;
  attachment_ids: string[];
  reply_to?: ResourceRef;
  mode?: "new" | "reply" | "reply_all" | "forward";
  state?: "draft" | "sending" | "sent" | "failed" | "uncertain";
  sent_message?: ResourceRef;
};
export type ContactCandidate = {
  source: ResourceRef;
  name: string;
  emails: string[];
  hint?: string;
};

export type Invocation = {
  task_id: string;
  instruction_revision: number;
  operation_id: string;
  call_id: string;
  tool: DefinitionRef;
  binding_digest: string;
  connection_id?: ID;
  arguments_ref?: string;
  arguments_digest?: string;
  authority_ref: string;
  parent_operation_id?: string;
};
export type PreparedAction = {
  digest: string;
  summary: string;
  effect: Effect;
  resources: ResourceRef[];
  recipients?: string[];
  provider_precondition?: string;
};
export type AdapterResult = {
  outcome: "confirmed" | "failed" | "submitted" | "uncertain";
  content: JSON;
  result_ids: string[];
  provider_refs: ResourceRef[];
  cost: Cost;
  error?: ErrorDetail;
};
export type OperationProjection = {
  id: string;
  call_id: string;
  name: string;
  tool?: DefinitionRef;
  effect_class: Effect;
  outcome: Outcome;
  cleared?: "" | "did_not_run" | "read_only" | "answered" | "contained";
  connection_id?: ID;
  account_summary?: string;
  action_summary?: string;
  authority_source?: "user_request" | "permission_rule" | "automation" | "approval" | "internal";
  provider_refs?: ResourceRef[];
  receipt_url?: string;
  result_ids?: string[];
  parent_operation_id?: string;
  cost?: Cost;
  created_at?: Instant;
  updated_at?: Instant;
  error?: ErrorDetail;
  revision: number;
  attempt?: number;
  result_ref?: string;
  artifact_ids?: string[] | null;
  action?: {
    digest: string;
    text: string;
    spec?: Record<string, string> | null;
  };
  owner?: {
    type: "task" | "message";
    id: string;
  };
  result?: Record<string, unknown>;
  binding_digest?: string;
  task_title?: string;
  recovery?: "none" | "reconciling" | "needs_review" | "resolved";
};
export type WorkItem = {
  task_id: string;
  title: string;
  status: "running" | "completed" | "failed" | "cancelled";
  bucket: "needs_you" | "working" | "finished";
  instruction_revision: number;
  origin: {
    kind: "conversation" | "automation" | "goal" | "external";
    conversation_id?: string;
    message_id?: string;
    automation_id?: ID;
    occurrence_id?: string;
    goal_id?: ID;
    continues_task_id?: string;
  };
  interaction_ids: string[];
  progress?: string;
  operations: {
    total: number;
    uncertain: number;
    failed: number;
    submitted: number;
  };
  result_ids: string[];
  cost: {
    amount_usd?: number;
    status: "known" | "unknown" | "not_applicable";
    budget_usd?: number;
  };
  parent_task_id?: string;
  started_at: Instant;
  finished_at?: Instant;
  cancel_requested: boolean;
  error?: ErrorDetail;
};
export type WorkDetail = {
  item: WorkItem;
  operations: OperationProjection[];
  interactions: Interaction[];
  result?: string;
  events?: RunEvent[];
  events_truncated?: boolean;
};
export type RunEvent = {
  id?: string;
  kind: string;
  text: string;
  created_at: Instant;
};

export type CardPart = {
  type: "card";
  schema_version: number;
  kind: "connection_request" | "question" | "approval" | "progress" | "calendar_event" | "mail_draft" | "mail_message" | "automation" | "goal" | "file" | "outcome" | "availability";
  resource: {
    kind: "interaction" | "automation" | "goal" | "connection" | "task" | "result";
    id: string;
    revision?: Revision;
  };
  actions?: string[];
  fallback: string;
  display?: "page" | "bubble";
  id?: string;
};
export type Provenance = {
  content: "generated" | "uploaded" | "copied" | "live";
  source?: ResourceRef;
  source_url?: string;
  retrieved_at?: Instant;
  connection_status?: ConnectionStatus;
};
export type LibraryItem = {
  id: string;
  root_id: string;
  version: number;
  revises?: string;
  superseded_by?: string;
  kind: "answer" | "file" | "screenshot" | "link" | "document" | "event" | "message" | "report";
  title: string;
  name?: string;
  media_type?: string;
  size?: number;
  url?: string;
  task_id?: string;
  conversation_id?: string;
  goal_id?: string;
  automation_id?: string;
  provenance: Provenance;
  index_status?: "indexed" | "pending" | "unsupported" | "failed" | "stale";
  created_at: Instant;
  updated_at?: Instant;
  deleted_at?: Instant;
  data?: unknown;
  collection?: string;
  preview_text?: string;
  delete_effect?: string;
  revision: Revision;
} & ({
  data_kind: "calendar_event";
  data?: CalendarEvent;
} | {
  data_kind: "mail_draft";
  data?: MailDraft;
} | {
  data_kind: "mail_message";
  data?: MailMessage;
} | {
  data_kind: "availability";
  data?: AvailabilityProposal;
} | {
  data_kind?: undefined;
});
export type SearchHit = {
  kind: "result" | "message" | "task" | "goal" | "automation" | "file" | "memory";
  id: string;
  title: string;
  snippet: string;
  jump?: ({
    kind: "conversation";
    conversation_id: string;
    message_id?: string;
  } | {
    kind: "task";
    task_id: string;
  } | {
    kind: "library";
    item_id: string;
  } | {
    kind: "goal";
    goal_id: ID;
  } | {
    kind: "automation";
    automation_id: ID;
  } | {
    kind: "memory";
    layer?: "core" | "log";
    memory_id?: string;
    log_date?: LocalDate;
    entry_id?: string;
  });
  provenance?: Provenance;
  freshness?: Instant;
  score?: number;
};
export type SearchResponse = {
  items: SearchHit[];
  next_cursor?: string;
  query?: string;
};
export type UploadAccept = {
  media_types: string[];
  max_bytes: number;
  extensions?: string[];
};
export type AvailabilityProposal = {
  slots: AvailabilitySlot[];
  duration_minutes: number;
  attendees: MailAddress[];
  status: "proposed" | "booked";
};
export type LibraryItemEditable = {
  title: string;
  collection?: string;
};
export type LibraryItemChange = {
  request_id: UUID;
  expected_revision: Revision;
  value: LibraryItemEditable;
};
export type LibraryAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: ({
    action: "revise";
    instruction: string;
  } | {
    action: "export";
    format: "md" | "pdf" | "docx" | "csv" | "ics" | "original";
  } | {
    action: "delete";
    scope: "version" | "all_versions";
  });
};
export type MemoryProvenance = "user_explicit" | "pre_compact_flush" | "summary_evicted" | "task_outcome" | "dream_curated";
export type MemoryFact = {
  id: ID;
  revision: Revision;
  text: string;
  about?: string;
  section?: string;
  pinned?: boolean;
  provenance?: MemoryProvenance;
  source_text?: string;
  supersedes_id?: string;
  scope: "user" | "chat";
  origin: {
    conversation_id?: string;
    message_id?: string;
    task_id?: string;
  };
  created_at: Instant;
  updated_at?: Instant;
};
export type MemoryFactEditable = {
  text: string;
  about?: string;
  section?: string;
  pinned?: boolean;
};
export type MemoryFactChange = {
  request_id: UUID;
  expected_revision: Revision;
  value: MemoryFactEditable;
};
export type MemoryAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: {
    action: "forget" | "pin" | "unpin" | "demote" | "consolidate";
  };
};
export type MemoryLogEntry = {
  id: ID;
  section?: string;
  text: string;
  about?: string;
  provenance: MemoryProvenance;
  source_input_id?: string;
  source_text?: string;
  run_id?: string;
  created_at: Instant;
  updated_at?: Instant;
};
export type MemoryLog = {
  date: LocalDate;
  revision: Revision;
  entries: MemoryLogEntry[];
  updated_at: Instant;
};
export type MemoryLogEntryAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: ({
    action: "promote";
    text?: string;
    about?: string;
    section?: string;
    pinned?: boolean;
  } | {
    action: "forget";
  });
};
export type WorkingMemoryItem = {
  id: ID;
  kind: "constraint" | "correction" | "preference" | "approval" | "open_question" | "commitment" | "decision" | "fact";
  quote: string;
  input_id: string;
  note?: string;
};
export type WorkingMemory = {
  revision: Revision;
  from?: Instant;
  extracted_through?: Instant;
  flushed_through?: Instant;
  through?: Instant;
  dream_through?: Instant;
  last_dream_at?: Instant;
  items: WorkingMemoryItem[];
  gist: string;
  turns: number;
  model?: string;
  core_bytes: number;
  core_budget_bytes: number;
  updated_at?: Instant;
};
export type WorkingMemoryItemAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: ({
    action: "promote";
    text?: string;
    about?: string;
    section?: string;
    pinned?: boolean;
  } | {
    action: "dismiss";
  });
};
export type MemorySearchHit = {
  chunk_id: string;
  layer: "core" | "log";
  memory_id?: string;
  log_date?: LocalDate;
  entry_id?: string;
  section?: string;
  about?: string;
  snippet: string;
  provenance: MemoryProvenance;
  pinned?: boolean;
  score: number;
  created_at: Instant;
  updated_at?: Instant;
};
export type MemorySearchResponse = {
  items: MemorySearchHit[];
  next_cursor?: string;
  query?: string;
};
export type InboxItem = {
  id: ID;
  delivery_id?: string;
  title: string;
  body: string;
  link: {
    kind: "interaction" | "automation" | "goal" | "connection" | "task" | "result";
    id: string;
    revision?: Revision;
  };
  created_at: Instant;
  acknowledged_at?: Instant;
  revision: Revision;
};
export type InboxAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: {
    action: "acknowledge";
  };
};

export type MCPServer = {
  id: ID;
  revision: Revision;
  source: {
    kind: "user";
  };
  name: string;
  transport: "http" | "stdio";
  endpoint?: string;
  connection_id?: ID;
  enabled: boolean;
  exposure?: {
    default?: "direct" | "discoverable" | "disabled";
    overrides?: Record<string, "direct" | "discoverable" | "programmatic" | "disabled">;
  };
  observed: {
    status: "disconnected" | "connecting" | "connected" | "needs_sign_in" | "failed" | "disabled";
    protocol_version?: string;
    capabilities?: string[];
    discovery_revision?: string;
    tool_count?: number;
    resource_count?: number;
    prompt_count?: number;
    last_success_at?: Instant;
    error?: ErrorDetail;
    diagnostics?: ({
      at: Instant;
      level: "info" | "warning" | "error";
      message: string;
    })[];
    credential?: "missing" | "present";
  };
  auth?: "none" | "oauth" | "configured";
};
export type MCPServerEditable = {
  name: string;
  endpoint?: string;
  command_ref?: string;
  environment_refs?: string[];
  auth: "none" | "oauth" | "configured";
  enabled: boolean;
  exposure: {
    default?: "direct" | "discoverable" | "disabled";
    overrides?: Record<string, "direct" | "discoverable" | "programmatic" | "disabled">;
  };
} & ({
  transport: "http";
} | {
  transport: "stdio";
});
export type MCPServerChange = {
  request_id: UUID;
  expected_revision: Revision;
  value: MCPServerEditable;
};
export type MCPServerAction = {
  request_id: UUID;
  expected_revision: Revision;
  value: ({
    action: "connect" | "reconnect" | "sign_in" | "sign_out" | "start" | "stop" | "restart";
  } | {
    action: "set_exposure";
    tool: string;
    exposure: "direct" | "discoverable" | "programmatic" | "disabled";
  });
};
export type MCPServerTool = {
  name: string;
  title: string;
  exposure: "direct" | "discoverable" | "programmatic" | "disabled";
  definition: DefinitionRef;
  availability: "ready" | "needs_connection" | "needs_permission" | "disabled" | "unavailable";
};
export type MCPCredentialInput = {
  request_id: UUID;
  token: string;
};

export type PermissionRule = {
  id: ID;
  revision: Revision;
  effect: "allow" | "ask" | "deny";
  tool_ids: string[];
  connection_ids: ID[];
  resource_ids: string[];
  action_classes: Effect[];
  recipients?: string[];
  automation_id?: ID;
  expires_at?: Instant;
  max_operations?: number;
  title?: string;
  created_at?: Instant;
  updated_at?: Instant;
  goal_id?: ID;
  summary?: string;
};
export type PermissionRuleEditable = {
  effect: "allow" | "ask" | "deny";
  tool_ids: string[];
  connection_ids: ID[];
  resource_ids: string[];
  action_classes: Effect[];
  recipients?: string[];
  automation_id?: ID;
  expires_at?: Instant;
  max_operations?: number;
  title?: string;
  goal_id?: ID;
};
export type PermissionRuleChange = {
  request_id: UUID;
  expected_revision: Revision;
  value: PermissionRuleEditable;
};
export type PermissionDecision = {
  result: "allow" | "deny" | "needs_interaction";
  reason: string;
  interaction_id?: ID;
};
export type QuestionPayload = {
  input: "text" | "choice" | "multi_choice" | "date" | "confirm";
  options?: {
    id: string;
    label: string;
  }[];
  constraints?: {
    max_length?: number;
    pattern?: string;
  };
};
export type ApprovalPayload = {
  summary: string;
  tool_id?: string;
  warning?: string;
  effect: Effect;
  account: {
    connection_id: ID;
    label: string;
  };
  resources: ResourceRef[];
  recipients?: string[];
  material_effects?: string[];
  card?: CardPart;
};
export type AccountSelectionPayload = {
  purpose: string;
  eligible_connection_ids: ID[];
  remember_as_default?: boolean;
};
export type SignInPayload = {
  auth_attempt_id?: ID;
  integration_id: string;
  connection_id?: string;
  authorize_url?: string;
  requested_scopes?: string[];
  return_to?: string;
  expires_at?: Instant;
} & ({
  auth: "oauth";
} | {
  auth: "app_password";
});
export type Interaction = {
  id: ID;
  revision: Revision;
  task_id?: string;
  instruction_revision?: number;
  conversation_id?: string;
  automation_id?: ID;
  status: "pending" | "resolved" | "expired" | "superseded";
  prompt: string;
  payload: JSON;
  action_digest?: string;
  expires_at?: Instant;
  created_at?: Instant;
  resolved_at?: Instant;
  legacy_question_id?: string;
  superseded_by?: ID;
  task_title?: string;
  resolution?: {
    kind: "answer" | "approve" | "decline" | "choose_account" | "signed_in" | "expired";
    text?: string;
    connection_id?: ID;
    at: Instant;
  };
} & ({
  kind: "question";
  payload: QuestionPayload;
} | {
  kind: "approval";
  payload: ApprovalPayload;
} | {
  kind: "account_selection";
  payload: AccountSelectionPayload;
} | {
  kind: "sign_in";
  payload: SignInPayload;
});
export type InteractionResponse = {
  request_id: UUID;
  expected_revision: Revision;
  value: ({
    kind: "answer";
    text: string;
    option_ids?: string[];
  } | {
    kind: "approve" | "decline";
    action_digest: string;
    remember_rule?: boolean;
  } | {
    kind: "choose_account";
    connection_id: ID;
    remember_as_default?: boolean;
  } | {
    kind: "decline";
  });
};

export type Binding = {
  adapter: "native" | "mcp";
  target: string;
  version: string;
};
export type Policy = {
  effect: Effect;
  retry: "safe" | "provider_key" | "reconcile_only";
  timeout_ms: number;
  location: "runtime";
  interrupted_cost_usd?: number;
  interrupted_cost_unknown?: boolean;
  cancellation?: "cooperative" | "none";
};
export type ToolDefinition = {
  ref: DefinitionRef;
  extension: DefinitionRef;
  model_name: string;
  title: string;
  description: string;
  input_schema: Record<string, unknown>;
  output_schema?: Record<string, unknown>;
  model_schema_transform?: string;
  binding: Binding;
  connection_type?: string;
  policy: Policy;
  exposure: "direct" | "discoverable" | "programmatic" | "disabled";
};
export type AvailableTool = {
  tool: DefinitionRef;
  title: string;
  description: string;
  connection_ids: ID[];
  availability: "ready" | "needs_connection" | "needs_permission" | "disabled" | "unavailable";
  reason_code?: string;
  recovery?: "connect" | "repair" | "enable" | "choose_account" | "request_access";
  capability: string;
  integration_id?: string;
  effect?: Effect;
  exposure?: "direct" | "discoverable" | "programmatic" | "disabled";
};
export type AvailableToolList = {
  items: AvailableTool[];
  next_cursor?: string;
};

export type AssistantFeature = keyof AssistantCapabilities["assistant_features"];
