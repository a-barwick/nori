import SwiftUI
import NoriCore
import NoriDesign

public struct RoutineDetailView: View {
    @Bindable private var store: NoriStore
    public let automationID: String

    @State private var isActing: Bool = false
    @State private var showCancelConfirm: Bool = false

    public init(store: NoriStore, automationID: String) {
        self.store = store
        self.automationID = automationID
    }

    private var automation: Automation? {
        store.automations.first(where: { $0.id == automationID })
    }

    public var body: some View {
        Group {
            if let automation {
                detailBody(for: automation)
            } else {
                NoriEmptyState(
                    title: "Routine not found",
                    message: "This routine is no longer available."
                )
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(NoriColors.canvas)
            }
        }
        .navigationTitle("Routine")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .toolbar {
            if let automation, let badge = statusBadge(for: automation) {
                ToolbarItem(placement: .automatic) {
                    NoriPill(badge.label, tone: badge.tone)
                }
            }
        }
        .task(id: automationID) {
            _ = await store.loadAutomation(id: automationID)
        }
    }

    private func detailBody(for automation: Automation) -> some View {
        let isEnabled = automation.status == .enabled && !automation.isSpentOneTime

        return ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                Text(automation.title)
                    .font(.system(size: 19, weight: .bold))
                    .foregroundStyle(NoriColors.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.bottom, 8)

                if let subtitle = statusLine(for: automation) {
                    NoriLine(subtitle)
                        .padding(.bottom, 14)
                }

                if automation.status != .cancelled {
                    HStack(spacing: 8) {
                        Button {
                            performAction { await store.runAutomationNow(automationID: automation.id) }
                        } label: {
                            Label("Run now", systemImage: "bolt.fill")
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                        .disabled(isActing)

                        if !automation.isSpentOneTime {
                            Button {
                                performAction { await store.toggleAutomation(automationID: automation.id) }
                            } label: {
                                Label(isEnabled ? "Pause" : "Resume", systemImage: isEnabled ? "pause.fill" : "play.fill")
                            }
                            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                            .disabled(isActing)
                        }

                        Button {
                            showCancelConfirm = true
                        } label: {
                            Text("Cancel")
                        }
                        .buttonStyle(NoriButtonStyle(.danger, compact: true))
                        .disabled(isActing)

                        Spacer(minLength: 0)
                    }
                    .padding(.bottom, 8)
                }

                if !automation.instruction.isEmpty {
                    NoriSectionHeader("What it does")
                    NoriCard(padding: 14) {
                        Text(automation.instruction)
                            .font(NoriTypography.body)
                            .foregroundStyle(NoriColors.ink)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }

                NoriSectionHeader("May do without asking")
                if automation.authorityRuleIDs.isEmpty {
                    Text("Asks before every action.")
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink2)
                } else {
                    let count = automation.authorityRuleIDs.count
                    Text(count == 1 ? "1 standing permission rule" : "\(count) standing permission rules")
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink2)
                }

                detailsSection(for: automation)
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .refreshable {
            _ = await store.loadAutomation(id: automationID)
        }
        .confirmationDialog(
            "Cancel this routine?",
            isPresented: $showCancelConfirm,
            titleVisibility: .visible
        ) {
            Button("Cancel routine", role: .destructive) {
                performAction { _ = await store.cancelAutomation(automationID: automation.id) }
            }
            Button("Keep", role: .cancel) {}
        } message: {
            Text("It won’t run again. Work already started keeps going until you stop it in Work.")
        }
    }

    private func detailsSection(for automation: Automation) -> some View {
        let whenText: String = {
            if automation.trigger.kind == "recurring", let tz = automation.trigger.timezone, !tz.isEmpty {
                return "\(automation.trigger.summaryLabel) (\(tz))"
            }
            return automation.trigger.summaryLabel
        }()

        let missedText: String = {
            switch automation.missedRun {
            case .latest: return "Runs once when Nori is back"
            case .skip: return "Skipped"
            case .all: return "Every missed run happens"
            }
        }()

        return VStack(alignment: .leading, spacing: 0) {
            NoriSectionHeader("Details")
                .padding(.top, 8)
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 16, verticalSpacing: 8) {
                detailRow(label: "When", value: whenText)
                detailRow(label: "If missed", value: missedText)
                GridRow {
                    Text("Budget")
                        .font(.system(size: 13))
                        .foregroundStyle(NoriColors.ink3)
                    Text(String(format: "$%.2f per run", automation.budgetUSD))
                        .font(.system(size: 13, weight: .medium, design: .monospaced))
                        .foregroundStyle(NoriColors.ink)
                }
                if let endAt = automation.endAt, !endAt.isEmpty {
                    detailRow(label: "Ends", value: NoriDateFormatting.shortDateTime(endAt))
                }
            }
            .padding(.top, 2)
        }
    }

    private func detailRow(label: String, value: String) -> some View {
        GridRow {
            Text(label)
                .font(.system(size: 13))
                .foregroundStyle(NoriColors.ink3)
            Text(value)
                .font(.system(size: 13))
                .foregroundStyle(NoriColors.ink)
                .fixedSize(horizontal: false, vertical: true)
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

    private func statusLine(for automation: Automation) -> String? {
        if automation.status == .enabled, let nextAt = automation.nextAt, !nextAt.isEmpty {
            return "Next run \(NoriDateFormatting.shortDateTime(nextAt))."
        }
        if automation.isSpentOneTime {
            return "This one-time routine has run."
        }
        if automation.status == .paused {
            return "Paused until you resume it."
        }
        if automation.status == .cancelled {
            return "Cancelled."
        }
        return nil
    }

    private func performAction(_ work: @escaping () async -> Void) {
        isActing = true
        Task {
            await work()
            isActing = false
        }
    }
}
