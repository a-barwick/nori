import SwiftUI
import NoriCore
import NoriDesign
#if canImport(QuickLook)
import QuickLook
#endif
#if canImport(PDFKit)
import PDFKit
#endif

public struct DocumentReaderSheet: View {
    @Bindable private var store: NoriStore
    public let itemID: String

    @Environment(\.dismiss) private var dismiss
    @State private var hydratedItem: NoriCore.LibraryItem?
    @State private var loadedItemID: String?
    @State private var hasAttemptedLoad = false
    @State private var isLoadingItem = false
    @State private var isLoadingFile = false
    @State private var loadedPayload: LibraryFilePayload?
    @State private var fileLoadError: String?
    @State private var decodedImage: DocumentPlatformImage?
    @State private var pdfPageCount: Int?
    @State private var pdfThumbnail: DocumentPlatformImage?
    @State private var tempFileURL: URL?
    @State private var quickLookURL: URL?
    @State private var tempFileCleanup = DocumentTempFileCleanup()
    @State private var reloadTrigger = 0

    @State private var revisionInstruction = ""
    @State private var isRevising = false
    @State private var queuedRevision: LibraryRevisionResult?

    public init(store: NoriStore, itemID: String) {
        self.store = store
        self.itemID = itemID
    }

    private struct LoadKey: Equatable {
        let itemID: String
        let trigger: Int
    }

    private var item: NoriCore.LibraryItem? {
        store.libraryItems.first(where: { $0.id == itemID }) ?? hydratedItem
    }

    private var effectiveMediaType: String {
        let payloadType = DocumentFileUtilities.normalizedMediaType(loadedPayload?.mediaType)
        let itemType = DocumentFileUtilities.normalizedMediaType(item?.mediaType)
        if !payloadType.isEmpty && payloadType != "application/octet-stream" {
            return payloadType
        }
        if !itemType.isEmpty {
            return itemType
        }
        return payloadType
    }

    private var isImageItem: Bool {
        if decodedImage != nil { return true }
        if item?.kind == .screenshot { return true }
        return effectiveMediaType.hasPrefix("image/")
    }

    private var isPDFItem: Bool {
        if effectiveMediaType == "application/pdf" { return true }
        if let name = loadedPayload?.filename ?? item?.name ?? item?.title,
           name.lowercased().hasSuffix(".pdf") {
            return true
        }
        return false
    }

    private var isTextPayload: Bool {
        guard let payload = loadedPayload, payload.utf8Text != nil else { return false }
        let mime = DocumentFileUtilities.normalizedMediaType(payload.mediaType)
        if DocumentFileUtilities.isTextMediaType(mime) { return true }
        if mime.isEmpty || mime == "application/octet-stream" {
            if let item, item.kind == .document || item.kind == .report || item.kind == .answer {
                return true
            }
        }
        return false
    }

    private var fullTextContent: String? {
        if isTextPayload, let text = loadedPayload?.utf8Text, !text.isEmpty {
            return text
        }
        if let cached = store.cachedBodyText(for: itemID), !cached.isEmpty {
            return cached
        }
        if let preview = item?.previewText, !preview.isEmpty {
            return preview
        }
        return nil
    }

    private var isMonospaceText: Bool {
        let mime = effectiveMediaType
        if DocumentFileUtilities.isMonospaceMediaType(mime) { return true }
        let candidateName = (loadedPayload?.filename ?? item?.name ?? item?.title ?? "").lowercased()
        let ext = (candidateName as NSString).pathExtension
        return DocumentFileUtilities.monospaceExtensions.contains(ext)
    }

    private var canRevise: Bool {
        guard let item else { return false }
        return item.kind == .document || item.kind == .report || item.kind == .answer
    }

    private var prefersFileShare: Bool {
        guard tempFileURL != nil else { return false }
        if isImageItem || isPDFItem { return true }
        if item?.kind == .file { return true }
        if loadedPayload != nil && !isTextPayload { return true }
        return false
    }

    private var externalLinkURL: URL? {
        guard let item, item.kind == .link, let raw = item.url,
              let url = URL(string: raw),
              let scheme = url.scheme?.lowercased(),
              scheme == "https" || scheme == "http" else {
            return nil
        }
        return url
    }

    private var canCopyContent: Bool {
        fullTextContent != nil || decodedImage != nil || externalLinkURL != nil
    }

