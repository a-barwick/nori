import SwiftUI
import NoriCore
import NoriDesign

public struct GoalsView: View {
    @Bindable private var store: NoriStore
    private let onJumpToNeedsYou: () -> Void

    public init(store: NoriStore, onJumpToNeedsYou: @escaping () -> Void) {
        self.store = store
        self.onJumpToNeedsYou = onJumpToNeedsYou
    }

    private var blockedCount: Int {
        store.goals.filter(\.isBlocked).count
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if blockedCount > 0 {
                    NoriLine(
                        blockedCount == 1
                            ? "One goal is waiting on your input."
                            : "\(blockedCount) goals are waiting on your input."
                    )
                }

                if store.goals.isEmpty {
                    NoriEmptyState(
                        title: "No goals yet",
                        message: "Tell Nori what you want done by when, and it tracks criteria and blockers here."
                    )
                } else {
                    ForEach(store.goals) { goal in
                        goalCard(goal)
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .refreshable {
            await store.refreshRoutinesAndGoals()
        }
    }

    // MARK: - Goal Card

    private func goalCard(_ goal: Goal) -> some View {
        let ratio = completionRatio(for: goal)

        return NoriCard(padding: 15) {
            VStack(alignment: .leading, spacing: 11) {
                // Header: Title + Status Pill
                HStack(alignment: .top, spacing: 8) {
                    Text(goal.title)
                        .font(.system(size: 15.5, weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    NoriPill(
                        goal.isBlocked ? "Blocked" : statusLabel(for: goal.status),
                        tone: goal.isBlocked ? .warning : statusTone(for: goal.status)
                    )
                }

                // Slim violet completion progress bar + summary
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

                    Text(subtitle(for: goal))
                        .font(.system(size: 12))
                        .foregroundStyle(NoriColors.ink2)
                }

                // Tappable lavender blocker banner -> Work Tasks tab
                ForEach(goal.blockers) { blocker in
                    Button {
                        onJumpToNeedsYou()
                    } label: {
                        HStack(spacing: 10) {
                            Text("\(Text("Action needed: ").font(.system(size: 12.5, weight: .semibold)).foregroundStyle(NoriColors.accent))\(Text(blocker.text).font(.system(size: 12.5)).foregroundStyle(NoriColors.ink))")
                                .lineLimit(2)
                                .multilineTextAlignment(.leading)
                                .frame(maxWidth: .infinity, alignment: .leading)

                            HStack(spacing: 3) {
                                Text("Review")
                                    .font(.system(size: 12, weight: .semibold))
                                Image(systemName: "chevron.right")
                                    .font(.system(size: 10, weight: .semibold))
                            }
                            .foregroundStyle(NoriColors.accent)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 5)
                            .background(NoriColors.elevated, in: Capsule())
                            .overlay(
                                Capsule()
                                    .strokeBorder(NoriColors.accentBorder, lineWidth: 1)
                            )
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 10)
                        .background(
                            NoriColors.accentSubtle,
                            in: RoundedRectangle(cornerRadius: 14, style: .continuous)
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: 14, style: .continuous)
                                .strokeBorder(NoriColors.accentBorder, lineWidth: 1)
                        )
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Action needed: \(blocker.text). Review in Tasks.")
                }

                // Completion Criteria Checklist
                if !goal.completionCriteria.isEmpty {
                    VStack(alignment: .leading, spacing: 7) {
                        ForEach(goal.completionCriteria) { criterion in
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
                    }
                    .padding(.top, 2)
                }
            }
        }
    }

    // MARK: - Helpers

    private func completionRatio(for goal: Goal) -> CGFloat {
        let total = goal.completionCriteria.count
        guard total > 0 else { return goal.status == .completed ? 1.0 : 0.0 }
        return min(1.0, max(0.0, CGFloat(goal.metCriteriaCount) / CGFloat(total)))
    }

    private func subtitle(for goal: Goal) -> String {
        var parts: [String] = []
        if !goal.completionCriteria.isEmpty {
            parts.append("\(goal.metCriteriaCount) of \(goal.completionCriteria.count) done")
        }
        if let deadline = goal.deadline, !deadline.isEmpty {
            parts.append("Target \(NoriDateFormatting.shortDate(deadline))")
        }
        if parts.isEmpty, !goal.progress.isEmpty {
            return goal.progress
        }
        return parts.isEmpty ? statusLabel(for: goal.status) : parts.joined(separator: " · ")
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
