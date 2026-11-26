import Foundation


public enum MarkState: String, Sendable, Codable, Hashable {
    case idle
    case attentive
    case thinking
}


public enum MessageRole: String, Sendable, Codable, Hashable {
    case user
    case assistant
}

public enum MessageReceipt: String, Sendable, Codable, Hashable {
    case sending
    case saved
}

public enum MessageStatus: String, Sendable, Codable, Hashable {
    case completed
    case failed
    case cancelled
    case superseded
}

public struct CardResourceRef: Sendable, Codable, Hashable {
    public var kind: String
    public var id: String
    public var revision: Int?

    public init(kind: String, id: String, revision: Int? = nil) {
        self.kind = kind
        self.id = id
        self.revision = revision
    }
}

public struct PageMetric: Sendable, Codable, Hashable, Identifiable {
    public var id: String { "\(label)-\(value)" }
    public var label: String
    public var value: String

    public init(label: String, value: String) {
        self.label = label
        self.value = value
    }
}

public struct MessagePart: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var type: String
    public var text: String?
    public var display: String?
    public var fallback: String?
    public var name: String?
    public var mediaType: String?
    public var size: Int?
    public var url: String?
    public var schemaVersion: Int?
    public var kind: String?
    public var resource: CardResourceRef?
    public var actions: [String]?
    public var title: String?
    public var metrics: [PageMetric]?
    public var scheduledAt: String?

    enum CodingKeys: String, CodingKey {
        case id
        case type
        case text
        case display
        case fallback
        case name
        case mediaType = "media_type"
        case size
        case url
        case schemaVersion = "schema_version"
        case kind
        case resource
        case actions
        case title
        case metrics
        case scheduledAt = "scheduled_at"
    }

    public init(
        id: String = UUID().uuidString,
        type: String,
        text: String? = nil,
        display: String? = nil,
        fallback: String? = nil,
        name: String? = nil,
        mediaType: String? = nil,
        size: Int? = nil,
        url: String? = nil,
        schemaVersion: Int? = nil,
        kind: String? = nil,
        resource: CardResourceRef? = nil,
        actions: [String]? = nil,
        title: String? = nil,
        metrics: [PageMetric]? = nil,
        scheduledAt: String? = nil
    ) {
        self.id = id
        self.type = type
        self.text = text
        self.display = display
        self.fallback = fallback
        self.name = name
        self.mediaType = mediaType
        self.size = size
        self.url = url
        self.schemaVersion = schemaVersion
        self.kind = kind
        self.resource = resource
        self.actions = actions
        self.title = title
        self.metrics = metrics
        self.scheduledAt = scheduledAt
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.type = try c.decode(String.self, forKey: .type)
        self.text = try c.decodeIfPresent(String.self, forKey: .text)
        self.display = try c.decodeIfPresent(String.self, forKey: .display)
        self.fallback = try c.decodeIfPresent(String.self, forKey: .fallback)
        self.name = try c.decodeIfPresent(String.self, forKey: .name)
        self.mediaType = try c.decodeIfPresent(String.self, forKey: .mediaType)
        self.size = try c.decodeIfPresent(Int.self, forKey: .size)
        self.url = try c.decodeIfPresent(String.self, forKey: .url)
        self.schemaVersion = try c.decodeIfPresent(Int.self, forKey: .schemaVersion)
        self.kind = try c.decodeIfPresent(String.self, forKey: .kind)
        self.resource = try c.decodeIfPresent(CardResourceRef.self, forKey: .resource)
        self.actions = try c.decodeIfPresent([String].self, forKey: .actions)
        self.title = try c.decodeIfPresent(String.self, forKey: .title)
        self.metrics = try c.decodeIfPresent([PageMetric].self, forKey: .metrics)
        self.scheduledAt = try c.decodeIfPresent(String.self, forKey: .scheduledAt)
        self.id = try c.decodeIfPresent(String.self, forKey: .id) ?? "\(type)-\(resource?.id ?? name ?? UUID().uuidString)"
    }

    public var isReminder: Bool {
        type == "reminder"
    }

    public var reminderTitle: String {
        if let name = name?.trimmingCharacters(in: .whitespacesAndNewlines), !name.isEmpty {
            return name
        }
        if let title = title?.trimmingCharacters(in: .whitespacesAndNewlines), !title.isEmpty {
            return title
        }
        return fallback?.trimmingCharacters(in: .whitespacesAndNewlines).nilIfEmpty ?? "Reminder"
    }
}

