import Foundation

public enum WorkBucket: String, Sendable, Codable, Hashable, CaseIterable {
    case needsYou = "needs_you"
    case working
    case finished
}

public struct WorkStep: Sendable, Codable, Hashable {
    public enum Status: String, Sendable, Codable, Hashable {
        case done
        case active
        case upcoming
    }

    public var text: String
    public var status: Status

    public init(text: String, status: Status) {
        self.text = text
        self.status = status
    }
}

public struct WorkOrigin: Sendable, Codable, Hashable {
    public enum Kind: String, Sendable, Codable, Hashable {
        case conversation
        case automation
        case external
    }

    public var kind: Kind
    public var conversationID: String?
    public var messageID: String?
    public var automationID: String?
    public var occurrenceID: String?
    public var continuesTaskID: String?

    enum CodingKeys: String, CodingKey {
        case kind
        case conversationID = "conversation_id"
        case messageID = "message_id"
        case automationID = "automation_id"
        case occurrenceID = "occurrence_id"
        case continuesTaskID = "continues_task_id"
    }

    public init(
        kind: Kind = .conversation,
        conversationID: String? = "assistant",
        messageID: String? = nil,
        automationID: String? = nil,
        occurrenceID: String? = nil,
        continuesTaskID: String? = nil
    ) {
        self.kind = kind
        self.conversationID = conversationID
        self.messageID = messageID
        self.automationID = automationID
        self.occurrenceID = occurrenceID
        self.continuesTaskID = continuesTaskID
    }

    public var displayLabel: String {
        switch kind {
        case .conversation: return "Chat"
        case .automation: return "Automation"
        case .external: return "External"
        }
    }
}

public struct WorkOperationsSummary: Sendable, Codable, Hashable {
    public var total: Int
    public var uncertain: Int
    public var failed: Int
    public var submitted: Int

    public init(total: Int = 0, uncertain: Int = 0, failed: Int = 0, submitted: Int = 0) {
        self.total = total
        self.uncertain = uncertain
        self.failed = failed
        self.submitted = submitted
    }
}

public struct WorkCost: Sendable, Codable, Hashable {
    public var amountUSD: Double?
    public var status: Cost.Status
    public var budgetUSD: Double?

    enum CodingKeys: String, CodingKey {
        case amountUSD = "amount_usd"
        case status
        case budgetUSD = "budget_usd"
    }

    public init(amountUSD: Double? = nil, status: Cost.Status = .known, budgetUSD: Double? = nil) {
        self.amountUSD = amountUSD
        self.status = status
        self.budgetUSD = budgetUSD
    }
}

public struct WorkItem: Sendable, Codable, Hashable, Identifiable {
    public var id: String { taskID }
    public var taskID: String
    public var title: String
    public var status: RunStatus
    public var bucket: WorkBucket
    public var instructionRevision: Int
    public var origin: WorkOrigin
    public var interactionIDs: [String]
    public var progress: String?
    public var operations: WorkOperationsSummary
    public var resultIDs: [String]
    public var cost: WorkCost
    public var parentTaskID: String?
    public var startedAt: String
    public var finishedAt: String?
    public var cancelRequested: Bool
    public var queued: Bool?
    public var steps: [WorkStep]?
    public var partial: Bool?
    public var error: ErrorDetail?

    enum CodingKeys: String, CodingKey {
        case taskID = "task_id"
        case title
        case status
        case bucket
        case instructionRevision = "instruction_revision"
        case origin
        case interactionIDs = "interaction_ids"
        case progress
        case operations
        case resultIDs = "result_ids"
        case cost
        case parentTaskID = "parent_task_id"
        case startedAt = "started_at"
        case finishedAt = "finished_at"
        case cancelRequested = "cancel_requested"
        case queued
        case steps
        case partial
        case error
    }

