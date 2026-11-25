import Foundation

// MARK: - Generic JSON Value

public enum AnyJSON: Sendable, Hashable, Codable {
    case null
    case bool(Bool)
    case int(Int)
    case double(Double)
    case string(String)
    case array([AnyJSON])
    case object([String: AnyJSON])

    public init(from decoder: any Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let value = try? container.decode(Bool.self) {
            self = .bool(value)
        } else if let value = try? container.decode(Int.self) {
            self = .int(value)
        } else if let value = try? container.decode(Double.self) {
            self = .double(value)
        } else if let value = try? container.decode(String.self) {
            self = .string(value)
        } else if let value = try? container.decode([AnyJSON].self) {
            self = .array(value)
        } else if let value = try? container.decode([String: AnyJSON].self) {
            self = .object(value)
        } else {
            throw DecodingError.dataCorruptedError(in: container, debugDescription: "Unsupported JSON value")
        }
    }

    public func encode(to encoder: any Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .null:
            try container.encodeNil()
        case .bool(let value):
            try container.encode(value)
        case .int(let value):
            try container.encode(value)
        case .double(let value):
            try container.encode(value)
        case .string(let value):
            try container.encode(value)
        case .array(let value):
            try container.encode(value)
        case .object(let value):
            try container.encode(value)
        }
    }

    public var stringValue: String? {
        if case .string(let value) = self { return value }
        return nil
    }

    public var objectValue: [String: AnyJSON]? {
        if case .object(let value) = self { return value }
        return nil
    }
}

// MARK: - Contract Enums & Shared Value Types

public enum Effect: String, Sendable, Codable, Hashable, CaseIterable {
    case read
    case reversibleWrite = "reversible_write"
    case sensitiveSend = "sensitive_send"
    case unknown
    case control

    public var displayLabel: String {
        switch self {
        case .read: return "Read"
        case .reversibleWrite: return "Reversible write"
        case .sensitiveSend: return "Sensitive send"
        case .unknown: return "Unknown effect"
        case .control: return "Control"
        }
    }
}

public enum Outcome: String, Sendable, Codable, Hashable, CaseIterable {
    case pending
    case ready
    case attempted
    case submitted
    case confirmed
    case failed
    case uncertain
    case skipped
    case voided

    public var displayLabel: String {
        switch self {
        case .pending: return "Pending"
        case .ready: return "Ready"
        case .attempted: return "Attempted"
        case .submitted: return "Submitted"
        case .confirmed: return "Confirmed"
        case .failed: return "Failed"
        case .uncertain: return "Uncertain"
        case .skipped: return "Skipped"
        case .voided: return "Voided"
        }
    }
}

public struct DefinitionRef: Sendable, Codable, Hashable {
    public var id: String
    public var version: String
    public var digest: String

    public init(id: String, version: String, digest: String) {
        self.id = id
        self.version = version
        self.digest = digest
    }
}

public struct ResourceRef: Sendable, Codable, Hashable {
    public var connectionID: String
    public var kind: String
    public var providerID: String
    public var providerRevision: String?

    enum CodingKeys: String, CodingKey {
        case connectionID = "connection_id"
        case kind
        case providerID = "provider_id"
        case providerRevision = "provider_revision"
    }

    public init(connectionID: String, kind: String, providerID: String, providerRevision: String? = nil) {
        self.connectionID = connectionID
        self.kind = kind
        self.providerID = providerID
        self.providerRevision = providerRevision
    }
}

public struct ErrorDetail: Sendable, Codable, Hashable {
    public var code: String
    public var message: String
    public var retryable: Bool

    public init(code: String = "error", message: String, retryable: Bool = false) {
        self.code = code
        self.message = message
        self.retryable = retryable
    }
}

public struct Cost: Sendable, Codable, Hashable {
    public enum Status: String, Sendable, Codable, Hashable {
        case known
        case unknown
        case notApplicable = "not_applicable"
    }

    public var amountUSD: Double?
    public var status: Status

    enum CodingKeys: String, CodingKey {
        case amountUSD = "amount_usd"
        case status
    }

    public init(amountUSD: Double? = nil, status: Status = .known) {
        self.amountUSD = amountUSD
        self.status = status
    }
}

