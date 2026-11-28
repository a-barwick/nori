import SwiftUI
import NoriCore
import NoriDesign

struct InteractionApprovalBody: View {
    let payload: ApprovalPayload
    let compact: Bool
    let isSubmitting: Bool
    let submittingAction: String?
    @Binding var isQuotedBodyExpanded: Bool
    let onApprove: (Bool) -> Void
    let onDecline: () -> Void

    var body: some View {
        if payload.isScheduleCreation {
            routineApprovalBody
        } else {
            standardApprovalBody
        }
    }

    private var routineApprovalBody: some View {
        let lines = payload.summary
            .components(separatedBy: .newlines)
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
        let detail = lines.count > 1 ? lines.dropFirst().joined(separator: " · ") : ""
        let automatic = payload.effect == .sensitiveSend
        let metaLine = [detail, automatic ? "Runs automatically" : ""]
            .filter { !$0.isEmpty }
            .joined(separator: " · ")

        return VStack(alignment: .leading, spacing: 12) {
            if !metaLine.isEmpty {
                Text(metaLine)
                    .font(.system(size: 13.5))
                    .foregroundStyle(NoriColors.ink2)
                    .lineSpacing(2)
                    .fixedSize(horizontal: false, vertical: true)
            }

            HStack(spacing: 8) {
                Button {
                    onApprove(false)
                } label: {
                    Text(submittingAction == "approve_once" ? "Creating…" : "Create")
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: compact))
                .disabled(isSubmitting)

                Button {
                    onDecline()
                } label: {
                    Text(submittingAction == "decline" ? "Cancelling…" : "Cancel")
                }
                .buttonStyle(NoriButtonStyle(.neutral, fullWidth: true, compact: compact))
                .disabled(isSubmitting)
            }
        }
    }

    @ViewBuilder
    private var standardApprovalBody: some View {
        let isSensitiveSend = payload.effect == .sensitiveSend
        let parsed = parseApprovalSummary(payload)
        let warning = payload.displayWarning
        let hasRecipients = payload.toolID != "google/mail/mail-send" && !(payload.recipients?.isEmpty ?? true)
        let hasMetadataRows = hasRecipients || payload.hasAccountLabel
        let canRememberRule = payload.approvalOnce != true

        VStack(alignment: .leading, spacing: 10) {
            if let head = parsed.head, !head.isEmpty {
                Text(head)
                    .font(.system(size: 13.5, weight: .medium))
                    .foregroundStyle(NoriColors.ink)
                    .lineSpacing(2)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if let quotedBody = parsed.quotedBody, !quotedBody.isEmpty {
                let canExpand = payload.approvalOnce != true && shouldOfferQuotedBodyExpansion(quotedBody)
                VStack(alignment: .leading, spacing: 4) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(quotedBody)
                            .font(.system(size: 13))
                            .lineSpacing(2.5)
                            .foregroundStyle(NoriColors.ink)
                            .lineLimit(isQuotedBodyExpanded || !canExpand ? nil : 4)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)

                        if canExpand {
                            Button {
                                withAnimation(.easeInOut(duration: 0.18)) {
                                    isQuotedBodyExpanded.toggle()
                                }
                            } label: {
                                HStack(spacing: 4) {
                                    Text(isQuotedBodyExpanded ? "Show less" : "Show more")
                                    Image(systemName: isQuotedBodyExpanded ? "chevron.up" : "chevron.down")
                                        .font(.system(size: 10, weight: .semibold))
                                }
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.accent)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    .overlay(
                        RoundedRectangle(cornerRadius: 12, style: .continuous)
                            .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                    )

                    if payload.approvalOnce != true && quotedBody.trimmingCharacters(in: .whitespacesAndNewlines).hasSuffix("…") {
                        Text("Only the start of the message is shown here. The rest is sent too.")
                            .font(.system(size: 11.5))
                            .foregroundStyle(NoriColors.ink2)
                    }
                }
            }

            if let warning, !warning.isEmpty {
                NoriNotice(
                    title: warning,
                    tone: .warning,
                    systemImage: "exclamationmark.triangle.fill"
                )
            }

            if hasMetadataRows {
                Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 6) {
                    if isSensitiveSend, hasRecipients, let recipients = payload.recipients, !recipients.isEmpty {
                        GridRow {
                            Text("To")
                                .font(.system(size: 12.5))
                                .foregroundStyle(NoriColors.ink3)
                            Text(recipients.joined(separator: ", "))
                                .font(.system(size: 12.5, weight: .medium))
                                .foregroundStyle(NoriColors.ink)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }

                    if payload.hasAccountLabel {
                        GridRow {
                            Text(isSensitiveSend ? "From" : "Account")
                                .font(.system(size: 12.5))
                                .foregroundStyle(NoriColors.ink3)
                            Text(payload.account.label)
                                .font(.system(size: 12.5, weight: .medium))
                                .foregroundStyle(NoriColors.ink)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }

                    if !isSensitiveSend, hasRecipients, let recipients = payload.recipients, !recipients.isEmpty {
                        GridRow {
                            Text("To")
                                .font(.system(size: 12.5))
                                .foregroundStyle(NoriColors.ink3)
                            Text(recipients.joined(separator: ", "))
                                .font(.system(size: 12.5, weight: .medium))
                                .foregroundStyle(NoriColors.ink)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                }
            }

            if let effects = payload.materialEffects, !effects.isEmpty {
                VStack(alignment: .leading, spacing: 5) {
                    ForEach(effects, id: \.self) { effect in
                        HStack(alignment: .top, spacing: 6) {
                            Image(systemName: "checkmark.shield")
                                .font(.system(size: 13, weight: .medium))
                                .foregroundStyle(NoriColors.ink3)
                                .padding(.top, 1)
                            Text(effect)
                                .font(.system(size: 12))
                                .foregroundStyle(NoriColors.ink2)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                }
                .padding(.top, 2)
            }

            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    Button {
                        onApprove(false)
                    } label: {
                        Text(
                            payload.isBudgetConfirmation
                                ? (submittingAction == "approve_once" ? "Approving…" : "Approve")
                                : (submittingAction == "approve_once" ? "Allowing…" : "Allow once")
                        )
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: compact))
                    .disabled(isSubmitting)

                    Button {
                        onDecline()
                    } label: {
                        Text(
                            payload.isBudgetConfirmation
                                ? (submittingAction == "decline" ? "Declining…" : "Decline")
                                : (submittingAction == "decline" ? "Denying…" : "Deny")
                        )
                    }
                    .buttonStyle(
                        NoriButtonStyle(
                            payload.isBudgetConfirmation ? .neutral : .danger,
                            fullWidth: true,
                            compact: compact
                        )
                    )
                    .disabled(isSubmitting)
                }

                if canRememberRule {
                    Button {
                        onApprove(true)
                    } label: {
                        Text(submittingAction == "approve_always" ? "Saving rule…" : "Always allow")
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, fullWidth: true, compact: compact))
                    .disabled(isSubmitting)
                }
            }
            .padding(.top, 4)
        }
    }

    private func shouldOfferQuotedBodyExpansion(_ text: String) -> Bool {
        let lines = text.components(separatedBy: .newlines)
        if lines.count > 4 {
            return true
        }
        let estimatedWrappedLines = lines.reduce(0) { total, line in
            total + max(1, Int(ceil(Double(max(1, line.count)) / 42.0)))
        }
        return estimatedWrappedLines > 4 || text.count > 160
    }

    private func parseApprovalSummary(_ payload: ApprovalPayload) -> (head: String?, quotedBody: String?) {
        let primaryText = payload.displaySummary
        if payload.toolID == "google/mail/mail-send" {
            return (nil, primaryText)
        }
        if payload.effect == .sensitiveSend, let newline = primaryText.firstIndex(of: "\n") {
            let head = String(primaryText[..<newline])
            let rest = String(primaryText[primaryText.index(after: newline)...])
                .trimmingCharacters(in: .whitespacesAndNewlines)
            return (head, rest.isEmpty ? "(No message text)" : rest)
        }
        return (primaryText.isEmpty ? nil : primaryText, nil)
    }
}
