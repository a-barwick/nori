import SwiftUI
import NoriCore
import NoriDesign

public struct StandingPermissionsView: View {
    @Bindable private var store: NoriStore
    @State private var confirmRuleID: String?

    public init(store: NoriStore) {
        self.store = store
    }

    private var allowedRules: [PermissionRule] {
        store.permissions.filter { $0.effect == .allow }
    }

    private var askRules: [PermissionRule] {
        store.permissions.filter { $0.effect == .ask }
    }

    private var blockedRules: [PermissionRule] {
        store.permissions.filter { $0.effect == .deny }
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let error = store.permissionsRead.error {
                    NoriRetryNotice("Couldn’t load permissions", detail: error) {
                        try? await store.refreshPermissionsThrowing()
                    }
                }

                if store.permissions.isEmpty && store.permissionsRead == .pending {
                    NoriLoadingState("Loading permissions…")
                } else if store.permissions.isEmpty && store.permissionsRead == .loaded {
                    NoriEmptyState(
                        title: "No standing permissions",
                        message: "Nori asks before making changes or sending messages."
                    )
                } else {
                    if !allowedRules.isEmpty {
                        ruleGroup(title: "Allowed", rules: allowedRules, dotColor: NoriColors.ok)
                    }
                    if !askRules.isEmpty {
                        ruleGroup(title: "Ask first", rules: askRules, dotColor: NoriColors.warning)
                    }
                    if !blockedRules.isEmpty {
                        ruleGroup(title: "Blocked", rules: blockedRules, dotColor: NoriColors.danger)
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 96)
        }
        .task {
            try? await store.refreshPermissionsThrowing()
        }
        .refreshable {
            try? await store.refreshPermissionsThrowing()
        }
        .background(NoriColors.canvas)
        .navigationTitle("Permissions")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
    }

    private func ruleGroup(title: String, rules: [PermissionRule], dotColor: Color) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            NoriSectionHeader(title)
            NoriRows {
                ForEach(Array(rules.enumerated()), id: \.element.id) { index, rule in
                    let isLast = index == rules.count - 1
                    let confirming = confirmRuleID == rule.id
                    VStack(spacing: 0) {
                        HStack(alignment: .center, spacing: 11) {
                            Circle()
                                .fill(dotColor)
                                .frame(width: 8, height: 8)
                                .accessibilityHidden(true)

                            VStack(alignment: .leading, spacing: 2) {
                                Text(rule.summary ?? rule.title ?? "Permission")
                                    .font(NoriTypography.body)
                                    .foregroundStyle(NoriColors.ink)
                                    .fixedSize(horizontal: false, vertical: true)

                                if let expiresAt = rule.expiresAt, !expiresAt.isEmpty {
                                    Text(NoriDateFormatting.expiryCountdown(expiresAt))
                                        .font(NoriTypography.caption)
                                        .foregroundStyle(NoriColors.ink3)
                                }
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)

                            if confirming {
                                HStack(spacing: 6) {
                                    Button("Remove") {
                                        confirmRuleID = nil
                                        Task { await store.revokePermissionRule(id: rule.id) }
                                    }
                                    .buttonStyle(NoriButtonStyle(.danger, compact: true))

                                    Button("Keep") {
                                        confirmRuleID = nil
                                    }
                                    .buttonStyle(NoriButtonStyle(.quiet, compact: true))
                                }
                            } else {
                                Button {
                                    confirmRuleID = rule.id
                                } label: {
                                    Image(systemName: "xmark")
                                        .font(.system(size: 12, weight: .semibold))
                                        .foregroundStyle(NoriColors.ink3)
                                        .frame(width: 28, height: 28)
                                        .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                                .accessibilityLabel("Remove rule")
                            }
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 12)

                        if !isLast {
                            Rectangle()
                                .fill(NoriColors.hairline)
                                .frame(height: 1)
                                .padding(.leading, 33)
                        }
                    }
                }
            }
        }
    }

}
