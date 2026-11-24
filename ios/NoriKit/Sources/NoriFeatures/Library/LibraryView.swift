import SwiftUI
import NoriCore
import NoriDesign

public enum LibraryFilterKind: String, CaseIterable, Hashable, Sendable {
    case all
    case documents
    case files
    case events

    public var label: String {
        switch self {
        case .all: return "All"
        case .documents: return "Documents"
        case .files: return "Files"
        case .events: return "Events"
        }
    }
}

public struct LibraryView: View {
    @Bindable private var store: NoriStore
    private let onOpenDocument: (NoriCore.LibraryItem) -> Void
    private let onSelectSearchHit: (SearchHit) -> Void

    @State private var query = ""
    @State private var selectedKind: LibraryFilterKind = .all
    @State private var isSearching = false

    public init(
        store: NoriStore,
        onOpenDocument: @escaping (NoriCore.LibraryItem) -> Void,
        onSelectSearchHit: @escaping (SearchHit) -> Void
    ) {
        self.store = store
        self.onOpenDocument = onOpenDocument
        self.onSelectSearchHit = onSelectSearchHit
    }

    private var trimmedQuery: String {
        query.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var filteredItems: [NoriCore.LibraryItem] {
        store.libraryItems.filter { item in
            selectedKind == .all || item.kind.filterGroup == selectedKind.rawValue
        }
    }

    private var groupedCollections: [(name: String?, items: [NoriCore.LibraryItem])] {
        let collections = Array(
            NSOrderedSet(array: filteredItems.compactMap(\.collection))
        ).compactMap { $0 as? String }

        if collections.isEmpty {
            return filteredItems.isEmpty ? [] : [(nil, filteredItems)]
        }

        var groups: [(String?, [NoriCore.LibraryItem])] = []
        for col in collections {
            let matching = filteredItems.filter { $0.collection == col }
            if !matching.isEmpty {
                groups.append((col, matching))
            }
        }
        let ungrouped = filteredItems.filter { $0.collection == nil }
        if !ungrouped.isEmpty {
            groups.append(("Other", ungrouped))
        }
        return groups
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                if trimmedQuery.isEmpty {
                    NoriSegmentedTabs(
                        options: LibraryFilterKind.allCases.map { kind in
                            NoriSegmentOption(value: kind, label: kind.label)
                        },
                        selection: $selectedKind
                    )

                    if groupedCollections.isEmpty {
                        NoriEmptyState(
                            title: "Nothing saved yet",
                            message: "Documents Nori creates, files you upload, and saved events appear here."
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
                                                detail: "\(item.provenance.content.displayLabel) · \(item.createdAt)",
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
                } else {
                    NoriSectionHeader("Universal matches across Nori") {
                        if isSearching {
                            ProgressView()
                                .controlSize(.mini)
                        } else {
                            Text("\(store.searchHits.count)")
                                .font(NoriTypography.caption)
                                .foregroundStyle(NoriColors.ink3)
                        }
                    }

                    if !isSearching && store.searchHits.isEmpty {
                        NoriEmptyState(
                            title: "No matches",
                            message: "Nothing across chats, tasks, library, memory, or goals matches “\(trimmedQuery)”."
                        )
                    } else {
                        NoriRows {
                            ForEach(Array(store.searchHits.enumerated()), id: \.element.id) { idx, hit in
                                let isLast = idx == store.searchHits.count - 1
                                Button {
                                    onSelectSearchHit(hit)
                                } label: {
                                    NoriRow(
                                        title: hit.title,
                                        detail: hitDetail(for: hit),
                                        systemImage: searchHitIcon(for: hit.kind),
                                        iconTone: searchHitTone(for: hit.kind),
                                        showChevron: true,
                                        showDivider: !isLast
                                    ) {
                                        NoriPill(hit.kind.displayLabel, tone: searchHitTone(for: hit.kind))
                                    }
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 6)
            .padding(.bottom, 28)
        }
        .background(NoriColors.canvas)
        .navigationTitle("Library")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.large)
        #endif
        .searchable(
            text: $query,
            prompt: "Search chats, tasks, saved, memory…"
        )
        .task(id: query) {
            let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty else {
                isSearching = false
                store.searchHits = []
                return
            }
            isSearching = true
            try? await Task.sleep(for: .milliseconds(180))
            guard !Task.isCancelled else { return }
            await store.searchLibrary(query: trimmed)
            if !Task.isCancelled {
                isSearching = false
            }
        }
        .refreshable {
            await store.refreshAll()
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

    private func searchHitIcon(for kind: SearchHit.Kind) -> String {
        switch kind {
        case .result: return "doc.text"
        case .message: return "bubble.left"
        case .task: return "checklist"
        case .goal: return "target"
        case .automation: return "repeat"
        case .file: return "paperclip"
        case .memory: return "brain"
        }
    }

    private func searchHitTone(for kind: SearchHit.Kind) -> NoriTone {
        switch kind {
        case .result, .memory: return .accent
        case .goal: return .warning
        case .automation: return .ok
        default: return .neutral
        }
    }

    private func hitDetail(for hit: SearchHit) -> String {
        if let prov = hit.provenance {
            if hit.snippet.isEmpty { return prov.content.displayLabel }
            return "\(hit.snippet) · \(prov.content.displayLabel)"
        }
        return hit.snippet
    }
}
