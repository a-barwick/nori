import Foundation
import Testing
@testable import NoriCore

/// Wrapper around `MockNoriTransport` that records every transport method call
/// without modifying `MockNoriTransport.swift` or any production files.
private actor BaselineCountingTransport: NoriTransport {
    private let base: MockNoriTransport
    private let mockMCPServers: [MCPServer]

    private(set) var callCounts: [String: Int] = [:]
    private(set) var totalCalls: Int = 0

    init(base: MockNoriTransport = MockNoriTransport(), mcpServers: [MCPServer] = []) {
        self.base = base
        self.mockMCPServers = mcpServers
    }

    func resetCounts() {
        callCounts = [:]
        totalCalls = 0
    }

    func snapshotCounts() -> (total: Int, byMethod: [String: Int]) {
        (totalCalls, callCounts)
    }

    private func record(_ method: String) {
        totalCalls += 1
        callCounts[method, default: 0] += 1
    }

    // MARK: - NoriTransport

    func ping() async -> HealthCheckResult {
        record("ping")
        return await base.ping()
    }

    func fetchCapabilities() async throws -> Capabilities {
        record("fetchCapabilities")
        return try await base.fetchCapabilities()
    }

    func fetchAssistantSummary() async throws -> AssistantSummary {
        record("fetchAssistantSummary")
        return try await base.fetchAssistantSummary()
    }

    func fetchSession(before cursor: String?) async throws -> Session {
        record("fetchSession")
        return try await base.fetchSession(before: cursor)
    }

    func fetchFullSnapshot() async throws -> NoriFullSnapshot {
        record("fetchFullSnapshot")
        return try await base.fetchFullSnapshot()
    }

    nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        base.eventStream(after: position)
    }

    func sendTurn(id: String, message: String, attachmentIDs: [String], modelSelection: ModelSelection?) async throws -> Session {
        record("sendTurn")
        return try await base.sendTurn(id: id, message: message, attachmentIDs: attachmentIDs, modelSelection: modelSelection)
    }

    func retryTurn(turnID: String?, messageID: String?) async throws -> Session {
        record("retryTurn")
        return try await base.retryTurn(turnID: turnID, messageID: messageID)
    }

    func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse {
        record("sendTaskCommand")
        return try await base.sendTaskCommand(command)
    }

    func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> NoriCore.Attachment {
        record("uploadFile")
        return try await base.uploadFile(id: id, filename: filename, mediaType: mediaType, data: data)
    }

    func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] {
        record("fetchWorkItems")
        return try await base.fetchWorkItems(bucket: bucket)
    }

    func fetchWorkDetail(taskID: String) async throws -> WorkDetail {
        record("fetchWorkDetail")
        return try await base.fetchWorkDetail(taskID: taskID)
    }

    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> {
        record("stopWork")
        return try await base.stopWork(taskID: taskID, requestID: requestID)
    }

    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> {
        record("continueWork")
        return try await base.continueWork(taskID: taskID, budgetUSD: budgetUSD, requestID: requestID)
    }

    func fetchInteractions() async throws -> [Interaction] {
        record("fetchInteractions")
        return try await base.fetchInteractions()
    }

    func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction> {
        record("respondToInteraction")
        return try await base.respondToInteraction(
            id: id,
            expectedRevision: expectedRevision,
            answer: answer,
            requestID: requestID
        )
    }

    func fetchMCPServers() async throws -> [MCPServer] {
        record("fetchMCPServers")
        _ = try await base.fetchMCPServers()
        return mockMCPServers
    }

    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] {
        record("fetchMCPTools")
        return try await base.fetchMCPTools(serverID: serverID)
    }

    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> {
        record("createMCPServer")
        return try await base.createMCPServer(input: input, requestID: requestID)
    }

    func performMCPAction(
        serverID: String,
        expectedRevision: Int,
        action: String,
        returnTo: String?,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        record("performMCPAction")
        return try await base.performMCPAction(
            serverID: serverID,
            expectedRevision: expectedRevision,
            action: action,
            returnTo: returnTo,
            requestID: requestID
        )
    }

    func setMCPCredential(
        serverID: String,
        expectedRevision: Int,
        secret: String,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        record("setMCPCredential")
        return try await base.setMCPCredential(
            serverID: serverID,
            expectedRevision: expectedRevision,
            secret: secret,
            requestID: requestID
        )
    }

    func setMCPToolExposure(
        serverID: String,
        expectedRevision: Int,
        toolName: String,
        exposure: MCPToolExposure,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        record("setMCPToolExposure")
        return try await base.setMCPToolExposure(
            serverID: serverID,
            expectedRevision: expectedRevision,
            toolName: toolName,
            exposure: exposure,
            requestID: requestID
        )
    }

    func fetchConnections() async throws -> [Connection] {
        record("fetchConnections")
        return try await base.fetchConnections()
    }

    func fetchIntegrations() async throws -> [IntegrationSummary] {
        record("fetchIntegrations")
        return try await base.fetchIntegrations()
    }

    func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        returnTo: String?,
        requestID: String
    ) async throws -> MutationReceipt<Connection> {
        record("performConnectionAction")
        return try await base.performConnectionAction(
            connectionID: connectionID,
            expectedRevision: expectedRevision,
            action: action,
            returnTo: returnTo,
            requestID: requestID
        )
    }

    func fetchLibrary() async throws -> [LibraryItem] {
        record("fetchLibrary")
        return try await base.fetchLibrary()
    }

    func fetchLibraryItem(id: String) async throws -> LibraryItem {
        record("fetchLibraryItem")
        return try await base.fetchLibraryItem(id: id)
    }

    func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload {
        record("fetchLibraryItemFile")
        return try await base.fetchLibraryItemFile(id: id, format: format)
    }

    func search(query: String) async throws -> SearchResponse {
        record("search")
        return try await base.search(query: query)
    }

    func reviseLibraryItem(
        id: String,
        expectedRevision: Int,
        instruction: String,
        requestID: String
    ) async throws -> MutationReceipt<LibraryItem> {
        record("reviseLibraryItem")
        return try await base.reviseLibraryItem(
            id: id,
            expectedRevision: expectedRevision,
            instruction: instruction,
            requestID: requestID
        )
    }

    func fetchCoreMemories() async throws -> [MemoryFact] {
        record("fetchCoreMemories")
        return try await base.fetchCoreMemories()
    }

    func fetchMemoryLogs() async throws -> [MemoryLog] {
        record("fetchMemoryLogs")
        return try await base.fetchMemoryLogs()
    }

    func fetchWorkingMemory() async throws -> WorkingMemory {
        record("fetchWorkingMemory")
        return try await base.fetchWorkingMemory()
    }

    func createMemory(
        text: String,
        section: String?,
        about: String?,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact> {
        record("createMemory")
        return try await base.createMemory(
            text: text,
            section: section,
            about: about,
            pinned: pinned,
            requestID: requestID
        )
    }

    func performMemoryAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<MemoryActionResource> {
        record("performMemoryAction")
        return try await base.performMemoryAction(
            id: id,
            expectedRevision: expectedRevision,
            action: action,
            requestID: requestID
        )
    }

    func performMemoryLogEntryAction(
        date: String,
        entryID: String,
        expectedRevision: Int,
        action: String,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryLogEntryActionResource> {
        record("performMemoryLogEntryAction")
        return try await base.performMemoryLogEntryAction(
            date: date,
            entryID: entryID,
            expectedRevision: expectedRevision,
            action: action,
            pinned: pinned,
            requestID: requestID
        )
    }

    func performWorkingMemoryAction(
        itemID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<WorkingMemoryActionResource> {
        record("performWorkingMemoryAction")
        return try await base.performWorkingMemoryAction(
            itemID: itemID,
            expectedRevision: expectedRevision,
            action: action,
            requestID: requestID
        )
    }

    func fetchAutomations() async throws -> [Automation] {
        record("fetchAutomations")
        return try await base.fetchAutomations()
    }

    func fetchAutomation(id: String) async throws -> Automation {
        record("fetchAutomation")
        return try await base.fetchAutomation(id: id)
    }

    func updateAutomation(
        id: String,
        expectedRevision: Int,
        value: AutomationEditable,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        record("updateAutomation")
        return try await base.updateAutomation(
            id: id,
            expectedRevision: expectedRevision,
            value: value,
            requestID: requestID
        )
    }

    func performAutomationAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        record("performAutomationAction")
        return try await base.performAutomationAction(
            id: id,
            expectedRevision: expectedRevision,
            action: action,
            requestID: requestID
        )
    }

    func fetchPermissions() async throws -> [PermissionRule] {
        record("fetchPermissions")
        return try await base.fetchPermissions()
    }

    func createPermissionRule(_ rule: PermissionRuleEditable, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        record("createPermissionRule")
        return try await base.createPermissionRule(rule, requestID: requestID)
    }

    func revokePermissionRule(id: String, expectedRevision: Int, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        record("revokePermissionRule")
        return try await base.revokePermissionRule(id: id, expectedRevision: expectedRevision, requestID: requestID)
    }

    func fetchPreferences() async throws -> Preferences {
        record("fetchPreferences")
        return try await base.fetchPreferences()
    }

    func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        record("savePreferences")
        return try await base.savePreferences(preferences, requestID: requestID)
    }

    func fetchModelKeys() async throws -> ModelKeysResponse {
        record("fetchModelKeys")
        return try await base.fetchModelKeys()
    }

    func fetchModelSettings() async throws -> ModelSettingsResponse {
        record("fetchModelSettings")
        return try await base.fetchModelSettings()
    }
}

