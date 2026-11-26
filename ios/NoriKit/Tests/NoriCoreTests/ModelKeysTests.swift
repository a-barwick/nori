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

    @Test("Model settings decode and use the account revision on save")
    @MainActor
    func accountModelSettings() async throws {
        let empty = #"{"model_profiles":{"profiles":[]},"selection":{},"revision":0,"recommendations":{}}"#
        let decoded = try JSONDecoder().decode(ModelSettingsResponse.self, from: Data(empty.utf8))
        #expect(decoded.selection.isEmpty)
        #expect(decoded.recommendations.isEmpty)

        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        try await store.refreshModelSettings()
        let selection = ModelSelection(chatProfileID: "anthropic-opus", chatReasoning: "low", taskProfileID: "anthropic-haiku", taskReasoning: "low")
        try await store.saveModelSettings(selection)
        #expect(store.modelSettings?.selection == selection)
        #expect(store.modelSettings?.revision == 1)

        let changedElsewhere = ModelSelection(chatProfileID: "openai-sol", chatReasoning: "high")
        await mock.changeModelsElsewhere(changedElsewhere)
        try await store.refreshModelSettings()
        do {
            try await store.saveModelSettings(selection, expectedRevision: 1)
            Issue.record("Expected a revision conflict")
        } catch let error as NoriAPIError {
            #expect(error.isRevisionConflict)
        }
        #expect(store.modelSettings?.selection == changedElsewhere)
        #expect(store.modelSettings?.revision == 2)

        let encoded = try JSONEncoder().encode(SaveModelSettingsRequest(selection: selection, expectedRevision: 2))
        let object = try JSONSerialization.jsonObject(with: encoded) as? [String: Any]
        #expect(object?["expected_revision"] as? Int == 2)
        #expect((object?["selection"] as? [String: String])?["chat_profile_id"] == "anthropic-opus")
    }

    @Test("Home model selection keeps an explicit policy choice and satisfies Chat setup")
    @MainActor
    func homeModelSelection() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        await mock.changeModelsElsewhere(ModelSelection(chatProfileID: "home-123", homeOnly: true))
        await store.refreshModelKeys()
        #expect(store.hasSelectedHomeChat)
        #expect(!store.needsModelKeySetup)

        let homeOnlyPolicy = ModelSelection(homeOnly: true)
        #expect(!homeOnlyPolicy.isEmpty)

        let versionedSelection = ModelSelection(
            chatProfileID: "openai-sol",
            chatProfileVersion: "v2",
            taskProfileID: "openai-mini",
            taskProfileVersion: "v3",
            homeOnly: false
        )
        let data = try JSONEncoder().encode(SaveModelSettingsRequest(selection: versionedSelection, expectedRevision: 1))
        let body = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let savedSelection = try #require(body["selection"] as? [String: Any])
        #expect(savedSelection["home_only"] as? Bool == false)
        #expect(savedSelection["chat_profile_version"] as? String == "v2")
        #expect(savedSelection["task_profile_version"] as? String == "v3")
        let roundTripped = try JSONDecoder().decode(SaveModelSettingsRequest.self, from: data)
        #expect(roundTripped.selection == versionedSelection)
    }

    @Test("NoriStore tracks BYOK setup state across refresh, save, error, and delete")
    @MainActor
    func storeModelKeyLifecycle() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))

        await store.refreshModelKeys()
        #expect(store.modelKeys.byokRequired == true)
        #expect(store.hasConfiguredModelKey == false)
        #expect(store.needsModelKeySetup == true)
        #expect(store.modelKeys.providers.count == 1)

        await mock.setNextSaveError(NoriAPIError(message: "Invalid API key rejected by provider.", statusCode: 422))
        do {
            _ = try await store.saveModelKey(provider: "vercel_ai_gateway", apiKey: "bad_key")
            Issue.record("Expected saveModelKey to throw on 422")
        } catch let apiErr as NoriAPIError {
            #expect(apiErr.statusCode == 422)
            #expect(apiErr.message == "Invalid API key rejected by provider.")
        }
        #expect(store.needsModelKeySetup == true)

        await mock.setNextSaveError(nil)
        let saved = try await store.saveModelKey(provider: "vercel_ai_gateway", apiKey: "vck_live_a1b2")
        #expect(saved.configured == true)
        #expect(saved.last4 == "a1b2")
        #expect(store.hasConfiguredModelKey == true)
        #expect(store.needsModelKeySetup == false)
        #expect(store.modelKeys.providers.first?.maskedKeyDisplay == "••••a1b2")

        let deleted = try await store.deleteModelKey(provider: "vercel_ai_gateway")
        #expect(deleted.configured == false)
        #expect(deleted.last4 == nil)
        #expect(store.hasConfiguredModelKey == false)
        #expect(store.needsModelKeySetup == true)
    }

    @Test("Reset clears model state and ignores model reads that began earlier")
    @MainActor
    func resetClearsModelState() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(
            transport: mock,
            authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()),
            resetMinimumDuration: .zero
        )
        let email = "model-reset-\(UUID().uuidString)@example.com"
        let legacySelectionKey = "nori.diagnostics.modelSelection.v1." + email.lowercased()
        defer { UserDefaults.standard.removeObject(forKey: legacySelectionKey) }

        store.authState = .signedIn(email: email)
        UserDefaults.standard.set(Data("old choice".utf8), forKey: legacySelectionKey)
        try await store.refreshModelSettings()
        try await store.saveModelSettings(ModelSelection(chatProfileID: "vercel_ai_gateway-opus", taskProfileID: "vercel_ai_gateway-haiku"))
        _ = try await store.saveModelKey(provider: "vercel_ai_gateway", apiKey: "vck_test_a1b2")
        store.modelKeyError = "Old key error"
        #expect(store.hasConfiguredModelKey)
        #expect(store.modelSettings?.selection.chatProfileID == "vercel_ai_gateway-opus")

        await mock.suspendNextModelReads()
        let oldSettings = Task { try? await store.refreshModelSettings() }
        let oldKeys = Task { try? await store.refreshModelKeysThrowing() }
        await mock.waitForSuspendedModelReads()

        try await store.resetUserData(confirmation: "RESET")
        #expect(store.authState == .signedOut)
        #expect(store.modelSettings == nil)
        #expect(store.modelKeyError == nil)
        #expect(!store.hasConfiguredModelKey)
        #expect(UserDefaults.standard.object(forKey: legacySelectionKey) == nil)

        await mock.resumeModelReads()
        await oldSettings.value
        await oldKeys.value
        #expect(store.modelSettings == nil)
        #expect(!store.hasConfiguredModelKey)
        try await store.refreshModelSettings()
        #expect(store.modelSettings?.selection.isEmpty == true)
        #expect(store.modelSettings?.revision == 0)
        await store.signOut()
    }

    @Test("A key save blocked by an interrupted reset opens recovery")
    @MainActor
    func keySaveDetectsPendingReset() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(
            transport: mock,
            authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()),
            resetMinimumDuration: .zero
        )
        let email = "key-reset-\(UUID().uuidString)@example.com"
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer { UserDefaults.standard.removeObject(forKey: markerKey) }
        store.authState = .signedIn(email: email)
        await mock.setPreferences(Preferences(dataResetPending: true))
        await mock.setNextSaveError(NoriAPIError(message: "Finish your reset.", statusCode: 409, code: "data_reset_pending"))
        await #expect(throws: NoriAPIError.self) {
            _ = try await store.saveModelKey(provider: "vercel_ai_gateway", apiKey: "vck_test_a1b2")
        }
        #expect(store.dataResetRecoveryRequired)
        #expect(!store.hasConfiguredModelKey)
        await store.signOut()
    }
}
