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
    private let onStartInChat: (String) -> Void
    private let onOpenModels: () -> Void

    @State private var steerDraft: String = ""
    @State private var isSteering: Bool = false
    @State private var isStopping: Bool = false
    @State private var isContinuing: Bool = false
    @State private var continueBudgetUSD: Double = 2.00
    @State private var didInitializeBudget: Bool = false
    @State private var isLoadingDetail: Bool = false
    @State private var copiedResult: Bool = false
    @State private var isResultReaderPresented: Bool = false
    @State private var userStepsExpanded: Bool? = nil
    @State private var expandedSearchGroupIDs: Set<String> = []

    public init(
        store: NoriStore,
        taskID: String,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in },
        onStartInChat: @escaping (String) -> Void = { _ in },
        onOpenModels: @escaping () -> Void = {}
    ) {
        self.store = store
        self.taskID = taskID
        self.onOpenDocument = onOpenDocument
        self.onStartInChat = onStartInChat
        self.onOpenModels = onOpenModels
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

    private var actionableInteractions: [Interaction] {
        pendingInteractions.filter { $0.kind != .question }
    }

    private var pendingQuestions: [Interaction] {
        pendingInteractions.filter { $0.kind == .question }
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

    private var primaryArtifact: NoriCore.LibraryItem? {
        guard let result = detail?.result?.trimmingCharacters(in: .whitespacesAndNewlines),
              !result.isEmpty,
              savedItems.count == 1,
              let candidate = savedItems.first else {
            return nil
        }
        let isTextDocKind = candidate.kind == .document || candidate.kind == .report || candidate.kind == .answer
        guard isTextDocKind else { return nil }
        let media = (candidate.mediaType ?? "").lowercased()
        if !media.isEmpty && !media.hasPrefix("text/") { return nil }
        if media.contains("csv") { return nil }
        return candidate
    }

    private var extraArtifacts: [NoriCore.LibraryItem] {
        guard let primary = primaryArtifact else { return savedItems }
        return savedItems.filter { $0.id != primary.id }
    }

    private var changesMade: [OperationProjection] {
        WorkStepEntry.externalChanges(from: detail?.operations ?? [])
    }

    private var unifiedSteps: [WorkStepEntry] {
        WorkStepEntry.build(operations: detail?.operations ?? [], events: detail?.events ?? [])
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
        .sheet(isPresented: $isResultReaderPresented) {
            if let item, let resultText = detail?.result, !resultText.isEmpty {
                let cardTitle = primaryArtifact?.title ?? (item.status == .failed ? "Completed so far" : "Summary")
                let bodyMarkdown = WorkStepEntry.stripLeadingMatchingHeading(resultText, title: cardTitle)
                TaskResultReaderSheet(
                    title: cardTitle,
                    markdown: bodyMarkdown,
                    onCopy: { copyText(resultText, toast: "Copied") }
                )
                .presentationDetents([.large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
            }
        }
    }

    private func detailScrollView(item: WorkItem) -> some View {
        let isRunning = item.status == .running && !item.cancelRequested
        let waitingInChat = isRunning && actionableInteractions.isEmpty && (!pendingQuestions.isEmpty || item.bucket == .needsYou)
        let hasUncertainChangeRow = changesMade.contains { $0.outcome == .uncertain }
        let relativeTime = NoriDateFormatting.relative(item.finishedAt ?? item.startedAt)

        return ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                HStack(alignment: .firstTextBaseline, spacing: 10) {
                    Text(item.title)
                        .font(.system(size: 20, weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    HStack(spacing: 8) {
                        Text(relativeTime)
                            .font(.system(size: 12.5))
                            .foregroundStyle(NoriColors.ink3)
                            .lineLimit(1)
                            .fixedSize()

                        if isRunning {
                            Button {
                                stopCurrentTask()
                            } label: {
                                HStack(spacing: 4) {
                                    Image(systemName: "stop.fill")
                                        .font(.system(size: 9.5))
                                    Text(isStopping ? "Stopping…" : "Stop")
                                }
                            }
                            .buttonStyle(NoriButtonStyle(.danger, compact: true))
                            .disabled(isStopping)
                        }
                    }
                }
                .padding(.bottom, 16)

                if item.error?.code == "model_key_required" || item.error?.message.contains("More → Models") == true {
                    Button("Open Models", action: onOpenModels)
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        .padding(.bottom, 12)
                }

                if !actionableInteractions.isEmpty {
                    NoriSectionHeader("Action needed")
                    VStack(spacing: 10) {
                        ForEach(actionableInteractions) { interaction in
                            InteractionCardView(
                                store: store,
                                interaction: interaction,
                                compact: true
                            )
                        }
                    }
                    .padding(.bottom, 14)
                }

                if waitingInChat {
                    waitingInChatCard
                        .padding(.bottom, 14)
                } else if isRunning && actionableInteractions.isEmpty {
                    liveWorkingCard(item: item)
                        .padding(.bottom, 14)
                }

                if item.status == .running && item.cancelRequested {
                    NoriNotice(
                        title: "Stopping task",
                        detail: "Work already done stays done; anything unconfirmed stays marked.",
                        tone: .neutral,
                        systemImage: "stop.circle"
                    )
                    .padding(.bottom, 14)
                }

                if shouldShowContinueCard(for: item) {
                    continueCard(item: item)
                        .padding(.bottom, 14)
                }

                if item.operations.uncertain > 0 && !hasUncertainChangeRow {
                    uncertainNotice
                        .padding(.bottom, 14)
                }

                if let resultText = detail?.result, !resultText.isEmpty {
                    outcomeCard(item: item, resultText: resultText)
                        .padding(.bottom, 14)
                }

                extraArtifactsSection

                changesMadeSection

                if item.status != .running {
                    followUpRow(item: item)
                        .padding(.top, 14)
                }

                stepsDisclosureSection(isRunning: isRunning)

                plainDetailsSection(item: item)
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
                let presets = WorkStepEntry.budgetPresets(currentBudgetUSD: item.cost.budgetUSD)
                continueBudgetUSD = presets.first ?? 2.00
            }
        }
    }

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

    private var waitingInChatCard: some View {
        NoriCard(
            padding: 14,
            backgroundColor: NoriColors.accentSubtle,
            borderColor: NoriColors.cardBorder
        ) {
            HStack(alignment: .top, spacing: 11) {
                NoriMark(size: 20, state: .idle)
                    .frame(width: 34, height: 34)
                    .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                VStack(alignment: .leading, spacing: 6) {
                    Text("Nori asked a question in Chat")
                        .font(.system(size: 14.5, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)

                    Text(pendingQuestions.first?.prompt ?? "Open Chat to reply so Nori can continue.")
                        .font(.system(size: 13.5))
                        .foregroundStyle(NoriColors.ink2)
                        .fixedSize(horizontal: false, vertical: true)

                    HStack(spacing: 8) {
                        Button {
                            onStartInChat("")
                        } label: {
                            HStack(spacing: 5) {
                                Image(systemName: "bubble.left")
                                    .font(.system(size: 12, weight: .semibold))
                                Text("Reply in Chat")
                            }
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))

                        Button(isStopping ? "Stopping…" : "Stop task") {
                            stopCurrentTask()
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        .disabled(isStopping)
                    }
                    .padding(.top, 4)
                }
            }
        }
    }

    private func liveWorkingCard(item: WorkItem) -> some View {
        let progressText = item.progress?.trimmingCharacters(in: .whitespacesAndNewlines)
        let label = (progressText?.isEmpty == false) ? progressText! : "Working on it."
        let canSend = !isSteering && !steerDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty

        return NoriCard(
            padding: 14,
            backgroundColor: NoriColors.accentSubtle,
            borderColor: NoriColors.cardBorder
        ) {
            VStack(alignment: .leading, spacing: 11) {
                HStack(spacing: 8) {
                    PulsingStatusDot(color: NoriColors.accent, size: 8, animated: true)
                    Text(label)
                        .font(.system(size: 14.5, weight: .medium))
                        .foregroundStyle(NoriColors.ink)
                        .fixedSize(horizontal: false, vertical: true)
                }

                HStack(spacing: 8) {
                    TextField("Add a note or adjust course…", text: $steerDraft)
                        .font(.system(size: 14))
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        .submitLabel(.send)
                        .onSubmit {
                            if canSend { sendSteer() }
                        }

                    Button {
                        sendSteer()
                    } label: {
                        Image(systemName: "arrow.up")
                            .font(.system(size: 13, weight: .bold))
                            .foregroundStyle(canSend ? NoriColors.onAccent : NoriColors.disabled)
                            .frame(width: 30, height: 30)
                            .background(canSend ? NoriColors.brandPrimary : NoriColors.disabledBg, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .disabled(!canSend)
                    .accessibilityLabel("Send update")
                }
                .padding(.leading, 14)
                .padding(.trailing, 5)
                .padding(.vertical, 5)
                .background(NoriColors.surface, in: Capsule())
                .overlay(
                    Capsule()
                        .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                )
            }
        }
    }

    private func continueCard(item: WorkItem) -> some View {
        let isBudgetCapped = item.isBudgetExhausted
        let presets = WorkStepEntry.budgetPresets(currentBudgetUSD: item.cost.budgetUSD)
        let heading: String = {
            if isBudgetCapped {
                if let budget = item.cost.budgetUSD {
                    return String(format: "Paused at $%.2f spending limit", budget)
                }
                return "Paused at spending limit"
            }
            return item.error?.message ?? "Task stopped before finishing"
        }()

        return NoriCard(
            padding: 14,
            backgroundColor: NoriColors.accentSubtle,
            borderColor: NoriColors.cardBorder
        ) {
            VStack(alignment: .leading, spacing: 8) {
                Text(heading)
                    .font(.system(size: 14.5, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)

                Text("Completed actions won’t be repeated. Saved work carries over.")
                    .font(.system(size: 13))
                    .foregroundStyle(NoriColors.ink2)

                if isBudgetCapped {
                    HStack(spacing: 8) {
                        Text("Limit")
                            .font(.system(size: 12.5, weight: .medium))
                            .foregroundStyle(NoriColors.ink3)
                        Spacer()
                        HStack(spacing: 6) {
                            ForEach(presets, id: \.self) { preset in
                                let selected = abs(continueBudgetUSD - preset) < 0.01
                                Button {
                                    continueBudgetUSD = preset
                                } label: {
                                    Text(String(format: "$%.0f", preset))
                                        .font(.system(size: 12, weight: .semibold, design: .monospaced))
                                        .foregroundStyle(selected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                                        .padding(.horizontal, 10)
                                        .frame(height: 27)
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
                    continueCurrentTask()
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: "play.fill")
                            .font(.system(size: 10.5))
                        Text(
                            isContinuing
                                ? "Starting…"
                                : isBudgetCapped
                                    ? String(format: "Continue ($%.2f)", continueBudgetUSD)
                                    : "Continue"
                        )
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, compact: true))
                .disabled(isContinuing)
                .padding(.top, 4)
            }
        }
    }

    private func outcomeCard(item: WorkItem, resultText: String) -> some View {
        let cardTitle = primaryArtifact?.title ?? (item.status == .failed ? "Completed so far" : "Summary")
        let displayMarkdown = WorkStepEntry.stripLeadingMatchingHeading(resultText, title: cardTitle)
        let isLong = WorkStepEntry.isLongMarkdown(displayMarkdown)

        return NoriCard(padding: 14) {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    if primaryArtifact != nil {
                        Image(systemName: "doc.text")
                            .font(.system(size: 13.5, weight: .medium))
                            .foregroundStyle(NoriColors.accent)
                            .frame(width: 28, height: 28)
                            .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                    }

                    Text(cardTitle)
                        .font(.system(size: 14, weight: .semibold))
                        .foregroundStyle(primaryArtifact != nil ? NoriColors.ink : NoriColors.ink2)
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    Button {
                        copyText(resultText, toast: "Copied")
                        copiedResult = true
                        Task {
                            try? await Task.sleep(for: .seconds(1.6))
                            copiedResult = false
                        }
                    } label: {
                        HStack(spacing: 4) {
                            Image(systemName: copiedResult ? "checkmark" : "doc.on.doc")
                                .font(.system(size: 11.5, weight: .medium))
                            Text(copiedResult ? "Copied" : "Copy")
                                .font(.system(size: 12, weight: .medium))
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                }

                if isLong {
                    ZStack(alignment: .bottom) {
                        MarkdownBlocksView(markdown: displayMarkdown)
                            .frame(maxWidth: .infinity, maxHeight: 200, alignment: .topLeading)
                            .clipped()

                        LinearGradient(
                            colors: [
                                NoriColors.surface.opacity(0),
                                NoriColors.surface.opacity(0.92),
                                NoriColors.surface
                            ],
                            startPoint: .top,
                            endPoint: .bottom
                        )
                        .frame(height: 86)
                        .overlay(alignment: .bottom) {
                            Button {
                                if let doc = primaryArtifact {
                                    onOpenDocument(doc)
                                } else {
                                    isResultReaderPresented = true
                                }
                            } label: {
                                HStack(spacing: 5) {
                                    Image(systemName: "arrow.up.left.and.arrow.down.right")
                                        .font(.system(size: 11.5, weight: .semibold))
                                    Text("Expand")
                                }
                            }
                            .buttonStyle(NoriButtonStyle(.tonal, compact: true))
                            .padding(.bottom, 2)
                        }
                    }
                } else {
                    MarkdownBlocksView(markdown: displayMarkdown)
                }
            }
        }
    }

    @ViewBuilder
    private var extraArtifactsSection: some View {
        if !extraArtifacts.isEmpty {
            NoriSectionHeader("Artifacts")
            NoriRows {
                ForEach(Array(extraArtifacts.enumerated()), id: \.element.id) { idx, doc in
                    Button {
                        onOpenDocument(doc)
                    } label: {
                        NoriRow(
                            title: doc.title,
                            detail: nil,
                            systemImage: "doc.text",
                            iconTone: .accent,
                            showChevron: true,
                            showDivider: idx < extraArtifacts.count - 1
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    @ViewBuilder
    private var changesMadeSection: some View {
        if !changesMade.isEmpty {
            NoriSectionHeader("Changes made")
            NoriRows {
                ForEach(Array(changesMade.enumerated()), id: \.element.id) { idx, op in
                    let isWarning = op.outcome == .uncertain || op.outcome == .failed
                    let exceptionDetail: String? = {
                        if op.outcome == .uncertain {
                            return "Checking with provider — won’t be repeated"
                        }
                        if op.outcome == .failed {
                            return op.error?.message ?? "Action failed"
                        }
                        return nil
                    }()
                    let exceptionBadge: OperationBadge? = (op.outcome == .confirmed && op.cleared == nil) ? nil : op.statusBadge
                    let receiptURL: URL? = {
                        guard let raw = op.receiptURL, raw.hasPrefix("https://") else { return nil }
                        return URL(string: raw)
                    }()

                    NoriRow(
                        title: op.displaySummary,
                        detail: exceptionDetail,
                        systemImage: isWarning ? "exclamationmark.triangle" : "checkmark",
                        iconTone: isWarning ? .warning : .ok,
                        showChevron: false,
                        showDivider: idx < changesMade.count - 1,
                        wrapTitle: true
                    ) {
                        HStack(spacing: 8) {
                            if let exceptionBadge {
                                NoriPill(exceptionBadge.label, tone: noriTone(for: exceptionBadge.tone))
                            }
                            if let receiptURL {
                                Link(destination: receiptURL) {
                                    HStack(spacing: 2) {
                                        Text("Open")
                                        Image(systemName: "arrow.up.right")
                                            .font(.system(size: 11, weight: .semibold))
                                    }
                                    .font(.system(size: 13, weight: .semibold))
                                    .foregroundStyle(NoriColors.accent)
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    private func followUpRow(item: WorkItem) -> some View {
        NoriRows {
            Button {
                onStartInChat("Following up on \"\(item.title)\": ")
            } label: {
                NoriRow(
                    title: "Follow up in Chat",
                    detail: nil,
                    systemImage: "bubble.left",
                    iconTone: .accent,
                    showChevron: true,
                    showDivider: false
                )
            }
            .buttonStyle(.plain)
        }
    }

    @ViewBuilder
    private func stepsDisclosureSection(isRunning: Bool) -> some View {
        let steps = unifiedSteps
        if !steps.isEmpty {
            let expanded = userStepsExpanded ?? isRunning
            NoriCard(padding: 0) {
                VStack(alignment: .leading, spacing: 0) {
                    Button {
                        withAnimation(.easeInOut(duration: 0.2)) {
                            userStepsExpanded = !expanded
                        }
                    } label: {
                        HStack(spacing: 8) {
                            if isRunning {
                                PulsingStatusDot(color: NoriColors.accent, size: 7.5, animated: true)
                            }
                            Text("Steps")
                                .font(.system(size: 14.5, weight: .semibold))
                                .foregroundStyle(NoriColors.ink)
                            Text("\(steps.count)")
                                .font(.system(size: 12.5))
                                .foregroundStyle(NoriColors.ink3)
                                .monospacedDigit()
                            Spacer(minLength: 8)
                            Image(systemName: "chevron.right")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.ink3)
                                .rotationEffect(.degrees(expanded ? 90 : 0))
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 13)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)

                    if expanded {
                        Rectangle()
                            .fill(NoriColors.hairline)
                            .frame(height: 1)

                        VStack(alignment: .leading, spacing: 0) {
                            ForEach(Array(steps.enumerated()), id: \.element.id) { idx, step in
                                stepTimelineRow(step: step, isLast: idx == steps.count - 1)
                            }
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 14)
                    }
                }
            }
            .padding(.top, 14)
        }
    }

    private func stepTimelineRow(step: WorkStepEntry, isLast: Bool) -> some View {
        let dotColor: Color = {
            switch step.status {
            case .done: return NoriColors.ok
            case .active: return NoriColors.accent
            case .warning: return NoriColors.warning
            case .error: return NoriColors.danger
            }
        }()

        return HStack(alignment: .top, spacing: 10) {
            VStack(spacing: 0) {
                Circle()
                    .fill(dotColor)
                    .frame(width: 7.5, height: 7.5)
                    .padding(.top, 5)
                if !isLast {
                    Rectangle()
                        .fill(NoriColors.hairline)
                        .frame(width: 1.5)
                        .frame(maxHeight: .infinity)
                        .padding(.top, 4)
                }
            }
            .frame(width: 12)

            VStack(alignment: .leading, spacing: 3) {
                switch step {
                case .searchGroup(let groupID, let title, _, let createdAt, let queries):
                    let isOpen = expandedSearchGroupIDs.contains(groupID)
                    Button {
                        withAnimation(.easeInOut(duration: 0.18)) {
                            if isOpen {
                                expandedSearchGroupIDs.remove(groupID)
                            } else {
                                expandedSearchGroupIDs.insert(groupID)
                            }
                        }
                    } label: {
                        HStack(spacing: 6) {
                            Image(systemName: "magnifyingglass")
                                .font(.system(size: 11.5))
                                .foregroundStyle(NoriColors.ink3)
                            Text(title)
                                .font(.system(size: 13.5))
                                .foregroundStyle(NoriColors.ink)
                                .multilineTextAlignment(.leading)
                                .frame(maxWidth: .infinity, alignment: .leading)
                            Image(systemName: "chevron.right")
                                .font(.system(size: 10.5, weight: .semibold))
                                .foregroundStyle(NoriColors.ink3)
                                .rotationEffect(.degrees(isOpen ? 90 : 0))
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)

                    if let createdAt, !createdAt.isEmpty {
                        Text(NoriDateFormatting.shortTime(createdAt))
                            .font(.system(size: 11.5))
                            .foregroundStyle(NoriColors.ink3)
                    }

                    if isOpen {
                        VStack(alignment: .leading, spacing: 4) {
                            ForEach(queries) { query in
                                Text("· \(query.text)")
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(NoriColors.ink2)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .padding(.leading, 10)
                        .padding(.top, 4)
                        .overlay(alignment: .leading) {
                            Rectangle()
                                .fill(NoriColors.hairline)
                                .frame(width: 2)
                                .padding(.top, 4)
                        }
                    }

                case .step(_, let title, _, let createdAt):
                    Text(title)
                        .font(.system(size: 13.5))
                        .foregroundStyle(NoriColors.ink)
                        .fixedSize(horizontal: false, vertical: true)

                    if let createdAt, !createdAt.isEmpty {
                        Text(NoriDateFormatting.shortTime(createdAt))
                            .font(.system(size: 11.5))
                            .foregroundStyle(NoriColors.ink3)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.bottom, isLast ? 0 : 14)
        }
    }

    private func plainDetailsSection(item: WorkItem) -> some View {
        let spent = item.cost.amountUSD ?? 0
        let budget = item.cost.budgetUSD
        let isNearCap = budget != nil && budget! > 0 && spent >= budget! * 0.95
        let costLabel: String = {
            if item.cost.status == .unknown && item.cost.amountUSD == nil {
                return "Unknown"
            }
            if let budget {
                return String(format: "$%.2f / $%.2f", spent, budget)
            }
            return String(format: "$%.2f", spent)
        }()
        let duration = NoriDateFormatting.duration(from: item.startedAt, to: item.finishedAt)
        let timeLabel: String = {
            let startStr = NoriDateFormatting.shortDateTime(item.startedAt)
            let endPart = item.finishedAt.map { " – \(NoriDateFormatting.shortTime($0))" } ?? ""
            let durPart = duration.map { " (\($0))" } ?? ""
            return "\(startStr)\(endPart)\(durPart)"
        }()

        return VStack(alignment: .leading, spacing: 0) {
            NoriSectionHeader("Details")

            VStack(alignment: .leading, spacing: 9) {
                detailRow(label: "Origin") {
                    if item.origin.kind == .conversation {
                        Button(item.origin.displayLabel) {
                            onStartInChat("")
                        }
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .buttonStyle(.plain)
                    } else {
                        Text(item.origin.displayLabel)
                            .font(.system(size: 13))
                            .foregroundStyle(NoriColors.ink2)
                    }
                }

                detailRow(label: "Time") {
                    Text(timeLabel)
                        .font(.system(size: 13))
                        .foregroundStyle(NoriColors.ink2)
                }

                detailRow(label: "Cost") {
                    Text(costLabel)
                        .font(.system(size: 12.5, design: .monospaced))
                        .foregroundStyle(isNearCap ? NoriColors.danger : NoriColors.ink2)
                }

                detailRow(label: "Task ID") {
                    Button {
                        copyText(item.taskID, toast: "Copied Task ID")
                    } label: {
                        HStack(spacing: 5) {
                            Text(item.taskID)
                                .font(.system(size: 12.5, design: .monospaced))
                                .foregroundStyle(NoriColors.ink2)
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Image(systemName: "doc.on.doc")
                                .font(.system(size: 11))
                                .foregroundStyle(NoriColors.ink3)
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

    private func detailRow<Content: View>(label: String, @ViewBuilder value: () -> Content) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text(label)
                .font(.system(size: 13))
                .foregroundStyle(NoriColors.ink3)
                .frame(width: 72, alignment: .leading)
            value()
                .frame(maxWidth: .infinity, alignment: .leading)
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

    private func copyText(_ text: String, toast: String) {
        #if canImport(UIKit)
        UIPasteboard.general.string = text
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

private struct TaskResultReaderSheet: View {
    @Environment(\.dismiss) private var dismiss
    let title: String
    let markdown: String
    let onCopy: () -> Void

    @State private var copied: Bool = false

    var body: some View {
        NavigationStack {
            ScrollView {
                MarkdownBlocksView(markdown: markdown)
                    .padding(.horizontal, 18)
                    .padding(.vertical, 16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle(title)
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
                ToolbarItemGroup(placement: .primaryAction) {
                    Button {
                        onCopy()
                        copied = true
                        Task {
                            try? await Task.sleep(for: .seconds(1.5))
                            copied = false
                        }
                    } label: {
                        Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    }
                    .accessibilityLabel("Copy")

                    ShareLink(item: markdown) {
                        Image(systemName: "square.and.arrow.up")
                    }
                    .accessibilityLabel("Share")
                }
            }
        }
    }
}
