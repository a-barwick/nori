import Foundation

extension NoriStore {
    public func pauseForBackground() {
        stopEventStream()
        streamingFlushTask?.cancel()
        streamingFlushTask = nil
        pendingStreamingText = nil
        cancelStreamRefreshTasks()
        chatContextTask?.cancel()
        chatContextTask = nil
        cancelInteractionFollowUpTasks()
    }

    public func resumeFromBackground() async {
        guard authState.isSignedIn else { return }
        async let summaryTask: Void = refreshSummary(force: true)
        async let sessionTask: Void = refreshSession(force: true)
        async let workTask: Void = refreshWorkAndInteractions(force: true)
        async let onboardingTask: Void = refreshOnboardingStatus()
        _ = await (summaryTask, sessionTask, workTask, onboardingTask)
        if authState.isSignedIn, !Task.isCancelled {
            startEventStream()
        }
    }

    public func refreshSummary(force: Bool = true) async {
        guard !isRefreshFresh(.summary, force: force) else { return }
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchAssistantSummary()
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            assistantSummary = latest
            markRefreshed(.summary)
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            _ = await handleAuthFailureIfNeeded(error)
        }
    }

    public func refreshSession(force: Bool = true) async {
        guard !isRefreshFresh(.session, force: force) else { return }
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

    func refreshOnboardingStatus() async {
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
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            applySnapshot(snapshot, preferencesReadID: preferencesReadID)
            connectionState = .connected
            lastError = snapshot.partialErrors.values.first
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            if await handleAuthFailureIfNeeded(error) { return }
            guard generation == sessionGeneration else { return }
            lastError = error.localizedDescription
            connectionState = .reconnecting
        }
    }

    func applySnapshot(_ snapshot: NoriFullSnapshot, preferencesReadID: UInt64) {
        if snapshot.didLoadPreferences, applyPreferences(snapshot.preferences, readID: preferencesReadID) {
            return
        }
        if snapshot.didLoadCapabilities {
            applyCapabilities(snapshot.capabilities)
        }
        self.assistantSummary = nil
        applySession(snapshot.session)
        hasLoadedWorkItems = snapshot.didLoadWorkItems
        hasLoadedInteractions = snapshot.didLoadInteractions
        if snapshot.didLoadWorkItems {
            markWorkChanged()
            workItems = snapshot.workItems
            markRefreshed(.workAndInteractions)
        }
        if snapshot.didLoadInteractions {
            interactionsRefreshID &+= 1
            settledInteractionsRefreshID = interactionsRefreshID
            interactions = snapshot.interactions
        }
        if snapshot.didLoadMCPServers {
            applyMCPServers(snapshot.mcpServers, tools: snapshot.mcpTools)
        }
        mcpServersRead = NoriReadStatus(didLoad: snapshot.didLoadMCPServers, error: snapshot.partialErrors["mcp"])
        if snapshot.didLoadConnections {
            self.connections = snapshot.connections
        }
        connectionsRead = NoriReadStatus(didLoad: snapshot.didLoadConnections, error: snapshot.partialErrors["connections"])
        // Only refreshApps loads the integration catalog.
        if !snapshot.didLoadMCPServers || !snapshot.didLoadConnections {
            updateRefreshFreshness(.apps, didLoad: false)
        }
        if snapshot.didLoadLibraryItems {
            self.libraryItems = snapshot.libraryItems
        }
        libraryRead = NoriReadStatus(didLoad: snapshot.didLoadLibraryItems, error: snapshot.partialErrors["library"])
        updateRefreshFreshness(.library, didLoad: snapshot.didLoadLibraryItems)
        if snapshot.didLoadCoreMemories {
            self.coreMemories = snapshot.coreMemories
        }
        coreMemoriesRead = NoriReadStatus(didLoad: snapshot.didLoadCoreMemories, error: snapshot.partialErrors["core_memories"])
        if snapshot.didLoadMemoryLogs {
            self.memoryLogs = snapshot.memoryLogs
        }
        hasLoadedWorkingMemory = snapshot.didLoadWorkingMemory || hasLoadedWorkingMemory
        if snapshot.didLoadWorkingMemory {
            workingMemory = snapshot.workingMemory
        }
        memoryRefreshError = snapshot.partialErrors["core_memories"]
            ?? snapshot.partialErrors["memory_logs"]
            ?? snapshot.partialErrors["working_memory"]
        updateRefreshFreshness(
            .memory,
            didLoad: snapshot.didLoadCoreMemories && snapshot.didLoadMemoryLogs && snapshot.didLoadWorkingMemory
        )
        if snapshot.didLoadAutomations {
            self.automations = snapshot.automations
        }
        automationsRead = NoriReadStatus(didLoad: snapshot.didLoadAutomations, error: snapshot.partialErrors["automations"])
        updateRefreshFreshness(.automations, didLoad: snapshot.didLoadAutomations)
        if snapshot.didLoadPermissions {
            self.permissions = snapshot.permissions
        }
        permissionsRead = NoriReadStatus(didLoad: snapshot.didLoadPermissions, error: snapshot.partialErrors["permissions"])
        updateRefreshFreshness(
            .permissionsAndPreferences,
            didLoad: snapshot.didLoadPermissions && snapshot.didLoadPreferences
        )
        if snapshot.didLoadModelKeys {
            self.modelKeys = snapshot.modelKeys
            markRefreshed(.modelKeys)
        }
        if snapshot.didLoadServiceKeys {
            self.serviceKeys = snapshot.serviceKeys
            markRefreshed(.serviceKeys)
        }
        if hasLoadedPreferences && hasLoadedSession {
            hasLoadedSnapshot = true
        }
        reportFailedWorkItemsIfNeeded(workItems)
    }

    func applySession(_ snapshot: Session) {
        session = snapshot
        hasLoadedSession = true
        markRefreshed(.session)
        isThinking = snapshot.conversation?.processing == true
        finishObservedReplies(in: snapshot.messages)
    }

    func finishSubmitSpan(turnID: String, outcome: String, status: NoriSpanStatus) {
        guard let entry = submitSpans.removeValue(forKey: turnID) else { return }
        entry.timeout.cancel()
        entry.span.setTag("nori.outcome", value: outcome)
        entry.span.finish(status: status)
    }

    func finishObservedReplies(in messages: [Message]) {
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

    func reportFailedWorkItemsIfNeeded(_ items: [WorkItem]) {
        for item in items where item.status == .failed {
            let originMessageID = item.origin.messageID
            let turnID = session.messages.first { $0.id == originMessageID }?.turnID
            reportFailedTaskIfNeeded(taskID: item.taskID, errorCode: item.error?.code, turnID: turnID)
        }
    }

    func reportFailedTaskIfNeeded(taskID: String, errorCode: String? = nil, turnID: String? = nil) {
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

    func startChatContextTasks() {
        chatContextTask?.cancel()
        chatContextTask = Task { [weak self] in
            guard let self, !Task.isCancelled else { return }
            async let capabilitiesTask: Void = self.refreshCapabilities()
            async let summaryTask: Void = self.refreshSummary(force: true)
            async let interactionsTask: Void = self.refreshInteractions()
            async let onboardingTask: Void = self.refreshOnboardingStatus()
            _ = await (capabilitiesTask, summaryTask, interactionsTask, onboardingTask)
        }
    }

    func startChatSession() {
        startEventStream()
        startChatContextTasks()
    }

    public func refreshCapabilities() async {
        let generation = sessionGeneration
        do {
            let latest = try await transport.fetchCapabilities()
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            applyCapabilities(latest)
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            _ = await handleAuthFailureIfNeeded(error)
        }
    }

    func applyCapabilities(_ latest: Capabilities?) {
        let hadPush = capabilities.map { $0.assistantFeatures?["push_notifications"] != false } ?? false
        capabilities = latest
        guard let latest else { return }
        if !hadPush, latest.assistantFeatures?["push_notifications"] != false {
            requestPushAuthorizationIfNeeded()
        }
        guard let entitlement = latest.entitlement else {
            if billingAccessBlock?.code == .creditExhausted || billingAccessBlock?.code == .subscriptionInactive {
                billingAccessBlock = nil
            }
            return
        }
        if !entitlement.suspended, billingAccessBlock?.code == .accountSuspended {
            billingAccessBlock = nil
        }
        if !entitlement.isInactive, billingAccessBlock?.code == .subscriptionInactive {
            billingAccessBlock = nil
        }
        if (entitlement.creditRemainingUSD > 0 || entitlement.allowBYOK),
           billingAccessBlock?.code == .creditExhausted,
           !entitlement.isTrialCreditsExhausted {
            if entitlement.creditRemainingUSD > 0 {
                billingAccessBlock = nil
            }
        }
    }

    public func startEventStream() {
        stopEventStream()
        guard authState.isSignedIn else { return }
        let startPosition = session.position ?? 0
        let activeTransport = transport
        let generation = sessionGeneration

        eventTask = Task { [weak self] in
            let stream = activeTransport.eventStream(after: startPosition)
            do {
                for try await event in stream {
                    guard let self, !Task.isCancelled, self.sessionGeneration == generation else { break }
                    await self.handleStreamEvent(event)
                }
                if let self, !Task.isCancelled, self.sessionGeneration == generation {
                    self.flushPendingStreamingAssistant()
                }
            } catch {
                guard let self, !Task.isCancelled, self.sessionGeneration == generation else { return }
                self.flushPendingStreamingAssistant()
                if await self.handleAuthFailureIfNeeded(error) { return }
                guard self.sessionGeneration == generation else { return }
                self.connectionState = .reconnecting
            }
        }
    }

    public func stopEventStream() {
        eventTask?.cancel()
        eventTask = nil
    }

    private func clearStreamingAssistant() {
        streamingFlushTask?.cancel()
        streamingFlushTask = nil
        pendingStreamingText = nil
        streamingAssistantText = nil
        streamingTurnID = nil
        isStreamingAssistant = false
    }

    private func flushPendingStreamingAssistant() {
        streamingFlushTask?.cancel()
        streamingFlushTask = nil
        guard let pending = pendingStreamingText else { return }
        pendingStreamingText = nil
        streamingAssistantText = pending
        isStreamingAssistant = !pending.isEmpty
    }

    private func scheduleStreamingAssistantText(_ text: String) {
        if streamingAssistantText == nil {
            streamingFlushTask?.cancel()
            streamingFlushTask = nil
            pendingStreamingText = nil
            streamingAssistantText = text
            isStreamingAssistant = !text.isEmpty
            return
        }
        pendingStreamingText = text
        isStreamingAssistant = !text.isEmpty
        guard streamingFlushTask == nil else { return }
        let generation = sessionGeneration
        let interval = streamingFlushInterval
        streamingFlushTask = Task { [weak self] in
            try? await Task.sleep(for: interval)
            guard let self, !Task.isCancelled, self.sessionGeneration == generation else { return }
            self.streamingFlushTask = nil
            if let pending = self.pendingStreamingText {
                self.pendingStreamingText = nil
                self.streamingAssistantText = pending
                self.isStreamingAssistant = !pending.isEmpty
            }
        }
    }

    private func scheduleStreamRefresh(
        _ domain: StreamRefreshDomain,
        operation: @escaping @MainActor (NoriStore) async -> Void
    ) {
        if streamRefreshTasks[domain] != nil {
            streamRefreshNeedsRerun.insert(domain)
            return
        }
        let generation = sessionGeneration
        streamRefreshTasks[domain] = Task { [weak self] in
            defer {
                if let self, self.sessionGeneration == generation {
                    self.streamRefreshTasks.removeValue(forKey: domain)
                    self.streamRefreshNeedsRerun.remove(domain)
                }
            }
            while let self, !Task.isCancelled, self.sessionGeneration == generation {
                self.streamRefreshNeedsRerun.remove(domain)
                await operation(self)
                guard !Task.isCancelled, self.sessionGeneration == generation else { break }
                if !self.streamRefreshNeedsRerun.contains(domain) {
                    break
                }
            }
        }
    }

    static func shouldAcceptRunUpdate(existing: Run, incoming: Run) -> Bool {
        if (existing.position ?? 0) > (incoming.position ?? 0) { return false }
        if (existing.eventSequence ?? 0) > (incoming.eventSequence ?? 0) { return false }
        if (existing.incorporated ?? 0) > (incoming.incorporated ?? 0) { return false }
        if existing.status != .running && incoming.status == .running { return false }
        if existing.cancelRequested && !incoming.cancelRequested { return false }
        return true
    }

    func mergeRun(_ run: Run) {
        if let idx = session.runs.firstIndex(where: { $0.id == run.id }) {
            guard Self.shouldAcceptRunUpdate(existing: session.runs[idx], incoming: run) else { return }
            session.runs[idx] = run
        } else {
            session.runs.insert(run, at: 0)
        }
        session.runs.sort { $0.startedAt > $1.startedAt }
    }

    func mergeResult(_ result: SavedResult) {
        var existing = session.results ?? []
        if let idx = existing.firstIndex(where: { $0.id == result.id }) {
            existing[idx] = result
        } else {
            existing.append(result)
        }
        existing.sort { $0.createdAt > $1.createdAt }
        session.results = existing
    }

    func handleStreamEvent(_ event: NoriStreamEvent) async {
        let generation = sessionGeneration
        if case .connectionState(let state) = event {
            connectionState = state
            return
        }
        connectionState = .connected
        if case .reset(let pos) = event {
            session.position = pos
            scheduleStreamRefresh(.reset) { store in
                await store.refreshAll()
                guard generation == store.sessionGeneration else { return }
                if store.session.position == nil {
                    store.session.position = pos
                }
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
                    clearStreamingAssistant()
                }
                NoriLogger.store.info("reply received", attributes: message.turnID.map { ["nori.turn.id": $0] } ?? [:], breadcrumb: true)
                finishObservedReplies(in: [message])
            }
        case .assistantDelta(let turnID, let text):
            if text.isEmpty {
                if streamingTurnID == nil || streamingTurnID == turnID {
                    clearStreamingAssistant()
                }
            } else if !session.messages.contains(where: { $0.role == .assistant && !$0.isSuperseded && $0.turnID == turnID }) {
                streamingTurnID = turnID
                scheduleStreamingAssistantText(text)
            }
        case .task(_, let run):
            mergeRun(run)
            if run.status == .failed {
                reportFailedTaskIfNeeded(taskID: run.id)
            }
            scheduleStreamRefresh(.workAndInteractions) { store in
                await store.refreshWorkAndInteractions(force: true)
            }
        case .result(_, let result):
            mergeResult(result)
            scheduleStreamRefresh(.library) { store in
                await store.refreshLibrary(force: true)
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
                if !isThinking {
                    clearStreamingAssistant()
                }
            }
        case .memory:
            scheduleStreamRefresh(.memory) { store in
                await store.refreshMemory(force: true)
            }
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
            billingAccessBlock = nil
            isThinking = updated.conversation?.processing == true || updated.conversation?.pending == true
            finishObservedReplies(in: updated.messages)
            return true
        } catch {
            guard generation == sessionGeneration else { return false }
            session.messages.removeAll { $0.id == expectedUserMessageID }
            isThinking = false
            lastError = error.localizedDescription
            if let apiError = error as? NoriAPIError,
               apiError.code == "model_key_required" || apiError.code == "missing_api_key" {
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
            _ = await handleOperationError(error)
            return false
        }
    }

    public func uploadAttachment(data: Data, filename: String, mediaType: String) async throws -> Attachment {
        let generation = sessionGeneration
        let uploadID = UUID().uuidString.lowercased()
        do {
            let attachment = try await transport.uploadFile(id: uploadID, filename: filename, mediaType: mediaType, data: data)
            guard generation == sessionGeneration else { throw CancellationError() }
            return attachment
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }
}
