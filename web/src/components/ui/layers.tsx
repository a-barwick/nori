"use client";

import { Dialog } from "radix-ui";
import { X } from "lucide-react";
import { IconButton } from "./primitives";
import { cn } from "@/lib/utils";

/** Opening a layer focuses the layer itself, not its first button (whose tooltip would pop). */
export function focusSelf(e: Event) {
  e.preventDefault();
  (e.currentTarget as HTMLElement | null)?.focus();
}

/** Right sheet: springs in, warm scrim, Esc closes (topmost first). */
export function Sheet({
  open,
  onOpenChange,
  title,
  width = 480,
  header,
  children,
  className,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  width?: number;
  header?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <Dialog.Content
          aria-describedby={undefined}
          onOpenAutoFocus={focusSelf}
          className={cn("sheet-right fixed inset-y-2 right-2 z-50 flex flex-col overflow-hidden rounded-sheet bg-surface shadow-pop", className)}
          style={{ width: `min(${width}px, calc(100vw - 16px))` }}
        >
          {header ?? (
            <div className="flex items-center justify-between px-6 pt-5 pb-2">
              <Dialog.Title className="text-title font-medium text-ink">{title}</Dialog.Title>
              <Dialog.Close asChild>
                <IconButton label="Close" size={32} tip={false}>
                  <X size={18} strokeWidth={1.5} />
                </IconButton>
              </Dialog.Close>
            </div>
          )}
          {header && <Dialog.Title className="sr-only">{title}</Dialog.Title>}
          <div className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export function Modal({
  open,
  onOpenChange,
  title,
  children,
  width = 480,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: React.ReactNode;
  width?: number;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-50 bg-overlay" />
        <Dialog.Content
          aria-describedby={undefined}
          onOpenAutoFocus={focusSelf}
          className="palette fixed top-[18vh] left-1/2 z-50 -translate-x-1/2 rounded-sheet bg-surface p-6 shadow-pop"
          style={{ width: `min(${width}px, calc(100vw - 32px))` }}
        >
          <div className="mb-4 flex items-center justify-between">
            <Dialog.Title className="text-title font-medium text-ink">{title}</Dialog.Title>
            <Dialog.Close asChild>
              <IconButton label="Close" size={32} tip={false}>
                <X size={18} strokeWidth={1.5} />
              </IconButton>
            </Dialog.Close>
          </div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
