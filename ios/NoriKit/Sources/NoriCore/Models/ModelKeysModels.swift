import Foundation

// MARK: - Model API Keys (BYOK)

public struct ModelKeyProvider: Sendable, Codable, Hashable, Identifiable {
    public var id: String { provider }

    public var provider: String
    public var label: String
    public var configured: Bool
    public var last4: String?
    public var updatedAt: String?
    public var validatedAt: String?

    enum CodingKeys: String, CodingKey {
        case provider
        case label
        case configured
        case last4
        case updatedAt = "updated_at"
        case validatedAt = "validated_at"
    }

    public init(
        provider: String,
        label: String,
        configured: Bool = false,
        last4: String? = nil,
        updatedAt: String? = nil,
        validatedAt: String? = nil
    ) {
        self.provider = provider
        self.label = label
        self.configured = configured
        self.last4 = last4
        self.updatedAt = updatedAt
        self.validatedAt = validatedAt
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

public struct SaveModelKeyRequest: Sendable, Codable, Hashable {
    public var apiKey: String

    enum CodingKeys: String, CodingKey {
        case apiKey = "api_key"
    }

    public init(apiKey: String) {
        self.apiKey = apiKey
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
