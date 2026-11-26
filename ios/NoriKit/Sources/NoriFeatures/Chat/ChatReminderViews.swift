import SwiftUI
import NoriCore
import NoriDesign

struct ReminderEditTarget: Identifiable, Hashable {
    var id: String { automationID }
    let automationID: String
    let title: String
    let scheduledAt: Date
}

struct InlineReminderCard: View {
    let part: MessagePart
    let automation: Automation?
    let isCancelling: Bool
    let onEdit: () -> Void
    let onCancel: () -> Void

    private var isCancelled: Bool {
        automation?.status == .cancelled
    }

    private var displayTitle: String {
        if let title = automation?.title.trimmingCharacters(in: .whitespacesAndNewlines), !title.isEmpty {
            return title
        }
        return part.reminderTitle
    }

    private var displayWhen: String? {
        if let iso = automation?.trigger.at ?? automation?.nextAt ?? part.scheduledAt,
           !iso.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            let formatted = NoriDateFormatting.reminderDateTime(iso)
            if !formatted.isEmpty {
                return formatted
            }
        }
        if let text = part.text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty {
            return text
        }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: isCancelled ? "bell.slash" : "bell.fill")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(isCancelled ? NoriColors.ink3 : NoriColors.accent)
                    .frame(width: 32, height: 32)
                    .background(
                        isCancelled ? NoriColors.fill1 : NoriColors.accentSubtle,
                        in: RoundedRectangle(cornerRadius: 10, style: .continuous)
                    )

                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 5) {
                        Text(isCancelled ? "Reminder cancelled" : "Reminder set")
                            .font(.system(size: 12.5, weight: .semibold))
                            .foregroundStyle(isCancelled ? NoriColors.ink3 : NoriColors.accent)

                        if let when = displayWhen, !isCancelled {
                            Text("· \(when)")
                                .font(.system(size: 12.5, weight: .medium))
                                .foregroundStyle(NoriColors.ink2)
                                .lineLimit(1)
                        }
                    }

                    Text(displayTitle)
                        .font(.system(size: 14.5, weight: .semibold))
                        .foregroundStyle(isCancelled ? NoriColors.ink3 : NoriColors.ink)
                        .strikethrough(isCancelled, color: NoriColors.ink3)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }

            if !isCancelled {
                HStack(spacing: 8) {
                    Button(action: onEdit) {
                        HStack(spacing: 4) {
                            Image(systemName: "pencil")
                                .font(.system(size: 11, weight: .semibold))
                            Text("Edit")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                    .disabled(isCancelling)
                    .accessibilityLabel("Edit reminder \(displayTitle)")

                    Button(action: onCancel) {
                        HStack(spacing: 4) {
                            if isCancelling {
                                ProgressView()
                                    .controlSize(.mini)
                            } else {
                                Image(systemName: "xmark")
                                    .font(.system(size: 10.5, weight: .semibold))
                            }
                            Text(isCancelling ? "Cancelling…" : "Cancel")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    .disabled(isCancelling)
                    .accessibilityLabel("Cancel reminder \(displayTitle)")

                    Spacer(minLength: 0)
                }
                .padding(.leading, 42)
            }
        }
        .padding(13)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            isCancelled ? NoriColors.sidebar : NoriColors.surface,
            in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(isCancelled ? NoriColors.cardBorder : NoriColors.accentBorder, lineWidth: 1)
        )
        .shadow(color: isCancelled ? .clear : NoriColors.cardShadow, radius: 8, x: 0, y: 2)
    }
}

struct EditReminderSheet: View {
    @Bindable var store: NoriStore
    let target: ReminderEditTarget

    @Environment(\.dismiss) private var dismiss
    @State private var title: String
    @State private var scheduledAt: Date
    @State private var isSaving: Bool = false

    init(store: NoriStore, target: ReminderEditTarget) {
        self.store = store
        self.target = target
        self._title = State(initialValue: target.title)
        self._scheduledAt = State(initialValue: target.scheduledAt)
    }

    private var isValid: Bool {
        !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !isSaving
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Reminder")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("What should Nori remind you about?", text: $title)
                            .textFieldStyle(.plain)
                            .padding(.horizontal, 12)
                            .frame(height: 42)
                            .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                            )
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("When")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        DatePicker(
                            "Reminder time",
                            selection: $scheduledAt,
                            displayedComponents: [.date, .hourAndMinute]
                        )
                        .labelsHidden()
                        .tint(NoriColors.accent)
                    }

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(.white)
                        } else {
                            Text("Save changes")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!isValid)
                    .padding(.top, 4)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Edit Reminder")
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
            let updated = await store.updateReminder(
                automationID: target.automationID,
                title: title,
                scheduledAt: scheduledAt
            )
            isSaving = false
            if updated != nil {
                dismiss()
            }
        }
    }
}
