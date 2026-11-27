import Foundation

public enum WorkBucket: String, Sendable, Codable, Hashable, CaseIterable {
    case needsYou = "needs_you"
    case working
    case finished
}

public struct WorkOrigin: Sendable, Codable, Hashable {
    public enum Kind: String, Sendable, Codable, Hashable {
        case conversation
        case automation
        case goal
        case external
    }

    public var kind: Kind
    public var conversationID: String?
    public var messageID: String?
    public var automationID: String?
    public var occurrenceID: String?
    public var goalID: String?
    public var continuesTaskID: String?

    enum CodingKeys: String, CodingKey {
        case kind
        case conversationID = "conversation_id"
        case messageID = "message_id"
        case automationID = "automation_id"
        case occurrenceID = "occurrence_id"
        case goalID = "goal_id"
        case continuesTaskID = "continues_task_id"
    }

    public init(
        kind: Kind = .conversation,
        conversationID: String? = "assistant",
        messageID: String? = nil,
        automationID: String? = nil,
        occurrenceID: String? = nil,
        goalID: String? = nil,
        continuesTaskID: String? = nil
    ) {
        self.kind = kind
        self.conversationID = conversationID
        self.messageID = messageID
        self.automationID = automationID
        self.occurrenceID = occurrenceID
        self.goalID = goalID
        self.continuesTaskID = continuesTaskID
    }

    public var displayLabel: String {
        switch kind {
        case .conversation: return "Chat"
        case .automation: return "Routine"
        case .goal: return "Goal"
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
