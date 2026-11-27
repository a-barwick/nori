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
    private let onOpenModels: () -> Void
    private let onStartInChat: ((String) -> Void)?

    @State private var steerDraft: String = ""
    @State private var isSteering: Bool = false
    @State private var isStopping: Bool = false
    @State private var isContinuing: Bool = false
    @State private var continueBudgetUSD: Double = 2.00
    @State private var didInitializeBudget: Bool = false
    @State private var isLoadingDetail: Bool = false
    @State private var expandedSearchGroupIDs: Set<String> = []
    @State private var copiedResult: Bool = false
    @State private var copiedTaskID: Bool = false

    public init(
        store: NoriStore,
        taskID: String,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in },
        onOpenModels: @escaping () -> Void = {},
        onStartInChat: ((String) -> Void)? = nil
    ) {
        self.store = store
        self.taskID = taskID
        self.onOpenDocument = onOpenDocument
        self.onOpenModels = onOpenModels
        self.onStartInChat = onStartInChat
    }

    private var detail: WorkDetail? {
        store.workDetails[taskID]
    }

    private var item: WorkItem? {
        detail?.item ?? store.workItems.first(where: { $0.taskID == taskID })
    }

    private var continuedByItems: [WorkItem] {
        store.workItems.filter { $0.origin.continuesTaskID == taskID }
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

    private var extraMilestoneEvents: [RunEvent] {
        let ops = detail?.operations ?? []
        var covered = Set<String>()
        for op in ops {
            covered.insert(op.name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased())
            covered.insert(op.displaySummary.trimmingCharacters(in: .whitespacesAndNewlines).lowercased())
            if let summary = op.actionSummary?.trimmingCharacters(in: .whitespacesAndNewlines), !summary.isEmpty {
                covered.insert(summary.lowercased())
            }
        }
        return (detail?.events ?? []).filter { event in
            let normalized = event.text.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
            return !normalized.isEmpty && normalized != "capability_load" && !covered.contains(normalized)
        }
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

    private func detailScrollView(item: WorkItem) -> some View {
        let isRunning = item.status == .running
        let actionablePending = pendingInteractions.filter { $0.kind != .question }
        let isWaitingInChat = isRunning
            && !item.cancelRequested
            && (item.bucket == .needsYou || pendingInteractions.contains(where: { $0.kind == .question }))
            && actionablePending.isEmpty
        let progressLine = noriProgressLine(for: item)
        let needsModelKey = item.error?.code == "model_key_required"
            || item.error?.code == "missing_api_key"
            || item.error?.message.contains("More → Models") == true

        return ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                Text(item.title)
                    .font(.system(size: 19, weight: .bold))
                    .foregroundStyle(NoriColors.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.bottom, progressLine == nil ? 12 : 8)

                if let progressLine {
                    NoriLine(
                        progressLine,
                        state: isRunning ? .thinking : .idle
                    )
                    .padding(.bottom, 14)
                }

                if needsModelKey {
                    Button("Open Models", action: onOpenModels)
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        .padding(.bottom, 14)
                }

                if isRunning {
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

                    if isWaitingInChat {
                        Button {
                            openInChat(draft: "")
                        } label: {
                            Label("Reply in Chat", systemImage: "bubble.left")
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                        .padding(.bottom, 12)
                    }

                    if item.operations.uncertain > 0 {
                        uncertainNotice
                            .padding(.bottom, 14)
                    }

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

                    stepsSection
                    resultSection(item: item)
                    savedArtifactsSection
                } else {
                    if shouldShowContinueCard(for: item) {
                        continueCard(item: item)
                            .padding(.bottom, 6)
                    }

                    if item.operations.uncertain > 0 {
                        uncertainNotice
                            .padding(.top, shouldShowContinueCard(for: item) ? 8 : 0)
                            .padding(.bottom, 6)
                    }

                    resultSection(item: item)
                    finishedActionsRow(item: item)
                    savedArtifactsSection
                    stepsSection
                }

                detailsSection(item: item)
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
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

    private func shouldShowContinueCard(for item: WorkItem) -> Bool {
        (item.status == .failed || item.isBudgetExhausted) && continuedByItems.isEmpty
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
                MarkdownBlocksView(markdown: resultText)
                    .textSelection(.enabled)
            }
        }
    }

    private func finishedActionsRow(item: WorkItem) -> some View {
        HStack(spacing: 8) {
            if let resultText = detail?.result, !resultText.isEmpty {
                Button {
                    copyString(resultText, toast: "Copied result")
                    copiedResult = true
                    Task {
                        try? await Task.sleep(for: .seconds(1.8))
                        copiedResult = false
                    }
                } label: {
                    Label(copiedResult ? "Copied" : "Copy", systemImage: copiedResult ? "checkmark" : "doc.on.doc")
                }
                .buttonStyle(NoriButtonStyle(.neutral, compact: true))
            }

            Button {
                openInChat(draft: "Follow up on \"\(item.title)\": ")
            } label: {
                Label("Follow up in Chat", systemImage: "bubble.left")
            }
            .buttonStyle(NoriButtonStyle(.tonal, compact: true))

            Spacer(minLength: 0)
        }
        .padding(.top, 12)
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
                            detail: doc.previewText,
                            systemImage: "doc.text",
                            iconTone: .accent,
                            showChevron: true,
                            showDivider: idx < savedItems.count - 1
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    @ViewBuilder
    private var stepsSection: some View {
        let entries = groupedActions
        let milestones = extraMilestoneEvents
        if !entries.isEmpty || !milestones.isEmpty {
            NoriSectionHeader("Steps")
            VStack(alignment: .leading, spacing: 10) {
                if !entries.isEmpty {
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

                if !milestones.isEmpty {
                    NoriRows {
                        ForEach(Array(milestones.enumerated()), id: \.element.id) { idx, event in
                            NoriRow(
                                title: event.text,
                                detail: event.createdAt.isEmpty ? nil : NoriDateFormatting.shortDateTime(event.createdAt),
                                showChevron: false,
                                showDivider: idx < milestones.count - 1,
                                wrapTitle: true
                            )
                        }
                    }
                }
            }
        }
    }

    private func noriProgressLine(for item: WorkItem) -> String? {
        if item.status == .running && item.cancelRequested {
            return nil
        }
        if item.status == .failed, let err = item.error?.message, !err.isEmpty {
            return err
        }
        if item.status == .running, pendingInteractions.isEmpty,
           let progress = item.progress?.trimmingCharacters(in: .whitespacesAndNewlines), !progress.isEmpty {
            return progress
        }
        if item.status == .completed && item.operations.uncertain > 0 {
            return "Finished, but one action isn’t confirmed yet."
        }
        return nil
    }

    private func detailsSection(item: WorkItem) -> some View {
        let spent = item.cost.amountUSD ?? 0
        let costLabel: String = {
            if item.cost.status == .unknown && item.cost.amountUSD == nil {
                return "Unknown"
            }
            if item.cost.status == .notApplicable {
                return "None"
            }
            if let budgetUSD = item.cost.budgetUSD {
                return String(format: "$%.2f / $%.2f", spent, budgetUSD)
            }
            return String(format: "$%.2f", spent)
        }()

        return VStack(alignment: .leading, spacing: 0) {
            NoriSectionHeader("Details")
                .padding(.top, 8)

            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 16, verticalSpacing: 8) {
                detailRow(label: "Origin", value: item.origin.displayLabel)
                detailRow(label: "Started", value: NoriDateFormatting.shortDateTime(item.startedAt))
                if let finishedAt = item.finishedAt, !finishedAt.isEmpty {
                    detailRow(label: "Finished", value: NoriDateFormatting.shortDateTime(finishedAt))
                }
                GridRow {
                    Text("Cost")
                        .font(.system(size: 13))
                        .foregroundStyle(NoriColors.ink3)
                    Text(costLabel)
                        .font(.system(size: 13, weight: .medium, design: .monospaced))
                        .foregroundStyle(NoriColors.ink)
                }
                if let continuesID = item.origin.continuesTaskID, !continuesID.isEmpty {
                    GridRow {
                        Text("Continues")
                            .font(.system(size: 13))
                            .foregroundStyle(NoriColors.ink3)
                        Button("Earlier task") {
                            store.pendingDeepLink = .workDetail(continuesID)
                        }
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .buttonStyle(.plain)
                    }
                }
                if !continuedByItems.isEmpty {
                    GridRow {
                        Text("Continued by")
                            .font(.system(size: 13))
                            .foregroundStyle(NoriColors.ink3)
                        VStack(alignment: .leading, spacing: 4) {
                            ForEach(continuedByItems) { other in
                                Button(other.title.isEmpty ? "Later task" : other.title) {
                                    store.pendingDeepLink = .workDetail(other.taskID)
                                }
                                .font(.system(size: 13, weight: .medium))
                                .foregroundStyle(NoriColors.accent)
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }
                GridRow {
                    Text("Task ID")
                        .font(.system(size: 13))
                        .foregroundStyle(NoriColors.ink3)
                    Button {
                        copyString(item.taskID, toast: "Copied Task ID")
                        copiedTaskID = true
                        Task {
                            try? await Task.sleep(for: .seconds(1.8))
                            copiedTaskID = false
                        }
                    } label: {
                        HStack(spacing: 6) {
                            Text(item.taskID)
                                .font(.system(size: 12, design: .monospaced))
                                .foregroundStyle(NoriColors.ink2)
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Image(systemName: copiedTaskID ? "checkmark" : "doc.on.doc")
                                .font(.system(size: 11, weight: .medium))
                                .foregroundStyle(NoriColors.accent)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Copy Task ID \(item.taskID)")
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
                                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
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
            borderColor: NoriColors.cardBorder
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
                                        .frame(height: 28)
                                        .background(
                                            selected ? NoriColors.chipSelectedBg : NoriColors.surface,
                                            in: Capsule()
                                        )
                                        .overlay(
                                            Capsule()
                                                .strokeBorder(
                                                    selected ? NoriColors.chipSelectedBorder : NoriColors.cardBorder,
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
                    continueCurrentTask(isBudgetCapped: isBudgetCapped, existingBudget: item.cost.budgetUSD)
                } label: {
                    Text(
                        isContinuing
                            ? "Starting…"
                            : (isBudgetCapped
                                ? String(format: "Continue task ($%.2f budget)", continueBudgetUSD)
                                : "Continue this task")
                    )
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                .disabled(isContinuing)
                .padding(.top, 2)
            }
        }
    }

    private func operationRow(op: OperationProjection, showDivider: Bool) -> some View {
        let isException = op.outcome != .confirmed || op.recovery != nil || op.cleared != nil
        var detailParts: [String] = []
        if isException, let account = op.accountSummary?.trimmingCharacters(in: .whitespacesAndNewlines), !account.isEmpty {
            detailParts.append(account)
        }
        if let recovery = op.recovery?.trimmingCharacters(in: .whitespacesAndNewlines), !recovery.isEmpty {
            let recoveryText: String = {
                switch recovery {
                case "reconciling": return "checking with the provider"
                case "needs_review": return "pending review"
                case "resolved": return "resolved"
                default: return recovery
                }
            }()
            detailParts.append(recoveryText)
        }
        if let createdAt = op.createdAt?.trimmingCharacters(in: .whitespacesAndNewlines), !createdAt.isEmpty {
            detailParts.append(NoriDateFormatting.shortDateTime(createdAt))
        }

        let safeReceiptURL: URL? = {
            guard let raw = op.receiptURL?.trimmingCharacters(in: .whitespacesAndNewlines),
                  let url = URL(string: raw),
                  url.scheme?.lowercased() == "https" else {
                return nil
            }
            return url
        }()

        return NoriRow(
            title: op.displaySummary,
            detail: detailParts.isEmpty ? nil : detailParts.joined(separator: " · "),
            systemImage: op.isWebSearch ? "magnifyingglass" : nil,
            iconTone: .neutral,
            showChevron: false,
            showDivider: showDivider,
            wrapTitle: true
        ) {
            if let safeReceiptURL {
                Link("Open", destination: safeReceiptURL)
                    .font(.system(size: 12.5, weight: .semibold))
                    .foregroundStyle(NoriColors.accent)
            }
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

    private func openInChat(draft: String) {
        if let onStartInChat {
            onStartInChat(draft)
        } else {
            if !draft.isEmpty {
                store.pendingComposerDraft = draft
            }
            store.pendingDeepLink = .tab(.chat)
        }
    }

    private func copyString(_ value: String, toast: String) {
        #if canImport(UIKit)
        UIPasteboard.general.string = value
        #endif
        store.showToast(toast)
    }

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

    private func continueCurrentTask(isBudgetCapped: Bool, existingBudget: Double?) {
        isContinuing = true
        let budget = isBudgetCapped ? continueBudgetUSD : (existingBudget ?? 2.00)
        Task {
            await store.continueTask(taskID: taskID, budgetUSD: budget)
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
