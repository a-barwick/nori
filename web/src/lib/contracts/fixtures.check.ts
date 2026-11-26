import type { APIError, AccountSelectionPayload, ActionRequest, AdapterResult, ApprovalPayload, AssistantCapabilities, AssistantSummary, Attendee, AuthAttempt, Automation, AutomationAction, AutomationChange, AutomationEditable, AutomationPreview, AvailabilityProposal, AvailabilitySlot, AvailableTool, AvailableToolList, Binding, CalendarEvent, Capabilities, CardPart, Change, Connection, ConnectionAction, ConnectionChange, ConnectionEditable, ConnectionStatus, ContactCandidate, Cost, DefinitionRef, Delivery, Effect, ErrorCode, ErrorDetail, Goal, GoalAction, GoalChange, GoalEditable, Health, HealthCode, ID, InboxAction, InboxItem, Instant, Integration, Interaction, InteractionResponse, Invocation, JSON, LibraryAction, LibraryItem, LibraryItemChange, LibraryItemEditable, ListResponse, LocalDate, MCPCredentialInput, MCPServer, MCPServerAction, MCPServerChange, MCPServerEditable, MCPServerTool, MailAddress, MailDraft, MailMessage, MemoryAction, MemoryFact, MemoryFactEditable, MemoryLog, MemoryLogEntry, MemoryLogEntryAction, MemoryProvenance, MemorySearchHit, MemorySearchResponse, MissedRun, MutationReceipt, OAuthCallback, Occurrence, OccurrenceStatus, OperationProjection, Outcome, PermissionDecision, PermissionRule, PermissionRuleChange, PermissionRuleEditable, Policy, Preferences, PreferencesChange, PreferencesEditable, PreparedAction, Provenance, QuestionPayload, Recovery, RecurrenceScope, Resource, ResourceRef, Revision, RunEvent, SearchHit, SearchResponse, SignInPayload, Timezone, ToolDefinition, UUID, UploadAccept, WorkDetail, WorkItem, WorkRef, WorkingHours, WorkingMemory, WorkingMemoryItem, WorkingMemoryItemAction } from "./assistant";

export const fx_common_change_valid: Change = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 4,
  "value": {
    "title": "Work calendar"
  }
};

export const fx_common_receipt_applied_valid: MutationReceipt = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "accepted": true,
  "status": "applied",
  "resource": {
    "id": "conn_1",
    "revision": 5
  }
};

export const fx_common_receipt_queued_valid: MutationReceipt = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "accepted": true,
  "status": "queued",
  "resource": {
    "id": "inst_1",
    "state": "installing"
  },
  "work": {
    "task_id": "task_9"
  }
};

export const fx_common_api_error_503_accepted_valid: APIError = {
  "error": "wake failed; work is saved",
  "code": "wake_unavailable",
  "retryable": true,
  "accepted": true,
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "receipt": {
    "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
    "accepted": true,
    "status": "queued",
    "resource": {
      "id": "auto_1"
    },
    "work": {
      "task_id": "task_2"
    }
  }
};

export const fx_common_api_error_409_valid: APIError = {
  "error": "revision 4 is stale; current is 5",
  "code": "revision_conflict",
  "retryable": false,
  "accepted": false,
  "request_id": "0a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d",
  "current": {
    "id": "conn_google_work",
    "revision": 5,
    "status": "connected"
  }
};

export const fx_common_list_valid: ListResponse = {
  "items": [],
  "next_cursor": "opaque-cursor-1"
};

export const fx_tools_tool_definition_valid: ToolDefinition = {
  "ref": {
    "id": "nori.google/calendar/events.create",
    "version": "1.0.0",
    "digest": "sha256:1111"
  },
  "extension": {
    "id": "nori.google/calendar",
    "version": "1.0.0",
    "digest": "sha256:2222"
  },
  "model_name": "calendar_create_event",
  "title": "Create calendar event",
  "description": "Creates an event on a selected calendar.",
  "input_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": [
      "calendar_id",
      "title",
      "time"
    ],
    "properties": {
      "calendar_id": {
        "type": "string"
      },
      "title": {
        "type": "string"
      },
      "time": {
        "type": "object"
      },
      "attendees": {
        "type": "array",
        "items": {
          "type": "string",
          "format": "email"
        }
      }
    },
    "additionalProperties": false
  },
  "output_schema": {
    "type": "object"
  },
  "binding": {
    "adapter": "native",
    "target": "google.calendar",
    "version": "1.0.0"
  },
  "connection_type": "google",
  "policy": {
    "effect": "reversible_write",
    "retry": "provider_key",
    "timeout_ms": 20000,
    "location": "runtime",
    "interrupted_cost_usd": 0,
    "interrupted_cost_unknown": false,
    "cancellation": "none"
  },
  "exposure": "direct"
};

export const fx_tools_available_tool_ready_valid: AvailableTool = {
  "tool": {
    "id": "nori.google/calendar/events.create",
    "version": "1.0.0",
    "digest": "sha256:1111"
  },
  "title": "Create calendar event",
  "description": "Creates an event.",
  "connection_ids": [
    "conn_google_personal",
    "conn_google_work"
  ],
  "availability": "ready",
  "capability": "calendar",
  "integration_id": "google_calendar"
};

export const fx_tools_available_tool_needs_connection_valid: AvailableTool = {
  "tool": {
    "id": "nori.google/calendar/events.create",
    "version": "1.0.0",
    "digest": "sha256:1111"
  },
  "title": "Create calendar event",
  "description": "",
  "connection_ids": [],
  "availability": "needs_connection",
  "reason_code": "no_google_account",
  "recovery": "connect",
  "capability": "calendar",
  "integration_id": "google_calendar"
};

export const fx_tools_available_tool_list_valid: AvailableToolList = {
  "items": [
    {
      "tool": {
        "id": "nori.google/calendar/events.create",
        "version": "1.0.0",
        "digest": "sha256:1111"
      },
      "title": "Create calendar event",
      "description": "Creates an event.",
      "connection_ids": [
        "conn_google_personal",
        "conn_google_work"
      ],
      "availability": "ready",
      "capability": "calendar"
    }
  ]
};

export const fx_connections_connection_connected_valid: Connection = {
  "id": "conn_google_personal",
  "revision": 4,
  "integration_id": "google",
  "title": "Personal Google",
  "account": {
    "provider_id": "acct_123",
    "label": "personal@example.com"
  },
  "status": "connected",
  "granted_scopes": [
    "calendar.readonly",
    "calendar.events"
  ],
  "selected_resource_ids": [
    "primary",
    "family"
  ],
  "health": {
    "checked_at": "2026-11-09T09:00:00Z",
    "code": "ready",
    "last_success_at": "2026-11-09T09:00:00Z"
  },
  "defaults": [
    "calendar.create"
  ],
  "created_at": "2026-11-09T09:00:00Z",
  "updated_at": "2026-11-09T09:00:00Z"
};

export const fx_connections_connection_needs_attention_valid: Connection = {
  "id": "conn_google_personal",
  "revision": 4,
  "integration_id": "google",
  "title": "Personal Google",
  "account": {
    "provider_id": "acct_123",
    "label": "personal@example.com"
  },
  "status": "needs_attention",
  "granted_scopes": [
    "calendar.readonly",
    "calendar.events"
  ],
  "selected_resource_ids": [
    "primary",
    "family"
  ],
  "health": {
    "checked_at": "2026-11-09T10:00:00Z",
    "code": "needs_sign_in",
    "message": "Google reported the grant was revoked."
  },
  "defaults": [
    "calendar.create"
  ],
  "created_at": "2026-11-09T09:00:00Z",
  "updated_at": "2026-11-09T09:00:00Z"
};

export const fx_connections_connection_change_valid: ConnectionChange = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 4,
  "value": {
    "integration_id": "google",
    "title": "Personal Google",
    "selected_resource_ids": [
      "primary"
    ]
  }
};

export const fx_connections_connection_action_disconnect_valid: ConnectionAction = {
  "request_id": "0a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d",
  "expected_revision": 4,
  "value": {
    "action": "disconnect",
    "delete_imported_content": false
  }
};

export const fx_connections_connection_action_connect_valid: ConnectionAction = {
  "request_id": "0a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d",
  "expected_revision": 0,
  "value": {
    "action": "connect",
    "return_to": "/apps/google",
    "capabilities": [
      "calendar.read",
      "calendar.write"
    ]
  }
};

export const fx_connections_resource_valid: Resource = {
  "ref": {
    "connection_id": "conn_google_personal",
    "kind": "calendar",
    "provider_id": "primary",
    "provider_revision": "etag-7"
  },
  "title": "Personal",
  "kind": "calendar",
  "selected": true,
  "writable": true
};

