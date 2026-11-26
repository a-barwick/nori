import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UniformTypeIdentifiers)
import UniformTypeIdentifiers
#endif
#if canImport(QuickLook)
import QuickLook
#endif
#if canImport(PDFKit)
import PDFKit
#endif
#if canImport(UIKit)
import UIKit
private typealias PlatformImage = UIImage
#elseif canImport(AppKit)
import AppKit
private typealias PlatformImage = NSImage
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
    @State private var decodedImage: PlatformImage?
    @State private var pdfPageCount: Int?
    @State private var pdfThumbnail: PlatformImage?
    @State private var tempFileURL: URL?
    @State private var quickLookURL: URL?
    @State private var tempFileCleanup = TempFileCleanup()
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
        let payloadType = Self.normalizedMediaType(loadedPayload?.mediaType)
        let itemType = Self.normalizedMediaType(item?.mediaType)
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
        let mime = Self.normalizedMediaType(payload.mediaType)
        if Self.isTextMediaType(mime) { return true }
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
        if let body = item?.bodyText, !body.isEmpty {
            return body
        }
        if let preview = item?.previewText, !preview.isEmpty {
            return preview
        }
        return nil
    }

    private var isMonospaceText: Bool {
        let mime = effectiveMediaType
        if Self.isMonospaceMediaType(mime) { return true }
        let candidateName = (loadedPayload?.filename ?? item?.name ?? item?.title ?? "").lowercased()
        let ext = (candidateName as NSString).pathExtension
        return Self.monospaceExtensions.contains(ext)
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
        if fullTextContent != nil { return true }
        if decodedImage != nil { return true }
        if externalLinkURL != nil { return true }
        return false
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
            imageBodyCard(image: image, item: item)
        } else if isPDFItem, loadedPayload != nil {
            pdfBodyCard(item: item)
        } else if loadedPayload != nil && !isTextPayload {
            binaryBodyCard(item: item)
        } else if let text = fullTextContent {
            textBodyCard(text: text, item: item)
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
    private func textBodyCard(text: String, item: NoriCore.LibraryItem) -> some View {
        NoriCard(padding: 16) {
            VStack(alignment: .leading, spacing: 10) {
                if isLoadingFile && loadedPayload == nil {
                    HStack(spacing: 6) {
                        ProgressView()
                            .controlSize(.mini)
                        Text("Loading full document…")
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink3)
                    }
                }
                if isMonospaceText {
                    ScrollView(.horizontal, showsIndicators: true) {
                        Text(text)
                            .font(NoriTypography.monoSmall)
                            .foregroundStyle(NoriColors.ink)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
        .tint(NoriColors.accent)
                    }
                } else {
                    MarkdownBlocksView(markdown: text)
                        .textSelection(.enabled)
                }
            }
        }
    }

    @ViewBuilder
    private func imageBodyCard(image: PlatformImage, item: NoriCore.LibraryItem) -> some View {
        NoriCard(padding: 12) {
            VStack(alignment: .leading, spacing: 10) {
                Button {
                    openQuickLookIfAvailable()
                } label: {
                    Self.swiftUIImage(from: image)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: .infinity, maxHeight: 460)
                        .clipShape(RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        .overlay(
                            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                .strokeBorder(NoriColors.hairline, lineWidth: 1)
                        )
                }
                .buttonStyle(.plain)
                .disabled(tempFileURL == nil)
                .accessibilityLabel("Preview \(item.title) full screen")

                HStack(spacing: 8) {
                    Text(displayFilename(for: item))
                        .font(NoriTypography.caption)
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(1)
                    Spacer(minLength: 8)
                    #if canImport(QuickLook)
                    if tempFileURL != nil {
                        Button {
                            openQuickLookIfAvailable()
                        } label: {
                            Label("Zoom", systemImage: "arrow.up.left.and.arrow.down.right")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.accent)
                        }
                        .buttonStyle(.plain)
                    }
                    #endif
                }
            }
        }
    }

    @ViewBuilder
    private func pdfBodyCard(item: NoriCore.LibraryItem) -> some View {
        NoriCard(padding: 14) {
            VStack(alignment: .leading, spacing: 12) {
                if let thumbnail = pdfThumbnail {
                    Button {
                        openQuickLookIfAvailable()
                    } label: {
                        Self.swiftUIImage(from: thumbnail)
                            .resizable()
                            .scaledToFit()
                            .frame(maxWidth: .infinity, maxHeight: 380)
                            .background(Color.white)
                            .clipShape(RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    .strokeBorder(NoriColors.hairline, lineWidth: 1)
                            )
                    }
                    .buttonStyle(.plain)
                    .disabled(tempFileURL == nil)
                    .accessibilityLabel("Open PDF preview")
                }

                HStack(spacing: 12) {
                    Image(systemName: "doc.richtext.fill")
                        .font(.system(size: 20, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .frame(width: 42, height: 42)
                        .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 11, style: .continuous))

                    VStack(alignment: .leading, spacing: 2) {
                        Text(displayFilename(for: item))
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(NoriColors.ink)
                            .lineLimit(1)
                        Text(pdfSubtitle(for: item))
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink2)
                    }

                    Spacer(minLength: 8)
                }

                #if canImport(QuickLook)
                if tempFileURL != nil {
                    Button {
                        openQuickLookIfAvailable()
                    } label: {
                        Label("Open PDF", systemImage: "eye")
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                }
                #endif
            }
        }
    }

    @ViewBuilder
    private func binaryBodyCard(item: NoriCore.LibraryItem) -> some View {
        NoriCard(padding: 16) {
            VStack(alignment: .leading, spacing: 14) {
                HStack(spacing: 12) {
                    Image(systemName: fileIconName(for: effectiveMediaType))
                        .font(.system(size: 22, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .frame(width: 46, height: 46)
                        .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 12, style: .continuous))

                    VStack(alignment: .leading, spacing: 3) {
                        Text(displayFilename(for: item))
                            .font(NoriTypography.rowTitle)
                            .foregroundStyle(NoriColors.ink)
                            .lineLimit(2)

                        Text(binarySubtitle(for: item))
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink2)
                    }

                    Spacer(minLength: 8)
                }

                #if canImport(QuickLook)
                if tempFileURL != nil {
                    Button {
                        openQuickLookIfAvailable()
                    } label: {
                        Label("Quick Look", systemImage: "eye")
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                }
                #endif
            }
        }
    }


    @ViewBuilder
    private func quickActionsRow(for item: NoriCore.LibraryItem) -> some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                if canCopyContent {
                    Button {
                        copyCurrentContent(for: item)
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
                    borderColor: NoriColors.accentMuted
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
                            .strokeBorder(NoriColors.hairline, lineWidth: 1)
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
        let mime = Self.normalizedMediaType(payload.mediaType)
        let effective = (!mime.isEmpty && mime != "application/octet-stream")
            ? mime
            : Self.normalizedMediaType(item.mediaType)

        decodedImage = nil
        pdfPageCount = nil
        pdfThumbnail = nil

        if effective.hasPrefix("image/") || item.kind == .screenshot {
            decodedImage = PlatformImage(data: payload.data)
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
            || !Self.isTextMediaType(effective)

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
        let filename = Self.resolvedFilename(
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
            guard let result else {
                return
            }
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

    private func copyCurrentContent(for item: NoriCore.LibraryItem) {
        if let text = fullTextContent {
            copyTextToClipboard(text)
            store.showToast("Copied to clipboard")
        } else if let image = decodedImage {
            copyImageToClipboard(image)
            store.showToast("Image copied to clipboard")
        } else if let url = externalLinkURL {
            copyTextToClipboard(url.absoluteString)
            store.showToast("Link copied to clipboard")
        }
    }

    private func copyTextToClipboard(_ text: String) {
        #if canImport(UIKit)
        UIPasteboard.general.string = text
        #elseif canImport(AppKit)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        #endif
    }

    private func copyImageToClipboard(_ image: PlatformImage) {
        #if canImport(UIKit)
        UIPasteboard.general.image = image
        #elseif canImport(AppKit)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.writeObjects([image])
        #endif
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
        Self.resolvedFilename(
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
            parts.append(Self.friendlyTypeLabel(for: effectiveMediaType))
        }
        if let size = formattedSizeLabel(for: item) {
            parts.append(size)
        }
        return parts.isEmpty ? "Binary file" : parts.joined(separator: " · ")
    }

    private func fileIconName(for mediaType: String) -> String {
        if mediaType == "application/pdf" { return "doc.richtext.fill" }
        if mediaType.hasPrefix("image/") { return "photo" }
        if mediaType.hasPrefix("audio/") { return "waveform" }
        if mediaType.hasPrefix("video/") { return "film" }
        if mediaType.contains("spreadsheet") || mediaType.contains("excel") || mediaType == "text/csv" {
            return "tablecells"
        }
        if mediaType.contains("presentation") || mediaType.contains("powerpoint") {
            return "rectangle.on.rectangle"
        }
        if mediaType.contains("zip") || mediaType.contains("archive") || mediaType.contains("gzip") {
            return "doc.zipper"
        }
        return "doc.fill"
    }

    private static func swiftUIImage(from platformImage: PlatformImage) -> Image {
        #if canImport(UIKit)
        Image(uiImage: platformImage)
        #elseif canImport(AppKit)
        Image(nsImage: platformImage)
        #endif
    }

    private static func normalizedMediaType(_ raw: String?) -> String {
        guard let raw else { return "" }
        return raw
            .split(separator: ";")
            .first?
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased() ?? ""
    }

    private static func isTextMediaType(_ mime: String) -> Bool {
        if mime.hasPrefix("text/") { return true }
        switch mime {
        case "application/json",
             "application/xml",
             "application/javascript",
             "application/typescript",
             "application/yaml",
             "application/x-yaml",
             "application/toml",
             "application/sql",
             "application/x-sh":
            return true
        default:
            return mime.hasSuffix("+json") || mime.hasSuffix("+xml")
        }
    }

    private static func isMonospaceMediaType(_ mime: String) -> Bool {
        switch mime {
        case "text/csv",
             "text/tab-separated-values",
             "text/html",
             "text/css",
             "text/javascript",
             "text/xml",
             "application/json",
             "application/xml",
             "application/javascript",
             "application/typescript",
             "application/yaml",
             "application/x-yaml",
             "application/toml",
             "application/sql",
             "application/x-sh":
            return true
        default:
            return mime.hasPrefix("text/x-") || mime.hasSuffix("+json") || mime.hasSuffix("+xml")
        }
    }

    private static let monospaceExtensions: Set<String> = [
        "csv", "tsv", "json", "jsonl", "xml", "yaml", "yml", "toml",
        "swift", "py", "go", "js", "ts", "tsx", "jsx", "rs", "c", "cpp", "h",
        "java", "kt", "rb", "sh", "zsh", "bash", "sql", "html", "css", "log"
    ]

    private static func friendlyTypeLabel(for mime: String) -> String {
        switch mime {
        case "application/pdf": return "PDF Document"
        case "application/zip": return "ZIP Archive"
        case "application/vnd.openxmlformats-officedocument.wordprocessingml.document": return "Word Document"
        case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": return "Spreadsheet"
        case "application/vnd.openxmlformats-officedocument.presentationml.presentation": return "Presentation"
        case "image/svg+xml": return "SVG Image"
        default:
            #if canImport(UniformTypeIdentifiers)
            if let utType = UTType(mimeType: mime), let description = utType.localizedDescription, !description.isEmpty {
                return description
            }
            #endif
            return mime
        }
    }

    private static func resolvedFilename(
        payloadFilename: String?,
        itemName: String?,
        itemTitle: String,
        mediaType: String
    ) -> String {
        let rawCandidate = [payloadFilename, itemName, itemTitle]
            .compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }
            .first(where: { !$0.isEmpty }) ?? "document"

        let lastComponent = (rawCandidate as NSString).lastPathComponent
        let sanitized = sanitizeFilename(lastComponent)
        let base = sanitized.isEmpty ? "document" : sanitized

        let existingExt = (base as NSString).pathExtension
        if !existingExt.isEmpty {
            return base
        }
        if let ext = preferredExtension(for: mediaType), !ext.isEmpty {
            return "\(base).\(ext)"
        }
        return base
    }

    private static func sanitizeFilename(_ raw: String) -> String {
        let forbidden = CharacterSet(charactersIn: "/\\:*?\"<>|\r\n\t")
        let cleaned = raw.unicodeScalars.map { scalar -> Character in
            if forbidden.contains(scalar) || scalar.value < 32 {
                return "_"
            }
            return Character(scalar)
        }
        let result = String(cleaned)
            .trimmingCharacters(in: CharacterSet(charactersIn: ". "))
        if result.count > 100 {
            return String(result.prefix(100))
        }
        return result
    }

    private static func preferredExtension(for mime: String) -> String? {
        switch mime {
        case "application/pdf": return "pdf"
        case "image/png": return "png"
        case "image/jpeg": return "jpg"
        case "image/webp": return "webp"
        case "image/gif": return "gif"
        case "image/heic": return "heic"
        case "image/svg+xml": return "svg"
        case "text/markdown", "text/x-markdown": return "md"
        case "text/csv": return "csv"
        case "text/plain": return "txt"
        case "text/html": return "html"
        case "application/json": return "json"
        case "application/zip": return "zip"
        case "application/vnd.openxmlformats-officedocument.wordprocessingml.document": return "docx"
        case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": return "xlsx"
        case "application/vnd.openxmlformats-officedocument.presentationml.presentation": return "pptx"
        default:
            #if canImport(UniformTypeIdentifiers)
            return UTType(mimeType: mime)?.preferredFilenameExtension
            #else
            return nil
            #endif
        }
    }
}


private final class TempFileCleanup: @unchecked Sendable {
    private let lock = NSLock()
    private var directories: [URL] = []

    func replace(with newDirectory: URL) {
        let previous: [URL]
        lock.lock()
        previous = directories
        directories = [newDirectory]
        lock.unlock()

        for dir in previous {
            try? FileManager.default.removeItem(at: dir)
        }
    }

    func clearAll() {
        let existing: [URL]
        lock.lock()
        existing = directories
        directories.removeAll()
        lock.unlock()

        for dir in existing {
            try? FileManager.default.removeItem(at: dir)
        }
    }

    deinit {
        clearAll()
    }
}


private struct MarkdownBlocksView: View {
    let markdown: String

    private enum Block: Hashable {
        case spacer
        case heading(level: Int, text: String)
        case bullet(text: String)
        case code(String)
        case rule
        case paragraph(String)
    }

    private var blocks: [Block] {
        var result: [Block] = []
        let lines = markdown.components(separatedBy: .newlines)
        var inCodeFence = false
        var codeLines: [String] = []

        for rawLine in lines {
            let trimmed = rawLine.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("```") {
                if inCodeFence {
                    result.append(.code(codeLines.joined(separator: "\n")))
                    codeLines.removeAll()
                    inCodeFence = false
                } else {
                    inCodeFence = true
                }
                continue
            }

            if inCodeFence {
                codeLines.append(rawLine)
                continue
            }

            if trimmed.isEmpty {
                result.append(.spacer)
            } else if trimmed == "---" || trimmed == "***" || trimmed == "___" {
                result.append(.rule)
            } else if trimmed.hasPrefix("### ") {
                result.append(.heading(level: 3, text: String(trimmed.dropFirst(4))))
            } else if trimmed.hasPrefix("## ") {
                result.append(.heading(level: 2, text: String(trimmed.dropFirst(3))))
            } else if trimmed.hasPrefix("# ") {
                result.append(.heading(level: 1, text: String(trimmed.dropFirst(2))))
            } else if trimmed.hasPrefix("- ") || trimmed.hasPrefix("* ") {
                result.append(.bullet(text: String(trimmed.dropFirst(2))))
            } else {
                result.append(.paragraph(trimmed))
            }
        }

        if inCodeFence && !codeLines.isEmpty {
            result.append(.code(codeLines.joined(separator: "\n")))
        }

        return result
    }

    var body: some View {
        LazyVStack(alignment: .leading, spacing: 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                switch block {
                case .spacer:
                    Spacer().frame(height: 2)
                case .rule:
                    Rectangle()
                        .fill(NoriColors.hairline)
                        .frame(height: 1)
                        .padding(.vertical, 4)
                case .heading(let level, let text):
                    inlineMarkdownText(text)
                        .font(.system(size: level == 1 ? 18 : (level == 2 ? 16.5 : 15), weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                        .padding(.top, level == 1 ? 0 : 4)
                case .bullet(let text):
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text("•")
                            .font(.system(size: 14, weight: .bold))
                            .foregroundStyle(NoriColors.accent)
                        inlineMarkdownText(text)
                            .font(.system(size: 14))
                            .foregroundStyle(NoriColors.ink)
                            .lineSpacing(3)
                    }
                case .code(let code):
                    ScrollView(.horizontal, showsIndicators: false) {
                        Text(code)
                            .font(NoriTypography.monoSmall)
                            .foregroundStyle(NoriColors.ink)
                            .frame(maxWidth: .infinity, alignment: .leading)
        .tint(NoriColors.accent)
                    }
                    .padding(10)
                    .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                            .strokeBorder(NoriColors.hairline, lineWidth: 1)
                    )
                case .paragraph(let text):
                    inlineMarkdownText(text)
                        .font(.system(size: 14))
                        .foregroundStyle(NoriColors.ink)
                        .lineSpacing(3)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .tint(NoriColors.accent)
    }

    private func inlineMarkdownText(_ source: String) -> Text {
        if let attributed = try? AttributedString(markdown: source) {
            return Text(attributed)
        }
        return Text(source)
    }
}
