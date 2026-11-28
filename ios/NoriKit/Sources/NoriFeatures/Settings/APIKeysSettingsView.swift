import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public struct ModelsSettingsView: View {
    @Environment(\.scenePhase) private var scenePhase
    @Bindable private var store: NoriStore
    @State private var isLoading = false
    @State private var isSaving = false
    @State private var modelLoadError: String?
    @State private var keyLoadError: String?
    @State private var serviceKeyLoadError: String?
    @State private var homeLoadError: String?
    @State private var saveError: String?
    @State private var suggestedProvider: String?
    @State private var showingSuggestion = false

    private static let trialBYOKNotice =
        "Bring-your-own API keys are available on paid plans. Your trial includes hosted credits."

    private var resolvedServices: [ServiceKeyStatus] {
        if !store.serviceKeys.services.isEmpty {
            return store.serviceKeys.services
        }
        let exaConfigured = store.capabilities?.serviceKeys?.exaConfigured ?? false
        let platformAvailable = store.capabilities?.serviceKeys?.platformExaAvailable ?? false
        return [
            ServiceKeyStatus(
                service: "exa",
                label: "Exa Web Search",
                configured: exaConfigured,
                source: exaConfigured ? "byok" : (platformAvailable ? "platform" : "none")
            )
        ]
    }

    public init(store: NoriStore) {
        self.store = store
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let block = store.activeBillingAccessBlock {
                    BillingAccessBannerView(block: block, allowBYOK: store.allowBYOK)
                }

                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Default models")
                    if let settings = store.modelSettings {
                        defaultModelsSection(settings: settings)
                        if let modelLoadError {
                            NoriNotice(title: "Could not refresh models", detail: modelLoadError, tone: .danger) {
                                Button("Retry") { Task { await load() } }
                                    .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                            }
                            .padding(.top, 8)
                        }
                    } else if isLoading {
                        NoriCard { ProgressView().tint(NoriColors.accent) }
                    } else if let modelLoadError {
                        NoriNotice(title: "Could not load models", detail: modelLoadError, tone: .danger) {
                            Button("Retry") { Task { await load() } }
                                .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        }
                    }
                }

                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Providers")
                    if !store.allowBYOK {
                        NoriNotice(
                            title: Self.trialBYOKNotice,
                            tone: .neutral,
                            systemImage: "info.circle"
                        )
                        .padding(.bottom, 10)
                    }
                    if store.needsModelKeySetup && !hasPairedHomeDevice {
                        NoriNotice(
                            title: "Add a provider API key to use Chat and Work.",
                            tone: .warning,
                            systemImage: "key.fill"
                        )
                        .padding(.bottom, 10)
                    }
                    if let keyLoadError {
                        NoriNotice(title: "Could not load providers", detail: keyLoadError, tone: .danger)
                            .padding(.bottom, 10)
                    }
                    if store.modelKeys.providers.isEmpty {
                        if !isLoading {
                            NoriCard {
                                Text("No model providers are available.")
                                    .font(NoriTypography.meta)
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }
                    } else {
                        NoriRows {
                            ForEach(Array(store.modelKeys.providers.enumerated()), id: \.element.id) { index, provider in
                                let isLast = index == store.modelKeys.providers.count - 1
                                if store.allowBYOK {
                                    NavigationLink {
                                        ProviderKeyDetailView(
                                            store: store,
                                            provider: provider,
                                            onSaved: { savedID in Task { await didSaveKey(savedID) } },
                                            onRemoved: { Task { await reloadModels() } }
                                        )
                                    } label: {
                                        providerRow(provider, showChevron: true, showDivider: !isLast)
                                    }
                                    .buttonStyle(.plain)
                                } else {
                                    providerRow(provider, showChevron: false, showDivider: !isLast)
                                }
                            }
                        }
                    }
                }

                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Web search")
                    if let serviceKeyLoadError, store.serviceKeys.services.isEmpty {
                        NoriNotice(title: "Could not load web search keys", detail: serviceKeyLoadError, tone: .danger) {
                            Button("Retry") { Task { await reloadServiceKeys() } }
                                .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        }
                        .padding(.bottom, 10)
                    }
                    NoriRows {
                        ForEach(Array(resolvedServices.enumerated()), id: \.element.id) { index, service in
                            let isLast = index == resolvedServices.count - 1
                            if store.allowBYOK {
                                NavigationLink {
                                    ServiceKeyDetailView(store: store, service: service)
                                } label: {
                                    serviceKeyRow(service, showChevron: true, showDivider: !isLast)
                                }
                                .buttonStyle(.plain)
                            } else {
                                serviceKeyRow(service, showChevron: false, showDivider: !isLast)
                            }
                        }
                    }
                }

                HomeConnectorSettingsView(
                    store: store,
                    loadError: homeLoadError,
                    onRetry: { Task { await reloadHomeConnectors() } }
                )
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
                    await reloadModels()
                }
            }
        }
        .refreshable { await load() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active {
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
                Task { await commitSelection(recommended) }
            }
            Button("Keep current models", role: .cancel) { suggestedProvider = nil }
        } message: {
            Text(recommendationSummary)
        }
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

    @ViewBuilder
    private func defaultModelsSection(settings: ModelSettingsResponse) -> some View {
        let profiles = settings.modelProfiles.profiles
        let selection = settings.selection
        let chatProfile = profiles.first { $0.id == selection.chatProfileID && $0.lanes.contains("chat") }
        let workProfile = profiles.first { $0.id == selection.taskProfileID && $0.lanes.contains("task") }
        let chatUnavailable = selection.chatProfileID != nil && chatProfile == nil
        let workUnavailable = selection.taskProfileID != nil && workProfile == nil
        let showHomeOnly = store.homeConnectors?.enabled == true || selection.homeOnly == true

        if profiles.isEmpty && selection.homeOnly != true && selection.chatProfileID == nil && selection.taskProfileID == nil {
            NoriCard {
                Text("Add a provider key or pair a home computer to choose models.")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
            }
        } else {
            VStack(alignment: .leading, spacing: 10) {
                if let saveError {
                    NoriNotice(title: "Could not save models", detail: saveError, tone: .danger)
                }
                if chatUnavailable || workUnavailable {
                    NoriNotice(title: "One of your selected models is unavailable. Choose an available model.", tone: .warning)
                }
                NoriRows {
                    NavigationLink {
                        ModelPickerView(
                            store: store,
                            lane: "chat",
                            title: "Chat model",
                            onCommit: { next in try await commitSelectionThrowing(next) }
                        )
                    } label: {
                        NoriRow(title: "Chat", showChevron: true, showDivider: true) {
                            Text(chatProfile.map(profileLabel) ?? (chatUnavailable ? "Unavailable" : "Choose"))
                                .font(NoriTypography.meta)
                                .foregroundStyle(chatUnavailable ? NoriColors.warning : NoriColors.ink2)
                                .lineLimit(1)
                        }
                    }
                    .buttonStyle(.plain)

                    NavigationLink {
                        ModelPickerView(
                            store: store,
                            lane: "task",
                            title: "Work model",
                            onCommit: { next in try await commitSelectionThrowing(next) }
                        )
                    } label: {
                        NoriRow(title: "Work", showChevron: true, showDivider: showHomeOnly) {
                            Text(workProfile.map(profileLabel) ?? (workUnavailable ? "Unavailable" : "None"))
                                .font(NoriTypography.meta)
                                .foregroundStyle(workUnavailable ? NoriColors.warning : NoriColors.ink2)
                                .lineLimit(1)
                        }
                    }
                    .buttonStyle(.plain)

                    if showHomeOnly {
                        NoriRow(title: "Home models only", showDivider: false) {
                            Toggle(
                                "Home models only",
                                isOn: Binding(
                                    get: { selection.homeOnly == true },
                                    set: { nextHomeOnly in
                                        var next = selection
                                        next.homeOnly = nextHomeOnly
                                        if nextHomeOnly {
                                            if chatProfile?.backend != "home",
                                               let homeChat = profiles.first(where: { $0.backend == "home" && $0.lanes.contains("chat") }) {
                                                next.chatProfileID = homeChat.id
                                                next.chatReasoning = homeChat.defaultReasoning
                                            }
                                            if workProfile != nil && workProfile?.backend != "home" {
                                                let homeWork = profiles.first(where: { $0.backend == "home" && $0.lanes.contains("task") })
                                                next.taskProfileID = homeWork?.id
                                                next.taskReasoning = homeWork?.defaultReasoning
                                            }
                                        }
                                        Task { await commitSelection(next) }
                                    }
                                )
                            )
                            .labelsHidden()
                            .tint(NoriColors.brandPrimary)
                            .disabled(
                                isSaving ||
                                    (selection.homeOnly != true &&
                                        !profiles.contains(where: { $0.backend == "home" && $0.lanes.contains("chat") }))
                            )
                        }
                    }
                }
            }
        }
    }

    private func profileLabel(_ profile: ModelProfile) -> String {
        switch profile.availability {
        case "offline": return "\(profile.label) · Offline"
        case "ollama_unavailable": return "\(profile.label) · Ollama unavailable"
        default: return profile.label
        }
    }

    @ViewBuilder
    private func providerRow(_ provider: ModelKeyProvider, showChevron: Bool, showDivider: Bool) -> some View {
        NoriBrandRow(
            title: provider.label,
            brand: NoriBrandKind.forProvider(provider: provider.provider),
            showChevron: showChevron,
            showDivider: showDivider
        ) {
            if provider.configured, let masked = provider.maskedKeyDisplay {
                Text(masked)
                    .font(NoriTypography.monoSmall)
                    .foregroundStyle(NoriColors.ink2)
            } else if !store.allowBYOK {
                Text("Paid plans")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.disabled)
            }
        }
    }

    @ViewBuilder
    private func serviceKeyRow(_ service: ServiceKeyStatus, showChevron: Bool, showDivider: Bool) -> some View {
        NoriRow(
            title: service.displayLabel,
            systemImage: "globe",
            iconTone: .neutral,
            showChevron: showChevron,
            showDivider: showDivider
        ) {
            if service.configured, let masked = service.maskedKeyDisplay {
                Text(masked)
                    .font(NoriTypography.monoSmall)
                    .foregroundStyle(NoriColors.ink2)
            } else if !store.allowBYOK {
                Text("Paid plans")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.disabled)
            }
        }
    }

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        modelLoadError = nil
        keyLoadError = nil
        serviceKeyLoadError = nil
        homeLoadError = nil
        async let keys: Void = store.refreshModelKeysThrowing()
        async let serviceKeys: Void = store.refreshServiceKeysThrowing()
        async let models: Void = store.refreshModelSettings()
        async let home: Void = store.refreshHomeConnectors()
        async let caps: Void = store.refreshCapabilities()
        do { try await models } catch { modelLoadError = error.localizedDescription }
        do { try await keys } catch { keyLoadError = error.localizedDescription }
        do { try await serviceKeys } catch is CancellationError { return } catch { serviceKeyLoadError = error.localizedDescription }
        do { try await home } catch is CancellationError { return } catch { homeLoadError = error.localizedDescription }
        _ = await caps
    }

    private func reloadServiceKeys() async {
        serviceKeyLoadError = nil
        do { try await store.refreshServiceKeysThrowing() }
        catch is CancellationError { return }
        catch { serviceKeyLoadError = error.localizedDescription }
    }

    private func reloadHomeConnectors() async {
        homeLoadError = nil
        do { try await store.refreshHomeConnectors() }
        catch is CancellationError { return }
        catch { homeLoadError = error.localizedDescription }
    }

    private func reloadModels() async {
        do {
            try await store.refreshModelSettings()
            modelLoadError = nil
        } catch {
            modelLoadError = error.localizedDescription
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

    private func commitSelection(_ next: ModelSelection) async {
        do {
            try await commitSelectionThrowing(next)
        } catch {
            saveError = error.localizedDescription
        }
    }

    private func commitSelectionThrowing(_ next: ModelSelection) async throws {
        guard let settings = store.modelSettings else {
            throw NoriAPIError(message: "Reload model settings and try again.")
        }
        isSaving = true
        saveError = nil
        defer { isSaving = false }
        try await store.saveModelSettings(next, expectedRevision: settings.revision)
    }
}

// MARK: - Searchable Model Picker with Top Reasoning Control

private struct ModelPickerView: View {
    @Bindable var store: NoriStore
    let lane: String
    let title: String
    let onCommit: (ModelSelection) async throws -> Void

    @State private var searchQuery = ""
    @State private var isSaving = false
    @State private var pickerError: String?

    private var isWork: Bool { lane == "task" }

    private var settings: ModelSettingsResponse? { store.modelSettings }
    private var selection: ModelSelection { settings?.selection ?? ModelSelection() }
    private var homeOnly: Bool { selection.homeOnly == true }
    private var selectedID: String? { isWork ? selection.taskProfileID : selection.chatProfileID }
    private var selectedEffort: String? { isWork ? selection.taskReasoning : selection.chatReasoning }

    private var laneProfiles: [ModelProfile] {
        (settings?.modelProfiles.profiles ?? []).filter { profile in
            profile.lanes.contains(lane) && (!homeOnly || profile.backend == "home")
        }
    }

    private var selectedProfile: ModelProfile? {
        laneProfiles.first { $0.id == selectedID }
    }

    private var filteredProfiles: [ModelProfile] {
        let needle = searchQuery.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !needle.isEmpty else { return laneProfiles }
        return laneProfiles.filter { profile in
            profile.label.lowercased().contains(needle)
                || profile.model.lowercased().contains(needle)
                || profile.backend.lowercased().contains(needle)
        }
    }

    private var cloudProfiles: [ModelProfile] {
        filteredProfiles.filter { $0.backend != "home" }
    }

    private var homeProfiles: [ModelProfile] {
        filteredProfiles.filter { $0.backend == "home" }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let pickerError {
                    NoriNotice(title: "Could not save model", detail: pickerError, tone: .danger)
                }

                if let efforts = selectedProfile?.supportedReasoning, !efforts.isEmpty {
                    VStack(alignment: .leading, spacing: 0) {
                        NoriSectionHeader("Reasoning")
                        NoriSegmentedTabs(
                            options: [NoriSegmentOption(value: "", label: "Default")] +
                                efforts.map { NoriSegmentOption(value: $0, label: $0.capitalized) },
                            selection: Binding(
                                get: { selectedEffort ?? "" },
                                set: { nextEffort in
                                    var next = selection
                                    let val = nextEffort.isEmpty ? nil : nextEffort
                                    if isWork { next.taskReasoning = val } else { next.chatReasoning = val }
                                    Task { await save(next) }
                                }
                            )
                        )
                    }
                }

                HStack(spacing: 8) {
                    Image(systemName: "magnifyingglass")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(NoriColors.ink3)
                    TextField("Search models…", text: $searchQuery)
                        .textFieldStyle(.plain)
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                    if !searchQuery.isEmpty {
                        Button {
                            searchQuery = ""
                        } label: {
                            Image(systemName: "xmark.circle.fill")
                                .foregroundStyle(NoriColors.ink3)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Clear search")
                    }
                }
                .padding(.horizontal, 12)
                .frame(height: 42)
                .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                        .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                )

                if filteredProfiles.isEmpty {
                    NoriEmptyState(
                        title: searchQuery.isEmpty ? "No models available" : "No models match “\(searchQuery)”",
                        message: searchQuery.isEmpty ? "Add a provider key or pair a home computer." : "Try a different keyword."
                    )
                } else {
                    if !cloudProfiles.isEmpty {
                        VStack(alignment: .leading, spacing: 0) {
                            NoriSectionHeader("Cloud")
                            profileRows(cloudProfiles)
                        }
                    }

                    if !homeProfiles.isEmpty {
                        VStack(alignment: .leading, spacing: 0) {
                            NoriSectionHeader("Home computer")
                            profileRows(homeProfiles)
                        }
                    }
                }

                if isWork && searchQuery.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && (selectedID != nil || !filteredProfiles.isEmpty) {
                    NoriRows {
                        Button {
                            var next = selection
                            next.taskProfileID = nil
                            next.taskReasoning = nil
                            Task { await save(next) }
                        } label: {
                            NoriRow(title: "No Work model", showDivider: false) {
                                if selectedID == nil || selectedID?.isEmpty == true {
                                    Image(systemName: "checkmark")
                                        .font(.system(size: 13, weight: .semibold))
                                        .foregroundStyle(NoriColors.accent)
                                }
                            }
                        }
                        .buttonStyle(.plain)
                        .disabled(isSaving)
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle(title)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
    }

    private func profileRows(_ profiles: [ModelProfile]) -> some View {
        NoriRows {
            ForEach(Array(profiles.enumerated()), id: \.element.id) { index, profile in
                let isLast = index == profiles.count - 1
                let active = profile.id == selectedID
                Button {
                    pick(profile)
                } label: {
                    NoriBrandRow(
                        title: profileLabel(profile),
                        brand: NoriBrandKind.forProvider(provider: profile.backend, model: profile.model, backend: profile.backend),
                        showDivider: !isLast
                    ) {
                        if active {
                            Image(systemName: "checkmark")
                                .font(.system(size: 13, weight: .semibold))
                                .foregroundStyle(NoriColors.accent)
                        }
                    }
                }
                .buttonStyle(.plain)
                .disabled(isSaving)
            }
        }
    }

    private func pick(_ profile: ModelProfile) {
        var next = selection
        let supported = profile.supportedReasoning ?? []
        let current = isWork ? next.taskReasoning : next.chatReasoning
        let resolvedEffort: String? = {
            if let current, supported.contains(current) { return current }
            if let def = profile.defaultReasoning, !def.isEmpty, supported.contains(def) { return def }
            return nil
        }()
        if isWork {
            next.taskProfileID = profile.id
            next.taskReasoning = resolvedEffort
        } else {
            next.chatProfileID = profile.id
            next.chatReasoning = resolvedEffort
        }
        Task { await save(next) }
    }

    private func save(_ next: ModelSelection) async {
        isSaving = true
        pickerError = nil
        defer { isSaving = false }
        do {
            try await onCommit(next)
        } catch {
            pickerError = error.localizedDescription
        }
    }

    private func profileLabel(_ profile: ModelProfile) -> String {
        switch profile.availability {
        case "offline": return "\(profile.label) · Offline"
        case "ollama_unavailable": return "\(profile.label) · Ollama unavailable"
        default: return profile.label
        }
    }
}

// MARK: - Provider Key Detail

private struct ProviderKeyDetailView: View {
    @Bindable var store: NoriStore
    let provider: ModelKeyProvider
    let onSaved: (String) -> Void
    let onRemoved: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var keyInput = ""
    @State private var isSaving = false
    @State private var isRemoving = false
    @State private var errorMessage: String?
    @State private var showingRemoveConfirmation = false

    private var liveProvider: ModelKeyProvider {
        store.modelKeys.providers.first { $0.provider == provider.provider } ?? provider
    }

    private var trimmedKey: String {
        keyInput.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var isBusy: Bool { isSaving || isRemoving }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                NoriCard(padding: 16) {
                    VStack(alignment: .leading, spacing: 14) {
                        if liveProvider.configured, let masked = liveProvider.maskedKeyDisplay {
                            HStack {
                                Text("Current key")
                                    .font(NoriTypography.rowTitle)
                                    .foregroundStyle(NoriColors.ink)
                                Spacer()
                                Text(masked)
                                    .font(NoriTypography.monoSmall)
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }

                        VStack(alignment: .leading, spacing: 6) {
                            Text(liveProvider.configured ? "Replace API key" : "API key")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink2)

                            SecureField("Paste your \(liveProvider.label) API key", text: $keyInput)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                .autocorrectionDisabled()
                                #if os(iOS)
                                .textInputAutocapitalization(.never)
                                #endif
                                .disabled(isBusy)
                                .padding(.horizontal, 12)
                                .frame(height: 42)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }

                        Text("Encrypted at rest and never shown again after saving.")
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink3)

                        if let errorMessage, !errorMessage.isEmpty {
                            NoriNotice(title: "Could not update key", detail: errorMessage, tone: .danger)
                        }

                        Button {
                            saveKey()
                        } label: {
                            if isSaving {
                                ProgressView().tint(NoriColors.onAccent)
                            } else {
                                Text("Save")
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                        .disabled(trimmedKey.isEmpty || isBusy)
                    }
                }

                if liveProvider.configured {
                    Button {
                        showingRemoveConfirmation = true
                    } label: {
                        if isRemoving {
                            ProgressView().tint(NoriColors.danger)
                        } else {
                            Text("Remove \(liveProvider.label) key")
                                .frame(maxWidth: .infinity)
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.danger, fullWidth: true))
                    .disabled(isBusy)
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle(liveProvider.label)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .alert("Remove \(liveProvider.label) key?", isPresented: $showingRemoveConfirmation) {
            Button("Remove", role: .destructive) { removeKey() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Models using this key will require another key or model selection.")
        }
    }

    private func saveKey() {
        let keyToSave = trimmedKey
        guard !keyToSave.isEmpty, !isBusy else { return }
        isSaving = true
        errorMessage = nil
        Task {
            defer { isSaving = false }
            do {
                _ = try await store.saveModelKey(provider: liveProvider.provider, apiKey: keyToSave)
                keyInput = ""
                triggerNotificationHaptic(.success)
                onSaved(liveProvider.provider)
                dismiss()
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
        Task {
            defer { isRemoving = false }
            do {
                _ = try await store.deleteModelKey(provider: liveProvider.provider)
                keyInput = ""
                triggerNotificationHaptic(.success)
                onRemoved()
                dismiss()
            } catch {
                errorMessage = error.localizedDescription
                triggerNotificationHaptic(.error)
            }
        }
    }

    #if canImport(UIKit)
    private func triggerNotificationHaptic(_ type: UINotificationFeedbackGenerator.FeedbackType) {
        UINotificationFeedbackGenerator().notificationOccurred(type)
    }
    #else
    private enum StubHapticType { case success, error }
    private func triggerNotificationHaptic(_ type: StubHapticType) {}
    #endif
}

// MARK: - Service Key Detail (BYOK Web Search, e.g. Exa)

private struct ServiceKeyDetailView: View {
    @Bindable var store: NoriStore
    let service: ServiceKeyStatus

    @Environment(\.dismiss) private var dismiss
    @State private var keyInput = ""
    @State private var isSaving = false
    @State private var isRemoving = false
    @State private var errorMessage: String?
    @State private var showingRemoveConfirmation = false

    private var liveService: ServiceKeyStatus {
        store.serviceKeys.services.first { $0.service == service.service } ?? service
    }

    private var trimmedKey: String {
        keyInput.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var isBusy: Bool { isSaving || isRemoving }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if !store.allowBYOK {
                    NoriNotice(
                        title: "Bring-your-own API keys are available on paid plans. Your trial includes hosted credits.",
                        tone: .neutral,
                        systemImage: "info.circle"
                    )
                }

                NoriCard(padding: 16) {
                    VStack(alignment: .leading, spacing: 14) {
                        if liveService.configured, let masked = liveService.maskedKeyDisplay {
                            HStack {
                                Text("Current key")
                                    .font(NoriTypography.rowTitle)
                                    .foregroundStyle(NoriColors.ink)
                                Spacer()
                                Text(masked)
                                    .font(NoriTypography.monoSmall)
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }

                        VStack(alignment: .leading, spacing: 6) {
                            Text(liveService.configured ? "Replace \(liveService.displayLabel) API key" : "\(liveService.displayLabel) API key")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink2)

                            SecureField("Paste your Exa API key", text: $keyInput)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                .autocorrectionDisabled()
                                #if os(iOS)
                                .textInputAutocapitalization(.never)
                                #endif
                                .disabled(isBusy || !store.allowBYOK)
                                .padding(.horizontal, 12)
                                .frame(height: 42)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }

                        Text("Encrypted at rest and never shown again after saving. Uses your own Exa key instead of hosted credits.")
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink3)

                        if let errorMessage, !errorMessage.isEmpty {
                            NoriNotice(title: "Could not update key", detail: errorMessage, tone: .danger)
                        }

                        Button {
                            saveKey()
                        } label: {
                            if isSaving {
                                HStack(spacing: 8) {
                                    ProgressView().tint(NoriColors.onAccent)
                                    Text("Validating…")
                                }
                            } else {
                                Text("Save")
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                        .disabled(trimmedKey.isEmpty || isBusy || !store.allowBYOK)
                    }
                }

                if liveService.configured {
                    Button {
                        showingRemoveConfirmation = true
                    } label: {
                        if isRemoving {
                            ProgressView().tint(NoriColors.danger)
                        } else {
                            Text("Remove \(liveService.displayLabel) key")
                                .frame(maxWidth: .infinity)
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.danger, fullWidth: true))
                    .disabled(isBusy)
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle(liveService.displayLabel)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .alert("Remove \(liveService.displayLabel) key?", isPresented: $showingRemoveConfirmation) {
            Button("Remove", role: .destructive) { removeKey() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Web search will use hosted credits when available.")
        }
    }

    private func saveKey() {
        let keyToSave = trimmedKey
        guard !keyToSave.isEmpty, !isBusy, store.allowBYOK else { return }
        isSaving = true
        errorMessage = nil
        Task {
            defer { isSaving = false }
            do {
                _ = try await store.saveServiceKey(service: liveService.service, apiKey: keyToSave)
                keyInput = ""
                triggerNotificationHaptic(.success)
                dismiss()
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
        Task {
            defer { isRemoving = false }
            do {
                _ = try await store.deleteServiceKey(service: liveService.service)
                keyInput = ""
                triggerNotificationHaptic(.success)
                dismiss()
            } catch {
                errorMessage = error.localizedDescription
                triggerNotificationHaptic(.error)
            }
        }
    }

    #if canImport(UIKit)
    private func triggerNotificationHaptic(_ type: UINotificationFeedbackGenerator.FeedbackType) {
        UINotificationFeedbackGenerator().notificationOccurred(type)
    }
    #else
    private enum StubHapticType { case success, error }
    private func triggerNotificationHaptic(_ type: StubHapticType) {}
    #endif
}
