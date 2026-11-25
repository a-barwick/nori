import SwiftUI
import PhotosUI
import UniformTypeIdentifiers
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

// MARK: - ChatView

public struct ChatView: View {
    @Bindable private var store: NoriStore
    private let onOpenActivity: () -> Void
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void
    private let onNavigateToNeedsYou: () -> Void
    private let onOpenTask: (String) -> Void

    @State private var draft: String = ""
    @State private var draftAttachments: [DraftAttachment] = []
    @State private var isAttachSheetPresented: Bool = false
    @State private var isFileImporterPresented: Bool = false
    @State private var isSending: Bool = false
    @State private var retryingMessageIDs: Set<String> = []
    @State private var cancellingReminderIDs: Set<String> = []
    @State private var editingReminder: ReminderEditTarget?
    @State private var visiblePendingInteractionIDs: Set<String> = []
    @FocusState private var isInputFocused: Bool
    @Namespace private var noriAvatarNamespace

    public init(
        store: NoriStore,
        onOpenActivity: @escaping () -> Void = {},
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void = { _ in },
        onNavigateToNeedsYou: @escaping () -> Void = {},
        onOpenTask: @escaping (String) -> Void = { _ in }
    ) {
        self.store = store
        self.onOpenActivity = onOpenActivity
        self.onOpenDocument = onOpenDocument
        self.onNavigateToNeedsYou = onNavigateToNeedsYou
        self.onOpenTask = onOpenTask
    }

