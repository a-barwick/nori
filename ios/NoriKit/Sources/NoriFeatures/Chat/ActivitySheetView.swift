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
        Array(store.finishedWorkItems.prefix(8))
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
            return NoriColors.markActivity
        }
    }

    private var statusSummaryText: String {
        switch store.connectionState {
        case .reconnecting, .offline:
            return "Connection interrupted"
        case .connecting:
            return "Connecting…"
        case .connected:
            if store.isThinking && activeTasks.isEmpty {
                return "Thinking…"
            }
            if !activeTasks.isEmpty {
                let n = activeTasks.count
                return "\(n) \(n == 1 ? "task" : "tasks") running"
            }
            return "All quiet"
        }
    }

    public var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    HStack(spacing: 12) {
                        NoriMark(size: 34, state: heroMarkState)
                        HStack(spacing: 6) {
                            PulsingStatusDot(
                                color: statusDotColor,
                                size: 7,
                                animated: !activeTasks.isEmpty || store.isThinking
                            )
                            Text(statusSummaryText)
                                .font(.system(size: 14, weight: .semibold))
                                .foregroundStyle(NoriColors.ink)
                        }
                        Spacer(minLength: 8)
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                            .strokeBorder(NoriColors.hairline, lineWidth: 1)
                    )
                    .padding(.top, 6)
                    .padding(.bottom, 4)

                    NoriSectionHeader("Active") {
                        if !activeTasks.isEmpty {
                            Text("\(activeTasks.count)")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.ink3)
                        }
                    }

                    if activeTasks.isEmpty {
                        NoriCard(padding: 13, backgroundColor: NoriColors.fill1, borderColor: NoriColors.hairline) {
                            Text(store.isThinking
                                 ? "Handling your latest message…"
                                 : "No background tasks running.")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    } else {
                        NoriRows {
                            ForEach(Array(activeTasks.enumerated()), id: \.element.id) { idx, item in
                                taskNavigationRow(
                                    item: item,
                                    detail: activeTaskDetail(for: item),
                                    showDivider: idx < activeTasks.count - 1
                                ) {
                                    activeTaskTrailing(for: item)
                                }
                            }
                        }
                    }

                    if !recentTasks.isEmpty {
                        NoriSectionHeader("Recent")
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
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 24)
            }
            .background(NoriColors.sheetBackground)
            .refreshable {
                await refreshActivity()
            }
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
                            .background(NoriColors.elevated, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Close Activity")
                }

                ToolbarItem(placement: .primaryAction) {
                    Button {
                        Task { await refreshActivity() }
                    } label: {
                        Group {
                            if isRefreshing || store.isLoading {
                                ProgressView().controlSize(.mini)
                            } else {
                                Image(systemName: "arrow.clockwise")
                                    .font(.system(size: 13, weight: .semibold))
                                    .foregroundStyle(NoriColors.ink)
                            }
                        }
                        .frame(width: 30, height: 30)
                        .background(NoriColors.elevated, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .disabled(isRefreshing || store.isLoading)
                    .accessibilityLabel("Refresh activity")
                }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .presentationBackground(NoriColors.sheetBackground)
        .presentationCornerRadius(NoriRadius.sheet)
    }

    private func refreshActivity() async {
        guard !isRefreshing else { return }
        isRefreshing = true
        async let sessionTask: Void = store.refreshSession()
        async let workTask: Void = store.refreshWorkAndInteractions()
        _ = await (sessionTask, workTask)
        isRefreshing = false
    }

    private func activeTaskDetail(for item: WorkItem) -> String {
        if let progress = item.progress?.trimmingCharacters(in: .whitespacesAndNewlines), !progress.isEmpty {
            return progress
        }
        return "Started \(NoriDateFormatting.relative(item.startedAt))"
    }

    @ViewBuilder
    private func activeTaskTrailing(for item: WorkItem) -> some View {
        if item.cancelRequested {
            NoriPill("Stopping", tone: .neutral)
        } else if item.bucket == .needsYou {
            NoriPill("Action needed", tone: .warning)
        } else if item.operations.uncertain > 0 {
            NoriPill("Checking", tone: .warning)
        } else if let progress = item.progress?.trimmingCharacters(in: .whitespacesAndNewlines), !progress.isEmpty {
            Text(NoriDateFormatting.relative(item.startedAt))
                .font(.system(size: 11.5, weight: .medium))
                .foregroundStyle(NoriColors.ink3)
        }
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
}