export const fx_execution_invocation_valid: Invocation = {
  "task_id": "task_9",
  "instruction_revision": 3,
  "operation_id": "op_42",
  "call_id": "call_7",
  "tool": {
    "id": "nori.google/calendar/events.create",
    "version": "1.0.0",
    "digest": "sha256:1111"
  },
  "binding_digest": "sha256:bind1",
  "connection_id": "conn_google_personal",
  "arguments_ref": "history://task_9/op_42/args",
  "arguments_digest": "sha256:args1",
  "authority_ref": "authority://task_9/3/op_42"
};

export const fx_execution_prepared_action_valid: PreparedAction = {
  "digest": "sha256:act1",
  "summary": "Create 'Dentist' on Personal, Tue 10:00-10:30 Europe/London, invite alex@example.com",
  "effect": "sensitive_send",
  "resources": [
    {
      "connection_id": "conn_google_personal",
      "kind": "calendar",
      "provider_id": "primary",
      "provider_revision": "etag-7"
    }
  ],
  "recipients": [
    "alex@example.com"
  ],
  "provider_precondition": "etag-7"
};

export const fx_execution_adapter_result_uncertain_valid: AdapterResult = {
  "outcome": "uncertain",
  "content": null,
  "result_ids": [],
  "provider_refs": [],
  "cost": {
    "status": "not_applicable"
  },
  "error": {
    "code": "timeout",
    "message": "No response within 20s; the event may exist.",
    "retryable": false
  }
};

export const fx_execution_adapter_result_confirmed_valid: AdapterResult = {
  "outcome": "confirmed",
  "content": {
    "event_id": "evt_123"
  },
  "result_ids": [
    "res_1"
  ],
  "provider_refs": [
    {
      "connection_id": "conn_google_personal",
      "kind": "calendar_event",
      "provider_id": "evt_123",
      "provider_revision": "etag-9"
    }
  ],
  "cost": {
    "amount_usd": 0,
    "status": "known"
  }
};

export const fx_execution_operation_projection_valid: OperationProjection = {
  "id": "op_42",
  "call_id": "call_7",
  "name": "calendar_create_event",
  "tool": {
    "id": "nori.google/calendar/events.create",
    "version": "1.0.0",
    "digest": "sha256:1111"
  },
  "effect_class": "sensitive_send",
  "outcome": "confirmed",
  "connection_id": "conn_google_personal",
  "account_summary": "Personal Google (personal@example.com)",
  "action_summary": "Created 'Dentist' and invited alex@example.com",
  "authority_source": "user_request",
  "provider_refs": [
    {
      "connection_id": "conn_google_personal",
      "kind": "calendar_event",
      "provider_id": "evt_123",
      "provider_revision": "etag-9"
    }
  ],
  "receipt_url": "https://calendar.google.com/event?eid=abc",
  "result_ids": [
    "res_1"
  ],
  "cost": {
    "status": "not_applicable"
  },
  "created_at": "2026-11-09T09:00:00Z",
  "updated_at": "2026-11-09T10:00:00Z",
  "revision": 3,
  "owner": {
    "type": "task",
    "id": "task_9"
  },
  "attempt": 1,
  "result_ref": "",
  "artifact_ids": [],
  "action": {
    "digest": "sha256:act1",
    "text": "Create 'Dentist' and invite alex@example.com",
    "spec": {
      "calendar": "primary"
    }
  },
  "binding_digest": "sha256:b1"
};

export const fx_permissions_rule_allow_calendar_valid: PermissionRule = {
  "id": "rule_1",
  "revision": 2,
  "effect": "allow",
  "tool_ids": [
    "nori.google/calendar/events.create",
    "nori.google/calendar/events.update"
  ],
  "connection_ids": [
    "conn_google_personal"
  ],
  "resource_ids": [
    "primary"
  ],
  "action_classes": [
    "reversible_write"
  ],
  "title": "Manage my personal calendar",
  "created_at": "2026-11-09T09:00:00Z"
};

export const fx_permissions_rule_automation_send_valid: PermissionRule = {
  "id": "rule_2",
  "revision": 2,
  "effect": "allow",
  "tool_ids": [
    "nori.google/mail/send"
  ],
  "connection_ids": [
    "conn_google_personal"
  ],
  "resource_ids": [
    "primary"
  ],
  "action_classes": [
    "sensitive_send"
  ],
  "title": "Manage my personal calendar",
  "created_at": "2026-11-09T09:00:00Z",
  "recipients": [
    "alex@example.com"
  ],
  "automation_id": "auto_1",
  "expires_at": "2027-01-01T00:00:00Z",
  "max_operations": 10
};

export const fx_permissions_rule_change_valid: PermissionRuleChange = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 0,
  "value": {
    "effect": "deny",
    "tool_ids": [
      "*"
    ],
    "connection_ids": [
      "conn_google_work"
    ],
    "resource_ids": [
      "*"
    ],
    "action_classes": [
      "sensitive_send"
    ],
    "title": "Never send from work"
  }
};

export const fx_permissions_decision_needs_interaction_valid: PermissionDecision = {
  "result": "needs_interaction",
  "reason": "sensitive_send to a new recipient requires approval",
  "interaction_id": "int_5"
};

export const fx_permissions_interaction_question_valid: Interaction = {
  "id": "int_1",
  "revision": 1,
  "task_id": "task_9",
  "instruction_revision": 3,
  "kind": "question",
  "status": "pending",
  "prompt": "Which Alex do you mean?",
  "payload": {
    "input": "choice",
    "options": [
      {
        "id": "c1",
        "label": "Alex Kim (alex@example.com)"
      },
      {
        "id": "c2",
        "label": "Alex Ruiz (aruiz@work.example)"
      }
    ]
  },
  "created_at": "2026-11-09T09:00:00Z"
};

export const fx_permissions_interaction_approval_valid: Interaction = {
  "id": "int_5",
  "revision": 1,
  "task_id": "task_9",
  "instruction_revision": 3,
  "kind": "approval",
  "status": "pending",
  "prompt": "Send the invite?",
  "action_digest": "sha256:act1",
  "payload": {
    "summary": "Invite alex@example.com to 'Dentist'",
    "effect": "sensitive_send",
    "account": {
      "connection_id": "conn_google_personal",
      "label": "personal@example.com"
    },
    "resources": [
      {
        "connection_id": "conn_google_personal",
        "kind": "calendar",
        "provider_id": "primary",
        "provider_revision": "etag-7"
      }
    ],
    "recipients": [
      "alex@example.com"
    ],
    "material_effects": [
      "Alex receives an email invitation"
    ]
  },
  "expires_at": "2026-11-09T10:00:00Z",
  "created_at": "2026-11-09T09:00:00Z"
};

export const fx_permissions_interaction_sign_in_valid: Interaction = {
  "id": "int_7",
  "revision": 1,
  "kind": "sign_in",
  "status": "pending",
  "prompt": "Sign in to Google to continue",
  "payload": {
    "auth": "oauth",
    "auth_attempt_id": "auth_1",
    "integration_id": "google",
    "authorize_url": "https://accounts.google.com/o/oauth2/v2/auth?state=opaque",
    "requested_scopes": [
      "calendar.events"
    ],
    "return_to": "/apps/google",
    "expires_at": "2026-11-09T10:00:00Z"
  },
  "expires_at": "2026-11-09T10:00:00Z"
};

export const fx_permissions_interaction_account_selection_valid: Interaction = {
  "id": "int_8",
  "revision": 1,
  "task_id": "task_9",
  "instruction_revision": 3,
  "kind": "account_selection",
  "status": "pending",
  "prompt": "Which calendar account?",
  "payload": {
    "purpose": "calendar.create",
    "eligible_connection_ids": [
      "conn_google_personal",
      "conn_google_work"
    ],
    "remember_as_default": true
  }
};

export const fx_permissions_interaction_legacy_question_valid: Interaction = {
  "id": "int_legacy_1",
  "revision": 1,
  "task_id": "task_9",
  "instruction_revision": 3,
  "kind": "question",
  "status": "pending",
  "prompt": "Which Alex do you mean?",
  "payload": {
    "input": "text"
  },
  "created_at": "2026-11-09T09:00:00Z",
  "legacy_question_id": "q_abc"
};

export const fx_permissions_response_approve_valid: InteractionResponse = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 1,
  "value": {
    "kind": "approve",
    "action_digest": "sha256:act1"
  }
};

export const fx_permissions_response_answer_valid: InteractionResponse = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 1,
  "value": {
    "kind": "answer",
    "text": "The first one",
    "option_ids": [
      "c1"
    ]
  }
};

export const fx_permissions_response_choose_account_valid: InteractionResponse = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 1,
  "value": {
    "kind": "choose_account",
    "connection_id": "conn_google_work",
    "remember_as_default": false
  }
};

