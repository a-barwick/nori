import Foundation

public enum InteractionKind: String, Sendable, Codable, Hashable {
    case question
    case approval
    case accountSelection = "account_selection"
    case signIn = "sign_in"
}

public enum InteractionStatus: String, Sendable, Codable, Hashable {
    case pending
    case resolved
    case expired
    case superseded
}

public struct QuestionOption: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var label: String

    public init(id: String, label: String) {
        self.id = id
        self.label = label
    }
}

public struct QuestionConstraints: Sendable, Codable, Hashable {
    public var maxLength: Int?
    public var pattern: String?

    enum CodingKeys: String, CodingKey {
        case maxLength = "max_length"
        case pattern
    }

    public init(maxLength: Int? = nil, pattern: String? = nil) {
        self.maxLength = maxLength
        self.pattern = pattern
    }
}

public struct QuestionPayload: Sendable, Codable, Hashable {
    public enum InputKind: String, Sendable, Codable, Hashable {
        case text
        case choice
        case multiChoice = "multi_choice"
        case date
        case confirm
    }

    public var input: InputKind
    public var options: [QuestionOption]?
    public var constraints: QuestionConstraints?

    public init(input: InputKind = .text, options: [QuestionOption]? = nil, constraints: QuestionConstraints? = nil) {
        self.input = input
        self.options = options
        self.constraints = constraints
    }
}

public struct ApprovalAccount: Sendable, Codable, Hashable {
    public var connectionID: String
    public var label: String

    enum CodingKeys: String, CodingKey {
        case connectionID = "connection_id"
        case label
    }

    public init(connectionID: String, label: String) {
        self.connectionID = connectionID
        self.label = label
    }
}

public struct ApprovalPayload: Sendable, Codable, Hashable {
    public var summary: String
    public var toolID: String?
    public var warning: String?
    public var effect: Effect
    public var account: ApprovalAccount
    public var resources: [ResourceRef]
    public var recipients: [String]?
    public var materialEffects: [String]?
    /// Optional rich subject/heading for presentation when summary contains body text.
    public var summaryHead: String?

    enum CodingKeys: String, CodingKey {
        case summary
        case toolID = "tool_id"
        case warning
        case effect
        case account
        case resources
        case recipients
        case materialEffects = "material_effects"
        case summaryHead = "summary_head"
    }

    public init(
        summary: String,
        toolID: String? = nil,
        warning: String? = nil,
        effect: Effect,
        account: ApprovalAccount,
        resources: [ResourceRef] = [],
        recipients: [String]? = nil,
        materialEffects: [String]? = nil,
        summaryHead: String? = nil
    ) {
        self.summary = summary
        self.toolID = toolID
        self.warning = warning
        self.effect = effect
        self.account = account
        self.resources = resources
        self.recipients = recipients
        self.materialEffects = materialEffects
        self.summaryHead = summaryHead
    }

