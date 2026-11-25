import SwiftUI
import NoriCore
import NoriDesign

// MARK: - Standing Permissions Screen

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
            parts.append("Automation")
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

// MARK: - Preferences & Budgets Screen

public struct PreferencesBudgetsView: View {
    @Bindable private var store: NoriStore
    @State private var showingEditPreferences = false

    public init(store: NoriStore) {
        self.store = store
    }

    private var budgetDetailString: String {
        let taskStr = String(format: "$%.2f per task", store.preferences.budgets.taskUSD)
        if let monthly = store.preferences.budgets.monthlyUSD {
            return "\(taskStr) · \(String(format: "$%.2f", monthly)) monthly cap"
        }
        return taskStr
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                NoriRows {
                    NoriRow(
                        title: "Time zone",
                        detail: store.preferences.timezone,
                        showDivider: true
                    )

                    NoriRow(
                        title: "Task budget",
                        detail: budgetDetailString,
                        showDivider: false
                    )
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
        .navigationTitle("Preferences & Budgets")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button("Edit") {
                    showingEditPreferences = true
                }
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(NoriColors.accent)
            }
        }
        .sheet(isPresented: $showingEditPreferences) {
            EditPreferencesSheet(store: store)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
    }
}

// MARK: - Appearance Screen

public struct AppearanceSettingsView: View {
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue
    @Namespace private var appearancePickerNamespace

    public init() {}

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                NoriCard(padding: 14) {
                    VStack(alignment: .leading, spacing: 14) {
                        // Interactive appearance preview reflecting live palette & mascot
                        VStack(alignment: .leading, spacing: 10) {
                            HStack {
                                Spacer(minLength: 32)
                                Text("What do I have tomorrow?")
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(NoriColors.onAccent)
                                    .padding(.horizontal, 11)
                                    .padding(.vertical, 7)
                                    .background(NoriColors.brandPrimary, in: Capsule())
                            }

                            HStack(alignment: .top, spacing: 7) {
                                NoriMark(size: 20, state: .idle)
                                    .padding(.top, 1)
                                Text("Two morning meetings and a reminder at noon.")
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(NoriColors.ink)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(
                            NoriColors.canvas,
                            in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                                .strokeBorder(NoriColors.hairline, lineWidth: 1)
                        )

                        // System / Light / Dark segmented control
                        HStack(spacing: 4) {
                            ForEach(NoriAppearance.allCases) { option in
                                let isSelected = appearance == option
                                Button {
                                    withAnimation(.spring(response: 0.24, dampingFraction: 0.86)) {
                                        appearanceRawValue = option.rawValue
                                    }
                                } label: {
                                    HStack(spacing: 5) {
                                        Image(systemName: option.systemImage)
                                            .font(.system(size: 12, weight: .semibold))
                                        Text(option.displayLabel)
                                            .font(.system(size: 13, weight: .semibold))
                                    }
                                    .foregroundStyle(isSelected ? NoriColors.ink : NoriColors.ink2)
                                    .frame(maxWidth: .infinity)
                                    .frame(height: 34)
                                    .background {
                                        if isSelected {
                                            Capsule()
                                                .fill(Color.adaptive(light: 0xFFFFFF, dark: 0x2C253D))
                                                .overlay(
                                                    Capsule()
                                                        .strokeBorder(NoriColors.hairline, lineWidth: 1)
                                                )
                                                .matchedGeometryEffect(id: "noriAppearanceSelection", in: appearancePickerNamespace)
                                        }
                                    }
                                    .contentShape(Capsule())
                                }
                                .buttonStyle(.plain)
                                .accessibilityAddTraits(isSelected ? .isSelected : [])
                            }
                        }
                        .padding(3)
                        .background(NoriColors.elevated, in: Capsule())
                        .overlay(
                            Capsule()
                                .strokeBorder(NoriColors.hairline, lineWidth: 1)
                        )
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .navigationTitle("Appearance")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
    }
}

// MARK: - Account Screen

public struct AccountSettingsView: View {
    @Bindable private var store: NoriStore

    public init(store: NoriStore) {
        self.store = store
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                NoriRows {
                    NoriRow(
                        title: store.authState.email ?? "Signed in",
                        systemImage: "person.crop.circle.fill",
                        iconTone: .accent,
                        showDivider: false
                    ) {
                        if store.connectionState == .reconnecting {
                            NoriPill("Reconnecting", tone: .warning)
                        } else if store.connectionState == .offline {
                            NoriPill("Offline", tone: .danger)
                        }
                    }
                }

                Button {
                    Task { await store.signOut() }
                } label: {
                    Text("Sign Out")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(NoriButtonStyle(.danger, fullWidth: true, compact: true))
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .refreshable {
            await store.refreshSummary()
        }
        .background(NoriColors.canvas)
        .navigationTitle("Account")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
    }
}

// MARK: - Diagnostics Screen

public struct DiagnosticsSettingsView: View {
    @Bindable private var store: NoriStore
    @State private var isPingingServer = false

    public init(store: NoriStore) {
        self.store = store
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                NoriCard {
                    VStack(alignment: .leading, spacing: 10) {
                        diagRow("Status", value: store.connectionState.rawValue.capitalized)
                        diagRow("Server", value: NoriConfig.serverURL.host() ?? "nori-core")
                        if let ping = store.lastPing {
                            let pingTime = NoriDateFormatting.shortTime(ISO8601DateFormatter().string(from: ping.checkedAt))
                            let pingSummary = ping.ok
                                ? "Healthy · \(ping.latencyMs) ms · \(pingTime)"
                                : "\(ping.errorMessage ?? ping.status) · \(ping.latencyMs) ms · \(pingTime)"
                            diagRow("Health", value: pingSummary)
                        }
                        if let caps = store.capabilities {
                            let contracts = [caps.contract, caps.conversationContract, caps.assistantContract]
                                .compactMap { $0 }
                                .joined(separator: " · ")
                            diagRow("Contracts", value: contracts, monospaced: true)
                            if let model = caps.model, !model.isEmpty {
                                diagRow("Model", value: model, monospaced: true)
                            }
                            if let features = caps.assistantFeatures {
                                let enabled = features.filter(\.value).map(\.key).sorted()
                                if !enabled.isEmpty {
                                    VStack(alignment: .leading, spacing: 6) {
                                        Text("Connected capabilities")
                                            .font(.system(size: 12, weight: .medium))
                                            .foregroundStyle(NoriColors.ink3)
                                        FlowCapabilitiesView(items: enabled)
                                    }
                                    .padding(.top, 2)
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
            await store.refreshSummary()
        }
        .background(NoriColors.canvas)
        .navigationTitle("Diagnostics")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button(isPingingServer ? "Checking…" : "Check health") {
                    pingServerHealth()
                }
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(NoriColors.accent)
                .disabled(isPingingServer)
            }
        }
    }

    private func pingServerHealth() {
        guard !isPingingServer else { return }
        isPingingServer = true
        Task {
            defer { isPingingServer = false }
            let result = await store.pingServer()
            store.showToast(
                result.ok
                    ? "Server reachable (\(result.latencyMs) ms)"
                    : (result.errorMessage ?? "Server unreachable")
            )
        }
    }

    private func diagRow(_ label: String, value: String, monospaced: Bool = false) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text(label)
                .font(.system(size: 12.5, weight: .medium))
                .foregroundStyle(NoriColors.ink3)
                .frame(width: 76, alignment: .leading)
            Text(value)
                .font(monospaced ? NoriTypography.monoSmall : .system(size: 13, weight: .medium))
                .foregroundStyle(NoriColors.ink)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

// MARK: - Capability Pills Wrap

private struct FlowCapabilitiesView: View {
    let items: [String]

    var body: some View {
        let columns = [GridItem(.adaptive(minimum: 82), spacing: 6)]
        LazyVGrid(columns: columns, alignment: .leading, spacing: 6) {
            ForEach(items, id: \.self) { name in
                NoriPill(name, tone: .accent)
            }
        }
    }
}

// MARK: - Edit Preferences Sheet

private struct EditPreferencesSheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss

    @State private var timezone = ""
    @State private var taskBudgetText = ""
    @State private var monthlyCapText = ""
    @State private var isSaving = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Time zone")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("America/Los_Angeles", text: $timezone)
                            .textFieldStyle(.plain)
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

                    HStack(spacing: 12) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Per-task budget ($)")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField("1.00", text: $taskBudgetText)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                #if os(iOS)
                                .keyboardType(.decimalPad)
                                #endif
                                .padding(.horizontal, 12)
                                .frame(height: 40)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }

                        VStack(alignment: .leading, spacing: 6) {
                            Text("Monthly cap ($)")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField("40.00", text: $monthlyCapText)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                #if os(iOS)
                                .keyboardType(.decimalPad)
                                #endif
                                .padding(.horizontal, 12)
                                .frame(height: 40)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }
                    }

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(.white)
                        } else {
                            Text("Save preferences")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(isSaving)
                    .padding(.top, 6)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Preferences")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(NoriColors.ink2)
                }
            }
            .onAppear {
                timezone = store.preferences.timezone
                taskBudgetText = String(format: "%.2f", store.preferences.budgets.taskUSD)
                monthlyCapText = store.preferences.budgets.monthlyUSD.map { String(format: "%.2f", $0) } ?? ""
            }
        }
    }

