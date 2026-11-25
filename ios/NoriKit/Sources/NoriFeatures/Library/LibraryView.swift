import SwiftUI
import NoriCore
import NoriDesign

public enum LibraryFilterKind: String, CaseIterable, Hashable, Sendable {
    case all
    case documents
    case files

    public var label: String {
        switch self {
        case .all: return "All"
        case .documents: return "Docs"
        case .files: return "Files"
        }
    }
}

private struct ArtifactMetricChip: Identifiable, Hashable {
    let id: String
    let label: String
    let value: String
}

public struct LibraryView: View {
    @Bindable private var store: NoriStore
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void

    @State private var selectedKind: LibraryFilterKind = .all

    public init(
        store: NoriStore,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void
    ) {
        self.store = store
        self.onOpenDocument = onOpenDocument
    }

    private var featuredItem: NoriCore.LibraryItem? {
        store.libraryItems.first
    }

    private var shouldShowFeatured: Bool {
        guard let featured = featuredItem else { return false }
        return matchesFilter(featured, kind: selectedKind)
    }

    private var hasMultipleKinds: Bool {
        let groups = Set(store.libraryItems.map(\.kind.filterGroup))
        return groups.count > 1
    }

    private var filteredSavedItems: [NoriCore.LibraryItem] {
        let featuredID = shouldShowFeatured ? featuredItem?.id : nil
        return store.libraryItems.filter { item in
            if let featuredID, item.id == featuredID {
                return false
            }
            return matchesFilter(item, kind: selectedKind)
        }
    }

