import AuthenticationServices
import SwiftUI
import NoriCore
import NoriDesign
#if os(iOS)
import UIKit
#endif

public struct MCPDetailView: View {
    @Bindable private var store: NoriStore
    public let serverID: String

    @Environment(\.webAuthenticationSession) private var webAuthenticationSession
    @State private var isBusy = false
    @State private var authError: String?
    @State private var accessToken = ""
    @State private var isTokenVisible = false
    @State private var isSavingCredential = false

    public init(store: NoriStore, serverID: String) {
        self.store = store
        self.serverID = serverID
    }

    private var server: MCPServer? {
        store.mcpServers.first(where: { $0.id == serverID })
    }

    private var tools: [MCPServerTool] {
        store.mcpTools[serverID] ?? []
    }

    public var body: some View {
        ScrollView {
            if let server {
                VStack(alignment: .leading, spacing: 14) {
                    if let error = server.observed.error?.message, !error.isEmpty {
                        NoriNotice(
                            title: server.observed.status == .needsSignIn ? "Sign-in required" : "Connection error",
                            detail: error,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                    }

                    if let authError {
                        NoriNotice(
                            title: "Sign-in interrupted",
                            detail: authError,
                            tone: .warning,
                            systemImage: "key.slash.fill"
                        )
                    }

                    // Connection Metadata Card
                    NoriCard {
                        VStack(alignment: .leading, spacing: 10) {
                            metadataRow("Transport", value: server.transport == .http ? "Streamable HTTP" : "Managed stdio")

                            if let endpoint = server.endpoint, !endpoint.isEmpty {
                                metadataRow("Endpoint", value: endpoint, monospaced: true)
                            }

                            if let protocolVersion = server.observed.protocolVersion, !protocolVersion.isEmpty {
                                metadataRow("Protocol", value: protocolVersion, monospaced: true)
                            }

                            if let lastSuccess = server.observed.lastSuccessAt, !lastSuccess.isEmpty {
                                metadataRow("Last call", value: NoriDateFormatting.shortDateTime(lastSuccess))
                            }

                            if let capabilities = server.observed.capabilities, !capabilities.isEmpty {
                                metadataRow("Supports", value: capabilities.joined(separator: ", "))
                            }

                            HStack(spacing: 8) {
                                let supportsOAuth = server.auth == .oauth || (server.auth == nil && server.observed.status == .needsSignIn)
                                if supportsOAuth && server.observed.status != .connected {
                                    Button {
                                        startOAuthFlow(for: server)
                                    } label: {
                                        Label(isBusy ? "Signing in…" : "Sign in (OAuth)", systemImage: "key.fill")
                                    }
                                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                                    .disabled(isBusy)

                                    if server.observed.status == .needsSignIn || server.observed.status == .failed {
                                        Button("Reconnect") {
                                            reconnect(serverID: server.id)
                                        }
                                        .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                                        .disabled(isBusy)
                                    }
                                } else {
                                    Button {
                                        reconnect(serverID: server.id)
                                    } label: {
                                        Label(isBusy ? "Reconnecting…" : "Reconnect", systemImage: "arrow.clockwise")
                                    }
                                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                                    .disabled(isBusy)
                                }
                            }
                            .padding(.top, 4)
                        }
                    }

                    if server.auth == .configured {
                        credentialCard(for: server)
                    }

                    // Discovered Tools
                    NoriSectionHeader("Discovered tools (\(tools.count))")

                    if tools.isEmpty {
                        NoriEmptyState(
                            title: "No tools discovered",
                            message: "Reconnect or finish sign-in to discover tools exposed by this server."
                        )
                    } else {
                        NoriRows {
                            ForEach(Array(tools.enumerated()), id: \.element.id) { index, tool in
                                let isLast = index == tools.count - 1
                                VStack(spacing: 0) {
                                    HStack(alignment: .center, spacing: 10) {
                                        VStack(alignment: .leading, spacing: 2) {
                                            Text(tool.title)
                                                .font(NoriTypography.rowTitle)
                                                .foregroundStyle(NoriColors.ink)
                                            Text(tool.name)
                                                .font(NoriTypography.monoSmall)
                                                .foregroundStyle(NoriColors.ink3)
                                                .lineLimit(1)
                                                .truncationMode(.middle)
                                        }
                                        .frame(maxWidth: .infinity, alignment: .leading)

                                        Menu {
                                            exposureMenuButton(serverID: server.id, tool: tool, exposure: .direct, title: "Always", subtitle: "Always in Nori's tool list")
                                            exposureMenuButton(serverID: server.id, tool: tool, exposure: .discoverable, title: "Relevant", subtitle: "Loaded when relevant")
                                            exposureMenuButton(serverID: server.id, tool: tool, exposure: .disabled, title: "Off", subtitle: "Hidden from Nori")
                                        } label: {
                                            HStack(spacing: 4) {
                                                Text(tool.exposure.displayLabel)
                                                    .font(.system(size: 12, weight: .semibold))
                                                Image(systemName: "chevron.up.chevron.down")
                                                    .font(.system(size: 10, weight: .semibold))
                                            }
                                            .foregroundStyle(tool.exposure == .disabled ? NoriColors.ink3 : NoriColors.ink)
                                            .padding(.horizontal, 10)
                                            .frame(height: 30)
                                            .background(NoriColors.fill1, in: Capsule())
                                            .overlay(
                                                Capsule()
                                                    .strokeBorder(NoriColors.hairlineStrong, lineWidth: 1)
                                            )
                                        }
                                    }
                                    .padding(.horizontal, 13)
                                    .padding(.vertical, 12)

                                    if !isLast {
                                        Rectangle()
                                            .fill(NoriColors.hairline)
                                            .frame(height: 1)
                                            .padding(.leading, 13)
                                    }
                                }
                            }
                        }
                    }
                }
                .padding(.horizontal, 16)
                .padding(.top, 10)
                .padding(.bottom, 28)
            } else {
                NoriEmptyState(
                    title: "MCP server not found",
                    message: "This server may have been removed."
                )
            }
        }
        .background(NoriColors.canvas)
        .navigationTitle(server?.name ?? "MCP Server")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .toolbar {
            if let server {
                ToolbarItem(placement: .primaryAction) {
                    NoriPill(server.observed.status.displayLabel, tone: statusTone(for: server.observed.status))
                }
            }
        }
    }

    @ViewBuilder
    private func credentialCard(for server: MCPServer) -> some View {
        let hasExistingToken = server.observed.status == .connected
            || server.observed.credential == "configured"
            || server.observed.credential == "set"
        let trimmedToken = accessToken.trimmingCharacters(in: .whitespacesAndNewlines)
        let canSave = !trimmedToken.isEmpty && !isSavingCredential

        NoriCard {
            VStack(alignment: .leading, spacing: 10) {
                HStack(alignment: .center, spacing: 8) {
                    Label("Access token", systemImage: "key.fill")
                        .font(NoriTypography.metaSemibold)
                        .foregroundStyle(NoriColors.ink)
                    Spacer()
                    if server.observed.status.needsAttention {
                        NoriPill("Required", tone: .warning)
                    } else if hasExistingToken {
                        NoriPill("Configured", tone: .ok)
                    }
                }

                Text(
                    hasExistingToken
                        ? "Enter a new access token or API key to replace the saved credential."
                        : "Enter an access token or API key to authenticate with this server."
                )
                .font(NoriTypography.caption)
                .foregroundStyle(NoriColors.ink2)
                .fixedSize(horizontal: false, vertical: true)

                HStack(spacing: 8) {
                    Group {
                        if isTokenVisible {
                            TextField("Paste access token or API key", text: $accessToken)
                        } else {
                            SecureField("Paste access token or API key", text: $accessToken)
                        }
                    }
                    .textFieldStyle(.plain)
                    .font(NoriTypography.monoSmall)
                    .autocorrectionDisabled()
                    #if os(iOS)
                    .textInputAutocapitalization(.never)
                    #endif
                    .disabled(isSavingCredential)

                    Button {
                        isTokenVisible.toggle()
                    } label: {
                        Image(systemName: isTokenVisible ? "eye.slash" : "eye")
                            .font(.system(size: 14, weight: .medium))
                            .foregroundStyle(NoriColors.ink3)
                            .frame(width: 28, height: 28)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(isTokenVisible ? "Hide token" : "Show token")
                }
                .padding(.horizontal, 12)
                .frame(height: 42)
                .background(NoriColors.fill1, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous)
                        .strokeBorder(NoriColors.hairline, lineWidth: 1)
                )

                Button {
                    saveCredential(for: server)
                } label: {
                    if isSavingCredential {
                        HStack(spacing: 8) {
                            ProgressView()
                                .controlSize(.small)
                                .tint(.white)
                            Text("Saving…")
                        }
                    } else {
                        Text(hasExistingToken ? "Update token" : "Save access token")
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                .disabled(!canSave)
                .opacity(canSave ? 1 : 0.5)
            }
        }
    }

    @ViewBuilder
    private func exposureMenuButton(
        serverID: String,
        tool: MCPServerTool,
        exposure: MCPToolExposure,
        title: String,
        subtitle: String
    ) -> some View {
        Button {
            Task {
                await store.setToolExposure(serverID: serverID, toolName: tool.name, exposure: exposure)
            }
        } label: {
            if tool.exposure == exposure {
                Label(title, systemImage: "checkmark")
            } else {
                Text("\(title) — \(subtitle)")
            }
        }
    }

    private func metadataRow(_ label: String, value: String, monospaced: Bool = false) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Text(label)
                .font(.system(size: 12.5, weight: .medium))
                .foregroundStyle(NoriColors.ink3)
                .frame(width: 72, alignment: .leading)

            Text(value)
                .font(monospaced ? NoriTypography.monoSmall : .system(size: 13, weight: .medium))
                .foregroundStyle(NoriColors.ink)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func statusTone(for status: MCPObservedStatus) -> NoriTone {
        switch status {
        case .connected: return .ok
        case .needsSignIn, .failed: return .danger
        case .connecting: return .accent
        default: return .neutral
        }
    }

    private func reconnect(serverID: String) {
        isBusy = true
        authError = nil
        Task {
            await store.reconnectMCPServer(serverID: serverID)
            isBusy = false
        }
    }

    private func saveCredential(for server: MCPServer) {
        let trimmed = accessToken.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !isSavingCredential else { return }
        isSavingCredential = true
        authError = nil
        Task {
            defer { isSavingCredential = false }
            let saved = await store.saveMCPCredential(serverID: server.id, secret: trimmed)
            if saved {
                accessToken = ""
                isTokenVisible = false
                #if os(iOS)
                UINotificationFeedbackGenerator().notificationOccurred(.success)
                #endif
            }
        }
    }

    private func startOAuthFlow(for server: MCPServer) {
        authError = nil
        isBusy = true
        Task {
            defer { isBusy = false }
            let attempt = await store.startMCPOAuth(serverID: server.id)
            guard let attempt else { return }

            guard let authorizeURL = URL(string: attempt.authorizeURL),
                  authorizeURL.scheme?.lowercased() == "https" else {
                let message = "The server returned an invalid sign-in link."
                authError = message
                store.showToast(message)
                return
            }

            do {
                let callbackURL = try await webAuthenticationSession.authenticate(
                    using: authorizeURL,
                    callback: .customScheme("nori"),
                    preferredBrowserSession: .ephemeral,
                    additionalHeaderFields: [:]
                )
                let outcome = Self.parseOAuthCallback(callbackURL)
                switch outcome.result {
                case "connected":
                    store.showToast("Signed in to \(server.name)")
                    #if os(iOS)
                    UINotificationFeedbackGenerator().notificationOccurred(.success)
                    #endif
                case "cancelled", "canceled":
                    store.showToast("Sign-in cancelled")
                case "failed":
                    let message = outcome.reason.map { "Sign-in failed: \($0)" } ?? "Sign-in failed for \(server.name)"
                    authError = message
                    store.showToast(message)
                default:
                    store.showToast("Updated \(server.name)")
                }
                await store.refreshApps()
            } catch {
                let nsError = error as NSError
                if nsError.domain == ASWebAuthenticationSessionErrorDomain,
                   nsError.code == ASWebAuthenticationSessionError.canceledLogin.rawValue {
                    store.showToast("Sign-in cancelled")
                } else {
                    authError = error.localizedDescription
                    store.showToast(error.localizedDescription)
                }
            }
        }
    }

    private static func parseOAuthCallback(_ url: URL) -> (result: String?, reason: String?) {
        let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        let map = Dictionary(
            items.compactMap { item in item.value.map { (item.name.lowercased(), $0) } },
            uniquingKeysWith: { _, latest in latest }
        )
        let result = map["result"]?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let rawReason = map["reason"]?.trimmingCharacters(in: .whitespacesAndNewlines)
        let reason = (rawReason?.isEmpty == false)
            ? rawReason?.replacingOccurrences(of: "_", with: " ")
            : nil
        return (result, reason)
    }
}