    private var effectiveMarkState: MarkState {
        if store.isThinking { return .thinking }
        if isInputFocused || !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return .attentive
        }
        return .idle
    }

    private var canSend: Bool {
        let hasText = !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let hasReadyAttachments = draftAttachments.contains { $0.uploaded != nil }
        let anyUploading = draftAttachments.contains { $0.isUploading }
        return (hasText || hasReadyAttachments) && !anyUploading && !isSending
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

    private var hasVisiblePendingInteraction: Bool {
        store.pendingInteractions.contains { visiblePendingInteractionIDs.contains($0.id) }
    }

    private var shouldShowNeedsYouBanner: Bool {
        store.needsYouCount > 0 && !hasVisiblePendingInteraction
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

            threadArea

            floatingComposer
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .onChange(of: isInputFocused) { _, focused in
            store.isComposerFocused = focused
        }
        .onAppear {
            consumePendingComposerDraft()
        }
        .onChange(of: store.pendingComposerDraft) { _, _ in
            consumePendingComposerDraft()
        }
        .sheet(isPresented: $isAttachSheetPresented) {
            AttachSheetView(
                maxUploadBytes: store.capabilities?.uploadAccept?.maxBytes ?? ImageUploadNormalizer.defaultMaxBytes,
                onPickedPhotoData: { data, filename, mediaType in
                    isAttachSheetPresented = false
                    uploadDraftAttachment(data: data, filename: filename, mediaType: mediaType)
                },
                onPhotoNormalizationError: { message in
                    isAttachSheetPresented = false
                    store.showToast("Photo failed: \(message)")
                },
                onRequestFilePicker: {
                    isAttachSheetPresented = false
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) {
                        isFileImporterPresented = true
                    }
                }
            )
        }
        .sheet(item: $editingReminder) { target in
            EditReminderSheet(store: store, target: target)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
        }
        .fileImporter(
            isPresented: $isFileImporterPresented,
            allowedContentTypes: [.pdf, .commaSeparatedText, .image, .plainText, .data],
            allowsMultipleSelection: true
        ) { result in
            switch result {
            case .success(let urls):
                for url in urls {
                    importLocalFile(url: url)
                }
            case .failure(let error):
                store.showToast(error.localizedDescription)
            }
        }
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
    }

    // MARK: - Thread

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
        let activeAvatarTargetID = store.isThinking ? "__thinking__" : (latestNoriID ?? "__none__")
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

                        if shouldShowInlineWorkLine {
                            if store.isThinking {
                                HStack(alignment: .center, spacing: 8) {
                                    NoriMark(size: 28, state: .thinking)
                                        .matchedGeometryEffect(id: "active-nori-avatar", in: noriAvatarNamespace)

                                    InlineWorkLine(
                                        label: workLineLabel,
                                        showMark: false,
                                        onDetails: onOpenActivity
                                    )
                                }
                                .padding(.top, 4)
                                .padding(.bottom, 10)
                                .id("work-line-anchor")
                            } else {
                                InlineWorkLine(
                                    label: workLineLabel,
                                    showMark: true,
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
                    .animation(.spring(response: 0.38, dampingFraction: 0.82), value: activeAvatarTargetID)
                    .padding(.horizontal, 14)
                    .padding(.top, 12)
                    .padding(.bottom, 12)
                }
                .scrollDismissesKeyboard(.interactively)
                .refreshable {
                    async let sessionTask: Void = store.refreshSession()
                    async let summaryTask: Void = store.refreshSummary()
                    _ = await (sessionTask, summaryTask)
                }
                .onChange(of: store.messages.count) { _, _ in
                    withAnimation(.easeOut(duration: 0.24)) {
                        proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                    }
                }
                .onChange(of: store.pendingInteractions.map(\.id)) { oldIDs, newIDs in
                    let activeIDs = Set(newIDs)
                    visiblePendingInteractionIDs.formIntersection(activeIDs)
                    if !activeIDs.subtracting(oldIDs).isEmpty {
                        withAnimation(.easeOut(duration: 0.24)) {
                            proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                        }
                    }
                }
                .onChange(of: store.isThinking) { _, _ in
                    withAnimation(.easeOut(duration: 0.24)) {
                        proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                    }
                }
                .onAppear {
                    proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                }

                if shouldShowNeedsYouBanner {
                    InlineNeedsYouBanner(
                        interaction: primaryPendingInThread,
                        totalPendingCount: store.needsYouCount,
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
        return "\(active) \(active == 1 ? "task" : "tasks") in progress"
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
                        UserMessageBubble(message: message, position: position)
                    } else {
                        NoriMessageBubble(
                            message: message,
                            showAvatar: message.id == activeNoriMessageID,
                            markState: effectiveMarkState,
                            avatarNamespace: noriAvatarNamespace
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
                        avatarNamespace: noriAvatarNamespace
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
                }
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
            previewText: part.text ?? part.fallback,
            bodyText: part.text ?? message.text
        )
    }

    // MARK: - Floating Frosted Glass Composer

    private var floatingComposer: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !draftAttachments.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(draftAttachments) { item in
                            AttachmentChipView(
                                item: item,
                                onRetry: {
                                    retryDraftAttachment(id: item.id)
                                },
                                onRemove: {
                                    withAnimation(.easeOut(duration: 0.16)) {
                                        draftAttachments.removeAll { $0.id == item.id }
                                    }
                                }
                            )
                        }
                    }
                    .padding(.horizontal, 4)
                }
            }

            HStack(alignment: .bottom, spacing: 6) {
                Button {
                    isInputFocused = false
                    isAttachSheetPresented = true
                } label: {
                    Image(systemName: "plus")
                        .font(.system(size: 18, weight: .medium))
                        .foregroundStyle(NoriColors.ink2)
                        .frame(width: 34, height: 34)
                        .background(NoriColors.fill1.opacity(0.001), in: Circle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Attach file or photo")

                TextField("Ask Nori anything…", text: $draft, axis: .vertical)
                    .font(NoriTypography.body)
                    .foregroundStyle(NoriColors.ink)
                    .tint(NoriColors.accent)
                    .lineLimit(1...5)
                    .focused($isInputFocused)
                    .padding(.vertical, 7)
                    .padding(.horizontal, 2)
                    .frame(minHeight: 34)
                    .submitLabel(.return)
                    .onSubmit {
                        #if os(macOS)
                        if canSend { sendCurrentDraft() }
                        #endif
                    }

                Button {
                    sendCurrentDraft()
                } label: {
                    Group {
                        if isSending {
                            ProgressView()
                                .controlSize(.small)
                                .tint(.white)
                        } else {
                            Image(systemName: "arrow.up")
                                .font(.system(size: 16, weight: .bold))
                                .foregroundStyle(canSend ? NoriColors.onAccent : NoriColors.disabled)
                        }
                    }
                    .frame(width: 34, height: 34)
                    .background(
                        isSending
                            ? NoriColors.brandPrimary.opacity(0.6)
                            : (canSend ? NoriColors.brandPrimary : NoriColors.disabledBg),
                        in: Circle()
                    )
                }
                .buttonStyle(.plain)
                .disabled(!canSend)
                .accessibilityLabel(isSending ? "Sending message" : "Send message")
            }
            .padding(.leading, 8)
            .padding(.trailing, 6)
            .padding(.vertical, 6)
            .background {
                RoundedRectangle(cornerRadius: 24, style: .continuous)
                    .fill(.ultraThinMaterial)
                    .overlay(
                        RoundedRectangle(cornerRadius: 24, style: .continuous)
                            .fill(NoriColors.glass.opacity(0.82))
                    )
            }
            .overlay(
                RoundedRectangle(cornerRadius: 24, style: .continuous)
                    .strokeBorder(
                        isInputFocused ? NoriColors.accentSignal : NoriColors.cardBorder,
                        lineWidth: isInputFocused ? 1.5 : 1
                    )
            )
            .animation(.easeOut(duration: 0.16), value: isInputFocused)
            .shadow(color: NoriColors.shadowColor, radius: 14, x: 0, y: 5)
        }
        .padding(.horizontal, 12)
        .padding(.top, 8)
        .padding(.bottom, 10)
        .background(
            LinearGradient(
                colors: [
                    NoriColors.canvas.opacity(0),
                    NoriColors.canvas.opacity(0.96)
                ],
                startPoint: .top,
                endPoint: .init(x: 0.5, y: 0.35)
            )
        )
    }

    // MARK: - Actions

    private func consumePendingComposerDraft() {
        guard let pending = store.pendingComposerDraft else { return }
        draft = pending
        store.pendingComposerDraft = nil
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) {
            isInputFocused = true
        }
    }

    private func sendCurrentDraft() {
        guard !isSending else { return }
        guard !draftAttachments.contains(where: \.isUploading) else { return }

        let rawDraft = draft
        let text = rawDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        let sentAttachments = draftAttachments.filter { $0.uploaded != nil }
        let uploaded = sentAttachments.compactMap(\.uploaded)
        guard !text.isEmpty || !uploaded.isEmpty else { return }

        triggerLightHaptic()
        draft = ""
        draftAttachments.removeAll { $0.uploaded != nil }
        isSending = true

        Task {
            let success = await store.sendMessage(text, attachments: uploaded)
            isSending = false
            if !success {
                restoreDraftAfterFailure(originalDraft: rawDraft, attachments: sentAttachments)
            }
        }
    }

    private func restoreDraftAfterFailure(originalDraft: String, attachments: [DraftAttachment]) {
        if !originalDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            if draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                draft = originalDraft
            } else {
                draft = "\(originalDraft)\n\(draft)"
            }
        }

        if !attachments.isEmpty {
            let existingIDs = Set(draftAttachments.map(\.id))
            let existingUploadedIDs = Set(draftAttachments.compactMap { $0.uploaded?.id })
            let missing = attachments.filter { item in
                guard !existingIDs.contains(item.id) else { return false }
                if let uploadedID = item.uploaded?.id, existingUploadedIDs.contains(uploadedID) {
                    return false
                }
                return true
            }
            if !missing.isEmpty {
                draftAttachments.insert(contentsOf: missing, at: 0)
            }
        }
    }

    private func importLocalFile(url: URL) {
        let accessed = url.startAccessingSecurityScopedResource()
        defer {
            if accessed { url.stopAccessingSecurityScopedResource() }
        }
        do {
            let data = try Data(contentsOf: url)
            let filename = url.lastPathComponent
            let ext = url.pathExtension.lowercased()
            let imageExtensions: Set<String> = ["heic", "heif", "png", "jpg", "jpeg", "webp", "tif", "tiff", "bmp", "gif"]
            if imageExtensions.contains(ext) {
                let maxBytes = store.capabilities?.uploadAccept?.maxBytes ?? ImageUploadNormalizer.defaultMaxBytes
                Task {
                    do {
                        let normalized = try await Task.detached(priority: .userInitiated) {
                            try ImageUploadNormalizer.normalize(
                                data: data,
                                suggestedFilename: filename,
                                maxBytes: maxBytes
                            )
                        }.value
                        uploadDraftAttachment(data: normalized.data, filename: normalized.filename, mediaType: normalized.mediaType)
                    } catch {
                        store.showToast("Could not prepare \(filename): \(error.localizedDescription)")
                    }
                }
                return
            }
            let mediaType: String
            switch ext {
            case "pdf": mediaType = "application/pdf"
            case "csv": mediaType = "text/csv"
            case "ics": mediaType = "text/calendar"
            case "txt": mediaType = "text/plain"
            case "md", "markdown": mediaType = "text/markdown"
            default: mediaType = "application/octet-stream"
            }
            uploadDraftAttachment(data: data, filename: filename, mediaType: mediaType)
        } catch {
            store.showToast("Could not read \(url.lastPathComponent): \(error.localizedDescription)")
        }
    }

    private func uploadDraftAttachment(data: Data, filename: String, mediaType: String) {
        let localID = UUID().uuidString
        let pending = DraftAttachment(
            id: localID,
            name: filename,
            mediaType: mediaType,
            data: data,
            isUploading: true,
            uploaded: nil,
            error: nil
        )
        draftAttachments.append(pending)
        performAttachmentUpload(localID: localID, data: data, filename: filename, mediaType: mediaType)
    }

    private func retryDraftAttachment(id: String) {
        guard let idx = draftAttachments.firstIndex(where: { $0.id == id }) else { return }
        guard !draftAttachments[idx].isUploading else { return }
        let item = draftAttachments[idx]
        draftAttachments[idx].isUploading = true
        draftAttachments[idx].error = nil
        performAttachmentUpload(localID: item.id, data: item.data, filename: item.name, mediaType: item.mediaType)
    }

    private func performAttachmentUpload(localID: String, data: Data, filename: String, mediaType: String) {
        Task {
            do {
                let uploaded = try await store.uploadAttachment(
                    data: data,
                    filename: filename,
                    mediaType: mediaType
                )
                if let idx = draftAttachments.firstIndex(where: { $0.id == localID }) {
                    draftAttachments[idx].isUploading = false
                    draftAttachments[idx].uploaded = uploaded
                    draftAttachments[idx].error = nil
                }
            } catch {
                let reason = error.localizedDescription
                if let idx = draftAttachments.firstIndex(where: { $0.id == localID }) {
                    draftAttachments[idx].isUploading = false
                    draftAttachments[idx].error = reason
                }
                store.showToast("Upload failed (\(filename)): \(reason)")
            }
        }
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

// MARK: - Banners

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

// MARK: - Thread Units & Bubbles

private enum BubblePosition {
    case single
    case first
    case middle
    case last

    static func forIndex(_ index: Int, count: Int) -> BubblePosition {
        if count <= 1 { return .single }
        if index == 0 { return .first }
        if index == count - 1 { return .last }
        return .middle
    }

    var userRadii: RectangleCornerRadii {
        let r = NoriRadius.bubble
        let p = NoriRadius.bubblePinched
        switch self {
        case .single:
            return RectangleCornerRadii(topLeading: r, bottomLeading: r, bottomTrailing: r, topTrailing: r)
        case .first:
            return RectangleCornerRadii(topLeading: r, bottomLeading: r, bottomTrailing: p, topTrailing: r)
        case .middle:
            return RectangleCornerRadii(topLeading: r, bottomLeading: r, bottomTrailing: p, topTrailing: p)
        case .last:
            return RectangleCornerRadii(topLeading: r, bottomLeading: r, bottomTrailing: r, topTrailing: p)
        }
    }
}

private enum ChatThreadUnit: Identifiable {
    case separator(id: String, label: String)
    case page(message: Message, part: MessagePart)
    case reminder(message: Message, parts: [MessagePart])
    case group(id: String, isUser: Bool, messages: [Message])
    case error(Message)
    case interaction(Interaction)

    var id: String {
        switch self {
        case .separator(let id, _): return id
        case .page(let message, _): return "page-\(message.id)"
        case .reminder(let message, _): return "reminder-\(message.id)"
        case .group(let id, _, _): return id
        case .error(let message): return "error-\(message.id)"
        case .interaction(let item): return Self.interactionAnchorID(for: item.id)
        }
    }

    static func interactionAnchorID(for interactionID: String) -> String {
        "inline-interaction-\(interactionID)"
    }

    static func build(
        from messages: [Message],
        pendingInteractions: [Interaction]
    ) -> [ChatThreadUnit] {
        var datedInteractions: [(index: Int, date: Date, interaction: Interaction)] = []
        var undatedInteractions: [Interaction] = []

        for (idx, interaction) in pendingInteractions.enumerated() {
            if let rawDate = interaction.createdAt,
               let parsedDate = NoriDateFormatting.parse(rawDate) {
                datedInteractions.append((index: idx, date: parsedDate, interaction: interaction))
            } else {
                undatedInteractions.append(interaction)
            }
        }

        datedInteractions.sort { lhs, rhs in
            if lhs.date != rhs.date {
                return lhs.date < rhs.date
            }
            return lhs.index < rhs.index
        }

        var result: [ChatThreadUnit] = []
        var buffer: [Message] = []
        var interactionCursor = 0

        func flushBuffer() {
            guard let first = buffer.first else { return }
            result.append(.group(id: "grp-\(first.id)-\(buffer.count)", isUser: first.isFromUser, messages: buffer))
            buffer = []
        }

        for msg in messages {
            if msg.isSuperseded {
                continue
            }

            if let msgDate = NoriDateFormatting.parse(msg.createdAt) {
                while interactionCursor < datedInteractions.count && datedInteractions[interactionCursor].date < msgDate {
                    flushBuffer()
                    result.append(.interaction(datedInteractions[interactionCursor].interaction))
                    interactionCursor += 1
                }
            }

            if let sep = msg.separatorLabel, !sep.isEmpty {
                flushBuffer()
                result.append(.separator(id: "sep-\(msg.id)", label: sep))
            }

            if msg.isFailed {
                flushBuffer()
                result.append(.error(msg))
                continue
            }

            let reminderParts = msg.reminderParts
            if !reminderParts.isEmpty {
                flushBuffer()
                result.append(.reminder(message: msg, parts: reminderParts))
                continue
            }

            if let pagePart = msg.pagePart {
                flushBuffer()
                result.append(.page(message: msg, part: pagePart))
                continue
            }

            if let last = buffer.last, last.isFromUser == msg.isFromUser {
                buffer.append(msg)
            } else {
                flushBuffer()
                buffer = [msg]
            }
        }

        flushBuffer()

        while interactionCursor < datedInteractions.count {
            result.append(.interaction(datedInteractions[interactionCursor].interaction))
            interactionCursor += 1
        }

        for interaction in undatedInteractions {
            result.append(.interaction(interaction))
        }

        return result
    }
}

private struct UserMessageBubble: View {
    let message: Message
    let position: BubblePosition

    private var attachmentParts: [MessagePart] {
        (message.parts ?? []).filter { $0.type == "attachment" }
    }

    var body: some View {
        HStack {
            Spacer(minLength: 52)
            VStack(alignment: .leading, spacing: 6) {
                if !message.text.isEmpty {
                    Text(message.text)
                        .font(NoriTypography.body)
                        .lineSpacing(2)
                        .foregroundStyle(NoriColors.onAccent)
                        .fixedSize(horizontal: false, vertical: true)
                }

                ForEach(attachmentParts) { part in
                    HStack(spacing: 6) {
                        Image(systemName: "doc.text")
                            .font(.system(size: 12, weight: .semibold))
                        Text(part.name ?? "Attachment")
                            .font(.system(size: 12, weight: .medium))
                            .lineLimit(1)
                    }
                    .foregroundStyle(Color.white)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 5)
                    .background(Color.white.opacity(0.16), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .background(
                NoriColors.brandPrimary,
                in: UnevenRoundedRectangle(cornerRadii: position.userRadii, style: .continuous)
            )
        }
    }
}

private struct NoriMessageBubble: View {
    let message: Message
    let showAvatar: Bool
    let markState: MarkState
    let avatarNamespace: Namespace.ID

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            if showAvatar {
                NoriMark(size: 28, state: markState)
                    .matchedGeometryEffect(id: "active-nori-avatar", in: avatarNamespace)
                    .padding(.top, 2)
            } else {
                Color.clear
                    .frame(width: 28, height: 1)
            }

            formattedMarkdownText(message.text)
                .font(NoriTypography.body)
                .lineSpacing(2.5)
                .foregroundStyle(NoriColors.ink)
                .tint(NoriColors.accent)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.vertical, 4)

            Spacer(minLength: 28)
        }
    }

    private func formattedMarkdownText(_ raw: String) -> Text {
        if let attributed = try? AttributedString(
            markdown: raw,
            options: AttributedString.MarkdownParsingOptions(
                interpretedSyntax: .inlineOnlyPreservingWhitespace
            )
        ) {
            return Text(attributed)
        }
        return Text(raw)
    }
}

