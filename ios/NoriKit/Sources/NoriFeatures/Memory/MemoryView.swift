import SwiftUI
import NoriCore
import NoriDesign

public enum MemoryLayerTab: String, CaseIterable, Hashable, Sendable {
    case core
    case logs
    case working
}

private enum MemorySearchLayerFilter: String, CaseIterable, Hashable, Sendable {
    case all
    case logs
    case core

    var label: String {
        switch self {
        case .all: return "All"
        case .logs: return "Logs"
        case .core: return "Core"
        }
    }

    var apiLayer: String? {
        switch self {
        case .all: return nil
        case .logs: return "logs"
        case .core: return "core"
        }
    }
}

private struct MemorySearchKey: Equatable, Sendable {
    let query: String
    let layer: MemorySearchLayerFilter
}

public struct MemoryView: View {
    @Bindable private var store: NoriStore

    @State private var selectedTab: MemoryLayerTab = .core
    @State private var searchQuery = ""
    @State private var searchLayer: MemorySearchLayerFilter = .all
    @State private var isSearching = false
    @State private var searchError: String?
    @State private var searchHits: [MemorySearchHit] = []
    @State private var showingAddSheet = false
    @State private var pendingForgetFact: MemoryFact?
    @State private var pendingPromoteEntry: PromoteCandidate?

    public init(store: NoriStore) {
        self.store = store
    }

