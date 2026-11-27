"use client";

import { useRouter } from "next/navigation";
import { FormEvent, useState } from "react";
import { Notice, inputClass } from "@/components/assistant/ui";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { PRIVACY_URL, TERMS_URL } from "@/lib/legal";
import { cn } from "@/lib/utils";

function friendlyInviteError(status: number, body: { error?: unknown; code?: unknown } | null): string {
  const code = typeof body?.code === "string" ? body.code : "";
  if (status === 429) return "Too many attempts. Wait a moment and try again.";
  if (code === "invite_code_required") return "Enter an invite code.";
  if (status === 401 || status === 403) {
    if (code === "invite_invalid" || code === "invite_expired" || code === "invite_used") {
      return "That invite code is invalid or has already been used.";
    }
    return "Your session expired. Use a different account or sign in again.";
  }
  if (status === 400 || status === 404 || status === 409) {
    return "That invite code is invalid or has already been used.";
  }
  if (status >= 500) return "Nori could not verify your invite code right now. Try again.";
  return "Could not verify your invite code. Try again.";
}

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
        const body = await response.json().catch(() => null) as { error?: unknown; code?: unknown } | null;
        setError(friendlyInviteError(response.status, body));
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

  const trimmedCode = code.trim();

  return (
    <main className="flex h-full flex-col overflow-y-auto bg-canvas p-6 text-center">
      <div className="mx-auto my-auto flex w-full max-w-sm flex-col items-center gap-6 py-4">
        <NoriMark size={72} state={submitting ? "thinking" : "idle"} />
        <h1 className="text-display font-semibold text-ink">Enter your invite code</h1>
        <form onSubmit={onSubmit} className="flex w-full flex-col gap-3 text-left">
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
            onChange={(e) => {
              setCode(e.target.value);
              if (error) setError(null);
            }}
            disabled={submitting}
            className={cn(inputClass, "h-12 px-3.5 font-mono")}
          />
          {error ? <Notice tone="danger">{error}</Notice> : null}
          <Button
            type="submit"
            variant="primary"
            disabled={submitting || !trimmedCode}
            className="h-12 w-full text-[15px]"
          >
            {submitting ? "Checking…" : "Continue"}
          </Button>
        </form>
        <form action="/api/auth/sign-out" method="post">
          <button
            type="submit"
            disabled={submitting}
            className="text-meta text-ink-2 hover:text-ink hover:underline disabled:text-disabled disabled:no-underline"
          >
            Use a different account
          </button>
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