export const fx_automations_automation_recurring_valid: Automation = {
  "id": "auto_1",
  "revision": 3,
  "title": "Morning briefing",
  "instruction": "Summarize today's agenda, urgent mail and due tasks.",
  "status": "enabled",
  "trigger": {
    "kind": "recurring",
    "local_start": "2026-11-10T07:30",
    "timezone": "Europe/London",
    "rrule": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"
  },
  "connection_ids": [
    "conn_google_personal"
  ],
  "budget_usd": 0.5,
  "max_active_runs": 1,
  "missed_run": "latest",
  "execution_window_seconds": 3600,
  "destination_ids": [
    "dest_inapp",
    "dest_push"
  ],
  "next_at": "2026-11-10T07:30:00Z",
  "created_at": "2026-11-09T09:00:00Z",
  "updated_at": "2026-11-09T09:00:00Z",
  "authority_rule_ids": [
    "rule_auto_1"
  ]
};

export const fx_automations_automation_once_valid: Automation = {
  "id": "auto_2",
  "revision": 3,
  "title": "Leave for dentist",
  "instruction": "Remind me to leave.",
  "status": "enabled",
  "trigger": {
    "kind": "once",
    "at": "2026-11-11T09:30:00Z"
  },
  "connection_ids": [
    "conn_google_personal"
  ],
  "budget_usd": 0.5,
  "max_active_runs": 1,
  "missed_run": "skip",
  "execution_window_seconds": 900,
  "destination_ids": [
    "dest_inapp",
    "dest_push"
  ],
  "next_at": "2026-11-10T07:30:00Z",
  "created_at": "2026-11-09T09:00:00Z",
  "updated_at": "2026-11-09T09:00:00Z",
  "authority_rule_ids": [
    "rule_auto_2"
  ]
};

export const fx_automations_automation_event_relative_valid: Automation = {
  "id": "auto_3",
  "revision": 3,
  "title": "Morning briefing",
  "instruction": "Summarize today's agenda, urgent mail and due tasks.",
  "status": "enabled",
  "trigger": {
    "kind": "event_relative",
    "event": {
      "connection_id": "conn_google_personal",
      "kind": "calendar_event",
      "provider_id": "evt_123",
      "provider_revision": "etag-9"
    },
    "offset_seconds": -1800
  },
  "connection_ids": [
    "conn_google_personal"
  ],
  "budget_usd": 0.5,
  "max_active_runs": 1,
  "missed_run": "latest",
  "execution_window_seconds": 3600,
  "destination_ids": [
    "dest_inapp",
    "dest_push"
  ],
  "next_at": "2026-11-10T07:30:00Z",
  "created_at": "2026-11-09T09:00:00Z",
  "updated_at": "2026-11-09T09:00:00Z",
  "authority_rule_ids": [
    "rule_auto_3"
  ]
};

export const fx_automations_automation_watch_valid: Automation = {
  "id": "auto_4",
  "revision": 3,
  "title": "Morning briefing",
  "instruction": "Summarize today's agenda, urgent mail and due tasks.",
  "status": "enabled",
  "trigger": {
    "kind": "watch",
    "source": {
      "connection_id": "conn_google_personal",
      "kind": "mail_thread",
      "provider_id": "thr_1"
    },
    "predicate_ref": "stored://auto_4/predicate/1",
    "min_interval_seconds": 900
  },
  "connection_ids": [
    "conn_google_personal"
  ],
  "budget_usd": 0.5,
  "max_active_runs": 1,
  "missed_run": "latest",
  "execution_window_seconds": 3600,
  "destination_ids": [
    "dest_inapp",
    "dest_push"
  ],
  "next_at": "2026-11-10T07:30:00Z",
  "created_at": "2026-11-09T09:00:00Z",
  "updated_at": "2026-11-09T09:00:00Z",
  "authority_rule_ids": [
    "rule_auto_4"
  ]
};

export const fx_automations_automation_change_valid: AutomationChange = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 0,
  "value": {
    "title": "Morning briefing",
    "instruction": "Summarize today.",
    "trigger": {
      "kind": "recurring",
      "local_start": "2026-11-10T07:30",
      "timezone": "Europe/London",
      "rrule": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"
    },
    "connection_ids": [
      "conn_google_personal"
    ],
    "requested_authority": {
      "action_classes": [
        "read"
      ]
    },
    "budget_usd": 0.5,
    "max_active_runs": 1,
    "missed_run": "latest",
    "execution_window_seconds": 3600,
    "destination_ids": [
      "dest_inapp"
    ]
  }
};

export const fx_automations_automation_action_snooze_valid: AutomationAction = {
  "request_id": "0a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d",
  "expected_revision": 3,
  "value": {
    "action": "snooze",
    "until": "2026-11-11T07:30:00Z"
  }
};

export const fx_automations_automation_action_pause_valid: AutomationAction = {
  "request_id": "0a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d",
  "expected_revision": 3,
  "value": {
    "action": "pause"
  }
};

export const fx_automations_occurrence_valid: Occurrence = {
  "id": "auto_1:2026-11-10T07:30:00Z:g2",
  "automation_id": "auto_1",
  "automation_revision": 3,
  "generation": 2,
  "scheduled_at": "2026-11-10T07:30:00Z",
  "status": "completed",
  "task_ids": [
    "task_20"
  ],
  "delivery_ids": [
    "dlv_1"
  ],
  "started_at": "2026-11-10T07:30:02Z",
  "finished_at": "2026-11-10T07:31:40Z",
  "summary": "3 meetings, 1 urgent email"
};

export const fx_automations_delivery_valid: Delivery = {
  "id": "dlv_1",
  "occurrence_id": "auto_1:2026-11-10T07:30:00Z:g2",
  "result_ids": [
    "res_7"
  ],
  "destination_id": "dest_push",
  "state": "accepted",
  "attempt_id": "att_1",
  "receipt": "apns:abc",
  "created_at": "2026-11-09T09:00:00Z"
};

export const fx_automations_goal_valid: Goal = {
  "id": "goal_1",
  "revision": 2,
  "title": "Book the dentist",
  "outcome": "A dentist appointment is booked before December.",
  "completion_criteria": [
    {
      "id": "c1",
      "text": "Appointment exists on Personal calendar",
      "met": false
    }
  ],
  "status": "active",
  "progress": "Two practices contacted; awaiting one reply.",
  "next_actions": [
    "Follow up with Smile Clinic if no reply by Friday"
  ],
  "blockers": [
    {
      "text": "Smile Clinic has not replied"
    }
  ],
  "linked": {
    "task_ids": [
      "task_9"
    ],
    "automation_ids": [
      "auto_5"
    ],
    "result_ids": []
  },
  "deadline": "2026-12-01T00:00:00Z",
  "follow_up": "monitor",
  "created_at": "2026-11-09T09:00:00Z"
};

export const fx_automations_goal_change_valid: GoalChange = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 0,
  "value": {
    "title": "Book the dentist",
    "outcome": "Booked before December",
    "completion_criteria": [
      {
        "text": "Appointment on calendar"
      }
    ],
    "follow_up": "remind"
  }
};

export const fx_automations_goal_action_complete_valid: GoalAction = {
  "request_id": "0a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d",
  "expected_revision": 2,
  "value": {
    "action": "complete",
    "keep_monitors": false
  }
};

export const fx_library_card_approval_valid: CardPart = {
  "type": "card",
  "schema_version": 1,
  "kind": "approval",
  "resource": {
    "kind": "interaction",
    "id": "int_5",
    "revision": 1
  },
  "actions": [
    "approve",
    "decline"
  ],
  "fallback": "Nori needs your approval to invite alex@example.com to 'Dentist'."
};

export const fx_library_card_calendar_event_valid: CardPart = {
  "type": "card",
  "schema_version": 1,
  "kind": "calendar_event",
  "resource": {
    "kind": "result",
    "id": "res_evt_123",
    "revision": 1
  },
  "actions": [
    "open"
  ],
  "fallback": "Dentist, Tue 10:00-10:30 on Personal"
};

export const fx_library_library_item_copied_valid: LibraryItem = {
  "id": "res_7",
  "root_id": "res_7",
  "version": 1,
  "kind": "report",
  "title": "Morning briefing, 10 Nov",
  "media_type": "text/markdown",
  "size": 2048,
  "url": "/api/v1/library/res_7/file",
  "task_id": "task_20",
  "automation_id": "auto_1",
  "provenance": {
    "content": "generated",
    "retrieved_at": "2026-11-10T07:31:00Z"
  },
  "index_status": "indexed",
  "created_at": "2026-11-10T07:31:40Z",
  "revision": 1
};

export const fx_library_library_item_copied_from_disconnected_valid: LibraryItem = {
  "id": "res_8",
  "root_id": "res_8",
  "version": 2,
  "revises": "res_8v1",
  "kind": "document",
  "title": "Reservation confirmation",
  "provenance": {
    "content": "copied",
    "source": {
      "connection_id": "conn_google_personal",
      "kind": "mail_message",
      "provider_id": "msg_1"
    },
    "retrieved_at": "2026-11-09T09:00:00Z",
    "connection_status": "disconnected"
  },
  "created_at": "2026-11-09T09:00:00Z",
  "revision": 1
};

