"use client";

import { useCallback, useState } from "react";
import { CardView } from "@/components/assistant/CardView";
import { ReminderCard } from "@/components/assistant/ReminderCard";
import type { CardPart } from "@/lib/contracts/assistant";
import type { Part } from "@/lib/types";
import { useBlobURL } from "@/lib/useBlobURL";

function formatSize(bytes: number) {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${bytes} bytes`;
}

function FilePart({ part, url }: { part: Part; url: string }) {
  const image = !!part.media_type?.startsWith("image/");
  const pdf = part.media_type === "application/pdf";
  const [requested, setRequested] = useState(false);
  const wanted = image || pdf || requested;
  const load = useCallback(async (signal: AbortSignal) => {
    const response = await fetch(url, { cache: "no-store", signal });
    if (!response.ok) throw new Error("This file is no longer available.");
    return new Blob([await response.arrayBuffer()], { type: part.media_type });
  }, [url, part.media_type]);
  const file = useBlobURL(wanted ? load : undefined);
  const label = `${part.name ?? "File"}${part.size ? ` · ${formatSize(part.size)}` : ""}`;
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {/* eslint-disable-next-line @next/next/no-img-element -- A blob URL has nothing for the image optimizer to fetch. */}
      {file?.url && image && <img src={file.url} alt={part.name ?? "Attached image"} className="max-h-72 max-w-full rounded-tile object-contain" />}
      {file?.url && pdf && <iframe src={file.url} title={part.name ?? "Attached PDF"} className="h-72 w-full min-w-[260px] rounded-tile border border-hairline bg-surface" />}
      <div className="flex min-w-0 items-center gap-2 text-meta">
        <span className="min-w-0 truncate">{label}</span>
        {file?.error !== undefined && <span role="alert">This file is no longer available.</span>}
        {file?.url && <a href={file.url} download={part.name} className="shrink-0 underline">Download</a>}
        {!wanted && <button type="button" onClick={() => setRequested(true)} className="shrink-0 underline">Get file</button>}
      </div>
    </div>
  );
}

function PartView({ part }: { part: Part }) {
  if (part.type === "link" && part.url && /^https?:\/\//.test(part.url)) return <a href={part.url} target="_blank" rel="noreferrer noopener" className="break-words text-meta underline">{part.name ?? part.url}</a>;
  if (["attachment", "artifact", "image"].includes(part.type) && part.url) return <FilePart part={part} url={part.url} />;
  if (part.type === "action") return <details className="text-caption text-ink-3"><summary className="cursor-pointer">{part.name}</summary><p className="mt-1 whitespace-pre-wrap break-words">{part.text}</p></details>;
  if (part.type === "card" && part.resource && part.kind && part.fallback !== undefined) return <CardView part={part as CardPart} />;
  if (part.type === "reminder") return <ReminderCard part={part} />;
  if (part.type === "note") return <p className="text-caption text-ink-3">{part.text}</p>;
  return part.fallback ? <p className="text-body">{part.fallback}</p> : null;
}

export function Parts({ parts }: { parts: Part[] }) {
  if (!parts.length) return null;
  return <div className="mt-2 flex flex-col gap-2 first:mt-0">{parts.map((part, index) => <PartView key={part.id ?? `${part.type}-${index}`} part={part} />)}</div>;
}
