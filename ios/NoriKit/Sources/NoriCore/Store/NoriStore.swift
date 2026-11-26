import Foundation
import Observation


public enum NoriTab: String, Sendable, Hashable, CaseIterable, Identifiable {
    case chat
    case work
    case more

    public var id: String { rawValue }
}

public enum NoriDeepLink: Sendable, Hashable {
    case tab(NoriTab)
    case activitySheet
    case apps
    case library
    case memory
    case routines
    case goals
    case settings
    case workDetail(String)
    case document(String)
    case oauthCallback(URL)

    public static func parse(_ url: URL) -> NoriDeepLink? {
        guard url.scheme?.lowercased() == "nori" else { return nil }
        let host = url.host?.lowercased() ?? ""
        let parts = url.pathComponents.filter { $0 != "/" }

        switch host {
        case "tab":
            if let name = parts.first?.lowercased() {
                if let tab = NoriTab(rawValue: name) {
                    return .tab(tab)
                }
                switch name {
                case "apps": return .apps
                case "library": return .library
                default: break
                }
            }
        case "chat": return .tab(.chat)
        case "work":
            if let taskID = parts.first, !taskID.isEmpty {
                return .workDetail(taskID)
            }
            return .tab(.work)
        case "apps": return .apps
        case "library":
            if let itemID = parts.first, !itemID.isEmpty {
                return .document(itemID)
            }
            return .library
        case "more": return .tab(.more)
        case "sheet":
            if parts.first?.lowercased() == "activity" {
                return .activitySheet
            }
        case "activity": return .activitySheet
        case "memory": return .memory
        case "routines", "automations": return .routines
        case "goals": return .goals
        case "settings": return .settings
        case "oauth", "callback": return .oauthCallback(url)
        default:
            break
        }
        return nil
    }
}

public enum ConnectionState: String, Sendable, Hashable {
    case connected
    case connecting
    case reconnecting
    case offline
}

public enum StatusTone: String, Sendable, Hashable {
    case normal
    case needsYou
    case offline
}

public struct StatusLine: Sendable, Hashable {
    public var text: String
    public var tone: StatusTone

    public init(text: String, tone: StatusTone = .normal) {
        self.text = text
        self.tone = tone
    }
}


@Observable
@MainActor
public final class NoriStore {
    public var authState: AuthState = .signedOut
    public var authError: String?
    public var connectionState: ConnectionState = .offline
    public var capabilities: Capabilities?
    public var assistantSummary: AssistantSummary?
    public var lastPing: HealthCheckResult?
    public private(set) var modelSelection = ModelSelection()

    private static let modelSelectionStorageKey = "nori.diagnostics.modelSelection.v1"

    private var modelSelectionKey: String? {
        guard case .signedIn(let email) = authState else { return nil }
        return Self.modelSelectionStorageKey + "." + email.lowercased()
    }

    private func loadStoredModelSelection() -> ModelSelection {
        guard let key = modelSelectionKey,
              let data = UserDefaults.standard.data(forKey: key),
              let decoded = try? JSONDecoder().decode(ModelSelection.self, from: data)
        else {
            return ModelSelection()
        }
        return decoded
    }

    public func setModelSelection(_ next: ModelSelection) {
        guard let key = modelSelectionKey else { return }
        modelSelection = next
        if next.isEmpty {
            UserDefaults.standard.removeObject(forKey: key)
        } else if let encoded = try? JSONEncoder().encode(next) {
            UserDefaults.standard.set(encoded, forKey: key)
        }
    }

    public var effectiveModelSelection: ModelSelection {
        modelSelection.normalized(using: capabilities?.modelProfiles)
    }

    public var session: Session
    public var workItems: [WorkItem]
    public var workDetails: [String: WorkDetail]
    public var interactions: [Interaction]
    public var mcpServers: [MCPServer]
    public var mcpTools: [String: [MCPServerTool]]
    public var connections: [Connection]
    public var libraryItems: [LibraryItem]
    public var searchHits: [SearchHit]
    public var coreMemories: [MemoryFact]
    public var memoryLogs: [MemoryLog]
    public var workingMemory: WorkingMemory
    public var automations: [Automation]
    public var goals: [Goal]
    public var permissions: [PermissionRule]
    public var preferences: Preferences

    public var isLoading: Bool = false
    public var isThinking: Bool = false
    public var streamingAssistantText: String?
    public var streamingTurnID: String?
    public var isComposerFocused: Bool = false
    public var pendingComposerDraft: String?
    public var lastError: String?
    public var memoryRefreshError: String?
    public var toastMessage: String?
    public var pendingDeepLink: NoriDeepLink?

    private let authProvider: AuthTokenProvider
    private let liveTransport: LiveNoriTransport?
    var transport: any NoriTransport
    var sessionGeneration: UInt64 = 0
    private var eventTask: Task<Void, Never>?
    private var toastTask: Task<Void, Never>?
    var hasBootstrapped: Bool = false

