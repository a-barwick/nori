import SwiftUI
import NoriCore
import NoriDesign

private let preferenceDayNames = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
private let preferenceDayOrder = [1, 2, 3, 4, 5, 6, 0]

private func humanizeAccountPurpose(_ purpose: String) -> String {
    switch purpose {
    case "calendar.create": return "New events go to"
    case "calendar.read": return "Read events from"
    case "calendar.write": return "Update events in"
    case "mail.send": return "Send mail as"
    case "mail.read": return "Read mail from"
    case "files.read": return "Read files from"
    case "files.write": return "Save files to"
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

public struct PreferencesBudgetsView: View {
    @Bindable private var store: NoriStore
    @State private var showingEditPreferences = false
    @State private var isLoading = false
    @State private var loadError: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var budgetDetailString: String {
        let taskStr = String(format: "$%.2f per task", store.preferences.budgets.taskUSD)
        if let monthly = store.preferences.budgets.monthlyUSD {
            return "\(taskStr) · \(String(format: "$%.2f", monthly)) monthly cap"
        }
        return taskStr
    }

    private var localeDisplay: String {
        let trimmed = store.preferences.locale?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? "Not set" : trimmed
    }

    private var workingHoursSummary: [WorkingHoursSlot] {
        preferenceDayOrder.compactMap { day in
            store.preferences.workingHours.first { $0.day == day }
        }
    }

    private var sortedAccountDefaults: [(purpose: String, connectionID: String)] {
        store.preferences.accountDefaults
            .sorted { $0.key < $1.key }
            .map { (purpose: $0.key, connectionID: $0.value) }
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
                    NoriRows {
                        NoriRow(
                            title: "Time zone",
                            detail: store.preferences.timezone,
                            showDivider: true
                        )

                        NoriRow(
                            title: "Language and region",
                            detail: localeDisplay,
                            showDivider: true
                        )

                        NoriRow(
                            title: "Task budget",
                            detail: budgetDetailString,
                            showDivider: false
                        )
                    }

                    VStack(alignment: .leading, spacing: 0) {
                        NoriSectionHeader("Working hours")
                        if workingHoursSummary.isEmpty {
                            NoriCard {
                                Text("No working hours set.")
                                    .font(NoriTypography.meta)
                                    .foregroundStyle(NoriColors.ink2)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                        } else {
                            NoriRows {
                                ForEach(Array(workingHoursSummary.enumerated()), id: \.element.day) { index, slot in
                                    let dayName = (0..<preferenceDayNames.count).contains(slot.day) ? preferenceDayNames[slot.day] : "Day \(slot.day)"
                                    NoriRow(
                                        title: dayName,
                                        detail: "\(slot.start) – \(slot.end)",
                                        showDivider: index < workingHoursSummary.count - 1
                                    )
                                }
                            }
                        }
                    }

                    if !sortedAccountDefaults.isEmpty {
                        VStack(alignment: .leading, spacing: 0) {
                            NoriSectionHeader("Default accounts")
                            NoriRows {
                                ForEach(Array(sortedAccountDefaults.enumerated()), id: \.element.purpose) { index, entry in
                                    NoriRow(
                                        title: humanizeAccountPurpose(entry.purpose),
                                        detail: connectionLabel(for: entry.connectionID),
                                        showDivider: index < sortedAccountDefaults.count - 1
                                    )
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
        .navigationTitle("Preferences & Budgets")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button("Edit") {
                    showingEditPreferences = true
                }
                .disabled(!store.hasLoadedPreferences)
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(NoriColors.accent)
            }
        }
        .sheet(isPresented: $showingEditPreferences) {
            EditPreferencesSheet(store: store)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
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

private struct EditPreferencesSheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss

    @State private var timezone = ""
    @State private var locale = ""
    @State private var taskBudgetText = ""
    @State private var monthlyCapText = ""
    @State private var workingHoursByDay: [Int: (start: String, end: String)] = [:]
    @State private var accountDefaults: [String: String] = [:]
    @State private var isSaving = false
    @State private var errorMessage: String?

    private var activeConnections: [Connection] {
        store.connections.filter { $0.status != .disconnected }
    }

    private var validationError: String? {
        if timezone.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "Enter a time zone."
        }
        let trimmedTask = taskBudgetText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedTask.isEmpty, let taskVal = Double(trimmedTask), taskVal.isFinite, taskVal >= 0 else {
            return "Enter a valid per-task budget (0 or more)."
        }
        let trimmedMonthly = monthlyCapText.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedMonthly.isEmpty {
            guard let monthlyVal = Double(trimmedMonthly), monthlyVal.isFinite, monthlyVal >= 0 else {
                return "Enter a valid monthly cap (0 or more), or leave blank."
            }
        }
        return nil
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Time zone")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("America/Los_Angeles", text: $timezone)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .autocorrectionDisabled()
                            #if os(iOS)
                            .textInputAutocapitalization(.never)
                            #endif
                            .padding(.horizontal, 12)
                            .frame(height: 40)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Language and region")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("en-US", text: $locale)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .autocorrectionDisabled()
                            #if os(iOS)
                            .textInputAutocapitalization(.never)
                            #endif
                            .padding(.horizontal, 12)
                            .frame(height: 40)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    HStack(spacing: 12) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Per-task budget ($)")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField("1.00", text: $taskBudgetText)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                #if os(iOS)
                                .keyboardType(.decimalPad)
                                #endif
                                .padding(.horizontal, 12)
                                .frame(height: 40)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }

                        VStack(alignment: .leading, spacing: 6) {
                            Text("Monthly cap ($)")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField("No limit", text: $monthlyCapText)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                #if os(iOS)
                                .keyboardType(.decimalPad)
                                #endif
                                .padding(.horizontal, 12)
                                .frame(height: 40)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }
                    }

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Working hours")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)

                        VStack(spacing: 8) {
                            ForEach(preferenceDayOrder, id: \.self) { day in
                                let isEnabled = workingHoursByDay[day] != nil
                                HStack(spacing: 10) {
                                    Toggle(isOn: Binding(
                                        get: { workingHoursByDay[day] != nil },
                                        set: { enabled in
                                            if enabled {
                                                workingHoursByDay[day] = ("09:00", "17:30")
                                            } else {
                                                workingHoursByDay.removeValue(forKey: day)
                                            }
                                        }
                                    )) {
                                        Text(preferenceDayNames[day])
                                            .font(NoriTypography.metaSemibold)
                                            .foregroundStyle(NoriColors.ink)
                                    }
                                    .toggleStyle(.switch)
                                    .tint(NoriColors.brandPrimary)
                                    .frame(width: 100)

                                    if isEnabled {
                                        TextField("09:00", text: Binding(
                                            get: { workingHoursByDay[day]?.start ?? "09:00" },
                                            set: { workingHoursByDay[day] = ($0, workingHoursByDay[day]?.end ?? "17:30") }
                                        ))
                                        .textFieldStyle(.plain)
                                        .font(NoriTypography.monoSmall)
                                        .foregroundStyle(NoriColors.ink)
                                        .tint(NoriColors.accent)
                                        .multilineTextAlignment(.center)
                                        .frame(height: 34)
                                        .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))

                                        Text("–")
                                            .foregroundStyle(NoriColors.ink3)

                                        TextField("17:30", text: Binding(
                                            get: { workingHoursByDay[day]?.end ?? "17:30" },
                                            set: { workingHoursByDay[day] = (workingHoursByDay[day]?.start ?? "09:00", $0) }
                                        ))
                                        .textFieldStyle(.plain)
                                        .font(NoriTypography.monoSmall)
                                        .foregroundStyle(NoriColors.ink)
                                        .tint(NoriColors.accent)
                                        .multilineTextAlignment(.center)
                                        .frame(height: 34)
                                        .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                                    } else {
                                        Spacer()
                                    }
                                }
                            }
                        }
                    }

                    if !accountDefaults.isEmpty && !activeConnections.isEmpty {
                        VStack(alignment: .leading, spacing: 8) {
                            Text("Default accounts")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)

                            ForEach(accountDefaults.keys.sorted(), id: \.self) { purpose in
                                HStack {
                                    Text(humanizeAccountPurpose(purpose))
                                        .font(NoriTypography.meta)
                                        .foregroundStyle(NoriColors.ink2)
                                    Spacer()
                                    Picker(humanizeAccountPurpose(purpose), selection: Binding(
                                        get: { accountDefaults[purpose] ?? "" },
                                        set: { accountDefaults[purpose] = $0 }
                                    )) {
                                        ForEach(activeConnections) { conn in
                                            let label = [conn.title, conn.account?.label]
                                                .compactMap { $0 }
                                                .filter { !$0.isEmpty }
                                                .joined(separator: " · ")
                                            Text(label.isEmpty ? conn.id : label).tag(conn.id)
                                        }
                                    }
                                    .pickerStyle(.menu)
                                    .tint(NoriColors.accent)
                                }
                                .padding(.horizontal, 12)
                                .frame(minHeight: 40)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            }
                        }
                    }

                    if let errorMessage {
                        NoriNotice(title: "Could not save preferences", detail: errorMessage, tone: .danger)
                    }

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(NoriColors.disabled)
                        } else {
                            Text("Save preferences")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(isSaving || validationError != nil)
                    .padding(.top, 6)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Preferences")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(NoriColors.ink2)
                        .disabled(isSaving)
                }
            }
            .interactiveDismissDisabled(isSaving)
            .onAppear {
                timezone = store.preferences.timezone
                locale = store.preferences.locale ?? ""
                taskBudgetText = String(format: "%.2f", store.preferences.budgets.taskUSD)
                monthlyCapText = store.preferences.budgets.monthlyUSD.map { String(format: "%.2f", $0) } ?? ""
                workingHoursByDay = Dictionary(
                    uniqueKeysWithValues: store.preferences.workingHours.map { ($0.day, ($0.start, $0.end)) }
                )
                accountDefaults = store.preferences.accountDefaults
            }
        }
    }

    private func save() {
        if let validationError {
            errorMessage = validationError
            return
        }
        isSaving = true
        errorMessage = nil
        var updated = store.preferences
        updated.timezone = timezone.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedLocale = locale.trimmingCharacters(in: .whitespacesAndNewlines)
        updated.locale = trimmedLocale.isEmpty ? nil : trimmedLocale
        if let taskVal = Double(taskBudgetText.trimmingCharacters(in: .whitespacesAndNewlines)), taskVal.isFinite, taskVal >= 0 {
            updated.budgets.taskUSD = taskVal
        }
        let trimmedMonthly = monthlyCapText.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmedMonthly.isEmpty {
            updated.budgets.monthlyUSD = nil
        } else if let monthlyVal = Double(trimmedMonthly), monthlyVal.isFinite, monthlyVal >= 0 {
            updated.budgets.monthlyUSD = monthlyVal
        }
        updated.workingHours = workingHoursByDay
            .map { WorkingHoursSlot(day: $0.key, start: $0.value.start, end: $0.value.end) }
            .sorted { $0.day < $1.day }
        updated.accountDefaults = accountDefaults

        Task {
            do {
                try await store.savePreferences(updated)
                isSaving = false
                dismiss()
            } catch {
                isSaving = false
                errorMessage = error.localizedDescription
            }
        }
    }
}
