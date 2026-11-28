import Foundation

public struct NoriFullSnapshot: Sendable, Hashable {
    public var capabilities: Capabilities?
    public var didLoadCapabilities: Bool
    public var session: Session
    public var workItems: [WorkItem]
    public var didLoadWorkItems: Bool
    public var interactions: [Interaction]
    public var didLoadInteractions: Bool
    public var mcpServers: [MCPServer]
    public var didLoadMCPServers: Bool
    public var mcpTools: [String: [MCPServerTool]]
    public var connections: [Connection]
    public var didLoadConnections: Bool
    public var libraryItems: [LibraryItem]
    public var didLoadLibraryItems: Bool
    public var coreMemories: [MemoryFact]
    public var didLoadCoreMemories: Bool
    public var memoryLogs: [MemoryLog]
    public var didLoadMemoryLogs: Bool
    public var workingMemory: WorkingMemory
    public var didLoadWorkingMemory: Bool
    public var automations: [Automation]
    public var didLoadAutomations: Bool
    public var permissions: [PermissionRule]
    public var didLoadPermissions: Bool
    public var preferences: Preferences
    public var didLoadPreferences: Bool
    public var modelKeys: ModelKeysResponse
    public var didLoadModelKeys: Bool
    public var serviceKeys: ServiceKeysResponse
    public var didLoadServiceKeys: Bool
    public var partialErrors: [String: String]

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
        permissions: [PermissionRule] = [],
        preferences: Preferences = Preferences(),
        modelKeys: ModelKeysResponse = ModelKeysResponse(),
        serviceKeys: ServiceKeysResponse = ServiceKeysResponse(),
        didLoadCapabilities: Bool = true,
        didLoadWorkItems: Bool = true,
        didLoadInteractions: Bool = true,
        didLoadMCPServers: Bool = true,
        didLoadConnections: Bool = true,
        didLoadLibraryItems: Bool = true,
        didLoadCoreMemories: Bool = true,
        didLoadMemoryLogs: Bool = true,
        didLoadWorkingMemory: Bool = true,
        didLoadAutomations: Bool = true,
        didLoadPermissions: Bool = true,
        didLoadPreferences: Bool = true,
        didLoadModelKeys: Bool = true,
        didLoadServiceKeys: Bool = true,
        partialErrors: [String: String] = [:]
    ) {
        self.capabilities = capabilities
        self.didLoadCapabilities = didLoadCapabilities
        self.session = session
        self.workItems = workItems
        self.didLoadWorkItems = didLoadWorkItems
        self.interactions = interactions
        self.didLoadInteractions = didLoadInteractions
        self.mcpServers = mcpServers
        self.didLoadMCPServers = didLoadMCPServers
        self.mcpTools = mcpTools
        self.connections = connections
        self.didLoadConnections = didLoadConnections
        self.libraryItems = libraryItems
        self.didLoadLibraryItems = didLoadLibraryItems
        self.coreMemories = coreMemories
        self.didLoadCoreMemories = didLoadCoreMemories
        self.memoryLogs = memoryLogs
        self.didLoadMemoryLogs = didLoadMemoryLogs
        self.workingMemory = workingMemory
        self.didLoadWorkingMemory = didLoadWorkingMemory
        self.automations = automations
        self.didLoadAutomations = didLoadAutomations
        self.permissions = permissions
        self.didLoadPermissions = didLoadPermissions
        self.preferences = preferences
        self.didLoadPreferences = didLoadPreferences
        self.modelKeys = modelKeys
        self.didLoadModelKeys = didLoadModelKeys
        self.serviceKeys = serviceKeys
        self.didLoadServiceKeys = didLoadServiceKeys
        self.partialErrors = partialErrors
    }
}

public protocol NoriTransport: Sendable {
    func ping() async -> HealthCheckResult
    func resolveUser(idToken: String, email: String?, name: String?) async throws -> UserResolveOutcome
    func redeemInvite(idToken: String, inviteCode: String, email: String?, name: String?) async throws -> String

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
    func fetchOperations(connectionID: String?) async throws -> [OperationProjection]
    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem>
    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem>

    func fetchInteractions() async throws -> [Interaction]
    func fetchInteraction(id: String) async throws -> Interaction
    func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction>

