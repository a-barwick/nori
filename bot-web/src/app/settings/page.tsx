"use client";

import { Minus, Plus } from "lucide-react";
import { Switch } from "radix-ui";
import { Medallion } from "@/components/mark/NoriMark";
import { Button, IconButton } from "@/components/ui/primitives";
import { Page, PageTitle } from "@/components/ui/Page";
import { cancelAll } from "@/lib/nori/engine";
import { useNori } from "@/lib/store";

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-6 border-b border-hairline py-4 last:border-0">
      <div className="min-w-0">
        <div className="text-row text-ink">{label}</div>
        {hint && <div className="text-meta text-ink-2">{hint}</div>}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}

const timeInput =
  "h-9 rounded-full bg-fill-1 px-3 font-mono text-mono-data text-ink shadow-[inset_0_0_0_1px_var(--hairline)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus";

export default function SettingsPage() {
  const settings = useNori((s) => s.settings);
  const identity = useNori((s) => s.identity);
  const update = useNori((s) => s.updateSettings);
  const set = useNori((s) => s.set);
  const reset = useNori((s) => s.resetDemo);

  return (
    <Page>
      <PageTitle>Settings</PageTitle>

      <section className="mt-8">
        <h2 className="mb-1 text-section font-medium text-ink">{identity.name}</h2>
        <Row label="Name & look">
          <div className="flex items-center gap-3">
            <Medallion size={36} />
            <Button size="sm" onClick={() => set({ editLookOpen: true })}>
              Edit
            </Button>
          </div>
        </Row>
      </section>

      <section className="mt-8">
        <h2 className="mb-1 text-section font-medium text-ink">Proactive messages</h2>
        <Row label={`${identity.name} can message me first`}>
          <Switch.Root
            checked={settings.proactive}
            onCheckedChange={(v) => update({ proactive: v })}
            aria-label="Allow proactive messages"
            className="t-fast relative h-6 w-10 rounded-full bg-fill-2 transition-colors data-[state=checked]:bg-accent"
          >
            <Switch.Thumb className="t-fast block size-5 translate-x-0.5 rounded-full bg-surface shadow-pill transition-transform data-[state=checked]:translate-x-[18px]" />
          </Switch.Root>
        </Row>
        <Row label="Quiet hours" hint="No messages unless something is urgent.">
          <div className="flex items-center gap-2 text-meta text-ink-2">
            <input type="time" aria-label="Quiet hours start" value={settings.quietStart} onChange={(e) => update({ quietStart: e.target.value })} className={timeInput} />
            to
            <input type="time" aria-label="Quiet hours end" value={settings.quietEnd} onChange={(e) => update({ quietEnd: e.target.value })} className={timeInput} />
          </div>
        </Row>
        <Row label="Daily limit" hint="Most unprompted messages in a day.">
          <div className="flex items-center gap-1">
            <IconButton label="Fewer" size={32} onClick={() => update({ dailyCap: Math.max(0, settings.dailyCap - 1) })}>
              <Minus size={16} strokeWidth={1.5} />
            </IconButton>
            <span className="w-6 text-center font-mono text-mono-data text-ink tabular-nums" aria-live="polite">
              {settings.dailyCap}
            </span>
            <IconButton label="More" size={32} onClick={() => update({ dailyCap: Math.min(12, settings.dailyCap + 1) })}>
              <Plus size={16} strokeWidth={1.5} />
            </IconButton>
          </div>
        </Row>
      </section>

      <section className="mt-8">
        <h2 className="mb-1 text-section font-medium text-ink">Data</h2>
        <Row label="Start fresh" hint="Clears the chat, panel, memory, and Library.">
          <Button
            size="sm"
            onClick={() => {
              cancelAll();
              reset();
            }}
          >
            Reset
          </Button>
        </Row>
      </section>
    </Page>
  );
}
