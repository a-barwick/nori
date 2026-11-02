"use client";

import { useEffect, useState } from "react";
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

/** One non-text part. A type this client doesn't know shows the fallback text the service sent with it. */
function PartView({ part }: { part: Part }) {
  if (part.type === "link" && part.url && /^https?:\/\//.test(part.url)) return <a href={part.url} target="_blank" rel="noreferrer noopener" className="break-words text-meta underline">{part.name ?? part.url}</a>;
  if (["attachment", "artifact", "image"].includes(part.type) && part.url) return <FilePart part={part} />;
  return part.fallback ? <p className="text-body">{part.fallback}</p> : null;
}

export function Parts({ parts }: { parts: Part[] }) {
  if (!parts.length) return null;
  return <div className="mt-2 flex flex-col gap-2 first:mt-0">{parts.map((part, index) => <PartView key={part.id ?? `${part.type}-${index}`} part={part} />)}</div>;
}
