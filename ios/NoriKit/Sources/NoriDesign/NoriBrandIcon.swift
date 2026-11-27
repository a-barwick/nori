import SwiftUI

public enum NoriBrandKind: Hashable, Sendable {
    case googleCalendar
    case googleDrive
    case gmail
    case linear
    case notion
    case anthropic
    case openAI
    case googleGemini
    case vercel
    case homeComputer
    case customApp
    case genericKey

    public var isBranded: Bool {
        switch self {
        case .homeComputer, .customApp, .genericKey:
            return false
        default:
            return true
        }
    }

    public static func forProvider(provider: String? = nil, model: String? = nil, backend: String? = nil) -> NoriBrandKind {
        let hay = [provider, model, backend].compactMap { $0 }.joined(separator: " ").lowercased()
        if backend?.lowercased() == "home"
            || hay.contains("home-")
            || hay.contains("ollama")
            || hay.contains("inkling")
            || hay.contains("local")
        {
            return .homeComputer
        }
        if hay.contains("anthropic") || hay.contains("claude") {
            return .anthropic
        }
        if hay.contains("openai") || hay.contains("gpt") || hay.contains("o3") || hay.contains("o4") || hay.contains("o1") {
            return .openAI
        }
        if hay.contains("google") || hay.contains("gemini") {
            return .googleGemini
        }
        if hay.contains("vercel") {
            return .vercel
        }
        return .genericKey
    }

    public static func forApp(name: String? = nil, endpoint: String? = nil) -> NoriBrandKind {
        let hay = [name, endpoint].compactMap { $0 }.joined(separator: " ").lowercased()
        if hay.contains("linear") { return .linear }
        if hay.contains("notion") { return .notion }
        if hay.contains("calendar") { return .googleCalendar }
        if hay.contains("drive") { return .googleDrive }
        if hay.contains("gmail") || hay.contains("mail") { return .gmail }
        return .customApp
    }
}

public struct NoriBrandIcon: View {
    @Environment(\.colorScheme) private var colorScheme
    public let kind: NoriBrandKind
    public let size: CGFloat

    public init(_ kind: NoriBrandKind, size: CGFloat = 36) {
        self.kind = kind
        self.size = size
    }

    private var tileBackground: Color {
        if !kind.isBranded {
            return NoriColors.fill1
        }
        return colorScheme == .dark ? Color(hex: 0x181820) : Color.white
    }

    private var tileBorder: Color {
        if !kind.isBranded {
            return .clear
        }
        return colorScheme == .dark ? Color.white.opacity(0.08) : Color(hex: 0x190936, alpha: 0.08)
    }

    public var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .fill(tileBackground)
            if kind.isBranded {
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .strokeBorder(tileBorder, lineWidth: 1)
            }
            glyph
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }

    @ViewBuilder
    private var glyph: some View {
        switch kind {
        case .googleCalendar:
            GoogleCalendarGlyph(isDark: colorScheme == .dark)
                .frame(width: 20, height: 20)
        case .googleDrive:
            GoogleDriveGlyph()
                .frame(width: 20, height: 20)
        case .gmail:
            GmailGlyph()
                .frame(width: 20, height: 20)
        case .linear:
            LinearGlyph()
                .frame(width: 19, height: 19)
        case .notion:
            NotionGlyph(isDark: colorScheme == .dark)
                .frame(width: 19, height: 19)
        case .anthropic:
            AnthropicGlyph()
                .frame(width: 18, height: 18)
        case .openAI:
            OpenAIGlyph(isDark: colorScheme == .dark)
                .frame(width: 19, height: 19)
        case .googleGemini:
            GeminiGlyph()
                .frame(width: 19, height: 19)
        case .vercel:
            VercelGlyph(isDark: colorScheme == .dark)
                .frame(width: 16, height: 16)
        case .homeComputer:
            Image(systemName: "desktopcomputer")
                .font(.system(size: 16, weight: .medium))
                .foregroundStyle(NoriColors.ink2)
        case .customApp:
            Image(systemName: "powerplug")
                .font(.system(size: 16, weight: .medium))
                .foregroundStyle(NoriColors.ink2)
        case .genericKey:
            Image(systemName: "key")
                .font(.system(size: 16, weight: .medium))
                .foregroundStyle(NoriColors.ink2)
        }
    }
}