    public var hasAccountLabel: Bool {
        !account.label.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    public var isScheduleCreation: Bool {
        let normalized = (toolID ?? "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return normalized.contains("automation-create")
            || normalized.contains("automation_create")
            || normalized.hasPrefix("nori/assistant/automation")
    }

    public var isInternalNoriSchedule: Bool {
        isScheduleCreation
            && account.connectionID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !hasAccountLabel
    }

    /// Warning text filtered for calm presentation.
    /// Suppresses the generic "This modifies external records." sentence for internal Nori schedules.
    public var displayWarning: String? {
        guard var trimmed = warning?.trimmingCharacters(in: .whitespacesAndNewlines), !trimmed.isEmpty else {
            return nil
        }
        if isInternalNoriSchedule {
            let externalPrefixes = [
                "this modifies external records.",
                "this modifies external records"
            ]
            let lower = trimmed.lowercased()
            for prefix in externalPrefixes where lower.hasPrefix(prefix) {
                trimmed = String(trimmed.dropFirst(prefix.count))
                    .trimmingCharacters(in: .whitespacesAndNewlines)
                break
            }
            return trimmed.isEmpty ? nil : trimmed
        }
        return trimmed
    }
}

public struct AccountSelectionPayload: Sendable, Codable, Hashable {
    public var purpose: String
    public var eligibleConnectionIDs: [String]
    public var rememberAsDefault: Bool?

    enum CodingKeys: String, CodingKey {
        case purpose
        case eligibleConnectionIDs = "eligible_connection_ids"
        case rememberAsDefault = "remember_as_default"
    }

    public init(purpose: String, eligibleConnectionIDs: [String], rememberAsDefault: Bool? = nil) {
        self.purpose = purpose
        self.eligibleConnectionIDs = eligibleConnectionIDs
        self.rememberAsDefault = rememberAsDefault
    }
}

public struct SignInPayload: Sendable, Codable, Hashable {
    public enum AuthKind: String, Sendable, Codable, Hashable {
        case oauth
        case appPassword = "app_password"
    }

    public var auth: AuthKind
    public var authAttemptID: String?
    public var integrationID: String
    public var connectionID: String?
    public var authorizeURL: String?
    public var requestedScopes: [String]?
    public var returnTo: String?
    public var expiresAt: String?

    enum CodingKeys: String, CodingKey {
        case auth
        case authAttemptID = "auth_attempt_id"
        case integrationID = "integration_id"
        case connectionID = "connection_id"
        case authorizeURL = "authorize_url"
        case requestedScopes = "requested_scopes"
        case returnTo = "return_to"
        case expiresAt = "expires_at"
    }

    public init(
        auth: AuthKind = .oauth,
        authAttemptID: String? = nil,
        integrationID: String,
        connectionID: String? = nil,
        authorizeURL: String? = nil,
        requestedScopes: [String]? = nil,
        returnTo: String? = nil,
        expiresAt: String? = nil
    ) {
        self.auth = auth
        self.authAttemptID = authAttemptID
        self.integrationID = integrationID
        self.connectionID = connectionID
        self.authorizeURL = authorizeURL
        self.requestedScopes = requestedScopes
        self.returnTo = returnTo
        self.expiresAt = expiresAt
    }
}

public enum InteractionPayload: Sendable, Hashable {
    case question(QuestionPayload)
    case approval(ApprovalPayload)
    case accountSelection(AccountSelectionPayload)
    case signIn(SignInPayload)
    case unknown(AnyJSON)
}

public struct InteractionResolution: Sendable, Codable, Hashable {
    public enum Kind: String, Sendable, Codable, Hashable {
        case answer
        case approve
        case decline
        case chooseAccount = "choose_account"
        case signedIn = "signed_in"
        case expired
    }

    public var kind: Kind
    public var text: String?
    public var connectionID: String?
    public var at: String

    enum CodingKeys: String, CodingKey {
        case kind
        case text
        case connectionID = "connection_id"
        case at
    }

    public init(kind: Kind, text: String? = nil, connectionID: String? = nil, at: String = ISO8601DateFormatter().string(from: Date())) {
        self.kind = kind
        self.text = text
        self.connectionID = connectionID
        self.at = at
    }
}

public struct Interaction: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var revision: Int
    public var kind: InteractionKind
    public var taskID: String?
    public var instructionRevision: Int?
    public var conversationID: String?
    public var automationID: String?
    public var status: InteractionStatus
    public var prompt: String
    public var payload: InteractionPayload
    public var actionDigest: String?
    public var expiresAt: String?
    public var createdAt: String?
    public var resolvedAt: String?
    public var legacyQuestionID: String?
    public var supersededBy: String?
    public var taskTitle: String?
    public var resolution: InteractionResolution?

    enum CodingKeys: String, CodingKey {
        case id
        case revision
        case kind
        case taskID = "task_id"
        case instructionRevision = "instruction_revision"
        case conversationID = "conversation_id"
        case automationID = "automation_id"
        case status
        case prompt
        case payload
        case actionDigest = "action_digest"
        case expiresAt = "expires_at"
        case createdAt = "created_at"
        case resolvedAt = "resolved_at"
        case legacyQuestionID = "legacy_question_id"
        case supersededBy = "superseded_by"
        case taskTitle = "task_title"
        case resolution
    }

    public init(
        id: String,
        revision: Int = 1,
        kind: InteractionKind,
        taskID: String? = nil,
        instructionRevision: Int? = nil,
        conversationID: String? = nil,
        automationID: String? = nil,
        status: InteractionStatus = .pending,
        prompt: String,
        payload: InteractionPayload,
        actionDigest: String? = nil,
        expiresAt: String? = nil,
        createdAt: String? = nil,
        resolvedAt: String? = nil,
        legacyQuestionID: String? = nil,
        supersededBy: String? = nil,
        taskTitle: String? = nil,
        resolution: InteractionResolution? = nil
    ) {
        self.id = id
        self.revision = revision
        self.kind = kind
        self.taskID = taskID
        self.instructionRevision = instructionRevision
        self.conversationID = conversationID
        self.automationID = automationID
        self.status = status
        self.prompt = prompt
        self.payload = payload
        self.actionDigest = actionDigest
        self.expiresAt = expiresAt
        self.createdAt = createdAt
        self.resolvedAt = resolvedAt
        self.legacyQuestionID = legacyQuestionID
        self.supersededBy = supersededBy
        self.taskTitle = taskTitle
        self.resolution = resolution
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.id = try c.decode(String.self, forKey: .id)
        self.revision = try c.decodeIfPresent(Int.self, forKey: .revision) ?? 1
        self.kind = try c.decode(InteractionKind.self, forKey: .kind)
        self.taskID = try c.decodeIfPresent(String.self, forKey: .taskID)
        self.instructionRevision = try c.decodeIfPresent(Int.self, forKey: .instructionRevision)
        self.conversationID = try c.decodeIfPresent(String.self, forKey: .conversationID)
        self.automationID = try c.decodeIfPresent(String.self, forKey: .automationID)
        self.status = try c.decodeIfPresent(InteractionStatus.self, forKey: .status) ?? .pending
        self.prompt = try c.decodeIfPresent(String.self, forKey: .prompt) ?? ""
        self.actionDigest = try c.decodeIfPresent(String.self, forKey: .actionDigest)
        self.expiresAt = try c.decodeIfPresent(String.self, forKey: .expiresAt)
        self.createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt)
        self.resolvedAt = try c.decodeIfPresent(String.self, forKey: .resolvedAt)
        self.legacyQuestionID = try c.decodeIfPresent(String.self, forKey: .legacyQuestionID)
        self.supersededBy = try c.decodeIfPresent(String.self, forKey: .supersededBy)
        self.taskTitle = try c.decodeIfPresent(String.self, forKey: .taskTitle)
        self.resolution = try c.decodeIfPresent(InteractionResolution.self, forKey: .resolution)

        switch self.kind {
        case .question:
            if let q = try? c.decode(QuestionPayload.self, forKey: .payload) {
                self.payload = .question(q)
            } else {
                self.payload = .unknown((try? c.decode(AnyJSON.self, forKey: .payload)) ?? .null)
            }
        case .approval:
            if let a = try? c.decode(ApprovalPayload.self, forKey: .payload) {
                self.payload = .approval(a)
            } else {
                self.payload = .unknown((try? c.decode(AnyJSON.self, forKey: .payload)) ?? .null)
            }
        case .accountSelection:
            if let acc = try? c.decode(AccountSelectionPayload.self, forKey: .payload) {
                self.payload = .accountSelection(acc)
            } else {
                self.payload = .unknown((try? c.decode(AnyJSON.self, forKey: .payload)) ?? .null)
            }
        case .signIn:
            if let s = try? c.decode(SignInPayload.self, forKey: .payload) {
                self.payload = .signIn(s)
            } else {
                self.payload = .unknown((try? c.decode(AnyJSON.self, forKey: .payload)) ?? .null)
            }
        }
    }

