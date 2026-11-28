// Code generated from api/openapi.yaml by scripts/gen-swift-openapi.mjs; DO NOT EDIT.
import Foundation

public enum OpenAPI {
    public enum Path {
        private static func encodeSegment(_ raw: String) -> String {
            var allowed = CharacterSet.urlPathAllowed
            allowed.remove(charactersIn: "/")
            return raw.addingPercentEncoding(withAllowedCharacters: allowed) ?? raw
        }

        public static let assistantSummary = "/v1/assistant/summary"
        public static let automations = "/v1/automations"
        public static let automationsPreview = "/v1/automations/preview"
        public static func automationsByID(id: String) -> String {
            "/v1/automations/\(encodeSegment(id))"
        }
        public static func automationsActions(id: String) -> String {
            "/v1/automations/\(encodeSegment(id))/actions"
        }
        public static func automationsOccurrences(id: String) -> String {
            "/v1/automations/\(encodeSegment(id))/occurrences"
        }
        public static let capabilities = "/v1/capabilities"
        public static let computer = "/v1/computer"
        public static let computerCheckpoints = "/v1/computer/checkpoints"
        public static let computerReset = "/v1/computer/reset"
        public static let computerRestore = "/v1/computer/restore"
        public static let computerSessions = "/v1/computer/sessions"
        public static func computerSessionsByID(id: String) -> String {
            "/v1/computer/sessions/\(encodeSegment(id))"
        }
        public static func computerSessionsWs(id: String) -> String {
            "/v1/computer/sessions/\(encodeSegment(id))/ws"
        }
        public static let computerSleep = "/v1/computer/sleep"
        public static let computerWake = "/v1/computer/wake"
        public static let connections = "/v1/connections"
        public static let connectionsOAuthCallback = "/v1/connections/oauth/callback"
        public static func connectionsByID(id: String) -> String {
            "/v1/connections/\(encodeSegment(id))"
        }
        public static func connectionsActions(id: String) -> String {
            "/v1/connections/\(encodeSegment(id))/actions"
        }
        public static func connectionsGoogleDrivePicker(id: String) -> String {
            "/v1/connections/\(encodeSegment(id))/google-drive/picker"
        }
        public static func connectionsResources(id: String) -> String {
            "/v1/connections/\(encodeSegment(id))/resources"
        }
        public static let conversationsAssistant = "/v1/conversations/assistant"
        public static let conversationsAssistantCancel = "/v1/conversations/assistant/cancel"
        public static let conversationsAssistantEvents = "/v1/conversations/assistant/events"
        public static let conversationsAssistantMessages = "/v1/conversations/assistant/messages"
        public static let conversationsAssistantOperations = "/v1/conversations/assistant/operations"
        public static let conversationsAssistantRetry = "/v1/conversations/assistant/retry"
        public static let conversationsAssistantUploads = "/v1/conversations/assistant/uploads"
        public static func conversationsAssistantUploadsByID(id: String) -> String {
            "/v1/conversations/assistant/uploads/\(encodeSegment(id))"
        }
        public static let costs = "/v1/costs"
        public static let devicesPush = "/v1/devices/push"
        public static func devicesPushByID(id: String) -> String {
            "/v1/devices/push/\(encodeSegment(id))"
        }
        public static let diagnostics = "/v1/diagnostics"
        public static let diagnosticsActions = "/v1/diagnostics/actions"
        public static let events = "/v1/events"
        public static let feedback = "/v1/feedback"
        public static let inbox = "/v1/inbox"
        public static func inboxActions(id: String) -> String {
            "/v1/inbox/\(encodeSegment(id))/actions"
        }
        public static let integrations = "/v1/integrations"
        public static func integrationsByID(id: String) -> String {
            "/v1/integrations/\(encodeSegment(id))"
        }
        public static let interactions = "/v1/interactions"
        public static func interactionsByID(id: String) -> String {
            "/v1/interactions/\(encodeSegment(id))"
        }
        public static func interactionsResponses(id: String) -> String {
            "/v1/interactions/\(encodeSegment(id))/responses"
        }
        public static let library = "/v1/library"
        public static func libraryByID(id: String) -> String {
            "/v1/library/\(encodeSegment(id))"
        }
        public static func libraryActions(id: String) -> String {
            "/v1/library/\(encodeSegment(id))/actions"
        }
        public static func libraryFile(id: String) -> String {
            "/v1/library/\(encodeSegment(id))/file"
        }
        public static func libraryVersions(id: String) -> String {
            "/v1/library/\(encodeSegment(id))/versions"
        }
        public static let mcp = "/v1/mcp"
        public static func mcpByID(id: String) -> String {
            "/v1/mcp/\(encodeSegment(id))"
        }
        public static func mcpActions(id: String) -> String {
            "/v1/mcp/\(encodeSegment(id))/actions"
        }
        public static func mcpCredential(id: String) -> String {
            "/v1/mcp/\(encodeSegment(id))/credential"
        }
        public static func mcpTools(id: String) -> String {
            "/v1/mcp/\(encodeSegment(id))/tools"
        }
        public static let memory = "/v1/memory"
        public static let memoryActions = "/v1/memory/actions"
        public static let memoryLogs = "/v1/memory/logs"
        public static func memoryLogsByDate(date: String) -> String {
            "/v1/memory/logs/\(encodeSegment(date))"
        }
        public static func memoryLogsActions(date: String, entryID: String) -> String {
            "/v1/memory/logs/\(encodeSegment(date))/\(encodeSegment(entryID))/actions"
        }
        public static let memorySearch = "/v1/memory/search"
        public static let memoryWorking = "/v1/memory/working"
        public static func memoryWorkingActions(itemID: String) -> String {
            "/v1/memory/working/\(encodeSegment(itemID))/actions"
        }
        public static func memoryByID(id: String) -> String {
            "/v1/memory/\(encodeSegment(id))"
        }
        public static func memoryActionsByID(id: String) -> String {
            "/v1/memory/\(encodeSegment(id))/actions"
        }
        public static func mutations(requestID: String) -> String {
            "/v1/mutations/\(encodeSegment(requestID))"
        }
        public static let operations = "/v1/operations"
        public static let permissions = "/v1/permissions"
        public static func permissionsByID(id: String) -> String {
            "/v1/permissions/\(encodeSegment(id))"
        }
        public static func permissionsActions(id: String) -> String {
            "/v1/permissions/\(encodeSegment(id))/actions"
        }
        public static let preferences = "/v1/preferences"
        public static func results(id: String) -> String {
            "/v1/results/\(encodeSegment(id))"
        }
        public static func resultsFile(id: String) -> String {
            "/v1/results/\(encodeSegment(id))/file"
        }
        public static let search = "/v1/search"
        public static let settingsAccountDelete = "/v1/settings/account/delete"
        public static let settingsAPICosts = "/v1/settings/api-costs"
        public static let settingsHomeConnectors = "/v1/settings/home-connectors"
        public static let settingsHomeConnectorsPairingApprove = "/v1/settings/home-connectors/pairing/approve"
        public static let settingsHomeConnectorsPairingLookup = "/v1/settings/home-connectors/pairing/lookup"
        public static func settingsHomeConnectorsByID(id: String) -> String {
            "/v1/settings/home-connectors/\(encodeSegment(id))"
        }
        public static func settingsHomeConnectorsTest(id: String) -> String {
            "/v1/settings/home-connectors/\(encodeSegment(id))/test"
        }
        public static let settingsModelKeys = "/v1/settings/model-keys"
        public static func settingsModelKeysByProvider(provider: String) -> String {
            "/v1/settings/model-keys/\(encodeSegment(provider))"
        }
        public static let settingsModels = "/v1/settings/models"
        public static let settingsReset = "/v1/settings/reset"
        public static let settingsServiceKeys = "/v1/settings/service-keys"
        public static func settingsServiceKeysByService(service: String) -> String {
            "/v1/settings/service-keys/\(encodeSegment(service))"
        }
        public static let snapshot = "/v1/snapshot"
        public static let tools = "/v1/tools"
        public static func toolsByID(id: String) -> String {
            "/v1/tools/\(encodeSegment(id))"
        }
        public static let uploads = "/v1/uploads"
        public static func uploadsByID(id: String) -> String {
            "/v1/uploads/\(encodeSegment(id))"
        }
        public static let work = "/v1/work"
        public static func workByTaskID(taskID: String) -> String {
            "/v1/work/\(encodeSegment(taskID))"
        }
        public static func workActions(taskID: String) -> String {
            "/v1/work/\(encodeSegment(taskID))/actions"
        }
        public static func workOperations(taskID: String) -> String {
            "/v1/work/\(encodeSegment(taskID))/operations"
        }
    }

    public struct APIError: Sendable, Codable, Hashable {
        public var error: String
        public var code: String?
        public var retryable: Bool?
        public var accepted: Bool?
        public var requestID: String?
        public var receipt: OpenAPI.MutationReceipt?
        public var current: AnyJSON?
        public var feature: String?

        public init(
            error: String,
            code: String? = nil,
            retryable: Bool? = nil,
            accepted: Bool? = nil,
            requestID: String? = nil,
            receipt: OpenAPI.MutationReceipt? = nil,
            current: AnyJSON? = nil,
            feature: String? = nil
        ) {
            self.error = error
            self.code = code
            self.retryable = retryable
            self.accepted = accepted
            self.requestID = requestID
            self.receipt = receipt
            self.current = current
            self.feature = feature
        }

        enum CodingKeys: String, CodingKey {
            case error
            case code
            case retryable
            case accepted
            case requestID = "request_id"
            case receipt
            case current
            case feature
        }
    }

    public struct AccountDeleteRequest: Sendable, Codable, Hashable {
        public var confirmation: String
        public var resumePending: Bool?

        public init(
            confirmation: String,
            resumePending: Bool? = nil
        ) {
            self.confirmation = confirmation
            self.resumePending = resumePending
        }

        enum CodingKeys: String, CodingKey {
            case confirmation
            case resumePending = "resume_pending"
        }
    }

    public struct AccountSelectionPayload: Sendable, Codable, Hashable {
        public var purpose: String
        public var eligibleConnectionIDs: [OpenAPI.ID]
        public var rememberAsDefault: Bool?

        public init(
            purpose: String,
            eligibleConnectionIDs: [OpenAPI.ID],
            rememberAsDefault: Bool? = nil
        ) {
            self.purpose = purpose
            self.eligibleConnectionIDs = eligibleConnectionIDs
            self.rememberAsDefault = rememberAsDefault
        }

        enum CodingKeys: String, CodingKey {
            case purpose
            case eligibleConnectionIDs = "eligible_connection_ids"
            case rememberAsDefault = "remember_as_default"
        }
    }

    public struct ApprovalPayload: Sendable, Codable, Hashable {
        public struct Account: Sendable, Codable, Hashable {
            public var connectionID: OpenAPI.ID
            public var label: String

            public init(
                connectionID: OpenAPI.ID,
                label: String
            ) {
                self.connectionID = connectionID
                self.label = label
            }

            enum CodingKeys: String, CodingKey {
                case connectionID = "connection_id"
                case label
            }
        }

        public var summary: String
        public var toolID: String?
        public var warning: String?
        public var effect: OpenAPI.Effect
        public var account: Account
        public var resources: [OpenAPI.ResourceRef]
        public var recipients: [String]?
        public var materialEffects: [String]?
        public var approvalOnce: Bool?
        public var card: OpenAPI.CardPart?

