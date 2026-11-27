import Foundation
import Testing
import NoriCore
import NoriDesign
@testable import NoriFeatures

@MainActor
@Suite("Performance Baseline — NoriDesign & NoriFeatures")
struct NoriFeaturesPerformanceBaselineTests {

    private static func measureMedianMs(iterations: Int = 5, _ block: () -> Void) -> (median: Double, runs: [Double]) {
        let clock = ContinuousClock()
        var runs: [Double] = []
        runs.reserveCapacity(iterations)
        for _ in 0..<iterations {
            let elapsed = clock.measure {
                block()
            }
            let ms = Double(elapsed.components.seconds) * 1_000.0
                + Double(elapsed.components.attoseconds) / 1_000_000_000_000_000.0
            runs.append(ms)
        }
        let sorted = runs.sorted()
        let median = sorted[sorted.count / 2]
        return (median, runs)
    }

    private static func formatRuns(_ runs: [Double]) -> String {
        runs.map { String(format: "%.3f", $0) }.joined(separator: ", ")
    }

    @Test("Baseline #1: NoriDateFormatting.parse — 2,000 ISO8601 timestamps (median ms over 5 iterations)")
    func baselineDateFormattingParse() {
        var timestamps: [String] = []
        timestamps.reserveCapacity(2_000)
        for i in 0..<1_000 {
            let day = (i % 28) + 1
            let hour = i % 24
            let minute = (i * 7) % 60
            let second = (i * 13) % 60
            let ms = (i * 37) % 1_000
            timestamps.append(String(
                format: "2026-11-%02dT%02d:%02d:%02d.%03dZ",
                day, hour, minute, second, ms
            ))
            timestamps.append(String(
                format: "2026-11-%02dT%02d:%02d:%02dZ",
                day, hour, minute, second
            ))
        }

        var lastParsedCount = 0
        let stats = Self.measureMedianMs(iterations: 5) {
            var count = 0
            for ts in timestamps {
                if NoriDateFormatting.parse(ts) != nil {
                    count += 1
                }
            }
            lastParsedCount = count
        }

        print("[BASELINE] NoriDateFormatting.parse (2,000 ISO8601 timestamps): median=\(String(format: "%.3f", stats.median)) ms (runs: [\(Self.formatRuns(stats.runs))] ms)")
        #expect(lastParsedCount == 2_000)
    }

    @Test("Baseline #2: ChatThreadUnit.build — 100 messages (median ms over 5 iterations)")
    func baselineChatThreadUnitBuild() {
        var messages: [Message] = []
        messages.reserveCapacity(100)
        for i in 0..<100 {
            let isUser = (i % 3 == 0)
            let hour = 10 + (i / 60)
            let minute = i % 60
            let createdAt = String(format: "2026-11-27T%02d:%02d:15.250Z", hour, minute)
            let separator: String? = (i % 25 == 0) ? "Today" : nil
            let parts: [MessagePart]? = (i % 15 == 14 && !isUser)
                ? [
                    MessagePart(id: "p-text-\(i)", type: "text", text: "Here is the summary document."),
                    MessagePart(id: "p-page-\(i)", type: "page", display: "page", title: "Report \(i)")
                ]
                : nil

            messages.append(Message(
                id: "msg-\(i)",
                runID: "run-\(i / 2)",
                role: isUser ? .user : .assistant,
                text: "Message \(i) content with realistic length for thread grouping.",
                parts: parts,
                createdAt: createdAt,
                separatorLabel: separator
            ))
        }

        let pendingInteractions: [Interaction] = (0..<5).map { idx in
            let minute = 10 + idx * 10
            return Interaction(
                id: "int-\(idx)",
                revision: 1,
                kind: .approval,
                status: .pending,
                prompt: "Approve action \(idx)?",
                payload: .approval(ApprovalPayload(
                    summary: "Approve action \(idx)",
                    effect: .sensitiveSend,
                    account: ApprovalAccount(connectionID: "conn-1", label: "alex@example.com")
                )),
                createdAt: String(format: "2026-11-27T10:%02d:30.000Z", minute)
            )
        }

        var lastUnitCount = 0
        let stats = Self.measureMedianMs(iterations: 5) {
            let units = ChatThreadUnit.build(from: messages, pendingInteractions: pendingInteractions)
            lastUnitCount = units.count
        }

        print("[BASELINE] ChatThreadUnit.build (100 messages, 5 pending interactions): median=\(String(format: "%.3f", stats.median)) ms (runs: [\(Self.formatRuns(stats.runs))] ms) -> \(lastUnitCount) units")
        #expect(lastUnitCount > 0)
    }

    @Test("Baseline #3: MarkdownBlocksView.parseBlocks — 50 rich markdown messages (median ms over 5 iterations)")
    func baselineMarkdownBlocksParse() {
        let richMessages: [String] = (0..<50).map { i in
            """
            # Weekly Audit Summary #\(i)

            Here is an overview with **bold text**, *italic emphasis*, and `inline_code_\(i)` across multiple sentences so the parser handles normal prose cleanly.

            ## Key Findings

            - Primary finding \(i): **formatter allocations** happen per call
              - Nested detail A for item \(i)
              - Nested detail B for item \(i)
            - Secondary finding \(i): redundant network refreshes on tab switches

            1. First prioritized action item for batch \(i)
            2. Second prioritized action item for batch \(i)
              3. Sub-step for verification \(i)

            > Always measure a clean deterministic baseline before optimizing.
            > Compare median timings over 5 iterations.

            | Metric | Target | Baseline #\(i) |
            | --- | --- | --- |
            | Parse 2,000 dates | < 5 ms | Pending |
            | Build 100 units | < 2 ms | Pending |
            | Parse 50 markdown | < 10 ms | Pending |

            ---

            ```swift
            struct AuditSample\(i) {
                let id: Int = \(i)
                func label() -> String { "sample-\\(id)" }
            }
            ```
            """
        }

        var lastTotalBlocks = 0
        let stats = Self.measureMedianMs(iterations: 5) {
            var total = 0
            for md in richMessages {
                total += MarkdownBlocksView.parseBlocks(md).count
            }
            lastTotalBlocks = total
        }

        print("[BASELINE] MarkdownBlocksView.parseBlocks (50 rich markdown messages): median=\(String(format: "%.3f", stats.median)) ms (runs: [\(Self.formatRuns(stats.runs))] ms) -> \(lastTotalBlocks) blocks")
        #expect(lastTotalBlocks > 0)
    }
}