    public func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(revision, forKey: .revision)
        try c.encode(kind, forKey: .kind)
        try c.encodeIfPresent(taskID, forKey: .taskID)
        try c.encodeIfPresent(instructionRevision, forKey: .instructionRevision)
        try c.encodeIfPresent(conversationID, forKey: .conversationID)
        try c.encodeIfPresent(automationID, forKey: .automationID)
        try c.encode(status, forKey: .status)
        try c.encode(prompt, forKey: .prompt)
        try c.encodeIfPresent(actionDigest, forKey: .actionDigest)
        try c.encodeIfPresent(expiresAt, forKey: .expiresAt)
        try c.encodeIfPresent(createdAt, forKey: .createdAt)
        try c.encodeIfPresent(resolvedAt, forKey: .resolvedAt)
        try c.encodeIfPresent(legacyQuestionID, forKey: .legacyQuestionID)
        try c.encodeIfPresent(supersededBy, forKey: .supersededBy)
        try c.encodeIfPresent(taskTitle, forKey: .taskTitle)
        try c.encodeIfPresent(resolution, forKey: .resolution)

        switch payload {
        case .question(let q): try c.encode(q, forKey: .payload)
        case .approval(let a): try c.encode(a, forKey: .payload)
        case .accountSelection(let acc): try c.encode(acc, forKey: .payload)
        case .signIn(let s): try c.encode(s, forKey: .payload)
        case .unknown(let json): try c.encode(json, forKey: .payload)
        }
    }
}

public enum InteractionAnswer: Sendable, Hashable, Encodable {
    case answer(text: String, optionIDs: [String]? = nil)
    case approve(actionDigest: String, rememberRule: Bool = false)
    case decline(actionDigest: String?)
    case chooseAccount(connectionID: String, rememberAsDefault: Bool = false)

    public var payloadDictionary: [String: AnyJSON] {
        switch self {
        case .answer(let text, let optionIDs):
            var dict: [String: AnyJSON] = ["kind": .string("answer"), "text": .string(text)]
            if let optionIDs, !optionIDs.isEmpty {
                dict["option_ids"] = .array(optionIDs.map { .string($0) })
            }
            return dict
        case .approve(let digest, let rememberRule):
            var dict: [String: AnyJSON] = [
                "kind": .string("approve"),
                "action_digest": .string(digest)
            ]
            if rememberRule {
                dict["remember_rule"] = .bool(true)
            }
            return dict
        case .decline(let digest):
            var dict: [String: AnyJSON] = ["kind": .string("decline")]
            if let digest, !digest.isEmpty {
                dict["action_digest"] = .string(digest)
            }
            return dict
        case .chooseAccount(let connectionID, let remember):
            return [
                "kind": .string("choose_account"),
                "connection_id": .string(connectionID),
                "remember_as_default": .bool(remember)
            ]
        }
    }

    public func encode(to encoder: any Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(payloadDictionary)
    }
}