private struct GoogleCalendarGlyph: View {
    let isDark: Bool
    var body: some View {
        Canvas { ctx, size in
            let w = size.width
            let h = size.height
            let r = w * 0.16
            let outer = Path(roundedRect: CGRect(x: w * 0.1, y: h * 0.1, width: w * 0.8, height: h * 0.8), cornerRadius: r)
            ctx.fill(outer, with: .color(Color(hex: 0x4285F4)))

            let inner = CGRect(x: w * 0.25, y: h * 0.28, width: w * 0.5, height: h * 0.44)
            ctx.fill(Path(inner), with: .color(.white))

            // Bottom green bar
            ctx.fill(Path(CGRect(x: w * 0.1, y: h * 0.72, width: w * 0.55, height: h * 0.18)), with: .color(Color(hex: 0x34A853)))
            // Right yellow bar
            ctx.fill(Path(CGRect(x: w * 0.75, y: h * 0.28, width: w * 0.15, height: h * 0.44)), with: .color(Color(hex: 0xFBBC04)))
            // Bottom-right red fold
            var fold = Path()
            fold.move(to: CGPoint(x: w * 0.65, y: h * 0.9))
            fold.addLine(to: CGPoint(x: w * 0.65, y: h * 0.72))
            fold.addLine(to: CGPoint(x: w * 0.9, y: h * 0.72))
            fold.closeSubpath()
            ctx.fill(fold, with: .color(Color(hex: 0xEA4335)))

            let text = Text("31")
                .font(.system(size: w * 0.36, weight: .bold))
                .foregroundStyle(Color(hex: 0x1967D2))
            ctx.draw(text, at: CGPoint(x: w * 0.5, y: h * 0.51))
            _ = isDark
        }
    }
}

private struct GoogleDriveGlyph: View {
    var body: some View {
        Canvas { ctx, size in
            let w = size.width
            let h = size.height

            // Left green leg
            var left = Path()
            left.move(to: CGPoint(x: w * 0.36, y: h * 0.14))
            left.addLine(to: CGPoint(x: w * 0.52, y: h * 0.42))
            left.addLine(to: CGPoint(x: w * 0.25, y: h * 0.88))
            left.addLine(to: CGPoint(x: w * 0.09, y: h * 0.60))
            left.closeSubpath()
            ctx.fill(left, with: .color(Color(hex: 0x00AC47)))

            // Right yellow leg
            var right = Path()
            right.move(to: CGPoint(x: w * 0.36, y: h * 0.14))
            right.addLine(to: CGPoint(x: w * 0.64, y: h * 0.14))
            right.addLine(to: CGPoint(x: w * 0.91, y: h * 0.60))
            right.addLine(to: CGPoint(x: w * 0.63, y: h * 0.60))
            right.closeSubpath()
            ctx.fill(right, with: .color(Color(hex: 0xFFBA00)))

            // Bottom blue bar
            var bottom = Path()
            bottom.move(to: CGPoint(x: w * 0.25, y: h * 0.88))
            bottom.addLine(to: CGPoint(x: w * 0.39, y: h * 0.60))
            bottom.addLine(to: CGPoint(x: w * 0.91, y: h * 0.60))
            bottom.addLine(to: CGPoint(x: w * 0.77, y: h * 0.88))
            bottom.closeSubpath()
            ctx.fill(bottom, with: .color(Color(hex: 0x2684FC)))
        }
    }
}