public struct AuthAttempt: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var authorizeURL: String
    public var expiresAt: String

    enum CodingKeys: String, CodingKey {
        case id
        case authorizeURL = "authorize_url"
        case expiresAt = "expires_at"
    }

    public init(id: String, authorizeURL: String, expiresAt: String) {
        self.id = id
        self.authorizeURL = authorizeURL
        self.expiresAt = expiresAt
    }
}

public struct WorkRef: Sendable, Codable, Hashable {
    public var taskID: String
    public var operationID: String?

    enum CodingKeys: String, CodingKey {
        case taskID = "task_id"
        case operationID = "operation_id"
    }

    public init(taskID: String, operationID: String? = nil) {
        self.taskID = taskID
        self.operationID = operationID
    }
}

public struct MutationReceipt<Resource: Sendable & Codable & Hashable>: Sendable, Codable, Hashable {
    public enum Status: String, Sendable, Codable, Hashable {
        case applied
        case queued
    }

    public var requestID: String
    public var accepted: Bool
    public var status: Status
    public var resource: Resource
    public var work: WorkRef?
    public var auth: AuthAttempt?

    enum CodingKeys: String, CodingKey {
        case requestID = "request_id"
        case accepted
        case status
        case resource
        case work
        case auth
    }

    public init(
        requestID: String,
        accepted: Bool = true,
        status: Status = .applied,
        resource: Resource,
        work: WorkRef? = nil,
        auth: AuthAttempt? = nil
    ) {
        self.requestID = requestID
        self.accepted = accepted
        self.status = status
        self.resource = resource
        self.work = work
        self.auth = auth
    }
}

public struct APIErrorPayload: Sendable, Codable, Hashable {
    public var error: String
    public var code: String?
    public var retryable: Bool?
    public var accepted: Bool?
    public var requestID: String?
    public var current: AnyJSON?

    enum CodingKeys: String, CodingKey {
        case error
        case code
        case retryable
        case accepted
        case requestID = "request_id"
        case current
    }

    public init(
        error: String,
        code: String? = nil,
        retryable: Bool? = nil,
        accepted: Bool? = nil,
        requestID: String? = nil,
        current: AnyJSON? = nil
    ) {
        self.error = error
        self.code = code
        self.retryable = retryable
        self.accepted = accepted
        self.requestID = requestID
        self.current = current
    }
}

public struct NoriAPIError: Error, LocalizedError, Sendable, Hashable {
    public var message: String
    public var statusCode: Int
    public var code: String?
    public var retryable: Bool
    public var accepted: Bool
    public var requestID: String?

    public var isRevisionConflict: Bool {
        statusCode == 409 || code == "revision_conflict" || code == "interaction_stale"
    }

    public var errorDescription: String? { message }

    public init(
        message: String,
        statusCode: Int = 0,
        code: String? = nil,
        retryable: Bool = false,
        accepted: Bool = false,
        requestID: String? = nil
    ) {
        self.message = message
        self.statusCode = statusCode
        self.code = code
        self.retryable = retryable
        self.accepted = accepted
        self.requestID = requestID
    }
}

public struct ListResponse<Item: Sendable & Codable & Hashable>: Sendable, Codable, Hashable {
    public var items: [Item]
    public var nextCursor: String?

    enum CodingKeys: String, CodingKey {
        case items
        case nextCursor = "next_cursor"
    }

    public init(items: [Item], nextCursor: String? = nil) {
        self.items = items
        self.nextCursor = nextCursor
    }
}

// MARK: - Capabilities & Summary

public struct UploadAccept: Sendable, Codable, Hashable {
    public var mediaTypes: [String]
    public var maxBytes: Int
    public var extensions: [String]?

    enum CodingKeys: String, CodingKey {
        case mediaTypes = "media_types"
        case maxBytes = "max_bytes"
        case extensions
    }

    public init(mediaTypes: [String], maxBytes: Int, extensions: [String]? = nil) {
        self.mediaTypes = mediaTypes
        self.maxBytes = maxBytes
        self.extensions = extensions
    }
}

public struct Capabilities: Sendable, Codable, Hashable {
    public var contract: String
    public var conversationContract: String?
    public var service: String
    public var persistence: String?
    public var provider: String?
    public var model: String?
    public var conversationModes: [String]
    public var features: [String: Bool]
    public var assistantContract: String?
    public var assistantFeatures: [String: Bool]?
    public var uploadAccept: UploadAccept?

