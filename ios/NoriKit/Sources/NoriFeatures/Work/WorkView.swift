import SwiftUI
import NoriCore
import NoriDesign


public enum WorkTabFilter: String, Sendable, Hashable, CaseIterable {
    case artifacts
    case automations
    case tasks
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
            let isPartial = item.partial == true
                || item.error?.code == "task_incomplete"
                || item.isBudgetExhausted
            return isPartial
                ? PillInfo(label: "Partial", tone: .warning)
                : PillInfo(label: "Failed", tone: .danger)
        case .cancelled:
            return PillInfo(label: "Stopped", tone: .neutral)
        }
    }
}


private enum UnifiedTaskRow: Identifiable {
    case inProgress(id: String, taskID: String, title: String, steps: [WorkStep])
    case todo(id: String, taskID: String?, title: String, replyInChat: Bool)
    case scheduled(id: String, automation: Automation, title: String, scheduleLabel: String)

    var id: String {
        switch self {
        case .inProgress(let id, _, _, _): return id
        case .todo(let id, _, _, _): return id
        case .scheduled(let id, _, _, _): return id
        }
    }

    var title: String {
        switch self {
        case .inProgress(_, _, let title, _): return title
        case .todo(_, _, let title, _): return title
        case .scheduled(_, _, let title, _): return title
        }
    }

    var canCancel: Bool {
        switch self {
        case .inProgress, .scheduled: return true
        case .todo(_, let taskID, _, _): return taskID != nil
        }
    }
}


private struct CircularTaskProgressRing: View {
    let steps: [WorkStep]
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var spinning = false

    private var doneCount: Int {
        steps.filter { $0.status == .done }.count
    }

    private var isIndeterminate: Bool {
        steps.isEmpty || doneCount == 0
    }

    private var fraction: Double {
        isIndeterminate ? 0.28 : min(0.95, Double(doneCount) / Double(steps.count))
    }

    var body: some View {
        ZStack {
            Circle()
                .stroke(NoriColors.fill2, lineWidth: 2.3)
            Circle()
                .trim(from: 0, to: fraction)
                .stroke(
                    NoriColors.brandPrimary,
                    style: StrokeStyle(lineWidth: 2.3, lineCap: .round)
                )
                .rotationEffect(.degrees(spinning && isIndeterminate && !reduceMotion ? 270 : -90))
        }
        .frame(width: 19, height: 19)
        .onAppear {
            guard isIndeterminate, !reduceMotion else { return }
            withAnimation(.linear(duration: 1.8).repeatForever(autoreverses: false)) {
                spinning = true
            }
        }
        .accessibilityLabel(steps.isEmpty ? "In progress" : "\(doneCount) of \(steps.count) steps")
    }
}


public struct WorkView: View {
    @Bindable private var store: NoriStore
    private let externalSelection: Binding<WorkTabFilter>?
    private let isLoadingOverview: Bool
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void
    private let onOpenTask: ((String) -> Void)?
    private let onStartInChat: (String) -> Void
    private let onOpenModels: () -> Void

    @State private var internalTab: WorkTabFilter = .tasks
    @State private var selectedTaskDestination: String?
    @State private var expandedRowID: String?
    @State private var confirmingCancelID: String?
    @State private var dismissedRowIDs: Set<String> = []

