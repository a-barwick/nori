import type { components } from "./openapi";

type Schemas = components["schemas"];

// Primitives & shared envelopes
export type ID = Schemas["ID"];
export type UUID = Schemas["UUID"];
export type Revision = Schemas["Revision"];
export type Instant = Schemas["Instant"];
export type LocalDate = Schemas["LocalDate"];
export type Timezone = Schemas["Timezone"];
export type Effect = Schemas["Effect"];
export type Outcome = Schemas["Outcome"];
export type DefinitionRef = Schemas["DefinitionRef"];
export type ResourceRef = Schemas["ResourceRef"];
export type WorkRef = Schemas["WorkRef"];
export type ErrorDetail = Schemas["ErrorDetail"];
export type Cost = Schemas["Cost"];
export type AuthAttempt = Schemas["AuthAttempt"];
export type MutationReceipt = Schemas["MutationReceipt"];
export type APIError = Schemas["APIError"];
export type BillingErrorCode =
  | "credit_exhausted"
  | "subscription_inactive"
  | "account_suspended"
  | "entitlement_missing"
  | "byok_not_allowed"
  | "global_budget_exceeded"
  | "trial_limit_exceeded";
export type ErrorCode = Schemas["ErrorCode"] | BillingErrorCode;
export type AssistantSummary = Schemas["AssistantSummary"];
export type UploadAccept = Schemas["UploadAccept"];
export type AssistantFeatures = Schemas["AssistantFeatures"];
export type AssistantFeature = keyof AssistantFeatures;
export type BillingTier = "trial" | "byok" | "standard" | "power";
export type EntitlementStatus = "active" | "billing_retry" | "past_due" | "canceled" | "expired";
export type BillingProvider = "manual" | "stripe" | "apple_iap";
export type EntitlementSummary = {
  tier: BillingTier;
  pending_tier?: BillingTier | null;
  status: EntitlementStatus;
  billing_provider: BillingProvider;
  period_start?: Instant;
  period_end?: Instant | null;
  grace_until?: Instant | null;
  suspended: boolean;
  suspended_reason?: string;
  allow_byok: boolean;
  credit_grant_usd: number;
  credit_used_usd: number;
  credit_pending_usd: number;
  credit_remaining_usd: number;
  byok_max_usd_per_day?: number;
  storage_used_bytes?: number;
  storage_limit_bytes?: number;
};
export type ServiceKeySource = "byok" | "user" | "platform" | "none" | string;
export type ServiceKeyStatus = {
  service: string;
  label?: string;
  configured: boolean;
  masked_key?: string;
  last4?: string;
  updated_at?: Instant;
  validated_at?: Instant;
  source?: ServiceKeySource;
};
export type ServiceKeysResponse = {
  allow_byok?: boolean;
  services: ServiceKeyStatus[];
};
export type CapabilitiesEntitlement = Schemas["CapabilitiesEntitlement"] & {
  suspended_reason?: string;
};
export type CapabilitiesServiceKeys = Schemas["CapabilitiesServiceKeys"];
export type Capabilities = Omit<Schemas["Capabilities"], "entitlement" | "service_keys"> & {
  entitlement?: CapabilitiesEntitlement;
  service_keys?: CapabilitiesServiceKeys;
};
export type WorkingHoursSlot = Schemas["WorkingHoursSlot"];
export type WorkingHours = Schemas["WorkingHours"];
export type QuietHours = Schemas["QuietHours"];
export type PushPolicy = Schemas["PushPolicy"];
export type ProactivityPreferences = Schemas["ProactivityPreferences"];
export type RegisterPushDeviceRequest = Schemas["RegisterPushDeviceRequest"];
export type PushDevice = Schemas["PushDevice"];
export type BriefItem = Schemas["BriefItem"];
export type BudgetPreferences = Schemas["BudgetPreferences"];
export type InputPreferences = Schemas["InputPreferences"];
export type Preferences = Schemas["Preferences"];
export type PreferencesEditable = Schemas["PreferencesEditable"];
export type PreferencesChange = Schemas["PreferencesChange"];
export type AccountDeleteRequest = Schemas["AccountDeleteRequest"];
export type DataResetRequest = Schemas["DataResetRequest"];

// Models, BYOK keys, Home AI & API costs
export type ModelProfile = Schemas["ModelProfile"];
export type ModelProfileCatalog = Schemas["ModelProfileCatalog"];
export type PinnedProfile = Schemas["PinnedProfile"];
export type ModelSelection = Schemas["ModelSelection"];
export type ModelKeyProvider = Schemas["ModelKeyProvider"];
export type ModelKeysResponse = Schemas["ModelKeysResponse"];
export type SetModelKeyRequest = Schemas["SetModelKeyRequest"];
export type ModelSettings = Schemas["ModelSettings"];
export type SaveModelSettingsRequest = Schemas["SaveModelSettingsRequest"];
export type HomeConnectorModel = Schemas["HomeConnectorModel"];
export type HomeConnector = Schemas["HomeConnector"];
export type HomeConnectorDownloads = Schemas["HomeConnectorDownloads"];
export type HomeConnectors = Schemas["HomeConnectors"];
export type HomePairing = Schemas["HomePairing"];
export type HomePairingRequest = Schemas["HomePairingRequest"];
export type HomeConnectorTestResponse = Schemas["HomeConnectorTestResponse"];
export type CostAmount = Schemas["CostAmount"];
export type ModelCost = Schemas["ModelCost"];
export type CostBucket = Schemas["CostBucket"];
export type BillingCosts = {
  hosted: CostAmount;
  byok: CostAmount;
  local: CostAmount;
};
export type CategoryCosts = {
  llm: CostAmount;
  search: CostAmount;
  computer: CostAmount;
};
export type CostReport = Schemas["CostReport"] & {
  by_billing?: BillingCosts;
  by_category?: CategoryCosts;
  entitlement?: EntitlementSummary;
};

