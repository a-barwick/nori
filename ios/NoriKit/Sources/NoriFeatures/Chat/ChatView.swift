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
    @FocusState private var isInputFocused: Bool

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

    public var body: some View {
        VStack(spacing: 0) {
            ChatTopBar(
                markState: effectiveMarkState,
                statusLine: store.statusLine,
                isActive: store.activeTasksCount > 0 || store.isThinking,
                onOpenActivity: onOpenActivity
            )

            if isReconnecting {
                ReconnectingBanner {
                    Task {
                        await store.refreshAll()
                        store.startEventStream()
                    }
                }
                .padding(.horizontal, 14)
                .padding(.top, 8)
                .transition(.move(edge: .top).combined(with: .opacity))
            }

            if let firstPending = store.pendingInteractions.first {
                InlineNeedsYouBanner(
                    interaction: firstPending,
                    totalPendingCount: store.needsYouCount,
                    onTap: onNavigateToNeedsYou
                )
                .padding(.horizontal, 14)
                .padding(.top, 8)
                .transition(.move(edge: .top).combined(with: .opacity))
            }

            threadScrollView

            floatingComposer
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .onChange(of: isInputFocused) { _, focused in
            store.isComposerFocused = focused
        }
        .sheet(isPresented: $isAttachSheetPresented) {
            AttachSheetView(
                onPickedPhotoData: { data, filename, mediaType in
                    isAttachSheetPresented = false
                    uploadDraftAttachment(data: data, filename: filename, mediaType: mediaType)
                },
                onRequestFilePicker: {
                    isAttachSheetPresented = false
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) {
                        isFileImporterPresented = true
                    }
                },
                onPickDemoSample: { sample in
                    isAttachSheetPresented = false
                    let sampleData = Data(sample.content.utf8)
                    uploadDraftAttachment(data: sampleData, filename: sample.filename, mediaType: sample.mediaType)
                }
            )
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

    private var threadScrollView: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    if store.messages.isEmpty && store.pendingInteractions.isEmpty {
                        NoriEmptyState(
                            title: store.isLoading ? "Loading conversation…" : "Ask Nori anything",
                            message: store.isLoading
                                ? "Syncing your persistent thread."
                                : "Messages, files, and background work all live in one continuous thread.",
                            markState: store.isLoading ? .thinking : .idle
                        )
                        .padding(.top, 48)
                    } else {
                        let units = ChatThreadUnit.build(from: store.messages)
                        ForEach(units) { unit in
                            threadUnitView(unit)
                        }
                    }

                    if !store.pendingInteractions.isEmpty {
                        VStack(alignment: .leading, spacing: 10) {
                            ForEach(store.pendingInteractions) { item in
                                InteractionCardView(
                                    store: store,
                                    interaction: item,
                                    compact: true,
                                    onOpenTask: onOpenTask
                                )
                                .id("inline-interaction-\(item.id)")
                            }
                        }
                        .padding(.top, 4)
                        .padding(.bottom, 10)
                    }

                    if store.isThinking || store.activeTasksCount > 0 {
                        InlineWorkLine(
                            label: workLineLabel,
                            onDetails: onOpenActivity
                        )
                        .padding(.top, 6)
                        .padding(.bottom, 10)
                        .id("work-line-anchor")
                    }

                    Color.clear
                        .frame(height: 8)
                        .id("chat-bottom-anchor")
                }
                .padding(.horizontal, 14)
                .padding(.top, 12)
                .padding(.bottom, 12)
            }
            .scrollDismissesKeyboard(.interactively)
            .refreshable {
                await store.refreshAll()
            }
            .onChange(of: store.messages.count) { _, _ in
                withAnimation(.easeOut(duration: 0.24)) {
                    proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
                }
            }
            .onChange(of: store.pendingInteractions.map(\.id)) { _, _ in
                withAnimation(.easeOut(duration: 0.24)) {
                    proxy.scrollTo("chat-bottom-anchor", anchor: .bottom)
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
        }
    }

    private var workLineLabel: String {
        if store.isThinking {
            return "Nori is thinking…"
        }
        let active = store.activeTasksCount
        let needs = store.needsYouCount
        let base = "\(active) \(active == 1 ? "task" : "tasks") in progress"
        if needs > 0 {
            return "\(base) · \(needs) waiting for you"
        }
        return base
    }

    @ViewBuilder
    private func threadUnitView(_ unit: ChatThreadUnit) -> some View {
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
                            showAvatar: index == 0
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
                            AttachmentChipView(item: item) {
                                withAnimation(.easeOut(duration: 0.16)) {
                                    draftAttachments.removeAll { $0.id == item.id }
                                }
                            }
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

                if canSend || !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !draftAttachments.isEmpty {
                    Button {
                        sendCurrentDraft()
                    } label: {
                        Image(systemName: "arrow.up")
                            .font(.system(size: 16, weight: .bold))
                            .foregroundStyle(Color.white)
                            .frame(width: 34, height: 34)
                            .background(
                                canSend ? NoriColors.accent : NoriColors.accent.opacity(0.4),
                                in: Circle()
                            )
                    }
                    .buttonStyle(.plain)
                    .disabled(!canSend)
                    .accessibilityLabel("Send message")
                } else {
                    Button {
                        handleVoicePrompt()
                    } label: {
                        Image(systemName: "mic")
                            .font(.system(size: 15, weight: .semibold))
                            .foregroundStyle(NoriColors.ink)
                            .frame(width: 34, height: 34)
                            .background(NoriColors.fill1, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Dictate prompt")
                }
            }
            .padding(.leading, 8)
            .padding(.trailing, 6)
            .padding(.vertical, 6)
            .background {
                RoundedRectangle(cornerRadius: 24, style: .continuous)
                    .fill(.ultraThinMaterial)
                    .overlay(
                        RoundedRectangle(cornerRadius: 24, style: .continuous)
                            .fill(NoriColors.glass.opacity(0.76))
                    )
            }
            .overlay(
                RoundedRectangle(cornerRadius: 24, style: .continuous)
                    .strokeBorder(
                        isInputFocused ? NoriColors.accentSignal : NoriColors.hairlineStrong,
                        lineWidth: 1
                    )
            )
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

    private func sendCurrentDraft() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        let uploaded = draftAttachments.compactMap(\.uploaded)
        guard !text.isEmpty || !uploaded.isEmpty else { return }

        triggerLightHaptic()
        draft = ""
        draftAttachments = []
        isSending = true

        Task {
            await store.sendMessage(text, attachments: uploaded)
            isSending = false
        }
    }

    private func handleVoicePrompt() {
        triggerLightHaptic()
        store.showToast("Use the iOS keyboard microphone to dictate")
        isInputFocused = true
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
            let mediaType: String
            switch ext {
            case "pdf": mediaType = "application/pdf"
            case "csv": mediaType = "text/csv"
            case "png": mediaType = "image/png"
            case "jpg", "jpeg": mediaType = "image/jpeg"
            case "webp": mediaType = "image/webp"
            case "md": mediaType = "text/markdown"
            default: mediaType = "application/octet-stream"
            }
            uploadDraftAttachment(data: data, filename: filename, mediaType: mediaType)
        } catch {
            store.showToast("Could not read \(url.lastPathComponent)")
        }
    }

    private func uploadDraftAttachment(data: Data, filename: String, mediaType: String) {
        let localID = UUID().uuidString
        let pending = DraftAttachment(
            id: localID,
            name: filename,
            mediaType: mediaType,
            isUploading: true,
            uploaded: nil,
            error: nil
        )
        draftAttachments.append(pending)

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
                }
            } catch {
                if let idx = draftAttachments.firstIndex(where: { $0.id == localID }) {
                    draftAttachments[idx].isUploading = false
                    draftAttachments[idx].error = error.localizedDescription
                }
                store.showToast("Upload failed: \(filename)")
            }
        }
    }

    private func triggerLightHaptic() {
        #if os(iOS)
        let generator = UIImpactFeedbackGenerator(style: .light)
        generator.impactOccurred()
        #endif
    }
}

