import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

private enum OnboardingMood {
    case hello
    case hop
    case nod
    case shake
    case celebrate
}

public struct OnboardingView: View {
    @Bindable private var store: NoriStore
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    @State private var step: Int = 1

    // Mascot animation state
    @State private var markOffsetY: CGFloat = 0
    @State private var markOffsetX: CGFloat = 0
    @State private var markRotation: Double = 0
    @State private var markScale: CGFloat = 1
    @State private var isHappy = false
    @State private var showSparkles = false
    @State private var happyTask: Task<Void, Never>?

    // Step 3 (API Key)
    @State private var keyInput = ""
    @State private var isReplacingKey = false
    @State private var isValidatingKey = false
    @State private var keyError: String?
    @FocusState private var isKeyFocused: Bool

    // Step 4 (About you)
    @State private var name = ""
    @State private var replyStyle = "Concise & direct"
    @State private var selectedFocus: Set<String> = ["Engineering", "Daily planning"]
    @State private var workingOn = ""
    @State private var isFinishing = false

    private let replyStyles = ["Concise & direct", "Warm & conversational", "Bullets first"]
    private let focusOptions = ["Engineering", "Daily planning", "Email & calendar", "Research", "Writing"]

    public init(store: NoriStore) {
        self.store = store
    }

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    private var primaryProvider: ModelKeyProvider? {
        store.modelKeys.providers.first
    }

    private var byokRequired: Bool {
        store.modelKeys.byokRequired
    }

    private var keyConfigured: Bool {
        primaryProvider?.configured == true && !isReplacingKey
    }

    private var canContinueStep3: Bool {
        !byokRequired || keyConfigured
    }

    private var step3MarkState: MarkState {
        if isValidatingKey { return .thinking }
        if !keyConfigured && isKeyFocused { return .attentive }
        return .idle
    }

