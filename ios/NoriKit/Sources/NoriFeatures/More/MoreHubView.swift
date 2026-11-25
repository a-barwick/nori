import SwiftUI
import NoriCore
import NoriDesign

public enum MoreRoute: Hashable, Sendable {
    case apps
    case mcpDetail(String)
    case memory
    case settings(SettingsSection?)
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

    private var memoryBarColor: Color {
        if store.coreMemoryRatio >= 0.98 { return NoriColors.danger }
        if store.coreMemoryRatio >= 0.85 { return NoriColors.warning }
        return NoriColors.brandPrimary
    }

    private var permissionsDetail: String {
        let count = store.permissions.count
        let noun = count == 1 ? "rule" : "rules"
        return "\(count) \(noun) · deny-wins"
    }

    private var preferencesDetail: String {
        "\(store.preferences.timezone) · $\(String(format: "%.2f", store.preferences.budgets.taskUSD)) / task"
    }

    public var body: some View {
        VStack(spacing: 0) {
            header

            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    heroCard
                        .padding(.top, 12)

                    NoriSectionHeader("Workspace")

                    workspacePair

                    NoriSectionHeader("Controls & Account")

                    controlsAndAccountRows
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 96)
            }
            .refreshable {
                async let summaryTask: Void = store.refreshSummary()
                async let appsTask: Void = store.refreshApps()
                async let memoryTask: Void = store.refreshMemory()
                async let permsPrefsTask: Void = store.refreshPermissionsAndPreferences()
                _ = await (summaryTask, appsTask, memoryTask, permsPrefsTask)
            }
        }
        .background(NoriColors.canvas)
        .navigationTitle("More")
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
    }

    // MARK: - Header

    private var header: some View {
        HStack(alignment: .center) {
            Text("More")
                .font(.system(size: 26, weight: .bold))
                .foregroundStyle(NoriColors.ink)
            Spacer()
        }
        .padding(.horizontal, 16)
        .padding(.top, 10)
        .padding(.bottom, 10)
        .background(
            NoriTopWash()
                .ignoresSafeArea(edges: .top)
        )
    }

    // MARK: - Your Nori Hero Card

    private var heroCard: some View {
        NoriHeroCard(padding: 16) {
            HStack(alignment: .center, spacing: 14) {
                ZStack {
                    RoundedRectangle(cornerRadius: 14, style: .continuous)
                        .fill(NoriColors.surface)
                        .overlay(
                            RoundedRectangle(cornerRadius: 14, style: .continuous)
                                .strokeBorder(NoriColors.hairline, lineWidth: 1)
                        )
                        .shadow(color: NoriColors.shadowColor, radius: 6, x: 0, y: 2)

                    NoriMark(size: 30, state: store.markState)
                }
                .frame(width: 50, height: 50)

                VStack(alignment: .leading, spacing: 3) {
                    Text("Your Nori")
                        .font(.system(size: 16, weight: .bold))
                        .foregroundStyle(NoriColors.ink)

                    Text(store.authState.email ?? "Signed in")
                        .font(.system(size: 13.5))
                        .foregroundStyle(NoriColors.ink2)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                if store.connectionState == .reconnecting {
                    NoriPill("Reconnecting", tone: .warning)
                } else if store.connectionState == .offline {
                    NoriPill("Offline", tone: .danger)
                }
            }
        }
    }

    // MARK: - Workspace Pair

    private var workspacePair: some View {
        HStack(alignment: .top, spacing: 12) {
            NavigationLink(value: MoreRoute.apps) {
                WorkspaceCard(
                    icon: "square.grid.2x2",
                    title: "Apps & MCP",
                    subtitle: appsSummary,
                    badgeText: store.appsAttentionCount > 0 ? "\(store.appsAttentionCount) attention" : nil,
                    badgeTone: .warning
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.memory) {
                WorkspaceCard(
                    icon: "brain.head.profile",
                    title: "Memory",
                    subtitle: memoryRatioString
                ) {
                    GeometryReader { proxy in
                        let fillWidth = store.coreMemoryUsedBytes > 0
                            ? max(6, proxy.size.width * store.coreMemoryRatio)
                            : 0
                        ZStack(alignment: .leading) {
                            Capsule()
                                .fill(NoriColors.fill2)
                            if fillWidth > 0 {
                                Capsule()
                                    .fill(memoryBarColor)
                                    .frame(width: min(proxy.size.width, fillWidth))
                            }
                        }
                    }
                    .frame(height: 4)
                }
            }
            .buttonStyle(.plain)
        }
        .fixedSize(horizontal: false, vertical: true)
    }

    // MARK: - Controls & Account

    private var controlsAndAccountRows: some View {
        NoriRows {
            NavigationLink(value: MoreRoute.settings(.permissions)) {
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

            NavigationLink(value: MoreRoute.settings(.preferences)) {
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

            NavigationLink(value: MoreRoute.settings(.appearance)) {
                NoriRow(
                    title: "Appearance",
                    detail: "\(appearance.displayLabel) · Theme & preview",
                    systemImage: appearance.systemImage,
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.settings(.account)) {
                NoriRow(
                    title: "Account & Diagnostics",
                    detail: "Google Sign-In · Diagnostics",
                    systemImage: "person.crop.circle",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: false
                )
            }
            .buttonStyle(.plain)
        }
    }
}

// MARK: - Workspace Card

private struct WorkspaceCard<Footer: View>: View {
    let icon: String
    let title: String
    let subtitle: String
    let badgeText: String?
    let badgeTone: NoriTone
    private let footer: Footer

    init(
        icon: String,
        title: String,
        subtitle: String,
        badgeText: String? = nil,
        badgeTone: NoriTone = .warning,
        @ViewBuilder footer: () -> Footer
    ) {
        self.icon = icon
        self.title = title
        self.subtitle = subtitle
        self.badgeText = badgeText
        self.badgeTone = badgeTone
        self.footer = footer()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .center, spacing: 6) {
                Image(systemName: icon)
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(NoriColors.accent)
                    .frame(width: 36, height: 36)
                    .background(NoriColors.accentSubtle, in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Spacer(minLength: 4)

                if let badgeText {
                    NoriPill(badgeText, tone: badgeTone)
                }
            }

            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)
                    .lineLimit(1)

                Text(subtitle)
                    .font(.system(size: 12.5))
                    .foregroundStyle(NoriColors.ink2)
                    .lineLimit(1)
                    .minimumScaleFactor(0.9)
            }

            Spacer(minLength: 0)

            footer
        }
        .padding(14)
        .frame(maxWidth: .infinity, minHeight: 124, maxHeight: .infinity, alignment: .topLeading)
        .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
    }
}

private extension WorkspaceCard where Footer == EmptyView {
    init(
        icon: String,
        title: String,
        subtitle: String,
        badgeText: String? = nil,
        badgeTone: NoriTone = .warning
    ) {
        self.init(
            icon: icon,
            title: title,
            subtitle: subtitle,
            badgeText: badgeText,
            badgeTone: badgeTone,
            footer: { EmptyView() }
        )
    }
}