    private var trimmedSearchQuery: String {
        searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var isSearchActive: Bool {
        !trimmedSearchQuery.isEmpty
    }

    private var searchKey: MemorySearchKey {
        MemorySearchKey(query: trimmedSearchQuery, layer: searchLayer)
    }

    private var budgetBarColor: Color {
        if store.coreMemoryRatio >= 0.98 { return NoriColors.danger }
        if store.coreMemoryRatio >= 0.85 { return NoriColors.warning }
        return NoriColors.brandPrimary
    }

    private var budgetMetricColor: Color {
        if store.coreMemoryRatio >= 0.98 { return NoriColors.danger }
        if store.coreMemoryRatio >= 0.85 { return NoriColors.warning }
        return NoriColors.ink2
    }

    private var budgetSummaryText: String {
        let used = Double(store.coreMemoryUsedBytes) / 1024.0
        let total = Double(store.coreMemoryBudgetBytes) / 1024.0
        return String(format: "%.1f / %.0f KB", used, total)
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                memorySearchControls
                    .padding(.horizontal, 16)
                    .padding(.top, 12)
                    .padding(.bottom, isSearchActive ? 8 : 4)

                if isSearchActive {
                    memorySearchResultsSection
                        .padding(.horizontal, 16)
                        .padding(.top, 6)
                        .padding(.bottom, 96)
                } else {
                    NoriUnderlineTabs(
                        options: [
                            NoriSegmentOption(value: MemoryLayerTab.core, label: "Core", count: store.coreMemories.count),
                            NoriSegmentOption(value: MemoryLayerTab.logs, label: "Daily Logs", count: totalLogEntries),
                            NoriSegmentOption(value: MemoryLayerTab.working, label: "Working", count: store.workingMemory.items.count)
                        ],
                        selection: $selectedTab
                    )

                    VStack(alignment: .leading, spacing: 14) {
                        if let error = store.memoryRefreshError {
                            Text("Couldn’t refresh all memory. \(error)")
                                .font(.footnote)
                                .foregroundStyle(NoriColors.warning)
                                .accessibilityAddTraits(.updatesFrequently)
                        }
                        switch selectedTab {
                        case .core:
                            coreMemorySection
                        case .logs:
                            dailyLogsSection
                        case .working:
                            workingMemorySection
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.top, 14)
                    .padding(.bottom, 96)
                }
            }
        }
        .background(NoriColors.canvas)
        .navigationTitle("Memory")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button {
                    showingAddSheet = true
                } label: {
                    Image(systemName: "plus")
                        .font(.system(size: 14, weight: .bold))
                        .foregroundStyle(NoriColors.onAccent)
                        .frame(width: 30, height: 30)
                        .background(NoriColors.brandPrimary, in: Circle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Add memory")
            }
        }
        .task {
            await store.refreshMemory()
        }
        .task(id: searchKey) {
            await runMemorySearch(for: searchKey)
        }
        .refreshable {
            await store.refreshMemory()
            if isSearchActive {
                await runMemorySearch(for: searchKey, debounce: false)
            }
        }
        .sheet(isPresented: $showingAddSheet) {
            AddMemorySheet(store: store)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        .sheet(item: $pendingPromoteEntry) { candidate in
            PromoteTaskOutcomeSheet(store: store, candidate: candidate)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        .alert(
            "Forget this memory?",
            isPresented: Binding(
                get: { pendingForgetFact != nil },
                set: { if !$0 { pendingForgetFact = nil } }
            ),
            presenting: pendingForgetFact
        ) { fact in
            Button("Forget", role: .destructive) {
                Task { await store.forgetMemory(memoryID: fact.id) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("Nori stops using this fact in future conversations. Existing chats stay untouched.")
        }
    }

    private var totalLogEntries: Int {
        store.memoryLogs.reduce(0) { $0 + $1.entries.count }
    }

    private var memorySearchControls: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass")
                    .font(.system(size: 14, weight: .medium))
                    .foregroundStyle(NoriColors.ink3)
                TextField("Search all memory…", text: $searchQuery)
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
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(NoriColors.ink3)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Clear search")
                }
            }
            .padding(.horizontal, 12)
            .frame(height: 40)
            .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
            )

            if isSearchActive {
                HStack(spacing: 8) {
                    ForEach(MemorySearchLayerFilter.allCases, id: \.self) { option in
                        let isSelected = searchLayer == option
                        Button {
                            searchLayer = option
                        } label: {
                            Text(option.label)
                                .font(NoriTypography.caption)
                                .foregroundStyle(isSelected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                                .padding(.horizontal, 12)
                                .frame(height: 28)
                                .background(
                                    isSelected ? NoriColors.chipSelectedBg : NoriColors.fill1,
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
                    }
                    Spacer(minLength: 0)
                }
            }
        }
    }

    @ViewBuilder
    private var memorySearchResultsSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let searchError {
                NoriNotice(title: "Could not search memory", detail: searchError, tone: .danger) {
                    Button("Retry") {
                        Task { await runMemorySearch(for: searchKey, debounce: false) }
                    }
                    .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                }
            } else if isSearching && searchHits.isEmpty {
                NoriCard {
                    HStack(spacing: 10) {
                        ProgressView().tint(NoriColors.accent)
                        Text("Searching memory…")
                            .font(NoriTypography.meta)
                            .foregroundStyle(NoriColors.ink2)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
            } else if searchHits.isEmpty {
                NoriEmptyState(
                    title: "No matching memories",
                    message: "Try a different keyword or switch the layer filter."
                ) {
                    Button("Clear search") {
                        searchQuery = ""
                        searchHits = []
                        searchError = nil
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                }
            } else {
                NoriRows {
                    ForEach(Array(searchHits.enumerated()), id: \.element.id) { index, hit in
                        let isLast = index == searchHits.count - 1
                        VStack(alignment: .leading, spacing: 6) {
                            HStack(alignment: .top, spacing: 8) {
                                Text(hit.snippet)
                                    .font(NoriTypography.body)
                                    .foregroundStyle(NoriColors.ink)
                                    .lineSpacing(2)
                                    .fixedSize(horizontal: false, vertical: true)
                                    .frame(maxWidth: .infinity, alignment: .leading)

                                NoriPill(hit.layer == "core" ? "Core" : "Daily Log", tone: hit.layer == "core" ? .working : .neutral)
                            }

                            let meta = searchHitMetadata(for: hit)
                            if !meta.isEmpty {
                                Text(meta)
                                    .font(NoriTypography.caption)
                                    .foregroundStyle(NoriColors.ink2)
                            }
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 12)

                        if !isLast {
                            Rectangle()
                                .fill(NoriColors.hairline)
                                .frame(height: 1)
                        }
                    }
                }
            }
        }
    }

    private func searchHitMetadata(for hit: MemorySearchHit) -> String {
        var parts: [String] = []
        if let section = hit.section, !section.isEmpty {
            parts.append(section)
        }
        parts.append(hit.provenance.displayLabel)
        if let date = hit.logDate, !date.isEmpty {
            parts.append(NoriDateFormatting.shortDate(date))
        } else if !hit.createdAt.isEmpty {
            parts.append(NoriDateFormatting.relative(hit.createdAt))
        }
        return parts.joined(separator: " · ")
    }

    private func runMemorySearch(for key: MemorySearchKey, debounce: Bool = true) async {
        guard !key.query.isEmpty else {
            isSearching = false
            searchError = nil
            searchHits = []
            return
        }
        isSearching = true
        searchError = nil
        if debounce {
            do {
                try await Task.sleep(for: .milliseconds(250))
            } catch {
                return
            }
        }
        guard !Task.isCancelled else { return }
        do {
            let response = try await store.searchMemory(query: key.query, layer: key.layer.apiLayer)
            guard !Task.isCancelled else { return }
            searchHits = response.items
            searchError = nil
        } catch is CancellationError {
            return
        } catch {
            guard !Task.isCancelled else { return }
            searchError = error.localizedDescription
        }
        isSearching = false
    }


    @ViewBuilder
    private var coreMemorySection: some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack(spacing: 8) {
                Text("Core memory budget")
                    .font(.system(size: 12.5, weight: .medium))
                    .foregroundStyle(NoriColors.ink2)
                    .lineLimit(1)
                Spacer(minLength: 8)
                Text(budgetSummaryText)
                    .font(.system(size: 12, weight: .semibold))
                    .monospacedDigit()
                    .foregroundStyle(budgetMetricColor)
                    .lineLimit(1)
            }

            GeometryReader { proxy in
                let ratio = min(max(store.coreMemoryRatio, 0), 1)
                ZStack(alignment: .leading) {
                    Capsule()
                        .fill(NoriColors.fill2)
                    if ratio > 0 {
                        Capsule()
                            .fill(budgetBarColor)
                            .frame(width: max(4, proxy.size.width * ratio))
                    }
                }
            }
            .frame(height: 4)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(NoriColors.surfaceTinted, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )

        if store.coreMemories.isEmpty {
            NoriEmptyState(
                title: "Nothing in Core Memory yet",
                message: "Save durable preferences, decisions, and context you want Nori to always keep in mind."
            ) {
                Button {
                    showingAddSheet = true
                } label: {
                    Label("Add memory", systemImage: "plus")
                }
                .buttonStyle(NoriButtonStyle(.primary, compact: true))
            }
        } else {
            NoriRows {
                ForEach(Array(store.coreMemories.enumerated()), id: \.element.id) { index, fact in
                    let isLast = index == store.coreMemories.count - 1
                    HStack(alignment: .top, spacing: 10) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text(fact.text)
                                .font(.system(size: 14))
                                .foregroundStyle(NoriColors.ink)
                                .lineSpacing(2)
                                .fixedSize(horizontal: false, vertical: true)

                            if let source = fact.sourceText, !source.isEmpty {
                                Text("“\(source)”")
                                    .font(.system(size: 12))
                                    .foregroundStyle(NoriColors.ink2)
                                    .padding(.leading, 8)
                                    .overlay(alignment: .leading) {
                                        Rectangle()
                                            .fill(NoriColors.hairlineStrong)
                                            .frame(width: 2)
                                    }
                            }

                            let metadata = coreMetadata(for: fact)
                            if !metadata.characters.isEmpty {
                                Text(metadata)
                                    .font(.system(size: 12))
                                    .foregroundStyle(NoriColors.ink2)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)

                        coreFactMenu(for: fact)
                    }
                    .padding(.horizontal, 14)
                    .padding(.vertical, 12)

                    if !isLast {
                        Rectangle()
                            .fill(NoriColors.hairline)
                            .frame(height: 1)
                    }
                }
            }
        }
    }


    @ViewBuilder
    private var dailyLogsSection: some View {
        if store.memoryLogs.isEmpty {
            NoriEmptyState(
                title: "No daily logs yet",
                message: "Session notes, auto-saved context, and task outcomes are logged here by day."
            )
        } else {
            ForEach(store.memoryLogs) { dayLog in
                VStack(alignment: .leading, spacing: 0) {
                    NoriSectionHeader(NoriDateFormatting.shortDate(dayLog.date)) {
                        Text("\(dayLog.entries.count) \(dayLog.entries.count == 1 ? "entry" : "entries")")
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink3)
                    }

                    NoriRows {
                        ForEach(Array(dayLog.entries.enumerated()), id: \.element.id) { index, entry in
                            let isLast = index == dayLog.entries.count - 1
                            HStack(alignment: .top, spacing: 10) {
                                VStack(alignment: .leading, spacing: 6) {
                                    Text(entry.text)
                                        .font(.system(size: 14))
                                        .foregroundStyle(NoriColors.ink)
                                        .lineSpacing(2)
                                        .fixedSize(horizontal: false, vertical: true)

                                    let metadata = logMetadata(for: entry)
                                    if !metadata.characters.isEmpty {
                                        Text(metadata)
                                            .font(.system(size: 12))
                                            .foregroundStyle(NoriColors.ink2)
                                            .fixedSize(horizontal: false, vertical: true)
                                    }
                                }
                                .frame(maxWidth: .infinity, alignment: .leading)

                                logEntryMenu(date: dayLog.date, entry: entry)
                            }
                            .padding(.horizontal, 14)
                            .padding(.vertical, 12)

                            if !isLast {
                                Rectangle()
                                    .fill(NoriColors.hairline)
                                    .frame(height: 1)
                            }
                        }
                    }
                }
            }
        }
    }


