import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public struct ChatView: View {
    @Bindable private var store: NoriStore
    private let scrollToBottomTrigger: Int
    private let onOpenActivity: () -> Void
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void
    private let onNavigateToNeedsYou: () -> Void
    private let onOpenTask: (String) -> Void
    private let onOpenModels: () -> Void

    @State private var retryingMessageIDs: Set<String> = []
    @State private var cancellingReminderIDs: Set<String> = []
    @State private var editingReminder: ReminderEditTarget?
    @State private var visiblePendingInteractionIDs: Set<String> = []
    @State private var isNearBottom: Bool = true
    @Namespace private var noriAvatarNamespace

    public init(
        store: NoriStore,
        scrollToBottomTrigger: Int = 0,
        onOpenActivity: @escaping () -> Void = {},
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in },
        onNavigateToNeedsYou: @escaping () -> Void = {},
        onOpenTask: @escaping (String) -> Void = { _ in },
        onOpenModels: @escaping () -> Void = {}
    ) {
        self.store = store
        self.scrollToBottomTrigger = scrollToBottomTrigger
        self.onOpenActivity = onOpenActivity
        self.onOpenDocument = onOpenDocument
        self.onNavigateToNeedsYou = onNavigateToNeedsYou
        self.onOpenTask = onOpenTask
        self.onOpenModels = onOpenModels
    }

    private var effectiveMarkState: MarkState {
        store.isThinking ? .thinking : .idle
    }

    private var isReconnecting: Bool {
        store.connectionState == .reconnecting || store.connectionState == .offline
    }

    private var threadUnits: [ChatThreadUnit] {
        ChatThreadUnit.build(
            from: store.messages,
            pendingInteractions: store.pendingInteractions
        )
    }

    private var inlinePendingInteractions: [Interaction] {
        store.pendingInteractions.filter(\.isChatInlineEligible)
    }

    private var hasVisiblePendingInteraction: Bool {
        inlinePendingInteractions.contains { visiblePendingInteractionIDs.contains($0.id) }
    }

    private var shouldShowNeedsYouBanner: Bool {
        !inlinePendingInteractions.isEmpty && !hasVisiblePendingInteraction
    }

    private var activeBackgroundTasksCount: Int {
        let needsYouTaskIDs = Set(
            store.workItems
                .filter { $0.bucket == .needsYou }
                .map(\.taskID)
        )
        let runningRuns = store.runs.filter {
            $0.status == .running
                && ($0.waiting?.isEmpty ?? true)
                && !needsYouTaskIDs.contains($0.id)
        }.count
        if let working = store.assistantSummary?.working {
            return max(working, runningRuns)
        }
        if !store.workItems.isEmpty {
            let workingItems = store.workItems.filter { $0.status == .running && $0.bucket == .working }.count
            return max(workingItems, runningRuns)
        }
        return max(store.activeTasksCount, runningRuns)
    }

    private var shouldShowInlineWorkLine: Bool {
        store.isThinking || activeBackgroundTasksCount > 0
    }

    public var body: some View {
        VStack(spacing: 0) {
            if isReconnecting {
                ReconnectingBanner {
                    Task {
                        await store.resumeFromBackground()
                    }
                }
                .padding(.horizontal, 14)
                .padding(.top, 8)
                .transition(.move(edge: .top).combined(with: .opacity))
            }

            if let message = store.modelKeyError {
                NoriNotice(title: "Model key required", detail: message, tone: .warning, systemImage: "key.fill") {
                    Button("Open Models", action: onOpenModels)
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                }
                .padding(.horizontal, 14)
                .padding(.top, 8)
            }

            threadArea

            ChatComposerView(store: store)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .sheet(item: $editingReminder) { target in
            EditReminderSheet(store: store, target: target)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
    }

    private func latestNoriMessageID(in units: [ChatThreadUnit]) -> String? {
        for unit in units.reversed() {
            switch unit {
            case .group(_, let isUser, let messages) where !isUser:
                if let last = messages.last {
                    return last.id
                }
            case .reminder(let message, let parts):
                let trimmedText = message.text.trimmingCharacters(in: .whitespacesAndNewlines)
                let matchesFallback = parts.contains {
                    $0.fallback?.trimmingCharacters(in: .whitespacesAndNewlines) == trimmedText
                }
                if !trimmedText.isEmpty && !matchesFallback {
                    return message.id
                }
            default:
                continue
            }
        }
        return nil
    }

    private var threadArea: some View {
        let units = threadUnits
        let latestNoriID = latestNoriMessageID(in: units)
        let primaryPendingInThread: Interaction? = units.compactMap { unit in
            if case .interaction(let item) = unit { return item }
            return nil
        }.first

        return ScrollViewReader { proxy in
            ZStack(alignment: .top) {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        if units.isEmpty {
                            NoriEmptyState(
                                title: store.isLoading ? "Loading conversation…" : "Ask Nori anything",
                                markState: store.isLoading ? .thinking : .idle
                            )
                            .padding(.top, 48)
                        } else {
                            ForEach(units) { unit in
                                threadUnitView(unit, activeNoriMessageID: store.isThinking ? nil : latestNoriID)
                            }
                        }

                        if let streaming = store.streamingAssistantText, !streaming.isEmpty {
                            HStack(alignment: .top, spacing: 8) {
                                NoriMark(size: 28, state: .thinking)
                                    .matchedGeometryEffect(id: "active-nori-avatar", in: noriAvatarNamespace)
                                Text(streaming)
                                    .font(.body)
                                    .foregroundStyle(.primary)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .padding(.top, 4)
                            .padding(.bottom, 10)
                            .id("streaming-reply-anchor")
                        } else if shouldShowInlineWorkLine {
                            if store.isThinking {
                                NoriThinkingIndicator(avatarNamespace: noriAvatarNamespace)
                                    .padding(.top, 4)
                                    .padding(.bottom, 10)
                                    .id("work-line-anchor")
                            } else {
                                InlineWorkLine(
                                    label: workLineLabel,
                                    onDetails: onOpenActivity
                                )
                                .padding(.top, 6)
                                .padding(.bottom, 10)
                                .id("work-line-anchor")
                            }
                        }

                        Color.clear
                            .frame(height: 8)
                            .id("chat-bottom-anchor")
                    }
                    .padding(.horizontal, 14)
                    .padding(.top, 12)
                    .padding(.bottom, 12)
                }
                .defaultScrollAnchor(.bottom)
                .onScrollGeometryChange(for: ChatScrollMetrics.self) { geo in
                    ChatScrollMetrics(
                        distanceFromBottom: max(0, geo.contentSize.height - geo.visibleRect.maxY),
                        contentHeight: geo.contentSize.height
                    )
                } action: { oldValue, newValue in
                    let contentChanged = abs(newValue.contentHeight - oldValue.contentHeight) > 1
                    if !contentChanged {
                        isNearBottom = newValue.distanceFromBottom <= 120
                    } else if newValue.distanceFromBottom <= 24 {
                        isNearBottom = true
                    }
                }
                .scrollDismissesKeyboard(.interactively)
                .refreshable {
                    async let sessionTask: Void = store.refreshSession()
                    async let summaryTask: Void = store.refreshSummary()
                    _ = await (sessionTask, summaryTask)
                }
                .onChange(of: store.messages.last?.id) { oldID, newID in
                    guard newID != nil, oldID != newID else { return }
                    let isNewUserMessage = store.messages.last?.isFromUser == true
                    guard isNearBottom || isNewUserMessage else { return }
                    withAnimation(.easeOut(duration: 0.22)) {
                        proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                    }
                }
                .onChange(of: store.pendingInteractions.map(\.id)) { oldIDs, newIDs in
                    let activeIDs = Set(newIDs)
                    visiblePendingInteractionIDs.formIntersection(activeIDs)
                    if !activeIDs.subtracting(oldIDs).isEmpty, isNearBottom {
                        withAnimation(.easeOut(duration: 0.22)) {
                            proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                        }
                    }
                }
                .onChange(of: store.isThinking) { _, isThinking in
                    guard isThinking, isNearBottom else { return }
                    withAnimation(.easeOut(duration: 0.22)) {
                        proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                    }
                }
                .onChange(of: store.streamingAssistantText) { _, newText in
                    guard let newText, !newText.isEmpty, isNearBottom else { return }
                    proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                }
                .onChange(of: scrollToBottomTrigger) { _, _ in
                    withAnimation(.easeOut(duration: 0.22)) {
                        proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                    }
                }

                if shouldShowNeedsYouBanner {
                    InlineNeedsYouBanner(
                        interaction: primaryPendingInThread,
                        totalPendingCount: max(store.actionableNeedsYouCount, inlinePendingInteractions.count),
                        scrollsToInlineCard: primaryPendingInThread != nil,
                        onTap: {
                            if let target = primaryPendingInThread {
                                withAnimation(.easeInOut(duration: 0.3)) {
                                    proxy.scrollTo(
                                        ChatThreadUnit.interactionAnchorID(for: target.id),
                                        anchor: .center
                                    )
                                }
                            } else {
                                onNavigateToNeedsYou()
                            }
                        }
                    )
                    .padding(.horizontal, 14)
                    .padding(.top, 8)
                    .transition(.move(edge: .top).combined(with: .opacity))
                }
            }
            .animation(.easeInOut(duration: 0.2), value: shouldShowNeedsYouBanner)
        }
    }

    private var workLineLabel: String {
        if store.isThinking {
            return "Nori is thinking…"
        }
        let active = activeBackgroundTasksCount
        if active == 1,
           let single = store.workItems.first(where: { $0.status == .running && $0.bucket == .working }),
           let progress = single.progress?.trimmingCharacters(in: .whitespacesAndNewlines),
           !progress.isEmpty {
            return progress
        }
        return active == 1 ? "Working in the background…" : "\(active) working"
    }

    @ViewBuilder
    private func threadUnitView(_ unit: ChatThreadUnit, activeNoriMessageID: String?) -> some View {
        switch unit {
        case .separator(let id, let label):
            Text(label)
                .font(.system(size: 11.5, weight: .medium))
                .foregroundStyle(NoriColors.ink3)
                .frame(maxWidth: .infinity)
                .padding(.top, 12)
                .padding(.bottom, 10)
                .id(id)

        case .page(let message, let part):
            PageBlockCard(
                message: message,
                part: part,
                onOpen: {
                    let doc = resolveLibraryItem(for: message, part: part)
                    onOpenDocument(doc)
                }
            )
            .padding(.top, 4)
            .padding(.bottom, 12)
            .id(message.id)

        case .group(let id, let isUser, let messages):
            VStack(alignment: isUser ? .trailing : .leading, spacing: 4) {
                ForEach(Array(messages.enumerated()), id: \.element.id) { index, message in
                    let position = BubblePosition.forIndex(index, count: messages.count)
                    if isUser {
                        UserMessageBubble(
                            message: message,
                            position: position,
                            animateArrival: message.receipt == .sending,
                            onCopy: copyMessageText
                        )
                    } else {
                        NoriMessageBubble(
                            message: message,
                            showAvatar: message.id == activeNoriMessageID,
                            markState: effectiveMarkState,
                            avatarNamespace: noriAvatarNamespace,
                            onCopy: copyMessageText
                        )
                    }
                }

                if isUser, let receipt = messages.last?.receipt {
                    MessageReceiptView(receipt: receipt)
                        .padding(.trailing, 4)
                        .padding(.top, 1)
                }
            }
            .frame(maxWidth: .infinity, alignment: isUser ? .trailing : .leading)
            .padding(.bottom, 12)
            .id(id)

        case .reminder(let message, let parts):
            VStack(alignment: .leading, spacing: 8) {
                let trimmedText = message.text.trimmingCharacters(in: .whitespacesAndNewlines)
                let matchesFallback = parts.contains {
                    $0.fallback?.trimmingCharacters(in: .whitespacesAndNewlines) == trimmedText
                }
                if !trimmedText.isEmpty && !matchesFallback {
                    NoriMessageBubble(
                        message: message,
                        showAvatar: message.id == activeNoriMessageID,
                        markState: effectiveMarkState,
                        avatarNamespace: noriAvatarNamespace,
                        onCopy: copyMessageText
                    )
                }

                ForEach(parts) { part in
                    let liveAutomation = store.automations.first(where: { $0.id == part.id })
                    InlineReminderCard(
                        part: part,
                        automation: liveAutomation,
                        isCancelling: cancellingReminderIDs.contains(part.id),
                        onEdit: {
                            openEditReminder(part: part, automation: liveAutomation)
                        },
                        onCancel: {
                            cancelReminder(automationID: part.id)
                        }
                    )
                }
            }
            .padding(.top, 4)
            .padding(.bottom, 12)
            .id("reminder-\(message.id)")

        case .error(let message):
            ChatErrorCard(
                message: message,
                isRetrying: retryingMessageIDs.contains(message.id),
                onRetry: {
                    retryFailedMessage(message)
                },
                onOpenModels: onOpenModels
            )
            .padding(.top, 4)
            .padding(.bottom, 12)
            .id(message.id)

        case .interaction(let item):
            InteractionCardView(
                store: store,
                interaction: item,
                compact: true,
                onOpenTask: onOpenTask
            )
            .padding(.top, 4)
            .padding(.bottom, 12)
            .id(ChatThreadUnit.interactionAnchorID(for: item.id))
            .onScrollVisibilityChange(threshold: 0.15) { isVisible in
                if isVisible {
                    visiblePendingInteractionIDs.insert(item.id)
                } else {
                    visiblePendingInteractionIDs.remove(item.id)
                }
            }
            .onDisappear {
                visiblePendingInteractionIDs.remove(item.id)
            }
        }
    }

    private func resolveLibraryItem(for message: Message, part: MessagePart) -> NoriCore.LibraryItem {
        let candidateID = part.resource?.id ?? message.resultIDs?.first
        if let candidateID, let existing = store.libraryItems.first(where: { $0.id == candidateID }) {
            return existing
        }
        return NoriCore.LibraryItem(
            id: candidateID ?? message.id,
            kind: .document,
            title: part.title ?? (message.text.isEmpty ? "Document" : message.text),
            taskID: message.runID.isEmpty ? nil : message.runID,
            provenance: Provenance(content: .generated),
            createdAt: message.createdAt,
            previewText: part.text ?? part.fallback ?? (message.text.isEmpty ? nil : message.text)
        )
    }

    private func copyMessageText(_ text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        #if os(iOS)
        UIPasteboard.general.string = text
        #elseif os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        #endif
        triggerLightHaptic()
        store.showToast("Copied")
    }

    private func retryFailedMessage(_ message: Message) {
        guard !retryingMessageIDs.contains(message.id) else { return }
        triggerLightHaptic()
        retryingMessageIDs.insert(message.id)
        Task {
            _ = await store.retryMessage(message)
            retryingMessageIDs.remove(message.id)
        }
    }

    private func openEditReminder(part: MessagePart, automation: Automation?) {
        let initialTitle = automation?.title ?? part.reminderTitle
        let rawISO = automation?.trigger.at ?? automation?.nextAt ?? part.scheduledAt
        let initialDate = rawISO.flatMap { NoriDateFormatting.parse($0) }
            ?? Date().addingTimeInterval(3600)
        editingReminder = ReminderEditTarget(
            automationID: part.id,
            title: initialTitle,
            scheduledAt: initialDate
        )
    }

    private func cancelReminder(automationID: String) {
        guard !cancellingReminderIDs.contains(automationID) else { return }
        triggerLightHaptic()
        cancellingReminderIDs.insert(automationID)
        Task {
            _ = await store.cancelAutomation(automationID: automationID)
            cancellingReminderIDs.remove(automationID)
        }
    }

    private func triggerLightHaptic() {
        #if os(iOS)
        let generator = UIImpactFeedbackGenerator(style: .light)
        generator.impactOccurred()
        #endif
    }
}

