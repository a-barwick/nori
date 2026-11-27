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

    private var hasMultipleKinds: Bool {
        let groups = Set(store.libraryItems.map(\.kind.filterGroup))
        return groups.count > 1
    }

    private var filteredSavedItems: [NoriCore.LibraryItem] {
        store.libraryItems.filter { item in
            matchesFilter(item, kind: selectedKind)
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
                    if hasMultipleKinds {
                        filterChipsBar
                    }

                    if groupedCollections.isEmpty {
                        NoriEmptyState(
                            title: "No matching \(selectedKind.label.lowercased())",
                            message: "Try switching the filter to view all saved artifacts."
                        )
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
        let meta: String = item.createdAt.isEmpty
            ? item.provenance.content.displayLabel
            : "\(item.provenance.content.displayLabel) · \(NoriDateFormatting.relative(item.createdAt))"
        if let excerpt = item.excerpt?.trimmingCharacters(in: .whitespacesAndNewlines), !excerpt.isEmpty {
            return "\(excerpt)\n\(meta)"
        }
        return meta
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