private struct MessageReceiptView: View {
    let receipt: MessageReceipt

    var body: some View {
        HStack(spacing: 3) {
            if receipt == .saved {
                Image(systemName: "checkmark")
                    .font(.system(size: 10, weight: .semibold))
                Text("Saved")
            } else {
                Text("Sending…")
            }
        }
        .font(.system(size: 11, weight: .medium))
        .foregroundStyle(NoriColors.ink3)
    }
}

// MARK: - Inline Reminder Confirmation Card & Edit Sheet

private struct ReminderEditTarget: Identifiable, Hashable {
    var id: String { automationID }
    let automationID: String
    let title: String
    let scheduledAt: Date
}

private struct InlineReminderCard: View {
    let part: MessagePart
    let automation: Automation?
    let isCancelling: Bool
    let onEdit: () -> Void
    let onCancel: () -> Void

    private var isCancelled: Bool {
        automation?.status == .cancelled
    }

    private var displayTitle: String {
        if let title = automation?.title.trimmingCharacters(in: .whitespacesAndNewlines), !title.isEmpty {
            return title
        }
        return part.reminderTitle
    }

    private var displayWhen: String? {
        if let iso = automation?.trigger.at ?? automation?.nextAt ?? part.scheduledAt,
           !iso.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            let formatted = NoriDateFormatting.reminderDateTime(iso)
            if !formatted.isEmpty {
                return formatted
            }
        }
        if let text = part.text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty {
            return text
        }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: isCancelled ? "bell.slash" : "bell.fill")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(isCancelled ? NoriColors.ink3 : NoriColors.accent)
                    .frame(width: 32, height: 32)
                    .background(
                        isCancelled ? NoriColors.fill1 : NoriColors.accentSubtle,
                        in: RoundedRectangle(cornerRadius: 10, style: .continuous)
                    )

                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 5) {
                        Text(isCancelled ? "Reminder cancelled" : "Reminder set")
                            .font(.system(size: 12.5, weight: .semibold))
                            .foregroundStyle(isCancelled ? NoriColors.ink3 : NoriColors.accent)

                        if let when = displayWhen, !isCancelled {
                            Text("· \(when)")
                                .font(.system(size: 12.5, weight: .medium))
                                .foregroundStyle(NoriColors.ink2)
                                .lineLimit(1)
                        }
                    }

                    Text(displayTitle)
                        .font(.system(size: 14.5, weight: .semibold))
                        .foregroundStyle(isCancelled ? NoriColors.ink3 : NoriColors.ink)
                        .strikethrough(isCancelled, color: NoriColors.ink3)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }

            if !isCancelled {
                HStack(spacing: 8) {
                    Button(action: onEdit) {
                        HStack(spacing: 4) {
                            Image(systemName: "pencil")
                                .font(.system(size: 11, weight: .semibold))
                            Text("Edit")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                    .disabled(isCancelling)
                    .accessibilityLabel("Edit reminder \(displayTitle)")

                    Button(action: onCancel) {
                        HStack(spacing: 4) {
                            if isCancelling {
                                ProgressView()
                                    .controlSize(.mini)
                            } else {
                                Image(systemName: "xmark")
                                    .font(.system(size: 10.5, weight: .semibold))
                            }
                            Text(isCancelling ? "Cancelling…" : "Cancel")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    .disabled(isCancelling)
                    .accessibilityLabel("Cancel reminder \(displayTitle)")

                    Spacer(minLength: 0)
                }
                .padding(.leading, 42)
            }
        }
        .padding(13)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            isCancelled ? NoriColors.sidebar : NoriColors.surface,
            in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(isCancelled ? NoriColors.hairline : NoriColors.hairlineStrong, lineWidth: 1)
        )
        .shadow(color: isCancelled ? .clear : NoriColors.ink.opacity(0.03), radius: 8, x: 0, y: 2)
    }
}

