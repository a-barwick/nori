"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import type { ReactNode } from "react";
import { AlertDialog } from "radix-ui";
import { ArrowLeft, ChevronRight, RefreshCw } from "lucide-react";
import { NoriMark } from "@/components/mark/NoriMark";
import { Button, IconButton } from "@/components/ui/primitives";
import { isUnavailableError } from "@/lib/assistant/api";
import { useAssistantConfig } from "@/lib/assistant/context";
import { cn } from "@/lib/utils";

export function Screen({
  title,
  subtitle,
  line,
  back,
  backLabel = "Back",
  actions,
  tabs,
  hideHeaderTitle = false,
  children,
}: {
  title: string;
  subtitle?: ReactNode;
  line?: ReactNode;
  back?: string;
  backLabel?: string;
  actions?: ReactNode;
  tabs?: ReactNode;
  hideHeaderTitle?: boolean;
  children: ReactNode;
}) {
  const router = useRouter();
  const showHeader = !hideHeaderTitle || !!tabs || !!back || !!actions || !!line;
  return (
    <main className="nori-screen flex min-h-0 flex-1 flex-col overflow-clip" aria-label={title}>
      {hideHeaderTitle && <h1 className="sr-only">{title}</h1>}
      {showHeader && (
        <header className={cn("shrink-0", hideHeaderTitle ? "bg-canvas" : "nori-top-wash", !tabs && "border-b border-hairline")}>
          <div
            className={cn(
              "nori-page-col",
              hideHeaderTitle ? "pt-4 pb-0 sm:pt-5" : "pt-5 sm:pt-6",
              !hideHeaderTitle && (tabs ? "pb-0" : "pb-4"),
            )}
          >
            {back && (
              <button
                type="button"
                onClick={() => router.push(back)}
                className="mb-3 inline-flex h-8 items-center gap-1.5 rounded-full bg-fill-1 px-3 text-meta font-semibold text-ink transition-colors hover:bg-fill-2"
              >
                <ArrowLeft size={14} aria-hidden />
                {backLabel}
              </button>
            )}
            {!hideHeaderTitle && (
              <div className="flex flex-wrap items-baseline justify-between gap-3">
                <h1 className="min-w-0 text-[26px] leading-8 font-bold tracking-[-0.025em] text-ink sm:text-[28px]">
                  {title}
                </h1>
                {(subtitle || actions) && (
                  <div className="flex shrink-0 flex-wrap items-center gap-2.5">
                    {subtitle && <span className="text-meta text-ink-2">{subtitle}</span>}
                    {actions}
                  </div>
                )}
              </div>
            )}
            {line && <NoriLine className="mt-2.5">{line}</NoriLine>}
            {tabs && <div className={hideHeaderTitle ? "" : "mt-3"}>{tabs}</div>}
          </div>
        </header>
      )}
      <div className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto">
        <div className={cn("nori-page-col pb-16", hideHeaderTitle && !showHeader ? "pt-6 sm:pt-7" : "pt-5")}>
          {children}
        </div>
      </div>
    </main>
  );
}

export function NoriLine({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <p className={cn("flex items-center gap-2.5 text-meta text-ink-2", className)} role="status">
      <NoriMark size={19} state="idle" />
      <span className="min-w-0">{children}</span>
    </p>
  );
}


export function Section({
  title,
  children,
  actions,
  className,
}: {
  title?: string;
  children: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("mt-6 first:mt-0", className)} aria-label={title}>
      {(title || actions) && (
        <div className="mb-2 flex items-center justify-between gap-2">
          {title && <h2 className="text-[13px] font-bold tracking-[-0.005em] text-ink-2">{title}</h2>}
          {actions}
        </div>
      )}
      {children}
    </section>
  );
}

export function Rows({ children, label }: { children: ReactNode; label?: string }) {
  return (
    <ul
      aria-label={label}
      className="divide-y divide-hairline overflow-hidden rounded-card border border-card-border bg-surface"
    >
      {children}
    </ul>
  );
}

