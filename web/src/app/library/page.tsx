"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { DropdownMenu } from "radix-ui";
import { ArrowLeft, Check, Download, Ellipsis, LayoutDashboard, ListFilter, PanelsTopLeft, Plus, Search, Sparkles, FileText, Trash2 } from "lucide-react";
import { Markdown } from "@/components/chat/Markdown";
import { Thumbnail } from "@/components/chat/Thumbnail";
import { Waveform } from "@/components/chat/Message";
import { Button, IconButton } from "@/components/ui/primitives";
import { artifactTypeLabel, useNori, type LibraryFilter } from "@/lib/store";
import { shortDate, duration } from "@/lib/time";
import type { Artifact } from "@/lib/types";
import { cn, waveform } from "@/lib/utils";

const NAV: { heading?: string; items: { key: LibraryFilter; label: string }[] }[] = [
  {
    heading: "Made by Nori",
    items: [
      { key: "all", label: "All" },
      { key: "document", label: "Documents" },
      { key: "page", label: "Pages" },
      { key: "dashboard", label: "Dashboards" },
    ],
  },
  {
    heading: "Media",
    items: [
      { key: "image", label: "Images" },
      { key: "audio", label: "Audio" },
    ],
  },
  { items: [{ key: "file", label: "Files" }] },
];

const menuItem =
  "flex h-9 cursor-default items-center gap-2.5 rounded-chip px-2.5 text-row-title text-ink outline-none data-[highlighted]:bg-fill-1";

function matches(a: Artifact, filter: LibraryFilter) {
  if (filter === "all") return a.madeBy === "nori";
  return a.type === filter;
}

function Sidebar({ q, setQ }: { q: string; setQ: (q: string) => void }) {
  const filter = useNori((s) => s.libraryFilter);
  const set = useNori((s) => s.set);
  return (
    <aside className="hairline-r flex h-full w-60 shrink-0 flex-col bg-sidebar px-3 pt-5">
      <label className="flex h-9 items-center gap-2 rounded-full bg-fill-1 px-3 text-ink-3 shadow-[inset_0_0_0_1px_var(--hairline)] focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-focus">
        <Search size={16} strokeWidth={1.5} aria-hidden />
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search Library"
          aria-label="Search Library"
          className="min-w-0 flex-1 bg-transparent text-input text-ink placeholder:text-ink-3 focus-visible:outline-none"
        />
      </label>
      <nav aria-label="Library" className="mt-4 flex flex-col gap-4">
        {NAV.map((group, i) => (
          <div key={i}>
            {group.heading && <div className="mb-1 px-2.5 text-caption text-ink-3">{group.heading}</div>}
            {group.items.map((item) => (
              <button
                key={item.key}
                type="button"
                aria-current={filter === item.key ? "page" : undefined}
                onClick={() => set({ libraryFilter: item.key, viewerId: null })}
                className={cn(
                  "t-fast flex h-9 w-full items-center rounded-chip px-2.5 text-left text-row-title transition-colors",
                  filter === item.key ? "bg-fill-2 font-medium text-ink" : "text-ink-2 hover:bg-fill-1 hover:text-ink",
                )}
              >
                {item.label}
              </button>
            ))}
          </div>
        ))}
      </nav>
    </aside>
  );
}

function Card({ a, selecting, selected, onToggle }: { a: Artifact; selecting: boolean; selected: boolean; onToggle: () => void }) {
  const set = useNori((s) => s.set);
  return (
    <button
      type="button"
      onClick={() => (selecting ? onToggle() : set({ viewerId: a.id }))}
      aria-pressed={selecting ? selected : undefined}
      className="group/card text-left"
    >
      <div className={cn("relative overflow-hidden rounded-card shadow-[inset_0_0_0_1px_var(--hairline)] transition-shadow", selected && "shadow-[0_0_0_2px_var(--accent-signal)]")}>
        <Thumbnail artifact={a} className="t-fast transition-transform duration-300 group-hover/card:scale-[1.015]" />
        {selecting && (
          <span
            className={cn(
              "absolute top-2.5 left-2.5 grid size-5 place-items-center rounded-full",
              selected ? "bg-accent text-on-accent" : "glass",
            )}
          >
            {selected && <Check size={13} strokeWidth={2.5} />}
          </span>
        )}
      </div>
      <div className="mt-2 truncate text-row-title font-medium text-ink">{a.title}</div>
      <div className="text-caption text-ink-3">
        {artifactTypeLabel[a.type]} · {shortDate(a.createdAt)}
      </div>
    </button>
  );
}

