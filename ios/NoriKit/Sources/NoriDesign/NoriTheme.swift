import SwiftUI

public extension Color {
    init(hex: UInt32, alpha: Double = 1.0) {
        let r = Double((hex >> 16) & 0xFF) / 255.0
        let g = Double((hex >> 8) & 0xFF) / 255.0
        let b = Double(hex & 0xFF) / 255.0
        self.init(.sRGB, red: r, green: g, blue: b, opacity: alpha)
    }
}

/// Exact Nori color tokens sourced from `web/src/app/globals.css`.
public enum NoriColors {
    // Surfaces & Borders
    public static let canvas = Color(hex: 0xFFFFFF)
    public static let surface = Color(hex: 0xFFFFFF)
    public static let sidebar = Color(hex: 0xF8F6FC)
    public static let backdrop = Color(hex: 0xEEE5FC)
    public static let fill1 = Color(hex: 0xF7F5FB)
    public static let fill2 = Color(hex: 0xEEE8FA)
    public static let hairline = Color(hex: 0xE7E2EF)
    public static let hairlineStrong = Color(hex: 0xCFC3E9)
    public static let overlay = Color(hex: 0x160531, alpha: 0.26)

    // Typography
    public static let ink = Color(hex: 0x190936)
    public static let ink2 = Color(hex: 0x726D82)
    public static let ink3 = Color(hex: 0x6E6880)
    public static let onAccent = Color(hex: 0xFFFFFF)

    // Electric Violet Accent
    public static let accent = Color(hex: 0x5000FF)
    public static let accentHover = Color(hex: 0x4200D6)
    public static let accentPressed = Color(hex: 0x3500AE)
    public static let accentSignal = Color(hex: 0x7943FF)
    public static let accentSubtle = Color(hex: 0xF1EBFF)
    public static let accentMuted = Color(hex: 0xE9DFFF)

    // Semantic Status Tones
    public static let warning = Color(hex: 0x946000)
    public static let warningBg = Color(hex: 0xFFF4DC)
    public static let danger = Color(hex: 0xCC1F33)
    public static let dangerBg = Color(hex: 0xFFF0F2)
    public static let ok = Color(hex: 0x1C7A43)
    public static let okBg = Color(hex: 0xE9F8EF)

    // NoriMark Onigiri Palette
    public static let markInk = Color(hex: 0x190936)
    public static let markFill = Color(hex: 0x101A30)
    public static let markFillSoft = Color(hex: 0xFDFCFF)

    // Glass & Chrome
    public static let glass = Color(hex: 0xFFFFFF, alpha: 0.92)
    public static let shadowColor = Color(hex: 0x302818, alpha: 0.08)
}

public enum NoriRadius {
    public static let pill: CGFloat = 999
    public static let bubble: CGFloat = 20
    public static let bubblePinched: CGFloat = 6
    public static let sheet: CGFloat = 22
    public static let card: CGFloat = 16
    public static let row: CGFloat = 14
    public static let tile: CGFloat = 12
    public static let chip: CGFloat = 10
}

public enum NoriTypography {
    public static let display = Font.system(size: 30, weight: .bold, design: .default)
    public static let title = Font.system(size: 22, weight: .bold, design: .default)
    public static let section = Font.system(size: 16, weight: .semibold, design: .default)
    public static let body = Font.system(size: 15, weight: .regular, design: .default)
    public static let bodyMedium = Font.system(size: 15, weight: .medium, design: .default)
    public static let rowTitle = Font.system(size: 14.5, weight: .semibold, design: .default)
    public static let meta = Font.system(size: 13, weight: .regular, design: .default)
    public static let metaSemibold = Font.system(size: 13, weight: .semibold, design: .default)
    public static let caption = Font.system(size: 12, weight: .medium, design: .default)
    public static let mono = Font.system(size: 12.5, weight: .medium, design: .monospaced)
    public static let monoSmall = Font.system(size: 11.5, weight: .medium, design: .monospaced)
}