    public var body: some View {
        NavigationStack {
            contentBody
                .background(NoriColors.sheetBackground)
                .navigationTitle(item?.title ?? "Document")
                #if os(iOS)
                .navigationBarTitleDisplayMode(.inline)
                #endif
                .toolbar {
                    if let item {
                        ToolbarItem(placement: .primaryAction) {
                            toolbarShareButton(for: item)
                        }
                    }
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
                        .accessibilityLabel("Close")
                    }
                }
        }
        #if canImport(QuickLook)
        .quickLookPreview($quickLookURL)
        #endif
        .task(id: LoadKey(itemID: itemID, trigger: reloadTrigger)) {
            await loadDocument()
        }
        .onDisappear {
            if quickLookURL == nil {
                tempFileCleanup.clearAll()
                tempFileURL = nil
            }
        }
    }

    @ViewBuilder
    private var contentBody: some View {
        ScrollView {
            if let item {
                VStack(alignment: .leading, spacing: 14) {
                    metadataPills(for: item)
                    documentBodySection(for: item)
                    quickActionsRow(for: item)
                    if canRevise {
                        revisionSection(for: item)
                    }
                }
                .padding(16)
            } else if !hasAttemptedLoad || isLoadingItem {
                VStack(spacing: 12) {
                    NoriMark(size: 40, state: .thinking)
                    ProgressView()
                    Text("Loading document…")
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 56)
            } else {
                NoriEmptyState(
                    title: "Document unavailable",
                    message: "This item could not be found in your Library."
                ) {
                    Button("Retry") {
                        reloadTrigger += 1
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                }
            }
        }
    }

    @ViewBuilder
    private func metadataPills(for item: NoriCore.LibraryItem) -> some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                NoriPill(item.provenance.content.displayLabel, tone: .accent)
                if item.version > 1 {
                    NoriPill("Version \(item.version)", tone: .neutral)
                }
                if !item.createdAt.isEmpty {
                    NoriPill(NoriDateFormatting.shortDateTime(item.createdAt), tone: .neutral)
                }
                if let collection = item.collection, !collection.isEmpty {
                    NoriPill(collection, tone: .neutral)
                }
                if let sizeLabel = formattedSizeLabel(for: item) {
                    NoriPill(sizeLabel, tone: .neutral)
                }
            }
        }
    }

    @ViewBuilder
    private func documentBodySection(for item: NoriCore.LibraryItem) -> some View {
        if let image = decodedImage {
            DocumentImageCard(
                image: image,
                title: item.title,
                filename: displayFilename(for: item),
                canQuickLook: tempFileURL != nil,
                onQuickLook: openQuickLookIfAvailable
            )
        } else if isPDFItem, loadedPayload != nil {
            DocumentPDFCard(
                thumbnail: pdfThumbnail,
                filename: displayFilename(for: item),
                subtitle: pdfSubtitle(for: item),
                canQuickLook: tempFileURL != nil,
                onQuickLook: openQuickLookIfAvailable
            )
        } else if loadedPayload != nil && !isTextPayload {
            DocumentBinaryCard(
                iconName: DocumentFileUtilities.fileIconName(for: effectiveMediaType),
                filename: displayFilename(for: item),
                subtitle: binarySubtitle(for: item),
                canQuickLook: tempFileURL != nil,
                onQuickLook: openQuickLookIfAvailable
            )
        } else if let text = fullTextContent {
            DocumentTextCard(
                text: text,
                isLoadingFull: isLoadingFile && loadedPayload == nil,
                isMonospace: isMonospaceText
            )
        } else if isLoadingFile {
            NoriCard(padding: 24) {
                VStack(spacing: 10) {
                    ProgressView()
                    Text(isImageItem ? "Loading image…" : (isPDFItem ? "Loading PDF…" : "Loading full content…"))
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 20)
            }
        } else if let fileLoadError {
            NoriNotice(
                title: "Couldn’t load file preview",
                detail: fileLoadError,
                tone: .warning,
                systemImage: "exclamationmark.triangle.fill"
            ) {
                Button("Retry") {
                    reloadTrigger += 1
                }
                .buttonStyle(NoriButtonStyle(.neutral, compact: true))
            }
        } else {
            NoriCard(padding: 16) {
                Text("No preview available for this item.")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
            }
        }
    }

    @ViewBuilder
    private func quickActionsRow(for item: NoriCore.LibraryItem) -> some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                if canCopyContent {
                    Button {
                        copyCurrentContent()
                    } label: {
                        Label("Copy", systemImage: "doc.on.doc")
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                }

                actionShareButton(for: item)

                #if canImport(QuickLook)
                if tempFileURL != nil && !isPDFItem && (isImageItem || !isTextPayload) {
                    Button {
                        openQuickLookIfAvailable()
                    } label: {
                        Label("Quick Look", systemImage: "eye")
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                }
                #endif

                if let url = externalLinkURL {
                    Link(destination: url) {
                        Label("Open link", systemImage: "arrow.up.right.square")
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                }
            }
        }
    }

    @ViewBuilder
    private func actionShareButton(for item: NoriCore.LibraryItem) -> some View {
        if prefersFileShare, let fileURL = tempFileURL {
            ShareLink(item: fileURL, subject: Text(item.title)) {
                Label("Share", systemImage: "square.and.arrow.up")
            }
            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
        } else {
            ShareLink(item: shareableText(for: item), subject: Text(item.title)) {
                Label("Share", systemImage: "square.and.arrow.up")
            }
            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
        }
    }

    @ViewBuilder
    private func toolbarShareButton(for item: NoriCore.LibraryItem) -> some View {
        if prefersFileShare, let fileURL = tempFileURL {
            ShareLink(item: fileURL, subject: Text(item.title)) {
                shareToolbarIcon
            }
        } else {
            ShareLink(item: shareableText(for: item), subject: Text(item.title)) {
                shareToolbarIcon
            }
        }
    }

    private var shareToolbarIcon: some View {
        Image(systemName: "square.and.arrow.up")
            .font(.system(size: 14, weight: .semibold))
            .foregroundStyle(NoriColors.ink)
            .frame(width: 32, height: 32)
            .background(NoriColors.fill1, in: Circle())
    }

    @ViewBuilder
    private func revisionSection(for item: NoriCore.LibraryItem) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            NoriSectionHeader("Ask Nori to revise")

            if let queued = queuedRevision, queued.isQueued {
                NoriCard(
                    padding: 12,
                    backgroundColor: NoriColors.accentSubtle,
                    borderColor: NoriColors.accentBorder
                ) {
                    VStack(alignment: .leading, spacing: 10) {
                        HStack(alignment: .top, spacing: 10) {
                            Image(systemName: "sparkles")
                                .font(.system(size: 15, weight: .semibold))
                                .foregroundStyle(NoriColors.accent)
                                .padding(.top, 1)
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Revision started")
                                    .font(.system(size: 13.5, weight: .semibold))
                                    .foregroundStyle(NoriColors.accent)
                                Text("Nori is updating this document in the background.")
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(NoriColors.ink2)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }

                        if let taskID = queued.taskID, !taskID.isEmpty {
                            Button {
                                store.pendingDeepLink = .workDetail(taskID)
                                dismiss()
                            } label: {
                                Label("View task", systemImage: "arrow.up.right")
                            }
                            .buttonStyle(NoriButtonStyle(.primary, compact: true))
                        }
                    }
                }
            }

            HStack(spacing: 8) {
                TextField("Shorten summary, convert to EUR…", text: $revisionInstruction)
                    .textFieldStyle(.plain)
                    .foregroundStyle(NoriColors.ink)
                    .tint(NoriColors.accent)
                    .padding(.horizontal, 12)
                    .frame(height: 40)
                    .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                            .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                    )
                    .onSubmit { submitRevision(for: item) }

                Button {
                    submitRevision(for: item)
                } label: {
                    if isRevising {
                        ProgressView().tint(.white)
                    } else {
                        Text("Revise")
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, compact: true))
                .disabled(revisionInstruction.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isRevising)
            }
        }
    }

    private func loadDocument() async {
        if loadedItemID != itemID {
            loadedItemID = itemID
            hydratedItem = nil
            loadedPayload = nil
            fileLoadError = nil
            decodedImage = nil
            pdfPageCount = nil
            pdfThumbnail = nil
            quickLookURL = nil
            tempFileURL = nil
            tempFileCleanup.clearAll()
            queuedRevision = nil
            hasAttemptedLoad = false
        }

        isLoadingItem = (item == nil)
        fileLoadError = nil

        let fetched = await store.loadLibraryItem(id: itemID)
        guard !Task.isCancelled else { return }

        if let fetched {
            hydratedItem = fetched
        }
        hasAttemptedLoad = true
        isLoadingItem = false

        guard let resolvedItem = self.item else { return }

        isLoadingFile = true
        defer { isLoadingFile = false }

        do {
            let payload = try await store.loadLibraryItemFile(id: resolvedItem.id, format: nil)
            guard !Task.isCancelled else { return }
            loadedPayload = payload
            fileLoadError = nil
            processLoadedPayload(payload, for: resolvedItem)
        } catch {
            guard !Task.isCancelled else { return }
            fileLoadError = error.localizedDescription
        }
    }

    private func processLoadedPayload(_ payload: LibraryFilePayload, for item: NoriCore.LibraryItem) {
        let mime = DocumentFileUtilities.normalizedMediaType(payload.mediaType)
        let effective = (!mime.isEmpty && mime != "application/octet-stream")
            ? mime
            : DocumentFileUtilities.normalizedMediaType(item.mediaType)

        decodedImage = nil
        pdfPageCount = nil
        pdfThumbnail = nil

        if effective.hasPrefix("image/") || item.kind == .screenshot {
            decodedImage = DocumentPlatformImage(data: payload.data)
        }

        #if canImport(PDFKit)
        if effective == "application/pdf" || (payload.filename ?? item.name ?? item.title).lowercased().hasSuffix(".pdf") {
            if let pdf = PDFDocument(data: payload.data) {
                pdfPageCount = pdf.pageCount
                pdfThumbnail = pdf.page(at: 0)?.thumbnail(of: CGSize(width: 720, height: 960), for: .mediaBox)
            }
        }
        #endif

        let needsTempFile = decodedImage != nil
            || effective == "application/pdf"
            || effective.hasPrefix("image/")
            || item.kind == .file
            || item.kind == .screenshot
            || !DocumentFileUtilities.isTextMediaType(effective)

        if needsTempFile, !payload.data.isEmpty {
            writeTempFile(data: payload.data, payloadFilename: payload.filename, mediaType: effective, item: item)
        } else {
            tempFileCleanup.clearAll()
            tempFileURL = nil
        }
    }

    private func writeTempFile(
        data: Data,
        payloadFilename: String?,
        mediaType: String,
        item: NoriCore.LibraryItem
    ) {
        let filename = DocumentFileUtilities.resolvedFilename(
            payloadFilename: payloadFilename,
            itemName: item.name,
            itemTitle: item.title,
            mediaType: mediaType
        )
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("nori-reader-\(UUID().uuidString)", isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let fileURL = dir.appendingPathComponent(filename)
            try data.write(to: fileURL, options: .atomic)
            tempFileCleanup.replace(with: dir)
            tempFileURL = fileURL
        } catch {
            tempFileURL = nil
        }
    }

    private func openQuickLookIfAvailable() {
        #if canImport(QuickLook)
        guard let url = tempFileURL else { return }
        quickLookURL = url
        #endif
    }

    private func submitRevision(for item: NoriCore.LibraryItem) {
        let trimmed = revisionInstruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !isRevising else { return }
        isRevising = true
        Task {
            let result = await store.reviseDocument(itemID: item.id, instruction: trimmed)
            isRevising = false
            guard let result else { return }
            revisionInstruction = ""
            if result.isQueued {
                queuedRevision = result
            } else {
                queuedRevision = nil
                if let updated = result.item {
                    hydratedItem = updated
                }
                reloadTrigger += 1
            }
        }
    }

    private func copyCurrentContent() {
        if let text = fullTextContent {
            DocumentFileUtilities.copyTextToClipboard(text)
            store.showToast("Copied to clipboard")
        } else if let image = decodedImage {
            DocumentFileUtilities.copyImageToClipboard(image)
            store.showToast("Image copied to clipboard")
        } else if let url = externalLinkURL {
            DocumentFileUtilities.copyTextToClipboard(url.absoluteString)
            store.showToast("Link copied to clipboard")
        }
    }

    private func shareableText(for item: NoriCore.LibraryItem) -> String {
        if let text = fullTextContent, !text.isEmpty {
            return "\(item.title)\n\n\(text)"
        }
        if let url = externalLinkURL {
            return "\(item.title)\n\(url.absoluteString)"
        }
        return item.title
    }

    private func displayFilename(for item: NoriCore.LibraryItem) -> String {
        DocumentFileUtilities.resolvedFilename(
            payloadFilename: loadedPayload?.filename,
            itemName: item.name,
            itemTitle: item.title,
            mediaType: effectiveMediaType
        )
    }

    private func formattedSizeLabel(for item: NoriCore.LibraryItem) -> String? {
        let byteCount: Int?
        if let payloadBytes = loadedPayload?.data.count, payloadBytes > 0 {
            byteCount = payloadBytes
        } else if let itemSize = item.size, itemSize > 0 {
            byteCount = itemSize
        } else {
            byteCount = nil
        }
        guard let byteCount else { return nil }
        return ByteCountFormatter.string(fromByteCount: Int64(byteCount), countStyle: .file)
    }

    private func pdfSubtitle(for item: NoriCore.LibraryItem) -> String {
        var parts: [String] = ["PDF"]
        if let count = pdfPageCount, count > 0 {
            parts.append(count == 1 ? "1 page" : "\(count) pages")
        }
        if let size = formattedSizeLabel(for: item) {
            parts.append(size)
        }
        return parts.joined(separator: " · ")
    }

    private func binarySubtitle(for item: NoriCore.LibraryItem) -> String {
        var parts: [String] = []
        if !effectiveMediaType.isEmpty {
            parts.append(DocumentFileUtilities.friendlyTypeLabel(for: effectiveMediaType))
        }
        if let size = formattedSizeLabel(for: item) {
            parts.append(size)
        }
        return parts.isEmpty ? "Binary file" : parts.joined(separator: " · ")
    }
}
