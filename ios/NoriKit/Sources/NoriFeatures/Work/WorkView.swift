import SwiftUI
import NoriCore
import NoriDesign

// MARK: - Work Tab Filter

public enum WorkTabFilter: String, Sendable, Hashable, CaseIterable {
    case needsYou = "needs_you"
    case working
    case scheduled
    case finished
}

public typealias WorkSegment = WorkTabFilter

// MARK: - Work Status Presentation Helper

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
            return PillInfo(label: "Needs you", tone: .accent)
        }
        switch item.status {
        case .running:
            return item.operations.uncertain > 0
                ? PillInfo(label: "Checking", tone: .warning)
                : PillInfo(label: "Working", tone: .neutral)
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

// MARK: - WorkView

public struct WorkView: View {
    @Bindable private var store: NoriStore
    private let externalSelection: Binding<WorkTabFilter>?
    private let onOpenActivity: () -> Void
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void
    private let onOpenRoutines: () -> Void
    private let onOpenTask: ((String) -> Void)?

    @State private var internalTab: WorkTabFilter = .needsYou
    @State private var selectedTaskDestination: String?

    public init(
        store: NoriStore,
        selectedTab: Binding<WorkTabFilter>? = nil,
        onOpenActivity: @escaping () -> Void = {},
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in },
        onOpenRoutines: @escaping () -> Void = {},
        onOpenTask: ((String) -> Void)? = nil
    ) {
        self.store = store
        self.externalSelection = selectedTab
        self.onOpenActivity = onOpenActivity
        self.onOpenDocument = onOpenDocument
        self.onOpenRoutines = onOpenRoutines
        self.onOpenTask = onOpenTask
    }

    private var activeTabBinding: Binding<WorkTabFilter> {
        externalSelection ?? $internalTab
    }

    private var activeTab: WorkTabFilter {
        activeTabBinding.wrappedValue
    }

    private var enabledAutomations: [Automation] {
        store.automations.filter { $0.status == .enabled }
    }

    private var orderedInteractions: [Interaction] {
        let pending = store.interactions.filter { $0.status == .pending }
        let resolved = store.interactions.filter { $0.status != .pending }
        return pending + resolved
    }

    private var segmentOptions: [NoriSegmentOption<WorkTabFilter>] {
        [
            NoriSegmentOption(value: .needsYou, label: "Needs you", count: store.needsYouCount),
            NoriSegmentOption(value: .working, label: "Working", count: store.activeTasksCount),
            NoriSegmentOption(value: .scheduled, label: "Scheduled", count: enabledAutomations.count),
            NoriSegmentOption(value: .finished, label: "Done", count: nil)
        ]
    }

    public var body: some View {
        VStack(spacing: 0) {
            // Large title header
            HStack(alignment: .center, spacing: 10) {
                Text("Work")
                    .font(.system(size: 26, weight: .bold))
                    .foregroundStyle(NoriColors.ink)
                Spacer(minLength: 8)
                Button(action: onOpenActivity) {
                    Image(systemName: "waveform.path.ecg")
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)
                        .frame(width: 34, height: 34)
                        .background(NoriColors.fill1, in: Circle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Open Activity sheet")
            }
            .padding(.horizontal, 16)
            .padding(.top, 8)
            .padding(.bottom, 6)

            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    NoriSegmentedTabs(options: segmentOptions, selection: activeTabBinding)

                    switch activeTab {
                    case .needsYou:
                        needsYouContent
                    case .working:
                        workingContent
                    case .scheduled:
                        scheduledContent
                    case .finished:
                        finishedContent
                    }
                }
                .padding(.horizontal, 16)
                .padding(.top, 8)
                .padding(.bottom, 28)
            }
            .refreshable {
                await store.refreshWorkAndInteractions()
            }
        }
        .background(NoriColors.canvas.ignoresSafeArea())
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

    // MARK: - Tab Contents

    @ViewBuilder
    private var needsYouContent: some View {
        let cards = orderedInteractions
        let orphanNeedsYouTasks = store.workItems.filter { item in
            item.bucket == .needsYou && !cards.contains(where: { $0.taskID == item.taskID })
        }

        if cards.isEmpty && orphanNeedsYouTasks.isEmpty {
            NoriEmptyState(
                title: "All clear",
                message: "Nothing needs you right now."
            )
        } else {
            VStack(spacing: 12) {
                ForEach(cards) { interaction in
                    InteractionCardView(
                        store: store,
                        interaction: interaction,
                        onOpenTask: { taskID in
                            navigateToTask(taskID)
                        }
                    )
                }

                if !orphanNeedsYouTasks.isEmpty {
                    NoriRows {
                        ForEach(Array(orphanNeedsYouTasks.enumerated()), id: \.element.id) { idx, item in
                            workRow(item: item, showDivider: idx < orphanNeedsYouTasks.count - 1)
                        }
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var workingContent: some View {
        let active = store.activeWorkItems
        if active.isEmpty {
            NoriEmptyState(
                title: "Nothing is running",
                message: "Ask Nori for something in Chat and background work will show up here."
            )
        } else {
            NoriRows {
                ForEach(Array(active.enumerated()), id: \.element.id) { idx, item in
                    workRow(item: item, showDivider: idx < active.count - 1)
                }
            }
        }
    }

    @ViewBuilder
    private var scheduledContent: some View {
        let scheduled = enabledAutomations
        if scheduled.isEmpty {
            NoriEmptyState(
                title: "No automations scheduled",
                message: "Enable recurring briefs or checks in Automations to see upcoming runs here."
            ) {
                Button("Open Automations", action: onOpenRoutines)
                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
            }
        } else {
            NoriRows {
                ForEach(Array(scheduled.enumerated()), id: \.element.id) { idx, automation in
                    Button(action: onOpenRoutines) {
                        NoriRow(
                            title: automation.title,
                            detail: triggerSummary(for: automation.trigger),
                            systemImage: "repeat",
                            iconTone: .accent,
                            showChevron: true,
                            showDivider: idx < scheduled.count - 1
                        ) {
                            if let nextAt = automation.nextAt, !nextAt.isEmpty {
                                NoriPill(NoriDateFormatting.relative(nextAt), tone: .neutral)
                            }
                        }
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    @ViewBuilder
    private var finishedContent: some View {
        let finished = store.finishedWorkItems
        if finished.isEmpty {
            NoriEmptyState(
                title: "No finished tasks yet",
                message: "Completed, incomplete, and stopped tasks appear here."
            )
        } else {
            NoriRows {
                ForEach(Array(finished.enumerated()), id: \.element.id) { idx, item in
                    workRow(item: item, showDivider: idx < finished.count - 1)
                }
            }
        }
    }

    // MARK: - Work Row

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

    private func triggerSummary(for trigger: AutomationTrigger) -> String {
        if let displayCadence = trigger.displayCadence, !displayCadence.isEmpty {
            return displayCadence
        }
        if let rrule = trigger.rrule, !rrule.isEmpty {
            return rrule
        }
        if let at = trigger.at, !at.isEmpty {
            return "Once at \(NoriDateFormatting.shortDateTime(at))"
        }
        return trigger.summaryLabel
    }

    private func navigateToTask(_ taskID: String) {
        if let onOpenTask {
            onOpenTask(taskID)
        } else {
            selectedTaskDestination = taskID
        }
    }
}
