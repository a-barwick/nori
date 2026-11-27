import type { Preferences } from "@/lib/contracts/assistant";

export function shouldShowOnboarding(params: {
  forceReplay: boolean;
  preferences?: Preferences;
  preferencesLoaded: boolean;
  messageCount: number;
  sessionLoaded: boolean;
  localCompleted: boolean;
}): boolean {
  if (params.forceReplay) return true;
  if (params.localCompleted) return false;
  if (!params.preferencesLoaded || !params.sessionLoaded) return false;
  if (params.preferences?.onboarding_completed_at) return false;
  if (params.preferences?.data_reset_at) return true;
  if (params.messageCount > 0) return false;
  return true;
}
