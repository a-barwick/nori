import SwiftUI
import NoriCore
import NoriDesign

public enum LibraryFilterKind: String, CaseIterable, Hashable, Sendable {
    case all
    case documents
    case files
    case events
    case messages

    public var label: String {
        switch self {
        case .all: return "All"
        case .documents: return "Docs"
        case .files: return "Files"
        case .events: return "Events"
        case .messages: return "Mail"
        }
    }
}

public struct LibraryView: View {
    @Bindable private var store: NoriStore
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void

    @State private var selectedKind: LibraryFilterKind = .all
    @State private var searchQuery: String = ""

    public init(
        store: NoriStore,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void
    ) {
        self.store = store
        self.onOpenDocument = onOpenDocument
    }

    private var trimmedQuery: String {
        searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var filteredSavedItems: [NoriCore.LibraryItem] {
        let query = trimmedQuery.lowercased()
        return store.libraryItems.filter { item in
            guard matchesFilter(item, kind: selectedKind) else { return false }
            guard !query.isEmpty else { return true }
            let haystack = [item.title, item.name ?? "", item.excerpt ?? "", item.previewText ?? ""]
                .joined(separator: " ")
                .lowercased()
            return haystack.contains(query)
        }
    }

    private var groupedCollections: [(name: String?, items: [NoriCore.LibraryItem])] {
        let items = filteredSavedItems
        guard !items.isEmpty else { return [] }

        var collectionOrder: [String] = []
        var buckets: [String: [NoriCore.LibraryItem]] = [:]
        var ungrouped: [NoriCore.LibraryItem] = []

        for item in items {
            if let col = item.collection {
                if buckets[col] == nil {
                    collectionOrder.append(col)
                }
                buckets[col, default: []].append(item)
            } else {
                ungrouped.append(item)
            }
        }

        if collectionOrder.isEmpty {
            return [(nil, ungrouped)]
        }

        var groups: [(String?, [NoriCore.LibraryItem])] = []
        groups.reserveCapacity(collectionOrder.count + (ungrouped.isEmpty ? 0 : 1))
        for col in collectionOrder {
            if let matching = buckets[col], !matching.isEmpty {
                groups.append((col, matching))
            }
        }
        if !ungrouped.isEmpty {
            groups.append(("Saved", ungrouped))
        }
        return groups
    }

    public var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 14) {
                searchBar
                filterChipsBar

                if store.libraryItems.isEmpty {
                    NoriEmptyState(
                        title: "Nothing saved yet",
                        message: "Documents Nori creates and files you share appear here."
                    )
                } else if groupedCollections.isEmpty {
                    NoriEmptyState(
                        title: trimmedQuery.isEmpty ? "No matching \(selectedKind.label.lowercased())" : "No matching artifacts",
                        message: trimmedQuery.isEmpty
                            ? "Try switching the filter to view all saved artifacts."
                            : "Nothing saved matches “\(trimmedQuery)”."
                    )
                } else {
                    ForEach(Array(groupedCollections.enumerated()), id: \.offset) { _, group in
                        VStack(alignment: .leading, spacing: 0) {
                            if let name = group.name {
                                NoriSectionHeader(name)
                            }
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
                                        )
                                    }
                                    .buttonStyle(.plain)
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

    private var searchBar: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(NoriColors.ink3)

            TextField("Filter artifacts & saved files…", text: $searchQuery)
                .font(.system(size: 14.5))
                .foregroundStyle(NoriColors.ink)
                .tint(NoriColors.accent)
                .textFieldStyle(.plain)

            if !searchQuery.isEmpty {
                Button {
                    searchQuery = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 15))
                        .foregroundStyle(NoriColors.ink3)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear search")
            }
        }
        .padding(.horizontal, 12)
        .frame(height: 40)
        .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
    }

    private var filterChipsBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
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
                            .padding(.horizontal, 12)
                            .frame(height: 30)
                            .background(
                                isSelected ? NoriColors.chipSelectedBg : NoriColors.elevated,
                                in: Capsule()
                            )
                            .overlay(
                                Capsule()
                                    .strokeBorder(
                                        isSelected ? NoriColors.chipSelectedBorder : NoriColors.cardBorder,
                                        lineWidth: 1
                                    )
                            )
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(isSelected ? .isSelected : [])
                }
            }
        }
    }

    private func matchesFilter(_ item: NoriCore.LibraryItem, kind: LibraryFilterKind) -> Bool {
        switch kind {
        case .all:
            return true
        case .documents:
            return item.kind.filterGroup == "documents"
        case .files:
            return item.kind.filterGroup == "files"
        case .events:
            return item.kind.filterGroup == "events"
        case .messages:
            return item.kind.filterGroup == "messages"
        }
    }

    private func rowDetail(for item: NoriCore.LibraryItem) -> String? {
        var metaParts: [String] = []
        if let status = item.provenance.connectionStatus, status != .connected {
            metaParts.append("Account disconnected")
        }
        if !item.createdAt.isEmpty {
            metaParts.append(NoriDateFormatting.relative(item.createdAt))
        }
        let meta = metaParts.joined(separator: " · ")
        if let excerpt = item.excerpt?.trimmingCharacters(in: .whitespacesAndNewlines), !excerpt.isEmpty {
            return meta.isEmpty ? excerpt : "\(excerpt)\n\(meta)"
        }
        return meta.isEmpty ? nil : meta
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
        case .document, .report, .answer, .link: return .accent
        case .event: return .ok
        default: return .neutral
        }
    }
}
