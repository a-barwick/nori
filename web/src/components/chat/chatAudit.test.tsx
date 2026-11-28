import assert from "node:assert/strict";
import { describe, test } from "node:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Markdown } from "@/components/chat/Markdown";
import { Parts } from "@/components/chat/Parts";
import { Thread } from "@/components/chat/Thread";
import { ReminderCard } from "@/components/assistant/ReminderCard";
import { RuntimeComposer } from "@/components/runtime/RuntimeComposer";
import { AssistantProvider } from "@/lib/assistant/context";
import { THINKING_WORDS } from "@/lib/thinkingWords";
import type { DraftFile, Part } from "@/lib/types";
import { documentTitle, resolveDocumentPresentation } from "@/lib/utils";

describe("Area A: Chat UI audit fixes (Web)", () => {
  test("resolveDocumentPresentation strips duplicate leading heading and preserves distinct subheadings", () => {
    const implicit = resolveDocumentPresentation("# Weekly Plan\n\n- Ship fixes\n- Run checks");
    assert.equal(implicit.title, "Weekly Plan");
    assert.equal(implicit.body, "- Ship fixes\n- Run checks");

    const matchingExplicit = resolveDocumentPresentation("# Weekly Plan\n\nBody copy", "Weekly Plan");
    assert.equal(matchingExplicit.title, "Weekly Plan");
    assert.equal(matchingExplicit.body, "Body copy");

    const distinctSubheading = resolveDocumentPresentation("## Preview\nDetails here.", "Sudden Impact");
    assert.equal(distinctSubheading.title, "Sudden Impact");
    assert.equal(distinctSubheading.body, "## Preview\nDetails here.");

    assert.equal(documentTitle(""), "Document");
    assert.equal(resolveDocumentPresentation("").title, "Document");
  });

  test("PageBlock strips duplicated title, uses tonal Open button, and omits hover hairline-strong", () => {
    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [
              {
                id: "m_page",
                sender: "nori",
                at: Date.now(),
                display: "page",
                text: "# Quarterly Summary\n\nKey metrics improved this quarter.",
              },
            ],
          }}
          emptyMessage="Ask Nori anything…"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );

    assert.equal(html.match(/Quarterly Summary/g)?.length, 2); // aria-label + header span only, not duplicated in body
    assert.match(html, /Key metrics improved this quarter\./);
    assert.equal(html.includes("hover:border-hairline-strong"), false);
    assert.equal(html.includes("Keep reading"), false);
    assert.match(html, /bg-fill-2 text-accent-text/); // Button variant="tonal"
  });

  test("Segmented cover + page card + trailing follow-up renders only one active Nori avatar", () => {
    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [
              {
                id: "m_split",
                sender: "nori",
                at: Date.now(),
                text: "",
                parts: [
                  { type: "text", text: "Here is the comparison:" },
                  { type: "page", display: "page", title: "Standing Desks", text: "| Desk | Price |" },
                  { type: "text", text: "Want me to check sale prices?" },
                ],
              },
            ],
          }}
          emptyMessage="Ask Nori anything…"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );

    assert.equal((html.match(/data-active-avatar="true"/g) ?? []).length, 1);
  });

  test("Thread streaming reply renders parsed Markdown with 26px active NoriMark", () => {
    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [{ id: "m1", sender: "nori", at: 1000, text: "Earlier reply" }],
          }}
          streamingText={"Here is a **bold** plan:\n\n- Step one\n- Step two"}
          emptyMessage="Ask Nori anything…"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );

    assert.match(html, /data-streaming-reply/);
    assert.match(html, /<strong[^>]*>bold<\/strong>/);
    assert.match(html, /<ul[^>]*>/);
    assert.equal((html.match(/data-active-avatar="true"/g) ?? []).length, 1);
    const streamingSlice = html.slice(html.indexOf("data-streaming-reply"));
    assert.match(streamingSlice, /width="26"/);
  });

  test("ErrorCard uses dark-mode safe border-card-border and bg-danger-bg without hardcoded rgba shadow", () => {
    const html = renderToStaticMarkup(
      <AssistantProvider localMode>
        <Thread
          conversation={{
            title: "Nori",
            items: [
              {
                id: "e1",
                sender: "nori",
                at: 2000,
                status: "failed",
                text: "Add a model key to continue.",
                error: { code: "missing_api_key", message: "Add a model key to continue.", retryable: false },
              },
            ],
          }}
          emptyMessage="Ask Nori anything…"
          hasOlder={false}
          loadingOlder={false}
          onLoadOlder={() => {}}
          onOpenDocument={() => {}}
          onUnread={() => {}}
        />
      </AssistantProvider>,
    );

    assert.match(html, /border-card-border/);
    assert.match(html, /bg-danger-bg/);
    assert.equal(html.includes("rgba(25,9,54"), false);
    assert.match(html, /Open Models/);
  });

  test("Parts hides raw action traces and Markdown CodeBlock flattens dark-mode styling", () => {
    const parts: Part[] = [
      { type: "action", name: "web_search", text: "raw debug payload" },
      { type: "note", text: "Saved to library" },
    ];
    const partsHtml = renderToStaticMarkup(<Parts parts={parts} />);
    assert.equal(partsHtml.includes("<details"), false);
    assert.equal(partsHtml.includes("raw debug payload"), false);
    assert.match(partsHtml, /Saved to library/);

    const codeHtml = renderToStaticMarkup(<Markdown variant="page">{"```ts\nconst x = 1;\n```"}</Markdown>);
    assert.match(codeHtml, /bg-fill-1/);
    assert.match(codeHtml, /border-card-border/);
    assert.equal(codeHtml.includes("hairline-strong"), false);
  });

  test("RuntimeComposer removes hardcoded purple shadow and disabled:opacity hacks", () => {
    const errorFile: DraftFile = {
      id: "f1",
      name: "report.pdf",
      media_type: "application/pdf",
      size: 1024,
      file: new File(["x"], "report.pdf", { type: "application/pdf" }),
      status: "error",
      error: "Upload failed",
    };
    const html = renderToStaticMarkup(
      <RuntimeComposer
        draft=""
        setDraft={() => {}}
        disabled
        onSend={() => {}}
        unread={0}
        onJump={() => {}}
        files={[errorFile]}
        onAttach={() => {}}
        onRetryFile={() => {}}
        onRemoveFile={() => {}}
      />,
    );

    assert.equal(html.includes("rgba(80,0,255"), false);
    assert.equal(html.includes("disabled:opacity-"), false);
    assert.match(html, /disabled:bg-disabled-bg/);
    assert.match(html, /border-card-border/);
    assert.match(html, /aria-label="Retry report\.pdf"[^>]*class="[^"]*size-6/);

    const readOnlyHtml = renderToStaticMarkup(
      <RuntimeComposer
        draft=""
        setDraft={() => {}}
        disabled={false}
        readOnly
        onSend={() => {}}
        unread={0}
        onJump={() => {}}
        files={[]}
        onAttach={() => {}}
        onRetryFile={() => {}}
        onRemoveFile={() => {}}
      />,
    );
    assert.match(readOnlyHtml, /placeholder="Read-only"/);
    assert.match(readOnlyHtml, /<textarea[^>]*disabled=""/);
    assert.match(readOnlyHtml, /aria-label="Attach a file"[^>]*disabled=""/);
    assert.match(readOnlyHtml, /aria-label="Send"[^>]*disabled=""/);
  });

  test("ReminderCard uses danger-quiet Cancel button and has no redundant status pill", () => {
    const html = renderToStaticMarkup(
      <ReminderCard
        part={{
          type: "reminder",
          id: "auto_1",
          name: "Submit expense report",
          text: "Tomorrow at 9:00 AM",
        }}
      />,
    );

    assert.match(html, /text-danger hover:bg-danger-bg/);
    assert.equal(html.includes(">Cancelled<"), false);
    assert.equal(html.includes(">Done<"), false);
  });

  test("THINKING_WORDS includes Nori's playful thinking phrases", () => {
    assert.ok(THINKING_WORDS.length >= 30);
    assert.ok(THINKING_WORDS.includes("Norifying…"));
    assert.ok(THINKING_WORDS.includes("Rolling the nori…"));
    for (const word of THINKING_WORDS) {
      assert.ok(word.endsWith("…"), `Expected ellipsis on thinking word: ${word}`);
    }
  });
});