private extension String {
    var nilIfEmpty: String? {
        isEmpty ? nil : self
    }
}

public struct Attachment: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var mediaType: String
    public var size: Int

    enum CodingKeys: String, CodingKey {
        case id
        case name
        case mediaType = "media_type"
        case size
    }

    public init(id: String = UUID().uuidString.lowercased(), name: String, mediaType: String, size: Int) {
        self.id = id
        self.name = name
        self.mediaType = mediaType
        self.size = size
    }
}

public struct Message: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var runID: String
    public var turnID: String?
    public var role: MessageRole
    public var status: MessageStatus?
    public var text: String
    public var parts: [MessagePart]?
    public var error: ErrorDetail?
    public var resultIDs: [String]?
    public var createdAt: String
    public var receipt: MessageReceipt?
    public var separatorLabel: String?

    enum CodingKeys: String, CodingKey {
        case id
        case runID = "run_id"
        case turnID = "turn_id"
        case role
        case status
        case text
        case parts
        case error
        case resultIDs = "result_ids"
        case createdAt = "created_at"
        case receipt
        case separatorLabel = "separator_label"
    }

    public init(
        id: String,
        runID: String = "",
        turnID: String? = nil,
        role: MessageRole,
        status: MessageStatus? = nil,
        text: String,
        parts: [MessagePart]? = nil,
        error: ErrorDetail? = nil,
        resultIDs: [String]? = nil,
        createdAt: String = ISO8601DateFormatter().string(from: Date()),
        receipt: MessageReceipt? = nil,
        separatorLabel: String? = nil
    ) {
        self.id = id
        self.runID = runID
        self.turnID = turnID
        self.role = role
        self.status = status
        self.text = text
        self.parts = parts
        self.error = error
        self.resultIDs = resultIDs
        self.createdAt = createdAt
        self.receipt = receipt
        self.separatorLabel = separatorLabel
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.id = try c.decode(String.self, forKey: .id)
        self.runID = try c.decodeIfPresent(String.self, forKey: .runID) ?? ""
        self.turnID = try c.decodeIfPresent(String.self, forKey: .turnID)
        self.role = try c.decode(MessageRole.self, forKey: .role)
        self.status = try? c.decodeIfPresent(MessageStatus.self, forKey: .status)
        self.text = try c.decodeIfPresent(String.self, forKey: .text) ?? ""
        self.parts = try c.decodeIfPresent([MessagePart].self, forKey: .parts)
        self.error = try c.decodeIfPresent(ErrorDetail.self, forKey: .error)
        self.resultIDs = try c.decodeIfPresent([String].self, forKey: .resultIDs)
        self.createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt) ?? ""
        self.receipt = try c.decodeIfPresent(MessageReceipt.self, forKey: .receipt)
        self.separatorLabel = try c.decodeIfPresent(String.self, forKey: .separatorLabel)
    }

    public var isFromUser: Bool { role == .user }
    public var isFailed: Bool { status == .failed }
    public var isSuperseded: Bool { status == .superseded }

    public var isRetryableError: Bool {
        isFailed && (error?.retryable ?? true)
    }

    public var displayErrorMessage: String {
        if let errMsg = error?.message.trimmingCharacters(in: .whitespacesAndNewlines), !errMsg.isEmpty {
            return errMsg
        }
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? "Nori couldn’t finish replying just now. Please try again." : trimmed
    }

    public var pagePart: MessagePart? {
        parts?.first(where: { $0.display == "page" || $0.type == "page" || ($0.type == "card" && $0.kind == "file") })
    }

    public var reminderParts: [MessagePart] {
        (parts ?? []).filter { $0.isReminder }
    }
}

