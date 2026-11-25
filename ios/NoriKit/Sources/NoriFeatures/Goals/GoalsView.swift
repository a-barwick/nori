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
                        NoriCard {
                            VStack(alignment: .leading, spacing: 10) {
                                HStack(alignment: .top, spacing: 8) {
                                    VStack(alignment: .leading, spacing: 3) {
                                        Text(goal.title)
                                            .font(.system(size: 15.5, weight: .bold))
                                            .foregroundStyle(NoriColors.ink)

                                        Text(subtitle(for: goal))
                                            .font(.system(size: 12))
                                            .foregroundStyle(NoriColors.ink2)
                                    }

                                    Spacer(minLength: 8)

                                    NoriPill(
                                        goal.isBlocked ? "Blocked" : statusLabel(for: goal.status),
                                        tone: goal.isBlocked ? .warning : statusTone(for: goal.status)
                                    )
                                }

                                // Tappable Blocker Banner -> Work -> Needs you
                                ForEach(goal.blockers) { blocker in
                                    Button {
                                        onJumpToNeedsYou()
                                    } label: {
                                        HStack(spacing: 10) {
                                            VStack(alignment: .leading, spacing: 2) {
                                                Text("Blocked")
                                                    .font(.system(size: 13, weight: .semibold))
                                                    .foregroundStyle(NoriColors.ink)
                                                Text(blocker.text)
                                                    .font(.system(size: 12))
                                                    .foregroundStyle(NoriColors.ink2)
                                                    .lineLimit(2)
                                                    .multilineTextAlignment(.leading)
                                            }
                                            .frame(maxWidth: .infinity, alignment: .leading)

                                            Text("Answer")
                                                .font(.system(size: 12, weight: .semibold))
                                                .foregroundStyle(NoriColors.accent)
                                                .padding(.horizontal, 10)
                                                .padding(.vertical, 5)
                                                .background(NoriColors.surface, in: Capsule())
                                                .overlay(
                                                    Capsule()
                                                        .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
                                                )
                                        }
                                        .padding(.horizontal, 12)
                                        .padding(.vertical, 10)
                                        .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                                        .overlay(
                                            RoundedRectangle(cornerRadius: 14, style: .continuous)
                                                .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
                                        )
                                    }
                                    .buttonStyle(.plain)
                                }

                                // Completion Criteria
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
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 28)
        }
        .background(NoriColors.canvas)
        .navigationTitle("Goals")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .refreshable {
            await store.refreshRoutinesAndGoals()
        }
    }

    private func subtitle(for goal: Goal) -> String {
        var parts = ["\(goal.metCriteriaCount) of \(goal.completionCriteria.count) done"]
        if let deadline = goal.deadline, !deadline.isEmpty {
            parts.append("By \(NoriDateFormatting.shortDate(deadline))")
        }
        return parts.joined(separator: " · ")
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
        case .active: return .accent
        case .completed: return .ok
        case .paused, .abandoned: return .neutral
        }
    }
}
