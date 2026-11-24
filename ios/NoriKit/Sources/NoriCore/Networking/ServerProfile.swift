import Foundation
import Security

// MARK: - Server Profile

public enum ServerMode: String, Sendable, Codable, Hashable, CaseIterable {
    /// Rich built-in interactive demo mode (no running server needed).
    case demo
    /// Direct connection to Go core (`nori-local`) via `/api/users/resolve` + `/users/{id}/...`.
    case directCore = "direct_core"
    /// Hosted Next.js / host BFF proxy (`/api/nori` or `/api`) authenticated with Bearer token.
    case hostedBFF = "hosted_bff"

    public var displayLabel: String {
        switch self {
        case .demo: return "Demo Mode"
        case .directCore: return "Direct Go Core"
        case .hostedBFF: return "Hosted BFF"
        }
    }
}

public enum ServerPreset: String, Sendable, Codable, Hashable, CaseIterable, Identifiable {
    case demo
    case simulatorLocal
    case lanMac
    case hostedOIDC
    case custom

    public var id: String { rawValue }
}

public struct ServerProfile: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var preset: ServerPreset
    public var name: String
    public var subtitle: String
    public var mode: ServerMode
    public var baseURLString: String
    public var email: String
    public var apiPrefix: String

    public init(
        id: String,
        preset: ServerPreset,
        name: String,
        subtitle: String,
        mode: ServerMode,
        baseURLString: String,
        email: String = "alex@example.com",
        apiPrefix: String = ""
    ) {
        self.id = id
        self.preset = preset
        self.name = name
        self.subtitle = subtitle
        self.mode = mode
        self.baseURLString = baseURLString
        self.email = email
        self.apiPrefix = apiPrefix
    }

    public var baseURL: URL? {
        let trimmed = baseURLString.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        return URL(string: trimmed)
    }

    public static let demo = ServerProfile(
        id: "demo",
        preset: .demo,
        name: "Demo Mode",
        subtitle: "Interactive mock data · no server needed",
        mode: .demo,
        baseURLString: "nori://demo",
        email: "alex@example.com"
    )

    public static let simulatorLocal = ServerProfile(
        id: "simulator-local",
        preset: .simulatorLocal,
        name: "Simulator · nori-local",
        subtitle: "http://127.0.0.1:8088 · Direct Go core",
        mode: .directCore,
        baseURLString: "http://127.0.0.1:8088",
        email: "you@example.com"
    )

    public static let lanMac = ServerProfile(
        id: "lan-mac",
        preset: .lanMac,
        name: "Local Mac · LAN / Tailscale",
        subtitle: "Direct Go core over Wi-Fi or Tailscale",
        mode: .directCore,
        baseURLString: "http://your-mac.local:8089",
        email: "you@example.com"
    )

    public static let hostedOIDC = ServerProfile(
        id: "hosted-oidc",
        preset: .hostedOIDC,
        name: "Hosted BFF / OIDC",
        subtitle: "HTTPS BFF with Bearer token",
        mode: .hostedBFF,
        baseURLString: "https://nori.example.com",
        email: "alex@example.com",
        apiPrefix: "/api"
    )

    public static let builtInPresets: [ServerProfile] = [
        .demo,
        .simulatorLocal,
        .lanMac,
        .hostedOIDC
    ]
}

// MARK: - UserDefaults Persistence

public struct ServerProfileStore: Sendable {
    private let suiteName: String?
    private let activeKey = "nori.ios.activeServerProfile.v1"
    private let customProfilesKey = "nori.ios.customServerProfiles.v1"

    public init(suiteName: String? = nil) {
        self.suiteName = suiteName
    }

    private func defaults() -> UserDefaults {
        if let suiteName, let suite = UserDefaults(suiteName: suiteName) {
            return suite
        }
        return .standard
    }

    public func loadActiveProfile() -> ServerProfile {
        let ud = defaults()
        guard let data = ud.data(forKey: activeKey),
              let decoded = try? JSONDecoder().decode(ServerProfile.self, from: data) else {
            return .demo
        }
        return decoded
    }

    public func saveActiveProfile(_ profile: ServerProfile) {
        let ud = defaults()
        if let data = try? JSONEncoder().encode(profile) {
            ud.set(data, forKey: activeKey)
        }
    }

    public func loadProfiles() -> [ServerProfile] {
        let ud = defaults()
        if let data = ud.data(forKey: customProfilesKey),
           let decoded = try? JSONDecoder().decode([ServerProfile].self, from: data),
           !decoded.isEmpty {
            return decoded
        }
        return ServerProfile.builtInPresets
    }

    public func saveProfiles(_ profiles: [ServerProfile]) {
        let ud = defaults()
        if let data = try? JSONEncoder().encode(profiles) {
            ud.set(data, forKey: customProfilesKey)
        }
    }
}

// MARK: - Keychain Helper

public struct KeychainStore: Sendable {
    public let service: String

    public init(service: String = "com.nori.ios") {
        self.service = service
    }

    @discardableResult
    public func saveToken(_ token: String, account: String) -> Bool {
        let data = Data(token.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        SecItemDelete(query as CFDictionary)
        var attrs = query
        attrs[kSecValueData as String] = data
        attrs[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(attrs as CFDictionary, nil)
        return status == errSecSuccess
    }

    public func loadToken(account: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne
        ]
        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        guard status == errSecSuccess, let data = result as? Data else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    @discardableResult
    public func deleteToken(account: String) -> Bool {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        let status = SecItemDelete(query as CFDictionary)
        return status == errSecSuccess || status == errSecItemNotFound
    }
}