    enum CodingKeys: String, CodingKey {
        case contract
        case conversationContract
        case service
        case persistence
        case provider
        case model
        case conversationModes
        case features
        case assistantContract = "assistant_contract"
        case assistantFeatures = "assistant_features"
        case uploadAccept = "upload_accept"
    }

    public init(
        contract: String = "nori-chat-2",
        conversationContract: String? = "nori-conversations-1",
        service: String = "nori",
        persistence: String? = "durable",
        provider: String? = "openai",
        model: String? = nil,
        conversationModes: [String] = ["assistant"],
        features: [String: Bool] = ["backgroundTasks": true],
        assistantContract: String? = "nori-assistant-1",
        assistantFeatures: [String: Bool]? = nil,
        uploadAccept: UploadAccept? = nil
    ) {
        self.contract = contract
        self.conversationContract = conversationContract
        self.service = service
        self.persistence = persistence
        self.provider = provider
        self.model = model
        self.conversationModes = conversationModes
        self.features = features
        self.assistantContract = assistantContract
        self.assistantFeatures = assistantFeatures
        self.uploadAccept = uploadAccept
    }
}

public struct AssistantSummary: Sendable, Codable, Hashable {
    public var revision: Int
    public var needsYou: Int
    public var working: Int
    public var appsAttention: Int
    public var inboxUnread: Int
    public var nextScheduledAt: String?

    enum CodingKeys: String, CodingKey {
        case revision
        case needsYou = "needs_you"
        case working
        case appsAttention = "apps_attention"
        case inboxUnread = "inbox_unread"
        case nextScheduledAt = "next_scheduled_at"
    }

    public init(
        revision: Int = 1,
        needsYou: Int = 0,
        working: Int = 0,
        appsAttention: Int = 0,
        inboxUnread: Int = 0,
        nextScheduledAt: String? = nil
    ) {
        self.revision = revision
        self.needsYou = needsYou
        self.working = working
        self.appsAttention = appsAttention
        self.inboxUnread = inboxUnread
        self.nextScheduledAt = nextScheduledAt
    }
}

// MARK: - Preferences & Permissions

public struct WorkingHoursSlot: Sendable, Codable, Hashable {
    public var day: Int
    public var start: String
    public var end: String

    public init(day: Int, start: String, end: String) {
        self.day = day
        self.start = start
        self.end = end
    }
}

public struct QuietHours: Sendable, Codable, Hashable {
    public var start: String
    public var end: String

    public init(start: String, end: String) {
        self.start = start
        self.end = end
    }
}

public struct BudgetPreferences: Sendable, Codable, Hashable {
    public var taskUSD: Double
    public var monthlyUSD: Double?

    enum CodingKeys: String, CodingKey {
        case taskUSD = "task_usd"
        case monthlyUSD = "monthly_usd"
    }

    public init(taskUSD: Double = 1.0, monthlyUSD: Double? = 25.0) {
        self.taskUSD = taskUSD
        self.monthlyUSD = monthlyUSD
    }
}

public struct InputPreferences: Sendable, Codable, Hashable {
    public var dictationLanguage: String?

    enum CodingKeys: String, CodingKey {
        case dictationLanguage = "dictation_language"
    }

    public init(dictationLanguage: String? = nil) {
        self.dictationLanguage = dictationLanguage
    }
}

public struct Preferences: Sendable, Codable, Hashable {
    public var revision: Int
    public var timezone: String
    public var locale: String?
    public var workingHours: [WorkingHoursSlot]
    public var quietHours: QuietHours?
    public var defaultDestinationIDs: [String]
    public var accountDefaults: [String: String]
    public var budgets: BudgetPreferences
    public var input: InputPreferences?
    public var updatedAt: String?

    enum CodingKeys: String, CodingKey {
        case revision
        case timezone
        case locale
        case workingHours = "working_hours"
        case quietHours = "quiet_hours"
        case defaultDestinationIDs = "default_destination_ids"
        case accountDefaults = "account_defaults"
        case budgets
        case input
        case updatedAt = "updated_at"
    }

    public init(
        revision: Int = 1,
        timezone: String = "America/Los_Angeles",
        locale: String? = "en-US",
        workingHours: [WorkingHoursSlot] = [],
        quietHours: QuietHours? = nil,
        defaultDestinationIDs: [String] = [],
        accountDefaults: [String: String] = [:],
        budgets: BudgetPreferences = BudgetPreferences(),
        input: InputPreferences? = nil,
        updatedAt: String? = nil
    ) {
        self.revision = revision
        self.timezone = timezone
        self.locale = locale
        self.workingHours = workingHours
        self.quietHours = quietHours
        self.defaultDestinationIDs = defaultDestinationIDs
        self.accountDefaults = accountDefaults
        self.budgets = budgets
        self.input = input
        self.updatedAt = updatedAt
    }
}

