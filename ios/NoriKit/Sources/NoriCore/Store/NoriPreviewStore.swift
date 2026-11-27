#if DEBUG
import Foundation

final class PreviewNoriTransport: NoriTransport, @unchecked Sendable {
    private let snapshot: NoriFullSnapshot
    private let details: [String: WorkDetail]

    init(snapshot: NoriFullSnapshot, details: [String: WorkDetail]) {
        self.snapshot = snapshot
        self.details = details
    }

    func ping() async -> HealthCheckResult {
        HealthCheckResult(ok: true, status: "ok", latencyMs: 18)
    }

    func fetchCapabilities() async throws -> Capabilities {
        snapshot.capabilities ?? Capabilities()
    }

    func fetchAssistantSummary() async throws -> AssistantSummary {
        AssistantSummary(
            needsYou: snapshot.interactions.filter { $0.status == .pending }.count,
            working: snapshot.workItems.filter { $0.status == .running && $0.bucket == .working }.count,
            appsAttention: 0
        )
    }

    func fetchSession(before cursor: String?) async throws -> Session { snapshot.session }
    func fetchFullSnapshot() async throws -> NoriFullSnapshot { snapshot }
    func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        AsyncThrowingStream { _ in }
    }

    func sendTurn(id: String, message: String, attachmentIDs: [String], modelSelection: ModelSelection?) async throws -> Session { snapshot.session }
    func retryTurn(turnID: String?, messageID: String?) async throws -> Session { snapshot.session }
    func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse {
        TaskCommandResponse(command: nil, run: nil, accepted: true, retryable: false, error: nil)
    }
    func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> Attachment {
        Attachment(id: id, name: filename, mediaType: mediaType, size: data.count)
    }

    func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] { snapshot.workItems }
    func fetchWorkDetail(taskID: String) async throws -> WorkDetail {
        details[taskID] ?? WorkDetail(item: snapshot.workItems[0])
    }
    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> {
        MutationReceipt(requestID: requestID, resource: snapshot.workItems[0])
    }
    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> {
        MutationReceipt(requestID: requestID, resource: snapshot.workItems[0])
    }
    func fetchInteractions() async throws -> [Interaction] { snapshot.interactions }
    func respondToInteraction(
        id: String,
        expectedRevision: Int,
        answer: InteractionAnswer,
        requestID: String
    ) async throws -> MutationReceipt<Interaction> {
        var resolved = snapshot.interactions.first(where: { $0.id == id }) ?? snapshot.interactions[0]
        resolved.status = .resolved
        return MutationReceipt(requestID: requestID, resource: resolved)
    }

    func fetchMCPServers() async throws -> [MCPServer] { snapshot.mcpServers }
    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] { snapshot.mcpTools[serverID] ?? [] }
    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "Preview mode")
    }
    func performMCPAction(serverID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "Preview mode")
    }
    func setMCPCredential(serverID: String, expectedRevision: Int, secret: String, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "Preview mode")
    }
    func setMCPToolExposure(serverID: String, expectedRevision: Int, toolName: String, exposure: MCPToolExposure, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "Preview mode")
    }
    func fetchConnections() async throws -> [Connection] { snapshot.connections }
    func performConnectionAction(connectionID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<Connection> {
        throw NoriAPIError(message: "Preview mode")
    }

    func fetchLibrary() async throws -> [LibraryItem] { snapshot.libraryItems }
    func fetchLibraryItem(id: String) async throws -> LibraryItem {
        snapshot.libraryItems.first(where: { $0.id == id }) ?? LibraryItem(id: id, title: "Document")
    }
    func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload {
        LibraryFilePayload(data: Data("# Preview\n\nRendered in dark mode.".utf8), mediaType: "text/markdown", filename: "preview.md")
    }
    func search(query: String) async throws -> SearchResponse { SearchResponse(items: []) }
    func reviseLibraryItem(id: String, expectedRevision: Int, instruction: String, requestID: String) async throws -> MutationReceipt<LibraryItem> {
        MutationReceipt(requestID: requestID, resource: snapshot.libraryItems[0])
    }

    func fetchCoreMemories() async throws -> [MemoryFact] { snapshot.coreMemories }
    func fetchMemoryLogs() async throws -> [MemoryLog] { snapshot.memoryLogs }
    func fetchWorkingMemory() async throws -> WorkingMemory { snapshot.workingMemory }
    func createMemory(text: String, section: String?, about: String?, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryFact> {
        MutationReceipt(requestID: requestID, resource: MemoryFact(id: UUID().uuidString, text: text, about: about, section: section, pinned: pinned))
    }
    func performMemoryAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<MemoryActionResource> {
        MutationReceipt(requestID: requestID, resource: .fact(snapshot.coreMemories[0]))
    }
    func performMemoryLogEntryAction(date: String, entryID: String, expectedRevision: Int, action: String, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryLogEntryActionResource> {
        MutationReceipt(requestID: requestID, resource: .promoted(snapshot.coreMemories[0]))
    }
    func performWorkingMemoryAction(itemID: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<WorkingMemoryActionResource> {
        throw NoriAPIError(message: "Preview mode")
    }

    func fetchAutomations() async throws -> [Automation] { snapshot.automations }
    func fetchAutomation(id: String) async throws -> Automation {
        snapshot.automations.first(where: { $0.id == id }) ?? snapshot.automations[0]
    }
    func updateAutomation(
        id: String,
        expectedRevision: Int,
        value: AutomationEditable,
        requestID: String
    ) async throws -> MutationReceipt<Automation> {
        var updated = snapshot.automations.first(where: { $0.id == id }) ?? snapshot.automations[0]
        updated.title = value.title
        updated.instruction = value.instruction
        updated.trigger = value.trigger
        return MutationReceipt(requestID: requestID, resource: updated)
    }
    func performAutomationAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<Automation> {
        MutationReceipt(requestID: requestID, resource: snapshot.automations[0])
    }
    func fetchPermissions() async throws -> [PermissionRule] { snapshot.permissions }
    func createPermissionRule(_ rule: PermissionRuleEditable, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        MutationReceipt(requestID: requestID, resource: snapshot.permissions[0])
    }
    func revokePermissionRule(id: String, expectedRevision: Int, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        MutationReceipt(requestID: requestID, resource: snapshot.permissions[0])
    }
    func fetchPreferences() async throws -> Preferences { snapshot.preferences }
    func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        MutationReceipt(requestID: requestID, resource: preferences)
    }

    func fetchModelKeys() async throws -> ModelKeysResponse {
        snapshot.modelKeys
    }

    func saveModelKey(provider: String, apiKey: String) async throws -> ModelKeyProvider {
        let now = ISO8601DateFormatter().string(from: Date())
        let label = snapshot.modelKeys.providers.first(where: { $0.provider == provider })?.label ?? provider
        let suffix = String(apiKey.suffix(4))
        return ModelKeyProvider(
            provider: provider,
            label: label,
            configured: true,
            last4: suffix,
            updatedAt: now,
            validatedAt: now
        )
    }

    func deleteModelKey(provider: String) async throws -> ModelKeyProvider {
        let label = snapshot.modelKeys.providers.first(where: { $0.provider == provider })?.label ?? provider
        return ModelKeyProvider(
            provider: provider,
            label: label,
            configured: false,
            last4: nil,
            updatedAt: nil,
            validatedAt: nil
        )
    }

    private var previewModelSettings = ModelSettingsResponse(
        modelProfiles: ModelProfileCatalog(profiles: [
            ModelProfile(
                id: "vercel_ai_gateway-neon",
                version: "v1",
                label: "Gemini 4 Neon via Vercel AI Gateway",
                backend: "vercel_ai_gateway",
                model: "google/gemini-4-neon",
                lanes: ["chat", "task"],
                supportedReasoning: ["low", "medium", "high", "xhigh"],
                defaultReasoning: "low",
                paid: true
            ),
            ModelProfile(
                id: "vercel_ai_gateway-hydrogen",
                version: "v1",
                label: "Gemini 4 Hydrogen via Vercel AI Gateway",
                backend: "vercel_ai_gateway",
                model: "google/gemini-4-hydrogen",
                lanes: ["chat", "task"],
                supportedReasoning: ["low", "medium", "high", "xhigh"],
                defaultReasoning: "low",
                paid: true
            ),
            ModelProfile(
                id: "vercel_ai_gateway-opus",
                version: "v1",
                label: "Claude Opus 5.5 via Vercel AI Gateway",
                backend: "vercel_ai_gateway",
                model: "anthropic/claude-opus-5.5",
                lanes: ["chat", "task"],
                supportedReasoning: ["low", "medium", "high", "xhigh", "max"],
                defaultReasoning: "low",
                paid: true
            ),
            ModelProfile(
                id: "vercel_ai_gateway-haiku",
                version: "v1",
                label: "Claude Haiku 5.5 via Vercel AI Gateway",
                backend: "vercel_ai_gateway",
                model: "anthropic/claude-haiku-5.5",
                lanes: ["chat", "task"],
                supportedReasoning: ["low", "medium", "high", "xhigh", "max"],
                defaultReasoning: "low",
                paid: true
            ),
        ]),
        selection: ModelSelection(
            chatProfileID: "vercel_ai_gateway-neon",
            chatReasoning: "low",
            taskProfileID: "vercel_ai_gateway-hydrogen",
            taskReasoning: "low"
        ),
        revision: 1,
        recommendations: [
            "vercel_ai_gateway": ModelSelection(
                chatProfileID: "vercel_ai_gateway-opus",
                chatReasoning: "low",
                taskProfileID: "vercel_ai_gateway-haiku",
                taskReasoning: "low"
            )
        ]
    )

    func fetchModelSettings() async throws -> ModelSettingsResponse {
        previewModelSettings
    }

    func saveModelSettings(_ request: SaveModelSettingsRequest) async throws -> ModelSettingsResponse {
        previewModelSettings.selection = request.selection
        previewModelSettings.revision += 1
        return previewModelSettings
    }

    func resetUserData(confirmation: String) async throws {
        guard confirmation == "RESET" else {
            throw NoriAPIError(message: "Type RESET to confirm.")
        }
        try await Task.sleep(for: .milliseconds(900))
    }
}

public extension NoriStore {
    static func makePreviewStore() -> NoriStore {
        let iso = ISO8601DateFormatter()
        let now = Date()
        let tMinus30m = iso.string(from: now.addingTimeInterval(-1800))
        let tMinus28m = iso.string(from: now.addingTimeInterval(-1680))
        let tMinus24m = iso.string(from: now.addingTimeInterval(-1440))
        let tMinus22m = iso.string(from: now.addingTimeInterval(-1320))
        let tMinus14m = iso.string(from: now.addingTimeInterval(-840))
        let tMinus10m = iso.string(from: now.addingTimeInterval(-600))
        let tMinus8m = iso.string(from: now.addingTimeInterval(-480))
        let tMinus5m = iso.string(from: now.addingTimeInterval(-300))
        let tMinus3m = iso.string(from: now.addingTimeInterval(-180))
        let tMinus2m = iso.string(from: now.addingTimeInterval(-120))
        let tMinus1m = iso.string(from: now.addingTimeInterval(-60))
        let tPlus29m = iso.string(from: now.addingTimeInterval(29 * 60))
        let tTomorrowNoon = iso.string(from: now.addingTimeInterval(24 * 3600))

        let messages: [Message] = [
            Message(
                id: "m-1",
                role: .user,
                text: "Can you set up a daily check for supernino updates at noon?",
                createdAt: tMinus30m,
                receipt: .saved,
                separatorLabel: "Today · 9:15 AM"
            ),
            Message(
                id: "m-2",
                role: .assistant,
                text: "Sure — I drafted a daily noon automation. Approve it whenever you're ready.",
                createdAt: tMinus28m
            ),
            Message(
                id: "m-3",
                role: .user,
                text: "What do I have going on tomorrow?",
                createdAt: tMinus14m,
                receipt: .saved,
                separatorLabel: "Today · 9:32 AM"
            ),
            Message(
                id: "m-4",
                role: .assistant,
                text: "You have two meetings in the morning and one reminder scheduled for noon:\n\n• **9:30 AM** — Design sync with Maya\n• **11:00 AM** — Q4 vendor renewal review\n• **12:00 PM** — Check supernino updates\n\n[Calendar reviewed](https://calendar.google.com)",
                createdAt: tMinus10m
            ),
            Message(
                id: "m-5",
                role: .user,
                text: "Any prep I should read before the 11:00 vendor review?",
                createdAt: tMinus5m,
                receipt: .saved
            ),
            Message(
                id: "m-6",
                role: .assistant,
                text: "Finance flagged a 12% seat-tier jump on the draft renewal. I'm pulling the latest usage numbers from last quarter now so you have the exact active-seat count before the call.",
                createdAt: tMinus3m
            ),
            Message(
                id: "m-7",
                role: .user,
                text: "Nice, keep the summary short — bullet points only.",
                createdAt: tMinus2m,
                receipt: .saved
            ),
            Message(
                id: "m-8",
                role: .assistant,
                text: "Got it. I'll drop a 3-bullet brief right here as soon as the usage pull finishes.",
                createdAt: tMinus1m
            )
        ]

        let approval = Interaction(
            id: "int-approval-1",
            kind: .approval,
            taskID: "task-1",
            status: .pending,
            prompt: "Make this change?",
            payload: .approval(
                ApprovalPayload(
                    summary: "Check supernino updates — Tomorrow at 12:00 PM",
                    toolID: "automation_create",
                    warning: "This action creates a scheduled automation using your connected accounts.",
                    effect: .reversibleWrite,
                    account: ApprovalAccount(connectionID: "conn-1", label: "austin@nori.app")
                )
            ),
            actionDigest: "digest-1",
            expiresAt: tPlus29m,
            createdAt: tMinus24m,
            taskTitle: "Schedule supernino updates"
        )

        let clarification = Interaction(
            id: "int-question-1",
            kind: .question,
            taskID: "task-2",
            status: .pending,
            prompt: "If the reminder can’t run at noon, should I deliver it late or skip it?",
            payload: .question(QuestionPayload(input: .text)),
            createdAt: tMinus22m,
            taskTitle: "Noon reminder fallback"
        )

        let workItems: [WorkItem] = [
            WorkItem(
                taskID: "task-1",
                title: "Check supernino updates",
                status: .running,
                bucket: .needsYou,
                interactionIDs: ["int-approval-1"],
                progress: "Waiting for approval to schedule daily check",
                operations: WorkOperationsSummary(total: 5, uncertain: 0, failed: 0, submitted: 1),
                cost: WorkCost(amountUSD: 0.14, status: .known, budgetUSD: 1.00),
                startedAt: tMinus10m
            ),
            WorkItem(
                taskID: "task-2",
                title: "Plan weekend trip to Kyoto",
                status: .running,
                bucket: .working,
                progress: "Comparing ryokan options near Higashiyama…",
                operations: WorkOperationsSummary(total: 4, uncertain: 0, failed: 0, submitted: 1),
                cost: WorkCost(amountUSD: 0.22, status: .known, budgetUSD: 1.00),
                startedAt: tMinus8m,
                steps: [
                    WorkStep(text: "Compare direct flights under $900", status: .done),
                    WorkStep(text: "Shortlist ryokans near Higashiyama", status: .done),
                    WorkStep(text: "Draft 3-day itinerary with dinner spots", status: .active),
                    WorkStep(text: "Confirm bookings and add to calendar", status: .upcoming)
                ]
            ),
            WorkItem(
                taskID: "task-queued-1",
                title: "Prep 1-page brief for Monday Q4 review",
                status: .running,
                bucket: .working,
                cost: WorkCost(amountUSD: 0, status: .known, budgetUSD: 1.00),
                startedAt: tMinus2m,
                queued: true
            ),
            WorkItem(
                taskID: "task-3",
                title: "Review tomorrow's calendar",
                status: .completed,
                bucket: .finished,
                progress: "Found 2 morning meetings and 1 noon reminder.",
                cost: WorkCost(amountUSD: 0.08, status: .known, budgetUSD: 1.00),
                startedAt: tMinus10m,
                finishedAt: tMinus8m
            ),
            WorkItem(
                taskID: "task-4",
                title: "Sync billing invoice export",
                status: .failed,
                bucket: .finished,
                cost: WorkCost(amountUSD: 0.19, status: .known, budgetUSD: 1.00),
                startedAt: tMinus10m,
                finishedAt: tMinus5m,
                error: ErrorDetail(code: "upstream", message: "Provider returned 503 Service Unavailable")
            ),
            WorkItem(
                taskID: "task-5",
                title: "Archive old vendor threads",
                status: .cancelled,
                bucket: .finished,
                cost: WorkCost(amountUSD: 0.03, status: .known, budgetUSD: 1.00),
                startedAt: tMinus10m,
                finishedAt: tMinus5m
            )
        ]

        let operations: [OperationProjection] = [
            OperationProjection(id: "op-1", name: "web_search", outcome: .confirmed, authoritySource: "internal", createdAt: tMinus10m),
            OperationProjection(id: "op-2", name: "web_search", outcome: .confirmed, authoritySource: "internal", createdAt: tMinus8m),
            OperationProjection(id: "op-3", name: "read_page", outcome: .confirmed, authoritySource: "internal", createdAt: tMinus5m),
            OperationProjection(id: "op-4", name: "automation_create", outcome: .pending, authoritySource: "internal", createdAt: tMinus2m),
            OperationProjection(id: "op-5", name: "generate_result", outcome: .attempted, authoritySource: "internal", createdAt: tMinus2m)
        ]

        let detail = WorkDetail(
            item: workItems[0],
            operations: operations,
            interactions: [approval],
            events: [
                RunEvent(text: "Loaded calendar and web research tools", createdAt: tMinus10m),
                RunEvent(text: "Prepared automation schedule for tomorrow at 12:00 PM", createdAt: tMinus2m)
            ]
        )

        let automations: [Automation] = [
            Automation(
                id: "auto-1",
                title: "Check supernino updates",
                status: .enabled,
                trigger: AutomationTrigger(kind: "recurring", rrule: "FREQ=DAILY;BYHOUR=12;BYMINUTE=0"),
                nextAt: tTomorrowNoon
            ),
            Automation(
                id: "auto-2",
                title: "Morning calendar briefing",
                status: .enabled,
                trigger: AutomationTrigger(kind: "recurring", rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR;BYHOUR=8;BYMINUTE=0"),
                nextAt: tTomorrowNoon
            ),
            Automation(
                id: "auto-3",
                title: "Friday vendor spend rollup",
                status: .paused,
                trigger: AutomationTrigger(kind: "recurring", rrule: "FREQ=WEEKLY;BYDAY=FR;BYHOUR=16;BYMINUTE=0")
            ),
            Automation(
                id: "auto-once-1",
                title: "Follow up with Sam if SOW isn’t signed",
                status: .enabled,
                trigger: AutomationTrigger(kind: "once", at: tTomorrowNoon),
                nextAt: tTomorrowNoon
            )
        ]

        let coreMemories: [MemoryFact] = [
            MemoryFact(
                id: "mem-1",
                text: "Prefers aisle seats and direct morning flights when booking travel.",
                about: "travel",
                section: "Preferences",
                pinned: true,
                provenance: .userExplicit,
                createdAt: tMinus10m
            ),
            MemoryFact(
                id: "mem-2",
                text: "Keep weekly stakeholder briefs under 200 words with key metrics at the top.",
                about: "writing",
                section: "Decisions",
                pinned: false,
                provenance: .userExplicit,
                createdAt: tMinus8m
            )
        ]

        let snapshot = NoriFullSnapshot(
            session: Session(messages: messages),
            workItems: workItems,
            interactions: [approval, clarification],
            coreMemories: coreMemories,
            workingMemory: WorkingMemory(
                items: [],
                gist: "Reviewing tomorrow's schedule and configuring supernino daily update checks.",
                coreBytes: 6400,
                coreBudgetBytes: 24 * 1024
            ),
            automations: automations,
            permissions: [
                PermissionRule(
                    id: "perm-1",
                    effect: .ask,
                    toolIDs: ["gmail.send"],
                    connectionIDs: ["*"],
                    actionClasses: [.sensitiveSend],
                    summary: "Ask before sending external email"
                ),
                PermissionRule(
                    id: "perm-2",
                    effect: .allow,
                    toolIDs: ["calendar.list"],
                    connectionIDs: ["*"],
                    actionClasses: [.read],
                    summary: "Read calendar events automatically"
                )
            ],
            modelKeys: ModelKeysResponse(
                byokRequired: false,
                providers: [
                    ModelKeyProvider(
                        provider: "vercel_ai_gateway",
                        label: "Vercel AI Gateway",
                        configured: true,
                        last4: "a1b2",
                        updatedAt: tMinus30m,
                        validatedAt: tMinus30m
                    )
                ]
            )
        )

        let transport = PreviewNoriTransport(snapshot: snapshot, details: ["task-1": detail])
        let store = NoriStore(transport: transport)
        store.hasBootstrapped = true
        store.authState = .signedIn(email: "austin@nori.app")
        store.connectionState = .connected
        store.session = snapshot.session
        store.workItems = snapshot.workItems
        store.workDetails = ["task-1": detail]
        store.interactions = snapshot.interactions
        store.coreMemories = snapshot.coreMemories
        store.workingMemory = snapshot.workingMemory
        store.automations = snapshot.automations
        store.permissions = snapshot.permissions
        store.modelKeys = snapshot.modelKeys
        store.assistantSummary = AssistantSummary(needsYou: 2, working: 1, appsAttention: 0)
        return store
    }
}
#endif
