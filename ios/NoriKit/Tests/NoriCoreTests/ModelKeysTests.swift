import Foundation
import Testing
@testable import NoriCore

@Suite("Model API Keys (BYOK) Contract & Store")
struct ModelKeysTests {
    @Test("Decodes GET /v1/settings/model-keys response contract and masked key display")
    func decodesModelKeysResponseContract() throws {
        let json = """
        {
          "byok_required": false,
          "providers": [
            {
              "provider": "vercel_ai_gateway",
              "label": "Vercel AI Gateway",
              "configured": true,
              "last4": "a1b2",
              "updated_at": "2026-11-25T17:00:00Z",
              "validated_at": "2026-11-25T17:00:00Z"
            },
            {
              "provider": "openai",
              "label": "OpenAI",
              "configured": false
            }
          ]
        }
        """

        let decoded = try JSONDecoder().decode(ModelKeysResponse.self, from: Data(json.utf8))
        #expect(decoded.byokRequired == false)
        #expect(decoded.providers.count == 2)

        let vercel = decoded.providers[0]
        #expect(vercel.id == "vercel_ai_gateway")
        #expect(vercel.provider == "vercel_ai_gateway")
        #expect(vercel.label == "Vercel AI Gateway")
        #expect(vercel.configured == true)
        #expect(vercel.last4 == "a1b2")
        #expect(vercel.updatedAt == "2026-11-25T17:00:00Z")
        #expect(vercel.validatedAt == "2026-11-25T17:00:00Z")
        #expect(vercel.maskedKeyDisplay == "••••a1b2")

        let openai = decoded.providers[1]
        #expect(openai.id == "openai")
        #expect(openai.configured == false)
        #expect(openai.last4 == nil)
        #expect(openai.updatedAt == nil)
        #expect(openai.validatedAt == nil)
        #expect(openai.maskedKeyDisplay == nil)
    }

    @Test("Decodes ModelKeysResponse gracefully when fields are omitted or null")
    func decodesSparseModelKeysResponse() throws {
        let nullProvidersJSON = #"{"byok_required": true, "providers": null}"#
        let decoded = try JSONDecoder().decode(ModelKeysResponse.self, from: Data(nullProvidersJSON.utf8))
        #expect(decoded.byokRequired == true)
        #expect(decoded.providers.isEmpty)

        let emptyJSON = #"{}"#
        let emptyDecoded = try JSONDecoder().decode(ModelKeysResponse.self, from: Data(emptyJSON.utf8))
        #expect(emptyDecoded.byokRequired == false)
        #expect(emptyDecoded.providers.isEmpty)
    }

    @Test("Encodes SaveModelKeyRequest with api_key and round-trips ModelKeysResponse")
    func encodesSaveRequestAndRoundTrips() throws {
        let encoder = JSONEncoder()
        let decoder = JSONDecoder()

        let request = SaveModelKeyRequest(apiKey: "vck_live_secret_9876")
        let encodedRequest = try encoder.encode(request)
        let rawObj = try JSONSerialization.jsonObject(with: encodedRequest) as? [String: Any]
        #expect(rawObj?["api_key"] as? String == "vck_live_secret_9876")
        #expect(try decoder.decode(SaveModelKeyRequest.self, from: encodedRequest) == request)

        let response = ModelKeysResponse(
            byokRequired: true,
            providers: [
                ModelKeyProvider(
                    provider: "vercel_ai_gateway",
                    label: "Vercel AI Gateway",
                    configured: true,
                    last4: "9876",
                    updatedAt: "2026-11-25T17:00:00Z",
                    validatedAt: "2026-11-25T17:00:00Z"
                )
            ]
        )
        let encodedResponse = try encoder.encode(response)
        let decodedResponse = try decoder.decode(ModelKeysResponse.self, from: encodedResponse)
        #expect(decodedResponse == response)
    }

