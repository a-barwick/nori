// The only hand-written contract shape left: the full /v1/capabilities payload, which
// combines the existing chat fields with AssistantCapabilities. Everything else comes
// from ./assistant.ts, generated from contracts/assistant/schemas.
import type { AssistantCapabilities } from "./assistant";

export type Capabilities = {
  contract: string;
  conversationContract?: string;
  features?: Record<string, boolean>;
} & Partial<AssistantCapabilities>;
export type AssistantFeature = keyof AssistantCapabilities["assistant_features"];

/** r2.5 as agreed (backend 155bba0), until integration publishes the schema. */
export type MCPClient = { id: string; revision: number; name: string; scopes: ("read" | "mutate")[]; created_at: string; revoked_at?: string; last_used_at?: string };
