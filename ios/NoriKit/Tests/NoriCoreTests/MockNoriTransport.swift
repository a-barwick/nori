import Foundation
@testable import NoriCore

final class InMemoryTokenStore: TokenStoring, @unchecked Sendable {
    private let lock = NSLock()
    private var storage: [String: String] = [:]

    init() {}

    @discardableResult
    func saveToken(_ token: String, account: String) -> Bool {
        lock.withLock {
            storage[account] = token
            return true
        }
    }

    func loadToken(account: String) -> String? {
        lock.withLock { storage[account] }
    }

    @discardableResult
    func deleteToken(account: String) -> Bool {
        lock.withLock {
            storage.removeValue(forKey: account)
            return true
        }
    }
}

actor MockNoriTransport: NoriTransport {
    struct DeleteCall: Sendable {
        let confirmation: String
        let resumePending: Bool
    }

    // Call counters
    var fetchFullSnapshotCallCount = 0
    var fetchSummaryCallCount = 0
    var fetchSessionCallCount = 0
    var fetchWorkItemsCallCount = 0
    var fetchMCPServersCallCount = 0
    var fetchConnectionsCallCount = 0
    var fetchPermissionsCallCount = 0

    // Account deletion state
    private(set) var deleteCalls: [DeleteCall] = []
    private var nextDeleteError: Error?

    // Model keys & settings state
    var modelKeysResponse = ModelKeysResponse(
        byokRequired: true,
        providers: [
            ModelKeyProvider(
                provider: "vercel_ai_gateway",
                label: "Vercel AI Gateway",
                configured: false
            )
        ]
    )
    var serviceKeysResponse = ServiceKeysResponse(
        services: [
            ServiceKeyStatus(
                service: "exa",
                label: "Exa Web Search",
                configured: false,
                source: "platform"
            )
        ]
    )
    var mockCapabilities = Capabilities()
    private var nextSaveModelKeyError: Error?
    private var nextSaveServiceKeyError: Error?
    private var nextSendTurnError: Error?
    private var nextFetchSessionError: Error?
    private var models = ModelSettingsResponse(
        modelProfiles: ModelProfileCatalog(),
        selection: ModelSelection(),
        revision: 0,
        recommendations: [:]
    )
    private var suspendSettingsRead = false
    private var suspendKeysRead = false
    private var blockedSettings: CheckedContinuation<Void, Never>?
    private var blockedKeys: CheckedContinuation<Void, Never>?
    private var readsStarted: CheckedContinuation<Void, Never>?

    // Session & Reset state
    private var delaySession = false
    private var suspendedSession: CheckedContinuation<Session, Never>?
    private var sessionStarted: CheckedContinuation<Void, Never>?
    private var resetFails = false
    private var failAfterStartingReset = false
    private var failResume = false
    private(set) var ordinaryResetCount = 0
    private(set) var resumeResetCount = 0
    private var delayReset = false
    private var suspendedReset: CheckedContinuation<Void, Never>?
    private var resetStarted: CheckedContinuation<Void, Never>?
    private var resetCompleted = false
    private var resetCompletionWaiter: CheckedContinuation<Void, Never>?
    private var mockPreferences = Preferences()

    // Snapshot & Domain state
    var customSnapshot: NoriFullSnapshot?
    var shouldFailSendTurn = false
    var shouldFailSnapshot = false
    var dismissReturnsIDOnly = false
    var reviseStatus: MutationReceipt<LibraryItem>.Status = .queued
    var lastConnectionReturnTo: String?
    var lastMCPReturnTo: String?
    var lastMCPCredentialSecret: String?
    var workingMemoryFailure = false

    // Interaction & Permission state
    var recordedAnswers: [InteractionAnswer] = []
    var nextInteractionRespondError: Error?
    var fetchedInteractionByID: [String: Interaction] = [:]
    var mockPermissions: [PermissionRule] = []
    var mockOperations: [OperationProjection] = []
    var mockMemorySearchResponse = MemorySearchResponse(items: [], query: "")
    var registeredPushRequests: [RegisterPushDeviceRequest] = []
    var deletedPushDeviceIDs: [String] = []

    // Stream & Interaction synchronization
    private nonisolated(unsafe) var streamEvents: [NoriStreamEvent] = []
    private nonisolated(unsafe) var streamTerminalError: Error?
    private let streamLock = NSLock()
    nonisolated let streamOpens: AsyncStream<Int>
    private nonisolated let streamOpenContinuation: AsyncStream<Int>.Continuation
    nonisolated let interactionReads: AsyncStream<Int>
    private let interactionReadContinuation: AsyncStream<Int>.Continuation
    private var delayInteractionReads = false
    private var shouldFailInteractionReads = false
    private var nextInteractionRead = 0
    private var suspendedInteractionReads: [Int: CheckedContinuation<[Interaction], Never>] = [:]

    init() {
        (streamOpens, streamOpenContinuation) = AsyncStream.makeStream(bufferingPolicy: .bufferingNewest(1))
        (interactionReads, interactionReadContinuation) = AsyncStream.makeStream()
    }

    // MARK: - Configuration Helpers

    func setNextDeleteError(_ error: Error?) { nextDeleteError = error }
    func setNextSaveError(_ error: Error?) { nextSaveModelKeyError = error }
    func setNextSaveServiceKeyError(_ error: Error?) { nextSaveServiceKeyError = error }
    func setNextSendTurnError(_ error: Error?) { nextSendTurnError = error }
    func setNextFetchSessionError(_ error: Error?) { nextFetchSessionError = error }
    func setCapabilities(_ caps: Capabilities) { mockCapabilities = caps }
    func setNextInteractionRespondError(_ error: Error?) { nextInteractionRespondError = error }
    func setFetchedInteraction(_ interaction: Interaction) { fetchedInteractionByID[interaction.id] = interaction }
    func setPermissions(_ rules: [PermissionRule]) { mockPermissions = rules }
    func setCustomSnapshot(_ snapshot: NoriFullSnapshot?) { customSnapshot = snapshot }
    func setResetFails(_ fail: Bool) { resetFails = fail }
    func setFailAfterStartingReset(_ fail: Bool) { failAfterStartingReset = fail }
    func setFailResume(_ fail: Bool) { failResume = fail }
    func setPreferences(_ preferences: Preferences) { mockPreferences = preferences }
    func setShouldFailSendTurn(_ fail: Bool) { shouldFailSendTurn = fail }
    func setShouldFailSnapshot(_ fail: Bool) { shouldFailSnapshot = fail }
    func setDismissReturnsIDOnly(_ flag: Bool) { dismissReturnsIDOnly = flag }
    func setReviseStatus(_ status: MutationReceipt<LibraryItem>.Status) { reviseStatus = status }
    func setWorkingMemoryFailure(_ value: Bool) { workingMemoryFailure = value }
    func suspendInteractionReads() { delayInteractionReads = true }
    func failInteractionReads() { shouldFailInteractionReads = true }

    func finishInteractionRead(_ index: Int, with interactions: [Interaction]) {
        suspendedInteractionReads.removeValue(forKey: index)?.resume(returning: interactions)
    }

    func setStreamEvents(_ events: [NoriStreamEvent], terminalError: Error?) {
        streamLock.lock()
        streamEvents = events
        streamTerminalError = terminalError
        streamLock.unlock()
    }

    func suspendNextSession() { delaySession = true }

    func waitForSuspendedSession() async {
        if suspendedSession != nil { return }
        await withCheckedContinuation { sessionStarted = $0 }
    }

    func finishSuspendedSession(_ session: Session) {
        suspendedSession?.resume(returning: session)
        suspendedSession = nil
    }

    func suspendNextReset() { delayReset = true }

    func waitForSuspendedReset() async {
        if suspendedReset != nil { return }
        await withCheckedContinuation { resetStarted = $0 }
    }

    func finishSuspendedReset() {
        suspendedReset?.resume()
        suspendedReset = nil
    }

    func waitForResetCompleted() async {
        if resetCompleted { return }
        await withCheckedContinuation { resetCompletionWaiter = $0 }
    }

    func suspendNextModelReads() {
        suspendSettingsRead = true
        suspendKeysRead = true
    }

    func waitForSuspendedModelReads() async {
        if blockedSettings != nil && blockedKeys != nil { return }
        await withCheckedContinuation { readsStarted = $0 }
    }

    private func signalModelReadsStarted() {
        if blockedSettings != nil && blockedKeys != nil {
            readsStarted?.resume()
            readsStarted = nil
        }
    }

    func resumeModelReads() {
        blockedSettings?.resume()
        blockedKeys?.resume()
        blockedSettings = nil
        blockedKeys = nil
    }

    func changeModelsElsewhere(_ selection: ModelSelection) {
        models.selection = selection
        models.revision += 1
    }

    // MARK: - NoriTransport

    func ping() async -> HealthCheckResult { HealthCheckResult(ok: true, status: "ok", latencyMs: 1) }
    func fetchCapabilities() async throws -> Capabilities { mockCapabilities }

    func fetchAssistantSummary() async throws -> AssistantSummary {
        fetchSummaryCallCount += 1
        return AssistantSummary(revision: 5, needsYou: 4, working: 2, appsAttention: 1, inboxUnread: 0)
    }

    func fetchSession(before cursor: String?) async throws -> Session {
        fetchSessionCallCount += 1
        if let nextFetchSessionError {
            self.nextFetchSessionError = nil
            throw nextFetchSessionError
        }
        if delaySession {
            delaySession = false
            return await withCheckedContinuation {
                suspendedSession = $0
                sessionStarted?.resume()
                sessionStarted = nil
            }
        }
        return Session()
    }

    func fetchFullSnapshot() async throws -> NoriFullSnapshot {
        fetchFullSnapshotCallCount += 1
        if shouldFailSnapshot {
            throw NoriAPIError(message: "Temporary snapshot failure", statusCode: 503, retryable: true)
        }
        if let customSnapshot {
            return customSnapshot
        }
        return NoriFullSnapshot(
            preferences: mockPreferences,
            modelKeys: modelKeysResponse,
            didLoadInteractions: !shouldFailInteractionReads
        )
    }

    nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        streamOpenContinuation.yield(position)
        streamLock.lock()
        let events = streamEvents
        let terminalErr = streamTerminalError
        streamLock.unlock()

        return AsyncThrowingStream { continuation in
            for event in events {
                continuation.yield(event)
            }
            if let terminalErr {
                continuation.finish(throwing: terminalErr)
            } else {
                continuation.finish()
            }
        }
    }

    func sendTurn(id: String, message: String, attachmentIDs: [String], modelSelection: ModelSelection?) async throws -> Session {
        if let nextSendTurnError {
            self.nextSendTurnError = nil
            throw nextSendTurnError
        }
        if shouldFailSendTurn {
            throw NoriAPIError(message: "Network dropped", statusCode: 503, retryable: true)
        }
        return Session(messages: [Message(id: "\(id)-user", turnID: id, role: .user, text: message, createdAt: "2026-11-24T10:00:00Z")])
    }

    func retryTurn(turnID: String?, messageID: String?) async throws -> Session {
        if shouldFailSendTurn {
            throw NoriAPIError(message: "Network dropped", statusCode: 503, retryable: true)
        }
        return Session(
            messages: [
                Message(
                    id: messageID ?? "m-err",
                    turnID: turnID,
                    role: .assistant,
                    status: .superseded,
                    text: "Retrying",
                    createdAt: "2026-11-24T10:00:00Z"
                )
            ],
            conversation: Conversation(pending: true, processing: true)
        )
    }

    func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse { TaskCommandResponse(accepted: true) }

    func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> NoriCore.Attachment {
        NoriCore.Attachment(id: id, name: filename, mediaType: mediaType, size: data.count)
    }

    func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] {
        fetchWorkItemsCallCount += 1
        return []
    }

    func fetchWorkDetail(taskID: String) async throws -> WorkDetail { throw NoriAPIError(message: "unimplemented") }
    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> { throw NoriAPIError(message: "unimplemented") }
    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> { throw NoriAPIError(message: "unimplemented") }
    func fetchOperations(connectionID: String?) async throws -> [OperationProjection] { mockOperations }

    func fetchInteractions() async throws -> [Interaction] {
        if shouldFailInteractionReads {
            throw NoriAPIError(message: "Interactions unavailable", statusCode: 503)
        }
        guard delayInteractionReads else { return [] }
        let index = nextInteractionRead
        nextInteractionRead += 1
        return await withCheckedContinuation {
            suspendedInteractionReads[index] = $0
            interactionReadContinuation.yield(index)
        }
    }

    func fetchInteraction(id: String) async throws -> Interaction {
        if let found = fetchedInteractionByID[id] {
            return found
        }
        throw NoriAPIError(message: "Interaction not found", statusCode: 404)
    }

    func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction> {
        recordedAnswers.append(answer)
        if let error = nextInteractionRespondError {
            nextInteractionRespondError = nil
            throw error
        }
        let resolutionKind: InteractionResolution.Kind
        switch answer {
        case .approve: resolutionKind = .approve
        case .decline: resolutionKind = .decline
        case .answer: resolutionKind = .answer
        case .chooseAccount: resolutionKind = .chooseAccount
        case .resume: resolutionKind = .resume
        case .cancel: resolutionKind = .cancel
        }
        let resolved = Interaction(
            id: id,
            revision: expectedRevision + 1,
            kind: .approval,
            status: .resolved,
            prompt: "Send email?",
            payload: .unknown(.null),
            resolution: InteractionResolution(kind: resolutionKind, at: "2026-11-25T04:10:00Z")
        )
        return MutationReceipt(requestID: requestID, resource: resolved)
    }

    func fetchMCPServers() async throws -> [MCPServer] {
        fetchMCPServersCallCount += 1
        return []
    }

    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] {
        [MCPServerTool(name: "issues_list", title: "List Issues")]
    }

    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "unimplemented")
    }

    func performMCPAction(serverID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<MCPServer> {
        lastMCPReturnTo = returnTo
        let server = MCPServer(id: serverID, revision: expectedRevision + 1, name: "Linear", observed: MCPObservedState(status: .needsSignIn), auth: .oauth)
        let auth = action == "sign_in" ? AuthAttempt(id: "att-mcp-1", authorizeURL: "https://linear.app/oauth", expiresAt: "2026-11-25T00:00:00Z") : nil
        return MutationReceipt(requestID: requestID, status: auth != nil ? .queued : .applied, resource: server, auth: auth)
    }

    func setMCPCredential(serverID: String, expectedRevision: Int, secret: String, requestID: String) async throws -> MutationReceipt<MCPServer> {
        lastMCPCredentialSecret = secret
        let server = MCPServer(id: serverID, revision: expectedRevision + 1, name: "Linear", observed: MCPObservedState(status: .connected, credential: "present"), auth: .configured)
        return MutationReceipt(requestID: requestID, status: .applied, resource: server)
    }

    func setMCPToolExposure(serverID: String, expectedRevision: Int, toolName: String, exposure: MCPToolExposure, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "unimplemented")
    }

    func fetchConnections() async throws -> [Connection] {
        fetchConnectionsCallCount += 1
        return []
    }

    func performConnectionAction(connectionID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<Connection> {
        lastConnectionReturnTo = returnTo
        let conn = Connection(id: connectionID, revision: expectedRevision + 1, integrationID: "gmail", title: "Work Gmail", status: .connecting)
        let auth = AuthAttempt(id: "att-conn-1", authorizeURL: "https://accounts.google.com/o/oauth2/auth", expiresAt: "2026-11-25T00:00:00Z")
        return MutationReceipt(requestID: requestID, status: .queued, resource: conn, auth: auth)
    }

    func fetchLibrary() async throws -> [LibraryItem] { [] }

    func fetchLibraryItem(id: String) async throws -> LibraryItem {
        LibraryItem(id: id, version: 1, kind: .document, title: "Deep Document", revision: 1)
    }

    func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload {
        LibraryFilePayload(data: Data("# Full Doc\n\nBody text.".utf8), mediaType: "text/markdown; charset=utf-8", filename: "Full Doc.md")
    }

    func search(query: String) async throws -> SearchResponse { SearchResponse(items: []) }

    func reviseLibraryItem(id: String, expectedRevision: Int, instruction: String, requestID: String) async throws -> MutationReceipt<LibraryItem> {
        if reviseStatus == .queued {
            let item = LibraryItem(id: id, version: 1, kind: .document, title: "Deep Document", revision: expectedRevision)
            return MutationReceipt(requestID: requestID, status: .queued, resource: item, work: WorkRef(taskID: "rev-task-1"))
        }
        let item = LibraryItem(id: id, version: 2, kind: .document, title: "Deep Document", revision: expectedRevision + 1)
        return MutationReceipt(requestID: requestID, status: .applied, resource: item)
    }

    func fetchCoreMemories() async throws -> [MemoryFact] { [] }

    func fetchMemoryLogs() async throws -> [MemoryLog] {
        [MemoryLog(date: "2026-11-24", revision: 21, entries: [MemoryLogEntry(id: "le-1", text: "Promote me", promoted: true)])]
    }

    func fetchWorkingMemory() async throws -> WorkingMemory {
        if workingMemoryFailure {
            throw NoriAPIError(message: "Working memory unavailable", statusCode: 503)
        }
        return WorkingMemory(revision: 35, items: [WorkingMemoryItem(id: "wm-2", kind: .fact, quote: "Dismiss me")], coreBytes: 512)
    }

    func searchMemory(query: String, layer: String?, limit: Int) async throws -> MemorySearchResponse {
        mockMemorySearchResponse
    }

    func createMemory(text: String, section: String?, about: String?, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryFact> {
        MutationReceipt(requestID: requestID, resource: MemoryFact(id: "new-mem", text: text, pinned: pinned))
    }

    func performMemoryAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<MemoryActionResource> {
        switch action {
        case "pin":
            return MutationReceipt(requestID: requestID, resource: .fact(MemoryFact(id: id, revision: expectedRevision + 1, text: "Fact", pinned: true)))
        case "unpin":
            return MutationReceipt(requestID: requestID, resource: .fact(MemoryFact(id: id, revision: expectedRevision + 1, text: "Fact", pinned: false)))
        case "demote":
            return MutationReceipt(requestID: requestID, resource: .demoted(MemoryDemoteResult(
                demoted: MemoryFact(id: id, text: "Fact"),
                logEntry: MemoryLogEntry(id: "demote_\(id)", text: "Fact"),
                note: "Moved"
            )))
        default:
            return MutationReceipt(requestID: requestID, resource: .forgotten(MemoryForgetResult(
                note: "Forgotten",
                forgotten: MemoryFact(id: id, text: "Fact")
            )))
        }
    }

    func performMemoryLogEntryAction(date: String, entryID: String, expectedRevision: Int, action: String, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryLogEntryActionResource> {
        if action == "promote" {
            return MutationReceipt(requestID: requestID, resource: .promoted(MemoryFact(id: "promoted-\(entryID)", text: "Promoted", pinned: pinned)))
        }
        return MutationReceipt(requestID: requestID, resource: .forgotten(MemoryLogEntryForgetResult(
            note: "Removed",
            forgotten: MemoryLogEntry(id: entryID, text: "Removed")
        )))
    }

    func performWorkingMemoryAction(itemID: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<WorkingMemoryActionResource> {
        if action == "promote" {
            return MutationReceipt(requestID: requestID, resource: .promoted(MemoryFact(id: "promoted-\(itemID)", text: "Promoted working")))
        }
        if dismissReturnsIDOnly {
            return MutationReceipt(requestID: requestID, resource: .dismissed(WorkingMemoryDismissResult(dismissedItemID: itemID)))
        }
        return MutationReceipt(requestID: requestID, resource: .workingMemory(WorkingMemory(revision: expectedRevision + 1, items: [])))
    }

    func fetchAutomations() async throws -> [Automation] { [] }

    func fetchAutomation(id: String) async throws -> Automation {
        Automation(
            id: id,
            revision: 1,
            title: "Check supernino updates",
            instruction: "Check supernino updates",
            status: .enabled,
            trigger: AutomationTrigger(kind: "once", at: "2026-11-25T20:00:00Z", timezone: nil),
            missedRun: .latest
        )
    }

    func updateAutomation(id: String, expectedRevision: Int, value: AutomationEditable, requestID: String) async throws -> MutationReceipt<Automation> {
        let updated = Automation(
            id: id,
            revision: expectedRevision + 1,
            title: value.title,
            instruction: value.instruction,
            status: .enabled,
            trigger: value.trigger,
            connectionIDs: value.connectionIDs,
            budgetUSD: value.budgetUSD,
            maxActiveRuns: value.maxActiveRuns,
            missedRun: value.missedRun ?? .latest,
            executionWindowSeconds: value.executionWindowSeconds,
            destinationIDs: value.destinationIDs,
            nextAt: value.trigger.at
        )
        return MutationReceipt(requestID: requestID, resource: updated)
    }

    func performAutomationAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<Automation> {
        let status: AutomationStatus = action == "cancel" ? .cancelled : (action == "pause" ? .paused : .enabled)
        let updated = Automation(
            id: id,
            revision: expectedRevision + 1,
            title: "Check supernino updates",
            instruction: "Check supernino updates",
            status: status,
            trigger: AutomationTrigger(kind: "once", at: "2026-11-25T20:00:00Z", timezone: nil),
            missedRun: .latest
        )
        return MutationReceipt(requestID: requestID, resource: updated)
    }

    func fetchPermissions() async throws -> [PermissionRule] {
        fetchPermissionsCallCount += 1
        return mockPermissions
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
        let fallback = mockPermissions.first ?? PermissionRule(
            id: id,
            revision: expectedRevision,
            effect: .allow,
            toolIDs: ["*"],
            connectionIDs: ["*"],
            resourceIDs: ["*"],
            actionClasses: [.read]
        )
        return MutationReceipt(requestID: requestID, resource: fallback)
    }

    func fetchPreferences() async throws -> Preferences { mockPreferences }

    func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        MutationReceipt(requestID: requestID, resource: preferences)
    }

    func fetchModelKeys() async throws -> ModelKeysResponse {
        let snapshot = modelKeysResponse
        if suspendKeysRead {
            suspendKeysRead = false
            await withCheckedContinuation { continuation in
                blockedKeys = continuation
                signalModelReadsStarted()
            }
        }
        return snapshot
    }

    func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider {
        if let nextSaveModelKeyError {
            throw nextSaveModelKeyError
        }
        let updated = ModelKeyProvider(
            provider: provider,
            label: "Vercel AI Gateway",
            configured: true,
            last4: String(apiKey.suffix(4)),
            updatedAt: "2026-11-25T17:00:00Z",
            validatedAt: "2026-11-25T17:00:00Z"
        )
        modelKeysResponse.providers = [updated]
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
        modelKeysResponse.providers = [updated]
        return updated
    }

    func fetchServiceKeys() async throws -> ServiceKeysResponse {
        serviceKeysResponse
    }

    func saveServiceKey(service: String, apiKey: String) async throws -> ServiceKeyStatus {
        if let nextSaveServiceKeyError {
            throw nextSaveServiceKeyError
        }
        let suffix = String(apiKey.suffix(4))
        let updated = ServiceKeyStatus(
            service: service,
            label: service == "exa" ? "Exa Web Search" : service.capitalized,
            configured: true,
            last4: suffix,
            maskedKey: "••••\(suffix)",
            source: "byok",
            updatedAt: "2026-11-28T01:00:00Z",
            validatedAt: "2026-11-28T01:00:00Z"
        )
        serviceKeysResponse.services = [updated]
        return updated
    }

    func deleteServiceKey(service: String) async throws -> ServiceKeyStatus {
        let updated = ServiceKeyStatus(
            service: service,
            label: service == "exa" ? "Exa Web Search" : service.capitalized,
            configured: false,
            last4: nil,
            maskedKey: nil,
            source: "platform",
            updatedAt: nil,
            validatedAt: nil
        )
        serviceKeysResponse.services = [updated]
        return updated
    }

    func fetchModelSettings() async throws -> ModelSettingsResponse {
        let snapshot = models
        if suspendSettingsRead {
            suspendSettingsRead = false
            await withCheckedContinuation { continuation in
                blockedSettings = continuation
                signalModelReadsStarted()
            }
        }
        return snapshot
    }

    func saveModelSettings(_ request: SaveModelSettingsRequest) async throws -> ModelSettingsResponse {
        guard request.expectedRevision == models.revision else {
            throw NoriAPIError(message: "Models changed elsewhere.", statusCode: 409)
        }
        models.selection = request.selection
        models.revision += 1
        return models
    }

    func resetUserData(confirmation: String) async throws {
        ordinaryResetCount += 1
        guard !resetFails, confirmation == "RESET" else {
            throw NoriAPIError(message: "Reset could not be completed.", statusCode: 409)
        }
        if failAfterStartingReset {
            mockPreferences.dataResetPending = true
            throw NoriAPIError(message: "Finish your reset.", statusCode: 502, code: "data_reset_pending")
        }
        if delayReset {
            delayReset = false
            await withCheckedContinuation {
                suspendedReset = $0
                resetStarted?.resume()
                resetStarted = nil
            }
        } else {
            mockPreferences = Preferences(dataResetAt: "2026-11-25T20:00:00Z")
        }
        modelKeysResponse.providers = modelKeysResponse.providers.map {
            ModelKeyProvider(provider: $0.provider, label: $0.label)
        }
        models = ModelSettingsResponse(
            modelProfiles: ModelProfileCatalog(),
            selection: ModelSelection(),
            revision: 0,
            recommendations: [:]
        )
        resetCompleted = true
        resetCompletionWaiter?.resume()
        resetCompletionWaiter = nil
    }

    func resumePendingDataReset() async throws {
        resumeResetCount += 1
        guard mockPreferences.dataResetPending == true else {
            throw NoriAPIError(message: "Reset is no longer pending.", statusCode: 409, code: "data_reset_not_pending")
        }
        if failResume {
            throw NoriAPIError(message: "Try again.", statusCode: 502, code: "data_reset_pending")
        }
        mockPreferences = Preferences(dataResetAt: "2026-11-25T20:00:00Z")
    }

    func deleteAccount(confirmation: String, resumePending: Bool) async throws {
        deleteCalls.append(DeleteCall(confirmation: confirmation, resumePending: resumePending))
        if let error = nextDeleteError {
            nextDeleteError = nil
            throw error
        }
    }

    func registerPushDevice(_ request: RegisterPushDeviceRequest) async throws -> PushDevice {
        registeredPushRequests.append(request)
        return PushDevice(
            id: "push-dev-\(registeredPushRequests.count)",
            deviceID: request.deviceID,
            environment: request.environment,
            createdAt: "2026-11-27T08:00:00Z",
            updatedAt: "2026-11-27T08:00:00Z"
        )
    }

    func deletePushDevice(id: String) async throws {
        deletedPushDeviceIDs.append(id)
    }
}
