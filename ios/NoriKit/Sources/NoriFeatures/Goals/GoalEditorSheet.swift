import SwiftUI
import NoriCore
import NoriDesign

public struct GoalEditorSheet: View {
    @Bindable private var store: NoriStore
    private let existing: Goal?
    private let onSaved: ((Goal) -> Void)?

    @Environment(\.dismiss) private var dismiss
    @State private var title: String
    @State private var outcome: String
    @State private var criteria: [CriterionDraft]
    @State private var hasDeadline: Bool
    @State private var deadlineDate: Date
    @State private var followUp: String
    @State private var isSaving = false
    @State private var errorMessage: String?

    private struct CriterionDraft: Identifiable {
        var id: UUID { localID }
        let localID = UUID()
        var serverID: String?
        var text: String
    }

    public init(store: NoriStore, existing: Goal? = nil, onSaved: ((Goal) -> Void)? = nil) {
        self.store = store
        self.existing = existing
        self.onSaved = onSaved
        _title = State(initialValue: existing?.title ?? "")
        _outcome = State(initialValue: existing?.outcome ?? "")
        let initialCriteria: [CriterionDraft] = {
            let items = existing?.completionCriteria ?? []
            if items.isEmpty {
                return [CriterionDraft(serverID: nil, text: "")]
            }
            return items.map { CriterionDraft(serverID: $0.id, text: $0.text) }
        }()
        _criteria = State(initialValue: initialCriteria)
        if let raw = existing?.deadline, let parsed = NoriDateFormatting.parse(raw) {
            _hasDeadline = State(initialValue: true)
            _deadlineDate = State(initialValue: parsed)
        } else {
            _hasDeadline = State(initialValue: false)
            _deadlineDate = State(initialValue: Calendar.current.date(byAdding: .day, value: 7, to: Date()) ?? Date())
        }
        _followUp = State(initialValue: existing?.followUp ?? "none")
    }

    private var canSave: Bool {
        !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !isSaving
    }

    public var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    if let errorMessage {
                        NoriNotice(
                            title: "Couldn’t save goal",
                            detail: errorMessage,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                    }

                    fieldBlock("Title") {
                        TextField("Plan weekend trip to Portland", text: $title)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(.horizontal, 12)
                            .frame(height: 42)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    fieldBlock("Outcome") {
                        TextField("What does finished look like?", text: $outcome, axis: .vertical)
                            .lineLimit(2...5)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(12)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    VStack(alignment: .leading, spacing: 8) {
                        HStack {
                            Text("Done when")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            Spacer()
                            Button {
                                criteria.append(CriterionDraft(serverID: nil, text: ""))
                            } label: {
                                Label("Add step", systemImage: "plus")
                                    .font(.system(size: 12.5, weight: .semibold))
                                    .foregroundStyle(NoriColors.accent)
                            }
                            .buttonStyle(.plain)
                        }

                        ForEach($criteria) { $draft in
                            HStack(spacing: 8) {
                                TextField("Criterion", text: $draft.text)
                                    .textFieldStyle(.plain)
                                    .foregroundStyle(NoriColors.ink)
                                    .tint(NoriColors.accent)
                                    .padding(.horizontal, 12)
                                    .frame(height: 40)
                                    .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))

                                if criteria.count > 1 {
                                    Button {
                                        criteria.removeAll { $0.localID == draft.localID }
                                    } label: {
                                        Image(systemName: "minus.circle.fill")
                                            .font(.system(size: 18))
                                            .foregroundStyle(NoriColors.ink3)
                                    }
                                    .buttonStyle(.plain)
                                    .accessibilityLabel("Remove criterion")
                                }
                            }
                        }
                    }

                    VStack(alignment: .leading, spacing: 10) {
                        Toggle(isOn: $hasDeadline) {
                            Text("Target date")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                        }
                        .tint(NoriColors.brandPrimary)

                        if hasDeadline {
                            DatePicker(
                                "Due",
                                selection: $deadlineDate,
                                displayedComponents: [.date]
                            )
                            .tint(NoriColors.accent)
                            .foregroundStyle(NoriColors.ink)
                        }
                    }

                    fieldBlock("Follow-up") {
                        Picker("Follow-up", selection: $followUp) {
                            Text("None").tag("none")
                            Text("Remind me").tag("remind")
                            Text("Monitor progress").tag("monitor")
                        }
                        .pickerStyle(.segmented)
                        .tint(NoriColors.accent)
                    }

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(NoriColors.disabled)
                        } else {
                            Text(existing == nil ? "Create goal" : "Save changes")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!canSave)
                    .padding(.top, 4)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle(existing == nil ? "New Goal" : "Edit Goal")
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
        guard !cleanTitle.isEmpty, !isSaving else { return }
        let cleanCriteria = criteria.compactMap { draft -> GoalCriterionInput? in
            let text = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty else { return nil }
            return GoalCriterionInput(id: draft.serverID, text: text)
        }
        let isoDeadline: String? = {
            guard hasDeadline else { return nil }
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime]
            return formatter.string(from: deadlineDate)
        }()

        let editable = GoalEditable(
            title: cleanTitle,
            outcome: outcome.trimmingCharacters(in: .whitespacesAndNewlines),
            completionCriteria: cleanCriteria,
            deadline: isoDeadline,
            followUp: followUp
        )

        isSaving = true
        errorMessage = nil
        Task {
            do {
                let saved: Goal
                if let existing {
                    saved = try await store.updateGoal(id: existing.id, value: editable)
                } else {
                    saved = try await store.createGoal(editable)
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