private struct GmailGlyph: View {
    var body: some View {
        Canvas { ctx, size in
            let w = size.width
            let h = size.height

            // Left blue pillar
            ctx.fill(
                Path(roundedRect: CGRect(x: w * 0.08, y: h * 0.26, width: w * 0.18, height: h * 0.58), cornerRadius: 1.5),
                with: .color(Color(hex: 0x4285F4))
            )
            // Right green pillar
            ctx.fill(
                Path(roundedRect: CGRect(x: w * 0.74, y: h * 0.26, width: w * 0.18, height: h * 0.58), cornerRadius: 1.5),
                with: .color(Color(hex: 0x34A853))
            )
            // Left red corner
            var leftTop = Path()
            leftTop.move(to: CGPoint(x: w * 0.08, y: h * 0.26))
            leftTop.addLine(to: CGPoint(x: w * 0.26, y: h * 0.26))
            leftTop.addLine(to: CGPoint(x: w * 0.26, y: h * 0.44))
            leftTop.addLine(to: CGPoint(x: w * 0.08, y: h * 0.32))
            leftTop.closeSubpath()
            ctx.fill(leftTop, with: .color(Color(hex: 0xC5221F)))

            // Right yellow corner
            var rightTop = Path()
            rightTop.move(to: CGPoint(x: w * 0.74, y: h * 0.26))
            rightTop.addLine(to: CGPoint(x: w * 0.92, y: h * 0.26))
            rightTop.addLine(to: CGPoint(x: w * 0.92, y: h * 0.32))
            rightTop.addLine(to: CGPoint(x: w * 0.74, y: h * 0.44))
            rightTop.closeSubpath()
            ctx.fill(rightTop, with: .color(Color(hex: 0xFBBC04)))

            // Center red V
            var vShape = Path()
            vShape.move(to: CGPoint(x: w * 0.26, y: h * 0.26))
            vShape.addLine(to: CGPoint(x: w * 0.50, y: h * 0.46))
            vShape.addLine(to: CGPoint(x: w * 0.74, y: h * 0.26))
            vShape.addLine(to: CGPoint(x: w * 0.74, y: h * 0.44))
            vShape.addLine(to: CGPoint(x: w * 0.50, y: h * 0.64))
            vShape.addLine(to: CGPoint(x: w * 0.26, y: h * 0.44))
            vShape.closeSubpath()
            ctx.fill(vShape, with: .color(Color(hex: 0xEA4335)))
        }
    }
}

private struct LinearGlyph: View {
    var body: some View {
        Canvas { ctx, size in
            let w = size.width
            let h = size.height
            let rect = CGRect(x: w * 0.08, y: h * 0.08, width: w * 0.84, height: h * 0.84)
            let circle = Path(ellipseIn: rect)
            ctx.clip(to: circle)
            ctx.fill(circle, with: .color(Color(hex: 0x5E6AD2)))

            // Three diagonal white stripes on bottom-left half (Linear mark)
            for offset in [0.06, 0.20, 0.34] {
                var stripe = Path()
                stripe.move(to: CGPoint(x: w * (0.04 + offset), y: h * 0.96))
                stripe.addLine(to: CGPoint(x: w * (0.04 + offset - 0.35), y: h * (0.96 - 0.35)))
                ctx.stroke(
                    stripe,
                    with: .color(.white.opacity(0.92)),
                    style: StrokeStyle(lineWidth: w * 0.075, lineCap: .round)
                )
            }
        }
    }
}

private struct NotionGlyph: View {
    let isDark: Bool
    var body: some View {
        let fg = isDark ? Color(hex: 0xF2EFF8) : Color(hex: 0x190936)
        ZStack {
            RoundedRectangle(cornerRadius: 3.5, style: .continuous)
                .strokeBorder(fg, lineWidth: 1.6)
            Text("N")
                .font(.system(size: 11.5, weight: .bold, design: .serif))
                .foregroundStyle(fg)
        }
    }
}

private struct AnthropicGlyph: View {
    var body: some View {
        Canvas { ctx, size in
            let w = size.width
            let h = size.height
            let color = Color(hex: 0xD97757)

            var left = Path()
            left.move(to: CGPoint(x: w * 0.14, y: h * 0.86))
            left.addLine(to: CGPoint(x: w * 0.38, y: h * 0.14))
            left.addLine(to: CGPoint(x: w * 0.52, y: h * 0.14))
            left.addLine(to: CGPoint(x: w * 0.28, y: h * 0.86))
            left.closeSubpath()
            ctx.fill(left, with: .color(color))

            var right = Path()
            right.move(to: CGPoint(x: w * 0.56, y: h * 0.14))
            right.addLine(to: CGPoint(x: w * 0.70, y: h * 0.14))
            right.addLine(to: CGPoint(x: w * 0.94, y: h * 0.86))
            right.addLine(to: CGPoint(x: w * 0.80, y: h * 0.86))
            right.closeSubpath()
            ctx.fill(right, with: .color(color))

            ctx.fill(Path(CGRect(x: w * 0.44, y: h * 0.58, width: w * 0.32, height: h * 0.11)), with: .color(color))
        }
    }
}

