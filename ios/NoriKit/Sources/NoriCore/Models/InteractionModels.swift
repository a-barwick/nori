import Foundation

public enum InteractionKind: String, Sendable, Codable, Hashable {
    case question
    case approval
    case accountSelection = "account_selection"
    case signIn = "sign_in"
    case computerTakeover = "computer_takeover"
}

public enum InteractionStatus: String, Sendable, Codable, Hashable {
    case pending
    case resolved
    case expired
    case superseded
}

public enum InteractionSurface: String, Sendable, Codable, Hashable {
    case chat
    case work
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

public struct CardPart: Sendable, Codable, Hashable {
    public var type: String
    public var schemaVersion: Int
    public var kind: String
    public var resource: CardResourceRef
    public var actions: [String]?
    public var fallback: String
    public var display: String?
    public var id: String?

    enum CodingKeys: String, CodingKey {
        case type
        case schemaVersion = "schema_version"
        case kind
        case resource
        case actions
        case fallback
        case display
        case id
    }

    public init(
        type: String = "card",
        schemaVersion: Int = 1,
        kind: String,
        resource: CardResourceRef,
        actions: [String]? = nil,
        fallback: String,
        display: String? = nil,
        id: String? = nil
    ) {
        self.type = type
        self.schemaVersion = schemaVersion
        self.kind = kind
        self.resource = resource
        self.actions = actions
        self.fallback = fallback
        self.display = display
        self.id = id
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
    public var approvalOnce: Bool?
    public var card: CardPart?
    public var reason: String?
    public var automationTitle: String?
    public var projectedDailyUSD: Double?
    public var dailyCapUSD: Double?

    enum CodingKeys: String, CodingKey {
        case summary
        case toolID = "tool_id"
        case warning
        case effect
        case account
        case resources
        case recipients
        case materialEffects = "material_effects"
        case approvalOnce = "approval_once"
        case card
        case reason
        case automationTitle = "automation_title"
        case projectedDailyUSD = "projected_daily_usd"
        case dailyCapUSD = "daily_cap_usd"
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
        approvalOnce: Bool? = nil,
        card: CardPart? = nil,
        reason: String? = nil,
        automationTitle: String? = nil,
        projectedDailyUSD: Double? = nil,
        dailyCapUSD: Double? = nil
    ) {
        self.summary = summary
        self.toolID = toolID
        self.warning = warning
        self.effect = effect
        self.account = account
        self.resources = resources
        self.recipients = recipients
        self.materialEffects = materialEffects
        self.approvalOnce = approvalOnce
        self.card = card
        self.reason = reason
        self.automationTitle = automationTitle
        self.projectedDailyUSD = projectedDailyUSD
        self.dailyCapUSD = dailyCapUSD
    }

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.summary = try c.decodeIfPresent(String.self, forKey: .summary) ?? ""
        self.toolID = try c.decodeIfPresent(String.self, forKey: .toolID)
        self.warning = try c.decodeIfPresent(String.self, forKey: .warning)
        self.effect = try c.decodeIfPresent(Effect.self, forKey: .effect) ?? .reversibleWrite
        self.account = try c.decodeIfPresent(ApprovalAccount.self, forKey: .account)
            ?? ApprovalAccount(connectionID: "", label: "")
        self.resources = try c.decodeIfPresent([ResourceRef].self, forKey: .resources) ?? []
        self.recipients = try c.decodeIfPresent([String].self, forKey: .recipients)
        self.materialEffects = try c.decodeIfPresent([String].self, forKey: .materialEffects)
        self.approvalOnce = try c.decodeIfPresent(Bool.self, forKey: .approvalOnce)
        self.card = try c.decodeIfPresent(CardPart.self, forKey: .card)
        self.reason = try c.decodeIfPresent(String.self, forKey: .reason)
        self.automationTitle = try c.decodeIfPresent(String.self, forKey: .automationTitle)
        self.projectedDailyUSD = try c.decodeIfPresent(Double.self, forKey: .projectedDailyUSD)
        self.dailyCapUSD = try c.decodeIfPresent(Double.self, forKey: .dailyCapUSD)
    }

