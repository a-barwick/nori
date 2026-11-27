import SwiftUI
import NoriCore
import NoriDesign

public enum MoreRoute: Hashable, Sendable {
    case apps
    case mcpDetail(String)
    case computer
    case memory
    case permissions
    case preferences
    case apiCosts
    case models
    case appearance
    case account
    case history
    case diagnostics
}

public struct MoreHubView: View {
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue
    @Bindable private var store: NoriStore
    @State private var showingResetData = false

    public init(store: NoriStore) {
        self.store = store
    }

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    private var connectedAppsCount: Int {
        let connectedMCP = store.mcpServers.filter { $0.observed.status == .connected }.count
        let connectedConnections = store.connections.filter { $0.status == .connected }.count
        return connectedMCP + connectedConnections
    }

    private var appsSummary: String? {
        guard connectedAppsCount > 0 else { return nil }
        return "\(connectedAppsCount) connected"
    }

    private var memoryRatioString: String? {
        guard store.hasLoadedWorkingMemory else { return nil }
        let usedKB = Double(store.coreMemoryUsedBytes) / 1024.0
        let budgetKB = Double(store.coreMemoryBudgetBytes) / 1024.0
        return String(format: "%.1f / %.0f KB", usedKB, budgetKB)
    }

    private var permissionsDetail: String? {
        let count = store.permissions.count
        guard count > 0 else { return nil }
        let noun = count == 1 ? "rule" : "rules"
        return "\(count) \(noun)"
    }

    private var preferencesDetail: String? {
        guard store.hasLoadedPreferences else { return nil }
        let tz = store.preferences.timezone.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !tz.isEmpty else { return nil }
        return "\(tz) · $\(String(format: "%.2f", store.preferences.budgets.taskUSD)) / task"
    }

    private var modelsDetail: String? {
        let configuredCount = store.modelKeys.providers.filter(\.configured).count
        guard configuredCount > 0 else { return nil }
        return "\(configuredCount) configured"
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                if store.needsModelKeySetup {
                    NoriNotice(
                        title: "Choose a model to use Nori.",
                        tone: .warning,
                        systemImage: "sparkles"
                    ) {
                        NavigationLink("Choose model", value: MoreRoute.models)
                            .buttonStyle(NoriButtonStyle(.primary, compact: true))
                    }
                    .padding(.bottom, 12)
                }

                NoriSectionHeader("Workspace")
                workspaceRows

                NoriSectionHeader("Settings")
                controlsRows

                NoriSectionHeader("Account")
                accountRows
            }
            .padding(.horizontal, 16)
            .padding(.top, 14)
            .padding(.bottom, 96)
        }
        .refreshable {
            async let summaryTask: Void = store.refreshSummary()
            async let appsTask: Void = store.refreshApps()
            async let memoryTask: Void = store.refreshMemory()
            async let permsPrefsTask: Void = store.refreshPermissionsAndPreferences()
            async let modelKeysTask: Void = store.refreshModelKeys()
            async let modelSettingsTask: Void = { _ = try? await store.refreshModelSettings() }()
            async let homeConnectorsTask: Void = { _ = try? await store.refreshHomeConnectors() }()
            _ = await (summaryTask, appsTask, memoryTask, permsPrefsTask, modelKeysTask, modelSettingsTask, homeConnectorsTask)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .sheet(isPresented: $showingResetData) {
            ResetDataSheet(store: store)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        .navigationTitle("More")
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
    }


    private var workspaceRows: some View {
        NoriRows {
            NavigationLink(value: MoreRoute.apps) {
                NoriRow(
                    title: "Apps & MCP",
                    detail: appsSummary,
                    systemImage: "square.grid.2x2",
                    iconTone: .accent,
                    showChevron: true,
                    showDivider: true
                ) {
                    if store.appsAttentionCount > 0 {
                        NoriPill("\(store.appsAttentionCount) attention", tone: .warning)
                    }
                }
            }
            .buttonStyle(.plain)

            if store.isComputerEnabled {
                NavigationLink(value: MoreRoute.computer) {
                    NoriRow(
                        title: "Computer",
                        detail: store.computerStatus?.status.displayLabel,
                        systemImage: "desktopcomputer",
                        iconTone: .accent,
                        showChevron: true,
                        showDivider: true
                    )
                }
                .buttonStyle(.plain)
            }

            NavigationLink(value: MoreRoute.memory) {
                NoriRow(
                    title: "Memory",
                    detail: memoryRatioString,
                    systemImage: "brain.head.profile",
                    iconTone: .accent,
                    showChevron: true,
                    showDivider: false
                )
            }
            .buttonStyle(.plain)
        }
    }


    private var controlsRows: some View {
        NoriRows {
            NavigationLink(value: MoreRoute.permissions) {
                NoriRow(
                    title: "Standing Permissions",
                    detail: permissionsDetail,
                    systemImage: "checkmark.shield",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.preferences) {
                NoriRow(
                    title: "Preferences & Budgets",
                    detail: preferencesDetail,
                    systemImage: "slider.horizontal.3",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.apiCosts) {
                NoriRow(
                    title: "API Costs",
                    systemImage: "chart.bar",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.appearance) {
                NoriRow(
                    title: "Appearance",
                    detail: appearance.displayLabel,
                    systemImage: appearance.systemImage,
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.models) {
                NoriRow(
                    title: "Models",
                    detail: modelsDetail,
                    systemImage: "sparkles",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                ) {
                    if store.needsModelKeySetup {
                        NoriPill("Required", tone: .warning)
                    }
                }
            }
            .buttonStyle(.plain)

            Button {
                showingResetData = true
            } label: {
                NoriRow(
                    title: "Reset Data",
                    systemImage: "arrow.counterclockwise",
                    iconTone: .danger,
                    showChevron: true,
                    showDivider: false
                )
            }
            .buttonStyle(.plain)
        }
    }


    private var accountRows: some View {
        NoriRows {
            NavigationLink(value: MoreRoute.account) {
                NoriRow(
                    title: "Account",
                    detail: store.authState.email,
                    systemImage: "person.crop.circle",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.history) {
                NoriRow(
                    title: "Action History",
                    systemImage: "clock.arrow.circlepath",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.diagnostics) {
                NoriRow(
                    title: "Diagnostics",
                    systemImage: "waveform.path.ecg",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: false
                ) {
                    if store.connectionState == .reconnecting {
                        NoriPill("Reconnecting", tone: .warning)
                    } else if store.connectionState == .offline {
                        NoriPill("Offline", tone: .danger)
                    }
                }
            }
            .buttonStyle(.plain)
        }
    }
}
