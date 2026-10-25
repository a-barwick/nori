"use client";

import { Dialog } from "radix-ui";
import { X } from "lucide-react";
import { useNori } from "@/lib/store";

export function Lightbox() {
  const lightbox = useNori((s) => s.lightbox);
  const set = useNori((s) => s.set);
  return (
    <Dialog.Root open={!!lightbox} onOpenChange={(o) => !o && set({ lightbox: null })}>
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-[70] bg-overlay backdrop-blur-sm" />
        <Dialog.Content aria-describedby={undefined} className="palette fixed inset-0 z-[70] grid place-items-center p-12" onClick={() => set({ lightbox: null })}>
          <Dialog.Title className="sr-only">{lightbox?.alt ?? "Image"}</Dialog.Title>
          {lightbox && (
            // eslint-disable-next-line @next/next/no-img-element
            <img
              src={lightbox.src}
              alt={lightbox.alt}
              className="max-h-full max-w-full rounded-card shadow-pop"
              onClick={(e) => e.stopPropagation()}
            />
          )}
          <Dialog.Close asChild>
            <button type="button" aria-label="Close" className="glass absolute top-5 right-5 grid size-9 place-items-center rounded-full text-ink">
              <X size={18} strokeWidth={1.5} />
            </button>
          </Dialog.Close>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
