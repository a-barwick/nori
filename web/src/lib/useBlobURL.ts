"use client";

import { useEffect, useState } from "react";

type Loader = (signal: AbortSignal) => Promise<Blob>;

export function useBlobURL(load: Loader | undefined) {
  const [result, setResult] = useState<{ load: Loader; url?: string; error?: unknown }>();
  useEffect(() => {
    if (!load) return;
    const controller = new AbortController();
    let url: string | undefined;
    void load(controller.signal).then((blob) => {
      if (controller.signal.aborted) return;
      url = URL.createObjectURL(blob);
      setResult({ load, url });
    }).catch((error) => {
      if (!controller.signal.aborted) setResult({ load, error });
    });
    return () => {
      controller.abort();
      if (url) URL.revokeObjectURL(url);
    };
  }, [load]);
  return result?.load === load ? result : undefined;
}
