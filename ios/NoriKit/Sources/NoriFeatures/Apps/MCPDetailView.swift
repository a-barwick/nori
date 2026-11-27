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
    @State private var isLoadingServer = false
    @State private var authError: String?
    @State private var accessToken = ""
    @State private var isTokenVisible = false
    @State private var isSavingCredential = false
    @State private var showEditSheet = false

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

                    NoriCard {
                        VStack(alignment: .leading, spacing: 10) {
                            metadataRow("Type", value: server.transport == .http ? "Remote server" : "Local server")

                            if let endpoint = server.endpoint, !endpoint.isEmpty {
                                metadataRow("Server URL", value: endpoint, monospaced: true)
                            }

                            if let lastSuccess = server.observed.lastSuccessAt, !lastSuccess.isEmpty {
                                metadataRow("Last call", value: NoriDateFormatting.shortDateTime(lastSuccess))
                            }

                            HStack(spacing: 8) {
                                let supportsOAuth = server.auth == .oauth || (server.auth == nil && server.observed.status == .needsSignIn)
                                if server.transport == .stdio {
                                    Button {
                                        performLifecycleAction(
                                            serverID: server.id,
                                            action: server.observed.status == .connected ? "restart" : "start"
                                        )
                                    } label: {
                                        Label(
                                            server.observed.status == .connected ? "Restart" : "Start",
                                            systemImage: server.observed.status == .connected ? "arrow.clockwise" : "play.fill"
                                        )
                                    }
                                    .buttonStyle(NoriButtonStyle(.primary, compact: true))
                                    .disabled(isBusy)

                                    if server.observed.status == .connected {
                                        Button("Stop") {
                                            performLifecycleAction(serverID: server.id, action: "stop")
                                        }
                                        .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                                        .disabled(isBusy)
                                    }
                                } else if supportsOAuth && server.observed.status != .connected {
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

                                Button {
                                    showEditSheet = true
                                } label: {
                                    Label("Edit", systemImage: "pencil")
                                }
                                .buttonStyle(NoriButtonStyle(.neutral, compact: true))
                                .disabled(isBusy)
                            }
                            .padding(.top, 4)
                        }
                    }

                    if server.auth == .configured {
                        credentialCard(for: server)
                    }

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
                                            exposureMenuButton(serverID: server.id, tool: tool, exposure: .discoverable, title: "When relevant", subtitle: "Loaded when relevant")
                                            exposureMenuButton(serverID: server.id, tool: tool, exposure: .disabled, title: "Off", subtitle: "Hidden from Nori")
                                        } label: {
                                            HStack(spacing: 4) {
                                                Text(exposureLabel(for: tool.exposure))
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
                                                    .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
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
                    let hasDetails = (server.observed.protocolVersion?.isEmpty == false)
                        || (server.observed.capabilities?.isEmpty == false)
                    if hasDetails {
                        NoriSectionHeader("Details")
                        NoriCard {
                            VStack(alignment: .leading, spacing: 10) {
                                if let protocolVersion = server.observed.protocolVersion, !protocolVersion.isEmpty {
                                    metadataRow("Protocol", value: protocolVersion, monospaced: true)
                                }
                                if let capabilities = server.observed.capabilities, !capabilities.isEmpty {
                                    metadataRow("Supports", value: capabilities.joined(separator: ", "))
                                }
                            }
                        }
                    }
                }
                .padding(.horizontal, 16)
                .padding(.top, 10)
                .padding(.bottom, 28)
            } else if isLoadingServer {
                NoriCard {
                    HStack(spacing: 10) {
                        ProgressView().tint(NoriColors.accent)
                        Text("Loading server…")
                            .font(NoriTypography.meta)
                            .foregroundStyle(NoriColors.ink2)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(16)
            } else {
                NoriEmptyState(
                    title: "MCP server not found",
                    message: "This server may have been removed."
                )
            }
        }
        .background(NoriColors.canvas)
        .task {
            if server == nil {
                isLoadingServer = true
                await store.refreshApps()
                isLoadingServer = false
            }
        }
        .refreshable {
            await store.refreshApps()
        }
        .navigationTitle(server?.name ?? "MCP Server")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .toolbar {
            if let server, server.observed.status != .connected {
                ToolbarItem(placement: .primaryAction) {
                    NoriPill(server.observed.status.displayLabel, tone: statusTone(for: server.observed.status))
                }
            }
        }
        .sheet(isPresented: $showEditSheet) {
            if let server {
                EditMCPServerSheet(store: store, server: server)
                    .presentationDetents([.medium, .large])
                    .presentationDragIndicator(.visible)
                    .presentationBackground(NoriColors.sheetBackground)
                    .presentationCornerRadius(NoriRadius.sheet)
            }
        }
    }

    private func exposureLabel(for exposure: MCPToolExposure) -> String {
        switch exposure {
        case .direct: return "Always"
        case .discoverable: return "When relevant"
        case .disabled: return "Off"
        case .programmatic: return exposure.displayLabel
        }
    }

    @ViewBuilder
    private func credentialCard(for server: MCPServer) -> some View {
        let hasExistingToken = server.observed.status == .connected
            || server.observed.credential == "present"
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
                        Text("Saved")
                            .font(NoriTypography.caption)
                            .foregroundStyle(NoriColors.ink2)
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
                    .foregroundStyle(NoriColors.ink)
                    .tint(NoriColors.accent)
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
                        .strokeBorder(NoriColors.cardBorder, lineWidth: 1)
                )

                Button {
                    saveCredential(for: server)
                } label: {
                    if isSavingCredential {
                        HStack(spacing: 8) {
                            ProgressView()
                                .controlSize(.small)
                                .tint(NoriColors.disabled)
                            Text("Saving…")
                        }
                    } else {
                        Text(hasExistingToken ? "Update token" : "Save access token")
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                .disabled(!canSave)
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
        case .connecting: return .working
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

    private func performLifecycleAction(serverID: String, action: String) {
        isBusy = true
        authError = nil
        Task {
            await store.performMCPServerAction(serverID: serverID, action: action)
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

private struct EditMCPServerSheet: View {
    @Bindable var store: NoriStore
    let server: MCPServer

    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var endpoint: String
    @State private var auth: MCPAuthKind
    @State private var defaultExposure: MCPToolExposure
    @State private var enabled: Bool
    @State private var isSaving = false
    @State private var errorMessage: String?

    init(store: NoriStore, server: MCPServer) {
        self.store = store
        self.server = server
        _name = State(initialValue: server.name)
        _endpoint = State(initialValue: server.endpoint ?? "")
        _auth = State(initialValue: server.auth ?? .oauth)
        _defaultExposure = State(initialValue: server.exposure?.default ?? .discoverable)
        _enabled = State(initialValue: server.enabled)
    }

    private var canSave: Bool {
        let cleanName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanName.isEmpty, !isSaving else { return false }
        if server.transport == .http {
            let cleanEndpoint = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
            return !cleanEndpoint.isEmpty
        }
        return true
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let errorMessage {
                        NoriNotice(
                            title: "Couldn’t update server",
                            detail: errorMessage,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Name")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("Server name", text: $name)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(.horizontal, 12)
                            .frame(height: 42)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    if server.transport == .http {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Server URL")
                                .font(NoriTypography.metaSemibold)
                                .foregroundStyle(NoriColors.ink)
                            TextField("https://mcp.example.com/mcp", text: $endpoint)
                                .textFieldStyle(.plain)
                                .font(NoriTypography.monoSmall)
                                .foregroundStyle(NoriColors.ink)
                                .tint(NoriColors.accent)
                                .autocorrectionDisabled()
                                #if os(iOS)
                                .textInputAutocapitalization(.never)
                                .keyboardType(.URL)
                                #endif
                                .padding(.horizontal, 12)
                                .frame(height: 42)
                                .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                        }
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Sign-in")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        Picker("Sign-in", selection: $auth) {
                            Text("OAuth").tag(MCPAuthKind.oauth)
                            Text("None").tag(MCPAuthKind.none)
                            Text("Access token").tag(MCPAuthKind.configured)
                        }
                        .pickerStyle(.segmented)
                        .tint(NoriColors.accent)
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Default tool access")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        Picker("Tools", selection: $defaultExposure) {
                            Text("When relevant").tag(MCPToolExposure.discoverable)
                            Text("Always").tag(MCPToolExposure.direct)
                            Text("Off").tag(MCPToolExposure.disabled)
                        }
                        .pickerStyle(.segmented)
                        .tint(NoriColors.accent)
                    }

                    Toggle(isOn: $enabled) {
                        Text("Server enabled")
                            .font(.system(size: 14, weight: .medium))
                            .foregroundStyle(NoriColors.ink)
                    }
                    .tint(NoriColors.brandPrimary)

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(NoriColors.disabled)
                        } else {
                            Text("Save changes")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!canSave)
                    .padding(.top, 4)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Edit Server")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .interactiveDismissDisabled(isSaving)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .foregroundStyle(NoriColors.ink2)
                        .disabled(isSaving)
                }
            }
        }
    }

    private func save() {
        guard canSave else { return }
        isSaving = true
        errorMessage = nil
        Task {
            do {
                _ = try await store.updateMCPServer(
                    serverID: server.id,
                    name: name,
                    endpoint: server.transport == .http ? endpoint : server.endpoint,
                    auth: auth,
                    enabled: enabled,
                    defaultExposure: defaultExposure
                )
                isSaving = false
                dismiss()
            } catch {
                isSaving = false
                errorMessage = error.localizedDescription
            }
        }
    }
}
