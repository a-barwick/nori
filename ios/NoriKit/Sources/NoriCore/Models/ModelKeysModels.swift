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