export function Row({
  href,
  onClick,
  leading,
  title,
  detail,
  trailing,
  chevron = !!href,
  className,
  wrapTitle,
}: {
  href?: string;
  onClick?: () => void;
  leading?: ReactNode;
  title: ReactNode;
  detail?: ReactNode;
  trailing?: ReactNode;
  chevron?: boolean;
  className?: string;
  wrapTitle?: boolean;
}) {
  const body = (
    <>
      {leading && <span className="shrink-0">{leading}</span>}
      <span className="min-w-0 flex-1">
        <span className={cn("block text-row font-semibold text-ink", wrapTitle ? "[overflow-wrap:anywhere]" : "truncate")}>
          {title}
        </span>
        {detail && <span className="mt-0.5 block text-meta text-ink-2 [overflow-wrap:anywhere]">{detail}</span>}
      </span>
      {trailing && <span className="flex shrink-0 items-center gap-2">{trailing}</span>}
      {chevron && <ChevronRight size={15} className="shrink-0 text-ink-3" aria-hidden />}
    </>
  );
  const cls = cn(
    "flex min-h-14 w-full items-center gap-3 px-4 py-3 text-left transition-colors",
    (href || onClick) && "hover:bg-fill-1 focus-visible:bg-fill-1",
    className,
  );
  return (
    <li>
      {href ? (
        <Link href={href} className={cls}>
          {body}
        </Link>
      ) : onClick ? (
        <button type="button" onClick={onClick} className={cls}>
          {body}
        </button>
      ) : (
        <div className={cls}>{body}</div>
      )}
    </li>
  );
}

const TONES = {
  neutral: "bg-inactive-bg text-inactive",
  accent: "bg-accent-subtle text-accent-text",
  working: "bg-working-bg text-working",
  warning: "bg-warning-bg text-warning",
  danger: "bg-danger-bg text-danger",
  scheduled: "bg-scheduled-bg text-scheduled",
  ok: "bg-ok-bg text-ok",
} as const;
export type Tone = keyof typeof TONES;

export function Pill({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  return (
    <span
      className={cn(
        "inline-flex h-[23px] shrink-0 items-center whitespace-nowrap rounded-full px-2.5 text-[11.5px] font-semibold",
        TONES[tone],
      )}
    >
      {children}
    </span>
  );
}

export function Badge({ count, label }: { count?: number; label: string }) {
  if (!count) return null;
  return (
    <span
      aria-label={`${count} ${label}`}
      className="inline-grid h-[19px] min-w-[19px] place-items-center rounded-full bg-accent px-1.5 text-[11px] leading-none font-bold tabular-nums text-on-accent"
    >
      {count > 99 ? "99+" : count}
    </span>
  );
}

export function Empty({
  title,
  children,
  action,
}: {
  title?: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center rounded-card border border-card-border bg-surface px-6 py-10 text-center">
      <NoriMark size={38} state="idle" />
      {title && <p className="mt-3 text-section font-semibold text-ink">{title}</p>}
      <p className={cn("max-w-md text-meta text-ink-2", title ? "mt-1" : "mt-3")}>{children}</p>
      {action && <div className="mt-4 flex justify-center gap-2">{action}</div>}
    </div>
  );
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div role="status" aria-live="polite" className="space-y-2.5">
      <span className="sr-only">{label}</span>
      {[0, 1, 2].map((n) => (
        <div key={n} className="h-14 animate-pulse rounded-card bg-fill-1 motion-reduce:animate-none" />
      ))}
    </div>
  );
}

