import SwiftUI
import NoriCore
import NoriDesign

struct DataResetLoadingView: View {
    var body: some View {
        VStack(spacing: 12) {
            ProgressView()
                .controlSize(.large)
                .tint(NoriColors.accent)
            Text("Resetting data…")
                .font(NoriTypography.meta)
                .foregroundStyle(NoriColors.ink2)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(NoriColors.canvas.ignoresSafeArea())
    }
}

struct DataResetRecoveryView: View {
    @Environment(\.scenePhase) private var scenePhase
    @Bindable var store: NoriStore
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    var body: some View {
        Group {
            if store.isResettingData {
                DataResetLoadingView()
            } else {
                VStack(alignment: .leading, spacing: 20) {
                    Text("Finish your data reset")
                        .font(.system(size: 26, weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                    Text("Your data reset needs to finish before you can continue.")
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink2)
                    if let errorMessage {
                        NoriNotice(title: "Could not finish reset", detail: errorMessage, tone: .danger)
                    }
                    Button("Finish reset") {
                        isSubmitting = true
                        errorMessage = nil
                        Task {
                            do {
                                try await store.finishPendingDataReset()
                            } catch {
                                errorMessage = error.localizedDescription
                            }
                            isSubmitting = false
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(isSubmitting)

                    Button("Sign out") {
                        Task { await store.signOut() }
                    }
                    .buttonStyle(NoriButtonStyle(.quiet, fullWidth: true))
                    .disabled(isSubmitting)
                }
                .padding(24)
                .frame(maxWidth: 480)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(NoriColors.canvas.ignoresSafeArea())
            }
        }
        .task { await refreshStatus() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await refreshStatus() } }
        }
    }

    private func refreshStatus() async {
        guard !store.isResettingData else { return }
        do {
            try await store.refreshDataResetStatus()
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

struct DataResetStatusView: View {
    @Bindable var store: NoriStore

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            Text("Reset status")
                .font(.system(size: 26, weight: .bold))
                .foregroundStyle(NoriColors.ink)
            Text("Reset isn’t active right now. Start a reset from More → Account.")
                .font(NoriTypography.body)
                .foregroundStyle(NoriColors.ink2)
            Button("Continue") { store.acknowledgeDataResetStatus() }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
            Button("Sign out") { Task { await store.signOut() } }
                .buttonStyle(NoriButtonStyle(.quiet, fullWidth: true))
        }
        .padding(24)
        .frame(maxWidth: 480)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(NoriColors.canvas.ignoresSafeArea())
    }
}
