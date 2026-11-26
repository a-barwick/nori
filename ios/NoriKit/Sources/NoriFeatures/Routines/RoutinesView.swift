import SwiftUI
import NoriCore
import NoriDesign

public struct RoutinesView: View {
    @Bindable private var store: NoriStore
    @State private var busyID: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var activeAutomations: [Automation] {
        let nonCancelled = store.automations.filter { $0.status != .cancelled }
        let enabled = nonCancelled.filter { $0.status == .enabled }
        let paused = nonCancelled.filter { $0.status != .enabled }
        return enabled + paused
    }

    private var nextEnabledAutomation: Automation? {
        let enabled = activeAutomations.filter { $0.status == .enabled }
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
                    return NoriDateFormatting.shortDateTime(nextAt)
                }
                return triggerSummary(for: next.trigger)
            }()
            return "Next up: \(next.title), \(when)."
        }
        return "All routines are currently paused."
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
        .refreshable {
            await store.refreshRoutinesAndGoals()
        }
    }


    private func routineRow(_ automation: Automation, showDivider: Bool) -> some View {
        let isEnabled = automation.status == .enabled
        let isBusy = busyID == automation.id

        return VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 10) {
                HStack(alignment: .top, spacing: 8) {
                    Text(automation.title)
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(isEnabled ? NoriColors.ink : NoriColors.ink2)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    NoriPill(
                        isEnabled ? "Scheduled" : automation.status.displayLabel,
                        tone: isEnabled ? .scheduled : .neutral
                    )
                }

                HStack(alignment: .center, spacing: 10) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(triggerSummary(for: automation.trigger))
                            .font(.system(size: 12.5))
                            .foregroundStyle(NoriColors.ink2)
                            .lineLimit(1)

                        if isEnabled,
                           automation.trigger.at == nil,
                           let nextAt = automation.nextAt, !nextAt.isEmpty {
                            Text("Next \(NoriDateFormatting.shortDateTime(nextAt))")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(NoriColors.scheduled)
                                .lineLimit(1)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

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
                                    .font(.system(size: 12, weight: .semibold))
                            }
                            .foregroundStyle(NoriColors.accent)
                            .padding(.horizontal, 10)
                            .frame(height: 28)
                            .background(NoriColors.accentSubtle, in: Capsule())
                            .overlay(
                                Capsule()
                                    .strokeBorder(NoriColors.accentBorder, lineWidth: 1)
                            )
                        }
                        .buttonStyle(.plain)
                        .disabled(isBusy)
                        .accessibilityLabel("Run \(automation.title) now")

                        Button {
                            busyID = automation.id
                            Task {
                                await store.toggleAutomation(automationID: automation.id)
                                busyID = nil
                            }
                        } label: {
                            Text(isEnabled ? "Pause" : "Resume")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(isEnabled ? NoriColors.ink2 : NoriColors.ink)
                                .padding(.horizontal, 9)
                                .frame(height: 28)
                                .background(NoriColors.fill1, in: Capsule())
                                .overlay(
                                    Capsule()
                                        .strokeBorder(NoriColors.hairline, lineWidth: 1)
                                )
                        }
                        .buttonStyle(.plain)
                        .disabled(isBusy)
                        .accessibilityLabel("\(isEnabled ? "Pause" : "Resume") \(automation.title)")
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


    private func triggerSummary(for trigger: AutomationTrigger) -> String {
        if let displayCadence = trigger.displayCadence, !displayCadence.isEmpty {
            return displayCadence
        }
        if let rrule = trigger.rrule, !rrule.isEmpty {
            return rrule
        }
        if let at = trigger.at, !at.isEmpty {
            return "Once · \(NoriDateFormatting.shortDateTime(at))"
        }
        return trigger.summaryLabel
    }
}
