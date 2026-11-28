import Foundation

extension NoriStore {
    func refreshInteractions() async {
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

    public func refreshWorkAndInteractions(force: Bool = true) async {
        guard !isRefreshFresh(.workAndInteractions, force: force) else { return }
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
                markRefreshed(.workAndInteractions)
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

    func markWorkChanged() {
        workRefreshID &+= 1
        settledWorkRefreshID = workRefreshID
    }

    public func refreshOperations(connectionID: String? = nil) async throws -> [OperationProjection] {
        let generation = sessionGeneration
        do {
            let items = try await transport.fetchOperations(connectionID: connectionID)
            guard generation == sessionGeneration, !Task.isCancelled else { throw CancellationError() }
            if connectionID == nil {
                operations = items
            }
            return items
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleAuthFailureIfNeeded(error)
            throw error
        }
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
            _ = await handleOperationError(error, showToast: false)
            return workDetails[taskID]
        }
    }

    public func applyCurrentInteraction(_ current: Interaction) {
        interactionsRefreshID &+= 1
        settledInteractionsRefreshID = interactionsRefreshID
        if let idx = interactions.firstIndex(where: { $0.id == current.id }) {
            interactions[idx] = current
        } else {
            interactions.insert(current, at: 0)
        }
    }

    func cancelInteractionFollowUpTasks() {
        for task in interactionFollowUpTasks.values {
            task.cancel()
        }
        interactionFollowUpTasks.removeAll()
    }

    func waitForInteractionFollowUpCompletion() async {
        let tasks = Array(interactionFollowUpTasks.values)
        for task in tasks {
            await task.value
        }
    }

    @discardableResult
    public func resolveInteraction(_ interaction: Interaction, answer: InteractionAnswer) async -> InteractionSubmitOutcome {
        let generation = sessionGeneration
        let requestID = UUID().uuidString.lowercased()
        do {
            let receipt = try await transport.respondToInteraction(
                id: interaction.id,
                expectedRevision: interaction.revision,
                answer: answer,
                requestID: requestID
            )
            guard generation == sessionGeneration else { return .failed("Cancelled") }
            var wasPending = interaction.status == .pending
            if let idx = interactions.firstIndex(where: { $0.id == interaction.id }) {
                wasPending = interactions[idx].status == .pending
                interactionsRefreshID &+= 1
                settledInteractionsRefreshID = interactionsRefreshID
                interactions[idx] = receipt.resource
            }
            if wasPending,
               receipt.resource.status != .pending,
               let currentNeedsYou = assistantSummary?.needsYou,
               currentNeedsYou > 0 {
                assistantSummary?.needsYou = currentNeedsYou - 1
            }

            let shouldRefreshPermissions: Bool
            if case .approve(_, let rememberRule, let editedFields) = answer,
               rememberRule,
               editedFields?.isEmpty != false {
                shouldRefreshPermissions = true
            } else {
                shouldRefreshPermissions = false
            }

            let followUpID = UUID()
            let followUpTask = Task { @MainActor [weak self] in
                defer {
                    self?.interactionFollowUpTasks.removeValue(forKey: followUpID)
                }
                guard let self, generation == self.sessionGeneration, !Task.isCancelled else { return }
                let activeTransport = self.transport
                async let workAndInteractionsTask: Void = self.refreshWorkAndInteractions()
                async let summaryTask: Void = self.refreshSummary()
                async let permissionsTask: [PermissionRule]? = shouldRefreshPermissions
                    ? (try? await activeTransport.fetchPermissions())
                    : nil

                _ = await (workAndInteractionsTask, summaryTask)
                let freshPermissions = await permissionsTask
                guard generation == self.sessionGeneration, !Task.isCancelled else { return }
                if let freshPermissions { self.permissions = freshPermissions }
            }
            interactionFollowUpTasks[followUpID] = followUpTask

            switch answer {
            case .approve(_, let rememberRule, let editedFields):
                if let editedFields, !editedFields.isEmpty {
                    showToast("Sent")
                } else {
                    showToast(
                        rememberRule
                            ? "Always allowed — rule saved"
                            : "Allowed once"
                    )
                }
            case .decline:
                showToast(interaction.kind == .approval ? "Denied — nothing was sent" : "Declined")
            case .answer: showToast("Answer sent to Nori")
            case .chooseAccount: showToast("Account selected")
            case .resume: showToast("Nori is continuing")
            case .cancel: showToast("Task paused")
            }
            return .resolved(receipt.resource)
        } catch {
            guard generation == sessionGeneration else { return .failed("Cancelled") }
            if await handleAuthFailureIfNeeded(error) {
                return .failed(error.localizedDescription)
            }
            if let apiErr = error as? NoriAPIError, apiErr.isRevisionConflict {
                let current: Interaction?
                if let embedded = apiErr.decodedCurrent(as: Interaction.self) {
                    current = embedded
                } else {
                    current = try? await transport.fetchInteraction(id: interaction.id)
                }
                guard generation == sessionGeneration else { return .failed("Cancelled") }
                let conflictMessage = current?.status == .pending
                    ? "This changed since it was shown. Review the current version."
                    : "This was already answered or replaced."
                lastError = conflictMessage
                return .conflict(current: current, message: conflictMessage)
            }
            lastError = error.localizedDescription
            showToast(error.localizedDescription)
            return .failed(error.localizedDescription)
        }
    }

    @discardableResult
    public func approveInteraction(
        _ interaction: Interaction,
        rememberRule: Bool = false,
        editedFields: EditedApprovalFields? = nil
    ) async -> InteractionSubmitOutcome {
        let effectiveRemember = (editedFields?.isEmpty == false) ? false : rememberRule
        return await resolveInteraction(
            interaction,
            answer: .approve(
                actionDigest: interaction.actionDigest ?? "",
                rememberRule: effectiveRemember,
                editedFields: editedFields
            )
        )
    }

    public func fetchOrFindInteraction(id: String) async -> Interaction? {
        if let existing = interactions.first(where: { $0.id == id }) {
            return existing
        }
        let generation = sessionGeneration
        do {
            let fetched = try await transport.fetchInteraction(id: id)
            guard generation == sessionGeneration else { return nil }
            applyCurrentInteraction(fetched)
            return fetched
        } catch {
            guard generation == sessionGeneration else { return nil }
            _ = await handleOperationError(error, showToast: false)
            return nil
        }
    }

    @discardableResult
    public func declineInteraction(_ interaction: Interaction) async -> InteractionSubmitOutcome {
        await resolveInteraction(interaction, answer: .decline(actionDigest: interaction.actionDigest))
    }

    @discardableResult
    public func answerInteraction(_ interaction: Interaction, text: String, optionIDs: [String]? = nil) async -> InteractionSubmitOutcome {
        await resolveInteraction(interaction, answer: .answer(text: text, optionIDs: optionIDs))
    }

    @discardableResult
    public func chooseAccountInteraction(_ interaction: Interaction, connectionID: String, rememberAsDefault: Bool = false) async -> InteractionSubmitOutcome {
        await resolveInteraction(interaction, answer: .chooseAccount(connectionID: connectionID, rememberAsDefault: rememberAsDefault))
    }

    @discardableResult
    public func resumeComputerTakeover(_ interaction: Interaction, note: String? = nil) async -> InteractionSubmitOutcome {
        await resolveInteraction(interaction, answer: .resume(text: note))
    }

    @discardableResult
    public func cancelComputerTakeover(_ interaction: Interaction, note: String? = nil) async -> InteractionSubmitOutcome {
        await resolveInteraction(interaction, answer: .cancel(text: note))
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
            _ = await handleOperationError(error)
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
            _ = await handleOperationError(error)
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
            _ = await handleOperationError(error)
        }
    }

