"use client";

import { ArrowLeft, Download } from "lucide-react";
import { Dialog } from "radix-ui";
import { Markdown } from "@/components/chat/Markdown";
import { Button, IconButton } from "@/components/ui/primitives";
import { firstSentence } from "@/lib/utils";
import { shortDate } from "@/lib/time";

/** A reading view of a saved reply; the conversation remains its source of truth. */
export function DocumentViewer({
  document,
  onClose,
}: {
  document: { body: string; at: number } | null;
  onClose: () => void;
}) {
  const title =
    document?.body.match(/^#{1,3}\s+(.+)$/m)?.[1] ??
    firstSentence(document?.body ?? "").slice(0, 48);
  const download = () => {
    if (!document) return;
    const url = URL.createObjectURL(
      new Blob([document.body], { type: "text/markdown" }),
    );
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
        <Dialog.Overlay className="fixed inset-0 z-40 bg-overlay" />
        <Dialog.Content
          aria-describedby={undefined}
          className="fixed inset-0 z-50 flex flex-col bg-canvas text-ink focus-visible:outline-none"
        >
          <header className="flex h-16 shrink-0 items-center gap-3 border-b border-hairline px-5">
            <IconButton label="Back to chat" onClick={onClose}>
              <ArrowLeft size={20} strokeWidth={1.5} />
            </IconButton>
            <div className="min-w-0 flex-1">
              <Dialog.Title className="truncate text-section font-medium">
                {title}
              </Dialog.Title>
              <p className="text-caption text-ink-3">
                Saved reply · {document && shortDate(document.at)}
              </p>
            </div>
            <Button variant="quiet" onClick={download}>
              <Download size={16} strokeWidth={1.5} aria-hidden />
              Download
            </Button>
          </header>
          <div className="scrollbar-quiet min-h-0 flex-1 overflow-y-auto">
            <div className="mx-auto max-w-[856px] px-6 pt-10 pb-24 sm:px-12">
              <div className="max-w-[68ch]">
                <Markdown variant="page">{document?.body ?? ""}</Markdown>
              </div>
            </div>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