private struct ReconnectingBanner: View {
    let onRetry: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Text("Can’t reach Nori right now. Resuming SSE…")
                .font(.system(size: 12.5, weight: .medium))
                .foregroundStyle(NoriColors.warning)
                .lineLimit(2)
            Spacer(minLength: 6)
            Button("Retry", action: onRetry)
                .font(.system(size: 11.5, weight: .semibold))
                .foregroundStyle(NoriColors.ink)
                .padding(.horizontal, 10)
                .frame(height: 26)
                .background(NoriColors.surface, in: Capsule())
                .buttonStyle(.plain)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 9)
        .background(NoriColors.warningBg, in: RoundedRectangle(cornerRadius: 13, style: .continuous))
    }
}

private struct InlineNeedsYouBanner: View {
    let interaction: Interaction?
    let totalPendingCount: Int
    let scrollsToInlineCard: Bool
    let onTap: () -> Void

    private var heading: String {
        if let interaction {
            return interaction.displayHeading
        }
        return totalPendingCount == 1
            ? "1 item needs your review"
            : "\(totalPendingCount) items need your review"
    }

    private var subtitle: String {
        if let interaction {
            let base = interaction.taskTitle ?? "Waiting for your review"
            if totalPendingCount > 1 {
                return "\(base) · +\(totalPendingCount - 1) more"
            }
            return base
        }
        return "Open Work to review"
    }

