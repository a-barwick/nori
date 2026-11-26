export type DesktopHomeStatus = {
  background: "off" | "approval_required" | "enabled" | "unavailable";
  paired: boolean;
  revocationPending?: boolean;
  state?: string;
  ollama_available?: boolean;
};

export type DesktopBridge = {
  homeStatus(): Promise<DesktopHomeStatus>;
  ollamaModels(): Promise<{ ollama_available: boolean; models?: { name: string; capabilities?: string[] }[] }>;
  registerBackground(): Promise<DesktopHomeStatus>;
  openBackgroundSettings(): Promise<void>;
  beginPairing(): Promise<{ user_code: string }>;
  finishPairing(): Promise<void>;
  cancelPairing(): Promise<void>;
  disconnectHome(): Promise<{ revocationPending: boolean }>;
  signOut(): Promise<void>;
  openInBrowser(): Promise<void>;
};

declare global {
  interface Window { noriDesktop?: DesktopBridge }
}