export const fx_library_search_response_valid: SearchResponse = {
  "query": "comparison from last week",
  "items": [
    {
      "kind": "result",
      "id": "res_3",
      "title": "Laptop comparison",
      "snippet": "...the Framework 13 wins on repairability...",
      "jump": {
        "kind": "conversation",
        "conversation_id": "conv_1",
        "message_id": "msg_55"
      },
      "freshness": "2026-11-09T09:00:00Z",
      "score": 0.82
    }
  ]
};

export const fx_library_upload_accept_valid: UploadAccept = {
  "media_types": [
    "image/png",
    "image/jpeg",
    "application/pdf",
    "text/csv",
    "text/plain",
    "text/markdown",
    "text/calendar",
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    "audio/mpeg"
  ],
  "max_bytes": 26214400
};

export const fx_mcp_mcp_server_valid: MCPServer = {
  "id": "mcp_1",
  "revision": 2,
  "source": {
    "kind": "user"
  },
  "name": "linear",
  "transport": "http",
  "endpoint": "https://mcp.linear.app/mcp",
  "connection_id": "conn_linear",
  "enabled": true,
  "exposure": {
    "default": "discoverable",
    "overrides": {
      "linear.issues.create": "direct"
    }
  },
  "observed": {
    "status": "connected",
    "protocol_version": "2026-07-28",
    "capabilities": [
      "tools",
      "resources",
      "prompts",
      "logging"
    ],
    "discovery_revision": "sha256:disc1",
    "tool_count": 24,
    "resource_count": 3,
    "prompt_count": 1,
    "last_success_at": "2026-11-09T09:00:00Z"
  }
};

export const fx_domain_calendar_event_timed_valid: CalendarEvent = {
  "ref": {
    "connection_id": "conn_google_personal",
    "kind": "calendar_event",
    "provider_id": "evt_123",
    "provider_revision": "etag-9"
  },
  "calendar": {
    "connection_id": "conn_google_personal",
    "kind": "calendar",
    "provider_id": "primary",
    "provider_revision": "etag-7"
  },
  "title": "Dentist",
  "time": {
    "kind": "timed",
    "start": "2026-11-11T10:00:00Z",
    "end": "2026-11-11T10:30:00Z",
    "timezone": "Europe/London"
  },
  "location": "12 High St",
  "attendees": [
    {
      "email": "personal@example.com",
      "role": "organizer",
      "response": "accepted",
      "self": true
    },
    {
      "email": "alex@example.com",
      "name": "Alex Kim",
      "role": "required",
      "response": "needs_action"
    }
  ],
  "status": "confirmed",
  "source_url": "https://calendar.google.com/event?eid=abc",
  "imported_from": "live",
  "supports": {
    "update": true,
    "cancel": true,
    "invite": true,
    "rsvp": true,
    "recurrence_scopes": [
      "instance",
      "following",
      "series"
    ]
  }
};

export const fx_domain_calendar_event_all_day_ics_valid: CalendarEvent = {
  "ref": {
    "connection_id": "upload_ics_1",
    "kind": "calendar_event",
    "provider_id": "uid-1@example"
  },
  "calendar": {
    "connection_id": "conn_google_personal",
    "kind": "calendar",
    "provider_id": "primary",
    "provider_revision": "etag-7"
  },
  "title": "Conference",
  "time": {
    "kind": "all_day",
    "start_date": "2026-11-20",
    "end_date": "2026-11-22"
  },
  "location": "12 High St",
  "attendees": [],
  "status": "confirmed",
  "source_url": "https://calendar.google.com/event?eid=abc",
  "imported_from": "ics",
  "supports": {
    "update": false,
    "cancel": false,
    "invite": false,
    "rsvp": false,
    "recurrence_scopes": []
  }
};

export const fx_domain_availability_unknown_valid: AvailabilitySlot = {
  "start": "2026-11-11T09:00:00Z",
  "end": "2026-11-11T12:00:00Z",
  "status": "unknown",
  "sources": [
    {
      "connection_id": "conn_google_work",
      "kind": "calendar",
      "provider_id": "alex@work.example"
    }
  ]
};

export const fx_domain_mail_message_snippet_valid: MailMessage = {
  "ref": {
    "connection_id": "conn_google_personal",
    "kind": "mail_message",
    "provider_id": "msg_1"
  },
  "thread": {
    "connection_id": "conn_google_personal",
    "kind": "mail_thread",
    "provider_id": "thr_1"
  },
  "from": {
    "email": "bookings@hotel.example",
    "name": "Hotel"
  },
  "to": [
    {
      "email": "personal@example.com"
    }
  ],
  "subject": "Your reservation",
  "received_at": "2026-11-09T09:00:00Z",
  "content": {
    "kind": "snippet",
    "text": "Your room is confirmed for 20-22 Nov..."
  },
  "attachments": [
    {
      "id": "att_1",
      "name": "confirmation.pdf",
      "media_type": "application/pdf",
      "size": 48213
    }
  ],
  "labels": [
    "INBOX"
  ],
  "unread": false,
  "source_url": "https://mail.google.com/mail/u/0/#inbox/thr_1",
  "supports": {
    "labels": true,
    "folders": false,
    "archive": true,
    "mark_read": true
  }
};

export const fx_domain_mail_draft_valid: MailDraft = {
  "ref": {
    "connection_id": "conn_google_personal",
    "kind": "mail_draft",
    "provider_id": "drf_1"
  },
  "sender": {
    "email": "personal@example.com"
  },
  "to": [
    {
      "email": "alex@example.com",
      "name": "Alex Kim"
    }
  ],
  "cc": [],
  "bcc": [],
  "subject": "Re: Dentist",
  "body_text": "Moved to Tuesday 10:00.",
  "attachment_ids": [],
  "reply_to": {
    "connection_id": "conn_google_personal",
    "kind": "mail_message",
    "provider_id": "msg_2"
  },
  "mode": "reply",
  "state": "draft"
};

export const fx_domain_contact_candidate_valid: ContactCandidate = {
  "source": {
    "connection_id": "conn_google_personal",
    "kind": "contact",
    "provider_id": "people/c1"
  },
  "name": "Alex Kim",
  "emails": [
    "alex@example.com"
  ],
  "hint": "3 threads in the last month"
};

export const fx_common_assistant_summary_valid: AssistantSummary = {
  "revision": 12,
  "needs_you": 2,
  "working": 1,
  "apps_attention": 1,
  "inbox_unread": 0,
  "next_scheduled_at": "2026-11-10T07:30:00Z"
};

export const fx_common_capabilities_valid: AssistantCapabilities = {
  "assistant_features": {
    "connections": true,
    "integrations": true,
    "work": false,
    "automations": false
  },
  "upload_accept": {
    "media_types": [
      "image/png",
      "application/pdf",
      "text/csv",
      "text/plain",
      "text/markdown",
      "text/calendar"
    ],
    "max_bytes": 20971520
  }
};

export const fx_common_preferences_valid: Preferences = {
  "revision": 3,
  "timezone": "America/Los_Angeles",
  "locale": "en-US",
  "working_hours": [
    {
      "day": 1,
      "start": "09:00",
      "end": "17:30"
    },
    {
      "day": 2,
      "start": "09:00",
      "end": "17:30"
    },
    {
      "day": 3,
      "start": "09:00",
      "end": "17:30"
    },
    {
      "day": 4,
      "start": "09:00",
      "end": "17:30"
    },
    {
      "day": 5,
      "start": "09:00",
      "end": "17:30"
    }
  ],
  "quiet_hours": {
    "start": "22:00",
    "end": "07:00"
  },
  "default_destination_ids": [
    "dest_inapp"
  ],
  "account_defaults": {
    "calendar.create": "conn_google_personal",
    "mail.send": "conn_hey"
  },
  "budgets": {
    "task_usd": 1.0,
    "monthly_usd": 50
  }
};

export const fx_common_preferences_change_valid: PreferencesChange = {
  "request_id": "6f1c2a8e-4b7d-4c1e-9a2f-0d3e5b7c9a11",
  "expected_revision": 3,
  "value": {
    "timezone": "America/New_York",
    "working_hours": [],
    "default_destination_ids": [],
    "account_defaults": {
      "calendar.create": "conn_google_work"
    },
    "budgets": {
      "task_usd": 2
    }
  }
};

export const fx_common_receipt_queued_with_auth_valid: MutationReceipt = {
  "request_id": "6f1c2a8e-4b7d-4c1e-9a2f-0d3e5b7c9a11",
  "accepted": true,
  "status": "queued",
  "resource": {
    "id": "conn_google_personal",
    "revision": 1,
    "status": "connecting"
  },
  "auth": {
    "id": "att_7",
    "authorize_url": "https://accounts.google.com/o/oauth2/v2/auth?client_id=example&state=opaque",
    "expires_at": "2026-11-09T09:10:00Z"
  }
};