    public init(
        transport: (any NoriTransport)? = nil,
        authProvider: AuthTokenProvider = AuthTokenProvider()
    ) {
        self.authProvider = authProvider
        if let transport {
            self.liveTransport = transport as? LiveNoriTransport
            self.transport = transport
        } else {
            let live = LiveNoriTransport(baseURL: NoriConfig.serverURL, authProvider: authProvider)
            self.liveTransport = live
            self.transport = live
        }
        self.connectionState = .offline

        self.session = Session()
        self.workItems = []
        self.workDetails = [:]
        self.interactions = []
        self.mcpServers = []
        self.mcpTools = [:]
        self.connections = []
        self.libraryItems = []
        self.searchHits = []
        self.coreMemories = []
        self.memoryLogs = []
        self.workingMemory = WorkingMemory()
        self.automations = []
        self.goals = []
        self.permissions = []
        self.preferences = Preferences()
    }


    public var messages: [Message] {
        session.messages
    }

    public var runs: [Run] {
        session.runs
    }

    public var pendingInteractions: [Interaction] {
        interactions.filter { $0.status == .pending }
    }

    public var needsYouCount: Int {
        assistantSummary?.needsYou ?? pendingInteractions.count
    }

    private var computedAppsAttentionCount: Int {
        let mcpCount = mcpServers.filter { $0.observed.status.needsAttention }.count
        let connCount = connections.filter { $0.status == .needsAttention }.count
        return mcpCount + connCount
    }

    public var appsAttentionCount: Int {
        assistantSummary?.appsAttention ?? computedAppsAttentionCount
    }

    public var activeWorkItems: [WorkItem] {
        workItems.filter { $0.status == .running }
    }

    public var finishedWorkItems: [WorkItem] {
        workItems.filter { $0.status != .running }
    }

    public var activeTasksCount: Int {
        assistantSummary?.working ?? activeWorkItems.count
    }

    public var statusLine: StatusLine {
        if connectionState == .reconnecting || connectionState == .offline {
            return StatusLine(text: "Reconnecting…", tone: .offline)
        }
        if isThinking {
            return StatusLine(text: "Thinking…", tone: .normal)
        }
        if needsYouCount > 0 {
            let label = needsYouCount == 1 ? "1 item needs action" : "\(needsYouCount) items need action"
            return StatusLine(text: label, tone: .needsYou)
        }
        if activeTasksCount > 0 {
            let label = activeTasksCount == 1 ? "1 task running" : "\(activeTasksCount) tasks running"
            return StatusLine(text: label, tone: .normal)
        }
        return StatusLine(text: "Ready when you are", tone: .normal)
    }

    public var markState: MarkState {
        if isThinking { return .thinking }
        if isComposerFocused { return .attentive }
        return .idle
    }

    public var coreMemoryUsedBytes: Int {
        workingMemory.coreBytes
    }

    public var coreMemoryBudgetBytes: Int {
        max(1, workingMemory.coreBudgetBytes)
    }

    public var coreMemoryRatio: Double {
        min(1.0, Double(coreMemoryUsedBytes) / Double(coreMemoryBudgetBytes))
    }


    public func bootstrapIfNeeded() async {
        guard !hasBootstrapped else { return }
        hasBootstrapped = true
        let generation = sessionGeneration
        let savedSession = await authProvider.loadSession()
        guard generation == sessionGeneration else { return }
        if let saved = savedSession {
            authState = .signedIn(email: saved.email)
            modelSelection = loadStoredModelSelection()
            await refreshAll()
            if generation == sessionGeneration, authState.isSignedIn {
                startEventStream()
            }
        } else {
            authState = .signedOut
            connectionState = .offline
        }
    }

    public func completeGoogleSignIn(callbackURL: URL, pkce: GoogleOAuthPKCE) async {
        sessionGeneration &+= 1
        let generation = sessionGeneration
        clearUserState()
        authError = nil
        authState = .signingIn
        do {
            let code = try pkce.extractAuthorizationCode(from: callbackURL)
            let session = try await pkce.exchangeCode(code)
            guard generation == sessionGeneration else { return }
            await authProvider.saveSession(session)
            if let liveTransport {
                await liveTransport.clearCachedIdentity()
            }
            let snapshot = try await transport.fetchFullSnapshot()
            guard generation == sessionGeneration else { return }
            applySnapshot(snapshot)
            authState = .signedIn(email: session.email)
            modelSelection = loadStoredModelSelection()
            connectionState = .connected
            lastError = nil
            startEventStream()
        } catch let apiErr as NoriAPIError where apiErr.statusCode == 401 || apiErr.statusCode == 403 {
            guard generation == sessionGeneration else { return }
            await authProvider.clearSession()
            guard generation == sessionGeneration else { return }
            authState = .signedOut
            connectionState = .offline
            authError = "This Google account is not authorized to access Nori."
        } catch {
            guard generation == sessionGeneration else { return }
            await authProvider.clearSession()
            guard generation == sessionGeneration else { return }
            authState = .signedOut
            connectionState = .offline
            authError = error.localizedDescription
        }
    }

    public func signOut() async {
        sessionGeneration &+= 1
        clearUserState()
        if let liveTransport {
            await liveTransport.clearCachedIdentity()
        }
        await authProvider.clearSession()
    }

