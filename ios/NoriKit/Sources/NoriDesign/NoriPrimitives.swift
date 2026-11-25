import SwiftUI
import NoriCore

// MARK: - Semantic Tone

public enum NoriTone: String, Sendable, Hashable {
    case neutral
    case accent
    case working
    case ok
    case warning
    case danger
    case scheduled

    public var foreground: Color {
        switch self {
        case .neutral: return NoriColors.inactive
        case .accent: return NoriColors.accent
        case .working: return NoriColors.working
        case .ok: return NoriColors.ok
        case .warning: return NoriColors.warning
        case .danger: return NoriColors.danger
        case .scheduled: return NoriColors.scheduled
        }
    }

    public var background: Color {
        switch self {
        case .neutral: return NoriColors.inactiveBg
        case .accent: return NoriColors.accentSubtle
        case .working: return NoriColors.workingBg
        case .ok: return NoriColors.okBg
        case .warning: return NoriColors.warningBg
        case .danger: return NoriColors.dangerBg
        case .scheduled: return NoriColors.scheduledBg
        }
    }

    public var border: Color {
        switch self {
        case .neutral: return Color.adaptive(light: 0xE7E2EF, dark: 0x000000, darkAlpha: 0.0)
        case .accent, .working: return Color.adaptive(light: 0xDFD4F6, dark: 0x000000, darkAlpha: 0.0)
        case .ok: return Color.adaptive(light: 0x1C7A43, lightAlpha: 0.22, dark: 0x000000, darkAlpha: 0.0)
        case .warning: return Color.adaptive(light: 0x946000, lightAlpha: 0.25, dark: 0x000000, darkAlpha: 0.0)
        case .danger: return Color.adaptive(light: 0xCC1F33, lightAlpha: 0.24, dark: 0x000000, darkAlpha: 0.0)
        case .scheduled: return Color.adaptive(light: 0x1D5BB8, lightAlpha: 0.24, dark: 0x000000, darkAlpha: 0.0)
        }
    }
}

// MARK: - NoriPill

public struct NoriPill: View {
    public let text: String
    public let tone: NoriTone
    public let systemImage: String?

    public init(_ text: String, tone: NoriTone = .neutral, systemImage: String? = nil) {
        self.text = text
        self.tone = tone
        self.systemImage = systemImage
    }

    public var body: some View {
        HStack(spacing: 4) {
            if let systemImage {
                Image(systemName: systemImage)
                    .font(.system(size: 10.5, weight: .semibold))
            }
            Text(text)
                .font(.system(size: 11.5, weight: .semibold))
                .lineLimit(1)
        }
        .foregroundStyle(tone.foreground)
        .padding(.horizontal, 8.5)
        .frame(height: 23)
        .background(tone.background, in: Capsule())
    }
}

// MARK: - NoriBadge

public struct NoriBadge: View {
    public let count: Int
    public let tone: NoriTone

    public init(count: Int, tone: NoriTone = .accent) {
        self.count = count
        self.tone = tone
    }

    public var body: some View {
        if count > 0 {
            let usesSolidAccent = tone == .accent || tone == .working
            let usesSolidDanger = tone == .danger
            let bg: Color = {
                if usesSolidAccent { return NoriColors.brandPrimary }
                if usesSolidDanger { return Color.adaptive(light: 0xCC1F33, dark: 0xD9384D) }
                return tone.background
            }()
            Text(count > 99 ? "99+" : "\(count)")
                .font(.system(size: 11, weight: .bold))
                .foregroundStyle(usesSolidAccent || usesSolidDanger ? NoriColors.onAccent : tone.foreground)
                .padding(.horizontal, 6)
                .frame(minWidth: 19, minHeight: 19)
                .background(bg, in: Capsule())
        }
    }
}

// MARK: - NoriCard

public struct NoriCard<Content: View>: View {
    public let padding: CGFloat
    public let backgroundColor: Color
    public let borderColor: Color
    private let content: Content