public struct SavedResult: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var rootID: String
    public var version: Int
    public var revises: String?
    public var supersededBy: String?
    public var runID: String
    public var kind: String
    public var title: String
    public var name: String?
    public var mediaType: String?
    public var size: Int?
    public var url: String?
    public var unmet: [String]?
    public var createdAt: String
    public var checkedAt: String?
    public var reportedAt: String?
    public var deliveredAt: String?
    public var messageID: String?

    enum CodingKeys: String, CodingKey {
        case id
        case rootID = "root_id"
        case version
        case revises
        case supersededBy = "superseded_by"
        case runID = "run_id"
        case kind
        case title
        case name
        case mediaType = "media_type"
        case size
        case url
        case unmet
        case createdAt = "created_at"
        case checkedAt = "checked_at"
        case reportedAt = "reported_at"
        case deliveredAt = "delivered_at"
        case messageID = "message_id"
    }

    public init(
        id: String,
        rootID: String,
        version: Int = 1,
        revises: String? = nil,
        supersededBy: String? = nil,
        runID: String,
        kind: String,
        title: String,
        name: String? = nil,
        mediaType: String? = nil,
        size: Int? = nil,
        url: String? = nil,
        unmet: [String]? = nil,
        createdAt: String,
        checkedAt: String? = nil,
        reportedAt: String? = nil,
        deliveredAt: String? = nil,
        messageID: String? = nil
    ) {
        self.id = id
        self.rootID = rootID
        self.version = version
        self.revises = revises
        self.supersededBy = supersededBy
        self.runID = runID
        self.kind = kind
        self.title = title
        self.name = name
        self.mediaType = mediaType
        self.size = size
        self.url = url
        self.unmet = unmet
        self.createdAt = createdAt
        self.checkedAt = checkedAt
        self.reportedAt = reportedAt
        self.deliveredAt = deliveredAt
        self.messageID = messageID
    }
}


public enum RunStatus: String, Sendable, Codable, Hashable {
    case running
    case completed
    case failed
    case cancelled
}

public struct RunEvent: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var sequence: Int?
    public var instructionRevision: Int?
    public var kind: String
    public var text: String
    public var createdAt: String

    enum CodingKeys: String, CodingKey {
        case id
        case sequence
        case instructionRevision = "instruction_revision"
        case kind
        case text
        case createdAt = "created_at"
    }

    public init(
        id: String = UUID().uuidString,
        sequence: Int? = nil,
        instructionRevision: Int? = nil,
        kind: String = "info",
        text: String,
        createdAt: String
    ) {
        self.id = id
        self.sequence = sequence
        self.instructionRevision = instructionRevision
        self.kind = kind
        self.text = text
        self.createdAt = createdAt
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.sequence = try c.decodeIfPresent(Int.self, forKey: .sequence)
        self.instructionRevision = try c.decodeIfPresent(Int.self, forKey: .instructionRevision)
        self.kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? "info"
        self.text = try c.decodeIfPresent(String.self, forKey: .text) ?? ""
        self.createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt) ?? ""
        self.id = try c.decodeIfPresent(String.self, forKey: .id) ?? "\(kind)-\(sequence ?? 0)-\(createdAt)"
    }
}

public struct TaskQuestion: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var kind: String
    public var text: String
    public var callID: String?
    public var instructionRevision: Int
    public var resolvedBy: String?

    enum CodingKeys: String, CodingKey {
        case id
        case kind
        case text
        case callID = "call_id"
        case instructionRevision = "instruction_revision"
        case resolvedBy = "resolved_by"
    }

    public init(
        id: String,
        kind: String = "input",
        text: String,
        callID: String? = nil,
        instructionRevision: Int = 1,
        resolvedBy: String? = nil
    ) {
        self.id = id
        self.kind = kind
        self.text = text
        self.callID = callID
        self.instructionRevision = instructionRevision
        self.resolvedBy = resolvedBy
    }
}

public struct TaskHold: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var taskID: String
    public var question: String
    public var status: String
    public var answeredTurnID: String?
    public var createdAt: String

    enum CodingKeys: String, CodingKey {
        case id
        case taskID = "task_id"
        case question
        case status
        case answeredTurnID = "answered_turn_id"
        case createdAt = "created_at"
    }

    public init(
        id: String,
        taskID: String,
        question: String,
        status: String,
        answeredTurnID: String? = nil,
        createdAt: String
    ) {
        self.id = id
        self.taskID = taskID
        self.question = question
        self.status = status
        self.answeredTurnID = answeredTurnID
        self.createdAt = createdAt
    }
}

public struct Artifact: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var kind: String

    public init(id: String, kind: String = "screenshot") {
        self.id = id
        self.kind = kind
    }
}

public struct Run: Sendable, Codable, Hashable, Identifiable {
    public struct Finishing: Sendable, Codable, Hashable {
        public var status: RunStatus
        public init(status: RunStatus) { self.status = status }
    }

