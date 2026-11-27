import SwiftUI
import NoriCore
import NoriDesign

public struct GoalDetailView: View {
    @Bindable private var store: NoriStore
    public let goalID: String
    private let onJumpToNeedsYou: () -> Void
    private let onOpenApps: () -> Void

    @State private var isActing = false
    @State private var showEditSheet = false
    @State private var showCompleteConfirm = false
    @State private var showDropConfirm = false
    @State private var selectedRoutineID: String?

    public init(
        store: NoriStore,
        goalID: String,
        onJumpToNeedsYou: @escaping () -> Void = {},
        onOpenApps: @escaping () -> Void = {}
    ) {
        self.store = store
        self.goalID = goalID
        self.onJumpToNeedsYou = onJumpToNeedsYou
        self.onOpenApps = onOpenApps
    }

    private var goal: Goal? {
        store.goals.first(where: { $0.id == goalID })
    }

    public var body: some View {
        Group {
            if let goal {
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        Text(goal.title)
                            .font(.system(size: 19, weight: .bold))
                            .foregroundStyle(NoriColors.ink)
                            .fixedSize(horizontal: false, vertical: true)
                            .padding(.bottom, 8)

                        if !goal.progress.isEmpty {
                            NoriLine(goal.progress)
                                .padding(.bottom, 12)
                        }

                        if !goal.outcome.isEmpty || (goal.deadline?.isEmpty == false) {
                            Text(outcomeLine(for: goal))
                                .font(NoriTypography.body)
                                .foregroundStyle(NoriColors.ink)
                                .fixedSize(horizontal: false, vertical: true)
                                .padding(.bottom, 12)
                        }

                        if goal.status == .active || goal.status == .paused {
                            ScrollView(.horizontal, showsIndicators: false) {
                                HStack(spacing: 8) {
                                    Button {
                                        showEditSheet = true
                                    } label: {
                                        Label("Edit", systemImage: "pencil")
                                    }
                                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                                    .disabled(isActing)

                                    Button {
                                        performAction(goal.status == .active ? "pause" : "resume")
                                    } label: {
                                        Label(
                                            goal.status == .active ? "Pause" : "Resume",
                                            systemImage: goal.status == .active ? "pause.fill" : "play.fill"
                                        )
                                    }
                                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                                    .disabled(isActing)

                                    Button {
                                        showCompleteConfirm = true
                                    } label: {
                                        Label("Mark done", systemImage: "checkmark")
                                    }
                                    .buttonStyle(NoriButtonStyle(.tonal, compact: true))
                                    .disabled(isActing)

                                    Button {
                                        showDropConfirm = true
                                    } label: {
                                        Text("Drop")
                                    }
                                    .buttonStyle(NoriButtonStyle(.danger, compact: true))
                                    .disabled(isActing)
                                }
                            }
                            .padding(.bottom, 10)
                        }

                        if !goal.blockers.isEmpty {
                            NoriSectionHeader("Blocked by")
                            NoriRows {
                                ForEach(Array(goal.blockers.enumerated()), id: \.element.id) { idx, blocker in
                                    Button {
                                        if blocker.interactionID != nil {
                                            onJumpToNeedsYou()
                                        } else if blocker.connectionID != nil {
                                            onOpenApps()
                                        }
                                    } label: {
                                        NoriRow(
                                            title: blocker.text,
                                            showChevron: false,
                                            showDivider: idx < goal.blockers.count - 1,
                                            wrapTitle: true
                                        ) {
                                            if blocker.interactionID != nil {
                                                Text("Review")
                                                    .font(.system(size: 13, weight: .semibold))
                                                    .foregroundStyle(NoriColors.accent)
                                            } else if blocker.connectionID != nil {
                                                Text("Fix")
                                                    .font(.system(size: 13, weight: .semibold))
                                                    .foregroundStyle(NoriColors.accent)
                                            }
                                        }
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                        }

                        if !goal.completionCriteria.isEmpty {
                            NoriSectionHeader("Done when (\(goal.metCriteriaCount) of \(goal.completionCriteria.count))")
                            VStack(alignment: .leading, spacing: 10) {
                                ForEach(goal.completionCriteria) { criterion in
                                    HStack(alignment: .top, spacing: 10) {
                                        Image(systemName: criterion.met ? "checkmark.circle.fill" : "circle")
                                            .font(.system(size: 16, weight: .medium))
                                            .foregroundStyle(criterion.met ? NoriColors.ok : NoriColors.hairlineStrong)
                                            .padding(.top, 1)

                                        VStack(alignment: .leading, spacing: 4) {
                                            Text(criterion.text)
                                                .font(NoriTypography.body)
                                                .foregroundStyle(criterion.met ? NoriColors.ink2 : NoriColors.ink)
                                                .fixedSize(horizontal: false, vertical: true)

                                            if let evidenceIDs = criterion.evidenceResultIDs, !evidenceIDs.isEmpty {
                                                HStack(spacing: 10) {
                                                    ForEach(evidenceIDs, id: \.self) { resultID in
                                                        Button("Evidence") {
                                                            store.pendingDeepLink = .document(resultID)
                                                        }
                                                        .font(.system(size: 12.5, weight: .medium))
                                                        .foregroundStyle(NoriColors.accent)
                                                        .buttonStyle(.plain)
                                                    }
                                                }
                                            }
                                        }
                                    }
                                }
                            }
                            .padding(.top, 2)
                        }

                        if !goal.nextActions.isEmpty {
                            NoriSectionHeader("Next")
                            VStack(alignment: .leading, spacing: 6) {
                                ForEach(goal.nextActions, id: \.self) { next in
                                    HStack(alignment: .top, spacing: 8) {
                                        Text("•")
                                            .foregroundStyle(NoriColors.ink3)
                                        Text(next)
                                            .font(NoriTypography.body)
                                            .foregroundStyle(NoriColors.ink)
                                            .fixedSize(horizontal: false, vertical: true)
                                    }
                                }
                            }
                            .padding(.top, 2)
                        }

                        linkedSection(for: goal)
                    }
                    .padding(.horizontal, 16)
                    .padding(.top, 12)
                    .padding(.bottom, 96)
                }
                .background(NoriColors.canvas)
                .refreshable {
                    _ = await store.loadGoal(id: goalID)
                }
                .sheet(isPresented: $showEditSheet) {
                    GoalEditorSheet(store: store, existing: goal)
                        .presentationDetents([.medium, .large])
                        .presentationDragIndicator(.visible)
                        .presentationBackground(NoriColors.sheetBackground)
                        .presentationCornerRadius(NoriRadius.sheet)
                }
                .navigationDestination(item: $selectedRoutineID) { routineID in
                    RoutineDetailView(store: store, automationID: routineID)
                }
                .confirmationDialog(
                    "Mark this goal done?",
                    isPresented: $showCompleteConfirm,
                    titleVisibility: .visible
                ) {
                    Button("Mark done") {
                        performAction("complete", keepMonitors: false)
                    }
                    if !goal.linked.automationIDs.isEmpty {
                        Button("Mark done & keep routines") {
                            performAction("complete", keepMonitors: true)
                        }
                    }
                    Button("Cancel", role: .cancel) {}
                }
                .confirmationDialog(
                    "Drop this goal?",
                    isPresented: $showDropConfirm,
                    titleVisibility: .visible
                ) {
                    Button("Drop goal", role: .destructive) {
                        performAction("abandon")
                    }
                    Button("Keep", role: .cancel) {}
                } message: {
                    Text("Nori will stop tracking this goal and pause linked monitors.")
                }
            } else {
                NoriEmptyState(
                    title: "Goal not found",
                    message: "This goal is no longer available."
                )
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(NoriColors.canvas)
            }
        }
        .navigationTitle("Goal")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .task(id: goalID) {
            _ = await store.loadGoal(id: goalID)
        }
        .toolbar {
            if let goal {
                if goal.isBlocked {
                    ToolbarItem(placement: .automatic) {
                        NoriPill("Blocked", tone: .warning)
                    }
                } else if goal.status != .active {
                    ToolbarItem(placement: .automatic) {
                        let label = goal.status == .completed ? "Done" : (goal.status == .paused ? "Paused" : "Dropped")
                        let tone: NoriTone = goal.status == .completed ? .ok : .neutral
                        NoriPill(label, tone: tone)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func linkedSection(for goal: Goal) -> some View {
        let linkedTasks = goal.linked.taskIDs.map { taskID in
            (id: taskID, title: store.workItems.first(where: { $0.taskID == taskID })?.title ?? "Task")
        }
        let linkedRoutines = goal.linked.automationIDs.map { routineID in
            (id: routineID, title: store.automations.first(where: { $0.id == routineID })?.title ?? "Routine")
        }
        let linkedResults = goal.linked.resultIDs.map { resultID in
            (id: resultID, title: store.libraryItems.first(where: { $0.id == resultID })?.title ?? "Saved artifact")
        }
        let total = linkedTasks.count + linkedRoutines.count + linkedResults.count

        if total > 0 {
            NoriSectionHeader("Linked")
            NoriRows {
                ForEach(Array(linkedTasks.enumerated()), id: \.element.id) { idx, entry in
                    Button {
                        store.pendingDeepLink = .workDetail(entry.id)
                    } label: {
                        NoriRow(
                            title: entry.title,
                            showChevron: true,
                            showDivider: idx < linkedTasks.count - 1 || !linkedRoutines.isEmpty || !linkedResults.isEmpty
                        )
                    }
                    .buttonStyle(.plain)
                }

                ForEach(Array(linkedRoutines.enumerated()), id: \.element.id) { idx, entry in
                    Button {
                        selectedRoutineID = entry.id
                    } label: {
                        NoriRow(
                            title: entry.title,
                            showChevron: true,
                            showDivider: idx < linkedRoutines.count - 1 || !linkedResults.isEmpty
                        )
                    }
                    .buttonStyle(.plain)
                }

                ForEach(Array(linkedResults.enumerated()), id: \.element.id) { idx, entry in
                    Button {
                        store.pendingDeepLink = .document(entry.id)
                    } label: {
                        NoriRow(
                            title: entry.title,
                            showChevron: true,
                            showDivider: idx < linkedResults.count - 1
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    private func performAction(_ action: String, keepMonitors: Bool? = nil) {
        isActing = true
        Task {
            _ = await store.performGoalAction(goalID: goalID, action: action, keepMonitors: keepMonitors)
            isActing = false
        }
    }

    private func outcomeLine(for goal: Goal) -> String {
        var text = goal.outcome
        if let deadline = goal.deadline, !deadline.isEmpty {
            let suffix = "Due \(NoriDateFormatting.shortDate(deadline))."
            text = text.isEmpty ? suffix : "\(text) \(suffix)"
        }
        return text
    }
}
