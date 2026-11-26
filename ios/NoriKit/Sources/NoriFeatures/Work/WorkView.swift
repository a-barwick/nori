import SwiftUI
import NoriCore
import NoriDesign


public enum WorkTabFilter: String, Sendable, Hashable, CaseIterable {
    case tasks
    case artifacts
    case goals
    case routines
}

public typealias WorkSegment = WorkTabFilter


enum WorkStatusPresentation {
    struct PillInfo {
        let label: String
        let tone: NoriTone
    }

    static func pill(for item: WorkItem) -> PillInfo {
        if item.cancelRequested && item.status == .running {
            return PillInfo(label: "Stopping", tone: .neutral)
        }
        if item.bucket == .needsYou && item.status == .running {
            return PillInfo(label: "Action needed", tone: .warning)
        }
        switch item.status {
        case .running:
            return item.operations.uncertain > 0
                ? PillInfo(label: "Checking", tone: .warning)
                : PillInfo(label: "Working", tone: .working)
        case .completed:
            return item.operations.uncertain > 0
                ? PillInfo(label: "Unconfirmed", tone: .warning)
                : PillInfo(label: "Done", tone: .ok)
        case .failed:
            let isIncomplete = item.isBudgetExhausted
                || (item.error?.message.lowercased().hasPrefix("incomplete") == true)
            return PillInfo(label: isIncomplete ? "Incomplete" : "Failed", tone: .danger)
        case .cancelled:
            return PillInfo(label: "Stopped", tone: .neutral)
        }
    }
}


public struct WorkView: View {
    @Bindable private var store: NoriStore
    private let externalSelection: Binding<WorkTabFilter>?
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void
    private let onOpenTask: ((String) -> Void)?
    private let onStartInChat: (String) -> Void

    @State private var internalTab: WorkTabFilter = .tasks
    @State private var selectedTaskDestination: String?
    @State private var didApplyInitialDefaultTab: Bool = false
    @State private var showAllRecentTasks: Bool = false
    @State private var isResolvedExpanded: Bool = false

