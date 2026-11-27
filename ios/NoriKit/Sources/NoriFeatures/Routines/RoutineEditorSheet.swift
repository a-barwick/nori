import SwiftUI
import NoriCore
import NoriDesign

public struct RoutineEditorSheet: View {
    @Bindable private var store: NoriStore
    private let existing: Automation?
    private let onSaved: ((Automation) -> Void)?

    @Environment(\.dismiss) private var dismiss
    @State private var title: String
    @State private var instruction: String
    @State private var scheduleKind: ScheduleKind
    @State private var frequency: RecurrenceFrequency
    @State private var runTime: Date
    @State private var onceDate: Date
    @State private var timezone: String
    @State private var missedRun: MissedRun
    @State private var budgetText: String
    @State private var isSaving = false
    @State private var errorMessage: String?

    private enum ScheduleKind: String, CaseIterable {
        case recurring = "Recurring"
        case once = "Once"
    }

    private enum RecurrenceFrequency: String, CaseIterable {
        case daily = "Every day"
        case weekdays = "Weekdays"
        case weekly = "Weekly"

        var rrule: String {
            switch self {
            case .daily: return "FREQ=DAILY"
            case .weekdays: return "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"
            case .weekly: return "FREQ=WEEKLY"
            }
        }
    }

    public init(store: NoriStore, existing: Automation? = nil, onSaved: ((Automation) -> Void)? = nil) {
        self.store = store
        self.existing = existing
        self.onSaved = onSaved

        _title = State(initialValue: existing?.title ?? "")
        _instruction = State(initialValue: existing?.instruction ?? "")

        let isOnce = existing?.trigger.kind == "once"
        _scheduleKind = State(initialValue: isOnce ? .once : .recurring)

        let initialFreq: RecurrenceFrequency = {
            let rrule = (existing?.trigger.rrule ?? "").uppercased()
            if rrule.contains("MO,TU,WE,TH,FR") { return .weekdays }
            if rrule.contains("FREQ=WEEKLY") { return .weekly }
            return .daily
        }()
        _frequency = State(initialValue: initialFreq)

        let defaultTime: Date = {
            var comps = Calendar.current.dateComponents([.year, .month, .day], from: Date())
            comps.hour = 9
            comps.minute = 0
            if let localStart = existing?.trigger.localStart,
               let tIndex = localStart.firstIndex(of: "T") {
                let hhmm = String(localStart[localStart.index(after: tIndex)...].prefix(5))
                let parts = hhmm.split(separator: ":")
                if parts.count == 2, let h = Int(parts[0]), let m = Int(parts[1]) {
                    comps.hour = h
                    comps.minute = m
                }
            }
            return Calendar.current.date(from: comps) ?? Date()
        }()
        _runTime = State(initialValue: defaultTime)

        let defaultOnce: Date = {
            if let raw = existing?.trigger.at, let parsed = NoriDateFormatting.parse(raw) {
                return parsed
            }
            return Date().addingTimeInterval(3600)
        }()
        _onceDate = State(initialValue: defaultOnce)

        let defaultTZ = existing?.trigger.timezone
            ?? (store.preferences.timezone.isEmpty ? TimeZone.current.identifier : store.preferences.timezone)
        _timezone = State(initialValue: defaultTZ)
        _missedRun = State(initialValue: existing?.missedRun ?? .skip)
        _budgetText = State(initialValue: String(format: "%.2f", existing?.budgetUSD ?? max(0.50, store.preferences.budgets.taskUSD)))
    }

    private var canSave: Bool {
        !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !instruction.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !isSaving
    }

