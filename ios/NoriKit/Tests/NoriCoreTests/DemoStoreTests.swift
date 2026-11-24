import Foundation
import Testing
@testable import NoriCore

@Suite("DemoNoriTransport & NoriStore Flows")
@MainActor
struct DemoStoreTests {
    private func makeDemoStore() async -> NoriStore {
        let suite = "nori.tests.\(UUID().uuidString)"
        let profileStore = ServerProfileStore(suiteName: suite)
        let transport = DemoNoriTransport(simulatedReplyDelayMs: 0)
        let store = NoriStore(profile: .demo, transport: transport, profileStore: profileStore)
        await store.refreshAll()
        return store
    }

    @Test("Seeds realistic demo snapshot and derived badges")
    func initialSeededState() async {
        let store = await makeDemoStore()
        #expect(store.messages.count == 5)
        #expect(store.needsYouCount == 2)
        #expect(store.appsAttentionCount == 1)
        #expect(store.activeTasksCount == 2)
        #expect(store.statusLine.text == "2 things need you")
        #expect(store.statusLine.tone == .needsYou)
        #expect(store.coreMemories.count == 4)
        #expect(store.coreMemoryBudgetBytes == 24 * 1024)
        #expect(store.libraryItems.count == 4)
        #expect(store.automations.count == 3)
        #expect(store.goals.count == 2)
        #expect(store.permissions.count == 3)
    }

    @Test("Sending a message appends user message and simulated Nori reply")
    func sendMessageFlow() async {
        let store = await makeDemoStore()
        let beforeCount = store.messages.count
        await store.sendMessage("Can we trim the Datadog seats?")
        #expect(store.messages.count == beforeCount + 2)
        #expect(store.messages.last?.role == .assistant)
        #expect(store.messages.last?.text.contains("Datadog") == true)
        #expect(store.isThinking == false)
    }

    @Test("Approving procurement card resolves interaction, updates task, and clears goal blocker")
    func approvalAndQuestionFlow() async {
        let store = await makeDemoStore()
        guard let approval = store.pendingInteractions.first(where: { $0.kind == .approval }) else {
            Issue.record("Missing seeded approval interaction")
            return
        }
        await store.approveInteraction(approval)
        #expect(store.needsYouCount == 1)
        #expect(store.workItems.first(where: { $0.taskID == "task-1" })?.status == .completed)
        #expect(store.goals.first(where: { $0.id == "goal-1" })?.isBlocked == false)

        if let question = store.pendingInteractions.first(where: { $0.kind == .question }) {
            await store.answerInteraction(question, text: "Work · alex@example.com", optionIDs: ["cal-work"])
        }
        #expect(store.needsYouCount == 0)
        #expect(store.statusLine.text == "Ready when you are")
    }

    @Test("Work task steer, stop, and continue with higher budget flows")
    func workControlFlows() async {
        let store = await makeDemoStore()
        await store.steerTask(taskID: "task-2", instruction: "Prefer 2:00 PM PT slot")
        let detail = await store.loadWorkDetail(taskID: "task-2")
        #expect(detail?.item.progress?.contains("Prefer 2:00 PM PT") == true)

        await store.stopTask(taskID: "task-2")
        #expect(store.workItems.first(where: { $0.taskID == "task-2" })?.status == .cancelled)

        let beforeWorkCount = store.workItems.count
        await store.continueTask(taskID: "task-4", budgetUSD: 2.50)
        #expect(store.workItems.count == beforeWorkCount + 1)
        #expect(store.workItems.first?.cost.budgetUSD == 2.50)
    }

    @Test("MCP server creation, OAuth completion, and tool exposure update")
    func appsAndMCPFlows() async {
        let store = await makeDemoStore()
        #expect(store.appsAttentionCount == 1)

        _ = await store.completeMCPOAuth(serverID: "mcp-linear")
        #expect(store.appsAttentionCount == 0)
        #expect(store.mcpServers.first(where: { $0.id == "mcp-linear" })?.observed.status == .connected)

        await store.setToolExposure(serverID: "mcp-gateway", toolName: "mcp_gateway__billing_query", exposure: .direct)
        let tool = store.mcpTools["mcp-gateway"]?.first(where: { $0.name == "mcp_gateway__billing_query" })
        #expect(tool?.exposure == .direct)

        await store.addMCPServer(name: "Notion Docs", endpoint: "https://mcp.notion.so/v1", auth: .none)
        #expect(store.mcpServers.contains(where: { $0.name == "Notion Docs" }))
    }

    @Test("3-layer Memory add, pin toggle, log promotion, Library search & revision, Automations, and Demo trigger")
    func memoryLibraryAndAutomationFlows() async {
        let store = await makeDemoStore()

        await store.addMemory(text: "Prefer Swift 6 strict concurrency across all iOS targets.", section: "Engineering", pinned: true)
        #expect(store.coreMemories.count == 5)
        let added = store.coreMemories[0]
        #expect(added.pinned == true)

        await store.togglePinMemory(memoryID: added.id)
        #expect(store.coreMemories.first(where: { $0.id == added.id })?.pinned == false)

        await store.promoteLogEntry(date: "2026-11-23", entryID: "log-1", pinned: false)
        #expect(store.coreMemories.count == 6)

        await store.searchLibrary(query: "Datadog")
        #expect(!store.searchHits.isEmpty)

        await store.reviseDocument(itemID: "lib-1", instruction: "Add executive summary at top")
        #expect(store.libraryItems.first(where: { $0.id == "lib-1" })?.version == 3)

        await store.toggleAutomation(automationID: "auto-1")
        #expect(store.automations.first(where: { $0.id == "auto-1" })?.status == .paused)

        await store.runAutomationNow(automationID: "auto-2")
        #expect(store.workItems.contains(where: { $0.origin.automationID == "auto-2" && $0.status == .running }))

        let beforeNeeds = store.needsYouCount
        await store.triggerDemoApproval()
        #expect(store.needsYouCount == beforeNeeds + 1)

        let ping = await store.pingServer()
        #expect(ping.ok == true)
    }
}
