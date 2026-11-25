import SwiftUI

/// User-selectable color appearance for Nori iOS.
/// Defaults to `.system` and applies immediately across views, mascot, sheets, keyboard, and native glass chrome.
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