export const fx_connections_integration_ready_valid: Integration = {
  "id": "google_calendar",
  "title": "Google Calendar",
  "description": "Read and change events on calendars you choose.",
  "categories": [
    "calendar"
  ],
  "icon": "google-calendar",
  "source": "native",
  "auth": "oauth",
  "multiple_accounts": true,
  "purposes": [
    {
      "id": "calendar.read",
      "title": "Read events"
    },
    {
      "id": "calendar.create",
      "title": "Create events"
    }
  ],
  "capabilities": [
    {
      "id": "calendar.read",
      "title": "Read events",
      "effect": "read",
      "supported": true,
      "scopes": [
        "https://www.googleapis.com/auth/calendar.readonly"
      ]
    },
    {
      "id": "calendar.write",
      "title": "Create and change events",
      "effect": "reversible_write",
      "supported": true
    }
  ],
  "availability": "available",
  "connection_ids": [
    "conn_google_personal"
  ]
};

export const fx_connections_integration_blocked_valid: Integration = {
  "id": "outlook_calendar",
  "title": "Outlook Calendar",
  "description": "Microsoft 365 and Outlook.com calendars.",
  "categories": [
    "calendar"
  ],
  "source": "native",
  "auth": "oauth",
  "multiple_accounts": true,
  "purposes": [],
  "capabilities": [],
  "availability": "blocked",
  "availability_reason": "This Nori deployment has no Microsoft app registration yet.",
  "connection_ids": []
};

export const fx_connections_integration_mcp_valid: Integration = {
  "id": "mcp_linear",
  "title": "Linear",
  "description": "Linear through its MCP server.",
  "categories": [
    "projects"
  ],
  "source": "mcp",
  "installation_id": "inst_linear",
  "auth": "oauth",
  "multiple_accounts": false,
  "purposes": [
    {
      "id": "projects.read",
      "title": "Read issues"
    }
  ],
  "capabilities": [
    {
      "id": "projects.read",
      "title": "Read issues",
      "effect": "read",
      "supported": true
    }
  ],
  "availability": "available",
  "connection_ids": []
};

export const fx_connections_connection_action_disconnect_flags_valid: ConnectionAction = {
  "request_id": "6f1c2a8e-4b7d-4c1e-9a2f-0d3e5b7c9a11",
  "expected_revision": 5,
  "value": {
    "action": "disconnect",
    "purge_cache": true,
    "delete_imported_content": false
  }
};

export const fx_connections_oauth_callback_valid: OAuthCallback = {
  "state": "0123456789abcdef0123",
  "code": "4/0Abc"
};

export const fx_connections_oauth_callback_cancelled_valid: OAuthCallback = {
  "state": "0123456789abcdef0123",
  "error": "access_denied"
};

export const fx_connections_connection_health_repair_valid: Connection = {
  "id": "conn_google_work",
  "revision": 7,
  "integration_id": "google_calendar",
  "title": "Work calendar",
  "account": {
    "provider_id": "1093",
    "label": "work@example.com"
  },
  "status": "needs_attention",
  "granted_scopes": [
    "https://www.googleapis.com/auth/calendar.readonly"
  ],
  "missing_scopes": [
    "https://www.googleapis.com/auth/calendar.events"
  ],
  "capabilities": [
    "calendar.read"
  ],
  "selected_resource_ids": [
    "primary"
  ],
  "health": {
    "checked_at": "2026-11-09T09:00:00Z",
    "code": "needs_more_access",
    "recovery": "request_access",
    "message": "Creating events needs more access.",
    "affected_capability_ids": [
      "calendar.write"
    ]
  },
  "defaults": []
};

export const fx_permissions_interaction_superseded_valid: Interaction = {
  "id": "int_5",
  "revision": 2,
  "task_id": "task_9",
  "instruction_revision": 3,
  "kind": "approval",
  "status": "superseded",
  "superseded_by": "int_6",
  "task_title": "Book the dentist",
  "prompt": "Send the invite?",
  "action_digest": "sha256:act1",
  "payload": {
    "summary": "Invite alex@example.com",
    "effect": "sensitive_send",
    "account": {
      "connection_id": "conn_google_personal",
      "label": "personal@example.com"
    },
    "resources": []
  },
  "resolution": {
    "kind": "expired",
    "at": "2026-11-09T09:05:00Z"
  }
};

export const fx_permissions_rule_with_summary_valid: PermissionRule = {
  "id": "rule_auto_1",
  "revision": 1,
  "effect": "allow",
  "tool_ids": [
    "nori.google/calendar/events.list"
  ],
  "connection_ids": [
    "conn_google_personal"
  ],
  "resource_ids": [
    "primary"
  ],
  "action_classes": [
    "read"
  ],
  "automation_id": "auto_1",
  "summary": "Read events on Personal (primary) for the morning briefing"
};

export const fx_execution_work_item_needs_you_valid: WorkItem = {
  "task_id": "task_9",
  "title": "Book the dentist",
  "status": "running",
  "bucket": "needs_you",
  "instruction_revision": 3,
  "origin": {
    "kind": "conversation",
    "conversation_id": "assistant",
    "message_id": "msg_1"
  },
  "interaction_ids": [
    "int_5"
  ],
  "progress": "Waiting for approval to send the invite",
  "operations": {
    "total": 3,
    "uncertain": 0,
    "failed": 0,
    "submitted": 0
  },
  "result_ids": [],
  "cost": {
    "amount_usd": 0.04,
    "status": "known",
    "budget_usd": 1
  },
  "started_at": "2026-11-09T09:00:00Z",
  "cancel_requested": false
};

export const fx_execution_work_item_uncertain_valid: WorkItem = {
  "task_id": "task_10",
  "title": "Send the update",
  "status": "completed",
  "bucket": "finished",
  "instruction_revision": 1,
  "origin": {
    "kind": "automation",
    "automation_id": "auto_1",
    "occurrence_id": "occ_1"
  },
  "interaction_ids": [],
  "operations": {
    "total": 2,
    "uncertain": 1,
    "failed": 0,
    "submitted": 0
  },
  "result_ids": [
    "res_2"
  ],
  "cost": {
    "status": "unknown"
  },
  "started_at": "2026-11-09T09:00:00Z",
  "finished_at": "2026-11-09T09:02:00Z",
  "cancel_requested": false
};

export const fx_execution_operation_projection_existing_wire_valid: OperationProjection = {
  "id": "ec27d94f-3b01-55d9-ba6a-0c4bf45199bc-m1-t1",
  "call_id": "call_PkF1OXEp17gbYRyrW4pNc0L2",
  "name": "web_search",
  "effect_class": "read",
  "outcome": "confirmed",
  "result_ref": "8cdaae2746815800f3d241f2fcf31d89791c631c6f5c33fbe355bd0afe4123fe",
  "revision": 1,
  "attempt": 1,
  "artifact_ids": null,
  "owner": {
    "type": "task",
    "id": "ec27d94f-3b01-55d9-ba6a-0c4bf45199bc"
  }
};

export const fx_automations_automation_preview_dst_valid: AutomationPreview = {
  "next_runs": [
    "2026-10-26T15:00:00Z",
    "2026-11-02T16:00:00Z",
    "2026-11-09T16:00:00Z"
  ],
  "timezone": "America/Los_Angeles",
  "proposed_rules": [
    {
      "effect": "allow",
      "tool_ids": [
        "nori.google/calendar/events.list"
      ],
      "connection_ids": [
        "conn_google_personal"
      ],
      "resource_ids": [
        "primary"
      ],
      "action_classes": [
        "read"
      ]
    }
  ],
  "authority_summary": "Read events on Personal (primary)",
  "missed_run_explanation": "If Nori is unavailable at 08:00, this run is skipped.",
  "warnings": [
    {
      "code": "dst_shift",
      "message": "Daylight saving time ends on Nov 1; the run stays at 08:00 local time."
    }
  ]
};

export const fx_automations_occurrence_skipped_valid: Occurrence = {
  "id": "occ_3",
  "automation_id": "auto_1",
  "automation_revision": 3,
  "generation": 2,
  "scheduled_at": "2026-11-09T16:00:00Z",
  "status": "skipped",
  "reason": "missed_window",
  "task_ids": [],
  "delivery_ids": []
};

export const fx_library_library_item_event_data_valid: LibraryItem = {
  "id": "res_evt_123",
  "root_id": "res_evt_123",
  "version": 1,
  "kind": "event",
  "title": "Dentist",
  "created_at": "2026-11-09T09:00:00Z",
  "provenance": {
    "content": "live",
    "source": {
      "connection_id": "conn_google_personal",
      "kind": "calendar_event",
      "provider_id": "evt_123",
      "provider_revision": "etag-9"
    },
    "connection_status": "connected"
  },
  "data_kind": "calendar_event",
  "data": {
    "ref": {
      "connection_id": "conn_google_personal",
      "kind": "calendar_event",
      "provider_id": "evt_123",
      "provider_revision": "etag-9"
    },
    "calendar": {
      "connection_id": "conn_google_personal",
      "kind": "calendar",
      "provider_id": "primary",
      "provider_revision": "etag-7"
    },
    "title": "Dentist",
    "time": {
      "kind": "timed",
      "start": "2026-11-11T10:00:00Z",
      "end": "2026-11-11T10:30:00Z",
      "timezone": "Europe/London"
    },
    "location": "12 High St",
    "attendees": [
      {
        "email": "personal@example.com",
        "role": "organizer",
        "response": "accepted",
        "self": true
      },
      {
        "email": "alex@example.com",
        "name": "Alex Kim",
        "role": "required",
        "response": "needs_action"
      }
    ],
    "status": "confirmed",
    "source_url": "https://calendar.google.com/event?eid=abc",
    "imported_from": "live",
    "supports": {
      "update": true,
      "cancel": true,
      "invite": true,
      "rsvp": true,
      "recurrence_scopes": [
        "instance",
        "following",
        "series"
      ]
    }
  },
  "revision": 1
};

