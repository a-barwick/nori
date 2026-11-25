# Nori iOS Design System

iOS-specific SwiftUI architecture, token APIs, native Liquid Glass conventions, and verification workflow.

> **Base specification:** Read [`docs/design-system.md`](../docs/design-system.md) first for platform-agnostic color theory, dark-mode separation principles, mascot rules, and semantic status meanings.

---

## 1. Architecture & Source of Truth

All iOS visual tokens and reusable primitives live in the `NoriDesign` target inside `ios/NoriKit/`, paired with minimal system-level color assets in `ios/NoriApp/Assets.xcassets/`.

| File | Responsibility |
| --- | --- |
| `ios/NoriKit/Sources/NoriDesign/NoriTheme.swift` | Adaptive Light/Dark `NoriColors`, structured namespaces, `NoriSpacing`, `NoriRadius`, `NoriTypography` |
| `ios/NoriKit/Sources/NoriDesign/NoriAppearance.swift` | `NoriAppearance` (`system`, `light`, `dark`) + `@AppStorage("nori.appearance")` key |
| `ios/NoriKit/Sources/NoriDesign/NoriMark.swift` | Appearance-aware animated SwiftUI `Canvas` mascot (`NoriMark`, `NoriMedallion`) |
| `ios/NoriKit/Sources/NoriDesign/NoriPrimitives.swift` | Core reusable components (`NoriCard`, `NoriRows`, `NoriPill`, `NoriTone`, `NoriButtonStyle`, `NoriNotice`, etc.) |
| `ios/NoriApp/Assets.xcassets/AccentColor.colorset` | Global UIKit/SwiftUI accent (`#5000FF` light / `#A98AFF` dark) |
| `ios/NoriApp/Assets.xcassets/LaunchBackground.colorset` | Launch screen backdrop (`#FFFFFF` light / `#000000` dark) referenced by `Info.plist` |
| `ios/NoriKit/Tests/NoriDesignTests/NoriDesignTests.swift` | Automated Light & Dark RGBA token, status tone, appearance, and radius assertions |

### Why Dynamic `UIColor`/`NSColor` Providers Are Used

`NoriTheme.swift` defines `Color.adaptive(light:lightAlpha:dark:darkAlpha:)` backed by dynamic `UIColor { traits in ... }` (iOS) and `NSColor(name: nil) { appearance in ... }` (macOS). This ensures:
1. Immediate trait-collection updates across SwiftUI views, sheets, alerts, and UIKit-backed controls (keyboards, pickers, `TabView`).
2. Zero asset-catalog compilation mismatch when running `swift test --package-path ios/NoriKit` on macOS (where `actool` does not compile `.xcassets`).
3. Direct unit-testability of resolved Light and Dark hex values in `NoriDesignTests`.

---

## 2. SwiftUI Token API

Prefer structured namespaces or the matching `NoriColors` properties—never write `Color(hex:)` inside `NoriFeatures`.

### Surfaces & Borders
```swift
NoriColors.canvas           // NoriColors.Background.canvas   (#FFFFFF / #000000)
NoriColors.surface          // NoriColors.Background.surface  (#FFFFFF / #0B0B0F)
NoriColors.sheetBackground  // Modal sheets                   (#FFFFFF / #0E0E13)
NoriColors.elevated         // NoriColors.Background.elevated (#F7F5FB / #14141A)
NoriColors.selected         // NoriColors.Background.selected (#EEE8FA / #261F38)
NoriColors.sidebar          // Resolved / recessed cards      (#F8F6FC / #07070A)
NoriColors.surfaceTinted    // Running task / budget strip    (#FAF8FE / #100E18)

NoriColors.cardBorder       // Card/input edge (#E7E2EF light / transparent dark)
NoriColors.hairline         // NoriColors.Border.subtle (#E7E2EF / white @ 6%)
NoriColors.hairlineStrong   // NoriColors.Border.strong (#CFC3E9 / white @ 14%)
NoriColors.accentBorder     // Accent rim (#DFD4F6 / #A98AFF @ 20%)
```

### Typography & Brand
```swift
NoriColors.ink              // NoriColors.Text.primary   (#190936 / #F2EFF8)
NoriColors.ink2             // NoriColors.Text.secondary (#726D82 / #A9A2B8)
NoriColors.ink3             // NoriColors.Text.muted     (#6E6880 / #878196)
NoriColors.disabled         // NoriColors.Text.disabled  (#A29CB0 / #6B6578)
NoriColors.disabledBg       // Disabled control fill     (#ECE9F4 / #14141A)
NoriColors.onAccent         // Text on brandPrimary      (#FFFFFF)

NoriColors.brandPrimary     // NoriColors.Brand.primary  (#5000FF / #7A3DF6)
NoriColors.accent           // NoriColors.Brand.accent   (#5000FF / #A98AFF)
NoriColors.accentSignal     // Focused input ring        (#7943FF / #A98AFF)
NoriColors.accentSubtle     // Tonal violet fill         (#F1EBFF / #1B162C)
```

### Semantic Tones (`NoriTone`)
Use `NoriTone` for pills (`NoriPill`), notices (`NoriNotice`), badges (`NoriBadge`), and row icon wells (`NoriRow`):

