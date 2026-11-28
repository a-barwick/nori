import type { ModelKeysResponse, ModelSettings, ServiceKeysResponse } from "@/lib/assistant/api";
import type { Capabilities, Connection } from "@/lib/contracts/assistant";

export const GOOGLE_DRIVE_NUDGE_DISMISSED_PREFIX = "nori.nudge.google_drive.dismissed:";

export function googleDriveNudgeDismissedKey(scope: string): string {
  return `${GOOGLE_DRIVE_NUDGE_DISMISSED_PREFIX}${scope}`;
}

export function isByokAllowed(serviceKeys?: ServiceKeysResponse, capabilities?: Capabilities): boolean {
  if (serviceKeys?.allow_byok !== undefined) return serviceKeys.allow_byok;
  if (!capabilities) return false;
  return capabilities.entitlement?.allow_byok ?? true;
}

export function needsByokSetup(
  data?: ModelKeysResponse,
  hasHomeChat = false,
  capabilities?: Capabilities,
): boolean {
  if (capabilities?.entitlement && !capabilities.entitlement.allow_byok) return false;
  return !!data?.byok_required && !hasHomeChat && !data.providers.some((provider) => provider.configured);
}

export function needsExaSetup(serviceKeys?: ServiceKeysResponse, capabilities?: Capabilities): boolean {
  if (!isByokAllowed(serviceKeys, capabilities)) return false;
  if (serviceKeys?.services && serviceKeys.services.length > 0) {
    const exa = serviceKeys.services.find((item) => item.service === "exa");
    if (!exa) return false;
    return !exa.configured && exa.source !== "platform";
  }
  if (capabilities?.service_keys) {
    return !capabilities.service_keys.exa_configured && !capabilities.service_keys.platform_exa_available;
  }
  return false;
}

export function isGoogleDriveConnected(connections?: readonly Connection[]): boolean {
  if (!connections) return false;
  return connections.some(
    (item) =>
      item.integration_id === "google" &&
      item.status !== "disconnected" &&
      item.status !== "disconnecting" &&
      !!item.account?.provider_id &&
      !!item.capabilities?.includes("files.read") &&
      !!item.capabilities?.includes("files.write"),
  );
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
