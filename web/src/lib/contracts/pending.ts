// The only hand-written contract shape left: the full /v1/capabilities payload, which
// combines the existing chat fields with r2's AssistantCapabilities. Everything else
// comes from ./assistant.ts, generated from contracts/assistant/schemas.
import type { AssistantCapabilities } from "./assistant";

export type Capabilities = {
  contract: string;
  conversationContract?: string;
  features?: Record<string, boolean>;
} & Partial<AssistantCapabilities>;
export type AssistantFeature = keyof AssistantCapabilities["assistant_features"];

/** r2.3 as agreed with backend (a6c0e65), until integration publishes the schema. */
export type MCPServerWithAuth = import("./assistant").MCPServer & {
  auth?: "none" | "oauth" | "configured";
  observed: import("./assistant").MCPServer["observed"] & { credential?: "missing" | "present" };
};
