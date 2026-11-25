import SwiftUI
import AuthenticationServices
import NoriCore
import NoriDesign
#if canImport(UIKit)
import UIKit
#endif

// MARK: - Interaction Heading Helper

extension Interaction {
    var displayHeading: String {
        if kind == .approval, case .approval(let payload) = payload, prompt == payload.summary {
            switch payload.effect {
            case .sensitiveSend: return "Send this?"
            case .reversibleWrite: return "Make this change?"
            case .read: return "Allow this?"
            case .unknown, .control: return "Approve this action?"
            }
        }
        return prompt.isEmpty ? "Needs your input" : prompt
    }
}

// MARK: - InteractionCardView

public struct InteractionCardView: View {
    @Bindable private var store: NoriStore
    private let interaction: Interaction
    private let compact: Bool
    private let onOpenTask: ((String) -> Void)?

    @State private var isSubmitting: Bool = false
    @State private var submittingAction: String?
    @State private var errorMessage: String?

    // Approval state (never defaulted on)
    @State private var rememberSimilarRule: Bool = false
    @State private var isQuotedBodyExpanded: Bool = false

    // Question state
    @State private var selectedOptionIDs: Set<String> = []
    @State private var textAnswer: String = ""
    @State private var dateAnswer: Date = Date()
    @State private var didSeedInitialChoice: Bool = false

    // Account selection state
    @State private var rememberAccountAsDefault: Bool = false
    @State private var didSeedAccountDefault: Bool = false

    // Sign-in OAuth helper
    @Environment(\.webAuthenticationSession) private var webAuthenticationSession

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
        VStack(alignment: .leading, spacing: 0) {
            // Card Header
            HStack(alignment: .top, spacing: 8) {
                Text(interaction.displayHeading)
                    .font(.system(size: 15.5, weight: .bold))
                    .foregroundStyle(NoriColors.ink)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)

                statusBadge
            }

            if !compact, let taskTitle = interaction.taskTitle, !taskTitle.isEmpty {
                if let taskID = interaction.taskID, let onOpenTask {
                    Button {
                        onOpenTask(taskID)
                    } label: {
                        HStack(spacing: 3) {
                            Text(taskTitle)
                                .lineLimit(1)
                            Image(systemName: "chevron.right")
                                .font(.system(size: 10, weight: .semibold))
                        }
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(NoriColors.accent)
                    }
                    .buttonStyle(.plain)
                    .padding(.top, 4)
                } else {
                    Text(taskTitle)
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(NoriColors.ink2)
                        .padding(.top, 4)
                }
            }

