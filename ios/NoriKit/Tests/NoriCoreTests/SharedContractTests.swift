import Foundation
import Testing
@testable import NoriCore

private let contractDirectory = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().deletingLastPathComponent()
    .appendingPathComponent("contracts/assistant", isDirectory: true)

private func fixture<T: Decodable>(_ path: String, as type: T.Type) throws -> T {
    try JSONDecoder().decode(type, from: Data(contentsOf: contractDirectory.appendingPathComponent(path)))
}

@Test("Decode shared contract r2.13 fixtures")
func sharedWireFixtures() throws {
    struct Index: Decodable { let revision: String }
    #expect(try fixture("fixtures/index.json", as: Index.self).revision == "r2.13")
    #expect(!(try fixture("fixtures/automations/automation.recurring.valid.json", as: Automation.self).id).isEmpty)
    #expect(!(try fixture("fixtures/automations/goal.valid.json", as: Goal.self).id).isEmpty)
    #expect(!(try fixture("fixtures/library/memory-fact.valid.json", as: MemoryFact.self).id).isEmpty)
    _ = try fixture("fixtures/common/preferences.valid.json", as: Preferences.self)
}

private struct ModelSelectionFixture: Decodable {
    struct Case: Decodable {
        let name: String
        let catalogAvailable: Bool
        let selection: ModelSelection
        let expected: ModelSelection
    }

    let catalog: ModelProfileCatalog
    let cases: [Case]
}

@Test("Normalize model selections the same way as web")
func sharedModelSelections() throws {
    let bundle = try fixture("client/model-selections.json", as: ModelSelectionFixture.self)
    for item in bundle.cases {
        #expect(item.selection.normalized(using: item.catalogAvailable ? bundle.catalog : nil) == item.expected, Comment(rawValue: item.name))
    }
}
