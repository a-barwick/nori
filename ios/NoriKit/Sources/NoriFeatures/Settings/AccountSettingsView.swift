import SwiftUI
import NoriCore
import NoriDesign

public struct AccountSettingsView: View {
    @Bindable private var store: NoriStore
    @State private var showingResetData = false
    @State private var showingDeleteAccount = false

    public init(store: NoriStore) {
        self.store = store
    }

    private func legalURL(path: String) -> URL {
        NoriConfig.legalURL(path: path)
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                if let block = store.activeBillingAccessBlock {
                    BillingAccessBannerView(block: block, allowBYOK: store.allowBYOK)
                }

                NoriRows {
                    NoriRow(
                        title: store.authState.email ?? "Signed in",
                        systemImage: "person.crop.circle.fill",
                        iconTone: .accent,
                        showDivider: store.capabilities?.entitlement != nil
                    ) {
                        if store.connectionState == .reconnecting {
                            NoriPill("Reconnecting", tone: .warning)
                        } else if store.connectionState == .offline {
                            NoriPill("Offline", tone: .danger)
                        }
                    }

                    if let entitlement = store.capabilities?.entitlement {
                        NavigationLink(value: MoreRoute.apiCosts) {
                            NoriRow(
                                title: "Plan",
                                systemImage: "chart.bar",
                                iconTone: .neutral,
                                showChevron: true,
                                showDivider: false
                            ) {
                                Text(entitlement.tierDisplayName)
                                    .font(NoriTypography.meta)
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }
                        .buttonStyle(.plain)
                    }
                }

                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("System")

                    NoriRows {
                        NavigationLink(value: MoreRoute.history) {
                            NoriRow(
                                title: "Action History",
                                systemImage: "clock.arrow.circlepath",
                                iconTone: .neutral,
                                showChevron: true,
                                showDivider: true
                            )
                        }
                        .buttonStyle(.plain)

                        NavigationLink(value: MoreRoute.diagnostics) {
                            NoriRow(
                                title: "Diagnostics",
                                systemImage: "waveform.path.ecg",
                                iconTone: .neutral,
                                showChevron: true,
                                showDivider: false
                            ) {
                                if store.connectionState == .reconnecting {
                                    NoriPill("Reconnecting", tone: .warning)
                                } else if store.connectionState == .offline {
                                    NoriPill("Offline", tone: .danger)
                                }
                            }
                        }
                        .buttonStyle(.plain)
                    }
                }

                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Legal")

                    NoriRows {
                        Link(destination: legalURL(path: "/privacy")) {
                            NoriRow(
                                title: "Privacy Policy",
                                showDivider: true
                            ) {
                                Image(systemName: "arrow.up.right")
                                    .font(.system(size: 12, weight: .semibold))
                                    .foregroundStyle(NoriColors.ink3)
                            }
                        }
                        .buttonStyle(.plain)

                        Link(destination: legalURL(path: "/terms")) {
                            NoriRow(
                                title: "Terms of Service",
                                showDivider: false
                            ) {
                                Image(systemName: "arrow.up.right")
                                    .font(.system(size: 12, weight: .semibold))
                                    .foregroundStyle(NoriColors.ink3)
                            }
                        }
                        .buttonStyle(.plain)
                    }
                }

                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Data")

                    NoriRows {
                        Button {
                            showingResetData = true
                        } label: {
                            NoriRow(
                                title: "Reset data",
                                systemImage: "arrow.counterclockwise",
                                iconTone: .danger,
                                showChevron: true,
                                showDivider: true
                            )
                        }
                        .buttonStyle(.plain)

                        Button {
                            showingDeleteAccount = true
                        } label: {
                            NoriRow(
                                title: store.accountDeletePending ? "Finish deleting account" : "Delete account",
                                systemImage: "person.crop.circle.badge.xmark",
                                iconTone: .danger,
                                showChevron: true,
                                showDivider: false
                            )
                        }
                        .buttonStyle(.plain)
                    }
                }

                Button {
                    Task { await store.signOut() }
                } label: {
                    Text("Sign Out")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(NoriButtonStyle(.quiet, fullWidth: true, compact: true))
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .refreshable {
            await store.refreshSummary()
        }
        .background(NoriColors.canvas)
        .navigationTitle("Account")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .sheet(isPresented: $showingResetData) {
            ResetDataSheet(store: store)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        .sheet(isPresented: $showingDeleteAccount) {
            DeleteAccountSheet(store: store)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
    }
}

struct ResetDataSheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss
    @State private var confirmation = ""
    @State private var isResetting = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Text("This permanently clears your chats, memories, work, files, schedules, preferences, model selections, and saved API keys from Nori and disconnects your apps. You’ll be signed out and start onboarding again when you sign back in.")
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink2)
                        .fixedSize(horizontal: false, vertical: true)

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Type RESET to confirm")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("RESET", text: $confirmation)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .autocorrectionDisabled()
                            #if os(iOS)
                            .textInputAutocapitalization(.characters)
                            #endif
                            .disabled(isResetting)
                            .padding(.horizontal, 12)
                            .frame(height: 44)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    if let errorMessage {
                        NoriNotice(title: "Could not reset data", detail: errorMessage, tone: .danger)
                    }

                    Button {
                        let confirmed = confirmation
                        isResetting = true
                        errorMessage = nil
                        Task {
                            do {
                                try await store.resetUserData(confirmation: confirmed)
                                isResetting = false
                                dismiss()
                            } catch {
                                isResetting = false
                                if store.dataResetRecoveryRequired {
                                    dismiss()
                                } else {
                                    errorMessage = error.localizedDescription
                                }
                            }
                        }
                    } label: {
                        if isResetting {
                            ProgressView().tint(NoriColors.danger)
                        } else {
                            Text("Reset data")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.danger, fullWidth: true))
                    .disabled(confirmation != "RESET" || isResetting)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Reset data")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(isResetting)
                }
            }
            .interactiveDismissDisabled(isResetting)
        }
    }
}
