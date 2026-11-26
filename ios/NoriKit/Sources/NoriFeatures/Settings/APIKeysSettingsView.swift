import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

// MARK: - API Keys Settings Screen

public struct APIKeysSettingsView: View {
    @Bindable private var store: NoriStore
    @State private var isLoading = false
    @State private var loadError: String?

    public init(store: NoriStore) {
        self.store = store
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                if store.needsModelKeySetup {
                    NoriNotice(
                        title: "API key required",
                        detail: "Add a provider API key below before sending messages or running tasks.",
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

                if let loadError, store.modelKeys.providers.isEmpty {
                    NoriNotice(
                        title: "Could not load API keys",
                        detail: loadError,
                        tone: .danger,
                        systemImage: "exclamationmark.triangle.fill"
                    ) {
                        Button("Retry") {
                            Task { await loadKeys() }
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    }
                } else if isLoading && store.modelKeys.providers.isEmpty {
                    NoriCard {
                        HStack(spacing: 10) {
                            ProgressView()
                                .tint(NoriColors.accent)
                            Text("Loading providers…")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }
                } else if store.modelKeys.providers.isEmpty {
                    NoriCard {
                        Text("No model key providers are available on this server.")
                            .font(NoriTypography.meta)
                            .foregroundStyle(NoriColors.ink2)
                    }
                } else {
                    VStack(alignment: .leading, spacing: 12) {
                        ForEach(store.modelKeys.providers) { provider in
                            ModelKeyProviderCard(store: store, provider: provider)
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .task {
            await loadKeys()
        }
        .refreshable {
            await loadKeys()
        }
        .background(NoriColors.canvas)
        .navigationTitle("API Keys")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
    }

    private func loadKeys() async {
        isLoading = true
        defer { isLoading = false }
        loadError = nil
        do {
            try await store.refreshModelKeysThrowing()
        } catch {
            loadError = error.localizedDescription
        }
    }
}

// MARK: - Provider Card

private struct ModelKeyProviderCard: View {
    @Bindable var store: NoriStore
    let provider: ModelKeyProvider

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
                        if let masked = provider.maskedKeyDisplay {
                            Text(masked)
                                .font(NoriTypography.monoSmall)
                                .foregroundStyle(NoriColors.ink2)
                        } else if !provider.configured {
                            Text("No API key configured")
                                .font(.system(size: 12.5))
                                .foregroundStyle(NoriColors.ink3)
                        }
                    }

                    Spacer(minLength: 8)

                    NoriPill(
                        provider.configured ? "Configured" : "Not set",
                        tone: provider.configured ? .ok : .neutral
                    )
                }

                if provider.configured, let timestampSummary {
                    Text(timestampSummary)
                        .font(.system(size: 12))
                        .foregroundStyle(NoriColors.ink3)
                }

                VStack(alignment: .leading, spacing: 6) {
                    Text(provider.configured ? "Replace API key" : "API key")
                        .font(NoriTypography.metaSemibold)
                        .foregroundStyle(NoriColors.ink2)

                    SecureField(
                        provider.configured ? "Paste new key to replace" : "Paste API key",
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
                            Text(provider.configured ? "Replace key" : "Save key")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                    .disabled(trimmedKey.isEmpty || isBusy)

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
                        .buttonStyle(NoriButtonStyle(.danger, compact: true))
                        .disabled(isBusy)
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
                triggerNotificationHaptic(.success)
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
