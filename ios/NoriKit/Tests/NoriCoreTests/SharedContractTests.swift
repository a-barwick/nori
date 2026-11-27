import Foundation
import Testing
@testable import NoriCore

private let repoRoot = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().deletingLastPathComponent()

private let apiDirectory = repoRoot.appendingPathComponent("api", isDirectory: true)

// MARK: - OpenAPI 3.1 Schema Synthesizer & Validator (Zero External Dependencies)

private final class OpenAPISpec {
    let schemas: [String: [String: Any]]
    let openAPIVersion: String

    init(url: URL) throws {
        let data = try Data(contentsOf: url)
        guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let version = root["openapi"] as? String,
              let components = root["components"] as? [String: Any],
              let rawSchemas = components["schemas"] as? [String: [String: Any]]
        else {
            throw NSError(domain: "OpenAPISpec", code: 1, userInfo: [NSLocalizedDescriptionKey: "Invalid api/openapi.json"])
        }
        self.openAPIVersion = version
        self.schemas = rawSchemas
    }

    enum SynthesisMode {
        case minimal // required fields only; omits all optional properties
        case full    // required + all optional properties populated
    }

    func resolve(_ schema: [String: Any]) -> [String: Any] {
        if let ref = schema["$ref"] as? String, ref.hasPrefix("#/components/schemas/") {
            let name = String(ref.dropFirst("#/components/schemas/".count))
            if let target = schemas[name] {
                return resolve(target)
            }
        }
        return schema
    }

    /// Collects the effective property dictionary and required set for a resolved object schema (including allOf).
    func objectProperties(of schema: [String: Any]) -> (properties: [String: [String: Any]], required: Set<String>) {
        let resolved = resolve(schema)
        var props: [String: [String: Any]] = [:]
        var req: Set<String> = []

        if let allOf = resolved["allOf"] as? [[String: Any]] {
            for sub in allOf {
                let subResult = objectProperties(of: sub)
                for (k, v) in subResult.properties { props[k] = v }
                req.formUnion(subResult.required)
            }
        }
        if let directProps = resolved["properties"] as? [String: [String: Any]] {
            for (k, v) in directProps { props[k] = v }
        }
        if let directReq = resolved["required"] as? [String] {
            req.formUnion(directReq)
        }
        return (props, req)
    }

    /// Returns all concrete variants of a schema (expanding top-level `oneOf`).
    func variants(of schemaName: String) -> [[String: Any]] {
        guard let raw = schemas[schemaName] else { return [] }
        let resolved = resolve(raw)
        if let oneOf = resolved["oneOf"] as? [[String: Any]] {
            return oneOf
        }
        return [resolved]
    }

    func synthesize(schema: [String: Any], mode: SynthesisMode, depth: Int = 0) -> Any {
        let resolved = resolve(schema)
        if depth > 8 {
            return NSNull()
        }

        if let oneOf = resolved["oneOf"] as? [[String: Any]], let first = oneOf.first {
            return synthesize(schema: first, mode: mode, depth: depth + 1)
        }

        if resolved["allOf"] != nil || resolved["properties"] != nil || schemaType(resolved) == "object" {
            let (props, req) = objectProperties(of: resolved)
            var out: [String: Any] = [:]
            if props.isEmpty {
                if case .full = mode, let addProps = resolved["additionalProperties"] as? [String: Any] {
                    out["sample_key"] = synthesize(schema: addProps, mode: mode, depth: depth + 1)
                }
                return out
            }
            for (key, propSchema) in props {
                if mode == .minimal && !req.contains(key) {
                    continue
                }
                out[key] = synthesize(schema: propSchema, mode: mode, depth: depth + 1)
            }
            return out
        }

        if let enumValues = resolved["enum"] as? [Any], let first = enumValues.first {
            return first
        }

        switch schemaType(resolved) {
        case "string":
            if let format = resolved["format"] as? String {
                switch format {
                case "date-time": return "2026-11-20T15:00:00Z"
                case "uuid": return "11111111-1111-4111-8111-111111111111"
                case "uri": return "https://example.com/auth"
                default: break
                }
            }
            if let pattern = resolved["pattern"] as? String {
                if pattern.contains("\\d{4}") { return "2026-11-20" }
                if pattern.contains(":[0-5]") { return "09:00" }
            }
            return "sample"
        case "integer":
            return (resolved["minimum"] as? Int) ?? 1
        case "number":
            return (resolved["minimum"] as? Double) ?? 1.5
        case "boolean":
            return true
        case "array":
            if mode == .minimal {
                return [Any]()
            }
            if let items = resolved["items"] as? [String: Any] {
                return [synthesize(schema: items, mode: mode, depth: depth + 1)]
            }
            return [Any]()
        default:
            // Unconstrained JSON schema `{}` (e.g. `current` or `resource`)
            return ["key": "value"]
        }
    }

