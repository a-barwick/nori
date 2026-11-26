"use client";

import { forwardRef } from "react";
import { Tooltip } from "radix-ui";
import { cn } from "@/lib/utils";

function Tip({
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
          className="tooltip z-[80] flex items-center gap-2 rounded-full bg-toast-bg px-2.5 py-1 text-caption text-toast-fg shadow-pop"
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
        "t-fast inline-grid shrink-0 place-items-center rounded-full transition-[background-color,color,opacity] disabled:text-disabled",
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
  variant?: "primary" | "neutral" | "tonal" | "quiet" | "danger" | "danger-quiet";
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
        "t-fast inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-full font-medium transition-[background-color,color,box-shadow] disabled:bg-disabled-bg disabled:text-disabled disabled:shadow-none",
        size === "md" ? "h-9 px-4 text-row-title" : "h-8 px-3 text-meta",
        variant === "primary" && "bg-accent text-on-accent hover:bg-accent-hover active:bg-accent-pressed",
        variant === "neutral" &&
          "bg-surface text-ink shadow-[inset_0_0_0_1px_var(--hairline-strong)] hover:bg-fill-1 active:bg-fill-2",
        variant === "tonal" && "bg-fill-2 text-accent-text hover:bg-accent-muted",
        variant === "quiet" && "text-ink-2 hover:bg-fill-1 hover:text-ink disabled:bg-transparent",
        variant === "danger" && "bg-danger-bg text-danger hover:opacity-90",
        variant === "danger-quiet" && "text-danger hover:bg-danger-bg disabled:bg-transparent",
        className,
      )}
      {...rest}
    />
  );
});

export function SectionHeader({ children, first, className }: { children: React.ReactNode; first?: boolean; className?: string }) {
  return (
    <h3 className={cn("text-section font-medium text-ink", first ? "mt-4" : "mt-6", "mb-1", className)}>{children}</h3>
  );
}
