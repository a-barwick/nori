import NoriCore
import NoriDesign
import NoriFeatures
import SwiftUI
#if os(iOS)
import UIKit
import UserNotifications
#endif

#if os(iOS)
@MainActor
final class NoriAppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    weak var store: NoriStore?
    private var pendingDeviceToken: Data?
    private var pendingPushUserInfo: [AnyHashable: Any]?

    func attach(store: NoriStore) {
        self.store = store
        if let token = pendingDeviceToken {
            pendingDeviceToken = nil
            store.handleAPNsDeviceToken(token)
        }
        if let userInfo = pendingPushUserInfo {
            pendingPushUserInfo = nil
            store.handlePushNotificationTap(userInfo: userInfo)
        }
    }

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        if let store {
            store.handleAPNsDeviceToken(deviceToken)
        } else {
            pendingDeviceToken = deviceToken
        }
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        NoriLogger.network.warning("push.apns_registration_failed", attributes: ["error": error.localizedDescription])
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let userInfo = response.notification.request.content.userInfo
        await MainActor.run {
            if let store = self.store {
                store.handlePushNotificationTap(userInfo: userInfo)
            } else {
                self.pendingPushUserInfo = userInfo
            }
        }
    }
}
#endif

@main
struct NoriApp: App {
    #if os(iOS)
    @UIApplicationDelegateAdaptor(NoriAppDelegate.self) private var appDelegate
    #endif
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
                .onAppear {
                    #if os(iOS)
                    appDelegate.attach(store: store)
                    #endif
                }
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