private struct EditReminderSheet: View {
    @Bindable var store: NoriStore
    let target: ReminderEditTarget

    @Environment(\.dismiss) private var dismiss
    @State private var title: String
    @State private var scheduledAt: Date
    @State private var isSaving: Bool = false

    init(store: NoriStore, target: ReminderEditTarget) {
        self.store = store
        self.target = target
        self._title = State(initialValue: target.title)
        self._scheduledAt = State(initialValue: target.scheduledAt)
    }

    private var isValid: Bool {
        !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !isSaving
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Reminder")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("What should Nori remind you about?", text: $title)
                            .textFieldStyle(.plain)
                            .padding(.horizontal, 12)
                            .frame(height: 42)
                            .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    .strokeBorder(NoriColors.hairline, lineWidth: 1)
                            )
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("When")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        DatePicker(
                            "Reminder time",
                            selection: $scheduledAt,
                            displayedComponents: [.date, .hourAndMinute]
                        )
                        .labelsHidden()
                        .tint(NoriColors.accent)
                    }

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(.white)
                        } else {
                            Text("Save changes")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!isValid)
                    .opacity(isValid ? 1 : 0.5)
                    .padding(.top, 4)
                }
                .padding(16)
            }
            .background(NoriColors.canvas)
            .navigationTitle("Edit Reminder")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(NoriColors.ink2)
                }
            }
        }
    }

    private func save() {
        guard isValid else { return }
        isSaving = true
        Task {
            let updated = await store.updateReminder(
                automationID: target.automationID,
                title: title,
                scheduledAt: scheduledAt
            )
            isSaving = false
            if updated != nil {
                dismiss()
            }
        }
    }
}

