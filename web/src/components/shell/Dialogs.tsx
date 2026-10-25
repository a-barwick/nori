"use client";

import { useState } from "react";
import { Check } from "lucide-react";
import { Button } from "@/components/ui/primitives";
import { Modal } from "@/components/ui/layers";
import { useNori } from "@/lib/store";

const SHORTCUTS: [string, string][] = [
  ["Search", "⌘K"],
  ["Keyboard shortcuts", "⌘/"],
  ["Settings", "⌘,"],
  ["Show or hide the agent panel", "⌘."],
  ["Chat, Search, Ideas, Goals, Library", "⌘1–5"],
  ["Edit your last message", "↑"],
  ["Close the top layer", "Esc"],
];

export function ShortcutsDialog() {
  const open = useNori((s) => s.shortcutsOpen);
  const set = useNori((s) => s.set);
  return (
    <Modal open={open} onOpenChange={(o) => set({ shortcutsOpen: o })} title="Keyboard shortcuts" width={460}>
      <ul>
        {SHORTCUTS.map(([label, keys]) => (
          <li key={label} className="flex items-center justify-between border-b border-hairline py-2.5 last:border-0">
            <span className="text-row-title text-ink">{label}</span>
            <kbd className="rounded-[6px] bg-fill-1 px-2 py-0.5 font-mono text-mono-data text-ink-2">{keys}</kbd>
          </li>
        ))}
      </ul>
      <p className="mt-3 text-caption text-ink-3">In a Chrome tab, Chrome keeps ⌘⇧N and ⌘1–5 for itself. Use Ctrl there instead.</p>
    </Modal>
  );
}

export function ReportDialog() {
  const open = useNori((s) => s.reportOpen);
  const set = useNori((s) => s.set);
  const [text, setText] = useState("");
  const [sent, setSent] = useState(false);
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        set({ reportOpen: o });
        if (!o) {
          setSent(false);
          setText("");
        }
      }}
      title="Report an issue"
    >
      {sent ? (
        <div className="flex items-center gap-2 py-6 text-row-title text-ink">
          <Check size={18} strokeWidth={1.5} className="text-accent-signal" aria-hidden />
          Thanks. It’s logged with this session.
        </div>
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (text.trim()) setSent(true);
          }}
        >
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={5}
            autoFocus
            aria-label="What went wrong?"
            placeholder="What went wrong?"
            className="w-full resize-none rounded-card bg-fill-1 p-3 text-input text-ink shadow-[inset_0_0_0_1px_var(--hairline)] placeholder:text-ink-3 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
          />
          <div className="mt-3 flex justify-end">
            <Button variant="primary" type="submit" disabled={!text.trim()}>
              Send
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