    private func schemaType(_ schema: [String: Any]) -> String? {
        if let single = schema["type"] as? String {
            return single
        }
        if let multi = schema["type"] as? [String] {
            return multi.first { $0 != "null" }
        }
        return nil
    }

    func allowedKeys(for schema: [String: Any]) -> (all: Set<String>, required: Set<String>) {
        let (props, req) = objectProperties(of: schema)
        return (Set(props.keys), req)
    }
}

private func assertDecodableConformance<T: Decodable>(
    _ type: T.Type,
    schema schemaName: String,
    spec: OpenAPISpec,
    sourceLocation: SourceLocation = #_sourceLocation
) throws {
    let variants = spec.variants(of: schemaName)
    #expect(!variants.isEmpty, "Schema \(schemaName) missing from api/openapi.json", sourceLocation: sourceLocation)
    let decoder = JSONDecoder()

    for (index, variant) in variants.enumerated() {
        for mode in [OpenAPISpec.SynthesisMode.minimal, .full] {
            let synthesized = spec.synthesize(schema: variant, mode: mode)
            let data = try JSONSerialization.data(withJSONObject: synthesized)
            do {
                _ = try decoder.decode(T.self, from: data)
            } catch {
                let raw = String(data: data, encoding: .utf8) ?? "<binary>"
                Issue.record(
                    "\(T.self) failed decoding \(schemaName) variant \(index) (\(mode)): \(error)\nJSON: \(raw)",
                    sourceLocation: sourceLocation
                )
            }
        }
    }
}

private func assertCodableConformance<T: Codable>(
    _ type: T.Type,
    schema schemaName: String,
    spec: OpenAPISpec,
    sourceLocation: SourceLocation = #_sourceLocation
) throws {
    let variants = spec.variants(of: schemaName)
    #expect(!variants.isEmpty, "Schema \(schemaName) missing from api/openapi.json", sourceLocation: sourceLocation)
    let decoder = JSONDecoder()
    let encoder = JSONEncoder()

    for (index, variant) in variants.enumerated() {
        let (allowed, required) = spec.allowedKeys(for: variant)
        for mode in [OpenAPISpec.SynthesisMode.minimal, .full] {
            let synthesized = spec.synthesize(schema: variant, mode: mode)
            let data = try JSONSerialization.data(withJSONObject: synthesized)
            let decoded: T
            do {
                decoded = try decoder.decode(T.self, from: data)
            } catch {
                let raw = String(data: data, encoding: .utf8) ?? "<binary>"
                Issue.record(
                    "\(T.self) failed decoding \(schemaName) variant \(index) (\(mode)): \(error)\nJSON: \(raw)",
                    sourceLocation: sourceLocation
                )
                continue
            }
            let reencoded = try encoder.encode(decoded)
            guard let object = try JSONSerialization.jsonObject(with: reencoded) as? [String: Any] else {
                Issue.record("\(T.self) re-encoded non-object JSON for \(schemaName)", sourceLocation: sourceLocation)
                continue
            }
            let emittedKeys = Set(object.keys)
            let unknownKeys = emittedKeys.subtracting(allowed)
            #expect(
                unknownKeys.isEmpty,
                "\(T.self) emitted non-schema keys \(unknownKeys.sorted()) for \(schemaName) variant \(index)",
                sourceLocation: sourceLocation
            )
            let missingRequired = required.subtracting(emittedKeys)
            #expect(
                missingRequired.isEmpty,
                "\(T.self) omitted required schema keys \(missingRequired.sorted()) for \(schemaName) variant \(index)",
                sourceLocation: sourceLocation
            )
        }
    }
}

// MARK: - Tests

