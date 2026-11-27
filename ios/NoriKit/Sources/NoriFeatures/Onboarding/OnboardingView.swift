import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public struct OnboardingView: View {
    @Bindable private var store: NoriStore
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue

    @State private var step: Int = 1
    @State private var moodRequest: OnboardingMoodRequest?

    @State private var keyInput = ""
    @State private var isReplacingKey = false
    @State private var isValidatingKey = false
    @State private var keyError: String?
    @State private var selectedProviderID = ""
    @State private var suggestedProvider: String?
    @State private var showingModelSuggestion = false
    @State private var showingModels = false
    @FocusState private var isKeyFocused: Bool

    @State private var name = ""
    @State private var replyStyle = "Concise & direct"
    @State private var selectedFocus: Set<String> = []
    @State private var workingOn = ""
    @State private var isFinishing = false
    @State private var finishError: String?
    @State private var lastSaveAboutYou = true

    private let replyStyles = ["Concise & direct", "Warm & conversational", "Bullets first"]
    private let focusOptions = ["Engineering", "Daily planning", "Email & calendar", "Research", "Writing"]

    public init(store: NoriStore) {
        self.store = store
    }

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    private var primaryProvider: ModelKeyProvider? {
        store.modelKeys.providers.first { $0.provider == selectedProviderID }
            ?? store.modelKeys.providers.first { $0.configured }
            ?? store.modelKeys.providers.first
    }

    private var byokRequired: Bool {
        store.modelKeys.byokRequired
    }

    private var keyConfigured: Bool {
        primaryProvider?.configured == true && !isReplacingKey
    }

    private var canContinueStep3: Bool {
        !byokRequired || ((store.hasConfiguredModelKey || store.hasSelectedHomeChat) && !isReplacingKey)
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
                    OnboardingMascotStage(
                        step: step,
                        markState: step == 3 ? step3MarkState : .idle,
                        moodRequest: moodRequest
                    ) {
                        triggerHaptic(.light)
                        triggerMood(.hop, happy: true, sparkles: step == 1)
                    }

                    Group {
                        switch step {
                        case 1:
                            OnboardingWelcomeStep {
                                triggerHaptic(.light)
                                goToStep(2)
                            }
                        case 2:
                            OnboardingAppearanceStep(
                                appearanceRawValue: $appearanceRawValue,
                                appearance: appearance,
                                onSelectAppearance: {
                                    triggerHaptic(.light)
                                    triggerMood(.hop, happy: true, duration: 0.6)
                                },
                                onContinue: {
                                    triggerHaptic(.light)
                                    goToStep(3)
                                }
                            )
                        case 3:
                            OnboardingModelKeyStep(
                                store: store,
                                primaryProvider: primaryProvider,
                                byokRequired: byokRequired,
                                keyConfigured: keyConfigured,
                                canContinue: canContinueStep3,
                                selectedProviderID: $selectedProviderID,
                                keyInput: $keyInput,
                                isReplacingKey: $isReplacingKey,
                                isValidatingKey: $isValidatingKey,
                                keyError: $keyError,
                                isKeyFocused: $isKeyFocused,
                                onVerifyKey: { Task { await verifyKey() } },
                                onOpenModels: { showingModels = true },
                                onContinue: {
                                    triggerHaptic(.light)
                                    goToStep(4)
                                }
                            )
                        default:
                            OnboardingAboutYouStep(
                                replyStyles: replyStyles,
                                focusOptions: focusOptions,
                                name: $name,
                                replyStyle: $replyStyle,
                                selectedFocus: $selectedFocus,
                                workingOn: $workingOn,
                                isFinishing: isFinishing,
                                finishError: finishError,
                                onOptionTap: {
                                    triggerHaptic(.light)
                                    triggerMood(.nod)
                                },
                                onFinish: { finish(saveAboutYou: true) },
                                onRetry: { finish(saveAboutYou: lastSaveAboutYou) }
                            )
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
        .task {
            try? await store.refreshHomeConnectors()
            try? await store.refreshModelSettings()
        }
        .sheet(isPresented: $showingModels) {
            NavigationStack {
                ModelsSettingsView(store: store)
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Done") { showingModels = false } } }
            }
            .presentationDragIndicator(.visible)
            .presentationBackground(NoriColors.sheetBackground)
            .presentationCornerRadius(NoriRadius.sheet)
        }
        .alert("Use recommended models?", isPresented: $showingModelSuggestion) {
            Button("Use recommended models") {
                guard let provider = suggestedProvider,
                      let recommendation = store.modelSettings?.recommendations[provider] else { return }
                Task {
                    do {
                        try await store.saveModelSettings(recommendation)
                    } catch {
                        keyError = error.localizedDescription
                    }
                }
            }
            Button("Keep current models", role: .cancel) { suggestedProvider = nil }
        } message: {
            Text(recommendationSummary)
        }
    }

    private var showsRightHeaderAction: Bool {
        (step == 3 && !keyConfigured) || step == 4
    }

    private var headerBar: some View {
        ZStack {
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
                .accessibilityHidden(step <= 1)

                Spacer()

                Button(step == 3 ? "Set up later" : "Skip") {
                    if step == 3 {
                        goToStep(4)
                    } else if step == 4 {
                        finish(saveAboutYou: false)
                    }
                }
                .font(NoriTypography.metaSemibold)
                .foregroundStyle(NoriColors.ink2)
                .opacity(showsRightHeaderAction ? 1 : 0)
                .disabled(!showsRightHeaderAction || isFinishing)
                .accessibilityHidden(!showsRightHeaderAction)
            }

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
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Step \(step) of 4")
        }
        .padding(.horizontal, 20)
        .padding(.top, 12)
        .padding(.bottom, 8)
    }

    private var recommendationSummary: String {
        guard let settings = store.modelSettings, let provider = suggestedProvider,
              let recommendation = settings.recommendations[provider] else {
            return "Choose models for Chat and Work."
        }
        func label(_ id: String?, effort: String?) -> String {
            let name = settings.modelProfiles.profiles.first { $0.id == id }?.label ?? "Model"
            guard let effort, !effort.isEmpty else { return name }
            return "\(name) · \(effort.capitalized)"
        }
        let chat = label(recommendation.chatProfileID, effort: recommendation.chatReasoning)
        let work = label(recommendation.taskProfileID, effort: recommendation.taskReasoning)
        return "Chat: \(chat)\nWork: \(work)"
    }

    private func goToStep(_ next: Int) {
        if next != 4 {
            finishError = nil
        }
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
        guard let providerID = primaryProvider?.provider else {
            keyError = "Could not load model providers."
            return
        }
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
            do {
                try await store.refreshModelSettings()
                if let settings = store.modelSettings,
                   let recommended = settings.recommendations[providerID],
                   settings.selection != recommended {
                    suggestedProvider = providerID
                    showingModelSuggestion = true
                }
            } catch {
                keyError = "Key saved. Could not load recommended models: \(error.localizedDescription)"
            }
        } catch {
            keyError = error.localizedDescription
            triggerMood(.shake)
        }
    }

    private func friendlyOnboardingSaveError(_ error: Error) -> String {
        if let apiErr = error as? NoriAPIError, !apiErr.message.isEmpty, apiErr.statusCode < 500, apiErr.statusCode > 0 {
            return apiErr.message
        }
        if let urlErr = error as? URLError,
           urlErr.code == .notConnectedToInternet || urlErr.code == .networkConnectionLost {
            return "You appear to be offline. Check your connection and try again."
        }
        return "Could not save your setup. Try again."
    }

    private func finish(saveAboutYou: Bool) {
        guard !isFinishing else { return }
        lastSaveAboutYou = saveAboutYou
        finishError = nil
        isFinishing = true
        let orderedFocus = focusOptions.filter { selectedFocus.contains($0) }
        Task {
            defer { isFinishing = false }
            do {
                try await store.completeOnboarding(
                    name: name,
                    replyStyle: replyStyle,
                    focus: orderedFocus,
                    workingOn: workingOn,
                    saveAboutYou: saveAboutYou
                )
                triggerHaptic(.success)
                triggerMood(.celebrate, happy: true, sparkles: true, duration: 0.85)
            } catch {
                finishError = friendlyOnboardingSaveError(error)
                triggerMood(.shake)
            }
        }
    }

    private func triggerMood(
        _ mood: OnboardingMood,
        happy: Bool = false,
        sparkles: Bool = false,
        duration: Double = 0.7
    ) {
        moodRequest = OnboardingMoodRequest(
            mood: mood,
            happy: happy,
            sparkles: sparkles,
            duration: duration
        )
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
