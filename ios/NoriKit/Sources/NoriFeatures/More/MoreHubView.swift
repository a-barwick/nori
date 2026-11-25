import SwiftUI
import NoriCore
import NoriDesign

public enum MoreRoute: Hashable, Sendable {
    case apps
    case mcpDetail(String)
    case memory
    case permissions
    case preferences
    case appearance
    case account
    case diagnostics
}

public struct MoreHubView: View {
    @AppStorage(NoriAppearance.storageKey) private var appearanceRawValue: String = NoriAppearance.system.rawValue
    @Bindable private var store: NoriStore

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

    private var totalAppsCount: Int {
        store.mcpServers.count + store.connections.count
    }

    private var appsSummary: String {
        if connectedAppsCount > 0 {
            return "\(connectedAppsCount) connected"
        }
        return "\(totalAppsCount) configured"
    }

    private var memoryRatioString: String {
        let usedKB = Double(store.coreMemoryUsedBytes) / 1024.0
        let budgetKB = Double(store.coreMemoryBudgetBytes) / 1024.0
        return String(format: "%.1f / %.0f KB", usedKB, budgetKB)
    }

    private var permissionsDetail: String {
        let count = store.permissions.count
        let noun = count == 1 ? "rule" : "rules"
        return "\(count) \(noun) · deny-wins"
    }

    private var preferencesDetail: String {
        "\(store.preferences.timezone) · $\(String(format: "%.2f", store.preferences.budgets.taskUSD)) / task"
    }

    private var diagnosticsDetail: String {
        let host = NoriConfig.serverURL.host() ?? "nori-core"
        return "\(store.connectionState.rawValue.capitalized) · \(host)"
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                NoriSectionHeader("Workspace")
                workspaceRows

                NoriSectionHeader("Controls")
                controlsRows

                NoriSectionHeader("Account")
                accountRows
            }
            .padding(.horizontal, 16)
            .padding(.top, 4)
            .padding(.bottom, 96)
        }
        .refreshable {
            async let summaryTask: Void = store.refreshSummary()
            async let appsTask: Void = store.refreshApps()
            async let memoryTask: Void = store.refreshMemory()
            async let permsPrefsTask: Void = store.refreshPermissionsAndPreferences()
            _ = await (summaryTask, appsTask, memoryTask, permsPrefsTask)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .navigationTitle("More")
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
    }

    // MARK: - Workspace

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

    // MARK: - Controls

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

            NavigationLink(value: MoreRoute.appearance) {
                NoriRow(
                    title: "Appearance",
                    detail: appearance.displayLabel,
                    systemImage: appearance.systemImage,
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: false
                )
            }
            .buttonStyle(.plain)
        }
    }

    // MARK: - Account

    private var accountRows: some View {
        NoriRows {
            NavigationLink(value: MoreRoute.account) {
                NoriRow(
                    title: "Account",
                    detail: store.authState.email ?? "Signed in",
                    systemImage: "person.crop.circle",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                ) {
                    if store.connectionState == .reconnecting {
                        NoriPill("Reconnecting", tone: .warning)
                    } else if store.connectionState == .offline {
                        NoriPill("Offline", tone: .danger)
                    }
                }
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.diagnostics) {
                NoriRow(
                    title: "Diagnostics",
                    detail: diagnosticsDetail,
                    systemImage: "waveform.path.ecg",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: false
                )
            }
            .buttonStyle(.plain)
        }
    }
}