    private func clearUserState() {
        stopEventStream()
        toastTask?.cancel()
        authState = .signedOut
        authError = nil
        connectionState = .offline
        capabilities = nil
        assistantSummary = nil
        lastPing = nil
        session = Session()
        workItems = []
        workDetails = [:]
        interactions = []
        mcpServers = []
        mcpTools = [:]
        connections = []
        libraryItems = []
        searchHits = []
        coreMemories = []
        memoryLogs = []
        workingMemory = WorkingMemory()
        automations = []
        goals = []
        permissions = []
        preferences = Preferences()
        isThinking = false
        isLoading = false
        isComposerFocused = false
        pendingComposerDraft = nil
        pendingDeepLink = nil
        lastError = nil
        memoryRefreshError = nil
        toastMessage = nil
        streamingAssistantText = nil
        streamingTurnID = nil
        modelSelection = ModelSelection()
    }

    func handleAuthFailureIfNeeded(_ error: Error) async -> Bool {
        if let apiErr = error as? NoriAPIError, apiErr.isAuthError {
            let nextGeneration = sessionGeneration &+ 1
            await signOut()
            guard nextGeneration == sessionGeneration else { return true }
            authError = "Your session expired or your account is not authorized. Please sign in again."
            return true
        }
        return false
    }

    public func pauseForBackground() {
        stopEventStream()
    }

    public func resumeFromBackground() async {
        guard authState.isSignedIn else { return }
        async let summaryTask: Void = refreshSummary()
        async let sessionTask: Void = refreshSession()
        async let workTask: Void = refreshWorkAndInteractions()
        _ = await (summaryTask, sessionTask, workTask)
        if authState.isSignedIn {
            startEventStream()
        }
    }