    @Test("NoriEndpointRouter builds scoped model-keys endpoints")
    func routerBuildsModelKeysEndpoints() throws {
        let router = NoriEndpointRouter(baseURL: URL(string: "https://api.nori.test")!)
        let listURL = try router.scopedURL(path: "/v1/settings/model-keys", userID: "usr-7")
        #expect(listURL.absoluteString == "https://api.nori.test/users/usr-7/v1/settings/model-keys")

        let providerURL = try router.scopedURL(path: "/v1/settings/model-keys/vercel_ai_gateway", userID: "usr-7")
        #expect(providerURL.absoluteString == "https://api.nori.test/users/usr-7/v1/settings/model-keys/vercel_ai_gateway")
    }

    @Test("NoriStore tracks BYOK setup state across refresh, save, error, and delete")
    @MainActor
    func storeModelKeyLifecycle() async throws {
        let mock = MockModelKeysTransport()
        let store = NoriStore(transport: mock)

        await store.refreshModelKeys()
        #expect(store.modelKeys.byokRequired == true)
        #expect(store.hasConfiguredModelKey == false)
        #expect(store.needsModelKeySetup == true)
        #expect(store.modelKeys.providers.count == 1)

        // Upstream rejection (422) propagates error and leaves provider unconfigured.
        await mock.setNextSaveError(NoriAPIError(message: "Invalid API key rejected by provider.", statusCode: 422))
        do {
            _ = try await store.saveModelKey(provider: "vercel_ai_gateway", apiKey: "bad_key")
            Issue.record("Expected saveModelKey to throw on 422")
        } catch let apiErr as NoriAPIError {
            #expect(apiErr.statusCode == 422)
            #expect(apiErr.message == "Invalid API key rejected by provider.")
        }
        #expect(store.needsModelKeySetup == true)

        // Valid key save updates state and clears needsModelKeySetup.
        await mock.setNextSaveError(nil)
        let saved = try await store.saveModelKey(provider: "vercel_ai_gateway", apiKey: "vck_live_a1b2")
        #expect(saved.configured == true)
        #expect(saved.last4 == "a1b2")
        #expect(store.hasConfiguredModelKey == true)
        #expect(store.needsModelKeySetup == false)
        #expect(store.modelKeys.providers.first?.maskedKeyDisplay == "••••a1b2")

        // Delete reverts provider to unconfigured and restores needsModelKeySetup.
        let deleted = try await store.deleteModelKey(provider: "vercel_ai_gateway")
        #expect(deleted.configured == false)
        #expect(deleted.last4 == nil)
        #expect(store.hasConfiguredModelKey == false)
        #expect(store.needsModelKeySetup == true)
    }
}