@MainActor
@Suite("Performance Baseline — NoriCore")
struct NoriCorePerformanceBaselineTests {

    private static func formatBreakdown(_ byMethod: [String: Int]) -> String {
        byMethod
            .sorted { $0.key < $1.key }
            .map { "\($0.key)=\($0.value)" }
            .joined(separator: ", ")
    }

    /// Calls `NoriStore.refreshForTab(.more)` matching `NoriRootView.refresh(for: .more)`.
    private static func performMoreTabRefresh(on store: NoriStore) async {
        await store.refreshForTab(.more)
    }

    @Test("Baseline #4: NoriStore More-tab refresh request count across 2 consecutive switches (3 MCP servers)")
    func baselineMoreTabRefreshRequestCount() async {
        let servers = [
            MCPServer(id: "mcp-1", revision: 1, name: "Linear", observed: MCPObservedState(status: .connected), auth: .oauth),
            MCPServer(id: "mcp-2", revision: 1, name: "Notion", observed: MCPObservedState(status: .connected), auth: .oauth),
            MCPServer(id: "mcp-3", revision: 1, name: "GitHub", observed: MCPObservedState(status: .connected), auth: .oauth)
        ]
        let base = MockNoriTransport()
        let transport = BaselineCountingTransport(base: base, mcpServers: servers)
        let store = NoriStore(transport: transport, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "baseline@example.com")

        await transport.resetCounts()
        await Self.performMoreTabRefresh(on: store)
        let firstSwitch = await transport.snapshotCounts()

        await transport.resetCounts()
        await Self.performMoreTabRefresh(on: store)
        let secondSwitch = await transport.snapshotCounts()

        let combinedTotal = firstSwitch.total + secondSwitch.total
        print("[BASELINE] NoriStore More-tab refresh (3 MCP servers): switch1=\(firstSwitch.total) calls (\(Self.formatBreakdown(firstSwitch.byMethod)))")
        print("[BASELINE] NoriStore More-tab refresh (3 MCP servers): switch2=\(secondSwitch.total) calls (\(Self.formatBreakdown(secondSwitch.byMethod)))")
        print("[BASELINE] NoriStore More-tab refresh (3 MCP servers): combined_2_switches=\(combinedTotal) calls")

        #expect(firstSwitch.total > 0)
        #expect(combinedTotal > 0)
    }

