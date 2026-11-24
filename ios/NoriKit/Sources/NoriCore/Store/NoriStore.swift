import Foundation
import Observation

// MARK: - Navigation & Status Types

public enum NoriTab: String, Sendable, Hashable, CaseIterable, Identifiable {
    case chat
    case work
    case apps
    case library
    case more

    public var id: String { rawValue }
}

public enum NoriDeepLink: Sendable, Hashable {
    case tab(NoriTab)
    case activitySheet
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
            if let name = parts.first?.lowercased(), let tab = NoriTab(rawValue: name) {
                return .tab(tab)
            }
        case "chat": return .tab(.chat)
        case "work":
            if let taskID = parts.first, !taskID.isEmpty {
                return .workDetail(taskID)
            }
            return .tab(.work)
        case "apps": return .tab(.apps)
        case "library":
            if let itemID = parts.first, !itemID.isEmpty {
                return .document(itemID)
            }
            return .tab(.library)
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
    case demo
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

// MARK: - NoriStore

@Observable
@MainActor
public final class NoriStore {
    // MARK: Connection & Profile State
    public var profile: ServerProfile
    public var savedProfiles: [ServerProfile]
    public var connectionState: ConnectionState
    public var capabilities: Capabilities?
    public var lastPing: HealthCheckResult?

    // MARK: Domain State
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

    // MARK: UI Coordination State
    public var isLoading: Bool = false
    public var isThinking: Bool = false
    public var isComposerFocused: Bool = false
    public var lastError: String?
    public var toastMessage: String?
    public var pendingDeepLink: NoriDeepLink?

    // MARK: Internals
    private let profileStore: ServerProfileStore
    private let keychain: KeychainStore
    private var transport: any NoriTransport
    private var eventTask: Task<Void, Never>?
    private var toastTask: Task<Void, Never>?
    private var hasBootstrapped: Bool = false

    public init(
        profile: ServerProfile? = nil,
        transport: (any NoriTransport)? = nil,
        profileStore: ServerProfileStore = ServerProfileStore(),
        keychain: KeychainStore = KeychainStore()
    ) {
        self.profileStore = profileStore
        self.keychain = keychain
        let initialProfile = profile ?? profileStore.loadActiveProfile()
        self.profile = initialProfile
        self.savedProfiles = profileStore.loadProfiles()
        self.connectionState = initialProfile.mode == .demo ? .demo : .connecting

        if let transport {
            self.transport = transport
        } else {
            self.transport = Self.makeTransport(for: initialProfile, keychain: keychain)
        }

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

    private static func makeTransport(for profile: ServerProfile, keychain: KeychainStore) -> any NoriTransport {
        switch profile.mode {
        case .demo:
            return DemoNoriTransport()
        case .directCore, .hostedBFF:
            if let live = try? LiveNoriTransport(profile: profile, keychain: keychain) {
                return live
            }
            return DemoNoriTransport()
        }
    }

    // MARK: - Derived Properties

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
        pendingInteractions.count
    }

    public var appsAttentionCount: Int {
        let mcpCount = mcpServers.filter { $0.observed.status.needsAttention }.count
        let connCount = connections.filter { $0.status == .needsAttention }.count
        return mcpCount + connCount
    }

    public var activeWorkItems: [WorkItem] {
        workItems.filter { $0.status == .running }
    }

    public var finishedWorkItems: [WorkItem] {
        workItems.filter { $0.status != .running }
    }

    public var activeTasksCount: Int {
        activeWorkItems.count
    }

