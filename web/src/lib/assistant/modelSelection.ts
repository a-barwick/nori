import type { Capabilities } from "@/lib/contracts/assistant";
import type { ModelSelection } from "@/lib/types";

type ModelProfileCatalog = NonNullable<Capabilities["model_profiles"]>;

export const EMPTY_SELECTION: ModelSelection = {};

export function parseModelSelection(raw: unknown): ModelSelection {
  if (!raw || typeof raw !== "object") return EMPTY_SELECTION;
  const value = raw as Partial<Record<keyof ModelSelection, unknown>>;
  return Object.fromEntries(
    (["chat_profile_id", "task_profile_id", "chat_reasoning", "task_reasoning"] as const)
      .filter((key) => typeof value[key] === "string" && value[key])
      .map((key) => [key, value[key]]),
  );
}

export function normalizeModelSelection(sel: ModelSelection, catalog?: ModelProfileCatalog): ModelSelection {
  if (!catalog?.profiles?.length) return EMPTY_SELECTION;
  const out: ModelSelection = {};
  const chatProfile = catalog.profiles.find(
    (p) => p.lanes.includes("chat") && p.id === (sel.chat_profile_id || catalog.default_chat_profile_id),
  );
  if (sel.chat_profile_id && catalog.profiles.some((p) => p.lanes.includes("chat") && p.id === sel.chat_profile_id)) {
    out.chat_profile_id = sel.chat_profile_id;
  }
  if (sel.chat_reasoning && chatProfile?.supported_reasoning?.includes(sel.chat_reasoning)) {
    out.chat_reasoning = sel.chat_reasoning;
  }
  const taskProfile = catalog.profiles.find(
    (p) => p.lanes.includes("task") && p.id === (sel.task_profile_id || catalog.default_task_profile_id),
  );
  if (sel.task_profile_id && catalog.profiles.some((p) => p.lanes.includes("task") && p.id === sel.task_profile_id)) {
    out.task_profile_id = sel.task_profile_id;
  }
  if (sel.task_reasoning && taskProfile?.supported_reasoning?.includes(sel.task_reasoning)) {
    out.task_reasoning = sel.task_reasoning;
  }
  return out;
}
