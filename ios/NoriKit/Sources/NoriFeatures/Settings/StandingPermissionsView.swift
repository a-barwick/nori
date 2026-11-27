import SwiftUI
import NoriCore
import NoriDesign

private enum PermissionEffectFilter: Hashable, CaseIterable {
    case all
    case deny
    case ask
    case allow

    var label: String {
        switch self {
        case .all: return "All"
        case .deny: return "Never allow"
        case .ask: return "Ask first"
        case .allow: return "Allow automatically"
        }
    }

    var effect: PermissionRule.RuleEffect? {
        switch self {
        case .all: return nil
        case .deny: return .deny
        case .ask: return .ask
        case .allow: return .allow
        }
    }
}

public struct StandingPermissionsView: View {
    @Bindable private var store: NoriStore
    @State private var showingAddPermissionRule = false
    @State private var rulePendingRemoval: PermissionRule?
    @State private var selectedFilter: PermissionEffectFilter = .all
    @State private var isLoading = false
    @State private var hasLoaded = false
    @State private var loadError: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var filteredRules: [PermissionRule] {
        guard let effect = selectedFilter.effect else { return store.permissions }
        return store.permissions.filter { $0.effect == effect }
    }

    private var orderedPermissionGroups: [(effect: PermissionRule.RuleEffect, rules: [PermissionRule])] {
        let precedence: [PermissionRule.RuleEffect] = [.deny, .ask, .allow]
        return precedence.compactMap { effect in
            let matched = filteredRules.filter { $0.effect == effect }
            return matched.isEmpty ? nil : (effect, matched)
        }
    }