    private func save() {
        isSaving = true
        var updated = store.preferences
        let trimmedTZ = timezone.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedTZ.isEmpty {
            updated.timezone = trimmedTZ
        }
        if let taskVal = Double(taskBudgetText), taskVal >= 0 {
            updated.budgets.taskUSD = taskVal
        }
        let trimmedMonthly = monthlyCapText.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmedMonthly.isEmpty {
            updated.budgets.monthlyUSD = nil
        } else if let monthlyVal = Double(trimmedMonthly), monthlyVal >= 0 {
            updated.budgets.monthlyUSD = monthlyVal
        }
        Task {
            await store.updatePreferences(updated)
            isSaving = false
            dismiss()
        }
    }
}

// MARK: - Add Permission Rule Sheet

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

                    // 3-State Rule Effect Selector
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
                                                isSelected ? tone.foreground : NoriColors.hairline,
                                                lineWidth: isSelected ? 1.5 : 1
                                            )
                                    )
                                    .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    // Target Picker
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

                    // Tool ID Filter
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

                    // Action Classes
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
                                            .strokeBorder(isSelected ? NoriColors.accent : NoriColors.hairline, lineWidth: 1)
                                    )
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    // Recipients (for sensitive sends)
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

                    // Plain-English Summary Preview
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
            if !parsedRecipients.isEmpty {
                recipients = parsedRecipients
            } else {
                recipients = ["*"]
            }
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