export const fx_library_search_hit_task_jump_valid: SearchHit = {
  "kind": "task",
  "id": "task_9",
  "title": "Compare flights",
  "snippet": "Three options under $400",
  "jump": {
    "kind": "task",
    "task_id": "task_9"
  }
};

export const fx_common_api_error_revision_conflict_valid: APIError = {
  "error": "This connection changed since you loaded it.",
  "code": "revision_conflict",
  "retryable": false,
  "accepted": false,
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
  "current": {
    "id": "conn_google_work",
    "revision": 5
  }
};

export const fx_execution_work_detail_events_valid: WorkDetail = {
  "item": {
    "task_id": "task_9",
    "title": "Book the dentist",
    "status": "running",
    "bucket": "needs_you",
    "instruction_revision": 3,
    "origin": {
      "kind": "conversation",
      "conversation_id": "assistant",
      "message_id": "msg_1"
    },
    "interaction_ids": [
      "int_5"
    ],
    "progress": "Waiting for approval to send the invite",
    "operations": {
      "total": 3,
      "uncertain": 0,
      "failed": 0,
      "submitted": 0
    },
    "result_ids": [],
    "cost": {
      "amount_usd": 0.04,
      "status": "known",
      "budget_usd": 1
    },
    "started_at": "2026-11-09T09:00:00Z",
    "cancel_requested": false
  },
  "operations": [],
  "interactions": [],
  "result": "",
  "events": [
    {
      "id": "ev1",
      "kind": "progress",
      "text": "Searching calendars",
      "created_at": "2026-11-09T09:00:00Z"
    }
  ],
  "events_truncated": false
};

export const fx_library_library_item_change_valid: LibraryItemChange = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
  "expected_revision": 2,
  "value": {
    "title": "Flight comparison",
    "collection": "Travel"
  }
};

export const fx_library_library_action_export_valid: LibraryAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
  "expected_revision": 2,
  "value": {
    "action": "export",
    "format": "pdf"
  }
};

export const fx_library_library_action_revise_valid: LibraryAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
  "expected_revision": 2,
  "value": {
    "action": "revise",
    "instruction": "Add the Thursday option."
  }
};

export const fx_library_memory_fact_valid: MemoryFact = {
  "id": "mem_1",
  "revision": 1,
  "text": "Prefers aisle seats",
  "scope": "user",
  "origin": {
    "conversation_id": "assistant",
    "message_id": "msg_3"
  },
  "created_at": "2026-11-09T09:00:00Z"
};

export const fx_library_memory_action_forget_valid: MemoryAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
  "expected_revision": 1,
  "value": {
    "action": "forget"
  }
};

export const fx_library_inbox_item_valid: InboxItem = {
  "id": "inb_1",
  "delivery_id": "del_1",
  "title": "Leave for the dentist",
  "body": "Leave in 15 minutes to arrive on time.",
  "link": {
    "kind": "automation",
    "id": "auto_2"
  },
  "created_at": "2026-11-09T09:00:00Z",
  "revision": 1
};

export const fx_library_upload_accept_extensions_valid: UploadAccept = {
  "media_types": [
    "text/calendar",
    "text/markdown"
  ],
  "max_bytes": 20971520,
  "extensions": [
    ".ics",
    ".md"
  ]
};

export const fx_mcp_mcp_server_change_http_valid: MCPServerChange = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
  "expected_revision": 0,
  "value": {
    "name": "Team wiki",
    "transport": "http",
    "endpoint": "https://mcp.example.com/mcp",
    "auth": "oauth",
    "enabled": true,
    "exposure": {
      "default": "discoverable"
    }
  }
};

export const fx_mcp_mcp_server_action_set_exposure_valid: MCPServerAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
  "expected_revision": 3,
  "value": {
    "action": "set_exposure",
    "tool": "search_pages",
    "exposure": "direct"
  }
};

export const fx_mcp_mcp_server_tool_valid: MCPServerTool = {
  "name": "search_pages",
  "title": "Search pages",
  "exposure": "discoverable",
  "definition": {
    "id": "mcp/wiki/search_pages",
    "version": "d1",
    "digest": "sha256:aa"
  },
  "availability": "ready"
};

export const fx_connections_connection_not_connected_no_account_valid: Connection = {
  "id": "conn_new",
  "revision": 1,
  "integration_id": "google",
  "title": "Google",
  "status": "not_connected",
  "granted_scopes": [],
  "selected_resource_ids": [],
  "health": {},
  "defaults": []
};

export const fx_mcp_mcp_credential_valid: MCPCredentialInput = {
  "request_id": "2c9a7b1e-5d3f-4e8a-9b6c-1d2e3f4a5b6c",
  "token": "opaque-token-value"
};

export const fx_mcp_mcp_server_configured_missing_credential_valid: MCPServer = {
  "id": "mcp_1",
  "revision": 2,
  "source": {
    "kind": "user"
  },
  "name": "linear",
  "transport": "http",
  "endpoint": "https://mcp.linear.app/mcp",
  "connection_id": "conn_linear",
  "enabled": true,
  "exposure": {
    "default": "discoverable",
    "overrides": {
      "linear.issues.create": "direct"
    }
  },
  "observed": {
    "status": "needs_sign_in",
    "protocol_version": "2026-07-28",
    "capabilities": [
      "tools",
      "resources",
      "prompts",
      "logging"
    ],
    "discovery_revision": "sha256:disc1",
    "tool_count": 24,
    "resource_count": 3,
    "prompt_count": 1,
    "last_success_at": "2026-11-09T09:00:00Z",
    "credential": "missing"
  },
  "auth": "configured"
};

export const fx_execution_work_item_continuation_valid: WorkItem = {
  "task_id": "task_11",
  "title": "Continue: university founding years",
  "status": "running",
  "bucket": "working",
  "instruction_revision": 3,
  "origin": {
    "kind": "conversation",
    "conversation_id": "assistant",
    "continues_task_id": "task_10"
  },
  "interaction_ids": [],
  "progress": "Waiting for approval to send the invite",
  "operations": {
    "total": 3,
    "uncertain": 0,
    "failed": 0,
    "submitted": 0
  },
  "result_ids": [],
  "cost": {
    "amount_usd": 0.01,
    "status": "known",
    "budget_usd": 0.5
  },
  "started_at": "2026-11-09T09:00:00Z",
  "cancel_requested": false
};

export const fx_execution_operation_projection_contained_valid: OperationProjection = {
  "id": "47e5eee4-m2-t3",
  "call_id": "call_PkF1OXEp17gbYRyrW4pNc0L2",
  "name": "text_stats",
  "effect_class": "unknown",
  "outcome": "failed",
  "result_ref": "8cdaae2746815800f3d241f2fcf31d89791c631c6f5c33fbe355bd0afe4123fe",
  "revision": 1,
  "attempt": 1,
  "artifact_ids": null,
  "owner": {
    "type": "task",
    "id": "47e5eee4-1df8-54e2-ab11-54a27b2b19f5"
  },
  "cleared": "contained"
};

export const fx_common_capabilities_wire_valid: Capabilities = {
  "contract": "nori-chat-2",
  "conversationContract": "nori-conversations-1",
  "service": "nori",
  "persistence": "durable",
  "provider": "openai",
  "model": "gpt-6.1",
  "conversationModes": [
    "assistant"
  ],
  "features": {
    "backgroundTasks": true,
    "jev": true
  },
  "assistant_features": {
    "connections": true,
    "integrations": true,
    "work": false,
    "automations": false
  },
  "upload_accept": {
    "media_types": [
      "image/png",
      "application/pdf",
      "text/csv",
      "text/plain",
      "text/markdown",
      "text/calendar"
    ],
    "max_bytes": 20971520
  }
};

export const fx_connections_connection_action_app_password_valid: ConnectionAction = {
  "request_id": "7c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f",
  "expected_revision": 1,
  "value": {
    "action": "connect",
    "username": "recorded.user@icloud.com",
    "password": "abcd-efgh-ijkl-mnop",
    "return_to": "/apps"
  }
};