    public init(
        padding: CGFloat = 14,
        backgroundColor: Color = NoriColors.surface,
        borderColor: Color = NoriColors.cardBorder,
        @ViewBuilder content: () -> Content
    ) {
        self.padding = padding
        self.backgroundColor = backgroundColor
        self.borderColor = borderColor
        self.content = content()
    }

    public var body: some View {
        content
            .padding(padding)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(backgroundColor, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                    .strokeBorder(borderColor, lineWidth: 1)
            )
    }
}

// MARK: - NoriRows & NoriRow

public struct NoriRows<Content: View>: View {
    private let content: Content

    public init(@ViewBuilder content: () -> Content) {
        self.content = content()
    }

    public var body: some View {
        VStack(spacing: 0) {
            content
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
    }
}

public struct NoriRow<Trailing: View>: View {
    public let title: String
    public let detail: String?
    public let systemImage: String?
    public let iconTone: NoriTone
    public let showChevron: Bool
    public let showDivider: Bool
    public let wrapTitle: Bool
    private let trailing: Trailing

    public init(
        title: String,
        detail: String? = nil,
        systemImage: String? = nil,
        iconTone: NoriTone = .neutral,
        showChevron: Bool = false,
        showDivider: Bool = true,
        wrapTitle: Bool = false,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self.title = title
        self.detail = detail
        self.systemImage = systemImage
        self.iconTone = iconTone
        self.showChevron = showChevron
        self.showDivider = showDivider
        self.wrapTitle = wrapTitle
        self.trailing = trailing()
    }

    public var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 11) {
                if let systemImage {
                    Image(systemName: systemImage)
                        .font(.system(size: 16, weight: .medium))
                        .foregroundStyle(iconTone.foreground)
                        .frame(width: 36, height: 36)
                        .background(iconTone.background, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                }

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
                    .padding(.leading, systemImage == nil ? 13 : 60)
            }
        }
    }
}

public extension NoriRow where Trailing == EmptyView {
    init(
        title: String,
        detail: String? = nil,
        systemImage: String? = nil,
        iconTone: NoriTone = .neutral,
        showChevron: Bool = false,
        showDivider: Bool = true,
        wrapTitle: Bool = false
    ) {
        self.init(
            title: title,
            detail: detail,
            systemImage: systemImage,
            iconTone: iconTone,
            showChevron: showChevron,
            showDivider: showDivider,
            wrapTitle: wrapTitle,
            trailing: { EmptyView() }
        )
    }
}

// MARK: - NoriSectionHeader

public struct NoriSectionHeader<Trailing: View>: View {
    public let title: String
    private let trailing: Trailing

    public init(_ title: String, @ViewBuilder trailing: () -> Trailing) {
        self.title = title
        self.trailing = trailing()
    }

    public var body: some View {
        HStack(alignment: .center) {
            Text(title)
                .font(.system(size: 13, weight: .bold))
                .foregroundStyle(NoriColors.ink2)
            Spacer(minLength: 8)
            trailing
        }
        .padding(.top, 14)
        .padding(.bottom, 6)
    }
}

public extension NoriSectionHeader where Trailing == EmptyView {
    init(_ title: String) {
        self.init(title, trailing: { EmptyView() })
    }
}

// MARK: - NoriSegmentedTabs

public struct NoriSegmentOption<Value: Hashable & Sendable>: Identifiable, Sendable {
    public var id: Value { value }
    public let value: Value
    public let label: String
    public let count: Int?

    public init(value: Value, label: String, count: Int? = nil) {
        self.value = value
        self.label = label
        self.count = count
    }
}

public struct NoriSegmentedTabs<Value: Hashable & Sendable>: View {
    public let options: [NoriSegmentOption<Value>]
    @Binding public var selection: Value

    public init(options: [NoriSegmentOption<Value>], selection: Binding<Value>) {
        self.options = options
        self._selection = selection
    }

