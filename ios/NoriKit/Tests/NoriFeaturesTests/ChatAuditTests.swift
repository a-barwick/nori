import Foundation
import UniformTypeIdentifiers
import Testing
import NoriCore
@testable import NoriFeatures

@MainActor
@Suite("Area A: Chat UI Audit & Parity Tests")
struct ChatAuditTests {
    @Test("MarkdownBlocksView parses headings (including h4), ordered lists, nested indentation, blockquotes, tables, and code blocks")
    func parsesRichMarkdownBlocks() {
        let md = """
        #### Deep Heading

        1. First step
          2. Nested step
        - Outer bullet
          - Inner bullet

        > Quoted line one
        > Quoted line two

        | Metric | Value |
        | --- | --- |
        | Latency | 120ms |
        | Errors | 0 |

        ```swift
        let answer = 42
        ```
        """

        let blocks = MarkdownBlocksView.parseBlocks(md)
        #expect(blocks.contains(.heading(level: 4, text: "Deep Heading")))
        #expect(blocks.contains(.ordered(indent: 0, marker: "1.", text: "First step")))
        #expect(blocks.contains(.ordered(indent: 1, marker: "2.", text: "Nested step")))
        #expect(blocks.contains(.bullet(indent: 0, text: "Outer bullet")))
        #expect(blocks.contains(.bullet(indent: 1, text: "Inner bullet")))
        #expect(blocks.contains(.blockquote("Quoted line one\nQuoted line two")))
        #expect(blocks.contains(.table(headers: ["Metric", "Value"], rows: [["Latency", "120ms"], ["Errors", "0"]])))
        #expect(blocks.contains(.code(language: "swift", code: "let answer = 42")))
    }

    @Test("MarkdownBlocksView detects block syntax accurately for NoriMessageBubble routing")
    func detectsBlockSyntax() {
        #expect(MarkdownBlocksView.containsBlockSyntax("Just a plain **bold** sentence.") == false)
        #expect(MarkdownBlocksView.containsBlockSyntax("Line one\nLine two") == false)
        #expect(MarkdownBlocksView.containsBlockSyntax("- Item one\n- Item two") == true)
        #expect(MarkdownBlocksView.containsBlockSyntax("1. First item") == true)
        #expect(MarkdownBlocksView.containsBlockSyntax("## Heading") == true)
        #expect(MarkdownBlocksView.containsBlockSyntax("> A quote") == true)
        #expect(MarkdownBlocksView.containsBlockSyntax("```\ncode\n```") == true)
        #expect(MarkdownBlocksView.containsBlockSyntax("| A | B |\n| --- | --- |\n| 1 | 2 |") == true)
    }

    @Test("MarkdownBlocksView strips duplicated leading title and falls back to Document")
    func resolvesDocumentPresentation() {
        let implicit = MarkdownBlocksView.resolveDocumentPresentation(
            body: "# Sprint Notes\n\nAll tasks shipped.",
            explicitTitle: nil
        )
        #expect(implicit.title == "Sprint Notes")
        #expect(implicit.body == "All tasks shipped.")

        let matching = MarkdownBlocksView.resolveDocumentPresentation(
            body: "# Sprint Notes\n\nAll tasks shipped.",
            explicitTitle: "Sprint Notes"
        )
        #expect(matching.title == "Sprint Notes")
        #expect(matching.body == "All tasks shipped.")

        let subheading = MarkdownBlocksView.resolveDocumentPresentation(
            body: "## Overview\nDetails.",
            explicitTitle: "Sprint Notes"
        )
        #expect(subheading.title == "Sprint Notes")
        #expect(subheading.body == "## Overview\nDetails.")

        let empty = MarkdownBlocksView.resolveDocumentPresentation(body: "", explicitTitle: nil)
        #expect(empty.title == "Document")
    }

