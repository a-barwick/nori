import Foundation

public struct IntegrationSummary: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var title: String
    public var availability: String
    public var availabilityReason: String?

    enum CodingKeys: String, CodingKey {
        case id, title, availability
        case availabilityReason = "availability_reason"
    }
}

public struct ConnectionResource: Sendable, Codable, Hashable, Identifiable {
    public struct Reference: Sendable, Codable, Hashable {
        public var connectionID: String
        public var kind: String
        public var providerID: String

        enum CodingKeys: String, CodingKey {
            case connectionID = "connection_id"
            case kind
            case providerID = "provider_id"
        }
    }

    public var ref: Reference
    public var title: String
    public var kind: String
    public var selected: Bool
    public var writable: Bool
    public var id: String { "\(ref.connectionID):\(ref.kind):\(ref.providerID)" }
}


public enum MCPTransport: String, Sendable, Codable, Hashable {
    case http
    case stdio
}

public enum MCPAuthKind: String, Sendable, Codable, Hashable, CaseIterable {
    case none
    case oauth
    case configured
}

public enum MCPToolExposure: String, Sendable, Codable, Hashable, CaseIterable {
    case direct
    case discoverable
    case programmatic
    case disabled

    public var displayLabel: String {
        switch self {
        case .direct: return "Always"
        case .discoverable: return "Relevant"
        case .programmatic: return "Programmatic"
        case .disabled: return "Off"
        }
    }
}

public enum MCPObservedStatus: String, Sendable, Codable, Hashable {
    case disconnected
    case connecting
    case connected
    case needsSignIn = "needs_sign_in"
    case failed
    case disabled

    public var displayLabel: String {
        switch self {
        case .disconnected: return "Disconnected"
        case .connecting: return "Connecting"
        case .connected: return "Connected"
        case .needsSignIn: return "Needs sign-in"
        case .failed: return "Failed"
        case .disabled: return "Disabled"
        }
    }

    public var needsAttention: Bool {
        self == .needsSignIn || self == .failed
    }
}

public struct MCPServerSource: Sendable, Codable, Hashable {
    public var kind: String

    public init(kind: String = "user") {
        self.kind = kind
    }
}

public struct MCPExposureConfig: Sendable, Codable, Hashable {
    public var `default`: MCPToolExposure?
    public var overrides: [String: MCPToolExposure]?

    public init(default defaultExposure: MCPToolExposure? = .discoverable, overrides: [String: MCPToolExposure]? = nil) {
        self.default = defaultExposure
        self.overrides = overrides
    }
}

public struct MCPDiagnostic: Sendable, Codable, Hashable, Identifiable {
    public var id: String { "\(at)-\(level)-\(message)" }
    public var at: String
    public var level: String
    public var message: String

    public init(at: String, level: String, message: String) {
        self.at = at
        self.level = level
        self.message = message
    }
}

public struct MCPObservedState: Sendable, Codable, Hashable {
    public var status: MCPObservedStatus
    public var protocolVersion: String?
    public var capabilities: [String]?
    public var discoveryRevision: String?
    public var toolCount: Int?
    public var resourceCount: Int?
    public var promptCount: Int?
    public var lastSuccessAt: String?
    public var error: ErrorDetail?
    public var diagnostics: [MCPDiagnostic]?
    public var credential: String?

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

    public init(
        status: MCPObservedStatus = .disconnected,
        protocolVersion: String? = nil,
        capabilities: [String]? = nil,
        discoveryRevision: String? = nil,
        toolCount: Int? = nil,
        resourceCount: Int? = nil,
        promptCount: Int? = nil,
        lastSuccessAt: String? = nil,
        error: ErrorDetail? = nil,
        diagnostics: [MCPDiagnostic]? = nil,
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
}

public struct MCPServer: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var revision: Int
    public var source: MCPServerSource
    public var name: String
    public var transport: MCPTransport
    public var endpoint: String?
    public var connectionID: String?
    public var enabled: Bool
    public var exposure: MCPExposureConfig?
    public var observed: MCPObservedState
    public var auth: MCPAuthKind?

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

    public init(
        id: String,
        revision: Int = 1,
        source: MCPServerSource = MCPServerSource(),
        name: String,
        transport: MCPTransport = .http,
        endpoint: String? = nil,
        connectionID: String? = nil,
        enabled: Bool = true,
        exposure: MCPExposureConfig? = MCPExposureConfig(),
        observed: MCPObservedState = MCPObservedState(),
        auth: MCPAuthKind? = .oauth
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
}

public enum MCPToolAvailability: String, Sendable, Codable, Hashable {
    case ready
    case needsConnection = "needs_connection"
    case needsPermission = "needs_permission"
    case disabled
    case unavailable
}

public struct MCPServerTool: Sendable, Codable, Hashable, Identifiable {
    public var id: String { name }
    public var name: String
    public var title: String
    public var exposure: MCPToolExposure
    public var definition: DefinitionRef
    public var availability: MCPToolAvailability

