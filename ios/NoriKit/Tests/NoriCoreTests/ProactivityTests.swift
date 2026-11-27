import Foundation
import Testing
@testable import NoriCore

final class InMemoryPushPersistence: PushDevicePersistence, @unchecked Sendable {
    private let lock = NSLock()
    private var registeredID: String?
    private let deviceID: String

    init(deviceID: String = "test-device-1", registeredID: String? = nil) {
        self.deviceID = deviceID
        self.registeredID = registeredID
    }

    func loadRegisteredDeviceID() -> String? {
        lock.withLock { registeredID }
    }

    func saveRegisteredDeviceID(_ id: String?) {
        lock.withLock { registeredID = id }
    }

    func loadDeviceIdentifier() -> String {
        deviceID
    }
}

@Suite("Proactivity v1 Models, Edited Approvals & APNs Lifecycle")
struct ProactivityTests {

    @Test("PartBrief decodes greeting, meta, items, and Day 1 empty-items fallback cleanly")
    func partBriefDecoding() throws {
        let fullJSON = """
        {
          "id": "brief-day30",
          "type": "brief",
          "greeting": "Good morning, Austin",
          "meta": "Fri Nov 27 · 58°F",
          "fallback": "Good morning, Austin — 3 items today.",
          "items": [
            { "title": "Design Sync with Sam", "trailing": "2:00 PM" },
            { "title": "Reply to Maya on Q4 pricing", "interaction_id": "int-maya-1", "action_label": "Review" },
            { "title": "PR #418 has 2 approvals", "trailing": "CI running" }
          ]
        }
        """
        let fullPart = try JSONDecoder().decode(MessagePart.self, from: Data(fullJSON.utf8))
        #expect(fullPart.isBrief)
        #expect(fullPart.greeting == "Good morning, Austin")
        #expect(fullPart.meta == "Fri Nov 27 · 58°F")
        #expect(fullPart.items?.count == 3)
        #expect(fullPart.items?[0].title == "Design Sync with Sam")
        #expect(fullPart.items?[0].trailing == "2:00 PM")
        #expect(fullPart.items?[1].interactionID == "int-maya-1")
        #expect(fullPart.items?[1].actionLabel == "Review")

        let day1JSON = """
        {
          "type": "brief",
          "greeting": "Good morning, Austin",
          "meta": "Fri Nov 27 · 58°F",
          "text": "I'll check in like this around 8 AM each morning. What's on your mind today?",
          "fallback": "Good morning, Austin.",
          "items": []
        }
        """
        let day1Part = try JSONDecoder().decode(MessagePart.self, from: Data(day1JSON.utf8))
        #expect(day1Part.isBrief)
        #expect(day1Part.items?.isEmpty == true)
        #expect(day1Part.text == "I'll check in like this around 8 AM each morning. What's on your mind today?")
    }