export function Failure({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const message = error instanceof Error ? error.message : String(error ?? "");
  if (isUnavailableError(error)) {
    return (
      <div role="status" className="flex items-center gap-3 rounded-card bg-fill-1 px-4 py-3 text-meta text-ink-2">
        <span className="min-w-0 flex-1">Can&apos;t reach Nori right now.</span>
        {onRetry && (
          <Button size="sm" variant="neutral" onClick={onRetry}>
            <RefreshCw size={14} aria-hidden />
            Try again
          </Button>
        )}
      </div>
    );
  }
  return (
    <div role="alert" className="flex items-center gap-3 rounded-card bg-danger-bg px-4 py-3 text-meta text-danger">
      <span className="min-w-0 flex-1">{message || "Something went wrong."}</span>
      {onRetry && (
        <Button size="sm" variant="neutral" onClick={onRetry}>
          <RefreshCw size={14} aria-hidden />
          Try again
        </Button>
      )}
    </div>
  );
}

export function Notice({
  tone = "neutral",
  children,
  action,
}: {
  tone?: Tone;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div
      role={tone === "danger" ? "alert" : "status"}
      className={cn("flex flex-wrap items-center gap-3 rounded-tile px-3.5 py-2.5 text-meta", TONES[tone])}
    >
      <span className="min-w-0 flex-1">{children}</span>
      {action}
    </div>
  );
}

export function NotYet({ what }: { what: string }) {
  const { ready } = useAssistantConfig();
  if (!ready) return <Loading />;
  return <Empty>{what} isn’t available on this Nori yet.</Empty>;
}

export function Confirm({
  trigger,
  title,
  body,
  confirm,
  onConfirm,
  danger,
}: {
  trigger: ReactNode;
  title: string;
  body?: ReactNode;
  confirm: string;
  onConfirm: () => void;
  danger?: boolean;
}) {
  return (
    <AlertDialog.Root>
      <AlertDialog.Trigger asChild>{trigger}</AlertDialog.Trigger>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <AlertDialog.Content className="anim-pop fixed top-1/2 left-1/2 z-50 w-[min(440px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 rounded-sheet border border-card-border bg-sheet p-6 shadow-pop">
          <AlertDialog.Title className="text-section font-semibold text-ink">{title}</AlertDialog.Title>
          {body ? (
            <AlertDialog.Description asChild>
              <div className="mt-2 text-body text-ink-2">{body}</div>
            </AlertDialog.Description>
          ) : (
            <AlertDialog.Description className="sr-only">{title}</AlertDialog.Description>
          )}
          <div className="mt-6 flex justify-end gap-2">
            <AlertDialog.Cancel asChild>
              <Button variant="quiet">Cancel</Button>
            </AlertDialog.Cancel>
            <AlertDialog.Action asChild>
              <Button
                variant="primary"
                className={danger ? "bg-danger! hover:bg-danger! active:bg-danger!" : undefined}
                onClick={onConfirm}
              >
                {confirm}
              </Button>
            </AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}

export function RefreshButton({ onClick, busy }: { onClick: () => void; busy?: boolean }) {
  return (
    <IconButton label="Refresh" size={32} onClick={onClick} disabled={busy}>
      <RefreshCw size={15} strokeWidth={1.7} className={busy ? "animate-spin motion-reduce:animate-none" : undefined} />
    </IconButton>
  );
}

export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-meta font-medium text-ink">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-caption text-ink-3">{hint}</span>}
    </label>
  );
}

export const inputBase =
  "rounded-tile border border-card-border bg-fill-1 px-3 py-2 text-input text-ink placeholder:text-ink-3 focus-visible:border-accent-signal focus-visible:bg-surface";
export const inputClass = `w-full ${inputBase}`;

export function Switch({
  checked,
  onCheckedChange,
  disabled,
  label,
}: {
  checked: boolean;
  onCheckedChange: (next: boolean) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onCheckedChange(!checked)}
      className={cn(
        "relative inline-flex h-[25px] w-[42px] shrink-0 items-center rounded-full transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-signal disabled:cursor-not-allowed disabled:opacity-50",
        checked ? "bg-accent" : "bg-fill-2",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "pointer-events-none block size-[19px] rounded-full bg-white shadow-xs transition-transform",
          checked ? "translate-x-[20px]" : "translate-x-[3px]",
        )}
      />
    </button>
  );
}