// Work & operations
export type OperationProjection = Schemas["OperationProjection"];
export type WorkStep = Schemas["WorkStep"];
export type WorkItem = Schemas["WorkItem"];
export type WorkDetail = Schemas["WorkDetail"];
export type RunEvent = Schemas["RunEvent"];
export type WorkActionRequest = Schemas["WorkActionRequest"];

// Interactions & permissions
export type PermissionRule = Schemas["PermissionRule"];
export type PermissionRuleEditable = Schemas["PermissionRuleEditable"];
export type PermissionRuleChange = Schemas["PermissionRuleChange"];
export type PermissionRuleAction = Schemas["PermissionRuleAction"];
export type QuestionPayload = Schemas["QuestionPayload"];
export type ApprovalPayload = Schemas["ApprovalPayload"];
export type AccountSelectionPayload = Schemas["AccountSelectionPayload"];
export type SignInPayload = Schemas["SignInPayload"];
export type ComputerTakeoverPayload = Schemas["ComputerTakeoverPayload"];
export type Interaction = Schemas["Interaction"];
export type InteractionResponse = Schemas["InteractionResponse"];

// Personal Agent Computer
export type ComputerCheckpoint = Schemas["ComputerCheckpoint"];
export type ComputerSessionSummary = Schemas["ComputerSessionSummary"];
export type ComputerStatus = Schemas["ComputerStatus"];
export type CreateComputerSessionRequest = Schemas["CreateComputerSessionRequest"];
export type CreateComputerSessionResponse = Schemas["CreateComputerSessionResponse"];
export type UpdateComputerSessionRequest = Schemas["UpdateComputerSessionRequest"];

// Automations & inbox
export type Trigger = Schemas["Trigger"];
export type MissedRun = Schemas["MissedRun"];
export type OccurrenceStatus = Schemas["OccurrenceStatus"];
export type Automation = Schemas["Automation"];
export type AutomationEditable = Schemas["AutomationEditable"];
export type AutomationChange = Schemas["AutomationChange"];
export type AutomationAction = Schemas["AutomationAction"];
export type Occurrence = Schemas["Occurrence"];
export type Delivery = Schemas["Delivery"];
export type AutomationPreview = Schemas["AutomationPreview"];
export type InboxItem = Schemas["InboxItem"];
export type InboxAction = Schemas["InboxAction"];

// Memory
export type MemoryProvenance = Schemas["MemoryProvenance"];
export type MemoryFact = Schemas["MemoryFact"];
export type MemoryFactEditable = Schemas["MemoryFactEditable"];
export type MemoryFactChange = Schemas["MemoryFactChange"];
export type MemoryAction = Schemas["MemoryAction"];
export type MemoryLogEntry = Schemas["MemoryLogEntry"];
export type MemoryLog = Schemas["MemoryLog"];
export type MemoryLogEntryAction = Schemas["MemoryLogEntryAction"];
export type WorkingMemoryItem = Schemas["WorkingMemoryItem"];
export type WorkingMemory = Schemas["WorkingMemory"];
export type WorkingMemoryItemAction = Schemas["WorkingMemoryItemAction"];
export type MemorySearchHit = Schemas["MemorySearchHit"];
export type MemorySearchResponse = Schemas["MemorySearchResponse"];

// Library, cards, domain objects & search
export type CardPart = Schemas["CardPart"];
export type Provenance = Schemas["Provenance"];
export type CalendarTime = Schemas["CalendarTime"];
export type RecurrenceScope = Schemas["RecurrenceScope"];
export type Attendee = Schemas["Attendee"];
export type CalendarEvent = Schemas["CalendarEvent"];
export type AvailabilitySlot = Schemas["AvailabilitySlot"];
export type MailAddress = Schemas["MailAddress"];
export type MailMessage = Schemas["MailMessage"];
export type MailDraft = Schemas["MailDraft"];
export type AvailabilityProposal = Schemas["AvailabilityProposal"];
export type LibraryItem = Schemas["LibraryItem"];
export type LibraryItemEditable = Schemas["LibraryItemEditable"];
export type LibraryItemChange = Schemas["LibraryItemChange"];
export type LibraryAction = Schemas["LibraryAction"];
export type SearchHit = Schemas["SearchHit"];
export type SearchResponse = Schemas["SearchResponse"];

// Integrations, connections, MCP & tools
export type ConnectionStatus = Schemas["ConnectionStatus"];
export type HealthCode = Schemas["HealthCode"];
export type Recovery = Schemas["Recovery"];
export type Health = Schemas["Health"];
export type Connection = Schemas["Connection"];
export type ConnectionEditable = Schemas["ConnectionEditable"];
export type ConnectionChange = Schemas["ConnectionChange"];
export type ConnectionAction = Schemas["ConnectionAction"];
export type Resource = Schemas["Resource"];
export type Integration = Schemas["Integration"];
export type MCPServer = Schemas["MCPServer"];
export type MCPServerEditable = Schemas["MCPServerEditable"];
export type MCPServerChange = Schemas["MCPServerChange"];
export type MCPServerAction = Schemas["MCPServerAction"];
export type MCPServerTool = Schemas["MCPServerTool"];
export type MCPCredentialInput = Schemas["MCPCredentialInput"];
export type AvailableTool = Schemas["AvailableTool"];
export type AvailableToolList = Schemas["AvailableToolList"];

// Diagnostics & feedback
export type DiagnosticItem = Schemas["DiagnosticItem"];
export type DiagnosticsActionRequest = Schemas["DiagnosticsActionRequest"];
export type FeedbackRequest = Schemas["FeedbackRequest"];