    private func count(for filter: PermissionEffectFilter) -> Int {
        guard let effect = filter.effect else { return store.permissions.count }
        return store.permissions.filter { $0.effect == effect }.count
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

                if let loadError {
                    NoriNotice(title: "Could not load permissions", detail: loadError, tone: .danger) {
                        Button("Retry") {
                            Task { await loadPermissions() }
                        }
                        .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                    }
                }

                if isLoading && !hasLoaded && store.permissions.isEmpty {
                    NoriCard {
                        HStack(spacing: 10) {
                            ProgressView().tint(NoriColors.accent)
                            Text("Loading standing permissions…")
                                .font(NoriTypography.meta)
                                .foregroundStyle(NoriColors.ink2)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                } else if store.permissions.isEmpty {
                    NoriEmptyState(
                        title: "No standing permissions",
                        message: "Nori asks before each change or send."
                    ) {
                        Button("Add rule") {
                            showingAddPermissionRule = true
                        }
                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                    }
                } else {
                    effectFilterBar

                    if orderedPermissionGroups.isEmpty {
                        NoriEmptyState(
                            title: "No \(selectedFilter.label.lowercased()) rules",
                            message: "Choose another filter or add a new rule."
                        )
                    } else {
                        VStack(alignment: .leading, spacing: 6) {
                            ForEach(orderedPermissionGroups, id: \.effect) { group in
                                VStack(alignment: .leading, spacing: 0) {
                                    NoriSectionHeader(group.effect.displayLabel)

                                    NoriRows {
                                        ForEach(Array(group.rules.enumerated()), id: \.element.id) { index, rule in
                                            let isLast = index == group.rules.count - 1
                                            NoriRow(
                                                title: rule.summary ?? rule.title ?? "Permission rule",
                                                detail: permissionDetail(for: rule),
                                                showDivider: !isLast,
                                                wrapTitle: true
                                            ) {
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
        .task {
            await loadPermissions()
        }
        .refreshable {
            await loadPermissions()
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
                .presentationDetents([.medium, .large])
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

    private var effectFilterBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(PermissionEffectFilter.allCases, id: \.self) { filter in
                    let isSelected = selectedFilter == filter
                    let itemCount = count(for: filter)
                    Button {
                        selectedFilter = filter
                    } label: {
                        HStack(spacing: 5) {
                            Text(filter.label)
                            if itemCount > 0 {
                                Text("\(itemCount)")
                                    .foregroundStyle(isSelected ? NoriColors.chipSelectedFg.opacity(0.8) : NoriColors.ink3)
                            }
                        }
                        .font(NoriTypography.caption)
                        .foregroundStyle(isSelected ? NoriColors.chipSelectedFg : NoriColors.ink2)
                        .padding(.horizontal, 12)
                        .frame(height: 30)
                        .background(
                            isSelected ? NoriColors.chipSelectedBg : NoriColors.fill1,
                            in: Capsule()
                        )
                        .overlay(
                            Capsule()
                                .strokeBorder(
                                    isSelected ? NoriColors.chipSelectedBorder : NoriColors.cardBorder,
                                    lineWidth: 1
                                )
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    private func loadPermissions() async {
        isLoading = true
        loadError = nil
        defer { isLoading = false }
        do {
            try await store.refreshPermissionsThrowing()
            hasLoaded = true
        } catch is CancellationError {
            return
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func resolveConnectionLabel(_ id: String) -> String {
        if id == "*" {
            return "All tools & MCP servers"
        }
        if let conn = store.connections.first(where: { $0.id == id }) {
            let label = [conn.title, conn.account?.label]
                .compactMap { $0 }
                .filter { !$0.isEmpty }
                .joined(separator: " · ")
            return label.isEmpty ? conn.title : label
        }
        if let server = store.mcpServers.first(where: { $0.id == id }) {
            return server.name
        }
        return "Connected app"
    }

    private func permissionDetail(for rule: PermissionRule) -> String {
        var parts: [String] = []
        if !rule.actionClasses.isEmpty {
            parts.append(rule.actionClasses.map(\.displayLabel).joined(separator: ", "))
        }
        if rule.connectionIDs.contains("*") {
            parts.append("All tools & MCP servers")
        } else if !rule.connectionIDs.isEmpty {
            let resolved = rule.connectionIDs.map(resolveConnectionLabel)
            parts.append(resolved.joined(separator: ", "))
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

    @State private var effect: PermissionRule.RuleEffect = .allow
    @State private var targetConnectionID: String = "*"
    @State private var toolIDInput: String = ""
    @State private var selectedClasses: Set<Effect> = [.reversibleWrite]
    @State private var recipientsInput: String = ""
    @State private var hasExpiry: Bool = false
    @State private var expiryDate: Date = Calendar.current.date(byAdding: .day, value: 30, to: Date()) ?? Date()
    @State private var isSaving: Bool = false

    private let selectableEffects: [Effect] = [.read, .reversibleWrite, .sensitiveSend, .unknown]

    private var targetOptions: [(id: String, label: String)] {
        var options: [(id: String, label: String)] = [("*", "All tools & MCP servers")]
        for conn in store.connections {
            let label = [conn.title, conn.account?.label]
                .compactMap { $0 }
                .filter { !$0.isEmpty }
                .joined(separator: " · ")
            options.append((conn.id, label.isEmpty ? conn.title : label))
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
        targetOptions.first(where: { $0.id == targetConnectionID })?.label ?? "this account"
    }

    private func actionPhrase(for actionClass: Effect) -> String {
        switch actionClass {
        case .read: return "read"
        case .reversibleWrite: return "make undoable changes"
        case .sensitiveSend: return "send"
        case .unknown: return "use tools with custom actions"
        case .control: return "run control actions"
        }
    }

    private var previewText: String {
        let actions = orderedClasses.map(actionPhrase).joined(separator: " or ")
        let actionPart = actions.isEmpty ? "act" : actions
        let recipientClause = selectedClasses.contains(.sensitiveSend) && !parsedRecipients.isEmpty
            ? " to \(parsedRecipients.joined(separator: ", "))"
            : ""
        switch effect {
        case .allow:
            return "Nori will automatically \(actionPart) with \(targetDisplayName)\(recipientClause)."
        case .ask:
            return "Nori will always ask first before it can \(actionPart) with \(targetDisplayName)\(recipientClause)."
        case .deny:
            return "Nori will never \(actionPart) with \(targetDisplayName)\(recipientClause)."
        }
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
                                    }
                                    .padding(.horizontal, 12)
                                    .padding(.vertical, 10)
                                    .background(
                                        isSelected ? tone.background : NoriColors.elevated,
                                        in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                    )
                                    .overlay(
                                        RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                            .strokeBorder(
                                                isSelected ? tone.border : NoriColors.cardBorder,
                                                lineWidth: 1
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
                        .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    VStack(alignment: .leading, spacing: 8) {
                        Text("Actions")
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
                                            .foregroundStyle(isSelected ? NoriColors.chipSelectedFg : NoriColors.ink3)
                                        Text(actionClass.displayLabel)
                                            .font(.system(size: 12.5, weight: .medium))
                                            .foregroundStyle(isSelected ? NoriColors.chipSelectedFg : NoriColors.ink)
                                            .lineLimit(1)
                                    }
                                    .padding(.horizontal, 10)
                                    .frame(maxWidth: .infinity, minHeight: 36, alignment: .leading)
                                    .background(
                                        isSelected ? NoriColors.chipSelectedBg : NoriColors.elevated,
                                        in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                    )
                                    .overlay(
                                        RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                            .strokeBorder(isSelected ? NoriColors.chipSelectedBorder : NoriColors.cardBorder, lineWidth: 1)
                                    )
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Specific tool (optional)")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("Any tool, or mcp/server_id/tool_name", text: $toolIDInput)
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

                    if selectedClasses.contains(.sensitiveSend) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text(effect == .allow ? "Only to" : "Only to (optional)")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField(
                                effect == .allow ? "sam@example.com, team@acme.com" : "Any recipient, or sam@example.com",
                                text: $recipientsInput
                            )
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

                    VStack(alignment: .leading, spacing: 8) {
                        Toggle(isOn: $hasExpiry) {
                            Text("Expires on a date")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                        }
                        .tint(NoriColors.brandPrimary)

                        if hasExpiry {
                            DatePicker(
                                "Until",
                                selection: $expiryDate,
                                in: Date()...,
                                displayedComponents: .date
                            )
                            .datePickerStyle(.compact)
                            .tint(NoriColors.accent)
                            .foregroundStyle(NoriColors.ink)
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
                        .disabled(isSaving)
                }
            }
            .interactiveDismissDisabled(isSaving)
        }
    }

    private func effectSubtitle(_ value: PermissionRule.RuleEffect) -> String {
        switch value {
        case .allow: return "Run without prompting when no Ask first or Never allow rule applies."
        case .ask: return "Always prompt for approval, overriding Allow automatically rules."
        case .deny: return "Always block this action, overriding every other rule."
        }
    }

    private func endOfDayISO(_ date: Date) -> String {
        var components = Calendar.current.dateComponents([.year, .month, .day], from: date)
        components.hour = 23
        components.minute = 59
        components.second = 59
        let target = Calendar.current.date(from: components) ?? date
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: target)
    }

    private func save() {
        isSaving = true
        let recipients: [String]?
        if selectedClasses.contains(.sensitiveSend) {
            recipients = parsedRecipients.isEmpty ? nil : parsedRecipients
        } else {
            recipients = nil
        }
        let expiresAt = hasExpiry ? endOfDayISO(expiryDate) : nil
        let rule = PermissionRuleEditable(
            effect: effect,
            toolIDs: parsedToolIDs,
            connectionIDs: [targetConnectionID],
            resourceIDs: ["*"],
            actionClasses: orderedClasses,
            recipients: recipients,
            expiresAt: expiresAt
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
