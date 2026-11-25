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

    private var budgetTone: NoriTone {
        if store.coreMemoryRatio >= 0.98 { return .danger }
        if store.coreMemoryRatio >= 0.85 { return .warning }
        return .neutral
    }

    private var budgetBarColor: Color {
        if store.coreMemoryRatio >= 0.98 { return NoriColors.danger }
        if store.coreMemoryRatio >= 0.85 { return NoriColors.warning }
        return NoriColors.accent
    }

    private var budgetPillText: String {
        let used = Double(store.coreMemoryUsedBytes) / 1024.0
        let total = Double(store.coreMemoryBudgetBytes) / 1024.0
        return String(format: "%.1f / %.0f KB", used, total)
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                NoriSegmentedTabs(
                    options: [
                        NoriSegmentOption(value: MemoryLayerTab.core, label: "Core", count: store.coreMemories.count),
                        NoriSegmentOption(value: MemoryLayerTab.logs, label: "Daily Logs", count: totalLogEntries),
                        NoriSegmentOption(value: MemoryLayerTab.working, label: "Working", count: store.workingMemory.items.count)
                    ],
                    selection: $selectedTab
                )

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
            .padding(.top, 8)
            .padding(.bottom, 28)
        }
        .background(NoriColors.canvas)
        .navigationTitle("Memory")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
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
                        .background(NoriColors.accent, in: Circle())
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
        }
        .sheet(item: $pendingPromoteEntry) { candidate in
            PromoteTaskOutcomeSheet(store: store, candidate: candidate)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
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
        // 24 KB Core Budget Card
        NoriCard {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text("Core budget")
                        .font(.system(size: 13.5, weight: .bold))
                        .foregroundStyle(NoriColors.ink)
                    Spacer()
                    NoriPill(budgetPillText, tone: budgetTone)
                }

                GeometryReader { proxy in
                    ZStack(alignment: .leading) {
                        Capsule()
                            .fill(NoriColors.fill2)
                        Capsule()
                            .fill(budgetBarColor)
                            .frame(width: max(6, proxy.size.width * store.coreMemoryRatio))
                    }
                }
                .frame(height: 6)

                Text("\(store.coreMemories.count) facts · \(pinnedCount) pinned · always loaded in chat context")
                    .font(.system(size: 11.5))
                    .foregroundStyle(NoriColors.ink3)
            }
        }

        // Filter bar
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 14, weight: .medium))
                .foregroundStyle(NoriColors.ink3)
            TextField("Filter core memories…", text: $coreFilter)
                .textFieldStyle(.plain)
                .font(NoriTypography.body)
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
                    VStack(alignment: .leading, spacing: 9) {
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

                        // Metadata Pills + Actions (never truncates Pin/Unpin or pills)
                        ViewThatFits(in: .horizontal) {
                            HStack(spacing: 6) {
                                coreFactPills(for: fact)
                                Spacer(minLength: 6)
                                coreFactActions(for: fact)
                            }

                            VStack(alignment: .leading, spacing: 8) {
                                HStack(spacing: 6) {
                                    coreFactPills(for: fact)
                                }
                                HStack(spacing: 6) {
                                    if !fact.createdAt.isEmpty {
                                        Text("Saved \(NoriDateFormatting.shortDate(fact.createdAt))")
                                            .font(.system(size: 11.5))
                                            .foregroundStyle(NoriColors.ink3)
                                            .lineLimit(1)
                                    }
                                    Spacer(minLength: 6)
                                    coreFactActions(for: fact)
                                }
                            }
                        }
                    }
                    .padding(.horizontal, 13)
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
                            VStack(alignment: .leading, spacing: 9) {
                                Text(entry.text)
                                    .font(.system(size: 14))
                                    .foregroundStyle(NoriColors.ink)
                                    .lineSpacing(2)
                                    .fixedSize(horizontal: false, vertical: true)

                                ViewThatFits(in: .horizontal) {
                                    HStack(spacing: 6) {
                                        logEntryPills(for: entry)
                                        Spacer(minLength: 6)
                                        logEntryActions(date: dayLog.date, entry: entry)
                                    }

                                    VStack(alignment: .leading, spacing: 8) {
                                        HStack(spacing: 6) {
                                            logEntryPills(for: entry)
                                        }
                                        HStack(spacing: 6) {
                                            Spacer(minLength: 0)
                                            logEntryActions(date: dayLog.date, entry: entry)
                                        }
                                    }
                                }
                            }
                            .padding(.horizontal, 13)
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
            NoriCard(backgroundColor: NoriColors.sidebar, borderColor: NoriColors.hairline) {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Active session gist")
                        .font(.system(size: 12, weight: .bold))
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
                    VStack(alignment: .leading, spacing: 8) {
                        HStack {
                            NoriPill(item.kind.displayLabel, tone: workingKindTone(item.kind))
                            Spacer()
                            Button("Save to Core") {
                                Task { await store.promoteWorkingMemoryItem(itemID: item.id) }
                            }
                            .font(.system(size: 11.5, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                            .buttonStyle(.plain)

                            Button {
                                Task { await store.dismissWorkingMemoryItem(itemID: item.id) }
                            } label: {
                                Image(systemName: "xmark")
                                    .font(.system(size: 11, weight: .semibold))
                                    .foregroundStyle(NoriColors.ink3)
                                    .frame(width: 24, height: 24)
                                    .background(NoriColors.fill1, in: Circle())
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel("Dismiss working note")
                        }

                        Text(item.quote)
                            .font(.system(size: 14))
                            .foregroundStyle(NoriColors.ink)
                            .lineSpacing(2)

                        if let note = item.note, !note.isEmpty {
                            Text(note)
                                .font(.system(size: 12))
                                .foregroundStyle(NoriColors.ink2)
                        }
                    }
                    .padding(.horizontal, 13)
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

    // MARK: - Row Pill & Action Helpers

    @ViewBuilder
    private func coreFactPills(for fact: MemoryFact) -> some View {
        if fact.pinned {
            NoriPill("Pinned", tone: .accent)
                .fixedSize()
        }
        if let prov = fact.provenance {
            NoriPill(prov.displayLabel, tone: provenanceTone(prov))
                .fixedSize()
        }
        if let section = fact.section, !section.isEmpty {
            NoriPill(section, tone: .neutral)
                .fixedSize()
        }
    }

    @ViewBuilder
    private func coreFactActions(for fact: MemoryFact) -> some View {
        Button {
            Task { await store.togglePinMemory(memoryID: fact.id) }
        } label: {
            Label(fact.pinned ? "Unpin" : "Pin", systemImage: fact.pinned ? "pin.slash" : "pin")
                .font(.system(size: 11.5, weight: .semibold))
                .foregroundStyle(NoriColors.ink2)
                .padding(.horizontal, 9)
                .frame(height: 26)
                .background(NoriColors.fill1, in: Capsule())
        }
        .buttonStyle(.plain)
        .fixedSize()

        Menu {
            Button {
                Task { await store.demoteMemory(memoryID: fact.id) }
            } label: {
                Label("Demote to Daily Logs", systemImage: "arrow.down.to.line")
            }

            Button(role: .destructive) {
                pendingForgetFact = fact
            } label: {
                Label("Forget memory", systemImage: "trash")
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(NoriColors.ink2)
                .frame(width: 26, height: 26)
                .background(NoriColors.fill1, in: Circle())
        }
        .fixedSize()
    }

    @ViewBuilder
    private func logEntryPills(for entry: MemoryLogEntry) -> some View {
        NoriPill(entry.provenance.displayLabel, tone: provenanceTone(entry.provenance))
            .fixedSize()
        if let section = entry.section, !section.isEmpty {
            NoriPill(section, tone: .neutral)
                .fixedSize()
        }
    }

    @ViewBuilder
    private func logEntryActions(date: String, entry: MemoryLogEntry) -> some View {
        if entry.promoted {
            NoriPill("In Core", tone: .ok, systemImage: "checkmark")
                .fixedSize()
        } else {
            Button {
                if entry.provenance == .taskOutcome {
                    pendingPromoteEntry = PromoteCandidate(date: date, entry: entry)
                } else {
                    Task {
                        await store.promoteLogEntry(date: date, entryID: entry.id, pinned: false)
                    }
                }
            } label: {
                Text("Promote to Core")
                    .font(.system(size: 11.5, weight: .semibold))
                    .foregroundStyle(NoriColors.accent)
                    .padding(.horizontal, 10)
                    .frame(height: 26)
                    .background(NoriColors.accentSubtle, in: Capsule())
            }
            .buttonStyle(.plain)
            .fixedSize()
        }

        Menu {
            Button(role: .destructive) {
                Task {
                    await store.forgetLogEntry(date: date, entryID: entry.id)
                }
            } label: {
                Label("Remove from log", systemImage: "trash")
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(NoriColors.ink2)
                .frame(width: 26, height: 26)
                .background(NoriColors.fill1, in: Circle())
        }
        .fixedSize()
    }

    private func provenanceTone(_ provenance: MemoryProvenance) -> NoriTone {
        switch provenance {
        case .userExplicit: return .accent
        case .dreamCurated: return .ok
        case .taskOutcome: return .warning
        case .preCompactFlush, .summaryEvicted: return .neutral
        }
    }

    private func workingKindTone(_ kind: WorkingMemoryItemKind) -> NoriTone {
        switch kind {
        case .constraint: return .warning
        case .correction: return .danger
        case .preference, .commitment: return .accent
        case .approval, .decision: return .ok
        case .openQuestion, .fact: return .neutral
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
                    .tint(NoriColors.accent)

                    Toggle(isOn: $pinInCore) {
                        Text("Pin in Core Memory (never auto-demoted)")
                            .font(.system(size: 13.5))
                            .foregroundStyle(NoriColors.ink2)
                    }
                    .tint(NoriColors.accent)

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
                    .opacity(reviewed ? 1 : 0.5)
                }
                .padding(16)
            }
            .background(NoriColors.canvas)
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
                    .padding(12)
                    .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
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
                                    Button {
                                        section = option
                                    } label: {
                                        Text(option)
                                            .font(.system(size: 12.5, weight: .semibold))
                                            .foregroundStyle(section == option ? Color.white : NoriColors.ink2)
                                            .padding(.horizontal, 11)
                                            .frame(height: 30)
                                            .background(section == option ? NoriColors.ink : NoriColors.fill1, in: Capsule())
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
                            .padding(.horizontal, 12)
                            .frame(height: 40)
                            .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
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
                    .tint(NoriColors.accent)

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
                    .opacity(isValid ? 1 : 0.5)
                }
                .padding(16)
            }
            .background(NoriColors.canvas)
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
