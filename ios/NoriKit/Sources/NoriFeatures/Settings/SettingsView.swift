import SwiftUI
import NoriCore
import NoriDesign

public enum SettingsSection: String, Hashable, Sendable {
    case server
    case permissions
    case preferences
    case diagnostics
}

public struct SettingsView: View {
    @Bindable private var store: NoriStore
    public let initialSection: SettingsSection?

    @State private var selectedProfileID: String = ""
    @State private var urlText: String = ""
    @State private var emailText: String = ""
    @State private var apiPrefixText: String = ""
    @State private var bearerToken: String = ""
    @State private var isTestingConnection = false
    @State private var isSwitchingServer = false

    @State private var showingEditPreferences = false

    public init(store: NoriStore, initialSection: SettingsSection? = nil) {
        self.store = store
        self.initialSection = initialSection
    }

    private var activeDraftProfile: ServerProfile {
        let base = store.savedProfiles.first(where: { $0.id == selectedProfileID }) ?? store.profile
        var copy = base
        if copy.mode != .demo {
            let trimmedURL = urlText.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmedURL.isEmpty {
                copy.baseURLString = trimmedURL
            }
            copy.email = emailText.trimmingCharacters(in: .whitespacesAndNewlines)
            copy.apiPrefix = apiPrefixText.trimmingCharacters(in: .whitespacesAndNewlines)
        }
        return copy
    }

    public var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    serverSection
                        .id(SettingsSection.server)

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
                syncDraftFromStore(profile: store.profile)
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

    // MARK: - Nori Server Connection Switcher