export const fx_connections_connection_action_app_password_repair_valid: ConnectionAction = {
  "request_id": "8d2e3f4a-5b6c-4d7e-8f9a-0b1c2d3e4f5a",
  "expected_revision": 4,
  "value": {
    "action": "repair",
    "username": "recorded.user@icloud.com",
    "password": "qrst-uvwx-yzab-cdef"
  }
};

export const fx_permissions_interaction_sign_in_app_password_valid: Interaction = {
  "id": "int_8",
  "revision": 1,
  "task_id": "task_1",
  "kind": "sign_in",
  "status": "pending",
  "prompt": "Sign in to iCloud again to continue",
  "payload": {
    "auth": "app_password",
    "integration_id": "icloud",
    "connection_id": "conn_icloud",
    "return_to": "/work/task_1"
  }
};

export const fx_permissions_interaction_sign_in_mcp_signed_in_valid: Interaction = {
  "id": "int_10",
  "revision": 2,
  "task_id": "task_1",
  "kind": "sign_in",
  "status": "resolved",
  "prompt": "Sign in to Linear to continue",
  "payload": {
    "auth": "oauth",
    "auth_attempt_id": "auth_2",
    "integration_id": "mcp",
    "connection_id": "mcp_linear",
    "authorize_url": "https://linear.app/oauth/authorize?state=opaque",
    "requested_scopes": [
      "read",
      "write"
    ],
    "expires_at": "2026-11-09T10:00:00Z"
  },
  "resolution": {
    "kind": "signed_in",
    "connection_id": "mcp_linear",
    "at": "2026-11-09T09:05:00Z"
  },
  "resolved_at": "2026-11-09T09:05:00Z"
};

export const fx_permissions_interaction_sign_in_declined_valid: Interaction = {
  "id": "int_11",
  "revision": 2,
  "task_id": "task_1",
  "kind": "sign_in",
  "status": "resolved",
  "prompt": "Sign in to Google to continue",
  "payload": {
    "auth": "oauth",
    "auth_attempt_id": "auth_3",
    "integration_id": "google",
    "connection_id": "conn_google",
    "authorize_url": "https://accounts.google.com/o/oauth2/v2/auth?state=opaque",
    "expires_at": "2026-11-09T10:00:00Z"
  },
  "resolution": {
    "kind": "decline",
    "at": "2026-11-09T09:06:00Z"
  },
  "resolved_at": "2026-11-09T09:06:00Z"
};

export const fx_permissions_interaction_response_sign_in_decline_valid: InteractionResponse = {
  "request_id": "8a7b6c5d-4e3f-4a2b-9c1d-0e9f8a7b6c5d",
  "expected_revision": 1,
  "value": {
    "kind": "decline"
  }
};

export const fx_automations_automation_editable_valid: AutomationEditable = {
  "title": "Morning briefing",
  "instruction": "Summarize today.",
  "trigger": {
    "kind": "recurring",
    "local_start": "2026-11-10T07:30",
    "timezone": "Europe/London",
    "rrule": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"
  },
  "connection_ids": [
    "conn_google_personal"
  ],
  "requested_authority": {
    "action_classes": [
      "read"
    ]
  },
  "budget_usd": 0.5,
  "max_active_runs": 1,
  "missed_run": "latest",
  "execution_window_seconds": 3600,
  "destination_ids": [
    "dest_inapp"
  ]
};

export const fx_automations_goal_editable_valid: GoalEditable = {
  "title": "Book the dentist",
  "outcome": "Booked before December",
  "completion_criteria": [
    {
      "text": "Appointment on calendar"
    }
  ],
  "follow_up": "remind"
};

export const fx_automations_missed_run_valid: MissedRun = "skip";

export const fx_automations_occurrence_status_valid: OccurrenceStatus = "completed";

export const fx_common_action_request_valid: ActionRequest = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 4,
  "value": {
    "action": "pause"
  }
};

export const fx_common_auth_attempt_valid: AuthAttempt = {
  "id": "auth_1",
  "authorize_url": "https://accounts.google.com/o/oauth2/v2/auth?state=opaque",
  "expires_at": "2026-11-09T10:00:00Z"
};

export const fx_common_cost_valid: Cost = {
  "amount_usd": 0.04,
  "status": "known"
};

export const fx_common_definition_ref_valid: DefinitionRef = {
  "id": "nori.google/calendar/events.create",
  "version": "1.0.0",
  "digest": "sha256:1111"
};

export const fx_common_effect_valid: Effect = "read";

export const fx_common_error_code_valid: ErrorCode = "not_found";

export const fx_common_error_detail_valid: ErrorDetail = {
  "code": "provider_unavailable",
  "message": "The calendar provider did not respond.",
  "retryable": true
};

export const fx_common_id_valid: ID = "conn_google_personal";

export const fx_common_instant_valid: Instant = "2026-11-09T09:00:00Z";

export const fx_common_json_valid: JSON = {
  "title": "Morning briefing"
};

export const fx_common_local_date_valid: LocalDate = "2026-11-10";

export const fx_common_outcome_valid: Outcome = "confirmed";

export const fx_common_preferences_editable_valid: PreferencesEditable = {
  "timezone": "America/Los_Angeles",
  "locale": "en-US",
  "working_hours": [
    {
      "day": 1,
      "start": "09:00",
      "end": "17:30"
    }
  ],
  "quiet_hours": {
    "start": "22:00",
    "end": "07:00"
  },
  "default_destination_ids": [
    "dest_inapp"
  ],
  "account_defaults": {
    "calendar.create": "conn_google_personal"
  },
  "budgets": {
    "task_usd": 1.0,
    "monthly_usd": 50
  }
};

export const fx_common_resource_ref_valid: ResourceRef = {
  "connection_id": "conn_google_personal",
  "kind": "calendar",
  "provider_id": "primary",
  "provider_revision": "etag-7"
};

export const fx_common_revision_valid: Revision = 4;

export const fx_common_timezone_valid: Timezone = "America/Los_Angeles";

export const fx_common_uuid_valid: UUID = "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d";

export const fx_common_work_ref_valid: WorkRef = {
  "task_id": "task_9",
  "operation_id": "op_1"
};

export const fx_common_working_hours_valid: WorkingHours = [
  {
    "day": 1,
    "start": "09:00",
    "end": "17:30"
  },
  {
    "day": 2,
    "start": "09:00",
    "end": "17:30"
  },
  {
    "day": 3,
    "start": "09:00",
    "end": "17:30"
  },
  {
    "day": 4,
    "start": "09:00",
    "end": "17:30"
  },
  {
    "day": 5,
    "start": "09:00",
    "end": "17:30"
  }
];

export const fx_connections_connection_editable_valid: ConnectionEditable = {
  "integration_id": "google",
  "title": "Personal Google",
  "selected_resource_ids": [
    "primary"
  ]
};

export const fx_connections_connection_status_valid: ConnectionStatus = "connected";

export const fx_connections_health_valid: Health = {
  "checked_at": "2026-11-09T09:00:00Z",
  "code": "ready",
  "last_success_at": "2026-11-09T09:00:00Z"
};

export const fx_connections_health_code_valid: HealthCode = "needs_sign_in";

export const fx_connections_recovery_valid: Recovery = "repair";

export const fx_domain_attendee_valid: Attendee = {
  "email": "alex@example.com",
  "name": "Alex Kim",
  "role": "required",
  "response": "needs_action"
};

export const fx_domain_mail_address_valid: MailAddress = {
  "email": "alex@example.com",
  "name": "Alex Kim"
};

export const fx_domain_recurrence_scope_valid: RecurrenceScope = "series";

export const fx_execution_run_event_valid: RunEvent = {
  "id": "ev1",
  "kind": "progress",
  "text": "Searching calendars",
  "created_at": "2026-11-09T09:00:00Z"
};

export const fx_library_availability_proposal_valid: AvailabilityProposal = {
  "slots": [
    {
      "start": "2026-11-11T09:00:00Z",
      "end": "2026-11-11T12:00:00Z",
      "status": "free",
      "sources": [
        {
          "connection_id": "conn_google_personal",
          "kind": "calendar",
          "provider_id": "primary"
        }
      ]
    }
  ],
  "duration_minutes": 30,
  "attendees": [
    {
      "email": "alex@example.com",
      "name": "Alex Kim"
    }
  ],
  "status": "proposed"
};

export const fx_library_inbox_action_valid: InboxAction = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 1,
  "value": {
    "action": "acknowledge"
  }
};

export const fx_library_library_item_editable_valid: LibraryItemEditable = {
  "title": "Flight comparison",
  "collection": "Travel"
};

export const fx_library_memory_fact_editable_valid: MemoryFactEditable = {
  "text": "Prefers aisle seats"
};

export const fx_library_provenance_valid: Provenance = {
  "content": "copied",
  "source": {
    "connection_id": "conn_google_personal",
    "kind": "calendar_event",
    "provider_id": "evt_123",
    "provider_revision": "etag-9"
  },
  "retrieved_at": "2026-11-10T07:31:00Z",
  "connection_status": "connected"
};

