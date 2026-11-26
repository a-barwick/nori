import Foundation
import Testing
@testable import NoriCore

@Suite("Routing, Deep Links & Codable Round-Trips")
struct RoutingAndCodableTests {
    @Test("Live Core router prefixes user-scoped routes with /users/{id}/ and encodes path segments")
    func directCoreRouting() throws {
        let router = NoriEndpointRouter(baseURL: NoriConfig.serverURL)

        let readyURL = try router.unscopedURL(path: "/readyz")
        #expect(readyURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/readyz")

        let resolveURL = try router.unscopedURL(path: "/api/users/resolve")
        #expect(resolveURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/api/users/resolve")

        let snapshotURL = try router.scopedURL(path: "/v1/snapshot", userID: "usr-42")
        #expect(snapshotURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/users/usr-42/v1/snapshot")

        let eventsURL = try router.scopedURL(
            path: "/v1/events",
            userID: "usr-42",
            queryItems: [URLQueryItem(name: "after", value: "12")]
        )
        #expect(eventsURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/users/usr-42/v1/events?after=12")

        let turnURL = try router.scopedURL(path: "/api/turn", userID: "usr-42")
        #expect(turnURL.absoluteString == "https://nori-core-8273801370.us-central1.run.app/users/usr-42/api/turn")

        let encodedSegment = NoriEndpointRouter.encodePathSegment("a/b?c#d e")
        #expect(encodedSegment == "a%2Fb%3Fc%23d%20e")
        let encodedURL = try router.scopedURL(path: "/v1/library/\(encodedSegment)/file", userID: "usr/1")
        #expect(encodedURL.absoluteString.contains("/users/usr%2F1/v1/library/a%2Fb%3Fc%23d%20e/file"))
    }

    @Test("Google OAuth PKCE builds reversed redirect scheme and validates state")
    func googleOAuthPKCE() throws {
        let clientID = "123456-abc.apps.googleusercontent.com"
        #expect(NoriConfig.reversedClientIDScheme(for: clientID) == "com.googleusercontent.apps.123456-abc")
        #expect(NoriConfig.googleRedirectURI(for: clientID) == "com.googleusercontent.apps.123456-abc:/oauth2redirect")

        let pkce = GoogleOAuthPKCE(clientID: clientID)
        let authURL = try pkce.authorizationURL()
        #expect(authURL.host == "accounts.google.com")
        #expect(authURL.query?.contains("code_challenge_method=S256") == true)

        let goodCallback = URL(string: "com.googleusercontent.apps.123456-abc:/oauth2redirect?code=xyz&state=\(pkce.state)")!
        #expect(try pkce.extractAuthorizationCode(from: goodCallback) == "xyz")
    }

    @Test("NoriTab exposes the 3-tab Chat, Work, and More structure")
    func threeTabStructure() {
        #expect(NoriTab.allCases == [.chat, .work, .more])
        #expect(NoriTab.chat.id == "chat")
        #expect(NoriTab.work.id == "work")
        #expect(NoriTab.more.id == "more")
        #expect(NoriTab(rawValue: "apps") == nil)
        #expect(NoriTab(rawValue: "library") == nil)
    }

    @Test("Parses nori:// deep links for 3-tab shell, backwards-compatible subroutes, sheets, and OAuth")
    func deepLinkParsing() {
        #expect(NoriDeepLink.parse(URL(string: "nori://chat")!) == .tab(.chat))
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/chat")!) == .tab(.chat))
        #expect(NoriDeepLink.parse(URL(string: "nori://work")!) == .tab(.work))
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/work")!) == .tab(.work))
        #expect(NoriDeepLink.parse(URL(string: "nori://work/task-1")!) == .workDetail("task-1"))
        #expect(NoriDeepLink.parse(URL(string: "nori://more")!) == .tab(.more))
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/more")!) == .tab(.more))

        #expect(NoriDeepLink.parse(URL(string: "nori://apps")!) == .apps)
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/apps")!) == .apps)
        #expect(NoriDeepLink.parse(URL(string: "nori://memory")!) == .memory)
        #expect(NoriDeepLink.parse(URL(string: "nori://library")!) == .library)
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/library")!) == .library)
        #expect(NoriDeepLink.parse(URL(string: "nori://library/doc-123")!) == .document("doc-123"))
        #expect(NoriDeepLink.parse(URL(string: "nori://routines")!) == .routines)
        #expect(NoriDeepLink.parse(URL(string: "nori://automations")!) == .routines)
        #expect(NoriDeepLink.parse(URL(string: "nori://goals")!) == .goals)
        #expect(NoriDeepLink.parse(URL(string: "nori://settings")!) == .settings)

        #expect(NoriDeepLink.parse(URL(string: "nori://activity")!) == .activitySheet)
        #expect(NoriDeepLink.parse(URL(string: "nori://sheet/activity")!) == .activitySheet)
        if case .oauthCallback(let url) = NoriDeepLink.parse(URL(string: "nori://oauth/callback?code=abc&state=123")!) {
            #expect(url.query?.contains("code=abc") == true)
        } else {
            Issue.record("Expected oauthCallback deep link")
        }
        if case .oauthCallback(let url) = NoriDeepLink.parse(URL(string: "nori://callback/oauth?code=def")!) {
            #expect(url.query?.contains("code=def") == true)
        } else {
            Issue.record("Expected callback oauthCallback deep link")
        }
        #expect(NoriDeepLink.parse(URL(string: "https://example.com/work")!) == nil)
    }