export function UnderlineTabs<T extends string>({
  value,
  onChange,
  items,
  label,
}: {
  value: T;
  onChange: (value: T) => void;
  items: { id: T; label: string; count?: number }[];
  label: string;
}) {
  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const index = items.findIndex((item) => item.id === value);
    const next =
      event.key === "ArrowRight"
        ? index + 1
        : event.key === "ArrowLeft"
          ? index - 1
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? items.length - 1
              : -2;
    if (next === -2) return;
    event.preventDefault();
    const target = items[(next + items.length) % items.length];
    onChange(target.id);
    requestAnimationFrame(() =>
      event.currentTarget?.querySelector<HTMLElement>(`[data-tab="${target.id}"]`)?.focus(),
    );
  };
  return (
    <div
      role="tablist"
      aria-label={label}
      onKeyDown={onKeyDown}
      className="scrollbar-quiet flex items-stretch gap-1 overflow-x-auto border-b border-hairline"
    >
      {items.map((item) => {
        const active = value === item.id;
        return (
          <button
            key={item.id}
            data-tab={item.id}
            tabIndex={active ? 0 : -1}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(item.id)}
            className={cn(
              "relative inline-flex shrink-0 items-center gap-1.5 px-3.5 pt-3 pb-2.5 text-[13.5px] transition-colors",
              active ? "font-semibold text-ink" : "font-medium text-ink-2 hover:text-ink",
            )}
          >
            <span>{item.label}</span>
            {item.count ? (
              <span
                className={cn(
                  "inline-flex min-w-[18px] items-center justify-center rounded-full bg-accent-subtle px-1.5 py-0.5 text-[11px] leading-none font-semibold tabular-nums",
                  active ? "text-accent-text" : "text-ink-2",
                )}
              >
                {item.count > 99 ? "99+" : item.count}
              </span>
            ) : null}
            {active && (
              <span
                aria-hidden
                className="absolute right-1.5 bottom-0 left-1.5 h-[2.5px] rounded-full bg-accent-text"
              />
            )}
          </button>
        );
      })}
    </div>
  );
}

export function Tabs<T extends string>({
  value,
  onChange,
  items,
  label,
}: {
  value: T;
  onChange: (value: T) => void;
  items: { id: T; label: string; count?: number }[];
  label: string;
}) {
  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const index = items.findIndex((item) => item.id === value);
    const next =
      event.key === "ArrowRight"
        ? index + 1
        : event.key === "ArrowLeft"
          ? index - 1
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? items.length - 1
              : -2;
    if (next === -2) return;
    event.preventDefault();
    const target = items[(next + items.length) % items.length];
    onChange(target.id);
    requestAnimationFrame(() =>
      event.currentTarget?.querySelector<HTMLElement>(`[data-tab="${target.id}"]`)?.focus(),
    );
  };
  return (
    <div
      role="tablist"
      aria-label={label}
      onKeyDown={onKeyDown}
      className="scrollbar-quiet -mx-1 mb-4 flex gap-1.5 overflow-x-auto px-1"
    >
      {items.map((item) => (
        <button
          key={item.id}
          data-tab={item.id}
          tabIndex={value === item.id ? 0 : -1}
          type="button"
          role="tab"
          aria-selected={value === item.id}
          onClick={() => onChange(item.id)}
          className={cn(
            "inline-flex h-8 shrink-0 items-center gap-1.5 rounded-full border px-3.5 text-meta font-semibold transition-colors",
            value === item.id
              ? "border-chip-selected-border bg-chip-selected-bg text-chip-selected-fg"
              : "border-transparent bg-fill-1 text-ink-2 hover:bg-fill-2 hover:text-ink",
          )}
        >
          {item.label}
          {item.count ? (
            <span className={cn("text-caption", value === item.id ? "text-chip-selected-fg/80" : "text-ink-3")}>
              {item.count}
            </span>
          ) : null}
        </button>
      ))}
    </div>
  );
}
