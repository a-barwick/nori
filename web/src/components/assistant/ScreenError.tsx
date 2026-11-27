"use client";

import * as Sentry from "@sentry/nextjs";
import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Button } from "@/components/ui/primitives";

export function ScreenError({
  error,
  reset,
  retry,
}: {
  error: Error & { digest?: string };
  reset?: () => void;
  retry?: () => void;
}) {
  const pathname = usePathname();
  const router = useRouter();
  useEffect(() => { Sentry.captureException(error); }, [error]);

  const recover = () => (reset ?? retry)?.();

  const openChat = () => {
    if (pathname === "/") {
      window.location.reload();
    } else {
      router.push("/");
      recover();
    }
  };

  return (
    <div role="alert" className="flex h-full min-h-0 flex-1 items-center justify-center overflow-y-auto bg-canvas p-6 text-ink">
      <div className="my-auto max-w-sm rounded-card border border-card-border bg-surface p-5 text-center">
        <h1 className="text-row font-semibold">This screen didn&apos;t load</h1>
        <p className="mt-1 text-body text-ink-2">Your work is safe. Try again, or open another screen.</p>
        <div className="mt-4 flex justify-center gap-2">
          <Button variant="primary" onClick={recover}>Try again</Button>
          <Button variant="neutral" onClick={openChat}>Open chat</Button>
        </div>
      </div>
    </div>
  );
}