// MARK: - Top Navigation Bar

private struct ChatTopBar: View {
    let markState: MarkState
    let statusLine: StatusLine
    let isActive: Bool
    let onOpenActivity: () -> Void

    private var statusColor: Color {
        switch statusLine.tone {
        case .needsYou: return NoriColors.accent
        case .offline: return NoriColors.warning
        case .normal: return NoriColors.ink2
        }
    }

    private var statusWeight: Font.Weight {
        switch statusLine.tone {
        case .needsYou: return .semibold
        case .offline: return .medium
        case .normal: return .regular
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Button(action: onOpenActivity) {
                    HStack(spacing: 10) {
                        NoriMark(size: 36, state: markState)
                        VStack(alignment: .leading, spacing: 1) {
                            Text("Nori")
                                .font(.system(size: 17, weight: .bold))
                                .foregroundStyle(NoriColors.ink)
                            Text(statusLine.text)
                                .font(.system(size: 11.5, weight: statusWeight))
                                .foregroundStyle(statusColor)
                                .lineLimit(1)
                        }
                    }
                    .padding(.vertical, 2)
                    .padding(.trailing, 6)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Nori status: \(statusLine.text)")

                Spacer(minLength: 8)

                Button(action: onOpenActivity) {
                    HStack(spacing: 6) {
                        Image(systemName: "waveform.path.ecg")
                            .font(.system(size: 13.5, weight: .semibold))
                        Text("Activity")
                            .font(.system(size: 12.5, weight: .semibold))
                        if isActive {
                            PulsingStatusDot(color: NoriColors.accent, size: 6.5)
                        }
                    }
                    .foregroundStyle(NoriColors.accent)
                    .padding(.horizontal, 11)
                    .frame(height: 34)
                    .background(NoriColors.surface, in: Capsule())
                    .overlay(
                        Capsule()
                            .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
                    )
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Open Activity sheet")
            }
            .padding(.horizontal, 16)
            .padding(.top, 8)
            .padding(.bottom, 10)
            .background(NoriColors.canvas.opacity(0.94))

            Rectangle()
                .fill(NoriColors.hairline)
                .frame(height: 1)
        }
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
    let interaction: Interaction
    let totalPendingCount: Int
    let onTap: () -> Void

    private var subtitle: String {
        let base = interaction.taskTitle ?? "Waiting for your review"
        if totalPendingCount > 1 {
            return "\(base) · +\(totalPendingCount - 1) more"
        }
        return base
    }

    var body: some View {
        Button(action: onTap) {
            HStack(spacing: 10) {
                Image(systemName: "checkmark.shield")
                    .font(.system(size: 14.5, weight: .semibold))
                    .foregroundStyle(Color.white)
                    .frame(width: 30, height: 30)
                    .background(NoriColors.accent, in: RoundedRectangle(cornerRadius: 9, style: .continuous))

                VStack(alignment: .leading, spacing: 1) {
                    Text(interaction.displayHeading)
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)
                        .lineLimit(1)
                    Text(subtitle)
                        .font(.system(size: 11.5))
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(1)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                Text("Review")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(NoriColors.accent)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 5)
                    .background(NoriColors.surface, in: Capsule())
                    .overlay(
                        Capsule()
                            .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
                    )
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 15, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 15, style: .continuous)
                    .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
            )
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(interaction.displayHeading). \(subtitle). Tap to review.")
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
    case group(id: String, isUser: Bool, messages: [Message])

    var id: String {
        switch self {
        case .separator(let id, _): return id
        case .page(let message, _): return "page-\(message.id)"
        case .group(let id, _, _): return id
        }
    }

    static func build(from messages: [Message]) -> [ChatThreadUnit] {
        var result: [ChatThreadUnit] = []
        var buffer: [Message] = []

        func flushBuffer() {
            guard let first = buffer.first else { return }
            result.append(.group(id: "grp-\(first.id)-\(buffer.count)", isUser: first.isFromUser, messages: buffer))
            buffer = []
        }

        for msg in messages {
            if let sep = msg.separatorLabel, !sep.isEmpty {
                flushBuffer()
                result.append(.separator(id: "sep-\(msg.id)", label: sep))
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
                NoriColors.accent,
                in: UnevenRoundedRectangle(cornerRadii: position.userRadii, style: .continuous)
            )
        }
    }
}

private struct NoriMessageBubble: View {
    let message: Message
    let showAvatar: Bool

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            if showAvatar {
                NoriMark(size: 28, state: .idle)
                    .padding(.top, 2)
            } else {
                Color.clear
                    .frame(width: 28, height: 1)
            }

            formattedMarkdownText(message.text)
                .font(NoriTypography.body)
                .lineSpacing(2.5)
                .foregroundStyle(NoriColors.ink)
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
                    Text("· \(message.createdAt)")
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
                .fill(NoriColors.hairline)
                .frame(height: 1)
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
        .shadow(color: NoriColors.ink.opacity(0.03), radius: 8, x: 0, y: 2)
    }
}

// MARK: - Inline WorkLine

private struct InlineWorkLine: View {
    let label: String
    let onDetails: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            NoriMark(size: 18, state: .thinking)
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
    var isUploading: Bool
    var uploaded: Attachment?
    var error: String?
}

private struct AttachmentChipView: View {
    let item: DraftAttachment
    let onRemove: () -> Void