// MARK: - Inline Chat Error Card

private struct ChatErrorCard: View {
    let message: Message
    let isRetrying: Bool
    let onRetry: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 9) {
                Image(systemName: "exclamationmark.circle.fill")
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(NoriColors.danger)
                    .padding(.top, 1)

                VStack(alignment: .leading, spacing: 3) {
                    Text("Couldn’t get a reply")
                        .font(.system(size: 13.5, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)

                    Text(message.displayErrorMessage)
                        .font(.system(size: 13))
                        .lineSpacing(2)
                        .foregroundStyle(NoriColors.ink2)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }

            if message.isRetryableError {
                HStack {
                    Button(action: onRetry) {
                        HStack(spacing: 6) {
                            if isRetrying {
                                ProgressView()
                                    .controlSize(.mini)
                                    .tint(NoriColors.onAccent)
                            } else {
                                Image(systemName: "arrow.clockwise")
                                    .font(.system(size: 11.5, weight: .semibold))
                            }
                            Text(isRetrying ? "Retrying…" : "Retry")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                    .disabled(isRetrying)
                    .accessibilityLabel(isRetrying ? "Retrying message" : "Retry message")

                    Spacer(minLength: 0)
                }
                .padding(.leading, 25)
            }
        }
        .padding(13)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            NoriColors.dangerBg.opacity(0.65),
            in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.danger.opacity(0.24), lineWidth: 1)
        )
    }
}