    public init(
        store: NoriStore,
        selectedTab: Binding<WorkTabFilter>? = nil,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in },
        onOpenTask: ((String) -> Void)? = nil,
        onStartInChat: @escaping (String) -> Void = { _ in }
    ) {
        self.store = store
        self.externalSelection = selectedTab
        self.onOpenDocument = onOpenDocument
        self.onOpenTask = onOpenTask
        self.onStartInChat = onStartInChat
    }

    private var activeTabBinding: Binding<WorkTabFilter> {
        externalSelection ?? $internalTab
    }

    private var activeTab: WorkTabFilter {
        activeTabBinding.wrappedValue
    }

    private var tabOptions: [NoriSegmentOption<WorkTabFilter>] {
        [
            NoriSegmentOption(
                value: .tasks,
                label: "Tasks",
                count: store.needsYouCount > 0 ? store.needsYouCount : nil
            ),
            NoriSegmentOption(value: .artifacts, label: "Artifacts"),
            NoriSegmentOption(value: .goals, label: "Goals"),
            NoriSegmentOption(value: .routines, label: "Routines")
        ]
    }

    private var pendingInteractions: [Interaction] {
        store.pendingInteractions
    }

    private var resolvedInteractions: [Interaction] {
        store.interactions.filter { $0.status != .pending }
    }

    private var orphanNeedsYouTasks: [WorkItem] {
        let pendingTaskIDs = Set(pendingInteractions.compactMap(\.taskID))
        return store.workItems.filter { item in
            item.bucket == .needsYou && !pendingTaskIDs.contains(item.taskID)
        }
    }

    public var body: some View {
        VStack(spacing: 0) {
            headerSection

            Group {
                switch activeTab {
                case .tasks:
                    tasksContent
                case .artifacts:
                    LibraryView(store: store, onOpenDocument: onOpenDocument)
                case .goals:
                    GoalsView(
                        store: store,
                        onJumpToNeedsYou: {
                            withAnimation(.easeInOut(duration: 0.18)) {
                                activeTabBinding.wrappedValue = .tasks
                            }
                        },
                        onStartInChat: onStartInChat
                    )
                case .routines:
                    RoutinesView(store: store)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .onAppear {
            applySmartDefaultTabIfNeeded()
        }
        .navigationDestination(item: $selectedTaskDestination) { taskID in
            WorkDetailView(
                store: store,
                taskID: taskID,
                onOpenDocument: onOpenDocument
            )
        }
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
    }


    private var headerSection: some View {
        VStack(alignment: .leading, spacing: 0) {
            NoriUnderlineTabs(options: tabOptions, selection: activeTabBinding)
                .padding(.top, 4)
        }
        .background(
            NoriTopWash()
                .ignoresSafeArea(edges: .top)
        )
    }


    private var tasksContent: some View {
        let pending = pendingInteractions
        let orphans = orphanNeedsYouTasks
        let needsYouTotal = pending.count + orphans.count
        let pendingTaskIDs = Set(pending.compactMap(\.taskID))
        let active = store.activeWorkItems.filter { item in
            item.bucket != .needsYou && !pendingTaskIDs.contains(item.taskID)
        }
        let finished = store.finishedWorkItems
        let resolved = resolvedInteractions
        let visibleRecent = showAllRecentTasks ? finished : Array(finished.prefix(5))
        let isAllClear = pending.isEmpty && orphans.isEmpty && active.isEmpty

        return ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                if needsYouTotal > 0 {
                    NoriSectionHeader("Action needed · \(needsYouTotal)")
                    VStack(spacing: 12) {
                        ForEach(pending) { interaction in
                            InteractionCardView(
                                store: store,
                                interaction: interaction,
                                onOpenTask: navigateToTask
                            )
                        }

                        if !orphans.isEmpty {
                            NoriRows {
                                ForEach(Array(orphans.enumerated()), id: \.element.id) { idx, item in
                                    workRow(item: item, showDivider: idx < orphans.count - 1)
                                }
                            }
                        }
                    }
                    .padding(.bottom, 6)
                }

                if isAllClear {
                    allClearCard
                        .padding(.top, 4)
                        .padding(.bottom, 8)
                }

                if !active.isEmpty {
                    NoriSectionHeader(active.count > 1 ? "Running · \(active.count)" : "Running")
                    VStack(spacing: 10) {
                        ForEach(active) { item in
                            runningTaskCard(item: item)
                        }
                    }
                    .padding(.bottom, 6)
                }

                if !finished.isEmpty {
                    NoriSectionHeader("Recent")
                    NoriRows {
                        ForEach(Array(visibleRecent.enumerated()), id: \.element.id) { idx, item in
                            workRow(item: item, showDivider: idx < visibleRecent.count - 1)
                        }
                    }

                    if finished.count > 5 {
                        Button {
                            withAnimation(.easeInOut(duration: 0.2)) {
                                showAllRecentTasks.toggle()
                            }
                        } label: {
                            HStack(spacing: 5) {
                                Text(showAllRecentTasks ? "Show fewer" : "Show all (\(finished.count))")
                                Image(systemName: showAllRecentTasks ? "chevron.up" : "chevron.down")
                                    .font(.system(size: 10.5, weight: .semibold))
                            }
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(NoriColors.ink2)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .padding(.top, 2)
                    }
                }

                if !resolved.isEmpty {
                    recentlyResolvedSection(resolved)
                        .padding(.top, 14)
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .refreshable {
            await store.refreshWorkAndInteractions()
        }
    }


    private var allClearCard: some View {
        HStack(alignment: .center, spacing: 12) {
            NoriMark(size: 22, state: .idle)
                .frame(width: 32, height: 32)
                .background(
                    NoriColors.elevated,
                    in: RoundedRectangle(cornerRadius: 10, style: .continuous)
                )

            Text("All clear")
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(NoriColors.ink)

            Spacer(minLength: 0)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            NoriColors.surface,
            in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
    }

    private func runningTaskCard(item: WorkItem) -> some View {
        let progressText: String = {
            if item.cancelRequested {
                return "Stopping…"
            }
            if let progress = item.progress?.trimmingCharacters(in: .whitespacesAndNewlines), !progress.isEmpty {
                return progress
            }
            return "Started \(NoriDateFormatting.relative(item.startedAt))"
        }()

        return Button {
            navigateToTask(item.taskID)
        } label: {
            HStack(alignment: .center, spacing: 11) {
                PulsingStatusDot(
                    color: item.operations.uncertain > 0 ? NoriColors.warning : NoriColors.markActivity,
                    size: 7.5,
                    animated: !item.cancelRequested
                )

                VStack(alignment: .leading, spacing: 3) {
                    Text(item.title)
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)

                    Text(progressText)
                        .font(.system(size: 12.5))
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                if item.operations.uncertain > 0 {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.system(size: 13))
                        .foregroundStyle(NoriColors.warning)
                        .accessibilityLabel("Has an unconfirmed action")
                }

                Image(systemName: "chevron.right")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(NoriColors.ink3)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 13)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(
                NoriColors.surfaceTinted,
                in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
            )
            .overlay(
                RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                    .strokeBorder(NoriColors.accentBorder, lineWidth: 1)
            )
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private func workRow(item: WorkItem, showDivider: Bool) -> some View {
        let pill = WorkStatusPresentation.pill(for: item)
        let timestampDetail: String = {
            if let finishedAt = item.finishedAt, !finishedAt.isEmpty {
                return "Finished \(NoriDateFormatting.relative(finishedAt))"
            }
            return "Started \(NoriDateFormatting.relative(item.startedAt))"
        }()
        let detail = item.error?.message ?? item.progress ?? timestampDetail

        return Button {
            navigateToTask(item.taskID)
        } label: {
            NoriRow(
                title: item.title,
                detail: detail,
                showChevron: true,
                showDivider: showDivider
            ) {
                HStack(spacing: 6) {
                    if item.operations.uncertain > 0 {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .font(.system(size: 13))
                            .foregroundStyle(NoriColors.warning)
                            .accessibilityLabel("Has an unconfirmed action")
                    }
                    NoriPill(pill.label, tone: pill.tone)
                }
            }
        }
        .buttonStyle(.plain)
    }

    private func recentlyResolvedSection(_ resolved: [Interaction]) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            Button {
                withAnimation(.easeInOut(duration: 0.2)) {
                    isResolvedExpanded.toggle()
                }
            } label: {
                HStack(spacing: 8) {
                    Text("Recently resolved (\(resolved.count))")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(NoriColors.ink2)
                    Spacer(minLength: 8)
                    Image(systemName: "chevron.right")
                        .font(.system(size: 11.5, weight: .semibold))
                        .foregroundStyle(NoriColors.ink3)
                        .rotationEffect(.degrees(isResolvedExpanded ? 90 : 0))
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 11)
                .background(
                    NoriColors.fill1,
                    in: RoundedRectangle(cornerRadius: 13, style: .continuous)
                )
                .overlay(
                    RoundedRectangle(cornerRadius: 13, style: .continuous)
                        .strokeBorder(NoriColors.hairline, lineWidth: 1)
                )
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityHint(isResolvedExpanded ? "Collapses resolved items" : "Expands resolved items")

            if isResolvedExpanded {
                VStack(spacing: 10) {
                    ForEach(resolved) { interaction in
                        InteractionCardView(
                            store: store,
                            interaction: interaction,
                            onOpenTask: navigateToTask
                        )
                    }
                }
                .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
    }


    private func applySmartDefaultTabIfNeeded() {
        guard !didApplyInitialDefaultTab else { return }
        didApplyInitialDefaultTab = true
        guard activeTab == .tasks else { return }

        if store.pendingInteractions.isEmpty
            && store.activeWorkItems.isEmpty
            && store.finishedWorkItems.isEmpty
            && !store.libraryItems.isEmpty {
            activeTabBinding.wrappedValue = .artifacts
        }
    }

    private func navigateToTask(_ taskID: String) {
        if let onOpenTask {
            onOpenTask(taskID)
        } else {
            selectedTaskDestination = taskID
        }
    }
}
