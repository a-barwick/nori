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

