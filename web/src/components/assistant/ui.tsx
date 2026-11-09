"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import type { ReactNode } from "react";
import { AlertDialog } from "radix-ui";
import { ArrowLeft, ChevronRight, RefreshCw } from "lucide-react";
import { Medallion } from "@/components/mark/NoriMark";
import { Button, IconButton } from "@/components/ui/primitives";
import { cn } from "@/lib/utils";

/** A screen: an optional back link, the title, a short line from Nori, then the content. */
export function Screen({ title, line, back, actions, children, wide }: {
  title: string; line?: ReactNode; back?: string; actions?: ReactNode; children: ReactNode; wide?: boolean;
}) {
  const router = useRouter();
  return (
    <main className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto" aria-label={title}>
      <div className={cn("nori-screen mx-auto w-full px-10 pt-8 pb-16", wide ? "max-w-[1080px]" : "max-w-[880px]")}>
        <header className="mb-6">
          {back && <button type="button" onClick={() => (window.history.length > 1 ? router.back() : router.push(back))} className="mb-3 -ml-1 inline-flex items-center gap-1 rounded-full px-1 py-0.5 text-meta text-ink-2 hover:text-ink"><ArrowLeft size={15} aria-hidden />Back</button>}
          <div className="flex items-start gap-3">
            <h1 className="min-w-0 flex-1 text-[26px] leading-8 font-semibold tracking-[-0.01em] text-ink">{title}</h1>
            {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
          </div>
          {line && <NoriLine className="mt-3">{line}</NoriLine>}
        </header>
        {children}
      </div>
    </main>
  );
}

export function NoriLine({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <p className={cn("flex items-center gap-2.5 text-body text-ink-2", className)} role="status">
      <Medallion size={26} />
      <span className="min-w-0">{children}</span>
    </p>
  );
}

export function Section({ title, children, actions, className }: { title?: string; children: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <section className={cn("mt-8 first:mt-0", className)} aria-label={title}>
      {(title || actions) && <div className="mb-2 flex items-center gap-2">
        {title && <h2 className="flex-1 text-section font-semibold text-ink">{title}</h2>}
        {actions}
      </div>}
      {children}
    </section>
  );
}

/** A rounded group of rows. */
export function Rows({ children, label }: { children: ReactNode; label?: string }) {
  return <ul aria-label={label} className="divide-y divide-hairline overflow-hidden rounded-card border border-hairline bg-surface">{children}</ul>;
}

export function Row({ href, onClick, leading, title, detail, trailing, chevron = !!href, className }: {
  href?: string; onClick?: () => void; leading?: ReactNode; title: ReactNode; detail?: ReactNode; trailing?: ReactNode; chevron?: boolean; className?: string;
}) {
  const body = <>
    {leading && <span className="shrink-0">{leading}</span>}
    <span className="min-w-0 flex-1">
      <span className="block truncate text-row font-medium text-ink">{title}</span>
      {detail && <span className="mt-0.5 block text-meta text-ink-2 [overflow-wrap:anywhere]">{detail}</span>}
    </span>
    {trailing && <span className="flex shrink-0 items-center gap-2">{trailing}</span>}
    {chevron && <ChevronRight size={16} className="shrink-0 text-ink-3" aria-hidden />}
  </>;
  const cls = cn("flex min-h-14 w-full items-center gap-3 px-4 py-3 text-left", (href || onClick) && "hover:bg-fill-1 focus-visible:bg-fill-1", className);
  return <li>{href ? <Link href={href} className={cls}>{body}</Link> : onClick ? <button type="button" onClick={onClick} className={cls}>{body}</button> : <div className={cls}>{body}</div>}</li>;
}

const TONES = {
  neutral: "bg-fill-1 text-ink-2",
  accent: "bg-accent-subtle text-accent-text",
  warning: "bg-warning-bg text-warning",
  danger: "bg-danger-bg text-danger",
  ok: "bg-[#e9f8ef] text-[#1c7a43]",
} as const;
export type Tone = keyof typeof TONES;

export function Pill({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  return <span className={cn("inline-flex h-6 shrink-0 items-center whitespace-nowrap rounded-full px-2.5 text-caption font-medium", TONES[tone])}>{children}</span>;
}

export function Badge({ count, label }: { count?: number; label: string }) {
  if (!count) return null;
  return <span aria-label={`${count} ${label}`} className="inline-grid h-[18px] min-w-[18px] place-items-center rounded-full bg-accent px-1 text-[11px] leading-none font-semibold text-on-accent">{count > 99 ? "99+" : count}</span>;
}

/** Empty, loading and error states each name a real next step. */
export function Empty({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return <div className="rounded-card border border-dashed border-hairline-strong px-6 py-10 text-center text-body text-ink-2">
    <p>{children}</p>
    {action && <div className="mt-4 flex justify-center gap-2">{action}</div>}
  </div>;
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return <div role="status" aria-live="polite" className="space-y-2">
    <span className="sr-only">{label}</span>
    {[0, 1, 2].map((n) => <div key={n} className="h-14 animate-pulse rounded-card bg-fill-1 motion-reduce:animate-none" />)}
  </div>;
}

export function Failure({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const message = error instanceof Error ? error.message : String(error ?? "");
  return <div role="alert" className="flex items-center gap-3 rounded-card bg-danger-bg px-4 py-3 text-meta text-danger">
    <span className="min-w-0 flex-1">{message || "Something went wrong."}</span>
    {onRetry && <Button size="sm" variant="neutral" onClick={onRetry}><RefreshCw size={14} aria-hidden />Try again</Button>}
  </div>;
}

export function Notice({ tone = "neutral", children, action }: { tone?: Tone; children: ReactNode; action?: ReactNode }) {
  return <div role={tone === "danger" ? "alert" : "status"} className={cn("flex flex-wrap items-center gap-3 rounded-card px-4 py-3 text-meta", TONES[tone])}>
    <span className="min-w-0 flex-1">{children}</span>{action}
  </div>;
}

/** Shown in place of a screen whose route family the connected core does not offer yet. */
export function NotYet({ what }: { what: string }) {
  return <Empty>{what} isn’t available on this Nori yet.</Empty>;
}

export function Confirm({ trigger, title, body, confirm, onConfirm, danger }: {
  trigger: ReactNode; title: string; body?: ReactNode; confirm: string; onConfirm: () => void; danger?: boolean;
}) {
  return (
    <AlertDialog.Root>
      <AlertDialog.Trigger asChild>{trigger}</AlertDialog.Trigger>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <AlertDialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 rounded-sheet bg-surface p-6 shadow-pop">
          <AlertDialog.Title className="text-section font-semibold text-ink">{title}</AlertDialog.Title>
          {body ? <AlertDialog.Description asChild><div className="mt-2 text-body text-ink-2">{body}</div></AlertDialog.Description> : <AlertDialog.Description className="sr-only">{title}</AlertDialog.Description>}
          <div className="mt-6 flex justify-end gap-2">
            <AlertDialog.Cancel asChild><Button variant="quiet">Cancel</Button></AlertDialog.Cancel>
            <AlertDialog.Action asChild><Button variant="primary" className={danger ? "bg-danger hover:bg-danger active:bg-danger" : undefined} onClick={onConfirm}>{confirm}</Button></AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}

export function RefreshButton({ onClick, busy }: { onClick: () => void; busy?: boolean }) {
  return <IconButton label="Refresh" size={32} onClick={onClick} disabled={busy}><RefreshCw size={16} strokeWidth={1.5} className={busy ? "animate-spin motion-reduce:animate-none" : undefined} /></IconButton>;
}

export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: ReactNode }) {
  return <label className="block">
    <span className="mb-1.5 block text-meta font-medium text-ink">{label}</span>
    {children}
    {hint && <span className="mt-1 block text-caption text-ink-3">{hint}</span>}
  </label>;
}

export const inputClass = "w-full rounded-tile border border-hairline bg-surface px-3 py-2 text-input text-ink placeholder:text-ink-3 focus-visible:border-hairline-strong";

export function Tabs<T extends string>({ value, onChange, items, label }: { value: T; onChange: (value: T) => void; items: { id: T; label: string; count?: number }[]; label: string }) {
  return <div role="tablist" aria-label={label} className="scrollbar-quiet -mx-1 mb-4 flex gap-1 overflow-x-auto px-1">
    {items.map((item) => <button key={item.id} type="button" role="tab" aria-selected={value === item.id} onClick={() => onChange(item.id)}
      className={cn("inline-flex h-9 shrink-0 items-center gap-1.5 rounded-full px-3.5 text-meta font-medium", value === item.id ? "bg-ink text-surface" : "text-ink-2 hover:bg-fill-1 hover:text-ink")}>
      {item.label}{item.count ? <span className={cn("text-caption", value === item.id ? "text-surface/70" : "text-ink-3")}>{item.count}</span> : null}
    </button>)}
  </div>;
}
