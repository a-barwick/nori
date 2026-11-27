import SwiftUI
import NoriCore
import NoriDesign

struct OnboardingWelcomeStep: View {
    let onContinue: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            Text("Hi, I’m Nori")
                .font(.title.bold())
                .foregroundStyle(NoriColors.ink)
                .multilineTextAlignment(.center)
                .accessibilityAddTraits(.isHeader)

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

            Button("Get started", action: onContinue)
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
}

struct OnboardingAppearanceStep: View {
    @Binding var appearanceRawValue: String
    let appearance: NoriAppearance
    let onSelectAppearance: () -> Void
    let onContinue: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            Text("Appearance")
                .font(.title.bold())
                .foregroundStyle(NoriColors.ink)
                .multilineTextAlignment(.center)
                .accessibilityAddTraits(.isHeader)

            HStack(spacing: 10) {
                ForEach(NoriAppearance.allCases) { option in
                    let selected = appearance == option
                    Button {
                        appearanceRawValue = option.rawValue
                        onSelectAppearance()
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
                    .accessibilityAddTraits(selected ? .isSelected : [])
                }
            }
            .padding(.top, 24)

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

            Button("Continue", action: onContinue)
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .padding(.top, 24)
        }
    }

    private func appearanceSwatch(_ mode: NoriAppearance) -> some View {
        ZStack {
            switch mode {
            case .light:
                NoriColors.Swatch.lightBg
            case .dark:
                NoriColors.Swatch.darkBg
            case .system:
                LinearGradient(
                    stops: [
                        .init(color: NoriColors.Swatch.lightBg, location: 0.495),
                        .init(color: NoriColors.Swatch.darkBg, location: 0.505)
                    ],
                    startPoint: .topLeading,
                    endPoint: .bottomTrailing
                )
            }

            GeometryReader { proxy in
                let innerWidth = max(0, proxy.size.width - 14)
                VStack(alignment: .leading, spacing: 0) {
                    HStack {
                        Spacer()
                        Capsule()
                            .fill(mode == .light ? NoriColors.Swatch.lightBubble : NoriColors.Swatch.darkBubble)
                            .frame(width: innerWidth * 0.44, height: 7)
                    }
                    Spacer()
                    Capsule()
                        .fill(
                            mode == .light
                                ? NoriColors.Swatch.lightLine
                                : mode == .dark
                                    ? NoriColors.Swatch.darkLine
                                    : NoriColors.Swatch.systemLine
                        )
                        .frame(width: innerWidth * 0.62, height: 6)
                }
                .padding(7)
            }
        }
        .frame(height: 52)
        .clipShape(RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
        )
        .accessibilityHidden(true)
    }
}

struct OnboardingModelKeyStep: View {
    @Bindable var store: NoriStore
    let primaryProvider: ModelKeyProvider?
    let byokRequired: Bool
    let keyConfigured: Bool
    let canContinue: Bool
    @Binding var selectedProviderID: String
    @Binding var keyInput: String
    @Binding var isReplacingKey: Bool
    @Binding var isValidatingKey: Bool
    @Binding var keyError: String?
    var isKeyFocused: FocusState<Bool>.Binding
    let onVerifyKey: () -> Void
    let onOpenModels: () -> Void
    let onContinue: () -> Void

    private var verifiedKeyLabel: String {
        if let last4 = primaryProvider?.last4, !last4.isEmpty {
            return "Key saved · ••••\(last4)"
        }
        return "Key saved"
    }

