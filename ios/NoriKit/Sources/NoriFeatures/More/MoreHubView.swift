import SwiftUI
import NoriCore
import NoriDesign

public enum MoreRoute: Hashable, Sendable {
    case memory
    case routines
    case goals
    case settings(SettingsSection?)
}

public struct MoreHubView: View {
    @Bindable private var store: NoriStore

    public init(store: NoriStore) {
        self.store = store
    }

    private let columns = [
        GridItem(.flexible(), spacing: 10),
        GridItem(.flexible(), spacing: 10)
    ]

    private var enabledAutomationsCount: Int {
        store.automations.filter { $0.status == .enabled }.count
    }

    private var nextAutomationSummary: String {
        if let next = store.automations.first(where: { $0.status == .enabled }) {
            let when = next.nextAt ?? next.trigger.summaryLabel
            return "Next: \(next.title), \(when)"
        }
        return "No active automations"
    }

    private var blockedGoalsCount: Int {
        store.goals.filter(\.isBlocked).count
    }

    private var activeGoalsCount: Int {
        store.goals.filter { $0.status == .active }.count
    }

    private var serverPill: (label: String, tone: NoriTone) {
        switch store.connectionState {
        case .demo: return ("Demo", .accent)
        case .connected: return ("Live", .ok)
        case .connecting: return ("Connecting", .neutral)
        case .reconnecting: return ("Retrying", .warning)
        case .offline: return ("Offline", .danger)
        }
    }

    private var serverIconTone: NoriTone {
        switch store.connectionState {
        case .demo: return .accent
        case .connected: return .ok
        case .connecting, .reconnecting: return .warning
        case .offline: return .danger
        }
    }

    private var memoryBudgetString: String {
        let usedKB = Double(store.coreMemoryUsedBytes) / 1024.0
        let budgetKB = Double(store.coreMemoryBudgetBytes) / 1024.0
        return String(format: "%.1f / %.0f KB used", usedKB, budgetKB)
    }

    private var capabilitiesSummary: String {
        let count = store.capabilities?.assistantFeatures?.values.filter { $0 }.count ?? 0
        if count > 0 {
            return "\(store.profile.mode.displayLabel) · \(count) capabilities"
        }
        return store.profile.mode.displayLabel
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                LazyVGrid(columns: columns, spacing: 10) {
                    // Memory Tile
                    NavigationLink(value: MoreRoute.memory) {
                        HubTileCard(
                            icon: "brain.head.profile",
                            iconTone: .accent,
                            pillText: "\(store.coreMemories.count) core",
                            pillTone: .neutral,
                            title: "Memory",
                            subtitle: memoryBudgetString
                        ) {
                            GeometryReader { proxy in
                                ZStack(alignment: .leading) {
                                    Capsule()
                                        .fill(NoriColors.fill2)
                                    Capsule()
                                        .fill(memoryBarColor)
                                        .frame(width: max(6, proxy.size.width * store.coreMemoryRatio))
                                }
                            }
                            .frame(height: 6)
                        }
                    }
                    .buttonStyle(.plain)

                    // Automations Tile
                    NavigationLink(value: MoreRoute.routines) {
                        HubTileCard(
                            icon: "repeat",
                            iconTone: .accent,
                            pillText: "\(enabledAutomationsCount) on",
                            pillTone: enabledAutomationsCount > 0 ? .ok : .neutral,
                            title: "Automations",
                            subtitle: nextAutomationSummary
                        )
                    }
                    .buttonStyle(.plain)

                    // Goals Tile
                    NavigationLink(value: MoreRoute.goals) {
                        HubTileCard(
                            icon: "target",
                            iconTone: blockedGoalsCount > 0 ? .warning : .accent,
                            pillText: blockedGoalsCount > 0 ? "\(blockedGoalsCount) blocked" : "\(activeGoalsCount) active",
                            pillTone: blockedGoalsCount > 0 ? .warning : .accent,
                            title: "Goals",
                            subtitle: "Track multi-step outcomes & criteria"
                        )
                    }
                    .buttonStyle(.plain)

                    // Server Tile
                    NavigationLink(value: MoreRoute.settings(.server)) {
                        HubTileCard(
                            icon: "server.rack",
                            iconTone: serverIconTone,
                            pillText: serverPill.label,
                            pillTone: serverPill.tone,
                            title: "Server",
                            subtitle: store.profile.name
                        )
                    }
                    .buttonStyle(.plain)
                }
                .padding(.top, 4)

                NoriSectionHeader("Settings")

                NoriRows {
                    NavigationLink(value: MoreRoute.settings(.permissions)) {
                        NoriRow(
                            title: "Permissions",
                            detail: "\(store.permissions.count) standing rules (deny-wins)",
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
                            detail: "\(store.preferences.timezone) · $\(String(format: "%.2f", store.preferences.budgets.taskUSD)) / task",
                            systemImage: "slider.horizontal.3",
                            iconTone: .neutral,
                            showChevron: true,
                            showDivider: true
                        )
                    }
                    .buttonStyle(.plain)

                    NavigationLink(value: MoreRoute.settings(.diagnostics)) {
                        NoriRow(
                            title: "Diagnostics",
                            detail: capabilitiesSummary,
                            systemImage: "waveform.path.ecg",
                            iconTone: .neutral,
                            showChevron: true,
                            showDivider: false
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 16)
            .padding(.bottom, 28)
        }
        .background(NoriColors.canvas)
        .navigationTitle("More")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.large)
        #endif
        .refreshable {
            await store.refreshAll()
        }
    }

    private var memoryBarColor: Color {
        if store.coreMemoryRatio >= 0.98 { return NoriColors.danger }
        if store.coreMemoryRatio >= 0.85 { return NoriColors.warning }
        return NoriColors.accent
    }
}

// MARK: - Hub Tile Card

private struct HubTileCard<Footer: View>: View {
    let icon: String
    let iconTone: NoriTone
    let pillText: String
    let pillTone: NoriTone
    let title: String
    let subtitle: String
    private let footer: Footer

    init(
        icon: String,
        iconTone: NoriTone,
        pillText: String,
        pillTone: NoriTone,
        title: String,
        subtitle: String,
        @ViewBuilder footer: () -> Footer
    ) {
        self.icon = icon
        self.iconTone = iconTone
        self.pillText = pillText
        self.pillTone = pillTone
        self.title = title
        self.subtitle = subtitle
        self.footer = footer()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .center) {
                Image(systemName: icon)
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(iconTone.foreground)
                    .frame(width: 36, height: 36)
                    .background(iconTone.background, in: RoundedRectangle(cornerRadius: 10, style: .continuous))

                Spacer(minLength: 4)

                NoriPill(pillText, tone: pillTone)
            }

            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(NoriColors.ink)
                Text(subtitle)
                    .font(.system(size: 12))
                    .foregroundStyle(NoriColors.ink2)
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)
            }

            Spacer(minLength: 0)

            footer
        }
        .padding(14)
        .frame(maxWidth: .infinity, minHeight: 132, alignment: .topLeading)
        .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.hairline, lineWidth: 1)
        )
    }
}

private extension HubTileCard where Footer == EmptyView {
    init(
        icon: String,
        iconTone: NoriTone,
        pillText: String,
        pillTone: NoriTone,
        title: String,
        subtitle: String
    ) {
        self.init(
            icon: icon,
            iconTone: iconTone,
            pillText: pillText,
            pillTone: pillTone,
            title: title,
            subtitle: subtitle,
            footer: { EmptyView() }
        )
    }
}
