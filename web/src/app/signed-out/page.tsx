"use client";

import Link from "next/link";
import { useEffect } from "react";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { SIGNED_OUT_EVENT_KEY } from "@/lib/runtime/session";

export default function SignedOut() {
  useEffect(() => {
    try { localStorage.setItem(SIGNED_OUT_EVENT_KEY, crypto.randomUUID()); } catch { }
  }, []);
  return <main className="flex h-full flex-col items-center justify-center gap-6 bg-canvas p-6 text-center">
    <NoriMark size={72} />
    <h1 className="text-display font-semibold text-ink">Signed out</h1>
    <form action="/api/auth/sign-in" method="post"><Button type="submit" variant="primary">Sign in</Button></form>
    <nav aria-label="Legal" className="mt-2 flex items-center gap-4 text-meta text-ink-2">
      <Link href="/privacy" className="hover:text-ink hover:underline">Privacy Policy</Link>
      <span aria-hidden className="text-ink-3">·</span>
      <Link href="/terms" className="hover:text-ink hover:underline">Terms of Service</Link>
    </nav>
  </main>;
}
