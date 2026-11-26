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
    case onboarding
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
    public private(set) var modelSettings: ModelSettingsResponse?
    public private(set) var homeConnectors: HomeConnectorsResponse?
    public var modelKeyError: String?
    public private(set) var dataResetRecoveryRequired = false
    public private(set) var dataResetStatusRequired = false
    public private(set) var isResettingData = false
    public private(set) var isDeletingAccount = false
    public private(set) var accountDeletePending = false
    public var accountDeletedNotice: String?

    public var session: Session
    public var workItems: [WorkItem]
    public var workDetails: [String: WorkDetail]
    public var interactions: [Interaction]
    public var mcpServers: [MCPServer]
    public var mcpTools: [String: [MCPServerTool]]
    public var connections: [Connection]
    public private(set) var integrations: [IntegrationSummary] = []
    public var libraryItems: [LibraryItem]
    public var searchHits: [SearchHit]
    public var coreMemories: [MemoryFact]
    public var memoryLogs: [MemoryLog]
    public var workingMemory: WorkingMemory
    public internal(set) var hasLoadedWorkingMemory = false
    public var automations: [Automation]
    public var goals: [Goal]
    public var permissions: [PermissionRule]
    public var preferences: Preferences
    public private(set) var hasLoadedPreferences = false
    public var modelKeys: ModelKeysResponse

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
    public var hasLoadedSnapshot: Bool = false
    public var isShowingOnboardingReplay: Bool = false
    private var hasLoadedSession = false
    private var isAwaitingResetMarker = false
    private var dataResetGeneration: UInt64 = 0
    private var resetRecoveryBaseline: String?
    private var resetRecoveryBaselineKnown = false
    private var resetRecoveryHasClearedData = false
    private var preferencesReadSequence: UInt64 = 0
    private var appliedPreferencesRead: UInt64 = 0

    private static let onboardingDoneKeyPrefix = "nori.onboarding.done."
    private static let observedResetKeyPrefix = "nori.data-reset.observed."

    private var onboardingDoneKey: String? {
        guard case .signedIn(let email) = authState else { return nil }
        return Self.onboardingDoneKeyPrefix + email.lowercased()
    }

    private var observedResetKey: String? {
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

    public func completeOnboarding(
        name: String,
        replyStyle: String,
        focus: [String],
        workingOn: String,
        saveAboutYou: Bool
    ) async {
        let generation = sessionGeneration
        if let key = onboardingDoneKey {
            UserDefaults.standard.set(true, forKey: key)
        }
        isShowingOnboardingReplay = false

        if saveAboutYou {
            var seeds: [(section: String, text: String)] = []
            let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
            let trimmedStyle = replyStyle.trimmingCharacters(in: .whitespacesAndNewlines)
            let trimmedWorking = workingOn.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmedName.isEmpty {
                seeds.append(("Preferences", "The user's name is \(trimmedName)."))
            }
            if !trimmedStyle.isEmpty {
                seeds.append(("Preferences", "Prefers \(trimmedStyle.lowercased()) replies."))
            }
            if !focus.isEmpty {
                let joined = focus.map { $0.lowercased() }.joined(separator: ", ")
                seeds.append(("Context", "Uses Nori for \(joined)."))
            }
            if !trimmedWorking.isEmpty {
                seeds.append(("Context", "Currently working on: \(trimmedWorking)"))
            }
            for seed in seeds {
                do {
                    let receipt = try await transport.createMemory(
                        text: seed.text,
                        section: seed.section,
                        about: nil,
                        pinned: false,
                        requestID: UUID().uuidString.lowercased()
                    )
                    guard generation == sessionGeneration else { return }
                    if !coreMemories.contains(where: { $0.id == receipt.resource.id }) {
                        coreMemories.insert(receipt.resource, at: 0)
                    }
                } catch {
                    guard generation == sessionGeneration else { return }
                    if await handleAuthFailureIfNeeded(error) { return }
                }
            }
        }

        var nextPrefs = preferences
        nextPrefs.timezone = TimeZone.current.identifier
        nextPrefs.onboardingCompletedAt = ISO8601DateFormatter().string(from: Date())
        do {
            let receipt = try await transport.savePreferences(
                nextPrefs,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            appliedPreferencesRead = nextPreferencesRead()
            preferences = receipt.resource
        } catch {
            guard generation == sessionGeneration else { return }
            if await handleAuthFailureIfNeeded(error) { return }
            preferences = nextPrefs
        }
    }

    private let authProvider: AuthTokenProvider
    private let resetMinimumDuration: Duration
    private let devLocalEmail: String?
    private let liveTransport: LiveNoriTransport?
    var transport: any NoriTransport
    var sessionGeneration: UInt64 = 0
    private var eventTask: Task<Void, Never>?
    private var chatContextTask: Task<Void, Never>?
    private var interactionsRefreshID: UInt64 = 0
    private var settledInteractionsRefreshID: UInt64 = 0
    private var workRefreshID: UInt64 = 0
    private var settledWorkRefreshID: UInt64 = 0
    private var hasLoadedWorkItems = false
    private var hasLoadedInteractions = false
    private var toastTask: Task<Void, Never>?
    @ObservationIgnored private var submitSpans: [String: (span: any NoriTelemetrySpan, timeout: Task<Void, Never>)] = [:]
    private var reportedFailedTaskIDs: Set<String> = []
    var hasBootstrapped: Bool = false

    public init(
        transport: (any NoriTransport)? = nil,
        authProvider: AuthTokenProvider = AuthTokenProvider(),
        resetMinimumDuration: Duration = .seconds(2),
        devLocalEmail: String? = nil
    ) {
        self.authProvider = authProvider
        self.resetMinimumDuration = resetMinimumDuration
        self.devLocalEmail = devLocalEmail
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
        self.modelKeys = ModelKeysResponse()
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

    public var hasConfiguredModelKey: Bool {
        modelKeys.providers.contains(where: \.configured)
    }

    public var needsModelKeySetup: Bool {
        modelKeys.byokRequired && !hasConfiguredModelKey && !hasSelectedHomeChat
    }

    public var hasSelectedHomeChat: Bool {
        guard let settings = modelSettings, let id = settings.selection.chatProfileID, !id.isEmpty else { return false }
        return settings.selection.homeOnly == true || id.hasPrefix("home-") || settings.modelProfiles.profiles.contains { $0.id == id && $0.backend == "home" }
    }

    public func bootstrapIfNeeded() async {
        guard !hasBootstrapped else { return }
        hasBootstrapped = true
        let generation = sessionGeneration
        let savedSession: AuthSession?
        if let devLocalEmail {
            savedSession = AuthSession(idToken: "", refreshToken: nil, expiresAt: .distantFuture, email: devLocalEmail)
        } else {
            savedSession = await authProvider.loadSession()
        }
        guard generation == sessionGeneration else { return }
        guard let savedSession else {
            authState = .signedOut
            connectionState = .offline
            return
        }

        authState = .signedIn(email: savedSession.email)
        isLoading = true
        connectionState = .connecting
        defer { if generation == sessionGeneration { isLoading = false } }

        do {
            let initialSession = try await transport.fetchSession(before: nil)
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            applySession(initialSession)
            lastError = nil
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            if await handleAuthFailureIfNeeded(error) { return }
            guard generation == sessionGeneration else { return }
            connectionState = .reconnecting
            lastError = error.localizedDescription
        }
        startChatSession()
    }

    public func completeGoogleSignIn(callbackURL: URL, pkce: GoogleOAuthPKCE) async {
        sessionGeneration &+= 1
        let generation = sessionGeneration
        clearUserState()
        authError = nil
        authState = .signingIn
        await clearTransportIdentity()
        guard generation == sessionGeneration else { return }
        do {
            let code = try pkce.extractAuthorizationCode(from: callbackURL)
            let session = try await pkce.exchangeCode(code)
            guard generation == sessionGeneration else { return }
            await authProvider.saveSession(session)
            guard generation == sessionGeneration else { return }
            await clearTransportIdentity()
            guard generation == sessionGeneration else { return }
            let initialSession = try await transport.fetchSession(before: nil)
            guard generation == sessionGeneration else { return }
            applySession(initialSession)
            authState = .signedIn(email: session.email)
            connectionState = .connected
            lastError = nil
            NoriLogger.auth.info("auth.signed_in", breadcrumb: true)
            startChatSession()
        } catch let apiErr as NoriAPIError where apiErr.statusCode == 401 || apiErr.statusCode == 403 {
            guard generation == sessionGeneration else { return }
            await authProvider.clearSession()
            guard generation == sessionGeneration else { return }
            await clearTransportIdentity()
            guard generation == sessionGeneration else { return }
            authState = .signedOut
            connectionState = .offline
            authError = "This Google account is not authorized to access Nori."
            NoriLogger.auth.warning("auth.unauthorized", attributes: ["http.status_code": String(apiErr.statusCode)])
        } catch {
            guard generation == sessionGeneration else { return }
            await authProvider.clearSession()
            guard generation == sessionGeneration else { return }
            await clearTransportIdentity()
            guard generation == sessionGeneration else { return }
            authState = .signedOut
            connectionState = .offline
            authError = error.localizedDescription
            let tags = NoriErrorDiagnostics.tags(for: error)
            NoriLogger.auth.error("auth.sign_in_failed", attributes: tags)
            NoriTelemetryRuntime.shared.captureError(error, category: "auth.sign_in", tags: tags)
        }
    }

    public func signOut() async {
        sessionGeneration &+= 1
        accountDeletePending = false
        clearUserState()
        NoriLogger.auth.info("auth.signed_out", breadcrumb: true)
        await clearTransportIdentity()
        await authProvider.clearSession()
    }

    public func deleteAccount(confirmation: String, resumePending: Bool = false) async throws {
        guard !isDeletingAccount else {
            throw NoriAPIError(message: "Account deletion is already in progress.")
        }
        isDeletingAccount = true
        defer { isDeletingAccount = false }
        let onboardingKey = onboardingDoneKey
        let observedKey = observedResetKey
        let legacyResetKey = authState.email.map { "nori.data-reset.completion." + $0.lowercased() }
        do {
            try await transport.deleteAccount(
                confirmation: confirmation.trimmingCharacters(in: .whitespacesAndNewlines),
                resumePending: resumePending
            )
        } catch {
            if let apiError = error as? NoriAPIError, apiError.code == "account_delete_pending" {
                accountDeletePending = true
            }
            throw error
        }
        if let key = onboardingKey { UserDefaults.standard.removeObject(forKey: key) }
        if let key = observedKey { UserDefaults.standard.removeObject(forKey: key) }
        if let legacyResetKey { UserDefaults.standard.removeObject(forKey: legacyResetKey) }
        accountDeletePending = false
        accountDeletedNotice = "Your account has been deleted."
        await signOut()
    }

    public func resetUserData(confirmation: String) async throws {
        try await performDataReset(confirmation: confirmation, resumePending: false)
    }

    public func finishPendingDataReset() async throws {
        try await performDataReset(confirmation: "RESET", resumePending: true)
    }

    private func performDataReset(confirmation: String, resumePending: Bool) async throws {
        guard !isResettingData else { throw NoriAPIError(message: "A reset is already in progress.") }
        isResettingData = true
        let duration = resetMinimumDuration
        let minimumDisplay = Task.detached {
            try? await Task.sleep(for: duration)
        }
        defer { isResettingData = false }
        do {
            try await executeDataReset(confirmation: confirmation, resumePending: resumePending)
            _ = await minimumDisplay.value
        } catch {
            _ = await minimumDisplay.value
            throw error
        }
    }

    private func executeDataReset(confirmation: String, resumePending: Bool) async throws {
        let generation = sessionGeneration
        let resetGeneration = dataResetGeneration
        let signedInState = authState
        let previousResetAt = preferences.dataResetAt
        let previousResetKnown = hasLoadedPreferences
        do {
            if resumePending {
                try await transport.resumePendingDataReset()
            } else {
                try await transport.resetUserData(confirmation: confirmation)
            }
        } catch {
            guard signedInState == authState else { throw CancellationError() }
            if let apiError = error as? NoriAPIError, apiError.code == "data_reset_pending" {
                enterDataResetRecovery(baseline: previousResetAt, baselineKnown: previousResetKnown, confirmed: true)
            }
            let latest = try? await refreshDataResetStatus()
            guard signedInState == authState else { throw CancellationError() }
            if let apiError = error as? NoriAPIError, apiError.code == "data_reset_not_pending", latest != nil {
                return
            }
            if let latest, latest.dataResetPending != true,
               previousResetKnown, let resetAt = latest.dataResetAt, !resetAt.isEmpty,
               resetAt != previousResetAt {
                clearLocalDataForReset()
                await signOut()
                return
            }
            if latest == nil {
                enterDataResetRecovery(baseline: previousResetAt, baselineKnown: previousResetKnown, confirmed: false)
            }
            throw error
        }
        guard generation == sessionGeneration else {
            if signedInState == authState && resetGeneration != dataResetGeneration {
                dataResetRecoveryRequired = false
                clearLocalDataForReset()
                await signOut()
                return
            }
            throw CancellationError()
        }
        guard authState.isSignedIn else { throw CancellationError() }

        clearLocalDataForReset()
        await signOut()
    }

    private func enterDataResetRecovery(baseline: String?, baselineKnown: Bool, confirmed: Bool) {
        guard authState.isSignedIn else { return }
        if dataResetRecoveryRequired && (!confirmed || resetRecoveryHasClearedData) {
            if !resetRecoveryBaselineKnown && baselineKnown {
                resetRecoveryBaseline = baseline
                resetRecoveryBaselineKnown = true
            }
            return
        }
        let savedBaseline = resetRecoveryBaselineKnown ? resetRecoveryBaseline : baseline
        let savedBaselineKnown = resetRecoveryBaselineKnown || baselineKnown
        if confirmed {
            clearLocalDataForReset()
        } else {
            sessionGeneration &+= 1
            stopEventStream()
            chatContextTask?.cancel()
            chatContextTask = nil
        }
        resetRecoveryBaseline = savedBaseline
        resetRecoveryBaselineKnown = savedBaselineKnown
        resetRecoveryHasClearedData = confirmed
        dataResetRecoveryRequired = true
        preferences.dataResetPending = true
        hasLoadedPreferences = true
        hasLoadedSnapshot = true
        startChatSession()
    }

    @discardableResult
    public func refreshDataResetStatus() async throws -> Preferences {
        let generation = sessionGeneration
        let readID = nextPreferencesRead()
        let latest = try await transport.fetchPreferences()
        guard generation == sessionGeneration else { throw CancellationError() }
        applyPreferences(latest, readID: readID)
        return latest
    }

    private func clearLocalDataForReset() {
        let signedInState = authState
        let currentConnectionState = connectionState
        if let key = onboardingDoneKey { UserDefaults.standard.removeObject(forKey: key) }
        if case .signedIn(let email) = signedInState {
            UserDefaults.standard.removeObject(forKey: "nori.diagnostics.modelSelection.v1." + email.lowercased())
        }
        UserDefaults.standard.removeObject(forKey: "nori.appearance")
        dataResetGeneration &+= 1
        sessionGeneration &+= 1
        clearUserState(preserveIdentity: true)
        authState = signedInState
        connectionState = currentConnectionState
    }

    private func nextPreferencesRead() -> UInt64 {
        preferencesReadSequence &+= 1
        return preferencesReadSequence
    }

    @discardableResult
    private func applyPreferences(_ latest: Preferences, readID: UInt64) -> Bool {
        guard readID >= appliedPreferencesRead else { return true }
        appliedPreferencesRead = readID
        if latest.dataResetPending == true {
            enterDataResetRecovery(baseline: latest.dataResetAt, baselineKnown: true, confirmed: true)
            preferences = latest
            hasLoadedPreferences = true
            hasLoadedSnapshot = true
            return true
        }
        let wasRecovering = dataResetRecoveryRequired
        let recovered = wasRecovering && resetRecoveryBaselineKnown &&
            latest.dataResetAt != nil && latest.dataResetAt != "" && latest.dataResetAt != resetRecoveryBaseline
        if recovered && !resetRecoveryHasClearedData {
            clearLocalDataForReset()
        }
        var detectedReset = false
        if let resetAt = latest.dataResetAt, !resetAt.isEmpty,
           let key = observedResetKey,
           UserDefaults.standard.string(forKey: key) != resetAt {
            if !isAwaitingResetMarker && !wasRecovering {
                clearLocalDataForReset()
                detectedReset = true
            }
            UserDefaults.standard.set(resetAt, forKey: key)
        }
        isAwaitingResetMarker = false
        if wasRecovering {
            dataResetRecoveryRequired = false
            resetRecoveryBaseline = nil
            resetRecoveryBaselineKnown = false
            resetRecoveryHasClearedData = false
            if recovered {
                Task { [weak self] in
                    await self?.signOut()
                }
                return true
            }
            detectedReset = true
            dataResetStatusRequired = true
        }
        preferences = latest
        hasLoadedPreferences = true
        if detectedReset {
            hasLoadedSnapshot = true
            startChatSession()
            Task { [weak self] in
                await self?.refreshAll()
            }
        }
        return detectedReset
    }

    private func clearUserState(preserveIdentity: Bool = false) {
        stopEventStream()
        chatContextTask?.cancel()
        chatContextTask = nil
        toastTask?.cancel()
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
        capabilities = nil
        assistantSummary = nil
        lastPing = nil
        session = Session()
        workItems = []
        workDetails = [:]
        interactions = []
        hasLoadedWorkItems = false
        hasLoadedInteractions = false
        mcpServers = []
        mcpTools = [:]
        connections = []
        integrations = []
        libraryItems = []
        searchHits = []
        coreMemories = []
        memoryLogs = []
        workingMemory = WorkingMemory()
        hasLoadedWorkingMemory = false
        automations = []
        goals = []
        permissions = []
        preferences = Preferences()
        hasLoadedPreferences = false
        modelKeys = ModelKeysResponse()
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
        modelSettings = nil
        homeConnectors = nil
        modelKeyError = nil
        dataResetRecoveryRequired = false
        dataResetStatusRequired = false
        resetRecoveryBaseline = nil
        resetRecoveryBaselineKnown = false
        resetRecoveryHasClearedData = false
    }

    private func clearTransportIdentity() async {
        if let liveTransport {
            await liveTransport.clearCachedIdentity()
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
        async let onboardingTask: Void = refreshOnboardingStatus()
        _ = await (summaryTask, sessionTask, workTask, onboardingTask)
        if authState.isSignedIn {
            startEventStream()
        }
    }

    public func refreshSummary() async {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchAssistantSummary()
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            assistantSummary = latest
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            _ = await handleAuthFailureIfNeeded(error)
        }
    }

    public func refreshSession() async {
        let generation = sessionGeneration
        do {
            let freshSession = try await transport.fetchSession(before: nil)
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            applySession(freshSession)
            if !hasLoadedPreferences {
                await refreshOnboardingStatus()
            }
        } catch {
            guard generation == sessionGeneration else { return }
            if !(await handleAuthFailureIfNeeded(error)) {
                lastError = error.localizedDescription
            }
        }
    }

    private func refreshInteractions() async {
        let generation = sessionGeneration
        interactionsRefreshID &+= 1
        let refreshID = interactionsRefreshID
        do {
            let latest = try await transport.fetchInteractions()
            guard generation == sessionGeneration, refreshID > settledInteractionsRefreshID, !Task.isCancelled else { return }
            settledInteractionsRefreshID = refreshID
            interactions = latest
            hasLoadedInteractions = true
        } catch {
            guard generation == sessionGeneration, refreshID > settledInteractionsRefreshID, !Task.isCancelled else { return }
            settledInteractionsRefreshID = refreshID
            hasLoadedInteractions = false
            if !(await handleAuthFailureIfNeeded(error)), generation == sessionGeneration {
                await refreshSummary()
            }
        }
    }

    public func refreshWorkAndInteractions() async {
        let generation = sessionGeneration
        workRefreshID &+= 1
        let refreshID = workRefreshID
        async let interactionsTask: Void = refreshInteractions()
        do {
            let work = try await transport.fetchWorkItems(bucket: nil)
            if generation == sessionGeneration, refreshID > settledWorkRefreshID, !Task.isCancelled {
                settledWorkRefreshID = refreshID
                workItems = work
                hasLoadedWorkItems = true
                reportFailedWorkItemsIfNeeded(work)
                assistantSummary?.working = activeWorkItems.count
            }
        } catch {
            if generation == sessionGeneration, refreshID > settledWorkRefreshID, !Task.isCancelled {
                settledWorkRefreshID = refreshID
                hasLoadedWorkItems = false
                _ = await handleAuthFailureIfNeeded(error)
            }
        }
        await interactionsTask
    }

    private func markWorkChanged() {
        workRefreshID &+= 1
        settledWorkRefreshID = workRefreshID
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
        async let integrationsResult: Result<[IntegrationSummary], Error> = {
            do { return .success(try await activeTransport.fetchIntegrations()) }
            catch { return .failure(error) }
        }()

        let (serversRes, connsRes, integrationsRes) = await (serversResult, connectionsResult, integrationsResult)
        guard generation == sessionGeneration else { return }
        if case .failure(let err) = serversRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = connsRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = integrationsRes, await handleAuthFailureIfNeeded(err) { return }

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
        if case .success(let items) = integrationsRes {
            self.integrations = items
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
        let preferencesReadID = nextPreferencesRead()
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
        guard generation == sessionGeneration, !Task.isCancelled else { return }
        if case .failure(let err) = permsRes, await handleAuthFailureIfNeeded(err) { return }
        if case .failure(let err) = prefsRes, await handleAuthFailureIfNeeded(err) { return }

        if case .success(let perms) = permsRes {
            self.permissions = perms
        }
        if case .success(let prefs) = prefsRes {
            applyPreferences(prefs, readID: preferencesReadID)
        }
    }

    private func refreshOnboardingStatus() async {
        let generation = sessionGeneration
        let preferencesReadID = nextPreferencesRead()
        do {
            let latest = try await transport.fetchPreferences()
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            applyPreferences(latest, readID: preferencesReadID)
            if hasLoadedSession { hasLoadedSnapshot = true }
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            _ = await handleAuthFailureIfNeeded(error)
        }
    }

    public func refreshAll() async {
        let generation = sessionGeneration
        let preferencesReadID = nextPreferencesRead()
        isLoading = true
        defer { if generation == sessionGeneration { isLoading = false } }
        connectionState = .connecting
        do {
            let snapshot = try await transport.fetchFullSnapshot()
            guard generation == sessionGeneration else { return }
            applySnapshot(snapshot, preferencesReadID: preferencesReadID)
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

    private func applySnapshot(_ snapshot: NoriFullSnapshot, preferencesReadID: UInt64) {
        if snapshot.didLoadPreferences, applyPreferences(snapshot.preferences, readID: preferencesReadID) {
            return
        }
        self.capabilities = snapshot.capabilities
        self.assistantSummary = nil
        applySession(snapshot.session)
        hasLoadedWorkItems = snapshot.didLoadWorkItems
        hasLoadedInteractions = snapshot.didLoadInteractions
        if snapshot.didLoadWorkItems {
            markWorkChanged()
            workItems = snapshot.workItems
        }
        if snapshot.didLoadInteractions {
            interactionsRefreshID &+= 1
            settledInteractionsRefreshID = interactionsRefreshID
            interactions = snapshot.interactions
        }
        self.mcpServers = snapshot.mcpServers
        self.mcpTools = snapshot.mcpTools
        self.connections = snapshot.connections
        self.libraryItems = snapshot.libraryItems
        self.coreMemories = snapshot.coreMemories
        self.memoryLogs = snapshot.memoryLogs
        hasLoadedWorkingMemory = snapshot.didLoadWorkingMemory
        if snapshot.didLoadWorkingMemory {
            workingMemory = snapshot.workingMemory
        }
        self.automations = snapshot.automations
        self.goals = snapshot.goals
        self.permissions = snapshot.permissions
        self.modelKeys = snapshot.modelKeys
        if hasLoadedPreferences && hasLoadedSession {
            hasLoadedSnapshot = true
        }
        reportFailedWorkItemsIfNeeded(workItems)
    }

    private func applySession(_ snapshot: Session) {
        session = snapshot
        hasLoadedSession = true
        isThinking = snapshot.conversation?.processing == true
        finishObservedReplies(in: snapshot.messages)
    }

    private func finishSubmitSpan(turnID: String, outcome: String, status: NoriSpanStatus) {
        guard let entry = submitSpans.removeValue(forKey: turnID) else { return }
        entry.timeout.cancel()
        entry.span.setTag("nori.outcome", value: outcome)
        entry.span.finish(status: status)
    }

    private func finishObservedReplies(in messages: [Message]) {
        guard !submitSpans.isEmpty else { return }
        for message in messages where message.role == .assistant && !message.isSuperseded {
            if let turnID = message.turnID {
                if message.status == .failed {
                    finishSubmitSpan(turnID: turnID, outcome: "error", status: .error)
                } else {
                    finishSubmitSpan(turnID: turnID, outcome: "reply", status: .ok)
                }
            }
        }
    }

    private func reportFailedWorkItemsIfNeeded(_ items: [WorkItem]) {
        for item in items where item.status == .failed {
            let originMessageID = item.origin.messageID
            let turnID = session.messages.first { $0.id == originMessageID }?.turnID
            reportFailedTaskIfNeeded(taskID: item.taskID, errorCode: item.error?.code, turnID: turnID)
        }
    }

    private func reportFailedTaskIfNeeded(taskID: String, errorCode: String? = nil, turnID: String? = nil) {
        guard NoriErrorDiagnostics.isSafeIdentifier(taskID), reportedFailedTaskIDs.insert(taskID).inserted else { return }
        var attrs: [String: String] = [
            "nori.task.id": taskID,
            "nori.task.status": "failed",
            "nori.client.platform": "ios"
        ]
        if let uid = NoriTelemetryRuntime.shared.currentUserID {
            attrs["nori.user.id"] = uid
        }
        if let errorCode, NoriErrorDiagnostics.isSafeIdentifier(errorCode) {
            attrs["nori.error.code"] = errorCode
        }
        if let turnID, NoriErrorDiagnostics.isSafeIdentifier(turnID) {
            attrs["nori.turn.id"] = turnID
        }
        NoriTelemetryRuntime.$parentSpan.withValue(turnID.flatMap { submitSpans[$0]?.span }) {
            NoriLogger.store.error("nori.task.failed", attributes: attrs)
            NoriTelemetryRuntime.shared.captureError(
                NoriAPIError(message: "Task failed"),
                category: "task.failed",
                tags: attrs
            )
        }
    }

    private func startChatSession() {
        startEventStream()
        chatContextTask = Task { [weak self] in
            guard let self, !Task.isCancelled else { return }
            async let capabilitiesTask: Void = self.refreshCapabilities()
            async let summaryTask: Void = self.refreshSummary()
            async let interactionsTask: Void = self.refreshInteractions()
            async let onboardingTask: Void = self.refreshOnboardingStatus()
            _ = await (capabilitiesTask, summaryTask, interactionsTask, onboardingTask)
        }
    }

    private func refreshCapabilities() async {
        let generation = sessionGeneration
        if let latest = try? await transport.fetchCapabilities(),
           generation == sessionGeneration, !Task.isCancelled {
            capabilities = latest
        }
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
                NoriLogger.store.info("reply received", attributes: message.turnID.map { ["nori.turn.id": $0] } ?? [:], breadcrumb: true)
                finishObservedReplies(in: [message])
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
            guard generation == sessionGeneration else { return }
            if !Task.isCancelled, run.status == .failed {
                reportFailedTaskIfNeeded(taskID: run.id)
            }
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

        var spanTags: [String: String] = [
            "nori.turn.id": clientID,
            "nori.client.platform": "ios"
        ]
        if let uid = NoriTelemetryRuntime.shared.currentUserID {
            spanTags["nori.user.id"] = uid
        }
        let submitSpan = NoriTelemetryRuntime.shared.startSpan(
            name: "nori.submit_to_reply",
            operation: "nori.turn",
            tags: spanTags
        )
        let timeout = Task { [weak self] in
            do {
                try await Task.sleep(for: .seconds(120))
                self?.finishSubmitSpan(turnID: clientID, outcome: "timeout", status: .unknown)
            } catch {}
        }
        submitSpans[clientID] = (submitSpan, timeout)
        NoriLogger.store.info("turn submitted", attributes: ["nori.turn.id": clientID], breadcrumb: true)

        do {
            let attachmentIDs = attachments.map(\.id)
            var updated = try await NoriTelemetryRuntime.$parentSpan.withValue(submitSpan) {
                try await transport.sendTurn(
                    id: clientID,
                    message: trimmed,
                    attachmentIDs: attachmentIDs,
                    modelSelection: nil
                )
            }
            guard generation == sessionGeneration else { return false }
            if !updated.messages.contains(where: { $0.id == expectedUserMessageID || $0.turnID == clientID && $0.role == .user }) {
                var savedBubble = optimistic
                savedBubble.receipt = .saved
                updated.messages.append(savedBubble)
            }
            self.session = updated
            modelKeyError = nil
            isThinking = updated.conversation?.processing == true || updated.conversation?.pending == true
            finishObservedReplies(in: updated.messages)
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            session.messages.removeAll { $0.id == expectedUserMessageID }
            isThinking = false
            lastError = error.localizedDescription
            if let apiError = error as? NoriAPIError, apiError.code == "model_key_required" {
                modelKeyError = apiError.message
            }
            if Task.isCancelled || error is CancellationError || (error as? URLError)?.code == .cancelled {
                finishSubmitSpan(turnID: clientID, outcome: "cancelled", status: .cancelled)
            } else {
                finishSubmitSpan(turnID: clientID, outcome: "error", status: .error)
            }
            var errTags = NoriErrorDiagnostics.tags(for: error)
            errTags["nori.turn.id"] = clientID
            NoriLogger.store.error("turn.submit_failed", attributes: errTags)
            if await handleAuthFailureIfNeeded(error) { return false }
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
                interactionsRefreshID &+= 1
                settledInteractionsRefreshID = interactionsRefreshID
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
                markWorkChanged()
                workItems = items
                hasLoadedWorkItems = true
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
                markWorkChanged()
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
            markWorkChanged()
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

    public func connectGoogle(capabilities: [String]) async -> AuthAttempt? {
        let generation = sessionGeneration
        do {
            let connection: Connection
            if let existing = connections.first(where: {
                $0.integrationID == "google" && $0.status != .disconnecting
            }) {
                connection = existing
            } else {
                let receipt = try await transport.createConnection(
                    integrationID: "google", requestID: UUID().uuidString.lowercased()
                )
                guard generation == sessionGeneration else { return nil }
                connection = receipt.resource
                connections.append(connection)
            }
            let receipt = try await transport.performConnectionAction(
                connectionID: connection.id,
                expectedRevision: connection.revision,
                action: "connect",
                returnTo: NoriConfig.oauthCallbackURL,
                capabilities: capabilities,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return nil }
            if let index = connections.firstIndex(where: { $0.id == connection.id }) {
                connections[index] = receipt.resource
            }
            return receipt.auth
        } catch {
            guard generation == sessionGeneration else { return nil }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return nil
        }
    }

    public func startGoogleDrivePicker(connectionID: String) async -> AuthAttempt? {
        let generation = sessionGeneration
        do {
            let attempt = try await transport.startGoogleDrivePicker(
                connectionID: connectionID, returnTo: NoriConfig.oauthCallbackURL
            )
            guard generation == sessionGeneration else { return nil }
            return attempt
        } catch {
            guard generation == sessionGeneration else { return nil }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return nil
        }
    }

    public func loadGoogleDriveFiles(connectionID: String) async -> [ConnectionResource]? {
        let generation = sessionGeneration
        do {
            let resources = try await transport.fetchConnectionResources(connectionID: connectionID)
            guard generation == sessionGeneration else { return nil }
            return resources.filter { $0.kind == "drive_file" }
        } catch {
            guard generation == sessionGeneration else { return nil }
            showToast(error.localizedDescription)
            return nil
        }
    }

    public func disconnectGoogle(connectionID: String) async {
        let generation = sessionGeneration
        guard let connection = connections.first(where: { $0.id == connectionID }) else { return }
        do {
            let receipt = try await transport.performConnectionAction(
                connectionID: connectionID,
                expectedRevision: connection.revision,
                action: "disconnect",
                returnTo: nil,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if let index = connections.firstIndex(where: { $0.id == connectionID }) {
                connections[index] = receipt.resource
            }
            await refreshApps()
            guard generation == sessionGeneration else { return }
            if let message = connections.first(where: { $0.id == connectionID })?.health.message, !message.isEmpty {
                showToast(message)
            } else {
                showToast("Disconnected Google")
            }
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
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
                markWorkChanged()
                workItems = items
                hasLoadedWorkItems = true
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
            appliedPreferencesRead = nextPreferencesRead()
            preferences = receipt.resource
            showToast("Preferences saved")
        } catch {
            guard generation == sessionGeneration else { return }
            if await handleAuthFailureIfNeeded(error) { return }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
        }
    }

    public func refreshModelKeys() async {
        let generation = sessionGeneration
        do {
            try await refreshModelKeysThrowing()
            if modelSettings == nil && modelKeys.byokRequired && !hasConfiguredModelKey {
                try? await refreshModelSettings()
            }
        } catch is CancellationError {
            return
        } catch {
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
        }
    }

    public func fetchAPICosts(days: Int) async throws -> APICostResponse {
        let generation = sessionGeneration
        do {
            let result = try await transport.fetchAPICosts(days: days)
            guard generation == sessionGeneration, !Task.isCancelled else { throw CancellationError() }
            return result
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func refreshModelSettings() async throws {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchModelSettings()
            guard generation == sessionGeneration else { throw CancellationError() }
            modelSettings = latest
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func refreshHomeConnectors() async throws {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchHomeConnectors()
            guard generation == sessionGeneration else { throw CancellationError() }
            homeConnectors = latest
        } catch let error as NoriAPIError where error.statusCode == 404 {
            guard generation == sessionGeneration else { throw CancellationError() }
            homeConnectors = nil
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func lookupHomePairing(_ code: String) async throws -> HomePairing {
        let generation = sessionGeneration
        let result = try await transport.lookupHomePairing(code)
        guard generation == sessionGeneration else { throw CancellationError() }
        return result
    }

    public func approveHomePairing(_ code: String) async throws {
        let generation = sessionGeneration
        try await transport.approveHomePairing(code)
        guard generation == sessionGeneration else { throw CancellationError() }
        try? await refreshHomeConnectors()
        try? await refreshModelSettings()
    }

    public func testHomeConnector(_ id: String) async throws {
        let generation = sessionGeneration
        let result = try await transport.testHomeConnector(id)
        guard generation == sessionGeneration else { throw CancellationError() }
        guard result.status == "ok" else { throw NoriAPIError(message: "Connection test did not complete.") }
    }

    public func revokeHomeConnector(_ id: String) async throws {
        let generation = sessionGeneration
        try await transport.revokeHomeConnector(id)
        guard generation == sessionGeneration else { throw CancellationError() }
        try? await refreshHomeConnectors()
        try? await refreshModelSettings()
    }

    public func saveModelSettings(_ selection: ModelSelection, expectedRevision: Int? = nil) async throws {
        let generation = sessionGeneration
        guard let settings = modelSettings else {
            throw NoriAPIError(message: "Reload model settings and try again.")
        }
        do {
            let request = SaveModelSettingsRequest(
                selection: selection,
                expectedRevision: expectedRevision ?? settings.revision
            )
            let updated = try await transport.saveModelSettings(request)
            guard generation == sessionGeneration else { throw CancellationError() }
            modelSettings = updated
            modelKeyError = nil
            showToast("Models saved")
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            if let apiError = error as? NoriAPIError, apiError.isRevisionConflict, apiError.code != "data_reset_pending" {
                try? await refreshModelSettings()
            }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func refreshModelKeysThrowing() async throws {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchModelKeys()
            guard generation == sessionGeneration else { return }
            modelKeys = latest
        } catch {
            guard generation == sessionGeneration else { return }
            if await handleAuthFailureIfNeeded(error) {
                return
            }
            throw error
        }
    }

    @discardableResult
    public func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider {
        let generation = sessionGeneration
        do {
            let updated = try await transport.saveModelKey(provider: provider, apiKey: apiKey)
            guard generation == sessionGeneration else { throw CancellationError() }
            upsertModelKeyProvider(updated)
            modelKeyError = nil
            showToast("\(updated.label) key saved")
            return updated
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            if await handleAuthFailureIfNeeded(error) {
                throw error
            }
            lastError = error.localizedDescription
            throw error
        }
    }

    @discardableResult
    public func deleteModelKey(provider: String) async throws -> ModelKeyProvider {
        let generation = sessionGeneration
        do {
            let updated = try await transport.deleteModelKey(provider: provider)
            guard generation == sessionGeneration else { throw CancellationError() }
            upsertModelKeyProvider(updated)
            showToast("\(updated.label) key removed")
            return updated
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            if await handleAuthFailureIfNeeded(error) {
                throw error
            }
            lastError = error.localizedDescription
            throw error
        }
    }

    private func upsertModelKeyProvider(_ updated: ModelKeyProvider) {
        if let idx = modelKeys.providers.firstIndex(where: { $0.provider == updated.provider }) {
            modelKeys.providers[idx] = updated
        } else {
            modelKeys.providers.append(updated)
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
