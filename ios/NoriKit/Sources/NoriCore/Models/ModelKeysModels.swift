import Foundation

// MARK: - Model API Keys (BYOK)

public typealias ModelKeyProvider = OpenAPI.ModelKeyProvider
public typealias SaveModelKeyRequest = OpenAPI.SetModelKeyRequest

extension OpenAPI.ModelKeyProvider: Identifiable {
    public var id: String { provider }

    public init(provider: String, label: String) {
        self.init(provider: provider, label: label, configured: false)
    }

    public var maskedKeyDisplay: String? {
        guard configured, let last4, !last4.isEmpty else { return nil }
        return "••••\(last4)"
    }
}

public struct ModelKeysResponse: Sendable, Codable, Hashable {
    public var byokRequired: Bool
    public var providers: [ModelKeyProvider]

    enum CodingKeys: String, CodingKey {
        case byokRequired = "byok_required"
        case providers
    }

    public init(byokRequired: Bool = false, providers: [ModelKeyProvider] = []) {
        self.byokRequired = byokRequired
        self.providers = providers
    }

    public init(from decoder: any Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        self.byokRequired = try container.decodeIfPresent(Bool.self, forKey: .byokRequired) ?? false
        self.providers = try container.decodeIfPresent([ModelKeyProvider].self, forKey: .providers) ?? []
    }
}

public struct ModelSettingsResponse: Sendable, Codable, Hashable {
    public var modelProfiles: ModelProfileCatalog
    public var selection: ModelSelection
    public var revision: Int
    public var recommendations: [String: ModelSelection]

    enum CodingKeys: String, CodingKey {
        case modelProfiles = "model_profiles"
        case selection
        case revision
        case recommendations
    }
}

public struct SaveModelSettingsRequest: Sendable, Codable, Hashable {
    public var selection: ModelSelection
    public var expectedRevision: Int

    enum CodingKeys: String, CodingKey {
        case selection
        case expectedRevision = "expected_revision"
    }

    public init(selection: ModelSelection, expectedRevision: Int) {
        self.selection = selection
        self.expectedRevision = expectedRevision
    }
}

// MARK: - Service API Keys (BYOK External Services, e.g. Exa)

public typealias ServiceKeyStatus = OpenAPI.ServiceKeyStatus
public typealias SaveServiceKeyRequest = OpenAPI.SetServiceKeyRequest

extension OpenAPI.ServiceKeyStatus: Identifiable {
    public var id: String { service }

    public var displayLabel: String {
        let trimmed = label.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty { return trimmed }
        if service == "exa" { return "Exa Web Search" }
        return service.capitalized
    }

    public var maskedKeyDisplay: String? {
        guard configured else { return nil }
        if let maskedKey, !maskedKey.isEmpty { return maskedKey }
        if let last4, !last4.isEmpty { return "••••\(last4)" }
        return "Configured"
    }
}

public struct ServiceKeysResponse: Sendable, Codable, Hashable {
    public var allowBYOK: Bool?
    public var services: [ServiceKeyStatus]

    enum CodingKeys: String, CodingKey {
        case allowBYOK = "allow_byok"
        case services
    }

    public init(allowBYOK: Bool? = nil, services: [ServiceKeyStatus] = []) {
        self.allowBYOK = allowBYOK
        self.services = services
    }

    public init(from decoder: any Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        self.allowBYOK = try container.decodeIfPresent(Bool.self, forKey: .allowBYOK)
        self.services = try container.decodeIfPresent([ServiceKeyStatus].self, forKey: .services) ?? []
    }
}
