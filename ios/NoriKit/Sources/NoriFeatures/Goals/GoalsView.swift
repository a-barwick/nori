import SwiftUI
import NoriCore
import NoriDesign

public struct GoalExample: Identifiable, Hashable, Sendable {
    public let id: String
    public let systemImage: String
    public let title: String
    public let prompt: String

    public init(id: String, systemImage: String, title: String, prompt: String) {
        self.id = id
        self.systemImage = systemImage
        self.title = title
        self.prompt = prompt
    }

    public static let defaults: [GoalExample] = [
        GoalExample(
            id: "trip",
            systemImage: "airplane",
            title: "Plan a weekend trip",
            prompt: "Create a goal to plan a weekend trip: compare flight and hotel options under $900, ask for my approval before booking, and add the confirmed itinerary to my calendar by Friday."
        ),
        GoalExample(
            id: "inbox",
            systemImage: "envelope.badge",
            title: "Clean up my inbox",
            prompt: "Create a goal to clean up my inbox by Sunday: find recurring newsletters and promos from the last 30 days, propose what to unsubscribe from for my approval, and summarize what remains."
        ),
        GoalExample(
            id: "prep",
            systemImage: "doc.text",
            title: "Prep for next week's meeting",
            prompt: "Create a goal to prepare for next week's review meeting: pull together recent updates and open action items, draft a 1-page brief, and check in with me 24 hours before."
        ),
        GoalExample(
            id: "appointment",
            systemImage: "calendar.badge.checkmark",
            title: "Book a dentist appointment",
            prompt: "Create a goal to book my dentist appointment: check my calendar for open mornings next week, draft the scheduling message for my approval, and mark it done once the appointment is confirmed."
        )
    ]
}

public struct GoalsView: View {
    @Bindable private var store: NoriStore
    private let onJumpToNeedsYou: () -> Void
    private let onStartInChat: (String) -> Void
    private let onOpenApps: (() -> Void)?

    @State private var showExamples: Bool = false
    @State private var selectedGoalID: String?

    public init(
        store: NoriStore,
        onJumpToNeedsYou: @escaping () -> Void,
        onStartInChat: @escaping (String) -> Void = { _ in },
        onOpenApps: (() -> Void)? = nil
    ) {
        self.store = store
        self.onJumpToNeedsYou = onJumpToNeedsYou
        self.onStartInChat = onStartInChat
        self.onOpenApps = onOpenApps
    }

    private var openGoals: [Goal] {
        store.goals.filter { $0.status == .active || $0.status == .paused }
    }

    private var closedGoals: [Goal] {
        store.goals.filter { $0.status == .completed || $0.status == .abandoned }
    }

