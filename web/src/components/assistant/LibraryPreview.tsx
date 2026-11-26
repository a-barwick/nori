"use client";

import { useCallback } from "react";
import { Markdown } from "@/components/chat/Markdown";
import { fetchFileBlob } from "@/lib/assistant/api";
import { eventTime, when } from "@/lib/assistant/format";
import type { LibraryItem } from "@/lib/contracts/assistant";
import { useBlobURL } from "@/lib/useBlobURL";
import { Loading, Notice, Pill } from "./ui";

export function LibraryPreview({ item, text }: { item: LibraryItem; text?: string }) {
  if (item.data_kind === "calendar_event" && item.data) {
    const event = item.data;
    return (
      <article className="rounded-card border border-hairline bg-surface p-4">
        <p className="text-section font-semibold">{event.title}</p>
        <p className="mt-1 text-body text-ink-2">
          {eventTime(event.time)}
          {event.location ? ` · ${event.location}` : ""}
        </p>
        {!!event.attendees?.length && (
          <p className="mt-2 text-meta text-ink-2">
            {event.attendees
              .filter((attendee) => !attendee.self)
              .map(
                (attendee) =>
                  `${attendee.name ?? attendee.email}${attendee.response === "needs_action" ? " (no reply)" : attendee.response === "declined" ? " (declined)" : ""}`,
              )
              .join(", ")}
          </p>
        )}
        {event.source_url && /^https:\/\//.test(event.source_url) && (
          <a
            href={event.source_url}
            target="_blank"
            rel="noreferrer noopener"
            className="mt-3 inline-block text-meta font-medium text-accent-text hover:underline"
          >
            Open in calendar
          </a>
        )}
      </article>
    );
  }
  if (item.data_kind === "mail_draft" && item.data) {
    const draft = item.data;
    return (
      <article className="rounded-card border border-hairline bg-surface p-4 text-body">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-meta">
          <dt className="text-ink-3">From</dt>
          <dd>{draft.sender.email}</dd>
          <dt className="text-ink-3">To</dt>
          <dd>{draft.to.map((to) => to.email).join(", ")}</dd>
          {draft.cc.length > 0 && (
            <>
              <dt className="text-ink-3">Cc</dt>
              <dd>{draft.cc.map((cc) => cc.email).join(", ")}</dd>
            </>
          )}
        </dl>
        <p className="mt-3 font-semibold">{draft.subject}</p>
        <p className="mt-2 whitespace-pre-wrap">{draft.body_text}</p>
        <p className="mt-3">
          <Pill tone={draft.state === "sent" ? "ok" : "neutral"}>{draft.state === "sent" ? "Sent" : "Draft, not sent"}</Pill>
        </p>
      </article>
    );
  }
  if (item.data_kind === "mail_message" && item.data) {
    const message = item.data;
    return (
      <article className="rounded-card border border-hairline bg-surface p-4 text-body">
        <p className="text-meta text-ink-2">
          {message.from.name ?? message.from.email} · {when(message.received_at)}
        </p>
        <p className="mt-1 font-semibold">{message.subject}</p>
        <p className="mt-2 whitespace-pre-wrap">{message.content.text}</p>
        {message.content.kind === "snippet" && <p className="mt-2 text-meta text-ink-3">Snippet only</p>}
      </article>
    );
  }
  if (item.data_kind === "availability" && item.data) {
    return (
      <article className="rounded-card border border-hairline bg-surface p-4">
        <ul className="space-y-1 text-body">
          {item.data.slots.map((slot) => (
            <li key={slot.start} className="flex justify-between gap-3">
              <span>{when(slot.start)}</span>
              <Pill tone={slot.status === "free" ? "ok" : slot.status === "unknown" ? "warning" : "neutral"}>
                {slot.status === "unknown" ? "Unknown" : slot.status}
              </Pill>
            </li>
          ))}
        </ul>
      </article>
    );
  }
  if (text) {
    return (
      <article className="nori-document">
        <Markdown variant="page">{text}</Markdown>
      </article>
    );
  }
  if ((item.kind === "screenshot" || item.media_type?.startsWith("image/") || item.media_type === "application/pdf") && item.url) {
    return <FilePreview url={item.url} mediaType={item.media_type} name={item.name ?? item.title} />;
  }
  return null;
}

function FilePreview({ url, mediaType, name }: { url: string; mediaType?: string; name: string }) {
  const load = useCallback(
    (signal: AbortSignal) => fetchFileBlob(url, mediaType, signal),
    [url, mediaType],
  );
  const file = useBlobURL(load);
  if (file?.error !== undefined) return <Notice tone="danger">This file is no longer available.</Notice>;
  if (!file?.url) return <Loading label="Loading preview…" />;
  return mediaType === "application/pdf" ? (
    <iframe src={file.url} title={name} className="h-[70vh] w-full rounded-card border border-hairline" />
  ) : (
    // eslint-disable-next-line @next/next/no-img-element
    <img src={file.url} alt={name} className="max-h-[70vh] max-w-full rounded-card border border-hairline object-contain" />
  );
}
