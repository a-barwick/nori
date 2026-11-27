import SwiftUI
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

public struct InteractionStatusPill: Equatable, Sendable {
    public let label: String
    public let tone: NoriTone
    public let systemImage: String?

    public init(label: String, tone: NoriTone, systemImage: String? = nil) {
        self.label = label
        self.tone = tone
        self.systemImage = systemImage
    }
}

public enum InteractionStatusPresentation {
    public static func statusPill(for interaction: Interaction) -> InteractionStatusPill {
        switch interaction.status {
        case .pending:
            if let expiresAt = interaction.expiresAt?.trimmingCharacters(in: .whitespacesAndNewlines),
               !expiresAt.isEmpty {
                return InteractionStatusPill(
                    label: NoriDateFormatting.expiryCountdown(expiresAt),
                    tone: .warning,
                    systemImage: "clock"
                )
            }
            return InteractionStatusPill(label: "Waiting", tone: .warning)

        case .expired:
            return InteractionStatusPill(label: "Expired", tone: .neutral)

        case .superseded:
            return InteractionStatusPill(label: "Replaced", tone: .neutral)

        case .resolved:
            switch interaction.resolution?.kind {
            case .approve:
                return InteractionStatusPill(label: "Approved", tone: .ok)
            case .decline:
                let label: String
                switch interaction.kind {
                case .signIn: label = "Not now"
                case .approval: label = "Denied"
                case .question, .accountSelection: label = "Declined"
                }
                return InteractionStatusPill(label: label, tone: .neutral)
            case .signedIn:
                return InteractionStatusPill(label: "Signed in", tone: .ok)
            case .expired:
                return InteractionStatusPill(label: "Expired", tone: .neutral)
            case .answer, .chooseAccount, .none:
                return InteractionStatusPill(label: "Answered", tone: .ok)
            }
        }
    }
}

extension Interaction {
    static func humanizedIntegrationName(_ raw: String) -> String {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "app" }
        let known: [String: String] = [
            "google": "Google",
            "gmail": "Gmail",
            "github": "GitHub",
            "gitlab": "GitLab",
            "slack": "Slack",
            "linear": "Linear",
            "notion": "Notion",
            "microsoft": "Microsoft",
            "outlook": "Outlook",
            "icloud": "iCloud"
        ]
        let lower = trimmed.lowercased()
        if let mapped = known[lower] { return mapped }
        return trimmed
            .components(separatedBy: CharacterSet(charactersIn: "_-"))
            .filter { !$0.isEmpty }
            .map { part in
                known[part.lowercased()] ?? part.prefix(1).uppercased() + part.dropFirst()
            }
            .joined(separator: " ")
    }

    var displayHeading: String {
        if kind == .signIn, case .signIn(let payload) = payload {
            if payload.integrationID != "mcp", !payload.integrationID.isEmpty {
                return "Connect \(Self.humanizedIntegrationName(payload.integrationID))"
            }
            return "Connect app"
        }
        if kind == .approval, case .approval(let payload) = payload {
            if payload.isScheduleCreation {
                let firstLine = payload.summary
                    .components(separatedBy: .newlines)
                    .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
                    .first { !$0.isEmpty }
                if let firstLine, !firstLine.isEmpty {
                    return firstLine
                }
                return "Create routine"
            }
            if prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || prompt == payload.summary {
                switch payload.effect {
                case .sensitiveSend: return "Send this?"
                case .reversibleWrite: return "Make this change?"
                case .read: return "Allow this?"
                case .unknown, .control: return "Approve this action?"
                }
            }
        }
        return prompt.isEmpty ? "Input requested" : prompt
    }
}

private struct InteractionConflictState: Equatable {
    let current: Interaction?
    let message: String
}

public struct InteractionCardView: View {
    @Bindable private var store: NoriStore
    private let interaction: Interaction
    private let compact: Bool
    private let onOpenTask: ((String) -> Void)?

    @State private var isSubmitting: Bool = false
    @State private var submittingAction: String?
    @State private var errorMessage: String?
    @State private var conflictState: InteractionConflictState?

    @State private var isQuotedBodyExpanded: Bool = false

    @State private var selectedOptionIDs: Set<String> = []
    @State private var textAnswer: String = ""
    @State private var dateAnswer: Date = Date()

    @State private var rememberAccountAsDefault: Bool = false
    @State private var didSeedAccountDefault: Bool = false

    public init(
        store: NoriStore,
        interaction: Interaction,
        compact: Bool = false,
        onOpenTask: ((String) -> Void)? = nil
    ) {
        self.store = store
        self.interaction = interaction
        self.compact = compact
        self.onOpenTask = onOpenTask
    }

    private var isResolved: Bool {
        interaction.status != .pending
    }

