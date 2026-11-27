import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public struct ModelsSettingsView: View {
    @Environment(\.scenePhase) private var scenePhase
    @Bindable private var store: NoriStore
    @State private var selection = ModelSelection()
    @State private var draftBase = ModelSelection()
    @State private var draftRevision: Int?
    @State private var isLoading = false
    @State private var isSaving = false
    @State private var modelLoadError: String?
    @State private var keyLoadError: String?
    @State private var homeLoadError: String?
    @State private var saveError: String?
    @State private var hasConflict = false
    @State private var suggestedProvider: String?
    @State private var showingSuggestion = false

    public init(store: NoriStore) {
        self.store = store
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                NoriSectionHeader("Models and reasoning")
                if let settings = store.modelSettings {
                    modelSelectionCard(settings: settings)
                    if let modelLoadError {
                        NoriNotice(title: "Could not refresh models", detail: modelLoadError, tone: .danger) {
                            Button("Retry") { Task { await load() } }
                                .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        }
                    }
                } else if isLoading {
                    NoriCard { ProgressView().tint(NoriColors.accent) }
                } else if let modelLoadError {
                    NoriNotice(title: "Could not load models", detail: modelLoadError, tone: .danger) {
                        Button("Retry") { Task { await load() } }
                            .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    }
                }

                HomeConnectorSettingsView(
                    store: store,
                    loadError: homeLoadError,
                    onRetry: { Task { await reloadHomeConnectors() } }
                )

                NoriSectionHeader("API Keys")
                if store.needsModelKeySetup && !hasPairedHomeDevice {
                    NoriNotice(
                        title: "API key required",
                        detail: "Add a provider API key to use Chat and Work.",
                        tone: .warning,
                        systemImage: "key.fill"
                    )
                }
                NoriNotice(
                    title: "Encrypted at rest",
                    detail: "Keys are encrypted on the server before storage and are never shown again after saving.",
                    tone: .neutral,
                    systemImage: "lock.shield"
                )
                if store.modelKeys.providers.isEmpty {
                    if let keyLoadError {
                        NoriNotice(title: "Could not load API keys", detail: keyLoadError, tone: .danger)
                    } else if !isLoading {
                        NoriCard {
                            Text("No model key providers are available.")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }
                } else {
                    if let keyLoadError {
                        NoriNotice(title: "Could not refresh API keys", detail: keyLoadError, tone: .danger)
                    }
                    ForEach(store.modelKeys.providers) { provider in
                        ModelKeyProviderCard(
                            store: store,
                            provider: provider,
                            onSaved: { provider in Task { await didSaveKey(provider) } },
                            onRemoved: { Task { await reloadModels() } }
                        )
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .task {
            await load()
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(10)) } catch { break }
                if store.homeConnectors?.enabled == true {
                    try? await store.refreshHomeConnectors()
                    if !hasUnsavedDraft { await reloadModels() }
                }
            }
        }
        .refreshable { await load() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active && !hasUnsavedDraft {
                Task { await load() }
            }
        }
        .background(NoriColors.canvas)
        .navigationTitle("Models")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .alert("Use recommended models?", isPresented: $showingSuggestion) {
            Button("Use recommended models") {
                guard let provider = suggestedProvider,
                      let recommended = store.modelSettings?.recommendations[provider] else { return }
                selection = recommended
                Task { await saveSelection() }
            }
            Button("Keep current models", role: .cancel) { suggestedProvider = nil }
        } message: {
            Text(recommendationSummary)
        }
    }

    private var hasUnsavedDraft: Bool {
        draftRevision != nil && selection != draftBase
    }

    private var hasPairedHomeDevice: Bool {
        store.homeConnectors?.enabled == true && !(store.homeConnectors?.devices.isEmpty ?? true)
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

    private func modelSelectionCard(settings: ModelSettingsResponse) -> some View {
        let profiles = settings.modelProfiles.profiles
        let chatProfile = profiles.first { $0.id == selection.chatProfileID && $0.lanes.contains("chat") }
        let taskProfile = profiles.first { $0.id == selection.taskProfileID && $0.lanes.contains("task") }
        return NoriCard {
            VStack(alignment: .leading, spacing: 12) {
                if profiles.isEmpty && selection.homeOnly != true && selection.chatProfileID == nil && selection.taskProfileID == nil {
                    Text("Add an API key or connect a home computer to choose models.")
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                } else {
                    profilePicker("Chat model", lane: "chat", profiles: profiles, selectedID: Binding(
                        get: { selection.chatProfileID ?? "" },
                        set: { id in
                            selection.chatProfileID = id.isEmpty ? nil : id
                            let nextProfile = profiles.first { $0.id == id }
                            let supported = nextProfile?.supportedReasoning ?? []
                            if let effort = selection.chatReasoning, !supported.contains(effort) {
                                if let defaultEffort = nextProfile?.defaultReasoning,
                                   !defaultEffort.isEmpty,
                                   supported.contains(defaultEffort) {
                                    selection.chatReasoning = defaultEffort
                                } else {
                                    selection.chatReasoning = nil
                                }
                            }
                        }
                    ))
                    if let chatProfile, !(chatProfile.supportedReasoning?.isEmpty ?? true) {
                        reasoningPicker("Chat reasoning", profile: chatProfile, effort: Binding(
                            get: { selection.chatReasoning ?? "" },
                            set: { selection.chatReasoning = $0.isEmpty ? nil : $0 }
                        ))
                    }
                    Rectangle()
                        .fill(NoriColors.hairline)
                        .frame(height: 1)
                    profilePicker("Work model", lane: "task", profiles: profiles, selectedID: Binding(
                        get: { selection.taskProfileID ?? "" },
                        set: { id in
                            selection.taskProfileID = id.isEmpty ? nil : id
                            let nextProfile = profiles.first { $0.id == id }
                            let supported = nextProfile?.supportedReasoning ?? []
                            if let effort = selection.taskReasoning, !supported.contains(effort) {
                                if let defaultEffort = nextProfile?.defaultReasoning,
                                   !defaultEffort.isEmpty,
                                   supported.contains(defaultEffort) {
                                    selection.taskReasoning = defaultEffort
                                } else {
                                    selection.taskReasoning = nil
                                }
                            }
                        }
                    ))
                    if let taskProfile, !(taskProfile.supportedReasoning?.isEmpty ?? true) {
                        reasoningPicker("Work reasoning", profile: taskProfile, effort: Binding(
                            get: { selection.taskReasoning ?? "" },
                            set: { selection.taskReasoning = $0.isEmpty ? nil : $0 }
                        ))
                    }
                    if store.homeConnectors?.enabled == true || selection.homeOnly == true {
                        Toggle(isOn: Binding(
                            get: { selection.homeOnly == true },
                            set: { selection.homeOnly = $0 }
                        )) {
                            VStack(alignment: .leading, spacing: 4) {
                                Text("Use home models only").font(NoriTypography.rowTitle)
                                Text("Model requests use your home computer. Nori still stores your conversations.")
                                    .font(NoriTypography.meta)
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }
                        .tint(NoriColors.brandPrimary)
                        .disabled(isSaving || (selection.homeOnly != true && chatProfile?.backend != "home"))
                    }
                    let homeWorkInvalid = selection.homeOnly == true && selection.taskProfileID != nil && taskProfile?.backend != "home"
                    if homeWorkInvalid {
                        NoriNotice(title: "Choose a home Work model or clear the Work model before saving.", tone: .warning)
                    }
                    if let saveError {
                        NoriNotice(title: "Could not save models", detail: saveError, tone: .danger) {
                            if hasConflict {
                                Button("Load saved models") { Task { await discardAndReloadModels() } }
                                    .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                            }
                        }
                    }
                    Button(isSaving ? "Saving…" : "Save models") {
                        Task { await saveSelection() }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                    .disabled(isSaving || !hasUnsavedDraft || hasConflict || homeWorkInvalid)
                }
            }
        }
    }

    private func profilePicker(
        _ title: String, lane: String, profiles: [ModelProfile], selectedID: Binding<String>
    ) -> some View {
        let available = profiles.filter { $0.lanes.contains(lane) }
        let isWork = lane == "task"
        let homeOnly = selection.homeOnly == true
        let selectedLabel: String = {
            if selectedID.wrappedValue.isEmpty {
                return isWork ? "No Work model" : "Choose a model"
            }
            if let match = available.first(where: { $0.id == selectedID.wrappedValue }) {
                return profileLabel(match)
            }
            return "Unavailable · choose a model"
        }()
        return VStack(alignment: .leading, spacing: 6) {
            Text(title)
                .font(NoriTypography.metaSemibold)
                .foregroundStyle(NoriColors.ink2)
            Menu {
                if isWork {
                    Button {
                        selectedID.wrappedValue = ""
                    } label: {
                        if selectedID.wrappedValue.isEmpty {
                            Label("No Work model", systemImage: "checkmark")
                        } else {
                            Text("No Work model")
                        }
                    }
                    profileOptions(available, selectedID: selectedID)
                } else {
                    let workProfiles = available.filter { $0.lanes.contains("task") }
                    let chatProfiles = available.filter { !$0.lanes.contains("task") }
                    if !workProfiles.isEmpty {
                        Section("Chat and Work") { profileOptions(workProfiles, selectedID: selectedID) }
                    }
                    if !chatProfiles.isEmpty {
                        Section("Chat only") { profileOptions(chatProfiles, selectedID: selectedID) }
                    }
                }
            } label: {
                pickerFieldLabel(selectedLabel, isPlaceholder: selectedID.wrappedValue.isEmpty)
            }
            .buttonStyle(.plain)
            .disabled(isSaving || (available.isEmpty && (!isWork || selectedID.wrappedValue.isEmpty)))
            if isWork {
                let hasWorkModels = available.contains { !homeOnly || $0.backend == "home" }
                Text(hasWorkModels
                     ? "These models can run web searches and use connected apps in Work."
                     : homeOnly ? "No home models are available for Work." : "No models are available for Work yet.")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
            }
        }
    }

    private func profileOptions(_ profiles: [ModelProfile], selectedID: Binding<String>) -> some View {
        ForEach(profiles) { profile in
            Button {
                selectedID.wrappedValue = profile.id
            } label: {
                if profile.id == selectedID.wrappedValue {
                    Label(profileLabel(profile), systemImage: "checkmark")
                } else {
                    Text(profileLabel(profile))
                }
            }
            .disabled(selection.homeOnly == true && profile.backend != "home")
        }
    }

    private func profileLabel(_ profile: ModelProfile) -> String {
        switch profile.availability {
        case "offline": return "\(profile.label) · Offline"
        case "ollama_unavailable": return "\(profile.label) · Ollama unavailable"
        default: return profile.label
        }
    }

    private func reasoningPicker(_ title: String, profile: ModelProfile, effort: Binding<String>) -> some View {
        let defaultLabel: String = {
            if let def = profile.defaultReasoning, !def.isEmpty {
                return "Default (\(def.capitalized))"
            }
            return "Default"
        }()
        let selectedLabel = effort.wrappedValue.isEmpty ? defaultLabel : effort.wrappedValue.capitalized
        return VStack(alignment: .leading, spacing: 6) {
            Text(title)
                .font(NoriTypography.metaSemibold)
                .foregroundStyle(NoriColors.ink2)
            Menu {
                Button {
                    effort.wrappedValue = ""
                } label: {
                    if effort.wrappedValue.isEmpty {
                        Label(defaultLabel, systemImage: "checkmark")
                    } else {
                        Text(defaultLabel)
                    }
                }
                ForEach(profile.supportedReasoning ?? [], id: \.self) { value in
                    Button {
                        effort.wrappedValue = value
                    } label: {
                        if value == effort.wrappedValue {
                            Label(value.capitalized, systemImage: "checkmark")
                        } else {
                            Text(value.capitalized)
                        }
                    }
                }
            } label: {
                pickerFieldLabel(selectedLabel, isPlaceholder: false)
            }
            .buttonStyle(.plain)
            .disabled(isSaving)
        }
    }

    private func pickerFieldLabel(_ text: String, isPlaceholder: Bool) -> some View {
        HStack(spacing: 8) {
            Text(text)
                .font(NoriTypography.meta)
                .foregroundStyle(isPlaceholder ? NoriColors.ink3 : NoriColors.ink)
                .lineLimit(1)
                .truncationMode(.tail)
                .frame(maxWidth: .infinity, alignment: .leading)
            Image(systemName: "chevron.up.chevron.down")
                .font(.system(size: 11, weight: .semibold))
                .foregroundStyle(NoriColors.accent)
        }
        .padding(.horizontal, 12)
        .frame(height: 40)
        .background(
            NoriColors.elevated,
            in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
    }

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        modelLoadError = nil
        keyLoadError = nil
        homeLoadError = nil
        async let keys: Void = store.refreshModelKeysThrowing()
        async let models: Void = store.refreshModelSettings()
        async let home: Void = store.refreshHomeConnectors()
        do {
            try await models
            adoptSettingsIfUnedited()
        } catch {
            modelLoadError = error.localizedDescription
        }
        do {
            try await keys
        } catch {
            keyLoadError = error.localizedDescription
        }
        do {
            try await home
        } catch is CancellationError {
            return
        } catch {
            homeLoadError = error.localizedDescription
        }
    }

    private func reloadHomeConnectors() async {
        homeLoadError = nil
        do {
            try await store.refreshHomeConnectors()
        } catch is CancellationError {
            return
        } catch {
            homeLoadError = error.localizedDescription
        }
    }

    private func reloadModels() async {
        do {
            try await store.refreshModelSettings()
            adoptSettingsIfUnedited()
            modelLoadError = nil
        } catch {
            modelLoadError = error.localizedDescription
        }
    }

    private func adoptSettingsIfUnedited() {
        guard !hasUnsavedDraft, let settings = store.modelSettings else { return }
        adoptSettings(settings)
    }

    private func adoptSettings(_ settings: ModelSettingsResponse) {
        selection = settings.selection
        draftBase = settings.selection
        draftRevision = settings.revision
    }

    private func discardAndReloadModels() async {
        do {
            try await store.refreshModelSettings()
            if let settings = store.modelSettings { adoptSettings(settings) }
            modelLoadError = nil
            saveError = nil
            hasConflict = false
        } catch {
            saveError = error.localizedDescription
        }
    }

    private func didSaveKey(_ provider: String) async {
        await reloadModels()
        guard let settings = store.modelSettings,
              let recommended = settings.recommendations[provider],
              settings.selection != recommended else { return }
        suggestedProvider = provider
        showingSuggestion = true
    }

    private func saveSelection() async {
        guard let draftRevision else {
            saveError = "Reload model settings and try again."
            return
        }
        isSaving = true
        saveError = nil
        hasConflict = false
        defer { isSaving = false }
        do {
            try await store.saveModelSettings(selection, expectedRevision: draftRevision)
            if let settings = store.modelSettings { adoptSettings(settings) }
        } catch {
            saveError = error.localizedDescription
            if let apiError = error as? NoriAPIError, apiError.isRevisionConflict {
                hasConflict = true
                saveError = "Models changed on another device. Load the saved models to continue."
            }
        }
    }
}

// MARK: - Provider Card

private struct ModelKeyProviderCard: View {
    @Bindable var store: NoriStore
    let provider: ModelKeyProvider
    let onSaved: (String) -> Void
    let onRemoved: () -> Void

    @State private var isEditing = false
    @State private var keyInput = ""
    @State private var isSaving = false
    @State private var isRemoving = false
    @State private var errorMessage: String?
    @State private var showingRemoveConfirmation = false
    @FocusState private var isInputFocused: Bool

    private var trimmedKey: String {
        keyInput.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var isBusy: Bool {
        isSaving || isRemoving
    }

    private var timestampSummary: String? {
        var parts: [String] = []
        if let updatedAt = provider.updatedAt, !updatedAt.isEmpty {
            parts.append("Updated \(NoriDateFormatting.relative(updatedAt))")
        }
        if let validatedAt = provider.validatedAt, !validatedAt.isEmpty {
            parts.append("Validated \(NoriDateFormatting.relative(validatedAt))")
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    var body: some View {
        NoriCard(padding: 14) {
            VStack(alignment: .leading, spacing: 12) {
                HStack(alignment: .center, spacing: 8) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(provider.label)
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(NoriColors.ink)
                        if provider.configured {
                            if let masked = provider.maskedKeyDisplay {
                                Text(masked)
                                    .font(NoriTypography.monoSmall)
                                    .foregroundStyle(NoriColors.ink2)
                            } else {
                                Text("Configured")
                                    .font(NoriTypography.meta)
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }
                    }

                    Spacer(minLength: 8)

                    if !provider.configured {
                        NoriPill("Not set", tone: .neutral)
                    }
                }

                if provider.configured, let timestampSummary {
                    Text(timestampSummary)
                        .font(.system(size: 12))
                        .foregroundStyle(NoriColors.ink3)
                }

                if isEditing {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(provider.configured ? "New \(provider.label) API key" : "\(provider.label) API key")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink2)

                        SecureField(
                            provider.configured ? "Paste new key to replace" : "Paste your API key",
                            text: $keyInput
                        )
                        .textFieldStyle(.plain)
                        .font(NoriTypography.mono)
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .textInputAutocapitalization(.never)
                        #endif
                        .focused($isInputFocused)
                        .disabled(isBusy)
                        .padding(.horizontal, 12)
                        .frame(height: 40)
                        .background(
                            NoriColors.elevated,
                            in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                .strokeBorder(
                                    isInputFocused ? NoriColors.accentSignal : NoriColors.cardBorder,
                                    lineWidth: isInputFocused ? 1.5 : 1
                                )
                        )
                        .onSubmit {
                            if !trimmedKey.isEmpty && !isBusy {
                                saveKey()
                            }
                        }
                    }

                    if let errorMessage, !errorMessage.isEmpty {
                        NoriNotice(
                            title: "Could not update key",
                            detail: errorMessage,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                    }

                    HStack(spacing: 8) {
                        Button {
                            saveKey()
                        } label: {
                            if isSaving {
                                ProgressView()
                                    .tint(NoriColors.onAccent)
                            } else {
                                Text("Save")
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                        .disabled(trimmedKey.isEmpty || isBusy)

                        Button("Cancel") {
                            isEditing = false
                            keyInput = ""
                            errorMessage = nil
                            isInputFocused = false
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        .disabled(isBusy)
                    }
                } else {
                    if let errorMessage, !errorMessage.isEmpty {
                        NoriNotice(
                            title: "Could not update key",
                            detail: errorMessage,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                    }

                    HStack(spacing: 8) {
                        Button(provider.configured ? "Replace" : "Add key") {
                            errorMessage = nil
                            keyInput = ""
                            isEditing = true
                            isInputFocused = true
                        }
                        .buttonStyle(NoriButtonStyle(provider.configured ? .neutral : .primary, compact: true))
                        .disabled(isBusy)

                        if provider.configured {
                            Button {
                                showingRemoveConfirmation = true
                            } label: {
                                if isRemoving {
                                    ProgressView()
                                        .tint(NoriColors.danger)
                                } else {
                                    Text("Remove")
                                }
                            }
                            .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                            .disabled(isBusy)
                        }
                    }
                }
            }
        }
        .alert(
            "Remove \(provider.label) key?",
            isPresented: $showingRemoveConfirmation
        ) {
            Button("Remove", role: .destructive) {
                removeKey()
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Nori will delete your stored key for \(provider.label). You can add a new one at any time.")
        }
    }

    private func saveKey() {
        let keyToSave = trimmedKey
        guard !keyToSave.isEmpty, !isBusy else { return }
        isSaving = true
        errorMessage = nil
        isInputFocused = false

        Task {
            defer { isSaving = false }
            do {
                _ = try await store.saveModelKey(provider: provider.provider, apiKey: keyToSave)
                keyInput = ""
                isEditing = false
                triggerNotificationHaptic(.success)
                onSaved(provider.provider)
            } catch {
                errorMessage = error.localizedDescription
                triggerNotificationHaptic(.error)
            }
        }
    }

    private func removeKey() {
        guard !isBusy else { return }
        isRemoving = true
        errorMessage = nil
        isInputFocused = false

        Task {
            defer { isRemoving = false }
            do {
                _ = try await store.deleteModelKey(provider: provider.provider)
                keyInput = ""
                triggerNotificationHaptic(.success)
                onRemoved()
            } catch {
                errorMessage = error.localizedDescription
                triggerNotificationHaptic(.error)
            }
        }
    }

    #if canImport(UIKit)
    private func triggerNotificationHaptic(_ type: UINotificationFeedbackGenerator.FeedbackType) {
        let generator = UINotificationFeedbackGenerator()
        generator.notificationOccurred(type)
    }
    #else
    private enum StubHapticType { case success, error }
    private func triggerNotificationHaptic(_ type: StubHapticType) {}
    #endif
}