    @Test("Codable round-trip for Interaction variants, AutomationTrigger, ConnectionResource, and Preferences")
    func codableRoundTrips() throws {
        let encoder = JSONEncoder()
        let decoder = JSONDecoder()

        let card = CardPart(
            kind: "mail.send",
            resource: CardResourceRef(kind: "draft", id: "draft-1", revision: 1),
            actions: ["approve", "decline"],
            fallback: "Email summary",
            display: "inline"
        )
        let approval = Interaction(
            id: "int-a",
            revision: 2,
            kind: .approval,
            taskID: "t-1",
            status: .pending,
            prompt: "Send email?",
            payload: .approval(ApprovalPayload(
                summary: "Email body",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "c-1", label: "alex@example.com"),
                recipients: ["procurement@acme.com"],
                materialEffects: ["Sends external email"],
                card: card
            )),
            actionDigest: "sha256:abc"
        )
        let encodedApproval = try encoder.encode(approval)
        let decodedApproval = try decoder.decode(Interaction.self, from: encodedApproval)
        #expect(decodedApproval == approval)

        let question = Interaction(
            id: "int-q",
            revision: 1,
            kind: .question,
            status: .pending,
            prompt: "Pick calendar",
            payload: .question(QuestionPayload(input: .choice, options: [QuestionOption(id: "w", label: "Work")]))
        )
        let decodedQuestion = try decoder.decode(Interaction.self, from: try encoder.encode(question))
        #expect(decodedQuestion == question)

        let accountSel = Interaction(
            id: "int-acc",
            revision: 1,
            kind: .accountSelection,
            status: .pending,
            prompt: "Choose account",
            payload: .accountSelection(AccountSelectionPayload(purpose: "calendar.create", eligibleConnectionIDs: ["c-1", "c-2"], rememberAsDefault: true))
        )
        let decodedAcc = try decoder.decode(Interaction.self, from: try encoder.encode(accountSel))
        #expect(decodedAcc == accountSel)

        let signIn = Interaction(
            id: "int-si",
            revision: 1,
            kind: .signIn,
            status: .pending,
            prompt: "Sign in to Linear",
            payload: .signIn(SignInPayload(auth: .oauth, integrationID: "mcp", connectionID: "mcp-linear", authorizeURL: "https://linear.app/oauth"))
        )
        let decodedSignIn = try decoder.decode(Interaction.self, from: try encoder.encode(signIn))
        #expect(decodedSignIn == signIn)

