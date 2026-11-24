import NoriCore
import NoriDesign
import NoriFeatures
import SwiftUI

@main
struct NoriApp: App {
    @State private var store: NoriStore

    init() {
        let initialStore = NoriStore()
        if let initialURL = Self.resolveInitialURL() {
            initialStore.handleIncomingURL(initialURL)
        }
        _store = State(initialValue: initialStore)
    }

    var body: some Scene {
        WindowGroup {
            // NoriRootView attaches `.onOpenURL` -> `store.handleIncomingURL(_:)`
            // for `nori://` OAuth callbacks and deep links registered in Info.plist.
            NoriRootView(store: store)
                .tint(NoriColors.accent)
                .preferredColorScheme(.light)
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
