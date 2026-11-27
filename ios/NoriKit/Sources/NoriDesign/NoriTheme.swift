import SwiftUI
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif

public extension Color {
    init(hex: UInt32, alpha: Double = 1.0) {
        let r = Double((hex >> 16) & 0xFF) / 255.0
        let g = Double((hex >> 8) & 0xFF) / 255.0
        let b = Double(hex & 0xFF) / 255.0
        self.init(.sRGB, red: r, green: g, blue: b, opacity: alpha)
    }

    static func adaptive(
        light: UInt32,
        lightAlpha: Double = 1.0,
        dark: UInt32,
        darkAlpha: Double = 1.0
    ) -> Color {
        #if canImport(UIKit)
        return Color(uiColor: UIColor { traits in
            let isDark = traits.userInterfaceStyle == .dark
            let hex = isDark ? dark : light
            let alpha = isDark ? darkAlpha : lightAlpha
            return UIColor(
                red: CGFloat((hex >> 16) & 0xFF) / 255.0,
                green: CGFloat((hex >> 8) & 0xFF) / 255.0,
                blue: CGFloat(hex & 0xFF) / 255.0,
                alpha: CGFloat(alpha)
            )
        })
        #elseif canImport(AppKit)
        return Color(nsColor: NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            let hex = isDark ? dark : light
            let alpha = isDark ? darkAlpha : lightAlpha
            return NSColor(
                srgbRed: CGFloat((hex >> 16) & 0xFF) / 255.0,
                green: CGFloat((hex >> 8) & 0xFF) / 255.0,
                blue: CGFloat(hex & 0xFF) / 255.0,
                alpha: CGFloat(alpha)
            )
        })
        #else
        return Color(hex: light, alpha: lightAlpha)
        #endif
    }
}

public enum NoriColors {
    public static let canvas = Color.adaptive(light: 0xFFFFFF, dark: 0x000000)
    public static let surface = Color.adaptive(light: 0xFFFFFF, dark: 0x0B0B0F)
    public static let sheetBackground = Color.adaptive(light: 0xFFFFFF, dark: 0x0E0E13)
    public static let sidebar = Color.adaptive(light: 0xF8F6FC, dark: 0x07070A)
    public static let surfaceTinted = Color.adaptive(light: 0xFAF8FE, dark: 0x100E18)
    public static let backdrop = Color.adaptive(light: 0xEEE5FC, dark: 0x14141A)
    public static let elevated = Color.adaptive(light: 0xF7F5FB, dark: 0x14141A)
    public static let fill1 = elevated
    public static let selected = Color.adaptive(light: 0xEEE8FA, dark: 0x261F38)
    public static let fill2 = selected

    public static let hairline = Color.adaptive(light: 0xE7E2EF, dark: 0xFFFFFF, darkAlpha: 0.06)
    public static let cardBorder = Color.adaptive(light: 0xE7E2EF, dark: 0x000000, darkAlpha: 0.0)
    public static let hairlineStrong = Color.adaptive(light: 0xCFC3E9, dark: 0xFFFFFF, darkAlpha: 0.14)
    public static let accentBorder = Color.adaptive(light: 0xDFD4F6, dark: 0xA98AFF, darkAlpha: 0.20)
    public static let overlay = Color.adaptive(light: 0x160531, lightAlpha: 0.26, dark: 0x000000, darkAlpha: 0.68)

    public static let ink = Color.adaptive(light: 0x190936, dark: 0xF2EFF8)
    public static let ink2 = Color.adaptive(light: 0x726D82, dark: 0xA9A2B8)
    public static let ink3 = Color.adaptive(light: 0x6E6880, dark: 0x878196)
    public static let disabled = Color.adaptive(light: 0xA29CB0, dark: 0x6B6578)
    public static let disabledBg = Color.adaptive(light: 0xECE9F4, dark: 0x14141A)
    public static let onAccent = Color(hex: 0xFFFFFF)

