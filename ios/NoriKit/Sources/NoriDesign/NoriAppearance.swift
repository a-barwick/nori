import SwiftUI

public enum NoriAppearance: String, CaseIterable, Identifiable, Sendable {
    case system
    case light
    case dark

    public static let storageKey = "nori.appearance"

    public var id: String { rawValue }

    public var displayLabel: String {
        switch self {
        case .system: return "System"
        case .light: return "Light"
        case .dark: return "Dark"
        }
    }

    public var systemImage: String {
        switch self {
        case .system: return "circle.lefthalf.filled"
        case .light: return "sun.max"
        case .dark: return "moon"
        }
    }

    public var colorScheme: ColorScheme? {
        switch self {
        case .system: return nil
        case .light: return .light
        case .dark: return .dark
        }
    }

    public init(storedRawValue: String) {
        self = NoriAppearance(rawValue: storedRawValue) ?? .system
    }
}

public enum NoriTabBarPosition: String, CaseIterable, Identifiable, Sendable {
    case top
    case bottom

    public static let storageKey = "nori.tabBarPosition"

    public var id: String { rawValue }

    public var displayLabel: String {
        switch self {
        case .top: return "Top"
        case .bottom: return "Bottom"
        }
    }

    public init(storedRawValue: String) {
        self = NoriTabBarPosition(rawValue: storedRawValue) ?? .top
    }
}