    public func refreshSummary() async {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchAssistantSummary()
            guard generation == sessionGeneration else { return }
            assistantSummary = latest
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleAuthFailureIfNeeded(error)
        }
    }

    public func refreshSession() async {
        let generation = sessionGeneration
        do {
            let freshSession = try await transport.fetchSession(before: nil)
            guard generation == sessionGeneration else { return }
            self.session = freshSession
            self.isThinking = freshSession.conversation?.processing == true
        } catch {
            guard generation == sessionGeneration else { return }
            if !(await handleAuthFailureIfNeeded(error)) {
                lastError = error.localizedDescription
            }
        }
    }

    public func refreshWorkAndInteractions() async {
        let generation = sessionGeneration
        let activeTransport = transport
        async let workResult: Result<[WorkItem], Error> = {
            do { return .success(try await activeTransport.fetchWorkItems(bucket: nil)) }
            catch { return .failure(error) }
        }()
        async let interactionsResult: Result<[Interaction], Error> = {
            do { return .success(try await activeTransport.fetchInteractions()) }
            catch { return .failure(error) }
        }()

        let (workRes, interRes) = await (workResult, interactionsResult)
        guard generation == sessionGeneration else { return }
        if case .failure(let err) = workRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = interRes, await handleAuthFailureIfNeeded(err) { return }

        if case .success(let freshWork) = workRes {
            self.workItems = freshWork
        }
        if case .success(let freshInteractions) = interRes {
            self.interactions = freshInteractions
        }
        if assistantSummary != nil {
            assistantSummary?.needsYou = pendingInteractions.count
            assistantSummary?.working = activeWorkItems.count
        }
    }

    public func refreshApps() async {
        let generation = sessionGeneration
        let activeTransport = transport
        async let serversResult: Result<[MCPServer], Error> = {
            do { return .success(try await activeTransport.fetchMCPServers()) }
            catch { return .failure(error) }
        }()
        async let connectionsResult: Result<[Connection], Error> = {
            do { return .success(try await activeTransport.fetchConnections()) }
            catch { return .failure(error) }
        }()

        let (serversRes, connsRes) = await (serversResult, connectionsResult)
        guard generation == sessionGeneration else { return }
        if case .failure(let err) = serversRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = connsRes, await handleAuthFailureIfNeeded(err) { return }

        if case .success(let servers) = serversRes {
            self.mcpServers = servers
            let toolsByServer = await withTaskGroup(
                of: (String, [MCPServerTool]?).self,
                returning: [String: [MCPServerTool]].self
            ) { group in
                for server in servers {
                    let serverID = server.id
                    group.addTask {
                        let tools = try? await activeTransport.fetchMCPTools(serverID: serverID)
                        return (serverID, tools)
                    }
                }
                var collected: [String: [MCPServerTool]] = [:]
                for await (serverID, tools) in group {
                    if let tools {
                        collected[serverID] = tools
                    }
                }
                return collected
            }
            guard generation == sessionGeneration else { return }
            self.mcpTools = toolsByServer
        }
        if case .success(let conns) = connsRes {
            self.connections = conns
        }
        if assistantSummary != nil {
            assistantSummary?.appsAttention = computedAppsAttentionCount
        }
    }

    public func refreshLibrary() async {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchLibrary()
            guard generation == sessionGeneration else { return }
            libraryItems = latest
        } catch {
            guard generation == sessionGeneration else { return }
            if !(await handleAuthFailureIfNeeded(error)) {
                lastError = error.localizedDescription
            }
        }
    }

    public func refreshRoutinesAndGoals() async {
        let generation = sessionGeneration
        let activeTransport = transport
        async let autosResult: Result<[Automation], Error> = {
            do { return .success(try await activeTransport.fetchAutomations()) }
            catch { return .failure(error) }
        }()
        async let goalsResult: Result<[Goal], Error> = {
            do { return .success(try await activeTransport.fetchGoals()) }
            catch { return .failure(error) }
        }()

        let (autosRes, goalsRes) = await (autosResult, goalsResult)
        guard generation == sessionGeneration else { return }
        if case .failure(let err) = autosRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = goalsRes, await handleAuthFailureIfNeeded(err) { return }

        if case .success(let autos) = autosRes {
            self.automations = autos
        }
        if case .success(let freshGoals) = goalsRes {
            self.goals = freshGoals
        }
    }

    public func refreshPermissionsAndPreferences() async {
        let generation = sessionGeneration
        let activeTransport = transport
        async let permsResult: Result<[PermissionRule], Error> = {
            do { return .success(try await activeTransport.fetchPermissions()) }
            catch { return .failure(error) }
        }()
        async let prefsResult: Result<Preferences, Error> = {
            do { return .success(try await activeTransport.fetchPreferences()) }
            catch { return .failure(error) }
        }()

        let (permsRes, prefsRes) = await (permsResult, prefsResult)
        guard generation == sessionGeneration else { return }
        if case .failure(let err) = permsRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = prefsRes, await handleAuthFailureIfNeeded(err) { return }

        if case .success(let perms) = permsRes {
            self.permissions = perms
        }
        if case .success(let prefs) = prefsRes {
            self.preferences = prefs
        }
    }

    public func refreshAll() async {
        let generation = sessionGeneration
        isLoading = true
        defer { if generation == sessionGeneration { isLoading = false } }
        connectionState = .connecting
        do {
            let snapshot = try await transport.fetchFullSnapshot()
            guard generation == sessionGeneration else { return }
            applySnapshot(snapshot)
            connectionState = .connected
            lastError = nil
        } catch let apiErr as NoriAPIError where apiErr.statusCode == 401 || apiErr.statusCode == 403 {
            guard generation == sessionGeneration else { return }
            await signOut()
            authError = "Your session expired or your account is not authorized. Please sign in again."
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            connectionState = .reconnecting
        }
    }

    private func applySnapshot(_ snapshot: NoriFullSnapshot) {
        self.capabilities = snapshot.capabilities
        self.assistantSummary = nil
        self.session = snapshot.session
        self.workItems = snapshot.workItems
        self.interactions = snapshot.interactions
        self.mcpServers = snapshot.mcpServers
        self.mcpTools = snapshot.mcpTools
        self.connections = snapshot.connections
        self.libraryItems = snapshot.libraryItems
        self.coreMemories = snapshot.coreMemories
        self.memoryLogs = snapshot.memoryLogs
        self.workingMemory = snapshot.workingMemory
        self.automations = snapshot.automations
        self.goals = snapshot.goals
        self.permissions = snapshot.permissions
        self.preferences = snapshot.preferences
        self.isThinking = snapshot.session.conversation?.processing == true
    }

    public func startEventStream() {
        stopEventStream()
        guard authState.isSignedIn else { return }
        let startPosition = session.position ?? 0
        let activeTransport = transport

        eventTask = Task { [weak self] in
            let stream = activeTransport.eventStream(after: startPosition)
            do {
                for try await event in stream {
                    guard let self, !Task.isCancelled else { break }
                    await self.handleStreamEvent(event)
                }
            } catch let apiErr as NoriAPIError where apiErr.statusCode == 401 || apiErr.statusCode == 403 {
                guard let self, !Task.isCancelled else { return }
                await self.signOut()
                self.authError = "Your session expired or your account is not authorized. Please sign in again."
            } catch {
                guard let self, !Task.isCancelled else { return }
                self.connectionState = .reconnecting
            }
        }
    }

    public func stopEventStream() {
        eventTask?.cancel()
        eventTask = nil
    }

    private func handleStreamEvent(_ event: NoriStreamEvent) async {
        let generation = sessionGeneration
        if case .connectionState(let state) = event {
            connectionState = state
            return
        }
        connectionState = .connected
        if case .reset(let pos) = event {
            session.position = pos
            await refreshAll()
            guard generation == sessionGeneration else { return }
            if session.position == nil {
                session.position = pos
            }
            return
        }
        if let seq = event.sequence, seq > (session.position ?? 0) {
            session.position = seq
        }
        switch event {
        case .connectionState, .reset:
            break
        case .message(_, let message):
            if let idx = session.messages.firstIndex(where: { $0.id == message.id }) {
                session.messages[idx] = message
            } else {
                session.messages.append(message)
            }
            if message.role == .assistant && !message.isSuperseded {
                isThinking = false
                if streamingTurnID == nil || message.turnID == nil || streamingTurnID == message.turnID {
                    streamingAssistantText = nil
                    streamingTurnID = nil
                }
            }
        case .assistantDelta(let turnID, let text):
            if text.isEmpty {
                if streamingTurnID == nil || streamingTurnID == turnID {
                    streamingAssistantText = nil
                    streamingTurnID = nil
                }
            } else if !session.messages.contains(where: { $0.role == .assistant && !$0.isSuperseded && $0.turnID == turnID }) {
                streamingTurnID = turnID
                streamingAssistantText = text
            }
        case .task(_, let run):
            if let idx = session.runs.firstIndex(where: { $0.id == run.id }) {
                session.runs[idx] = run
            } else {
                session.runs.insert(run, at: 0)
            }
            await refreshWorkAndInteractions()
        case .result:
            await refreshLibrary()
        case .readPosition(_, let pos):
            session.readPosition = pos
        case .conversation(_, let presence), .presence(let presence):
            if let paused = presence.paused {
                session.paused = paused
            }
            if let conv = presence.conversation {
                session.conversation = conv
                isThinking = conv.processing || conv.pending
                if !isThinking {
                    streamingAssistantText = nil
                    streamingTurnID = nil
                }
            }
        case .memory:
            await refreshMemory()
        case .unknown:
            break
        }
    }


    @discardableResult
    public func sendMessage(_ text: String, attachments: [Attachment] = []) async -> Bool {
        let generation = sessionGeneration
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty || !attachments.isEmpty else { return false }

        let clientID = UUID().uuidString.lowercased()
        let expectedUserMessageID = "\(clientID)-user"
        let optimistic = Message(
            id: expectedUserMessageID,
            turnID: clientID,
            role: .user,
            text: trimmed,
            createdAt: ISO8601DateFormatter().string(from: Date()),
            receipt: .sending
        )
        session.messages.append(optimistic)
        isThinking = true
        lastError = nil

        do {
            let activeSelection = effectiveModelSelection
            let attachmentIDs = attachments.map(\.id)
            var updated: Session
            do {
                updated = try await transport.sendTurn(
                    id: clientID,
                    message: trimmed,
                    attachmentIDs: attachmentIDs,
                    modelSelection: activeSelection.isEmpty ? nil : activeSelection
                )
            } catch let error as NoriAPIError where !activeSelection.isEmpty && error.code == "model_selection_unavailable" {
                guard generation == sessionGeneration else { return false }
                let latest = try? await transport.fetchCapabilities()
                guard generation == sessionGeneration else { return false }
                capabilities = latest
                updated = try await transport.sendTurn(id: clientID, message: trimmed, attachmentIDs: attachmentIDs, modelSelection: nil)
            }
            guard generation == sessionGeneration else { return false }
            if !updated.messages.contains(where: { $0.id == expectedUserMessageID || $0.turnID == clientID && $0.role == .user }) {
                var savedBubble = optimistic
                savedBubble.receipt = .saved
                updated.messages.append(savedBubble)
            }
            self.session = updated
            isThinking = updated.conversation?.processing == true || updated.conversation?.pending == true
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            session.messages.removeAll { $0.id == expectedUserMessageID }
            isThinking = false
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return false
        }
    }

    @discardableResult
    public func retryMessage(_ message: Message) async -> Bool {
        let generation = sessionGeneration
        isThinking = true
        lastError = nil
        do {
            let updated = try await transport.retryTurn(
                turnID: message.turnID,
                messageID: message.id
            )
            guard generation == sessionGeneration else { return false }
            self.session = updated
            isThinking = updated.conversation?.processing == true || updated.conversation?.pending == true
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            isThinking = false
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return false
        }
    }

    public func uploadAttachment(data: Data, filename: String, mediaType: String) async throws -> Attachment {
        let generation = sessionGeneration
        let uploadID = UUID().uuidString.lowercased()
        let attachment = try await transport.uploadFile(id: uploadID, filename: filename, mediaType: mediaType, data: data)
        guard generation == sessionGeneration else { throw CancellationError() }
        return attachment
    }


    public func loadWorkDetail(taskID: String) async -> WorkDetail? {
        let generation = sessionGeneration
        do {
            let detail = try await transport.fetchWorkDetail(taskID: taskID)
            guard generation == sessionGeneration else { return nil }
            workDetails[taskID] = detail
            return detail
        } catch {
            guard generation == sessionGeneration else { return nil }
            lastError = error.localizedDescription
            return workDetails[taskID]
        }
    }

    public func resolveInteraction(_ interaction: Interaction, answer: InteractionAnswer) async {
        let generation = sessionGeneration
        let requestID = UUID().uuidString.lowercased()
        do {
            let receipt = try await transport.respondToInteraction(
                id: interaction.id,
                expectedRevision: interaction.revision,
                answer: answer,
                requestID: requestID
            )
            guard generation == sessionGeneration else { return }
            if let idx = interactions.firstIndex(where: { $0.id == interaction.id }) {
                interactions[idx] = receipt.resource
            }
            async let workAndInteractionsTask: Void = refreshWorkAndInteractions()
            async let summaryTask: Void = refreshSummary()
            let shouldRefreshPermissions: Bool
            if case .approve(_, let rememberRule) = answer, rememberRule {
                shouldRefreshPermissions = true
            } else {
                shouldRefreshPermissions = false
            }
            let activeTransport = transport
            async let permissionsTask: [PermissionRule]? = shouldRefreshPermissions
                ? (try? await activeTransport.fetchPermissions())
                : nil

            _ = await (workAndInteractionsTask, summaryTask)
            let freshPermissions = await permissionsTask
            guard generation == sessionGeneration else { return }
            if let freshPermissions { self.permissions = freshPermissions }

            switch answer {
            case .approve(_, let rememberRule):
                showToast(
                    rememberRule
                        ? "Always allowed — rule saved & Nori is completing the action"
                        : "Approved — Nori is completing the action"
                )
            case .decline: showToast("Declined — nothing was sent")
            case .answer: showToast("Answer sent to Nori")
            case .chooseAccount: showToast("Account selected")
            }
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func approveInteraction(_ interaction: Interaction, rememberRule: Bool = false) async {
        await resolveInteraction(
            interaction,
            answer: .approve(actionDigest: interaction.actionDigest ?? "", rememberRule: rememberRule)
        )
    }

    public func declineInteraction(_ interaction: Interaction) async {
        await resolveInteraction(interaction, answer: .decline(actionDigest: interaction.actionDigest))
    }

    public func answerInteraction(_ interaction: Interaction, text: String, optionIDs: [String]? = nil) async {
        await resolveInteraction(interaction, answer: .answer(text: text, optionIDs: optionIDs))
    }

    public func chooseAccountInteraction(_ interaction: Interaction, connectionID: String, rememberAsDefault: Bool = false) async {
        await resolveInteraction(interaction, answer: .chooseAccount(connectionID: connectionID, rememberAsDefault: rememberAsDefault))
    }

    public func steerTask(taskID: String, instruction: String) async {
        let generation = sessionGeneration
        let trimmed = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        do {
            let cmd = TaskCommand(id: UUID().uuidString.lowercased(), runID: taskID, kind: .steer, text: trimmed)
            _ = try await transport.sendTaskCommand(cmd)
            guard generation == sessionGeneration else { return }
            let detail = try? await transport.fetchWorkDetail(taskID: taskID)
            guard generation == sessionGeneration else { return }
            if let detail {
                workDetails[taskID] = detail
            }
            let items = try? await transport.fetchWorkItems(bucket: nil)
            guard generation == sessionGeneration else { return }
            if let items {
                workItems = items
            }
            showToast("Steered task")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func stopTask(taskID: String) async {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.stopWork(taskID: taskID, requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { return }
            if let idx = workItems.firstIndex(where: { $0.taskID == taskID }) {
                workItems[idx] = receipt.resource
            }
            let detail = try? await transport.fetchWorkDetail(taskID: taskID)
            guard generation == sessionGeneration else { return }
            if let detail {
                workDetails[taskID] = detail
            }
            showToast("Task stopped")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func continueTask(taskID: String, budgetUSD: Double = 2.00) async {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.continueWork(taskID: taskID, budgetUSD: budgetUSD, requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { return }
            workItems.insert(receipt.resource, at: 0)
            let detail = try? await transport.fetchWorkDetail(taskID: receipt.resource.taskID)
            guard generation == sessionGeneration else { return }
            if let detail {
                workDetails[receipt.resource.taskID] = detail
            }
            showToast("Continuing with $\(String(format: "%.2f", budgetUSD)) budget")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }


    public func addMCPServer(
        name: String,
        endpoint: String,
        auth: MCPAuthKind = .oauth,
        defaultExposure: MCPToolExposure = .discoverable
    ) async {
        let generation = sessionGeneration
        let cleanName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanEndpoint = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanName.isEmpty, !cleanEndpoint.isEmpty else { return }
        do {
            let input = MCPServerInput(
                name: cleanName,
                transport: .http,
                endpoint: cleanEndpoint,
                auth: auth,
                enabled: true,
                exposure: MCPExposureConfig(default: defaultExposure)
            )
            let receipt = try await transport.createMCPServer(input: input, requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { return }
            mcpServers.append(receipt.resource)
            let tools = try? await transport.fetchMCPTools(serverID: receipt.resource.id)
            guard generation == sessionGeneration else { return }
            if let tools {
                mcpTools[receipt.resource.id] = tools
            }
            showToast("Added \(cleanName)")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func setToolExposure(serverID: String, toolName: String, exposure: MCPToolExposure) async {
        let generation = sessionGeneration
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return }
        do {
            let receipt = try await transport.setMCPToolExposure(
                serverID: serverID,
                expectedRevision: server.revision,
                toolName: toolName,
                exposure: exposure,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            let tools = try? await transport.fetchMCPTools(serverID: serverID)
            guard generation == sessionGeneration else { return }
            if let tools {
                mcpTools[serverID] = tools
            }
            showToast("Tool set to \(exposure.displayLabel)")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func reconnectMCPServer(serverID: String) async {
        let generation = sessionGeneration
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return }
        do {
            let receipt = try await transport.performMCPAction(
                serverID: serverID,
                expectedRevision: server.revision,
                action: "reconnect",
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            let tools = try? await transport.fetchMCPTools(serverID: serverID)
            guard generation == sessionGeneration else { return }
            if let tools {
                mcpTools[serverID] = tools
            }
            showToast("Reconnected \(server.name)")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    @discardableResult
    public func startMCPOAuth(serverID: String) async -> AuthAttempt? {
        let generation = sessionGeneration
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return nil }
        do {
            let receipt = try await transport.performMCPAction(
                serverID: serverID,
                expectedRevision: server.revision,
                action: "sign_in",
                returnTo: NoriConfig.oauthCallbackURL,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            let tools = try? await transport.fetchMCPTools(serverID: serverID)
            guard generation == sessionGeneration else { return nil }
            if let tools {
                mcpTools[serverID] = tools
            }
            if receipt.auth == nil {
                showToast("Signed in to \(server.name)")
            }
            return receipt.auth
        } catch {
            guard generation == sessionGeneration else { return nil }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return nil
        }
    }

    @discardableResult
    public func completeMCPOAuth(serverID: String, callbackURL: URL? = nil) async -> AuthAttempt? {
        let generation = sessionGeneration
        if callbackURL != nil {
            async let appsTask: Void = refreshApps()
            async let workTask: Void = refreshWorkAndInteractions()
            async let summaryTask: Void = refreshSummary()
            _ = await (appsTask, workTask, summaryTask)
            guard generation == sessionGeneration else { return nil }
            if let server = mcpServers.first(where: { $0.id == serverID }),
               server.observed.status == .connected {
                showToast("Signed in to \(server.name)")
            }
            return nil
        }
        return await startMCPOAuth(serverID: serverID)
    }

    @discardableResult
    public func saveMCPCredential(serverID: String, secret: String) async -> Bool {
        let generation = sessionGeneration
        let trimmed = secret.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty,
              let server = mcpServers.first(where: { $0.id == serverID }) else {
            return false
        }
        do {
            let receipt = try await transport.setMCPCredential(
                serverID: serverID,
                expectedRevision: server.revision,
                secret: trimmed,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return false }
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            let tools = try? await transport.fetchMCPTools(serverID: serverID)
            guard generation == sessionGeneration else { return false }
            if let tools {
                mcpTools[serverID] = tools
            }
            showToast("Saved credential for \(server.name)")
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return false
        }
    }

    @discardableResult
    public func connectConnection(connectionID: String) async -> AuthAttempt? {
        let generation = sessionGeneration
        guard let conn = connections.first(where: { $0.id == connectionID }) else { return nil }
        do {
            let receipt = try await transport.performConnectionAction(
                connectionID: connectionID,
                expectedRevision: conn.revision,
                action: "connect",
                returnTo: NoriConfig.oauthCallbackURL,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let idx = connections.firstIndex(where: { $0.id == connectionID }) {
                connections[idx] = receipt.resource
            }
            if receipt.auth == nil {
                showToast("Connected \(conn.title)")
            }
            return receipt.auth
        } catch {
            guard generation == sessionGeneration else { return nil }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return nil
        }
    }


    public func loadLibraryItem(id: String) async -> LibraryItem? {
        let generation = sessionGeneration
        do {
            let item = try await transport.fetchLibraryItem(id: id)
            guard generation == sessionGeneration else { return nil }
            if let idx = libraryItems.firstIndex(where: { $0.id == item.id }) {
                var merged = item
                if merged.bodyText == nil {
                    merged.bodyText = libraryItems[idx].bodyText
                }
                libraryItems[idx] = merged
                return merged
            } else {
                libraryItems.insert(item, at: 0)
                return item
            }
        } catch {
            guard generation == sessionGeneration else { return nil }
            lastError = error.localizedDescription
            return libraryItems.first(where: { $0.id == id })
        }
    }

    public func loadLibraryItemFile(id: String, format: String? = nil) async throws -> LibraryFilePayload {
        let generation = sessionGeneration
        let payload = try await transport.fetchLibraryItemFile(id: id, format: format)
        guard generation == sessionGeneration else { throw CancellationError() }
        if payload.mediaType.lowercased().hasPrefix("text/"),
           let text = payload.utf8Text,
           let idx = libraryItems.firstIndex(where: { $0.id == id }) {
            libraryItems[idx].bodyText = text
        }
        return payload
    }

    public func searchLibrary(query: String) async {
        let generation = sessionGeneration
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            searchHits = []
            return
        }
        do {
            let response = try await transport.search(query: trimmed)
            guard generation == sessionGeneration else { return }
            searchHits = response.items
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
        }
    }

    @discardableResult
    public func reviseDocument(itemID: String, instruction: String) async -> LibraryRevisionResult? {
        let generation = sessionGeneration
        let trimmed = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        let item: LibraryItem
        if let cached = libraryItems.first(where: { $0.id == itemID }) {
            item = cached
        } else if let loaded = await loadLibraryItem(id: itemID) {
            item = loaded
        } else {
            return nil
        }
        do {
            let receipt = try await transport.reviseLibraryItem(
                id: itemID,
                expectedRevision: item.revision,
                instruction: trimmed,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let idx = libraryItems.firstIndex(where: { $0.id == itemID }) {
                libraryItems[idx] = receipt.resource
            } else {
                libraryItems.insert(receipt.resource, at: 0)
            }
            if receipt.status == .queued {
                showToast("Revision started")
                await refreshWorkAndInteractions()
                guard generation == sessionGeneration else { return nil }
            } else {
                showToast("Revision saved (v\(receipt.resource.version))")
            }
            return LibraryRevisionResult(
                status: receipt.status,
                taskID: receipt.work?.taskID,
                item: receipt.resource
            )
        } catch {
            guard generation == sessionGeneration else { return nil }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return nil
        }
    }


    private func upsertAutomation(_ automation: Automation) {
        if let idx = automations.firstIndex(where: { $0.id == automation.id }) {
            automations[idx] = automation
        } else {
            automations.insert(automation, at: 0)
        }
    }

    public func loadAutomation(id: String) async -> Automation? {
        let generation = sessionGeneration
        do {
            let fresh = try await transport.fetchAutomation(id: id)
            guard generation == sessionGeneration else { return nil }
            upsertAutomation(fresh)
            return fresh
        } catch {
            guard generation == sessionGeneration else { return nil }
            return automations.first(where: { $0.id == id })
        }
    }

    @discardableResult
    public func cancelAutomation(automationID: String) async -> Bool {
        let generation = sessionGeneration
        let target: Automation
        if let fresh = await loadAutomation(id: automationID) {
            target = fresh
        } else {
            guard generation == sessionGeneration else { return false }
            showToast("Reminder not found")
            return false
        }
        do {
            let receipt = try await transport.performAutomationAction(
                id: automationID,
                expectedRevision: target.revision,
                action: "cancel",
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return false }
            upsertAutomation(receipt.resource)
            showToast("Reminder cancelled")
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return false
        }
    }

    @discardableResult
    public func updateReminder(automationID: String, title: String, scheduledAt: Date) async -> Automation? {
        let generation = sessionGeneration
        let cleanTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanTitle.isEmpty else { return nil }
        guard let current = await loadAutomation(id: automationID) else {
            guard generation == sessionGeneration else { return nil }
            showToast("Reminder not found")
            return nil
        }

        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        let isoAt = formatter.string(from: scheduledAt)

        var editable = current.editable
        let previousTitle = current.title.trimmingCharacters(in: .whitespacesAndNewlines)
        editable.title = cleanTitle
        if editable.instruction.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            || editable.instruction.trimmingCharacters(in: .whitespacesAndNewlines) == previousTitle {
            editable.instruction = cleanTitle
        }
        editable.trigger = AutomationTrigger(kind: "once", at: isoAt, timezone: nil)

        do {
            let receipt = try await transport.updateAutomation(
                id: automationID,
                expectedRevision: current.revision,
                value: editable,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            upsertAutomation(receipt.resource)
            showToast("Reminder updated")
            return receipt.resource
        } catch {
            guard generation == sessionGeneration else { return nil }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return nil
        }
    }

    public func toggleAutomation(automationID: String) async {
        let generation = sessionGeneration
        guard let auto = automations.first(where: { $0.id == automationID }) else { return }
        let action = auto.status == .enabled ? "pause" : "resume"
        do {
            let receipt = try await transport.performAutomationAction(
                id: automationID,
                expectedRevision: auto.revision,
                action: action,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if let idx = automations.firstIndex(where: { $0.id == automationID }) {
                automations[idx] = receipt.resource
            }
            showToast(receipt.resource.status == .enabled ? "Resumed \(auto.title)" : "Paused \(auto.title)")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func runAutomationNow(automationID: String) async {
        let generation = sessionGeneration
        guard let auto = automations.first(where: { $0.id == automationID }) else { return }
        do {
            let receipt = try await transport.performAutomationAction(
                id: automationID,
                expectedRevision: auto.revision,
                action: "run_now",
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if let idx = automations.firstIndex(where: { $0.id == automationID }) {
                automations[idx] = receipt.resource
            }
            let items = try? await transport.fetchWorkItems(bucket: nil)
            guard generation == sessionGeneration else { return }
            if let items {
                workItems = items
            }
            showToast("Running \"\(auto.title)\" now")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func createPermissionRule(_ rule: PermissionRuleEditable) async -> Bool {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.createPermissionRule(rule, requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { return false }
            if let idx = permissions.firstIndex(where: { $0.id == receipt.resource.id }) {
                permissions[idx] = receipt.resource
            } else {
                permissions.insert(receipt.resource, at: 0)
            }
            showToast("Permission rule saved")
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return false
        }
    }

    public func revokePermissionRule(id: String) async {
        let generation = sessionGeneration
        guard let rule = permissions.first(where: { $0.id == id }) else { return }
        do {
            _ = try await transport.revokePermissionRule(
                id: id,
                expectedRevision: rule.revision,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            permissions.removeAll { $0.id == id }
            showToast("Permission rule removed")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func updatePreferences(_ updated: Preferences) async {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.savePreferences(updated, requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { return }
            preferences = receipt.resource
            showToast("Preferences saved")
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }


    public func pingServer() async -> HealthCheckResult {
        let generation = sessionGeneration
        let result = await transport.ping()
        guard generation == sessionGeneration else { return result }
        lastPing = result
        connectionState = result.ok ? .connected : .offline
        return result
    }


    public func handleIncomingURL(_ url: URL) {
        guard let link = NoriDeepLink.parse(url) else { return }
        if case .oauthCallback = link {
            showToast("Sign-in callback received")
            Task {
                async let appsTask: Void = refreshApps()
                async let workTask: Void = refreshWorkAndInteractions()
                async let summaryTask: Void = refreshSummary()
                _ = await (appsTask, workTask, summaryTask)
            }
        }
        pendingDeepLink = link
    }

    public func showToast(_ message: String) {
        toastTask?.cancel()
        toastMessage = message
        toastTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(2.4))
            guard let self, !Task.isCancelled else { return }
            self.toastMessage = nil
        }
    }
}