    var body: some View {
        VStack(spacing: 0) {
            Text(store.homeConnectors?.enabled == true ? "Choose your AI model" : "Add your API key")
                .font(.title.bold())
                .foregroundStyle(NoriColors.ink)
                .multilineTextAlignment(.center)
                .accessibilityAddTraits(.isHeader)

            NoriCard(padding: 16) {
                VStack(alignment: .leading, spacing: 14) {
                    HStack(spacing: 10) {
                        Image(systemName: "key.fill")
                            .font(.system(size: 15, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                            .frame(width: 36, height: 36)
                            .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))

                        if store.modelKeys.providers.isEmpty {
                            Text("Providers unavailable")
                                .font(NoriTypography.rowTitle)
                                .foregroundStyle(NoriColors.ink3)
                        } else {
                            Picker("Provider", selection: Binding(
                                get: { primaryProvider?.provider ?? "" },
                                set: { provider in
                                    selectedProviderID = provider
                                    isReplacingKey = false
                                    keyInput = ""
                                    keyError = nil
                                }
                            )) {
                                ForEach(store.modelKeys.providers) { provider in
                                    Text(provider.label).tag(provider.provider)
                                }
                            }
                            .pickerStyle(.menu)
                            .labelsHidden()
                            .disabled(isValidatingKey)
                        }

                        Spacer()

                        if isValidatingKey {
                            NoriPill("Saving", tone: .working)
                        } else if keyConfigured {
                            NoriPill("Configured", tone: .ok)
                        } else if byokRequired && store.homeConnectors?.enabled != true {
                            NoriPill("Required", tone: .warning)
                        }
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
                                    .focused(isKeyFocused)
                                    .disabled(isValidatingKey || primaryProvider == nil)
                                    .onSubmit(onVerifyKey)

                                Button(isValidatingKey ? "Saving…" : "Save", action: onVerifyKey)
                                    .font(NoriTypography.metaSemibold)
                                    .foregroundStyle((isValidatingKey || primaryProvider == nil) ? NoriColors.disabled : NoriColors.accent)
                                    .padding(.horizontal, 10)
                                    .padding(.vertical, 6)
                                    .background(
                                        (isValidatingKey || primaryProvider == nil) ? NoriColors.disabledBg : NoriColors.surface,
                                        in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                    )
                                    .disabled(isValidatingKey || primaryProvider == nil)
                            }
                            .padding(.horizontal, 12)
                            .frame(minHeight: 44)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    .strokeBorder(
                                        isKeyFocused.wrappedValue ? NoriColors.accentSignal : NoriColors.cardBorder,
                                        lineWidth: isKeyFocused.wrappedValue ? 1.5 : 1
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
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriTone.ok.foreground)
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 10)
                        .background(NoriTone.ok.background, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
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

            if store.homeConnectors?.enabled == true {
                Button("Use a home AI server", action: onOpenModels)
                    .buttonStyle(NoriButtonStyle(.tonal, fullWidth: true))
                    .padding(.top, 12)
            }

            Button("Continue", action: onContinue)
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(!canContinue || isValidatingKey)
                .padding(.top, 24)
        }
    }
}

struct OnboardingAboutYouStep: View {
    let replyStyles: [String]
    let focusOptions: [String]
    @Binding var name: String
    @Binding var replyStyle: String
    @Binding var selectedFocus: Set<String>
    @Binding var workingOn: String
    let isFinishing: Bool
    let finishError: String?
    let onOptionTap: () -> Void
    let onFinish: () -> Void
    let onRetry: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            Text("About you")
                .font(.title.bold())
                .foregroundStyle(NoriColors.ink)
                .multilineTextAlignment(.center)
                .accessibilityAddTraits(.isHeader)

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
                            .frame(minHeight: 44)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
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
                                    onOptionTap()
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
                                    onOptionTap()
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
                            .frame(minHeight: 44)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                            )
                    }
                }
            }
            .padding(.top, 20)

            if let finishError {
                NoriNotice(
                    title: finishError,
                    tone: .danger,
                    systemImage: "exclamationmark.triangle.fill"
                ) {
                    Button("Retry", action: onRetry)
                        .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                        .disabled(isFinishing)
                }
                .padding(.top, 16)
            }

            Button(isFinishing ? "Starting…" : "Start chatting", action: onFinish)
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(isFinishing)
                .padding(.top, 24)
        }
    }

    private func chipButton(_ label: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(label)
                .font(.footnote.weight(selected ? .semibold : .medium))
                .foregroundStyle(selected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                .padding(.horizontal, 13)
                .padding(.vertical, 8)
                .background(
                    selected ? NoriColors.chipSelectedBg : NoriColors.elevated,
                    in: Capsule()
                )
                .overlay(
                    Capsule()
                        .strokeBorder(
                            selected ? NoriColors.chipSelectedBorder : NoriColors.cardBorder,
                            lineWidth: 1
                        )
                )
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}
