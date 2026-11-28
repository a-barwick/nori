import type { Preferences } from "@/lib/contracts/assistant";

export function shouldShowOnboarding(params: {
  forceReplay: boolean;
  preferences?: Preferences;
  preferencesLoaded: boolean;
  session?: { messages?: ReadonlyArray<{ role: string }>; next_cursor?: string };
  sessionLoaded: boolean;
  localCompleted: boolean;
}): boolean {
  if (params.forceReplay) return true;
  if (params.localCompleted) return false;
  if (!params.preferencesLoaded || !params.sessionLoaded) return false;
  if (params.preferences?.onboarding_completed_at) return false;
  if (params.preferences?.data_reset_at) return true;
  return !params.session?.next_cursor && !params.session?.messages?.some(message => message.role === "user");
}