    public var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    if let errorMessage {
                        NoriNotice(
                            title: "Couldn’t save routine",
                            detail: errorMessage,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                    }

                    fieldBlock("Name") {
                        TextField("Morning calendar brief", text: $title)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(.horizontal, 12)
                            .frame(height: 42)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    fieldBlock("What should Nori do?") {
                        TextField("Summarize today’s meetings and flag any conflicts.", text: $instruction, axis: .vertical)
                            .lineLimit(3...6)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(12)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    fieldBlock("Schedule") {
                        Picker("Schedule", selection: $scheduleKind) {
                            ForEach(ScheduleKind.allCases, id: \.self) { kind in
                                Text(kind.rawValue).tag(kind)
                            }
                        }
                        .pickerStyle(.segmented)
                        .tint(NoriColors.accent)

                        if scheduleKind == .once {
                            DatePicker("When", selection: $onceDate, displayedComponents: [.date, .hourAndMinute])
                                .tint(NoriColors.accent)
                                .foregroundStyle(NoriColors.ink)
                                .padding(.top, 4)
                        } else {
                            Picker("Repeat", selection: $frequency) {
                                ForEach(RecurrenceFrequency.allCases, id: \.self) { freq in
                                    Text(freq.rawValue).tag(freq)
                                }
                            }
                            .pickerStyle(.segmented)
                            .tint(NoriColors.accent)
                            .padding(.top, 4)

                            DatePicker("Time", selection: $runTime, displayedComponents: [.hourAndMinute])
                                .tint(NoriColors.accent)
                                .foregroundStyle(NoriColors.ink)
                        }
                    }

                    fieldBlock("If missed") {
                        Picker("If missed", selection: $missedRun) {
                            Text("Skip").tag(MissedRun.skip)
                            Text("Run latest").tag(MissedRun.latest)
                            Text("Run all").tag(MissedRun.all)
                        }
                        .pickerStyle(.segmented)
                        .tint(NoriColors.accent)
                    }

                    fieldBlock("Budget per run (USD)") {
                        TextField("1.00", text: $budgetText)
                            .textFieldStyle(.plain)
                            #if os(iOS)
                            .keyboardType(.decimalPad)
                            #endif
                            .font(NoriTypography.monoSmall)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(.horizontal, 12)
                            .frame(height: 42)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(NoriColors.disabled)
                        } else {
                            Text(existing == nil ? "Create routine" : "Save changes")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!canSave)
                    .padding(.top, 4)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle(existing == nil ? "New Routine" : "Edit Routine")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .interactiveDismissDisabled(isSaving)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(NoriColors.ink2)
                        .disabled(isSaving)
                }
            }
        }
    }

    private func fieldBlock<Content: View>(_ label: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(label)
                .font(NoriTypography.metaSemibold)
                .foregroundStyle(NoriColors.ink)
            content()
        }
    }

    private func save() {
        let cleanTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanInstruction = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanTitle.isEmpty, !cleanInstruction.isEmpty, !isSaving else { return }
        guard let budget = Double(budgetText.trimmingCharacters(in: .whitespacesAndNewlines)), budget >= 0 else {
            errorMessage = "Enter a valid budget amount."
            return
        }

        let trigger: AutomationTrigger
        if scheduleKind == .once {
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime]
            trigger = AutomationTrigger(
                kind: "once",
                at: formatter.string(from: onceDate),
                timezone: timezone
            )
        } else {
            let comps = Calendar.current.dateComponents([.year, .month, .day], from: Date())
            let timeComps = Calendar.current.dateComponents([.hour, .minute], from: runTime)
            let localStart = String(
                format: "%04d-%02d-%02dT%02d:%02d:00",
                comps.year ?? 2026,
                comps.month ?? 1,
                comps.day ?? 1,
                timeComps.hour ?? 9,
                timeComps.minute ?? 0
            )
            trigger = AutomationTrigger(
                kind: "recurring",
                localStart: localStart,
                timezone: timezone,
                rrule: frequency.rrule
            )
        }

        var editable = existing?.editable ?? AutomationEditable(
            title: cleanTitle,
            instruction: cleanInstruction,
            trigger: trigger
        )
        editable.title = cleanTitle
        editable.instruction = cleanInstruction
        editable.trigger = trigger
        editable.missedRun = missedRun
        editable.budgetUSD = budget

        isSaving = true
        errorMessage = nil
        Task {
            do {
                let saved: Automation
                if let existing {
                    saved = try await store.saveAutomation(id: existing.id, value: editable)
                } else {
                    saved = try await store.createAutomation(editable)
                }
                isSaving = false
                onSaved?(saved)
                dismiss()
            } catch {
                isSaving = false
                errorMessage = error.localizedDescription
            }
        }
    }
}
