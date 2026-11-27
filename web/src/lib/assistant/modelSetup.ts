import type { ModelKeysResponse, ModelSettings } from "@/lib/assistant/api";

export function needsByokSetup(data?: ModelKeysResponse, hasHomeChat = false): boolean {
  return !!data?.byok_required && !hasHomeChat && !data.providers.some((provider) => provider.configured);
}

export function byokPromptLabel(data?: ModelKeysResponse): string {
  return data?.providers.length === 1 ? data.providers[0].label : "model provider";
}

export function hasSelectedHomeChat(data?: ModelSettings): boolean {
  return (
    !!data?.selection.chat_profile_id &&
    (data.selection.home_only === true ||
      data.selection.chat_profile_id.startsWith("home-") ||
      data.model_profiles?.profiles.some(
        (profile) => profile.id === data.selection.chat_profile_id && profile.backend === "home",
      ))
  );
}
