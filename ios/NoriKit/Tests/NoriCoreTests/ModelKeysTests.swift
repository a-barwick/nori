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

    @Test("Decodes ServiceKeysResponse and ServiceKeyStatus fields cleanly")
    func decodesServiceKeysContract() throws {
        let json = """
        {
          "services": [
            {
              "service": "exa",
              "label": "Exa Web Search",
              "configured": true,
              "last4": "77aa",
              "masked_key": "••••77aa",
              "source": "byok",
              "updated_at": "2026-11-28T01:00:00Z",
              "validated_at": "2026-11-28T01:00:00Z"
            },
            {
              "service": "custom",
              "label": "",
              "configured": false,
              "source": "none"
            }
          ]
        }
        """

        let decoded = try JSONDecoder().decode(ServiceKeysResponse.self, from: Data(json.utf8))
        #expect(decoded.services.count == 2)

        let exa = decoded.services[0]
        #expect(exa.id == "exa")
        #expect(exa.displayLabel == "Exa Web Search")
        #expect(exa.configured == true)
        #expect(exa.maskedKey == "••••77aa")
        #expect(exa.maskedKeyDisplay == "••••77aa")
        #expect(exa.source == "byok")
        #expect(exa.updatedAt == "2026-11-28T01:00:00Z")
        #expect(exa.validatedAt == "2026-11-28T01:00:00Z")

        let custom = decoded.services[1]
        #expect(custom.displayLabel == "Custom")
        #expect(custom.maskedKeyDisplay == nil)

        let emptyDecoded = try JSONDecoder().decode(ServiceKeysResponse.self, from: Data("{}".utf8))
        #expect(emptyDecoded.services.isEmpty)
        #expect(emptyDecoded.allowBYOK == nil)

        let saveReq = SaveServiceKeyRequest(apiKey: "exa_live_77aa")
        let encoded = try JSONEncoder().encode(saveReq)
        let obj = try JSONSerialization.jsonObject(with: encoded) as? [String: Any]
        #expect(obj?["api_key"] as? String == "exa_live_77aa")
    }

    @Test("NoriStore manages BYOK service keys and respects trial allow_byok gating")
    @MainActor
    func storeServiceKeyLifecycleAndTrialGating() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "byok@example.com")

        await mock.setCapabilities(
            Capabilities(
                entitlement: CapabilitiesEntitlement(
                    tier: "trial",
                    status: "active",
                    allowBYOK: false,
                    suspended: false,
                    creditRemainingUSD: 4.5,
                    creditGrantUSD: 5.0,
                    storageUsedBytes: 0,
                    storageLimitBytes: 104_857_600,
                    maxRoutines: 3,
                    maxConcurrentTasks: 2,
                    maxDesktopWs: 1,
                    maxCheckpoints: 1
                ),
                serviceKeys: CapabilitiesServiceKeys(exaConfigured: false, platformExaAvailable: true)
            )
        )
        await store.refreshCapabilities()
        #expect(store.allowBYOK == false)
        #expect(store.needsModelKeySetup == false)

        await mock.setNextSaveServiceKeyError(
            NoriAPIError(message: "", statusCode: 402, code: "byok_not_allowed")
        )
        do {
            _ = try await store.saveServiceKey(service: "exa", apiKey: "exa_trial_1234")
            Issue.record("Expected 402 byok_not_allowed")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 402)
            #expect(err.isBYOKNotAllowed)
            #expect(err.message.contains("paid plans"))
        }

        await mock.setNextSaveServiceKeyError(nil)
        await mock.setCapabilities(
            Capabilities(
                entitlement: CapabilitiesEntitlement(
                    tier: "standard",
                    status: "active",
                    allowBYOK: true,
                    suspended: false,
                    creditRemainingUSD: 10.0,
                    creditGrantUSD: 10.0,
                    storageUsedBytes: 0,
                    storageLimitBytes: 524_288_000,
                    maxRoutines: 15,
                    maxConcurrentTasks: 4,
                    maxDesktopWs: 1,
                    maxCheckpoints: 3
                ),
                serviceKeys: CapabilitiesServiceKeys(exaConfigured: false, platformExaAvailable: true)
            )
        )
        await store.refreshCapabilities()
        #expect(store.allowBYOK == true)

        try await store.refreshServiceKeysThrowing()
        #expect(store.serviceKeys.services.first?.configured == false)

        let saved = try await store.saveServiceKey(service: "exa", apiKey: "exa_live_9876")
        #expect(saved.configured == true)
        #expect(saved.maskedKeyDisplay == "••••9876")
        #expect(saved.source == "byok")
        #expect(store.capabilities?.serviceKeys?.exaConfigured == true)

        let deleted = try await store.deleteServiceKey(service: "exa")
        #expect(deleted.configured == false)
        #expect(deleted.source == "platform")
        #expect(store.capabilities?.serviceKeys?.exaConfigured == false)
    }

    @Test("Structured 402/403 billing errors enter calm read-only state without signing out")
    @MainActor
    func billingAccessErrorsEnterReadOnlyMode() async throws {
        let exhausted = NoriAPIError(message: "", statusCode: 402, code: "credit_exhausted")
        #expect(exhausted.isCreditExhausted)
        #expect(exhausted.isBillingAccessBlock)
        #expect(!exhausted.isAuthError)
        #expect(exhausted.message.contains("hosted credits are used up"))

        let inactive = NoriAPIError(message: "Request failed (402).", statusCode: 402, code: "subscription_inactive")
        #expect(inactive.isSubscriptionInactive)
        #expect(inactive.isBillingAccessBlock)
        #expect(inactive.message.contains("subscription is inactive"))

        let byokBlocked = NoriAPIError(message: "", statusCode: 402, code: "byok_not_allowed")
        #expect(byokBlocked.isBYOKNotAllowed)
        #expect(!byokBlocked.isBillingAccessBlock)
        #expect(byokBlocked.message.contains("paid plans"))

        let suspended = NoriAPIError(message: "Account suspended by admin.", statusCode: 403, code: "account_suspended")
        #expect(suspended.isAccountSuspended)
        #expect(suspended.isBillingAccessBlock)
        #expect(!suspended.isAuthError)
        #expect(suspended.message == "Account suspended by admin.")

        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "readonly@example.com")

        // 402 credit_exhausted puts store in read-only mode, cleared when user adds a BYOK key
        await mock.setNextSendTurnError(exhausted)
        let sentWhileExhausted = await store.sendMessage("Hello")
        #expect(sentWhileExhausted == false)
        #expect(store.authState.isSignedIn)
        #expect(store.isChatReadOnly)
        #expect(store.activeBillingAccessBlock?.code == .creditExhausted)

        _ = try await store.saveModelKey(provider: "vercel_ai_gateway", apiKey: "vck_live_a1b2")
        #expect(store.activeBillingAccessBlock == nil)
        #expect(!store.isChatReadOnly)

        // 403 account_suspended keeps user signed in with read-only banner instead of forcing generic sign-out
        await mock.setNextSendTurnError(suspended)
        let sentWhileSuspended = await store.sendMessage("Still there?")
        #expect(sentWhileSuspended == false)
        #expect(store.authState.isSignedIn)
        #expect(store.isChatReadOnly)
        #expect(store.activeBillingAccessBlock?.code == .accountSuspended)
        #expect(store.activeBillingAccessBlock?.message == "Account suspended by admin.")
    }
}
