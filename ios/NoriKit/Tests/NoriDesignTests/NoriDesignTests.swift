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
        #expect(hexString(for: NoriColors.Background.canvas, dark: true) == "#000000")
        #expect(hexString(for: NoriColors.Background.surface, dark: true) == "#0B0B0F")
        #expect(hexString(for: NoriColors.Background.elevated, dark: true) == "#14141A")
        #expect(hexString(for: NoriColors.Background.selected, dark: true) == "#261F38")
        #expect(hexString(for: NoriColors.sheetBackground, dark: true) == "#0E0E13")

        #expect(hexString(for: NoriColors.Text.primary, dark: true) == "#F2EFF8")
        #expect(hexString(for: NoriColors.Text.secondary, dark: true) == "#A9A2B8")
        #expect(hexString(for: NoriColors.Text.muted, dark: true) == "#878196")
        #expect(hexString(for: NoriColors.Text.disabled, dark: true) == "#6B6578")

        #expect(hexString(for: NoriColors.Brand.primary, dark: true) == "#7A3DF6")
        #expect(hexString(for: NoriColors.accentPressed, dark: true) == "#5922CC")
        #expect(hexString(for: NoriColors.Brand.accent, dark: true) == "#A98AFF")

        #expect(hexString(for: NoriColors.Mascot.body, dark: true) == "#C6C4CE")
        #expect(hexString(for: NoriColors.Mascot.ink, dark: true) == "#171522")
        #expect(hexString(for: NoriColors.Mascot.seaweed, dark: true) == "#171522")
        #expect(hexString(for: NoriColors.Mascot.edge, dark: true) == "#D1CFD8")
        #expect(hexString(for: NoriColors.Mascot.activity, dark: true) == "#7A3DF6")

        let darkMascot = NoriColors.Mascot.resolvedColors(for: .dark)
        #expect(hexString(for: darkMascot.body, dark: true) == "#C6C4CE")
        #expect(hexString(for: darkMascot.seaweed, dark: true) == "#171522")
        #expect(hexString(for: darkMascot.ink, dark: true) == "#171522")
        #expect(hexString(for: darkMascot.edge, dark: true) == "#D1CFD8")

        #expect(hexString(for: NoriColors.chipSelectedFg, dark: true) == "#A98AFF")
        #expect(hexString(for: NoriColors.chipSelectedBorder, dark: true) == "#A98AFF")
        #expect(hexString(for: NoriColors.toastBg, dark: true) == "#1E1B29")
        #expect(hexString(for: NoriColors.heroStart, dark: true) == "#100E18")
        #expect(hexString(for: NoriColors.heroEnd, dark: true) == "#171226")
        #expect(hexString(for: NoriColors.topWashStart, dark: true) == "#100E18")
    }

    @Test("Swatch tokens match cross-platform appearance swatch palette")
    func swatchTokens() {
        #expect(hexString(for: NoriColors.Swatch.lightBg, dark: false) == "#FFFFFF")
        #expect(hexString(for: NoriColors.Swatch.darkBg, dark: true) == "#000000")
        #expect(hexString(for: NoriColors.Swatch.lightBubble, dark: false) == "#5000FF")
        #expect(hexString(for: NoriColors.Swatch.darkBubble, dark: true) == "#7A3DF6")
        #expect(hexString(for: NoriColors.Swatch.lightLine, dark: false) == "#E7E2EF")
        #expect(hexString(for: NoriColors.Swatch.darkLine, dark: true) == "#261F38")
        #expect(hexString(for: NoriColors.Swatch.systemLine, dark: true) == "#A98AFF")
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

    @Test("Brand icons resolve providers and apps cleanly")
    func brandIconResolution() {
        #expect(NoriBrandKind.forProvider(provider: "anthropic") == .anthropic)
        #expect(NoriBrandKind.forProvider(model: "claude-sonnet-4-6") == .anthropic)
        #expect(NoriBrandKind.forProvider(provider: "openai") == .openAI)
        #expect(NoriBrandKind.forProvider(model: "gpt-5") == .openAI)
        #expect(NoriBrandKind.forProvider(provider: "google") == .googleGemini)
        #expect(NoriBrandKind.forProvider(model: "inkling-mini-2", backend: "home") == .homeComputer)
        #expect(NoriBrandKind.forProvider(provider: "custom") == .genericKey)

        #expect(NoriBrandKind.forApp(name: "Linear") == .linear)
        #expect(NoriBrandKind.forApp(name: "Notion") == .notion)
        #expect(NoriBrandKind.forApp(name: "Google Calendar") == .googleCalendar)
        #expect(NoriBrandKind.forApp(name: "Home Notes") == .customApp)
    }

    @Test("NoriDateFormatting.parse matches ISO8601DateFormatter across standard, fractional, offset, leap, and date-only inputs")
    func dateFormattingParseParity() {
        let isoFractional = ISO8601DateFormatter()
        isoFractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let isoStandard = ISO8601DateFormatter()
        isoStandard.formatOptions = [.withInternetDateTime]

        let samples: [String] = [
            "1970-01-01T00:00:00Z",
            "2000-02-29T12:34:56Z",
            "2024-02-29T23:59:59.999Z",
            "2026-11-27T10:15:30Z",
            "2026-11-27T10:15:30.250Z",
            "2026-11-27T10:15:30+02:00",
            "2026-11-27T10:15:30-08:00",
            "2026-11-27T10:15:30.5+05:30",
            "  2026-11-27T10:15:30.123Z \n"
        ]

        for sample in samples {
            let trimmed = sample.trimmingCharacters(in: .whitespacesAndNewlines)
            let expected = isoFractional.date(from: trimmed) ?? isoStandard.date(from: trimmed)
            let actual = NoriDateFormatting.parse(sample)
            #expect(actual != nil, "Failed to parse \(sample)")
            if let expected, let actual {
                #expect(abs(actual.timeIntervalSince1970 - expected.timeIntervalSince1970) < 0.001)
            }
        }

        // High-precision fractional seconds (microseconds / nanoseconds) and compact offsets
        let micro = NoriDateFormatting.parse("2026-11-27T10:15:30.123456Z")
        let milli = NoriDateFormatting.parse("2026-11-27T10:15:30.123Z")
        #expect(micro != nil && milli != nil)
        if let micro, let milli {
            #expect(abs((micro.timeIntervalSince1970 - milli.timeIntervalSince1970) - 0.000456) < 0.00001)
        }

        let compactOffset = NoriDateFormatting.parse("2026-11-27T10:15:30-0800")
        let colonOffset = NoriDateFormatting.parse("2026-11-27T10:15:30-08:00")
        #expect(compactOffset != nil && compactOffset == colonOffset)

        // Date-only parses in local calendar/timezone
        let dateOnly = NoriDateFormatting.parse("2026-11-27")
        #expect(dateOnly != nil)
        if let dateOnly {
            let comps = Calendar.current.dateComponents([.year, .month, .day, .hour, .minute], from: dateOnly)
            #expect(comps.year == 2026 && comps.month == 11 && comps.day == 27 && comps.hour == 0 && comps.minute == 0)
        }

        // Invalid inputs return nil
        #expect(NoriDateFormatting.parse("") == nil)
        #expect(NoriDateFormatting.parse("   ") == nil)
        #expect(NoriDateFormatting.parse("not-a-date") == nil)
        #expect(NoriDateFormatting.parse("2026-13-01T10:00:00Z") == nil)
        #expect(NoriDateFormatting.parse("2026-11-27T10:00:00") == nil)
    }

    @Test("NoriDateFormatting display helpers format relative, shortDateTime, reminderDateTime, and duration consistently")
    func dateFormattingDisplayHelpers() {
        let now = NoriDateFormatting.parse("2026-11-27T15:00:00Z")!
        #expect(NoriDateFormatting.relative("2026-11-27T14:59:40Z", now: now) == "Just now")
        #expect(NoriDateFormatting.relative("2026-11-27T14:45:00Z", now: now) == "15m ago")
        #expect(NoriDateFormatting.relative("2026-11-27T12:00:00Z", now: now) == "3h ago")
        #expect(NoriDateFormatting.relative("2026-11-27T15:10:00Z", now: now) == "in 10m")
        #expect(NoriDateFormatting.duration(from: "2026-11-27T14:00:00Z", to: "2026-11-27T15:30:00Z") == "1h 30m")
        #expect(NoriDateFormatting.expiryCountdown("2026-11-27T14:59:00Z", now: now) == "Expired")
        #expect(NoriDateFormatting.expiryCountdown("2026-11-27T15:15:00Z", now: now) == "Expires in 15m")

        let sameDayShort = NoriDateFormatting.shortDateTime("2026-11-27T14:30:00Z", now: now)
        #expect(!sameDayShort.isEmpty)
        let sameDayReminder = NoriDateFormatting.reminderDateTime("2026-11-27T14:30:00Z", now: now)
        #expect(sameDayReminder.hasPrefix("Today at "))
        let otherYear = NoriDateFormatting.shortDate("2025-05-10T12:00:00Z", now: now)
        #expect(otherYear.contains("2025"))
    }
}