private actor MockModelKeysTransport: NoriTransport {
    private var response = ModelKeysResponse(
        byokRequired: true,
        providers: [
            ModelKeyProvider(
                provider: "vercel_ai_gateway",
                label: "Vercel AI Gateway",
                configured: false
            )
        ]
    )
    private var nextSaveError: Error?

    func setNextSaveError(_ error: Error?) {
        nextSaveError = error
    }

    func fetchModelKeys() async throws -> ModelKeysResponse {
        response
    }

    func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider {
        if let nextSaveError {
            throw nextSaveError
        }
        let updated = ModelKeyProvider(
            provider: provider,
            label: "Vercel AI Gateway",
            configured: true,
            last4: String(apiKey.suffix(4)),
            updatedAt: "2026-11-25T17:00:00Z",
            validatedAt: "2026-11-25T17:00:00Z"
        )
        response.providers = [updated]
        return updated
    }

    func deleteModelKey(provider: String) async throws -> ModelKeyProvider {
        let updated = ModelKeyProvider(
            provider: provider,
            label: "Vercel AI Gateway",
            configured: false,
            last4: nil,
            updatedAt: nil,
            validatedAt: nil
        )
        response.providers = [updated]
        return updated
    }

    func ping() async -> HealthCheckResult { HealthCheckResult(ok: true, status: "ok", latencyMs: 1) }
    func fetchCapabilities() async throws -> Capabilities { Capabilities() }
    func fetchAssistantSummary() async throws -> AssistantSummary { AssistantSummary() }
    func fetchSession(before cursor: String?) async throws -> Session { Session() }
    func fetchFullSnapshot() async throws -> NoriFullSnapshot { NoriFullSnapshot(modelKeys: response) }
    nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        AsyncThrowingStream { $0.finish() }
    }
    func sendTurn(id: String, message: String, attachmentIDs: [String]) async throws -> Session { Session() }
    func retryTurn(turnID: String?, messageID: String?) async throws -> Session { Session() }
    func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse { TaskCommandResponse(accepted: true) }
    func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> NoriCore.Attachment {
        NoriCore.Attachment(id: id, name: filename, mediaType: mediaType, size: data.count)
    }
    func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] { [] }
    func fetchWorkDetail(taskID: String) async throws -> WorkDetail { throw NoriAPIError(message: "unimplemented") }
    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> { throw NoriAPIError(message: "unimplemented") }
    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> { throw NoriAPIError(message: "unimplemented") }
    func fetchInteractions() async throws -> [Interaction] { [] }
    func respondToInteraction(id: String, expectedRevision: Int, answer: InteractionAnswer, requestID: String) async throws -> MutationReceipt<Interaction> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchMCPServers() async throws -> [MCPServer] { [] }
    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] { [] }
    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> { throw NoriAPIError(message: "unimplemented") }
    func performMCPAction(serverID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "unimplemented")
    }
    func setMCPCredential(serverID: String, expectedRevision: Int, secret: String, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "unimplemented")
    }
    func setMCPToolExposure(serverID: String, expectedRevision: Int, toolName: String, exposure: MCPToolExposure, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchConnections() async throws -> [Connection] { [] }
    func performConnectionAction(connectionID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<Connection> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchLibrary() async throws -> [LibraryItem] { [] }
    func fetchLibraryItem(id: String) async throws -> LibraryItem { throw NoriAPIError(message: "unimplemented") }
    func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload { throw NoriAPIError(message: "unimplemented") }
    func search(query: String) async throws -> SearchResponse { SearchResponse(items: []) }
    func reviseLibraryItem(id: String, expectedRevision: Int, instruction: String, requestID: String) async throws -> MutationReceipt<LibraryItem> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchCoreMemories() async throws -> [MemoryFact] { [] }
    func fetchMemoryLogs() async throws -> [MemoryLog] { [] }
    func fetchWorkingMemory() async throws -> WorkingMemory { WorkingMemory() }
    func createMemory(text: String, section: String?, about: String?, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryFact> {
        throw NoriAPIError(message: "unimplemented")
    }
    func performMemoryAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<MemoryActionResource> {
        throw NoriAPIError(message: "unimplemented")
    }
    func performMemoryLogEntryAction(date: String, entryID: String, expectedRevision: Int, action: String, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryLogEntryActionResource> {
        throw NoriAPIError(message: "unimplemented")
    }
    func performWorkingMemoryAction(itemID: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<WorkingMemoryActionResource> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchAutomations() async throws -> [Automation] { [] }
    func fetchAutomation(id: String) async throws -> Automation { throw NoriAPIError(message: "unimplemented") }
    func updateAutomation(id: String, expectedRevision: Int, value: AutomationEditable, requestID: String) async throws -> MutationReceipt<Automation> {
        throw NoriAPIError(message: "unimplemented")
    }
    func performAutomationAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<Automation> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchGoals() async throws -> [Goal] { [] }
    func fetchPermissions() async throws -> [PermissionRule] { [] }
    func createPermissionRule(_ rule: PermissionRuleEditable, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        throw NoriAPIError(message: "unimplemented")
    }
    func revokePermissionRule(id: String, expectedRevision: Int, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchPreferences() async throws -> Preferences { Preferences() }
    func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        MutationReceipt(requestID: requestID, resource: preferences)
    }
}