    @Test("Baseline #5: NoriStore.resolveInteraction transport call count before returning vs total")
    func baselineResolveInteractionCallCount() async {
        let transport = BaselineCountingTransport()
        let store = NoriStore(transport: transport, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "baseline@example.com")

        let interaction = Interaction(
            id: "int-baseline-1",
            revision: 1,
            kind: .approval,
            status: .pending,
            prompt: "Send email to team?",
            payload: .approval(ApprovalPayload(
                summary: "Send email",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "conn-1", label: "alex@example.com")
            )),
            actionDigest: "sha256:baseline"
        )
        store.interactions = [interaction]

        await transport.resetCounts()
        let outcome = await store.resolveInteraction(
            interaction,
            answer: .approve(actionDigest: "sha256:baseline", rememberRule: false)
        )
        let beforeReturn = await transport.snapshotCounts()

        // Allow any detached/background refresh tasks to complete so we can compare before-return vs total.
        try? await Task.sleep(for: .milliseconds(50))
        let afterDrain = await transport.snapshotCounts()

        print("[BASELINE] NoriStore.resolveInteraction (approve, rememberRule=false): before_return=\(beforeReturn.total) calls (\(Self.formatBreakdown(beforeReturn.byMethod))) | total_after_drain=\(afterDrain.total) calls (\(Self.formatBreakdown(afterDrain.byMethod)))")

        if case .resolved = outcome {
            #expect(beforeReturn.total >= 1)
            #expect(afterDrain.total >= beforeReturn.total)
        } else {
            Issue.record("Expected resolveInteraction to return .resolved, got \(outcome)")
        }
    }
}
