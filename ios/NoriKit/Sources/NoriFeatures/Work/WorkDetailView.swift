import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public struct WorkDetailView: View {
    @Bindable private var store: NoriStore
    public let taskID: String
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void

    @State private var steerDraft: String = ""
    @State private var isSteering: Bool = false
    @State private var isStopping: Bool = false
    @State private var isContinuing: Bool = false
    @State private var continueBudgetUSD: Double = 2.00
    @State private var didInitializeBudget: Bool = false
    @State private var isLoadingDetail: Bool = false
    @State private var expandedSearchGroupIDs: Set<String> = []

    public init(
        store: NoriStore,
        taskID: String,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in }
    ) {
        self.store = store
        self.taskID = taskID
        self.onOpenDocument = onOpenDocument
    }

    private var detail: WorkDetail? {
        store.workDetails[taskID]
    }

    private var item: WorkItem? {
        detail?.item ?? store.workItems.first(where: { $0.taskID == taskID })
    }

    private var pendingInteractions: [Interaction] {
        let fromDetail = detail?.interactions.filter { $0.status == .pending } ?? []
        if !fromDetail.isEmpty { return fromDetail }
        return store.interactions.filter { $0.taskID == taskID && $0.status == .pending }
    }

    private var shownOperations: [OperationProjection] {
        (detail?.operations ?? []).filter { $0.name != "capability_load" }
    }

    private var groupedActions: [WorkActionEntry] {
        WorkActionEntry.group(shownOperations)
    }

    private var timelineEvents: [RunEvent] {
        let toolNames = Set((detail?.operations ?? []).map(\.name))
        return (detail?.events ?? []).filter { !toolNames.contains($0.text) && $0.text != "capability_load" }
    }

    private var savedItems: [NoriCore.LibraryItem] {
        guard let item, !item.resultIDs.isEmpty else { return [] }
        return item.resultIDs.compactMap { resultID in
            if let lib = store.libraryItems.first(where: { $0.id == resultID }) {
                return lib
            }
            if let saved = store.session.results?.first(where: { $0.id == resultID }) {
                return NoriCore.LibraryItem(
                    id: saved.id,
                    rootID: saved.rootID,
                    version: saved.version,
                    kind: .document,
                    title: saved.title,
                    name: saved.name,
                    mediaType: saved.mediaType,
                    size: saved.size,
                    url: saved.url,
                    taskID: saved.runID,
                    provenance: Provenance(content: .generated),
                    createdAt: saved.createdAt
                )
            }
            return nil
        }
    }

    public var body: some View {
        Group {
            if let item {
                detailScrollView(item: item)
            } else if isLoadingDetail {
                VStack(spacing: 12) {
                    ProgressView()
                    Text("Loading task details…")
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(NoriColors.canvas)
            } else {
                NoriEmptyState(
                    title: "Task not found",
                    message: "This task isn’t in your recent work."
                )
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(NoriColors.canvas)
            }
        }
        .navigationTitle("Task")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .toolbar {
            if let item {
                ToolbarItem(placement: .automatic) {
                    let pill = WorkStatusPresentation.pill(for: item)
                    NoriPill(pill.label, tone: pill.tone)
                }
            }
        }
        .task(id: taskID) {
            await loadDetail()
        }
    }

    // MARK: - Detail Content

    private func detailScrollView(item: WorkItem) -> some View {
        let isRunning = item.status == .running

        return ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                // 1. Task Title & Progress / Status Line
                Text(item.title)
                    .font(.system(size: 19, weight: .bold))
                    .foregroundStyle(NoriColors.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.bottom, 8)

                NoriLine(
                    noriProgressLine(for: item),
                    state: isRunning ? .thinking : .idle
                )
                .padding(.bottom, 14)

                if isRunning {
                    // Running State Ordering:
                    // 2. Pending action needed cards
                    if !pendingInteractions.isEmpty {
                        NoriSectionHeader("Action needed")
                        VStack(spacing: 10) {
                            ForEach(pendingInteractions) { interaction in
                                InteractionCardView(
                                    store: store,
                                    interaction: interaction,
                                    compact: true
                                )
                            }
                        }
                        .padding(.bottom, 10)
                    }

                    // 3. Uncertain notice (if any)
                    if item.operations.uncertain > 0 {
                        uncertainNotice
                            .padding(.bottom, 14)
                    }

                    // 4. Steer & Stop controls (or Stopping notice)
                    if !item.cancelRequested {
                        steerSection(item: item)
                    } else {
                        NoriNotice(
                            title: "Stopping task",
                            detail: "Work already done stays done; anything unconfirmed stays marked.",
                            tone: .neutral,
                            systemImage: "stop.circle"
                        )
                        .padding(.bottom, 4)
                    }

                    // 5. Actions audit & Activity timeline
                    actionsAuditSection
                    activityTimelineSection

                    // Preserve early result / artifacts if already generated while running
                    resultSection(item: item)
                    savedArtifactsSection
                } else {
                    // Finished State Ordering:
                    // 2. Continue card (if failed / budget-capped / incomplete)
                    if shouldShowContinueCard(for: item) {
                        continueCard(item: item)
                            .padding(.bottom, 6)
                    }

                    if item.operations.uncertain > 0 {
                        uncertainNotice
                            .padding(.top, shouldShowContinueCard(for: item) ? 8 : 0)
                            .padding(.bottom, 6)
                    }

                    // 3. Result
                    resultSection(item: item)

                    // 4. Saved artifacts
                    savedArtifactsSection

                    // 5. Actions audit
                    actionsAuditSection

                    // 6. Activity timeline
                    activityTimelineSection
                }

                // Bottom Footer: Task budget & origin demoted to a compact single strip
                taskFooterStrip(item: item)
                    .padding(.top, 18)
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 32)
        }
        .background(NoriColors.canvas)
        .refreshable {
            await loadDetail()
        }
        .onAppear {
            if !didInitializeBudget {
                didInitializeBudget = true
                let currentBudget = item.cost.budgetUSD ?? 1.00
                continueBudgetUSD = max(2.00, (currentBudget * 2.0).rounded(.up))
            }
        }
    }

    // MARK: - Subsections

    private func shouldShowContinueCard(for item: WorkItem) -> Bool {
        item.status == .failed || item.isBudgetExhausted
    }

    private var uncertainNotice: some View {
        NoriNotice(
            title: "Checking an unconfirmed action",
            detail: "An action may or may not have gone through. Nori is checking with the provider and won’t repeat it.",
            tone: .warning,
            systemImage: "exclamationmark.triangle.fill"
        )
    }

    @ViewBuilder
    private func resultSection(item: WorkItem) -> some View {
        if let resultText = detail?.result, !resultText.isEmpty {
            NoriSectionHeader(item.status == .failed ? "Partial answer" : "Result")
            NoriCard(padding: 14) {
                Text(resultText)
                    .font(.system(size: 13.5))
                    .lineSpacing(3)
                    .foregroundStyle(NoriColors.ink)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    @ViewBuilder
    private var savedArtifactsSection: some View {
        if !savedItems.isEmpty {
            NoriSectionHeader("Saved artifacts")
            NoriRows {
                ForEach(Array(savedItems.enumerated()), id: \.element.id) { idx, doc in
                    Button {
                        onOpenDocument(doc)
                    } label: {
                        NoriRow(
                            title: doc.title,
                            detail: doc.previewText ?? doc.provenance.content.displayLabel,
                            systemImage: "doc.text",
                            iconTone: .accent,
                            showChevron: true,
                            showDivider: idx < savedItems.count - 1
                        ) {
                            if doc.version > 1 {
                                NoriPill("v\(doc.version)", tone: .neutral)
                            }
                        }
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    @ViewBuilder
    private var actionsAuditSection: some View {
        let entries = groupedActions
        if !entries.isEmpty {
            NoriSectionHeader("Actions (\(entries.count))")
            NoriRows {
                ForEach(Array(entries.enumerated()), id: \.element.id) { idx, entry in
                    let showDivider = idx < entries.count - 1
                    switch entry {
                    case .single(let op):
                        operationRow(op: op, showDivider: showDivider)
                    case .webSearchGroup(let groupID, let ops):
                        webSearchGroupRow(
                            groupID: groupID,
                            title: entry.groupTitle ?? "Searched the web (\(ops.count) queries)",
                            operations: ops,
                            badge: entry.groupBadge,
                            showDivider: showDivider
                        )
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var activityTimelineSection: some View {
        if !timelineEvents.isEmpty {
            NoriSectionHeader("Activity")
            ActivityTimelineView(events: timelineEvents)
                .padding(.top, 4)
        }
    }

    private func noriProgressLine(for item: WorkItem) -> String {
        if item.status == .running && item.cancelRequested {
            return "Stopping."
        }
        if let err = item.error?.message, !err.isEmpty {
            return err
        }
        if let progress = item.progress, !progress.isEmpty {
            return progress
        }
        switch item.status {
        case .running: return "Working on it."
        case .completed: return "Done."
        case .failed: return "This task failed."
        case .cancelled: return "Stopped."
        }
    }

    private func taskFooterStrip(item: WorkItem) -> some View {
        let spent = item.cost.amountUSD ?? 0
        let budget = max(0.01, item.cost.budgetUSD ?? 1.00)
        let ratio = min(1.0, max(0, spent / budget))
        let isNearCap = item.cost.budgetUSD != nil && ratio >= 0.95
        let costLabel: String = {
            if item.cost.status == .unknown && item.cost.amountUSD == nil {
                return "Cost pending"
            }
            if let budgetUSD = item.cost.budgetUSD {
                return String(format: "$%.2f / $%.2f", spent, budgetUSD)
            }
            return String(format: "$%.2f", spent)
        }()

        return HStack(spacing: 8) {
            Text("From \(item.origin.displayLabel) · \(NoriDateFormatting.shortDateTime(item.startedAt))")
                .font(.system(size: 11.5))
                .foregroundStyle(NoriColors.ink3)
                .lineLimit(1)
                .minimumScaleFactor(0.85)
                .truncationMode(.tail)

            Spacer(minLength: 8)

            HStack(spacing: 6) {
                if item.cost.budgetUSD != nil {
                    ZStack(alignment: .leading) {
                        Capsule()
                            .fill(NoriColors.fill2)
                        Capsule()
                            .fill(isNearCap ? NoriColors.danger : NoriColors.brandPrimary)
                            .frame(width: max(4, 28 * ratio))
                    }
                    .frame(width: 28, height: 4)
                }

                Text(costLabel)
                    .font(.system(size: 11.5, weight: .medium, design: .monospaced))
                    .foregroundStyle(isNearCap ? NoriColors.danger : NoriColors.ink2)
                    .lineLimit(1)
                    .fixedSize()
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 9)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                .strokeBorder(NoriColors.hairline, lineWidth: 1)
        )
    }

    private func steerSection(item: WorkItem) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            NoriSectionHeader("Steer this task")
            NoriCard(padding: 13) {
                VStack(alignment: .leading, spacing: 10) {
                    TextField("Also CC finance, or change scope…", text: $steerDraft, axis: .vertical)
                        .font(.system(size: 14.5))
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        .lineLimit(2...5)
                        .padding(.horizontal, 11)
                        .padding(.vertical, 9)
                        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        .overlay(
                            RoundedRectangle(cornerRadius: 12, style: .continuous)
                                .strokeBorder(NoriColors.hairline, lineWidth: 1)
                        )

                    HStack(spacing: 8) {
                        Button {
                            sendSteer()
                        } label: {
                            Text(isSteering ? "Sending…" : "Send update")
                        }
                        .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                        .disabled(isSteering || steerDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)

                        Button {
                            stopCurrentTask()
                        } label: {
                            HStack(spacing: 5) {
                                Image(systemName: "stop.fill")
                                    .font(.system(size: 10))
                                Text(isStopping ? "Stopping…" : "Stop")
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.danger, compact: true))
                        .disabled(isStopping)
                    }
                }
            }
        }
    }

    private func continueCard(item: WorkItem) -> some View {
        let isBudgetCapped = item.isBudgetExhausted
        return NoriCard(
            padding: 14,
            backgroundColor: NoriColors.accentSubtle,
            borderColor: NoriColors.hairlineStrong
        ) {
            VStack(alignment: .leading, spacing: 8) {
                Text(isBudgetCapped ? "Continue with a higher budget" : "Continue this task")
                    .font(.system(size: 13.5, weight: .bold))
                    .foregroundStyle(NoriColors.ink)

                Text("Completed actions aren’t repeated. Saved results carry over.")
                    .font(.system(size: 12.5))
                    .foregroundStyle(NoriColors.ink2)

                if isBudgetCapped {
                    HStack(spacing: 10) {
                        Text("New limit")
                            .font(.system(size: 12.5, weight: .medium))
                            .foregroundStyle(NoriColors.ink2)
                        Spacer()
                        HStack(spacing: 6) {
                            ForEach([2.0, 5.0, 10.0], id: \.self) { preset in
                                let selected = abs(continueBudgetUSD - preset) < 0.01
                                Button {
                                    continueBudgetUSD = preset
                                } label: {
                                    Text(String(format: "$%.0f", preset))
                                        .font(.system(size: 12, weight: .semibold, design: .monospaced))
                                        .foregroundStyle(selected ? NoriColors.chipSelectedFg : NoriColors.ink)
                                        .padding(.horizontal, 10)
                                        .frame(height: 26)
                                        .background(
                                            selected ? NoriColors.chipSelectedBg : NoriColors.surface,
                                            in: Capsule()
                                        )
                                        .overlay(
                                            Capsule()
                                                .strokeBorder(
                                                    selected ? NoriColors.chipSelectedBorder : NoriColors.hairline,
                                                    lineWidth: 1
                                                )
                                        )
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                    .padding(.top, 2)
                }

                Button {
                    continueCurrentTask()
                } label: {
                    Text(isContinuing
                         ? "Starting…"
                         : String(format: "Continue task ($%.2f budget)", continueBudgetUSD))
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                .disabled(isContinuing)
                .padding(.top, 2)
            }
        }
    }

    private func operationRow(op: OperationProjection, showDivider: Bool) -> some View {
        var detailParts: [String] = []
        if let account = op.accountSummary?.trimmingCharacters(in: .whitespacesAndNewlines), !account.isEmpty {
            detailParts.append(account)
        }
        if let authority = op.displayAuthorityLabel {
            detailParts.append("because \(authority)")
        }
        if let createdAt = op.createdAt?.trimmingCharacters(in: .whitespacesAndNewlines), !createdAt.isEmpty {
            detailParts.append(NoriDateFormatting.shortDateTime(createdAt))
        }

        return NoriRow(
            title: op.displaySummary,
            detail: detailParts.isEmpty ? nil : detailParts.joined(separator: " · "),
            systemImage: op.isWebSearch ? "magnifyingglass" : nil,
            iconTone: .neutral,
            showChevron: false,
            showDivider: showDivider,
            wrapTitle: true
        ) {
            if let badge = op.statusBadge {
                NoriPill(badge.label, tone: noriTone(for: badge.tone))
            }
        }
    }

    private func webSearchGroupRow(
        groupID: String,
        title: String,
        operations: [OperationProjection],
        badge: OperationBadge?,
        showDivider: Bool
    ) -> some View {
        let isExpanded = expandedSearchGroupIDs.contains(groupID)
        let latestTimestamp = operations.compactMap { $0.createdAt?.trimmingCharacters(in: .whitespacesAndNewlines) }
            .first(where: { !$0.isEmpty })

        return VStack(spacing: 0) {
            Button {
                withAnimation(.easeInOut(duration: 0.2)) {
                    if isExpanded {
                        expandedSearchGroupIDs.remove(groupID)
                    } else {
                        expandedSearchGroupIDs.insert(groupID)
                    }
                }
            } label: {
                HStack(spacing: 11) {
                    Image(systemName: "magnifyingglass")
                        .font(.system(size: 15, weight: .medium))
                        .foregroundStyle(NoriColors.ink2)
                        .frame(width: 36, height: 36)
                        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                    VStack(alignment: .leading, spacing: 2) {
                        Text(title)
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(NoriColors.ink)
                            .multilineTextAlignment(.leading)

                        if let latestTimestamp {
                            Text(NoriDateFormatting.shortDateTime(latestTimestamp))
                                .font(.system(size: 12.5))
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

                    HStack(spacing: 6) {
                        if let badge {
                            NoriPill(badge.label, tone: noriTone(for: badge.tone))
                        }
                        Image(systemName: "chevron.right")
                            .font(.system(size: 11.5, weight: .semibold))
                            .foregroundStyle(NoriColors.ink3)
                            .rotationEffect(.degrees(isExpanded ? 90 : 0))
                    }
                }
                .padding(.horizontal, 13)
                .padding(.vertical, 12)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityHint(isExpanded ? "Collapses web search queries" : "Expands web search queries")

            if isExpanded {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(operations) { op in
                        HStack(alignment: .top, spacing: 8) {
                            Circle()
                                .fill(NoriColors.hairlineStrong)
                                .frame(width: 5, height: 5)
                                .padding(.top, 6)

                            VStack(alignment: .leading, spacing: 2) {
                                Text(op.displaySummary)
                                    .font(.system(size: 13))
                                    .foregroundStyle(NoriColors.ink)
                                    .fixedSize(horizontal: false, vertical: true)
                                if let createdAt = op.createdAt, !createdAt.isEmpty {
                                    Text(NoriDateFormatting.shortTime(createdAt))
                                        .font(.system(size: 11.5))
                                        .foregroundStyle(NoriColors.ink3)
                                }
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)

                            if let opBadge = op.statusBadge {
                                NoriPill(opBadge.label, tone: noriTone(for: opBadge.tone))
                            }
                        }
                    }
                }
                .padding(.leading, 60)
                .padding(.trailing, 13)
                .padding(.bottom, 12)
                .transition(.opacity.combined(with: .move(edge: .top)))
            }

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .padding(.leading, 60)
            }
        }
    }

    private func noriTone(for tone: OperationBadgeTone) -> NoriTone {
        switch tone {
        case .neutral: return .neutral
        case .ok: return .ok
        case .warning: return .warning
        case .danger: return .danger
        }
    }

    // MARK: - Actions

    private func loadDetail() async {
        isLoadingDetail = true
        _ = await store.loadWorkDetail(taskID: taskID)
        isLoadingDetail = false
    }

    private func sendSteer() {
        let instruction = steerDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !instruction.isEmpty else { return }
        isSteering = true
        Task {
            await store.steerTask(taskID: taskID, instruction: instruction)
            steerDraft = ""
            isSteering = false
            triggerSuccessHaptic()
        }
    }

    private func stopCurrentTask() {
        isStopping = true
        Task {
            await store.stopTask(taskID: taskID)
            isStopping = false
        }
    }

    private func continueCurrentTask() {
        isContinuing = true
        Task {
            await store.continueTask(taskID: taskID, budgetUSD: continueBudgetUSD)
            isContinuing = false
            triggerSuccessHaptic()
        }
    }

    private func triggerSuccessHaptic() {
        #if os(iOS)
        let generator = UINotificationFeedbackGenerator()
        generator.notificationOccurred(.success)
        #endif
    }
}

// MARK: - Vertical Hairline Activity Timeline

private struct ActivityTimelineView: View {
    let events: [RunEvent]

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(events.enumerated()), id: \.element.id) { idx, event in
                let isLast = idx == events.count - 1
                HStack(alignment: .top, spacing: 12) {
                    VStack(spacing: 0) {
                        Circle()
                            .fill(NoriColors.accentSignal)
                            .frame(width: 9, height: 9)
                            .overlay(
                                Circle()
                                    .strokeBorder(NoriColors.canvas, lineWidth: 2)
                            )
                            .padding(.top, 4)

                        if !isLast {
                            Rectangle()
                                .fill(NoriColors.hairlineStrong)
                                .frame(width: 1.5)
                                .frame(maxHeight: .infinity)
                        }
                    }
                    .frame(width: 12)

                    VStack(alignment: .leading, spacing: 2) {
                        if !event.createdAt.isEmpty {
                            Text(NoriDateFormatting.shortDateTime(event.createdAt))
                                .font(.system(size: 11, weight: .medium, design: .monospaced))
                                .foregroundStyle(NoriColors.ink3)
                        }
                        Text(event.text)
                            .font(.system(size: 13.5))
                            .foregroundStyle(NoriColors.ink)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.bottom, isLast ? 0 : 12)
                }
            }
        }
        .padding(.leading, 4)
    }
}