    @ViewBuilder
    private var workingMemorySection: some View {
        if !store.workingMemory.gist.isEmpty {
            NoriCard(backgroundColor: NoriColors.surfaceTinted, borderColor: NoriColors.cardBorder) {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Active session gist")
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(NoriColors.ink2)
                    Text(store.workingMemory.gist)
                        .font(.system(size: 13.5))
                        .foregroundStyle(NoriColors.ink)
                        .lineSpacing(2)
                }
            }
        }

        if store.workingMemory.items.isEmpty {
            NoriEmptyState(
                title: "Working memory is clear",
                message: "Active constraints, open questions, and commitments from your current thread appear here."
            )
        } else {
            NoriRows {
                ForEach(Array(store.workingMemory.items.enumerated()), id: \.element.id) { index, item in
                    let isLast = index == store.workingMemory.items.count - 1
                    HStack(alignment: .top, spacing: 10) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text(item.quote)
                                .font(.system(size: 14))
                                .foregroundStyle(NoriColors.ink)
                                .lineSpacing(2)
                                .fixedSize(horizontal: false, vertical: true)

                            if let note = item.note, !note.isEmpty {
                                Text(note)
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(NoriColors.ink2)
                                    .fixedSize(horizontal: false, vertical: true)
                            }

                            Text(item.kind.displayLabel)
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(workingKindColor(item.kind))
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)

                        workingItemMenu(for: item)
                    }
                    .padding(.horizontal, 14)
                    .padding(.vertical, 12)

                    if !isLast {
                        Rectangle()
                            .fill(NoriColors.hairline)
                            .frame(height: 1)
                    }
                }
            }
        }
    }


    private func coreMetadata(for fact: MemoryFact) -> AttributedString {
        var result = AttributedString()
        var hasSegment = false

        func append(_ text: String, color: Color = NoriColors.ink2, weight: Font.Weight = .regular) {
            if hasSegment {
                var separator = AttributedString(" · ")
                separator.foregroundColor = NoriColors.ink3
                result.append(separator)
            }
            var segment = AttributedString(text)
            segment.foregroundColor = color
            if weight != .regular {
                segment.font = .system(size: 12, weight: weight)
            }
            result.append(segment)
            hasSegment = true
        }

        if let section = fact.section, !section.isEmpty {
            append(section)
        }
        if let provenance = fact.provenance {
            append(provenance.displayLabel)
        }
        if !fact.createdAt.isEmpty {
            append(NoriDateFormatting.relative(fact.createdAt))
        }
        return result
    }

    private func logMetadata(for entry: MemoryLogEntry) -> AttributedString {
        var result = AttributedString()
        var hasSegment = false

        func append(_ text: String, color: Color = NoriColors.ink2, weight: Font.Weight = .regular) {
            if hasSegment {
                var separator = AttributedString(" · ")
                separator.foregroundColor = NoriColors.ink3
                result.append(separator)
            }
            var segment = AttributedString(text)
            segment.foregroundColor = color
            if weight != .regular {
                segment.font = .system(size: 12, weight: weight)
            }
            result.append(segment)
            hasSegment = true
        }

        if entry.promoted {
            append("In Core", color: NoriColors.ok, weight: .semibold)
        }
        if let section = entry.section, !section.isEmpty {
            append(section)
        }
        append(entry.provenance.displayLabel)
        if !entry.createdAt.isEmpty, NoriDateFormatting.parse(entry.createdAt) != nil {
            append(NoriDateFormatting.shortTime(entry.createdAt))
        }
        return result
    }

    private var rowMenuLabel: some View {
        Image(systemName: "ellipsis")
            .font(.system(size: 13, weight: .semibold))
            .foregroundStyle(NoriColors.ink2)
            .frame(width: 28, height: 28)
            .contentShape(Circle())
    }

    @ViewBuilder
    private func coreFactMenu(for fact: MemoryFact) -> some View {
        Menu {
            Button {
                Task { await store.demoteMemory(memoryID: fact.id) }
            } label: {
                Label("Demote to Daily Logs", systemImage: "arrow.down.to.line")
            }

            Divider()

            Button(role: .destructive) {
                pendingForgetFact = fact
            } label: {
                Label("Forget", systemImage: "trash")
            }
        } label: {
            rowMenuLabel
        }
        .menuIndicator(.hidden)
        .buttonStyle(.plain)
        .accessibilityLabel("Memory actions")
    }

    @ViewBuilder
    private func logEntryMenu(date: String, entry: MemoryLogEntry) -> some View {
        Menu {
            if !entry.promoted {
                Button {
                    if entry.provenance == .taskOutcome {
                        pendingPromoteEntry = PromoteCandidate(date: date, entry: entry)
                    } else {
                        Task {
                            await store.promoteLogEntry(date: date, entryID: entry.id, pinned: false)
                        }
                    }
                } label: {
                    Label("Promote to Core", systemImage: "arrow.up.to.line")
                }

                Divider()
            }

            Button(role: .destructive) {
                Task {
                    await store.forgetLogEntry(date: date, entryID: entry.id)
                }
            } label: {
                Label("Remove from log", systemImage: "trash")
            }
        } label: {
            rowMenuLabel
        }
        .menuIndicator(.hidden)
        .buttonStyle(.plain)
        .accessibilityLabel("Log entry actions")
    }

    @ViewBuilder
    private func workingItemMenu(for item: WorkingMemoryItem) -> some View {
        Menu {
            Button {
                Task { await store.promoteWorkingMemoryItem(itemID: item.id) }
            } label: {
                Label("Save to Core", systemImage: "arrow.up.to.line")
            }

            Divider()

            Button(role: .destructive) {
                Task { await store.dismissWorkingMemoryItem(itemID: item.id) }
            } label: {
                Label("Dismiss", systemImage: "xmark")
            }
        } label: {
            rowMenuLabel
        }
        .menuIndicator(.hidden)
        .buttonStyle(.plain)
        .accessibilityLabel("Working note actions")
    }

    private func workingKindColor(_ kind: WorkingMemoryItemKind) -> Color {
        switch kind {
        case .constraint: return NoriColors.warning
        case .correction: return NoriColors.danger
        case .preference, .commitment: return NoriColors.accent
        case .approval, .decision: return NoriColors.ok
        case .openQuestion, .fact: return NoriColors.ink2
        }
    }
}