    public static let brandPrimary = Color.adaptive(light: 0x5000FF, dark: 0x7A3DF6)
    public static let accentHover = Color.adaptive(light: 0x4200D6, dark: 0x692FE0)
    public static let accentPressed = Color.adaptive(light: 0x3500AE, dark: 0x5922CC)
    public static let accent = Color.adaptive(light: 0x5000FF, dark: 0xA98AFF)
    public static let accentSignal = Color.adaptive(light: 0x7943FF, dark: 0xA98AFF)
    public static let accentSubtle = Color.adaptive(light: 0xF1EBFF, dark: 0x1B162C)
    public static let accentMuted = Color.adaptive(light: 0xE9DFFF, dark: 0x261F38)

    public static let ok = Color.adaptive(light: 0x1C7A43, dark: 0x85D4A4)
    public static let okBg = Color.adaptive(light: 0xE9F8EF, dark: 0x0F2019)

    public static let working = Color.adaptive(light: 0x5000FF, dark: 0xA98AFF)
    public static let workingBg = Color.adaptive(light: 0xF1EBFF, dark: 0x1B162C)

    public static let warning = Color.adaptive(light: 0x946000, dark: 0xF2C276)
    public static let warningBg = Color.adaptive(light: 0xFFF4DC, dark: 0x21190F)

    public static let danger = Color.adaptive(light: 0xCC1F33, dark: 0xFF9BA7)
    public static let dangerBg = Color.adaptive(light: 0xFFF0F2, dark: 0x25131B)

    public static let scheduled = Color.adaptive(light: 0x1D5BB8, dark: 0x99B9EF)
    public static let scheduledBg = Color.adaptive(light: 0xEAF2FE, dark: 0x111A28)

    public static let inactive = Color.adaptive(light: 0x6E6880, dark: 0x878196)
    public static let inactiveBg = Color.adaptive(light: 0xF7F5FB, dark: 0x14141A)

    public static let markFillSoft = Color.adaptive(light: Mascot.bodyLightHex, dark: Mascot.bodyDarkHex)
    public static let markFill = Color.adaptive(light: Mascot.seaweedLightHex, dark: Mascot.seaweedDarkHex)
    public static let markInk = Color.adaptive(light: Mascot.inkLightHex, dark: Mascot.inkDarkHex)
    public static let markEdge = Color.adaptive(light: Mascot.edgeLightHex, dark: Mascot.edgeDarkHex)
    public static let markActivity = brandPrimary

    public static let chipSelectedBg = Color.adaptive(light: 0xEEE8FA, dark: 0x261F38)
    public static let chipSelectedFg = Color.adaptive(light: 0x5000FF, dark: 0xA98AFF)
    public static let chipSelectedBorder = Color.adaptive(light: 0xDFD4F6, dark: 0xA98AFF, darkAlpha: 0.20)

    public static let glass = Color.adaptive(light: 0xFFFFFF, lightAlpha: 0.92, dark: 0x14141A, darkAlpha: 0.88)
    public static let shadowColor = Color.adaptive(light: 0x302818, lightAlpha: 0.08, dark: 0x000000, darkAlpha: 0.0)
    public static let cardShadow = Color.adaptive(light: 0x190936, lightAlpha: 0.04, dark: 0x000000, darkAlpha: 0.0)
    public static let accentShadow = Color.adaptive(light: 0x5000FF, lightAlpha: 0.06, dark: 0x000000, darkAlpha: 0.0)
    public static let toastBg = Color.adaptive(light: 0x190936, dark: 0x1E1B29)
    public static let toastFg = Color.adaptive(light: 0xFFFFFF, dark: 0xF2EFF8)
    public static let heroStart = Color.adaptive(light: 0xFBF9FF, dark: 0x100E18)
    public static let heroEnd = Color.adaptive(light: 0xF1EBFF, dark: 0x171226)
    public static let topWashStart = Color.adaptive(light: 0xFAF8FE, dark: 0x100E18)
    public static let topWashEnd = Color.adaptive(light: 0xFFFFFF, dark: 0x000000)

    public enum Swatch {
        public static let lightBg = Color(hex: 0xFFFFFF)
        public static let darkBg = Color(hex: 0x000000)
        public static let lightBubble = Color(hex: 0x5000FF)
        public static let darkBubble = Color(hex: 0x7A3DF6)
        public static let lightLine = Color(hex: 0xE7E2EF)
        public static let darkLine = Color(hex: 0x261F38)
        public static let systemLine = Color(hex: 0xA98AFF, alpha: 0.6)
    }

