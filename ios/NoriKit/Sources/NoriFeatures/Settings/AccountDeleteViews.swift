import SwiftUI
import NoriCore
import NoriDesign

struct DeleteAccountSheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss
    @State private var confirmation = ""
    @State private var errorMessage: String?

    private var expectedEmail: String {
        store.authState.email?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }

    private var confirmationMatches: Bool {
        let trimmed = confirmation.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return false }
        if expectedEmail.isEmpty { return true }
        return trimmed.caseInsensitiveCompare(expectedEmail) == .orderedSame
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Text("Permanently deletes your account and all Nori data, including chats, memories, work, files, schedules, preferences, and saved API keys.")
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink2)
                        .fixedSize(horizontal: false, vertical: true)

                    if store.accountDeletePending {
                        NoriNotice(
                            title: "Account deletion incomplete",
                            detail: "Your account deletion was interrupted. Retry below to finish removing your account.",
                            tone: .warning
                        )
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Type your account email to confirm")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField(expectedEmail.isEmpty ? "you@example.com" : expectedEmail, text: $confirmation)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .autocorrectionDisabled()
                            #if os(iOS)
                            .textInputAutocapitalization(.never)
                            .keyboardType(.emailAddress)
                            #endif
                            .padding(.horizontal, 12)
                            .frame(height: 44)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    if let errorMessage {
                        NoriNotice(title: "Could not delete account", detail: errorMessage, tone: .danger)
                    }

                    Button {
                        errorMessage = nil
                        Task {
                            do {
                                try await store.deleteAccount(
                                    confirmation: confirmation,
                                    resumePending: store.accountDeletePending
                                )
                                dismiss()
                            } catch {
                                errorMessage = error.localizedDescription
                            }
                        }
                    } label: {
                        if store.isDeletingAccount {
                            ProgressView("Deleting account…")
                        } else {
                            Text(store.accountDeletePending ? "Finish deleting account" : "Delete account")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.danger, fullWidth: true))
                    .disabled(!confirmationMatches || store.isDeletingAccount)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Delete account")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(store.isDeletingAccount)
                }
            }
            .interactiveDismissDisabled(store.isDeletingAccount)
        }
    }
}

public struct AccountDeleteRecoveryView: View {
    @Bindable private var store: NoriStore
    @State private var confirmationText = ""
    @State private var errorMessage: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var expectedEmail: String {
        store.authState.email?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }

    private var confirmationMatches: Bool {
        let entered = confirmationText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !entered.isEmpty else { return false }
        if expectedEmail.isEmpty { return true }
        return entered.caseInsensitiveCompare(expectedEmail) == .orderedSame
    }

    public var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    Text("Your account deletion was interrupted. Enter your account email to finish removing your account and data.")
                        .font(.system(size: 15))
                        .foregroundStyle(NoriColors.ink2)

                    TextField(expectedEmail.isEmpty ? "Account email" : expectedEmail, text: $confirmationText)
                        .textFieldStyle(.plain)
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        #if os(iOS)
                        .textInputAutocapitalization(.never)
                        .keyboardType(.emailAddress)
                        #endif
                        .autocorrectionDisabled()
                        .padding(.horizontal, 12)
                        .frame(height: 44)
                        .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        .overlay(
                            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                        )
                        .disabled(store.isDeletingAccount)

                    if let errorMessage, !errorMessage.isEmpty {
                        NoriNotice(title: "Could not delete account", detail: errorMessage, tone: .danger)
                    }

                    Button {
                        errorMessage = nil
                        Task {
                            do {
                                try await store.deleteAccount(
                                    confirmation: confirmationText.trimmingCharacters(in: .whitespacesAndNewlines),
                                    resumePending: true
                                )
                            } catch {
                                errorMessage = error.localizedDescription
                            }
                        }
                    } label: {
                        if store.isDeletingAccount {
                            ProgressView("Deleting account…")
                        } else {
                            Text("Finish deleting account")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.danger, fullWidth: true))
                    .disabled(!confirmationMatches || store.isDeletingAccount)

                    Button("Sign out") {
                        Task { await store.signOut() }
                    }
                    .buttonStyle(NoriButtonStyle(.quiet, fullWidth: true))
                    .disabled(store.isDeletingAccount)
                }
                .padding(20)
            }
            .background(NoriColors.canvas)
            .navigationTitle("Finish deleting account")
        }
    }
}
