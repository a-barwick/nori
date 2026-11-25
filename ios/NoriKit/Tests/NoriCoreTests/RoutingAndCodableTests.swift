import Foundation
import Testing
@testable import NoriCore

@Suite("Routing, Deep Links & Codable Round-Trips")
struct RoutingAndCodableTests {
    @Test("Live Core router prefixes user-scoped routes with /users/{id}/")
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

    @Test("Parses nori:// deep links for tabs, sheets, subroutes, and OAuth")
    func deepLinkParsing() {
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/work")!) == .tab(.work))
        #expect(NoriDeepLink.parse(URL(string: "nori://tab/apps")!) == .tab(.apps))
        #expect(NoriDeepLink.parse(URL(string: "nori://sheet/activity")!) == .activitySheet)
        #expect(NoriDeepLink.parse(URL(string: "nori://memory")!) == .memory)
        #expect(NoriDeepLink.parse(URL(string: "nori://work/task-1")!) == .workDetail("task-1"))
        if case .oauthCallback(let url) = NoriDeepLink.parse(URL(string: "nori://oauth/callback?code=abc&state=123")!) {
            #expect(url.query?.contains("code=abc") == true)
        } else {
            Issue.record("Expected oauthCallback deep link")
        }
    }

    @Test("Codable round-trip for Interaction variants, Session, MCPServer, Memory, Automation, Goal, Preferences")
    func codableRoundTrips() throws {
        let encoder = JSONEncoder()
        let decoder = JSONDecoder()

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
                materialEffects: ["Sends external email"]
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
        let mock = MockApprovalTransport()
        let store = NoriStore(transport: mock)

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

        await store.approveInteraction(pending, rememberRule: true)

        let captured = await mock.recordedAnswers
        #expect(captured == [.approve(actionDigest: "sha256:xyz", rememberRule: true)])
        #expect(await mock.fetchPermissionsCallCount >= 1)
        #expect(store.pendingInteractions.isEmpty)
        #expect(store.permissions.count == 1)
        #expect(store.permissions.first?.id == "rule-remembered")
    }
}

private actor MockApprovalTransport: NoriTransport {
    var recordedAnswers: [InteractionAnswer] = []
    var fetchPermissionsCallCount: Int = 0
    private var savedRule = PermissionRule(
        id: "rule-remembered",
        revision: 1,
        effect: .allow,
        toolIDs: ["gmail.send"],
        connectionIDs: ["conn-1"],
        resourceIDs: ["*"],
        actionClasses: [.sensitiveSend]
    )

    func ping() async -> HealthCheckResult { HealthCheckResult(ok: true, status: "ok", latencyMs: 1) }
    func fetchCapabilities() async throws -> Capabilities { Capabilities() }
    func fetchSession(before cursor: String?) async throws -> Session { Session() }
    func fetchFullSnapshot() async throws -> NoriFullSnapshot {
        NoriFullSnapshot(interactions: [], permissions: [])
    }
    nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        AsyncThrowingStream { $0.finish() }
    }
    func sendTurn(id: String, message: String, attachmentIDs: [String]) async throws -> Session { Session() }
    func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse { TaskCommandResponse(accepted: true) }
    func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> NoriCore.Attachment {
        NoriCore.Attachment(id: id, name: filename, mediaType: mediaType, size: data.count)
    }
    func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] { [] }
    func fetchWorkDetail(taskID: String) async throws -> WorkDetail {
        throw NoriAPIError(message: "Not implemented")
    }
    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> {
        throw NoriAPIError(message: "Not implemented")
    }
    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> {
        throw NoriAPIError(message: "Not implemented")
    }
    func fetchInteractions() async throws -> [Interaction] { [] }
    func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction> {
        recordedAnswers.append(answer)
        let resolved = Interaction(
            id: id,
            revision: expectedRevision + 1,
            kind: .approval,
            status: .resolved,
            prompt: "Send email?",
            payload: .unknown(.null),
            resolution: InteractionResolution(kind: .approve, at: "2026-11-25T04:10:00Z")
        )
        return MutationReceipt(requestID: requestID, resource: resolved)
    }
    func fetchMCPServers() async throws -> [MCPServer] { [] }
    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] { [] }
    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "Not implemented")
    }
    func performMCPAction(serverID: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "Not implemented")
    }
    func setMCPToolExposure(serverID: String, expectedRevision: Int, toolName: String, exposure: MCPToolExposure, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "Not implemented")
    }
    func fetchConnections() async throws -> [Connection] { [] }
    func performConnectionAction(connectionID: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<Connection> {
        throw NoriAPIError(message: "Not implemented")
    }
    func fetchLibrary() async throws -> [LibraryItem] { [] }
    func fetchLibraryItem(id: String) async throws -> LibraryItem {
        throw NoriAPIError(message: "Not implemented")
    }
    func search(query: String) async throws -> SearchResponse { SearchResponse(items: []) }
    func reviseLibraryItem(id: String, expectedRevision: Int, instruction: String, requestID: String) async throws -> MutationReceipt<LibraryItem> {
        throw NoriAPIError(message: "Not implemented")
    }
    func fetchCoreMemories() async throws -> [MemoryFact] { [] }
    func fetchMemoryLogs() async throws -> [MemoryLog] { [] }
    func fetchWorkingMemory() async throws -> WorkingMemory { WorkingMemory() }
    func createMemory(text: String, section: String?, about: String?, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryFact> {
        throw NoriAPIError(message: "Not implemented")
    }
    func performMemoryAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<MemoryFact> {
        throw NoriAPIError(message: "Not implemented")
    }
    func performMemoryLogEntryAction(date: String, entryID: String, expectedRevision: Int, action: String, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryLog> {
        throw NoriAPIError(message: "Not implemented")
    }
    func performWorkingMemoryAction(itemID: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<WorkingMemory> {
        throw NoriAPIError(message: "Not implemented")
    }
    func fetchAutomations() async throws -> [Automation] { [] }
    func performAutomationAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<Automation> {
        throw NoriAPIError(message: "Not implemented")
    }
    func fetchGoals() async throws -> [Goal] { [] }
    func fetchPermissions() async throws -> [PermissionRule] {
        fetchPermissionsCallCount += 1
        return [savedRule]
    }
    func createPermissionRule(_ rule: PermissionRuleEditable, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        let created = PermissionRule(
            id: "rule-created",
            revision: 1,
            effect: rule.effect,
            toolIDs: rule.toolIDs,
            connectionIDs: rule.connectionIDs,
            resourceIDs: rule.resourceIDs,
            actionClasses: rule.actionClasses,
            recipients: rule.recipients
        )
        return MutationReceipt(requestID: requestID, resource: created)
    }
    func revokePermissionRule(id: String, expectedRevision: Int, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        return MutationReceipt(requestID: requestID, resource: savedRule)
    }
    func fetchPreferences() async throws -> Preferences { Preferences() }
    func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        MutationReceipt(requestID: requestID, resource: preferences)
    }
}
