import Testing
import SwiftUI
@testable import NoriDesign

#if canImport(AppKit)
import AppKit
#elseif canImport(UIKit)
import UIKit
#endif

@Suite("Nori Dark Mode Design System")
struct NoriDesignTests {

    private func hexString(for color: Color, dark: Bool) -> String {
        #if canImport(AppKit)
        let appearance = NSAppearance(named: dark ? .darkAqua : .aqua)!
        var resolvedHex = ""
        appearance.performAsCurrentDrawingAppearance {
            let nsColor = NSColor(color).usingColorSpace(.sRGB)!
            let r = Int(round(nsColor.redComponent * 255.0))
            let g = Int(round(nsColor.greenComponent * 255.0))
            let b = Int(round(nsColor.blueComponent * 255.0))
            resolvedHex = String(format: "#%02X%02X%02X", r, g, b)
        }
        return resolvedHex
        #elseif canImport(UIKit)
        let traits = UITraitCollection(userInterfaceStyle: dark ? .dark : .light)
        let uiColor = UIColor(color).resolvedColor(with: traits)
        var r: CGFloat = 0
        var g: CGFloat = 0
        var b: CGFloat = 0
        var a: CGFloat = 0
        uiColor.getRed(&r, green: &g, blue: &b, alpha: &a)
        return String(format: "#%02X%02X%02X", Int(round(r * 255)), Int(round(g * 255)), Int(round(b * 255)))
        #else
        return ""
        #endif
    }

    @Test("NoriAppearance supports System, Light, and Dark with System as default")
    func appearanceOptions() {
        #expect(NoriAppearance.allCases == [.system, .light, .dark])
        #expect(NoriAppearance.system.colorScheme == nil)
        #expect(NoriAppearance.light.colorScheme == .light)
        #expect(NoriAppearance.dark.colorScheme == .dark)
        #expect(NoriAppearance(storedRawValue: "unknown") == .system)
        #expect(NoriAppearance(storedRawValue: "dark") == .dark)
        #expect(NoriAppearance(storedRawValue: "light") == .light)
    }

    @Test("Dark mode foundation, typography, brand, and mascot tokens match calibrated design palette")
    func darkModeFoundationTokens() {
        // Foundation surfaces
        #expect(hexString(for: NoriColors.Background.canvas, dark: true) == "#000000")
        #expect(hexString(for: NoriColors.Background.surface, dark: true) == "#0B0B0F")
        #expect(hexString(for: NoriColors.Background.elevated, dark: true) == "#14141A")
        #expect(hexString(for: NoriColors.Background.selected, dark: true) == "#261F38")
        #expect(hexString(for: NoriColors.sheetBackground, dark: true) == "#0E0E13")

        // Typography
        #expect(hexString(for: NoriColors.Text.primary, dark: true) == "#F2EFF8")
        #expect(hexString(for: NoriColors.Text.secondary, dark: true) == "#A9A2B8")
        #expect(hexString(for: NoriColors.Text.muted, dark: true) == "#878196")
        #expect(hexString(for: NoriColors.Text.disabled, dark: true) == "#6B6578")

        // Brand
        #expect(hexString(for: NoriColors.Brand.primary, dark: true) == "#7A3DF6")
        #expect(hexString(for: NoriColors.Brand.accent, dark: true) == "#A98AFF")

        // Mascot
        #expect(hexString(for: NoriColors.Mascot.body, dark: true) == "#C6C4CE")
        #expect(hexString(for: NoriColors.Mascot.ink, dark: true) == "#171522")
        #expect(hexString(for: NoriColors.Mascot.seaweed, dark: true) == "#171522")
        #expect(hexString(for: NoriColors.Mascot.edge, dark: true) == "#D1CFD8")
        #expect(hexString(for: NoriColors.Mascot.activity, dark: true) == "#7A3DF6")
    }

    @Test("Dark mode semantic status tones match specification table")
    func darkModeSemanticStatusTokens() {
        #expect(hexString(for: NoriTone.ok.foreground, dark: true) == "#85D4A4")
        #expect(hexString(for: NoriTone.ok.background, dark: true) == "#0F2019")

        #expect(hexString(for: NoriTone.working.foreground, dark: true) == "#A98AFF")
        #expect(hexString(for: NoriTone.working.background, dark: true) == "#1B162C")

        #expect(hexString(for: NoriTone.warning.foreground, dark: true) == "#F2C276")
        #expect(hexString(for: NoriTone.warning.background, dark: true) == "#21190F")

        #expect(hexString(for: NoriTone.danger.foreground, dark: true) == "#FF9BA7")
        #expect(hexString(for: NoriTone.danger.background, dark: true) == "#25131B")

        #expect(hexString(for: NoriTone.scheduled.foreground, dark: true) == "#99B9EF")
        #expect(hexString(for: NoriTone.scheduled.background, dark: true) == "#111A28")

        #expect(hexString(for: NoriTone.neutral.foreground, dark: true) == "#878196")
        #expect(hexString(for: NoriTone.neutral.background, dark: true) == "#14141A")
    }

    @Test("Light mode foundation and mascot tokens remain preserved")
    func lightModePreserved() {
        #expect(hexString(for: NoriColors.canvas, dark: false) == "#FFFFFF")
        #expect(hexString(for: NoriColors.ink, dark: false) == "#190936")
        #expect(hexString(for: NoriColors.brandPrimary, dark: false) == "#5000FF")
        #expect(hexString(for: NoriColors.Mascot.body, dark: false) == "#FDFCFF")
        #expect(hexString(for: NoriColors.Mascot.seaweed, dark: false) == "#101A30")
        #expect(hexString(for: NoriColors.Mascot.ink, dark: false) == "#190936")
    }

    @Test("Geometry radii match design specification")
    func geometryRadii() {
        #expect(NoriRadius.tile == 12)
        #expect(NoriRadius.card == 16)
        #expect(NoriRadius.bubble == 20)
        #expect(NoriRadius.approvalCard == 20)
        #expect(NoriRadius.sheet == 28)
    }
}