@Test("NoriCore Codable models conform to api/openapi.json schemas with optional fields omitted and populated")
func openAPISchemaConformance() throws {
    let spec = try OpenAPISpec(url: apiDirectory.appendingPathComponent("openapi.json"))
    #expect(spec.openAPIVersion == "3.1.0")

    // Core capabilities, models & preferences
    try assertCodableConformance(Capabilities.self, schema: "Capabilities", spec: spec)
    try assertCodableConformance(ModelProfile.self, schema: "ModelProfile", spec: spec)
    try assertCodableConformance(ModelProfileCatalog.self, schema: "ModelProfileCatalog", spec: spec)
    try assertCodableConformance(ModelSelection.self, schema: "ModelSelection", spec: spec)
    try assertCodableConformance(Preferences.self, schema: "Preferences", spec: spec)
    try assertCodableConformance(AssistantSummary.self, schema: "AssistantSummary", spec: spec)
    try assertCodableConformance(APIErrorPayload.self, schema: "APIError", spec: spec)

    // Automations (including all 4 Trigger variants: once, recurring, event_relative, watch)
    try assertCodableConformance(AutomationTrigger.self, schema: "Trigger", spec: spec)
    try assertCodableConformance(Automation.self, schema: "Automation", spec: spec)
    try assertCodableConformance(AutomationEditable.self, schema: "AutomationEditable", spec: spec)
    try assertCodableConformance(WorkStep.self, schema: "WorkStep", spec: spec)

    // Memory
    try assertCodableConformance(MemoryFact.self, schema: "MemoryFact", spec: spec)
    try assertCodableConformance(MemoryLogEntry.self, schema: "MemoryLogEntry", spec: spec)
    try assertCodableConformance(MemoryLog.self, schema: "MemoryLog", spec: spec)
    try assertCodableConformance(WorkingMemory.self, schema: "WorkingMemory", spec: spec)
    try assertCodableConformance(MemorySearchHit.self, schema: "MemorySearchHit", spec: spec)
    try assertCodableConformance(MemorySearchResponse.self, schema: "MemorySearchResponse", spec: spec)

    // Work & Operations
    try assertCodableConformance(WorkItem.self, schema: "WorkItem", spec: spec)
    try assertCodableConformance(OperationProjection.self, schema: "OperationProjection", spec: spec)
    try assertCodableConformance(WorkDetail.self, schema: "WorkDetail", spec: spec)

    // Interactions & Permissions (all 4 Interaction variants + ApprovalPayload with card)
    try assertCodableConformance(Interaction.self, schema: "Interaction", spec: spec)
    try assertCodableConformance(ApprovalPayload.self, schema: "ApprovalPayload", spec: spec)
    try assertCodableConformance(PermissionRule.self, schema: "PermissionRule", spec: spec)

    // Connections & MCP (including optional ConnectionResource.selected/writable)
    try assertCodableConformance(Connection.self, schema: "Connection", spec: spec)
    try assertCodableConformance(ConnectionResource.self, schema: "Resource", spec: spec)
    try assertCodableConformance(MCPServer.self, schema: "MCPServer", spec: spec)
    try assertCodableConformance(MCPServerTool.self, schema: "MCPServerTool", spec: spec)

    // Library & Search
    try assertCodableConformance(LibraryItem.self, schema: "LibraryItem", spec: spec)
    try assertCodableConformance(SearchResponse.self, schema: "SearchResponse", spec: spec)

    // Settings: BYOK Model Keys, Model Settings, Home Connectors, API Costs
    try assertCodableConformance(ModelKeyProvider.self, schema: "ModelKeyProvider", spec: spec)
    try assertCodableConformance(ModelKeysResponse.self, schema: "ModelKeysResponse", spec: spec)
    try assertCodableConformance(ModelSettingsResponse.self, schema: "ModelSettings", spec: spec)
    try assertCodableConformance(HomeConnector.self, schema: "HomeConnector", spec: spec)
    try assertCodableConformance(HomeConnectorsResponse.self, schema: "HomeConnectors", spec: spec)
    try assertCodableConformance(HomePairing.self, schema: "HomePairing", spec: spec)
    try assertCodableConformance(HomePairingRequest.self, schema: "HomePairingRequest", spec: spec)
    try assertCodableConformance(HomeConnectorTest.self, schema: "HomeConnectorTestResponse", spec: spec)
    try assertCodableConformance(SaveModelKeyRequest.self, schema: "SetModelKeyRequest", spec: spec)
    try assertCodableConformance(SaveModelSettingsRequest.self, schema: "SaveModelSettingsRequest", spec: spec)
    try assertDecodableConformance(APICostResponse.self, schema: "CostReport", spec: spec)

    // Session & Conversation Models
    try assertDecodableConformance(Attachment.self, schema: "Attachment", spec: spec)
    try assertDecodableConformance(Artifact.self, schema: "Artifact", spec: spec)
    try assertDecodableConformance(RunEvent.self, schema: "RunEvent", spec: spec)
    try assertDecodableConformance(TaskQuestion.self, schema: "TaskQuestion", spec: spec)
    try assertDecodableConformance(TaskHold.self, schema: "TaskHold", spec: spec)
    try assertDecodableConformance(SavedResult.self, schema: "SavedResult", spec: spec)
    try assertDecodableConformance(Run.self, schema: "Run", spec: spec)
    try assertDecodableConformance(Message.self, schema: "Message", spec: spec)
    try assertDecodableConformance(Session.self, schema: "Session", spec: spec)
}