    public init(
        taskID: String,
        title: String,
        status: RunStatus,
        bucket: WorkBucket,
        instructionRevision: Int = 1,
        origin: WorkOrigin = WorkOrigin(),
        interactionIDs: [String] = [],
        progress: String? = nil,
        operations: WorkOperationsSummary = WorkOperationsSummary(),
        resultIDs: [String] = [],
        cost: WorkCost = WorkCost(),
        parentTaskID: String? = nil,
        startedAt: String,
        finishedAt: String? = nil,
        cancelRequested: Bool = false,
        queued: Bool? = nil,
        steps: [WorkStep]? = nil,
        partial: Bool? = nil,
        error: ErrorDetail? = nil
    ) {
        self.taskID = taskID
        self.title = title
        self.status = status
        self.bucket = bucket
        self.instructionRevision = instructionRevision
        self.origin = origin
        self.interactionIDs = interactionIDs
        self.progress = progress
        self.operations = operations
        self.resultIDs = resultIDs
        self.cost = cost
        self.parentTaskID = parentTaskID
        self.startedAt = startedAt
        self.finishedAt = finishedAt
        self.cancelRequested = cancelRequested
        self.queued = queued
        self.steps = steps
        self.partial = partial
        self.error = error
    }

    public var isBudgetExhausted: Bool {
        guard status == .failed else { return false }
        if let err = error?.message.lowercased(), err.contains("spending limit") || err.contains("budget") {
            return true
        }
        if let spent = cost.amountUSD, let budget = cost.budgetUSD, budget > 0, spent >= budget {
            return true
        }
        return false
    }
}

public struct OperationAction: Sendable, Codable, Hashable {
    public var digest: String
    public var text: String
    public var spec: [String: String]?

    public init(digest: String, text: String, spec: [String: String]? = nil) {
        self.digest = digest
        self.text = text
        self.spec = spec
    }
}

public struct OperationOwner: Sendable, Codable, Hashable {
    public var type: String
    public var id: String

    public init(type: String = "task", id: String) {
        self.type = type
        self.id = id
    }
}

public struct OperationProjection: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var callID: String
    public var name: String
    public var tool: DefinitionRef?
    public var effectClass: Effect
    public var outcome: Outcome
    public var cleared: String?
    public var connectionID: String?
    public var accountSummary: String?
    public var actionSummary: String?
    public var authoritySource: String?
    public var providerRefs: [ResourceRef]?
    public var receiptURL: String?
    public var resultIDs: [String]?
    public var parentOperationID: String?
    public var cost: Cost?
    public var createdAt: String?
    public var updatedAt: String?
    public var error: ErrorDetail?
    public var revision: Int
    public var attempt: Int?
    public var resultRef: String?
    public var artifactIDs: [String]?
    public var action: OperationAction?
    public var owner: OperationOwner?
    public var bindingDigest: String?
    public var taskTitle: String?
    public var recovery: String?

    enum CodingKeys: String, CodingKey {
        case id
        case callID = "call_id"
        case name
        case tool
        case effectClass = "effect_class"
        case outcome
        case cleared
        case connectionID = "connection_id"
        case accountSummary = "account_summary"
        case actionSummary = "action_summary"
        case authoritySource = "authority_source"
        case providerRefs = "provider_refs"
        case receiptURL = "receipt_url"
        case resultIDs = "result_ids"
        case parentOperationID = "parent_operation_id"
        case cost
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case error
        case revision
        case attempt
        case resultRef = "result_ref"
        case artifactIDs = "artifact_ids"
        case action
        case owner
        case bindingDigest = "binding_digest"
        case taskTitle = "task_title"
        case recovery
    }

    public init(
        id: String,
        callID: String = "",
        name: String,
        tool: DefinitionRef? = nil,
        effectClass: Effect = .read,
        outcome: Outcome = .confirmed,
        cleared: String? = nil,
        connectionID: String? = nil,
        accountSummary: String? = nil,
        actionSummary: String? = nil,
        authoritySource: String? = nil,
        providerRefs: [ResourceRef]? = nil,
        receiptURL: String? = nil,
        resultIDs: [String]? = nil,
        parentOperationID: String? = nil,
        cost: Cost? = nil,
        createdAt: String? = nil,
        updatedAt: String? = nil,
        error: ErrorDetail? = nil,
        revision: Int = 1,
        attempt: Int? = nil,
        resultRef: String? = nil,
        artifactIDs: [String]? = nil,
        action: OperationAction? = nil,
        owner: OperationOwner? = nil,
        bindingDigest: String? = nil,
        taskTitle: String? = nil,
        recovery: String? = nil
    ) {
        self.id = id
        self.callID = callID
        self.name = name
        self.tool = tool
        self.effectClass = effectClass
        self.outcome = outcome
        self.cleared = cleared
        self.connectionID = connectionID
        self.accountSummary = accountSummary
        self.actionSummary = actionSummary
        self.authoritySource = authoritySource
        self.providerRefs = providerRefs
        self.receiptURL = receiptURL
        self.resultIDs = resultIDs
        self.parentOperationID = parentOperationID
        self.cost = cost
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.error = error
        self.revision = revision
        self.attempt = attempt
        self.resultRef = resultRef
        self.artifactIDs = artifactIDs
        self.action = action
        self.owner = owner
        self.bindingDigest = bindingDigest
        self.taskTitle = taskTitle
        self.recovery = recovery
    }

    public var isWebSearch: Bool {
        let lowerName = name.lowercased()
        if lowerName == "web_search" || lowerName == "web_search_call" {
            return true
        }
        if let toolID = tool?.id.lowercased(), toolID == "web_search" || toolID.hasSuffix("/web_search") {
            return true
        }
        return false
    }

    public var displaySummary: String {
        if let summary = actionSummary?.trimmingCharacters(in: .whitespacesAndNewlines), !summary.isEmpty {
            return summary
        }
        if let text = action?.text.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty {
            return text
        }
        if isWebSearch {
            return "Searched the web"
        }
        return name
    }

    public var displayAuthorityLabel: String? {
        guard let raw = authoritySource?.trimmingCharacters(in: .whitespacesAndNewlines), !raw.isEmpty else {
            return nil
        }
        switch raw {
        case "user_request": return "you asked"
        case "permission_rule": return "a permission rule"
        case "automation": return "a routine"
        case "approval": return "your approval"
        case "internal": return "Nori"
        default: return raw
        }
    }

    public var displayAuthority: String {
        displayAuthorityLabel ?? ""
    }

    public var statusBadge: OperationBadge? {
        if let cleared = cleared?.trimmingCharacters(in: .whitespacesAndNewlines), !cleared.isEmpty {
            switch cleared {
            case "did_not_run": return OperationBadge(label: "Didn’t run", tone: .neutral)
            case "read_only": return OperationBadge(label: "Only read", tone: .neutral)
            case "answered": return OperationBadge(label: "Settled", tone: .neutral)
            case "contained": return OperationBadge(label: "Contained", tone: .warning)
            default: break
            }
        }
        switch outcome {
        case .confirmed:
            if effectClass == .read {
                return nil
            }
            return OperationBadge(label: "Confirmed", tone: .ok)
        case .pending: return OperationBadge(label: "Waiting", tone: .neutral)
        case .uncertain: return OperationBadge(label: "Unconfirmed", tone: .warning)
        case .failed: return OperationBadge(label: "Failed", tone: .danger)
        case .submitted: return OperationBadge(label: "Submitted", tone: .neutral)
        case .attempted: return OperationBadge(label: "In progress", tone: .neutral)
        case .ready: return OperationBadge(label: "Ready", tone: .neutral)
        case .skipped: return OperationBadge(label: "Skipped", tone: .neutral)
        case .voided: return OperationBadge(label: "Cancelled", tone: .neutral)
        }
    }
}

