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
    case settings
    case onboarding
    case workDetail(String)
    case document(String)
    case reviewInteraction(String)
    case oauthCallback(URL)

    public static func parse(_ url: URL) -> NoriDeepLink? {
        guard url.scheme?.lowercased() == "nori" else { return nil }
        let host = url.host?.lowercased() ?? ""
        let parts = url.pathComponents.filter { $0 != "/" }
        let queryItems = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []

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
        case "chat":
            if let interactionID = queryItems.first(where: { $0.name == "interaction_id" })?.value,
               !interactionID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                return .reviewInteraction(interactionID)
            }
            return .tab(.chat)
        case "review":
            if let interactionID = parts.first, !interactionID.isEmpty {
                return .reviewInteraction(interactionID)
            }
            return .tab(.chat)
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
        case "settings": return .settings
        case "onboarding": return .onboarding
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

public enum GoogleSignInResult: Sendable, Equatable {
    case signedIn
    case inviteRequired(AuthSession)
    case forbidden(String)
}

public enum InteractionSubmitOutcome: Sendable, Equatable {
    case resolved(Interaction)
    case conflict(current: Interaction?, message: String)
    case failed(String)
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
    public internal(set) var modelSettings: ModelSettingsResponse?
    public internal(set) var homeConnectors: HomeConnectorsResponse?
    public internal(set) var computerStatus: ComputerStatus?
    public var modelKeyError: String?
    public var billingAccessBlock: BillingAccessBlock?
    public internal(set) var dataResetRecoveryRequired = false
    public internal(set) var dataResetStatusRequired = false
    public internal(set) var isResettingData = false
    public internal(set) var isDeletingAccount = false
    public internal(set) var accountDeletePending = false
    public var accountDeletedNotice: String?

    public var session: Session
    public var workItems: [WorkItem]
    public var workDetails: [String: WorkDetail]
    public var operations: [OperationProjection]
    public var interactions: [Interaction]
    public var mcpServers: [MCPServer]
    public var mcpTools: [String: [MCPServerTool]]
    public var connections: [Connection]
    public internal(set) var integrations: [IntegrationSummary] = []
    public var libraryItems: [LibraryItem]
    public internal(set) var libraryBodyCache: [String: String] = [:]
    public var searchHits: [SearchHit]
    public var coreMemories: [MemoryFact]
    public var memoryLogs: [MemoryLog]
    public var workingMemory: WorkingMemory
    public internal(set) var hasLoadedWorkingMemory = false
    public var memorySearchHits: [MemorySearchHit] = []
    public var automations: [Automation]
    public var permissions: [PermissionRule]
    public var preferences: Preferences
    public internal(set) var hasLoadedPreferences = false
    public var modelKeys: ModelKeysResponse
    public var serviceKeys: ServiceKeysResponse

    public var isLoading: Bool = false
    public var isThinking: Bool = false
    public var isStreamingAssistant: Bool = false
    public var streamingAssistantText: String?
    public var streamingTurnID: String?
    public var pendingComposerDraft: String?
    public var lastError: String?
    public var memoryRefreshError: String?
    public var toastMessage: String?
    public var pendingDeepLink: NoriDeepLink?
    public var pendingReviewInteractionID: String?
    public var hasLoadedSnapshot: Bool = false
    public var isShowingOnboardingReplay: Bool = false

    public let pushManager: NoriPushManager

    var hasLoadedSession = false
    var isAwaitingResetMarker = false
    var dataResetGeneration: UInt64 = 0
    var resetRecoveryBaseline: String?
    var resetRecoveryBaselineKnown = false
    var resetRecoveryHasClearedData = false
    var preferencesReadSequence: UInt64 = 0
    var appliedPreferencesRead: UInt64 = 0

    static let onboardingDoneKeyPrefix = "nori.onboarding.done."
    static let observedResetKeyPrefix = "nori.data-reset.observed."

    let authProvider: AuthTokenProvider
    let resetMinimumDuration: Duration
    let devLocalEmail: String?
    let liveTransport: LiveNoriTransport?
    var transport: any NoriTransport
    enum StreamRefreshDomain: Hashable {
        case reset
        case workAndInteractions
        case library
        case memory
    }

    public enum RefreshDomain: Hashable, Sendable {
        case session
        case summary
        case workAndInteractions
        case library
        case automations
        case apps
        case memory
        case permissionsAndPreferences
        case modelKeys
        case computerStatus
    }