        let receipt = MutationReceipt(
            requestID: "req-1",
            accepted: true,
            status: .applied,
            resource: Preferences(revision: 4, timezone: "America/Los_Angeles", budgets: BudgetPreferences(taskUSD: 1.5, monthlyUSD: 30.0))
        )
        let decodedReceipt = try decoder.decode(MutationReceipt<Preferences>.self, from: try encoder.encode(receipt))
        #expect(decodedReceipt == receipt)
    }

    @Test("AutomationTrigger round-trips event_relative and watch triggers with ResourceRef and without display_cadence")
    func automationTriggerRoundTrip() throws {
        let encoder = JSONEncoder()
        let decoder = JSONDecoder()

        let eventTrigger = AutomationTrigger(
            kind: "event_relative",
            event: ResourceRef(connectionID: "conn-cal", kind: "calendar_event", providerID: "evt-9"),
            offsetSeconds: -900
        )
        let eventData = try encoder.encode(eventTrigger)
        let rawEventObj = try JSONSerialization.jsonObject(with: eventData)
        let eventJSON = try #require(rawEventObj as? [String: Any])
        #expect(eventJSON["display_cadence"] == nil)
        #expect(try decoder.decode(AutomationTrigger.self, from: eventData) == eventTrigger)

        let watchTrigger = AutomationTrigger(
            kind: "watch",
            source: ResourceRef(connectionID: "conn-mail", kind: "mailbox", providerID: "inbox")
        )
        let watchData = try encoder.encode(watchTrigger)
        #expect(try decoder.decode(AutomationTrigger.self, from: watchData) == watchTrigger)
        #expect(watchTrigger.summaryLabel == "Watching for changes")
    }

    @Test("ConnectionResource decodes when selected/writable are omitted and preserves provider_revision")
    func connectionResourceOptionalFlagsAndRevision() throws {
        let json = """
        {
          "ref": {
            "connection_id": "conn-drive",
            "kind": "drive_file",
            "provider_id": "file-42",
            "provider_revision": "rev-abc"
          },
          "title": "Q4 Roadmap",
          "kind": "drive_file"
        }
        """
        let decoded = try JSONDecoder().decode(ConnectionResource.self, from: Data(json.utf8))
        #expect(decoded.ref.providerRevision == "rev-abc")
        #expect(decoded.selected == nil)
        #expect(decoded.writable == nil)
        #expect(decoded.isSelected == false)
        #expect(decoded.isWritable == false)
    }

    @Test("Parity display labels align with Web (Archived, Routine)")
    func parityDisplayLabels() {
        #expect(MemoryProvenance.summaryEvicted.displayLabel == "Archived")
        #expect(WorkOrigin(kind: .automation).displayLabel == "Routine")
        #expect(SearchHit.Kind.automation.displayLabel == "Routine")
        let op = OperationProjection(id: "op-1", name: "mail.send", authoritySource: "automation")
        #expect(op.displayAuthorityLabel == "a routine")
    }

    @Test("Codable round-trip for .ask PermissionRule and PermissionRuleEditable")
    func permissionRuleAskRoundTrip() throws {
        let encoder = JSONEncoder()
        let decoder = JSONDecoder()

        let askRule = PermissionRule(
            id: "rule-ask-1",
            revision: 3,
            effect: .ask,
            toolIDs: ["gmail.send"],
            connectionIDs: ["conn-1"],
            resourceIDs: ["*"],
            actionClasses: [.sensitiveSend],
            recipients: ["*"],
            title: "Always confirm email sends",
            summary: "Ask first before sending with gmail.send on conn-1."
        )
        let encoded = try encoder.encode(askRule)
        let jsonObject = try JSONSerialization.jsonObject(with: encoded) as? [String: Any]
        #expect(jsonObject?["effect"] as? String == "ask")

        let decoded = try decoder.decode(PermissionRule.self, from: encoded)
        #expect(decoded == askRule)
        #expect(decoded.effect == .ask)
        #expect(PermissionRule.RuleEffect.allow.displayLabel == "Allow automatically")
        #expect(PermissionRule.RuleEffect.ask.displayLabel == "Ask first")
        #expect(PermissionRule.RuleEffect.deny.displayLabel == "Never allow")

        let editable = PermissionRuleEditable(
            effect: .ask,
            toolIDs: ["calendar.delete_event"],
            connectionIDs: ["*"],
            resourceIDs: ["*"],
            actionClasses: [.reversibleWrite]
        )
        let decodedEditable = try decoder.decode(PermissionRuleEditable.self, from: try encoder.encode(editable))
        #expect(decodedEditable == editable)
    }

    @Test("Codable round-trip for ApprovalPayload with warning and tool_id")
    func approvalPayloadWarningAndToolIDRoundTrip() throws {
        let encoder = JSONEncoder()
        let decoder = JSONDecoder()

        let payload = ApprovalPayload(
            summary: "Send Q3 update to external list",
            toolID: "gmail.send",
            warning: "External recipient outside your organization.",
            effect: .sensitiveSend,
            account: ApprovalAccount(connectionID: "conn-mail", label: "alex@example.com"),
            resources: [ResourceRef(connectionID: "conn-mail", kind: "message", providerID: "draft-9")],
            recipients: ["partner@external.org"],
            materialEffects: ["Sends an email that cannot be undone"]
        )
        let interaction = Interaction(
            id: "int-warn-1",
            revision: 1,
            kind: .approval,
            taskID: "task-7",
            status: .pending,
            prompt: "Send email?",
            payload: .approval(payload),
            actionDigest: "sha256:deadbeef",
            expiresAt: "2026-11-25T04:20:00Z"
        )

        let encoded = try encoder.encode(interaction)
        let rawObj = try JSONSerialization.jsonObject(with: encoded) as? [String: Any]
        let rawPayload = rawObj?["payload"] as? [String: Any]
        #expect(rawPayload?["tool_id"] as? String == "gmail.send")
        #expect(rawPayload?["warning"] as? String == "External recipient outside your organization.")

        let decoded = try decoder.decode(Interaction.self, from: encoded)
        #expect(decoded == interaction)
        if case .approval(let decodedPayload) = decoded.payload {
            #expect(decodedPayload.toolID == "gmail.send")
            #expect(decodedPayload.warning == "External recipient outside your organization.")
        } else {
            Issue.record("Expected approval payload")
        }
    }

    @Test("InteractionAnswer.approve encodes remember_rule only when true")
    func interactionAnswerApproveRememberRuleEncoding() throws {
        let encoder = JSONEncoder()

        let approveAlways = InteractionAnswer.approve(actionDigest: "sha256:1234", rememberRule: true)
        #expect(approveAlways.payloadDictionary["kind"] == .string("approve"))
        #expect(approveAlways.payloadDictionary["action_digest"] == .string("sha256:1234"))
        #expect(approveAlways.payloadDictionary["remember_rule"] == .bool(true))

        let alwaysData = try encoder.encode(approveAlways)
        let alwaysJSON = try JSONSerialization.jsonObject(with: alwaysData) as? [String: Any]
        #expect(alwaysJSON?["kind"] as? String == "approve")
        #expect(alwaysJSON?["action_digest"] as? String == "sha256:1234")
        #expect(alwaysJSON?["remember_rule"] as? Bool == true)

        let approveOnceExplicit = InteractionAnswer.approve(actionDigest: "sha256:1234", rememberRule: false)
        #expect(approveOnceExplicit.payloadDictionary["remember_rule"] == nil)

        let onceData = try encoder.encode(approveOnceExplicit)
        let onceJSON = try JSONSerialization.jsonObject(with: onceData) as? [String: Any]
        #expect(onceJSON?["kind"] as? String == "approve")
        #expect(onceJSON?["action_digest"] as? String == "sha256:1234")
        #expect(onceJSON?["remember_rule"] == nil)

        let approveDefault = InteractionAnswer.approve(actionDigest: "sha256:1234")
        #expect(approveDefault == approveOnceExplicit)
        #expect(approveDefault.payloadDictionary["remember_rule"] == nil)
    }

    @Test("NoriStore.approveInteraction sends remember_rule and refreshes permissions when true")
    @MainActor
    func storeApproveInteractionRememberRuleRefreshesPermissions() async {
        let mock = MockNoriTransport()
        let savedRule = PermissionRule(
            id: "rule-remembered",
            revision: 1,
            effect: .allow,
            toolIDs: ["gmail.send"],
            connectionIDs: ["conn-1"],
            resourceIDs: ["*"],
            actionClasses: [.sensitiveSend]
        )
        await mock.setPermissions([savedRule])
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))

        let pending = Interaction(
            id: "int-1",
            revision: 1,
            kind: .approval,
            taskID: "task-1",
            status: .pending,
            prompt: "Send email?",
            payload: .approval(ApprovalPayload(
                summary: "Send email",
                toolID: "gmail.send",
                warning: "External recipient",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "conn-1", label: "alex@example.com")
            )),
            actionDigest: "sha256:xyz"
        )
        store.interactions = [pending]

        let outcome = await store.approveInteraction(pending, rememberRule: true)
        if case .resolved = outcome {} else {
            Issue.record("Expected resolved outcome")
        }

        let captured = await mock.recordedAnswers
        #expect(captured == [.approve(actionDigest: "sha256:xyz", rememberRule: true)])
        #expect(await mock.fetchPermissionsCallCount >= 1)
        #expect(await mock.fetchFullSnapshotCallCount == 0)
        #expect(store.pendingInteractions.isEmpty)
        #expect(store.permissions.count == 1)
        #expect(store.permissions.first?.id == "rule-remembered")
    }
}