    public var hasAccountLabel: Bool {
        !account.label.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    public var isBudgetConfirmation: Bool {
        let normalizedTool = (toolID ?? "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let normalizedReason = (reason ?? "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return normalizedTool == "nori/assistant/proactive-budget" || normalizedReason == "budget_exceeded"
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

    public var displaySummary: String {
        let trimmed = summary.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty {
            return summary
        }
        if isBudgetConfirmation,
           let title = automationTitle?.trimmingCharacters(in: .whitespacesAndNewlines),
           !title.isEmpty {
            return "Allow \"\(title)\" to run proactively."
        }
        return card?.fallback ?? ""
    }

    public var displayWarning: String? {
        if var trimmed = warning?.trimmingCharacters(in: .whitespacesAndNewlines), !trimmed.isEmpty {
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
        if isBudgetConfirmation,
           let projected = projectedDailyUSD,
           let cap = dailyCapUSD {
            return String(
                format: "Projected daily proactive spend is $%.2f/day, above your $%.2f/day limit.",
                projected,
                cap
            )
        }
        return nil
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

public struct ComputerTakeoverPayload: Sendable, Codable, Hashable {
    public var reason: String?
    public var url: String?
    public var computerID: String?
    public var taskID: String?

    enum CodingKeys: String, CodingKey {
        case reason
        case url
        case computerID = "computer_id"
        case taskID = "task_id"
    }

    public init(
        reason: String? = nil,
        url: String? = nil,
        computerID: String? = nil,
        taskID: String? = nil
    ) {
        self.reason = reason
        self.url = url
        self.computerID = computerID
        self.taskID = taskID
    }
}

public enum InteractionPayload: Sendable, Hashable {
    case question(QuestionPayload)
    case approval(ApprovalPayload)
    case accountSelection(AccountSelectionPayload)
    case signIn(SignInPayload)
    case computerTakeover(ComputerTakeoverPayload)
    case unknown(AnyJSON)
}

public struct InteractionResolution: Sendable, Codable, Hashable {
    public enum Kind: String, Sendable, Codable, Hashable {
        case answer
        case approve
        case decline
        case chooseAccount = "choose_account"
        case signedIn = "signed_in"
        case resume
        case cancel
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
    public var surface: InteractionSurface?
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
        case surface
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
        surface: InteractionSurface? = nil,
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
        self.surface = surface
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
        self.surface = try? c.decodeIfPresent(InteractionSurface.self, forKey: .surface)
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
        case .computerTakeover:
            if let t = try? c.decode(ComputerTakeoverPayload.self, forKey: .payload) {
                self.payload = .computerTakeover(t)
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
        try c.encodeIfPresent(surface, forKey: .surface)
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
        case .computerTakeover(let t): try c.encode(t, forKey: .payload)
        case .unknown(let json): try c.encode(json, forKey: .payload)
        }
    }
}

public struct EditedApprovalFields: Sendable, Codable, Hashable {
    public var subject: String?
    public var body: String?

    public init(subject: String? = nil, body: String? = nil) {
        self.subject = subject
        self.body = body
    }

    public var isEmpty: Bool {
        subject == nil && body == nil
    }

    public var jsonObject: [String: AnyJSON] {
        var dict: [String: AnyJSON] = [:]
        if let subject {
            dict["subject"] = .string(subject)
        }
        if let body {
            dict["body"] = .string(body)
        }
        return dict
    }
}

public enum InteractionAnswer: Sendable, Hashable, Encodable {
    case answer(text: String, optionIDs: [String]? = nil)
    case approve(actionDigest: String, rememberRule: Bool = false, editedFields: EditedApprovalFields? = nil)
    case decline(actionDigest: String?)
    case chooseAccount(connectionID: String, rememberAsDefault: Bool = false)
    case resume(text: String? = nil)
    case cancel(text: String? = nil)

    public var payloadDictionary: [String: AnyJSON] {
        switch self {
        case .answer(let text, let optionIDs):
            var dict: [String: AnyJSON] = ["kind": .string("answer"), "text": .string(text)]
            if let optionIDs, !optionIDs.isEmpty {
                dict["option_ids"] = .array(optionIDs.map { .string($0) })
            }
            return dict
        case .approve(let digest, let rememberRule, let editedFields):
            var dict: [String: AnyJSON] = [
                "kind": .string("approve"),
                "action_digest": .string(digest)
            ]
            if let editedFields, !editedFields.isEmpty {
                dict["edited_fields"] = .object(editedFields.jsonObject)
            } else if rememberRule {
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
        case .resume(let text):
            var dict: [String: AnyJSON] = ["kind": .string("resume")]
            if let text, !text.isEmpty {
                dict["text"] = .string(text)
            }
            return dict
        case .cancel(let text):
            var dict: [String: AnyJSON] = ["kind": .string("cancel")]
            if let text, !text.isEmpty {
                dict["text"] = .string(text)
            }
            return dict
        }
    }

    public func encode(to encoder: any Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(payloadDictionary)
    }
}
