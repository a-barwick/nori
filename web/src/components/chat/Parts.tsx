"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/primitives";
import type { Part } from "@/lib/types";

function formatSize(bytes: number) {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${bytes} bytes`;
}

/** Reads a file once, so the browser shows it by the type the service found in its bytes. */
function useFile(url: string | undefined, mediaType: string | undefined, enabled: boolean) {
  const [file, setFile] = useState<{ url?: string; missing?: boolean }>({});
  useEffect(() => {
    if (!url || !enabled) return;
    const controller = new AbortController();
    let objectUrl: string | undefined;
    void fetch(url, { cache: "no-store", signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error();
        objectUrl = URL.createObjectURL(new Blob([await response.arrayBuffer()], { type: mediaType }));
        setFile({ url: objectUrl });
      })
      .catch(() => { if (!controller.signal.aborted) setFile({ missing: true }); });
    return () => { controller.abort(); if (objectUrl) URL.revokeObjectURL(objectUrl); };
  }, [url, mediaType, enabled]);
  return file;
}

/** An image or PDF shows a preview. Any other file shows its name and a download. */
function FilePart({ part }: { part: Part }) {
  const image = !!part.media_type?.startsWith("image/");
  const pdf = part.media_type === "application/pdf";
  const [wanted, setWanted] = useState(image || pdf);
  const file = useFile(part.url, part.media_type, wanted);
  const label = `${part.name ?? "File"}${part.size ? ` · ${formatSize(part.size)}` : ""}`;
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {/* eslint-disable-next-line @next/next/no-img-element -- A blob URL has nothing for the image optimizer to fetch. */}
      {file.url && image && <img src={file.url} alt={part.name ?? "Attached image"} className="max-h-72 max-w-full rounded-tile object-contain" />}
      {file.url && pdf && <iframe src={file.url} title={part.name ?? "Attached PDF"} className="h-72 w-full min-w-[260px] rounded-tile border border-hairline bg-surface" />}
      <div className="flex min-w-0 items-center gap-2 text-meta">
        <span className="min-w-0 truncate">{label}</span>
        {file.missing && <span role="alert">This file is no longer available.</span>}
        {file.url && <a href={file.url} download={part.name} className="shrink-0 underline">Download</a>}
        {!file.url && !file.missing && !wanted && <button type="button" onClick={() => setWanted(true)} className="shrink-0 underline">Get file</button>}
      </div>
    </div>
  );
}

/** A proactive offer's card. Its decision is saved by the service, which updates this part when it changes. */
function OfferPart({ part }: { part: Part }) {
  const [sending, setSending] = useState<"accepted" | "dismissed">();
  const [error, setError] = useState("");
  const decide = async (decision: "accepted" | "dismissed") => {
    setSending(decision);
    setError("");
    try {
      const response = await fetch(part.url!, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: part.id, decision }), cache: "no-store" });
      if (!response.ok && response.status !== 409) throw new Error();
    } catch {
      setError("That didn't save. Try again, or reply in the chat.");
      setSending(undefined);
    }
  };
  if (part.status === "accepted") return <p className="text-caption text-ink-3">Accepted.</p>;
  if (part.status === "dismissed") return <p className="text-caption text-ink-3">Dismissed. I won&apos;t bring this one up again.</p>;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" disabled={!!sending} onClick={() => void decide("accepted")}>Yes, look</Button>
      <Button size="sm" variant="quiet" disabled={!!sending} onClick={() => void decide("dismissed")}>Dismiss</Button>
      {error && <span role="alert" className="text-caption">{error}</span>}
    </div>
  );
}

/** A chat action waiting for an exact decision. The service saves it and streams the change. */
function ApprovalPart({ part }: { part: Part }) {
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");
  const decide = async (decision: "approve" | "decline") => {
    setSending(true);
    setError("");
    try {
      const response = await fetch(part.url!, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ questionId: part.id, decision }), cache: "no-store", redirect: "error" });
      if (!response.ok) throw new Error();
    } catch {
      setError("That didn't save. Try again.");
      setSending(false);
    }
  };
  return (
    <div role="group" aria-label="Action approval" className="flex flex-col gap-2 rounded-tile bg-surface p-3 shadow-pill">
      <p className="whitespace-pre-wrap break-words text-body">{part.text}</p>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" disabled={sending} onClick={() => void decide("approve")}>Approve</Button>
        <Button size="sm" variant="quiet" disabled={sending} onClick={() => void decide("decline")}>Decline</Button>
        {error && <span role="alert" className="text-caption">{error}</span>}
      </div>
    </div>
  );
}

/** One non-text part. A type this client doesn't know shows the fallback text the service sent with it. */
function PartView({ part }: { part: Part }) {
  if (part.type === "link" && part.url && /^https?:\/\//.test(part.url)) return <a href={part.url} target="_blank" rel="noreferrer noopener" className="break-words text-meta underline">{part.name ?? part.url}</a>;
  if (["attachment", "artifact", "image"].includes(part.type) && part.url) return <FilePart part={part} />;
  if (part.type === "offer" && part.id && part.url) return <OfferPart part={part} />;
  if (part.type === "approval" && part.id && part.url) return <ApprovalPart part={part} />;
  if (part.type === "action") return <details className="text-caption text-ink-3"><summary className="cursor-pointer">{part.name}</summary><p className="mt-1 whitespace-pre-wrap break-words">{part.text}</p></details>;
  if (part.type === "note") return <p className="text-caption text-ink-3">{part.text}</p>;
  return part.fallback ? <p className="text-body">{part.fallback}</p> : null;
}

export function Parts({ parts }: { parts: Part[] }) {
  if (!parts.length) return null;
  return <div className="mt-2 flex flex-col gap-2 first:mt-0">{parts.map((part, index) => <PartView key={part.id ?? `${part.type}-${index}`} part={part} />)}</div>;
}
