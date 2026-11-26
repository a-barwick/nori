import Foundation
import Testing
@testable import NoriCore

@Suite("Account Deletion")
struct AccountDeleteTests {
    @Test("NoriStore.deleteAccount clears local state, token store session, and transitions to signedOut")
    @MainActor
    func deleteAccountSuccessCleansUpAndSignsOut() async throws {
        let transport = MockNoriTransport()
        let email = "account-cleanup-\(UUID().uuidString.lowercased())@example.com"
        let auth = AuthTokenProvider(tokenStore: InMemoryTokenStore())
        await auth.saveSession(AuthSession(
            idToken: "test-id-token",
            refreshToken: "test-refresh-token",
            expiresAt: Date().addingTimeInterval(3600),
            email: email
        ))
        let store = NoriStore(transport: transport, authProvider: auth)
        store.authState = .signedIn(email: email)
        store.session = Session(messages: [Message(id: "m1", role: .user, text: "hello")])
        store.coreMemories = [MemoryFact(id: "mem-1", text: "likes tea")]

        let onboardingKey = "nori.onboarding.done.\(email)"
        let legacyResetKey = "nori.data-reset.completion.\(email)"
        UserDefaults.standard.set(true, forKey: onboardingKey)
        UserDefaults.standard.set(true, forKey: legacyResetKey)
        defer {
            UserDefaults.standard.removeObject(forKey: onboardingKey)
            UserDefaults.standard.removeObject(forKey: legacyResetKey)
        }

        try await store.deleteAccount(confirmation: "  \(email)  ")

        let calls = await transport.deleteCalls
        #expect(calls.count == 1)
        #expect(calls[0].confirmation == email)
        #expect(calls[0].resumePending == false)
        #expect(store.authState == .signedOut)
        #expect(store.session.messages.isEmpty)
        #expect(store.coreMemories.isEmpty)
        #expect(store.accountDeletePending == false)
        #expect(store.accountDeletedNotice == "Your account has been deleted.")
        #expect(UserDefaults.standard.object(forKey: onboardingKey) == nil)
        #expect(UserDefaults.standard.object(forKey: legacyResetKey) == nil)
        #expect(await auth.loadSession() == nil)
    }

    @Test("NoriStore.deleteAccount enters pending state on account_delete_pending and succeeds on retry")
    @MainActor
    func deleteAccountPartialFailureAndRetryRecovery() async throws {
        let transport = MockNoriTransport()
        await transport.setNextDeleteError(
            NoriAPIError(
                message: "Your account deletion is incomplete. Finish deleting your account to continue.",
                statusCode: 502,
                code: "account_delete_pending"
            )
        )
        let auth = AuthTokenProvider(tokenStore: InMemoryTokenStore())
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
        let transport = MockNoriTransport()
        await transport.setNextDeleteError(
            NoriAPIError(
                message: "The legacy owner account cannot be deleted from self-service settings.",
                statusCode: 403,
                code: "account_delete_forbidden"
            )
        )
        let auth = AuthTokenProvider(tokenStore: InMemoryTokenStore())
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
        let transport = MockNoriTransport()
        await transport.setNextDeleteError(
            NoriAPIError(
                message: "Enter your account email to confirm deletion.",
                statusCode: 400,
                code: "validation_failed"
            )
        )
        let auth = AuthTokenProvider(tokenStore: InMemoryTokenStore())
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
