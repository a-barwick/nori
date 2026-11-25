"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Calendar, Download, FileText, Image as ImageIcon, Link2, Mail, Pencil, Search } from "lucide-react";
import { Markdown } from "@/components/chat/Markdown";
import { Button } from "@/components/ui/primitives";
import { assistantURL, mutate, newRequestID } from "@/lib/assistant/api";
import { useAssistant } from "@/lib/assistant/context";
import { eventTime, plainText, relative, when } from "@/lib/assistant/format";
import { useLibrary } from "@/lib/assistant/sources";
import { invalidate, useAction, useResource } from "@/lib/assistant/useResource";
import type { LibraryItem, LibraryItem as BaseItem, SearchHit, SearchResponse } from "@/lib/contracts/assistant";
import { useBlobURL } from "@/lib/useBlobURL";
import { cn } from "@/lib/utils";
import { Confirm, Empty, Failure, inputClass, Loading, Notice, Pill, Row, Rows, Screen, Section, Tabs } from "./ui";

type List<T> = { items: T[] };
type Kind = "all" | "documents" | "files" | "events" | "messages";
const GROUP: Record<BaseItem["kind"], Kind> = {
  answer: "documents",
  report: "documents",
  document: "documents",
  file: "files",
  screenshot: "files",
  link: "documents",
  event: "events",
  message: "messages",
};

function KindIcon({ item }: { item: Pick<BaseItem, "kind" | "media_type"> }) {
  const Icon =
    item.kind === "event"
      ? Calendar
      : item.kind === "message"
        ? Mail
        : item.kind === "link"
          ? Link2
          : item.kind === "screenshot" || item.media_type?.startsWith("image/")
            ? ImageIcon
            : FileText;
  const isDoc = GROUP[item.kind] === "documents";
  return (
    <span
      className={cn(
        "grid size-9 shrink-0 place-items-center rounded-[10px]",
        isDoc ? "bg-accent-subtle text-accent-text" : "bg-fill-1 text-ink-2",
      )}
    >
      <Icon size={17} strokeWidth={1.85} aria-hidden />
    </span>
  );
}

/** Copied-versus-live provenance and the source account's state, in a few words. */
export function provenanceLabel(item: Pick<BaseItem, "provenance">) {
  const { content, connection_status } = item.provenance;
  const gone = connection_status && connection_status !== "connected";
  if (content === "live") return gone ? "Live link · account disconnected" : "Live from the source";
  if (content === "copied") return gone ? "Saved copy · account disconnected" : "Saved copy";
  return content === "uploaded" ? "You uploaded this" : "Made by Nori";
}

export function jumpHref(hit: SearchHit) {
  const jump = hit.jump;
  if (!jump) {
    if (hit.kind === "task") return `/work/${encodeURIComponent(hit.id)}`;
    if (hit.kind === "memory") return "/memory";
    return `/library/${encodeURIComponent(hit.id)}`;
  }
  switch (jump.kind) {
    case "conversation":
      return `/${jump.message_id ? `#${jump.message_id}` : ""}`;
    case "task":
      return `/work/${encodeURIComponent(jump.task_id)}`;
    case "library":
      return `/library/${encodeURIComponent(jump.item_id)}`;
    case "goal":
      return `/goals/${encodeURIComponent(jump.goal_id)}`;
    case "automation":
      return `/routines/${encodeURIComponent(jump.automation_id)}`;
    case "memory": {
      const params = new URLSearchParams();
      if (jump.layer === "log" || (!jump.layer && (jump.log_date || jump.entry_id))) {
        params.set("tab", "logs");
        if (jump.log_date) params.set("date", jump.log_date);
        if (jump.entry_id) params.set("entry", jump.entry_id);
        return `/memory?${params.toString()}`;
      }
      if (jump.layer === "core" || jump.memory_id) {
        params.set("tab", "core");
        if (jump.memory_id) params.set("id", jump.memory_id);
        return `/memory?${params.toString()}`;
      }
      return "/memory";
    }
  }
}