    private var groupedCollections: [(name: String?, items: [NoriCore.LibraryItem])] {
        let items = filteredSavedItems
        let collections = Array(
            NSOrderedSet(array: items.compactMap(\.collection))
        ).compactMap { $0 as? String }

        if collections.isEmpty {
            return items.isEmpty ? [] : [(nil, items)]
        }

        var groups: [(String?, [NoriCore.LibraryItem])] = []
        for col in collections {
            let matching = items.filter { $0.collection == col }
            if !matching.isEmpty {
                groups.append((col, matching))
            }
        }
        let ungrouped = items.filter { $0.collection == nil }
        if !ungrouped.isEmpty {
            groups.append(("Saved", ungrouped))
        }
        return groups
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if store.libraryItems.isEmpty {
                    NoriEmptyState(
                        title: "Nothing saved yet",
                        message: "Documents Nori creates and files you share appear here."
                    )
                } else {
                    if shouldShowFeatured, let featured = featuredItem {
                        featuredArtifactCard(featured)
                    }

                    if hasMultipleKinds {
                        filterChipsBar
                    }

                    if groupedCollections.isEmpty {
                        if !shouldShowFeatured {
                            NoriEmptyState(
                                title: "No matching \(selectedKind.label.lowercased())",
                                message: "Try switching the filter to view all saved artifacts."
                            )
                        }
                    } else {
                        ForEach(Array(groupedCollections.enumerated()), id: \.offset) { _, group in
                            VStack(alignment: .leading, spacing: 0) {
                                NoriSectionHeader(group.name ?? "Saved")
                                NoriRows {
                                    ForEach(Array(group.items.enumerated()), id: \.element.id) { idx, item in
                                        let isLast = idx == group.items.count - 1
                                        Button {
                                            onOpenDocument(item)
                                        } label: {
                                            NoriRow(
                                                title: item.title,
                                                detail: rowDetail(for: item),
                                                systemImage: iconName(for: item),
                                                iconTone: iconTone(for: item),
                                                showChevron: true,
                                                showDivider: !isLast
                                            ) {
                                                if item.version > 1 {
                                                    NoriPill("v\(item.version)", tone: .neutral)
                                                }
                                            }
                                        }
                                        .buttonStyle(.plain)
                                    }
                                }
                            }
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .refreshable {
            await store.refreshLibrary()
        }
    }

    // MARK: - Featured Artifact Card

    private func featuredArtifactCard(_ item: NoriCore.LibraryItem) -> some View {
        let snippet = featuredSnippet(for: item)
        let metrics = extractMetrics(from: item)
        let tags = featuredTags(for: item)

        return Button {
            onOpenDocument(item)
        } label: {
            NoriHeroCard(padding: 16) {
                VStack(alignment: .leading, spacing: 0) {
                    // Attribution header echoing Chat's PageBlockCard
                    HStack(spacing: 6) {
                        NoriMark(size: 16, state: .idle)

                        Text(item.provenance.content.displayLabel)
                            .font(.system(size: 12, weight: .semibold))
                            .foregroundStyle(NoriColors.ink)
                            .lineLimit(1)

                        if !item.createdAt.isEmpty {
                            Text("· \(NoriDateFormatting.relative(item.createdAt))")
                                .font(.system(size: 12))
                                .foregroundStyle(NoriColors.ink2)
                                .lineLimit(1)
                        }

                        Spacer(minLength: 8)

                        if item.version > 1 {
                            Text("v\(item.version)")
                                .font(.system(size: 11, weight: .semibold))
                                .foregroundStyle(NoriColors.accent)
                                .padding(.horizontal, 8)
                                .frame(height: 22)
                                .background(NoriColors.surface, in: Capsule())
                                .overlay(
                                    Capsule()
                                        .strokeBorder(NoriColors.accentBorder, lineWidth: 1)
                                )
                        }

                        Image(systemName: "arrow.up.right")
                            .font(.system(size: 11.5, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                    }
                    .padding(.bottom, 10)

                    Rectangle()
                        .fill(NoriColors.accentBorder.opacity(0.8))
                        .frame(height: 1)
                        .padding(.bottom, 10)

                    Text(item.title)
                        .font(.system(size: 16, weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)

                    if let snippet, !snippet.isEmpty {
                        Text(snippet)
                            .font(.system(size: 13.5))
                            .lineSpacing(2.5)
                            .foregroundStyle(NoriColors.ink2)
                            .lineLimit(3)
                            .multilineTextAlignment(.leading)
                            .padding(.top, 6)
                    }

                    if !metrics.isEmpty {
                        LazyVGrid(
                            columns: [
                                GridItem(.flexible(), spacing: 8),
                                GridItem(.flexible(), spacing: 8)
                            ],
                            spacing: 8
                        ) {
                            ForEach(metrics.prefix(4)) { metric in
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(metric.label)
                                        .font(.system(size: 11))
                                        .foregroundStyle(NoriColors.ink3)
                                        .lineLimit(1)
                                    Text(metric.value)
                                        .font(.system(size: 13, weight: .bold, design: .monospaced))
                                        .foregroundStyle(NoriColors.ink)
                                        .lineLimit(1)
                                }
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .padding(.horizontal, 10)
                                .padding(.vertical, 7)
                                .background(
                                    NoriColors.surface.opacity(0.86),
                                    in: RoundedRectangle(cornerRadius: 10, style: .continuous)
                                )
                                .overlay(
                                    RoundedRectangle(cornerRadius: 10, style: .continuous)
                                        .strokeBorder(NoriColors.accentBorder.opacity(0.75), lineWidth: 1)
                                )
                            }
                        }
                        .padding(.top, 11)
                    } else if !tags.isEmpty {
                        HStack(spacing: 6) {
                            ForEach(tags, id: \.self) { tag in
                                Text(tag)
                                    .font(.system(size: 11.5, weight: .medium))
                                    .foregroundStyle(NoriColors.ink2)
                                    .lineLimit(1)
                                    .padding(.horizontal, 9)
                                    .frame(height: 24)
                                    .background(
                                        NoriColors.surface.opacity(0.86),
                                        in: Capsule()
                                    )
                                    .overlay(
                                        Capsule()
                                            .strokeBorder(NoriColors.accentBorder.opacity(0.75), lineWidth: 1)
                                    )
                            }
                            Spacer(minLength: 0)
                        }
                        .padding(.top, 10)
                    }
                }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    // MARK: - Quiet Filter Chips

    private var filterChipsBar: some View {
        HStack(spacing: 6) {
            ForEach(LibraryFilterKind.allCases, id: \.self) { kind in
                let isSelected = selectedKind == kind
                Button {
                    withAnimation(.easeInOut(duration: 0.16)) {
                        selectedKind = kind
                    }
                } label: {
                    Text(kind.label)
                        .font(.system(size: 12.5, weight: isSelected ? .semibold : .medium))
                        .foregroundStyle(isSelected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                        .padding(.horizontal, 11)
                        .frame(height: 28)
                        .background(
                            isSelected ? NoriColors.chipSelectedBg : NoriColors.elevated,
                            in: Capsule()
                        )
                        .overlay(
                            Capsule()
                                .strokeBorder(
                                    isSelected ? NoriColors.chipSelectedBorder : NoriColors.hairline,
                                    lineWidth: 1
                                )
                        )
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(isSelected ? .isSelected : [])
            }
            Spacer(minLength: 0)
        }
        .padding(.top, 2)
    }

    // MARK: - Helpers

    private func matchesFilter(_ item: NoriCore.LibraryItem, kind: LibraryFilterKind) -> Bool {
        switch kind {
        case .all:
            return true
        case .documents:
            return item.kind.filterGroup == "documents"
        case .files:
            return item.kind.filterGroup == "files"
        }
    }

    private func rowDetail(for item: NoriCore.LibraryItem) -> String {
        if item.createdAt.isEmpty {
            return item.provenance.content.displayLabel
        }
        return "\(item.provenance.content.displayLabel) · \(NoriDateFormatting.relative(item.createdAt))"
    }

    private func featuredSnippet(for item: NoriCore.LibraryItem) -> String? {
        if let preview = item.previewText?.trimmingCharacters(in: .whitespacesAndNewlines), !preview.isEmpty {
            return preview
        }
        if let body = item.bodyText?.trimmingCharacters(in: .whitespacesAndNewlines), !body.isEmpty {
            let cleaned = body
                .components(separatedBy: .newlines)
                .map { $0.trimmingCharacters(in: .whitespaces) }
                .filter { !$0.isEmpty }
                .prefix(3)
                .joined(separator: " ")
            return cleaned.isEmpty ? nil : cleaned
        }
        return nil
    }

    private func extractMetrics(from item: NoriCore.LibraryItem) -> [ArtifactMetricChip] {
        guard let object = item.data?.objectValue,
              case .array(let rawMetrics)? = object["metrics"] else {
            return []
        }

        return rawMetrics.enumerated().compactMap { idx, entry in
            guard let dict = entry.objectValue,
                  let label = dict["label"]?.stringValue,
                  !label.isEmpty else {
                return nil
            }
            let value: String? = {
                switch dict["value"] {
                case .string(let s): return s
                case .int(let i): return "\(i)"
                case .double(let d): return String(format: "%.2f", d)
                case .bool(let b): return b ? "Yes" : "No"
                default: return nil
                }
            }()
            guard let value, !value.isEmpty else { return nil }
            return ArtifactMetricChip(id: "\(idx)-\(label)", label: label, value: value)
        }
    }

    private func featuredTags(for item: NoriCore.LibraryItem) -> [String] {
        var tags: [String] = []
        if let collection = item.collection?.trimmingCharacters(in: .whitespacesAndNewlines), !collection.isEmpty {
            tags.append(collection)
        }
        if let object = item.data?.objectValue,
           case .array(let rawTags)? = object["tags"] {
            for raw in rawTags {
                if let tag = raw.stringValue?.trimmingCharacters(in: .whitespacesAndNewlines),
                   !tag.isEmpty,
                   !tags.contains(tag) {
                    tags.append(tag)
                }
            }
        }
        if let size = item.size, size > 0 {
            tags.append(formattedByteSize(size))
        }
        if tags.isEmpty {
            tags.append(kindDisplayLabel(for: item))
        }
        return Array(tags.prefix(3))
    }

    private func formattedByteSize(_ bytes: Int) -> String {
        if bytes < 1024 {
            return "\(bytes) B"
        }
        let kb = Double(bytes) / 1024.0
        if kb < 1024 {
            return String(format: "%.0f KB", kb)
        }
        return String(format: "%.1f MB", kb / 1024.0)
    }

    private func kindDisplayLabel(for item: NoriCore.LibraryItem) -> String {
        switch item.kind {
        case .document: return "Document"
        case .report: return "Report"
        case .answer: return "Brief"
        case .screenshot: return "Screenshot"
        case .file: return "File"
        case .link: return "Link"
        case .event: return "Event"
        case .message: return "Message"
        }
    }

    private func iconName(for item: NoriCore.LibraryItem) -> String {
        switch item.kind {
        case .document, .report, .answer: return "doc.text"
        case .screenshot: return "photo"
        case .file:
            if item.mediaType?.hasPrefix("image/") == true { return "photo" }
            return "doc"
        case .link: return "link"
        case .event: return "calendar"
        case .message: return "envelope"
        }
    }

    private func iconTone(for item: NoriCore.LibraryItem) -> NoriTone {
        switch item.kind {
        case .document, .report, .answer: return .accent
        case .event: return .ok
        default: return .neutral
        }
    }
}