        public init(
            summary: String,
            toolID: String? = nil,
            warning: String? = nil,
            effect: OpenAPI.Effect,
            account: Account,
            resources: [OpenAPI.ResourceRef],
            recipients: [String]? = nil,
            materialEffects: [String]? = nil,
            approvalOnce: Bool? = nil,
            card: OpenAPI.CardPart? = nil
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
        }

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
        }
    }

    public struct Artifact: Sendable, Codable, Hashable {
        public var id: String
        public var kind: String

        public init(
            id: String,
            kind: String
        ) {
            self.id = id
            self.kind = kind
        }
    }

    public struct AssistantExtras: Sendable, Codable, Hashable {
        public var tasks: [OpenAPI.Run]?
        public var results: [OpenAPI.SavedResult]?
        public var readPosition: OpenAPI.ReadPosition

        public init(
            tasks: [OpenAPI.Run]? = nil,
            results: [OpenAPI.SavedResult]? = nil,
            readPosition: OpenAPI.ReadPosition
        ) {
            self.tasks = tasks
            self.results = results
            self.readPosition = readPosition
        }
    }

    public struct AssistantFeatures: Sendable, Codable, Hashable {
        public var integrations: Bool?
        public var connections: Bool?
        public var mcp: Bool?
        public var interactions: Bool?
        public var work: Bool?
        public var automations: Bool?
        public var library: Bool?
        public var search: Bool?
        public var permissions: Bool?
        public var preferences: Bool?
        public var memory: Bool?
        public var inbox: Bool?
        public var operations: Bool?
        public var computer: Bool?

        public init(
            integrations: Bool? = nil,
            connections: Bool? = nil,
            mcp: Bool? = nil,
            interactions: Bool? = nil,
            work: Bool? = nil,
            automations: Bool? = nil,
            library: Bool? = nil,
            search: Bool? = nil,
            permissions: Bool? = nil,
            preferences: Bool? = nil,
            memory: Bool? = nil,
            inbox: Bool? = nil,
            operations: Bool? = nil,
            computer: Bool? = nil
        ) {
            self.integrations = integrations
            self.connections = connections
            self.mcp = mcp
            self.interactions = interactions
            self.work = work
            self.automations = automations
            self.library = library
            self.search = search
            self.permissions = permissions
            self.preferences = preferences
            self.memory = memory
            self.inbox = inbox
            self.operations = operations
            self.computer = computer
        }
    }

    public struct AssistantSummary: Sendable, Codable, Hashable {
        public var revision: OpenAPI.Revision
        public var needsYou: Int
        public var working: Int
        public var appsAttention: Int
        public var inboxUnread: Int
        public var nextScheduledAt: OpenAPI.Instant?

        public init(
            revision: OpenAPI.Revision,
            needsYou: Int,
            working: Int,
            appsAttention: Int,
            inboxUnread: Int,
            nextScheduledAt: OpenAPI.Instant? = nil
        ) {
            self.revision = revision
            self.needsYou = needsYou
            self.working = working
            self.appsAttention = appsAttention
            self.inboxUnread = inboxUnread
            self.nextScheduledAt = nextScheduledAt
        }

        enum CodingKeys: String, CodingKey {
            case revision
            case needsYou = "needs_you"
            case working
            case appsAttention = "apps_attention"
            case inboxUnread = "inbox_unread"
            case nextScheduledAt = "next_scheduled_at"
        }
    }

    public struct Attachment: Sendable, Codable, Hashable {
        public var id: String
        public var name: String
        public var mediaType: String
        public var size: Int

        public init(
            id: String,
            name: String,
            mediaType: String,
            size: Int
        ) {
            self.id = id
            self.name = name
            self.mediaType = mediaType
            self.size = size
        }

        enum CodingKeys: String, CodingKey {
            case id
            case name
            case mediaType = "media_type"
            case size
        }
    }

    public struct Attendee: Sendable, Codable, Hashable {
        public var email: String
        public var name: String?
        public var role: String
        public var response: String
        public var isSelf: Bool?

        public init(
            email: String,
            name: String? = nil,
            role: String,
            response: String,
            isSelf: Bool? = nil
        ) {
            self.email = email
            self.name = name
            self.role = role
            self.response = response
            self.isSelf = isSelf
        }

        enum CodingKeys: String, CodingKey {
            case email
            case name
            case role
            case response
            case isSelf = "self"
        }
    }

    public struct AuthAttempt: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var authorizeURL: String
        public var expiresAt: OpenAPI.Instant

        public init(
            id: OpenAPI.ID,
            authorizeURL: String,
            expiresAt: OpenAPI.Instant
        ) {
            self.id = id
            self.authorizeURL = authorizeURL
            self.expiresAt = expiresAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case authorizeURL = "authorize_url"
            case expiresAt = "expires_at"
        }
    }

    public struct Automation: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var revision: OpenAPI.Revision
        public var title: String
        public var instruction: String
        public var status: String
        public var trigger: OpenAPI.Trigger
        public var connectionIDs: [OpenAPI.ID]
        public var budgetUSD: Double
        public var maxActiveRuns: Int
        public var missedRun: OpenAPI.MissedRun
        public var executionWindowSeconds: Int
        public var destinationIDs: [OpenAPI.ID]
        public var nextAt: OpenAPI.Instant?
        public var endAt: OpenAPI.Instant?
        public var maxOccurrences: Int?
        public var lastOccurrenceID: String?
        public var createdAt: OpenAPI.Instant?
        public var updatedAt: OpenAPI.Instant?
        public var authorityRuleIDs: [OpenAPI.ID]
        public var snoozedUntil: OpenAPI.Instant?
        public var lastOccurrence: OpenAPI.AutomationLastOccurrence?

        public init(
            id: OpenAPI.ID,
            revision: OpenAPI.Revision,
            title: String,
            instruction: String,
            status: String,
            trigger: OpenAPI.Trigger,
            connectionIDs: [OpenAPI.ID],
            budgetUSD: Double,
            maxActiveRuns: Int,
            missedRun: OpenAPI.MissedRun,
            executionWindowSeconds: Int,
            destinationIDs: [OpenAPI.ID],
            nextAt: OpenAPI.Instant? = nil,
            endAt: OpenAPI.Instant? = nil,
            maxOccurrences: Int? = nil,
            lastOccurrenceID: String? = nil,
            createdAt: OpenAPI.Instant? = nil,
            updatedAt: OpenAPI.Instant? = nil,
            authorityRuleIDs: [OpenAPI.ID],
            snoozedUntil: OpenAPI.Instant? = nil,
            lastOccurrence: OpenAPI.AutomationLastOccurrence? = nil
        ) {
            self.id = id
            self.revision = revision
            self.title = title
            self.instruction = instruction
            self.status = status
            self.trigger = trigger
            self.connectionIDs = connectionIDs
            self.budgetUSD = budgetUSD
            self.maxActiveRuns = maxActiveRuns
            self.missedRun = missedRun
            self.executionWindowSeconds = executionWindowSeconds
            self.destinationIDs = destinationIDs
            self.nextAt = nextAt
            self.endAt = endAt
            self.maxOccurrences = maxOccurrences
            self.lastOccurrenceID = lastOccurrenceID
            self.createdAt = createdAt
            self.updatedAt = updatedAt
            self.authorityRuleIDs = authorityRuleIDs
            self.snoozedUntil = snoozedUntil
            self.lastOccurrence = lastOccurrence
        }

        enum CodingKeys: String, CodingKey {
            case id
            case revision
            case title
            case instruction
            case status
            case trigger
            case connectionIDs = "connection_ids"
            case budgetUSD = "budget_usd"
            case maxActiveRuns = "max_active_runs"
            case missedRun = "missed_run"
            case executionWindowSeconds = "execution_window_seconds"
            case destinationIDs = "destination_ids"
            case nextAt = "next_at"
            case endAt = "end_at"
            case maxOccurrences = "max_occurrences"
            case lastOccurrenceID = "last_occurrence_id"
            case createdAt = "created_at"
            case updatedAt = "updated_at"
            case authorityRuleIDs = "authority_rule_ids"
            case snoozedUntil = "snoozed_until"
            case lastOccurrence = "last_occurrence"
        }
    }

    public struct AutomationAction: Sendable, Codable, Hashable {
        public enum Value: Sendable, Codable, Hashable {
            public struct PausePayload: Sendable, Codable, Hashable {
                public var action: String

                public init(
                    action: String
                ) {
                    self.action = action
                }
            }
            public struct SnoozePayload: Sendable, Codable, Hashable {
                public var action: String
                public var until: OpenAPI.Instant

                public init(
                    action: String,
                    until: OpenAPI.Instant
                ) {
                    self.action = action
                    self.until = until
                }
            }

            case pause(PausePayload)
            case snooze(SnoozePayload)

            private enum DiscriminatorKey: String, CodingKey {
                case discriminator = "action"
            }

            public init(from decoder: any Decoder) throws {
                let container = try decoder.container(keyedBy: DiscriminatorKey.self)
                let kind = try container.decode(String.self, forKey: .discriminator)
                switch kind {
                case "pause", "resume", "cancel", "run_now":
                    self = .pause(try PausePayload(from: decoder))
                case "snooze":
                    self = .snooze(try SnoozePayload(from: decoder))
                default:
                    throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Value action: \(String(describing: kind))")
                }
            }

            public func encode(to encoder: any Encoder) throws {
                switch self {
                case .pause(let value):
                    try value.encode(to: encoder)
                case .snooze(let value):
                    try value.encode(to: encoder)
                }
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct AutomationChange: Sendable, Codable, Hashable {
        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: OpenAPI.AutomationEditable

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: OpenAPI.AutomationEditable
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct AutomationEditable: Sendable, Codable, Hashable {
        public var title: String
        public var instruction: String
        public var trigger: OpenAPI.Trigger
        public var connectionIDs: [OpenAPI.ID]
        public var requestedAuthority: OpenAPI.AutomationRequestedAuthority?
        public var budgetUSD: Double
        public var maxActiveRuns: Int
        public var missedRun: OpenAPI.MissedRun?
        public var executionWindowSeconds: Int
        public var destinationIDs: [OpenAPI.ID]
        public var endAt: OpenAPI.Instant?
        public var maxOccurrences: Int?

        public init(
            title: String,
            instruction: String,
            trigger: OpenAPI.Trigger,
            connectionIDs: [OpenAPI.ID],
            requestedAuthority: OpenAPI.AutomationRequestedAuthority? = nil,
            budgetUSD: Double,
            maxActiveRuns: Int,
            missedRun: OpenAPI.MissedRun? = nil,
            executionWindowSeconds: Int,
            destinationIDs: [OpenAPI.ID],
            endAt: OpenAPI.Instant? = nil,
            maxOccurrences: Int? = nil
        ) {
            self.title = title
            self.instruction = instruction
            self.trigger = trigger
            self.connectionIDs = connectionIDs
            self.requestedAuthority = requestedAuthority
            self.budgetUSD = budgetUSD
            self.maxActiveRuns = maxActiveRuns
            self.missedRun = missedRun
            self.executionWindowSeconds = executionWindowSeconds
            self.destinationIDs = destinationIDs
            self.endAt = endAt
            self.maxOccurrences = maxOccurrences
        }

        enum CodingKeys: String, CodingKey {
            case title
            case instruction
            case trigger
            case connectionIDs = "connection_ids"
            case requestedAuthority = "requested_authority"
            case budgetUSD = "budget_usd"
            case maxActiveRuns = "max_active_runs"
            case missedRun = "missed_run"
            case executionWindowSeconds = "execution_window_seconds"
            case destinationIDs = "destination_ids"
            case endAt = "end_at"
            case maxOccurrences = "max_occurrences"
        }
    }

    public struct AutomationLastOccurrence: Sendable, Codable, Hashable {
        public var id: String
        public var status: OpenAPI.OccurrenceStatus
        public var scheduledAt: OpenAPI.Instant
        public var summary: String?

        public init(
            id: String,
            status: OpenAPI.OccurrenceStatus,
            scheduledAt: OpenAPI.Instant,
            summary: String? = nil
        ) {
            self.id = id
            self.status = status
            self.scheduledAt = scheduledAt
            self.summary = summary
        }

        enum CodingKeys: String, CodingKey {
            case id
            case status
            case scheduledAt = "scheduled_at"
            case summary
        }
    }

    public struct AutomationPreview: Sendable, Codable, Hashable {
        public var nextRuns: [OpenAPI.Instant]
        public var timezone: OpenAPI.Timezone
        public var proposedRules: [OpenAPI.PermissionRuleEditable]
        public var authoritySummary: String?
        public var missedRunExplanation: String
        public var warnings: [OpenAPI.AutomationPreviewWarning]

        public init(
            nextRuns: [OpenAPI.Instant],
            timezone: OpenAPI.Timezone,
            proposedRules: [OpenAPI.PermissionRuleEditable],
            authoritySummary: String? = nil,
            missedRunExplanation: String,
            warnings: [OpenAPI.AutomationPreviewWarning]
        ) {
            self.nextRuns = nextRuns
            self.timezone = timezone
            self.proposedRules = proposedRules
            self.authoritySummary = authoritySummary
            self.missedRunExplanation = missedRunExplanation
            self.warnings = warnings
        }

        enum CodingKeys: String, CodingKey {
            case nextRuns = "next_runs"
            case timezone
            case proposedRules = "proposed_rules"
            case authoritySummary = "authority_summary"
            case missedRunExplanation = "missed_run_explanation"
            case warnings
        }
    }

    public struct AutomationPreviewWarning: Sendable, Codable, Hashable {
        public var code: String
        public var message: String

        public init(
            code: String,
            message: String
        ) {
            self.code = code
            self.message = message
        }
    }

    public struct AutomationRequestedAuthority: Sendable, Codable, Hashable {
        public var actionClasses: [OpenAPI.Effect]
        public var toolIDs: [String]?
        public var resourceIDs: [String]?
        public var recipients: [String]?
        public var maxOperations: Int?

        public init(
            actionClasses: [OpenAPI.Effect],
            toolIDs: [String]? = nil,
            resourceIDs: [String]? = nil,
            recipients: [String]? = nil,
            maxOperations: Int? = nil
        ) {
            self.actionClasses = actionClasses
            self.toolIDs = toolIDs
            self.resourceIDs = resourceIDs
            self.recipients = recipients
            self.maxOperations = maxOperations
        }

        enum CodingKeys: String, CodingKey {
            case actionClasses = "action_classes"
            case toolIDs = "tool_ids"
            case resourceIDs = "resource_ids"
            case recipients
            case maxOperations = "max_operations"
        }
    }

    public struct AvailabilityProposal: Sendable, Codable, Hashable {
        public var slots: [OpenAPI.AvailabilitySlot]
        public var durationMinutes: Int
        public var attendees: [OpenAPI.MailAddress]
        public var status: String

        public init(
            slots: [OpenAPI.AvailabilitySlot],
            durationMinutes: Int,
            attendees: [OpenAPI.MailAddress],
            status: String
        ) {
            self.slots = slots
            self.durationMinutes = durationMinutes
            self.attendees = attendees
            self.status = status
        }

        enum CodingKeys: String, CodingKey {
            case slots
            case durationMinutes = "duration_minutes"
            case attendees
            case status
        }
    }

    public struct AvailabilitySlot: Sendable, Codable, Hashable {
        public var start: OpenAPI.Instant
        public var end: OpenAPI.Instant
        public var status: String
        public var sources: [OpenAPI.ResourceRef]?

        public init(
            start: OpenAPI.Instant,
            end: OpenAPI.Instant,
            status: String,
            sources: [OpenAPI.ResourceRef]? = nil
        ) {
            self.start = start
            self.end = end
            self.status = status
            self.sources = sources
        }
    }

    public struct AvailableTool: Sendable, Codable, Hashable {
        public var tool: OpenAPI.DefinitionRef
        public var title: String
        public var description: String
        public var connectionIDs: [OpenAPI.ID]
        public var availability: String
        public var reasonCode: String?
        public var recovery: String?
        public var capability: String
        public var integrationID: String?
        public var effect: OpenAPI.Effect?
        public var exposure: String?

        public init(
            tool: OpenAPI.DefinitionRef,
            title: String,
            description: String,
            connectionIDs: [OpenAPI.ID],
            availability: String,
            reasonCode: String? = nil,
            recovery: String? = nil,
            capability: String,
            integrationID: String? = nil,
            effect: OpenAPI.Effect? = nil,
            exposure: String? = nil
        ) {
            self.tool = tool
            self.title = title
            self.description = description
            self.connectionIDs = connectionIDs
            self.availability = availability
            self.reasonCode = reasonCode
            self.recovery = recovery
            self.capability = capability
            self.integrationID = integrationID
            self.effect = effect
            self.exposure = exposure
        }

        enum CodingKeys: String, CodingKey {
            case tool
            case title
            case description
            case connectionIDs = "connection_ids"
            case availability
            case reasonCode = "reason_code"
            case recovery
            case capability
            case integrationID = "integration_id"
            case effect
            case exposure
        }
    }

    public struct AvailableToolList: Sendable, Codable, Hashable {
        public var items: [OpenAPI.AvailableTool]
        public var nextCursor: String?

        public init(
            items: [OpenAPI.AvailableTool],
            nextCursor: String? = nil
        ) {
            self.items = items
            self.nextCursor = nextCursor
        }

        enum CodingKeys: String, CodingKey {
            case items
            case nextCursor = "next_cursor"
        }
    }

    public struct BillingCosts: Sendable, Codable, Hashable {
        public var hosted: OpenAPI.CostAmount
        public var byok: OpenAPI.CostAmount
        public var local: OpenAPI.CostAmount

        public init(
            hosted: OpenAPI.CostAmount,
            byok: OpenAPI.CostAmount,
            local: OpenAPI.CostAmount
        ) {
            self.hosted = hosted
            self.byok = byok
            self.local = local
        }
    }

    public struct BriefItem: Sendable, Codable, Hashable {
        public var title: String
        public var trailing: String?
        public var interactionID: OpenAPI.ID?
        public var actionLabel: String?

        public init(
            title: String,
            trailing: String? = nil,
            interactionID: OpenAPI.ID? = nil,
            actionLabel: String? = nil
        ) {
            self.title = title
            self.trailing = trailing
            self.interactionID = interactionID
            self.actionLabel = actionLabel
        }

        enum CodingKeys: String, CodingKey {
            case title
            case trailing
            case interactionID = "interaction_id"
            case actionLabel = "action_label"
        }
    }

    public struct BudgetPreferences: Sendable, Codable, Hashable {
        public var taskUSD: Double
        public var monthlyUSD: Double?

        public init(
            taskUSD: Double,
            monthlyUSD: Double? = nil
        ) {
            self.taskUSD = taskUSD
            self.monthlyUSD = monthlyUSD
        }

        enum CodingKeys: String, CodingKey {
            case taskUSD = "task_usd"
            case monthlyUSD = "monthly_usd"
        }
    }

    public struct CalendarEvent: Sendable, Codable, Hashable {
        public struct Recurrence: Sendable, Codable, Hashable {
            public var rrule: String?
            public var seriesID: String?
            public var occurrenceID: String?
            public var exception: Bool?

            public init(
                rrule: String? = nil,
                seriesID: String? = nil,
                occurrenceID: String? = nil,
                exception: Bool? = nil
            ) {
                self.rrule = rrule
                self.seriesID = seriesID
                self.occurrenceID = occurrenceID
                self.exception = exception
            }

            enum CodingKeys: String, CodingKey {
                case rrule
                case seriesID = "series_id"
                case occurrenceID = "occurrence_id"
                case exception
            }
        }
        public struct Supports: Sendable, Codable, Hashable {
            public var update: Bool
            public var cancel: Bool
            public var invite: Bool
            public var rsvp: Bool
            public var recurrenceScopes: [OpenAPI.RecurrenceScope]

            public init(
                update: Bool,
                cancel: Bool,
                invite: Bool,
                rsvp: Bool,
                recurrenceScopes: [OpenAPI.RecurrenceScope]
            ) {
                self.update = update
                self.cancel = cancel
                self.invite = invite
                self.rsvp = rsvp
                self.recurrenceScopes = recurrenceScopes
            }

            enum CodingKeys: String, CodingKey {
                case update
                case cancel
                case invite
                case rsvp
                case recurrenceScopes = "recurrence_scopes"
            }
        }

        public var ref: OpenAPI.ResourceRef
        public var calendar: OpenAPI.ResourceRef
        public var title: String
        public var time: OpenAPI.CalendarTime
        public var location: String?
        public var meetingURL: String?
        public var description: String?
        public var attendees: [OpenAPI.Attendee]?
        public var recurrence: Recurrence?
        public var status: String?
        public var sourceURL: String?
        public var importedFrom: String?
        public var supports: Supports

        public init(
            ref: OpenAPI.ResourceRef,
            calendar: OpenAPI.ResourceRef,
            title: String,
            time: OpenAPI.CalendarTime,
            location: String? = nil,
            meetingURL: String? = nil,
            description: String? = nil,
            attendees: [OpenAPI.Attendee]? = nil,
            recurrence: Recurrence? = nil,
            status: String? = nil,
            sourceURL: String? = nil,
            importedFrom: String? = nil,
            supports: Supports
        ) {
            self.ref = ref
            self.calendar = calendar
            self.title = title
            self.time = time
            self.location = location
            self.meetingURL = meetingURL
            self.description = description
            self.attendees = attendees
            self.recurrence = recurrence
            self.status = status
            self.sourceURL = sourceURL
            self.importedFrom = importedFrom
            self.supports = supports
        }

        enum CodingKeys: String, CodingKey {
            case ref
            case calendar
            case title
            case time
            case location
            case meetingURL = "meeting_url"
            case description
            case attendees
            case recurrence
            case status
            case sourceURL = "source_url"
            case importedFrom = "imported_from"
            case supports
        }
    }

    public enum CalendarTime: Sendable, Codable, Hashable {
        public struct AllDayPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var startDate: OpenAPI.LocalDate
            public var endDate: OpenAPI.LocalDate

            public init(
                kind: String,
                startDate: OpenAPI.LocalDate,
                endDate: OpenAPI.LocalDate
            ) {
                self.kind = kind
                self.startDate = startDate
                self.endDate = endDate
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case startDate = "start_date"
                case endDate = "end_date"
            }
        }
        public struct TimedPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var start: OpenAPI.Instant
            public var end: OpenAPI.Instant
            public var timezone: OpenAPI.Timezone

            public init(
                kind: String,
                start: OpenAPI.Instant,
                end: OpenAPI.Instant,
                timezone: OpenAPI.Timezone
            ) {
                self.kind = kind
                self.start = start
                self.end = end
                self.timezone = timezone
            }
        }

        case allDay(AllDayPayload)
        case timed(TimedPayload)

        private enum DiscriminatorKey: String, CodingKey {
            case discriminator = "kind"
        }

        public init(from decoder: any Decoder) throws {
            let container = try decoder.container(keyedBy: DiscriminatorKey.self)
            let kind = try container.decode(String.self, forKey: .discriminator)
            switch kind {
            case "all_day":
                self = .allDay(try AllDayPayload(from: decoder))
            case "timed":
                self = .timed(try TimedPayload(from: decoder))
            default:
                throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported CalendarTime kind: \(String(describing: kind))")
            }
        }

        public func encode(to encoder: any Encoder) throws {
            switch self {
            case .allDay(let value):
                try value.encode(to: encoder)
            case .timed(let value):
                try value.encode(to: encoder)
            }
        }
    }

    public struct Capabilities: Sendable, Codable, Hashable {
        public struct Features: Sendable, Codable, Hashable {
            public var backgroundTasks: Bool?
            public var jev: Bool?

            public init(
                backgroundTasks: Bool? = nil,
                jev: Bool? = nil
            ) {
                self.backgroundTasks = backgroundTasks
                self.jev = jev
            }
        }

        public var contract: String
        public var conversationContract: String?
        public var service: String
        public var persistence: String?
        public var provider: String?
        public var model: String?
        public var conversationModes: [String]
        public var features: Features
        public var assistantContract: String?
        public var assistantFeatures: OpenAPI.AssistantFeatures?
        public var uploadAccept: OpenAPI.UploadAccept?
        public var modelProfiles: OpenAPI.ModelProfileCatalog?
        public var entitlement: OpenAPI.CapabilitiesEntitlement?
        public var serviceKeys: OpenAPI.CapabilitiesServiceKeys?

        public init(
            contract: String,
            conversationContract: String? = nil,
            service: String,
            persistence: String? = nil,
            provider: String? = nil,
            model: String? = nil,
            conversationModes: [String],
            features: Features,
            assistantContract: String? = nil,
            assistantFeatures: OpenAPI.AssistantFeatures? = nil,
            uploadAccept: OpenAPI.UploadAccept? = nil,
            modelProfiles: OpenAPI.ModelProfileCatalog? = nil,
            entitlement: OpenAPI.CapabilitiesEntitlement? = nil,
            serviceKeys: OpenAPI.CapabilitiesServiceKeys? = nil
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
            self.modelProfiles = modelProfiles
            self.entitlement = entitlement
            self.serviceKeys = serviceKeys
        }

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
            case modelProfiles = "model_profiles"
            case entitlement
            case serviceKeys = "service_keys"
        }
    }

    public struct CapabilitiesEntitlement: Sendable, Codable, Hashable {
        public var tier: String
        public var status: String
        public var allowBYOK: Bool
        public var suspended: Bool
        public var creditRemainingUSD: Double
        public var creditGrantUSD: Double
        public var storageUsedBytes: Int
        public var storageLimitBytes: Int
        public var maxRoutines: Int
        public var maxConcurrentTasks: Int
        public var maxDesktopWs: Int
        public var maxCheckpoints: Int

        public init(
            tier: String,
            status: String,
            allowBYOK: Bool,
            suspended: Bool,
            creditRemainingUSD: Double,
            creditGrantUSD: Double,
            storageUsedBytes: Int,
            storageLimitBytes: Int,
            maxRoutines: Int,
            maxConcurrentTasks: Int,
            maxDesktopWs: Int,
            maxCheckpoints: Int
        ) {
            self.tier = tier
            self.status = status
            self.allowBYOK = allowBYOK
            self.suspended = suspended
            self.creditRemainingUSD = creditRemainingUSD
            self.creditGrantUSD = creditGrantUSD
            self.storageUsedBytes = storageUsedBytes
            self.storageLimitBytes = storageLimitBytes
            self.maxRoutines = maxRoutines
            self.maxConcurrentTasks = maxConcurrentTasks
            self.maxDesktopWs = maxDesktopWs
            self.maxCheckpoints = maxCheckpoints
        }

        enum CodingKeys: String, CodingKey {
            case tier
            case status
            case allowBYOK = "allow_byok"
            case suspended
            case creditRemainingUSD = "credit_remaining_usd"
            case creditGrantUSD = "credit_grant_usd"
            case storageUsedBytes = "storage_used_bytes"
            case storageLimitBytes = "storage_limit_bytes"
            case maxRoutines = "max_routines"
            case maxConcurrentTasks = "max_concurrent_tasks"
            case maxDesktopWs = "max_desktop_ws"
            case maxCheckpoints = "max_checkpoints"
        }
    }

    public struct CapabilitiesServiceKeys: Sendable, Codable, Hashable {
        public var exaConfigured: Bool
        public var platformExaAvailable: Bool

        public init(
            exaConfigured: Bool,
            platformExaAvailable: Bool
        ) {
            self.exaConfigured = exaConfigured
            self.platformExaAvailable = platformExaAvailable
        }

        enum CodingKeys: String, CodingKey {
            case exaConfigured = "exa_configured"
            case platformExaAvailable = "platform_exa_available"
        }
    }

    public struct CardPart: Sendable, Codable, Hashable {
        public struct Resource: Sendable, Codable, Hashable {
            public var kind: String
            public var id: String
            public var revision: OpenAPI.Revision?

            public init(
                kind: String,
                id: String,
                revision: OpenAPI.Revision? = nil
            ) {
                self.kind = kind
                self.id = id
                self.revision = revision
            }
        }

        public var type: String
        public var schemaVersion: Int
        public var kind: String
        public var resource: Resource
        public var actions: [String]?
        public var fallback: String
        public var display: String?
        public var id: String?

        public init(
            type: String,
            schemaVersion: Int,
            kind: String,
            resource: Resource,
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
    }

    public struct CategoryCosts: Sendable, Codable, Hashable {
        public var llm: OpenAPI.CostAmount
        public var search: OpenAPI.CostAmount
        public var computer: OpenAPI.CostAmount

        public init(
            llm: OpenAPI.CostAmount,
            search: OpenAPI.CostAmount,
            computer: OpenAPI.CostAmount
        ) {
            self.llm = llm
            self.search = search
            self.computer = computer
        }
    }

    public struct ClassificationHint: Sendable, Codable, Hashable {
        public var intent: String?
        public var intents: [String]?
        public var confidence: Double?
        public var available: Bool

        public init(
            intent: String? = nil,
            intents: [String]? = nil,
            confidence: Double? = nil,
            available: Bool
        ) {
            self.intent = intent
            self.intents = intents
            self.confidence = confidence
            self.available = available
        }
    }

    public struct ComputerCheckpoint: Sendable, Codable, Hashable {
        public var id: String
        public var label: String?
        public var createdAt: OpenAPI.Instant

        public init(
            id: String,
            label: String? = nil,
            createdAt: OpenAPI.Instant
        ) {
            self.id = id
            self.label = label
            self.createdAt = createdAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case label
            case createdAt = "created_at"
        }
    }

    public struct ComputerSessionSummary: Sendable, Codable, Hashable {
        public var id: String
        public var computerID: String
        public var mode: String
        public var taskID: String?
        public var interactionID: String?
        public var expiresAt: OpenAPI.Instant
        public var createdAt: OpenAPI.Instant
        public var lastActivityAt: OpenAPI.Instant?

        public init(
            id: String,
            computerID: String,
            mode: String,
            taskID: String? = nil,
            interactionID: String? = nil,
            expiresAt: OpenAPI.Instant,
            createdAt: OpenAPI.Instant,
            lastActivityAt: OpenAPI.Instant? = nil
        ) {
            self.id = id
            self.computerID = computerID
            self.mode = mode
            self.taskID = taskID
            self.interactionID = interactionID
            self.expiresAt = expiresAt
            self.createdAt = createdAt
            self.lastActivityAt = lastActivityAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case computerID = "computer_id"
            case mode
            case taskID = "task_id"
            case interactionID = "interaction_id"
            case expiresAt = "expires_at"
            case createdAt = "created_at"
            case lastActivityAt = "last_activity_at"
        }
    }

    public struct ComputerStatus: Sendable, Codable, Hashable {
        public var enabled: Bool
        public var computerID: String?
        public var provider: String
        public var status: String
        public var baselineCheckpointID: String?
        public var diskUsedBytes: Int
        public var diskMaxBytes: Int
        public var checkpoints: [OpenAPI.ComputerCheckpoint]
        public var activeSessions: [OpenAPI.ComputerSessionSummary]
        public var lastActiveAt: OpenAPI.Instant?

        public init(
            enabled: Bool,
            computerID: String? = nil,
            provider: String,
            status: String,
            baselineCheckpointID: String? = nil,
            diskUsedBytes: Int,
            diskMaxBytes: Int,
            checkpoints: [OpenAPI.ComputerCheckpoint],
            activeSessions: [OpenAPI.ComputerSessionSummary],
            lastActiveAt: OpenAPI.Instant? = nil
        ) {
            self.enabled = enabled
            self.computerID = computerID
            self.provider = provider
            self.status = status
            self.baselineCheckpointID = baselineCheckpointID
            self.diskUsedBytes = diskUsedBytes
            self.diskMaxBytes = diskMaxBytes
            self.checkpoints = checkpoints
            self.activeSessions = activeSessions
            self.lastActiveAt = lastActiveAt
        }

        enum CodingKeys: String, CodingKey {
            case enabled
            case computerID = "computer_id"
            case provider
            case status
            case baselineCheckpointID = "baseline_checkpoint_id"
            case diskUsedBytes = "disk_used_bytes"
            case diskMaxBytes = "disk_max_bytes"
            case checkpoints
            case activeSessions = "active_sessions"
            case lastActiveAt = "last_active_at"
        }
    }

    public struct ComputerTakeoverPayload: Sendable, Codable, Hashable {
        public var reason: String?
        public var url: String?
        public var computerID: String?
        public var taskID: String?

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

        enum CodingKeys: String, CodingKey {
            case reason
            case url
            case computerID = "computer_id"
            case taskID = "task_id"
        }
    }

    public struct Connection: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var revision: OpenAPI.Revision
        public var integrationID: String
        public var title: String
        public var account: OpenAPI.ConnectionAccount?
        public var status: OpenAPI.ConnectionStatus
        public var grantedScopes: [String]
        public var selectedResourceIDs: [String]
        public var health: OpenAPI.Health
        public var defaults: [String]
        public var createdAt: OpenAPI.Instant?
        public var updatedAt: OpenAPI.Instant?
        public var missingScopes: [String]?
        public var capabilities: [String]?

        public init(
            id: OpenAPI.ID,
            revision: OpenAPI.Revision,
            integrationID: String,
            title: String,
            account: OpenAPI.ConnectionAccount? = nil,
            status: OpenAPI.ConnectionStatus,
            grantedScopes: [String],
            selectedResourceIDs: [String],
            health: OpenAPI.Health,
            defaults: [String],
            createdAt: OpenAPI.Instant? = nil,
            updatedAt: OpenAPI.Instant? = nil,
            missingScopes: [String]? = nil,
            capabilities: [String]? = nil
        ) {
            self.id = id
            self.revision = revision
            self.integrationID = integrationID
            self.title = title
            self.account = account
            self.status = status
            self.grantedScopes = grantedScopes
            self.selectedResourceIDs = selectedResourceIDs
            self.health = health
            self.defaults = defaults
            self.createdAt = createdAt
            self.updatedAt = updatedAt
            self.missingScopes = missingScopes
            self.capabilities = capabilities
        }

        enum CodingKeys: String, CodingKey {
            case id
            case revision
            case integrationID = "integration_id"
            case title
            case account
            case status
            case grantedScopes = "granted_scopes"
            case selectedResourceIDs = "selected_resource_ids"
            case health
            case defaults
            case createdAt = "created_at"
            case updatedAt = "updated_at"
            case missingScopes = "missing_scopes"
            case capabilities
        }
    }

    public struct ConnectionAccount: Sendable, Codable, Hashable {
        public var providerID: String
        public var label: String

        public init(
            providerID: String,
            label: String
        ) {
            self.providerID = providerID
            self.label = label
        }

        enum CodingKeys: String, CodingKey {
            case providerID = "provider_id"
            case label
        }
    }

    public struct ConnectionAction: Sendable, Codable, Hashable {
        public enum Value: Sendable, Codable, Hashable {
            public struct ConnectPayload: Sendable, Codable, Hashable {
                public var action: String
                public var capabilities: [String]?
                public var returnTo: String?
                public var username: String?
                public var password: String?

                public init(
                    action: String,
                    capabilities: [String]? = nil,
                    returnTo: String? = nil,
                    username: String? = nil,
                    password: String? = nil
                ) {
                    self.action = action
                    self.capabilities = capabilities
                    self.returnTo = returnTo
                    self.username = username
                    self.password = password
                }

                enum CodingKeys: String, CodingKey {
                    case action
                    case capabilities
                    case returnTo = "return_to"
                    case username
                    case password
                }
            }
            public struct DisconnectPayload: Sendable, Codable, Hashable {
                public var action: String
                public var purgeCache: Bool?
                public var deleteImportedContent: Bool?

                public init(
                    action: String,
                    purgeCache: Bool? = nil,
                    deleteImportedContent: Bool? = nil
                ) {
                    self.action = action
                    self.purgeCache = purgeCache
                    self.deleteImportedContent = deleteImportedContent
                }

                enum CodingKeys: String, CodingKey {
                    case action
                    case purgeCache = "purge_cache"
                    case deleteImportedContent = "delete_imported_content"
                }
            }
            public struct EnablePayload: Sendable, Codable, Hashable {
                public var action: String

                public init(
                    action: String
                ) {
                    self.action = action
                }
            }

            case connect(ConnectPayload)
            case disconnect(DisconnectPayload)
            case enable(EnablePayload)

            private enum DiscriminatorKey: String, CodingKey {
                case discriminator = "action"
            }

            public init(from decoder: any Decoder) throws {
                let container = try decoder.container(keyedBy: DiscriminatorKey.self)
                let kind = try container.decode(String.self, forKey: .discriminator)
                switch kind {
                case "connect", "repair":
                    self = .connect(try ConnectPayload(from: decoder))
                case "disconnect":
                    self = .disconnect(try DisconnectPayload(from: decoder))
                case "enable", "disable":
                    self = .enable(try EnablePayload(from: decoder))
                default:
                    throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Value action: \(String(describing: kind))")
                }
            }

            public func encode(to encoder: any Encoder) throws {
                switch self {
                case .connect(let value):
                    try value.encode(to: encoder)
                case .disconnect(let value):
                    try value.encode(to: encoder)
                case .enable(let value):
                    try value.encode(to: encoder)
                }
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct ConnectionChange: Sendable, Codable, Hashable {
        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: OpenAPI.ConnectionEditable

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: OpenAPI.ConnectionEditable
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct ConnectionEditable: Sendable, Codable, Hashable {
        public var integrationID: String
        public var title: String
        public var selectedResourceIDs: [String]
        public var configuration: [String: AnyJSON]?

        public init(
            integrationID: String,
            title: String,
            selectedResourceIDs: [String],
            configuration: [String: AnyJSON]? = nil
        ) {
            self.integrationID = integrationID
            self.title = title
            self.selectedResourceIDs = selectedResourceIDs
            self.configuration = configuration
        }

        enum CodingKeys: String, CodingKey {
            case integrationID = "integration_id"
            case title
            case selectedResourceIDs = "selected_resource_ids"
            case configuration
        }
    }

    public enum ConnectionStatus: String, Sendable, Codable, Hashable, CaseIterable {
        case notConnected = "not_connected"
        case connecting = "connecting"
        case connected = "connected"
        case needsAttention = "needs_attention"
        case disabled = "disabled"
        case disconnecting = "disconnecting"
        case disconnected = "disconnected"
    }

    public struct ConversationActionRecord: Sendable, Codable, Hashable {
        public var id: String
        public var request: String
        public var bytes: Int
        public var status: String
        public var error: String?

        public init(
            id: String,
            request: String,
            bytes: Int,
            status: String,
            error: String? = nil
        ) {
            self.id = id
            self.request = request
            self.bytes = bytes
            self.status = status
            self.error = error
        }
    }

    public struct ConversationCancelRequest: Sendable, Codable, Hashable {
        public var turnID: OpenAPI.UUID

        public init(
            turnID: OpenAPI.UUID
        ) {
            self.turnID = turnID
        }

        enum CodingKeys: String, CodingKey {
            case turnID = "turnId"
        }
    }

    public struct ConversationDispatch: Sendable, Codable, Hashable {
        public var id: String
        public var request: String
        public var status: String
        public var createdAt: OpenAPI.Instant

        public init(
            id: String,
            request: String,
            status: String,
            createdAt: OpenAPI.Instant
        ) {
            self.id = id
            self.request = request
            self.status = status
            self.createdAt = createdAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case request
            case status
            case createdAt = "created_at"
        }
    }

    public struct ConversationHeader: Sendable, Codable, Hashable {
        public var id: String
        public var title: String
        public var mode: String
        public var activeTurn: String?
        public var position: Int
        public var updatedAt: OpenAPI.Instant
        public var turnPolicy: String
        public var status: OpenAPI.ConversationStatus?
        public var paused: Bool?

        public init(
            id: String,
            title: String,
            mode: String,
            activeTurn: String? = nil,
            position: Int,
            updatedAt: OpenAPI.Instant,
            turnPolicy: String,
            status: OpenAPI.ConversationStatus? = nil,
            paused: Bool? = nil
        ) {
            self.id = id
            self.title = title
            self.mode = mode
            self.activeTurn = activeTurn
            self.position = position
            self.updatedAt = updatedAt
            self.turnPolicy = turnPolicy
            self.status = status
            self.paused = paused
        }
    }

    public struct ConversationHistoryResponse: Sendable, Codable, Hashable {
        public var messages: [OpenAPI.MessageView]
        public var nextCursor: String?

        public init(
            messages: [OpenAPI.MessageView],
            nextCursor: String? = nil
        ) {
            self.messages = messages
            self.nextCursor = nextCursor
        }
    }

    public struct ConversationMessageAccepted: Sendable, Codable, Hashable {
        public var turnID: OpenAPI.UUID
        public var conversation: OpenAPI.ConversationView

        public init(
            turnID: OpenAPI.UUID,
            conversation: OpenAPI.ConversationView
        ) {
            self.turnID = turnID
            self.conversation = conversation
        }

        enum CodingKeys: String, CodingKey {
            case turnID = "turnId"
            case conversation
        }
    }

    public struct ConversationMessageRequest: Sendable, Codable, Hashable {
        public var id: OpenAPI.UUID
        public var text: String?
        public var attachments: [OpenAPI.UUID]?
        public var parts: [OpenAPI.Part]?
        public var model: OpenAPI.ModelSelection?

        public init(
            id: OpenAPI.UUID,
            text: String? = nil,
            attachments: [OpenAPI.UUID]? = nil,
            parts: [OpenAPI.Part]? = nil,
            model: OpenAPI.ModelSelection? = nil
        ) {
            self.id = id
            self.text = text
            self.attachments = attachments
            self.parts = parts
            self.model = model
        }
    }

    public struct ConversationOperationsResponse: Sendable, Codable, Hashable {
        public var operations: [OpenAPI.OperationProjection]

        public init(
            operations: [OpenAPI.OperationProjection]
        ) {
            self.operations = operations
        }
    }

    public struct ConversationRetryAccepted: Sendable, Codable, Hashable {
        public var conversation: OpenAPI.ConversationView

        public init(
            conversation: OpenAPI.ConversationView
        ) {
            self.conversation = conversation
        }
    }

    public struct ConversationRetryRequest: Sendable, Codable, Hashable {
        public var turnID: String?
        public var turnID2: String?
        public var messageID: String?
        public var messageID2: String?

        public init(
            turnID: String? = nil,
            turnID2: String? = nil,
            messageID: String? = nil,
            messageID2: String? = nil
        ) {
            self.turnID = turnID
            self.turnID2 = turnID2
            self.messageID = messageID
            self.messageID2 = messageID2
        }

        enum CodingKeys: String, CodingKey {
            case turnID = "turn_id"
            case turnID2 = "turnId"
            case messageID = "message_id"
            case messageID2 = "messageId"
        }
    }

    public struct ConversationStatus: Sendable, Codable, Hashable {
        public var introducing: Bool?
        public var pending: Bool
        public var processing: Bool
        public var turnID: String?
        public var inputIDs: [String]?
        public var restartCount: Int
        public var routingBlocked: Bool?
        public var frozen: Bool
        public var revision: Int
        public var consumed: Int

        public init(
            introducing: Bool? = nil,
            pending: Bool,
            processing: Bool,
            turnID: String? = nil,
            inputIDs: [String]? = nil,
            restartCount: Int,
            routingBlocked: Bool? = nil,
            frozen: Bool,
            revision: Int,
            consumed: Int
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
    }

    public struct ConversationTrace: Sendable, Codable, Hashable {
        public var id: String
        public var turnID: String
        public var attempt: Int
        public var inputIDs: [String]?
        public var model: String
        public var servedModel: String?
        public var modelSelection: OpenAPI.ModelSelection?
        public var status: String
        public var startedAt: OpenAPI.Instant
        public var finishedAt: OpenAPI.Instant?
        public var durationMs: Int?
        public var classificationHint: OpenAPI.ClassificationHint?
        public var contextTruncated: Bool?
        public var contextTurns: Int?
        public var contextBytes: Int?
        public var actions: [OpenAPI.ConversationActionRecord]?
        public var usage: [OpenAPI.Usage]?
        public var error: String?

        public init(
            id: String,
            turnID: String,
            attempt: Int,
            inputIDs: [String]? = nil,
            model: String,
            servedModel: String? = nil,
            modelSelection: OpenAPI.ModelSelection? = nil,
            status: String,
            startedAt: OpenAPI.Instant,
            finishedAt: OpenAPI.Instant? = nil,
            durationMs: Int? = nil,
            classificationHint: OpenAPI.ClassificationHint? = nil,
            contextTruncated: Bool? = nil,
            contextTurns: Int? = nil,
            contextBytes: Int? = nil,
            actions: [OpenAPI.ConversationActionRecord]? = nil,
            usage: [OpenAPI.Usage]? = nil,
            error: String? = nil
        ) {
            self.id = id
            self.turnID = turnID
            self.attempt = attempt
            self.inputIDs = inputIDs
            self.model = model
            self.servedModel = servedModel
            self.modelSelection = modelSelection
            self.status = status
            self.startedAt = startedAt
            self.finishedAt = finishedAt
            self.durationMs = durationMs
            self.classificationHint = classificationHint
            self.contextTruncated = contextTruncated
            self.contextTurns = contextTurns
            self.contextBytes = contextBytes
            self.actions = actions
            self.usage = usage
            self.error = error
        }

        enum CodingKeys: String, CodingKey {
            case id
            case turnID = "turn_id"
            case attempt
            case inputIDs = "input_ids"
            case model
            case servedModel = "served_model"
            case modelSelection = "model_selection"
            case status
            case startedAt = "started_at"
            case finishedAt = "finished_at"
            case durationMs = "duration_ms"
            case classificationHint = "classification_hint"
            case contextTruncated = "context_truncated"
            case contextTurns = "context_turns"
            case contextBytes = "context_bytes"
            case actions
            case usage
            case error
        }
    }

    public struct ConversationView: Sendable, Codable, Hashable {
        public var id: String
        public var title: String
        public var mode: String
        public var activeTurn: String?
        public var position: Int
        public var updatedAt: OpenAPI.Instant
        public var turnPolicy: String
        public var status: OpenAPI.ConversationStatus?
        public var paused: Bool?
        public var messages: [OpenAPI.MessageView]
        public var nextBefore: Int?
        public var nextCursor: String?
        public var assistant: OpenAPI.AssistantExtras?

        public init(
            id: String,
            title: String,
            mode: String,
            activeTurn: String? = nil,
            position: Int,
            updatedAt: OpenAPI.Instant,
            turnPolicy: String,
            status: OpenAPI.ConversationStatus? = nil,
            paused: Bool? = nil,
            messages: [OpenAPI.MessageView],
            nextBefore: Int? = nil,
            nextCursor: String? = nil,
            assistant: OpenAPI.AssistantExtras? = nil
        ) {
            self.id = id
            self.title = title
            self.mode = mode
            self.activeTurn = activeTurn
            self.position = position
            self.updatedAt = updatedAt
            self.turnPolicy = turnPolicy
            self.status = status
            self.paused = paused
            self.messages = messages
            self.nextBefore = nextBefore
            self.nextCursor = nextCursor
            self.assistant = assistant
        }
    }

    public struct Cost: Sendable, Codable, Hashable {
        public var amountUSD: Double?
        public var status: String

        public init(
            amountUSD: Double? = nil,
            status: String
        ) {
            self.amountUSD = amountUSD
            self.status = status
        }

        enum CodingKeys: String, CodingKey {
            case amountUSD = "amount_usd"
            case status
        }
    }

    public struct CostAmount: Sendable, Codable, Hashable {
        public var costUSD: Double
        public var pricedCalls: Int
        public var unpricedCalls: Int
        public var localCalls: Int
        public var pendingCalls: Int

        public init(
            costUSD: Double,
            pricedCalls: Int,
            unpricedCalls: Int,
            localCalls: Int,
            pendingCalls: Int
        ) {
            self.costUSD = costUSD
            self.pricedCalls = pricedCalls
            self.unpricedCalls = unpricedCalls
            self.localCalls = localCalls
            self.pendingCalls = pendingCalls
        }

        enum CodingKeys: String, CodingKey {
            case costUSD = "cost_usd"
            case pricedCalls = "priced_calls"
            case unpricedCalls = "unpriced_calls"
            case localCalls = "local_calls"
            case pendingCalls = "pending_calls"
        }
    }

    public struct CostBucket: Sendable, Codable, Hashable {
        public var startAt: OpenAPI.Instant
        public var endAt: OpenAPI.Instant
        public var totals: OpenAPI.CostAmount
        public var models: [OpenAPI.ModelCost]

        public init(
            startAt: OpenAPI.Instant,
            endAt: OpenAPI.Instant,
            totals: OpenAPI.CostAmount,
            models: [OpenAPI.ModelCost]
        ) {
            self.startAt = startAt
            self.endAt = endAt
            self.totals = totals
            self.models = models
        }

        enum CodingKeys: String, CodingKey {
            case startAt = "start_at"
            case endAt = "end_at"
            case totals
            case models
        }
    }

    public struct CostReport: Sendable, Codable, Hashable {
        public var days: Int
        public var timezone: OpenAPI.Timezone
        public var startAt: OpenAPI.Instant
        public var endAt: OpenAPI.Instant
        public var bucketSize: String
        public var totals: OpenAPI.CostAmount
        public var byBilling: OpenAPI.BillingCosts
        public var byCategory: OpenAPI.CategoryCosts
        public var entitlement: OpenAPI.EntitlementCostSummary?
        public var models: [OpenAPI.ModelCost]
        public var buckets: [OpenAPI.CostBucket]

        public init(
            days: Int,
            timezone: OpenAPI.Timezone,
            startAt: OpenAPI.Instant,
            endAt: OpenAPI.Instant,
            bucketSize: String,
            totals: OpenAPI.CostAmount,
            byBilling: OpenAPI.BillingCosts,
            byCategory: OpenAPI.CategoryCosts,
            entitlement: OpenAPI.EntitlementCostSummary? = nil,
            models: [OpenAPI.ModelCost],
            buckets: [OpenAPI.CostBucket]
        ) {
            self.days = days
            self.timezone = timezone
            self.startAt = startAt
            self.endAt = endAt
            self.bucketSize = bucketSize
            self.totals = totals
            self.byBilling = byBilling
            self.byCategory = byCategory
            self.entitlement = entitlement
            self.models = models
            self.buckets = buckets
        }

        enum CodingKeys: String, CodingKey {
            case days
            case timezone
            case startAt = "start_at"
            case endAt = "end_at"
            case bucketSize = "bucket_size"
            case totals
            case byBilling = "by_billing"
            case byCategory = "by_category"
            case entitlement
            case models
            case buckets
        }
    }

    public struct CreateComputerCheckpointRequest: Sendable, Codable, Hashable {
        public var label: String?

        public init(
            label: String? = nil
        ) {
            self.label = label
        }
    }

    public struct CreateComputerSessionRequest: Sendable, Codable, Hashable {
        public var mode: String?
        public var taskID: String?
        public var interactionID: String?

        public init(
            mode: String? = nil,
            taskID: String? = nil,
            interactionID: String? = nil
        ) {
            self.mode = mode
            self.taskID = taskID
            self.interactionID = interactionID
        }

        enum CodingKeys: String, CodingKey {
            case mode
            case taskID = "task_id"
            case interactionID = "interaction_id"
        }
    }

    public struct CreateComputerSessionResponse: Sendable, Codable, Hashable {
        public var id: String
        public var computerID: String
        public var mode: String
        public var taskID: String?
        public var interactionID: String?
        public var token: String
        public var wsPath: String
        public var expiresAt: OpenAPI.Instant

        public init(
            id: String,
            computerID: String,
            mode: String,
            taskID: String? = nil,
            interactionID: String? = nil,
            token: String,
            wsPath: String,
            expiresAt: OpenAPI.Instant
        ) {
            self.id = id
            self.computerID = computerID
            self.mode = mode
            self.taskID = taskID
            self.interactionID = interactionID
            self.token = token
            self.wsPath = wsPath
            self.expiresAt = expiresAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case computerID = "computer_id"
            case mode
            case taskID = "task_id"
            case interactionID = "interaction_id"
            case token
            case wsPath = "ws_path"
            case expiresAt = "expires_at"
        }
    }

    public struct DataResetRequest: Sendable, Codable, Hashable {
        public var confirmation: String
        public var resumePending: Bool?

        public init(
            confirmation: String,
            resumePending: Bool? = nil
        ) {
            self.confirmation = confirmation
            self.resumePending = resumePending
        }

        enum CodingKeys: String, CodingKey {
            case confirmation
            case resumePending = "resume_pending"
        }
    }

    public struct DefinitionRef: Sendable, Codable, Hashable {
        public var id: String
        public var version: String
        public var digest: String

        public init(
            id: String,
            version: String,
            digest: String
        ) {
            self.id = id
            self.version = version
            self.digest = digest
        }
    }

    public struct Delivery: Sendable, Codable, Hashable {
        public var id: String
        public var occurrenceID: String?
        public var resultIDs: [String]?
        public var taskID: String?
        public var destinationID: OpenAPI.ID
        public var state: String
        public var attemptID: String?
        public var receipt: String?
        public var acknowledgedAt: OpenAPI.Instant?
        public var scheduledFor: OpenAPI.Instant?
        public var createdAt: OpenAPI.Instant?
        public var updatedAt: OpenAPI.Instant?
        public var error: OpenAPI.ErrorDetail?

        public init(
            id: String,
            occurrenceID: String? = nil,
            resultIDs: [String]? = nil,
            taskID: String? = nil,
            destinationID: OpenAPI.ID,
            state: String,
            attemptID: String? = nil,
            receipt: String? = nil,
            acknowledgedAt: OpenAPI.Instant? = nil,
            scheduledFor: OpenAPI.Instant? = nil,
            createdAt: OpenAPI.Instant? = nil,
            updatedAt: OpenAPI.Instant? = nil,
            error: OpenAPI.ErrorDetail? = nil
        ) {
            self.id = id
            self.occurrenceID = occurrenceID
            self.resultIDs = resultIDs
            self.taskID = taskID
            self.destinationID = destinationID
            self.state = state
            self.attemptID = attemptID
            self.receipt = receipt
            self.acknowledgedAt = acknowledgedAt
            self.scheduledFor = scheduledFor
            self.createdAt = createdAt
            self.updatedAt = updatedAt
            self.error = error
        }

        enum CodingKeys: String, CodingKey {
            case id
            case occurrenceID = "occurrence_id"
            case resultIDs = "result_ids"
            case taskID = "task_id"
            case destinationID = "destination_id"
            case state
            case attemptID = "attempt_id"
            case receipt
            case acknowledgedAt = "acknowledged_at"
            case scheduledFor = "scheduled_for"
            case createdAt = "created_at"
            case updatedAt = "updated_at"
            case error
        }
    }

    public struct DiagnosticItem: Sendable, Codable, Hashable {
        public var kind: String
        public var severity: String
        public var subject: [String: AnyJSON]
        public var message: String
        public var repair: String?
        public var since: OpenAPI.Instant?

        public init(
            kind: String,
            severity: String,
            subject: [String: AnyJSON],
            message: String,
            repair: String? = nil,
            since: OpenAPI.Instant? = nil
        ) {
            self.kind = kind
            self.severity = severity
            self.subject = subject
            self.message = message
            self.repair = repair
            self.since = since
        }
    }

    public struct DiagnosticsActionRequest: Sendable, Codable, Hashable {
        public struct Value: Sendable, Codable, Hashable {
            public var action: String

            public init(
                action: String
            ) {
                self.action = action
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public enum Effect: String, Sendable, Codable, Hashable, CaseIterable {
        case read = "read"
        case reversibleWrite = "reversible_write"
        case sensitiveSend = "sensitive_send"
        case unknown = "unknown"
        case control = "control"
    }

    public struct EntitlementCostSummary: Sendable, Codable, Hashable {
        public var tier: String
        public var pendingTier: String?
        public var status: String
        public var billingProvider: String
        public var periodStart: OpenAPI.Instant
        public var periodEnd: OpenAPI.Instant?
        public var graceUntil: OpenAPI.Instant?
        public var suspended: Bool
        public var suspendedReason: String?
        public var allowBYOK: Bool
        public var creditGrantUSD: Double
        public var creditUsedUSD: Double
        public var creditPendingUSD: Double
        public var creditRemainingUSD: Double
        public var byokMaxUSDPerDay: Double
        public var storageUsedBytes: Int
        public var storageLimitBytes: Int

        public init(
            tier: String,
            pendingTier: String? = nil,
            status: String,
            billingProvider: String,
            periodStart: OpenAPI.Instant,
            periodEnd: OpenAPI.Instant? = nil,
            graceUntil: OpenAPI.Instant? = nil,
            suspended: Bool,
            suspendedReason: String? = nil,
            allowBYOK: Bool,
            creditGrantUSD: Double,
            creditUsedUSD: Double,
            creditPendingUSD: Double,
            creditRemainingUSD: Double,
            byokMaxUSDPerDay: Double,
            storageUsedBytes: Int,
            storageLimitBytes: Int
        ) {
            self.tier = tier
            self.pendingTier = pendingTier
            self.status = status
            self.billingProvider = billingProvider
            self.periodStart = periodStart
            self.periodEnd = periodEnd
            self.graceUntil = graceUntil
            self.suspended = suspended
            self.suspendedReason = suspendedReason
            self.allowBYOK = allowBYOK
            self.creditGrantUSD = creditGrantUSD
            self.creditUsedUSD = creditUsedUSD
            self.creditPendingUSD = creditPendingUSD
            self.creditRemainingUSD = creditRemainingUSD
            self.byokMaxUSDPerDay = byokMaxUSDPerDay
            self.storageUsedBytes = storageUsedBytes
            self.storageLimitBytes = storageLimitBytes
        }

        enum CodingKeys: String, CodingKey {
            case tier
            case pendingTier = "pending_tier"
            case status
            case billingProvider = "billing_provider"
            case periodStart = "period_start"
            case periodEnd = "period_end"
            case graceUntil = "grace_until"
            case suspended
            case suspendedReason = "suspended_reason"
            case allowBYOK = "allow_byok"
            case creditGrantUSD = "credit_grant_usd"
            case creditUsedUSD = "credit_used_usd"
            case creditPendingUSD = "credit_pending_usd"
            case creditRemainingUSD = "credit_remaining_usd"
            case byokMaxUSDPerDay = "byok_max_usd_per_day"
            case storageUsedBytes = "storage_used_bytes"
            case storageLimitBytes = "storage_limit_bytes"
        }
    }

    public enum ErrorCode: String, Sendable, Codable, Hashable, CaseIterable {
        case revisionConflict = "revision_conflict"
        case idempotencyConflict = "idempotency_conflict"
        case interactionStale = "interaction_stale"
        case validationFailed = "validation_failed"
        case notFound = "not_found"
        case credentialStoreUnavailable = "credential_store_unavailable"
        case providerUnavailable = "provider_unavailable"
        case accountDeletePending = "account_delete_pending"
        case accountDeleteNotPending = "account_delete_not_pending"
        case accountDeleteForbidden = "account_delete_forbidden"
        case accountDeleteBusy = "account_delete_busy"
        case dataResetPending = "data_reset_pending"
        case dataResetNotPending = "data_reset_not_pending"
        case invalidAPIKey = "invalid_api_key"
        case missingAPIKey = "missing_api_key"
        case modelSelectionUnavailable = "model_selection_unavailable"
        case payloadTooLarge = "payload_too_large"
    }

    public struct ErrorDetail: Sendable, Codable, Hashable {
        public var code: String
        public var message: String
        public var retryable: Bool

        public init(
            code: String,
            message: String,
            retryable: Bool
        ) {
            self.code = code
            self.message = message
            self.retryable = retryable
        }
    }

    public struct FeedbackRequest: Sendable, Codable, Hashable {
        public struct Value: Sendable, Codable, Hashable {
            public var kind: String
            public var taskID: String?
            public var operationID: String?
            public var connectionID: OpenAPI.ID?
            public var text: String?

            public init(
                kind: String,
                taskID: String? = nil,
                operationID: String? = nil,
                connectionID: OpenAPI.ID? = nil,
                text: String? = nil
            ) {
                self.kind = kind
                self.taskID = taskID
                self.operationID = operationID
                self.connectionID = connectionID
                self.text = text
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case taskID = "task_id"
                case operationID = "operation_id"
                case connectionID = "connection_id"
                case text
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct GoogleDrivePickerRequest: Sendable, Codable, Hashable {
        public var returnTo: String?

        public init(
            returnTo: String? = nil
        ) {
            self.returnTo = returnTo
        }

        enum CodingKeys: String, CodingKey {
            case returnTo = "return_to"
        }
    }

    public struct Health: Sendable, Codable, Hashable {
        public var checkedAt: OpenAPI.Instant?
        public var code: OpenAPI.HealthCode?
        public var message: String?
        public var lastSuccessAt: OpenAPI.Instant?
        public var recovery: OpenAPI.Recovery?
        public var affectedCapabilityIDs: [String]?
        public var retryAfter: OpenAPI.Instant?

        public init(
            checkedAt: OpenAPI.Instant? = nil,
            code: OpenAPI.HealthCode? = nil,
            message: String? = nil,
            lastSuccessAt: OpenAPI.Instant? = nil,
            recovery: OpenAPI.Recovery? = nil,
            affectedCapabilityIDs: [String]? = nil,
            retryAfter: OpenAPI.Instant? = nil
        ) {
            self.checkedAt = checkedAt
            self.code = code
            self.message = message
            self.lastSuccessAt = lastSuccessAt
            self.recovery = recovery
            self.affectedCapabilityIDs = affectedCapabilityIDs
            self.retryAfter = retryAfter
        }

        enum CodingKeys: String, CodingKey {
            case checkedAt = "checked_at"
            case code
            case message
            case lastSuccessAt = "last_success_at"
            case recovery
            case affectedCapabilityIDs = "affected_capability_ids"
            case retryAfter = "retry_after"
        }
    }

    public enum HealthCode: String, Sendable, Codable, Hashable, CaseIterable {
        case ready = "ready"
        case needsSignIn = "needs_sign_in"
        case needsMoreAccess = "needs_more_access"
        case revoked = "revoked"
        case rateLimited = "rate_limited"
        case temporarilyFailing = "temporarily_failing"
        case providerUnavailable = "provider_unavailable"
        case credentialStoreUnavailable = "credential_store_unavailable"
    }

    public struct HomeConnector: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var name: String
        public var status: String
        public var lastSeenAt: OpenAPI.Instant?
        public var version: String?
        public var models: [OpenAPI.HomeConnectorModel]

        public init(
            id: OpenAPI.ID,
            name: String,
            status: String,
            lastSeenAt: OpenAPI.Instant? = nil,
            version: String? = nil,
            models: [OpenAPI.HomeConnectorModel]
        ) {
            self.id = id
            self.name = name
            self.status = status
            self.lastSeenAt = lastSeenAt
            self.version = version
            self.models = models
        }

        enum CodingKeys: String, CodingKey {
            case id
            case name
            case status
            case lastSeenAt = "last_seen_at"
            case version
            case models
        }
    }

    public struct HomeConnectorDownloads: Sendable, Codable, Hashable {
        public var macos: String?
        public var linuxAmd64: String?
        public var linuxArm64: String?

        public init(
            macos: String? = nil,
            linuxAmd64: String? = nil,
            linuxArm64: String? = nil
        ) {
            self.macos = macos
            self.linuxAmd64 = linuxAmd64
            self.linuxArm64 = linuxArm64
        }

        enum CodingKeys: String, CodingKey {
            case macos
            case linuxAmd64 = "linux_amd64"
            case linuxArm64 = "linux_arm64"
        }
    }

    public struct HomeConnectorModel: Sendable, Codable, Hashable {
        public var name: String
        public var digest: String?
        public var capabilities: [String]?

        public init(
            name: String,
            digest: String? = nil,
            capabilities: [String]? = nil
        ) {
            self.name = name
            self.digest = digest
            self.capabilities = capabilities
        }
    }

    public struct HomeConnectorTestResponse: Sendable, Codable, Hashable {
        public var status: String

        public init(
            status: String
        ) {
            self.status = status
        }
    }

    public struct HomeConnectors: Sendable, Codable, Hashable {
        public var enabled: Bool
        public var devices: [OpenAPI.HomeConnector]
        public var downloads: OpenAPI.HomeConnectorDownloads?

        public init(
            enabled: Bool,
            devices: [OpenAPI.HomeConnector],
            downloads: OpenAPI.HomeConnectorDownloads? = nil
        ) {
            self.enabled = enabled
            self.devices = devices
            self.downloads = downloads
        }
    }

    public struct HomePairing: Sendable, Codable, Hashable {
        public var name: String
        public var userCode: String
        public var expiresAt: OpenAPI.Instant

        public init(
            name: String,
            userCode: String,
            expiresAt: OpenAPI.Instant
        ) {
            self.name = name
            self.userCode = userCode
            self.expiresAt = expiresAt
        }

        enum CodingKeys: String, CodingKey {
            case name
            case userCode = "user_code"
            case expiresAt = "expires_at"
        }
    }

    public struct HomePairingRequest: Sendable, Codable, Hashable {
        public var userCode: String

        public init(
            userCode: String
        ) {
            self.userCode = userCode
        }

        enum CodingKeys: String, CodingKey {
            case userCode = "user_code"
        }
    }

    public typealias ID = String

    public struct InboxAction: Sendable, Codable, Hashable {
        public struct Value: Sendable, Codable, Hashable {
            public var action: String

            public init(
                action: String
            ) {
                self.action = action
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct InboxItem: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var deliveryID: String?
        public var title: String
        public var body: String
        public var link: OpenAPI.InboxLink
        public var createdAt: OpenAPI.Instant
        public var acknowledgedAt: OpenAPI.Instant?
        public var revision: OpenAPI.Revision

        public init(
            id: OpenAPI.ID,
            deliveryID: String? = nil,
            title: String,
            body: String,
            link: OpenAPI.InboxLink,
            createdAt: OpenAPI.Instant,
            acknowledgedAt: OpenAPI.Instant? = nil,
            revision: OpenAPI.Revision
        ) {
            self.id = id
            self.deliveryID = deliveryID
            self.title = title
            self.body = body
            self.link = link
            self.createdAt = createdAt
            self.acknowledgedAt = acknowledgedAt
            self.revision = revision
        }

        enum CodingKeys: String, CodingKey {
            case id
            case deliveryID = "delivery_id"
            case title
            case body
            case link
            case createdAt = "created_at"
            case acknowledgedAt = "acknowledged_at"
            case revision
        }
    }

    public struct InboxLink: Sendable, Codable, Hashable {
        public var kind: String
        public var id: String
        public var revision: OpenAPI.Revision?

        public init(
            kind: String,
            id: String,
            revision: OpenAPI.Revision? = nil
        ) {
            self.kind = kind
            self.id = id
            self.revision = revision
        }
    }

    public struct InputPreferences: Sendable, Codable, Hashable {
        public var dictationLanguage: String?

        public init(
            dictationLanguage: String? = nil
        ) {
            self.dictationLanguage = dictationLanguage
        }

        enum CodingKeys: String, CodingKey {
            case dictationLanguage = "dictation_language"
        }
    }

    public typealias Instant = String

    public struct Integration: Sendable, Codable, Hashable {
        public struct PurposeItem: Sendable, Codable, Hashable {
            public var id: String
            public var title: String

            public init(
                id: String,
                title: String
            ) {
                self.id = id
                self.title = title
            }
        }
        public struct CapabilitieItem: Sendable, Codable, Hashable {
            public var id: String
            public var title: String
            public var effect: OpenAPI.Effect
            public var supported: Bool
            public var reason: String?
            public var scopes: [String]?

            public init(
                id: String,
                title: String,
                effect: OpenAPI.Effect,
                supported: Bool,
                reason: String? = nil,
                scopes: [String]? = nil
            ) {
                self.id = id
                self.title = title
                self.effect = effect
                self.supported = supported
                self.reason = reason
                self.scopes = scopes
            }
        }

        public var id: String
        public var title: String
        public var description: String
        public var categories: [String]
        public var icon: String?
        public var source: String
        public var installationID: OpenAPI.ID?
        public var auth: String
        public var multipleAccounts: Bool
        public var purposes: [PurposeItem]
        public var capabilities: [CapabilitieItem]
        public var availability: String
        public var availabilityReason: String?
        public var connectionIDs: [OpenAPI.ID]

        public init(
            id: String,
            title: String,
            description: String,
            categories: [String],
            icon: String? = nil,
            source: String,
            installationID: OpenAPI.ID? = nil,
            auth: String,
            multipleAccounts: Bool,
            purposes: [PurposeItem],
            capabilities: [CapabilitieItem],
            availability: String,
            availabilityReason: String? = nil,
            connectionIDs: [OpenAPI.ID]
        ) {
            self.id = id
            self.title = title
            self.description = description
            self.categories = categories
            self.icon = icon
            self.source = source
            self.installationID = installationID
            self.auth = auth
            self.multipleAccounts = multipleAccounts
            self.purposes = purposes
            self.capabilities = capabilities
            self.availability = availability
            self.availabilityReason = availabilityReason
            self.connectionIDs = connectionIDs
        }

        enum CodingKeys: String, CodingKey {
            case id
            case title
            case description
            case categories
            case icon
            case source
            case installationID = "installation_id"
            case auth
            case multipleAccounts = "multiple_accounts"
            case purposes
            case capabilities
            case availability
            case availabilityReason = "availability_reason"
            case connectionIDs = "connection_ids"
        }
    }

    public enum Interaction: Sendable, Codable, Hashable {
        public struct QuestionPayload: Sendable, Codable, Hashable {
            public var id: OpenAPI.ID
            public var revision: OpenAPI.Revision
            public var taskID: String?
            public var instructionRevision: Int?
            public var conversationID: String?
            public var automationID: OpenAPI.ID?
            public var status: String
            public var surface: String?
            public var prompt: String
            public var actionDigest: String?
            public var expiresAt: OpenAPI.Instant?
            public var createdAt: OpenAPI.Instant?
            public var resolvedAt: OpenAPI.Instant?
            public var legacyQuestionID: String?
            public var supersededBy: OpenAPI.ID?
            public var taskTitle: String?
            public var resolution: OpenAPI.InteractionResolution?
            public var kind: String
            public var payload: OpenAPI.QuestionPayload

            public init(
                id: OpenAPI.ID,
                revision: OpenAPI.Revision,
                taskID: String? = nil,
                instructionRevision: Int? = nil,
                conversationID: String? = nil,
                automationID: OpenAPI.ID? = nil,
                status: String,
                surface: String? = nil,
                prompt: String,
                actionDigest: String? = nil,
                expiresAt: OpenAPI.Instant? = nil,
                createdAt: OpenAPI.Instant? = nil,
                resolvedAt: OpenAPI.Instant? = nil,
                legacyQuestionID: String? = nil,
                supersededBy: OpenAPI.ID? = nil,
                taskTitle: String? = nil,
                resolution: OpenAPI.InteractionResolution? = nil,
                kind: String,
                payload: OpenAPI.QuestionPayload
            ) {
                self.id = id
                self.revision = revision
                self.taskID = taskID
                self.instructionRevision = instructionRevision
                self.conversationID = conversationID
                self.automationID = automationID
                self.status = status
                self.surface = surface
                self.prompt = prompt
                self.actionDigest = actionDigest
                self.expiresAt = expiresAt
                self.createdAt = createdAt
                self.resolvedAt = resolvedAt
                self.legacyQuestionID = legacyQuestionID
                self.supersededBy = supersededBy
                self.taskTitle = taskTitle
                self.resolution = resolution
                self.kind = kind
                self.payload = payload
            }

            enum CodingKeys: String, CodingKey {
                case id
                case revision
                case taskID = "task_id"
                case instructionRevision = "instruction_revision"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case status
                case surface
                case prompt
                case actionDigest = "action_digest"
                case expiresAt = "expires_at"
                case createdAt = "created_at"
                case resolvedAt = "resolved_at"
                case legacyQuestionID = "legacy_question_id"
                case supersededBy = "superseded_by"
                case taskTitle = "task_title"
                case resolution
                case kind
                case payload
            }
        }
        public struct ApprovalPayload: Sendable, Codable, Hashable {
            public var id: OpenAPI.ID
            public var revision: OpenAPI.Revision
            public var taskID: String?
            public var instructionRevision: Int?
            public var conversationID: String?
            public var automationID: OpenAPI.ID?
            public var status: String
            public var surface: String?
            public var prompt: String
            public var actionDigest: String?
            public var expiresAt: OpenAPI.Instant?
            public var createdAt: OpenAPI.Instant?
            public var resolvedAt: OpenAPI.Instant?
            public var legacyQuestionID: String?
            public var supersededBy: OpenAPI.ID?
            public var taskTitle: String?
            public var resolution: OpenAPI.InteractionResolution?
            public var kind: String
            public var payload: OpenAPI.ApprovalPayload

            public init(
                id: OpenAPI.ID,
                revision: OpenAPI.Revision,
                taskID: String? = nil,
                instructionRevision: Int? = nil,
                conversationID: String? = nil,
                automationID: OpenAPI.ID? = nil,
                status: String,
                surface: String? = nil,
                prompt: String,
                actionDigest: String? = nil,
                expiresAt: OpenAPI.Instant? = nil,
                createdAt: OpenAPI.Instant? = nil,
                resolvedAt: OpenAPI.Instant? = nil,
                legacyQuestionID: String? = nil,
                supersededBy: OpenAPI.ID? = nil,
                taskTitle: String? = nil,
                resolution: OpenAPI.InteractionResolution? = nil,
                kind: String,
                payload: OpenAPI.ApprovalPayload
            ) {
                self.id = id
                self.revision = revision
                self.taskID = taskID
                self.instructionRevision = instructionRevision
                self.conversationID = conversationID
                self.automationID = automationID
                self.status = status
                self.surface = surface
                self.prompt = prompt
                self.actionDigest = actionDigest
                self.expiresAt = expiresAt
                self.createdAt = createdAt
                self.resolvedAt = resolvedAt
                self.legacyQuestionID = legacyQuestionID
                self.supersededBy = supersededBy
                self.taskTitle = taskTitle
                self.resolution = resolution
                self.kind = kind
                self.payload = payload
            }

            enum CodingKeys: String, CodingKey {
                case id
                case revision
                case taskID = "task_id"
                case instructionRevision = "instruction_revision"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case status
                case surface
                case prompt
                case actionDigest = "action_digest"
                case expiresAt = "expires_at"
                case createdAt = "created_at"
                case resolvedAt = "resolved_at"
                case legacyQuestionID = "legacy_question_id"
                case supersededBy = "superseded_by"
                case taskTitle = "task_title"
                case resolution
                case kind
                case payload
            }
        }
        public struct AccountSelectionPayload: Sendable, Codable, Hashable {
            public var id: OpenAPI.ID
            public var revision: OpenAPI.Revision
            public var taskID: String?
            public var instructionRevision: Int?
            public var conversationID: String?
            public var automationID: OpenAPI.ID?
            public var status: String
            public var surface: String?
            public var prompt: String
            public var actionDigest: String?
            public var expiresAt: OpenAPI.Instant?
            public var createdAt: OpenAPI.Instant?
            public var resolvedAt: OpenAPI.Instant?
            public var legacyQuestionID: String?
            public var supersededBy: OpenAPI.ID?
            public var taskTitle: String?
            public var resolution: OpenAPI.InteractionResolution?
            public var kind: String
            public var payload: OpenAPI.AccountSelectionPayload

            public init(
                id: OpenAPI.ID,
                revision: OpenAPI.Revision,
                taskID: String? = nil,
                instructionRevision: Int? = nil,
                conversationID: String? = nil,
                automationID: OpenAPI.ID? = nil,
                status: String,
                surface: String? = nil,
                prompt: String,
                actionDigest: String? = nil,
                expiresAt: OpenAPI.Instant? = nil,
                createdAt: OpenAPI.Instant? = nil,
                resolvedAt: OpenAPI.Instant? = nil,
                legacyQuestionID: String? = nil,
                supersededBy: OpenAPI.ID? = nil,
                taskTitle: String? = nil,
                resolution: OpenAPI.InteractionResolution? = nil,
                kind: String,
                payload: OpenAPI.AccountSelectionPayload
            ) {
                self.id = id
                self.revision = revision
                self.taskID = taskID
                self.instructionRevision = instructionRevision
                self.conversationID = conversationID
                self.automationID = automationID
                self.status = status
                self.surface = surface
                self.prompt = prompt
                self.actionDigest = actionDigest
                self.expiresAt = expiresAt
                self.createdAt = createdAt
                self.resolvedAt = resolvedAt
                self.legacyQuestionID = legacyQuestionID
                self.supersededBy = supersededBy
                self.taskTitle = taskTitle
                self.resolution = resolution
                self.kind = kind
                self.payload = payload
            }

            enum CodingKeys: String, CodingKey {
                case id
                case revision
                case taskID = "task_id"
                case instructionRevision = "instruction_revision"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case status
                case surface
                case prompt
                case actionDigest = "action_digest"
                case expiresAt = "expires_at"
                case createdAt = "created_at"
                case resolvedAt = "resolved_at"
                case legacyQuestionID = "legacy_question_id"
                case supersededBy = "superseded_by"
                case taskTitle = "task_title"
                case resolution
                case kind
                case payload
            }
        }
        public struct SignInPayload: Sendable, Codable, Hashable {
            public var id: OpenAPI.ID
            public var revision: OpenAPI.Revision
            public var taskID: String?
            public var instructionRevision: Int?
            public var conversationID: String?
            public var automationID: OpenAPI.ID?
            public var status: String
            public var surface: String?
            public var prompt: String
            public var actionDigest: String?
            public var expiresAt: OpenAPI.Instant?
            public var createdAt: OpenAPI.Instant?
            public var resolvedAt: OpenAPI.Instant?
            public var legacyQuestionID: String?
            public var supersededBy: OpenAPI.ID?
            public var taskTitle: String?
            public var resolution: OpenAPI.InteractionResolution?
            public var kind: String
            public var payload: OpenAPI.SignInPayload

            public init(
                id: OpenAPI.ID,
                revision: OpenAPI.Revision,
                taskID: String? = nil,
                instructionRevision: Int? = nil,
                conversationID: String? = nil,
                automationID: OpenAPI.ID? = nil,
                status: String,
                surface: String? = nil,
                prompt: String,
                actionDigest: String? = nil,
                expiresAt: OpenAPI.Instant? = nil,
                createdAt: OpenAPI.Instant? = nil,
                resolvedAt: OpenAPI.Instant? = nil,
                legacyQuestionID: String? = nil,
                supersededBy: OpenAPI.ID? = nil,
                taskTitle: String? = nil,
                resolution: OpenAPI.InteractionResolution? = nil,
                kind: String,
                payload: OpenAPI.SignInPayload
            ) {
                self.id = id
                self.revision = revision
                self.taskID = taskID
                self.instructionRevision = instructionRevision
                self.conversationID = conversationID
                self.automationID = automationID
                self.status = status
                self.surface = surface
                self.prompt = prompt
                self.actionDigest = actionDigest
                self.expiresAt = expiresAt
                self.createdAt = createdAt
                self.resolvedAt = resolvedAt
                self.legacyQuestionID = legacyQuestionID
                self.supersededBy = supersededBy
                self.taskTitle = taskTitle
                self.resolution = resolution
                self.kind = kind
                self.payload = payload
            }

            enum CodingKeys: String, CodingKey {
                case id
                case revision
                case taskID = "task_id"
                case instructionRevision = "instruction_revision"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case status
                case surface
                case prompt
                case actionDigest = "action_digest"
                case expiresAt = "expires_at"
                case createdAt = "created_at"
                case resolvedAt = "resolved_at"
                case legacyQuestionID = "legacy_question_id"
                case supersededBy = "superseded_by"
                case taskTitle = "task_title"
                case resolution
                case kind
                case payload
            }
        }
        public struct ComputerTakeoverPayload: Sendable, Codable, Hashable {
            public var id: OpenAPI.ID
            public var revision: OpenAPI.Revision
            public var taskID: String?
            public var instructionRevision: Int?
            public var conversationID: String?
            public var automationID: OpenAPI.ID?
            public var status: String
            public var surface: String?
            public var prompt: String
            public var actionDigest: String?
            public var expiresAt: OpenAPI.Instant?
            public var createdAt: OpenAPI.Instant?
            public var resolvedAt: OpenAPI.Instant?
            public var legacyQuestionID: String?
            public var supersededBy: OpenAPI.ID?
            public var taskTitle: String?
            public var resolution: OpenAPI.InteractionResolution?
            public var kind: String
            public var payload: OpenAPI.ComputerTakeoverPayload

            public init(
                id: OpenAPI.ID,
                revision: OpenAPI.Revision,
                taskID: String? = nil,
                instructionRevision: Int? = nil,
                conversationID: String? = nil,
                automationID: OpenAPI.ID? = nil,
                status: String,
                surface: String? = nil,
                prompt: String,
                actionDigest: String? = nil,
                expiresAt: OpenAPI.Instant? = nil,
                createdAt: OpenAPI.Instant? = nil,
                resolvedAt: OpenAPI.Instant? = nil,
                legacyQuestionID: String? = nil,
                supersededBy: OpenAPI.ID? = nil,
                taskTitle: String? = nil,
                resolution: OpenAPI.InteractionResolution? = nil,
                kind: String,
                payload: OpenAPI.ComputerTakeoverPayload
            ) {
                self.id = id
                self.revision = revision
                self.taskID = taskID
                self.instructionRevision = instructionRevision
                self.conversationID = conversationID
                self.automationID = automationID
                self.status = status
                self.surface = surface
                self.prompt = prompt
                self.actionDigest = actionDigest
                self.expiresAt = expiresAt
                self.createdAt = createdAt
                self.resolvedAt = resolvedAt
                self.legacyQuestionID = legacyQuestionID
                self.supersededBy = supersededBy
                self.taskTitle = taskTitle
                self.resolution = resolution
                self.kind = kind
                self.payload = payload
            }

            enum CodingKeys: String, CodingKey {
                case id
                case revision
                case taskID = "task_id"
                case instructionRevision = "instruction_revision"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case status
                case surface
                case prompt
                case actionDigest = "action_digest"
                case expiresAt = "expires_at"
                case createdAt = "created_at"
                case resolvedAt = "resolved_at"
                case legacyQuestionID = "legacy_question_id"
                case supersededBy = "superseded_by"
                case taskTitle = "task_title"
                case resolution
                case kind
                case payload
            }
        }

        case question(QuestionPayload)
        case approval(ApprovalPayload)
        case accountSelection(AccountSelectionPayload)
        case signIn(SignInPayload)
        case computerTakeover(ComputerTakeoverPayload)

        private enum DiscriminatorKey: String, CodingKey {
            case discriminator = "kind"
        }

        public init(from decoder: any Decoder) throws {
            let container = try decoder.container(keyedBy: DiscriminatorKey.self)
            let kind = try container.decode(String.self, forKey: .discriminator)
            switch kind {
            case "question":
                self = .question(try QuestionPayload(from: decoder))
            case "approval":
                self = .approval(try ApprovalPayload(from: decoder))
            case "account_selection":
                self = .accountSelection(try AccountSelectionPayload(from: decoder))
            case "sign_in":
                self = .signIn(try SignInPayload(from: decoder))
            case "computer_takeover":
                self = .computerTakeover(try ComputerTakeoverPayload(from: decoder))
            default:
                throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Interaction kind: \(String(describing: kind))")
            }
        }

        public func encode(to encoder: any Encoder) throws {
            switch self {
            case .question(let value):
                try value.encode(to: encoder)
            case .approval(let value):
                try value.encode(to: encoder)
            case .accountSelection(let value):
                try value.encode(to: encoder)
            case .signIn(let value):
                try value.encode(to: encoder)
            case .computerTakeover(let value):
                try value.encode(to: encoder)
            }
        }
    }

    public struct InteractionBase: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var revision: OpenAPI.Revision
        public var taskID: String?
        public var instructionRevision: Int?
        public var conversationID: String?
        public var automationID: OpenAPI.ID?
        public var status: String
        public var surface: String?
        public var prompt: String
        public var actionDigest: String?
        public var expiresAt: OpenAPI.Instant?
        public var createdAt: OpenAPI.Instant?
        public var resolvedAt: OpenAPI.Instant?
        public var legacyQuestionID: String?
        public var supersededBy: OpenAPI.ID?
        public var taskTitle: String?
        public var resolution: OpenAPI.InteractionResolution?

        public init(
            id: OpenAPI.ID,
            revision: OpenAPI.Revision,
            taskID: String? = nil,
            instructionRevision: Int? = nil,
            conversationID: String? = nil,
            automationID: OpenAPI.ID? = nil,
            status: String,
            surface: String? = nil,
            prompt: String,
            actionDigest: String? = nil,
            expiresAt: OpenAPI.Instant? = nil,
            createdAt: OpenAPI.Instant? = nil,
            resolvedAt: OpenAPI.Instant? = nil,
            legacyQuestionID: String? = nil,
            supersededBy: OpenAPI.ID? = nil,
            taskTitle: String? = nil,
            resolution: OpenAPI.InteractionResolution? = nil
        ) {
            self.id = id
            self.revision = revision
            self.taskID = taskID
            self.instructionRevision = instructionRevision
            self.conversationID = conversationID
            self.automationID = automationID
            self.status = status
            self.surface = surface
            self.prompt = prompt
            self.actionDigest = actionDigest
            self.expiresAt = expiresAt
            self.createdAt = createdAt
            self.resolvedAt = resolvedAt
            self.legacyQuestionID = legacyQuestionID
            self.supersededBy = supersededBy
            self.taskTitle = taskTitle
            self.resolution = resolution
        }

        enum CodingKeys: String, CodingKey {
            case id
            case revision
            case taskID = "task_id"
            case instructionRevision = "instruction_revision"
            case conversationID = "conversation_id"
            case automationID = "automation_id"
            case status
            case surface
            case prompt
            case actionDigest = "action_digest"
            case expiresAt = "expires_at"
            case createdAt = "created_at"
            case resolvedAt = "resolved_at"
            case legacyQuestionID = "legacy_question_id"
            case supersededBy = "superseded_by"
            case taskTitle = "task_title"
            case resolution
        }
    }

    public struct InteractionResolution: Sendable, Codable, Hashable {
        public var kind: String
        public var text: String?
        public var connectionID: OpenAPI.ID?
        public var at: OpenAPI.Instant

        public init(
            kind: String,
            text: String? = nil,
            connectionID: OpenAPI.ID? = nil,
            at: OpenAPI.Instant
        ) {
            self.kind = kind
            self.text = text
            self.connectionID = connectionID
            self.at = at
        }

        enum CodingKeys: String, CodingKey {
            case kind
            case text
            case connectionID = "connection_id"
            case at
        }
    }

    public struct InteractionResponse: Sendable, Codable, Hashable {
        public enum Value: Sendable, Codable, Hashable {
            public struct Variant1Payload: Sendable, Codable, Hashable {
                public var kind: String
                public var text: String
                public var optionIDs: [String]?

                public init(
                    kind: String,
                    text: String,
                    optionIDs: [String]? = nil
                ) {
                    self.kind = kind
                    self.text = text
                    self.optionIDs = optionIDs
                }

                enum CodingKeys: String, CodingKey {
                    case kind
                    case text
                    case optionIDs = "option_ids"
                }
            }
            public struct Variant2Payload: Sendable, Codable, Hashable {
                public struct EditedFields: Sendable, Codable, Hashable {
                    public var subject: String?
                    public var body: String?

                    public init(
                        subject: String? = nil,
                        body: String? = nil
                    ) {
                        self.subject = subject
                        self.body = body
                    }
                }

                public var kind: String
                public var actionDigest: String
                public var rememberRule: Bool?
                public var editedFields: EditedFields?

                public init(
                    kind: String,
                    actionDigest: String,
                    rememberRule: Bool? = nil,
                    editedFields: EditedFields? = nil
                ) {
                    self.kind = kind
                    self.actionDigest = actionDigest
                    self.rememberRule = rememberRule
                    self.editedFields = editedFields
                }

                enum CodingKeys: String, CodingKey {
                    case kind
                    case actionDigest = "action_digest"
                    case rememberRule = "remember_rule"
                    case editedFields = "edited_fields"
                }
            }
            public struct Variant3Payload: Sendable, Codable, Hashable {
                public var kind: String
                public var connectionID: OpenAPI.ID
                public var rememberAsDefault: Bool?

                public init(
                    kind: String,
                    connectionID: OpenAPI.ID,
                    rememberAsDefault: Bool? = nil
                ) {
                    self.kind = kind
                    self.connectionID = connectionID
                    self.rememberAsDefault = rememberAsDefault
                }

                enum CodingKeys: String, CodingKey {
                    case kind
                    case connectionID = "connection_id"
                    case rememberAsDefault = "remember_as_default"
                }
            }
            public struct Variant4Payload: Sendable, Codable, Hashable {
                public var kind: String

                public init(
                    kind: String
                ) {
                    self.kind = kind
                }
            }
            public struct Variant5Payload: Sendable, Codable, Hashable {
                public var kind: String
                public var text: String?

                public init(
                    kind: String,
                    text: String? = nil
                ) {
                    self.kind = kind
                    self.text = text
                }
            }

            case variant1(Variant1Payload)
            case variant2(Variant2Payload)
            case variant3(Variant3Payload)
            case variant4(Variant4Payload)
            case variant5(Variant5Payload)

            public init(from decoder: any Decoder) throws {
                if let value = try? Variant1Payload(from: decoder) {
                    self = .variant1(value)
                    return
                }
                if let value = try? Variant2Payload(from: decoder) {
                    self = .variant2(value)
                    return
                }
                if let value = try? Variant3Payload(from: decoder) {
                    self = .variant3(value)
                    return
                }
                if let value = try? Variant4Payload(from: decoder) {
                    self = .variant4(value)
                    return
                }
                if let value = try? Variant5Payload(from: decoder) {
                    self = .variant5(value)
                    return
                }
                throw DecodingError.dataCorrupted(DecodingError.Context(codingPath: decoder.codingPath, debugDescription: "Could not decode Value"))
            }

            public func encode(to encoder: any Encoder) throws {
                switch self {
                case .variant1(let value):
                    try value.encode(to: encoder)
                case .variant2(let value):
                    try value.encode(to: encoder)
                case .variant3(let value):
                    try value.encode(to: encoder)
                case .variant4(let value):
                    try value.encode(to: encoder)
                case .variant5(let value):
                    try value.encode(to: encoder)
                }
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct LibraryAction: Sendable, Codable, Hashable {
        public enum Value: Sendable, Codable, Hashable {
            public struct RevisePayload: Sendable, Codable, Hashable {
                public var action: String
                public var instruction: String

                public init(
                    action: String,
                    instruction: String
                ) {
                    self.action = action
                    self.instruction = instruction
                }
            }
            public struct ExportPayload: Sendable, Codable, Hashable {
                public var action: String
                public var format: String

                public init(
                    action: String,
                    format: String
                ) {
                    self.action = action
                    self.format = format
                }
            }
            public struct DeletePayload: Sendable, Codable, Hashable {
                public var action: String
                public var scope: String

                public init(
                    action: String,
                    scope: String
                ) {
                    self.action = action
                    self.scope = scope
                }
            }

            case revise(RevisePayload)
            case export(ExportPayload)
            case delete(DeletePayload)

            private enum DiscriminatorKey: String, CodingKey {
                case discriminator = "action"
            }

            public init(from decoder: any Decoder) throws {
                let container = try decoder.container(keyedBy: DiscriminatorKey.self)
                let kind = try container.decode(String.self, forKey: .discriminator)
                switch kind {
                case "revise":
                    self = .revise(try RevisePayload(from: decoder))
                case "export":
                    self = .export(try ExportPayload(from: decoder))
                case "delete":
                    self = .delete(try DeletePayload(from: decoder))
                default:
                    throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Value action: \(String(describing: kind))")
                }
            }

            public func encode(to encoder: any Encoder) throws {
                switch self {
                case .revise(let value):
                    try value.encode(to: encoder)
                case .export(let value):
                    try value.encode(to: encoder)
                case .delete(let value):
                    try value.encode(to: encoder)
                }
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public enum LibraryItem: Sendable, Codable, Hashable {
        public struct CalendarEventPayload: Sendable, Codable, Hashable {
            public var id: String
            public var rootID: String
            public var version: Int
            public var revises: String?
            public var supersededBy: String?
            public var kind: String
            public var title: String
            public var name: String?
            public var mediaType: String?
            public var size: Int?
            public var url: String?
            public var taskID: String?
            public var conversationID: String?
            public var automationID: String?
            public var provenance: OpenAPI.Provenance
            public var indexStatus: String?
            public var createdAt: OpenAPI.Instant
            public var updatedAt: OpenAPI.Instant?
            public var deletedAt: OpenAPI.Instant?
            public var collection: String?
            public var previewText: String?
            public var excerpt: String?
            public var deleteEffect: String?
            public var revision: OpenAPI.Revision
            public var dataKind: String
            public var data: OpenAPI.CalendarEvent?

            public init(
                id: String,
                rootID: String,
                version: Int,
                revises: String? = nil,
                supersededBy: String? = nil,
                kind: String,
                title: String,
                name: String? = nil,
                mediaType: String? = nil,
                size: Int? = nil,
                url: String? = nil,
                taskID: String? = nil,
                conversationID: String? = nil,
                automationID: String? = nil,
                provenance: OpenAPI.Provenance,
                indexStatus: String? = nil,
                createdAt: OpenAPI.Instant,
                updatedAt: OpenAPI.Instant? = nil,
                deletedAt: OpenAPI.Instant? = nil,
                collection: String? = nil,
                previewText: String? = nil,
                excerpt: String? = nil,
                deleteEffect: String? = nil,
                revision: OpenAPI.Revision,
                dataKind: String,
                data: OpenAPI.CalendarEvent? = nil
            ) {
                self.id = id
                self.rootID = rootID
                self.version = version
                self.revises = revises
                self.supersededBy = supersededBy
                self.kind = kind
                self.title = title
                self.name = name
                self.mediaType = mediaType
                self.size = size
                self.url = url
                self.taskID = taskID
                self.conversationID = conversationID
                self.automationID = automationID
                self.provenance = provenance
                self.indexStatus = indexStatus
                self.createdAt = createdAt
                self.updatedAt = updatedAt
                self.deletedAt = deletedAt
                self.collection = collection
                self.previewText = previewText
                self.excerpt = excerpt
                self.deleteEffect = deleteEffect
                self.revision = revision
                self.dataKind = dataKind
                self.data = data
            }

            enum CodingKeys: String, CodingKey {
                case id
                case rootID = "root_id"
                case version
                case revises
                case supersededBy = "superseded_by"
                case kind
                case title
                case name
                case mediaType = "media_type"
                case size
                case url
                case taskID = "task_id"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case provenance
                case indexStatus = "index_status"
                case createdAt = "created_at"
                case updatedAt = "updated_at"
                case deletedAt = "deleted_at"
                case collection
                case previewText = "preview_text"
                case excerpt
                case deleteEffect = "delete_effect"
                case revision
                case dataKind = "data_kind"
                case data
            }
        }
        public struct MailDraftPayload: Sendable, Codable, Hashable {
            public var id: String
            public var rootID: String
            public var version: Int
            public var revises: String?
            public var supersededBy: String?
            public var kind: String
            public var title: String
            public var name: String?
            public var mediaType: String?
            public var size: Int?
            public var url: String?
            public var taskID: String?
            public var conversationID: String?
            public var automationID: String?
            public var provenance: OpenAPI.Provenance
            public var indexStatus: String?
            public var createdAt: OpenAPI.Instant
            public var updatedAt: OpenAPI.Instant?
            public var deletedAt: OpenAPI.Instant?
            public var collection: String?
            public var previewText: String?
            public var excerpt: String?
            public var deleteEffect: String?
            public var revision: OpenAPI.Revision
            public var dataKind: String
            public var data: OpenAPI.MailDraft?

            public init(
                id: String,
                rootID: String,
                version: Int,
                revises: String? = nil,
                supersededBy: String? = nil,
                kind: String,
                title: String,
                name: String? = nil,
                mediaType: String? = nil,
                size: Int? = nil,
                url: String? = nil,
                taskID: String? = nil,
                conversationID: String? = nil,
                automationID: String? = nil,
                provenance: OpenAPI.Provenance,
                indexStatus: String? = nil,
                createdAt: OpenAPI.Instant,
                updatedAt: OpenAPI.Instant? = nil,
                deletedAt: OpenAPI.Instant? = nil,
                collection: String? = nil,
                previewText: String? = nil,
                excerpt: String? = nil,
                deleteEffect: String? = nil,
                revision: OpenAPI.Revision,
                dataKind: String,
                data: OpenAPI.MailDraft? = nil
            ) {
                self.id = id
                self.rootID = rootID
                self.version = version
                self.revises = revises
                self.supersededBy = supersededBy
                self.kind = kind
                self.title = title
                self.name = name
                self.mediaType = mediaType
                self.size = size
                self.url = url
                self.taskID = taskID
                self.conversationID = conversationID
                self.automationID = automationID
                self.provenance = provenance
                self.indexStatus = indexStatus
                self.createdAt = createdAt
                self.updatedAt = updatedAt
                self.deletedAt = deletedAt
                self.collection = collection
                self.previewText = previewText
                self.excerpt = excerpt
                self.deleteEffect = deleteEffect
                self.revision = revision
                self.dataKind = dataKind
                self.data = data
            }

            enum CodingKeys: String, CodingKey {
                case id
                case rootID = "root_id"
                case version
                case revises
                case supersededBy = "superseded_by"
                case kind
                case title
                case name
                case mediaType = "media_type"
                case size
                case url
                case taskID = "task_id"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case provenance
                case indexStatus = "index_status"
                case createdAt = "created_at"
                case updatedAt = "updated_at"
                case deletedAt = "deleted_at"
                case collection
                case previewText = "preview_text"
                case excerpt
                case deleteEffect = "delete_effect"
                case revision
                case dataKind = "data_kind"
                case data
            }
        }
        public struct MailMessagePayload: Sendable, Codable, Hashable {
            public var id: String
            public var rootID: String
            public var version: Int
            public var revises: String?
            public var supersededBy: String?
            public var kind: String
            public var title: String
            public var name: String?
            public var mediaType: String?
            public var size: Int?
            public var url: String?
            public var taskID: String?
            public var conversationID: String?
            public var automationID: String?
            public var provenance: OpenAPI.Provenance
            public var indexStatus: String?
            public var createdAt: OpenAPI.Instant
            public var updatedAt: OpenAPI.Instant?
            public var deletedAt: OpenAPI.Instant?
            public var collection: String?
            public var previewText: String?
            public var excerpt: String?
            public var deleteEffect: String?
            public var revision: OpenAPI.Revision
            public var dataKind: String
            public var data: OpenAPI.MailMessage?

            public init(
                id: String,
                rootID: String,
                version: Int,
                revises: String? = nil,
                supersededBy: String? = nil,
                kind: String,
                title: String,
                name: String? = nil,
                mediaType: String? = nil,
                size: Int? = nil,
                url: String? = nil,
                taskID: String? = nil,
                conversationID: String? = nil,
                automationID: String? = nil,
                provenance: OpenAPI.Provenance,
                indexStatus: String? = nil,
                createdAt: OpenAPI.Instant,
                updatedAt: OpenAPI.Instant? = nil,
                deletedAt: OpenAPI.Instant? = nil,
                collection: String? = nil,
                previewText: String? = nil,
                excerpt: String? = nil,
                deleteEffect: String? = nil,
                revision: OpenAPI.Revision,
                dataKind: String,
                data: OpenAPI.MailMessage? = nil
            ) {
                self.id = id
                self.rootID = rootID
                self.version = version
                self.revises = revises
                self.supersededBy = supersededBy
                self.kind = kind
                self.title = title
                self.name = name
                self.mediaType = mediaType
                self.size = size
                self.url = url
                self.taskID = taskID
                self.conversationID = conversationID
                self.automationID = automationID
                self.provenance = provenance
                self.indexStatus = indexStatus
                self.createdAt = createdAt
                self.updatedAt = updatedAt
                self.deletedAt = deletedAt
                self.collection = collection
                self.previewText = previewText
                self.excerpt = excerpt
                self.deleteEffect = deleteEffect
                self.revision = revision
                self.dataKind = dataKind
                self.data = data
            }

            enum CodingKeys: String, CodingKey {
                case id
                case rootID = "root_id"
                case version
                case revises
                case supersededBy = "superseded_by"
                case kind
                case title
                case name
                case mediaType = "media_type"
                case size
                case url
                case taskID = "task_id"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case provenance
                case indexStatus = "index_status"
                case createdAt = "created_at"
                case updatedAt = "updated_at"
                case deletedAt = "deleted_at"
                case collection
                case previewText = "preview_text"
                case excerpt
                case deleteEffect = "delete_effect"
                case revision
                case dataKind = "data_kind"
                case data
            }
        }
        public struct AvailabilityPayload: Sendable, Codable, Hashable {
            public var id: String
            public var rootID: String
            public var version: Int
            public var revises: String?
            public var supersededBy: String?
            public var kind: String
            public var title: String
            public var name: String?
            public var mediaType: String?
            public var size: Int?
            public var url: String?
            public var taskID: String?
            public var conversationID: String?
            public var automationID: String?
            public var provenance: OpenAPI.Provenance
            public var indexStatus: String?
            public var createdAt: OpenAPI.Instant
            public var updatedAt: OpenAPI.Instant?
            public var deletedAt: OpenAPI.Instant?
            public var collection: String?
            public var previewText: String?
            public var excerpt: String?
            public var deleteEffect: String?
            public var revision: OpenAPI.Revision
            public var dataKind: String
            public var data: OpenAPI.AvailabilityProposal?

            public init(
                id: String,
                rootID: String,
                version: Int,
                revises: String? = nil,
                supersededBy: String? = nil,
                kind: String,
                title: String,
                name: String? = nil,
                mediaType: String? = nil,
                size: Int? = nil,
                url: String? = nil,
                taskID: String? = nil,
                conversationID: String? = nil,
                automationID: String? = nil,
                provenance: OpenAPI.Provenance,
                indexStatus: String? = nil,
                createdAt: OpenAPI.Instant,
                updatedAt: OpenAPI.Instant? = nil,
                deletedAt: OpenAPI.Instant? = nil,
                collection: String? = nil,
                previewText: String? = nil,
                excerpt: String? = nil,
                deleteEffect: String? = nil,
                revision: OpenAPI.Revision,
                dataKind: String,
                data: OpenAPI.AvailabilityProposal? = nil
            ) {
                self.id = id
                self.rootID = rootID
                self.version = version
                self.revises = revises
                self.supersededBy = supersededBy
                self.kind = kind
                self.title = title
                self.name = name
                self.mediaType = mediaType
                self.size = size
                self.url = url
                self.taskID = taskID
                self.conversationID = conversationID
                self.automationID = automationID
                self.provenance = provenance
                self.indexStatus = indexStatus
                self.createdAt = createdAt
                self.updatedAt = updatedAt
                self.deletedAt = deletedAt
                self.collection = collection
                self.previewText = previewText
                self.excerpt = excerpt
                self.deleteEffect = deleteEffect
                self.revision = revision
                self.dataKind = dataKind
                self.data = data
            }

            enum CodingKeys: String, CodingKey {
                case id
                case rootID = "root_id"
                case version
                case revises
                case supersededBy = "superseded_by"
                case kind
                case title
                case name
                case mediaType = "media_type"
                case size
                case url
                case taskID = "task_id"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case provenance
                case indexStatus = "index_status"
                case createdAt = "created_at"
                case updatedAt = "updated_at"
                case deletedAt = "deleted_at"
                case collection
                case previewText = "preview_text"
                case excerpt
                case deleteEffect = "delete_effect"
                case revision
                case dataKind = "data_kind"
                case data
            }
        }
        public struct ValuePayload: Sendable, Codable, Hashable {
            public var id: String
            public var rootID: String
            public var version: Int
            public var revises: String?
            public var supersededBy: String?
            public var kind: String
            public var title: String
            public var name: String?
            public var mediaType: String?
            public var size: Int?
            public var url: String?
            public var taskID: String?
            public var conversationID: String?
            public var automationID: String?
            public var provenance: OpenAPI.Provenance
            public var indexStatus: String?
            public var createdAt: OpenAPI.Instant
            public var updatedAt: OpenAPI.Instant?
            public var deletedAt: OpenAPI.Instant?
            public var collection: String?
            public var previewText: String?
            public var excerpt: String?
            public var deleteEffect: String?
            public var revision: OpenAPI.Revision
            public var dataKind: String?
            public var data: AnyJSON?

            public init(
                id: String,
                rootID: String,
                version: Int,
                revises: String? = nil,
                supersededBy: String? = nil,
                kind: String,
                title: String,
                name: String? = nil,
                mediaType: String? = nil,
                size: Int? = nil,
                url: String? = nil,
                taskID: String? = nil,
                conversationID: String? = nil,
                automationID: String? = nil,
                provenance: OpenAPI.Provenance,
                indexStatus: String? = nil,
                createdAt: OpenAPI.Instant,
                updatedAt: OpenAPI.Instant? = nil,
                deletedAt: OpenAPI.Instant? = nil,
                collection: String? = nil,
                previewText: String? = nil,
                excerpt: String? = nil,
                deleteEffect: String? = nil,
                revision: OpenAPI.Revision,
                dataKind: String? = nil,
                data: AnyJSON? = nil
            ) {
                self.id = id
                self.rootID = rootID
                self.version = version
                self.revises = revises
                self.supersededBy = supersededBy
                self.kind = kind
                self.title = title
                self.name = name
                self.mediaType = mediaType
                self.size = size
                self.url = url
                self.taskID = taskID
                self.conversationID = conversationID
                self.automationID = automationID
                self.provenance = provenance
                self.indexStatus = indexStatus
                self.createdAt = createdAt
                self.updatedAt = updatedAt
                self.deletedAt = deletedAt
                self.collection = collection
                self.previewText = previewText
                self.excerpt = excerpt
                self.deleteEffect = deleteEffect
                self.revision = revision
                self.dataKind = dataKind
                self.data = data
            }

            enum CodingKeys: String, CodingKey {
                case id
                case rootID = "root_id"
                case version
                case revises
                case supersededBy = "superseded_by"
                case kind
                case title
                case name
                case mediaType = "media_type"
                case size
                case url
                case taskID = "task_id"
                case conversationID = "conversation_id"
                case automationID = "automation_id"
                case provenance
                case indexStatus = "index_status"
                case createdAt = "created_at"
                case updatedAt = "updated_at"
                case deletedAt = "deleted_at"
                case collection
                case previewText = "preview_text"
                case excerpt
                case deleteEffect = "delete_effect"
                case revision
                case dataKind = "data_kind"
                case data
            }
        }

        case calendarEvent(CalendarEventPayload)
        case mailDraft(MailDraftPayload)
        case mailMessage(MailMessagePayload)
        case availability(AvailabilityPayload)
        case value(ValuePayload)

        private enum DiscriminatorKey: String, CodingKey {
            case discriminator = "data_kind"
        }

        public init(from decoder: any Decoder) throws {
            let container = try decoder.container(keyedBy: DiscriminatorKey.self)
            let kind = try container.decodeIfPresent(String.self, forKey: .discriminator)
            switch kind {
            case "calendar_event":
                self = .calendarEvent(try CalendarEventPayload(from: decoder))
            case "mail_draft":
                self = .mailDraft(try MailDraftPayload(from: decoder))
            case "mail_message":
                self = .mailMessage(try MailMessagePayload(from: decoder))
            case "availability":
                self = .availability(try AvailabilityPayload(from: decoder))
            case "", nil:
                self = .value(try ValuePayload(from: decoder))
            default:
                throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported LibraryItem data_kind: \(String(describing: kind))")
            }
        }

        public func encode(to encoder: any Encoder) throws {
            switch self {
            case .calendarEvent(let value):
                try value.encode(to: encoder)
            case .mailDraft(let value):
                try value.encode(to: encoder)
            case .mailMessage(let value):
                try value.encode(to: encoder)
            case .availability(let value):
                try value.encode(to: encoder)
            case .value(let value):
                try value.encode(to: encoder)
            }
        }
    }

    public struct LibraryItemBase: Sendable, Codable, Hashable {
        public var id: String
        public var rootID: String
        public var version: Int
        public var revises: String?
        public var supersededBy: String?
        public var kind: String
        public var title: String
        public var name: String?
        public var mediaType: String?
        public var size: Int?
        public var url: String?
        public var taskID: String?
        public var conversationID: String?
        public var automationID: String?
        public var provenance: OpenAPI.Provenance
        public var indexStatus: String?
        public var createdAt: OpenAPI.Instant
        public var updatedAt: OpenAPI.Instant?
        public var deletedAt: OpenAPI.Instant?
        public var collection: String?
        public var previewText: String?
        public var excerpt: String?
        public var deleteEffect: String?
        public var revision: OpenAPI.Revision

        public init(
            id: String,
            rootID: String,
            version: Int,
            revises: String? = nil,
            supersededBy: String? = nil,
            kind: String,
            title: String,
            name: String? = nil,
            mediaType: String? = nil,
            size: Int? = nil,
            url: String? = nil,
            taskID: String? = nil,
            conversationID: String? = nil,
            automationID: String? = nil,
            provenance: OpenAPI.Provenance,
            indexStatus: String? = nil,
            createdAt: OpenAPI.Instant,
            updatedAt: OpenAPI.Instant? = nil,
            deletedAt: OpenAPI.Instant? = nil,
            collection: String? = nil,
            previewText: String? = nil,
            excerpt: String? = nil,
            deleteEffect: String? = nil,
            revision: OpenAPI.Revision
        ) {
            self.id = id
            self.rootID = rootID
            self.version = version
            self.revises = revises
            self.supersededBy = supersededBy
            self.kind = kind
            self.title = title
            self.name = name
            self.mediaType = mediaType
            self.size = size
            self.url = url
            self.taskID = taskID
            self.conversationID = conversationID
            self.automationID = automationID
            self.provenance = provenance
            self.indexStatus = indexStatus
            self.createdAt = createdAt
            self.updatedAt = updatedAt
            self.deletedAt = deletedAt
            self.collection = collection
            self.previewText = previewText
            self.excerpt = excerpt
            self.deleteEffect = deleteEffect
            self.revision = revision
        }

        enum CodingKeys: String, CodingKey {
            case id
            case rootID = "root_id"
            case version
            case revises
            case supersededBy = "superseded_by"
            case kind
            case title
            case name
            case mediaType = "media_type"
            case size
            case url
            case taskID = "task_id"
            case conversationID = "conversation_id"
            case automationID = "automation_id"
            case provenance
            case indexStatus = "index_status"
            case createdAt = "created_at"
            case updatedAt = "updated_at"
            case deletedAt = "deleted_at"
            case collection
            case previewText = "preview_text"
            case excerpt
            case deleteEffect = "delete_effect"
            case revision
        }
    }

    public struct LibraryItemChange: Sendable, Codable, Hashable {
        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: OpenAPI.LibraryItemEditable

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: OpenAPI.LibraryItemEditable
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct LibraryItemEditable: Sendable, Codable, Hashable {
        public var title: String
        public var collection: String?

        public init(
            title: String,
            collection: String? = nil
        ) {
            self.title = title
            self.collection = collection
        }
    }

    public typealias LocalDate = String

    public struct MCPCredentialInput: Sendable, Codable, Hashable {
        public var requestID: OpenAPI.UUID
        public var token: String

        public init(
            requestID: OpenAPI.UUID,
            token: String
        ) {
            self.requestID = requestID
            self.token = token
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case token
        }
    }

    public struct MCPDiagnostic: Sendable, Codable, Hashable {
        public var at: OpenAPI.Instant
        public var level: String
        public var message: String

        public init(
            at: OpenAPI.Instant,
            level: String,
            message: String
        ) {
            self.at = at
            self.level = level
            self.message = message
        }
    }

    public struct MCPExposureConfig: Sendable, Codable, Hashable {
        public var `default`: String?
        public var overrides: [String: String]?

        public init(
            `default`: String? = nil,
            overrides: [String: String]? = nil
        ) {
            self.`default` = `default`
            self.overrides = overrides
        }
    }

    public struct MCPObservedState: Sendable, Codable, Hashable {
        public var status: String
        public var protocolVersion: String?
        public var capabilities: [String]?
        public var discoveryRevision: String?
        public var toolCount: Int?
        public var resourceCount: Int?
        public var promptCount: Int?
        public var lastSuccessAt: OpenAPI.Instant?
        public var error: OpenAPI.ErrorDetail?
        public var diagnostics: [OpenAPI.MCPDiagnostic]?
        public var credential: String?

        public init(
            status: String,
            protocolVersion: String? = nil,
            capabilities: [String]? = nil,
            discoveryRevision: String? = nil,
            toolCount: Int? = nil,
            resourceCount: Int? = nil,
            promptCount: Int? = nil,
            lastSuccessAt: OpenAPI.Instant? = nil,
            error: OpenAPI.ErrorDetail? = nil,
            diagnostics: [OpenAPI.MCPDiagnostic]? = nil,
            credential: String? = nil
        ) {
            self.status = status
            self.protocolVersion = protocolVersion
            self.capabilities = capabilities
            self.discoveryRevision = discoveryRevision
            self.toolCount = toolCount
            self.resourceCount = resourceCount
            self.promptCount = promptCount
            self.lastSuccessAt = lastSuccessAt
            self.error = error
            self.diagnostics = diagnostics
            self.credential = credential
        }

        enum CodingKeys: String, CodingKey {
            case status
            case protocolVersion = "protocol_version"
            case capabilities
            case discoveryRevision = "discovery_revision"
            case toolCount = "tool_count"
            case resourceCount = "resource_count"
            case promptCount = "prompt_count"
            case lastSuccessAt = "last_success_at"
            case error
            case diagnostics
            case credential
        }
    }

    public struct MCPServer: Sendable, Codable, Hashable {
        public struct Source: Sendable, Codable, Hashable {
            public var kind: String

            public init(
                kind: String
            ) {
                self.kind = kind
            }
        }

        public var id: OpenAPI.ID
        public var revision: OpenAPI.Revision
        public var source: Source
        public var name: String
        public var transport: String
        public var endpoint: String?
        public var connectionID: OpenAPI.ID?
        public var enabled: Bool
        public var exposure: OpenAPI.MCPExposureConfig?
        public var observed: OpenAPI.MCPObservedState
        public var auth: String?

        public init(
            id: OpenAPI.ID,
            revision: OpenAPI.Revision,
            source: Source,
            name: String,
            transport: String,
            endpoint: String? = nil,
            connectionID: OpenAPI.ID? = nil,
            enabled: Bool,
            exposure: OpenAPI.MCPExposureConfig? = nil,
            observed: OpenAPI.MCPObservedState,
            auth: String? = nil
        ) {
            self.id = id
            self.revision = revision
            self.source = source
            self.name = name
            self.transport = transport
            self.endpoint = endpoint
            self.connectionID = connectionID
            self.enabled = enabled
            self.exposure = exposure
            self.observed = observed
            self.auth = auth
        }

        enum CodingKeys: String, CodingKey {
            case id
            case revision
            case source
            case name
            case transport
            case endpoint
            case connectionID = "connection_id"
            case enabled
            case exposure
            case observed
            case auth
        }
    }

    public struct MCPServerAction: Sendable, Codable, Hashable {
        public enum Value: Sendable, Codable, Hashable {
            public struct ConnectPayload: Sendable, Codable, Hashable {
                public var action: String
                public var returnTo: String?

                public init(
                    action: String,
                    returnTo: String? = nil
                ) {
                    self.action = action
                    self.returnTo = returnTo
                }

                enum CodingKeys: String, CodingKey {
                    case action
                    case returnTo = "return_to"
                }
            }
            public struct SetExposurePayload: Sendable, Codable, Hashable {
                public var action: String
                public var tool: String
                public var exposure: String

                public init(
                    action: String,
                    tool: String,
                    exposure: String
                ) {
                    self.action = action
                    self.tool = tool
                    self.exposure = exposure
                }
            }

            case connect(ConnectPayload)
            case setExposure(SetExposurePayload)

            private enum DiscriminatorKey: String, CodingKey {
                case discriminator = "action"
            }

            public init(from decoder: any Decoder) throws {
                let container = try decoder.container(keyedBy: DiscriminatorKey.self)
                let kind = try container.decode(String.self, forKey: .discriminator)
                switch kind {
                case "connect", "reconnect", "sign_in", "sign_out", "start", "stop", "restart":
                    self = .connect(try ConnectPayload(from: decoder))
                case "set_exposure":
                    self = .setExposure(try SetExposurePayload(from: decoder))
                default:
                    throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Value action: \(String(describing: kind))")
                }
            }

            public func encode(to encoder: any Encoder) throws {
                switch self {
                case .connect(let value):
                    try value.encode(to: encoder)
                case .setExposure(let value):
                    try value.encode(to: encoder)
                }
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct MCPServerChange: Sendable, Codable, Hashable {
        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: OpenAPI.MCPServerEditable

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: OpenAPI.MCPServerEditable
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct MCPServerEditable: Sendable, Codable, Hashable {
        public var name: String
        public var transport: String
        public var endpoint: String?
        public var commandRef: String?
        public var environmentRefs: [String]?
        public var auth: String
        public var enabled: Bool
        public var exposure: OpenAPI.MCPExposureConfig

        public init(
            name: String,
            transport: String,
            endpoint: String? = nil,
            commandRef: String? = nil,
            environmentRefs: [String]? = nil,
            auth: String,
            enabled: Bool,
            exposure: OpenAPI.MCPExposureConfig
        ) {
            self.name = name
            self.transport = transport
            self.endpoint = endpoint
            self.commandRef = commandRef
            self.environmentRefs = environmentRefs
            self.auth = auth
            self.enabled = enabled
            self.exposure = exposure
        }

        enum CodingKeys: String, CodingKey {
            case name
            case transport
            case endpoint
            case commandRef = "command_ref"
            case environmentRefs = "environment_refs"
            case auth
            case enabled
            case exposure
        }
    }

    public struct MCPServerTool: Sendable, Codable, Hashable {
        public var name: String
        public var title: String
        public var exposure: String
        public var definition: OpenAPI.DefinitionRef
        public var availability: String

        public init(
            name: String,
            title: String,
            exposure: String,
            definition: OpenAPI.DefinitionRef,
            availability: String
        ) {
            self.name = name
            self.title = title
            self.exposure = exposure
            self.definition = definition
            self.availability = availability
        }
    }

    public struct MailAddress: Sendable, Codable, Hashable {
        public var email: String
        public var name: String?

        public init(
            email: String,
            name: String? = nil
        ) {
            self.email = email
            self.name = name
        }
    }

    public struct MailDraft: Sendable, Codable, Hashable {
        public var ref: OpenAPI.ResourceRef
        public var sender: OpenAPI.MailAddress
        public var to: [OpenAPI.MailAddress]
        public var cc: [OpenAPI.MailAddress]
        public var bcc: [OpenAPI.MailAddress]
        public var subject: String
        public var bodyText: String
        public var bodyHTMLResultID: String?
        public var attachmentIDs: [String]
        public var replyTo: OpenAPI.ResourceRef?
        public var mode: String?
        public var state: String?
        public var sentMessage: OpenAPI.ResourceRef?

        public init(
            ref: OpenAPI.ResourceRef,
            sender: OpenAPI.MailAddress,
            to: [OpenAPI.MailAddress],
            cc: [OpenAPI.MailAddress],
            bcc: [OpenAPI.MailAddress],
            subject: String,
            bodyText: String,
            bodyHTMLResultID: String? = nil,
            attachmentIDs: [String],
            replyTo: OpenAPI.ResourceRef? = nil,
            mode: String? = nil,
            state: String? = nil,
            sentMessage: OpenAPI.ResourceRef? = nil
        ) {
            self.ref = ref
            self.sender = sender
            self.to = to
            self.cc = cc
            self.bcc = bcc
            self.subject = subject
            self.bodyText = bodyText
            self.bodyHTMLResultID = bodyHTMLResultID
            self.attachmentIDs = attachmentIDs
            self.replyTo = replyTo
            self.mode = mode
            self.state = state
            self.sentMessage = sentMessage
        }

        enum CodingKeys: String, CodingKey {
            case ref
            case sender
            case to
            case cc
            case bcc
            case subject
            case bodyText = "body_text"
            case bodyHTMLResultID = "body_html_result_id"
            case attachmentIDs = "attachment_ids"
            case replyTo = "reply_to"
            case mode
            case state
            case sentMessage = "sent_message"
        }
    }

    public struct MailMessage: Sendable, Codable, Hashable {
        public struct Content: Sendable, Codable, Hashable {
            public var kind: String
            public var text: String?
            public var resultID: String?

            public init(
                kind: String,
                text: String? = nil,
                resultID: String? = nil
            ) {
                self.kind = kind
                self.text = text
                self.resultID = resultID
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case text
                case resultID = "result_id"
            }
        }
        public struct AttachmentItem: Sendable, Codable, Hashable {
            public var id: String
            public var name: String
            public var mediaType: String
            public var size: Int
            public var uploadID: String?

            public init(
                id: String,
                name: String,
                mediaType: String,
                size: Int,
                uploadID: String? = nil
            ) {
                self.id = id
                self.name = name
                self.mediaType = mediaType
                self.size = size
                self.uploadID = uploadID
            }

            enum CodingKeys: String, CodingKey {
                case id
                case name
                case mediaType = "media_type"
                case size
                case uploadID = "upload_id"
            }
        }
        public struct Supports: Sendable, Codable, Hashable {
            public var labels: Bool?
            public var folders: Bool?
            public var archive: Bool?
            public var markRead: Bool?

            public init(
                labels: Bool? = nil,
                folders: Bool? = nil,
                archive: Bool? = nil,
                markRead: Bool? = nil
            ) {
                self.labels = labels
                self.folders = folders
                self.archive = archive
                self.markRead = markRead
            }

            enum CodingKeys: String, CodingKey {
                case labels
                case folders
                case archive
                case markRead = "mark_read"
            }
        }

        public var ref: OpenAPI.ResourceRef
        public var thread: OpenAPI.ResourceRef
        public var from: OpenAPI.MailAddress
        public var to: [OpenAPI.MailAddress]
        public var cc: [OpenAPI.MailAddress]?
        public var subject: String
        public var receivedAt: OpenAPI.Instant
        public var content: Content
        public var attachments: [AttachmentItem]?
        public var labels: [String]?
        public var folder: String?
        public var unread: Bool?
        public var sourceURL: String?
        public var supports: Supports?

        public init(
            ref: OpenAPI.ResourceRef,
            thread: OpenAPI.ResourceRef,
            from: OpenAPI.MailAddress,
            to: [OpenAPI.MailAddress],
            cc: [OpenAPI.MailAddress]? = nil,
            subject: String,
            receivedAt: OpenAPI.Instant,
            content: Content,
            attachments: [AttachmentItem]? = nil,
            labels: [String]? = nil,
            folder: String? = nil,
            unread: Bool? = nil,
            sourceURL: String? = nil,
            supports: Supports? = nil
        ) {
            self.ref = ref
            self.thread = thread
            self.from = from
            self.to = to
            self.cc = cc
            self.subject = subject
            self.receivedAt = receivedAt
            self.content = content
            self.attachments = attachments
            self.labels = labels
            self.folder = folder
            self.unread = unread
            self.sourceURL = sourceURL
            self.supports = supports
        }

        enum CodingKeys: String, CodingKey {
            case ref
            case thread
            case from
            case to
            case cc
            case subject
            case receivedAt = "received_at"
            case content
            case attachments
            case labels
            case folder
            case unread
            case sourceURL = "source_url"
            case supports
        }
    }

    public struct MemoryAction: Sendable, Codable, Hashable {
        public struct Value: Sendable, Codable, Hashable {
            public var action: String

            public init(
                action: String
            ) {
                self.action = action
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct MemoryFact: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var revision: OpenAPI.Revision
        public var text: String
        public var about: String?
        public var section: String?
        public var pinned: Bool?
        public var provenance: OpenAPI.MemoryProvenance?
        public var sourceText: String?
        public var supersedesID: String?
        public var scope: String
        public var origin: OpenAPI.MemoryOrigin
        public var createdAt: OpenAPI.Instant
        public var updatedAt: OpenAPI.Instant?

        public init(
            id: OpenAPI.ID,
            revision: OpenAPI.Revision,
            text: String,
            about: String? = nil,
            section: String? = nil,
            pinned: Bool? = nil,
            provenance: OpenAPI.MemoryProvenance? = nil,
            sourceText: String? = nil,
            supersedesID: String? = nil,
            scope: String,
            origin: OpenAPI.MemoryOrigin,
            createdAt: OpenAPI.Instant,
            updatedAt: OpenAPI.Instant? = nil
        ) {
            self.id = id
            self.revision = revision
            self.text = text
            self.about = about
            self.section = section
            self.pinned = pinned
            self.provenance = provenance
            self.sourceText = sourceText
            self.supersedesID = supersedesID
            self.scope = scope
            self.origin = origin
            self.createdAt = createdAt
            self.updatedAt = updatedAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case revision
            case text
            case about
            case section
            case pinned
            case provenance
            case sourceText = "source_text"
            case supersedesID = "supersedes_id"
            case scope
            case origin
            case createdAt = "created_at"
            case updatedAt = "updated_at"
        }
    }

    public struct MemoryFactChange: Sendable, Codable, Hashable {
        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: OpenAPI.MemoryFactEditable

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: OpenAPI.MemoryFactEditable
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct MemoryFactEditable: Sendable, Codable, Hashable {
        public var text: String
        public var about: String?
        public var section: String?
        public var pinned: Bool?

        public init(
            text: String,
            about: String? = nil,
            section: String? = nil,
            pinned: Bool? = nil
        ) {
            self.text = text
            self.about = about
            self.section = section
            self.pinned = pinned
        }
    }

    public struct MemoryLog: Sendable, Codable, Hashable {
        public var date: OpenAPI.LocalDate
        public var revision: OpenAPI.Revision
        public var entries: [OpenAPI.MemoryLogEntry]
        public var updatedAt: OpenAPI.Instant

        public init(
            date: OpenAPI.LocalDate,
            revision: OpenAPI.Revision,
            entries: [OpenAPI.MemoryLogEntry],
            updatedAt: OpenAPI.Instant
        ) {
            self.date = date
            self.revision = revision
            self.entries = entries
            self.updatedAt = updatedAt
        }

        enum CodingKeys: String, CodingKey {
            case date
            case revision
            case entries
            case updatedAt = "updated_at"
        }
    }

    public struct MemoryLogEntry: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var section: String?
        public var text: String
        public var about: String?
        public var provenance: OpenAPI.MemoryProvenance
        public var sourceInputID: String?
        public var sourceText: String?
        public var runID: String?
        public var createdAt: OpenAPI.Instant
        public var updatedAt: OpenAPI.Instant?

        public init(
            id: OpenAPI.ID,
            section: String? = nil,
            text: String,
            about: String? = nil,
            provenance: OpenAPI.MemoryProvenance,
            sourceInputID: String? = nil,
            sourceText: String? = nil,
            runID: String? = nil,
            createdAt: OpenAPI.Instant,
            updatedAt: OpenAPI.Instant? = nil
        ) {
            self.id = id
            self.section = section
            self.text = text
            self.about = about
            self.provenance = provenance
            self.sourceInputID = sourceInputID
            self.sourceText = sourceText
            self.runID = runID
            self.createdAt = createdAt
            self.updatedAt = updatedAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case section
            case text
            case about
            case provenance
            case sourceInputID = "source_input_id"
            case sourceText = "source_text"
            case runID = "run_id"
            case createdAt = "created_at"
            case updatedAt = "updated_at"
        }
    }

    public struct MemoryLogEntryAction: Sendable, Codable, Hashable {
        public enum Value: Sendable, Codable, Hashable {
            public struct PromotePayload: Sendable, Codable, Hashable {
                public var action: String
                public var text: String?
                public var about: String?
                public var section: String?
                public var pinned: Bool?

                public init(
                    action: String,
                    text: String? = nil,
                    about: String? = nil,
                    section: String? = nil,
                    pinned: Bool? = nil
                ) {
                    self.action = action
                    self.text = text
                    self.about = about
                    self.section = section
                    self.pinned = pinned
                }
            }
            public struct ForgetPayload: Sendable, Codable, Hashable {
                public var action: String

                public init(
                    action: String
                ) {
                    self.action = action
                }
            }

            case promote(PromotePayload)
            case forget(ForgetPayload)

            private enum DiscriminatorKey: String, CodingKey {
                case discriminator = "action"
            }

            public init(from decoder: any Decoder) throws {
                let container = try decoder.container(keyedBy: DiscriminatorKey.self)
                let kind = try container.decode(String.self, forKey: .discriminator)
                switch kind {
                case "promote":
                    self = .promote(try PromotePayload(from: decoder))
                case "forget":
                    self = .forget(try ForgetPayload(from: decoder))
                default:
                    throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Value action: \(String(describing: kind))")
                }
            }

            public func encode(to encoder: any Encoder) throws {
                switch self {
                case .promote(let value):
                    try value.encode(to: encoder)
                case .forget(let value):
                    try value.encode(to: encoder)
                }
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct MemoryOrigin: Sendable, Codable, Hashable {
        public var conversationID: String?
        public var messageID: String?
        public var taskID: String?

        public init(
            conversationID: String? = nil,
            messageID: String? = nil,
            taskID: String? = nil
        ) {
            self.conversationID = conversationID
            self.messageID = messageID
            self.taskID = taskID
        }

        enum CodingKeys: String, CodingKey {
            case conversationID = "conversation_id"
            case messageID = "message_id"
            case taskID = "task_id"
        }
    }

    public enum MemoryProvenance: String, Sendable, Codable, Hashable, CaseIterable {
        case userExplicit = "user_explicit"
        case preCompactFlush = "pre_compact_flush"
        case summaryEvicted = "summary_evicted"
        case taskOutcome = "task_outcome"
        case dreamCurated = "dream_curated"
    }

    public struct MemorySearchHit: Sendable, Codable, Hashable {
        public var chunkID: String
        public var layer: String
        public var memoryID: String?
        public var logDate: OpenAPI.LocalDate?
        public var entryID: String?
        public var section: String?
        public var about: String?
        public var snippet: String
        public var provenance: OpenAPI.MemoryProvenance
        public var pinned: Bool?
        public var score: Double
        public var createdAt: OpenAPI.Instant
        public var updatedAt: OpenAPI.Instant?

        public init(
            chunkID: String,
            layer: String,
            memoryID: String? = nil,
            logDate: OpenAPI.LocalDate? = nil,
            entryID: String? = nil,
            section: String? = nil,
            about: String? = nil,
            snippet: String,
            provenance: OpenAPI.MemoryProvenance,
            pinned: Bool? = nil,
            score: Double,
            createdAt: OpenAPI.Instant,
            updatedAt: OpenAPI.Instant? = nil
        ) {
            self.chunkID = chunkID
            self.layer = layer
            self.memoryID = memoryID
            self.logDate = logDate
            self.entryID = entryID
            self.section = section
            self.about = about
            self.snippet = snippet
            self.provenance = provenance
            self.pinned = pinned
            self.score = score
            self.createdAt = createdAt
            self.updatedAt = updatedAt
        }

        enum CodingKeys: String, CodingKey {
            case chunkID = "chunk_id"
            case layer
            case memoryID = "memory_id"
            case logDate = "log_date"
            case entryID = "entry_id"
            case section
            case about
            case snippet
            case provenance
            case pinned
            case score
            case createdAt = "created_at"
            case updatedAt = "updated_at"
        }
    }

    public struct MemorySearchResponse: Sendable, Codable, Hashable {
        public var items: [OpenAPI.MemorySearchHit]
        public var nextCursor: String?
        public var query: String?

        public init(
            items: [OpenAPI.MemorySearchHit],
            nextCursor: String? = nil,
            query: String? = nil
        ) {
            self.items = items
            self.nextCursor = nextCursor
            self.query = query
        }

        enum CodingKeys: String, CodingKey {
            case items
            case nextCursor = "next_cursor"
            case query
        }
    }

    public struct Message: Sendable, Codable, Hashable {
        public var id: String
        public var runID: String
        public var turnID: String?
        public var role: String
        public var status: OpenAPI.MessageStatus?
        public var text: String
        public var parts: [OpenAPI.Part]?
        public var resultIDs: [String]?
        public var error: OpenAPI.MessageError?
        public var createdAt: OpenAPI.Instant

        public init(
            id: String,
            runID: String,
            turnID: String? = nil,
            role: String,
            status: OpenAPI.MessageStatus? = nil,
            text: String,
            parts: [OpenAPI.Part]? = nil,
            resultIDs: [String]? = nil,
            error: OpenAPI.MessageError? = nil,
            createdAt: OpenAPI.Instant
        ) {
            self.id = id
            self.runID = runID
            self.turnID = turnID
            self.role = role
            self.status = status
            self.text = text
            self.parts = parts
            self.resultIDs = resultIDs
            self.error = error
            self.createdAt = createdAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case runID = "run_id"
            case turnID = "turn_id"
            case role
            case status
            case text
            case parts
            case resultIDs = "result_ids"
            case error
            case createdAt = "created_at"
        }
    }

    public struct MessageError: Sendable, Codable, Hashable {
        public var code: String
        public var message: String
        public var retryable: Bool

        public init(
            code: String,
            message: String,
            retryable: Bool
        ) {
            self.code = code
            self.message = message
            self.retryable = retryable
        }
    }

    public enum MessageStatus: String, Sendable, Codable, Hashable, CaseIterable {
        case completed = "completed"
        case failed = "failed"
        case cancelled = "cancelled"
        case superseded = "superseded"
    }

    public struct MessageView: Sendable, Codable, Hashable {
        public var id: String
        public var role: String
        public var text: String
        public var status: String
        public var parts: [OpenAPI.Part]
        public var sequence: Int?
        public var turnID: String?
        public var createdAt: OpenAPI.Instant?
        public var attachments: [OpenAPI.Attachment]?
        public var operations: [OpenAPI.OperationProjection]?
        public var resultIDs: [String]?
        public var error: OpenAPI.MessageError?

        public init(
            id: String,
            role: String,
            text: String,
            status: String,
            parts: [OpenAPI.Part],
            sequence: Int? = nil,
            turnID: String? = nil,
            createdAt: OpenAPI.Instant? = nil,
            attachments: [OpenAPI.Attachment]? = nil,
            operations: [OpenAPI.OperationProjection]? = nil,
            resultIDs: [String]? = nil,
            error: OpenAPI.MessageError? = nil
        ) {
            self.id = id
            self.role = role
            self.text = text
            self.status = status
            self.parts = parts
            self.sequence = sequence
            self.turnID = turnID
            self.createdAt = createdAt
            self.attachments = attachments
            self.operations = operations
            self.resultIDs = resultIDs
            self.error = error
        }

        enum CodingKeys: String, CodingKey {
            case id
            case role
            case text
            case status
            case parts
            case sequence
            case turnID = "turnId"
            case createdAt
            case attachments
            case operations
            case resultIDs = "resultIds"
            case error
        }
    }

    public enum MissedRun: String, Sendable, Codable, Hashable, CaseIterable {
        case skip = "skip"
        case latest = "latest"
        case all = "all"
    }

    public struct ModelCost: Sendable, Codable, Hashable {
        public var costUSD: Double
        public var pricedCalls: Int
        public var unpricedCalls: Int
        public var localCalls: Int
        public var pendingCalls: Int
        public var model: String

        public init(
            costUSD: Double,
            pricedCalls: Int,
            unpricedCalls: Int,
            localCalls: Int,
            pendingCalls: Int,
            model: String
        ) {
            self.costUSD = costUSD
            self.pricedCalls = pricedCalls
            self.unpricedCalls = unpricedCalls
            self.localCalls = localCalls
            self.pendingCalls = pendingCalls
            self.model = model
        }

        enum CodingKeys: String, CodingKey {
            case costUSD = "cost_usd"
            case pricedCalls = "priced_calls"
            case unpricedCalls = "unpriced_calls"
            case localCalls = "local_calls"
            case pendingCalls = "pending_calls"
            case model
        }
    }

    public struct ModelKeyProvider: Sendable, Codable, Hashable {
        public var provider: String
        public var label: String
        public var configured: Bool
        public var last4: String?
        public var updatedAt: OpenAPI.Instant?
        public var validatedAt: OpenAPI.Instant?

        public init(
            provider: String,
            label: String,
            configured: Bool,
            last4: String? = nil,
            updatedAt: OpenAPI.Instant? = nil,
            validatedAt: OpenAPI.Instant? = nil
        ) {
            self.provider = provider
            self.label = label
            self.configured = configured
            self.last4 = last4
            self.updatedAt = updatedAt
            self.validatedAt = validatedAt
        }

        enum CodingKeys: String, CodingKey {
            case provider
            case label
            case configured
            case last4
            case updatedAt = "updated_at"
            case validatedAt = "validated_at"
        }
    }

    public struct ModelKeysResponse: Sendable, Codable, Hashable {
        public var byokRequired: Bool
        public var providers: [OpenAPI.ModelKeyProvider]

        public init(
            byokRequired: Bool,
            providers: [OpenAPI.ModelKeyProvider]
        ) {
            self.byokRequired = byokRequired
            self.providers = providers
        }

        enum CodingKeys: String, CodingKey {
            case byokRequired = "byok_required"
            case providers
        }
    }

    public struct ModelProfile: Sendable, Codable, Hashable {
        public var id: String
        public var version: String
        public var label: String
        public var backend: String
        public var model: String
        public var lanes: [String]
        public var supportedReasoning: [String]?
        public var defaultReasoning: String?
        public var paid: Bool
        public var homeDeviceID: String?
        public var availability: String?

        public init(
            id: String,
            version: String,
            label: String,
            backend: String,
            model: String,
            lanes: [String],
            supportedReasoning: [String]? = nil,
            defaultReasoning: String? = nil,
            paid: Bool,
            homeDeviceID: String? = nil,
            availability: String? = nil
        ) {
            self.id = id
            self.version = version
            self.label = label
            self.backend = backend
            self.model = model
            self.lanes = lanes
            self.supportedReasoning = supportedReasoning
            self.defaultReasoning = defaultReasoning
            self.paid = paid
            self.homeDeviceID = homeDeviceID
            self.availability = availability
        }

        enum CodingKeys: String, CodingKey {
            case id
            case version
            case label
            case backend
            case model
            case lanes
            case supportedReasoning = "supported_reasoning"
            case defaultReasoning = "default_reasoning"
            case paid
            case homeDeviceID = "home_device_id"
            case availability
        }
    }

    public struct ModelProfileCatalog: Sendable, Codable, Hashable {
        public var defaultChatProfileID: String?
        public var defaultChatReasoning: String?
        public var defaultTaskProfileID: String?
        public var defaultTaskReasoning: String?
        public var profiles: [OpenAPI.ModelProfile]

        public init(
            defaultChatProfileID: String? = nil,
            defaultChatReasoning: String? = nil,
            defaultTaskProfileID: String? = nil,
            defaultTaskReasoning: String? = nil,
            profiles: [OpenAPI.ModelProfile]
        ) {
            self.defaultChatProfileID = defaultChatProfileID
            self.defaultChatReasoning = defaultChatReasoning
            self.defaultTaskProfileID = defaultTaskProfileID
            self.defaultTaskReasoning = defaultTaskReasoning
            self.profiles = profiles
        }

        enum CodingKeys: String, CodingKey {
            case defaultChatProfileID = "default_chat_profile_id"
            case defaultChatReasoning = "default_chat_reasoning"
            case defaultTaskProfileID = "default_task_profile_id"
            case defaultTaskReasoning = "default_task_reasoning"
            case profiles
        }
    }

    public struct ModelSelection: Sendable, Codable, Hashable {
        public var homeOnly: Bool?
        public var chatProfileID: String?
        public var chatProfileVersion: String?
        public var chatReasoning: String?
        public var chatPinned: OpenAPI.PinnedProfile?
        public var taskProfileID: String?
        public var taskProfileVersion: String?
        public var taskReasoning: String?
        public var taskPinned: OpenAPI.PinnedProfile?

        public init(
            homeOnly: Bool? = nil,
            chatProfileID: String? = nil,
            chatProfileVersion: String? = nil,
            chatReasoning: String? = nil,
            chatPinned: OpenAPI.PinnedProfile? = nil,
            taskProfileID: String? = nil,
            taskProfileVersion: String? = nil,
            taskReasoning: String? = nil,
            taskPinned: OpenAPI.PinnedProfile? = nil
        ) {
            self.homeOnly = homeOnly
            self.chatProfileID = chatProfileID
            self.chatProfileVersion = chatProfileVersion
            self.chatReasoning = chatReasoning
            self.chatPinned = chatPinned
            self.taskProfileID = taskProfileID
            self.taskProfileVersion = taskProfileVersion
            self.taskReasoning = taskReasoning
            self.taskPinned = taskPinned
        }

        enum CodingKeys: String, CodingKey {
            case homeOnly = "home_only"
            case chatProfileID = "chat_profile_id"
            case chatProfileVersion = "chat_profile_version"
            case chatReasoning = "chat_reasoning"
            case chatPinned = "chat_pinned"
            case taskProfileID = "task_profile_id"
            case taskProfileVersion = "task_profile_version"
            case taskReasoning = "task_reasoning"
            case taskPinned = "task_pinned"
        }
    }

    public struct ModelSettings: Sendable, Codable, Hashable {
        public var modelProfiles: OpenAPI.ModelProfileCatalog
        public var selection: OpenAPI.ModelSelection
        public var revision: OpenAPI.Revision
        public var recommendations: [String: OpenAPI.ModelSelection]

        public init(
            modelProfiles: OpenAPI.ModelProfileCatalog,
            selection: OpenAPI.ModelSelection,
            revision: OpenAPI.Revision,
            recommendations: [String: OpenAPI.ModelSelection]
        ) {
            self.modelProfiles = modelProfiles
            self.selection = selection
            self.revision = revision
            self.recommendations = recommendations
        }

        enum CodingKeys: String, CodingKey {
            case modelProfiles = "model_profiles"
            case selection
            case revision
            case recommendations
        }
    }

    public enum MutationReceipt: Sendable, Codable, Hashable {
        public struct AppliedPayload: Sendable, Codable, Hashable {
            public var requestID: OpenAPI.UUID
            public var accepted: Bool
            public var status: String
            public var resource: AnyJSON
            public var auth: OpenAPI.AuthAttempt?

            public init(
                requestID: OpenAPI.UUID,
                accepted: Bool,
                status: String,
                resource: AnyJSON,
                auth: OpenAPI.AuthAttempt? = nil
            ) {
                self.requestID = requestID
                self.accepted = accepted
                self.status = status
                self.resource = resource
                self.auth = auth
            }

            enum CodingKeys: String, CodingKey {
                case requestID = "request_id"
                case accepted
                case status
                case resource
                case auth
            }
        }
        public struct QueuedPayload: Sendable, Codable, Hashable {
            public var requestID: OpenAPI.UUID
            public var accepted: Bool
            public var status: String
            public var resource: AnyJSON
            public var work: OpenAPI.WorkRef?
            public var auth: OpenAPI.AuthAttempt?

            public init(
                requestID: OpenAPI.UUID,
                accepted: Bool,
                status: String,
                resource: AnyJSON,
                work: OpenAPI.WorkRef? = nil,
                auth: OpenAPI.AuthAttempt? = nil
            ) {
                self.requestID = requestID
                self.accepted = accepted
                self.status = status
                self.resource = resource
                self.work = work
                self.auth = auth
            }

            enum CodingKeys: String, CodingKey {
                case requestID = "request_id"
                case accepted
                case status
                case resource
                case work
                case auth
            }
        }

        case applied(AppliedPayload)
        case queued(QueuedPayload)

        private enum DiscriminatorKey: String, CodingKey {
            case discriminator = "status"
        }

        public init(from decoder: any Decoder) throws {
            let container = try decoder.container(keyedBy: DiscriminatorKey.self)
            let kind = try container.decode(String.self, forKey: .discriminator)
            switch kind {
            case "applied":
                self = .applied(try AppliedPayload(from: decoder))
            case "queued":
                self = .queued(try QueuedPayload(from: decoder))
            default:
                throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported MutationReceipt status: \(String(describing: kind))")
            }
        }

        public func encode(to encoder: any Encoder) throws {
            switch self {
            case .applied(let value):
                try value.encode(to: encoder)
            case .queued(let value):
                try value.encode(to: encoder)
            }
        }
    }

    public struct OAuthCallbackRedirect: Sendable, Codable, Hashable {
        public var location: String

        public init(
            location: String
        ) {
            self.location = location
        }
    }

    public struct Occurrence: Sendable, Codable, Hashable {
        public var id: String
        public var automationID: OpenAPI.ID
        public var automationRevision: OpenAPI.Revision
        public var generation: Int?
        public var scheduledAt: OpenAPI.Instant
        public var sourceRevision: String?
        public var status: OpenAPI.OccurrenceStatus
        public var taskIDs: [String]
        public var deliveryIDs: [String]
        public var startedAt: OpenAPI.Instant?
        public var finishedAt: OpenAPI.Instant?
        public var summary: String?
        public var error: OpenAPI.ErrorDetail?
        public var reason: String?
        public var deliveries: [OpenAPI.Delivery]?

        public init(
            id: String,
            automationID: OpenAPI.ID,
            automationRevision: OpenAPI.Revision,
            generation: Int? = nil,
            scheduledAt: OpenAPI.Instant,
            sourceRevision: String? = nil,
            status: OpenAPI.OccurrenceStatus,
            taskIDs: [String],
            deliveryIDs: [String],
            startedAt: OpenAPI.Instant? = nil,
            finishedAt: OpenAPI.Instant? = nil,
            summary: String? = nil,
            error: OpenAPI.ErrorDetail? = nil,
            reason: String? = nil,
            deliveries: [OpenAPI.Delivery]? = nil
        ) {
            self.id = id
            self.automationID = automationID
            self.automationRevision = automationRevision
            self.generation = generation
            self.scheduledAt = scheduledAt
            self.sourceRevision = sourceRevision
            self.status = status
            self.taskIDs = taskIDs
            self.deliveryIDs = deliveryIDs
            self.startedAt = startedAt
            self.finishedAt = finishedAt
            self.summary = summary
            self.error = error
            self.reason = reason
            self.deliveries = deliveries
        }

        enum CodingKeys: String, CodingKey {
            case id
            case automationID = "automation_id"
            case automationRevision = "automation_revision"
            case generation
            case scheduledAt = "scheduled_at"
            case sourceRevision = "source_revision"
            case status
            case taskIDs = "task_ids"
            case deliveryIDs = "delivery_ids"
            case startedAt = "started_at"
            case finishedAt = "finished_at"
            case summary
            case error
            case reason
            case deliveries
        }
    }

    public enum OccurrenceStatus: String, Sendable, Codable, Hashable, CaseIterable {
        case pending = "pending"
        case accepted = "accepted"
        case running = "running"
        case completed = "completed"
        case failed = "failed"
        case cancelled = "cancelled"
        case expired = "expired"
        case skipped = "skipped"
    }

    public struct OperationAction: Sendable, Codable, Hashable {
        public var digest: String
        public var text: String
        public var spec: [String: String]?
        public var intent: Bool?
        public var precondition: String?
        public var keys: [String: String]?

        public init(
            digest: String,
            text: String,
            spec: [String: String]? = nil,
            intent: Bool? = nil,
            precondition: String? = nil,
            keys: [String: String]? = nil
        ) {
            self.digest = digest
            self.text = text
            self.spec = spec
            self.intent = intent
            self.precondition = precondition
            self.keys = keys
        }
    }

    public struct OperationOwner: Sendable, Codable, Hashable {
        public var type: String
        public var id: String

        public init(
            type: String,
            id: String
        ) {
            self.type = type
            self.id = id
        }
    }

    public struct OperationProjection: Sendable, Codable, Hashable {
        public var id: String
        public var callID: String
        public var name: String
        public var tool: OpenAPI.DefinitionRef?
        public var effectClass: OpenAPI.Effect
        public var outcome: OpenAPI.Outcome
        public var cleared: String?
        public var connectionID: OpenAPI.ID?
        public var accountSummary: String?
        public var actionSummary: String?
        public var authoritySource: String?
        public var providerRefs: [OpenAPI.ResourceRef]?
        public var receiptURL: String?
        public var resultIDs: [String]?
        public var parentOperationID: String?
        public var cost: OpenAPI.Cost?
        public var createdAt: OpenAPI.Instant?
        public var updatedAt: OpenAPI.Instant?
        public var error: OpenAPI.ErrorDetail?
        public var revision: OpenAPI.Revision
        public var attempt: Int?
        public var resultRef: String?
        public var artifactIDs: [String]?
        public var action: OpenAPI.OperationAction?
        public var owner: OpenAPI.OperationOwner?
        public var result: [String: AnyJSON]?
        public var bindingDigest: String?
        public var taskTitle: String?
        public var recovery: String?

        public init(
            id: String,
            callID: String,
            name: String,
            tool: OpenAPI.DefinitionRef? = nil,
            effectClass: OpenAPI.Effect,
            outcome: OpenAPI.Outcome,
            cleared: String? = nil,
            connectionID: OpenAPI.ID? = nil,
            accountSummary: String? = nil,
            actionSummary: String? = nil,
            authoritySource: String? = nil,
            providerRefs: [OpenAPI.ResourceRef]? = nil,
            receiptURL: String? = nil,
            resultIDs: [String]? = nil,
            parentOperationID: String? = nil,
            cost: OpenAPI.Cost? = nil,
            createdAt: OpenAPI.Instant? = nil,
            updatedAt: OpenAPI.Instant? = nil,
            error: OpenAPI.ErrorDetail? = nil,
            revision: OpenAPI.Revision,
            attempt: Int? = nil,
            resultRef: String? = nil,
            artifactIDs: [String]? = nil,
            action: OpenAPI.OperationAction? = nil,
            owner: OpenAPI.OperationOwner? = nil,
            result: [String: AnyJSON]? = nil,
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
            self.result = result
            self.bindingDigest = bindingDigest
            self.taskTitle = taskTitle
            self.recovery = recovery
        }

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
            case result
            case bindingDigest = "binding_digest"
            case taskTitle = "task_title"
            case recovery
        }
    }

    public enum Outcome: String, Sendable, Codable, Hashable, CaseIterable {
        case pending = "pending"
        case ready = "ready"
        case attempted = "attempted"
        case submitted = "submitted"
        case confirmed = "confirmed"
        case failed = "failed"
        case uncertain = "uncertain"
        case skipped = "skipped"
        case voided = "voided"
    }

    public struct Part: Sendable, Codable, Hashable {
        public struct Resource: Sendable, Codable, Hashable {
            public var kind: String
            public var id: String
            public var revision: OpenAPI.Revision?

            public init(
                kind: String,
                id: String,
                revision: OpenAPI.Revision? = nil
            ) {
                self.kind = kind
                self.id = id
                self.revision = revision
            }
        }

        public var type: String
        public var title: String?
        public var text: String?
        public var display: String?
        public var fallback: String?
        public var id: String?
        public var name: String?
        public var mediaType: String?
        public var size: Int?
        public var url: String?
        public var scheduledAt: String?
        public var greeting: String?
        public var meta: String?
        public var items: [OpenAPI.BriefItem]?
        public var schemaVersion: Int?
        public var kind: String?
        public var resource: Resource?
        public var actions: [String]?

        public init(
            type: String,
            title: String? = nil,
            text: String? = nil,
            display: String? = nil,
            fallback: String? = nil,
            id: String? = nil,
            name: String? = nil,
            mediaType: String? = nil,
            size: Int? = nil,
            url: String? = nil,
            scheduledAt: String? = nil,
            greeting: String? = nil,
            meta: String? = nil,
            items: [OpenAPI.BriefItem]? = nil,
            schemaVersion: Int? = nil,
            kind: String? = nil,
            resource: Resource? = nil,
            actions: [String]? = nil
        ) {
            self.type = type
            self.title = title
            self.text = text
            self.display = display
            self.fallback = fallback
            self.id = id
            self.name = name
            self.mediaType = mediaType
            self.size = size
            self.url = url
            self.scheduledAt = scheduledAt
            self.greeting = greeting
            self.meta = meta
            self.items = items
            self.schemaVersion = schemaVersion
            self.kind = kind
            self.resource = resource
            self.actions = actions
        }

        enum CodingKeys: String, CodingKey {
            case type
            case title
            case text
            case display
            case fallback
            case id
            case name
            case mediaType = "media_type"
            case size
            case url
            case scheduledAt = "scheduled_at"
            case greeting
            case meta
            case items
            case schemaVersion = "schema_version"
            case kind
            case resource
            case actions
        }
    }

    public struct PermissionRule: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var revision: OpenAPI.Revision
        public var effect: String
        public var toolIDs: [String]
        public var connectionIDs: [OpenAPI.ID]
        public var resourceIDs: [String]
        public var actionClasses: [OpenAPI.Effect]
        public var recipients: [String]?
        public var automationID: OpenAPI.ID?
        public var expiresAt: OpenAPI.Instant?
        public var maxOperations: Int?
        public var title: String?
        public var createdAt: OpenAPI.Instant?
        public var updatedAt: OpenAPI.Instant?
        public var summary: String?

        public init(
            id: OpenAPI.ID,
            revision: OpenAPI.Revision,
            effect: String,
            toolIDs: [String],
            connectionIDs: [OpenAPI.ID],
            resourceIDs: [String],
            actionClasses: [OpenAPI.Effect],
            recipients: [String]? = nil,
            automationID: OpenAPI.ID? = nil,
            expiresAt: OpenAPI.Instant? = nil,
            maxOperations: Int? = nil,
            title: String? = nil,
            createdAt: OpenAPI.Instant? = nil,
            updatedAt: OpenAPI.Instant? = nil,
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
            self.summary = summary
        }

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
            case summary
        }
    }

    public struct PermissionRuleAction: Sendable, Codable, Hashable {
        public struct Value: Sendable, Codable, Hashable {
            public var action: String

            public init(
                action: String
            ) {
                self.action = action
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct PermissionRuleChange: Sendable, Codable, Hashable {
        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: OpenAPI.PermissionRuleEditable

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: OpenAPI.PermissionRuleEditable
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct PermissionRuleEditable: Sendable, Codable, Hashable {
        public var effect: String
        public var toolIDs: [String]
        public var connectionIDs: [OpenAPI.ID]
        public var resourceIDs: [String]
        public var actionClasses: [OpenAPI.Effect]
        public var recipients: [String]?
        public var automationID: OpenAPI.ID?
        public var expiresAt: OpenAPI.Instant?
        public var maxOperations: Int?
        public var title: String?

        public init(
            effect: String,
            toolIDs: [String],
            connectionIDs: [OpenAPI.ID],
            resourceIDs: [String],
            actionClasses: [OpenAPI.Effect],
            recipients: [String]? = nil,
            automationID: OpenAPI.ID? = nil,
            expiresAt: OpenAPI.Instant? = nil,
            maxOperations: Int? = nil,
            title: String? = nil
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
        }

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
        }
    }

    public struct PinnedProfile: Sendable, Codable, Hashable {
        public var backend: String?
        public var model: String?
        public var adapter: String?
        public var homeDeviceID: String?
        public var modelDigest: String?
        public var endpoint: String?
        public var supportedReasoning: [String]?
        public var defaultReasoning: String?
        public var paid: Bool?

        public init(
            backend: String? = nil,
            model: String? = nil,
            adapter: String? = nil,
            homeDeviceID: String? = nil,
            modelDigest: String? = nil,
            endpoint: String? = nil,
            supportedReasoning: [String]? = nil,
            defaultReasoning: String? = nil,
            paid: Bool? = nil
        ) {
            self.backend = backend
            self.model = model
            self.adapter = adapter
            self.homeDeviceID = homeDeviceID
            self.modelDigest = modelDigest
            self.endpoint = endpoint
            self.supportedReasoning = supportedReasoning
            self.defaultReasoning = defaultReasoning
            self.paid = paid
        }

        enum CodingKeys: String, CodingKey {
            case backend
            case model
            case adapter
            case homeDeviceID = "home_device_id"
            case modelDigest = "model_digest"
            case endpoint
            case supportedReasoning = "supported_reasoning"
            case defaultReasoning = "default_reasoning"
            case paid
        }
    }

    public struct Preferences: Sendable, Codable, Hashable {
        public var revision: OpenAPI.Revision
        public var timezone: OpenAPI.Timezone
        public var locale: String?
        public var workingHours: OpenAPI.WorkingHours
        public var quietHours: OpenAPI.QuietHours?
        public var defaultDestinationIDs: [OpenAPI.ID]
        public var accountDefaults: [String: OpenAPI.ID]
        public var budgets: OpenAPI.BudgetPreferences
        public var input: OpenAPI.InputPreferences?
        public var proactivity: OpenAPI.ProactivityPreferences?
        public var onboardingCompletedAt: OpenAPI.Instant?
        public var dataResetAt: OpenAPI.Instant?
        public var dataResetPending: Bool?
        public var updatedAt: OpenAPI.Instant?

        public init(
            revision: OpenAPI.Revision,
            timezone: OpenAPI.Timezone,
            locale: String? = nil,
            workingHours: OpenAPI.WorkingHours,
            quietHours: OpenAPI.QuietHours? = nil,
            defaultDestinationIDs: [OpenAPI.ID],
            accountDefaults: [String: OpenAPI.ID],
            budgets: OpenAPI.BudgetPreferences,
            input: OpenAPI.InputPreferences? = nil,
            proactivity: OpenAPI.ProactivityPreferences? = nil,
            onboardingCompletedAt: OpenAPI.Instant? = nil,
            dataResetAt: OpenAPI.Instant? = nil,
            dataResetPending: Bool? = nil,
            updatedAt: OpenAPI.Instant? = nil
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
            self.proactivity = proactivity
            self.onboardingCompletedAt = onboardingCompletedAt
            self.dataResetAt = dataResetAt
            self.dataResetPending = dataResetPending
            self.updatedAt = updatedAt
        }

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
            case proactivity
            case onboardingCompletedAt = "onboarding_completed_at"
            case dataResetAt = "data_reset_at"
            case dataResetPending = "data_reset_pending"
            case updatedAt = "updated_at"
        }
    }

    public struct PreferencesChange: Sendable, Codable, Hashable {
        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: OpenAPI.PreferencesEditable

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: OpenAPI.PreferencesEditable
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct PreferencesEditable: Sendable, Codable, Hashable {
        public var timezone: OpenAPI.Timezone
        public var locale: String?
        public var workingHours: OpenAPI.WorkingHours
        public var quietHours: OpenAPI.QuietHours?
        public var defaultDestinationIDs: [OpenAPI.ID]
        public var accountDefaults: [String: OpenAPI.ID]
        public var budgets: OpenAPI.BudgetPreferences
        public var input: OpenAPI.InputPreferences?
        public var proactivity: OpenAPI.ProactivityPreferences?
        public var onboardingCompletedAt: OpenAPI.Instant?

        public init(
            timezone: OpenAPI.Timezone,
            locale: String? = nil,
            workingHours: OpenAPI.WorkingHours,
            quietHours: OpenAPI.QuietHours? = nil,
            defaultDestinationIDs: [OpenAPI.ID],
            accountDefaults: [String: OpenAPI.ID],
            budgets: OpenAPI.BudgetPreferences,
            input: OpenAPI.InputPreferences? = nil,
            proactivity: OpenAPI.ProactivityPreferences? = nil,
            onboardingCompletedAt: OpenAPI.Instant? = nil
        ) {
            self.timezone = timezone
            self.locale = locale
            self.workingHours = workingHours
            self.quietHours = quietHours
            self.defaultDestinationIDs = defaultDestinationIDs
            self.accountDefaults = accountDefaults
            self.budgets = budgets
            self.input = input
            self.proactivity = proactivity
            self.onboardingCompletedAt = onboardingCompletedAt
        }

        enum CodingKeys: String, CodingKey {
            case timezone
            case locale
            case workingHours = "working_hours"
            case quietHours = "quiet_hours"
            case defaultDestinationIDs = "default_destination_ids"
            case accountDefaults = "account_defaults"
            case budgets
            case input
            case proactivity
            case onboardingCompletedAt = "onboarding_completed_at"
        }
    }

    public struct ProactivityPreferences: Sendable, Codable, Hashable {
        public var pushPolicy: OpenAPI.PushPolicy?
        public var pushChats: Bool?
        public var pushUrgentAndApprovals: Bool?
        public var pushMorningBrief: Bool?
        public var morningBriefTime: String?
        public var morningBriefEnabled: Bool?
        public var dailyProactiveBudgetUSD: Double?

        public init(
            pushPolicy: OpenAPI.PushPolicy? = nil,
            pushChats: Bool? = nil,
            pushUrgentAndApprovals: Bool? = nil,
            pushMorningBrief: Bool? = nil,
            morningBriefTime: String? = nil,
            morningBriefEnabled: Bool? = nil,
            dailyProactiveBudgetUSD: Double? = nil
        ) {
            self.pushPolicy = pushPolicy
            self.pushChats = pushChats
            self.pushUrgentAndApprovals = pushUrgentAndApprovals
            self.pushMorningBrief = pushMorningBrief
            self.morningBriefTime = morningBriefTime
            self.morningBriefEnabled = morningBriefEnabled
            self.dailyProactiveBudgetUSD = dailyProactiveBudgetUSD
        }

        enum CodingKeys: String, CodingKey {
            case pushPolicy = "push_policy"
            case pushChats = "push_chats"
            case pushUrgentAndApprovals = "push_urgent_and_approvals"
            case pushMorningBrief = "push_morning_brief"
            case morningBriefTime = "morning_brief_time"
            case morningBriefEnabled = "morning_brief_enabled"
            case dailyProactiveBudgetUSD = "daily_proactive_budget_usd"
        }
    }

    public struct Provenance: Sendable, Codable, Hashable {
        public var content: String
        public var source: OpenAPI.ResourceRef?
        public var sourceURL: String?
        public var retrievedAt: OpenAPI.Instant?
        public var connectionStatus: OpenAPI.ConnectionStatus?

        public init(
            content: String,
            source: OpenAPI.ResourceRef? = nil,
            sourceURL: String? = nil,
            retrievedAt: OpenAPI.Instant? = nil,
            connectionStatus: OpenAPI.ConnectionStatus? = nil
        ) {
            self.content = content
            self.source = source
            self.sourceURL = sourceURL
            self.retrievedAt = retrievedAt
            self.connectionStatus = connectionStatus
        }

        enum CodingKeys: String, CodingKey {
            case content
            case source
            case sourceURL = "source_url"
            case retrievedAt = "retrieved_at"
            case connectionStatus = "connection_status"
        }
    }

    public struct PushDevice: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var deviceID: String?
        public var environment: String
        public var createdAt: OpenAPI.Instant
        public var updatedAt: OpenAPI.Instant

        public init(
            id: OpenAPI.ID,
            deviceID: String? = nil,
            environment: String,
            createdAt: OpenAPI.Instant,
            updatedAt: OpenAPI.Instant
        ) {
            self.id = id
            self.deviceID = deviceID
            self.environment = environment
            self.createdAt = createdAt
            self.updatedAt = updatedAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case deviceID = "device_id"
            case environment
            case createdAt = "created_at"
            case updatedAt = "updated_at"
        }
    }

    public enum PushPolicy: String, Sendable, Codable, Hashable, CaseIterable {
        case urgentAndApprovals = "urgent_and_approvals"
        case includeBrief = "include_brief"
        case quietOnly = "quiet_only"
    }

    public struct QuestionPayload: Sendable, Codable, Hashable {
        public struct OptionItem: Sendable, Codable, Hashable {
            public var id: String
            public var label: String

            public init(
                id: String,
                label: String
            ) {
                self.id = id
                self.label = label
            }
        }
        public struct Constraints: Sendable, Codable, Hashable {
            public var maxLength: Int?
            public var pattern: String?

            public init(
                maxLength: Int? = nil,
                pattern: String? = nil
            ) {
                self.maxLength = maxLength
                self.pattern = pattern
            }

            enum CodingKeys: String, CodingKey {
                case maxLength = "max_length"
                case pattern
            }
        }

        public var input: String
        public var options: [OptionItem]?
        public var constraints: Constraints?

        public init(
            input: String,
            options: [OptionItem]? = nil,
            constraints: Constraints? = nil
        ) {
            self.input = input
            self.options = options
            self.constraints = constraints
        }
    }

    public struct QuietHours: Sendable, Codable, Hashable {
        public var enabled: Bool?
        public var start: String
        public var end: String

        public init(
            enabled: Bool? = nil,
            start: String,
            end: String
        ) {
            self.enabled = enabled
            self.start = start
            self.end = end
        }
    }

    public struct ReadPosition: Sendable, Codable, Hashable {
        public var messageID: String?
        public var through: OpenAPI.Instant?
        public var position: Int

        public init(
            messageID: String? = nil,
            through: OpenAPI.Instant? = nil,
            position: Int
        ) {
            self.messageID = messageID
            self.through = through
            self.position = position
        }

        enum CodingKeys: String, CodingKey {
            case messageID = "message_id"
            case through
            case position
        }
    }

    public enum Recovery: String, Sendable, Codable, Hashable, CaseIterable {
        case connect = "connect"
        case repair = "repair"
        case requestAccess = "request_access"
        case wait = "wait"
        case none = "none"
    }

    public enum RecurrenceScope: String, Sendable, Codable, Hashable, CaseIterable {
        case instance = "instance"
        case following = "following"
        case series = "series"
    }

    public struct RegisterPushDeviceRequest: Sendable, Codable, Hashable {
        public var token: String
        public var deviceID: String?
        public var environment: String

        public init(
            token: String,
            deviceID: String? = nil,
            environment: String
        ) {
            self.token = token
            self.deviceID = deviceID
            self.environment = environment
        }

        enum CodingKeys: String, CodingKey {
            case token
            case deviceID = "device_id"
            case environment
        }
    }

    public struct Resource: Sendable, Codable, Hashable {
        public var ref: OpenAPI.ResourceRef
        public var title: String
        public var kind: String
        public var selected: Bool?
        public var writable: Bool?

        public init(
            ref: OpenAPI.ResourceRef,
            title: String,
            kind: String,
            selected: Bool? = nil,
            writable: Bool? = nil
        ) {
            self.ref = ref
            self.title = title
            self.kind = kind
            self.selected = selected
            self.writable = writable
        }
    }

    public struct ResourceRef: Sendable, Codable, Hashable {
        public var connectionID: OpenAPI.ID
        public var kind: String
        public var providerID: String
        public var providerRevision: String?
        public var url: String?

        public init(
            connectionID: OpenAPI.ID,
            kind: String,
            providerID: String,
            providerRevision: String? = nil,
            url: String? = nil
        ) {
            self.connectionID = connectionID
            self.kind = kind
            self.providerID = providerID
            self.providerRevision = providerRevision
            self.url = url
        }

        enum CodingKeys: String, CodingKey {
            case connectionID = "connection_id"
            case kind
            case providerID = "provider_id"
            case providerRevision = "provider_revision"
            case url
        }
    }

    public struct RestoreComputerRequest: Sendable, Codable, Hashable {
        public var checkpointID: String?

        public init(
            checkpointID: String? = nil
        ) {
            self.checkpointID = checkpointID
        }

        enum CodingKeys: String, CodingKey {
            case checkpointID = "checkpoint_id"
        }
    }

    public typealias Revision = Int

    public struct Run: Sendable, Codable, Hashable {
        public struct Finishing: Sendable, Codable, Hashable {
            public var status: OpenAPI.RunStatus?

            public init(
                status: OpenAPI.RunStatus? = nil
            ) {
                self.status = status
            }
        }

        public var id: String
        public var title: String?
        public var status: OpenAPI.RunStatus
        public var cancelRequested: Bool
        public var origin: OpenAPI.TaskOrigin?
        public var finishing: Finishing?
        public var cleanupPending: Bool?
        public var cleanupError: String?
        public var request: String?
        public var holds: [OpenAPI.TaskHold]?
        public var position: Int?
        public var eventSequence: Int?
        public var question: OpenAPI.TaskQuestion?
        public var result: String?
        public var revision: Int?
        public var incorporated: Int?
        public var waiting: String?
        public var progress: String?
        public var spentUSD: Double?
        public var costUnknown: Bool?
        public var budgetUSD: Double?
        public var modelSelection: OpenAPI.ModelSelection?
        public var parentID: String?
        public var subtaskSpentUSD: Double?
        public var startedAt: OpenAPI.Instant
        public var finishedAt: OpenAPI.Instant?
        public var error: String?
        public var events: [OpenAPI.RunEvent]?
        public var artifacts: [OpenAPI.Artifact]?

        public init(
            id: String,
            title: String? = nil,
            status: OpenAPI.RunStatus,
            cancelRequested: Bool,
            origin: OpenAPI.TaskOrigin? = nil,
            finishing: Finishing? = nil,
            cleanupPending: Bool? = nil,
            cleanupError: String? = nil,
            request: String? = nil,
            holds: [OpenAPI.TaskHold]? = nil,
            position: Int? = nil,
            eventSequence: Int? = nil,
            question: OpenAPI.TaskQuestion? = nil,
            result: String? = nil,
            revision: Int? = nil,
            incorporated: Int? = nil,
            waiting: String? = nil,
            progress: String? = nil,
            spentUSD: Double? = nil,
            costUnknown: Bool? = nil,
            budgetUSD: Double? = nil,
            modelSelection: OpenAPI.ModelSelection? = nil,
            parentID: String? = nil,
            subtaskSpentUSD: Double? = nil,
            startedAt: OpenAPI.Instant,
            finishedAt: OpenAPI.Instant? = nil,
            error: String? = nil,
            events: [OpenAPI.RunEvent]? = nil,
            artifacts: [OpenAPI.Artifact]? = nil
        ) {
            self.id = id
            self.title = title
            self.status = status
            self.cancelRequested = cancelRequested
            self.origin = origin
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
            self.modelSelection = modelSelection
            self.parentID = parentID
            self.subtaskSpentUSD = subtaskSpentUSD
            self.startedAt = startedAt
            self.finishedAt = finishedAt
            self.error = error
            self.events = events
            self.artifacts = artifacts
        }

        enum CodingKeys: String, CodingKey {
            case id
            case title
            case status
            case cancelRequested = "cancel_requested"
            case origin
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
            case modelSelection = "model_selection"
            case parentID = "parent_id"
            case subtaskSpentUSD = "subtask_spent_usd"
            case startedAt = "started_at"
            case finishedAt = "finished_at"
            case error
            case events
            case artifacts
        }
    }

    public struct RunEvent: Sendable, Codable, Hashable {
        public var id: String?
        public var sequence: Int?
        public var instructionRevision: Int?
        public var kind: String
        public var text: String
        public var createdAt: OpenAPI.Instant

        public init(
            id: String? = nil,
            sequence: Int? = nil,
            instructionRevision: Int? = nil,
            kind: String,
            text: String,
            createdAt: OpenAPI.Instant
        ) {
            self.id = id
            self.sequence = sequence
            self.instructionRevision = instructionRevision
            self.kind = kind
            self.text = text
            self.createdAt = createdAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case sequence
            case instructionRevision = "instruction_revision"
            case kind
            case text
            case createdAt = "created_at"
        }
    }

    public enum RunStatus: String, Sendable, Codable, Hashable, CaseIterable {
        case running = "running"
        case completed = "completed"
        case failed = "failed"
        case cancelled = "cancelled"
    }

    public struct SaveModelSettingsRequest: Sendable, Codable, Hashable {
        public var selection: OpenAPI.ModelSelection
        public var expectedRevision: OpenAPI.Revision

        public init(
            selection: OpenAPI.ModelSelection,
            expectedRevision: OpenAPI.Revision
        ) {
            self.selection = selection
            self.expectedRevision = expectedRevision
        }

        enum CodingKeys: String, CodingKey {
            case selection
            case expectedRevision = "expected_revision"
        }
    }

    public struct SavedResult: Sendable, Codable, Hashable {
        public var id: String
        public var rootID: String
        public var version: Int
        public var revises: String?
        public var supersededBy: String?
        public var runID: String
        public var revision: Int?
        public var kind: String
        public var title: String
        public var text: String?
        public var name: String?
        public var mediaType: String?
        public var size: Int?
        public var artifactID: String?
        public var url: String?
        public var evidence: [String]?
        public var unmet: [String]?
        public var createdAt: OpenAPI.Instant
        public var checkedAt: OpenAPI.Instant?
        public var reportedAt: OpenAPI.Instant?
        public var deliveredAt: OpenAPI.Instant?
        public var messageID: String?

        public init(
            id: String,
            rootID: String,
            version: Int,
            revises: String? = nil,
            supersededBy: String? = nil,
            runID: String,
            revision: Int? = nil,
            kind: String,
            title: String,
            text: String? = nil,
            name: String? = nil,
            mediaType: String? = nil,
            size: Int? = nil,
            artifactID: String? = nil,
            url: String? = nil,
            evidence: [String]? = nil,
            unmet: [String]? = nil,
            createdAt: OpenAPI.Instant,
            checkedAt: OpenAPI.Instant? = nil,
            reportedAt: OpenAPI.Instant? = nil,
            deliveredAt: OpenAPI.Instant? = nil,
            messageID: String? = nil
        ) {
            self.id = id
            self.rootID = rootID
            self.version = version
            self.revises = revises
            self.supersededBy = supersededBy
            self.runID = runID
            self.revision = revision
            self.kind = kind
            self.title = title
            self.text = text
            self.name = name
            self.mediaType = mediaType
            self.size = size
            self.artifactID = artifactID
            self.url = url
            self.evidence = evidence
            self.unmet = unmet
            self.createdAt = createdAt
            self.checkedAt = checkedAt
            self.reportedAt = reportedAt
            self.deliveredAt = deliveredAt
            self.messageID = messageID
        }

        enum CodingKeys: String, CodingKey {
            case id
            case rootID = "root_id"
            case version
            case revises
            case supersededBy = "superseded_by"
            case runID = "run_id"
            case revision
            case kind
            case title
            case text
            case name
            case mediaType = "media_type"
            case size
            case artifactID = "artifact_id"
            case url
            case evidence
            case unmet
            case createdAt = "created_at"
            case checkedAt = "checked_at"
            case reportedAt = "reported_at"
            case deliveredAt = "delivered_at"
            case messageID = "message_id"
        }
    }

    public struct SearchHit: Sendable, Codable, Hashable {
        public var kind: String
        public var id: String
        public var title: String
        public var snippet: String
        public var jump: OpenAPI.SearchJump?
        public var provenance: OpenAPI.Provenance?
        public var freshness: OpenAPI.Instant?
        public var score: Double?

        public init(
            kind: String,
            id: String,
            title: String,
            snippet: String,
            jump: OpenAPI.SearchJump? = nil,
            provenance: OpenAPI.Provenance? = nil,
            freshness: OpenAPI.Instant? = nil,
            score: Double? = nil
        ) {
            self.kind = kind
            self.id = id
            self.title = title
            self.snippet = snippet
            self.jump = jump
            self.provenance = provenance
            self.freshness = freshness
            self.score = score
        }
    }

    public enum SearchJump: Sendable, Codable, Hashable {
        public struct ConversationPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var conversationID: String
            public var messageID: String?

            public init(
                kind: String,
                conversationID: String,
                messageID: String? = nil
            ) {
                self.kind = kind
                self.conversationID = conversationID
                self.messageID = messageID
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case conversationID = "conversation_id"
                case messageID = "message_id"
            }
        }
        public struct TaskPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var taskID: String

            public init(
                kind: String,
                taskID: String
            ) {
                self.kind = kind
                self.taskID = taskID
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case taskID = "task_id"
            }
        }
        public struct LibraryPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var itemID: String

            public init(
                kind: String,
                itemID: String
            ) {
                self.kind = kind
                self.itemID = itemID
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case itemID = "item_id"
            }
        }
        public struct AutomationPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var automationID: OpenAPI.ID

            public init(
                kind: String,
                automationID: OpenAPI.ID
            ) {
                self.kind = kind
                self.automationID = automationID
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case automationID = "automation_id"
            }
        }
        public struct MemoryPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var layer: String?
            public var memoryID: String?
            public var logDate: OpenAPI.LocalDate?
            public var entryID: String?

            public init(
                kind: String,
                layer: String? = nil,
                memoryID: String? = nil,
                logDate: OpenAPI.LocalDate? = nil,
                entryID: String? = nil
            ) {
                self.kind = kind
                self.layer = layer
                self.memoryID = memoryID
                self.logDate = logDate
                self.entryID = entryID
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case layer
                case memoryID = "memory_id"
                case logDate = "log_date"
                case entryID = "entry_id"
            }
        }

        case conversation(ConversationPayload)
        case task(TaskPayload)
        case library(LibraryPayload)
        case automation(AutomationPayload)
        case memory(MemoryPayload)

        private enum DiscriminatorKey: String, CodingKey {
            case discriminator = "kind"
        }

        public init(from decoder: any Decoder) throws {
            let container = try decoder.container(keyedBy: DiscriminatorKey.self)
            let kind = try container.decode(String.self, forKey: .discriminator)
            switch kind {
            case "conversation":
                self = .conversation(try ConversationPayload(from: decoder))
            case "task":
                self = .task(try TaskPayload(from: decoder))
            case "library":
                self = .library(try LibraryPayload(from: decoder))
            case "automation":
                self = .automation(try AutomationPayload(from: decoder))
            case "memory":
                self = .memory(try MemoryPayload(from: decoder))
            default:
                throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported SearchJump kind: \(String(describing: kind))")
            }
        }

        public func encode(to encoder: any Encoder) throws {
            switch self {
            case .conversation(let value):
                try value.encode(to: encoder)
            case .task(let value):
                try value.encode(to: encoder)
            case .library(let value):
                try value.encode(to: encoder)
            case .automation(let value):
                try value.encode(to: encoder)
            case .memory(let value):
                try value.encode(to: encoder)
            }
        }
    }

    public struct SearchResponse: Sendable, Codable, Hashable {
        public var items: [OpenAPI.SearchHit]
        public var nextCursor: String?
        public var query: String?

        public init(
            items: [OpenAPI.SearchHit],
            nextCursor: String? = nil,
            query: String? = nil
        ) {
            self.items = items
            self.nextCursor = nextCursor
            self.query = query
        }

        enum CodingKeys: String, CodingKey {
            case items
            case nextCursor = "next_cursor"
            case query
        }
    }

    public struct ServiceKeyStatus: Sendable, Codable, Hashable {
        public var service: String
        public var label: String
        public var configured: Bool
        public var last4: String?
        public var maskedKey: String?
        public var source: String?
        public var updatedAt: OpenAPI.Instant?
        public var validatedAt: OpenAPI.Instant?

        public init(
            service: String,
            label: String,
            configured: Bool,
            last4: String? = nil,
            maskedKey: String? = nil,
            source: String? = nil,
            updatedAt: OpenAPI.Instant? = nil,
            validatedAt: OpenAPI.Instant? = nil
        ) {
            self.service = service
            self.label = label
            self.configured = configured
            self.last4 = last4
            self.maskedKey = maskedKey
            self.source = source
            self.updatedAt = updatedAt
            self.validatedAt = validatedAt
        }

        enum CodingKeys: String, CodingKey {
            case service
            case label
            case configured
            case last4
            case maskedKey = "masked_key"
            case source
            case updatedAt = "updated_at"
            case validatedAt = "validated_at"
        }
    }

    public struct ServiceKeysResponse: Sendable, Codable, Hashable {
        public var services: [OpenAPI.ServiceKeyStatus]

        public init(
            services: [OpenAPI.ServiceKeyStatus]
        ) {
            self.services = services
        }
    }

    public struct Session: Sendable, Codable, Hashable {
        public var messages: [OpenAPI.Message]?
        public var runs: [OpenAPI.Run]?
        public var paused: Bool
        public var dispatches: [OpenAPI.ConversationDispatch]?
        public var classificationHint: OpenAPI.ClassificationHint?
        public var usage: [OpenAPI.Usage]?
        public var conversation: OpenAPI.ConversationStatus?
        public var traces: [OpenAPI.ConversationTrace]?
        public var nextCursor: String?
        public var results: [OpenAPI.SavedResult]?
        public var position: Int?
        public var readPosition: OpenAPI.ReadPosition?

        public init(
            messages: [OpenAPI.Message]? = nil,
            runs: [OpenAPI.Run]? = nil,
            paused: Bool,
            dispatches: [OpenAPI.ConversationDispatch]? = nil,
            classificationHint: OpenAPI.ClassificationHint? = nil,
            usage: [OpenAPI.Usage]? = nil,
            conversation: OpenAPI.ConversationStatus? = nil,
            traces: [OpenAPI.ConversationTrace]? = nil,
            nextCursor: String? = nil,
            results: [OpenAPI.SavedResult]? = nil,
            position: Int? = nil,
            readPosition: OpenAPI.ReadPosition? = nil
        ) {
            self.messages = messages
            self.runs = runs
            self.paused = paused
            self.dispatches = dispatches
            self.classificationHint = classificationHint
            self.usage = usage
            self.conversation = conversation
            self.traces = traces
            self.nextCursor = nextCursor
            self.results = results
            self.position = position
            self.readPosition = readPosition
        }

        enum CodingKeys: String, CodingKey {
            case messages
            case runs
            case paused
            case dispatches
            case classificationHint = "classification_hint"
            case usage
            case conversation
            case traces
            case nextCursor = "next_cursor"
            case results
            case position
            case readPosition = "read_position"
        }
    }

    public struct SetModelKeyRequest: Sendable, Codable, Hashable {
        public var apiKey: String

        public init(
            apiKey: String
        ) {
            self.apiKey = apiKey
        }

        enum CodingKeys: String, CodingKey {
            case apiKey = "api_key"
        }
    }

    public struct SetServiceKeyRequest: Sendable, Codable, Hashable {
        public var apiKey: String

        public init(
            apiKey: String
        ) {
            self.apiKey = apiKey
        }

        enum CodingKeys: String, CodingKey {
            case apiKey = "api_key"
        }
    }

    public struct SignInPayload: Sendable, Codable, Hashable {
        public var auth: String
        public var authAttemptID: OpenAPI.ID?
        public var integrationID: String
        public var connectionID: String?
        public var authorizeURL: String?
        public var requestedScopes: [String]?
        public var returnTo: String?
        public var expiresAt: OpenAPI.Instant?

        public init(
            auth: String,
            authAttemptID: OpenAPI.ID? = nil,
            integrationID: String,
            connectionID: String? = nil,
            authorizeURL: String? = nil,
            requestedScopes: [String]? = nil,
            returnTo: String? = nil,
            expiresAt: OpenAPI.Instant? = nil
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
    }

    public struct TaskHold: Sendable, Codable, Hashable {
        public var id: String
        public var taskID: String
        public var question: String
        public var status: String
        public var answeredTurnID: String?
        public var createdAt: OpenAPI.Instant

        public init(
            id: String,
            taskID: String,
            question: String,
            status: String,
            answeredTurnID: String? = nil,
            createdAt: OpenAPI.Instant
        ) {
            self.id = id
            self.taskID = taskID
            self.question = question
            self.status = status
            self.answeredTurnID = answeredTurnID
            self.createdAt = createdAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case taskID = "task_id"
            case question
            case status
            case answeredTurnID = "answered_turn_id"
            case createdAt = "created_at"
        }
    }

    public struct TaskOrigin: Sendable, Codable, Hashable {
        public var conversationID: String?
        public var messageID: String?
        public var automationID: OpenAPI.ID?
        public var occurrenceID: String?
        public var continuesTaskID: String?

        public init(
            conversationID: String? = nil,
            messageID: String? = nil,
            automationID: OpenAPI.ID? = nil,
            occurrenceID: String? = nil,
            continuesTaskID: String? = nil
        ) {
            self.conversationID = conversationID
            self.messageID = messageID
            self.automationID = automationID
            self.occurrenceID = occurrenceID
            self.continuesTaskID = continuesTaskID
        }

        enum CodingKeys: String, CodingKey {
            case conversationID = "conversation_id"
            case messageID = "message_id"
            case automationID = "automation_id"
            case occurrenceID = "occurrence_id"
            case continuesTaskID = "continues_task_id"
        }
    }

    public struct TaskQuestion: Sendable, Codable, Hashable {
        public var id: String
        public var kind: String
        public var text: String
        public var callID: String?
        public var instructionRevision: Int
        public var resolvedBy: String?
        public var readOnly: Bool?
        public var clears: [String]?
        public var input: String?
        public var options: [OpenAPI.TaskQuestionOption]?
        public var interactive: Bool?

        public init(
            id: String,
            kind: String,
            text: String,
            callID: String? = nil,
            instructionRevision: Int,
            resolvedBy: String? = nil,
            readOnly: Bool? = nil,
            clears: [String]? = nil,
            input: String? = nil,
            options: [OpenAPI.TaskQuestionOption]? = nil,
            interactive: Bool? = nil
        ) {
            self.id = id
            self.kind = kind
            self.text = text
            self.callID = callID
            self.instructionRevision = instructionRevision
            self.resolvedBy = resolvedBy
            self.readOnly = readOnly
            self.clears = clears
            self.input = input
            self.options = options
            self.interactive = interactive
        }

        enum CodingKeys: String, CodingKey {
            case id
            case kind
            case text
            case callID = "call_id"
            case instructionRevision = "instruction_revision"
            case resolvedBy = "resolved_by"
            case readOnly = "read_only"
            case clears
            case input
            case options
            case interactive
        }
    }

    public struct TaskQuestionOption: Sendable, Codable, Hashable {
        public var id: String
        public var label: String

        public init(
            id: String,
            label: String
        ) {
            self.id = id
            self.label = label
        }
    }

    public typealias Timezone = String

    public enum Trigger: Sendable, Codable, Hashable {
        public struct OncePayload: Sendable, Codable, Hashable {
            public var kind: String
            public var at: OpenAPI.Instant

            public init(
                kind: String,
                at: OpenAPI.Instant
            ) {
                self.kind = kind
                self.at = at
            }
        }
        public struct RecurringPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var localStart: String
            public var timezone: OpenAPI.Timezone
            public var rrule: String

            public init(
                kind: String,
                localStart: String,
                timezone: OpenAPI.Timezone,
                rrule: String
            ) {
                self.kind = kind
                self.localStart = localStart
                self.timezone = timezone
                self.rrule = rrule
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case localStart = "local_start"
                case timezone
                case rrule
            }
        }
        public struct EventRelativePayload: Sendable, Codable, Hashable {
            public var kind: String
            public var event: OpenAPI.ResourceRef
            public var offsetSeconds: Int

            public init(
                kind: String,
                event: OpenAPI.ResourceRef,
                offsetSeconds: Int
            ) {
                self.kind = kind
                self.event = event
                self.offsetSeconds = offsetSeconds
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case event
                case offsetSeconds = "offset_seconds"
            }
        }
        public struct WatchPayload: Sendable, Codable, Hashable {
            public var kind: String
            public var source: OpenAPI.ResourceRef
            public var predicateRef: String
            public var minIntervalSeconds: Int

            public init(
                kind: String,
                source: OpenAPI.ResourceRef,
                predicateRef: String,
                minIntervalSeconds: Int
            ) {
                self.kind = kind
                self.source = source
                self.predicateRef = predicateRef
                self.minIntervalSeconds = minIntervalSeconds
            }

            enum CodingKeys: String, CodingKey {
                case kind
                case source
                case predicateRef = "predicate_ref"
                case minIntervalSeconds = "min_interval_seconds"
            }
        }

        case once(OncePayload)
        case recurring(RecurringPayload)
        case eventRelative(EventRelativePayload)
        case watch(WatchPayload)

        private enum DiscriminatorKey: String, CodingKey {
            case discriminator = "kind"
        }

        public init(from decoder: any Decoder) throws {
            let container = try decoder.container(keyedBy: DiscriminatorKey.self)
            let kind = try container.decode(String.self, forKey: .discriminator)
            switch kind {
            case "once":
                self = .once(try OncePayload(from: decoder))
            case "recurring":
                self = .recurring(try RecurringPayload(from: decoder))
            case "event_relative":
                self = .eventRelative(try EventRelativePayload(from: decoder))
            case "watch":
                self = .watch(try WatchPayload(from: decoder))
            default:
                throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Trigger kind: \(String(describing: kind))")
            }
        }

        public func encode(to encoder: any Encoder) throws {
            switch self {
            case .once(let value):
                try value.encode(to: encoder)
            case .recurring(let value):
                try value.encode(to: encoder)
            case .eventRelative(let value):
                try value.encode(to: encoder)
            case .watch(let value):
                try value.encode(to: encoder)
            }
        }
    }

    public typealias UUID = String

    public struct UpdateComputerSessionRequest: Sendable, Codable, Hashable {
        public var mode: String

        public init(
            mode: String
        ) {
            self.mode = mode
        }
    }

    public struct UploadAccept: Sendable, Codable, Hashable {
        public var mediaTypes: [String]
        public var maxBytes: Int
        public var extensions: [String]?

        public init(
            mediaTypes: [String],
            maxBytes: Int,
            extensions: [String]? = nil
        ) {
            self.mediaTypes = mediaTypes
            self.maxBytes = maxBytes
            self.extensions = extensions
        }

        enum CodingKeys: String, CodingKey {
            case mediaTypes = "media_types"
            case maxBytes = "max_bytes"
            case extensions
        }
    }

    public struct Usage: Sendable, Codable, Hashable {
        public var id: String
        public var inputID: String
        public var provider: String
        public var model: String
        public var inputTokens: Int?
        public var outputTokens: Int?
        public var totalTokens: Int?
        public var cachedTokens: Int?
        public var cacheWriteTokens: Int?
        public var costUSD: Double?
        public var estimatedExposureUSD: Double?
        public var costStatus: String
        public var requestStatus: String
        public var createdAt: OpenAPI.Instant

        public init(
            id: String,
            inputID: String,
            provider: String,
            model: String,
            inputTokens: Int? = nil,
            outputTokens: Int? = nil,
            totalTokens: Int? = nil,
            cachedTokens: Int? = nil,
            cacheWriteTokens: Int? = nil,
            costUSD: Double? = nil,
            estimatedExposureUSD: Double? = nil,
            costStatus: String,
            requestStatus: String,
            createdAt: OpenAPI.Instant
        ) {
            self.id = id
            self.inputID = inputID
            self.provider = provider
            self.model = model
            self.inputTokens = inputTokens
            self.outputTokens = outputTokens
            self.totalTokens = totalTokens
            self.cachedTokens = cachedTokens
            self.cacheWriteTokens = cacheWriteTokens
            self.costUSD = costUSD
            self.estimatedExposureUSD = estimatedExposureUSD
            self.costStatus = costStatus
            self.requestStatus = requestStatus
            self.createdAt = createdAt
        }

        enum CodingKeys: String, CodingKey {
            case id
            case inputID = "input_id"
            case provider
            case model
            case inputTokens = "input_tokens"
            case outputTokens = "output_tokens"
            case totalTokens = "total_tokens"
            case cachedTokens = "cached_tokens"
            case cacheWriteTokens = "cache_write_tokens"
            case costUSD = "cost_usd"
            case estimatedExposureUSD = "estimated_exposure_usd"
            case costStatus = "cost_status"
            case requestStatus = "request_status"
            case createdAt = "created_at"
        }
    }

    public struct WorkActionRequest: Sendable, Codable, Hashable {
        public enum Value: Sendable, Codable, Hashable {
            public struct StopPayload: Sendable, Codable, Hashable {
                public var action: String

                public init(
                    action: String
                ) {
                    self.action = action
                }
            }
            public struct ContinuePayload: Sendable, Codable, Hashable {
                public var action: String
                public var budgetUSD: Double?

                public init(
                    action: String,
                    budgetUSD: Double? = nil
                ) {
                    self.action = action
                    self.budgetUSD = budgetUSD
                }

                enum CodingKeys: String, CodingKey {
                    case action
                    case budgetUSD = "budget_usd"
                }
            }

            case stop(StopPayload)
            case `continue`(ContinuePayload)

            private enum DiscriminatorKey: String, CodingKey {
                case discriminator = "action"
            }

            public init(from decoder: any Decoder) throws {
                let container = try decoder.container(keyedBy: DiscriminatorKey.self)
                let kind = try container.decode(String.self, forKey: .discriminator)
                switch kind {
                case "stop":
                    self = .stop(try StopPayload(from: decoder))
                case "continue":
                    self = .`continue`(try ContinuePayload(from: decoder))
                default:
                    throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Value action: \(String(describing: kind))")
                }
            }

            public func encode(to encoder: any Encoder) throws {
                switch self {
                case .stop(let value):
                    try value.encode(to: encoder)
                case .`continue`(let value):
                    try value.encode(to: encoder)
                }
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

    public struct WorkCost: Sendable, Codable, Hashable {
        public var amountUSD: Double?
        public var status: String
        public var budgetUSD: Double?

        public init(
            amountUSD: Double? = nil,
            status: String,
            budgetUSD: Double? = nil
        ) {
            self.amountUSD = amountUSD
            self.status = status
            self.budgetUSD = budgetUSD
        }

        enum CodingKeys: String, CodingKey {
            case amountUSD = "amount_usd"
            case status
            case budgetUSD = "budget_usd"
        }
    }

    public struct WorkDetail: Sendable, Codable, Hashable {
        public var item: OpenAPI.WorkItem
        public var operations: [OpenAPI.OperationProjection]
        public var interactions: [OpenAPI.Interaction]
        public var result: String?
        public var events: [OpenAPI.RunEvent]?
        public var eventsTruncated: Bool?

        public init(
            item: OpenAPI.WorkItem,
            operations: [OpenAPI.OperationProjection],
            interactions: [OpenAPI.Interaction],
            result: String? = nil,
            events: [OpenAPI.RunEvent]? = nil,
            eventsTruncated: Bool? = nil
        ) {
            self.item = item
            self.operations = operations
            self.interactions = interactions
            self.result = result
            self.events = events
            self.eventsTruncated = eventsTruncated
        }

        enum CodingKeys: String, CodingKey {
            case item
            case operations
            case interactions
            case result
            case events
            case eventsTruncated = "events_truncated"
        }
    }

    public struct WorkItem: Sendable, Codable, Hashable {
        public var taskID: String
        public var title: String
        public var status: OpenAPI.RunStatus
        public var bucket: String
        public var instructionRevision: Int
        public var origin: OpenAPI.WorkOrigin
        public var interactionIDs: [String]
        public var progress: String?
        public var operations: OpenAPI.WorkOperationsSummary
        public var resultIDs: [String]
        public var cost: OpenAPI.WorkCost
        public var parentTaskID: String?
        public var startedAt: OpenAPI.Instant
        public var finishedAt: OpenAPI.Instant?
        public var cancelRequested: Bool
        public var queued: Bool?
        public var steps: [OpenAPI.WorkStep]?
        public var partial: Bool?
        public var error: OpenAPI.ErrorDetail?

        public init(
            taskID: String,
            title: String,
            status: OpenAPI.RunStatus,
            bucket: String,
            instructionRevision: Int,
            origin: OpenAPI.WorkOrigin,
            interactionIDs: [String],
            progress: String? = nil,
            operations: OpenAPI.WorkOperationsSummary,
            resultIDs: [String],
            cost: OpenAPI.WorkCost,
            parentTaskID: String? = nil,
            startedAt: OpenAPI.Instant,
            finishedAt: OpenAPI.Instant? = nil,
            cancelRequested: Bool,
            queued: Bool? = nil,
            steps: [OpenAPI.WorkStep]? = nil,
            partial: Bool? = nil,
            error: OpenAPI.ErrorDetail? = nil
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
    }

    public struct WorkOperationsSummary: Sendable, Codable, Hashable {
        public var total: Int
        public var uncertain: Int
        public var failed: Int
        public var submitted: Int

        public init(
            total: Int,
            uncertain: Int,
            failed: Int,
            submitted: Int
        ) {
            self.total = total
            self.uncertain = uncertain
            self.failed = failed
            self.submitted = submitted
        }
    }

    public struct WorkOrigin: Sendable, Codable, Hashable {
        public var kind: String
        public var conversationID: String?
        public var messageID: String?
        public var automationID: OpenAPI.ID?
        public var occurrenceID: String?
        public var continuesTaskID: String?

        public init(
            kind: String,
            conversationID: String? = nil,
            messageID: String? = nil,
            automationID: OpenAPI.ID? = nil,
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

        enum CodingKeys: String, CodingKey {
            case kind
            case conversationID = "conversation_id"
            case messageID = "message_id"
            case automationID = "automation_id"
            case occurrenceID = "occurrence_id"
            case continuesTaskID = "continues_task_id"
        }
    }

    public struct WorkRef: Sendable, Codable, Hashable {
        public var taskID: String
        public var operationID: String?

        public init(
            taskID: String,
            operationID: String? = nil
        ) {
            self.taskID = taskID
            self.operationID = operationID
        }

        enum CodingKeys: String, CodingKey {
            case taskID = "task_id"
            case operationID = "operation_id"
        }
    }

    public struct WorkStep: Sendable, Codable, Hashable {
        public var text: String
        public var status: String

        public init(
            text: String,
            status: String
        ) {
            self.text = text
            self.status = status
        }
    }

    public typealias WorkingHours = [OpenAPI.WorkingHoursSlot]

    public struct WorkingHoursSlot: Sendable, Codable, Hashable {
        public var day: Int
        public var start: String
        public var end: String

        public init(
            day: Int,
            start: String,
            end: String
        ) {
            self.day = day
            self.start = start
            self.end = end
        }
    }

    public struct WorkingMemory: Sendable, Codable, Hashable {
        public var revision: OpenAPI.Revision
        public var from: OpenAPI.Instant?
        public var extractedThrough: OpenAPI.Instant?
        public var flushedThrough: OpenAPI.Instant?
        public var through: OpenAPI.Instant?
        public var dreamThrough: OpenAPI.Instant?
        public var lastDreamAt: OpenAPI.Instant?
        public var items: [OpenAPI.WorkingMemoryItem]
        public var gist: String
        public var turns: Int
        public var model: String?
        public var coreBytes: Int
        public var coreBudgetBytes: Int
        public var updatedAt: OpenAPI.Instant?

        public init(
            revision: OpenAPI.Revision,
            from: OpenAPI.Instant? = nil,
            extractedThrough: OpenAPI.Instant? = nil,
            flushedThrough: OpenAPI.Instant? = nil,
            through: OpenAPI.Instant? = nil,
            dreamThrough: OpenAPI.Instant? = nil,
            lastDreamAt: OpenAPI.Instant? = nil,
            items: [OpenAPI.WorkingMemoryItem],
            gist: String,
            turns: Int,
            model: String? = nil,
            coreBytes: Int,
            coreBudgetBytes: Int,
            updatedAt: OpenAPI.Instant? = nil
        ) {
            self.revision = revision
            self.from = from
            self.extractedThrough = extractedThrough
            self.flushedThrough = flushedThrough
            self.through = through
            self.dreamThrough = dreamThrough
            self.lastDreamAt = lastDreamAt
            self.items = items
            self.gist = gist
            self.turns = turns
            self.model = model
            self.coreBytes = coreBytes
            self.coreBudgetBytes = coreBudgetBytes
            self.updatedAt = updatedAt
        }

        enum CodingKeys: String, CodingKey {
            case revision
            case from
            case extractedThrough = "extracted_through"
            case flushedThrough = "flushed_through"
            case through
            case dreamThrough = "dream_through"
            case lastDreamAt = "last_dream_at"
            case items
            case gist
            case turns
            case model
            case coreBytes = "core_bytes"
            case coreBudgetBytes = "core_budget_bytes"
            case updatedAt = "updated_at"
        }
    }

    public struct WorkingMemoryItem: Sendable, Codable, Hashable {
        public var id: OpenAPI.ID
        public var kind: String
        public var quote: String
        public var inputID: String
        public var note: String?

        public init(
            id: OpenAPI.ID,
            kind: String,
            quote: String,
            inputID: String,
            note: String? = nil
        ) {
            self.id = id
            self.kind = kind
            self.quote = quote
            self.inputID = inputID
            self.note = note
        }

        enum CodingKeys: String, CodingKey {
            case id
            case kind
            case quote
            case inputID = "input_id"
            case note
        }
    }

    public struct WorkingMemoryItemAction: Sendable, Codable, Hashable {
        public enum Value: Sendable, Codable, Hashable {
            public struct PromotePayload: Sendable, Codable, Hashable {
                public var action: String
                public var text: String?
                public var about: String?
                public var section: String?
                public var pinned: Bool?

                public init(
                    action: String,
                    text: String? = nil,
                    about: String? = nil,
                    section: String? = nil,
                    pinned: Bool? = nil
                ) {
                    self.action = action
                    self.text = text
                    self.about = about
                    self.section = section
                    self.pinned = pinned
                }
            }
            public struct DismissPayload: Sendable, Codable, Hashable {
                public var action: String

                public init(
                    action: String
                ) {
                    self.action = action
                }
            }

            case promote(PromotePayload)
            case dismiss(DismissPayload)

            private enum DiscriminatorKey: String, CodingKey {
                case discriminator = "action"
            }

            public init(from decoder: any Decoder) throws {
                let container = try decoder.container(keyedBy: DiscriminatorKey.self)
                let kind = try container.decode(String.self, forKey: .discriminator)
                switch kind {
                case "promote":
                    self = .promote(try PromotePayload(from: decoder))
                case "dismiss":
                    self = .dismiss(try DismissPayload(from: decoder))
                default:
                    throw DecodingError.dataCorruptedError(forKey: .discriminator, in: container, debugDescription: "Unsupported Value action: \(String(describing: kind))")
                }
            }

            public func encode(to encoder: any Encoder) throws {
                switch self {
                case .promote(let value):
                    try value.encode(to: encoder)
                case .dismiss(let value):
                    try value.encode(to: encoder)
                }
            }
        }

        public var requestID: OpenAPI.UUID
        public var expectedRevision: OpenAPI.Revision
        public var value: Value

        public init(
            requestID: OpenAPI.UUID,
            expectedRevision: OpenAPI.Revision,
            value: Value
        ) {
            self.requestID = requestID
            self.expectedRevision = expectedRevision
            self.value = value
        }

        enum CodingKeys: String, CodingKey {
            case requestID = "request_id"
            case expectedRevision = "expected_revision"
            case value
        }
    }

}