    public var id: String
    public var status: RunStatus
    public var cancelRequested: Bool
    public var finishing: Finishing?
    public var cleanupPending: Bool?
    public var cleanupError: String?
    public var request: String?
    public var holds: [TaskHold]?
    public var position: Int?
    public var eventSequence: Int?
    public var question: TaskQuestion?
    public var result: String?
    public var revision: Int?
    public var incorporated: Int?
    public var waiting: String?
    public var progress: String?
    public var spentUSD: Double?
    public var costUnknown: Bool?
    public var budgetUSD: Double?
    public var parentID: String?
    public var subtaskSpentUSD: Double?
    public var startedAt: String
    public var finishedAt: String?
    public var error: String?
    public var events: [RunEvent]?
    public var artifacts: [Artifact]?

    enum CodingKeys: String, CodingKey {
        case id
        case status
        case cancelRequested = "cancel_requested"
        case finishing
        case cleanupPending = "cleanup_pending"
        case cleanupError = "cleanup_error"
        case request
        case holds
        case position
        case eventSequence = "event_sequence"
        case question
        case result
        case revision
        case incorporated
        case waiting
        case progress
        case spentUSD = "spent_usd"
        case costUnknown = "cost_unknown"
        case budgetUSD = "budget_usd"
        case parentID = "parent_id"
        case subtaskSpentUSD = "subtask_spent_usd"
        case startedAt = "started_at"
        case finishedAt = "finished_at"
        case error
        case events
        case artifacts
    }

    public init(
        id: String,
        status: RunStatus,
        cancelRequested: Bool = false,
        finishing: Finishing? = nil,
        cleanupPending: Bool? = nil,
        cleanupError: String? = nil,
        request: String? = nil,
        holds: [TaskHold]? = nil,
        position: Int? = nil,
        eventSequence: Int? = nil,
        question: TaskQuestion? = nil,
        result: String? = nil,
        revision: Int? = nil,
        incorporated: Int? = nil,
        waiting: String? = nil,
        progress: String? = nil,
        spentUSD: Double? = nil,
        costUnknown: Bool? = nil,
        budgetUSD: Double? = nil,
        parentID: String? = nil,
        subtaskSpentUSD: Double? = nil,
        startedAt: String,
        finishedAt: String? = nil,
        error: String? = nil,
        events: [RunEvent]? = nil,
        artifacts: [Artifact]? = nil
    ) {
        self.id = id
        self.status = status
        self.cancelRequested = cancelRequested
        self.finishing = finishing
        self.cleanupPending = cleanupPending
        self.cleanupError = cleanupError
        self.request = request
        self.holds = holds
        self.position = position
        self.eventSequence = eventSequence
        self.question = question
        self.result = result
        self.revision = revision
        self.incorporated = incorporated
        self.waiting = waiting
        self.progress = progress
        self.spentUSD = spentUSD
        self.costUnknown = costUnknown
        self.budgetUSD = budgetUSD
        self.parentID = parentID
        self.subtaskSpentUSD = subtaskSpentUSD
        self.startedAt = startedAt
        self.finishedAt = finishedAt
        self.error = error
        self.events = events
        self.artifacts = artifacts
    }
}

public enum TaskControlKind: String, Sendable, Codable, Hashable {
    case steer
    case answer
    case stop
    case `continue`
    case approve
    case decline
}

public struct TaskCommand: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var runID: String
    public var kind: TaskControlKind
    public var text: String?
    public var questionID: String?

    enum CodingKeys: String, CodingKey {
        case id
        case runID = "run_id"
        case kind
        case text
        case questionID = "question_id"
    }

    public init(
        id: String = UUID().uuidString.lowercased(),
        runID: String,
        kind: TaskControlKind,
        text: String? = nil,
        questionID: String? = nil
    ) {
        self.id = id
        self.runID = runID
        self.kind = kind
        self.text = text
        self.questionID = questionID
    }
}

public struct TaskCommandReceipt: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var runID: String
    public var kind: TaskControlKind
    public var text: String?
    public var questionID: String?
    public var position: Int?
    public var revision: Int?

    enum CodingKeys: String, CodingKey {
        case id
        case runID = "run_id"
        case kind
        case text
        case questionID = "question_id"
        case position
        case revision
    }
}

public struct TaskCommandResponse: Sendable, Codable, Hashable {
    public var command: TaskCommandReceipt?
    public var run: Run?
    public var accepted: Bool?
    public var retryable: Bool?
    public var error: String?
}


