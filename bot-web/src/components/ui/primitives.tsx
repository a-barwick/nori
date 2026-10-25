"use client";

import { forwardRef } from "react";
import { Tooltip } from "radix-ui";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

export function Tip({
  label,
  shortcut,
  side = "top",
  children,
  disabled,
}: {
  label: string;
  shortcut?: string;
  side?: "top" | "right" | "bottom" | "left";
  children: React.ReactNode;
  disabled?: boolean;
}) {
  if (disabled) return <>{children}</>;
  return (
    <Tooltip.Root>
      <Tooltip.Trigger asChild>{children}</Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Content
          side={side}
          sideOffset={8}
          className="tooltip z-[80] flex items-center gap-2 rounded-full bg-ink px-2.5 py-1 text-caption text-surface shadow-pop"
        >
          {label}
          {shortcut && <span className="font-mono text-mono-data opacity-70">{shortcut}</span>}
        </Tooltip.Content>
      </Tooltip.Portal>
    </Tooltip.Root>
  );
}

type IconButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  label: string;
  shortcut?: string;
  tipSide?: "top" | "right" | "bottom" | "left";
  size?: 28 | 32 | 36 | 44;
  active?: boolean;
  tip?: boolean;
};

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, shortcut, tipSide = "top", size = 36, active, tip = true, className, children, ...rest },
  ref,
) {
  const button = (
    <button
      ref={ref}
      type="button"
      aria-label={label}
      className={cn(
        "t-fast inline-grid shrink-0 place-items-center rounded-full transition-[background-color,color,opacity]",
        active ? "bg-fill-2 text-ink" : "text-ink-2 hover:bg-fill-1 hover:text-ink",
        className,
      )}
      style={{ width: size, height: size }}
      {...rest}
    >
      {children}
    </button>
  );
  return tip ? (
    <Tip label={label} shortcut={shortcut} side={tipSide}>
      {button}
    </Tip>
  ) : (
    button
  );
});

type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "neutral" | "quiet";
  size?: "sm" | "md";
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "neutral", size = "md", className, type = "button", ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={cn(
        "t-fast inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-full font-medium transition-[background-color,color,box-shadow] disabled:opacity-50",
        size === "md" ? "h-9 px-4 text-row-title" : "h-8 px-3 text-meta",
        variant === "primary" && "bg-accent text-on-accent hover:bg-accent-hover active:bg-accent-pressed",
        variant === "neutral" &&
          "bg-surface text-ink shadow-[inset_0_0_0_1px_var(--hairline-strong)] hover:bg-fill-1 active:bg-fill-2",
        variant === "quiet" && "text-ink-2 hover:bg-fill-1 hover:text-ink",
        className,
      )}
      {...rest}
    />
  );
});

export function Kbd({ children }: { children: React.ReactNode }) {
  return <span className="font-mono text-mono-data text-ink-3">{children}</span>;
}

export function SectionHeader({ children, first, className }: { children: React.ReactNode; first?: boolean; className?: string }) {
  return (
    <h3 className={cn("text-section font-medium text-ink", first ? "mt-4" : "mt-6", "mb-1", className)}>{children}</h3>
  );
}

/** Emoji in UI copy is fine; this renders Fluent 3D emoji directly on the paper. */
export function Emoji3D({ name, size = 40, className }: { name: string; size?: number; className?: string }) {
  return (
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={`/emoji/${name}.png`}
      alt=""
      width={size}
      height={size}
      draggable={false}
      className={cn("shrink-0 select-none", className)}
      style={{ filter: "var(--drop-contact)" }}
    />
  );
}

/** A panel tab with nothing in it yet: icon, tab name, one quiet line. */
export function EmptyTab({ icon: Icon, title, line }: { icon: LucideIcon; title: string; line: string }) {
  return (
    <div className="flex flex-col items-center py-12 text-center">
      <Icon size={32} strokeWidth={1.5} className="text-ink-3" aria-hidden />
      <div className="mt-3 text-section font-medium text-ink">{title}</div>
      <div className="mt-0.5 text-meta text-ink-2">{line}</div>
    </div>
  );
}
