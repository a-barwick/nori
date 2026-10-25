"use client";

import { useId } from "react";
import { Brain, Sparkle, type LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { useNori } from "@/lib/store";
import { monoDate } from "@/lib/time";

export function Grain() {
  const id = useId().replace(/:/g, "");
  return (
    <svg className="pointer-events-none absolute inset-0 size-full opacity-[0.07] mix-blend-overlay" aria-hidden>
      <filter id={`grain-${id}`}>
        <feTurbulence type="fractalNoise" baseFrequency="0.8" numOctaves="3" stitchTiles="stitch" />
        <feColorMatrix type="saturate" values="0" />
        <feComponentTransfer>
          <feFuncR type="linear" slope="3" intercept="-1" />
          <feFuncG type="linear" slope="3" intercept="-1" />
          <feFuncB type="linear" slope="3" intercept="-1" />
          <feFuncA type="linear" slope="0" intercept="1" />
        </feComponentTransfer>
      </filter>
      <rect width="100%" height="100%" filter={`url(#grain-${id})`} />
    </svg>
  );
}

export const IDENTITY_CARDS: Record<"soul" | "memory", { title: string; label: string; icon: LucideIcon; from: string; to: string }> = {
  soul: { title: "Soul", label: "Personality · rules", icon: Sparkle, from: "var(--soul-from)", to: "var(--soul-to)" },
  memory: { title: "Memory", label: "Private · handle gently", icon: Brain, from: "var(--memory-from)", to: "var(--memory-to)" },
};

function IdentityCard({ kind }: { kind: "soul" | "memory" }) {
  const doc = useNori((s) => s.identity[kind]);
  const set = useNori((s) => s.set);
  const card = IDENTITY_CARDS[kind];
  const Icon = card.icon;
  return (
    <button
      type="button"
      onClick={() => set({ identitySheet: kind })}
      aria-label={`${card.title}: open`}
      className="group relative isolate aspect-[1/1.1] overflow-hidden rounded-sheet p-4 text-left text-on-identity transition-[translate,box-shadow] duration-150 ease-standard hover:-translate-y-0.5 hover:shadow-float"
      style={{ background: `linear-gradient(140deg, ${card.from}, ${card.to})` }}
    >
      <Grain />
      <div className="relative">
        <div className="text-title font-medium uppercase">{card.title}</div>
        <div className="mt-1 font-mono text-mono-label text-on-identity/75 uppercase">{card.label}</div>
      </div>
      <div className="absolute bottom-4 left-4 font-mono text-mono-data">{monoDate(doc.updatedAt)}</div>
      <Icon size={20} strokeWidth={1.5} className="absolute right-4 bottom-4 opacity-70" aria-hidden />
    </button>
  );
}

export function IdentityTab() {
  const name = useNori((s) => s.identity.name);
  const set = useNori((s) => s.set);
  return (
    <div className="pt-4">
      <div className="rounded-sheet bg-surface p-4 shadow-[inset_0_0_0_1px_var(--hairline)]">
        <div className="text-section font-medium text-ink">{name}</div>
        <Button className="mt-3 w-full" onClick={() => set({ editLookOpen: true })}>
          Edit name & look
        </Button>
      </div>
      <div className="mt-3 grid grid-cols-2 gap-3">
        <IdentityCard kind="soul" />
        <IdentityCard kind="memory" />
      </div>
    </div>
  );
}
