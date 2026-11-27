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
    @State private var activeItemID: String
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
    @State private var versions: [NoriCore.LibraryItem] = []

    @State private var revisionInstruction = ""
    @State private var isRevising = false
    @State private var queuedRevision: LibraryRevisionResult?

    @State private var showRenameAlert = false
    @State private var renameDraft = ""
    @State private var isRenaming = false
    @State private var showDeleteConfirm = false
    @State private var isDeleting = false

    public init(store: NoriStore, itemID: String) {
        self.store = store
        self.itemID = itemID
        _activeItemID = State(initialValue: itemID)
    }

    private struct LoadKey: Equatable {
        let itemID: String
        let trigger: Int
    }

    private var item: NoriCore.LibraryItem? {
        store.libraryItems.first(where: { $0.id == activeItemID }) ?? hydratedItem
    }

    private var hasStructuredData: Bool {
        DocumentStructuredDataCard.canRender(dataKind: item?.dataKind, data: item?.data)
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
        if let cached = store.cachedBodyText(for: activeItemID), !cached.isEmpty {
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

    private var structuredCopyText: String? {
        DocumentStructuredDataCard.copyableText(dataKind: item?.dataKind, data: item?.data)
    }

    private var canCopyContent: Bool {
        structuredCopyText != nil || fullTextContent != nil || decodedImage != nil || externalLinkURL != nil
    }

    public var body: some View {
        NavigationStack {
            contentBody
                .background(NoriColors.sheetBackground)
                .navigationTitle(item?.title ?? "Artifact")
                #if os(iOS)
                .navigationBarTitleDisplayMode(.inline)
                #endif
                .toolbar {
                    if let item {
                        ToolbarItem(placement: .primaryAction) {
                            HStack(spacing: 8) {
                                toolbarShareButton(for: item)
                                Menu {
                                    Button {
                                        renameDraft = item.title
                                        showRenameAlert = true
                                    } label: {
                                        Label("Rename", systemImage: "pencil")
                                    }
                                    Divider()
                                    Button(role: .destructive) {
                                        showDeleteConfirm = true
                                    } label: {
                                        Label("Delete", systemImage: "trash")
                                    }
                                } label: {
                                    Image(systemName: "ellipsis")
                                        .font(.system(size: 14, weight: .semibold))
                                        .foregroundStyle(NoriColors.ink)
                                        .frame(width: 32, height: 32)
                                        .background(NoriColors.fill1, in: Circle())
                                }
                                .accessibilityLabel("Artifact actions")
                            }
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
        .task(id: LoadKey(itemID: activeItemID, trigger: reloadTrigger)) {
            await loadDocument()
        }
        .alert("Rename artifact", isPresented: $showRenameAlert) {
            TextField("Title", text: $renameDraft)
            Button("Save") {
                renameCurrentItem()
            }
            .disabled(renameDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isRenaming)
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            "Delete this artifact?",
            isPresented: $showDeleteConfirm,
            titleVisibility: .visible
        ) {
            Button("Delete this version", role: .destructive) {
                deleteCurrentItem(scope: "version")
            }
            if versions.count > 1 {
                Button("Delete all versions", role: .destructive) {
                    deleteCurrentItem(scope: "all_versions")
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(versions.count > 1 ? "Choose whether to remove only this version or every version." : "This removes the artifact from Nori.")
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
                    if let newerID = item.supersededBy, !newerID.isEmpty {
                        NoriNotice(
                            title: "A newer version exists",
                            tone: .accent,
                            systemImage: "arrow.up.circle.fill"
                        ) {
                            Button("Open latest") {
                                activeItemID = newerID
                            }
                            .buttonStyle(NoriButtonStyle(.tonal, compact: true))
                        }
                    }
                    documentBodySection(for: item)
                    if canCopyContent || externalLinkURL != nil {
                        quickActionsRow(for: item)
                    }
                    if canRevise {
                        revisionSection(for: item)
                    }
                    if versions.count > 1 {
                        versionsSection(current: item)
                    }
                    detailsSection(for: item)
                }
                .padding(16)
            } else if !hasAttemptedLoad || isLoadingItem {
                VStack(spacing: 12) {
                    NoriMark(size: 40, state: .thinking)
                    ProgressView()
                    Text("Loading artifact…")
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 56)
            } else {
                NoriEmptyState(
                    title: "Artifact unavailable",
                    message: "This item could not be found in your Artifacts."
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
    private func documentBodySection(for item: NoriCore.LibraryItem) -> some View {
        if hasStructuredData, let dataKind = item.dataKind, let data = item.data {
            DocumentStructuredDataCard(dataKind: dataKind, data: data)
        } else if let image = decodedImage {
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
            VStack(spacing: 10) {
                ProgressView()
                Text(isImageItem ? "Loading image…" : (isPDFItem ? "Loading PDF…" : "Loading full content…"))
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 28)
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
            Text("No preview available for this item.")
                .font(NoriTypography.meta)
                .foregroundStyle(NoriColors.ink2)
                .padding(.vertical, 8)
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
            NoriSectionHeader("Revise")

            if let queued = queuedRevision, queued.isQueued {
                NoriCard(
                    padding: 12,
                    backgroundColor: NoriColors.accentSubtle,
                    borderColor: NoriColors.cardBorder
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
                                Text("Nori is updating this artifact in the background.")
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
                        ProgressView().tint(NoriColors.onAccent)
                    } else {
                        Text("Revise")
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, compact: true))
                .disabled(revisionInstruction.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isRevising)
            }
        }
    }

    @ViewBuilder
    private func versionsSection(current: NoriCore.LibraryItem) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            NoriSectionHeader("Versions")
            NoriRows {
                ForEach(Array(versions.enumerated()), id: \.element.id) { idx, ver in
                    let isCurrent = ver.id == current.id
                    Button {
                        if !isCurrent {
                            activeItemID = ver.id
                        }
                    } label: {
                        NoriRow(
                            title: ver.title,
                            detail: ver.createdAt.isEmpty ? "Version \(ver.version)" : "Version \(ver.version) · \(NoriDateFormatting.shortDateTime(ver.createdAt))",
                            showChevron: !isCurrent,
                            showDivider: idx < versions.count - 1
                        ) {
                            if isCurrent {
                                Text("Current")
                                    .font(NoriTypography.caption)
                                    .foregroundStyle(NoriColors.accent)
                            }
                        }
                    }
                    .buttonStyle(.plain)
                    .disabled(isCurrent)
                }
            }
        }
    }

    private func detailsSection(for item: NoriCore.LibraryItem) -> some View {
        let sourceURL: URL? = {
            guard let raw = item.provenance.sourceURL,
                  let url = URL(string: raw),
                  url.scheme?.lowercased() == "https" else { return nil }
            return url
        }()

        return VStack(alignment: .leading, spacing: 0) {
            NoriSectionHeader("Details")
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 16, verticalSpacing: 8) {
                detailRow(label: "Origin", value: item.provenance.content.displayLabel)
                if item.version > 1 {
                    detailRow(label: "Version", value: "Version \(item.version)")
                }
                if let collection = item.collection, !collection.isEmpty {
                    detailRow(label: "Collection", value: collection)
                }
                if let sizeLabel = formattedSizeLabel(for: item) {
                    detailRow(label: "Size", value: sizeLabel)
                }
                if !item.createdAt.isEmpty {
                    detailRow(label: "Saved", value: NoriDateFormatting.shortDateTime(item.createdAt))
                }
                if let taskID = item.taskID, !taskID.isEmpty {
                    GridRow {
                        Text("Task")
                            .font(.system(size: 13))
                            .foregroundStyle(NoriColors.ink3)
                        Button("Open task") {
                            store.pendingDeepLink = .workDetail(taskID)
                            dismiss()
                        }
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .buttonStyle(.plain)
                    }
                }
                if let automationID = item.automationID, !automationID.isEmpty {
                    GridRow {
                        Text("Automation")
                            .font(.system(size: 13))
                            .foregroundStyle(NoriColors.ink3)
                        Button("Open automations") {
                            store.pendingDeepLink = .routines
                            dismiss()
                        }
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                        .buttonStyle(.plain)
                    }
                }
                if let sourceURL {
                    GridRow {
                        Text("Source")
                            .font(.system(size: 13))
                            .foregroundStyle(NoriColors.ink3)
                        Link("Open original", destination: sourceURL)
                            .font(.system(size: 13, weight: .medium))
                            .foregroundStyle(NoriColors.accent)
                    }
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

    private struct ProcessedDocumentPayload: @unchecked Sendable {
        let decodedImage: DocumentPlatformImage?
        let pdfPageCount: Int?
        let pdfThumbnail: DocumentPlatformImage?
        let tempDir: URL?
        let tempFileURL: URL?
    }

    private func loadDocument() async {
        if loadedItemID != activeItemID {
            loadedItemID = activeItemID
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

        let initialItem = self.item
        let shouldPreloadFile = initialItem != nil && !hasStructuredData
        isLoadingItem = (initialItem == nil)
        isLoadingFile = shouldPreloadFile
        fileLoadError = nil

        let targetItemID = activeItemID
        async let fetchedTask = store.loadLibraryItem(id: targetItemID)
        async let versionsTask = store.loadLibraryVersions(itemID: targetItemID)
        async let preloadedFileTask: Result<LibraryFilePayload, Error>? = {
            guard shouldPreloadFile else { return nil }
            do {
                return .success(try await store.loadLibraryItemFile(id: targetItemID, format: nil))
            } catch {
                return .failure(error)
            }
        }()

        let fetched = await fetchedTask
        let loadedVersions = await versionsTask
        guard !Task.isCancelled else {
            isLoadingFile = false
            return
        }

        if let fetched {
            hydratedItem = fetched
        }
        if !loadedVersions.isEmpty {
            versions = loadedVersions.sorted { $0.version > $1.version }
        }
        hasAttemptedLoad = true
        isLoadingItem = false

        guard let resolvedItem = self.item else {
            isLoadingFile = false
            return
        }
        if DocumentStructuredDataCard.canRender(dataKind: resolvedItem.dataKind, data: resolvedItem.data) {
            isLoadingFile = false
            return
        }

        isLoadingFile = true
        defer { isLoadingFile = false }

        let fileResult: Result<LibraryFilePayload, Error>
        if let preloaded = await preloadedFileTask {
            fileResult = preloaded
        } else {
            do {
                fileResult = .success(try await store.loadLibraryItemFile(id: resolvedItem.id, format: nil))
            } catch {
                fileResult = .failure(error)
            }
        }
        guard !Task.isCancelled else { return }

        switch fileResult {
        case .success(let payload):
            loadedPayload = payload
            fileLoadError = nil
            let processed = await Task.detached(priority: .userInitiated) {
                Self.processLoadedPayloadOffMain(payload, for: resolvedItem)
            }.value
            guard !Task.isCancelled else {
                if let tempDir = processed.tempDir {
                    try? FileManager.default.removeItem(at: tempDir)
                }
                return
            }
            decodedImage = processed.decodedImage
            pdfPageCount = processed.pdfPageCount
            pdfThumbnail = processed.pdfThumbnail
            if let tempDir = processed.tempDir, let fileURL = processed.tempFileURL {
                tempFileCleanup.replace(with: tempDir)
                tempFileURL = fileURL
            } else {
                tempFileCleanup.clearAll()
                tempFileURL = nil
            }
        case .failure(let error):
            fileLoadError = error.localizedDescription
        }
    }

    private func renameCurrentItem() {
        let trimmed = renameDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, let current = item, !isRenaming else { return }
        isRenaming = true
        Task {
            do {
                let updated = try await store.renameLibraryItem(itemID: current.id, title: trimmed)
                hydratedItem = updated
                if let idx = versions.firstIndex(where: { $0.id == updated.id }) {
                    versions[idx] = updated
                }
            } catch {
                store.showToast(error.localizedDescription)
            }
            isRenaming = false
        }
    }

    private func deleteCurrentItem(scope: String) {
        guard let current = item, !isDeleting else { return }
        isDeleting = true
        Task {
            let deleted = await store.deleteLibraryItem(itemID: current.id, scope: scope)
            isDeleting = false
            if deleted {
                dismiss()
            }
        }
    }

    private nonisolated static func processLoadedPayloadOffMain(
        _ payload: LibraryFilePayload,
        for item: NoriCore.LibraryItem
    ) -> ProcessedDocumentPayload {
        let mime = DocumentFileUtilities.normalizedMediaType(payload.mediaType)
        let effective = (!mime.isEmpty && mime != "application/octet-stream")
            ? mime
            : DocumentFileUtilities.normalizedMediaType(item.mediaType)

        var image: DocumentPlatformImage?
        var pageCount: Int?
        var thumbnail: DocumentPlatformImage?

        if effective.hasPrefix("image/") || item.kind == .screenshot {
            image = DocumentPlatformImage(data: payload.data)
        }

        #if canImport(PDFKit)
        if effective == "application/pdf" || (payload.filename ?? item.name ?? item.title).lowercased().hasSuffix(".pdf") {
            if let pdf = PDFDocument(data: payload.data) {
                pageCount = pdf.pageCount
                thumbnail = pdf.page(at: 0)?.thumbnail(of: CGSize(width: 720, height: 960), for: .mediaBox)
            }
        }
        #endif

        let needsTempFile = image != nil
            || effective == "application/pdf"
            || effective.hasPrefix("image/")
            || item.kind == .file
            || item.kind == .screenshot
            || !DocumentFileUtilities.isTextMediaType(effective)

        var tempDir: URL?
        var fileURL: URL?
        if needsTempFile, !payload.data.isEmpty {
            let written = writeTempFileOffMain(
                data: payload.data,
                payloadFilename: payload.filename,
                mediaType: effective,
                item: item
            )
            tempDir = written?.dir
            fileURL = written?.fileURL
        }

        return ProcessedDocumentPayload(
            decodedImage: image,
            pdfPageCount: pageCount,
            pdfThumbnail: thumbnail,
            tempDir: tempDir,
            tempFileURL: fileURL
        )
    }

    private nonisolated static func writeTempFileOffMain(
        data: Data,
        payloadFilename: String?,
        mediaType: String,
        item: NoriCore.LibraryItem
    ) -> (dir: URL, fileURL: URL)? {
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
            return (dir: dir, fileURL: fileURL)
        } catch {
            return nil
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
        if let structured = structuredCopyText {
            DocumentFileUtilities.copyTextToClipboard(structured)
            store.showToast("Copied to clipboard")
        } else if let text = fullTextContent {
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
        if let structured = structuredCopyText, !structured.isEmpty {
            return "\(item.title)\n\n\(structured)"
        }
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