// MARK: - Inline PageBlock Card

private struct PageBlockCard: View {
    let message: Message
    let part: MessagePart
    let onOpen: () -> Void

    private var title: String {
        part.title ?? (message.text.isEmpty ? "Saved Document" : message.text)
    }

    private var summary: String? {
        part.text ?? part.fallback
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                NoriMark(size: 16, state: .idle)
                Text("Nori")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)
                if !message.createdAt.isEmpty {
                    Text("· \(NoriDateFormatting.shortDateTime(message.createdAt))")
                        .font(.system(size: 12))
                        .foregroundStyle(NoriColors.ink2)
                }
                Spacer(minLength: 8)
                Button(action: onOpen) {
                    HStack(spacing: 4) {
                        Image(systemName: "doc.text")
                            .font(.system(size: 11, weight: .semibold))
                        Text("Open")
                            .font(.system(size: 11.5, weight: .semibold))
                    }
                    .foregroundStyle(NoriColors.accent)
                    .padding(.horizontal, 9)
                    .frame(height: 26)
                    .background(NoriColors.accentSubtle, in: Capsule())
                }
                .buttonStyle(.plain)
            }
            .padding(.bottom, 10)

            Rectangle()
                .fill(NoriColors.cardBorder)
                .frame(height: 0.5)
                .padding(.bottom, 10)

            Text(title)
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(NoriColors.ink)
                .padding(.bottom, 6)

            if let summary, !summary.isEmpty {
                Text(summary)
                    .font(.system(size: 13.5))
                    .lineSpacing(2.5)
                    .foregroundStyle(NoriColors.ink2)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if let metrics = part.metrics, !metrics.isEmpty {
                LazyVGrid(
                    columns: [
                        GridItem(.flexible(), spacing: 8),
                        GridItem(.flexible(), spacing: 8)
                    ],
                    spacing: 8
                ) {
                    ForEach(metrics) { metric in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(metric.label)
                                .font(.system(size: 11))
                                .foregroundStyle(NoriColors.ink3)
                                .lineLimit(1)
                            Text(metric.value)
                                .font(.system(size: 13.5, weight: .bold, design: .monospaced))
                                .foregroundStyle(NoriColors.ink)
                                .lineLimit(1)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 10)
                        .padding(.vertical, 8)
                        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                }
                .padding(.top, 10)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.hairline, lineWidth: 1)
        )
        .shadow(color: NoriColors.cardShadow, radius: 8, x: 0, y: 2)
    }
}