    func fetchMCPServers() async throws -> [MCPServer]
    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool]
    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer>
    func updateMCPServer(
        serverID: String,
        expectedRevision: Int,
        input: MCPServerInput,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer>
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
    func fetchIntegrations() async throws -> [IntegrationSummary]
    func fetchConnectionResources(connectionID: String) async throws -> [ConnectionResource]
    func createConnection(integrationID: String, requestID: String) async throws -> MutationReceipt<Connection>
    func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        returnTo: String?,
        capabilities: [String],
        requestID: String
    ) async throws -> MutationReceipt<Connection>
    func startGoogleDrivePicker(connectionID: String, returnTo: String) async throws -> AuthAttempt
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
    func renameLibraryItem(
        id: String,
        expectedRevision: Int,
        title: String,
        collection: String?,
        requestID: String
    ) async throws -> MutationReceipt<LibraryItem>
    func deleteLibraryItem(
        id: String,
        expectedRevision: Int,
        scope: String,
        requestID: String
    ) async throws -> MutationReceipt<LibraryItem>
    func fetchLibraryItemVersions(id: String) async throws -> [LibraryItem]

    func fetchCoreMemories() async throws -> [MemoryFact]
    func fetchMemoryLogs() async throws -> [MemoryLog]
    func fetchMemoryLog(date: String) async throws -> MemoryLog
    func fetchWorkingMemory() async throws -> WorkingMemory
    func searchMemory(query: String, layer: String?, limit: Int) async throws -> MemorySearchResponse
    func createMemory(
        text: String,
        section: String?,
        about: String?,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact>
    func updateMemory(
        id: String,
        expectedRevision: Int,
        text: String,
        section: String?,
        about: String?,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact>
    func consolidateMemory(requestID: String) async throws
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
    func promoteMemoryLogEntry(
        date: String,
        entryID: String,
        expectedRevision: Int,
        text: String?,
        section: String?,
        about: String?,
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
    func createAutomation(
        _ value: AutomationEditable,
        requestID: String
    ) async throws -> MutationReceipt<Automation>
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
    func snoozeAutomation(
        id: String,
        expectedRevision: Int,
        until: String,
        requestID: String
    ) async throws -> MutationReceipt<Automation>
    func fetchAutomationOccurrences(id: String) async throws -> [AutomationOccurrence]
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
    func fetchAPICosts(days: Int) async throws -> APICostResponse
    func resetUserData(confirmation: String) async throws
    func resumePendingDataReset() async throws
    func deleteAccount(confirmation: String, resumePending: Bool) async throws

    // Model API Keys (BYOK)
    func fetchModelKeys() async throws -> ModelKeysResponse
    func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider
    func deleteModelKey(provider: String) async throws -> ModelKeyProvider
    func fetchServiceKeys() async throws -> ServiceKeysResponse
    func saveServiceKey(service: String, apiKey: String) async throws -> ServiceKeyStatus
    func deleteServiceKey(service: String) async throws -> ServiceKeyStatus
    func fetchModelSettings() async throws -> ModelSettingsResponse
    func saveModelSettings(_ request: SaveModelSettingsRequest) async throws -> ModelSettingsResponse
    func fetchHomeConnectors() async throws -> HomeConnectorsResponse
    func lookupHomePairing(_ code: String) async throws -> HomePairing
    func approveHomePairing(_ code: String) async throws
    func testHomeConnector(_ id: String) async throws -> HomeConnectorTest
    func revokeHomeConnector(_ id: String) async throws

    // Personal Agent Computer
    func fetchComputerStatus() async throws -> ComputerStatus
    func wakeComputer() async throws -> ComputerStatus
    func sleepComputer() async throws
    func resetComputer() async throws -> ComputerStatus
    func listComputerCheckpoints() async throws -> [ComputerCheckpoint]
    func createComputerCheckpoint(label: String?) async throws -> ComputerCheckpoint
    func restoreComputerCheckpoint(checkpointID: String?) async throws -> ComputerStatus
    func createComputerSession(_ request: CreateComputerSessionRequest) async throws -> CreateComputerSessionResponse
    func updateComputerSessionMode(sessionID: String, mode: ComputerSessionMode) async throws -> ComputerSessionSummary
    func endComputerSession(sessionID: String) async throws
    func computerDesktopWebSocketRequest(for session: CreateComputerSessionResponse) async throws -> URLRequest

    // Push notifications (APNs)
    func registerPushDevice(_ request: RegisterPushDeviceRequest) async throws -> PushDevice
    func deletePushDevice(id: String) async throws
}

public extension NoriTransport {
    func resolveUser(idToken: String, email: String?, name: String?) async throws -> UserResolveOutcome {
        .resolved(userID: "user-default")
    }

    func redeemInvite(idToken: String, inviteCode: String, email: String?, name: String?) async throws -> String {
        throw NoriAPIError(message: "Invite redemption is not supported by this transport.")
    }

    func fetchOperations(connectionID: String? = nil) async throws -> [OperationProjection] {
        []
    }

    func fetchInteraction(id: String) async throws -> Interaction {
        let all = try await fetchInteractions()
        if let found = all.first(where: { $0.id == id }) {
            return found
        }
        throw NoriAPIError(message: "Interaction not found.", statusCode: 404)
    }

    func fetchMemoryLog(date: String) async throws -> MemoryLog {
        let all = try await fetchMemoryLogs()
        if let found = all.first(where: { $0.date == date }) {
            return found
        }
        throw NoriAPIError(message: "Memory log not found.", statusCode: 404)
    }

    func searchMemory(query: String, layer: String? = nil, limit: Int = 20) async throws -> MemorySearchResponse {
        MemorySearchResponse(items: [], query: query)
    }

    func fetchAPICosts(days: Int) async throws -> APICostResponse {
        throw NoriAPIError(message: "API costs are not supported by this transport.")
    }

    func resumePendingDataReset() async throws {
        throw NoriAPIError(message: "Reset recovery is not supported by this transport.")
    }
    func resetUserData(confirmation: String) async throws {
        throw NoriAPIError(message: "Reset is not supported by this transport.")
    }
    func deleteAccount(confirmation: String, resumePending: Bool) async throws {
        throw NoriAPIError(message: "Account deletion is not supported by this transport.")
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

    func fetchServiceKeys() async throws -> ServiceKeysResponse {
        ServiceKeysResponse()
    }

    func saveServiceKey(service: String, apiKey: String) async throws -> ServiceKeyStatus {
        throw NoriAPIError(message: "Service keys are not supported by this transport.")
    }

    func deleteServiceKey(service: String) async throws -> ServiceKeyStatus {
        throw NoriAPIError(message: "Service keys are not supported by this transport.")
    }

    func fetchModelSettings() async throws -> ModelSettingsResponse {
        throw NoriAPIError(message: "Model settings are not supported by this transport.")
    }

    func saveModelSettings(_ request: SaveModelSettingsRequest) async throws -> ModelSettingsResponse {
        throw NoriAPIError(message: "Model settings are not supported by this transport.")
    }
    func fetchHomeConnectors() async throws -> HomeConnectorsResponse {
        throw NoriAPIError(message: "Home connectors are not supported by this transport.", statusCode: 404)
    }
    func lookupHomePairing(_ code: String) async throws -> HomePairing {
        throw NoriAPIError(message: "Home connectors are not supported by this transport.")
    }
    func approveHomePairing(_ code: String) async throws {
        throw NoriAPIError(message: "Home connectors are not supported by this transport.")
    }
    func testHomeConnector(_ id: String) async throws -> HomeConnectorTest {
        throw NoriAPIError(message: "Home connectors are not supported by this transport.")
    }
    func revokeHomeConnector(_ id: String) async throws {
        throw NoriAPIError(message: "Home connectors are not supported by this transport.")
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

    func fetchIntegrations() async throws -> [IntegrationSummary] { [] }

    func fetchConnectionResources(connectionID: String) async throws -> [ConnectionResource] { [] }

    func createConnection(integrationID: String, requestID: String) async throws -> MutationReceipt<Connection> {
        throw NoriAPIError(message: "Connections are not supported by this transport.")
    }

    func performConnectionAction(
        connectionID: String,
        expectedRevision: Int,
        action: String,
        returnTo: String?,
        capabilities: [String],
        requestID: String
    ) async throws -> MutationReceipt<Connection> {
        guard capabilities.isEmpty else {
            throw NoriAPIError(message: "Selecting account access is not supported by this transport.")
        }
        return try await performConnectionAction(
            connectionID: connectionID,
            expectedRevision: expectedRevision,
            action: action,
            returnTo: returnTo,
            requestID: requestID
        )
    }

    func startGoogleDrivePicker(connectionID: String, returnTo: String) async throws -> AuthAttempt {
        throw NoriAPIError(message: "Google Drive is not supported by this transport.")
    }

    func fetchLibraryItemFile(id: String) async throws -> LibraryFilePayload {
        try await fetchLibraryItemFile(id: id, format: nil)
    }

    func sendTurn(id: String, message: String, attachmentIDs: [String]) async throws -> Session {
        try await sendTurn(id: id, message: message, attachmentIDs: attachmentIDs, modelSelection: nil)
    }

    func updateMCPServer(
        serverID: String,
        expectedRevision: Int,
        input: MCPServerInput,
        requestID: String
    ) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "Updating MCP servers is not supported by this transport.")
    }

    func renameLibraryItem(
        id: String,
        expectedRevision: Int,
        title: String,
        collection: String?,
        requestID: String
    ) async throws -> MutationReceipt<LibraryItem> {
        throw NoriAPIError(message: "Renaming artifacts is not supported by this transport.")
    }

    func deleteLibraryItem(
        id: String,
        expectedRevision: Int,
        scope: String,
        requestID: String
    ) async throws -> MutationReceipt<LibraryItem> {
        throw NoriAPIError(message: "Deleting artifacts is not supported by this transport.")
    }

    func fetchLibraryItemVersions(id: String) async throws -> [LibraryItem] { [] }

    func updateMemory(
        id: String,
        expectedRevision: Int,
        text: String,
        section: String?,
        about: String?,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryFact> {
        throw NoriAPIError(message: "Editing memory is not supported by this transport.")
    }

    func consolidateMemory(requestID: String) async throws {
        throw NoriAPIError(message: "Consolidating memory is not supported by this transport.")
    }

    func promoteMemoryLogEntry(
        date: String,
        entryID: String,
        expectedRevision: Int,
        text: String?,
        section: String?,
        about: String?,
        pinned: Bool,
        requestID: String
    ) async throws -> MutationReceipt<MemoryLogEntryActionResource> {
        try await performMemoryLogEntryAction(
            date: date,
            entryID: entryID,
            expectedRevision: expectedRevision,
            action: "promote",
            pinned: pinned,
            requestID: requestID
        )
    }

    func createAutomation(
        _ value: AutomationEditable,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        throw NoriAPIError(message: "Creating routines is not supported by this transport.")
    }

    func snoozeAutomation(
        id: String,
        expectedRevision: Int,
        until: String,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        throw NoriAPIError(message: "Snoozing routines is not supported by this transport.")
    }

    func fetchAutomationOccurrences(id: String) async throws -> [AutomationOccurrence] { [] }

    func fetchComputerStatus() async throws -> ComputerStatus {
        ComputerStatus(enabled: false)
    }

    func wakeComputer() async throws -> ComputerStatus {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func sleepComputer() async throws {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func resetComputer() async throws -> ComputerStatus {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func listComputerCheckpoints() async throws -> [ComputerCheckpoint] {
        try await fetchComputerStatus().checkpoints
    }

    func createComputerCheckpoint(label: String?) async throws -> ComputerCheckpoint {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func restoreComputerCheckpoint(checkpointID: String?) async throws -> ComputerStatus {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func createComputerSession(_ request: CreateComputerSessionRequest) async throws -> CreateComputerSessionResponse {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func updateComputerSessionMode(sessionID: String, mode: ComputerSessionMode) async throws -> ComputerSessionSummary {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func endComputerSession(sessionID: String) async throws {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func computerDesktopWebSocketRequest(for session: CreateComputerSessionResponse) async throws -> URLRequest {
        throw NoriAPIError(message: "Nori Computer is not supported by this transport.")
    }

    func registerPushDevice(_ request: RegisterPushDeviceRequest) async throws -> PushDevice {
        PushDevice(id: "push-local", deviceID: request.deviceID, environment: request.environment)
    }

    func deletePushDevice(id: String) async throws {}
}
