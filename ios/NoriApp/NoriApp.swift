import NoriCore
import NoriDesign
import NoriFeatures
import SwiftUI

@main
struct NoriApp: App {
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue
    @State private var store: NoriStore

    init() {
        #if DEBUG
        let initialStore: NoriStore = CommandLine.arguments.contains("-nori-preview")
            ? NoriStore.makePreviewStore()
            : NoriStore()
        #else
        let initialStore = NoriStore()
        #endif
        if let initialURL = Self.resolveInitialURL() {
            initialStore.handleIncomingURL(initialURL)
        }
        _store = State(initialValue: initialStore)
    }

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    var body: some Scene {
        WindowGroup {
            NoriRootView(store: store)
                .tint(NoriColors.accent)
                .preferredColorScheme(appearance.colorScheme)
        }
    }

    private static func resolveInitialURL() -> URL? {
        let args = CommandLine.arguments
        for flag in ["-nori-url", "--nori-url"] {
            if let idx = args.firstIndex(of: flag), args.indices.contains(idx + 1) {
                return URL(string: args[idx + 1])
            }
        }
        if let envURL = ProcessInfo.processInfo.environment["NORI_INITIAL_URL"], !envURL.isEmpty {
            return URL(string: envURL)
        }
        return nil
    }
}
