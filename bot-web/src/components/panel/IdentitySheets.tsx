"use client";

import { useState } from "react";
import { Dialog } from "radix-ui";
import { ChevronRight, X } from "lucide-react";
import { Markdown } from "@/components/chat/Markdown";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button } from "@/components/ui/primitives";
import { Modal, Sheet } from "@/components/ui/layers";
import { compactDiff, diffLines } from "@/lib/diff";
import { useNori } from "@/lib/store";
import { monoDate, shortDate } from "@/lib/time";
import type { IdentityRevision, MarkLook } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Grain, IDENTITY_CARDS } from "./IdentityTab";

function Revision({ rev }: { rev: IdentityRevision }) {
  const [open, setOpen] = useState(false);
  const lines = compactDiff(diffLines(rev.before, rev.after));
  return (
    <li className="border-b border-hairline last:border-0">
      <button type="button" aria-expanded={open} onClick={() => setOpen((o) => !o)} className="flex w-full items-center gap-3 py-3 text-left">
        <span className="w-16 shrink-0 font-mono text-mono-data text-ink-3">{monoDate(rev.at)}</span>
        <span className="flex-1 text-row-title font-medium text-ink">{rev.note}</span>
        <ChevronRight size={16} strokeWidth={1.5} className={cn("t-fast text-ink-3 transition-transform", open && "rotate-90")} aria-hidden />
      </button>
      {open && (
        <div className="anim-fade mb-3 overflow-hidden rounded-tile shadow-[inset_0_0_0_1px_var(--hairline)]">
          {lines.map((l, i) => (
            <div
              key={i}
              className={cn(
                "flex gap-2 px-3 py-1 font-mono text-mono-data whitespace-pre-wrap",
                l.type === "add" && "bg-accent-subtle text-accent-text",
                l.type === "del" && "bg-danger-bg text-danger",
                l.type === "same" && "text-ink-3",
              )}
            >
              <span aria-hidden className="w-2 shrink-0">{l.type === "add" ? "+" : l.type === "del" ? "−" : ""}</span>
              <span className="sr-only">{l.type === "add" ? "Added:" : l.type === "del" ? "Removed:" : ""}</span>
              <span className={cn(l.type === "del" && "line-through decoration-1")}>{l.text || " "}</span>
            </div>
          ))}
        </div>
      )}
    </li>
  );
}

export function IdentitySheet() {
  const kind = useNori((s) => s.identitySheet);
  const set = useNori((s) => s.set);
  const [lastKind, setLastKind] = useState<"soul" | "memory">("soul");
  if (kind && kind !== lastKind) setLastKind(kind);
  const shown = kind ?? lastKind;
  return (
    <Sheet
      open={!!kind}
      onOpenChange={(o) => !o && set({ identitySheet: null })}
      title={IDENTITY_CARDS[shown].title}
      header={<SheetHeader kind={shown} />}
    >
      <IdentityBody key={shown} kind={shown} />
    </Sheet>
  );
}

function SheetHeader({ kind }: { kind: "soul" | "memory" }) {
  const card = IDENTITY_CARDS[kind];
  const doc = useNori((s) => s.identity[kind]);
  const Icon = card.icon;
  return (
    <div className="p-2">
      <div
        className="relative isolate overflow-hidden rounded-[16px] p-5 text-on-identity"
        style={{ background: `linear-gradient(140deg, ${card.from}, ${card.to})` }}
      >
        <Grain />
        <div className="relative flex items-start justify-between">
          <div>
            <div className="text-title font-medium uppercase">{card.title}</div>
            <div className="mt-1 font-mono text-mono-label text-on-identity/75 uppercase">{card.label}</div>
          </div>
          <Dialog.Close asChild>
            <button type="button" aria-label="Close" className="glass grid size-8 place-items-center rounded-full text-ink">
              <X size={16} strokeWidth={1.5} />
            </button>
          </Dialog.Close>
        </div>
        <div className="relative mt-10 flex items-end justify-between">
          <span className="font-mono text-mono-data">{monoDate(doc.updatedAt)}</span>
          <Icon size={20} strokeWidth={1.5} className="opacity-70" aria-hidden />
        </div>
      </div>
    </div>
  );
}

