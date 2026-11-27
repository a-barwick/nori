import SwiftUI
import NoriCore
import NoriDesign

public struct InviteRedeemView: View {
    @Bindable private var store: NoriStore
    private let session: AuthSession
    private let onRedeemed: () -> Void
    private let onCancel: () -> Void

    @State private var inviteCode = ""
    @State private var isSubmitting = false
    @State private var errorMessage: String?
    @FocusState private var isCodeFocused: Bool

    public init(
        store: NoriStore,
        session: AuthSession,
        onRedeemed: @escaping () -> Void,
        onCancel: @escaping () -> Void
    ) {
        self.store = store
        self.session = session
        self.onRedeemed = onRedeemed
        self.onCancel = onCancel
    }

    public var body: some View {
        GeometryReader { proxy in
            ScrollView {
                VStack(spacing: 24) {
                    Spacer(minLength: 24)

                    VStack(spacing: 14) {
                        NoriMark(size: 56, state: isSubmitting ? .thinking : .idle)
                        Text("Enter your invite code")
                            .font(NoriTypography.title)
                            .foregroundStyle(NoriColors.ink)
                            .multilineTextAlignment(.center)
                            .accessibilityAddTraits(.isHeader)
                        if !session.email.isEmpty {
                            Text(session.email)
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }

                    if let errorMessage {
                        NoriNotice(
                            title: "Could not redeem invite",
                            detail: errorMessage,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                        .padding(.horizontal, 24)
                    }

                    VStack(spacing: 12) {
                        inviteTextField
                            .font(NoriTypography.mono)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .autocorrectionDisabled()
                            .focused($isCodeFocused)
                            .submitLabel(.continue)
                            .onSubmit(submit)
                            .disabled(isSubmitting)
                            .onChange(of: inviteCode) { _, _ in
                                if errorMessage != nil { errorMessage = nil }
                            }
                            .padding(.horizontal, 14)
                            .frame(minHeight: 48)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    .strokeBorder(
                                        isCodeFocused ? NoriColors.accentSignal : NoriColors.cardBorder,
                                        lineWidth: isCodeFocused ? 1.5 : 1
                                    )
                            )

                        Button {
                            submit()
                        } label: {
                            HStack(spacing: 8) {
                                if isSubmitting {
                                    ProgressView()
                                        .tint(NoriColors.disabled)
                                }
                                Text(isSubmitting ? "Checking…" : "Continue")
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, large: true))
                        .disabled(isSubmitting || inviteCode.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)

                        Button("Use a different account") {
                            onCancel()
                        }
                        .font(NoriTypography.meta)
                        .foregroundStyle(isSubmitting ? NoriColors.disabled : NoriColors.ink2)
                        .disabled(isSubmitting)
                        .padding(.top, 4)
                    }
                    .padding(.horizontal, 24)

                    Spacer(minLength: 24)

                    HStack(spacing: 16) {
                        Link("Privacy Policy", destination: NoriConfig.legalURL(path: "/privacy"))
                        Link("Terms of Service", destination: NoriConfig.legalURL(path: "/terms"))
                    }
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink3)
                    .padding(.bottom, 20)
                }
                .frame(maxWidth: 420)
                .frame(maxWidth: .infinity, minHeight: proxy.size.height)
            }
            .scrollDismissesKeyboard(.interactively)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
    }

    @ViewBuilder
    private var inviteTextField: some View {
        #if os(iOS)
        TextField("NORI-XXXX-XXXX-XXXX", text: $inviteCode)
            .textInputAutocapitalization(.characters)
        #else
        TextField("NORI-XXXX-XXXX-XXXX", text: $inviteCode)
        #endif
    }

    private func friendlyInviteError(_ error: Error) -> String {
        if let apiErr = error as? NoriAPIError {
            if apiErr.statusCode == 429 {
                return "Too many attempts. Wait a moment and try again."
            }
            if apiErr.code == "invite_code_required" {
                return "Enter an invite code."
            }
            if apiErr.statusCode == 400 || apiErr.statusCode == 404 || apiErr.statusCode == 409
                || apiErr.code == "invite_invalid" || apiErr.code == "invite_expired" || apiErr.code == "invite_used" {
                return "That invite code is invalid or has already been used."
            }
            if apiErr.isAuthError {
                return "Your session expired. Use a different account or sign in again."
            }
            if apiErr.statusCode >= 500 {
                return "Nori could not verify your invite code right now. Try again."
            }
            return "Could not verify your invite code. Try again."
        }
        if let urlErr = error as? URLError {
            switch urlErr.code {
            case .notConnectedToInternet, .networkConnectionLost:
                return "You appear to be offline. Check your connection and try again."
            default:
                return "Could not reach Nori. Check your connection and try again."
            }
        }
        return "Could not verify your invite code. Try again."
    }

    private func submit() {
        let trimmed = inviteCode.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            errorMessage = "Enter an invite code."
            return
        }
        errorMessage = nil
        isSubmitting = true
        Task {
            defer { isSubmitting = false }
            do {
                try await store.redeemInvite(code: trimmed, for: session)
                onRedeemed()
            } catch {
                errorMessage = friendlyInviteError(error)
            }
        }
    }
}
