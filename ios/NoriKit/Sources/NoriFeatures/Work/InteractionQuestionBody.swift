import SwiftUI
import NoriCore
import NoriDesign

struct InteractionQuestionBody: View {
    let payload: QuestionPayload
    let compact: Bool
    let isSubmitting: Bool
    let submittingAction: String?
    @Binding var selectedOptionIDs: Set<String>
    @Binding var textAnswer: String
    @Binding var dateAnswer: Date
    let onAnswer: (String, [String]?, String) -> Void

    var body: some View {
        switch payload.input {
        case .confirm:
            HStack(spacing: 8) {
                Button {
                    onAnswer("Yes", nil, "confirm_yes")
                } label: {
                    Text(submittingAction == "confirm_yes" ? "Sending…" : "Yes")
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: compact))
                .disabled(isSubmitting)

                Button {
                    onAnswer("No", nil, "confirm_no")
                } label: {
                    Text(submittingAction == "confirm_no" ? "Sending…" : "No")
                }
                .buttonStyle(NoriButtonStyle(.neutral, fullWidth: true, compact: compact))
                .disabled(isSubmitting)
            }

        case .choice, .multiChoice:
            if let options = payload.options, !options.isEmpty {
                let isMulti = payload.input == .multiChoice
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(options) { option in
                        let isSelected = selectedOptionIDs.contains(option.id)
                        Button {
                            if isMulti {
                                if isSelected {
                                    selectedOptionIDs.remove(option.id)
                                } else {
                                    selectedOptionIDs.insert(option.id)
                                }
                            } else {
                                selectedOptionIDs = [option.id]
                            }
                        } label: {
                            HStack(spacing: 10) {
                                InteractionSelectionIndicator(isSelected: isSelected, isMulti: isMulti)
                                Text(option.label)
                                    .font(.system(size: 13.5, weight: .medium))
                                    .foregroundStyle(NoriColors.ink)
                                    .multilineTextAlignment(.leading)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .padding(.horizontal, 12)
                            .padding(.vertical, 10)
                            .background(
                                isSelected ? NoriColors.selected : NoriColors.elevated,
                                in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                            )
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    .strokeBorder(
                                        isSelected ? NoriColors.accent : NoriColors.cardBorder,
                                        lineWidth: 1
                                    )
                            )
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .disabled(isSubmitting)
                    }

                    Button {
                        let pickedLabels = options
                            .filter { selectedOptionIDs.contains($0.id) }
                            .map(\.label)
                            .joined(separator: ", ")
                        let orderedIDs = options
                            .filter { selectedOptionIDs.contains($0.id) }
                            .map(\.id)
                        onAnswer(pickedLabels, orderedIDs, "answer")
                    } label: {
                        Text(submittingAction == "answer" ? "Sending…" : "Send answer")
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(isSubmitting || selectedOptionIDs.isEmpty)
                    .padding(.top, 6)
                }
            } else {
                freeTextQuestionForm
            }

        case .date:
            VStack(alignment: .leading, spacing: 10) {
                DatePicker(
                    "Choose date",
                    selection: $dateAnswer,
                    displayedComponents: [.date]
                )
                .labelsHidden()
                .tint(NoriColors.accent)

                Button {
                    let formatter = DateFormatter()
                    formatter.dateFormat = "yyyy-MM-dd"
                    onAnswer(formatter.string(from: dateAnswer), nil, "answer")
                } label: {
                    Text(submittingAction == "answer" ? "Sending…" : "Send answer")
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(isSubmitting)
            }

        case .text:
            freeTextQuestionForm
        }
    }

    private var freeTextQuestionForm: some View {
        let trimmed = textAnswer.trimmingCharacters(in: .whitespacesAndNewlines)
        return VStack(alignment: .leading, spacing: 10) {
            TextField("Your answer…", text: $textAnswer, axis: .vertical)
                .font(NoriTypography.body)
                .foregroundStyle(NoriColors.ink)
                .tint(NoriColors.accent)
                .lineLimit(2...5)
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: 12, style: .continuous)
                        .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                )

            Button {
                onAnswer(trimmed, nil, "answer")
            } label: {
                Text(submittingAction == "answer" ? "Sending…" : "Send answer")
            }
            .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
            .disabled(isSubmitting || trimmed.isEmpty)
        }
    }
}

struct InteractionAccountSelectionBody: View {
    @Bindable var store: NoriStore
    let payload: AccountSelectionPayload
    let isSubmitting: Bool
    let submittingAction: String?
    @Binding var rememberAccountAsDefault: Bool
    let onChooseAccount: (String) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !payload.purpose.isEmpty {
                Text(payload.purpose)
                    .font(.system(size: 12.5))
                    .foregroundStyle(NoriColors.ink2)
            }

            ForEach(payload.eligibleConnectionIDs, id: \.self) { connectionID in
                let connection = store.connections.first(where: { $0.id == connectionID })
                let primaryLabel = connection?.account?.label ?? connection?.title ?? "Connected account (\(connectionID))"
                let secondaryLabel = connection?.title

                Button {
                    onChooseAccount(connectionID)
                } label: {
                    HStack(spacing: 10) {
                        Image(systemName: "person.crop.circle")
                            .font(.system(size: 18))
                            .foregroundStyle(NoriColors.accent)

                        VStack(alignment: .leading, spacing: 1) {
                            Text(primaryLabel)
                                .font(.system(size: 13.5, weight: .semibold))
                                .foregroundStyle(NoriColors.ink)
                                .lineLimit(1)
                            if let secondaryLabel, secondaryLabel != primaryLabel {
                                Text(secondaryLabel)
                                    .font(.system(size: 12))
                                    .foregroundStyle(NoriColors.ink2)
                                    .lineLimit(1)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)

                        if submittingAction == connectionID {
                            ProgressView().controlSize(.mini)
                        } else {
                            Image(systemName: "chevron.right")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.ink3)
                        }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: 12, style: .continuous)
                            .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                    )
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(isSubmitting)
            }

            Toggle(isOn: $rememberAccountAsDefault) {
                Text("Use this account next time")
                    .font(.system(size: 12.5))
                    .foregroundStyle(NoriColors.ink2)
            }
            .tint(NoriColors.brandPrimary)
            .padding(.top, 2)
        }
    }
}

struct InteractionSelectionIndicator: View {
    let isSelected: Bool
    let isMulti: Bool

    var body: some View {
        if isMulti {
            ZStack {
                RoundedRectangle(cornerRadius: 5, style: .continuous)
                    .fill(isSelected ? NoriColors.brandPrimary : Color.clear)
                    .frame(width: 18, height: 18)
                RoundedRectangle(cornerRadius: 5, style: .continuous)
                    .strokeBorder(isSelected ? NoriColors.brandPrimary : NoriColors.hairlineStrong, lineWidth: 2)
                    .frame(width: 18, height: 18)
                if isSelected {
                    Image(systemName: "checkmark")
                        .font(.system(size: 10.5, weight: .bold))
                        .foregroundStyle(NoriColors.onAccent)
                }
            }
        } else {
            ZStack {
                Circle()
                    .fill(isSelected ? NoriColors.brandPrimary : Color.clear)
                    .frame(width: 18, height: 18)
                Circle()
                    .strokeBorder(isSelected ? NoriColors.brandPrimary : NoriColors.hairlineStrong, lineWidth: 2)
                    .frame(width: 18, height: 18)
                if isSelected {
                    Circle()
                        .fill(NoriColors.onAccent)
                        .frame(width: 6, height: 6)
                }
            }
        }
    }
}
