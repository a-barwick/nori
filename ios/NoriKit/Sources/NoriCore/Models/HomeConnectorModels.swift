import Foundation

public struct HomeConnectorModel: Sendable, Codable, Hashable {
    public var name: String
    public var digest: String?
    public var capabilities: [String]?
}

public struct HomeConnector: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var status: String
    public var lastSeenAt: String?
    public var version: String?
    public var models: [HomeConnectorModel]

    enum CodingKeys: String, CodingKey {
        case id, name, status, version, models
        case lastSeenAt = "last_seen_at"
    }
}

public struct HomeConnectorDownloads: Sendable, Codable, Hashable {
    public var macOS: String?
    public var linuxAMD64: String?
    public var linuxARM64: String?

    enum CodingKeys: String, CodingKey {
        case macOS = "macos"
        case linuxAMD64 = "linux_amd64"
        case linuxARM64 = "linux_arm64"
    }
}

public struct HomeConnectorsResponse: Sendable, Codable, Hashable {
    public var enabled: Bool
    public var devices: [HomeConnector]
    public var downloads: HomeConnectorDownloads?
}

public struct HomePairing: Sendable, Codable, Hashable {
    public var name: String
    public var userCode: String
    public var expiresAt: String

    enum CodingKeys: String, CodingKey {
        case name
        case userCode = "user_code"
        case expiresAt = "expires_at"
    }
}

public struct HomePairingRequest: Sendable, Codable {
    public var userCode: String

    enum CodingKeys: String, CodingKey {
        case userCode = "user_code"
    }

    public init(userCode: String) {
        self.userCode = userCode
    }
}

public struct HomeConnectorTest: Sendable, Codable {
    public var status: String
}