    public var body: some View {
        VStack(spacing: 0) {
            headerBar

            ScrollView {
                VStack(spacing: 0) {
                    mascotStage

                    Group {
                        switch step {
                        case 1:
                            step1View
                        case 2:
                            step2View
                        case 3:
                            step3View
                        default:
                            step4View
                        }
                    }
                    .transition(.opacity.combined(with: .move(edge: .bottom)))
                }
                .padding(.horizontal, 20)
                .padding(.top, 12)
                .padding(.bottom, 32)
                .frame(maxWidth: 480)
                .frame(maxWidth: .infinity)
            }
            .scrollDismissesKeyboard(.interactively)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .onAppear {
            triggerMood(.hello, sparkles: true)
        }
        .onDisappear {
            happyTask?.cancel()
        }
    }

    // MARK: - Header

    private var headerBar: some View {
        HStack {
            Button("Back") {
                if step > 1 {
                    goToStep(step - 1)
                }
            }
            .font(NoriTypography.metaSemibold)
            .foregroundStyle(NoriColors.ink2)
            .opacity(step > 1 ? 1 : 0)
            .disabled(step <= 1)

            Spacer()

            HStack(spacing: 6) {
                ForEach(1...4, id: \.self) { idx in
                    Capsule()
                        .fill(
                            idx == step
                                ? NoriColors.brandPrimary
                                : idx < step
                                    ? NoriColors.accent.opacity(0.35)
                                    : NoriColors.elevated
                        )
                        .frame(width: idx == step ? 30 : 16, height: 5)
                        .animation(.spring(response: 0.28, dampingFraction: 0.85), value: step)
                }
            }

            Spacer()

            Button(step == 3 ? "Set up later" : "Skip") {
                if step < 4 {
                    goToStep(step + 1)
                } else {
                    finish(saveAboutYou: false)
                }
            }
            .font(NoriTypography.metaSemibold)
            .foregroundStyle(NoriColors.ink2)
            .disabled(isFinishing)
        }
        .padding(.horizontal, 20)
        .padding(.top, 12)
        .padding(.bottom, 8)
    }

    // MARK: - Mascot Stage

    private var mascotStage: some View {
        Button {
            triggerHaptic(.light)
            triggerMood(.hop, happy: true, sparkles: step == 1)
        } label: {
            ZStack {
                if showSparkles && !reduceMotion {
                    OnboardingSparkles()
                        .transition(.opacity)
                }
                NoriMark(
                    size: step == 1 ? 76 : 72,
                    state: step == 3 ? step3MarkState : .idle,
                    happy: isHappy
                )
                .scaleEffect(markScale)
                .rotationEffect(.degrees(markRotation))
                .offset(x: markOffsetX, y: markOffsetY)
            }
            .frame(height: 86)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Nori")
        .padding(.bottom, 12)
    }

    // MARK: - Step 1: Hi, I'm Nori

    private var step1View: some View {
        VStack(spacing: 0) {
            Text("Hi, I’m Nori")
                .font(.system(size: 28, weight: .bold))
                .foregroundStyle(NoriColors.ink)
                .multilineTextAlignment(.center)

            VStack(spacing: 10) {
                pillarRow(
                    icon: "bubble.left.fill",
                    iconFg: NoriColors.accent,
                    iconBg: NoriColors.accentSubtle,
                    title: "Chat through ideas, drafts, and questions"
                )
                pillarRow(
                    icon: "checkmark.square.fill",
                    iconFg: NoriTone.scheduled.foreground,
                    iconBg: NoriTone.scheduled.background,
                    title: "Hand off tasks and approve before anything sends"
                )
                pillarRow(
                    icon: "lightbulb.fill",
                    iconFg: NoriTone.ok.foreground,
                    iconBg: NoriTone.ok.background,
                    title: "Remembers your preferences and context"
                )
            }
            .padding(.top, 24)

            Button("Get started") {
                triggerHaptic(.light)
                goToStep(2)
            }
            .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
            .padding(.top, 24)
        }
    }

    private func pillarRow(icon: String, iconFg: Color, iconBg: Color, title: String) -> some View {
        NoriCard(padding: 14) {
            HStack(spacing: 14) {
                Image(systemName: icon)
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(iconFg)
                    .frame(width: 36, height: 36)
                    .background(iconBg, in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Text(title)
                    .font(NoriTypography.rowTitle)
                    .foregroundStyle(NoriColors.ink)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    // MARK: - Step 2: Appearance

    private var step2View: some View {
        VStack(spacing: 0) {
            Text("Appearance")
                .font(.system(size: 28, weight: .bold))
                .foregroundStyle(NoriColors.ink)
                .multilineTextAlignment(.center)

            HStack(spacing: 10) {
                ForEach(NoriAppearance.allCases) { option in
                    let selected = appearance == option
                    Button {
                        appearanceRawValue = option.rawValue
                        triggerHaptic(.light)
                        triggerMood(.hop, happy: true, duration: 0.6)
                    } label: {
                        VStack(spacing: 10) {
                            appearanceSwatch(option)
                            Text(option.displayLabel)
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity)
                        .background(
                            selected ? NoriColors.selected : NoriColors.surface,
                            in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                                .strokeBorder(
                                    selected ? NoriColors.accentSignal : NoriColors.cardBorder,
                                    lineWidth: selected ? 1.5 : 1
                                )
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.top, 24)

            // Live mini chat preview
            NoriCard(padding: 14) {
                VStack(alignment: .leading, spacing: 10) {
                    HStack {
                        Spacer(minLength: 32)
                        Text("Summarize my calendar for tomorrow")
                            .font(NoriTypography.meta)
                            .foregroundStyle(NoriColors.onAccent)
                            .padding(.horizontal, 13)
                            .padding(.vertical, 8)
                            .background(
                                NoriColors.brandPrimary,
                                in: RoundedRectangle(cornerRadius: 18, style: .continuous)
                            )
                    }

                    HStack(alignment: .top, spacing: 10) {
                        NoriMark(size: 24, state: .idle)
                            .padding(.top, 1)
                        VStack(alignment: .leading, spacing: 6) {
                            Text("You have 3 meetings starting at 10:00 AM, with an open block from 1:00–3:30 PM.")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink)
                            NoriPill("Ready", tone: .ok)
                        }
                    }
                }
            }
            .padding(.top, 14)

            Button("Continue") {
                triggerHaptic(.light)
                goToStep(3)
            }
            .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
            .padding(.top, 24)
        }
    }

    private func appearanceSwatch(_ mode: NoriAppearance) -> some View {
        ZStack {
            switch mode {
            case .light:
                Color.white
            case .dark:
                Color.black
            case .system:
                LinearGradient(
                    stops: [
                        .init(color: .white, location: 0.495),
                        .init(color: .black, location: 0.505)
                    ],
                    startPoint: .topLeading,
                    endPoint: .bottomTrailing
                )
            }

            VStack(alignment: .leading, spacing: 0) {
                HStack {
                    Spacer()
                    Capsule()
                        .fill(mode == .light ? Color(hex: 0x5000FF) : Color(hex: 0x7A3DF6))
                        .frame(width: 28, height: 7)
                }
                Spacer()
                Capsule()
                    .fill(
                        mode == .light
                            ? Color(hex: 0xE7E2EF)
                            : mode == .dark
                                ? Color(hex: 0x261F38)
                                : Color(hex: 0xA98AFF).opacity(0.6)
                    )
                    .frame(width: 42, height: 6)
            }
            .padding(7)
        }
        .frame(height: 52)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
        )
    }

    // MARK: - Step 3: Add your API key

    private var step3View: some View {
        VStack(spacing: 0) {
            Text("Add your API key")
                .font(.system(size: 28, weight: .bold))
                .foregroundStyle(NoriColors.ink)
                .multilineTextAlignment(.center)

            NoriCard(padding: 16) {
                VStack(alignment: .leading, spacing: 14) {
                    HStack(spacing: 10) {
                        Image(systemName: "key.fill")
                            .font(.system(size: 15, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                            .frame(width: 36, height: 36)
                            .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                        Text(primaryProvider?.label ?? "Vercel AI Gateway")
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(NoriColors.ink)

                        Spacer()

                        NoriPill(
                            isValidatingKey ? "Validating" : keyConfigured ? "Configured" : byokRequired ? "Required" : "Optional",
                            tone: isValidatingKey ? .working : keyConfigured ? .ok : byokRequired ? .warning : .neutral
                        )
                    }

                    if !keyConfigured {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("API key")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink2)

                            HStack(spacing: 8) {
                                SecureField("Paste API key", text: $keyInput)
                                    .textFieldStyle(.plain)
                                    .font(NoriTypography.mono)
                                    .foregroundStyle(NoriColors.ink)
                                    .tint(NoriColors.accent)
                                    .autocorrectionDisabled()
                                    #if os(iOS)
                                    .textInputAutocapitalization(.never)
                                    #endif
                                    .focused($isKeyFocused)
                                    .disabled(isValidatingKey)
                                    .onSubmit {
                                        Task { await verifyKey() }
                                    }

                                Button(isValidatingKey ? "Checking…" : "Verify") {
                                    Task { await verifyKey() }
                                }
                                .font(.system(size: 12.5, weight: .semibold))
                                .foregroundStyle(NoriColors.accent)
                                .padding(.horizontal, 10)
                                .padding(.vertical, 6)
                                .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                                .disabled(isValidatingKey)
                            }
                            .padding(.horizontal, 12)
                            .frame(height: 44)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: 12, style: .continuous)
                                    .strokeBorder(
                                        isKeyFocused ? NoriColors.accentSignal : NoriColors.cardBorder,
                                        lineWidth: isKeyFocused ? 1.5 : 1
                                    )
                            )
                        }
                    } else {
                        HStack {
                            Text(verifiedKeyLabel)
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriTone.ok.foreground)
                            Spacer()
                            Button("Replace") {
                                isReplacingKey = true
                                keyError = nil
                            }
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(NoriTone.ok.foreground)
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 10)
                        .background(NoriTone.ok.background, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    }

                    if let keyError {
                        NoriNotice(title: keyError, tone: .danger)
                    }
                }
            }
            .padding(.top, 24)

            NoriNotice(
                title: "Encrypted at rest and never displayed again after saving.",
                tone: .neutral,
                systemImage: "lock.fill"
            )
            .padding(.top, 12)

            Button("Continue") {
                triggerHaptic(.light)
                goToStep(4)
            }
            .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
            .disabled(!canContinueStep3 || isValidatingKey)
            .padding(.top, 24)
        }
    }

    private var verifiedKeyLabel: String {
        if let last4 = primaryProvider?.last4, !last4.isEmpty {
            return "Key verified · ••••\(last4)"
        }
        return "Key verified"
    }

    // MARK: - Step 4: About you

    private var step4View: some View {
        VStack(spacing: 0) {
            Text("About you")
                .font(.system(size: 28, weight: .bold))
                .foregroundStyle(NoriColors.ink)
                .multilineTextAlignment(.center)

            NoriCard(padding: 16) {
                VStack(alignment: .leading, spacing: 16) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Name")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink2)

                        TextField("Your name", text: $name)
                            .textFieldStyle(.plain)
                            .font(NoriTypography.body)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(.horizontal, 12)
                            .frame(height: 44)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: 12, style: .continuous)
                                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                            )
                    }

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Reply style")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink2)

                        OnboardingChipFlow(spacing: 8) {
                            ForEach(replyStyles, id: \.self) { option in
                                chipButton(option, selected: replyStyle == option) {
                                    replyStyle = option
                                    triggerHaptic(.light)
                                    triggerMood(.nod)
                                }
                            }
                        }
                    }

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Focus")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink2)

                        OnboardingChipFlow(spacing: 8) {
                            ForEach(focusOptions, id: \.self) { option in
                                chipButton(option, selected: selectedFocus.contains(option)) {
                                    if selectedFocus.contains(option) {
                                        selectedFocus.remove(option)
                                    } else {
                                        selectedFocus.insert(option)
                                    }
                                    triggerHaptic(.light)
                                    triggerMood(.nod)
                                }
                            }
                        }
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("What are you working on?")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink2)

                        TextField("e.g. Building a mobile app", text: $workingOn)
                            .textFieldStyle(.plain)
                            .font(NoriTypography.body)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(.horizontal, 12)
                            .frame(height: 44)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: 12, style: .continuous)
                                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                            )
                    }
                }
            }
            .padding(.top, 20)

            Button(isFinishing ? "Starting…" : "Start chatting") {
                finish(saveAboutYou: true)
            }
            .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
            .disabled(isFinishing)
            .padding(.top, 24)
        }
    }

    private func chipButton(_ label: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(label)
                .font(.system(size: 13.5, weight: selected ? .semibold : .medium))
                .foregroundStyle(selected ? NoriColors.accent : NoriColors.ink2)
                .padding(.horizontal, 13)
                .padding(.vertical, 8)
                .background(
                    selected ? NoriColors.selected : NoriColors.elevated,
                    in: Capsule()
                )
                .overlay(
                    Capsule()
                        .strokeBorder(
                            selected ? NoriColors.accentBorder : NoriColors.cardBorder,
                            lineWidth: 1
                        )
                )
        }
        .buttonStyle(.plain)
    }

    // MARK: - Actions & Animations

    private func goToStep(_ next: Int) {
        withAnimation(.easeOut(duration: 0.22)) {
            step = next
        }
        if next == 1 {
            triggerMood(.hello, sparkles: true)
        } else if next == 2 || next == 4 {
            triggerMood(.nod)
        }
    }

    private func verifyKey() async {
        let trimmed = keyInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            keyError = "Enter an API key."
            triggerMood(.shake)
            return
        }
        let providerID = primaryProvider?.provider ?? "vercel_ai_gateway"
        keyError = nil
        isValidatingKey = true
        defer { isValidatingKey = false }
        do {
            _ = try await store.saveModelKey(provider: providerID, apiKey: trimmed)
            keyInput = ""
            isReplacingKey = false
            isKeyFocused = false
            triggerHaptic(.success)
            triggerMood(.hop, happy: true, sparkles: true, duration: 0.85)
        } catch {
            keyError = error.localizedDescription
            triggerMood(.shake)
        }
    }

    private func finish(saveAboutYou: Bool) {
        guard !isFinishing else { return }
        isFinishing = true
        triggerHaptic(.success)
        triggerMood(.celebrate, happy: true, sparkles: true, duration: 0.85)
        let orderedFocus = focusOptions.filter { selectedFocus.contains($0) }
        Task {
            await store.completeOnboarding(
                name: name,
                replyStyle: replyStyle,
                focus: orderedFocus,
                workingOn: workingOn,
                saveAboutYou: saveAboutYou
            )
            isFinishing = false
        }
    }

    private func triggerMood(
        _ mood: OnboardingMood,
        happy: Bool = false,
        sparkles: Bool = false,
        duration: Double = 0.7
    ) {
        if happy {
            happyTask?.cancel()
            isHappy = true
            happyTask = Task {
                try? await Task.sleep(for: .seconds(duration))
                guard !Task.isCancelled else { return }
                isHappy = false
            }
        }
        if sparkles && !reduceMotion {
            showSparkles = false
            withAnimation(.easeOut(duration: 0.2)) {
                showSparkles = true
            }
            Task {
                try? await Task.sleep(for: .milliseconds(720))
                withAnimation(.easeOut(duration: 0.15)) {
                    showSparkles = false
                }
            }
        }
        guard !reduceMotion else { return }

        Task {
            switch mood {
            case .hello:
                withAnimation(.spring(response: 0.34, dampingFraction: 0.62)) {
                    markOffsetY = -6
                    markRotation = -6
                    markScale = 1.03
                }
                try? await Task.sleep(for: .milliseconds(260))
                withAnimation(.spring(response: 0.32, dampingFraction: 0.68)) {
                    markOffsetY = 0
                    markRotation = 4
                    markScale = 1.0
                }
                try? await Task.sleep(for: .milliseconds(220))
                withAnimation(.spring(response: 0.28, dampingFraction: 0.8)) {
                    markRotation = 0
                }
            case .hop:
                withAnimation(.spring(response: 0.24, dampingFraction: 0.6)) {
                    markOffsetY = -9
                    markRotation = -3
                    markScale = 1.04
                }
                try? await Task.sleep(for: .milliseconds(210))
                withAnimation(.spring(response: 0.28, dampingFraction: 0.72)) {
                    markOffsetY = 0
                    markRotation = 0
                    markScale = 1.0
                }
            case .nod:
                withAnimation(.easeOut(duration: 0.15)) {
                    markOffsetY = 3
                    markRotation = 3
                }
                try? await Task.sleep(for: .milliseconds(150))
                withAnimation(.spring(response: 0.26, dampingFraction: 0.78)) {
                    markOffsetY = 0
                    markRotation = 0
                }
            case .shake:
                for x in [-6.0, 6.0, -4.0, 0.0] {
                    withAnimation(.easeInOut(duration: 0.08)) {
                        markOffsetX = x
                        markRotation = x * 0.6
                    }
                    try? await Task.sleep(for: .milliseconds(85))
                }
            case .celebrate:
                withAnimation(.spring(response: 0.26, dampingFraction: 0.55)) {
                    markOffsetY = -12
                    markRotation = -5
                    markScale = 1.06
                }
                try? await Task.sleep(for: .milliseconds(250))
                withAnimation(.spring(response: 0.3, dampingFraction: 0.7)) {
                    markOffsetY = 0
                    markRotation = 0
                    markScale = 1.0
                }
            }
        }
    }

    private enum HapticKind { case light, success }

    private func triggerHaptic(_ kind: HapticKind) {
        #if canImport(UIKit)
        switch kind {
        case .light:
            UIImpactFeedbackGenerator(style: .light).impactOccurred()
        case .success:
            UINotificationFeedbackGenerator().notificationOccurred(.success)
        }
        #endif
    }
}

