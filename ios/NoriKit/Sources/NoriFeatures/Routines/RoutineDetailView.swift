import SwiftUI
import NoriCore
import NoriDesign

public struct RoutineDetailView: View {
    @Bindable private var store: NoriStore
    public let automationID: String

    @State private var isActing: Bool = false
    @State private var showCancelConfirm: Bool = false
    @State private var showEditSheet: Bool = false
    @State private var occurrences: [AutomationOccurrence] = []
    @State private var isLoadingHistory: Bool = false

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
            await refreshDetail()
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
                    ScrollView(.horizontal, showsIndicators: false) {
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

                                if isEnabled {
                                    Menu {
                                        Button("1 hour") {
                                            snooze(hours: 1, automationID: automation.id)
                                        }
                                        Button("Tomorrow morning") {
                                            snoozeUntilTomorrowMorning(automationID: automation.id)
                                        }
                                        Button("Next week") {
                                            snooze(hours: 24 * 7, automationID: automation.id)
                                        }
                                    } label: {
                                        Label("Snooze", systemImage: "moon.zzz")
                                            .font(.system(size: 12.5, weight: .semibold))
                                            .foregroundStyle(NoriColors.ink)
                                            .padding(.horizontal, 12)
                                            .frame(height: 32)
                                            .background(NoriColors.surface, in: Capsule())
                                            .overlay(
                                                Capsule()
                                                    .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
                                            )
                                    }
                                    .disabled(isActing)
                                }
                            }

                            Button {
                                showEditSheet = true
                            } label: {
                                Label("Edit", systemImage: "pencil")
                            }
                            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                            .disabled(isActing)

                            Button {
                                showCancelConfirm = true
                            } label: {
                                Text("Cancel")
                            }
                            .buttonStyle(NoriButtonStyle(.danger, compact: true))
                            .disabled(isActing)
                        }
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

                historySection
                detailsSection(for: automation)
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .refreshable {
            await refreshDetail()
        }
        .sheet(isPresented: $showEditSheet) {
            RoutineEditorSheet(store: store, existing: automation)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
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

    @ViewBuilder
    private var historySection: some View {
        if isLoadingHistory && occurrences.isEmpty {
            NoriSectionHeader("History")
            HStack(spacing: 8) {
                ProgressView().controlSize(.small).tint(NoriColors.accent)
                Text("Loading recent runs…")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
            }
            .padding(.vertical, 6)
        } else if !occurrences.isEmpty {
            NoriSectionHeader("History")
            NoriRows {
                ForEach(Array(occurrences.prefix(15).enumerated()), id: \.element.id) { idx, occ in
                    let taskID = occ.taskIDs.first
                    let titleText = (occ.summary?.isEmpty == false)
                        ? (occ.summary ?? "")
                        : NoriDateFormatting.shortDateTime(occ.scheduledAt)
                    let detailText = (occ.summary?.isEmpty == false)
                        ? NoriDateFormatting.shortDateTime(occ.scheduledAt)
                        : occ.error?.message
                    let pill = occurrencePill(for: occ.status)
                    Group {
                        if let taskID, !taskID.isEmpty {
                            Button {
                                store.pendingDeepLink = .workDetail(taskID)
                            } label: {
                                NoriRow(
                                    title: titleText,
                                    detail: detailText,
                                    showChevron: true,
                                    showDivider: idx < min(occurrences.count, 15) - 1,
                                    wrapTitle: true
                                ) {
                                    if let pill {
                                        NoriPill(pill.label, tone: pill.tone)
                                    }
                                }
                            }
                            .buttonStyle(.plain)
                        } else {
                            NoriRow(
                                title: titleText,
                                detail: detailText,
                                showChevron: false,
                                showDivider: idx < min(occurrences.count, 15) - 1,
                                wrapTitle: true
                            ) {
                                if let pill {
                                    NoriPill(pill.label, tone: pill.tone)
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    private func occurrencePill(for status: String) -> (label: String, tone: NoriTone)? {
        switch status {
        case "completed": return nil
        case "failed": return ("Failed", .danger)
        case "running", "accepted": return ("Running", .working)
        case "skipped": return ("Skipped", .neutral)
        case "cancelled": return ("Cancelled", .neutral)
        case "expired": return ("Expired", .neutral)
        default: return nil
        }
    }

    private func refreshDetail() async {
        isLoadingHistory = true
        async let autoTask = store.loadAutomation(id: automationID)
        async let historyTask = store.loadAutomationOccurrences(automationID: automationID)
        _ = await autoTask
        occurrences = await historyTask
        isLoadingHistory = false
    }

    private func snooze(hours: Int, automationID: String) {
        let target = Date().addingTimeInterval(TimeInterval(hours * 3600))
        performAction { await store.snoozeAutomation(automationID: automationID, until: target) }
    }

    private func snoozeUntilTomorrowMorning(automationID: String) {
        var comps = Calendar.current.dateComponents([.year, .month, .day], from: Date().addingTimeInterval(86_400))
        comps.hour = 9
        comps.minute = 0
        let target = Calendar.current.date(from: comps) ?? Date().addingTimeInterval(86_400)
        performAction { await store.snoozeAutomation(automationID: automationID, until: target) }
    }

    private func performAction(_ work: @escaping () async -> Void) {
        isActing = true
        Task {
            await work()
            occurrences = await store.loadAutomationOccurrences(automationID: automationID)
            isActing = false
        }
    }
}
