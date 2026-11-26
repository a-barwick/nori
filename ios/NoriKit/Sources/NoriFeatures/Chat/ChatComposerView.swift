import SwiftUI
import PhotosUI
import UniformTypeIdentifiers
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

struct ChatComposerView: View {
    @Bindable var store: NoriStore

    @State private var draft: String = ""
    @State private var draftAttachments: [DraftAttachment] = []
    @State private var isAttachSheetPresented: Bool = false
    @State private var pendingFileImporterAfterAttachDismiss: Bool = false
    @State private var isFileImporterPresented: Bool = false
    @State private var isSending: Bool = false
    @State private var focusRequestToken: Int = 0
    @FocusState private var isInputFocused: Bool

    private var canSend: Bool {
        let hasText = !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let hasReadyAttachments = draftAttachments.contains { $0.uploaded != nil }
        let anyUploading = draftAttachments.contains { $0.isUploading }
        return (hasText || hasReadyAttachments) && !anyUploading && !isSending && !store.isLoading
    }

    var body: some View {
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
        .simultaneousGesture(
            DragGesture(minimumDistance: 12)
                .onChanged { value in
                    guard isInputFocused else { return }
                    if value.translation.height > 20 && abs(value.translation.height) > abs(value.translation.width) {
                        isInputFocused = false
                    }
                }
        )
        .onAppear {
            consumePendingComposerDraft()
        }
        .onChange(of: store.pendingComposerDraft) { _, _ in
            consumePendingComposerDraft()
        }
        .task(id: focusRequestToken) {
            guard focusRequestToken > 0 else { return }
            await Task.yield()
            guard !Task.isCancelled else { return }
            isInputFocused = true
        }
        .sheet(
            isPresented: $isAttachSheetPresented,
            onDismiss: {
                if pendingFileImporterAfterAttachDismiss {
                    pendingFileImporterAfterAttachDismiss = false
                    isFileImporterPresented = true
                }
            }
        ) {
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
                    pendingFileImporterAfterAttachDismiss = true
                    isAttachSheetPresented = false
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
    }

    private func consumePendingComposerDraft() {
        guard let pending = store.pendingComposerDraft else { return }
        draft = pending
        store.pendingComposerDraft = nil
        focusRequestToken &+= 1
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

    private func triggerLightHaptic() {
        #if os(iOS)
        let generator = UIImpactFeedbackGenerator(style: .light)
        generator.impactOccurred()
        #endif
    }
}

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
                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
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