    @ViewBuilder
    private var serverSection: some View {
        NoriSectionHeader("Nori server connection") {
            if let ping = store.lastPing {
                NoriPill(
                    ping.ok ? "\(ping.latencyMs) ms" : "Unreachable",
                    tone: ping.ok ? .ok : .danger
                )
            } else {
                NoriPill(store.profile.mode.displayLabel, tone: store.isDemoMode ? .accent : .neutral)
            }
        }

        NoriRows {
            ForEach(Array(store.savedProfiles.enumerated()), id: \.element.id) { index, preset in
                let isLast = index == store.savedProfiles.count - 1
                let isSelected = selectedProfileID == preset.id
                let isActive = store.profile.id == preset.id

                Button {
                    selectPreset(preset)
                } label: {
                    VStack(spacing: 0) {
                        HStack(alignment: .center, spacing: 11) {
                            ZStack {
                                Circle()
                                    .strokeBorder(
                                        isSelected ? NoriColors.accent : NoriColors.hairlineStrong,
                                        lineWidth: 2
                                    )
                                    .frame(width: 18, height: 18)
                                if isSelected {
                                    Circle()
                                        .fill(NoriColors.accent)
                                        .frame(width: 10, height: 10)
                                }
                            }

                            VStack(alignment: .leading, spacing: 2) {
                                Text(preset.name)
                                    .font(NoriTypography.rowTitle)
                                    .foregroundStyle(NoriColors.ink)
                                Text(preset.baseURLString)
                                    .font(NoriTypography.monoSmall)
                                    .foregroundStyle(NoriColors.ink2)
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                                Text(preset.subtitle)
                                    .font(.system(size: 12))
                                    .foregroundStyle(NoriColors.ink3)
                                    .lineLimit(1)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)

                            if isActive {
                                if let ping = store.lastPing, ping.ok {
                                    NoriPill("\(ping.latencyMs) ms", tone: .ok)
                                } else {
                                    NoriPill("Active", tone: preset.mode == .demo ? .accent : .ok)
                                }
                            }
                        }
                        .padding(.horizontal, 13)
                        .padding(.vertical, 12)
                        .contentShape(Rectangle())

                        if !isLast {
                            Rectangle()
                                .fill(NoriColors.hairline)
                                .frame(height: 1)
                                .padding(.leading, 42)
                        }
                    }
                }
                .buttonStyle(.plain)
            }
        }

        // Editable Connection Parameters (for non-demo presets)
        if activeDraftProfile.mode != .demo {
            NoriCard {
                VStack(alignment: .leading, spacing: 12) {
                    VStack(alignment: .leading, spacing: 5) {
                        Text("Server URL")
                            .font(.system(size: 12, weight: .semibold))
                            .foregroundStyle(NoriColors.ink2)
                        TextField("http://127.0.0.1:8080", text: $urlText)
                            .textFieldStyle(.plain)
                            .font(NoriTypography.mono)
                            .autocorrectionDisabled()
                            #if os(iOS)
                            .textInputAutocapitalization(.never)
                            .keyboardType(.URL)
                            #endif
                            .padding(.horizontal, 11)
                            .frame(height: 38)
                            .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                            .overlay(
                                RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                    .strokeBorder(NoriColors.hairline, lineWidth: 1)
                            )
                    }

                    if activeDraftProfile.mode == .directCore {
                        VStack(alignment: .leading, spacing: 5) {
                            Text("Local user email (NORI_LOCAL_EMAIL)")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.ink2)
                            TextField("local@nori.invalid", text: $emailText)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .autocorrectionDisabled()
                                #if os(iOS)
                                .textInputAutocapitalization(.never)
                                .keyboardType(.emailAddress)
                                #endif
                                .padding(.horizontal, 11)
                                .frame(height: 38)
                                .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                                .overlay(
                                    RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                        .strokeBorder(NoriColors.hairline, lineWidth: 1)
                                )
                        }
                    } else if activeDraftProfile.mode == .hostedBFF {
                        VStack(alignment: .leading, spacing: 5) {
                            Text("API prefix (e.g. /api or /api/nori)")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.ink2)
                            TextField("/api", text: $apiPrefixText)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .autocorrectionDisabled()
                                #if os(iOS)
                                .textInputAutocapitalization(.never)
                                #endif
                                .padding(.horizontal, 11)
                                .frame(height: 38)
                                .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                                .overlay(
                                    RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                        .strokeBorder(NoriColors.hairline, lineWidth: 1)
                                )
                        }

                        VStack(alignment: .leading, spacing: 5) {
                            Text("Bearer token (stored in iOS Keychain)")
                                .font(.system(size: 12, weight: .semibold))
                                .foregroundStyle(NoriColors.ink2)
                            SecureField("Paste OIDC / BFF access token", text: $bearerToken)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.mono)
                                .autocorrectionDisabled()
                                .padding(.horizontal, 11)
                                .frame(height: 38)
                                .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous))
                                .overlay(
                                    RoundedRectangle(cornerRadius: NoriRadius.chip, style: .continuous)
                                        .strokeBorder(NoriColors.hairline, lineWidth: 1)
                                )
                        }
                    }
                }
            }
            .padding(.top, 10)
        }

        // Apply & Live /healthz Test Row
        HStack(spacing: 8) {
            Button {
                applyAndConnect()
            } label: {
                if isSwitchingServer {
                    ProgressView().tint(.white)
                } else {
                    Text(activeDraftProfile.id == store.profile.id ? "Save & Reconnect" : "Connect Profile")
                }
            }
            .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
            .disabled(isSwitchingServer)

            Button {
                testConnection()
            } label: {
                if isTestingConnection {
                    ProgressView()
                } else {
                    Label("Test /healthz", systemImage: "antenna.radiowaves.left.and.right")
                }
            }
            .buttonStyle(NoriButtonStyle(.neutral, compact: true))
            .disabled(isTestingConnection)
        }
        .padding(.top, 10)

        if let ping = store.lastPing {
            NoriNotice(
                title: ping.ok
                    ? "Connected · \(ping.status.uppercased()) (\(ping.latencyMs) ms)"
                    : "Health check failed (\(ping.latencyMs) ms)",
                detail: ping.ok
                    ? (ping.model.map { "Model: \($0)" } ?? "Server responded cleanly to /healthz.")
                    : (ping.errorMessage ?? "Could not reach \(store.profile.baseURLString)/healthz."),
                tone: ping.ok ? .ok : .danger,
                systemImage: ping.ok ? "checkmark.circle.fill" : "exclamationmark.triangle.fill"
            )
            .padding(.top, 10)
        }

        if store.isDemoMode {
            HStack {
                Button {
                    Task { await store.triggerDemoApproval() }
                } label: {
                    Label("Simulate incoming approval", systemImage: "bell.badge")
                }
                .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                Spacer()
            }
            .padding(.top, 8)
        }
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
                diagRow("Runtime", value: store.profile.mode.displayLabel)
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

    private func syncDraftFromStore(profile: ServerProfile) {
        selectedProfileID = profile.id
        urlText = profile.baseURLString
        emailText = profile.email
        apiPrefixText = profile.apiPrefix
        bearerToken = store.loadBearerToken(for: profile.id)
    }

    private func selectPreset(_ preset: ServerProfile) {
        selectedProfileID = preset.id
        urlText = preset.baseURLString
        emailText = preset.email
        apiPrefixText = preset.apiPrefix
        bearerToken = store.loadBearerToken(for: preset.id)
    }

    private func applyAndConnect() {
        let updated = activeDraftProfile
        isSwitchingServer = true
        if updated.mode == .hostedBFF {
            store.saveBearerToken(bearerToken, for: updated.id)
        }
        Task {
            await store.switchProfile(updated)
            _ = await store.pingServer()
            isSwitchingServer = false
        }
    }

    private func testConnection() {
        isTestingConnection = true
        let draft = activeDraftProfile
        if draft.mode == .hostedBFF {
            store.saveBearerToken(bearerToken, for: draft.id)
        }
        Task {
            if draft != store.profile {
                await store.switchProfile(draft)
            }
            _ = await store.pingServer()
            isTestingConnection = false
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
