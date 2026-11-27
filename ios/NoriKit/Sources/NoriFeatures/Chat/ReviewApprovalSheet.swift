import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public struct ParsedApprovalDraft: Sendable, Equatable {
    public var recipient: String?
    public var subject: String?
    public var body: String

    public init(recipient: String? = nil, subject: String? = nil, body: String) {
        self.recipient = recipient
        self.subject = subject
        self.body = body
    }
}

public enum ApprovalDraftParser {
    public static func parse(interaction: Interaction) -> ParsedApprovalDraft {
        var recipient: String?
        var subject: String?
        var rawText = interaction.prompt

        if case .approval(let payload) = interaction.payload {
            if let recipients = payload.recipients, !recipients.isEmpty {
                recipient = recipients.joined(separator: ", ")
            }
            let summary = payload.summary.trimmingCharacters(in: .whitespacesAndNewlines)
            if !summary.isEmpty {
                rawText = summary
            } else if let fallback = payload.card?.fallback.trimmingCharacters(in: .whitespacesAndNewlines), !fallback.isEmpty {
                rawText = fallback
            }
        }

        let lines = rawText.components(separatedBy: .newlines)
        var bodyLines: [String] = []
        var parsingHeaders = true

        for line in lines {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if parsingHeaders {
                if trimmed.isEmpty {
                    if !bodyLines.isEmpty || recipient != nil || subject != nil {
                        parsingHeaders = false
                    }
                    continue
                }
                let lower = trimmed.lowercased()
                if lower.hasPrefix("to:") {
                    let val = String(trimmed.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                    if !val.isEmpty, recipient == nil {
                        recipient = val
                    }
                    continue
                }
                if lower.hasPrefix("subject:") {
                    let val = String(trimmed.dropFirst(8)).trimmingCharacters(in: .whitespaces)
                    if !val.isEmpty {
                        subject = val
                    }
                    continue
                }
                parsingHeaders = false
            }
            bodyLines.append(line)
        }

        let body = bodyLines.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
        return ParsedApprovalDraft(
            recipient: recipient,
            subject: subject,
            body: body.isEmpty ? rawText.trimmingCharacters(in: .whitespacesAndNewlines) : body
        )
    }

    public static func editedFields(
        initial: ParsedApprovalDraft,
        currentSubject: String,
        currentBody: String
    ) -> EditedApprovalFields? {
        let trimmedInitialSubject = initial.subject?.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedCurrentSubject = currentSubject.trimmingCharacters(in: .whitespacesAndNewlines)
        let subjectChanged = (trimmedInitialSubject != nil) && (trimmedCurrentSubject != trimmedInitialSubject)

        let bodyChanged = currentBody != initial.body
        guard subjectChanged || bodyChanged else { return nil }

        return EditedApprovalFields(
            subject: subjectChanged ? trimmedCurrentSubject : nil,
            body: bodyChanged ? currentBody : nil
        )
    }
}

struct ReviewApprovalSheet: View {
    @Bindable var store: NoriStore
    let interaction: Interaction
    @Environment(\.dismiss) private var dismiss

    private let initialDraft: ParsedApprovalDraft
    @State private var subjectText: String
    @State private var bodyText: String
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    init(store: NoriStore, interaction: Interaction) {
        self.store = store
        self.interaction = interaction
        let parsed = ApprovalDraftParser.parse(interaction: interaction)
        self.initialDraft = parsed
        self._subjectText = State(initialValue: parsed.subject ?? "")
        self._bodyText = State(initialValue: parsed.body)
    }

    private var sheetTitle: String {
        if case .approval(let payload) = interaction.payload, payload.effect == .sensitiveSend {
            return "Review reply"
        }
        return "Review action"
    }

    private var primaryButtonTitle: String {
        if case .approval(let payload) = interaction.payload, payload.effect == .sensitiveSend {
            return isSubmitting ? "Sending…" : "Send reply"
        }
        return isSubmitting ? "Sending…" : "Approve"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text(sheetTitle)
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)

                Spacer()

                Button {
                    dismiss()
                } label: {
                    Image(systemName: "xmark")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(NoriColors.ink2)
                        .frame(width: 28, height: 28)
                        .background(NoriColors.elevated, in: Circle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Close")
            }

            if initialDraft.recipient != nil || initialDraft.subject != nil {
                VStack(alignment: .leading, spacing: 6) {
                    if let recipient = initialDraft.recipient, !recipient.isEmpty {
                        HStack(alignment: .firstTextBaseline, spacing: 6) {
                            Text("To:")
                                .font(.system(size: 13.5, weight: .semibold))
                                .foregroundStyle(NoriColors.ink)
                            Text(recipient)
                                .font(.system(size: 13.5))
                                .foregroundStyle(NoriColors.ink2)
                                .lineLimit(2)
                        }
                    }

                    if initialDraft.subject != nil {
                        HStack(alignment: .firstTextBaseline, spacing: 6) {
                            Text("Subject:")
                                .font(.system(size: 13.5, weight: .semibold))
                                .foregroundStyle(NoriColors.ink)
                            TextField("Subject", text: $subjectText)
                                .font(.system(size: 13.5))
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                .textFieldStyle(.plain)
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 14)
                .padding(.vertical, 11)
                .background(NoriColors.sidebar, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            }

            TextEditor(text: $bodyText)
                .font(.system(size: 14.5))
                .foregroundStyle(NoriColors.ink)
                .tint(NoriColors.accent)
                .scrollContentBackground(.hidden)
                .padding(.horizontal, 10)
                .padding(.vertical, 8)
                .frame(minHeight: 130, maxHeight: 240)
                .background(
                    NoriColors.elevated,
                    in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                )
                .overlay(
                    RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                        .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                )

            if let errorMessage {
                Text(errorMessage)
                    .font(.system(size: 12.5))
                    .foregroundStyle(NoriColors.danger)
            }

            HStack(spacing: 8) {
                Spacer(minLength: 0)

                Button("Skip") {
                    submitSkip()
                }
                .buttonStyle(NoriButtonStyle(.quiet))
                .disabled(isSubmitting)

                Button(primaryButtonTitle) {
                    submitSend()
                }
                .buttonStyle(NoriButtonStyle(.primary))
                .disabled(isSubmitting || bodyText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
            .padding(.top, 2)
        }
        .padding(.horizontal, 20)
        .padding(.top, 18)
        .padding(.bottom, 24)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(NoriColors.sheetBackground)
    }

    private func submitSend() {
        guard !isSubmitting else { return }
        #if os(iOS)
        UIImpactFeedbackGenerator(style: .medium).impactOccurred()
        #endif
        isSubmitting = true
        errorMessage = nil
        let edited = ApprovalDraftParser.editedFields(
            initial: initialDraft,
            currentSubject: subjectText,
            currentBody: bodyText
        )
        Task {
            let outcome = await store.approveInteraction(
                interaction,
                rememberRule: false,
                editedFields: edited
            )
            isSubmitting = false
            switch outcome {
            case .resolved:
                dismiss()
            case .conflict(_, let message), .failed(let message):
                errorMessage = message
            }
        }
    }

    private func submitSkip() {
        guard !isSubmitting else { return }
        #if os(iOS)
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
        #endif
        isSubmitting = true
        errorMessage = nil
        Task {
            let outcome = await store.declineInteraction(interaction)
            isSubmitting = false
            switch outcome {
            case .resolved:
                dismiss()
            case .conflict(_, let message), .failed(let message):
                errorMessage = message
            }
        }
    }
}