    private var blockedCount: Int {
        openGoals.filter(\.isBlocked).count
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if store.goals.isEmpty {
                    NoriCard(padding: 16) {
                        Text("Give Nori something bigger you want done—like planning a trip or organizing an event—and it will keep track of every step until it’s finished.")
                            .font(NoriTypography.body)
                            .foregroundStyle(NoriColors.ink)
                            .fixedSize(horizontal: false, vertical: true)
                    }

                    VStack(alignment: .leading, spacing: 0) {
                        NoriSectionHeader("Quick-start in Chat")
                        quickStartGrid
                    }
                } else {
                    HStack(alignment: .center, spacing: 10) {
                        if blockedCount > 0 {
                            NoriLine(
                                blockedCount == 1
                                    ? "One goal is waiting for input."
                                    : "\(blockedCount) goals are waiting for input."
                            )
                        } else {
                            Spacer(minLength: 0)
                        }

                        Button {
                            withAnimation(.easeInOut(duration: 0.18)) {
                                showExamples.toggle()
                            }
                        } label: {
                            HStack(spacing: 5) {
                                Image(systemName: "sparkles")
                                    .font(.system(size: 11.5, weight: .semibold))
                                Text(showExamples ? "Hide examples" : "Ideas")
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    }

                    if showExamples {
                        VStack(alignment: .leading, spacing: 0) {
                            NoriSectionHeader("Quick-start in Chat")
                            quickStartGrid
                        }
                        .transition(.opacity.combined(with: .move(edge: .top)))
                    }

                    ForEach(openGoals) { goal in
                        goalCard(goal)
                    }

                    if !closedGoals.isEmpty {
                        VStack(alignment: .leading, spacing: 0) {
                            NoriSectionHeader("Closed")
                            NoriRows {
                                ForEach(Array(closedGoals.enumerated()), id: \.element.id) { idx, goal in
                                    Button {
                                        selectedGoalID = goal.id
                                    } label: {
                                        NoriRow(
                                            title: goal.title,
                                            detail: closedDetail(for: goal),
                                            showChevron: true,
                                            showDivider: idx < closedGoals.count - 1
                                        ) {
                                            NoriPill(
                                                statusLabel(for: goal.status),
                                                tone: statusTone(for: goal.status)
                                            )
                                        }
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationDestination(item: $selectedGoalID) { goalID in
            GoalDetailView(
                store: store,
                goalID: goalID,
                onJumpToNeedsYou: onJumpToNeedsYou,
                onOpenApps: openAppsDestination
            )
        }
        .refreshable {
            await store.refreshRoutinesAndGoals()
        }
    }

    private func openAppsDestination() {
        if let onOpenApps {
            onOpenApps()
        } else {
            store.pendingDeepLink = .apps
        }
    }

    private func routeBlocker(_ blocker: GoalBlocker, goalID: String) {
        if blocker.interactionID != nil {
            onJumpToNeedsYou()
        } else if blocker.connectionID != nil {
            openAppsDestination()
        } else {
            selectedGoalID = goalID
        }
    }

    private var quickStartGrid: some View {
        let columns = [GridItem(.adaptive(minimum: 260), spacing: 10)]
        return LazyVGrid(columns: columns, spacing: 10) {
            ForEach(GoalExample.defaults) { example in
                Button {
                    onStartInChat(example.prompt)
                } label: {
                    HStack(spacing: 12) {
                        Image(systemName: example.systemImage)
                            .font(.system(size: 15.5, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                            .frame(width: 36, height: 36)
                            .background(
                                NoriColors.accentSubtle,
                                in: RoundedRectangle(cornerRadius: 10, style: .continuous)
                            )

                        Text(example.title)
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(NoriColors.ink)
                            .lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)

                        Image(systemName: "arrow.up.right")
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(NoriColors.ink3)
                    }
                    .padding(.horizontal, 14)
                    .padding(.vertical, 12)
                    .background(
                        NoriColors.surface,
                        in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                    )
                    .overlay(
                        RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                            .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                    )
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
    }

    private func goalCard(_ goal: Goal) -> some View {
        let ratio = completionRatio(for: goal)
        let sub = subtitle(for: goal)
        let visibleCriteria = Array(goal.completionCriteria.prefix(4))
        let remainingCount = max(0, goal.completionCriteria.count - 4)
        let badge: (label: String, tone: NoriTone)? = {
            if goal.isBlocked { return ("Blocked", .warning) }
            if goal.status != .active { return (statusLabel(for: goal.status), statusTone(for: goal.status)) }
            return nil
        }()

        return NoriCard(padding: 15) {
            VStack(alignment: .leading, spacing: 11) {
                Button {
                    selectedGoalID = goal.id
                } label: {
                    HStack(alignment: .top, spacing: 8) {
                        Text(goal.title)
                            .font(.system(size: 15.5, weight: .bold))
                            .foregroundStyle(NoriColors.ink)
                            .multilineTextAlignment(.leading)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)

                        if let badge {
                            NoriPill(badge.label, tone: badge.tone)
                        }
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)

                if !goal.completionCriteria.isEmpty || sub != nil {
                    VStack(alignment: .leading, spacing: 6) {
                        if !goal.completionCriteria.isEmpty {
                            GeometryReader { proxy in
                                let fillWidth = ratio > 0 ? max(6, proxy.size.width * ratio) : 0
                                ZStack(alignment: .leading) {
                                    Capsule()
                                        .fill(NoriColors.fill2)
                                    if fillWidth > 0 {
                                        Capsule()
                                            .fill(goal.status == .completed ? NoriColors.ok : NoriColors.brandPrimary)
                                            .frame(width: min(proxy.size.width, fillWidth))
                                    }
                                }
                            }
                            .frame(height: 4)
                        }

                        if let sub {
                            Text(sub)
                                .font(.system(size: 12))
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }
                }

                ForEach(goal.blockers) { blocker in
                    Button {
                        routeBlocker(blocker, goalID: goal.id)
                    } label: {
                        HStack(spacing: 10) {
                            Text("\(Text("Action needed: ").font(.system(size: 12.5, weight: .semibold)).foregroundStyle(NoriColors.accent))\(Text(blocker.text).font(.system(size: 12.5)).foregroundStyle(NoriColors.ink))")
                                .lineLimit(2)
                                .multilineTextAlignment(.leading)
                                .frame(maxWidth: .infinity, alignment: .leading)

                            HStack(spacing: 3) {
                                Text(blocker.connectionID != nil && blocker.interactionID == nil ? "Fix" : "Review")
                                    .font(.system(size: 12, weight: .semibold))
                                Image(systemName: "chevron.right")
                                    .font(.system(size: 10.5, weight: .semibold))
                            }
                            .foregroundStyle(NoriColors.accent)
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 10)
                        .background(
                            NoriColors.accentSubtle,
                            in: RoundedRectangle(cornerRadius: 14, style: .continuous)
                        )
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Action needed: \(blocker.text).")
                }

                if !visibleCriteria.isEmpty {
                    VStack(alignment: .leading, spacing: 7) {
                        ForEach(visibleCriteria) { criterion in
                            HStack(alignment: .top, spacing: 8) {
                                Image(systemName: criterion.met ? "checkmark.circle.fill" : "circle")
                                    .font(.system(size: 15, weight: .medium))
                                    .foregroundStyle(criterion.met ? NoriColors.ok : NoriColors.hairlineStrong)
                                    .padding(.top, 1)

                                Text(criterion.text)
                                    .font(.system(size: 13.5))
                                    .foregroundStyle(criterion.met ? NoriColors.ink2 : NoriColors.ink)
                                    .strikethrough(criterion.met, color: NoriColors.ink3)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }

                        if remainingCount > 0 {
                            Button {
                                selectedGoalID = goal.id
                            } label: {
                                Text("+\(remainingCount) more")
                                    .font(.system(size: 12, weight: .medium))
                                    .foregroundStyle(NoriColors.ink3)
                                    .padding(.leading, 23)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    .padding(.top, 2)
                }
            }
        }
    }

    private func completionRatio(for goal: Goal) -> CGFloat {
        let total = goal.completionCriteria.count
        guard total > 0 else { return goal.status == .completed ? 1.0 : 0.0 }
        return min(1.0, max(0.0, CGFloat(goal.metCriteriaCount) / CGFloat(total)))
    }

    private func subtitle(for goal: Goal) -> String? {
        var parts: [String] = []
        if !goal.completionCriteria.isEmpty {
            parts.append("\(goal.metCriteriaCount) of \(goal.completionCriteria.count) done")
        }
        if let deadline = goal.deadline, !deadline.isEmpty {
            parts.append("Due \(NoriDateFormatting.shortDate(deadline))")
        }
        if parts.isEmpty, !goal.progress.isEmpty {
            return goal.progress
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private func closedDetail(for goal: Goal) -> String? {
        if let completedAt = goal.completedAt, !completedAt.isEmpty {
            return "Finished \(NoriDateFormatting.relative(completedAt))"
        }
        return nil
    }

    private func statusLabel(for status: GoalStatus) -> String {
        switch status {
        case .active: return "Active"
        case .paused: return "Paused"
        case .completed: return "Done"
        case .abandoned: return "Dropped"
        }
    }

    private func statusTone(for status: GoalStatus) -> NoriTone {
        switch status {
        case .active: return .working
        case .completed: return .ok
        case .paused, .abandoned: return .neutral
        }
    }
}