    public var body: some View {
        let pill = InteractionStatusPresentation.statusPill(for: interaction)
        let hasExpiry = interaction.status == .pending
            && !(interaction.expiresAt?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true)
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(interaction.displayHeading)
                    .font(.system(size: 15.5, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)

                if isResolved {
                    NoriPill(pill.label, tone: pill.tone, systemImage: pill.systemImage)
                } else if hasExpiry {
                    Text(pill.label)
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(NoriColors.warning)
                }
            }

            if isResolved {
                resolvedBody(pillLabel: pill.label)
            } else {
                pendingBody
                    .padding(.top, 6)
            }

            if let onOpenTask, let taskID = interaction.taskID, !taskID.isEmpty, !compact {
                Button {
                    onOpenTask(taskID)
                } label: {
                    HStack(spacing: 4) {
                        Text(interaction.taskTitle?.isEmpty == false ? interaction.taskTitle! : "Open task")
                            .lineLimit(1)
                        Image(systemName: "chevron.right")
                            .font(.system(size: 10, weight: .semibold))
                    }
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(NoriColors.accent)
                    .padding(.top, 8)
                }
                .buttonStyle(.plain)
            }

            if let conflictState {
                NoriNotice(
                    title: conflictState.message,
                    tone: .warning,
                    systemImage: "arrow.triangle.2.circlepath"
                ) {
                    Button("Show current") {
                        applyConflictResolution(conflictState)
                    }
                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                }
                .padding(.top, 10)
            }

            if let errorMessage {
                NoriNotice(title: errorMessage, tone: .danger, systemImage: "exclamationmark.triangle.fill")
                    .padding(.top, 10)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            isResolved ? NoriColors.sidebar : NoriColors.surface,
            in: RoundedRectangle(cornerRadius: NoriRadius.approvalCard, style: .continuous)
        )
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.approvalCard, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
        .shadow(
            color: isResolved ? .clear : NoriColors.accentShadow,
            radius: 10,
            x: 0,
            y: 2
        )
        .onAppear {
            seedInitialSelectionsIfNeeded()
        }
        .onChange(of: interaction.id) { _, _ in
            resetLocalState()
        }
        .onChange(of: interaction.revision) { _, _ in
            conflictState = nil
            errorMessage = nil
        }
    }

    @ViewBuilder
    private func resolvedBody(pillLabel: String) -> some View {
        if interaction.status == .superseded {
            Text(interaction.supersededBy != nil
                 ? "A newer version replaced this. It’s in Action needed."
                 : "A newer version replaced this.")
                .font(.system(size: 13))
                .foregroundStyle(NoriColors.ink2)
                .padding(.top, 8)
        } else if let line = resolvedDetailLine(pillLabel: pillLabel) {
            let icon = resolvedIcon
            HStack(spacing: 6) {
                Image(systemName: icon.name)
                    .font(.system(size: 12, weight: .bold))
                    .foregroundStyle(icon.color)
                Text(line)
                    .font(.system(size: 13))
                    .foregroundStyle(NoriColors.ink2)
            }
            .padding(.top, 8)
        }
    }

    private var resolvedIcon: (name: String, color: Color) {
        if interaction.status == .expired || interaction.resolution?.kind == .expired {
            return ("clock", NoriColors.ink3)
        }
        if interaction.resolution?.kind == .decline {
            return ("minus.circle", NoriColors.ink3)
        }
        return ("checkmark", NoriColors.ok)
    }

