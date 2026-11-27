import SwiftUI
import NoriCore
import NoriDesign

public enum MoreRoute: Hashable, Sendable {
    case apps
    case mcpDetail(String)
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

    public init(store: NoriStore) {
        self.store = store
    }

    private var appearance: NoriAppearance {
        NoriAppearance(storedRawValue: appearanceRawValue)
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

                NoriSectionHeader("Nori")
                noriRows

                NoriSectionHeader("Settings")
                settingsRows

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
        .navigationTitle("More")
        #if os(iOS)
        .toolbar(.hidden, for: .navigationBar)
        #endif
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
            NavigationLink(value: MoreRoute.models) {
                NoriRow(
                    title: "Models",
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
