"use client";

import * as Sentry from "@sentry/nextjs";
import Link from "next/link";
import { useEffect } from "react";

/**
 * A screen that failed to render shows this calm card instead of the bare framework error page. Keeping the error in
 * a route boundary also matters in development: Next's dev client reloads the whole page on every server change once
 * an error has reached its root, which turns one failure into a reload loop. Nothing here retries by itself.
 */
export function ScreenError({ error, retry }: { error: Error & { digest?: string }; retry: () => void }) {
  useEffect(() => { Sentry.captureException(error); }, [error]);
  return (
    <div role="alert" className="flex h-full min-h-0 flex-1 items-center justify-center bg-canvas p-6 text-ink">
      <div className="max-w-sm rounded-card border border-hairline bg-surface p-5 text-center">
        <h1 className="text-row font-semibold">This screen didn&apos;t load</h1>
        <p className="mt-1 text-body text-ink-2">Your work is safe. Try again, or open another screen.</p>
        <div className="mt-4 flex justify-center gap-2">
          <button type="button" onClick={() => retry()} className="rounded-full bg-accent px-4 py-1.5 text-row-title font-medium text-on-accent">Try again</button>
          <Link href="/" className="rounded-full border border-hairline px-4 py-1.5 text-row-title font-medium text-ink">Open chat</Link>
        </div>
      </div>
    </div>
  );
}