- `.ok` — Completed / Confirmed (`#85D4A4` on `#0F2019`)
- `.working` — In progress / Working / Connecting (`#A98AFF` on `#1B162C`)
- `.warning` — Needs approval / Needs you / Attention (`#F2C276` on `#21190F`)
- `.danger` — Failed / Error / Destructive (`#FF9BA7` on `#25131B`)
- `.scheduled` — Scheduled automations & routines (`#99B9EF` on `#111A28`)
- `.neutral` — Cancelled / Paused / Inactive (`#878196` on `#14141A`)
- `.accent` — Brand accent highlight (`#A98AFF` on `#1B162C`)

In Dark Mode, `NoriTone.border` resolves to transparent so pills and `NoriNotice` banners remain fill-only and noise-free.

---

## 3. Appearance Switching & Propagation

- **Storage:** `@AppStorage(NoriAppearance.storageKey)` (`"nori.appearance"`), defaulting to `NoriAppearance.system`.
- **Root Wiring:** Applied via `.preferredColorScheme(appearance.colorScheme)` at the `WindowGroup` root in `NoriApp.swift` and inside `NoriRootView.swift`.
- **Never add `UIUserInterfaceStyle` to `Info.plist`.** Doing so overrides `System` appearance and locks the entire process into a single mode.
- **Settings UI:** Exposed in **More → Appearance** (`SettingsSection.appearance` in `SettingsView.swift`), featuring a live interactive preview (mascot, activity dot, status pill, user bubble, assistant message) and a `System | Light | Dark` segmented control.

---

## 4. iOS Native Chrome, Materials & Components

### Floating Navigation (`TabView`)
- Use native SwiftUI `TabView` with `.tint(NoriColors.accent)` so iOS 26/27+ Liquid Glass renders the floating glass capsule in portrait and vertical rail in landscape automatically.
- Do not build custom opaque bottom tab bars.

### Modal Sheets
Every `.sheet` presentation must follow the sheet surface contract so sheets sit cleanly one level above the `#000000` canvas:
```swift
.sheet(isPresented: $isPresented) {
    MySheetView()
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .presentationBackground(NoriColors.sheetBackground)
        .presentationCornerRadius(NoriRadius.sheet) // 28 pt
}
```
Inside the sheet, use `.background(NoriColors.sheetBackground)` on the scroll content and `NoriColors.elevated` (`#14141A`) for text inputs (with `.foregroundStyle(NoriColors.ink)` and `.tint(NoriColors.accent)`).

### `NoriMark` (`Canvas` Rule)
SwiftUI `Canvas` closures do not reliably invalidate on trait changes if they only read static dynamic `Color` values. `NoriMark` reads `@Environment(\.colorScheme)` explicitly and resolves Light vs. Dark mascot fills/strokes before drawing so switching Appearance updates every mascot instance immediately.

### Buttons (`NoriButtonStyle`)
Use `NoriButtonStyle(.primary | .neutral | .tonal | .quiet | .danger, fullWidth:compact:)`.
- Disabled state is handled automatically via `@Environment(\.isEnabled)` (`NoriColors.disabledBg` + `NoriColors.disabled`, borderless in Dark Mode). **Do not** add manual `.opacity(isValid ? 1 : 0.5)` hacks at call sites.
- Use `NoriColors.brandPrimary` for `.tint(...)` on `Toggle` switches so filled switch tracks stay saturated `#7A3DF6`, while links, pickers, and cursors inherit `.tint(NoriColors.accent)` (`#A98AFF`).

### Composer & Focus Rings
- Background: `.ultraThinMaterial` overlaid with `NoriColors.glass.opacity(0.82)`.
- Border: `isInputFocused ? NoriColors.accentSignal : NoriColors.cardBorder` (`1.5 pt` when focused, invisible when unfocused in Dark Mode).
- Keyboard appearance adapts automatically from the root `preferredColorScheme`—never hardcode `.keyboardAppearance`.

### Typography, Haptics & Accessibility
- Keep SF Pro (`Font.system(...)`) and Dynamic Type scaling identical across Light and Dark modes.
- Respect `@Environment(\.accessibilityReduceMotion)` in all custom animations (`NoriMark`, pulsing dots).
- Pair key actions (send, approve, decline, steer, save) with `UIImpactFeedbackGenerator` / `UINotificationFeedbackGenerator`.

---

## 5. Verification & Visual QA Workflow

1. **Unit Tests (tokens, appearance, core):**
   ```bash
   swift test --package-path ios/NoriKit
   ```
   Always keep `NoriDesignTests` in sync whenever a token hex value is intentionally updated.

2. **iOS Simulator Build:**
   ```bash
   xcodebuild -project ios/Nori.xcodeproj -scheme Nori \
     -destination 'platform=iOS Simulator,name=iPhone 18 Pro' build
   ```

3. **Live Simulator Preview (Dark & Light, no OAuth required):**
   Debug builds support `-nori-preview` (`NoriPreviewStore.swift`, `#if DEBUG` only) to launch with a realistic multi-turn Chat thread, approval/clarification cards, Work items, Actions, Routines, Memory, and Settings:
   ```bash
   # Switch simulator appearance & launch preview build
   xcrun simctl ui booted appearance dark
   xcrun simctl launch --terminate-running-process booted com.nori.ios -nori-preview

   # Open Simulator GUI (Xcode 27 uses DeviceHub.app)
   open /Applications/Xcode.app/Contents/Applications/DeviceHub.app
   ```
   Deep-link directly to any screen with `-nori-url nori://chat`, `nori://work`, `nori://routines`, `nori://memory`, `nori://more`, or `nori://settings`.