// MARK: - Inline WorkLine

private struct InlineWorkLine: View {
    let label: String
    var showMark: Bool = true
    let onDetails: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            if showMark {
                NoriMark(size: 18, state: .thinking)
            }
            Text(label)
                .font(.system(size: 12.5))
                .foregroundStyle(NoriColors.ink2)
                .lineLimit(2)
            Spacer(minLength: 8)
            Button("Details", action: onDetails)
                .font(.system(size: 12.5, weight: .semibold))
                .foregroundStyle(NoriColors.accent)
                .buttonStyle(.plain)
        }
        .padding(.horizontal, 11)
        .padding(.vertical, 8)
        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }
}

// MARK: - Draft Attachments & Attach Sheet

private struct DraftAttachment: Identifiable, Sendable {
    let id: String
    let name: String
    let mediaType: String
    let data: Data
    var isUploading: Bool
    var uploaded: Attachment?
    var error: String?
}

private struct AttachmentChipView: View {
    let item: DraftAttachment
    let onRetry: () -> Void
    let onRemove: () -> Void

    var body: some View {
        if let error = item.error, !error.isEmpty {
            HStack(alignment: .center, spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(NoriColors.danger)

                VStack(alignment: .leading, spacing: 1) {
                    Text(item.name)
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(NoriColors.danger)
                        .lineLimit(1)
                    Text(error)
                        .font(.system(size: 11))
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(2)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: 200, alignment: .leading)

                Button(action: onRetry) {
                    HStack(spacing: 3) {
                        Image(systemName: "arrow.clockwise")
                            .font(.system(size: 10, weight: .bold))
                        Text("Retry")
                            .font(.system(size: 11.5, weight: .semibold))
                    }
                    .foregroundStyle(NoriColors.danger)
                    .padding(.horizontal, 8)
                    .frame(height: 24)
                    .background(NoriColors.surface, in: Capsule())
                    .overlay(
                        Capsule()
                            .strokeBorder(NoriColors.danger.opacity(0.32), lineWidth: 1)
                    )
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Retry uploading \(item.name)")

                Button(action: onRemove) {
                    Image(systemName: "xmark")
                        .font(.system(size: 10, weight: .bold))
                        .foregroundStyle(NoriColors.ink3)
                        .frame(width: 18, height: 18)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Remove \(item.name)")
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .background(NoriColors.dangerBg, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .strokeBorder(NoriColors.danger.opacity(0.35), lineWidth: 1)
            )
        } else {
            HStack(spacing: 5) {
                if item.isUploading {
                    ProgressView()
                        .controlSize(.mini)
                } else {
                    Image(systemName: item.mediaType.hasPrefix("image/") ? "photo" : "doc.text")
                        .font(.system(size: 11.5, weight: .medium))
                        .foregroundStyle(NoriColors.ink2)
                }

                Text(item.name)
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(NoriColors.ink)
                    .lineLimit(1)

                Button(action: onRemove) {
                    Image(systemName: "xmark")
                        .font(.system(size: 10, weight: .bold))
                        .foregroundStyle(NoriColors.ink3)
                        .frame(width: 18, height: 18)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Remove \(item.name)")
            }
            .padding(.leading, 9)
            .padding(.trailing, 5)
            .padding(.vertical, 6)
            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                    .strokeBorder(NoriColors.hairline, lineWidth: 1)
            )
        }
    }
}

private struct AttachSheetView: View {
    @Environment(\.dismiss) private var dismiss
    let maxUploadBytes: Int
    let onPickedPhotoData: (Data, String, String) -> Void
    let onPhotoNormalizationError: (String) -> Void
    let onRequestFilePicker: () -> Void

    @State private var selectedPhotoItem: PhotosPickerItem?

    var body: some View {
        let photoLibraryRow = NoriRow(
            title: "Photo Library",
            detail: "Photos & screenshots (auto-converted if needed)",
            systemImage: "photo.on.rectangle",
            iconTone: .accent,
            showChevron: true,
            showDivider: true
        )

        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    NoriRows {
                        PhotosPicker(selection: $selectedPhotoItem, matching: .images) {
                            photoLibraryRow
                        }
                        .buttonStyle(.plain)

                        Button(action: onRequestFilePicker) {
                            NoriRow(
                                title: "Choose File",
                                detail: "PDF, CSV, Markdown via Files",
                                systemImage: "folder",
                                iconTone: .accent,
                                showChevron: true,
                                showDivider: false
                            )
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.top, 4)
                .padding(.bottom, 20)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Attach to Nori")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button {
                        dismiss()
                    } label: {
                        Image(systemName: "xmark")
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(NoriColors.ink)
                            .frame(width: 30, height: 30)
                            .background(NoriColors.elevated, in: Circle())
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .onChange(of: selectedPhotoItem) { _, newItem in
            guard let newItem else { return }
            let limit = maxUploadBytes
            Task {
                do {
                    guard let rawData = try await newItem.loadTransferable(type: Data.self) else {
                        onPhotoNormalizationError("Could not load the selected photo.")
                        selectedPhotoItem = nil
                        return
                    }
                    let baseFilename = "photo_\(Int(Date().timeIntervalSince1970))"
                    let normalized = try await Task.detached(priority: .userInitiated) {
                        try ImageUploadNormalizer.normalize(
                            data: rawData,
                            suggestedFilename: baseFilename,
                            maxBytes: limit
                        )
                    }.value
                    onPickedPhotoData(normalized.data, normalized.filename, normalized.mediaType)
                } catch {
                    onPhotoNormalizationError(error.localizedDescription)
                }
                selectedPhotoItem = nil
            }
        }
        .presentationDetents([.height(240), .medium])
        .presentationDragIndicator(.visible)
        .presentationBackground(NoriColors.sheetBackground)
        .presentationCornerRadius(NoriRadius.sheet)
    }
}

// MARK: - Pulsing Status Dot (Shared within NoriFeatures)

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
