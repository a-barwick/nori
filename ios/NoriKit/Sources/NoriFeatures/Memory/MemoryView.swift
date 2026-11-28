import SwiftUI
import NoriCore
import NoriDesign

public enum MemoryTopicGrouping {
    private static let canonicalOrder = [
        "About you",
        "Preferences",
        "Projects",
        "People",
        "Home & Routine",
        "Travel & Food",
    ]

    public static func normalize(_ section: String?) -> String {
        let trimmed = (section ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "About you" }
        switch trimmed.lowercased() {
        case "about", "about you", "profile", "identity", "bio", "background", "user":
            return "About you"
        case "preference", "preferences", "style", "communication", "likes", "dislikes":
            return "Preferences"
        case "project", "projects", "work", "goals", "decisions":
            return "Projects"
        case "people", "person", "contacts", "family", "friends", "relationships", "team":
            return "People"
        case "home", "routine", "routines", "home & routine", "schedule", "health", "habits":
            return "Home & Routine"
        case "travel", "food", "travel & food", "dining", "places", "trips":
            return "Travel & Food"
        default:
            return trimmed.prefix(1).uppercased() + trimmed.dropFirst()
        }
    }

    public static func group(_ facts: [MemoryFact]) -> [(topic: String, items: [MemoryFact])] {
        var buckets: [String: [MemoryFact]] = [:]
        for fact in facts {
            let topic = normalize(fact.section)
            buckets[topic, default: []].append(fact)
        }
        return buckets
            .map { (topic: $0.key, items: $0.value) }
            .sorted { lhs, rhs in
                let il = canonicalOrder.firstIndex(of: lhs.topic)
                let ir = canonicalOrder.firstIndex(of: rhs.topic)
                switch (il, ir) {
                case let (a?, b?): return a < b
                case (_?, nil): return true
                case (nil, _?): return false
                case (nil, nil): return lhs.topic.localizedCaseInsensitiveCompare(rhs.topic) == .orderedAscending
                }
            }
    }
}

public struct MemoryView: View {
    @Bindable private var store: NoriStore

    @State private var searchQuery = ""
    @State private var isSearching = false
    @State private var searchError: String?
    @State private var searchHits: [MemorySearchHit] = []
    @State private var searchResultQuery: String?
    @State private var confirmRowID: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var trimmedQuery: String {
        searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var isSearchActive: Bool {
        !trimmedQuery.isEmpty
    }

    private var topicGroups: [(topic: String, items: [MemoryFact])] {
        MemoryTopicGrouping.group(store.coreMemories)
    }

    private var matchingFacts: [MemoryFact] {
        guard isSearchActive else { return [] }
        let needle = trimmedQuery.lowercased()
        return store.coreMemories.filter { fact in
            fact.text.lowercased().contains(needle)
                || MemoryTopicGrouping.normalize(fact.section).lowercased().contains(needle)
        }
    }

    private var logHits: [MemorySearchHit] {
        guard isSearchActive, searchResultQuery == trimmedQuery else { return [] }
        return searchHits.filter { $0.layer == "log" }
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                searchField

                if let error = store.memoryRefreshError {
                    NoriRetryNotice("Couldn’t load all memory", detail: error) {
                        await store.refreshMemory()
                    }
                }

                if isSearchActive {
                    searchResultsSection
                } else if topicGroups.isEmpty && store.coreMemoriesRead == .pending {
                    NoriLoadingState("Loading memory…")
                } else if topicGroups.isEmpty && store.coreMemoriesRead == .loaded {
                    NoriEmptyState(
                        title: "Nothing remembered yet",
                        message: "Nori will remember helpful details as you chat."
                    )
                } else if !topicGroups.isEmpty {
                    NoriRows {
                        ForEach(Array(topicGroups.enumerated()), id: \.element.topic) { index, group in
                            let isLast = index == topicGroups.count - 1
                            NavigationLink {
                                MemoryTopicDetailView(store: store, topic: group.topic)
                            } label: {
                                NoriRow(
                                    title: group.topic,
                                    showChevron: true,
                                    showDivider: !isLast
                                ) {
                                    Text("\(group.items.count)")
                                        .font(NoriTypography.meta)
                                        .monospacedDigit()
                                        .foregroundStyle(NoriColors.ink3)
                                }
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle("Memory")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .task {
            await store.refreshMemory()
        }
        .task(id: trimmedQuery) {
            await runMemorySearch(query: trimmedQuery)
        }
        .refreshable {
            await store.refreshMemory()
            if isSearchActive {
                await runMemorySearch(query: trimmedQuery, debounce: false)
            }
        }
    }

    private var searchField: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(NoriColors.ink3)
            TextField("Search everything Nori remembers…", text: $searchQuery)
                .textFieldStyle(.plain)
                .font(NoriTypography.body)
                .foregroundStyle(NoriColors.ink)
                .tint(NoriColors.accent)
            if isSearching {
                ProgressView()
                    .controlSize(.small)
                    .tint(NoriColors.accent)
            } else if !searchQuery.isEmpty {
                Button {
                    searchQuery = ""
                    searchHits = []
                    searchError = nil
                    confirmRowID = nil
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(NoriColors.ink3)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear search")
            }
        }
        .padding(.horizontal, 12)
        .frame(height: 42)
        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
    }

    @ViewBuilder
    private var searchResultsSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let searchError {
                NoriRetryNotice("Couldn’t search memory", detail: searchError) {
                    await runMemorySearch(query: trimmedQuery, debounce: false)
                }
            }

            if !matchingFacts.isEmpty {
                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Saved")
                    NoriRows {
                        ForEach(Array(matchingFacts.enumerated()), id: \.element.id) { index, fact in
                            ForgettableMemoryRow(
                                text: fact.text,
                                detail: MemoryTopicGrouping.normalize(fact.section),
                                confirming: confirmRowID == fact.id,
                                showDivider: index < matchingFacts.count - 1,
                                onStartConfirm: { confirmRowID = fact.id },
                                onCancel: { confirmRowID = nil },
                                onConfirm: {
                                    confirmRowID = nil
                                    Task { await store.forgetMemory(memoryID: fact.id) }
                                }
                            )
                        }
                    }
                }
            }

            if !logHits.isEmpty {
                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader("Past chats")
                    NoriRows {
                        ForEach(Array(logHits.enumerated()), id: \.element.id) { index, hit in
                            let rowID = "log:\(hit.id)"
                            let canForget = hit.logDate != nil && hit.entryID != nil
                            ForgettableMemoryRow(
                                text: hit.snippet,
                                detail: hit.logDate,
                                confirming: confirmRowID == rowID,
                                canForget: canForget,
                                showDivider: index < logHits.count - 1,
                                onStartConfirm: { confirmRowID = rowID },
                                onCancel: { confirmRowID = nil },
                                onConfirm: {
                                    confirmRowID = nil
                                    if let date = hit.logDate, let entryID = hit.entryID {
                                        Task {
                                            await store.forgetLogEntry(date: date, entryID: entryID)
                                            await runMemorySearch(query: trimmedQuery, debounce: false)
                                        }
                                    }
                                }
                            )
                        }
                    }
                }
            }

            if matchingFacts.isEmpty && logHits.isEmpty && searchError == nil {
                if isSearching || searchResultQuery != trimmedQuery {
                    NoriLoadingState("Searching memory…")
                } else if store.coreMemoriesRead == .loaded {
                    NoriEmptyState(
                        title: "Nothing matches “\(trimmedQuery)”",
                        message: "Try a different keyword."
                    )
                }
            }
        }
    }