public struct PermissionRule: Sendable, Codable, Hashable, Identifiable {
    public enum RuleEffect: String, Sendable, Codable, Hashable, CaseIterable {
        case allow
        case ask
        case deny

        public var displayLabel: String {
            switch self {
            case .allow: return "Allow automatically"
            case .ask: return "Ask first"
            case .deny: return "Never allow"
            }
        }

        public var badgeLabel: String {
            switch self {
            case .allow: return "Allowed"
            case .ask: return "Ask first"
            case .deny: return "Never"
            }
        }
    }

    public var id: String
    public var revision: Int
    public var effect: RuleEffect
    public var toolIDs: [String]
    public var connectionIDs: [String]
    public var resourceIDs: [String]
    public var actionClasses: [Effect]
    public var recipients: [String]?
    public var automationID: String?
    public var expiresAt: String?
    public var maxOperations: Int?
    public var title: String?
    public var createdAt: String?
    public var updatedAt: String?
    public var goalID: String?
    public var summary: String?

    enum CodingKeys: String, CodingKey {
        case id
        case revision
        case effect
        case toolIDs = "tool_ids"
        case connectionIDs = "connection_ids"
        case resourceIDs = "resource_ids"
        case actionClasses = "action_classes"
        case recipients
        case automationID = "automation_id"
        case expiresAt = "expires_at"
        case maxOperations = "max_operations"
        case title
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case goalID = "goal_id"
        case summary
    }

    public init(
        id: String,
        revision: Int = 1,
        effect: RuleEffect,
        toolIDs: [String] = [],
        connectionIDs: [String] = [],
        resourceIDs: [String] = [],
        actionClasses: [Effect] = [],
        recipients: [String]? = nil,
        automationID: String? = nil,
        expiresAt: String? = nil,
        maxOperations: Int? = nil,
        title: String? = nil,
        createdAt: String? = nil,
        updatedAt: String? = nil,
        goalID: String? = nil,
        summary: String? = nil
    ) {
        self.id = id
        self.revision = revision
        self.effect = effect
        self.toolIDs = toolIDs
        self.connectionIDs = connectionIDs
        self.resourceIDs = resourceIDs
        self.actionClasses = actionClasses
        self.recipients = recipients
        self.automationID = automationID
        self.expiresAt = expiresAt
        self.maxOperations = maxOperations
        self.title = title
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.goalID = goalID
        self.summary = summary
    }
}

public struct PermissionRuleEditable: Sendable, Codable, Hashable {
    public var effect: PermissionRule.RuleEffect
    public var toolIDs: [String]
    public var connectionIDs: [String]
    public var resourceIDs: [String]
    public var actionClasses: [Effect]
    public var recipients: [String]?
    public var automationID: String?
    public var expiresAt: String?
    public var maxOperations: Int?
    public var title: String?
    public var goalID: String?

    enum CodingKeys: String, CodingKey {
        case effect
        case toolIDs = "tool_ids"
        case connectionIDs = "connection_ids"
        case resourceIDs = "resource_ids"
        case actionClasses = "action_classes"
        case recipients
        case automationID = "automation_id"
        case expiresAt = "expires_at"
        case maxOperations = "max_operations"
        case title
        case goalID = "goal_id"
    }

    public init(
        effect: PermissionRule.RuleEffect,
        toolIDs: [String] = ["*"],
        connectionIDs: [String] = ["*"],
        resourceIDs: [String] = ["*"],
        actionClasses: [Effect],
        recipients: [String]? = nil,
        automationID: String? = nil,
        expiresAt: String? = nil,
        maxOperations: Int? = nil,
        title: String? = nil,
        goalID: String? = nil
    ) {
        self.effect = effect
        self.toolIDs = toolIDs
        self.connectionIDs = connectionIDs
        self.resourceIDs = resourceIDs
        self.actionClasses = actionClasses
        self.recipients = recipients
        self.automationID = automationID
        self.expiresAt = expiresAt
        self.maxOperations = maxOperations
        self.title = title
        self.goalID = goalID
    }
}
