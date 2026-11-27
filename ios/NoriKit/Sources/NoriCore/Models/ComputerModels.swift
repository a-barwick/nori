import Foundation

public enum ComputerState: String, Sendable, Codable, Hashable {
    case notProvisioned = "not_provisioned"
    case provisioning
    case running = "ready"
    case sleeping
    case resetting
    case error
    case deleting
    case deleted

    public var displayLabel: String {
        switch self {
        case .running:
            return "Active"
        case .sleeping:
            return "Asleep"
        case .provisioning:
            return "Starting…"
        case .resetting:
            return "Resetting"
        case .notProvisioned:
            return "Ready to start"
        case .error:
            return "Needs attention"
        case .deleting, .deleted:
            return "Updating…"
        }
    }
}

public enum ComputerSessionMode: String, Sendable, Codable, Hashable {
    case view
    case control
}

public struct ComputerCheckpoint: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var label: String?
    public var createdAt: String?

    enum CodingKeys: String, CodingKey {
        case id
        case label
        case createdAt = "created_at"
    }

    public init(id: String, label: String? = nil, createdAt: String? = nil) {
        self.id = id
        self.label = label
        self.createdAt = createdAt
    }
}

public struct ComputerSessionSummary: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var computerID: String
    public var mode: ComputerSessionMode
    public var taskID: String?
    public var interactionID: String?
    public var expiresAt: String
    public var createdAt: String?
    public var lastActivityAt: String?

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

    public init(
        id: String,
        computerID: String,
        mode: ComputerSessionMode,
        taskID: String? = nil,
        interactionID: String? = nil,
        expiresAt: String,
        createdAt: String? = nil,
        lastActivityAt: String? = nil
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
}

public struct ComputerStatus: Sendable, Codable, Hashable {
    public var enabled: Bool
    public var computerID: String?
    public var provider: String
    public var status: ComputerState
    public var baselineCheckpointID: String?
    public var diskUsedBytes: Int64
    public var diskMaxBytes: Int64
    public var checkpoints: [ComputerCheckpoint]
    public var activeSessions: [ComputerSessionSummary]
    public var lastActiveAt: String?

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

    public init(
        enabled: Bool = false,
        computerID: String? = nil,
        provider: String = "disabled",
        status: ComputerState = .notProvisioned,
        baselineCheckpointID: String? = nil,
        diskUsedBytes: Int64 = 0,
        diskMaxBytes: Int64 = 0,
        checkpoints: [ComputerCheckpoint] = [],
        activeSessions: [ComputerSessionSummary] = [],
        lastActiveAt: String? = nil
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

    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
        self.computerID = try c.decodeIfPresent(String.self, forKey: .computerID)
        self.provider = try c.decodeIfPresent(String.self, forKey: .provider) ?? "disabled"
        self.status = try c.decodeIfPresent(ComputerState.self, forKey: .status) ?? .notProvisioned
        self.baselineCheckpointID = try c.decodeIfPresent(String.self, forKey: .baselineCheckpointID)
        self.diskUsedBytes = try c.decodeIfPresent(Int64.self, forKey: .diskUsedBytes) ?? 0
        self.diskMaxBytes = try c.decodeIfPresent(Int64.self, forKey: .diskMaxBytes) ?? 0
        self.checkpoints = try c.decodeIfPresent([ComputerCheckpoint].self, forKey: .checkpoints) ?? []
        self.activeSessions = try c.decodeIfPresent([ComputerSessionSummary].self, forKey: .activeSessions) ?? []
        self.lastActiveAt = try c.decodeIfPresent(String.self, forKey: .lastActiveAt)
    }

    public var diskUsageRatio: Double {
        guard diskMaxBytes > 0 else { return 0 }
        return min(1.0, max(0.0, Double(diskUsedBytes) / Double(diskMaxBytes)))
    }
}

public struct CreateComputerSessionRequest: Sendable, Codable, Hashable {
    public var mode: ComputerSessionMode?
    public var taskID: String?
    public var interactionID: String?

    enum CodingKeys: String, CodingKey {
        case mode
        case taskID = "task_id"
        case interactionID = "interaction_id"
    }

    public init(mode: ComputerSessionMode? = nil, taskID: String? = nil, interactionID: String? = nil) {
        self.mode = mode
        self.taskID = taskID
        self.interactionID = interactionID
    }
}

public struct CreateComputerSessionResponse: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var computerID: String
    public var mode: ComputerSessionMode
    public var taskID: String?
    public var interactionID: String?
    public var token: String
    public var wsPath: String
    public var expiresAt: String

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

    public init(
        id: String,
        computerID: String,
        mode: ComputerSessionMode,
        taskID: String? = nil,
        interactionID: String? = nil,
        token: String,
        wsPath: String,
        expiresAt: String
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
}

public struct UpdateComputerSessionRequest: Sendable, Codable, Hashable {
    public var mode: ComputerSessionMode

    public init(mode: ComputerSessionMode) {
        self.mode = mode
    }
}

public struct CreateComputerCheckpointRequest: Sendable, Codable, Hashable {
    public var label: String?

    public init(label: String? = nil) {
        self.label = label
    }
}

public struct RestoreComputerRequest: Sendable, Codable, Hashable {
    public var checkpointID: String?

    enum CodingKeys: String, CodingKey {
        case checkpointID = "checkpoint_id"
    }

    public init(checkpointID: String? = nil) {
        self.checkpointID = checkpointID
    }
}