@Test("Generated OpenAPI Swift models round-trip against api/openapi.json schemas")
func generatedOpenAPISchemaConformance() throws {
    let spec = try OpenAPISpec(url: apiDirectory.appendingPathComponent("openapi.json"))

    // Conversation & Chat endpoints
    try assertCodableConformance(OpenAPI.ConversationView.self, schema: "ConversationView", spec: spec)
    try assertCodableConformance(OpenAPI.ConversationHistoryResponse.self, schema: "ConversationHistoryResponse", spec: spec)
    try assertCodableConformance(OpenAPI.ConversationMessageRequest.self, schema: "ConversationMessageRequest", spec: spec)
    try assertCodableConformance(OpenAPI.ConversationMessageAccepted.self, schema: "ConversationMessageAccepted", spec: spec)
    try assertCodableConformance(OpenAPI.ConversationOperationsResponse.self, schema: "ConversationOperationsResponse", spec: spec)
    try assertCodableConformance(OpenAPI.ConversationStatus.self, schema: "ConversationStatus", spec: spec)
    try assertCodableConformance(OpenAPI.Session.self, schema: "Session", spec: spec)

    // Mutation requests & account maintenance
    try assertCodableConformance(OpenAPI.AccountDeleteRequest.self, schema: "AccountDeleteRequest", spec: spec)
    try assertCodableConformance(OpenAPI.DataResetRequest.self, schema: "DataResetRequest", spec: spec)
    try assertCodableConformance(OpenAPI.FeedbackRequest.self, schema: "FeedbackRequest", spec: spec)
    try assertCodableConformance(OpenAPI.AutomationChange.self, schema: "AutomationChange", spec: spec)
    try assertCodableConformance(OpenAPI.ConnectionChange.self, schema: "ConnectionChange", spec: spec)
    try assertCodableConformance(OpenAPI.PreferencesChange.self, schema: "PreferencesChange", spec: spec)
    try assertCodableConformance(OpenAPI.PermissionRuleChange.self, schema: "PermissionRuleChange", spec: spec)
    try assertCodableConformance(OpenAPI.MemoryFactChange.self, schema: "MemoryFactChange", spec: spec)
    try assertCodableConformance(OpenAPI.GoogleDrivePickerRequest.self, schema: "GoogleDrivePickerRequest", spec: spec)
    try assertCodableConformance(OpenAPI.WorkActionRequest.self, schema: "WorkActionRequest", spec: spec)
    try assertCodableConformance(OpenAPI.InteractionResponse.self, schema: "InteractionResponse", spec: spec)

    // Discriminated oneOf unions & allOf merged structs
    try assertCodableConformance(OpenAPI.MutationReceipt.self, schema: "MutationReceipt", spec: spec)
    try assertCodableConformance(OpenAPI.CalendarTime.self, schema: "CalendarTime", spec: spec)
    try assertCodableConformance(OpenAPI.SearchJump.self, schema: "SearchJump", spec: spec)
    try assertCodableConformance(OpenAPI.Trigger.self, schema: "Trigger", spec: spec)
    try assertCodableConformance(OpenAPI.Interaction.self, schema: "Interaction", spec: spec)
    try assertCodableConformance(OpenAPI.LibraryItem.self, schema: "LibraryItem", spec: spec)
    try assertCodableConformance(OpenAPI.ModelCost.self, schema: "ModelCost", spec: spec)
    try assertCodableConformance(OpenAPI.Attendee.self, schema: "Attendee", spec: spec)

    // Generated path helpers percent-encode parameters cleanly
    #expect(OpenAPI.Path.capabilities == "/v1/capabilities")
    #expect(OpenAPI.Path.workActions(taskID: "t/1") == "/v1/work/t%2F1/actions")
    #expect(OpenAPI.Path.memoryLogsActions(date: "2026-11-26", entryID: "e 1") == "/v1/memory/logs/2026-11-26/e%201/actions")
}

private struct ModelSelectionFixture: Decodable {
    struct Case: Decodable {
        let name: String
        let catalogAvailable: Bool
        let selection: ModelSelection
        let expected: ModelSelection
    }

    let catalog: ModelProfileCatalog
    let cases: [Case]
}

@Test("Normalize model selections the same way as web")
func sharedModelSelections() throws {
    let url = apiDirectory.appendingPathComponent("test-vectors/model-selections.json")
    let bundle = try JSONDecoder().decode(ModelSelectionFixture.self, from: Data(contentsOf: url))
    for item in bundle.cases {
        #expect(item.selection.normalized(using: item.catalogAvailable ? bundle.catalog : nil) == item.expected, Comment(rawValue: item.name))
    }
}
