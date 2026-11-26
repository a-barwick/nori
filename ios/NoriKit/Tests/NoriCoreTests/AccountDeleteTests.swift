import Foundation
import Testing
@testable import NoriCore

@Suite("Account Deletion")
struct AccountDeleteTests {
    @Test("NoriStore.deleteAccount clears local state, Keychain session, and transitions to signedOut")
    @MainActor
    func deleteAccountSuccessCleansUpAndSignsOut() async throws {
        let transport = MockAccountDeleteTransport()
        let auth = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.test.acct-del.\(UUID().uuidString)"))
        await auth.saveSession(AuthSession(
            idToken: "test-id-token",
            refreshToken: "test-refresh-token",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alice@example.com"
        ))
        let store = NoriStore(transport: transport, authProvider: auth)
        store.authState = .signedIn(email: "alice@example.com")
        store.session = Session(messages: [Message(id: "m1", role: .user, text: "hello")])
        store.coreMemories = [MemoryFact(id: "mem-1", text: "likes tea")]

        let onboardingKey = "nori.onboarding.done.alice@example.com"
        UserDefaults.standard.set(true, forKey: onboardingKey)
        defer { UserDefaults.standard.removeObject(forKey: onboardingKey) }

        try await store.deleteAccount(confirmation: "  alice@example.com  ")

        let calls = await transport.deleteCalls
        #expect(calls.count == 1)
        #expect(calls[0].confirmation == "alice@example.com")
        #expect(calls[0].resumePending == false)
        #expect(store.authState == .signedOut)
        #expect(store.session.messages.isEmpty)
        #expect(store.coreMemories.isEmpty)
        #expect(store.accountDeletePending == false)
        #expect(store.accountDeletedNotice == "Your account has been deleted.")
        #expect(UserDefaults.standard.object(forKey: onboardingKey) == nil)
        #expect(await auth.loadSession() == nil)
    }

    @Test("NoriStore.deleteAccount enters pending state on account_delete_pending and succeeds on retry")
    @MainActor
    func deleteAccountPartialFailureAndRetryRecovery() async throws {
        let transport = MockAccountDeleteTransport()
        await transport.setNextError(
            NoriAPIError(
                message: "Your account deletion is incomplete. Finish deleting your account to continue.",
                statusCode: 502,
                code: "account_delete_pending"
            )
        )
        let auth = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.test.acct-del.\(UUID().uuidString)"))
        await auth.saveSession(AuthSession(
            idToken: "test-id-token",
            refreshToken: "test-refresh-token",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alice@example.com"
        ))
        let store = NoriStore(transport: transport, authProvider: auth)
        store.authState = .signedIn(email: "alice@example.com")

        do {
            try await store.deleteAccount(confirmation: "alice@example.com")
            Issue.record("Expected account_delete_pending error")
        } catch let error as NoriAPIError {
            #expect(error.code == "account_delete_pending")
            #expect(error.statusCode == 502)
        }

        #expect(store.accountDeletePending == true)
        #expect(store.authState == .signedIn(email: "alice@example.com"))
        #expect(await auth.loadSession() != nil)

        // Retry with resumePending = true
        try await store.deleteAccount(confirmation: "alice@example.com", resumePending: true)

        let calls = await transport.deleteCalls
        #expect(calls.count == 2)
        #expect(calls[1].resumePending == true)
        #expect(store.accountDeletePending == false)
        #expect(store.authState == .signedOut)
        #expect(await auth.loadSession() == nil)
    }

    @Test("NoriStore.deleteAccount surfaces legacy owner 403 error without signing out")
    @MainActor
    func deleteAccountLegacyOwnerForbidden() async throws {
        let transport = MockAccountDeleteTransport()
        await transport.setNextError(
            NoriAPIError(
                message: "The legacy owner account cannot be deleted from self-service settings.",
                statusCode: 403,
                code: "account_delete_forbidden"
            )
        )
        let auth = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.test.acct-del.\(UUID().uuidString)"))
        await auth.saveSession(AuthSession(
            idToken: "owner-id-token",
            refreshToken: nil,
            expiresAt: Date().addingTimeInterval(3600),
            email: "owner@example.com"
        ))
        let store = NoriStore(transport: transport, authProvider: auth)
        store.authState = .signedIn(email: "owner@example.com")

        do {
            try await store.deleteAccount(confirmation: "owner@example.com")
            Issue.record("Expected account_delete_forbidden error")
        } catch let error as NoriAPIError {
            #expect(error.statusCode == 403)
            #expect(error.code == "account_delete_forbidden")
        }

        #expect(store.accountDeletePending == false)
        #expect(store.authState == .signedIn(email: "owner@example.com"))
        #expect(await auth.loadSession() != nil)
        await auth.clearSession()
    }

    @Test("NoriStore.deleteAccount surfaces validation_failed on email mismatch")
    @MainActor
    func deleteAccountValidationFailed() async throws {
        let transport = MockAccountDeleteTransport()
        await transport.setNextError(
            NoriAPIError(
                message: "Enter your account email to confirm deletion.",
                statusCode: 400,
                code: "validation_failed"
            )
        )
        let auth = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.test.acct-del.\(UUID().uuidString)"))
        let store = NoriStore(transport: transport, authProvider: auth)
        store.authState = .signedIn(email: "alice@example.com")

        do {
            try await store.deleteAccount(confirmation: "wrong@example.com")
            Issue.record("Expected validation_failed error")
        } catch let error as NoriAPIError {
            #expect(error.statusCode == 400)
            #expect(error.code == "validation_failed")
        }

        #expect(store.accountDeletePending == false)
        #expect(store.authState == .signedIn(email: "alice@example.com"))
    }
}