public struct ReadPosition: Sendable, Codable, Hashable {
    public var messageID: String
    public var through: String
    public var position: Int

    enum CodingKeys: String, CodingKey {
        case messageID = "message_id"
        case through
        case position
    }

    public init(messageID: String, through: String, position: Int) {
        self.messageID = messageID
        self.through = through
        self.position = position
    }
}

public struct Conversation: Sendable, Codable, Hashable {
    public var introducing: Bool?
    public var pending: Bool
    public var processing: Bool
    public var turnID: String?
    public var inputIDs: [String]
    public var restartCount: Int
    public var routingBlocked: Bool?
    public var frozen: Bool
    public var revision: Int
    public var consumed: Int

    enum CodingKeys: String, CodingKey {
        case introducing
        case pending
        case processing
        case turnID = "turn_id"
        case inputIDs = "input_ids"
        case restartCount = "restart_count"
        case routingBlocked = "routing_blocked"
        case frozen
        case revision
        case consumed
    }

    public init(
        introducing: Bool? = nil,
        pending: Bool = false,
        processing: Bool = false,
        turnID: String? = nil,
        inputIDs: [String] = [],
        restartCount: Int = 0,
        routingBlocked: Bool? = nil,
        frozen: Bool = false,
        revision: Int = 0,
        consumed: Int = 0
    ) {
        self.introducing = introducing
        self.pending = pending
        self.processing = processing
        self.turnID = turnID
        self.inputIDs = inputIDs
        self.restartCount = restartCount
        self.routingBlocked = routingBlocked
        self.frozen = frozen
        self.revision = revision
        self.consumed = consumed
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.introducing = try c.decodeIfPresent(Bool.self, forKey: .introducing)
        self.pending = try c.decodeIfPresent(Bool.self, forKey: .pending) ?? false
        self.processing = try c.decodeIfPresent(Bool.self, forKey: .processing) ?? false
        self.turnID = try c.decodeIfPresent(String.self, forKey: .turnID)
        self.inputIDs = try c.decodeIfPresent([String].self, forKey: .inputIDs) ?? []
        self.restartCount = try c.decodeIfPresent(Int.self, forKey: .restartCount) ?? 0
        self.routingBlocked = try c.decodeIfPresent(Bool.self, forKey: .routingBlocked)
        self.frozen = try c.decodeIfPresent(Bool.self, forKey: .frozen) ?? false
        self.revision = try c.decodeIfPresent(Int.self, forKey: .revision) ?? 0
        self.consumed = try c.decodeIfPresent(Int.self, forKey: .consumed) ?? 0
    }
}

public struct Session: Sendable, Codable, Hashable {
    public var messages: [Message]
    public var runs: [Run]
    public var paused: Bool
    public var conversation: Conversation?
    public var nextCursor: String?
    public var results: [SavedResult]?
    public var position: Int?
    public var readPosition: ReadPosition?

    enum CodingKeys: String, CodingKey {
        case messages
        case runs
        case paused
        case conversation
        case nextCursor = "next_cursor"
        case results
        case position
        case readPosition = "read_position"
    }

    public init(
        messages: [Message] = [],
        runs: [Run] = [],
        paused: Bool = false,
        conversation: Conversation? = nil,
        nextCursor: String? = nil,
        results: [SavedResult]? = nil,
        position: Int? = 0,
        readPosition: ReadPosition? = nil
    ) {
        self.messages = messages
        self.runs = runs
        self.paused = paused
        self.conversation = conversation
        self.nextCursor = nextCursor
        self.results = results
        self.position = position
        self.readPosition = readPosition
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.messages = try c.decodeIfPresent([Message].self, forKey: .messages) ?? []
        self.runs = try c.decodeIfPresent([Run].self, forKey: .runs) ?? []
        self.paused = try c.decodeIfPresent(Bool.self, forKey: .paused) ?? false
        self.conversation = try c.decodeIfPresent(Conversation.self, forKey: .conversation)
        self.nextCursor = try c.decodeIfPresent(String.self, forKey: .nextCursor)
        self.results = try c.decodeIfPresent([SavedResult].self, forKey: .results)
        self.position = try c.decodeIfPresent(Int.self, forKey: .position)
        self.readPosition = try c.decodeIfPresent(ReadPosition.self, forKey: .readPosition)
    }
}