export const fx_permissions_account_selection_payload_valid: AccountSelectionPayload = {
  "purpose": "calendar.create",
  "eligible_connection_ids": [
    "conn_google_personal",
    "conn_google_work"
  ],
  "remember_as_default": true
};

export const fx_permissions_approval_payload_valid: ApprovalPayload = {
  "summary": "Invite alex@example.com to 'Dentist'",
  "effect": "sensitive_send",
  "account": {
    "connection_id": "conn_google_personal",
    "label": "personal@example.com"
  },
  "resources": [
    {
      "connection_id": "conn_google_personal",
      "kind": "calendar",
      "provider_id": "primary",
      "provider_revision": "etag-7"
    }
  ],
  "recipients": [
    "alex@example.com"
  ],
  "material_effects": [
    "Alex receives an email invitation"
  ]
};

export const fx_permissions_permission_rule_editable_valid: PermissionRuleEditable = {
  "effect": "deny",
  "tool_ids": [
    "*"
  ],
  "connection_ids": [
    "conn_google_work"
  ],
  "resource_ids": [
    "*"
  ],
  "action_classes": [
    "sensitive_send"
  ],
  "title": "Never send from work"
};

export const fx_permissions_question_payload_valid: QuestionPayload = {
  "input": "choice",
  "options": [
    {
      "id": "c1",
      "label": "Alex Kim (alex@example.com)"
    },
    {
      "id": "c2",
      "label": "Alex Ruiz (aruiz@work.example)"
    }
  ]
};

export const fx_permissions_sign_in_payload_valid: SignInPayload = {
  "auth": "oauth",
  "auth_attempt_id": "auth_1",
  "integration_id": "google",
  "authorize_url": "https://accounts.google.com/o/oauth2/v2/auth?state=opaque",
  "requested_scopes": [
    "calendar.events"
  ],
  "return_to": "/apps/google",
  "expires_at": "2026-11-09T10:00:00Z"
};

export const fx_mcp_mcp_server_editable_valid: MCPServerEditable = {
  "name": "Team wiki",
  "transport": "http",
  "endpoint": "https://mcp.example.com/mcp",
  "auth": "oauth",
  "enabled": true,
  "exposure": {
    "default": "discoverable"
  }
};

export const fx_tools_binding_valid: Binding = {
  "adapter": "native",
  "target": "google.calendar",
  "version": "1.0.0"
};

export const fx_tools_policy_valid: Policy = {
  "effect": "reversible_write",
  "retry": "provider_key",
  "timeout_ms": 20000,
  "location": "runtime",
  "interrupted_cost_usd": 0,
  "interrupted_cost_unknown": false,
  "cancellation": "none"
};

export const fx_library_memory_provenance_valid: MemoryProvenance = "pre_compact_flush";

export const fx_library_memory_fact_pinned_curated_valid: MemoryFact = {
  "id": "mem_2",
  "revision": 1732400000000,
  "text": "Always use UTC ISO-8601 timestamps in reports.",
  "about": "the user",
  "section": "Preferences",
  "pinned": true,
  "provenance": "pre_compact_flush",
  "source_text": "Please always use UTC ISO-8601 timestamps in reports.",
  "scope": "user",
  "origin": {
    "conversation_id": "assistant",
    "message_id": "msg_12"
  },
  "created_at": "2026-11-23T09:00:00Z",
  "updated_at": "2026-11-23T09:05:00Z"
};

export const fx_library_memory_action_pin_valid: MemoryAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2e",
  "expected_revision": 1732400000000,
  "value": {
    "action": "pin"
  }
};

export const fx_library_memory_action_consolidate_valid: MemoryAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2f",
  "expected_revision": 0,
  "value": {
    "action": "consolidate"
  }
};

export const fx_library_memory_log_entry_valid: MemoryLogEntry = {
  "id": "log_entry_1",
  "section": "Decisions",
  "text": "Chose Postgres 18 hash-partitioned FTS table for memory recall.",
  "about": "Nori architecture",
  "provenance": "pre_compact_flush",
  "source_input_id": "in_42",
  "source_text": "Let's isolate FTS in nori_memory_chunks.",
  "created_at": "2026-11-23T10:15:00Z",
  "updated_at": "2026-11-23T10:15:00Z"
};

export const fx_library_memory_log_valid: MemoryLog = {
  "date": "2026-11-23",
  "revision": 1732400100000,
  "entries": [
    {
      "id": "log_entry_1",
      "section": "Completed Work",
      "text": "Drafted Q4 budget spreadsheet and shared summary.",
      "provenance": "task_outcome",
      "run_id": "run_99",
      "created_at": "2026-11-23T11:00:00Z",
      "updated_at": "2026-11-23T11:00:00Z"
    }
  ],
  "updated_at": "2026-11-23T11:00:00Z"
};

export const fx_library_memory_log_entry_action_promote_valid: MemoryLogEntryAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c31",
  "expected_revision": 1732400100000,
  "value": {
    "action": "promote",
    "section": "Projects",
    "pinned": false
  }
};

export const fx_library_memory_log_entry_action_forget_valid: MemoryLogEntryAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c32",
  "expected_revision": 1732400100000,
  "value": {
    "action": "forget"
  }
};

export const fx_library_working_memory_item_valid: WorkingMemoryItem = {
  "id": "si_0123456789abcdef",
  "kind": "constraint",
  "quote": "Never book flights before 9am.",
  "input_id": "in_7",
  "note": "Flight time window"
};

export const fx_library_working_memory_valid: WorkingMemory = {
  "revision": 1732400200000,
  "from": "2026-11-01T00:00:00Z",
  "extracted_through": "2026-11-23T12:15:00Z",
  "flushed_through": "2026-11-23T12:00:00Z",
  "through": "2026-11-23T12:00:00Z",
  "dream_through": "2026-11-22T23:00:00Z",
  "last_dream_at": "2026-11-22T23:15:00Z",
  "items": [
    {
      "id": "si_0123456789abcdef",
      "kind": "constraint",
      "quote": "Never book flights before 9am.",
      "input_id": "in_7",
      "note": "Flight time window"
    }
  ],
  "gist": "User is planning a December trip to Tokyo with morning flight constraints.",
  "turns": 18,
  "model": "gpt-5",
  "core_bytes": 1420,
  "core_budget_bytes": 24576,
  "updated_at": "2026-11-23T12:00:00Z"
};

export const fx_library_working_memory_item_action_dismiss_valid: WorkingMemoryItemAction = {
  "request_id": "8b9a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c33",
  "expected_revision": 1732400200000,
  "value": {
    "action": "dismiss"
  }
};

export const fx_library_memory_search_hit_valid: MemorySearchHit = {
  "chunk_id": "log:2026-11-23:log_entry_1",
  "layer": "log",
  "log_date": "2026-11-23",
  "entry_id": "log_entry_1",
  "section": "Decisions",
  "about": "Nori architecture",
  "snippet": "Chose Postgres 18 hash-partitioned FTS table for memory recall.",
  "provenance": "pre_compact_flush",
  "score": 0.91,
  "created_at": "2026-11-23T10:15:00Z",
  "updated_at": "2026-11-23T10:15:00Z"
};

export const fx_library_memory_search_response_valid: MemorySearchResponse = {
  "items": [
    {
      "chunk_id": "core:mem_2",
      "layer": "core",
      "memory_id": "mem_2",
      "section": "Preferences",
      "about": "the user",
      "snippet": "Always use UTC ISO-8601 timestamps in reports.",
      "provenance": "pre_compact_flush",
      "pinned": true,
      "score": 1.15,
      "created_at": "2026-11-23T09:00:00Z",
      "updated_at": "2026-11-23T09:05:00Z"
    }
  ],
  "query": "UTC timestamps"
};

export const fx_library_search_hit_memory_jump_valid: SearchHit = {
  "kind": "memory",
  "id": "core:mem_2",
  "title": "Preferences",
  "snippet": "Always use UTC ISO-8601 timestamps in reports.",
  "jump": {
    "kind": "memory",
    "layer": "core",
    "memory_id": "mem_2"
  },
  "freshness": "2026-11-23T09:05:00Z",
  "score": 1.15
};

export const fx_permissions_rule_ask_first_valid: PermissionRule = {
  "id": "rule_ask_1",
  "revision": 1,
  "effect": "ask",
  "tool_ids": [
    "nori.google/mail/messages.send"
  ],
  "connection_ids": [
    "conn_google_personal"
  ],
  "resource_ids": [
    "*"
  ],
  "action_classes": [
    "sensitive_send"
  ],
  "recipients": [
    "*"
  ],
  "title": "Ask before sending email",
  "created_at": "2026-11-09T09:00:00Z"
};

export const fx_permissions_response_approve_always_valid: InteractionResponse = {
  "request_id": "6f1c2a0e-1b7e-4f0a-9c2d-3e4f5a6b7c8d",
  "expected_revision": 1,
  "value": {
    "kind": "approve",
    "action_digest": "sha256:act1",
    "remember_rule": true
  }
};