public enum OperationBadgeTone: String, Sendable, Hashable {
    case neutral
    case ok
    case warning
    case danger
}

public struct OperationBadge: Sendable, Hashable {
    public let label: String
    public let tone: OperationBadgeTone

    public init(label: String, tone: OperationBadgeTone) {
        self.label = label
        self.tone = tone
    }
}

public enum WorkActionEntry: Sendable, Hashable, Identifiable {
    case single(OperationProjection)
    case webSearchGroup(id: String, operations: [OperationProjection])

    public var id: String {
        switch self {
        case .single(let op):
            return op.id
        case .webSearchGroup(let id, _):
            return id
        }
    }

    public var groupTitle: String? {
        guard case .webSearchGroup(_, let ops) = self else { return nil }
        return "Searched the web (\(ops.count) \(ops.count == 1 ? "query" : "queries"))"
    }

    public var groupBadge: OperationBadge? {
        guard case .webSearchGroup(_, let ops) = self else { return nil }
        if ops.contains(where: { $0.outcome == .failed }) {
            return OperationBadge(label: "Failed", tone: .danger)
        }
        if ops.contains(where: { $0.outcome == .attempted || $0.outcome == .pending }) {
            return OperationBadge(label: "In progress", tone: .neutral)
        }
        return nil
    }