// MARK: - Minimal Sparkles

private struct OnboardingSparkles: View {
    @State private var expanded = false

    var body: some View {
        ZStack {
            Circle()
                .fill(NoriColors.accent)
                .frame(width: 6, height: 6)
                .offset(x: expanded ? -46 : -24, y: expanded ? -28 : -14)
                .opacity(expanded ? 0 : 1)
            Circle()
                .fill(NoriTone.ok.foreground)
                .frame(width: 6, height: 6)
                .offset(x: expanded ? 46 : 24, y: expanded ? -26 : -12)
                .opacity(expanded ? 0 : 1)
            Circle()
                .fill(NoriColors.brandPrimary)
                .frame(width: 6, height: 6)
                .offset(x: expanded ? -48 : -26, y: expanded ? 12 : 6)
                .opacity(expanded ? 0 : 1)
            Circle()
                .fill(NoriColors.accent)
                .frame(width: 6, height: 6)
                .offset(x: expanded ? 48 : 26, y: expanded ? 14 : 6)
                .opacity(expanded ? 0 : 1)
        }
        .allowsHitTesting(false)
        .onAppear {
            withAnimation(.easeOut(duration: 0.65)) {
                expanded = true
            }
        }
    }
}

// MARK: - Wrapping Chip Layout

private struct OnboardingChipFlow: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0

        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x + size.width > maxWidth, x > 0 {
                x = 0
                y += rowHeight + spacing
                rowHeight = 0
            }
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
        return CGSize(width: maxWidth.isFinite ? maxWidth : x, height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX
        var y = bounds.minY
        var rowHeight: CGFloat = 0

        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x + size.width > bounds.maxX, x > bounds.minX {
                x = bounds.minX
                y += rowHeight + spacing
                rowHeight = 0
            }
            subview.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
    }
}
