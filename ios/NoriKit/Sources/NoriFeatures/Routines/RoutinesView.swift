import SwiftUI
import NoriCore
import NoriDesign

public struct RoutinesView: View {
    @Bindable private var store: NoriStore
    @State private var busyID: String?
    @State private var selectedRoutineID: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var activeAutomations: [Automation] {
        let nonCancelled = store.automations.filter { $0.status != .cancelled }
        let enabled = nonCancelled.filter { $0.status == .enabled && !$0.isSpentOneTime }
        let other = nonCancelled.filter { $0.status != .enabled || $0.isSpentOneTime }
        return enabled + other
    }

    private var nextEnabledAutomation: Automation? {
        let enabled = activeAutomations.filter { $0.status == .enabled && !$0.isSpentOneTime }
        return enabled.min { lhs, rhs in
            let lDate = lhs.nextAt.flatMap { NoriDateFormatting.parse($0) }
            let rDate = rhs.nextAt.flatMap { NoriDateFormatting.parse($0) }
            switch (lDate, rDate) {
            case (let l?, let r?): return l < r
            case (_?, nil): return true
            case (nil, _?): return false
            case (nil, nil): return false
            }
        }
    }

    private var nextRunLine: String? {
        guard !activeAutomations.isEmpty else { return nil }
        if let next = nextEnabledAutomation {
            let when: String = {
                if let nextAt = next.nextAt, !nextAt.isEmpty {
                    return NoriDateFormatting.relative(nextAt)
                }
                return next.trigger.summaryLabel
            }()
            return "Next up: \(next.title), \(when)."
        }
        return "All routines are paused."
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let nextRunLine {
                    NoriLine(nextRunLine)
                }

                if activeAutomations.isEmpty {
                    NoriEmptyState(
                        title: "No routines yet",
                        message: "Ask Nori in Chat to set up recurring briefs, monitors, or scheduled checks."
                    )
                } else {
                    NoriRows {
                        ForEach(Array(activeAutomations.enumerated()), id: \.element.id) { idx, automation in
                            routineRow(
                                automation,
                                showDivider: idx < activeAutomations.count - 1
                            )
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationDestination(item: $selectedRoutineID) { routineID in
            RoutineDetailView(store: store, automationID: routineID)
        }
        .refreshable {
            await store.refreshRoutinesAndGoals()
        }
    }

    private func statusBadge(for automation: Automation) -> (label: String, tone: NoriTone)? {
        if automation.lastRunFailed {
            return ("Last run failed", .danger)
        }
        if automation.isSpentOneTime {
            return ("Done", .neutral)
        }
        if automation.isSnoozed {
            return ("Snoozed", .neutral)
        }
        if automation.status != .enabled {
            return (automation.status.displayLabel, .neutral)
        }
        return nil
    }

    private func routineRow(_ automation: Automation, showDivider: Bool) -> some View {
        let isEnabled = automation.status == .enabled && !automation.isSpentOneTime
        let isBusy = busyID == automation.id
        let badge = statusBadge(for: automation)

        return VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 10) {
                Button {
                    selectedRoutineID = automation.id
                } label: {
                    HStack(alignment: .top, spacing: 8) {
                        Text(automation.title)
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(isEnabled ? NoriColors.ink : NoriColors.ink2)
                            .lineLimit(2)
                            .multilineTextAlignment(.leading)
                            .frame(maxWidth: .infinity, alignment: .leading)

                        if let badge {
                            NoriPill(badge.label, tone: badge.tone)
                        }
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)

                HStack(alignment: .center, spacing: 10) {
                    Button {
                        selectedRoutineID = automation.id
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(automation.trigger.summaryLabel)
                                .font(.system(size: 12.5))
                                .foregroundStyle(NoriColors.ink2)
                                .lineLimit(1)

                            if isEnabled,
                               automation.trigger.at == nil,
                               let nextAt = automation.nextAt, !nextAt.isEmpty {
                                Text("Next \(NoriDateFormatting.relative(nextAt))")
                                    .font(.system(size: 12, weight: .medium))
                                    .foregroundStyle(NoriColors.accent)
                                    .lineLimit(1)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)

                    HStack(spacing: 6) {
                        Button {
                            busyID = automation.id
                            Task {
                                await store.runAutomationNow(automationID: automation.id)
                                busyID = nil
                            }
                        } label: {
                            HStack(spacing: 4) {
                                if isBusy {
                                    ProgressView()
                                        .controlSize(.mini)
                                        .tint(NoriColors.accent)
                                } else {
                                    Image(systemName: "play.fill")
                                        .font(.system(size: 9.5, weight: .semibold))
                                }
                                Text("Run")
                                    .font(.system(size: 12.5, weight: .semibold))
                            }
                            .foregroundStyle(isBusy ? NoriColors.disabled : NoriColors.accent)
                            .padding(.horizontal, 12)
                            .frame(minHeight: 34)
                            .background(isBusy ? NoriColors.disabledBg : NoriColors.selected, in: Capsule())
                        }
                        .buttonStyle(.plain)
                        .disabled(isBusy)
                        .accessibilityLabel("Run \(automation.title) now")

                        if !automation.isSpentOneTime {
                            Button {
                                busyID = automation.id
                                Task {
                                    await store.toggleAutomation(automationID: automation.id)
                                    busyID = nil
                                }
                            } label: {
                                Text(isEnabled ? "Pause" : "Resume")
                                    .font(.system(size: 12.5, weight: .medium))
                                    .foregroundStyle(isBusy ? NoriColors.disabled : (isEnabled ? NoriColors.ink2 : NoriColors.ink))
                                    .padding(.horizontal, 12)
                                    .frame(minHeight: 34)
                                    .background(isBusy ? NoriColors.disabledBg : NoriColors.elevated, in: Capsule())
                                    .overlay(
                                        Capsule()
                                            .strokeBorder(isBusy ? .clear : NoriColors.cardBorder, lineWidth: 1)
                                    )
                            }
                            .buttonStyle(.plain)
                            .disabled(isBusy)
                            .accessibilityLabel("\(isEnabled ? "Pause" : "Resume") \(automation.title)")
                        }
                    }
                }
            }
            .padding(.horizontal, 13)
            .padding(.vertical, 12)

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .padding(.leading, 13)
            }
        }
    }
}