    public static func group(_ operations: [OperationProjection]) -> [WorkActionEntry] {
        var entries: [WorkActionEntry] = []
        var searchRun: [OperationProjection] = []

        func flushSearchRun() {
            guard !searchRun.isEmpty else { return }
            if searchRun.count == 1 {
                entries.append(.single(searchRun[0]))
            } else {
                entries.append(.webSearchGroup(id: "web-search-group-\(searchRun[0].id)-\(searchRun.count)", operations: searchRun))
            }
            searchRun.removeAll(keepingCapacity: true)
        }

        for op in operations {
            if op.isWebSearch {
                searchRun.append(op)
            } else {
                flushSearchRun()
                entries.append(.single(op))
            }
        }
        flushSearchRun()
        return entries
    }
}

public enum WorkStepStatus: String, Sendable, Hashable {
    case done
    case active
    case warning
    case error
}

public struct WorkStepQuery: Sendable, Hashable, Identifiable {
    public let id: String
    public let text: String
    public let createdAt: String?

    public init(id: String, text: String, createdAt: String? = nil) {
        self.id = id
        self.text = text
        self.createdAt = createdAt
    }
}

public enum WorkStepEntry: Sendable, Hashable, Identifiable {
    case step(id: String, title: String, status: WorkStepStatus, createdAt: String?)
    case searchGroup(id: String, title: String, status: WorkStepStatus, createdAt: String?, queries: [WorkStepQuery])

    public var id: String {
        switch self {
        case .step(let id, _, _, _): return id
        case .searchGroup(let id, _, _, _, _): return id
        }
    }

    public var title: String {
        switch self {
        case .step(_, let title, _, _): return title
        case .searchGroup(_, let title, _, _, _): return title
        }
    }

    public var status: WorkStepStatus {
        switch self {
        case .step(_, _, let status, _): return status
        case .searchGroup(_, _, let status, _, _): return status
        }
    }

    public var createdAt: String? {
        switch self {
        case .step(_, _, _, let createdAt): return createdAt
        case .searchGroup(_, _, _, let createdAt, _): return createdAt
        }
    }

    public static func externalChanges(from operations: [OperationProjection]) -> [OperationProjection] {
        operations.filter { op in
            guard op.name != "capability_load" else { return false }
            if op.effectClass == .reversibleWrite || op.effectClass == .sensitiveSend {
                return true
            }
            return op.effectClass != .read && (op.outcome == .uncertain || op.outcome == .failed)
        }
    }

    public static func build(operations: [OperationProjection], events: [RunEvent]) -> [WorkStepEntry] {
        let visibleOps = operations.filter { $0.name != "capability_load" }
        let grouped = WorkActionEntry.group(visibleOps)
        let toolNames = Set(operations.map(\.name))
        let opSummaries = Set(visibleOps.map { $0.displaySummary.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() })

        struct IndexedEntry {
            let entry: WorkStepEntry
            let sortTimestamp: Double
            let order: Int
        }

        func parseSortTimestamp(_ iso: String?) -> Double {
            guard let iso, !iso.isEmpty else { return .greatestFiniteMagnitude }
            let frac = ISO8601DateFormatter()
            frac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = frac.date(from: iso) {
                return date.timeIntervalSince1970
            }
            let std = ISO8601DateFormatter()
            std.formatOptions = [.withInternetDateTime]
            if let date = std.date(from: iso) {
                return date.timeIntervalSince1970
            }
            return .greatestFiniteMagnitude
        }

        func stepStatus(for op: OperationProjection) -> WorkStepStatus {
            switch op.outcome {
            case .failed: return .error
            case .uncertain: return .warning
            case .attempted, .pending, .submitted: return .active
            default: return .done
            }
        }

        var indexed: [IndexedEntry] = []
        var order = 0

        for entry in grouped {
            switch entry {
            case .webSearchGroup(let groupID, let ops):
                let hasFailed = ops.contains { $0.outcome == .failed }
                let hasActive = ops.contains { $0.outcome == .attempted || $0.outcome == .pending }
                let firstTime = ops.first { ($0.createdAt ?? "").isEmpty == false }?.createdAt
                let status: WorkStepStatus = hasFailed ? .error : (hasActive ? .active : .done)
                let title = "Searched the web (\(ops.count) \(ops.count == 1 ? "query" : "queries"))"
                let queries = ops.map { WorkStepQuery(id: $0.id, text: $0.displaySummary, createdAt: $0.createdAt) }
                indexed.append(
                    IndexedEntry(
                        entry: .searchGroup(id: groupID, title: title, status: status, createdAt: firstTime, queries: queries),
                        sortTimestamp: parseSortTimestamp(firstTime),
                        order: order
                    )
                )
                order += 1
            case .single(let op):
                indexed.append(
                    IndexedEntry(
                        entry: .step(id: "op-\(op.id)", title: op.displaySummary, status: stepStatus(for: op), createdAt: op.createdAt),
                        sortTimestamp: parseSortTimestamp(op.createdAt),
                        order: order
                    )
                )
                order += 1
            }
        }

        for (idx, event) in events.enumerated() {
            let trimmed = event.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty, !toolNames.contains(trimmed), trimmed != "capability_load" else { continue }
            guard !opSummaries.contains(trimmed.lowercased()) else { continue }
            indexed.append(
                IndexedEntry(
                    entry: .step(id: "evt-\(event.createdAt)-\(idx)", title: trimmed, status: .done, createdAt: event.createdAt),
                    sortTimestamp: parseSortTimestamp(event.createdAt),
                    order: order
                )
            )
            order += 1
        }

        return indexed
            .sorted { lhs, rhs in
                if lhs.sortTimestamp != rhs.sortTimestamp {
                    return lhs.sortTimestamp < rhs.sortTimestamp
                }
                return lhs.order < rhs.order
            }
            .map(\.entry)
    }

