// The only hand-written contract shape left: the full /v1/capabilities payload, which
// combines the existing chat fields with r2's AssistantCapabilities. Everything else
// comes from ./assistant.ts, generated from contracts/assistant/schemas.
import type { AssistantCapabilities } from "./assistant";

export type Capabilities = {
  contract: string;
  conversationContract?: string;
  features?: Record<string, boolean>;
} & Partial<Omit<AssistantCapabilities, "assistant_features">> & {
  /** The core also advertises `skills` (proposed for r2.4). */
  assistant_features?: AssistantCapabilities["assistant_features"] & { skills?: boolean };
};
export type AssistantFeature = keyof NonNullable<Capabilities["assistant_features"]>;


/** GET /v1/skills items as backend serves them (proposed as SkillView for r2.4). */
export type SkillView = import("./assistant").Skill & { id: string; revision: number; installation_id?: string; enabled: boolean };
