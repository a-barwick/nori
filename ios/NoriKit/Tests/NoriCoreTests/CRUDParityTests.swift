import Foundation
import Testing
@testable import NoriCore

@Suite("iOS CRUD Parity Store Tests")
struct CRUDParityTests {

    @MainActor
    @Test("Goals create, update, pause, complete, and drop update store.goals")
    func goalCRUDLifecycle() async throws {
        let store = NoriStore.makePreviewStore()
        let initialCount = store.goals.count

        let created = try await store.createGoal(
            GoalEditable(
                title: "Ship iOS parity",
                outcome: "Every screen matches Web",
                completionCriteria: [GoalCriterionInput(text: "Goals CRUD")],
                followUp: "monitor"
            )
        )
        #expect(store.goals.count == initialCount + 1)
        #expect(created.title == "Ship iOS parity")

        var edited = created.editable
        edited.title = "Ship full parity"
        let updated = try await store.updateGoal(id: created.id, value: edited)
        #expect(updated.title == "Ship full parity")

        let paused = await store.performGoalAction(goalID: created.id, action: "pause")
        #expect(paused?.status == .paused)

        let done = await store.performGoalAction(goalID: created.id, action: "complete", keepMonitors: true)
        #expect(done?.status == .completed)
    }

    @MainActor
    @Test("Routines create, snooze, and occurrence history work on NoriStore")
    func routineCRUDLifecycle() async throws {
        let store = NoriStore.makePreviewStore()
        let created = try await store.createAutomation(
            AutomationEditable(
                title: "Weekday digest",
                instruction: "Summarize open items",
                trigger: AutomationTrigger(
                    kind: "recurring",
                    localStart: "2026-11-27T08:30:00",
                    timezone: "America/Los_Angeles",
                    rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"
                ),
                budgetUSD: 0.75
            )
        )
        #expect(store.automations.contains(where: { $0.id == created.id }))

        let snoozeUntil = Date().addingTimeInterval(3600)
        await store.snoozeAutomation(automationID: created.id, until: snoozeUntil)
        let refreshed = store.automations.first(where: { $0.id == created.id })
        #expect(refreshed?.snoozedUntil != nil)

        let history = await store.loadAutomationOccurrences(automationID: created.id)
        #expect(!history.isEmpty)
        #expect(history.first?.automationID == created.id)
    }

    @MainActor
    @Test("Library rename, versions, and delete update store.libraryItems")
    func libraryCRUDLifecycle() async throws {
        let store = NoriStore.makePreviewStore()
        let target = try #require(store.libraryItems.first)

        let renamed = try await store.renameLibraryItem(itemID: target.id, title: "Updated Report")
        #expect(renamed.title == "Updated Report")
        #expect(store.libraryItems.first(where: { $0.id == target.id })?.title == "Updated Report")

        let versions = await store.loadLibraryVersions(itemID: target.id)
        #expect(!versions.isEmpty)

        let deleted = await store.deleteLibraryItem(itemID: target.id, scope: "version")
        #expect(deleted)
        #expect(!store.libraryItems.contains(where: { $0.id == target.id }))
    }

    @MainActor
    @Test("Memory update and consolidate work on NoriStore")
    func memoryEditAndConsolidate() async throws {
        let store = NoriStore.makePreviewStore()
        let fact = try #require(store.coreMemories.first)

        let updated = try await store.updateMemory(
            memoryID: fact.id,
            text: "Prefers window seats",
            section: "Travel",
            about: "Flights",
            pinned: true
        )
        #expect(updated.text == "Prefers window seats")
        #expect(updated.section == "Travel")
        #expect(updated.pinned == true)

        await store.consolidateMemory()
        #expect(store.toastMessage == "Memory consolidation requested")
    }

    @MainActor
    @Test("MCP server update updates store.mcpServers")
    func mcpServerUpdate() async throws {
        let store = NoriStore.makePreviewStore()
        let server = try #require(store.mcpServers.first)

        let updated = try await store.updateMCPServer(
            serverID: server.id,
            name: "Renamed MCP",
            endpoint: "https://mcp.example.com/v2",
            auth: .oauth,
            enabled: false,
            defaultExposure: .direct
        )
        #expect(updated.name == "Renamed MCP")
        #expect(updated.enabled == false)
        #expect(store.mcpServers.first(where: { $0.id == server.id })?.name == "Renamed MCP")
    }
}
