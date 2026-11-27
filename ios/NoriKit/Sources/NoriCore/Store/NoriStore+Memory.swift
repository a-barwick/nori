import Foundation

extension NoriStore {
    public func refreshMemory() async {
        let generation = sessionGeneration
        let activeTransport = transport
        async let facts = captureMemoryRefresh { try await activeTransport.fetchCoreMemories() }
        async let logs = captureMemoryRefresh { try await activeTransport.fetchMemoryLogs() }
        async let working = captureMemoryRefresh { try await activeTransport.fetchWorkingMemory() }
        let (factsResult, logsResult, workingResult) = await (facts, logs, working)
        guard generation == sessionGeneration, !Task.isCancelled else { return }
        let errors = [factsResult.failure, logsResult.failure, workingResult.failure].compactMap { $0 }
        for err in errors {
            if await handleAuthFailureIfNeeded(err) {
                return
            }
        }
        if case .success(let latest) = factsResult { coreMemories = latest }
        if case .success(let latest) = logsResult { memoryLogs = latest }
        if case .success(let latest) = workingResult {
            workingMemory = latest
            hasLoadedWorkingMemory = true
        }
        memoryRefreshError = errors.first?.localizedDescription
    }

    public func searchMemory(query: String, layer: String? = nil, limit: Int = 20) async throws -> MemorySearchResponse {
        let generation = sessionGeneration
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            memorySearchHits = []
            return MemorySearchResponse(items: [], query: "")
        }
        do {
            let response = try await transport.searchMemory(query: trimmed, layer: layer, limit: limit)
            guard generation == sessionGeneration, !Task.isCancelled else { throw CancellationError() }
            memorySearchHits = response.items
            return response
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
    }