    private func runMemorySearch(query: String, debounce: Bool = true) async {
        guard !query.isEmpty else {
            isSearching = false
            searchError = nil
            searchHits = []
            searchResultQuery = nil
            return
        }
        isSearching = true
        searchError = nil
        if debounce {
            try? await Task.sleep(for: .milliseconds(200))
            if Task.isCancelled { return }
        }
        defer {
            if !Task.isCancelled && query == trimmedQuery {
                isSearching = false
            }
        }
        do {
            let results = try await store.searchMemory(query: query, layer: nil, limit: 20)
            guard !Task.isCancelled, query == trimmedQuery else { return }
            searchHits = results.items
            searchResultQuery = query
        } catch is CancellationError {
            return
        } catch {
            guard !Task.isCancelled, query == trimmedQuery else { return }
            searchError = error.localizedDescription
        }
    }
}

private struct MemoryTopicDetailView: View {
    @Bindable var store: NoriStore
    let topic: String
    @State private var confirmID: String?

    private var facts: [MemoryFact] {
        store.coreMemories.filter { MemoryTopicGrouping.normalize($0.section) == topic }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if facts.isEmpty {
                    NoriEmptyState(
                        title: "Nothing left in \(topic)",
                        message: "Facts Nori learns in this topic will appear here."
                    )
                } else {
                    NoriRows {
                        ForEach(Array(facts.enumerated()), id: \.element.id) { index, fact in
                            ForgettableMemoryRow(
                                text: fact.text,
                                detail: nil,
                                confirming: confirmID == fact.id,
                                showDivider: index < facts.count - 1,
                                onStartConfirm: { confirmID = fact.id },
                                onCancel: { confirmID = nil },
                                onConfirm: {
                                    confirmID = nil
                                    Task { await store.forgetMemory(memoryID: fact.id) }
                                }
                            )
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle(topic)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
    }
}

private struct ForgettableMemoryRow: View {
    let text: String
    let detail: String?
    let confirming: Bool
    var canForget: Bool = true
    let showDivider: Bool
    let onStartConfirm: () -> Void
    let onCancel: () -> Void
    let onConfirm: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 10) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(text)
                        .font(NoriTypography.body)
                        .foregroundStyle(NoriColors.ink)
                        .fixedSize(horizontal: false, vertical: true)
                    if let detail, !detail.isEmpty {
                        Text(detail)
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink3)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                if canForget {
                    if confirming {
                        HStack(spacing: 6) {
                            Button("Forget", action: onConfirm)
                                .buttonStyle(NoriButtonStyle(.danger, compact: true))
                            Button("Keep", action: onCancel)
                                .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        }
                    } else {
                        Button(action: onStartConfirm) {
                            Image(systemName: "xmark")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.ink3)
                                .frame(width: 28, height: 28)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Forget")
                    }
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            if showDivider {
                Rectangle()
                    .fill(NoriColors.hairline)
                    .frame(height: 1)
                    .padding(.leading, 14)
            }
        }
    }
}