            // Card Body
            Group {
                if isResolved {
                    resolvedBody
                } else {
                    pendingBody
                }
            }
            .padding(.top, 10)

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
                .strokeBorder(
                    NoriColors.cardBorder,
                    lineWidth: 1
                )
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
    }

    @ViewBuilder
    private var statusBadge: some View {
        if isResolved {
            switch interaction.status {
            case .resolved:
                NoriPill("Answered", tone: .ok)
            case .expired:
                NoriPill("Expired", tone: .neutral)
            case .superseded:
                NoriPill("Replaced", tone: .neutral)
            case .pending:
                EmptyView()
            }
        } else if let expiresAt = interaction.expiresAt?.trimmingCharacters(in: .whitespacesAndNewlines), !expiresAt.isEmpty {
            NoriPill(NoriDateFormatting.expiryCountdown(expiresAt), tone: .warning, systemImage: "clock")
        }
    }

    // MARK: - Resolved Body

    @ViewBuilder
    private var resolvedBody: some View {
        if interaction.status == .superseded {
            Text(interaction.supersededBy != nil
                 ? "A newer version replaced this. It’s in Needs you."
                 : "A newer version replaced this.")
                .font(.system(size: 13))
                .foregroundStyle(NoriColors.ink2)
        } else {
            let label = resolvedSummaryText
            HStack(spacing: 6) {
                Image(systemName: "checkmark")
                    .font(.system(size: 12, weight: .bold))
                    .foregroundStyle(NoriColors.ok)
                Text(label)
                    .font(.system(size: 13))
                    .foregroundStyle(NoriColors.ink2)
            }
        }
    }

    private var resolvedSummaryText: String {
        guard let res = interaction.resolution else { return "Resolved" }
        let base: String
        switch res.kind {
        case .approve:
            base = "Approved"
        case .signedIn:
            base = "Signed in"
        case .decline:
            base = interaction.kind == .signIn ? "Not now" : "Declined"
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
        if !res.at.isEmpty {
            return "\(base) · \(NoriDateFormatting.shortDateTime(res.at))"
        }
        return base
    }

    // MARK: - Pending Body Dispatcher

    @ViewBuilder
    private var pendingBody: some View {
        switch interaction.payload {
        case .approval(let payload):
            approvalBody(payload)
        case .question(let payload):
            questionBody(payload)
        case .accountSelection(let payload):
            accountSelectionBody(payload)
        case .signIn(let payload):
            signInBody(payload)
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

    // MARK: - Approval Card

    private func approvalBody(_ payload: ApprovalPayload) -> some View {
        let isSensitiveSend = payload.effect == .sensitiveSend
        let parsed = parseApprovalSummary(payload)

        return VStack(alignment: .leading, spacing: 10) {
            if let warning = payload.warning?.trimmingCharacters(in: .whitespacesAndNewlines), !warning.isEmpty {
                NoriNotice(
                    title: warning,
                    tone: .warning,
                    systemImage: "exclamationmark.triangle.fill"
                )
            }

            if let head = parsed.head, !head.isEmpty {
                Text(head)
                    .font(.system(size: 13.5, weight: .medium))
                    .foregroundStyle(NoriColors.ink)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if let quotedBody = parsed.quotedBody, !quotedBody.isEmpty {
                let canExpand = shouldOfferQuotedBodyExpansion(quotedBody)
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
                            .strokeBorder(NoriColors.hairline, lineWidth: 1)
                    )

                    if quotedBody.trimmingCharacters(in: .whitespacesAndNewlines).hasSuffix("…") {
                        Text("Only the start of the message is shown here. The rest is sent too.")
                            .font(.system(size: 11.5))
                            .foregroundStyle(NoriColors.ink2)
                    }
                }
            }

            // Key-Value Metadata Grid
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 6) {
                if isSensitiveSend, let recipients = payload.recipients, !recipients.isEmpty {
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

                GridRow {
                    Text(isSensitiveSend ? "From" : "Account")
                        .font(.system(size: 12.5))
                        .foregroundStyle(NoriColors.ink3)
                    Text(payload.account.label)
                        .font(.system(size: 12.5, weight: .medium))
                        .foregroundStyle(NoriColors.ink)
                        .fixedSize(horizontal: false, vertical: true)
                }

                if !isSensitiveSend, let recipients = payload.recipients, !recipients.isEmpty {
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

                if let toolID = payload.toolID, !toolID.isEmpty {
                    GridRow {
                        Text("Tool")
                            .font(.system(size: 12.5))
                            .foregroundStyle(NoriColors.ink3)
                        Text(toolID)
                            .font(NoriTypography.monoSmall)
                            .foregroundStyle(NoriColors.ink)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }

                GridRow {
                    Text("Effect")
                        .font(.system(size: 12.5))
                        .foregroundStyle(NoriColors.ink3)
                    NoriPill(
                        payload.effect.displayLabel,
                        tone: isSensitiveSend ? .warning : .neutral
                    )
                }

                if let expiresAt = interaction.expiresAt, !expiresAt.isEmpty {
                    GridRow {
                        Text("Expires")
                            .font(.system(size: 12.5))
                            .foregroundStyle(NoriColors.ink3)
                        Text(NoriDateFormatting.shortDateTime(expiresAt))
                            .font(.system(size: 12.5, weight: .medium))
                            .foregroundStyle(NoriColors.warning)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }

            // Material Effects with Shield Icon
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

            // Primary 2-Button Approval Actions + deliberate secondary rule checkbox
            let canRememberRule = supportsRememberRule(for: payload)
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    Button {
                        submitApprove(rememberRule: canRememberRule && rememberSimilarRule)
                    } label: {
                        Text(
                            submittingAction == "approve_always"
                                ? "Saving rule…"
                                : (submittingAction == "approve_once" ? "Approving…" : "Approve")
                        )
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: compact))
                    .disabled(isSubmitting)

                    Button {
                        submitDecline()
                    } label: {
                        Text(submittingAction == "decline" ? "Declining…" : "Decline")
                    }
                    .buttonStyle(NoriButtonStyle(.neutral, fullWidth: true, compact: compact))
                    .disabled(isSubmitting)
                }

                if canRememberRule {
                    Button {
                        rememberSimilarRule.toggle()
                    } label: {
                        HStack(spacing: 8) {
                            selectionIndicator(isSelected: rememberSimilarRule, isMulti: true)
                            Text("Always allow similar")
                                .font(.system(size: 12.5, weight: .medium))
                                .foregroundStyle(rememberSimilarRule ? NoriColors.ink : NoriColors.ink2)
                            Spacer(minLength: 0)
                        }
                        .padding(.vertical, 3)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .disabled(isSubmitting)
                    .accessibilityLabel("Always allow similar")
                    .accessibilityValue(rememberSimilarRule ? "Selected" : "Not selected")
                }
            }
            .padding(.top, 4)
        }
    }

    private func supportsRememberRule(for payload: ApprovalPayload) -> Bool {
        true
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
        if let head = payload.summaryHead, !head.isEmpty {
            return (head, payload.summary)
        }
        if payload.effect == .sensitiveSend, let newline = payload.summary.firstIndex(of: "\n") {
            let head = String(payload.summary[..<newline])
            let rest = String(payload.summary[payload.summary.index(after: newline)...])
                .trimmingCharacters(in: .whitespacesAndNewlines)
            return (head, rest.isEmpty ? "(No message text)" : rest)
        }
        return (payload.summary, nil)
    }

    // MARK: - Question Card

    @ViewBuilder
    private func questionBody(_ payload: QuestionPayload) -> some View {
        switch payload.input {
        case .confirm:
            HStack(spacing: 8) {
                Button {
                    submitAnswer(text: "Yes", optionIDs: nil)
                } label: {
                    Text("Yes")
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(isSubmitting)

                Button {
                    submitAnswer(text: "No", optionIDs: nil)
                } label: {
                    Text("No")
                }
                .buttonStyle(NoriButtonStyle(.neutral, fullWidth: true))
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
                                selectionIndicator(isSelected: isSelected, isMulti: isMulti)
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
                                        isSelected ? NoriColors.accent : NoriColors.hairline,
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
                        submitAnswer(text: pickedLabels, optionIDs: orderedIDs)
                    } label: {
                        Text(submittingAction == "answer" ? "Sending…" : "Send answer")
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(isSubmitting || selectedOptionIDs.isEmpty)
                    .padding(.top, 6)
                }
            } else {
                freeTextQuestionForm(payload: payload)
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
                    submitAnswer(text: formatter.string(from: dateAnswer), optionIDs: nil)
                } label: {
                    Text(submittingAction == "answer" ? "Sending…" : "Send answer")
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(isSubmitting)
            }

        case .text:
            freeTextQuestionForm(payload: payload)
        }
    }

    private func freeTextQuestionForm(payload: QuestionPayload) -> some View {
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
                submitAnswer(text: trimmed, optionIDs: nil)
            } label: {
                Text(submittingAction == "answer" ? "Sending…" : "Send answer")
            }
            .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
            .disabled(isSubmitting || trimmed.isEmpty)
        }
    }

    @ViewBuilder
    private func selectionIndicator(isSelected: Bool, isMulti: Bool) -> some View {
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

    // MARK: - Account Selection Card

    private func accountSelectionBody(_ payload: AccountSelectionPayload) -> some View {
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
                    submitChooseAccount(connectionID: connectionID)
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
                            .strokeBorder(NoriColors.hairline, lineWidth: 1)
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

    // MARK: - Sign In Card

    private func signInBody(_ payload: SignInPayload) -> some View {
        let isMCP = payload.integrationID == "mcp"
        let server = isMCP
            ? store.mcpServers.first(where: { $0.id == payload.connectionID })
            : nil
        let connection = !isMCP
            ? store.connections.first(where: { $0.id == payload.connectionID || $0.integrationID == payload.integrationID })
            : nil
        let displayTitle = server?.name ?? connection?.title ?? payload.integrationID.capitalized
        let safeURL = payload.authorizeURL.flatMap { Self.safeAuthorizeURL($0) }
        let blockedUnsafeURL = payload.authorizeURL != nil && safeURL == nil

        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: "key.fill")
                    .font(.system(size: 14, weight: .semibold))
                    .foregroundStyle(NoriColors.accent)
                    .frame(width: 30, height: 30)
                    .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 9, style: .continuous))

                VStack(alignment: .leading, spacing: 1) {
                    Text(displayTitle)
                        .font(.system(size: 13.5, weight: .semibold))
                        .foregroundStyle(NoriColors.ink)
                    if let accountLabel = connection?.account?.label {
                        Text(accountLabel)
                            .font(.system(size: 12))
                            .foregroundStyle(NoriColors.ink2)
                    } else if isMCP {
                        Text("OAuth sign-in via ASWebAuthenticationSession")
                            .font(.system(size: 12))
                            .foregroundStyle(NoriColors.ink2)
                    }
                }
            }

            if let scopes = payload.requestedScopes, !scopes.isEmpty {
                Text("Will request: \(scopes.joined(separator: ", "))")
                    .font(.system(size: 12))
                    .foregroundStyle(NoriColors.ink2)
            }

            if blockedUnsafeURL {
                NoriNotice(
                    title: "This sign-in link isn’t one Nori issued, so it won’t open.",
                    tone: .danger,
                    systemImage: "exclamationmark.shield.fill"
                )
            }

            HStack(spacing: 8) {
                Button {
                    startSignInFlow(
                        payload: payload,
                        safeURL: safeURL,
                        server: server,
                        connection: connection,
                        displayTitle: displayTitle
                    )
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: "key")
                            .font(.system(size: 13, weight: .semibold))
                        Text(submittingAction == "sign_in" ? "Opening sign-in…" : "Sign in")
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(isSubmitting || blockedUnsafeURL)

                Button {
                    submitDecline()
                } label: {
                    Text(submittingAction == "decline" ? "Declining…" : "Not now")
                }
                .buttonStyle(NoriButtonStyle(.neutral, fullWidth: true))
                .disabled(isSubmitting)
            }
            .padding(.top, 2)
        }
    }

    private static func safeAuthorizeURL(_ raw: String) -> URL? {
        guard let url = URL(string: raw), let scheme = url.scheme?.lowercased() else { return nil }
        return scheme == "https" ? url : nil
    }

    // MARK: - Submission Helpers

    private func seedInitialSelectionsIfNeeded() {
        if !didSeedInitialChoice, case .question(let payload) = interaction.payload {
            didSeedInitialChoice = true
            if payload.input == .choice, let first = payload.options?.first, selectedOptionIDs.isEmpty {
                selectedOptionIDs = [first.id]
            }
        }
        if !didSeedAccountDefault, case .accountSelection(let payload) = interaction.payload {
            didSeedAccountDefault = true
            rememberAccountAsDefault = payload.rememberAsDefault ?? false
        }
    }

    private func submitApprove(rememberRule: Bool = false) {
        errorMessage = nil
        isSubmitting = true
        submittingAction = rememberRule ? "approve_always" : "approve_once"
        Task {
            await store.approveInteraction(interaction, rememberRule: rememberRule)
            triggerNotificationHaptic(.success)
            isSubmitting = false
            submittingAction = nil
        }
    }

    private func submitDecline() {
        errorMessage = nil
        isSubmitting = true
        submittingAction = "decline"
        Task {
            await store.declineInteraction(interaction)
            triggerNotificationHaptic(.warning)
            isSubmitting = false
            submittingAction = nil
        }
    }

    private func submitAnswer(text: String, optionIDs: [String]?) {
        errorMessage = nil
        isSubmitting = true
        submittingAction = "answer"
        Task {
            await store.answerInteraction(interaction, text: text, optionIDs: optionIDs)
            triggerNotificationHaptic(.success)
            isSubmitting = false
            submittingAction = nil
        }
    }

    private func submitChooseAccount(connectionID: String) {
        errorMessage = nil
        isSubmitting = true
        submittingAction = connectionID
        Task {
            await store.chooseAccountInteraction(
                interaction,
                connectionID: connectionID,
                rememberAsDefault: rememberAccountAsDefault
            )
            triggerNotificationHaptic(.success)
            isSubmitting = false
            submittingAction = nil
        }
    }

    private func startSignInFlow(
        payload: SignInPayload,
        safeURL: URL?,
        server: MCPServer?,
        connection: Connection?,
        displayTitle: String
    ) {
        errorMessage = nil
        isSubmitting = true
        submittingAction = "sign_in"

        Task {
            defer {
                isSubmitting = false
                submittingAction = nil
            }

            let targetURL: URL
            if let safeURL {
                targetURL = safeURL
            } else if let server {
                let attempt = await store.startMCPOAuth(serverID: server.id)
                guard let attempt else {
                    await refreshAfterSignIn()
                    if store.lastError == nil {
                        triggerNotificationHaptic(.success)
                    }
                    return
                }
                guard let attemptURL = Self.safeAuthorizeURL(attempt.authorizeURL) else {
                    errorMessage = "The server returned an invalid sign-in link."
                    return
                }
                targetURL = attemptURL
            } else if let connID = connection?.id ?? payload.connectionID {
                let attempt = await store.connectConnection(connectionID: connID)
                guard let attempt else {
                    await refreshAfterSignIn()
                    if store.lastError == nil {
                        triggerNotificationHaptic(.success)
                    }
                    return
                }
                guard let attemptURL = Self.safeAuthorizeURL(attempt.authorizeURL) else {
                    errorMessage = "The server returned an invalid sign-in link."
                    return
                }
                targetURL = attemptURL
            } else {
                errorMessage = "Sign-in link expired. Reconnect from More → Apps & MCP to resume this task."
                return
            }

            do {
                let callbackURL = try await webAuthenticationSession.authenticate(
                    using: targetURL,
                    callback: .customScheme("nori"),
                    preferredBrowserSession: .ephemeral,
                    additionalHeaderFields: [:]
                )
                let outcome = Self.parseOAuthCallback(callbackURL)
                switch outcome.result {
                case "connected":
                    store.showToast(server != nil ? "Signed in to \(displayTitle)" : "Connected \(displayTitle)")
                    triggerNotificationHaptic(.success)
                case "cancelled", "canceled":
                    store.showToast("Sign-in cancelled")
                    triggerNotificationHaptic(.warning)
                case "failed":
                    let message = outcome.reason.map { "Sign-in failed: \($0)" } ?? "Sign-in failed for \(displayTitle)"
                    errorMessage = message
                    store.showToast(message)
                    triggerNotificationHaptic(.warning)
                default:
                    triggerNotificationHaptic(.success)
                }
                await refreshAfterSignIn()
            } catch {
                let nsError = error as NSError
                if nsError.domain == ASWebAuthenticationSessionErrorDomain,
                   nsError.code == ASWebAuthenticationSessionError.canceledLogin.rawValue {
                    store.showToast("Sign-in cancelled")
                    return
                }
                errorMessage = error.localizedDescription
            }
        }
    }

    private func refreshAfterSignIn() async {
        async let appsTask: Void = store.refreshApps()
        async let workTask: Void = store.refreshWorkAndInteractions()
        async let summaryTask: Void = store.refreshSummary()
        _ = await (appsTask, workTask, summaryTask)
    }

    private static func parseOAuthCallback(_ url: URL) -> (result: String?, reason: String?) {
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        let map = Dictionary(
            items.compactMap { item in item.value.map { (item.name.lowercased(), $0) } },
            uniquingKeysWith: { _, latest in latest }
        )
        let result = map["result"]?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let rawReason = map["reason"]?.trimmingCharacters(in: .whitespacesAndNewlines)
        let reason = (rawReason?.isEmpty == false)
            ? rawReason?.replacingOccurrences(of: "_", with: " ")
            : nil
        return (result, reason)
    }

    private enum HapticOutcome {
        case success
        case warning
    }

    private func triggerNotificationHaptic(_ outcome: HapticOutcome) {
        #if os(iOS)
        let generator = UINotificationFeedbackGenerator()
        generator.prepare()
        generator.notificationOccurred(outcome == .success ? .success : .warning)
        #endif
    }
}
