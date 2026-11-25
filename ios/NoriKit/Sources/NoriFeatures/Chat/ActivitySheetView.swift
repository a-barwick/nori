import SwiftUI
import NoriCore
import NoriDesign

public struct ActivitySheetView: View {
    @Environment(\.dismiss) private var dismiss
    @Bindable private var store: NoriStore
    private let onSelectTask: ((String) -> Void)?
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void
    @State private var isRefreshing = false

    public init(
        store: NoriStore,
        onSelectTask: ((String) -> Void)? = nil,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in }
    ) {
        self.store = store
        self.onSelectTask = onSelectTask
        self.onOpenDocument = onOpenDocument
    }

    private var activeTasks: [WorkItem] {
        store.activeWorkItems
    }

    private var recentTasks: [WorkItem] {
        store.finishedWorkItems
    }

    private var heroMarkState: MarkState {
        (activeTasks.isEmpty && !store.isThinking) ? .idle : .thinking
    }

    private var statusDotColor: Color {
        switch store.connectionState {
        case .reconnecting, .offline:
            return NoriColors.warning
        case .connecting:
            return NoriColors.ink3
        case .connected:
            return NoriColors.accentSignal
        }
    }

    private var heroStatusText: String {
        switch store.connectionState {
        case .reconnecting, .offline:
            return "Connection interrupted"
        case .connecting:
            return "Connecting to Nori…"
        case .connected:
            if store.isThinking && activeTasks.isEmpty {
                return "Thinking…"
            }
            if !activeTasks.isEmpty {
                let n = activeTasks.count
                return "Working on \(n) \(n == 1 ? "task" : "tasks")"
            }
            return "Ready when you are"
        }
    }

    public var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    // Hero NoriMedallion & connection status
                    VStack(spacing: 6) {
                        NoriMark(size: 72, state: heroMarkState)
                        Text("Nori")
                            .font(.system(size: 18, weight: .bold))
                            .foregroundStyle(NoriColors.ink)
                        HStack(spacing: 6) {
                            PulsingStatusDot(
                                color: statusDotColor,
                                size: 7,
                                animated: !activeTasks.isEmpty || store.isThinking
                            )
                            Text(heroStatusText)
                                .font(.system(size: 13))
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.top, 8)
                    .padding(.bottom, 16)

                    // Active Tasks Section
                    NoriSectionHeader("Active tasks") {
                        if !activeTasks.isEmpty {
                            Text("\(activeTasks.count)")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.ink3)
                        }
                    }

                    if activeTasks.isEmpty {
                        NoriCard(padding: 14, backgroundColor: NoriColors.fill1, borderColor: NoriColors.hairline) {
                            Text(store.isThinking
                                 ? "Nori is handling your latest message…"
                                 : "No background tasks running right now.")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                                .frame(maxWidth: .infinity, alignment: .center)
                        }
                    } else {
                        NoriRows {
                            ForEach(Array(activeTasks.enumerated()), id: \.element.id) { idx, item in
                                taskNavigationRow(
                                    item: item,
                                    detail: item.progress ?? "Started \(NoriDateFormatting.relative(item.startedAt))",
                                    showDivider: idx < activeTasks.count - 1
                                ) {
                                    if let amount = item.cost.amountUSD {
                                        NoriPill(
                                            formatUSD(amount),
                                            tone: item.bucket == .needsYou ? .accent : .neutral
                                        )
                                    } else {
                                        let pill = WorkStatusPresentation.pill(for: item)
                                        NoriPill(pill.label, tone: pill.tone)
                                    }
                                }
                            }
                        }
                    }

                    // Recent Tasks Section
                    if !recentTasks.isEmpty {
                        NoriSectionHeader("Recent (\(recentTasks.count))")
                        NoriRows {
                            ForEach(Array(recentTasks.enumerated()), id: \.element.id) { idx, item in
                                let pill = WorkStatusPresentation.pill(for: item)
                                taskNavigationRow(
                                    item: item,
                                    detail: recentTaskDetail(for: item),
                                    showDivider: idx < recentTasks.count - 1
                                ) {
                                    NoriPill(pill.label, tone: pill.tone)
                                }
                            }
                        }
                    }

                    Button {
                        guard !isRefreshing else { return }
                        isRefreshing = true
                        Task {
                            async let sessionTask: Void = store.refreshSession()
                            async let workTask: Void = store.refreshWorkAndInteractions()
                            _ = await (sessionTask, workTask)
                            isRefreshing = false
                        }
                    } label: {
                        HStack(spacing: 6) {
                            if isRefreshing || store.isLoading {
                                ProgressView().controlSize(.mini)
                            }
                            Text(isRefreshing || store.isLoading ? "Refreshing…" : "Refresh activity")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.quiet, fullWidth: true, compact: true))
                    .disabled(isRefreshing || store.isLoading)
                    .padding(.top, 16)
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 24)
            }
            .background(NoriColors.canvas)
            .navigationTitle("Activity")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button {
                        dismiss()
                    } label: {
                        Image(systemName: "xmark")
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(NoriColors.ink)
                            .frame(width: 30, height: 30)
                            .background(NoriColors.fill1, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Close Activity")
                }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    @ViewBuilder
    private func taskNavigationRow<Trailing: View>(
        item: WorkItem,
        detail: String,
        showDivider: Bool,
        @ViewBuilder trailing: () -> Trailing
    ) -> some View {
        if let onSelectTask {
            Button {
                dismiss()
                onSelectTask(item.taskID)
            } label: {
                NoriRow(
                    title: item.title,
                    detail: detail,
                    showChevron: true,
                    showDivider: showDivider,
                    trailing: trailing
                )
            }
            .buttonStyle(.plain)
        } else {
            NavigationLink {
                WorkDetailView(
                    store: store,
                    taskID: item.taskID,
                    onOpenDocument: onOpenDocument
                )
            } label: {
                NoriRow(
                    title: item.title,
                    detail: detail,
                    showChevron: true,
                    showDivider: showDivider,
                    trailing: trailing
                )
            }
            .buttonStyle(.plain)
        }
    }

    private func recentTaskDetail(for item: WorkItem) -> String {
        if let finishedAt = item.finishedAt, !finishedAt.isEmpty {
            let relativeFinished = NoriDateFormatting.relative(finishedAt)
            if let dur = NoriDateFormatting.duration(from: item.startedAt, to: finishedAt) {
                return "\(relativeFinished) · \(dur)"
            }
            return relativeFinished
        }
        return "Started \(NoriDateFormatting.relative(item.startedAt))"
    }

    private func formatUSD(_ value: Double) -> String {
        String(format: "$%.2f", value)
    }
}
