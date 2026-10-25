"use client";

import { useRouter } from "next/navigation";
import { DropdownMenu } from "radix-ui";
import { Ellipsis, MessageCircle, MinusCircle, Clock } from "lucide-react";
import { Emoji3D, IconButton } from "@/components/ui/primitives";
import { Page, PageTitle, Subtitle } from "@/components/ui/Page";
import { startIdea } from "@/lib/nori/engine";
import { useNori } from "@/lib/store";
import type { Idea, IdeaGroup } from "@/lib/types";

const GROUPS: IdeaGroup[] = ["For you", "Around the house", "Shopping"];

const menuItem =
  "flex h-9 cursor-default items-center gap-2.5 rounded-chip px-2.5 text-row-title text-ink outline-none data-[highlighted]:bg-fill-1";

function IdeaRow({ idea }: { idea: Idea }) {
  const router = useRouter();
  const dismiss = useNori((s) => s.dismissIdea);
  const start = () => {
    router.push("/");
    void startIdea(idea.id);
  };
  return (
    <li className="group/idea relative">
      <button
        type="button"
        onClick={start}
        className="t-fast -mx-3 flex w-[calc(100%+24px)] items-start gap-4 rounded-row px-3 py-3 pr-14 text-left transition-colors hover:bg-fill-1"
      >
        <Emoji3D name={idea.emoji} size={40} />
        <span className="min-w-0 flex-1">
          <span className="block text-row font-medium text-ink">{idea.title}</span>
          <span className="mt-0.5 line-clamp-3 block text-meta text-ink-2">{idea.description}</span>
        </span>
      </button>
      <DropdownMenu.Root>
        <DropdownMenu.Trigger asChild>
          <IconButton label="More" size={32} className="absolute top-1/2 right-0 -translate-y-1/2">
            <Ellipsis size={18} strokeWidth={1.5} />
          </IconButton>
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content align="end" sideOffset={6} className="menu z-[60] min-w-48 rounded-card bg-surface p-1.5 shadow-pop">
            <DropdownMenu.Item className={menuItem} onSelect={start}>
              <MessageCircle size={16} strokeWidth={1.5} className="text-ink-2" /> Start in chat
            </DropdownMenu.Item>
            <DropdownMenu.Item className={menuItem} onSelect={() => dismiss(idea.id, "not-now")}>
              <Clock size={16} strokeWidth={1.5} className="text-ink-2" /> Not now
            </DropdownMenu.Item>
            <DropdownMenu.Item className={menuItem} onSelect={() => dismiss(idea.id, "fewer")}>
              <MinusCircle size={16} strokeWidth={1.5} className="text-ink-2" /> Fewer like this
            </DropdownMenu.Item>
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
    </li>
  );
}

export default function IdeasPage() {
  const ideas = useNori((s) => s.ideas);
  return (
    <Page>
      <PageTitle>Ideas</PageTitle>
      <Subtitle>I keep a running list of ways I could help. My favorites land here.</Subtitle>
      {GROUPS.map((group, i) => {
        const rows = ideas.filter((idea) => idea.group === group && !idea.dismissed);
        if (!rows.length) return null;
        return (
          <section key={group} className={i === 0 ? "mt-8" : "mt-7"}>
            <h2 className="mb-1 text-section font-medium text-ink">{group}</h2>
            <ul>
              {rows.map((idea) => (
                <IdeaRow key={idea.id} idea={idea} />
              ))}
            </ul>
          </section>
        );
      })}
    </Page>
  );
}