    var body: some View {
        HStack(spacing: 5) {
            if item.isUploading {
                ProgressView()
                    .controlSize(.mini)
            } else {
                Image(systemName: item.mediaType.hasPrefix("image/") ? "photo" : "doc.text")
                    .font(.system(size: 11.5, weight: .medium))
                    .foregroundStyle(item.error != nil ? NoriColors.danger : NoriColors.ink2)
            }

            Text(item.name)
                .font(.system(size: 12, weight: .medium))
                .foregroundStyle(item.error != nil ? NoriColors.danger : NoriColors.ink)
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
        .padding(.vertical, 5)
        .background(NoriColors.fill1, in: Capsule())
        .overlay(
            Capsule()
                .strokeBorder(item.error != nil ? NoriColors.danger.opacity(0.4) : NoriColors.hairlineStrong, lineWidth: 1)
        )
    }
}

private struct DemoAttachmentSample: Identifiable {
    let id: String
    let filename: String
    let detail: String
    let mediaType: String
    let systemImage: String
    let content: String

    static let samples: [DemoAttachmentSample] = [
        DemoAttachmentSample(
            id: "quote",
            filename: "vendor_quote.pdf",
            detail: "Sample Q4 Datadog renewal quote",
            mediaType: "application/pdf",
            systemImage: "doc.text",
            content: "Datadog Pro Annual Quote — 8 seats @ $150/mo = $14,400/yr."
        ),
        DemoAttachmentSample(
            id: "whiteboard",
            filename: "whiteboard_sketch.png",
            detail: "Sample architecture whiteboard capture",
            mediaType: "image/png",
            systemImage: "photo",
            content: "PNG_DEMO_IMAGE_PAYLOAD"
        ),
        DemoAttachmentSample(
            id: "csv",
            filename: "Q4_billing_export.csv",
            detail: "Sample vendor seats CSV export",
            mediaType: "text/csv",
            systemImage: "tablecells",
            content: "vendor,plan,annual_usd,seats_active,seats_billed\nAWS,Enterprise,29400,18,18\nDatadog,Pro,14200,6,8\n"
        )
    ]
}

private struct AttachSheetView: View {
    @Environment(\.dismiss) private var dismiss
    let onPickedPhotoData: (Data, String, String) -> Void
    let onRequestFilePicker: () -> Void
    let onPickDemoSample: (DemoAttachmentSample) -> Void

