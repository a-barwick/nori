"use client";

import { useMemo, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { Command } from "cmdk";
import { Dialog } from "radix-ui";
import { Clock, FileText, Lightbulb, MessageCircle, Search, Target, type LucideIcon } from "lucide-react";
import { excerptOf } from "@/components/chat/Message";
import { useNow } from "@/lib/hooks";
import { startIdea } from "@/lib/nori/engine";
import { artifactTypeLabel, useNori } from "@/lib/store";
import { relative } from "@/lib/time";
import { firstSentence } from "@/lib/utils";

interface Row {
  id: string;
  icon: LucideIcon;
  title: string;
  excerpt: string;
  at?: number;
  keywords: string;
  run: () => void;
}

export function SearchPalette() {
  const open = useNori((s) => s.searchOpen);
  const set = useNori((s) => s.set);
  return (
    <Dialog.Root open={open} onOpenChange={(o) => set({ searchOpen: o })}>
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <Dialog.Content
          aria-describedby={undefined}
          className="palette fixed top-[18vh] left-1/2 z-50 w-[min(560px,calc(100vw-32px))] -translate-x-1/2 overflow-hidden rounded-sheet bg-surface shadow-pop"
        >
          <Dialog.Title className="sr-only">Search</Dialog.Title>
          {open && <Palette />}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function Palette() {
  const s = useNori();
  const router = useRouter();
  const pathname = usePathname();
  const now = useNow();
  const [q, setQ] = useState("");

  const close = () => s.set({ searchOpen: false });
  const go = (path: string) => {
    close();
    if (pathname !== path) router.push(path);
  };

  const sections = useMemo(() => {
    // Single channel for now: side chats are hidden, so search only the main thread.
    const chats: Row[] = s.threads.filter((t) => t.kind === "main").flatMap((t) =>
      t.items
        .filter((i) => i.kind === "text" || i.kind === "voice")
        .map((i) => ({
          id: `chat-${i.id}`,
          icon: MessageCircle,
          title: t.kind === "main" ? s.identity.name : t.title,
          excerpt: `${i.sender === "me" ? "You: " : ""}${excerptOf(i, s.artifacts)}`,
          at: i.at,
          keywords: i.kind === "text" ? i.text : (i.transcript ?? ""),
          run: () => {
            s.set({ activeThreadId: t.id });
            go("/");
            setTimeout(() => useNori.getState().highlightItem(i.id), 250);
          },
        })),
    );
    const ideas: Row[] = s.ideas
      .filter((i) => !i.dismissed)
      .map((i) => ({
        id: `idea-${i.id}`,
        icon: Lightbulb,
        title: i.title,
        excerpt: i.description,
        keywords: `${i.group} ${i.description}`,
        run: () => {
          go("/");
          void startIdea(i.id);
        },
      }));
    const goals: Row[] = s.goals.map((g) => ({
      id: `goal-${g.id}`,
      icon: Target,
      title: g.title,
      excerpt: g.line,
      keywords: `${g.status} ${g.line}`,
      run: () => {
        go("/goals");
        s.set({ goalSheetId: g.id });
      },
    }));
    const library: Row[] = s.artifacts.map((a) => ({
      id: `art-${a.id}`,
      icon: FileText,
      title: a.title,
      excerpt: a.body ? firstSentence(a.body.replace(/^#+.*$/m, "")) : artifactTypeLabel[a.type],
      at: a.createdAt,
      keywords: `${artifactTypeLabel[a.type]} ${a.body ?? ""}`,
      run: () => {
        s.set({ viewerId: a.id, libraryFilter: "all" });
        go("/library");
      },
    }));
    const byRecent = [...chats].sort((a, b) => (b.at ?? 0) - (a.at ?? 0));
    const recents: Row[] = byRecent.slice(0, 3).map((r) => ({ ...r, id: `recent-${r.id}`, icon: Clock }));
    const latestPerThread = byRecent.filter((r, i) => byRecent.findIndex((x) => x.title === r.title) === i);
    return [
      { label: "Recents", rows: q ? [] : recents },
      { label: "Chats", rows: q ? chats : latestPerThread.filter((r) => !recents.some((x) => x.id === `recent-${r.id}`)) },
      { label: "Ideas", rows: q ? ideas : ideas.slice(0, 3) },
      { label: "Goals", rows: q ? goals : goals.slice(0, 3) },
      { label: "Library", rows: q ? library : library.slice(0, 3) },
    ];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [s.threads, s.ideas, s.goals, s.artifacts, q]);

  return (
    <Command label="Search" loop className="flex max-h-[min(560px,64vh)] flex-col">
      <div className="flex items-center gap-3 border-b border-hairline px-5">
        <Search size={20} strokeWidth={1.5} className="shrink-0 text-ink-3" aria-hidden />
        <Command.Input
          value={q}
          onValueChange={setQ}
          autoFocus
          placeholder="Search chats, ideas, goals, and Library"
          className="h-14 min-w-0 flex-1 bg-transparent text-search text-ink placeholder:text-ink-3 focus-visible:outline-none"
        />
      </div>
      <Command.List className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto p-2">
        <Command.Empty className="py-10 text-center text-meta text-ink-2">No results</Command.Empty>
        {sections.map(
          (sec) =>
            sec.rows.length > 0 && (
              <Command.Group
                key={sec.label}
                heading={sec.label}
                className="[&_[cmdk-group-heading]]:px-3 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-caption [&_[cmdk-group-heading]]:text-ink-3"
              >
                {sec.rows.map((r) => (
                  <Command.Item
                    key={r.id}
                    value={r.id}
                    keywords={[r.title, r.excerpt, r.keywords]}
                    onSelect={r.run}
                    className="flex cursor-default items-center gap-3 rounded-chip px-3 py-2 data-[selected=true]:bg-fill-1"
                  >
                    <r.icon size={18} strokeWidth={1.5} className="shrink-0 text-ink-2" aria-hidden />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-row-title font-medium text-ink">{r.title}</span>
                      <span className="block truncate text-meta text-ink-2">{r.excerpt}</span>
                    </span>
                    {r.at && <span className="shrink-0 text-caption text-ink-3">{relative(r.at, now)}</span>}
                  </Command.Item>
                ))}
              </Command.Group>
            ),
        )}
      </Command.List>
    </Command>
  );
}
