import SwiftUI
import NoriCore
import NoriDesign

public struct ActionHistoryView: View {
    @Bindable private var store: NoriStore
    @State private var selectedConnectionID: String?
    @State private var operations: [OperationProjection] = []
    @State private var hasLoaded = false
    @State private var isLoading = false
    @State private var loadError: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var displayedOperations: [OperationProjection] {
        if !hasLoaded && selectedConnectionID == nil && operations.isEmpty && !store.operations.isEmpty {
            return store.operations
        }
        return operations
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if !store.connections.isEmpty {
                    connectionFilterBar
                }

                if let loadError {
                    NoriNotice(title: "Could not load action history", detail: loadError, tone: .danger) {
                        Button("Retry") {
                            Task { await reload() }
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    }
                }

                if isLoading && displayedOperations.isEmpty {
                    NoriCard {
                        HStack(spacing: 10) {
                            ProgressView().tint(NoriColors.accent)
                            Text("Loading action history…")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                } else if displayedOperations.isEmpty {
                    NoriEmptyState(
                        title: "No actions yet",
                        message: selectedConnectionID == nil
                            ? "External actions Nori takes across your connected apps appear here."
                            : "No actions have been recorded for this account yet."
                    )
                } else {
                    NoriRows {
                        ForEach(Array(displayedOperations.enumerated()), id: \.element.id) { index, op in
                            operationRow(op, showDivider: index < displayedOperations.count - 1)
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle("Action History")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .task(id: selectedConnectionID) {
            await reload()
        }
        .refreshable {
            await reload()
        }
    }

    private var connectionFilterBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                filterChip(title: "All accounts", isSelected: selectedConnectionID == nil) {
                    selectedConnectionID = nil
                }
                ForEach(store.connections) { connection in
                    let label = connection.account?.label ?? connection.title
                    filterChip(title: label, isSelected: selectedConnectionID == connection.id) {
                        selectedConnectionID = connection.id
                    }
                }
            }
        }
    }

    private func filterChip(title: String, isSelected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(NoriTypography.caption)
                .foregroundStyle(isSelected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                .padding(.horizontal, 12)
                .frame(height: 30)
                .background(
                    isSelected ? NoriColors.chipSelectedBg : NoriColors.fill1,
                    in: Capsule()
                )
                .overlay(
                    Capsule()
                        .strokeBorder(
                            isSelected ? NoriColors.chipSelectedBorder : NoriColors.cardBorder,
                            lineWidth: 1
                        )
                )
        }
        .buttonStyle(.plain)
    }

    private func operationRow(_ op: OperationProjection, showDivider: Bool) -> some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .top, spacing: 8) {
                    Text(op.displaySummary)
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    if let badge = op.statusBadge {
                        NoriPill(badge.label, tone: badge.tone.noriTone)
                    }
                }

                let metaParts = metadataParts(for: op)
                if !metaParts.isEmpty {
                    Text(metaParts.joined(separator: " · "))
                        .font(NoriTypography.caption)
                        .foregroundStyle(NoriColors.ink2)
                        .fixedSize(horizontal: false, vertical: true)
                }

                if let err = op.error?.message, !err.isEmpty {
                    Text(err)
                        .font(NoriTypography.caption)
                        .foregroundStyle(NoriColors.danger)
                        .fixedSize(horizontal: false, vertical: true)
                }

                if let receipt = op.receiptURL,
                   let url = URL(string: receipt),
                   url.scheme?.lowercased() == "https" {
                    Link(destination: url) {
                        HStack(spacing: 4) {
                            Text("Open")
                            Image(systemName: "arrow.up.right")
                                .font(.caption2.weight(.semibold))
                        }
                        .font(NoriTypography.caption)
                        .foregroundStyle(NoriColors.accent)
                    }
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .padding(.leading, 14)
            }
        }
    }

    private func metadataParts(for op: OperationProjection) -> [String] {
        var parts: [String] = []
        if selectedConnectionID == nil, let account = op.accountSummary, !account.isEmpty {
            parts.append(account)
        }
        if let authority = op.displayAuthorityLabel, !authority.isEmpty {
            parts.append(authority)
        }
        if let taskTitle = op.taskTitle, !taskTitle.isEmpty {
            parts.append(taskTitle)
        }
        if let timestamp = op.updatedAt ?? op.createdAt, !timestamp.isEmpty {
            parts.append(NoriDateFormatting.relative(timestamp))
        }
        return parts
    }

    private func reload() async {
        isLoading = true
        loadError = nil
        defer { isLoading = false }
        do {
            operations = try await store.refreshOperations(connectionID: selectedConnectionID)
            hasLoaded = true
        } catch is CancellationError {
            return
        } catch {
            loadError = error.localizedDescription
        }
    }
}

private extension OperationBadgeTone {
    var noriTone: NoriTone {
        switch self {
        case .neutral: return .neutral
        case .ok: return .ok
        case .warning: return .warning
        case .danger: return .danger
        }
    }
}