    var sessionGeneration: UInt64 = 0
    var eventTask: Task<Void, Never>?
    var chatContextTask: Task<Void, Never>?
    var oauthCallbackTask: Task<Void, Never>?
    var postResetTask: Task<Void, Never>?
    var interactionFollowUpTasks: [UUID: Task<Void, Never>] = [:]
    @ObservationIgnored var streamRefreshTasks: [StreamRefreshDomain: Task<Void, Never>] = [:]
    @ObservationIgnored var streamRefreshNeedsRerun: Set<StreamRefreshDomain> = []
    @ObservationIgnored var pendingStreamingText: String?
    @ObservationIgnored var streamingFlushTask: Task<Void, Never>?
    @ObservationIgnored var streamingFlushInterval: Duration = .milliseconds(40)
    @ObservationIgnored var lastRefreshedAt: [RefreshDomain: ContinuousClock.Instant] = [:]
    @ObservationIgnored var tabRefreshTTL: Duration = .seconds(20)
    var interactionsRefreshID: UInt64 = 0
    var settledInteractionsRefreshID: UInt64 = 0
    var workRefreshID: UInt64 = 0
    var settledWorkRefreshID: UInt64 = 0
    var hasLoadedWorkItems = false
    var hasLoadedInteractions = false
    var toastTask: Task<Void, Never>?
    @ObservationIgnored var submitSpans: [String: (span: any NoriTelemetrySpan, timeout: Task<Void, Never>)] = [:]
    var reportedFailedTaskIDs: Set<String> = []
    var hasBootstrapped: Bool = false
    var isCheckingSavedSession: Bool = false
    var onboardingSavedFactKeys: Set<String> = []
    var onboardingSavedPreferences: Bool = false

    public var isBootstrappingAuth: Bool {
        !hasBootstrapped || isCheckingSavedSession
    }

    public init(
        transport: (any NoriTransport)? = nil,
        authProvider: AuthTokenProvider = AuthTokenProvider(),
        resetMinimumDuration: Duration = .seconds(2),
        devLocalEmail: String? = nil,
        pushManager: NoriPushManager? = nil
    ) {
        self.authProvider = authProvider
        self.resetMinimumDuration = resetMinimumDuration
        self.devLocalEmail = devLocalEmail
        self.pushManager = pushManager ?? NoriPushManager()
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
        self.operations = []
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
        self.permissions = []
        self.preferences = Preferences()
        self.modelKeys = ModelKeysResponse()
        self.serviceKeys = ServiceKeysResponse()
    }

    var onboardingDoneKey: String? {
        guard case .signedIn(let email) = authState else { return nil }
        return Self.onboardingDoneKeyPrefix + email.lowercased()
    }

    var observedResetKey: String? {
        guard case .signedIn(let email) = authState else { return nil }
        return Self.observedResetKeyPrefix + email.lowercased()
    }

    public func acknowledgeDataResetStatus() {
        dataResetStatusRequired = false
    }

    public var isOnboardingLocallyCompleted: Bool {
        guard let key = onboardingDoneKey else { return false }
        return UserDefaults.standard.bool(forKey: key)
    }

    public var shouldShowOnboarding: Bool {
        if isShowingOnboardingReplay { return true }
        guard authState.isSignedIn, hasLoadedSnapshot else { return false }
        if let resetAt = preferences.dataResetAt, !resetAt.isEmpty {
            return preferences.onboardingCompletedAt?.isEmpty != false
        }
        if isOnboardingLocallyCompleted { return false }
        if let completedAt = preferences.onboardingCompletedAt, !completedAt.isEmpty { return false }
        if !session.messages.isEmpty { return false }
        return true
    }