    public var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ForEach(options) { option in
                    let isSelected = option.value == selection
                    Button {
                        withAnimation(.easeInOut(duration: 0.16)) {
                            selection = option.value
                        }
                    } label: {
                        HStack(spacing: 5) {
                            Text(option.label)
                                .font(.system(size: 13, weight: .semibold))
                            if let count = option.count, count > 0 {
                                Text("\(count)")
                                    .font(.system(size: 11, weight: .bold))
                                    .foregroundStyle(isSelected ? NoriColors.accent : NoriColors.ink3)
                            }
                        }
                        .foregroundStyle(isSelected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                        .padding(.horizontal, 12)
                        .frame(height: 34)
                        .background(isSelected ? NoriColors.chipSelectedBg : NoriColors.elevated, in: Capsule())
                        .overlay(
                            Capsule()
                                .strokeBorder(isSelected ? NoriColors.chipSelectedBorder : NoriColors.hairline, lineWidth: 1)
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.vertical, 2)
        }
    }
}

// MARK: - NoriButtonStyle

public enum NoriButtonKind: Sendable {
    case primary
    case neutral
    case tonal
    case quiet
    case danger
}

public struct NoriButtonStyle: ButtonStyle {
    public let kind: NoriButtonKind
    public let fullWidth: Bool
    public let compact: Bool

    public init(_ kind: NoriButtonKind = .neutral, fullWidth: Bool = false, compact: Bool = false) {
        self.kind = kind
        self.fullWidth = fullWidth
        self.compact = compact
    }

    public func makeBody(configuration: Configuration) -> some View {
        StyledButtonBody(
            configuration: configuration,
            kind: kind,
            fullWidth: fullWidth,
            compact: compact
        )
    }

    private struct StyledButtonBody: View {
        @Environment(\.isEnabled) private var isEnabled
        let configuration: Configuration
        let kind: NoriButtonKind
        let fullWidth: Bool
        let compact: Bool

        var body: some View {
            let bg: Color
            let fg: Color
            let border: Color

            if !isEnabled {
                switch kind {
                case .primary, .tonal, .quiet:
                    bg = NoriColors.disabledBg
                    fg = NoriColors.disabled
                    border = NoriColors.cardBorder
                case .neutral:
                    bg = NoriColors.surface
                    fg = NoriColors.disabled
                    border = NoriColors.hairline
                case .danger:
                    bg = NoriColors.disabledBg
                    fg = NoriColors.disabled
                    border = NoriColors.cardBorder
                }
            } else {
                switch kind {
                case .primary:
                    bg = configuration.isPressed ? NoriColors.accentHover : NoriColors.brandPrimary
                    fg = NoriColors.onAccent
                    border = Color.clear
                case .neutral:
                    bg = configuration.isPressed ? NoriColors.elevated : NoriColors.surface
                    fg = NoriColors.ink
                    border = NoriColors.hairlineStrong
                case .tonal:
                    bg = configuration.isPressed ? NoriColors.accentSubtle : NoriColors.selected
                    fg = NoriColors.accent
                    border = NoriColors.accentBorder
                case .quiet:
                    bg = configuration.isPressed ? NoriColors.selected : NoriColors.elevated
                    fg = NoriColors.ink
                    border = Color.clear
                case .danger:
                    bg = configuration.isPressed ? NoriColors.danger.opacity(0.22) : NoriColors.dangerBg
                    fg = NoriColors.danger
                    border = NoriColors.danger.opacity(0.34)
                }
            }

            return configuration.label
                .font(.system(size: compact ? 12.5 : 13.5, weight: .semibold))
                .foregroundStyle(fg)
                .padding(.horizontal, compact ? 12 : 15)
                .frame(maxWidth: fullWidth ? .infinity : nil)
                .frame(height: compact ? 32 : 38)
                .background(bg, in: Capsule())
                .overlay(
                    Capsule()
                        .strokeBorder(border, lineWidth: border == .clear ? 0 : 1)
                )
                .scaleEffect(isEnabled && configuration.isPressed ? 0.97 : 1.0)
                .animation(.easeOut(duration: 0.12), value: configuration.isPressed)
        }
    }
}

public extension ButtonStyle where Self == NoriButtonStyle {
    static var noriPrimary: NoriButtonStyle { NoriButtonStyle(.primary) }
    static var noriNeutral: NoriButtonStyle { NoriButtonStyle(.neutral) }
    static var noriTonal: NoriButtonStyle { NoriButtonStyle(.tonal) }
    static var noriQuiet: NoriButtonStyle { NoriButtonStyle(.quiet) }
    static var noriDanger: NoriButtonStyle { NoriButtonStyle(.danger) }
}

// MARK: - NoriLine

/// Calm Nori commentary line with small onigiri mark and secondary text.
public struct NoriLine: View {
    public let text: String
    public let markState: MarkState