    public func addMemory(text: String, section: String? = nil, about: String? = nil, pinned: Bool = false) async {
        let generation = sessionGeneration
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
            guard generation == sessionGeneration else { return }
            upsertCoreMemory(receipt.resource)
            if let working = try? await transport.fetchWorkingMemory() {
                guard generation == sessionGeneration else { return }
                workingMemory = working
            }
            guard generation == sessionGeneration else { return }
            showToast("Saved to Core Memory")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    private func upsertCoreMemory(_ fact: MemoryFact) {
        if let idx = coreMemories.firstIndex(where: { $0.id == fact.id }) {
            coreMemories[idx] = fact
        } else {
            coreMemories.insert(fact, at: 0)
        }
    }

    @discardableResult
    public func updateMemory(
        memoryID: String,
        text: String,
        section: String? = nil,
        about: String? = nil,
        pinned: Bool
    ) async throws -> MemoryFact {
        let generation = sessionGeneration
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            throw NoriAPIError(message: "Enter something to remember.")
        }
        guard let current = coreMemories.first(where: { $0.id == memoryID }) else {
            throw NoriAPIError(message: "Memory not found.", statusCode: 404)
        }
        let cleanSection = section?.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanAbout = about?.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            let receipt = try await transport.updateMemory(
                id: memoryID,
                expectedRevision: current.revision,
                text: trimmed,
                section: (cleanSection?.isEmpty == false) ? cleanSection : nil,
                about: (cleanAbout?.isEmpty == false) ? cleanAbout : nil,
                pinned: pinned,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { throw CancellationError() }
            upsertCoreMemory(receipt.resource)
            if let working = try? await transport.fetchWorkingMemory() {
                guard generation == sessionGeneration else { throw CancellationError() }
                workingMemory = working
            }
            showToast("Memory updated")
            return receipt.resource
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error, showToast: false)
            throw error
        }
    }

    public func consolidateMemory() async {
        let generation = sessionGeneration
        do {
            try await transport.consolidateMemory(requestID: UUID().uuidString.lowercased())
            guard generation == sessionGeneration else { return }
            await refreshMemory()
            guard generation == sessionGeneration else { return }
            showToast("Memory consolidation requested")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func togglePinMemory(memoryID: String) async {
        let generation = sessionGeneration
        guard let fact = coreMemories.first(where: { $0.id == memoryID }) else { return }
        let action = fact.pinned ? "unpin" : "pin"
        do {
            let receipt = try await transport.performMemoryAction(
                id: memoryID,
                expectedRevision: fact.revision,
                action: action,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            if case .fact(let updated) = receipt.resource {
                upsertCoreMemory(updated)
                showToast(updated.pinned ? "Pinned to Core Memory" : "Unpinned memory")
            } else if let freshFacts = try? await transport.fetchCoreMemories() {
                guard generation == sessionGeneration else { return }
                coreMemories = freshFacts
                let nowPinned = coreMemories.first(where: { $0.id == memoryID })?.pinned ?? !fact.pinned
                showToast(nowPinned ? "Pinned to Core Memory" : "Unpinned memory")
            } else {
                guard generation == sessionGeneration else { return }
                showToast(!fact.pinned ? "Pinned to Core Memory" : "Unpinned memory")
            }
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func demoteMemory(memoryID: String) async {
        let generation = sessionGeneration
        guard let fact = coreMemories.first(where: { $0.id == memoryID }) else { return }
        do {
            _ = try await transport.performMemoryAction(
                id: memoryID,
                expectedRevision: fact.revision,
                action: "demote",
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            coreMemories.removeAll { $0.id == memoryID }
            let activeTransport = transport
            async let logsTask = try? activeTransport.fetchMemoryLogs()
            async let workingTask = try? activeTransport.fetchWorkingMemory()
            if let logs = await logsTask {
                guard generation == sessionGeneration else { return }
                memoryLogs = logs
            }
            if let working = await workingTask {
                guard generation == sessionGeneration else { return }
                workingMemory = working
            }
            guard generation == sessionGeneration else { return }
            showToast("Moved to Daily Log")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func forgetMemory(memoryID: String) async {
        let generation = sessionGeneration
        guard let fact = coreMemories.first(where: { $0.id == memoryID }) else { return }
        do {
            _ = try await transport.performMemoryAction(
                id: memoryID,
                expectedRevision: fact.revision,
                action: "forget",
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            coreMemories.removeAll { $0.id == memoryID }
            if let working = try? await transport.fetchWorkingMemory() {
                guard generation == sessionGeneration else { return }
                workingMemory = working
            }
            guard generation == sessionGeneration else { return }
            showToast("Memory forgotten")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func promoteLogEntry(
        date: String,
        entryID: String,
        text: String? = nil,
        section: String? = nil,
        about: String? = nil,
        pinned: Bool = false
    ) async {
        let generation = sessionGeneration
        guard let log = memoryLogs.first(where: { $0.date == date }) else { return }
        let cleanText = text?.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanSection = section?.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanAbout = about?.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            let receipt: MutationReceipt<MemoryLogEntryActionResource>
            if (cleanText?.isEmpty == false) || (cleanSection?.isEmpty == false) || (cleanAbout?.isEmpty == false) {
                receipt = try await transport.promoteMemoryLogEntry(
                    date: date,
                    entryID: entryID,
                    expectedRevision: log.revision,
                    text: (cleanText?.isEmpty == false) ? cleanText : nil,
                    section: (cleanSection?.isEmpty == false) ? cleanSection : nil,
                    about: (cleanAbout?.isEmpty == false) ? cleanAbout : nil,
                    pinned: pinned,
                    requestID: UUID().uuidString.lowercased()
                )
            } else {
                receipt = try await transport.performMemoryLogEntryAction(
                    date: date,
                    entryID: entryID,
                    expectedRevision: log.revision,
                    action: "promote",
                    pinned: pinned,
                    requestID: UUID().uuidString.lowercased()
                )
            }
            guard generation == sessionGeneration else { return }
            switch receipt.resource {
            case .promoted(let fact):
                upsertCoreMemory(fact)
                if let logIdx = memoryLogs.firstIndex(where: { $0.date == date }),
                   let entryIdx = memoryLogs[logIdx].entries.firstIndex(where: { $0.id == entryID }) {
                    memoryLogs[logIdx].entries[entryIdx].promoted = true
                }
            case .log(let updatedLog):
                if let idx = memoryLogs.firstIndex(where: { $0.date == date }) {
                    memoryLogs[idx] = updatedLog
                }
                if let facts = try? await transport.fetchCoreMemories() {
                    guard generation == sessionGeneration else { return }
                    coreMemories = facts
                }
            case .forgotten:
                if let facts = try? await transport.fetchCoreMemories() {
                    guard generation == sessionGeneration else { return }
                    coreMemories = facts
                }
            }
            if let working = try? await transport.fetchWorkingMemory() {
                guard generation == sessionGeneration else { return }
                workingMemory = working
            }
            guard generation == sessionGeneration else { return }
            showToast("Promoted to Core Memory")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func forgetLogEntry(date: String, entryID: String) async {
        let generation = sessionGeneration
        do {
            let log: MemoryLog
            if let cached = memoryLogs.first(where: { $0.date == date }) {
                log = cached
            } else {
                log = try await transport.fetchMemoryLog(date: date)
                guard generation == sessionGeneration else { return }
            }
            let receipt = try await transport.performMemoryLogEntryAction(
                date: date,
                entryID: entryID,
                expectedRevision: log.revision,
                action: "forget",
                pinned: false,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            switch receipt.resource {
            case .log(let updatedLog):
                if let idx = memoryLogs.firstIndex(where: { $0.date == date }) {
                    memoryLogs[idx] = updatedLog
                }
            case .forgotten, .promoted:
                if let logIdx = memoryLogs.firstIndex(where: { $0.date == date }) {
                    memoryLogs[logIdx].entries.removeAll { $0.id == entryID }
                    if memoryLogs[logIdx].entries.isEmpty {
                        memoryLogs.remove(at: logIdx)
                    }
                }
                if let freshLogs = try? await transport.fetchMemoryLogs() {
                    guard generation == sessionGeneration else { return }
                    memoryLogs = freshLogs
                }
            }
            guard generation == sessionGeneration else { return }
            showToast("Removed log entry")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func promoteWorkingMemoryItem(itemID: String) async {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.performWorkingMemoryAction(
                itemID: itemID,
                expectedRevision: workingMemory.revision,
                action: "promote",
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            switch receipt.resource {
            case .promoted(let fact):
                upsertCoreMemory(fact)
            case .workingMemory(let updatedWorking):
                workingMemory = updatedWorking
                if let facts = try? await transport.fetchCoreMemories() {
                    guard generation == sessionGeneration else { return }
                    coreMemories = facts
                }
            case .dismissed(let dismissed):
                workingMemory.items.removeAll { $0.id == dismissed.dismissedItemID || $0.id == itemID }
                if let facts = try? await transport.fetchCoreMemories() {
                    guard generation == sessionGeneration else { return }
                    coreMemories = facts
                }
            }
            if let freshWorking = try? await transport.fetchWorkingMemory() {
                guard generation == sessionGeneration else { return }
                workingMemory = freshWorking
            }
            guard generation == sessionGeneration else { return }
            showToast("Saved to Core Memory")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func dismissWorkingMemoryItem(itemID: String) async {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.performWorkingMemoryAction(
                itemID: itemID,
                expectedRevision: workingMemory.revision,
                action: "dismiss",
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            switch receipt.resource {
            case .workingMemory(let updatedWorking):
                workingMemory = updatedWorking
            case .dismissed(let dismissed):
                workingMemory.items.removeAll { $0.id == dismissed.dismissedItemID || $0.id == itemID }
                if let freshWorking = try? await transport.fetchWorkingMemory() {
                    guard generation == sessionGeneration else { return }
                    workingMemory = freshWorking
                }
            case .promoted:
                workingMemory.items.removeAll { $0.id == itemID }
            }
            guard generation == sessionGeneration else { return }
            showToast("Dismissed working note")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }
}

private func captureMemoryRefresh<T: Sendable>(
    _ operation: @Sendable () async throws -> T
) async -> Result<T, Error> {
    do { return .success(try await operation()) }
    catch { return .failure(error) }
}

private extension Result {
    var failure: Failure? {
        if case .failure(let error) = self { return error }
        return nil
    }
}