    public func refreshAutomations(force: Bool = true) async {
        guard !isRefreshFresh(.automations, force: force) else { return }
        let generation = sessionGeneration
        do {
            let autos = try await transport.fetchAutomations()
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            self.automations = autos
            automationsRead = .loaded
            markRefreshed(.automations)
        } catch {
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            if await handleAuthFailureIfNeeded(error) { return }
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            automationsRead = .failed(error.localizedDescription)
            updateRefreshFreshness(.automations, didLoad: false)
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
            _ = await handleAuthFailureIfNeeded(error)
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
            _ = await handleOperationError(error)
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
            _ = await handleOperationError(error)
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
            _ = await handleOperationError(error)
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
            _ = await handleOperationError(error)
        }
    }

    @discardableResult
    public func createAutomation(_ value: AutomationEditable) async throws -> Automation {
        let generation = sessionGeneration
        do {
            let receipt = try await transport.createAutomation(
                value,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { throw CancellationError() }
            upsertAutomation(receipt.resource)
            showToast("Routine created")
            return receipt.resource
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error, showToast: false)
            throw error
        }
    }

    @discardableResult
    public func saveAutomation(id: String, value: AutomationEditable) async throws -> Automation {
        let generation = sessionGeneration
        let current: Automation
        if let cached = automations.first(where: { $0.id == id }) {
            current = cached
        } else if let loaded = await loadAutomation(id: id) {
            current = loaded
        } else {
            throw NoriAPIError(message: "Routine not found.", statusCode: 404)
        }
        do {
            let receipt = try await transport.updateAutomation(
                id: id,
                expectedRevision: current.revision,
                value: value,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { throw CancellationError() }
            upsertAutomation(receipt.resource)
            showToast("Routine updated")
            return receipt.resource
        } catch {
            guard generation == sessionGeneration else { throw CancellationError() }
            _ = await handleOperationError(error, showToast: false)
            throw error
        }
    }

    public func snoozeAutomation(automationID: String, until: Date) async {
        let generation = sessionGeneration
        guard let auto = automations.first(where: { $0.id == automationID }) else { return }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        let iso = formatter.string(from: until)
        do {
            let receipt = try await transport.snoozeAutomation(
                id: automationID,
                expectedRevision: auto.revision,
                until: iso,
                requestID: UUID().uuidString.lowercased()
            )
            guard generation == sessionGeneration else { return }
            upsertAutomation(receipt.resource)
            showToast("Snoozed \(auto.title)")
        } catch {
            guard generation == sessionGeneration else { return }
            _ = await handleOperationError(error)
        }
    }

    public func loadAutomationOccurrences(automationID: String) async -> [AutomationOccurrence] {
        let generation = sessionGeneration
        do {
            let items = try await transport.fetchAutomationOccurrences(id: automationID)
            guard generation == sessionGeneration else { return [] }
            return items
        } catch {
            guard generation == sessionGeneration else { return [] }
            _ = await handleAuthFailureIfNeeded(error)
            return []
        }
    }
}
