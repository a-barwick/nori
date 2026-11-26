"use client";

import { useState, type FormEvent } from "react";
import { Button } from "@/components/ui/primitives";
import "@/lib/desktop";
import { Notice } from "./ui";

export function SignOutForm() {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    if (!window.noriDesktop) return;
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setError("");
    try {
      await window.noriDesktop.signOut();
    } catch {
      setError("Nori couldn’t sign out. Try again.");
      setPending(false);
    }
  };

  return <form action="/api/auth/sign-out" method="post" onSubmit={(event) => void submit(event)} className="space-y-3">
    {error && <Notice tone="danger">{error}</Notice>}
    <Button type="submit" variant="quiet" disabled={pending}>{pending ? "Signing out…" : "Sign out"}</Button>
  </form>;
}
