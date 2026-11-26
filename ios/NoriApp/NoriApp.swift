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
        let isPreview = CommandLine.arguments.contains("-nori-preview")
        SentryNoriTelemetry.bootstrap(isPreviewMode: isPreview)
        let initialStore: NoriStore
        if isPreview {
            initialStore = NoriStore.makePreviewStore()
        } else if let localServer = Self.resolveArg("-nori-local-server", env: "NORI_LOCAL_SERVER"),
                  let localURL = URL(string: localServer),
                  let localEmail = Self.resolveArg("-nori-local-email", env: "NORI_LOCAL_EMAIL") {
            let transport = LiveNoriTransport(baseURL: localURL, localEmail: localEmail)
            initialStore = NoriStore(transport: transport, devLocalEmail: localEmail)
        } else {
            initialStore = NoriStore()
        }
        #else
        SentryNoriTelemetry.bootstrap(isPreviewMode: false)
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

    private static func resolveArg(_ flag: String, env: String) -> String? {
        let args = CommandLine.arguments
        if let idx = args.firstIndex(of: flag), args.indices.contains(idx + 1) {
            let val = args[idx + 1].trimmingCharacters(in: .whitespacesAndNewlines)
            if !val.isEmpty { return val }
        }
        if let envVal = ProcessInfo.processInfo.environment[env]?.trimmingCharacters(in: .whitespacesAndNewlines),
           !envVal.isEmpty {
            return envVal
        }
        return nil
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