    @Test("InlineNeedsYouCopy uses calm review copy without banned phrases")
    func inlineNeedsYouCopy() {
        let singleHeading = InlineNeedsYouCopy.heading(interaction: nil, totalPendingCount: 1)
        let multiHeading = InlineNeedsYouCopy.heading(interaction: nil, totalPendingCount: 3)
        #expect(singleHeading == "1 item waiting for review")
        #expect(multiHeading == "3 items waiting for review")
        #expect(!singleHeading.lowercased().contains("needs your"))
        #expect(!multiHeading.lowercased().contains("needs your"))

        let interaction = Interaction(
            id: "int-1",
            revision: 1,
            kind: .approval,
            status: .pending,
            prompt: "Send email?",
            payload: .approval(ApprovalPayload(
                summary: "Send email",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "conn-1", label: "alex@example.com")
            ))
        )
        #expect(InlineNeedsYouCopy.subtitle(interaction: interaction, totalPendingCount: 1) == "Action needed")
    }

    @Test("NoriThinkingWords includes Nori's playful thinking phrases")
    func playfulThinkingWords() {
        #expect(NoriThinkingWords.all.count == 35)
        #expect(NoriThinkingWords.all.contains("Norifying…"))
        #expect(NoriThinkingWords.all.contains("Rolling the nori…"))
        for word in NoriThinkingWords.all {
            #expect(word.hasSuffix("…"))
        }
    }

    @Test("ChatErrorCard handles both model_key_required and missing_api_key")
    func errorCardModelKeyCodes() {
        #expect(ChatErrorCard.isModelKeyError("model_key_required") == true)
        #expect(ChatErrorCard.isModelKeyError("missing_api_key") == true)
        #expect(ChatErrorCard.isModelKeyError("provider_overloaded") == false)
        #expect(ChatErrorCard.isModelKeyError(nil) == false)
    }

    @Test("NoriMessageBubble surfaces non-text parts while hiding action/text/reminder/page parts")
    func visibleNonTextPartsFiltering() {
        let message = Message(
            id: "m-1",
            role: .assistant,
            text: "Here are the resources.",
            parts: [
                MessagePart(id: "p1", type: "text", text: "Here are the resources."),
                MessagePart(id: "p2", type: "action", text: "raw trace", name: "web_search"),
                MessagePart(id: "p3", type: "link", name: "Nori Docs", url: "https://example.com"),
                MessagePart(id: "p4", type: "card", fallback: "Card fallback", title: "Status Card"),
                MessagePart(id: "p5", type: "note", text: "Saved note"),
                MessagePart(id: "p6", type: "reminder", name: "Call mom"),
                MessagePart(id: "p7", type: "page", display: "page", title: "Doc")
            ]
        )
        let visible = NoriMessageBubble.visibleNonTextParts(in: message)
        #expect(visible.map { $0.id } == ["p3", "p4", "p5"])
    }

    @Test("InlineReminderCard handles set, done one-shot, and cancelled states")
    func reminderCardStates() {
        let active = Automation(
            id: "a1",
            title: "Call dentist",
            status: .enabled,
            trigger: AutomationTrigger(kind: "once", at: "2099-01-01T10:00:00Z"),
            nextAt: "2099-01-01T10:00:00Z"
        )
        #expect(InlineReminderCard.displayState(for: active) == .set)

        let firedOnce = Automation(
            id: "a2",
            title: "Call dentist",
            status: .enabled,
            trigger: AutomationTrigger(kind: "once", at: "2025-01-01T10:00:00Z"),
            nextAt: nil
        )
        #expect(InlineReminderCard.displayState(for: firedOnce) == .done)

        let cancelled = Automation(
            id: "a3",
            title: "Call dentist",
            status: .cancelled,
            trigger: AutomationTrigger(kind: "once", at: "2099-01-01T10:00:00Z"),
            nextAt: nil
        )
        #expect(InlineReminderCard.displayState(for: cancelled) == .cancelled)
    }

    @Test("ChatComposerView filters import types without raw .data and maps upload errors to plain copy")
    func composerImportTypesAndErrors() {
        let types = ChatComposerView.allowedImportTypes
        #expect(types.contains(.pdf))
        #expect(types.contains(.commaSeparatedText))
        #expect(types.contains(.image))
        #expect(types.contains(.plainText))
        #expect(!types.contains(.data))

        let tooLarge = NoriAPIError(message: "Payload too large", statusCode: 413)
        #expect(ChatComposerView.friendlyUploadError(tooLarge) == "That file is too large to upload.")

        let cocoaErr = NSError(domain: NSCocoaErrorDomain, code: 257, userInfo: [NSLocalizedDescriptionKey: "The file couldn’t be opened because you don’t have permission to view it."])
        let mapped = ChatComposerView.friendlyUploadError(cocoaErr)
        #expect(mapped == "Couldn’t upload this file. Please try again.")
        #expect(!mapped.contains("NSCocoaErrorDomain"))
    }
}
