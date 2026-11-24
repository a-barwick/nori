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
    @State private var showingDemoOAuth = false
    @State private var isBusy = false
    @State private var authError: String?

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
                                metadataRow("Last call", value: lastSuccess)
                            }

                            if let capabilities = server.observed.capabilities, !capabilities.isEmpty {
                                metadataRow("Supports", value: capabilities.joined(separator: ", "))
                            }

                            HStack(spacing: 8) {
                                if server.observed.status == .needsSignIn || (server.auth == .oauth && server.observed.status != .connected) {
                                    Button {
                                        startOAuthFlow(for: server)
                                    } label: {
                                        Label(isBusy ? "Signing in…" : "Sign in (OAuth)", systemImage: "key.fill")
                                    }
                                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                                    .disabled(isBusy)
                                } else {
                                    Button {
                                        reconnect(serverID: server.id)
                                    } label: {
                                        Label(isBusy ? "Reconnecting…" : "Reconnect", systemImage: "arrow.clockwise")
                                    }
                                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                                    .disabled(isBusy)
                                }

                                if server.observed.status == .needsSignIn {
                                    Button("Reconnect") {
                                        reconnect(serverID: server.id)
                                    }
                                    .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                                    .disabled(isBusy)
                                }
                            }
                            .padding(.top, 4)
                        }
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
        .sheet(isPresented: $showingDemoOAuth) {
            if let server {
                MCPOAuthDemoSheet(store: store, server: server)
                    .presentationDetents([.medium])
                    .presentationDragIndicator(.visible)
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

    private func startOAuthFlow(for server: MCPServer) {
        authError = nil
        isBusy = true
        Task {
            defer { isBusy = false }
            let authAttempt = await store.completeMCPOAuth(serverID: server.id)
            guard let urlString = authAttempt?.authorizeURL,
                  let authorizeURL = URL(string: urlString),
                  authorizeURL.scheme == "https" else {
                return
            }

            do {
                let callbackURL = try await webAuthenticationSession.authenticate(
                    using: authorizeURL,
                    callback: .customScheme("nori"),
                    preferredBrowserSession: .ephemeral,
                    additionalHeaderFields: [:]
                )
                store.handleIncomingURL(callbackURL)
                await store.refreshAll()
            } catch {
                let nsError = error as NSError
                if nsError.domain != ASWebAuthenticationSessionErrorDomain || nsError.code != ASWebAuthenticationSessionError.canceledLogin.rawValue {
                    authError = error.localizedDescription
                }
            }
        }
    }
}

// MARK: - Demo OAuth Sheet

struct MCPOAuthDemoSheet: View {
    @Bindable var store: NoriStore
    let server: MCPServer

    @Environment(\.dismiss) private var dismiss
    @State private var isAuthorizing = false

    private var hostTitle: String {
        if let endpoint = server.endpoint, let host = URL(string: endpoint)?.host {
            return "\(host) · OAuth"
        }
        return "\(server.name) · OAuth"
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 16) {
                ZStack {
                    RoundedRectangle(cornerRadius: 16, style: .continuous)
                        .fill(NoriColors.accentSubtle)
                        .frame(width: 56, height: 56)
                    Image(systemName: "key.fill")
                        .font(.system(size: 24, weight: .semibold))
                        .foregroundStyle(NoriColors.accent)
                }
                .padding(.top, 12)

                Text("ASWebAuthenticationSession")
                    .font(.system(size: 17, weight: .bold))
                    .foregroundStyle(NoriColors.ink)

                Text("Native iOS ephemeral OAuth returns directly to nori://oauth/callback and resumes waiting tasks automatically.")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 12)

                Button {
                    isAuthorizing = true
                    Task {
                        _ = await store.completeMCPOAuth(serverID: server.id)
                        #if os(iOS)
                        UINotificationFeedbackGenerator().notificationOccurred(.success)
                        #endif
                        isAuthorizing = false
                        dismiss()
                    }
                } label: {
                    if isAuthorizing {
                        ProgressView().tint(.white)
                    } else {
                        Text("Authorize \(server.name)")
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(isAuthorizing)
                .padding(.top, 6)

                Spacer()
            }
            .padding(20)
            .background(NoriColors.canvas)
            .navigationTitle(hostTitle)
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(NoriColors.ink2)
                }
            }
        }
    }
}