    public var statusLine: StatusLine {
        if connectionState == .reconnecting || connectionState == .offline {
            return StatusLine(text: "Reconnecting…", tone: .offline)
        }
        if isThinking {
            return StatusLine(text: "Thinking…", tone: .normal)
        }
        if needsYouCount > 0 {
            let label = needsYouCount == 1 ? "1 thing needs you" : "\(needsYouCount) things need you"
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

    public var isDemoMode: Bool {
        profile.mode == .demo
    }

    // MARK: - Lifecycle & Snapshot Sync

    public func bootstrapIfNeeded() async {
        guard !hasBootstrapped else { return }
        hasBootstrapped = true
        await refreshAll()
        startEventStream()
    }

    public func refreshAll() async {
        isLoading = true
        defer { isLoading = false }
        if profile.mode != .demo {
            connectionState = .connecting
        }
        do {
            let snapshot = try await transport.fetchFullSnapshot()
            applySnapshot(snapshot)
            connectionState = profile.mode == .demo ? .demo : .connected
            lastError = nil
        } catch {
            lastError = error.localizedDescription
            if profile.mode != .demo {
                connectionState = .reconnecting
            }
        }
    }

    private func applySnapshot(_ snapshot: NoriFullSnapshot) {
        self.capabilities = snapshot.capabilities
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
        guard profile.mode != .demo else { return }
        let startPosition = session.position ?? 0
        let activeTransport = transport

        eventTask = Task { [weak self] in
            let stream = activeTransport.eventStream(after: startPosition)
            do {
                for try await event in stream {
                    guard let self, !Task.isCancelled else { break }
                    await self.handleStreamEvent(event)
                }
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
        connectionState = profile.mode == .demo ? .demo : .connected
        if let seq = event.sequence, seq > (session.position ?? 0) {
            session.position = seq
        }
        switch event {
        case .reset:
            await refreshAll()
        case .message(_, let message):
            if let idx = session.messages.firstIndex(where: { $0.id == message.id }) {
                session.messages[idx] = message
            } else {
                session.messages.append(message)
            }
            if message.role == .assistant {
                isThinking = false
            }
        case .task(_, let run):
            if let idx = session.runs.firstIndex(where: { $0.id == run.id }) {
                session.runs[idx] = run
            } else {
                session.runs.insert(run, at: 0)
            }
            if let freshWork = try? await transport.fetchWorkItems(bucket: nil) {
                self.workItems = freshWork
            }
            if let freshInteractions = try? await transport.fetchInteractions() {
                self.interactions = freshInteractions
            }
        case .result:
            if let items = try? await transport.fetchLibrary() {
                self.libraryItems = items
            }
        case .readPosition(_, let pos):
            session.readPosition = pos
        case .conversation(_, let presence), .presence(let presence):
            if let paused = presence.paused {
                session.paused = paused
            }
            if let conv = presence.conversation {
                session.conversation = conv
                isThinking = conv.processing || conv.pending
            }
        case .memory:
            if let facts = try? await transport.fetchCoreMemories() {
                self.coreMemories = facts
            }
            if let working = try? await transport.fetchWorkingMemory() {
                self.workingMemory = working
            }
        case .unknown:
            break
        }
    }

    // MARK: - Chat & Uploads

    public func sendMessage(_ text: String, attachments: [Attachment] = []) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty || !attachments.isEmpty else { return }

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
            var updated = try await transport.sendTurn(
                id: clientID,
                message: trimmed,
                attachmentIDs: attachments.map(\.id)
            )
            // Keep the confirmed user message visible while the core worker processes the turn.
            if profile.mode != .demo,
               !updated.messages.contains(where: { $0.id == expectedUserMessageID || $0.turnID == clientID && $0.role == .user }) {
                var savedBubble = optimistic
                savedBubble.receipt = .saved
                updated.messages.append(savedBubble)
            }
            self.session = updated
            if profile.mode == .demo {
                isThinking = false
            } else {
                isThinking = updated.conversation?.processing == true || updated.conversation?.pending == true
            }
        } catch {
            session.messages.removeAll { $0.id == expectedUserMessageID }
            isThinking = false
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func uploadAttachment(data: Data, filename: String, mediaType: String) async throws -> Attachment {
        let uploadID = UUID().uuidString.lowercased()
        return try await transport.uploadFile(id: uploadID, filename: filename, mediaType: mediaType, data: data)
    }

    // MARK: - Work & Interactions

    public func loadWorkDetail(taskID: String) async -> WorkDetail? {
        do {
            let detail = try await transport.fetchWorkDetail(taskID: taskID)
            workDetails[taskID] = detail
            return detail
        } catch {
            lastError = error.localizedDescription
            return workDetails[taskID]
        }
    }

    public func resolveInteraction(_ interaction: Interaction, answer: InteractionAnswer) async {
        let requestID = UUID().uuidString.lowercased()
        do {
            let receipt = try await transport.respondToInteraction(
                id: interaction.id,
                expectedRevision: interaction.revision,
                answer: answer,
                requestID: requestID
            )
            if let idx = interactions.firstIndex(where: { $0.id == interaction.id }) {
                interactions[idx] = receipt.resource
            }
            // Refresh linked state cleanly
            let snapshot = try await transport.fetchFullSnapshot()
            applySnapshot(snapshot)

            switch answer {
            case .approve: showToast("Approved — Nori is completing the action")
            case .decline: showToast("Declined — nothing was sent")
            case .answer: showToast("Answer sent to Nori")
            case .chooseAccount: showToast("Account selected")
            }
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func approveInteraction(_ interaction: Interaction) async {
        await resolveInteraction(interaction, answer: .approve(actionDigest: interaction.actionDigest ?? ""))
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
        let trimmed = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        do {
            let cmd = TaskCommand(id: UUID().uuidString.lowercased(), runID: taskID, kind: .steer, text: trimmed)
            _ = try await transport.sendTaskCommand(cmd)
            if let detail = try? await transport.fetchWorkDetail(taskID: taskID) {
                workDetails[taskID] = detail
            }
            if let items = try? await transport.fetchWorkItems(bucket: nil) {
                workItems = items
            }
            showToast("Steered task")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func stopTask(taskID: String) async {
        do {
            let receipt = try await transport.stopWork(taskID: taskID, requestID: UUID().uuidString.lowercased())
            if let idx = workItems.firstIndex(where: { $0.taskID == taskID }) {
                workItems[idx] = receipt.resource
            }
            if let detail = try? await transport.fetchWorkDetail(taskID: taskID) {
                workDetails[taskID] = detail
            }
            showToast("Task stopped")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func continueTask(taskID: String, budgetUSD: Double = 2.00) async {
        do {
            let receipt = try await transport.continueWork(taskID: taskID, budgetUSD: budgetUSD, requestID: UUID().uuidString.lowercased())
            workItems.insert(receipt.resource, at: 0)
            if let detail = try? await transport.fetchWorkDetail(taskID: receipt.resource.taskID) {
                workDetails[receipt.resource.taskID] = detail
            }
            showToast("Continuing with $\(String(format: "%.2f", budgetUSD)) budget")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    // MARK: - Apps: MCP & Connections

    public func addMCPServer(
        name: String,
        endpoint: String,
        auth: MCPAuthKind = .oauth,
        defaultExposure: MCPToolExposure = .discoverable
    ) async {
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
            mcpServers.append(receipt.resource)
            if let tools = try? await transport.fetchMCPTools(serverID: receipt.resource.id) {
                mcpTools[receipt.resource.id] = tools
            }
            showToast("Added \(cleanName)")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func setToolExposure(serverID: String, toolName: String, exposure: MCPToolExposure) async {
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return }
        do {
            let receipt = try await transport.setMCPToolExposure(
                serverID: serverID,
                expectedRevision: server.revision,
                toolName: toolName,
                exposure: exposure,
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            if let tools = try? await transport.fetchMCPTools(serverID: serverID) {
                mcpTools[serverID] = tools
            }
            showToast("Tool set to \(exposure.displayLabel)")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func reconnectMCPServer(serverID: String) async {
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return }
        do {
            let receipt = try await transport.performMCPAction(
                serverID: serverID,
                expectedRevision: server.revision,
                action: "reconnect",
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            if let tools = try? await transport.fetchMCPTools(serverID: serverID) {
                mcpTools[serverID] = tools
            }
            showToast("Reconnected \(server.name)")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func completeMCPOAuth(serverID: String, callbackURL: URL? = nil) async -> AuthAttempt? {
        guard let server = mcpServers.first(where: { $0.id == serverID }) else { return nil }
        do {
            let receipt = try await transport.performMCPAction(
                serverID: serverID,
                expectedRevision: server.revision,
                action: "sign_in",
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = mcpServers.firstIndex(where: { $0.id == serverID }) {
                mcpServers[idx] = receipt.resource
            }
            if let tools = try? await transport.fetchMCPTools(serverID: serverID) {
                mcpTools[serverID] = tools
            }
            if receipt.auth == nil {
                showToast("Signed in to \(server.name)")
            }
            return receipt.auth
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return nil
        }
    }

    public func connectConnection(connectionID: String) async {
        guard let conn = connections.first(where: { $0.id == connectionID }) else { return }
        do {
            let receipt = try await transport.performConnectionAction(
                connectionID: connectionID,
                expectedRevision: conn.revision,
                action: "connect",
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = connections.firstIndex(where: { $0.id == connectionID }) {
                connections[idx] = receipt.resource
            }
            showToast("Connected \(conn.title)")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    // MARK: - Library & Search

    public func searchLibrary(query: String) async {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            searchHits = []
            return
        }
        do {
            let response = try await transport.search(query: trimmed)
            searchHits = response.items
        } catch {
            lastError = error.localizedDescription
        }
    }

    public func reviseDocument(itemID: String, instruction: String) async {
        let trimmed = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, let item = libraryItems.first(where: { $0.id == itemID }) else { return }
        do {
            let receipt = try await transport.reviseLibraryItem(
                id: itemID,
                expectedRevision: item.revision,
                instruction: trimmed,
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = libraryItems.firstIndex(where: { $0.id == itemID }) {
                libraryItems[idx] = receipt.resource
            }
            showToast("Revision saved (v\(receipt.resource.version))")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    // MARK: - 3-Layer Memory

    public func addMemory(text: String, section: String? = "Preferences", about: String? = nil, pinned: Bool = false) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        do {
            let receipt = try await transport.createMemory(
                text: trimmed,
                section: section,
                about: about,
                pinned: pinned,
                requestID: UUID().uuidString.lowercased()
            )
            coreMemories.insert(receipt.resource, at: 0)
            if let working = try? await transport.fetchWorkingMemory() {
                workingMemory = working
            }
            showToast("Saved to Core Memory")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func togglePinMemory(memoryID: String) async {
        guard let fact = coreMemories.first(where: { $0.id == memoryID }) else { return }
        let action = fact.pinned ? "unpin" : "pin"
        do {
            let receipt = try await transport.performMemoryAction(
                id: memoryID,
                expectedRevision: fact.revision,
                action: action,
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = coreMemories.firstIndex(where: { $0.id == memoryID }) {
                coreMemories[idx] = receipt.resource
            }
            showToast(receipt.resource.pinned ? "Pinned to Core Memory" : "Unpinned memory")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func demoteMemory(memoryID: String) async {
        guard let fact = coreMemories.first(where: { $0.id == memoryID }) else { return }
        do {
            _ = try await transport.performMemoryAction(
                id: memoryID,
                expectedRevision: fact.revision,
                action: "demote",
                requestID: UUID().uuidString.lowercased()
            )
            coreMemories.removeAll { $0.id == memoryID }
            if let logs = try? await transport.fetchMemoryLogs() {
                memoryLogs = logs
            }
            if let working = try? await transport.fetchWorkingMemory() {
                workingMemory = working
            }
            showToast("Moved to Daily Log")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func forgetMemory(memoryID: String) async {
        guard let fact = coreMemories.first(where: { $0.id == memoryID }) else { return }
        do {
            _ = try await transport.performMemoryAction(
                id: memoryID,
                expectedRevision: fact.revision,
                action: "forget",
                requestID: UUID().uuidString.lowercased()
            )
            coreMemories.removeAll { $0.id == memoryID }
            if let working = try? await transport.fetchWorkingMemory() {
                workingMemory = working
            }
            showToast("Memory forgotten")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func promoteLogEntry(date: String, entryID: String, pinned: Bool = false) async {
        guard let log = memoryLogs.first(where: { $0.date == date }) else { return }
        do {
            let receipt = try await transport.performMemoryLogEntryAction(
                date: date,
                entryID: entryID,
                expectedRevision: log.revision,
                action: "promote",
                pinned: pinned,
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = memoryLogs.firstIndex(where: { $0.date == date }) {
                memoryLogs[idx] = receipt.resource
            }
            if let facts = try? await transport.fetchCoreMemories() {
                coreMemories = facts
            }
            if let working = try? await transport.fetchWorkingMemory() {
                workingMemory = working
            }
            showToast("Promoted to Core Memory")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func forgetLogEntry(date: String, entryID: String) async {
        guard let log = memoryLogs.first(where: { $0.date == date }) else { return }
        do {
            let receipt = try await transport.performMemoryLogEntryAction(
                date: date,
                entryID: entryID,
                expectedRevision: log.revision,
                action: "forget",
                pinned: false,
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = memoryLogs.firstIndex(where: { $0.date == date }) {
                memoryLogs[idx] = receipt.resource
            }
            showToast("Removed log entry")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func promoteWorkingMemoryItem(itemID: String) async {
        do {
            let receipt = try await transport.performWorkingMemoryAction(
                itemID: itemID,
                expectedRevision: workingMemory.revision,
                action: "promote",
                requestID: UUID().uuidString.lowercased()
            )
            workingMemory = receipt.resource
            if let facts = try? await transport.fetchCoreMemories() {
                coreMemories = facts
            }
            showToast("Saved to Core Memory")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func dismissWorkingMemoryItem(itemID: String) async {
        do {
            let receipt = try await transport.performWorkingMemoryAction(
                itemID: itemID,
                expectedRevision: workingMemory.revision,
                action: "dismiss",
                requestID: UUID().uuidString.lowercased()
            )
            workingMemory = receipt.resource
            showToast("Dismissed working note")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    // MARK: - Automations & Preferences

    public func toggleAutomation(automationID: String) async {
        guard let auto = automations.first(where: { $0.id == automationID }) else { return }
        let action = auto.status == .enabled ? "pause" : "resume"
        do {
            let receipt = try await transport.performAutomationAction(
                id: automationID,
                expectedRevision: auto.revision,
                action: action,
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = automations.firstIndex(where: { $0.id == automationID }) {
                automations[idx] = receipt.resource
            }
            showToast(receipt.resource.status == .enabled ? "Resumed \(auto.title)" : "Paused \(auto.title)")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func runAutomationNow(automationID: String) async {
        guard let auto = automations.first(where: { $0.id == automationID }) else { return }
        do {
            let receipt = try await transport.performAutomationAction(
                id: automationID,
                expectedRevision: auto.revision,
                action: "run_now",
                requestID: UUID().uuidString.lowercased()
            )
            if let idx = automations.firstIndex(where: { $0.id == automationID }) {
                automations[idx] = receipt.resource
            }
            if let items = try? await transport.fetchWorkItems(bucket: nil) {
                workItems = items
            }
            showToast("Running \"\(auto.title)\" now")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func updatePreferences(_ updated: Preferences) async {
        do {
            let receipt = try await transport.savePreferences(updated, requestID: UUID().uuidString.lowercased())
            preferences = receipt.resource
            showToast("Preferences saved")
        } catch {
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    // MARK: - Server Connection, Keychain Token & Profile Switching

    public func pingServer() async -> HealthCheckResult {
        let result = await transport.ping()
        lastPing = result
        if result.ok {
            connectionState = profile.mode == .demo ? .demo : .connected
        } else if profile.mode != .demo {
            connectionState = .offline
        }
        return result
    }

    public func saveBearerToken(_ token: String, for profileID: String? = nil) {
        let account = profileID ?? profile.id
        if token.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            keychain.deleteToken(account: account)
        } else {
            keychain.saveToken(token, account: account)
        }
    }

    public func loadBearerToken(for profileID: String? = nil) -> String {
        keychain.loadToken(account: profileID ?? profile.id) ?? ""
    }

    public func switchProfile(_ newProfile: ServerProfile) async {
        stopEventStream()
        profile = newProfile
        profileStore.saveActiveProfile(newProfile)
        if let idx = savedProfiles.firstIndex(where: { $0.id == newProfile.id }) {
            savedProfiles[idx] = newProfile
        } else {
            savedProfiles.append(newProfile)
        }
        profileStore.saveProfiles(savedProfiles)
        transport = Self.makeTransport(for: newProfile, keychain: keychain)
        connectionState = newProfile.mode == .demo ? .demo : .connecting
        lastPing = nil
        await refreshAll()
        startEventStream()
        showToast("Switched to \(newProfile.name)")
    }

    public func triggerDemoApproval() async {
        guard let demo = transport as? DemoNoriTransport else {
            showToast("Demo approval is available in Demo Mode")
            return
        }
        let created = await demo.triggerDemoApproval()
        if let snapshot = try? await demo.fetchFullSnapshot() {
            applySnapshot(snapshot)
        }
        showToast("New approval queued: \(created.taskTitle ?? "Action")")
    }

    // MARK: - Incoming URL & Toast

    public func handleIncomingURL(_ url: URL) {
        guard let link = NoriDeepLink.parse(url) else { return }
        if case .oauthCallback = link {
            showToast("Sign-in callback received")
            Task { await refreshAll() }
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