private actor MockAccountDeleteTransport: NoriTransport {
    struct DeleteCall: Sendable {
        let confirmation: String
        let resumePending: Bool
    }

    private(set) var deleteCalls: [DeleteCall] = []
    private var nextError: Error?

    func setNextError(_ error: Error?) {
        nextError = error
    }

    func deleteAccount(confirmation: String, resumePending: Bool) async throws {
        deleteCalls.append(DeleteCall(confirmation: confirmation, resumePending: resumePending))
        if let error = nextError {
            nextError = nil
            throw error
        }
    }

    func ping() async -> HealthCheckResult { HealthCheckResult(ok: true, status: "ok", latencyMs: 1) }
    func fetchCapabilities() async throws -> Capabilities { Capabilities() }
    func fetchAssistantSummary() async throws -> AssistantSummary { AssistantSummary() }
    func fetchSession(before cursor: String?) async throws -> Session { Session() }
    func fetchFullSnapshot() async throws -> NoriFullSnapshot { NoriFullSnapshot() }
    nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> { AsyncThrowingStream { $0.finish() } }
    func sendTurn(id: String, message: String, attachmentIDs: [String], modelSelection: ModelSelection?) async throws -> Session { Session() }
    func retryTurn(turnID: String?, messageID: String?) async throws -> Session { Session() }
    func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse {
        TaskCommandResponse(command: nil, run: nil, accepted: true, retryable: false, error: nil)
    }
    func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> NoriCore.Attachment {
        NoriCore.Attachment(id: id, name: filename, mediaType: mediaType, size: data.count)
    }
    func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] { [] }
    func fetchWorkDetail(taskID: String) async throws -> WorkDetail { throw NoriAPIError(message: "unused") }
    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> { throw NoriAPIError(message: "unused") }
    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> { throw NoriAPIError(message: "unused") }
    func fetchInteractions() async throws -> [Interaction] { [] }
    func respondToInteraction(id: String, expectedRevision: Int, answer: InteractionAnswer, requestID: String) async throws -> MutationReceipt<Interaction> { throw NoriAPIError(message: "unused") }
    func fetchMCPServers() async throws -> [MCPServer] { [] }
    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] { [] }
    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> { throw NoriAPIError(message: "unused") }
    func performMCPAction(serverID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<MCPServer> { throw NoriAPIError(message: "unused") }
    func setMCPCredential(serverID: String, expectedRevision: Int, secret: String, requestID: String) async throws -> MutationReceipt<MCPServer> { throw NoriAPIError(message: "unused") }
    func setMCPToolExposure(serverID: String, expectedRevision: Int, toolName: String, exposure: MCPToolExposure, requestID: String) async throws -> MutationReceipt<MCPServer> { throw NoriAPIError(message: "unused") }
    func fetchConnections() async throws -> [Connection] { [] }
    func performConnectionAction(connectionID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<Connection> { throw NoriAPIError(message: "unused") }
    func fetchLibrary() async throws -> [LibraryItem] { [] }
    func fetchLibraryItem(id: String) async throws -> LibraryItem { throw NoriAPIError(message: "unused") }
    func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload { throw NoriAPIError(message: "unused") }
    func search(query: String) async throws -> SearchResponse { SearchResponse(items: []) }
    func reviseLibraryItem(id: String, expectedRevision: Int, instruction: String, requestID: String) async throws -> MutationReceipt<LibraryItem> { throw NoriAPIError(message: "unused") }
    func fetchCoreMemories() async throws -> [MemoryFact] { [] }
    func fetchMemoryLogs() async throws -> [MemoryLog] { [] }
    func fetchWorkingMemory() async throws -> WorkingMemory { WorkingMemory(items: []) }
    func createMemory(text: String, section: String?, about: String?, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryFact> { throw NoriAPIError(message: "unused") }
    func performMemoryAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<MemoryActionResource> { throw NoriAPIError(message: "unused") }
    func performMemoryLogEntryAction(date: String, entryID: String, expectedRevision: Int, action: String, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryLogEntryActionResource> { throw NoriAPIError(message: "unused") }
    func performWorkingMemoryAction(itemID: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<WorkingMemoryActionResource> { throw NoriAPIError(message: "unused") }
    func fetchAutomations() async throws -> [Automation] { [] }
    func fetchAutomation(id: String) async throws -> Automation { throw NoriAPIError(message: "unused") }
    func updateAutomation(id: String, expectedRevision: Int, value: AutomationEditable, requestID: String) async throws -> MutationReceipt<Automation> { throw NoriAPIError(message: "unused") }
    func performAutomationAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<Automation> { throw NoriAPIError(message: "unused") }
    func fetchGoals() async throws -> [Goal] { [] }
    func fetchPermissions() async throws -> [PermissionRule] { [] }
    func createPermissionRule(_ rule: PermissionRuleEditable, requestID: String) async throws -> MutationReceipt<PermissionRule> { throw NoriAPIError(message: "unused") }
    func revokePermissionRule(id: String, expectedRevision: Int, requestID: String) async throws -> MutationReceipt<PermissionRule> { throw NoriAPIError(message: "unused") }
    func fetchPreferences() async throws -> Preferences { Preferences() }
    func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> { throw NoriAPIError(message: "unused") }
    func fetchModelSettings() async throws -> ModelSettingsResponse {
        ModelSettingsResponse(modelProfiles: ModelProfileCatalog(), selection: ModelSelection(), revision: 0, recommendations: [:])
    }
    func saveModelSettings(_ request: SaveModelSettingsRequest) async throws -> ModelSettingsResponse {
        throw NoriAPIError(message: "unused")
    }
}