private struct PromoteCandidate: Identifiable, Hashable {
    var id: String { "\(date):\(entry.id)" }
    let date: String
    let entry: MemoryLogEntry
}

private struct PromoteTaskOutcomeSheet: View {
    @Bindable var store: NoriStore
    let candidate: PromoteCandidate

    @Environment(\.dismiss) private var dismiss
    @State private var reviewed = false
    @State private var isSaving = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    NoriNotice(
                        title: "Review task outcome before promoting",
                        detail: "Task outcomes come from automated runs. Verify accuracy before loading this into every future chat.",
                        tone: .warning,
                        systemImage: "exclamationmark.shield.fill"
                    )

                    NoriCard {
                        Text(candidate.entry.text)
                            .font(.system(size: 14))
                            .foregroundStyle(NoriColors.ink)
                            .lineSpacing(2)
                    }

                    Toggle(isOn: $reviewed) {
                        Text("I reviewed this fact and want Nori to remember it in Core Memory")
                            .font(.system(size: 13.5, weight: .medium))
                            .foregroundStyle(NoriColors.ink)
                    }
                    .tint(NoriColors.brandPrimary)

                    Button {
                        isSaving = true
                        Task {
                            await store.promoteLogEntry(
                                date: candidate.date,
                                entryID: candidate.entry.id,
                                pinned: false
                            )
                            isSaving = false
                            dismiss()
                        }
                    } label: {
                        if isSaving {
                            ProgressView().tint(.white)
                        } else {
                            Text("Promote to Core Memory")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!reviewed || isSaving)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Promote to Core")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(NoriColors.ink2)
                }
            }
        }
    }
}