    public init(_ text: String, state: MarkState = .idle) {
        self.text = text
        self.markState = state
    }

    public var body: some View {
        HStack(alignment: .center, spacing: 8) {
            NoriMark(size: 18, state: markState)
            Text(text)
                .font(.system(size: 13.5))
                .foregroundStyle(NoriColors.ink2)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

// MARK: - NoriToast

public struct NoriToast: View {
    public let message: String

    public init(_ message: String) {
        self.message = message
    }

    public var body: some View {
        Text(message)
            .font(.system(size: 13, weight: .semibold))
            .foregroundStyle(NoriColors.toastFg)
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            .background(NoriColors.toastBg, in: Capsule())
            .overlay(
                Capsule()
                    .strokeBorder(NoriColors.hairline, lineWidth: 1)
            )
            .shadow(color: NoriColors.shadowColor, radius: 14, x: 0, y: 6)
    }
}

// MARK: - NoriEmptyState

public struct NoriEmptyState<Action: View>: View {
    public let title: String
    public let message: String
    public let markState: MarkState
    private let action: Action

    public init(
        title: String,
        message: String,
        markState: MarkState = .idle,
        @ViewBuilder action: () -> Action
    ) {
        self.title = title
        self.message = message
        self.markState = markState
        self.action = action()
    }

    public var body: some View {
        VStack(spacing: 10) {
            NoriMark(size: 44, state: markState)
            Text(title)
                .font(NoriTypography.section)
                .foregroundStyle(NoriColors.ink)
            Text(message)
                .font(NoriTypography.meta)
                .foregroundStyle(NoriColors.ink2)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 16)
            action
                .padding(.top, 4)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 32)
        .padding(.horizontal, 16)
    }
}

public extension NoriEmptyState where Action == EmptyView {
    init(title: String, message: String, markState: MarkState = .idle) {
        self.init(title: title, message: message, markState: markState, action: { EmptyView() })
    }
}

// MARK: - NoriNotice

public struct NoriNotice<Trailing: View>: View {
    public let title: String
    public let detail: String?
    public let tone: NoriTone
    public let systemImage: String?
    private let trailing: Trailing

    public init(
        title: String,
        detail: String? = nil,
        tone: NoriTone = .warning,
        systemImage: String? = "info.circle.fill",
        @ViewBuilder trailing: () -> Trailing
    ) {
        self.title = title
        self.detail = detail
        self.tone = tone
        self.systemImage = systemImage
        self.trailing = trailing()
    }

    public var body: some View {
        HStack(alignment: .center, spacing: 10) {
            if let systemImage {
                Image(systemName: systemImage)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(tone.foreground)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.system(size: 13.5, weight: .semibold))
                    .foregroundStyle(tone.foreground)
                if let detail, !detail.isEmpty {
                    Text(detail)
                        .font(.system(size: 12.5))
                        .foregroundStyle(NoriColors.ink2)
                }
            }
            Spacer(minLength: 8)
            trailing
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(tone.background, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                .strokeBorder(tone.border, lineWidth: 1)
        )
    }
}

public extension NoriNotice where Trailing == EmptyView {
    init(title: String, detail: String? = nil, tone: NoriTone = .warning, systemImage: String? = "info.circle.fill") {
        self.init(title: title, detail: detail, tone: tone, systemImage: systemImage, trailing: { EmptyView() })
    }
}

// MARK: - NoriUnderlineTabs

public struct NoriUnderlineTabs<Value: Hashable & Sendable>: View {
    public let options: [NoriSegmentOption<Value>]
    @Binding public var selection: Value
    @Namespace private var underlineNamespace

    public init(options: [NoriSegmentOption<Value>], selection: Binding<Value>) {
        self.options = options
        self._selection = selection
    }

    public var body: some View {
        HStack(spacing: 0) {
            ForEach(options) { option in
                let isSelected = option.value == selection
                Button {
                    withAnimation(.spring(response: 0.26, dampingFraction: 0.86)) {
                        selection = option.value
                    }
                } label: {
                    VStack(spacing: 0) {
                        HStack(spacing: 4) {
                            Text(option.label)
                                .font(.system(size: 13.5, weight: isSelected ? .semibold : .medium))
                                .foregroundStyle(isSelected ? NoriColors.ink : NoriColors.ink2)
                                .lineLimit(1)
                                .minimumScaleFactor(0.85)

                            if let count = option.count, count > 0 {
                                Text(count > 99 ? "99+" : "\(count)")
                                    .font(.system(size: 11, weight: .semibold))
                                    .monospacedDigit()
                                    .foregroundStyle(isSelected ? NoriColors.accent : NoriColors.ink2)
                                    .padding(.horizontal, 5.5)
                                    .frame(minWidth: 18, minHeight: 18)
                                    .background(isSelected ? NoriColors.selected : NoriColors.elevated, in: Capsule())
                                    .lineLimit(1)
                                    .fixedSize()
                            }
                        }
                        .padding(.horizontal, 4)
                        .padding(.top, 10)
                        .padding(.bottom, 9)

                        ZStack {
                            Color.clear
                                .frame(height: 2.5)
                            if isSelected {
                                RoundedRectangle(cornerRadius: 1.25, style: .continuous)
                                    .fill(NoriColors.accent)
                                    .frame(height: 2.5)
                                    .matchedGeometryEffect(id: "noriUnderlineTabIndicator", in: underlineNamespace)
                            }
                        }
                    }
                    .frame(maxWidth: .infinity)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(isSelected ? .isSelected : [])
            }
        }
        .background(alignment: .bottom) {
            Rectangle()
                .fill(NoriColors.hairline)
                .frame(height: 1)
        }
    }
}

// MARK: - NoriHeroCard

public struct NoriHeroCard<Content: View>: View {
    public let padding: CGFloat
    private let content: Content

    public init(padding: CGFloat = 16, @ViewBuilder content: () -> Content) {
        self.padding = padding
        self.content = content()
    }

    public var body: some View {
        let shape = RoundedRectangle(cornerRadius: 20, style: .continuous)
        content
            .padding(padding)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                LinearGradient(
                    colors: [NoriColors.heroStart, NoriColors.heroEnd],
                    startPoint: .topLeading,
                    endPoint: .bottomTrailing
                ),
                in: shape
            )
            .overlay(
                shape.strokeBorder(NoriColors.cardBorder, lineWidth: 1)
            )
            .shadow(color: NoriColors.accentShadow, radius: 14, x: 0, y: 6)
    }
}

// MARK: - NoriTopWash

public struct NoriTopWash: View {
    public init() {}

    public var body: some View {
        LinearGradient(
            colors: [NoriColors.topWashStart, NoriColors.topWashEnd],
            startPoint: .top,
            endPoint: .bottom
        )
    }
}
