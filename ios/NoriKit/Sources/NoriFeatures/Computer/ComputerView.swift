import SwiftUI
import NoriCore
import NoriDesign

public struct ComputerView: View {
    @Bindable private var store: NoriStore

    @State private var isLoading = false
    @State private var isBusy = false
    @State private var showingDesktop = false
    @State private var desktopInitialMode: ComputerSessionMode = .control
    @State private var checkpointLabel = ""
    @State private var showingResetConfirm = false
    @State private var errorMessage: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var status: ComputerStatus? {
        store.computerStatus
    }

    private var stateTone: NoriTone {
        switch status?.status {
        case .running: return .ok
        case .provisioning: return .warning
        case .error: return .danger
        default: return .neutral
        }
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                if let errorMessage {
                    NoriNotice(title: errorMessage, tone: .danger, systemImage: "exclamationmark.triangle.fill")
                        .padding(.bottom, 12)
                }

                if let status, !status.enabled {
                    NoriEmptyState(
                        title: "Nori Computer isn’t enabled yet",
                        message: "When enabled, Nori gets a private Linux workspace and browser you can watch or take over anytime."
                    )
                } else {
                    statusCard
                        .padding(.bottom, 14)

                    NoriSectionHeader("Checkpoints")
                    checkpointsCard
                        .padding(.bottom, 14)

                    NoriSectionHeader("Maintenance")
                    maintenanceCard
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .navigationTitle("Computer")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .task {
            isLoading = true
            _ = await store.refreshComputerStatus()
            isLoading = false
        }
        .refreshable {
            _ = await store.refreshComputerStatus()
        }
        .sheet(isPresented: $showingDesktop) {
            ComputerDesktopSheet(
                store: store,
                initialMode: desktopInitialMode
            )
            .presentationDetents([.large])
            .presentationDragIndicator(.visible)
            .presentationBackground(NoriColors.sheetBackground)
            .presentationCornerRadius(NoriRadius.sheet)
        }
        .confirmationDialog(
            "Reset your computer?",
            isPresented: $showingResetConfirm,
            titleVisibility: .visible
        ) {
            Button("Reset Computer", role: .destructive) {
                performAction { _ = try await store.resetComputer() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This clears installed tools, workspace files, and saved browser sign-ins. Files saved in Library are unaffected.")
        }
    }

    private var statusCard: some View {
        let state = status?.status ?? .notProvisioned
        return NoriCard(padding: 14) {
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 10) {
                    Image(systemName: "desktopcomputer")
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(NoriColors.accent)
                        .frame(width: 36, height: 36)
                        .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                    VStack(alignment: .leading, spacing: 2) {
                        HStack(spacing: 8) {
                            Text("Personal Workspace")
                                .font(.system(size: 15, weight: .semibold))
                                .foregroundStyle(NoriColors.ink)
                            NoriPill(isLoading && status == nil ? "Checking…" : state.displayLabel, tone: stateTone)
                        }
                        Text("Your files, installed tools, and browser sign-ins persist across sleep and wake.")
                            .font(.system(size: 13))
                            .foregroundStyle(NoriColors.ink2)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }

                if let status, status.diskMaxBytes > 0 {
                    VStack(alignment: .leading, spacing: 6) {
                        HStack {
                            Text("Storage")
                                .font(.system(size: 12.5))
                                .foregroundStyle(NoriColors.ink2)
                            Spacer()
                            Text("\(formatBytes(status.diskUsedBytes)) / \(formatBytes(status.diskMaxBytes))")
                                .font(.system(size: 12.5, design: .monospaced))
                                .foregroundStyle(NoriColors.ink2)
                        }
                        GeometryReader { geo in
                            ZStack(alignment: .leading) {
                                Capsule().fill(NoriColors.hairline)
                                Capsule()
                                    .fill(NoriColors.accent)
                                    .frame(width: max(4, geo.size.width * status.diskUsageRatio))
                            }
                        }
                        .frame(height: 6)
                    }
                }

                HStack(spacing: 8) {
                    Button {
                        desktopInitialMode = .control
                        showingDesktop = true
                    } label: {
                        HStack(spacing: 5) {
                            Image(systemName: "desktopcomputer")
                                .font(.system(size: 12, weight: .semibold))
                            Text("Open Desktop")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                    .disabled(isBusy)

                    if state == .running {
                        Button("Sleep") {
                            performAction { try await store.sleepComputer() }
                        }
                        .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                        .disabled(isBusy)
                    } else {
                        Button("Wake") {
                            performAction { _ = try await store.wakeComputer() }
                        }
                        .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                        .disabled(isBusy)
                    }
                }
            }
        }
    }

    private var checkpointsCard: some View {
        let checkpoints = status?.checkpoints ?? []
        return NoriCard(padding: 14) {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    TextField("Optional label (e.g. Before upgrades)", text: $checkpointLabel)
                        .font(.system(size: 14))
                        .foregroundStyle(NoriColors.ink)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        .background(NoriColors.canvas, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                        .overlay(
                            RoundedRectangle(cornerRadius: 10, style: .continuous)
                                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                        )

                    Button("Save") {
                        let label = checkpointLabel.trimmingCharacters(in: .whitespacesAndNewlines)
                        performAction {
                            _ = try await store.createComputerCheckpoint(label: label.isEmpty ? nil : label)
                            checkpointLabel = ""
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                    .disabled(isBusy)
                }

                if checkpoints.isEmpty {
                    Text("No saved checkpoints yet. Save one before installing major tools or changing setup.")
                        .font(.system(size: 13))
                        .foregroundStyle(NoriColors.ink2)
                } else {
                    VStack(spacing: 0) {
                        ForEach(Array(checkpoints.enumerated()), id: \.element.id) { idx, ckpt in
                            HStack(spacing: 10) {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(ckpt.label?.isEmpty == false ? ckpt.label! : ckpt.id)
                                        .font(.system(size: 14, weight: .medium))
                                        .foregroundStyle(NoriColors.ink)
                                    if let createdAt = ckpt.createdAt, !createdAt.isEmpty {
                                        Text(NoriDateFormatting.shortDateTime(createdAt))
                                            .font(.system(size: 12))
                                            .foregroundStyle(NoriColors.ink3)
                                    }
                                }
                                Spacer()
                                Button("Restore") {
                                    performAction {
                                        _ = try await store.restoreComputerCheckpoint(checkpointID: ckpt.id)
                                    }
                                }
                                .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                                .disabled(isBusy)
                            }
                            .padding(.vertical, 8)
                            if idx < checkpoints.count - 1 {
                                Rectangle().fill(NoriColors.hairline).frame(height: 1)
                            }
                        }
                    }
                }
            }
        }
    }

    private var maintenanceCard: some View {
        NoriRows {
            Button {
                showingResetConfirm = true
            } label: {
                NoriRow(
                    title: "Reset Computer",
                    detail: "Restore a clean workspace and clear saved browser sign-ins.",
                    systemImage: "arrow.counterclockwise",
                    iconTone: .danger,
                    showChevron: true,
                    showDivider: false
                )
            }
            .buttonStyle(.plain)
            .disabled(isBusy)
        }
    }

    private func performAction(_ work: @escaping () async throws -> Void) {
        errorMessage = nil
        isBusy = true
        Task {
            do {
                try await work()
            } catch {
                errorMessage = error.localizedDescription
            }
            isBusy = false
        }
    }

    private func formatBytes(_ bytes: Int64) -> String {
        if bytes <= 0 { return "0 MB" }
        let gb = Double(bytes) / Double(1024 * 1024 * 1024)
        if gb >= 1.0 {
            return String(format: "%.1f GB", gb)
        }
        let mb = max(1, Int(round(Double(bytes) / Double(1024 * 1024))))
        return "\(mb) MB"
    }
}