    public var serverHost: String {
        liveTransport?.baseURL.host() ?? NoriConfig.serverURL.host() ?? "nori-core"
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

    public var hasLoadedWorkOverview: Bool {
        hasLoadedWorkItems && hasLoadedInteractions
    }

    public var needsYouCount: Int {
        hasLoadedInteractions ? pendingInteractions.count : (assistantSummary?.needsYou ?? pendingInteractions.count)
    }

    public var actionableNeedsYouCount: Int {
        hasLoadedInteractions
            ? pendingInteractions.filter { $0.kind != .question }.count
            : (assistantSummary?.needsYou ?? 0)
    }

    var computedAppsAttentionCount: Int {
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

    public var coreMemoryUsedBytes: Int {
        workingMemory.coreBytes
    }

    public var coreMemoryBudgetBytes: Int {
        max(1, workingMemory.coreBudgetBytes)
    }

    public var coreMemoryRatio: Double {
        min(1.0, Double(coreMemoryUsedBytes) / Double(coreMemoryBudgetBytes))
    }

    public var allowBYOK: Bool {
        serviceKeys.allowBYOK ?? capabilities?.entitlement?.allowBYOK ?? true
    }

    public var hasConfiguredModelKey: Bool {
        modelKeys.providers.contains(where: \.configured)
    }

    public var needsModelKeySetup: Bool {
        allowBYOK && modelKeys.byokRequired && !hasConfiguredModelKey && !hasSelectedHomeChat
    }

    public var activeBillingAccessBlock: BillingAccessBlock? {
        if let entitlement = capabilities?.entitlement {
            if entitlement.suspended {
                let message = billingAccessBlock?.code == .accountSuspended
                    ? billingAccessBlock!.message
                    : BillingAccessBlockCode.accountSuspended.defaultMessage
                return BillingAccessBlock(code: .accountSuspended, message: message)
            }
            if entitlement.isInactive {
                let message = billingAccessBlock?.code == .subscriptionInactive
                    ? billingAccessBlock!.message
                    : BillingAccessBlockCode.subscriptionInactive.defaultMessage
                return BillingAccessBlock(code: .subscriptionInactive, message: message)
            }
        }
        if let billingAccessBlock {
            return billingAccessBlock
        }
        if let entitlement = capabilities?.entitlement, entitlement.isTrialCreditsExhausted {
            return BillingAccessBlock(
                code: .creditExhausted,
                message: "Your trial credits are used up. Upgrade your plan to continue."
            )
        }
        return nil
    }

    public var isChatReadOnly: Bool {
        activeBillingAccessBlock != nil
    }

    public var hasSelectedHomeChat: Bool {
        guard let settings = modelSettings, let id = settings.selection.chatProfileID, !id.isEmpty else { return false }
        return settings.selection.homeOnly == true || id.hasPrefix("home-") || settings.modelProfiles.profiles.contains { $0.id == id && $0.backend == "home" }
    }

    public var isComputerEnabled: Bool {
        capabilities?.assistantFeatures?["computer"] == true || computerStatus?.enabled == true
    }

    public func cachedBodyText(for itemID: String) -> String? {
        libraryBodyCache[itemID]
    }

    func isRefreshFresh(_ domain: RefreshDomain, force: Bool) -> Bool {
        guard !force, let last = lastRefreshedAt[domain] else { return false }
        return last.duration(to: .now) < tabRefreshTTL
    }

    func markRefreshed(_ domain: RefreshDomain) {
        lastRefreshedAt[domain] = .now
    }

    public func refreshForTab(_ tab: NoriTab, force: Bool = false) async {
        guard authState.isSignedIn else { return }
        switch tab {
        case .chat:
            async let sessionTask: Void = refreshSession(force: force)
            async let summaryTask: Void = refreshSummary(force: force)
            _ = await (sessionTask, summaryTask)
        case .work:
            let computerEnabled = isComputerEnabled
            async let workTask: Void = refreshWorkAndInteractions(force: force)
            async let libraryTask: Void = refreshLibrary(force: force)
            async let automationsTask: Void = refreshAutomations(force: force)
            async let computerTask: Void = {
                if computerEnabled {
                    _ = await self.refreshComputerStatus(force: force)
                }
            }()
            _ = await (workTask, libraryTask, automationsTask, computerTask)
        case .more:
            let shouldRefreshCaps = force || capabilities == nil
            async let summaryTask: Void = refreshSummary(force: force)
            async let modelKeysTask: Void = refreshModelKeys(force: force)
            async let prefsTask: Void = refreshPermissionsAndPreferences(force: force)
            async let capsTask: Void = {
                if shouldRefreshCaps {
                    await self.refreshCapabilities()
                }
            }()
            _ = await (summaryTask, modelKeysTask, prefsTask, capsTask)
        }
    }

    func cancelStreamRefreshTasks() {
        streamRefreshNeedsRerun.removeAll()
        for task in streamRefreshTasks.values {
            task.cancel()
        }
        streamRefreshTasks.removeAll()
    }

    func clearUserState(preserveIdentity: Bool = false) {
        stopEventStream()
        streamingFlushTask?.cancel()
        streamingFlushTask = nil
        pendingStreamingText = nil
        cancelStreamRefreshTasks()
        lastRefreshedAt.removeAll()
        chatContextTask?.cancel()
        chatContextTask = nil
        oauthCallbackTask?.cancel()
        oauthCallbackTask = nil
        postResetTask?.cancel()
        postResetTask = nil
        cancelInteractionFollowUpTasks()
        toastTask?.cancel()
        toastTask = nil
        for turnID in Array(submitSpans.keys) {
            finishSubmitSpan(turnID: turnID, outcome: "cancelled", status: .cancelled)
        }
        reportedFailedTaskIDs.removeAll()
        if !preserveIdentity {
            NoriTelemetryRuntime.shared.setUser(id: nil)
        }
        authState = .signedOut
        authError = nil
        connectionState = .offline
        hasLoadedSnapshot = false
        hasLoadedSession = false
        isAwaitingResetMarker = false
        isShowingOnboardingReplay = false
        onboardingSavedFactKeys.removeAll()
        onboardingSavedPreferences = false
        capabilities = nil
        assistantSummary = nil
        lastPing = nil
        session = Session()
        workItems = []
        workDetails = [:]
        operations = []
        interactions = []
        hasLoadedWorkItems = false
        hasLoadedInteractions = false
        mcpServers = []
        mcpTools = [:]
        connections = []
        integrations = []
        libraryItems = []
        libraryBodyCache = [:]
        searchHits = []
        coreMemories = []
        memoryLogs = []
        workingMemory = WorkingMemory()
        hasLoadedWorkingMemory = false
        memorySearchHits = []
        automations = []
        permissions = []
        preferences = Preferences()
        hasLoadedPreferences = false
        modelKeys = ModelKeysResponse()
        serviceKeys = ServiceKeysResponse()
        billingAccessBlock = nil
        isThinking = false
        isLoading = false
        isStreamingAssistant = false
        pendingComposerDraft = nil
        pendingDeepLink = nil
        pendingReviewInteractionID = nil
        lastError = nil
        memoryRefreshError = nil
        toastMessage = nil
        streamingAssistantText = nil
        streamingTurnID = nil
        modelSettings = nil
        homeConnectors = nil
        computerStatus = nil
        modelKeyError = nil
        dataResetRecoveryRequired = false
        dataResetStatusRequired = false
        resetRecoveryBaseline = nil
        resetRecoveryBaselineKnown = false
        resetRecoveryHasClearedData = false
    }

    func clearTransportIdentity(email: String? = nil) async {
        let resolvedEmail = email ?? authState.email
        if let liveTransport {
            await liveTransport.clearCachedIdentity(email: resolvedEmail)
        } else {
            NoriTelemetryRuntime.shared.setUser(id: nil)
        }
    }

    func handleAuthFailureIfNeeded(_ error: Error) async -> Bool {
        if let apiErr = error as? NoriAPIError, apiErr.code == "account_delete_pending" {
            stopEventStream()
            accountDeletePending = true
            return true
        }
        if let apiErr = error as? NoriAPIError, apiErr.code == "data_reset_pending" {
            let baseline = preferences.dataResetAt
            let baselineKnown = hasLoadedPreferences
            enterDataResetRecovery(baseline: baseline, baselineKnown: baselineKnown, confirmed: true)
            _ = try? await refreshDataResetStatus()
            return true
        }
        if let apiErr = error as? NoriAPIError, let block = apiErr.billingAccessBlock {
            billingAccessBlock = block
            if block.isSuspended {
                stopEventStream()
            }
            return true
        }
        if let apiErr = error as? NoriAPIError, apiErr.isAuthError {
            let nextGeneration = sessionGeneration &+ 1
            await signOut()
            guard nextGeneration == sessionGeneration else { return true }
            authError = "Your session expired or your account is not authorized. Please sign in again."
            return true
        }
        return false
    }

    @discardableResult
    func handleOperationError(_ error: Error, showToast shouldToast: Bool = true) async -> Bool {
        if await handleAuthFailureIfNeeded(error) {
            return true
        }
        lastError = error.localizedDescription
        if shouldToast {
            showToast(error.localizedDescription)
        }
        return false
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
            oauthCallbackTask?.cancel()
            oauthCallbackTask = Task { [weak self] in
                guard let self, !Task.isCancelled else { return }
                async let appsTask: Void = self.refreshApps()
                async let workTask: Void = self.refreshWorkAndInteractions()
                async let summaryTask: Void = self.refreshSummary()
                _ = await (appsTask, workTask, summaryTask)
            }
        }
        pendingDeepLink = link
    }

    public func handleAPNsDeviceToken(_ tokenData: Data) {
        Task { @MainActor [weak self] in
            guard let self else { return }
            await self.pushManager.updateDeviceToken(
                tokenData,
                transport: self.transport,
                isSignedIn: self.authState.isSignedIn
            )
        }
    }

    public func requestPushAuthorizationIfNeeded() {
        Task { @MainActor [weak self] in
            guard let self else { return }
            await self.pushManager.requestAuthorizationIfNeeded()
        }
    }

    public func handlePushNotificationTap(userInfo: [AnyHashable: Any]) {
        handlePushNotificationTap(interactionID: NoriPushManager.extractInteractionID(from: userInfo))
    }

    public func handlePushNotificationTap(interactionID: String?) {
        if let interactionID, !interactionID.isEmpty {
            pendingDeepLink = .reviewInteraction(interactionID)
        } else {
            pendingDeepLink = .tab(.chat)
        }
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

    // MARK: - Deterministic Test Synchronization Hooks

    func waitForEventStreamCompletion() async {
        await eventTask?.value
    }

    func waitForChatContextCompletion() async {
        await chatContextTask?.value
    }

    func waitForOAuthCallbackCompletion() async {
        await oauthCallbackTask?.value
    }

    func waitForBackgroundRefreshes() async {
        while !streamRefreshTasks.isEmpty {
            let tasks = Array(streamRefreshTasks.values)
            for task in tasks {
                await task.value
            }
        }
    }
}
