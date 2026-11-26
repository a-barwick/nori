import Foundation

public struct NoriFullSnapshot: Sendable, Hashable {
    public var capabilities: Capabilities?
    public var session: Session
    public var workItems: [WorkItem]
    public var interactions: [Interaction]
    var didLoadWorkItems: Bool
    var didLoadInteractions: Bool
    public var mcpServers: [MCPServer]
    public var mcpTools: [String: [MCPServerTool]]
    public var connections: [Connection]
    public var libraryItems: [LibraryItem]
    public var coreMemories: [MemoryFact]
    public var memoryLogs: [MemoryLog]
    public var workingMemory: WorkingMemory
    var didLoadWorkingMemory: Bool
    public var automations: [Automation]
    public var goals: [Goal]
    public var permissions: [PermissionRule]
    public var preferences: Preferences
    var didLoadPreferences: Bool
    public var modelKeys: ModelKeysResponse

    public init(
        capabilities: Capabilities? = nil,
        session: Session = Session(),
        workItems: [WorkItem] = [],
        interactions: [Interaction] = [],
        mcpServers: [MCPServer] = [],
        mcpTools: [String: [MCPServerTool]] = [:],
        connections: [Connection] = [],
        libraryItems: [LibraryItem] = [],
        coreMemories: [MemoryFact] = [],
        memoryLogs: [MemoryLog] = [],
        workingMemory: WorkingMemory = WorkingMemory(),
        automations: [Automation] = [],
        goals: [Goal] = [],
        permissions: [PermissionRule] = [],
        preferences: Preferences = Preferences(),
        modelKeys: ModelKeysResponse = ModelKeysResponse(),
        didLoadWorkItems: Bool = true,
        didLoadInteractions: Bool = true,
        didLoadWorkingMemory: Bool = true,
        didLoadPreferences: Bool = true
    ) {
        self.capabilities = capabilities
        self.session = session
        self.workItems = workItems
        self.interactions = interactions
        self.didLoadWorkItems = didLoadWorkItems
        self.didLoadInteractions = didLoadInteractions
        self.mcpServers = mcpServers
        self.mcpTools = mcpTools
        self.connections = connections
        self.libraryItems = libraryItems
        self.coreMemories = coreMemories
        self.memoryLogs = memoryLogs
        self.workingMemory = workingMemory
        self.didLoadWorkingMemory = didLoadWorkingMemory
        self.automations = automations
        self.goals = goals
        self.permissions = permissions
        self.preferences = preferences
        self.didLoadPreferences = didLoadPreferences
        self.modelKeys = modelKeys
    }
}

public protocol NoriTransport: Sendable {
    func ping() async -> HealthCheckResult
    func fetchCapabilities() async throws -> Capabilities
    func fetchAssistantSummary() async throws -> AssistantSummary
    func fetchSession(before cursor: String?) async throws -> Session
    func fetchFullSnapshot() async throws -> NoriFullSnapshot

    func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error>

    func sendTurn(id: String, message: String, attachmentIDs: [String], modelSelection: ModelSelection?) async throws -> Session
    func retryTurn(turnID: String?, messageID: String?) async throws -> Session
    func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse
    func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> Attachment

    func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem]
    func fetchWorkDetail(taskID: String) async throws -> WorkDetail
    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem>
    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem>

    func fetchInteractions() async throws -> [Interaction]
    func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction>

    func fetchMCPServers() async throws -> [MCPServer]
    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool]
    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer>
    func performMCPAction(
        serverID: String,
        expectedRevision: Int,
        action: String,
        returnTo: String?,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer>
    func setMCPCredential(
        serverID: String,
        expectedRevision: Int,
        secret: String,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer>
    func setMCPToolExposure(
        serverID: String,
        expectedRevision: Int,
        toolName: String,
        exposure: MCPToolExposure,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer>
    func fetchConnections() async throws -> [Connection]
    func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        returnTo: String?,
        requestID: String
    ) async throws -> MutationReceipt<Connection>

    func fetchLibrary() async throws -> [LibraryItem]
    func fetchLibraryItem(id: String) async throws -> LibraryItem
    func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload
    func search(query: String) async throws -> SearchResponse
    func reviseLibraryItem(
        id: String,
        expectedRevision: Int,
        instruction: String,
        requestID: String
    ) async throws -> MutationReceipt<LibraryItem>

    func fetchCoreMemories() async throws -> [MemoryFact]
    func fetchMemoryLogs() async throws -> [MemoryLog]
    func fetchWorkingMemory() async throws -> WorkingMemory
    func createMemory(
        text: String,
        section: String?,
        about: String?,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact>
    func performMemoryAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<MemoryActionResource>
    func performMemoryLogEntryAction(
        date: String,
        entryID: String,
        expectedRevision: Int,
        action: String,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryLogEntryActionResource>
    func performWorkingMemoryAction(
        itemID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<WorkingMemoryActionResource>

    func fetchAutomations() async throws -> [Automation]
    func fetchAutomation(id: String) async throws -> Automation
    func updateAutomation(
        id: String,
        expectedRevision: Int,
        value: AutomationEditable,
        requestID: String
    ) async throws -> MutationReceipt<Automation>
    func performAutomationAction(
        id: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<Automation>
    func fetchGoals() async throws -> [Goal]
    func fetchPermissions() async throws -> [PermissionRule]
    func createPermissionRule(
        _ rule: PermissionRuleEditable,
        requestID: String
    ) async throws -> MutationReceipt<PermissionRule>
    func revokePermissionRule(
        id: String,
        expectedRevision: Int,
        requestID: String
    ) async throws -> MutationReceipt<PermissionRule>
    func fetchPreferences() async throws -> Preferences
    func savePreferences(
        _ preferences: Preferences,
        requestID: String
    ) async throws -> MutationReceipt<Preferences>
    func resetUserData(confirmation: String) async throws

    // Model API Keys (BYOK)
    func fetchModelKeys() async throws -> ModelKeysResponse
    func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider
    func deleteModelKey(provider: String) async throws -> ModelKeyProvider
}

public extension NoriTransport {
    func resetUserData(confirmation: String) async throws {
        throw NoriAPIError(message: "Reset is not supported by this transport.")
    }

    func fetchModelKeys() async throws -> ModelKeysResponse {
        ModelKeysResponse()
    }

    func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider {
        throw NoriAPIError(message: "Model keys are not supported by this transport.")
    }

    func deleteModelKey(provider: String) async throws -> ModelKeyProvider {
        throw NoriAPIError(message: "Model keys are not supported by this transport.")
    }
    func performMCPAction(
        serverID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        try await performMCPAction(
            serverID: serverID,
            expectedRevision: expectedRevision,
            action: action,
            returnTo: nil,
            requestID: requestID
        )
    }

    func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        requestID: String
    ) async throws -> MutationReceipt<Connection> {
        try await performConnectionAction(
            connectionID: connectionID,
            expectedRevision: expectedRevision,
            action: action,
            returnTo: nil,
            requestID: requestID
        )
    }

    func fetchLibraryItemFile(id: String) async throws -> LibraryFilePayload {
        try await fetchLibraryItemFile(id: id, format: nil)
    }

    func sendTurn(id: String, message: String, attachmentIDs: [String]) async throws -> Session {
        try await sendTurn(id: id, message: message, attachmentIDs: attachmentIDs, modelSelection: nil)
    }
}
