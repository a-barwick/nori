import SwiftUI
import NoriCore
import NoriDesign

private let preferenceDayNames = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
private let preferenceDayOrder = [1, 2, 3, 4, 5, 6, 0]

private func humanizeAccountPurpose(_ purpose: String) -> String {
    switch purpose {
    case "calendar.create": return "New events"
    case "calendar.read": return "Read events"
    case "calendar.write": return "Update events"
    case "mail.send": return "Send mail"
    case "mail.read": return "Read mail"
    case "files.read": return "Read files"
    case "files.write": return "Save files"
    default:
        let words = purpose
            .replacingOccurrences(of: ".", with: " ")
            .replacingOccurrences(of: "_", with: " ")
            .replacingOccurrences(of: "-", with: " ")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard let first = words.first else { return purpose }
        return String(first).uppercased() + words.dropFirst()
    }
}

private func brandForPurpose(_ purpose: String) -> NoriBrandKind {
    if purpose.hasPrefix("calendar") { return .googleCalendar }
    if purpose.hasPrefix("mail") { return .gmail }
    if purpose.hasPrefix("files") { return .googleDrive }
    return .customApp
}

public struct PreferencesBudgetsView: View {
    @Bindable private var store: NoriStore
    @State private var editingField: RegionField?
    @State private var draftValue = ""
    @State private var isSaving = false
    @State private var isLoading = false
    @State private var loadError: String?

    private enum RegionField: Hashable {
        case timezone
        case locale
    }

    public init(store: NoriStore) {
        self.store = store
    }

    private var localeDisplay: String {
        let trimmed = store.preferences.locale?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? "Not set" : trimmed
    }

    private var workingHoursSummaryText: String {
        let ordered = preferenceDayOrder.compactMap { day in
            store.preferences.workingHours.first { $0.day == day }
        }
        guard !ordered.isEmpty else { return "Not set" }
        let sameTimes = ordered.allSatisfy { $0.start == ordered[0].start && $0.end == ordered[0].end }
        let days = ordered.map(\.day)
        let dayLabel = days == [1, 2, 3, 4, 5] ? "Mon – Fri" : "\(ordered.count) days"
        return sameTimes ? "\(dayLabel) · \(ordered[0].start)–\(ordered[0].end)" : dayLabel
    }

    private var sortedAccountDefaults: [(purpose: String, connectionID: String)] {
        store.preferences.accountDefaults
            .sorted { $0.key < $1.key }
            .map { (purpose: $0.key, connectionID: $0.value) }
    }

    private var activeConnections: [Connection] {
        store.connections.filter { $0.status != .disconnected }
    }