    public init(
        store: NoriStore,
        selectedTab: Binding<WorkTabFilter>? = nil,
        isLoadingOverview: Bool = false,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in },
        onOpenTask: ((String) -> Void)? = nil,
        onStartInChat: @escaping (String) -> Void = { _ in },
        onOpenModels: @escaping () -> Void = {}
    ) {
        self.store = store
        self.externalSelection = selectedTab
        self.isLoadingOverview = isLoadingOverview
        self.onOpenDocument = onOpenDocument
        self.onOpenTask = onOpenTask
        self.onStartInChat = onStartInChat
        self.onOpenModels = onOpenModels
    }

    private var activeTabBinding: Binding<WorkTabFilter> {
        externalSelection ?? $internalTab
    }

    private var activeTab: WorkTabFilter {
        activeTabBinding.wrappedValue
    }

    private var tabOptions: [NoriSegmentOption<WorkTabFilter>] {
        [
            NoriSegmentOption(value: .artifacts, label: "Artifacts"),
            NoriSegmentOption(value: .automations, label: "Automations"),
            NoriSegmentOption(
                value: .tasks,
                label: "Tasks",
                count: store.actionableNeedsYouCount > 0 ? store.actionableNeedsYouCount : nil
            )
        ]
    }

    private var pendingInteractions: [Interaction] {
        store.pendingInteractions
    }

    private var actionableInteractions: [Interaction] {
        pendingInteractions.filter { $0.kind != .question }
    }

    private var chatWaitingQuestions: [Interaction] {
        pendingInteractions.filter { $0.kind == .question }
    }

    private var unifiedRows: [UnifiedTaskRow] {
        let actionableTaskIDs = Set(actionableInteractions.compactMap(\.taskID))
        let chatWaitingTaskIDs = Set(chatWaitingQuestions.compactMap(\.taskID))

        var running: [UnifiedTaskRow] = []
        var queued: [UnifiedTaskRow] = []
        var seenTaskIDs = Set<String>()

        for item in store.workItems {
            guard item.status == .running, !item.cancelRequested else { continue }
            guard !actionableTaskIDs.contains(item.taskID) else { continue }
            guard item.bucket != .needsYou, !chatWaitingTaskIDs.contains(item.taskID) else { continue }
            seenTaskIDs.insert(item.taskID)
            if item.queued == true {
                queued.append(.todo(id: "task:\(item.taskID)", taskID: item.taskID, title: item.title, replyInChat: false))
            } else {
                running.append(.inProgress(id: "task:\(item.taskID)", taskID: item.taskID, title: item.title, steps: item.steps ?? []))
            }
        }

        var waiting: [UnifiedTaskRow] = []
        var waitingSeen = Set<String>()
        for q in chatWaitingQuestions {
            let key = q.taskID ?? q.id
            guard waitingSeen.insert(key).inserted else { continue }
            let workItem = q.taskID.flatMap { tid in store.workItems.first { $0.taskID == tid } }
            let title = workItem?.title ?? q.taskTitle ?? "Waiting in Chat"
            waiting.append(.todo(id: "chat:\(key)", taskID: q.taskID, title: title, replyInChat: true))
        }
        for item in store.workItems where item.status == .running && !item.cancelRequested && item.bucket == .needsYou {
            guard !actionableTaskIDs.contains(item.taskID), !seenTaskIDs.contains(item.taskID) else { continue }
            guard waitingSeen.insert(item.taskID).inserted else { continue }
            waiting.append(.todo(id: "chat:\(item.taskID)", taskID: item.taskID, title: item.title, replyInChat: true))
        }

        let scheduled: [UnifiedTaskRow] = store.automations
            .filter { auto in
                auto.status == .enabled
                    && auto.trigger.kind == "once"
                    && !((auto.nextAt ?? auto.trigger.at ?? "").isEmpty)
            }
            .sorted { lhs, rhs in
                let lDate = (lhs.nextAt ?? lhs.trigger.at).flatMap { NoriDateFormatting.parse($0) }
                let rDate = (rhs.nextAt ?? rhs.trigger.at).flatMap { NoriDateFormatting.parse($0) }
                switch (lDate, rDate) {
                case (let l?, let r?): return l < r
                case (_?, nil): return true
                case (nil, _?): return false
                case (nil, nil): return lhs.title < rhs.title
                }
            }
            .map { auto in
                let raw = auto.nextAt ?? auto.trigger.at ?? ""
                let label = raw.isEmpty ? "" : NoriDateFormatting.shortDateTime(raw)
                return .scheduled(id: "auto:\(auto.id)", automation: auto, title: auto.title, scheduleLabel: label)
            }

        return (running + waiting + queued + scheduled).filter { !dismissedRowIDs.contains($0.id) }
    }

    public var body: some View {
        VStack(spacing: 0) {
            headerSection

            Group {
                switch activeTab {
                case .artifacts:
                    LibraryView(store: store, onOpenDocument: onOpenDocument)
                case .automations:
                    RoutinesView(store: store)
                case .tasks:
                    tasksContent
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .navigationDestination(item: $selectedTaskDestination) { taskID in
            WorkDetailView(
                store: store,
                taskID: taskID,
                onOpenDocument: onOpenDocument,
                onStartInChat: onStartInChat,
                onOpenModels: onOpenModels
            )
        }
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
    }


    private var headerSection: some View {
        VStack(alignment: .leading, spacing: 0) {
            NoriUnderlineTabs(options: tabOptions, selection: activeTabBinding)
                .padding(.horizontal, 8)
                .padding(.top, 12)
        }
        .background(
            NoriTopWash()
                .ignoresSafeArea(edges: .top)
        )
    }


    private var tasksContent: some View {
        let actionable = actionableInteractions
        let rows = unifiedRows
        let isAllClear = actionable.isEmpty && rows.isEmpty

        return ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                if !actionable.isEmpty {
                    NoriSectionHeader(actionable.count > 1 ? "Action needed · \(actionable.count)" : "Action needed")
                    VStack(spacing: 12) {
                        ForEach(actionable) { interaction in
                            InteractionCardView(
                                store: store,
                                interaction: interaction,
                                onOpenTask: navigateToTask
                            )
                        }
                    }
                    .padding(.bottom, rows.isEmpty ? 6 : 18)
                }

                if isAllClear, isLoadingOverview, !store.hasLoadedWorkOverview {
                    ProgressView()
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 16)
                } else if isAllClear, !store.hasLoadedWorkOverview {
                    Button("Refresh tasks") {
                        Task {
                            async let work: Void = store.refreshWorkAndInteractions()
                            async let autos: Void = store.refreshAutomations()
                            _ = await (work, autos)
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, fullWidth: true))
                    .padding(.vertical, 8)
                } else if isAllClear {
                    allClearCard
                        .padding(.top, 4)
                        .padding(.bottom, 8)
                }

                if !rows.isEmpty {
                    NoriRows {
                        ForEach(Array(rows.enumerated()), id: \.element.id) { idx, row in
                            unifiedRowView(row, showDivider: idx < rows.count - 1)
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .refreshable {
            async let work: Void = store.refreshWorkAndInteractions()
            async let autos: Void = store.refreshAutomations()
            _ = await (work, autos)
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


    private func unifiedRowView(_ row: UnifiedTaskRow, showDivider: Bool) -> some View {
        let steps: [WorkStep] = {
            if case .inProgress(_, _, _, let s) = row { return s }
            return []
        }()
        let hasSteps = !steps.isEmpty
        let isExpanded = hasSteps && expandedRowID == row.id
        let isConfirming = confirmingCancelID == row.id

        return VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .center, spacing: 12) {
                leadingMarker(for: row)
                    .frame(width: 20, height: 20)

                Text(row.title)
                    .font(row.isScheduled ? .system(size: 14.5, weight: .medium) : NoriTypography.rowTitle)
                    .foregroundStyle(row.isScheduled ? NoriColors.ink2 : NoriColors.ink)
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)
                    .frame(maxWidth: .infinity, alignment: .leading)

                trailingControls(for: row, hasSteps: hasSteps, isExpanded: isExpanded, isConfirming: isConfirming)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 13)
            .contentShape(Rectangle())
            .onTapGesture {
                if isConfirming {
                    confirmingCancelID = nil
                    return
                }
                guard hasSteps else { return }
                withAnimation(.easeInOut(duration: 0.18)) {
                    expandedRowID = (expandedRowID == row.id) ? nil : row.id
                }
            }

            if isExpanded {
                VStack(alignment: .leading, spacing: 7) {
                    ForEach(Array(steps.enumerated()), id: \.offset) { _, step in
                        HStack(alignment: .top, spacing: 9) {
                            stepMarker(for: step.status)
                                .padding(.top, 2)
                            Text(step.text)
                                .font(.system(size: 13, weight: step.status == .active ? .semibold : .regular))
                                .foregroundStyle(stepForeground(for: step.status))
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                }
                .padding(.leading, 46)
                .padding(.trailing, 14)
                .padding(.bottom, 13)
            }

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .padding(.leading, 46)
            }
        }
    }

    @ViewBuilder
    private func leadingMarker(for row: UnifiedTaskRow) -> some View {
        switch row {
        case .inProgress(_, _, _, let steps):
            CircularTaskProgressRing(steps: steps)
        case .todo:
            Circle()
                .stroke(NoriColors.hairlineStrong, lineWidth: 1.75)
                .frame(width: 15, height: 15)
        case .scheduled:
            Image(systemName: "clock")
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(NoriColors.scheduled)
        }
    }

    @ViewBuilder
    private func trailingControls(
        for row: UnifiedTaskRow,
        hasSteps: Bool,
        isExpanded: Bool,
        isConfirming: Bool
    ) -> some View {
        if isConfirming {
            HStack(spacing: 6) {
                Button("Cancel") {
                    confirmCancel(row)
                }
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(NoriColors.danger)
                .padding(.horizontal, 10)
                .frame(height: 28)
                .background(NoriColors.dangerBg, in: Capsule())
                .buttonStyle(.plain)

                Button("Keep") {
                    confirmingCancelID = nil
                }
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(NoriColors.ink2)
                .padding(.horizontal, 10)
                .frame(height: 28)
                .background(NoriColors.fill1, in: Capsule())
                .buttonStyle(.plain)
            }
        } else {
            HStack(spacing: 8) {
                if case .todo(_, _, _, let replyInChat) = row, replyInChat {
                    Button {
                        onStartInChat("")
                    } label: {
                        Text("Reply")
                            .font(.system(size: 12, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                            .padding(.horizontal, 10)
                            .frame(height: 26)
                            .background(NoriColors.accentSubtle, in: Capsule())
                    }
                    .buttonStyle(.plain)
                }

                if case .scheduled(_, _, _, let label) = row, !label.isEmpty {
                    Text(label)
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(NoriColors.scheduled)
                        .lineLimit(1)
                }

                if row.canCancel {
                    Button {
                        withAnimation(.easeInOut(duration: 0.14)) {
                            confirmingCancelID = row.id
                        }
                    } label: {
                        Image(systemName: "xmark")
                            .font(.system(size: 11, weight: .semibold))
                            .foregroundStyle(NoriColors.ink3)
                            .frame(width: 26, height: 26)
                            .contentShape(Circle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Cancel \(row.title)")
                }

                if hasSteps {
                    Image(systemName: "chevron.right")
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundStyle(NoriColors.ink3)
                        .rotationEffect(.degrees(isExpanded ? 90 : 0))
                }
            }
        }
    }

    @ViewBuilder
    private func stepMarker(for status: WorkStep.Status) -> some View {
        switch status {
        case .done:
            Image(systemName: "checkmark")
                .font(.system(size: 8.5, weight: .bold))
                .foregroundStyle(NoriColors.ok)
                .frame(width: 14, height: 14)
                .background(NoriColors.okBg, in: Circle())
        case .active:
            Circle()
                .fill(NoriColors.brandPrimary)
                .frame(width: 6.5, height: 6.5)
                .frame(width: 14, height: 14)
        case .upcoming:
            Circle()
                .stroke(NoriColors.ink3.opacity(0.6), lineWidth: 1.5)
                .frame(width: 6.5, height: 6.5)
                .frame(width: 14, height: 14)
        }
    }

    private func stepForeground(for status: WorkStep.Status) -> Color {
        switch status {
        case .done: return NoriColors.ink3
        case .active: return NoriColors.ink
        case .upcoming: return NoriColors.ink2
        }
    }

    private func confirmCancel(_ row: UnifiedTaskRow) {
        #if os(iOS)
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
        #endif
        confirmingCancelID = nil
        if expandedRowID == row.id { expandedRowID = nil }
        dismissedRowIDs.insert(row.id)

        Task {
            switch row {
            case .inProgress(_, let taskID, _, _):
                await store.stopTask(taskID: taskID)
            case .todo(_, let taskID, _, _):
                if let taskID {
                    await store.stopTask(taskID: taskID)
                }
            case .scheduled(_, let automation, _, _):
                let ok = await store.cancelAutomation(automationID: automation.id)
                if !ok {
                    dismissedRowIDs.remove(row.id)
                }
            }
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

private extension UnifiedTaskRow {
    var isScheduled: Bool {
        if case .scheduled = self { return true }
        return false
    }
}
