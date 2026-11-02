"use client";

import type { Part } from "@/lib/types";

/** One non-text part. A type this client doesn't know shows the fallback text the service sent with it. */
function PartView({ part }: { part: Part }) {
  return part.fallback ? <p className="text-body">{part.fallback}</p> : null;
}

export function Parts({ parts }: { parts: Part[] }) {
  if (!parts.length) return null;
  return <div className="mt-2 flex flex-col gap-2 first:mt-0">{parts.map((part, index) => <PartView key={part.id ?? `${part.type}-${index}`} part={part} />)}</div>;
}
