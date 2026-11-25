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
        store.automations.filter { $0.status != .cancelled }
    }

    private var nextRunLine: String {
        if let next = activeAutomations.first(where: { $0.status == .enabled }) {
            let when: String = {
                if let nextAt = next.nextAt, !nextAt.isEmpty {
                    return NoriDateFormatting.shortDateTime(nextAt)
                }
                return triggerSummary(for: next.trigger)
            }()
            return "Next: \(next.title), \(when)."
        }
        return activeAutomations.isEmpty
            ? "No automations scheduled yet."
            : "All automations are currently paused."
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

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                NoriLine(nextRunLine)

                if activeAutomations.isEmpty {
                    NoriEmptyState(
                        title: "No automations yet",
                        message: "Ask Nori in Chat to set up recurring briefs, monitors, or scheduled checks."
                    )
                } else {
                    ForEach(activeAutomations) { automation in
                        let isEnabled = automation.status == .enabled
                        let isBusy = busyID == automation.id

                        NoriCard {
                            VStack(alignment: .leading, spacing: 10) {
                                HStack(alignment: .top, spacing: 8) {
                                    VStack(alignment: .leading, spacing: 3) {
                                        Text(automation.title)
                                            .font(.system(size: 15, weight: .semibold))
                                            .foregroundStyle(NoriColors.ink)

                                        Text("\(triggerSummary(for: automation.trigger)) · $\(String(format: "%.2f", automation.budgetUSD))/run")
                                            .font(.system(size: 12.5))
                                            .foregroundStyle(NoriColors.ink2)

                                        if isEnabled, let nextAt = automation.nextAt, !nextAt.isEmpty {
                                            Text("Next run: \(NoriDateFormatting.shortDateTime(nextAt))")
                                                .font(.system(size: 12, weight: .medium))
                                                .foregroundStyle(NoriColors.accent)
                                        }
                                    }

                                    Spacer(minLength: 8)

                                    NoriPill(
                                        isEnabled ? "On" : automation.status.displayLabel,
                                        tone: isEnabled ? .ok : .neutral
                                    )
                                }

                                if !automation.instruction.isEmpty {
                                    Text(automation.instruction)
                                        .font(.system(size: 13))
                                        .foregroundStyle(NoriColors.ink2)
                                        .lineLimit(3)
                                }

                                HStack(spacing: 8) {
                                    Button {
                                        busyID = automation.id
                                        Task {
                                            await store.runAutomationNow(automationID: automation.id)
                                            busyID = nil
                                        }
                                    } label: {
                                        Label("Run now", systemImage: "bolt.fill")
                                    }
                                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                                    .disabled(isBusy)

                                    Button {
                                        busyID = automation.id
                                        Task {
                                            await store.toggleAutomation(automationID: automation.id)
                                            busyID = nil
                                        }
                                    } label: {
                                        Text(isEnabled ? "Pause" : "Resume")
                                    }
                                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                                    .disabled(isBusy)
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
        .navigationTitle("Automations")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .refreshable {
            await store.refreshRoutinesAndGoals()
        }
    }
}