    var body: some View {
        Button(action: onTap) {
            HStack(spacing: 10) {
                Circle()
                    .fill(NoriColors.markActivity)
                    .frame(width: 8, height: 8)
                    .padding(.leading, 2)

                VStack(alignment: .leading, spacing: 1) {
                    Text(heading)
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)
                        .lineLimit(1)
                    Text(subtitle)
                        .font(.system(size: 11.5))
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(1)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                HStack(spacing: 4) {
                    if scrollsToInlineCard {
                        Image(systemName: "arrow.up")
                            .font(.system(size: 10.5, weight: .semibold))
                    }
                    Text("Review")
                        .font(.system(size: 12, weight: .semibold))
                    if !scrollsToInlineCard {
                        Image(systemName: "chevron.right")
                            .font(.system(size: 10, weight: .semibold))
                    }
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
            .padding(.vertical, 9)
            .background {
                RoundedRectangle(cornerRadius: 15, style: .continuous)
                    .fill(.ultraThinMaterial)
                    .overlay(
                        RoundedRectangle(cornerRadius: 15, style: .continuous)
                            .fill(NoriColors.accentSubtle.opacity(0.9))
                    )
            }
            .overlay(
                RoundedRectangle(cornerRadius: 15, style: .continuous)
                    .strokeBorder(NoriColors.accentBorder, lineWidth: 1)
            )
            .shadow(color: NoriColors.shadowColor, radius: 10, x: 0, y: 4)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(heading). \(subtitle).")
        .accessibilityHint(
            scrollsToInlineCard
                ? "Scrolls to the pending card in this conversation"
                : "Opens Work tasks needing your review"
        )
    }
}

enum NoriThinkingWords {
    static let all: [String] = [
        // Whimsical (Claude-style)
        "Onigirifying…",
        "Umamifying…",
        "Norifying…",
        "Makifying…",
        "Temaki-ing…",
        "Wasabi-ing…",
        "Sushifying…",
        "Dashi-ing…",
        "Omakase-ing…",
        "Kombulating…",
        "Mirinating…",
        "Musubifying…",
        "Bentofying…",
        "Misofying…",
        "Furikake-ing…",
        "Yuzufying…",
        "Matchafying…",
        "Mochifying…",
        // Cozy sushi prep
        "Rolling the nori…",
        "Seasoning the rice…",
        "Toasting the seaweed…",
        "Pressing the onigiri…",
        "Simmering dashi…",
        "Grating wasabi…",
        "Plating…",
        "Fanning the rice…",
        "Tucking in the filling…",
        "Sprinkling sesame seeds…",
        "Folding the tamago…",
        "Slicing the maki…",
        "Brushing with soy…",
        "Shaving bonito flakes…",
        "Pickling the ginger…",
        "Pouring green tea…",
        "Setting the chopsticks…"
    ]
}

private struct NoriThinkingIndicator: View {
    let avatarNamespace: Namespace.ID

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var wordIndex: Int = Int.random(in: 0..<NoriThinkingWords.all.count)

    private var currentWord: String {
        NoriThinkingWords.all[wordIndex % NoriThinkingWords.all.count]
    }

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            NoriMark(size: 26, state: .thinking)
                .matchedGeometryEffect(id: "active-nori-avatar", in: avatarNamespace)

            Text(currentWord)
                .font(.system(size: 13.5, weight: .medium))
                .foregroundStyle(NoriColors.ink2)
                .contentTransition(reduceMotion ? .identity : .opacity)
        }
        .padding(.vertical, 3)
        .task {
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2.6))
                guard !Task.isCancelled else { break }
                if reduceMotion {
                    wordIndex = (wordIndex + 1) % NoriThinkingWords.all.count
                } else {
                    withAnimation(.easeInOut(duration: 0.25)) {
                        wordIndex = (wordIndex + 1) % NoriThinkingWords.all.count
                    }
                }
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(currentWord)
    }
}

private struct InlineWorkLine: View {
    let label: String
    let onDetails: () -> Void

    var body: some View {
        Button(action: onDetails) {
            HStack(spacing: 8) {
                PulsingStatusDot(color: NoriColors.brandPrimary, size: 7, animated: true)
                Text(label)
                    .font(.system(size: 13, weight: .medium))
                    .foregroundStyle(NoriColors.ink2)
                    .lineLimit(1)
            }
            .padding(.leading, 36)
            .padding(.vertical, 4)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}

struct PulsingStatusDot: View {
    let color: Color
    var size: CGFloat = 7
    var animated: Bool = true

    @State private var pulsing: Bool = false

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: size, height: size)
            .scaleEffect(animated && pulsing ? 1.25 : 0.9)
            .opacity(animated && pulsing ? 0.55 : 1.0)
            .onAppear {
                guard animated else { return }
                withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) {
                    pulsing = true
                }
            }
    }
}
