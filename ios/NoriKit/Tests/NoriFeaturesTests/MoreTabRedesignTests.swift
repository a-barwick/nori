import Foundation
import Testing
import NoriCore
import NoriFeatures

@Suite("More Tab Redesign Parity")
struct MoreTabRedesignTests {
    @Test("MemoryTopicGrouping normalizes sections and orders canonical folders")
    func memoryTopicGrouping() {
        #expect(MemoryTopicGrouping.normalize(nil) == "About you")
        #expect(MemoryTopicGrouping.normalize("profile") == "About you")
        #expect(MemoryTopicGrouping.normalize("Preferences") == "Preferences")
        #expect(MemoryTopicGrouping.normalize("work") == "Projects")
        #expect(MemoryTopicGrouping.normalize("family") == "People")
        #expect(MemoryTopicGrouping.normalize("routine") == "Home & Routine")
        #expect(MemoryTopicGrouping.normalize("dining") == "Travel & Food")
        #expect(MemoryTopicGrouping.normalize("guitar") == "Guitar")

        let facts = [
            MemoryFact(id: "1", revision: 1, text: "Plays guitar", section: "guitar"),
            MemoryFact(id: "2", revision: 1, text: "Likes ramen", section: "dining"),
            MemoryFact(id: "3", revision: 1, text: "Aisle seats", section: "Preferences"),
            MemoryFact(id: "4", revision: 1, text: "Lives in Seattle", section: nil),
            MemoryFact(id: "5", revision: 1, text: "Builds Nori", section: "profile"),
        ]

        let groups = MemoryTopicGrouping.group(facts)
        #expect(groups.map(\.topic) == ["About you", "Preferences", "Travel & Food", "Guitar"])
        #expect(groups.map { $0.items.count } == [2, 1, 1, 1])
    }

    @Test("PersonalizationPreferences encodes and decodes structured fields")
    @MainActor
    func personalizationCodableRoundTrip() throws {
        let prefs = Preferences(
            personalization: PersonalizationPreferences(
                name: "Austin",
                replyStyle: "Bullets first",
                customInstructions: "Lead with the answer.",
                focus: ["Engineering", "Research"],
                workingOn: "Nori"
            )
        )
        let data = try JSONEncoder().encode(prefs)
        let decoded = try JSONDecoder().decode(Preferences.self, from: data)
        #expect(decoded.personalization?.name == "Austin")
        #expect(decoded.personalization?.replyStyle == "Bullets first")
        #expect(decoded.personalization?.customInstructions == "Lead with the answer.")
        #expect(decoded.personalization?.focus == ["Engineering", "Research"])
        #expect(decoded.personalization?.workingOn == "Nori")
        #expect(PersonalizationSettingsView.responseStyleOptions.count == 3)
    }

    @Test("NoriStore detects missing Exa key and Google Drive connection state")
    @MainActor
    func storeKeyAlertsAndDriveNudge() {
        let store = NoriStore()
        #expect(store.needsExaKeySetup == false)

        store.serviceKeys = ServiceKeysResponse(
            allowBYOK: true,
            services: [ServiceKeyStatus(service: "exa", label: "Exa Web Search", configured: false, source: "none")]
        )
        #expect(store.needsExaKeySetup == true)
        #expect(store.needsAnyKeySetup == true)

        store.serviceKeys = ServiceKeysResponse(
            allowBYOK: true,
            services: [ServiceKeyStatus(service: "exa", label: "Exa Web Search", configured: true, source: "byok")]
        )
        #expect(store.needsExaKeySetup == false)

        store.serviceKeys = ServiceKeysResponse(
            allowBYOK: true,
            services: [ServiceKeyStatus(service: "exa", label: "Exa Web Search", configured: false, source: "platform")]
        )
        #expect(store.needsExaKeySetup == false)

        #expect(store.isGoogleDriveConnected == false)
    }
}