    @State private var selectedPhotoItem: PhotosPickerItem?

    var body: some View {
        let photoLibraryRow = NoriRow(
            title: "Photo Library",
            detail: "PNG, JPEG, WebP via native PhotosPicker",
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

                    NoriSectionHeader("Quick Demo Samples")

                    NoriRows {
                        ForEach(Array(DemoAttachmentSample.samples.enumerated()), id: \.element.id) { idx, sample in
                            Button {
                                onPickDemoSample(sample)
                            } label: {
                                NoriRow(
                                    title: sample.filename,
                                    detail: sample.detail,
                                    systemImage: sample.systemImage,
                                    iconTone: .neutral,
                                    showChevron: false,
                                    showDivider: idx < DemoAttachmentSample.samples.count - 1
                                ) {
                                    NoriPill("Attach", tone: .accent)
                                }
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
                .padding(.horizontal, 16)
                .padding(.top, 4)
                .padding(.bottom, 20)
            }
            .background(NoriColors.canvas)
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
                            .background(NoriColors.fill1, in: Circle())
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .onChange(of: selectedPhotoItem) { _, newItem in
            guard let newItem else { return }
            Task {
                if let data = try? await newItem.loadTransferable(type: Data.self) {
                    let filename = "photo_\(Int(Date().timeIntervalSince1970)).jpg"
                    onPickedPhotoData(data, filename, "image/jpeg")
                }
                selectedPhotoItem = nil
            }
        }
        .presentationDetents([.height(410), .medium])
        .presentationDragIndicator(.visible)
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