    public init(
        name: String,
        title: String,
        exposure: MCPToolExposure = .discoverable,
        definition: DefinitionRef? = nil,
        availability: MCPToolAvailability = .ready
    ) {
        self.name = name
        self.title = title
        self.exposure = exposure
        self.definition = definition ?? DefinitionRef(id: name, version: "1.0.0", digest: "sha256:demo")
        self.availability = availability
    }
}

public struct MCPServerInput: Sendable, Codable, Hashable {
    public var name: String
    public var transport: MCPTransport
    public var endpoint: String?
    public var auth: MCPAuthKind
    public var enabled: Bool
    public var exposure: MCPExposureConfig

    public init(
        name: String,
        transport: MCPTransport = .http,
        endpoint: String?,
        auth: MCPAuthKind = .oauth,
        enabled: Bool = true,
        exposure: MCPExposureConfig = MCPExposureConfig(default: .discoverable)
    ) {
        self.name = name
        self.transport = transport
        self.endpoint = endpoint
        self.auth = auth
        self.enabled = enabled
        self.exposure = exposure
    }
}

public struct MCPCredentialInput: Sendable, Codable, Hashable {
    public var requestID: String
    public var token: String

    enum CodingKeys: String, CodingKey {
        case requestID = "request_id"
        case token
    }

    public init(requestID: String, token: String) {
        self.requestID = requestID
        self.token = token
    }
}


public enum ConnectionStatus: String, Sendable, Codable, Hashable {
    case notConnected = "not_connected"
    case connecting
    case connected
    case needsAttention = "needs_attention"
    case disabled
    case disconnecting
    case disconnected

    public var displayLabel: String {
        switch self {
        case .notConnected: return "Not connected"
        case .connecting: return "Connecting"
        case .connected: return "Connected"
        case .needsAttention: return "Needs attention"
        case .disabled: return "Disabled"
        case .disconnecting: return "Disconnecting"
        case .disconnected: return "Disconnected"
        }
    }
}

public struct ConnectionAccount: Sendable, Codable, Hashable {
    public var providerID: String
    public var label: String

    enum CodingKeys: String, CodingKey {
        case providerID = "provider_id"
        case label
    }

    public init(providerID: String, label: String) {
        self.providerID = providerID
        self.label = label
    }
}

public struct ConnectionHealth: Sendable, Codable, Hashable {
    public var checkedAt: String?
    public var code: String?
    public var message: String?
    public var lastSuccessAt: String?
    public var recovery: String?
    public var affectedCapabilityIDs: [String]?
    public var retryAfter: String?

    enum CodingKeys: String, CodingKey {
        case checkedAt = "checked_at"
        case code
        case message
        case lastSuccessAt = "last_success_at"
        case recovery
        case affectedCapabilityIDs = "affected_capability_ids"
        case retryAfter = "retry_after"
    }

    public init(
        checkedAt: String? = nil,
        code: String? = "ready",
        message: String? = nil,
        lastSuccessAt: String? = nil,
        recovery: String? = "none",
        affectedCapabilityIDs: [String]? = nil,
        retryAfter: String? = nil
    ) {
        self.checkedAt = checkedAt
        self.code = code
        self.message = message
        self.lastSuccessAt = lastSuccessAt
        self.recovery = recovery
        self.affectedCapabilityIDs = affectedCapabilityIDs
        self.retryAfter = retryAfter
    }
}

public struct Connection: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var revision: Int
    public var integrationID: String
    public var title: String
    public var account: ConnectionAccount?
    public var status: ConnectionStatus
    public var grantedScopes: [String]
    public var selectedResourceIDs: [String]
    public var health: ConnectionHealth
    public var defaults: [String]
    public var createdAt: String?
    public var updatedAt: String?
    public var missingScopes: [String]?
    public var capabilities: [String]?

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

    public init(
        id: String,
        revision: Int = 1,
        integrationID: String,
        title: String,
        account: ConnectionAccount? = nil,
        status: ConnectionStatus = .connected,
        grantedScopes: [String] = [],
        selectedResourceIDs: [String] = [],
        health: ConnectionHealth = ConnectionHealth(),
        defaults: [String] = [],
        createdAt: String? = nil,
        updatedAt: String? = nil,
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
}
