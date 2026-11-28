import SwiftUI
import NoriCore
import NoriDesign

public enum MoreRoute: Hashable, Sendable {
    case apps
    case mcpDetail(String)
    case personalization
    case memory
    case permissions
    case notifications
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

    public init(store: NoriStore) {
        self.store = store
    }

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
    }

    private var modelsPillLabel: String? {
        if store.needsModelKeySetup && store.needsExaKeySetup {
            return "2 keys"
        }
        if store.needsModelKeySetup {
            return "Model key"
        }
        if store.needsExaKeySetup {
            return "Web search"
        }
        return nil
    }

    private var showDriveNudge: Bool {
        store.hasLoadedConnectionsForNudge &&
        store.isGoogleIntegrationAvailable &&
        !store.isGoogleDriveConnected &&
        !store.isGoogleDriveNudgeDismissed
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                if let block = store.activeBillingAccessBlock {
                    BillingAccessBannerView(block: block, allowBYOK: store.allowBYOK) {
                        if !block.isSuspended {
                            NavigationLink("Plan", value: MoreRoute.apiCosts)
                                .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                        }
                    }
                    .padding(.bottom, 12)
                }

                if store.needsModelKeySetup {
                    keyAlertBanner(
                        title: "Connect an AI model to get started",
                        detail: "Add an AI model key in Models so Nori can chat and run tasks for you.",
                        background: NoriColors.warningBg,
                        titleColor: NoriColors.warning,
                        detailColor: NoriColors.warning.opacity(0.9),
                        actionColor: NoriColors.warning
                    )
                    .padding(.bottom, 10)
                }

                if store.needsExaKeySetup {
                    keyAlertBanner(
                        title: "Enable web search",
                        detail: "For the best experience, enable web search by adding a web search key in Models.",
                        background: NoriColors.accentSubtle,
                        titleColor: NoriColors.ink,
                        detailColor: NoriColors.ink2,
                        actionColor: NoriColors.accent
                    )
                    .padding(.bottom, 12)
                }

                if showDriveNudge {
                    driveNudgeCard
                        .padding(.bottom, 14)
                }

                NoriSectionHeader("Nori")
                noriRows

                NoriSectionHeader("General")
                settingsRows

                NoriSectionHeader("Account")
                accountRows
            }
            .padding(.horizontal, 16)
            .padding(.top, 14)
            .padding(.bottom, 32)
            .noriReadableColumn()
        }
        .task {
            if !store.isGoogleDriveNudgeDismissed && !store.hasLoadedConnectionsForNudge {
                await store.refreshApps(force: false)
            }
        }
        .refreshable {
            async let moreTask: Void = store.refreshForTab(.more, force: true)
            async let appsTask: Void = store.refreshApps(force: true)
            async let modelSettingsTask: Void = { _ = try? await store.refreshModelSettings() }()
            async let homeConnectorsTask: Void = { _ = try? await store.refreshHomeConnectors() }()
            _ = await (moreTask, appsTask, modelSettingsTask, homeConnectorsTask)
        }
        .background(NoriColors.canvas.ignoresSafeArea())
        .navigationTitle("Settings")
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
    }

    private func keyAlertBanner(
        title: String,
        detail: String,
        background: Color,
        titleColor: Color,
        detailColor: Color,
        actionColor: Color
    ) -> some View {
        HStack(alignment: .center, spacing: 10) {
            HStack(alignment: .top, spacing: 9) {
                Circle()
                    .fill(NoriColors.danger)
                    .frame(width: 8, height: 8)
                    .padding(.top, 5)
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.system(size: 14, weight: .semibold))
                        .foregroundStyle(titleColor)
                    Text(detail)
                        .font(.system(size: 12.5))
                        .foregroundStyle(detailColor)
                }
            }
            Spacer(minLength: 8)
            NavigationLink("Add key", value: MoreRoute.models)
                .font(.system(size: 13, weight: .semibold))
                .foregroundStyle(actionColor)
                .underline()
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(background, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
    }

    private var driveNudgeCard: some View {
        HStack(spacing: 12) {
            NoriBrandIcon(.googleDrive, size: 38)

            VStack(alignment: .leading, spacing: 2) {
                Text("Connect Google Drive")
                    .font(NoriTypography.rowTitle)
                    .foregroundStyle(NoriColors.ink)
                Text("Let Nori reference the docs and files you choose.")
                    .font(.system(size: 12.5))
                    .foregroundStyle(NoriColors.ink2)
            }
            .frame(maxWidth: .infinity, alignment: .leading)

            NavigationLink("Connect", value: MoreRoute.apps)
                .buttonStyle(NoriButtonStyle(.primary, compact: true))

            Button {
                store.dismissGoogleDriveNudge()
            } label: {
                Image(systemName: "xmark")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(NoriColors.ink3)
                    .frame(width: 26, height: 26)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Dismiss Google Drive suggestion")
        }
        .padding(13)
        .background(NoriColors.surface, in: RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: NoriRadius.card, style: .continuous)
                .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
        )
    }

    private var noriRows: some View {
        NoriRows {
            NavigationLink(value: MoreRoute.apps) {
                NoriRow(
                    title: "Apps",
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

            NavigationLink(value: MoreRoute.personalization) {
                NoriRow(
                    title: "Personalization",
                    systemImage: "wand.and.stars",
                    iconTone: .accent,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.memory) {
                NoriRow(
                    title: "Memory",
                    systemImage: "brain.head.profile",
                    iconTone: .accent,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.permissions) {
                NoriRow(
                    title: "Permissions",
                    systemImage: "checkmark.shield",
                    iconTone: .accent,
                    showChevron: true,
                    showDivider: false
                )
            }
            .buttonStyle(.plain)
        }
    }

    private var settingsRows: some View {
        NoriRows {
            NavigationLink(value: MoreRoute.notifications) {
                NoriRow(
                    title: "Notifications",
                    systemImage: "bell",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.models) {
                NoriRow(
                    title: "Models",
                    systemImage: "sparkles",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                ) {
                    if let pillLabel = modelsPillLabel {
                        HStack(spacing: 5) {
                            Circle()
                                .fill(NoriColors.danger)
                                .frame(width: 6, height: 6)
                                .accessibilityHidden(true)
                            Text(pillLabel)
                                .font(.system(size: 11.5, weight: .semibold))
                        }
                        .foregroundStyle(NoriColors.warning)
                        .padding(.horizontal, 8.5)
                        .frame(height: 23)
                        .background(NoriColors.warningBg, in: Capsule())
                    }
                }
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.apiCosts) {
                NoriRow(
                    title: "Usage & Budgets",
                    systemImage: "chart.bar",
                    iconTone: .neutral,
                    showChevron: true,
                    showDivider: true
                )
            }
            .buttonStyle(.plain)

            NavigationLink(value: MoreRoute.preferences) {
                NoriRow(
                    title: "Preferences",
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
                    systemImage: appearance.systemImage,
                    iconTone: .neutral,
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
                    systemImage: "person.crop.circle",
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