    public enum Background {
        public static let canvas = NoriColors.canvas
        public static let surface = NoriColors.surface
        public static let elevated = NoriColors.elevated
        public static let selected = NoriColors.selected
    }

    public enum Border {
        public static let subtle = NoriColors.hairline
        public static let strong = NoriColors.hairlineStrong
    }

    public enum Text {
        public static let primary = NoriColors.ink
        public static let secondary = NoriColors.ink2
        public static let muted = NoriColors.ink3
        public static let disabled = NoriColors.disabled
    }

    public enum Brand {
        public static let primary = NoriColors.brandPrimary
        public static let accent = NoriColors.accent
    }

    public enum Mascot {
        public static let bodyLightHex: UInt32 = 0xFDFCFF
        public static let bodyDarkHex: UInt32 = 0xC6C4CE
        public static let seaweedLightHex: UInt32 = 0x101A30
        public static let seaweedDarkHex: UInt32 = 0x171522
        public static let inkLightHex: UInt32 = 0x190936
        public static let inkDarkHex: UInt32 = 0x171522
        public static let edgeLightHex: UInt32 = 0x190936
        public static let edgeDarkHex: UInt32 = 0xD1CFD8

        public static let body = NoriColors.markFillSoft
        public static let ink = NoriColors.markInk
        public static let seaweed = NoriColors.markFill
        public static let edge = NoriColors.markEdge
        public static let activity = NoriColors.markActivity

        public static func resolvedColors(for colorScheme: ColorScheme) -> (body: Color, seaweed: Color, ink: Color, edge: Color) {
            let isDark = colorScheme == .dark
            return (
                body: Color(hex: isDark ? bodyDarkHex : bodyLightHex),
                seaweed: Color(hex: isDark ? seaweedDarkHex : seaweedLightHex),
                ink: Color(hex: isDark ? inkDarkHex : inkLightHex),
                edge: Color(hex: isDark ? edgeDarkHex : edgeLightHex)
            )
        }
    }

    public enum Status {
        public static let success = NoriColors.ok
        public static let successBg = NoriColors.okBg
        public static let working = NoriColors.working
        public static let workingBg = NoriColors.workingBg
        public static let warning = NoriColors.warning
        public static let warningBg = NoriColors.warningBg
        public static let error = NoriColors.danger
        public static let errorBg = NoriColors.dangerBg
        public static let scheduled = NoriColors.scheduled
        public static let scheduledBg = NoriColors.scheduledBg
        public static let inactive = NoriColors.inactive
        public static let inactiveBg = NoriColors.inactiveBg
    }
}

public enum NoriSpacing {
    public static let xs: CGFloat = 4
    public static let sm: CGFloat = 8
    public static let md: CGFloat = 12
    public static let lg: CGFloat = 16
    public static let xl: CGFloat = 20
    public static let xxl: CGFloat = 24
    public static let xxxl: CGFloat = 32
}

public enum NoriRadius {
    public static let pill: CGFloat = 999
    public static let sheet: CGFloat = 28
    public static let bubble: CGFloat = 20
    public static let approvalCard: CGFloat = 20
    public static let bubblePinched: CGFloat = 6
    public static let card: CGFloat = 16
    public static let row: CGFloat = 14
    public static let tile: CGFloat = 12
    public static let chip: CGFloat = 10
}

public enum NoriTypography {
    public static let display = Font.system(.largeTitle, design: .default, weight: .bold)
    public static let title = Font.system(.title2, design: .default, weight: .bold)
    public static let section = Font.system(.callout, design: .default, weight: .semibold)
    public static let cardHeading = Font.system(.headline, design: .default, weight: .semibold)
    public static let body = Font.system(.subheadline, design: .default, weight: .regular)
    public static let bodyMedium = Font.system(.subheadline, design: .default, weight: .medium)
    public static let rowTitle = Font.system(.subheadline, design: .default, weight: .semibold)
    public static let meta = Font.system(.footnote, design: .default, weight: .regular)
    public static let metaSemibold = Font.system(.footnote, design: .default, weight: .semibold)
    public static let caption = Font.system(.caption, design: .default, weight: .medium)
    public static let mono = Font.system(.caption, design: .monospaced, weight: .medium)
    public static let monoSmall = Font.system(.caption2, design: .monospaced, weight: .medium)
}
