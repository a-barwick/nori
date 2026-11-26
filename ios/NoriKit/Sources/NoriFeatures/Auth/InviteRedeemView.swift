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
        VStack(spacing: 24) {
            Spacer()

            VStack(spacing: 14) {
                NoriMark(size: 52, state: isSubmitting ? .thinking : .idle)
                Text("Enter your invite code")
                    .font(NoriTypography.title)
                    .foregroundStyle(NoriColors.ink)
                    .multilineTextAlignment(.center)
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
                    .autocorrectionDisabled()
                    .disabled(isSubmitting)
                    .padding(.horizontal, 14)
                    .frame(height: 48)
                    .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                            .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                    )

                Button {
                    submit()
                } label: {
                    HStack(spacing: 8) {
                        if isSubmitting {
                            ProgressView()
                                .tint(.white)
                        }
                        Text(isSubmitting ? "Checking…" : "Continue")
                            .font(NoriTypography.section)
                    }
                    .frame(maxWidth: .infinity)
                    .frame(height: 48)
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(isSubmitting || inviteCode.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)

                Button("Use a different account") {
                    onCancel()
                }
                .font(NoriTypography.meta)
                .foregroundStyle(NoriColors.ink2)
                .disabled(isSubmitting)
                .padding(.top, 4)
            }
            .padding(.horizontal, 24)

            Spacer()

            HStack(spacing: 16) {
                Link("Privacy Policy", destination: NoriConfig.legalURL(path: "/privacy"))
                Link("Terms of Service", destination: NoriConfig.legalURL(path: "/terms"))
            }
            .font(NoriTypography.meta)
            .foregroundStyle(NoriColors.ink3)
            .padding(.bottom, 20)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(NoriColors.canvas)
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
                errorMessage = error.localizedDescription
            }
        }
    }
}