    private func connectionLabel(for id: String) -> String {
        guard let connection = store.connections.first(where: { $0.id == id }) else {
            return id
        }
        return [connection.title, connection.account?.label]
            .compactMap { $0 }
            .filter { !$0.isEmpty }
            .joined(separator: " · ")
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let loadError {
                    NoriNotice(title: "Could not load preferences", detail: loadError, tone: .danger) {
                        Button("Retry") {
                            Task { await loadPreferences() }
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    }
                }

                if isLoading && !store.hasLoadedPreferences {
                    NoriCard {
                        HStack(spacing: 10) {
                            ProgressView().tint(NoriColors.accent)
                            Text("Loading preferences…")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                } else if store.hasLoadedPreferences {
                    VStack(alignment: .leading, spacing: 0) {
                        NoriSectionHeader("Region")
                        NoriRows {
                            regionRow(
                                field: .timezone,
                                title: "Time zone",
                                displayValue: store.preferences.timezone,
                                placeholder: "America/Los_Angeles",
                                showDivider: true
                            )
                            regionRow(
                                field: .locale,
                                title: "Language & region",
                                displayValue: localeDisplay,
                                placeholder: "en-US",
                                showDivider: false
                            )
                        }
                    }

                    VStack(alignment: .leading, spacing: 0) {
                        NoriSectionHeader("Schedule")
                        NoriRows {
                            NavigationLink {
                                WorkingHoursEditorView(store: store)
                            } label: {
                                NoriRow(title: "Working hours", showChevron: true, showDivider: false) {
                                    Text(workingHoursSummaryText)
                                        .font(NoriTypography.meta)
                                        .monospacedDigit()
                                        .foregroundStyle(NoriColors.ink2)
                                }
                            }
                            .buttonStyle(.plain)
                        }
                    }

                    if !sortedAccountDefaults.isEmpty {
                        VStack(alignment: .leading, spacing: 0) {
                            NoriSectionHeader("Default accounts")
                            NoriRows {
                                ForEach(Array(sortedAccountDefaults.enumerated()), id: \.element.purpose) { index, entry in
                                    NoriBrandRow(
                                        title: humanizeAccountPurpose(entry.purpose),
                                        brand: brandForPurpose(entry.purpose),
                                        showDivider: index < sortedAccountDefaults.count - 1
                                    ) {
                                        if activeConnections.count > 1 {
                                            Picker(
                                                humanizeAccountPurpose(entry.purpose),
                                                selection: Binding(
                                                    get: { store.preferences.accountDefaults[entry.purpose] ?? entry.connectionID },
                                                    set: { nextID in saveAccountDefault(purpose: entry.purpose, connectionID: nextID) }
                                                )
                                            ) {
                                                ForEach(activeConnections) { conn in
                                                    Text(connectionLabel(for: conn.id)).tag(conn.id)
                                                }
                                            }
                                            .pickerStyle(.menu)
                                            .tint(NoriColors.accent)
                                        } else {
                                            Text(connectionLabel(for: entry.connectionID))
                                                .font(NoriTypography.meta)
                                                .foregroundStyle(NoriColors.ink2)
                                        }
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
        .task {
            if !store.hasLoadedPreferences {
                await loadPreferences()
            }
        }
        .refreshable {
            await loadPreferences()
        }
        .background(NoriColors.canvas)
        .navigationTitle("Preferences")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
    }

    @ViewBuilder
    private func regionRow(
        field: RegionField,
        title: String,
        displayValue: String,
        placeholder: String,
        showDivider: Bool
    ) -> some View {
        if editingField == field {
            VStack(spacing: 0) {
                HStack(spacing: 8) {
                    Text(title)
                        .font(NoriTypography.rowTitle)
                        .foregroundStyle(NoriColors.ink)
                    Spacer(minLength: 4)
                    TextField(placeholder, text: $draftValue)
                        .textFieldStyle(.plain)
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink)
                        .tint(NoriColors.accent)
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .textInputAutocapitalization(.never)
                        #endif
                        .frame(width: 140)
                        .padding(.horizontal, 10)
                        .frame(height: 34)
                        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                    Button("Save") { saveRegion(field) }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                        .disabled(isSaving || (field == .timezone && draftValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty))
                    Button("Cancel") { editingField = nil }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        .disabled(isSaving)
                }
                .padding(.horizontal, 13)
                .padding(.vertical, 10)

                if showDivider {
                    Rectangle()
                        .fill(NoriColors.hairline)
                        .frame(height: 1)
                        .padding(.leading, 13)
                }
            }
        } else {
            Button {
                editingField = field
                draftValue = field == .timezone ? store.preferences.timezone : (store.preferences.locale ?? "")
            } label: {
                NoriRow(title: title, showChevron: true, showDivider: showDivider) {
                    Text(displayValue)
                        .font(NoriTypography.meta)
                        .foregroundStyle(NoriColors.ink2)
                }
            }
            .buttonStyle(.plain)
        }
    }

    private func saveRegion(_ field: RegionField) {
        let trimmed = draftValue.trimmingCharacters(in: .whitespacesAndNewlines)
        if field == .timezone && trimmed.isEmpty { return }
        var updated = store.preferences
        switch field {
        case .timezone: updated.timezone = trimmed
        case .locale: updated.locale = trimmed.isEmpty ? nil : trimmed
        }
        isSaving = true
        Task {
            defer { isSaving = false }
            await store.updatePreferences(updated)
            editingField = nil
        }
    }

    private func saveAccountDefault(purpose: String, connectionID: String) {
        var updated = store.preferences
        updated.accountDefaults[purpose] = connectionID
        Task { await store.updatePreferences(updated) }
    }

    private func loadPreferences() async {
        isLoading = true
        loadError = nil
        defer { isLoading = false }
        do {
            try await store.refreshPreferencesThrowing()
        } catch is CancellationError {
            return
        } catch {
            loadError = error.localizedDescription
        }
    }
}

private struct WorkingHoursEditorView: View {
    @Bindable var store: NoriStore
    @State private var hoursByDay: [Int: (start: String, end: String)] = [:]
    @State private var isSaving = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                NoriRows {
                    ForEach(Array(preferenceDayOrder.enumerated()), id: \.element) { index, day in
                        let isLast = index == preferenceDayOrder.count - 1
                        let slot = hoursByDay[day]
                        VStack(spacing: 0) {
                            HStack(spacing: 10) {
                                Toggle(
                                    preferenceDayNames[day],
                                    isOn: Binding(
                                        get: { hoursByDay[day] != nil },
                                        set: { on in
                                            if on {
                                                hoursByDay[day] = ("09:00", "17:30")
                                            } else {
                                                hoursByDay.removeValue(forKey: day)
                                            }
                                            commit()
                                        }
                                    )
                                )
                                .labelsHidden()
                                .tint(NoriColors.brandPrimary)
                                .disabled(isSaving)

                                Text(preferenceDayNames[day])
                                    .font(NoriTypography.rowTitle)
                                    .foregroundStyle(NoriColors.ink)

                                Spacer(minLength: 8)

                                if slot != nil {
                                    TextField("09:00", text: Binding(
                                        get: { hoursByDay[day]?.start ?? "09:00" },
                                        set: { hoursByDay[day] = ($0, hoursByDay[day]?.end ?? "17:30") }
                                    ))
                                    .onSubmit { commit() }
                                    .textFieldStyle(.plain)
                                    .font(NoriTypography.monoSmall)
                                    .foregroundStyle(NoriColors.ink)
                                    .tint(NoriColors.accent)
                                    .multilineTextAlignment(.center)
                                    .frame(width: 64, height: 32)
                                    .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))

                                    Text("–").foregroundStyle(NoriColors.ink3)

                                    TextField("17:30", text: Binding(
                                        get: { hoursByDay[day]?.end ?? "17:30" },
                                        set: { hoursByDay[day] = (hoursByDay[day]?.start ?? "09:00", $0) }
                                    ))
                                    .onSubmit { commit() }
                                    .textFieldStyle(.plain)
                                    .font(NoriTypography.monoSmall)
                                    .foregroundStyle(NoriColors.ink)
                                    .tint(NoriColors.accent)
                                    .multilineTextAlignment(.center)
                                    .frame(width: 64, height: 32)
                                    .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                                } else {
                                    Text("Off")
                                        .font(NoriTypography.meta)
                                        .foregroundStyle(NoriColors.ink3)
                                }
                            }
                            .padding(.horizontal, 13)
                            .padding(.vertical, 11)

                            if !isLast {
                                Rectangle()
                                    .fill(NoriColors.hairline)
                                    .frame(height: 1)
                                    .padding(.leading, 13)
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
        .navigationTitle("Working hours")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .onAppear {
            hoursByDay = Dictionary(
                uniqueKeysWithValues: store.preferences.workingHours.map { ($0.day, ($0.start, $0.end)) }
            )
        }
        .onDisappear {
            commit()
        }
    }

    private func commit() {
        let slots = hoursByDay
            .map { WorkingHoursSlot(day: $0.key, start: $0.value.start, end: $0.value.end) }
            .sorted { $0.day < $1.day }
        guard slots != store.preferences.workingHours else { return }
        var updated = store.preferences
        updated.workingHours = slots
        isSaving = true
        Task {
            defer { isSaving = false }
            await store.updatePreferences(updated)
        }
    }
}
