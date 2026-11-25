"use client";

import { useCallback, useEffect, useSyncExternalStore } from "react";

export type NoriAppearance = "system" | "light" | "dark";

export const APPEARANCE_STORAGE_KEY = "nori.appearance";
const APPEARANCE_EVENT = "nori:appearance";

export const APPEARANCE_OPTIONS: readonly {
  id: NoriAppearance;
  label: string;
}[] = [
  { id: "system", label: "System" },
  { id: "light", label: "Light" },
  { id: "dark", label: "Dark" },
] as const;

export function parseAppearance(raw: string | null | undefined): NoriAppearance {
  if (raw === "light" || raw === "dark" || raw === "system") return raw;
  return "system";
}

export function appearanceLabel(appearance: NoriAppearance): string {
  return appearance === "light" ? "Light" : appearance === "dark" ? "Dark" : "System";
}

export function readStoredAppearance(): NoriAppearance {
  if (typeof window === "undefined") return "system";
  try {
    return parseAppearance(window.localStorage.getItem(APPEARANCE_STORAGE_KEY));
  } catch {
    return "system";
  }
}

export function applyAppearanceToDocument(appearance: NoriAppearance): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  if (appearance === "system") {
    delete root.dataset.appearance;
  } else {
    root.dataset.appearance = appearance;
  }
}

export function setStoredAppearance(next: NoriAppearance): void {
  if (typeof window !== "undefined") {
    try {
      window.localStorage.setItem(APPEARANCE_STORAGE_KEY, next);
    } catch {
      // Storage unavailable (private browsing / restricted iframe)
    }
  }
  applyAppearanceToDocument(next);
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent(APPEARANCE_EVENT, { detail: next }));
  }
}

function subscribe(callback: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  const onStorage = (event: StorageEvent) => {
    if (event.key === null || event.key === APPEARANCE_STORAGE_KEY) {
      applyAppearanceToDocument(readStoredAppearance());
      callback();
    }
  };
  const onCustom = () => callback();
  window.addEventListener("storage", onStorage);
  window.addEventListener(APPEARANCE_EVENT, onCustom);
  return () => {
    window.removeEventListener("storage", onStorage);
    window.removeEventListener(APPEARANCE_EVENT, onCustom);
  };
}

export function useAppearance(): [NoriAppearance, (next: NoriAppearance) => void] {
  const appearance = useSyncExternalStore(subscribe, readStoredAppearance, () => "system" as NoriAppearance);

  useEffect(() => {
    applyAppearanceToDocument(appearance);
  }, [appearance]);

  const update = useCallback((next: NoriAppearance) => {
    setStoredAppearance(next);
  }, []);

  return [appearance, update];
}

/**
 * Inline boot script placed in <head> so the saved Light/Dark preference applies
 * before first paint with zero flash of the wrong color scheme.
 */
export const APPEARANCE_BOOT_SCRIPT = `(function(){try{var v=localStorage.getItem(${JSON.stringify(APPEARANCE_STORAGE_KEY)});if(v==="light"||v==="dark"){document.documentElement.dataset.appearance=v;}}catch(e){}})();`;
