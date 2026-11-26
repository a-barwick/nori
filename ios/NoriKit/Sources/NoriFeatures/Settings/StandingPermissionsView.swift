import SwiftUI
import NoriCore
import NoriDesign

public struct StandingPermissionsView: View {
    @Bindable private var store: NoriStore
    @State private var showingAddPermissionRule = false
    @State private var rulePendingRemoval: PermissionRule?

    public init(store: NoriStore) {
        self.store = store
    }

    private var orderedPermissionGroups: [(effect: PermissionRule.RuleEffect, rules: [PermissionRule])] {
        let precedence: [PermissionRule.RuleEffect] = [.deny, .ask, .allow]
        return precedence.compactMap { effect in
            let matched = store.permissions.filter { $0.effect == effect }
            return matched.isEmpty ? nil : (effect, matched)
        }
    }

    private func pillTone(for effect: PermissionRule.RuleEffect) -> NoriTone {
        switch effect {
        case .deny: return .danger
        case .ask: return .warning
        case .allow: return .ok
        }
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                NoriNotice(
                    title: "Rule precedence",
                    detail: "Never allow beats everything; Ask first always overrides Allow automatically.",
                    tone: .neutral,
                    systemImage: "shield.lefthalf.filled"
                )

                if store.permissions.isEmpty {
                    NoriCard {
                        Text("No custom rules yet. Nori asks before each change or external send.")
                            .font(NoriTypography.meta)
                            .foregroundStyle(NoriColors.ink2)
                    }
                } else {
                    VStack(alignment: .leading, spacing: 12) {
                        ForEach(orderedPermissionGroups, id: \.effect) { group in
                            VStack(alignment: .leading, spacing: 6) {
                                Text(group.effect.displayLabel)
                                    .font(.system(size: 12, weight: .semibold))
                                    .foregroundStyle(NoriColors.ink3)
                                    .padding(.leading, 4)

                                NoriRows {
                                    ForEach(Array(group.rules.enumerated()), id: \.element.id) { index, rule in
                                        let isLast = index == group.rules.count - 1
                                        NoriRow(
                                            title: rule.summary ?? rule.title ?? "Permission rule",
                                            detail: permissionDetail(for: rule),
                                            showDivider: !isLast,
                                            wrapTitle: true
                                        ) {
                                            HStack(spacing: 6) {
                                                NoriPill(
                                                    rule.effect.badgeLabel,
                                                    tone: pillTone(for: rule.effect)
                                                )
                                                Button {
                                                    rulePendingRemoval = rule
                                                } label: {
                                                    Image(systemName: "trash")
                                                        .font(.system(size: 12.5, weight: .medium))
                                                        .foregroundStyle(NoriColors.ink3)
                                                        .frame(width: 28, height: 28)
                                                        .contentShape(Rectangle())
                                                }
                                                .buttonStyle(.plain)
                                                .accessibilityLabel("Remove rule")
                                            }
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
        .refreshable {
            await store.refreshPermissionsAndPreferences()
        }
        .background(NoriColors.canvas)
        .navigationTitle("Standing Permissions")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button("Add rule") {
                    showingAddPermissionRule = true
                }
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(NoriColors.accent)
            }
        }
        .sheet(isPresented: $showingAddPermissionRule) {
            AddPermissionRuleSheet(store: store)
                .presentationDetents([.large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        .alert(
            "Remove this rule?",
            isPresented: Binding(
                get: { rulePendingRemoval != nil },
                set: { if !$0 { rulePendingRemoval = nil } }
            ),
            presenting: rulePendingRemoval
        ) { rule in
            Button("Remove", role: .destructive) {
                Task { await store.revokePermissionRule(id: rule.id) }
                rulePendingRemoval = nil
            }
            Button("Cancel", role: .cancel) {
                rulePendingRemoval = nil
            }
        } message: { rule in
            Text(rule.summary ?? rule.title ?? "Nori will fall back to default approval behavior for this action.")
        }
    }

    private func permissionDetail(for rule: PermissionRule) -> String {
        var parts: [String] = []
        if !rule.actionClasses.isEmpty {
            parts.append(rule.actionClasses.map(\.displayLabel).joined(separator: ", "))
        }
        if rule.connectionIDs.contains("*") {
            parts.append("All tools & MCP servers")
        } else if !rule.connectionIDs.isEmpty {
            parts.append(rule.connectionIDs.joined(separator: ", "))
        }
        if rule.automationID != nil {
            parts.append("Routine")
        }
        if let recipients = rule.recipients, !recipients.isEmpty {
            parts.append("To \(recipients.joined(separator: ", "))")
        }
        if let expiresAt = rule.expiresAt, !expiresAt.isEmpty {
            parts.append(NoriDateFormatting.expiryCountdown(expiresAt))
        }
        return parts.isEmpty ? "Standing rule" : parts.joined(separator: " · ")
    }
}

private struct AddPermissionRuleSheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss

    @State private var effect: PermissionRule.RuleEffect = .ask
    @State private var targetConnectionID: String = "*"
    @State private var toolIDInput: String = "*"
    @State private var selectedClasses: Set<Effect> = [.sensitiveSend]
    @State private var recipientsInput: String = ""
    @State private var isSaving: Bool = false

    private let selectableEffects: [Effect] = [.read, .reversibleWrite, .sensitiveSend, .unknown]

    private var targetOptions: [(id: String, label: String)] {
        var options: [(id: String, label: String)] = [("*", "All tools & MCP servers")]
        for conn in store.connections {
            let label = [conn.title, conn.account?.label]
                .compactMap { $0 }
                .filter { !$0.isEmpty }
                .joined(separator: " · ")
            options.append((conn.id, label.isEmpty ? conn.id : label))
        }
        for server in store.mcpServers {
            options.append((server.id, "\(server.name) (MCP)"))
        }
        return options
    }

    private var parsedToolIDs: [String] {
        let parts = toolIDInput
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
        return parts.isEmpty ? ["*"] : parts
    }

    private var parsedRecipients: [String] {
        recipientsInput
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
    }

    private var orderedClasses: [Effect] {
        selectableEffects.filter { selectedClasses.contains($0) }
    }

    private var canSave: Bool {
        guard !isSaving, !orderedClasses.isEmpty else { return false }
        if effect == .allow && selectedClasses.contains(.sensitiveSend) && parsedRecipients.isEmpty {
            return false
        }
        return true
    }

    private var targetDisplayName: String {
        targetOptions.first(where: { $0.id == targetConnectionID })?.label ?? targetConnectionID
    }

    private var previewText: String {
        let prefix: String
        switch effect {
        case .allow: prefix = "Nori will allow automatically:"
        case .ask: prefix = "Nori will always ask first before:"
        case .deny: prefix = "Nori will never allow:"
        }
        let actions = orderedClasses.map { $0.displayLabel.lowercased() }.joined(separator: ", ")
        let tools = parsedToolIDs == ["*"] ? "any tool" : parsedToolIDs.joined(separator: ", ")
        var summary = "\(prefix) \(actions.isEmpty ? "selected actions" : actions) using \(tools) on \(targetDisplayName)"
        if selectedClasses.contains(.sensitiveSend) {
            if !parsedRecipients.isEmpty {
                summary += " to \(parsedRecipients.joined(separator: ", "))"
            } else if effect != .allow {
                summary += " to anyone"
            }
        }
        return summary + "."
    }

    private func effectTone(_ value: PermissionRule.RuleEffect) -> NoriTone {
        switch value {
        case .allow: return .ok
        case .ask: return .warning
        case .deny: return .danger
        }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    NoriNotice(
                        title: "Rule precedence",
                        detail: "Never allow beats everything; Ask first always overrides Allow automatically.",
                        tone: .neutral,
                        systemImage: "shield.lefthalf.filled"
                    )

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Rule behavior")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)

                        VStack(spacing: 6) {
                            ForEach([PermissionRule.RuleEffect.allow, .ask, .deny], id: \.self) { option in
                                let isSelected = effect == option
                                let tone = effectTone(option)
                                Button {
                                    effect = option
                                } label: {
                                    HStack(spacing: 10) {
                                        Image(systemName: isSelected ? "checkmark.circle.fill" : "circle")
                                            .font(.system(size: 16, weight: .semibold))
                                            .foregroundStyle(isSelected ? tone.foreground : NoriColors.ink3)
                                        VStack(alignment: .leading, spacing: 1) {
                                            Text(option.displayLabel)
                                                .font(.system(size: 14, weight: .semibold))
                                                .foregroundStyle(NoriColors.ink)
                                            Text(effectSubtitle(option))
                                                .font(.system(size: 12))
                                                .foregroundStyle(NoriColors.ink2)
                                        }
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                        NoriPill(option.badgeLabel, tone: tone)
                                    }
                                    .padding(.horizontal, 12)
                                    .padding(.vertical, 10)
                                    .background(
                                        isSelected ? tone.background : NoriColors.surface,
                                        in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    )
                                    .overlay(
                                        RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                            .strokeBorder(
                                                isSelected ? tone.foreground : NoriColors.cardBorder,
                                                lineWidth: isSelected ? 1.5 : 1
                                            )
                                    )
                                    .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Target")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)

                        Picker("Target", selection: $targetConnectionID) {
                            ForEach(targetOptions, id: \.id) { option in
                                Text(option.label).tag(option.id)
                            }
                        }
                        .pickerStyle(.menu)
                        .tint(NoriColors.accent)
                        .padding(.horizontal, 12)
                        .frame(maxWidth: .infinity, minHeight: 40, alignment: .leading)
                        .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Tool ID (use * for all)")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("* or gmail.send", text: $toolIDInput)
                            .textFieldStyle(.plain)
                            .font(NoriTypography.mono)
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

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Action classes")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)

                        let columns = [GridItem(.adaptive(minimum: 130), spacing: 8)]
                        LazyVGrid(columns: columns, alignment: .leading, spacing: 8) {
                            ForEach(selectableEffects, id: \.self) { actionClass in
                                let isSelected = selectedClasses.contains(actionClass)
                                Button {
                                    if isSelected {
                                        selectedClasses.remove(actionClass)
                                    } else {
                                        selectedClasses.insert(actionClass)
                                    }
                                } label: {
                                    HStack(spacing: 6) {
                                        Image(systemName: isSelected ? "checkmark.square.fill" : "square")
                                            .font(.system(size: 13.5, weight: .semibold))
                                            .foregroundStyle(isSelected ? NoriColors.accent : NoriColors.ink3)
                                        Text(actionClass.displayLabel)
                                            .font(.system(size: 12.5, weight: .medium))
                                            .foregroundStyle(NoriColors.ink)
                                            .lineLimit(1)
                                    }
                                    .padding(.horizontal, 10)
                                    .frame(maxWidth: .infinity, minHeight: 36, alignment: .leading)
                                    .background(
                                        isSelected ? NoriColors.selected : NoriColors.elevated,
                                        in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                    )
                                    .overlay(
                                        RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                            .strokeBorder(isSelected ? NoriColors.accent : NoriColors.cardBorder, lineWidth: 1)
                                    )
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    if selectedClasses.contains(.sensitiveSend) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text(effect == .allow ? "Recipients (required, or * for anyone)" : "Recipients (leave blank for anyone)")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField("sam@example.com, team@acme.com", text: $recipientsInput)
                                .textFieldStyle(.plain)
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                .autocorrectionDisabled()
                                #if os(iOS)
                                .textInputAutocapitalization(.never)
                                .keyboardType(.emailAddress)
                                #endif
                                .padding(.horizontal, 12)
                                .frame(height: 40)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }
                    }

                    Text(previewText)
                        .font(.system(size: 13))
                        .foregroundStyle(NoriColors.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(.white)
                        } else {
                            Text("Save rule")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!canSave)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Add Permission Rule")
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

    private func effectSubtitle(_ value: PermissionRule.RuleEffect) -> String {
        switch value {
        case .allow: return "Run without prompting when no Ask first or Never allow rule applies."
        case .ask: return "Always prompt for approval, overriding Allow automatically rules."
        case .deny: return "Always block this action, overriding every other rule."
        }
    }

    private func save() {
        isSaving = true
        let recipients: [String]?
        if selectedClasses.contains(.sensitiveSend) {
            recipients = parsedRecipients.isEmpty ? ["*"] : parsedRecipients
        } else {
            recipients = nil
        }
        let rule = PermissionRuleEditable(
            effect: effect,
            toolIDs: parsedToolIDs,
            connectionIDs: [targetConnectionID],
            resourceIDs: ["*"],
            actionClasses: orderedClasses,
            recipients: recipients
        )
        Task {
            let saved = await store.createPermissionRule(rule)
            isSaving = false
            if saved {
                dismiss()
            }
        }
    }
}