private struct OpenAIGlyph: View {
    let isDark: Bool
    var body: some View {
        let fg = isDark ? Color(hex: 0xF2EFF8) : Color(hex: 0x190936)
        Canvas { ctx, size in
            let cx = size.width / 2
            let cy = size.height / 2
            let r = size.width * 0.26
            for i in 0..<6 {
                let angle = Double(i) * (.pi / 3.0)
                let px = cx + CGFloat(cos(angle)) * r * 0.85
                let py = cy + CGFloat(sin(angle)) * r * 0.85
                let loop = Path(ellipseIn: CGRect(x: px - r * 0.72, y: py - r * 0.72, width: r * 1.44, height: r * 1.44))
                ctx.stroke(loop, with: .color(fg), lineWidth: 1.5)
            }
        }
    }
}

private struct GeminiGlyph: View {
    var body: some View {
        Canvas { ctx, size in
            let w = size.width
            let h = size.height
            var star = Path()
            star.move(to: CGPoint(x: w * 0.5, y: h * 0.04))
            star.addQuadCurve(to: CGPoint(x: w * 0.96, y: h * 0.5), control: CGPoint(x: w * 0.56, y: h * 0.44))
            star.addQuadCurve(to: CGPoint(x: w * 0.5, y: h * 0.96), control: CGPoint(x: w * 0.56, y: h * 0.56))
            star.addQuadCurve(to: CGPoint(x: w * 0.04, y: h * 0.5), control: CGPoint(x: w * 0.44, y: h * 0.56))
            star.addQuadCurve(to: CGPoint(x: w * 0.5, y: h * 0.04), control: CGPoint(x: w * 0.44, y: h * 0.44))
            star.closeSubpath()

            ctx.fill(
                star,
                with: .linearGradient(
                    Gradient(colors: [Color(hex: 0x4285F4), Color(hex: 0x9B72CB), Color(hex: 0xD96570)]),
                    startPoint: .zero,
                    endPoint: CGPoint(x: w, y: h)
                )
            )
        }
    }
}

private struct VercelGlyph: View {
    let isDark: Bool
    var body: some View {
        let fg = isDark ? Color(hex: 0xF2EFF8) : Color(hex: 0x190936)
        Canvas { ctx, size in
            let w = size.width
            let h = size.height
            var tri = Path()
            tri.move(to: CGPoint(x: w * 0.5, y: h * 0.12))
            tri.addLine(to: CGPoint(x: w * 0.94, y: h * 0.88))
            tri.addLine(to: CGPoint(x: w * 0.06, y: h * 0.88))
            tri.closeSubpath()
            ctx.fill(tri, with: .color(fg))
        }
    }
}

public struct NoriBrandRow<Trailing: View>: View {
    public let title: String
    public let detail: String?
    public let brand: NoriBrandKind
    public let showChevron: Bool
    public let showDivider: Bool
    public let wrapTitle: Bool
    private let trailing: Trailing

    public init(
        title: String,
        detail: String? = nil,
        brand: NoriBrandKind,
        showChevron: Bool = false,
        showDivider: Bool = true,
        wrapTitle: Bool = false,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self.title = title
        self.detail = detail
        self.brand = brand
        self.showChevron = showChevron
        self.showDivider = showDivider
        self.wrapTitle = wrapTitle
        self.trailing = trailing()
    }

    public var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 11) {
                NoriBrandIcon(brand)

                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                        .lineLimit(wrapTitle ? nil : 1)
                        .multilineTextAlignment(.leading)

                    if let detail, !detail.isEmpty {
                        Text(detail)
                            .font(.system(size: 12.5))
                            .foregroundStyle(NoriColors.ink2)
                            .lineLimit(2)
                            .multilineTextAlignment(.leading)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                HStack(spacing: 6) {
                    trailing
                    if showChevron {
                        Image(systemName: "chevron.right")
                            .font(.system(size: 12, weight: .semibold))
                            .foregroundStyle(NoriColors.ink3)
                    }
                }
            }
            .padding(.horizontal, 13)
            .padding(.vertical, 12)
            .contentShape(Rectangle())

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .padding(.leading, 60)
            }
        }
    }
}

public extension NoriBrandRow where Trailing == EmptyView {
    init(
        title: String,
        detail: String? = nil,
        brand: NoriBrandKind,
        showChevron: Bool = false,
        showDivider: Bool = true,
        wrapTitle: Bool = false
    ) {
        self.init(
            title: title,
            detail: detail,
            brand: brand,
            showChevron: showChevron,
            showDivider: showDivider,
            wrapTitle: wrapTitle
        ) {
            EmptyView()
        }
    }
}