    private func resolvedDetailLine(pillLabel: String) -> String? {
        guard let res = interaction.resolution else {
            if let resolvedAt = interaction.resolvedAt, !resolvedAt.isEmpty {
                return NoriDateFormatting.shortDateTime(resolvedAt)
            }
            return nil
        }
        let base: String
        switch res.kind {
        case .approve:
            base = "Approved"
        case .signedIn:
            base = "Signed in"
        case .decline:
            switch interaction.kind {
            case .signIn: base = "Not now"
            case .approval: base = "Denied"
            case .question, .accountSelection: base = "Declined"
            }
        case .chooseAccount:
            base = "Account chosen"
        case .expired:
            base = "Expired"
        case .answer:
            if let text = res.text, !text.isEmpty {
                base = "“\(text)”"
            } else {
                base = "Answered"
            }
        }
        let duplicatesPill = base.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
            == pillLabel.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let timeText = res.at.isEmpty ? nil : NoriDateFormatting.shortDateTime(res.at)
        let parts = [duplicatesPill ? nil : base, timeText].compactMap { $0 }.filter { !$0.isEmpty }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    @ViewBuilder
    private var pendingBody: some View {
        switch interaction.payload {
        case .approval(let payload):
            InteractionApprovalBody(
                payload: payload,
                compact: compact,
                isSubmitting: isSubmitting,
                submittingAction: submittingAction,
                isQuotedBodyExpanded: $isQuotedBodyExpanded,
                onApprove: { rememberRule in submitApprove(rememberRule: rememberRule) },
                onDecline: { submitDecline() }
            )
        case .question(let payload):
            InteractionQuestionBody(
                payload: payload,
                compact: compact,
                isSubmitting: isSubmitting,
                submittingAction: submittingAction,
                selectedOptionIDs: $selectedOptionIDs,
                textAnswer: $textAnswer,
                dateAnswer: $dateAnswer,
                onAnswer: { text, optionIDs, tag in
                    submitAnswer(text: text, optionIDs: optionIDs, actionTag: tag)
                }
            )
        case .accountSelection(let payload):
            InteractionAccountSelectionBody(
                store: store,
                payload: payload,
                isSubmitting: isSubmitting,
                submittingAction: submittingAction,
                rememberAccountAsDefault: $rememberAccountAsDefault,
                onChooseAccount: { connectionID in
                    submitChooseAccount(connectionID: connectionID)
                }
            )
        case .signIn(let payload):
            InteractionSignInBody(
                store: store,
                payload: payload,
                isSubmitting: $isSubmitting,
                submittingAction: $submittingAction,
                errorMessage: $errorMessage,
                onDecline: { submitDecline() },
                onHaptic: { triggerNotificationHaptic($0) }
            )
        case .unknown:
            VStack(alignment: .leading, spacing: 10) {
                Text("This interaction requires a newer client version.")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
                Button("Dismiss") {
                    submitDecline()
                }
                .buttonStyle(NoriButtonStyle(.neutral, compact: true))
            }
        }
    }

    private func resetLocalState() {
        selectedOptionIDs = []
        textAnswer = ""
        isQuotedBodyExpanded = false
        didSeedAccountDefault = false
        errorMessage = nil
        conflictState = nil
        seedInitialSelectionsIfNeeded()
    }

    private func seedInitialSelectionsIfNeeded() {
        if !didSeedAccountDefault, case .accountSelection(let payload) = interaction.payload {
            didSeedAccountDefault = true
            rememberAccountAsDefault = payload.rememberAsDefault ?? false
        }
    }

    private func applyConflictResolution(_ state: InteractionConflictState) {
        conflictState = nil
        errorMessage = nil
        if let current = state.current {
            store.applyCurrentInteraction(current)
        } else {
            Task { await store.refreshWorkAndInteractions() }
        }
    }

    private func handleSubmitOutcome(_ outcome: InteractionSubmitOutcome, successHaptic: InteractionHapticOutcome) {
        switch outcome {
        case .resolved:
            triggerNotificationHaptic(successHaptic)
        case .conflict(let current, let message):
            conflictState = InteractionConflictState(current: current, message: message)
            triggerNotificationHaptic(.warning)
        case .failed(let message):
            if message != "Cancelled" {
                errorMessage = message
            }
            triggerNotificationHaptic(.warning)
        }
    }

    private func submitApprove(rememberRule: Bool = false) {
        errorMessage = nil
        conflictState = nil
        isSubmitting = true
        submittingAction = rememberRule ? "approve_always" : "approve_once"
        Task {
            let outcome = await store.approveInteraction(interaction, rememberRule: rememberRule)
            handleSubmitOutcome(outcome, successHaptic: .success)
            isSubmitting = false
            submittingAction = nil
        }
    }

    private func submitDecline() {
        errorMessage = nil
        conflictState = nil
        isSubmitting = true
        submittingAction = "decline"
        Task {
            let outcome = await store.declineInteraction(interaction)
            handleSubmitOutcome(outcome, successHaptic: .warning)
            isSubmitting = false
            submittingAction = nil
        }
    }

    private func submitAnswer(text: String, optionIDs: [String]?, actionTag: String = "answer") {
        errorMessage = nil
        conflictState = nil
        isSubmitting = true
        submittingAction = actionTag
        Task {
            let outcome = await store.answerInteraction(interaction, text: text, optionIDs: optionIDs)
            handleSubmitOutcome(outcome, successHaptic: .success)
            isSubmitting = false
            submittingAction = nil
        }
    }

    private func submitChooseAccount(connectionID: String) {
        errorMessage = nil
        conflictState = nil
        isSubmitting = true
        submittingAction = connectionID
        Task {
            let outcome = await store.chooseAccountInteraction(
                interaction,
                connectionID: connectionID,
                rememberAsDefault: rememberAccountAsDefault
            )
            handleSubmitOutcome(outcome, successHaptic: .success)
            isSubmitting = false
            submittingAction = nil
        }
    }

    private func triggerNotificationHaptic(_ outcome: InteractionHapticOutcome) {
        #if os(iOS)
        let generator = UINotificationFeedbackGenerator()
        generator.prepare()
        generator.notificationOccurred(outcome == .success ? .success : .warning)
        #endif
    }
}

enum InteractionHapticOutcome {
    case success
    case warning
}
