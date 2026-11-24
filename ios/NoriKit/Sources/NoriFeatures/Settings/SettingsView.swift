import SwiftUI
import NoriCore
import NoriDesign

public enum SettingsSection: String, Hashable, Sendable {
    case account
    case permissions
    case preferences
    case diagnostics
}

public struct SettingsView: View {
    @Bindable private var store: NoriStore
    public let initialSection: SettingsSection?

    @State private var showingEditPreferences = false

    public init(store: NoriStore, initialSection: SettingsSection? = nil) {
        self.store = store
        self.initialSection = initialSection
    }

    public var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    accountSection
                        .id(SettingsSection.account)

                    permissionsSection
                        .id(SettingsSection.permissions)

                    preferencesSection
                        .id(SettingsSection.preferences)

                    diagnosticsSection
                        .id(SettingsSection.diagnostics)
                }
                .padding(.horizontal, 16)
                .padding(.top, 4)
                .padding(.bottom, 32)
            }
            .onAppear {
                if let initialSection {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.08) {
                        withAnimation(.easeOut(duration: 0.2)) {
                            proxy.scrollTo(initialSection, anchor: .top)
                        }
                    }
                }
            }
        }
        .background(NoriColors.canvas)
        .navigationTitle("Settings")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .sheet(isPresented: $showingEditPreferences) {
            EditPreferencesSheet(store: store)
                .presentationDetents([.medium])
                .presentationDragIndicator(.visible)
        }
    }

    // MARK: - Account

    @ViewBuilder
    private var accountSection: some View {
        NoriSectionHeader("Account") {
            NoriPill(
                store.connectionState == .connected ? "Connected" : store.connectionState.rawValue.capitalized,
                tone: store.connectionState == .connected ? .ok : .warning
            )
        }

        NoriRows {
            NoriRow(
                title: store.authState.email ?? "Signed in",
                detail: "Signed in with Google",
                systemImage: "person.crop.circle.fill",
                iconTone: .accent,
                showDivider: false
            )
        }

        Button {
            Task { await store.signOut() }
        } label: {
            Text("Sign Out")
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(NoriButtonStyle(.danger, fullWidth: true, compact: true))
        .padding(.top, 10)
    }

    // MARK: - Standing Permissions

    @ViewBuilder
    private var permissionsSection: some View {
        NoriSectionHeader("Standing permissions") {
            Text("\(store.permissions.count)")
                .font(NoriTypography.caption)
                .foregroundStyle(NoriColors.ink3)
        }

        if store.permissions.isEmpty {
            NoriCard {
                Text("No standing permissions. Nori asks before each change or external send.")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
            }
        } else {
            NoriRows {
                ForEach(Array(store.permissions.enumerated()), id: \.element.id) { index, rule in
                    let isLast = index == store.permissions.count - 1
                    NoriRow(
                        title: rule.summary ?? rule.title ?? "Permission rule",
                        detail: permissionDetail(for: rule),
                        showDivider: !isLast,
                        wrapTitle: true
                    ) {
                        NoriPill(
                            rule.effect == .deny ? "Never" : "Allowed",
                            tone: rule.effect == .deny ? .danger : .neutral
                        )
                    }
                }
            }
        }
    }

    // MARK: - Preferences & Budgets

    @ViewBuilder
    private var preferencesSection: some View {
        NoriSectionHeader("Preferences & Budgets") {
            Button("Edit") {
                showingEditPreferences = true
            }
            .font(.system(size: 13, weight: .semibold))
            .foregroundStyle(NoriColors.accent)
            .buttonStyle(.plain)
        }

        NoriRows {
            NoriRow(
                title: "Time zone",
                detail: store.preferences.timezone,
                showDivider: true
            )

            NoriRow(
                title: "Task budget",
                detail: budgetDetailString,
                showDivider: true
            )

            NoriRow(
                title: "Push notifications (APNs)",
                detail: "Approvals, questions & task completions",
                showDivider: false
            ) {
                NoriPill("On", tone: .ok)
            }
        }
    }

    private var budgetDetailString: String {
        let taskStr = String(format: "$%.2f per task", store.preferences.budgets.taskUSD)
        if let monthly = store.preferences.budgets.monthlyUSD {
            return "\(taskStr) · \(String(format: "$%.2f", monthly)) monthly cap"
        }
        return taskStr
    }

    // MARK: - Diagnostics

    @ViewBuilder
    private var diagnosticsSection: some View {
        NoriSectionHeader("Diagnostics")

        NoriCard {
            VStack(alignment: .leading, spacing: 10) {
                diagRow("Status", value: store.connectionState.rawValue.capitalized)
                diagRow("Server", value: NoriConfig.serverURL.host() ?? "nori-core")
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
        return parts.isEmpty ? "Standing rule" : parts.joined(separator: " · ")
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
                            .autocorrectionDisabled()
                            #if os(iOS)
                            .textInputAutocapitalization(.never)
                            #endif
                            .padding(.horizontal, 12)
                            .frame(height: 40)
                            .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    HStack(spacing: 12) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Per-task budget ($)")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField("1.00", text: $taskBudgetText)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                #if os(iOS)
                                .keyboardType(.decimalPad)
                                #endif
                                .padding(.horizontal, 12)
                                .frame(height: 40)
                                .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }

                        VStack(alignment: .leading, spacing: 6) {
                            Text("Monthly cap ($)")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField("40.00", text: $monthlyCapText)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                #if os(iOS)
                                .keyboardType(.decimalPad)
                                #endif
                                .padding(.horizontal, 12)
                                .frame(height: 40)
                                .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
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
            .background(NoriColors.canvas)
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