    public static func stripLeadingMatchingHeading(_ markdown: String, title: String) -> String {
        let normalizedTitle = title.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !normalizedTitle.isEmpty else { return markdown }
        let lines = markdown.components(separatedBy: .newlines)
        guard let firstIndex = lines.firstIndex(where: { !$0.trimmingCharacters(in: .whitespaces).isEmpty }) else {
            return markdown
        }
        let firstLine = lines[firstIndex].trimmingCharacters(in: .whitespaces)
        var hashes = 0
        for char in firstLine {
            if char == "#" { hashes += 1 } else { break }
        }
        guard hashes >= 1 && hashes <= 6 else { return markdown }
        let afterHashes = firstLine.dropFirst(hashes)
        guard afterHashes.first == " " else { return markdown }
        let headingText = afterHashes
            .trimmingCharacters(in: .whitespaces)
            .trimmingCharacters(in: CharacterSet(charactersIn: "#"))
            .trimmingCharacters(in: .whitespaces)
            .lowercased()
        guard headingText == normalizedTitle else { return markdown }
        let remaining = lines[(firstIndex + 1)...].joined(separator: "\n")
        return remaining.replacingOccurrences(of: #"^\s*\n"#, with: "", options: .regularExpression)
    }

    public static func budgetPresets(currentBudgetUSD: Double?) -> [Double] {
        let current = (currentBudgetUSD ?? 1.0) > 0 ? (currentBudgetUSD ?? 1.0) : 1.0
        if current < 10 {
            let standard = [2.0, 5.0, 10.0].filter { $0 > current }
            if standard.count == 3 { return standard }
            let base = (current * 2.0).rounded(.up)
            var result = standard
            for candidate in [base, base * 2.0] where !result.contains(candidate) && result.count < 3 {
                result.append(candidate)
            }
            return result
        }
        return [
            (current * 1.5).rounded(.up),
            (current * 2.0).rounded(.up),
            (current * 3.0).rounded(.up)
        ]
    }

    public static func isLongMarkdown(_ markdown: String) -> Bool {
        let trimmed = markdown.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.count > 400 { return true }
        let nonEmptyLines = trimmed
            .components(separatedBy: .newlines)
            .filter { !$0.trimmingCharacters(in: .whitespaces).isEmpty }
        return nonEmptyLines.count > 7
    }
}

public struct WorkDetail: Sendable, Codable, Hashable {
    public var item: WorkItem
    public var operations: [OperationProjection]
    public var interactions: [Interaction]
    public var result: String?
    public var events: [RunEvent]?
    public var eventsTruncated: Bool?

    enum CodingKeys: String, CodingKey {
        case item
        case operations
        case interactions
        case result
        case events
        case eventsTruncated = "events_truncated"
    }

    public init(
        item: WorkItem,
        operations: [OperationProjection] = [],
        interactions: [Interaction] = [],
        result: String? = nil,
        events: [RunEvent]? = nil,
        eventsTruncated: Bool? = nil
    ) {
        self.item = item
        self.operations = operations
        self.interactions = interactions
        self.result = result
        self.events = events
        self.eventsTruncated = eventsTruncated
    }
}
