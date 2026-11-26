"use client";

import { useRouter } from "next/navigation";
import { FormEvent, useState } from "react";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { PRIVACY_URL, TERMS_URL } from "@/lib/legal";

export default function InvitePage() {
  const router = useRouter();
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = code.trim();
    if (!trimmed) {
      setError("Enter an invite code.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const response = await fetch("/api/invite/redeem", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ invite_code: trimmed }),
        cache: "no-store",
      });
      if (!response.ok) {
        const body = await response.json().catch(() => null) as { error?: unknown } | null;
        setError(typeof body?.error === "string" && body.error ? body.error : "Invalid invite code.");
        return;
      }
      router.replace("/");
      router.refresh();
    } catch {
      setError("Nori could not verify your invite code. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="flex h-full flex-col items-center justify-center gap-6 bg-canvas p-6 text-center">
      <NoriMark size={72} />
      <h1 className="text-display font-semibold text-ink">Enter your invite code</h1>
      <form onSubmit={onSubmit} className="flex w-full max-w-sm flex-col gap-3 text-left">
        <label htmlFor="invite-code" className="sr-only">Invite code</label>
        <input
          id="invite-code"
          name="invite_code"
          type="text"
          autoComplete="off"
          autoCapitalize="characters"
          spellCheck={false}
          placeholder="NORI-XXXX-XXXX-XXXX"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          disabled={submitting}
          className="h-11 w-full rounded-xl border border-line bg-surface px-3.5 text-body text-ink outline-none transition focus:border-accent"
        />
        {error ? (
          <p role="alert" className="text-meta text-danger">{error}</p>
        ) : null}
        <Button type="submit" variant="primary" disabled={submitting}>
          {submitting ? "Checking…" : "Continue"}
        </Button>
      </form>
      <form action="/api/auth/sign-out" method="post">
        <button type="submit" className="text-meta text-ink-2 hover:text-ink hover:underline">
          Sign out
        </button>
      </form>
      <nav aria-label="Legal" className="mt-2 flex items-center gap-4 text-meta text-ink-2">
        <a href={PRIVACY_URL} className="hover:text-ink hover:underline">Privacy Policy</a>
        <span aria-hidden className="text-ink-3">·</span>
        <a href={TERMS_URL} className="hover:text-ink hover:underline">Terms of Service</a>
      </nav>
    </main>
  );
}
