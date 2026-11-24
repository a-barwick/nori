import SwiftUI
import NoriCore
import NoriDesign
#if os(iOS)
import UIKit
#elseif os(macOS)
import AppKit
#endif

public struct DocumentReaderSheet: View {
    @Bindable private var store: NoriStore
    public let itemID: String

    @Environment(\.dismiss) private var dismiss
    @State private var revisionInstruction = ""
    @State private var isRevising = false

    public init(store: NoriStore, itemID: String) {
        self.store = store
        self.itemID = itemID
    }

    private var item: NoriCore.LibraryItem? {
        store.libraryItems.first(where: { $0.id == itemID })
    }

    private var bodyContent: String {
        if let body = item?.bodyText, !body.isEmpty {
            return body
        }
        if let preview = item?.previewText, !preview.isEmpty {
            return preview
        }
        return "No preview available for this item."
    }

    private var canRevise: Bool {
        guard let item else { return false }
        return item.kind == .document || item.kind == .report || item.kind == .answer
    }

    public var body: some View {
        NavigationStack {
            ScrollView {
                if let item {
                    VStack(alignment: .leading, spacing: 14) {
                        // Provenance, Version & Metadata Pills
                        ScrollView(.horizontal, showsIndicators: false) {
                            HStack(spacing: 6) {
                                NoriPill(item.provenance.content.displayLabel, tone: .accent)
                                if item.version > 1 {
                                    NoriPill("Version \(item.version)", tone: .neutral)
                                }
                                NoriPill(item.createdAt, tone: .neutral)
                                if let collection = item.collection, !collection.isEmpty {
                                    NoriPill(collection, tone: .neutral)
                                }
                            }
                        }

                        // Document Body Card
                        NoriCard(padding: 16) {
                            if item.kind == .file && (item.mediaType == "text/csv" || item.title.hasSuffix(".csv")) {
                                ScrollView(.horizontal, showsIndicators: false) {
                                    Text(bodyContent)
                                        .font(NoriTypography.monoSmall)
                                        .foregroundStyle(NoriColors.ink)
                                        .textSelection(.enabled)
                                }
                            } else {
                                MarkdownBlocksView(markdown: bodyContent)
                                    .textSelection(.enabled)
                            }
                        }

                        // Quick Actions: Copy & Share
                        HStack(spacing: 10) {
                            Button {
                                copyToClipboard(bodyContent)
                                store.showToast("Copied to clipboard")
                            } label: {
                                Label("Copy", systemImage: "doc.on.doc")
                            }
                            .buttonStyle(NoriButtonStyle(.neutral, compact: true))

                            ShareLink(
                                item: "\(item.title)\n\n\(bodyContent)",
                                subject: Text(item.title)
                            ) {
                                Label("Share", systemImage: "square.and.arrow.up")
                            }
                            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                        }

                        // Ask Nori to Revise
                        if canRevise {
                            VStack(alignment: .leading, spacing: 8) {
                                NoriSectionHeader("Ask Nori to revise")

                                HStack(spacing: 8) {
                                    TextField("Shorten summary, convert to EUR…", text: $revisionInstruction)
                                        .textFieldStyle(.plain)
                                        .padding(.horizontal, 12)
                                        .frame(height: 40)
                                        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
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
                    }
                    .padding(16)
                } else {
                    NoriEmptyState(
                        title: "Document unavailable",
                        message: "This item could not be found in your Library."
                    )
                }
            }
            .background(NoriColors.canvas)
            .navigationTitle(item?.title ?? "Document")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                if let item {
                    ToolbarItem(placement: .primaryAction) {
                        ShareLink(
                            item: "\(item.title)\n\n\(bodyContent)",
                            subject: Text(item.title)
                        ) {
                            Image(systemName: "square.and.arrow.up")
                                .font(.system(size: 14, weight: .semibold))
                                .foregroundStyle(NoriColors.ink)
                                .frame(width: 32, height: 32)
                                .background(NoriColors.fill1, in: Circle())
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
    }

    private func submitRevision(for item: NoriCore.LibraryItem) {
        let trimmed = revisionInstruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !isRevising else { return }
        isRevising = true
        Task {
            await store.reviseDocument(itemID: item.id, instruction: trimmed)
            revisionInstruction = ""
            isRevising = false
        }
    }

    private func copyToClipboard(_ text: String) {
        #if os(iOS)
        UIPasteboard.general.string = text
        #elseif os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        #endif
    }
}

// MARK: - Lightweight Markdown Block Renderer

private struct MarkdownBlocksView: View {
    let markdown: String

    private var lines: [String] {
        markdown.components(separatedBy: .newlines)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(lines.enumerated()), id: \.offset) { _, rawLine in
                let line = rawLine.trimmingCharacters(in: .whitespaces)
                if line.isEmpty {
                    Spacer().frame(height: 2)
                } else if line.hasPrefix("### ") {
                    inlineMarkdownText(String(line.dropFirst(4)))
                        .font(.system(size: 15, weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                        .padding(.top, 4)
                } else if line.hasPrefix("## ") {
                    inlineMarkdownText(String(line.dropFirst(3)))
                        .font(.system(size: 16.5, weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                        .padding(.top, 4)
                } else if line.hasPrefix("# ") {
                    inlineMarkdownText(String(line.dropFirst(2)))
                        .font(.system(size: 18, weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                } else if line.hasPrefix("- ") || line.hasPrefix("* ") {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text("•")
                            .font(.system(size: 14, weight: .bold))
                            .foregroundStyle(NoriColors.accent)
                        inlineMarkdownText(String(line.dropFirst(2)))
                            .font(.system(size: 14))
                            .foregroundStyle(NoriColors.ink)
                            .lineSpacing(3)
                    }
                } else {
                    inlineMarkdownText(line)
                        .font(.system(size: 14))
                        .foregroundStyle(NoriColors.ink)
                        .lineSpacing(3)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func inlineMarkdownText(_ source: String) -> Text {
        if let attributed = try? AttributedString(markdown: source) {
            return Text(attributed)
        }
        return Text(source)
    }
}