function Grid() {
  const artifacts = useNori((s) => s.artifacts);
  const filter = useNori((s) => s.libraryFilter);
  const set = useNori((s) => s.set);
  const router = useRouter();
  const [q, setQ] = useState("");
  const [sort, setSort] = useState<"new" | "old" | "az">("new");
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);

  const rows = artifacts
    .filter((a) => matches(a, filter))
    .filter((a) => !q || `${a.title} ${a.body ?? ""}`.toLowerCase().includes(q.toLowerCase()))
    .sort((a, b) => (sort === "az" ? a.title.localeCompare(b.title) : sort === "old" ? a.createdAt - b.createdAt : b.createdAt - a.createdAt));

  const title = filter === "all" ? "Library" : NAV.flatMap((g) => g.items).find((i) => i.key === filter)!.label;

  const askNori = (prompt: string) => {
    set({ activeThreadId: "main", drafts: { ...useNori.getState().drafts, main: prompt }, composerFocus: Date.now() });
    router.push("/");
  };

  return (
    <div className="flex h-full">
      <Sidebar q={q} setQ={setQ} />
      <div className="scrollbar-quiet h-full min-w-0 flex-1 overflow-y-auto">
        <div className="px-10 pt-12 pb-24">
          <div className="flex items-center justify-between gap-4">
            <h1 className="text-display font-medium text-ink">{title}</h1>
            <div className="flex items-center gap-2">
              <Button
                onClick={() => {
                  setSelecting((s) => !s);
                  setSelected([]);
                }}
              >
                {selecting ? (selected.length ? `Done · ${selected.length}` : "Done") : "Select"}
              </Button>
              {selecting && selected.length > 0 && (
                <IconButton
                  label="Delete selected"
                  onClick={() => {
                    useNori.setState((s) => ({ artifacts: s.artifacts.filter((a) => !selected.includes(a.id)) }));
                    setSelected([]);
                    setSelecting(false);
                  }}
                  className="bg-surface shadow-[inset_0_0_0_1px_var(--hairline-strong)]"
                >
                  <Trash2 size={17} strokeWidth={1.5} />
                </IconButton>
              )}
              <DropdownMenu.Root>
                <DropdownMenu.Trigger asChild>
                  <IconButton label="Sort" className="bg-surface shadow-[inset_0_0_0_1px_var(--hairline-strong)]">
                    <ListFilter size={17} strokeWidth={1.5} />
                  </IconButton>
                </DropdownMenu.Trigger>
                <DropdownMenu.Portal>
                  <DropdownMenu.Content align="end" sideOffset={6} className="menu z-[60] min-w-40 rounded-card bg-surface p-1.5 shadow-pop">
                    <DropdownMenu.RadioGroup value={sort} onValueChange={(v) => setSort(v as typeof sort)}>
                      {[
                        ["new", "Newest first"],
                        ["old", "Oldest first"],
                        ["az", "A to Z"],
                      ].map(([k, label]) => (
                        <DropdownMenu.RadioItem key={k} value={k} className={menuItem}>
                          <span className="w-4">
                            <DropdownMenu.ItemIndicator>
                              <Check size={15} strokeWidth={1.5} />
                            </DropdownMenu.ItemIndicator>
                          </span>
                          {label}
                        </DropdownMenu.RadioItem>
                      ))}
                    </DropdownMenu.RadioGroup>
                  </DropdownMenu.Content>
                </DropdownMenu.Portal>
              </DropdownMenu.Root>
              <DropdownMenu.Root>
                <DropdownMenu.Trigger asChild>
                  <Button variant="primary">
                    <Plus size={16} strokeWidth={2} aria-hidden />
                    Create
                  </Button>
                </DropdownMenu.Trigger>
                <DropdownMenu.Portal>
                  <DropdownMenu.Content align="end" sideOffset={6} className="menu z-[60] min-w-48 rounded-card bg-surface p-1.5 shadow-pop">
                    <DropdownMenu.Item className={menuItem} onSelect={() => askNori("Make me a document about ")}>
                      <FileText size={16} strokeWidth={1.5} className="text-ink-2" /> Document
                    </DropdownMenu.Item>
                    <DropdownMenu.Item className={menuItem} onSelect={() => askNori("Make me a page about ")}>
                      <PanelsTopLeft size={16} strokeWidth={1.5} className="text-ink-2" /> Page
                    </DropdownMenu.Item>
                    <DropdownMenu.Item className={menuItem} onSelect={() => askNori("Make me a dashboard for ")}>
                      <LayoutDashboard size={16} strokeWidth={1.5} className="text-ink-2" /> Dashboard
                    </DropdownMenu.Item>
                  </DropdownMenu.Content>
                </DropdownMenu.Portal>
              </DropdownMenu.Root>
            </div>
          </div>

          {rows.length ? (
            <div className="mt-8 grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-x-5 gap-y-7">
              {rows.map((a) => (
                <Card
                  key={a.id}
                  a={a}
                  selecting={selecting}
                  selected={selected.includes(a.id)}
                  onToggle={() => setSelected((s) => (s.includes(a.id) ? s.filter((x) => x !== a.id) : [...s, a.id]))}
                />
              ))}
            </div>
          ) : q ? (
            <div className="py-24 text-center text-meta text-ink-2">No matches</div>
          ) : (
            <div className="flex flex-col items-center py-24 text-center">
              <Sparkles size={32} strokeWidth={1.5} className="text-ink-3" aria-hidden />
              <div className="mt-3 text-section font-medium text-ink">Nothing here yet</div>
              <button
                type="button"
                onClick={() => askNori("")}
                className="t-fast mt-4 rounded-chip border border-dashed border-chip-border px-2.5 py-2 text-input text-ink transition-[background-color,border-color] hover:border-solid hover:border-accent-signal hover:bg-accent-subtle"
              >
                Ask Nori to make something
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function Viewer({ a }: { a: Artifact }) {
  const set = useNori((s) => s.set);
  const router = useRouter();
  const download = () => {
    const blob = a.src
      ? null
      : new Blob([a.body ?? a.title], { type: "text/markdown" });
    const href = a.src ?? URL.createObjectURL(blob!);
    const link = document.createElement("a");
    link.href = href;
    link.download = a.src ? a.src.split("/").pop()! : `${a.title}.md`;
    link.click();
    if (blob) setTimeout(() => URL.revokeObjectURL(href), 1000);
  };
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-16 shrink-0 items-center gap-3 border-b border-hairline px-5">
        <IconButton label="Back to Library" onClick={() => set({ viewerId: null })}>
          <ArrowLeft size={20} strokeWidth={1.5} />
        </IconButton>
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-section font-medium text-ink">{a.title}</h1>
          <div className="text-caption text-ink-3">
            {artifactTypeLabel[a.type]} · {shortDate(a.createdAt)}
          </div>
        </div>
        <Button
          onClick={() => {
            set({ replyTo: { threadId: useNori.getState().activeThreadId, artifactId: a.id }, composerFocus: Date.now() });
            router.push("/");
          }}
        >
          Discuss
        </Button>
        <Button variant="quiet" onClick={download}>
          <Download size={16} strokeWidth={1.5} aria-hidden />
          Download
        </Button>
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild>
            <IconButton label="More">
              <Ellipsis size={18} strokeWidth={1.5} />
            </IconButton>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content align="end" sideOffset={6} className="menu z-[60] min-w-44 rounded-card bg-surface p-1.5 shadow-pop">
              <DropdownMenu.Item
                className={cn(menuItem, "text-danger data-[highlighted]:bg-danger-bg")}
                onSelect={() => {
                  useNori.setState((s) => ({ artifacts: s.artifacts.filter((x) => x.id !== a.id), viewerId: null }));
                }}
              >
                <Trash2 size={16} strokeWidth={1.5} /> Delete
              </DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Portal>
        </DropdownMenu.Root>
      </header>
      <div className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto">
        {a.src ? (
          <div className="grid min-h-full place-items-center p-10">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src={a.src} alt={a.title} className="max-h-[72vh] max-w-full rounded-card shadow-float" />
          </div>
        ) : a.type === "audio" ? (
          <div className="grid min-h-full place-items-center p-10">
            <div className="flex items-center gap-4 rounded-full bg-bubble px-5 py-3">
              <Waveform bars={waveform(11, 64)} progress={0} height={32} />
              <span className="font-mono text-mono-data text-ink-2">{duration(a.durationMs ?? 0)}</span>
            </div>
          </div>
        ) : a.body ? (
          <div className="mx-auto max-w-[856px] px-12 pt-10 pb-24">
            <div className="max-w-[68ch]">
              <Markdown variant="page">{a.body}</Markdown>
            </div>
          </div>
        ) : (
          <div className="grid min-h-full place-items-center p-10">
            <div className="w-72 overflow-hidden rounded-card shadow-[inset_0_0_0_1px_var(--hairline)]">
              <Thumbnail artifact={a} />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export default function LibraryPage() {
  const viewer = useNori((s) => s.artifacts.find((a) => a.id === s.viewerId));
  return viewer ? <Viewer a={viewer} /> : <Grid />;
}
