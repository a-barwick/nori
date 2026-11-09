// Shapes queued for contract r2 (frontend F12, F14 remainder, F15–F19 in
// docs/assistant-r0-review-frontend.md) that are not yet in contracts/assistant/schemas.
// Hand-written until r2 lands; each is then replaced by its generated type in ./assistant.ts.
// Screens built on these are fixture-backed only.
import type {
  AssistantCapabilities, ComputerSession as BaseComputerSession, DefinitionRef, Effect, ID, Instant,
  LibraryItem as BaseLibraryItem, OperationProjection, Revision, UploadAccept,
} from "./assistant";

/** r1 capabilities plus F15 extensions and an r2 `skills` feature flag. */
export type Capabilities = {
  contract: string;
  conversationContract?: string;
  features?: Record<string, boolean>;
} & Partial<Omit<AssistantCapabilities, "upload_accept" | "assistant_features">> & {
  assistant_features?: AssistantCapabilities["assistant_features"] & { skills?: boolean };
  upload_accept?: UploadAccept & { extensions?: string[] };
};
export type AssistantFeature = keyof NonNullable<Capabilities["assistant_features"]>;

/** F12 */
export type LibraryItem = BaseLibraryItem & { collection?: string; preview_text?: string; delete_effect?: string };

/** F14 */
export type MemoryFact = { id: ID; revision: Revision; text: string; scope: "user" | "chat"; origin: { conversation_id?: string; message_id?: string; task_id?: string }; created_at: Instant; updated_at: Instant };
export type InboxItem = { id: ID; delivery_id?: string; title: string; body: string; link: { kind: string; id: string }; created_at: Instant; acknowledged_at?: Instant };
export type Operation = OperationProjection & { task_title?: string; recovery?: "none" | "reconciling" | "needs_review" | "resolved" };

/** WorkDetail additions the Work screen needs for parity with today's task panel (proposed for r2). */
export type WorkActivity = { result?: string; events?: { kind: string; text: string; created_at: Instant }[] };

/** F16 */
export type CatalogEntry = {
  package: DefinitionRef;
  title: string;
  description: string;
  publisher: { id: string; name: string };
  categories: string[];
  latest_version: string;
  provenance: { source: "registry" | "repository" | "upload"; verified: boolean };
  requested_access: { connection_types: string[]; action_classes: Effect[]; network?: string[]; filesystem?: string[] };
  contributions_summary: { tools: number; skills: number; prompts: number; mcp_servers: number };
  installation_id?: ID;
};
export type InstallationReview = {
  from_version?: string; to_version?: string;
  tools_added: string[]; tools_removed: string[]; schemas_changed: string[];
  access_added: string[]; access_removed: string[];
  dependents: { automation_ids: ID[]; goal_ids: ID[] };
};

/** F17 */
export type MCPTool = { name: string; title: string; exposure: "direct" | "discoverable" | "programmatic" | "disabled"; definition: DefinitionRef; availability: string };

/** F19 */
export type ComputerSession = BaseComputerSession & { waiting_task_ids?: string[]; task_title?: string };
