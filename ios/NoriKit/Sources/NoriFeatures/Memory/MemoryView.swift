import SwiftUI
import NoriCore
import NoriDesign

public enum MemoryLayerTab: String, CaseIterable, Hashable, Sendable {
    case core
    case logs
    case working
}

public struct MemoryView: View {
    @Bindable private var store: NoriStore

    @State private var selectedTab: MemoryLayerTab = .core
    @State private var coreFilter = ""
    @State private var showingAddSheet = false
    @State private var pendingForgetFact: MemoryFact?
    @State private var pendingPromoteEntry: PromoteCandidate?

    public init(store: NoriStore) {
        self.store = store
    }

    private var pinnedCount: Int {
        store.coreMemories.filter(\.pinned).count
    }

    private var filteredCoreMemories: [MemoryFact] {
        let needle = coreFilter.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !needle.isEmpty else { return store.coreMemories }
        return store.coreMemories.filter { fact in
            let parts = [
                fact.text,
                fact.section ?? "",
                fact.about ?? "",
                fact.provenance?.displayLabel ?? "",
                fact.sourceText ?? ""
            ]
            return parts.joined(separator: " ").lowercased().contains(needle)
        }
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
                NoriUnderlineTabs(
                    options: [
                        NoriSegmentOption(value: MemoryLayerTab.core, label: "Core", count: store.coreMemories.count),
                        NoriSegmentOption(value: MemoryLayerTab.logs, label: "Daily Logs", count: totalLogEntries),
                        NoriSegmentOption(value: MemoryLayerTab.working, label: "Working", count: store.workingMemory.items.count)
                    ],
                    selection: $selectedTab
                )

                VStack(alignment: .leading, spacing: 14) {
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
        .refreshable {
            await store.refreshMemory()
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

    // MARK: - Core Tab

    @ViewBuilder
    private var coreMemorySection: some View {
        // Slim compact 24 KB Core Budget Strip
        VStack(alignment: .leading, spacing: 7) {
            HStack(spacing: 8) {
                Text("Core budget · \(pinnedCount) pinned")
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
                .strokeBorder(NoriColors.hairline, lineWidth: 1)
        )

        // Filter bar
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(NoriColors.ink3)
            TextField("Filter core memories…", text: $coreFilter)
                .textFieldStyle(.plain)
                .font(NoriTypography.body)
                .foregroundStyle(NoriColors.ink)
                .tint(NoriColors.accent)
            if !coreFilter.isEmpty {
                Button {
                    coreFilter = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(NoriColors.ink3)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 12)
        .frame(height: 40)
        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                .strokeBorder(NoriColors.hairline, lineWidth: 1)
        )

        if filteredCoreMemories.isEmpty {
            NoriEmptyState(
                title: coreFilter.isEmpty ? "Nothing in Core Memory yet" : "No matching memories",
                message: coreFilter.isEmpty
                    ? "Save durable preferences, decisions, and context you want Nori to always keep in mind."
                    : "Try a shorter filter or clear your search."
            ) {
                if !coreFilter.isEmpty {
                    Button("Clear filter") { coreFilter = "" }
                        .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                } else {
                    Button {
                        showingAddSheet = true
                    } label: {
                        Label("Add memory", systemImage: "plus")
                    }
                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                }
            }
        } else {
            NoriRows {
                ForEach(Array(filteredCoreMemories.enumerated()), id: \.element.id) { index, fact in
                    let isLast = index == filteredCoreMemories.count - 1
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

    // MARK: - Daily Logs Tab

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

    // MARK: - Working Tab

    @ViewBuilder
    private var workingMemorySection: some View {
        if !store.workingMemory.gist.isEmpty {
            NoriCard(backgroundColor: NoriColors.surfaceTinted, borderColor: NoriColors.hairline) {
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

    // MARK: - Quiet Metadata & Row Action Menus

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

        if fact.pinned {
            append("Pinned", color: NoriColors.accent, weight: .semibold)
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
                Task { await store.togglePinMemory(memoryID: fact.id) }
            } label: {
                Label(fact.pinned ? "Unpin" : "Pin", systemImage: fact.pinned ? "pin.slash" : "pin")
            }

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

// MARK: - Supporting Sheets

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
    @State private var pinInCore = false
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

                    Toggle(isOn: $pinInCore) {
                        Text("Pin in Core Memory (never auto-demoted)")
                            .font(.system(size: 13.5))
                            .foregroundStyle(NoriColors.ink2)
                    }
                    .tint(NoriColors.brandPrimary)

                    Button {
                        isSaving = true
                        Task {
                            await store.promoteLogEntry(
                                date: candidate.date,
                                entryID: candidate.entry.id,
                                pinned: pinInCore
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
    @State private var section = "Preferences"
    @State private var about = ""
    @State private var pinned = false
    @State private var isSaving = false

    private let maxBytes = 2000
    private let sectionSuggestions = ["Preferences", "People", "Projects", "Decisions", "Context"]

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
                            .strokeBorder(byteCount > maxBytes ? NoriColors.danger : NoriColors.hairline, lineWidth: 1)
                    )

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Section")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)

                        ScrollView(.horizontal, showsIndicators: false) {
                            HStack(spacing: 6) {
                                ForEach(sectionSuggestions, id: \.self) { option in
                                    let isSelected = section == option
                                    Button {
                                        section = option
                                    } label: {
                                        Text(option)
                                            .font(.system(size: 12.5, weight: .semibold))
                                            .foregroundStyle(isSelected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                                            .padding(.horizontal, 11)
                                            .frame(height: 30)
                                            .background(isSelected ? NoriColors.chipSelectedBg : NoriColors.elevated, in: Capsule())
                                            .overlay(
                                                Capsule()
                                                    .strokeBorder(isSelected ? NoriColors.chipSelectedBorder : NoriColors.hairline, lineWidth: 1)
                                            )
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                        }
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("About (optional)")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("travel, procurement, calendar…", text: $about)
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
                    }

                    Toggle(isOn: $pinned) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Pin to Core Memory")
                                .font(.system(size: 14, weight: .medium))
                                .foregroundStyle(NoriColors.ink)
                            Text("Never auto-demoted during memory consolidation.")
                                .font(.system(size: 12))
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }
                    .tint(NoriColors.brandPrimary)

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(.white)
                        } else {
                            Text("Save to Core Memory")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!isValid)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Add Core Memory")
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
                section: section.isEmpty ? nil : section,
                about: about.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? nil : about,
                pinned: pinned
            )
            isSaving = false
            dismiss()
        }
    }
}