private struct AddMemorySheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss

    @State private var factText = ""
    @State private var isSaving = false

    private let maxBytes = 2000

    private var byteCount: Int {
        factText.trimmingCharacters(in: .whitespacesAndNewlines).utf8.count
    }

    private var isValid: Bool {
        byteCount > 0 && byteCount <= maxBytes && !isSaving
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    HStack {
                        Text("What should Nori remember?")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        Spacer()
                        Text("\(byteCount) / \(maxBytes) B")
                            .font(NoriTypography.monoSmall)
                            .foregroundStyle(byteCount > maxBytes ? NoriColors.danger : NoriColors.ink3)
                    }

                    TextField(
                        "Prefers aisle seats and direct morning flights…",
                        text: $factText,
                        axis: .vertical
                    )
                    .lineLimit(3...6)
                    .textFieldStyle(.plain)
                    .foregroundStyle(NoriColors.ink)
                    .tint(NoriColors.accent)
                    .padding(12)
                    .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                            .strokeBorder(byteCount > maxBytes ? NoriColors.danger : NoriColors.cardBorder, lineWidth: 1)
                    )

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(.white)
                        } else {
                            Text("Save")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!isValid)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Add Memory")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(NoriColors.ink2)
                }
            }
        }
    }

    private func save() {
        guard isValid else { return }
        isSaving = true
        Task {
            await store.addMemory(
                text: factText,
                section: nil,
                about: nil,
                pinned: false
            )
            isSaving = false
            dismiss()
        }
    }
}