/** Embeddable Artifacts / Library content used inside Work -> Artifacts and standalone /library. */
export function LibraryContent({ syncURL = false }: { syncURL?: boolean }) {
  const { has } = useAssistant();
  const params = useSearchParams();
  const router = useRouter();
  const [query, setQuery] = useState(syncURL ? (params.get("q") ?? "") : "");
  const [kind, setKind] = useState<Kind>("all");
  const [settled, setSettled] = useState(query);

  useEffect(() => {
    const timer = setTimeout(() => setSettled(query.trim()), 300);
    return () => clearTimeout(timer);
  }, [query]);

  useEffect(() => {
    if (!syncURL) return;
    router.replace(settled ? `/library?q=${encodeURIComponent(settled)}` : "/library", { scroll: false });
  }, [settled, router, syncURL]);

  const library = useLibrary();
  const search = useResource<SearchResponse>(has("search") && settled ? `/search?q=${encodeURIComponent(settled)}` : null, {
    poll: 0,
  });
  const items = useMemo(() => (library.items ?? []) as LibraryItem[], [library.items]);
  const filtered = useMemo(
    () =>
      items.filter(
        (item) =>
          (kind === "all" || GROUP[item.kind] === kind) &&
          (has("search") ||
            !settled ||
            `${item.title} ${item.name ?? ""} ${item.preview_text ?? ""}`.toLowerCase().includes(settled.toLowerCase())),
      ),
    [items, kind, settled, has],
  );
  const collections = useMemo(() => [...new Set(filtered.map((item) => item.collection).filter(Boolean))] as string[], [filtered]);

  return (
    <div className="space-y-4">
      <label className="relative block">
        <span className="sr-only">Filter artifacts</span>
        <Search size={15} className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-ink-3" aria-hidden />
        <input
          autoFocus={syncURL && !!params.get("q")}
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Filter artifacts & saved files…"
          className={cn(inputClass, "h-10 pl-9 text-[14px]")}
        />
      </label>

      {settled && has("search") ? (
        search.loading ? (
          <Loading label="Searching…" />
        ) : search.error ? (
          <Failure error={search.error} onRetry={() => void search.reload()} />
        ) : search.data?.items.length ? (
          <Rows label="Search results">
            {search.data.items.map((hit) => (
              <Row
                key={`${hit.kind}:${hit.id}`}
                href={jumpHref(hit)}
                title={hit.title}
                detail={
                  <>
                    <span className="line-clamp-2">{hit.snippet ? plainText(hit.snippet) : hit.snippet}</span>
                    {hit.provenance && (
                      <span className="mt-0.5 block text-caption text-ink-3">
                        {provenanceLabel({ provenance: hit.provenance })}
                      </span>
                    )}
                  </>
                }
                trailing={
                  <Pill>
                    {
                      {
                        result: "Saved",
                        message: "Chat",
                        task: "Task",
                        goal: "Goal",
                        automation: "Routine",
                        file: "File",
                        memory: "Memory",
                      }[hit.kind]
                    }
                  </Pill>
                }
              />
            ))}
          </Rows>
        ) : (
          <Empty title="No matches">Nothing matches “{settled}”. Try fewer words.</Empty>
        )
      ) : (
        <>
          <Tabs
            label="Kind"
            value={kind}
            onChange={setKind}
            items={[
              { id: "all", label: "All" },
              { id: "documents", label: "Docs" },
              { id: "files", label: "Files" },
              { id: "events", label: "Events" },
              { id: "messages", label: "Mail" },
            ]}
          />
          {library.error && <Failure error={library.error} onRetry={() => void library.reload()} />}
          {library.loading ? (
            <Loading />
          ) : filtered.length ? (
            collections.length ? (
              <>
                {[...collections, undefined].map((collection) => {
                  const rows = filtered.filter((item) => (item.collection ?? undefined) === collection);
                  if (!rows.length) return null;
                  return (
                    <Section key={collection ?? "_"} title={collection ?? "Saved"}>
                      <Rows>
                        {rows.map((item) => (
                          <Row
                            key={item.id}
                            href={`/library/${encodeURIComponent(item.id)}`}
                            leading={<KindIcon item={item} />}
                            title={item.title}
                            detail={[
                              item.preview_text ? plainText(item.preview_text) : "",
                              `${provenanceLabel(item)} · ${relative(item.created_at)}`,
                            ]
                              .filter(Boolean)
                              .join(" · ")}
                            trailing={item.version > 1 ? <Pill>v{item.version}</Pill> : undefined}
                          />
                        ))}
                      </Rows>
                    </Section>
                  );
                })}
              </>
            ) : (
              <Rows label="Saved artifacts">
                {filtered.map((item) => (
                  <Row
                    key={item.id}
                    href={`/library/${encodeURIComponent(item.id)}`}
                    leading={<KindIcon item={item} />}
                    title={item.title}
                    detail={[
                      item.preview_text ? plainText(item.preview_text) : "",
                      `${provenanceLabel(item)} · ${relative(item.created_at)}`,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                    trailing={item.version > 1 ? <Pill>v{item.version}</Pill> : undefined}
                  />
                ))}
              </Rows>
            )
          ) : (
            <Empty title={settled ? "No matching artifacts" : "Nothing saved yet"}>
              {settled ? `Nothing saved matches “${settled}”.` : "Documents Nori creates and files you share appear here."}
            </Empty>
          )}
        </>
      )}
    </div>
  );
}

export function LibraryScreen() {
  return (
    <Screen title="Artifacts" back="/work?tab=artifacts" backLabel="Work">
      <LibraryContent syncURL />
    </Screen>
  );
}

export function LibraryDetailScreen({ id }: { id: string }) {
  const { has } = useAssistant();
  const live = has("library");
  const remote = useResource<LibraryItem>(live ? `/library/${encodeURIComponent(id)}` : null, { poll: 30_000 });
  const versions = useResource<List<LibraryItem>>(live ? `/library/${encodeURIComponent(id)}/versions` : null, { poll: 0 });
  const legacy = useLibrary();
  const item = live ? remote.data : (legacy.items as LibraryItem[] | undefined)?.find((candidate) => candidate.id === id);
  const [text, setText] = useState<string>();
  const action = useAction();
  const router = useRouter();
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState("");
  const [revision, setRevision] = useState("");

  useEffect(() => {
    if (!item || item.data || !["answer", "report", "document"].includes(item.kind)) return;
    let cancelled = false;
    const path = live ? assistantURL(`/library/${encodeURIComponent(item.id)}/file`) : `/api/v1/results/${encodeURIComponent(item.id)}`;
    fetch(path, { cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) return;
        const type = response.headers.get("content-type") ?? "";
        const body = type.includes("json") ? (await response.json())?.text : await response.text();
        if (!cancelled && typeof body === "string") setText(body);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [item, live]);

  if (live ? remote.loading : legacy.loading) {
    return (
      <Screen title="Artifact" back="/work?tab=artifacts" backLabel="Work">
        <Loading />
      </Screen>
    );
  }
  if (!item) {
    return (
      <Screen title="Artifact" back="/work?tab=artifacts" backLabel="Work">
        {remote.error ? (
          <Failure error={remote.error} />
        ) : (
          <Empty
            action={
              <Link href="/work?tab=artifacts" className="text-meta font-semibold text-accent-text hover:underline">
                Back to Artifacts
              </Link>
            }
          >
            This isn’t in your Library. It may have been deleted.
          </Empty>
        )}
      </Screen>
    );
  }

  const act = (value: Record<string, unknown>, label: string) =>
    action.run(label, async () => {
      const receipt = await mutate<LibraryItem>(`/library/${encodeURIComponent(id)}/actions`, {
        request_id: newRequestID(),
        expected_revision: item.revision,
        value,
      });
      invalidate("/library");
      if (value.action === "delete") router.push("/work?tab=artifacts");
      else if (receipt.status === "queued" && receipt.work?.task_id) {
        router.push(`/work/${encodeURIComponent(receipt.work.task_id)}`);
      }
    });

  const rename = () =>
    action.run("rename", async () => {
      await mutate(
        `/library/${encodeURIComponent(id)}`,
        {
          request_id: newRequestID(),
          expected_revision: item.revision,
          value: { title: title.trim(), ...(item.collection ? { collection: item.collection } : {}) },
        },
        "PUT",
      );
      setRenaming(false);
      invalidate("/library");
    });

  const fileURL =
    item.url &&
    ((item.url.startsWith("/") && !item.url.startsWith("//") && !item.url.startsWith("/\\") && !item.url.includes("\\")) ||
      /^https:\/\//.test(item.url))
      ? item.url
      : undefined;

  return (
    <Screen
      title={item.title}
      back="/work?tab=artifacts"
      backLabel="Work"
      line={provenanceLabel(item)}
      actions={
        <>
          {item.version > 1 && <Pill tone="accent">v{item.version}</Pill>}
          {item.superseded_by && (
            <Link href={`/library/${encodeURIComponent(item.superseded_by)}`}>
              <Pill tone="warning">Newer version</Pill>
            </Link>
          )}
        </>
      }
    >
      {action.error && (
        <div className="mb-4">
          <Notice tone="danger">{action.error}</Notice>
        </div>
      )}
      <Preview item={item} text={text} />
      <div className="mt-4 flex flex-wrap gap-2">
        {fileURL && item.kind !== "link" && (
          <a
            href={fileURL}
            download={item.name}
            rel="noreferrer noopener"
            className="inline-flex h-9 items-center gap-1.5 rounded-full px-4 text-row-title font-medium shadow-[inset_0_0_0_1px_var(--hairline-strong)] hover:bg-fill-1"
          >
            <Download size={15} aria-hidden />
            Download
          </a>
        )}
        {item.kind === "link" && fileURL && (
          <a
            href={fileURL}
            target="_blank"
            rel="noreferrer noopener"
            className="inline-flex h-9 items-center gap-1.5 rounded-full px-4 text-row-title font-medium shadow-[inset_0_0_0_1px_var(--hairline-strong)] hover:bg-fill-1"
          >
            <Link2 size={15} aria-hidden />
            Open link
          </a>
        )}
        {text && (
          <Button
            onClick={() => {
              const url = URL.createObjectURL(new Blob([text], { type: "text/markdown" }));
              const link = document.createElement("a");
              link.href = url;
              link.download = `${item.title.slice(0, 80)}.md`;
              link.click();
              setTimeout(() => URL.revokeObjectURL(url), 1000);
            }}
          >
            <Download size={15} aria-hidden />
            Export
          </Button>
        )}
        {live && (
          <Button
            onClick={() => {
              setTitle(item.title);
              setRenaming(true);
            }}
          >
            <Pencil size={14} aria-hidden />
            Rename
          </Button>
        )}
      </div>
      {renaming && (
        <form
          className="mt-3 flex gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            if (title.trim()) void rename();
          }}
        >
          <input
            aria-label="New name"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            className={inputClass}
            autoFocus
          />
          <Button type="submit" variant="primary" disabled={!title.trim() || !!action.pending}>
            Save
          </Button>
          <Button variant="quiet" onClick={() => setRenaming(false)}>
            Cancel
          </Button>
        </form>
      )}
      {live && ["answer", "report", "document"].includes(item.kind) && (
        <Section title="Revise">
          <form
            className="flex flex-col gap-2 sm:flex-row"
            onSubmit={(event) => {
              event.preventDefault();
              if (revision.trim()) void act({ action: "revise", instruction: revision.trim() }, "revise");
            }}
          >
            <input
              aria-label="What to change"
              value={revision}
              onChange={(event) => setRevision(event.target.value)}
              className={inputClass}
              placeholder="Add prices in euros, shorten the summary…"
            />
            <Button type="submit" variant="primary" disabled={!revision.trim() || !!action.pending}>
              {action.pending === "revise" ? "Starting…" : "Make a new version"}
            </Button>
          </form>
        </Section>
      )}
      {(versions.data?.items.length ?? 0) > 1 && (
        <Section title="Versions">
          <Rows>
            {versions.data!.items.map((version) => (
              <Row
                key={version.id}
                href={version.id === item.id ? undefined : `/library/${encodeURIComponent(version.id)}`}
                title={`Version ${version.version}`}
                detail={when(version.created_at)}
                trailing={version.id === item.id ? <Pill tone="accent">Current</Pill> : undefined}
              />
            ))}
          </Rows>
        </Section>
      )}
      <Section title="From">
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-meta">
          {item.task_id && (
            <>
              <dt className="text-ink-3">Task</dt>
              <dd>
                <Link className="text-accent-text hover:underline" href={`/work/${encodeURIComponent(item.task_id)}`}>
                  Open task
                </Link>
              </dd>
            </>
          )}
          {item.automation_id && (
            <>
              <dt className="text-ink-3">Routine</dt>
              <dd>
                <Link className="text-accent-text hover:underline" href={`/routines/${encodeURIComponent(item.automation_id)}`}>
                  Open routine
                </Link>
              </dd>
            </>
          )}
          {item.goal_id && (
            <>
              <dt className="text-ink-3">Goal</dt>
              <dd>
                <Link className="text-accent-text hover:underline" href={`/goals/${encodeURIComponent(item.goal_id)}`}>
                  Open goal
                </Link>
              </dd>
            </>
          )}
          {item.provenance.source_url && /^https:\/\//.test(item.provenance.source_url) && (
            <>
              <dt className="text-ink-3">Source</dt>
              <dd>
                <a
                  className="text-accent-text hover:underline"
                  href={item.provenance.source_url}
                  target="_blank"
                  rel="noreferrer noopener"
                >
                  Open original
                </a>
              </dd>
            </>
          )}
          {item.provenance.retrieved_at && (
            <>
              <dt className="text-ink-3">Fetched</dt>
              <dd>{when(item.provenance.retrieved_at)}</dd>
            </>
          )}
          <dt className="text-ink-3">Saved</dt>
          <dd>{when(item.created_at)}</dd>
        </dl>
      </Section>
      {live && (
        <Section>
          <Confirm
            danger
            title="Delete this?"
            confirm="Delete"
            onConfirm={() => void act({ action: "delete", scope: "all_versions" }, "delete")}
            body={item.delete_effect ?? "Removes it and its versions from Nori."}
            trigger={
              <Button variant="quiet" className="text-danger!" disabled={!!action.pending}>
                Delete
              </Button>
            }
          />
        </Section>
      )}
    </Screen>
  );
}

function Preview({ item, text }: { item: LibraryItem; text?: string }) {
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
    async (signal: AbortSignal) => {
      const response = await fetch(url, { cache: "no-store", signal });
      if (!response.ok) throw new Error("This file is no longer available.");
      return new Blob([await response.arrayBuffer()], { type: mediaType });
    },
    [url, mediaType],
  );
  const file = useBlobURL(load);
  if (file?.error !== undefined) return <Notice tone="danger">This file is no longer available.</Notice>;
  if (!file?.url) return <Loading label="Loading preview…" />;
  return mediaType === "application/pdf" ? (
    <iframe src={file.url} title={name} className="h-[70vh] w-full rounded-card border border-hairline" />
  ) : (
    // eslint-disable-next-line @next/next/no-img-element -- blob URL
    <img src={file.url} alt={name} className="max-h-[70vh] max-w-full rounded-card border border-hairline object-contain" />
  );
}