function IdentityBody({ kind }: { kind: "soul" | "memory" }) {
  const doc = useNori((s) => s.identity[kind]);
  const save = useNori((s) => s.saveIdentityDoc);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(doc.content);

  return (
    <div className="px-6 pt-3 pb-8">
      <div className="mb-2 flex items-center justify-between">
        <span className="text-caption text-ink-3">Updated {shortDate(doc.updatedAt)}</span>
        {!editing && (
          <Button
            size="sm"
            onClick={() => {
              setDraft(doc.content);
              setEditing(true);
            }}
          >
            Edit
          </Button>
        )}
      </div>
      {editing ? (
        <div>
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            aria-label={`Edit ${IDENTITY_CARDS[kind].title}`}
            className="scrollbar-quiet h-[380px] w-full resize-none rounded-card bg-canvas p-4 text-input text-ink shadow-[inset_0_0_0_1px_var(--hairline-strong)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
            autoFocus
          />
          <div className="mt-3 flex justify-end gap-2">
            <Button size="sm" variant="quiet" onClick={() => setEditing(false)}>
              Cancel
            </Button>
            <Button
              size="sm"
              variant="primary"
              onClick={() => {
                save(kind, draft, "Edited by you");
                setEditing(false);
              }}
            >
              Save
            </Button>
          </div>
        </div>
      ) : (
        <Markdown variant="page">{doc.content}</Markdown>
      )}

      <h3 className="mt-8 mb-1 text-section font-medium text-ink">History</h3>
      <ul>
        {doc.history.map((rev) => (
          <Revision key={rev.at} rev={rev} />
        ))}
      </ul>
    </div>
  );
}

const LOOKS: { key: MarkLook; label: string }[] = [
  { key: "onigiri", label: "Onigiri" },
  { key: "sprout", label: "Sprout" },
  { key: "pebble", label: "Pebble" },
];

export function EditLookDialog() {
  const open = useNori((s) => s.editLookOpen);
  const set = useNori((s) => s.set);
  return (
    <Modal open={open} onOpenChange={(o) => set({ editLookOpen: o })} title="Name & look" width={440}>
      {open && <EditLookForm onDone={() => set({ editLookOpen: false })} />}
    </Modal>
  );
}

function EditLookForm({ onDone }: { onDone: () => void }) {
  const identity = useNori((s) => s.identity);
  const save = useNori((s) => s.setNameAndLook);
  const [name, setName] = useState(identity.name);
  const [look, setLook] = useState<MarkLook>(identity.look);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save(name, look);
        onDone();
      }}
    >
      <input
        value={name}
        onChange={(e) => setName(e.target.value)}
        aria-label="Name"
        maxLength={24}
        className="h-11 w-full rounded-full bg-fill-1 px-4 text-input text-ink shadow-[inset_0_0_0_1px_var(--hairline)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
      />
      <div role="radiogroup" aria-label="Look" className="mt-4 grid grid-cols-3 gap-2">
        {LOOKS.map((l) => (
          <button
            key={l.key}
            type="button"
            role="radio"
            aria-checked={look === l.key}
            onClick={() => setLook(l.key)}
            className={cn(
              "t-fast flex flex-col items-center gap-2 rounded-card py-4 transition-colors",
              look === l.key ? "bg-accent-subtle shadow-[inset_0_0_0_1.5px_var(--accent-signal)]" : "hover:bg-fill-1 shadow-[inset_0_0_0_1px_var(--hairline)]",
            )}
          >
            <NoriMark size={48} look={l.key} state="idle" />
            <span className="text-meta text-ink">{l.label}</span>
          </button>
        ))}
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="quiet" onClick={onDone}>
          Cancel
        </Button>
        <Button variant="primary" type="submit">
          Save
        </Button>
      </div>
    </form>
  );
}