    @Test("InteractionAnswer.approve encodes edited_fields and NEVER sets remember_rule true when edited")
    func editedApprovalEncoding() throws {
        let edited = EditedApprovalFields(subject: "Re: Q4 Pricing", body: "Updated reply body.")
        // Even if rememberRule: true is passed alongside editedFields, remember_rule must NOT be emitted.
        let answer = InteractionAnswer.approve(
            actionDigest: "sha256:deadbeef",
            rememberRule: true,
            editedFields: edited
        )
        let data = try JSONEncoder().encode(answer)
        let obj = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])

        #expect(obj["kind"] as? String == "approve")
        #expect(obj["action_digest"] as? String == "sha256:deadbeef")
        #expect(obj["remember_rule"] == nil)

        let fields = try #require(obj["edited_fields"] as? [String: Any])
        #expect(fields["subject"] as? String == "Re: Q4 Pricing")
        #expect(fields["body"] as? String == "Updated reply body.")
    }

    @Test("NoriStore.approveInteraction forwards edited_fields and forces rememberRule false")
    @MainActor
    func storeApproveInteractionWithEdits() async {
        let transport = MockNoriTransport()
        let store = NoriStore(transport: transport)
        let interaction = Interaction(
            id: "int-edit-1",
            revision: 2,
            kind: .approval,
            status: .pending,
            prompt: "Send email",
            payload: .approval(
                ApprovalPayload(
                    summary: "Draft body",
                    effect: .sensitiveSend,
                    account: ApprovalAccount(connectionID: "conn-1", label: "Gmail")
                )
            ),
            actionDigest: "sha256:9999"
        )
        store.interactions = [interaction]

        let outcome = await store.approveInteraction(
            interaction,
            rememberRule: true,
            editedFields: EditedApprovalFields(body: "Edited body")
        )
        if case .resolved = outcome {
            // expected
        } else {
            Issue.record("Expected resolved outcome")
        }

        let recorded = await transport.recordedAnswers
        #expect(recorded.count == 1)
        #expect(recorded[0] == .approve(
            actionDigest: "sha256:9999",
            rememberRule: false,
            editedFields: EditedApprovalFields(body: "Edited body")
        ))
    }

    @Test("ProactivityPreferences and QuietHours.enabled round-trip cleanly inside Preferences")
    func proactivityPreferencesRoundTrip() throws {
        let prefs = Preferences(
            revision: 4,
            timezone: "America/Los_Angeles",
            locale: "en-US",
            quietHours: QuietHours(enabled: true, start: "21:00", end: "07:30"),
            proactivity: ProactivityPreferences(
                pushPolicy: .includeBrief,
                morningBriefTime: "08:00",
                morningBriefEnabled: true,
                dailyProactiveBudgetUSD: 1.50
            )
        )

        let data = try JSONEncoder().encode(prefs)
        let decoded = try JSONDecoder().decode(Preferences.self, from: data)

        #expect(decoded.quietHours?.enabled == true)
        #expect(decoded.quietHours?.start == "21:00")
        #expect(decoded.quietHours?.end == "07:30")
        #expect(decoded.proactivity?.pushPolicy == .includeBrief)
        #expect(decoded.proactivity?.morningBriefTime == "08:00")
        #expect(decoded.proactivity?.morningBriefEnabled == true)
        #expect(decoded.proactivity?.dailyProactiveBudgetUSD == 1.50)

        #expect(PushPolicy.urgentAndApprovals.next == .includeBrief)
        #expect(PushPolicy.includeBrief.next == .quietOnly)
        #expect(PushPolicy.quietOnly.next == .urgentAndApprovals)
    }

    @Test("APNs token registration on sign-in, deduplication, and deletion on sign-out")
    @MainActor
    func apnsTokenLifecycle() async {
        let transport = MockNoriTransport()
        let persistence = InMemoryPushPersistence(deviceID: "ios-sim-42")
        let pushManager = NoriPushManager(persistence: persistence, environment: .sandbox)
        let store = NoriStore(
            transport: transport,
            authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()),
            pushManager: pushManager
        )

        let tokenBytes = Data([0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02])
        #expect(NoriPushManager.hexString(from: tokenBytes) == "deadbeef0102")

        // Token arrives before sign-in: cached locally, not sent yet
        await pushManager.updateDeviceToken(tokenBytes, transport: transport, isSignedIn: false)
        #expect(await transport.registeredPushRequests.isEmpty)
        #expect(pushManager.latestTokenHex == "deadbeef0102")

        // User signs in -> pending token registers with POST /v1/devices/push
        store.authState = .signedIn(email: "austin@example.com")
        await pushManager.registerPendingTokenIfSignedIn(transport: transport)

        let requests = await transport.registeredPushRequests
        #expect(requests.count == 1)
        #expect(requests[0].token == "deadbeef0102")
        #expect(requests[0].deviceID == "ios-sim-42")
        #expect(requests[0].environment == "sandbox")
        #expect(pushManager.registeredDeviceID == "push-dev-1")
        #expect(persistence.loadRegisteredDeviceID() == "push-dev-1")

        // Calling again with same token is a no-op
        await pushManager.updateDeviceToken(tokenBytes, transport: transport, isSignedIn: true)
        #expect(await transport.registeredPushRequests.count == 1)

        // Sign out -> DELETE /v1/devices/push/{id} and local registered ID cleared
        await store.signOut()
        let deleted = await transport.deletedPushDeviceIDs
        #expect(deleted == ["push-dev-1"])
        #expect(pushManager.registeredDeviceID == nil)
        #expect(persistence.loadRegisteredDeviceID() == nil)
    }

    @Test("Push payload extraction and deep-link routing open Review sheet for interaction_id")
    @MainActor
    func pushTapDeepLinkRouting() {
        let store = NoriStore(transport: MockNoriTransport())

        // Top-level interaction_id
        store.handlePushNotificationTap(userInfo: ["interaction_id": "int-push-1"])
        #expect(store.pendingDeepLink == .reviewInteraction("int-push-1"))

        // Nested under nori dict
        store.handlePushNotificationTap(userInfo: ["nori": ["interaction_id": "int-push-2"]])
        #expect(store.pendingDeepLink == .reviewInteraction("int-push-2"))

        // No interaction_id falls back to chat tab
        store.handlePushNotificationTap(userInfo: ["aps": ["alert": "Good morning"]])
        #expect(store.pendingDeepLink == .tab(.chat))

        // URL deep-link parsing
        let reviewURL = URL(string: "nori://review/int-url-9")!
        #expect(NoriDeepLink.parse(reviewURL) == .reviewInteraction("int-url-9"))
        let chatQueryURL = URL(string: "nori://chat?interaction_id=int-url-10")!
        #expect(NoriDeepLink.parse(chatQueryURL) == .reviewInteraction("int-url-10"))
    }
}
