"use client";

import { useCallback, useEffect, useRef, useState } from "react";

export function useCopyToClipboard(resetMs = 1400) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);

  const copy = useCallback(
    (text: string) => {
      if (!text) return;
      void navigator.clipboard?.writeText(text);
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        timer.current = null;
        setCopied(false);
      }, resetMs);
    },
    [resetMs],
  );

  return { copied, copy };
}

export function downloadBlob(content: BlobPart, filename: string, mimeType = "text/markdown") {
  const url = URL.createObjectURL(new Blob([content], { type: mimeType }));
  const link = window.document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export function stripSearchParams(keys: readonly string[]) {
  if (typeof window === "undefined") return false;
  const url = new URL(window.location.href);
  let changed = false;
  for (const key of keys) {
    if (url.searchParams.has(key)) {
      url.searchParams.delete(key);
      changed = true;
    }
  }
  if (changed) {
    window.history.replaceState(window.history.state, "", url);
  }
  return changed;
}

export function useStripSearchParams(keys: readonly string[], enabled = true) {
  const keySignature = keys.join("\0");
  useEffect(() => {
    if (!enabled) return;
    stripSearchParams(keySignature ? keySignature.split("\0") : []);
  }, [enabled, keySignature]);
}
