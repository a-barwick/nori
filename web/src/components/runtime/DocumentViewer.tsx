"use client";

import { useState } from "react";
import { Check, Copy, Download, X } from "lucide-react";
import { Dialog } from "radix-ui";
import { Markdown } from "@/components/chat/Markdown";
import { Button, IconButton } from "@/components/ui/primitives";
import { documentTitle } from "@/lib/utils";
import { shortDate } from "@/lib/time";

export function DocumentViewer({
  document,
  onClose,
}: {
  document: { body: string; at: number } | null;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const title = documentTitle(document?.body ?? "");

  const download = () => {
    if (!document) return;
    const url = URL.createObjectURL(new Blob([document.body], { type: "text/markdown" }));
    const link = window.document.createElement("a");
    link.href = url;
    link.download = `${title.replace(/[^\p{L}\p{N} -]/gu, "").slice(0, 80) || "Nori document"}.md`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  return (
    <Dialog.Root
      open={!!document}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="scrim fixed inset-0 z-40 bg-overlay sm:top-[60px]" />
        <Dialog.Content
          aria-describedby={undefined}
          className="sheet-right fixed inset-y-0 right-0 z-50 flex w-[min(560px,100vw)] flex-col border-l border-hairline bg-sheet text-ink shadow-sheet focus-visible:outline-none sm:top-[60px]"
        >
          <header className="flex h-[54px] shrink-0 items-center gap-3 border-b border-hairline bg-wash-top px-5">
            <div className="min-w-0 flex-1">
              <Dialog.Title className="truncate text-row font-semibold text-ink">{title}</Dialog.Title>
              <p className="text-caption text-ink-3">Saved reply · {document && shortDate(document.at)}</p>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              <Button
                size="sm"
                variant="quiet"
                onClick={() => {
                  if (!document) return;
                  void navigator.clipboard?.writeText(document.body);
                  setCopied(true);
                  setTimeout(() => setCopied(false), 1400);
                }}
              >
                {copied ? <Check size={14} strokeWidth={1.8} /> : <Copy size={14} strokeWidth={1.8} />}
                {copied ? "Copied" : "Copy"}
              </Button>
              <IconButton size={32} label="Download Markdown" onClick={download}>
                <Download size={15} strokeWidth={1.8} />
              </IconButton>
              <IconButton size={32} label="Close document" onClick={onClose}>
                <X size={16} strokeWidth={1.8} />
              </IconButton>
            </div>
          </header>
          <div className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto px-6 py-6 sm:px-7">
            <div className="max-w-[68ch]">
              <Markdown variant="page">{document?.body ?? ""}</Markdown>
            </div>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
