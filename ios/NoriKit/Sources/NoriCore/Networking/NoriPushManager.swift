import Foundation
#if canImport(UserNotifications)
import UserNotifications
#endif
#if canImport(UIKit)
import UIKit
#endif

public enum PushEnvironment: String, Sendable, Codable, Hashable {
    case sandbox
    case production

    public static var current: PushEnvironment {
        #if DEBUG
        return .sandbox
        #else
        return .production
        #endif
    }
}

public protocol PushDevicePersistence: Sendable {
    func loadRegisteredDeviceID() -> String?
    func saveRegisteredDeviceID(_ id: String?)
    func loadDeviceIdentifier() -> String
}

public final class UserDefaultsPushPersistence: PushDevicePersistence, @unchecked Sendable {
    private let defaults: UserDefaults
    private let registeredIDKey: String
    private let deviceIDKey: String

    public init(
        defaults: UserDefaults = .standard,
        registeredIDKey: String = "nori.push.registeredDeviceID",
        deviceIDKey: String = "nori.push.deviceIdentifier"
    ) {
        self.defaults = defaults
        self.registeredIDKey = registeredIDKey
        self.deviceIDKey = deviceIDKey
    }

    public func loadRegisteredDeviceID() -> String? {
        defaults.string(forKey: registeredIDKey)
    }

    public func saveRegisteredDeviceID(_ id: String?) {
        if let id, !id.isEmpty {
            defaults.set(id, forKey: registeredIDKey)
        } else {
            defaults.removeObject(forKey: registeredIDKey)
        }
    }

    public func loadDeviceIdentifier() -> String {
        if let existing = defaults.string(forKey: deviceIDKey), !existing.isEmpty {
            return existing
        }
        let created = UUID().uuidString.lowercased()
        defaults.set(created, forKey: deviceIDKey)
        return created
    }
}

@MainActor
public final class NoriPushManager {
    private let persistence: any PushDevicePersistence
    private let environment: PushEnvironment
    public private(set) var latestTokenHex: String?
    public private(set) var registeredDeviceID: String?
    private var lastRegisteredTokenHex: String?

    public init(
        persistence: any PushDevicePersistence = UserDefaultsPushPersistence(),
        environment: PushEnvironment = .current
    ) {
        self.persistence = persistence
        self.environment = environment
        self.registeredDeviceID = persistence.loadRegisteredDeviceID()
    }

    public static func hexString(from deviceToken: Data) -> String {
        deviceToken.map { String(format: "%02x", $0) }.joined()
    }

    public nonisolated static func extractInteractionID(from userInfo: [AnyHashable: Any]) -> String? {
        if let direct = userInfo["interaction_id"] as? String {
            let trimmed = direct.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmed.isEmpty { return trimmed }
        }
        for containerKey in ["nori", "data"] {
            if let nested = userInfo[containerKey] as? [AnyHashable: Any],
               let value = nested["interaction_id"] as? String {
                let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
                if !trimmed.isEmpty { return trimmed }
            }
        }
        return nil
    }

    public func requestAuthorizationIfNeeded() async {
        #if os(iOS) && canImport(UserNotifications)
        let center = UNUserNotificationCenter.current()
        let settings = await center.notificationSettings()
        switch settings.authorizationStatus {
        case .notDetermined:
            let granted = (try? await center.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
            if granted {
                UIApplication.shared.registerForRemoteNotifications()
            }
        case .authorized, .provisional, .ephemeral:
            UIApplication.shared.registerForRemoteNotifications()
        case .denied:
            break
        @unknown default:
            break
        }
        #endif
    }

    public func updateDeviceToken(_ tokenData: Data, transport: any NoriTransport, isSignedIn: Bool) async {
        let tokenHex = Self.hexString(from: tokenData)
        guard !tokenHex.isEmpty else { return }
        latestTokenHex = tokenHex
        guard isSignedIn else { return }
        await registerTokenIfNeeded(tokenHex: tokenHex, transport: transport)
    }

    public func registerPendingTokenIfSignedIn(transport: any NoriTransport) async {
        guard let tokenHex = latestTokenHex, !tokenHex.isEmpty else { return }
        await registerTokenIfNeeded(tokenHex: tokenHex, transport: transport)
    }

    private func registerTokenIfNeeded(tokenHex: String, transport: any NoriTransport) async {
        if lastRegisteredTokenHex == tokenHex, registeredDeviceID != nil {
            return
        }
        let req = RegisterPushDeviceRequest(
            token: tokenHex,
            deviceID: persistence.loadDeviceIdentifier(),
            environment: environment.rawValue
        )
        do {
            let device = try await transport.registerPushDevice(req)
            registeredDeviceID = device.id
            lastRegisteredTokenHex = tokenHex
            persistence.saveRegisteredDeviceID(device.id)
        } catch {
            NoriLogger.network.warning("push.register_failed", attributes: ["error": error.localizedDescription])
        }
    }

    public func unregisterOnSignOut(transport: any NoriTransport) async {
        let deviceID = registeredDeviceID ?? persistence.loadRegisteredDeviceID()
        registeredDeviceID = nil
        lastRegisteredTokenHex = nil
        persistence.saveRegisteredDeviceID(nil)
        guard let deviceID, !deviceID.isEmpty else { return }
        do {
            try await transport.deletePushDevice(id: deviceID)
        } catch {
            NoriLogger.network.warning("push.unregister_failed", attributes: ["error": error.localizedDescription])
        }
    }
}
