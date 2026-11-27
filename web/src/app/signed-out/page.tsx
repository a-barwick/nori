"use client";

import { useEffect } from "react";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { PRIVACY_URL, TERMS_URL } from "@/lib/legal";
import { SIGNED_OUT_EVENT_KEY } from "@/lib/runtime/session";

export default function SignedOut() {
  useEffect(() => {
    try { localStorage.setItem(SIGNED_OUT_EVENT_KEY, crypto.randomUUID()); } catch { }
  }, []);
  return (
    <main className="flex h-full flex-col overflow-y-auto bg-canvas p-6 text-center">
      <div className="mx-auto my-auto flex w-full max-w-sm flex-col items-center gap-6 py-4">
        <NoriMark size={72} />
        <h1 className="text-display font-semibold text-ink">Nori</h1>
        <form action="/api/auth/sign-in" method="post" className="w-full">
          <Button type="submit" variant="primary" className="h-12 w-full text-[15px]">
            Sign in with Google
          </Button>
        </form>
        <nav aria-label="Legal" className="mt-2 flex items-center gap-4 text-meta text-ink-2">
          <a href={PRIVACY_URL} className="hover:text-ink hover:underline">Privacy Policy</a>
          <span aria-hidden className="text-ink-3">·</span>
          <a href={TERMS_URL} className="hover:text-ink hover:underline">Terms of Service</a>
        </nav>
      </div>
    </main>
  );
}
