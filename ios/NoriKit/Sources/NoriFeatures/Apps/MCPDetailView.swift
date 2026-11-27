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
                let supportsOAuth = server.auth == .oauth || (server.auth == nil && server.observed.status == .needsSignIn)
                let needsSignIn = server.observed.status == .needsSignIn || (supportsOAuth && server.observed.status != .connected)

                VStack(alignment: .leading, spacing: 14) {
                    if needsSignIn {
                        NoriNotice(
                            title: server.observed.error?.message ?? "Sign in to reconnect \(server.name).",
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        ) {
                            Button(isBusy ? "Signing in…" : "Sign in") {
                                startOAuthFlow(for: server)
                            }
                            .buttonStyle(NoriButtonStyle(.primary, compact: true))
                            .disabled(isBusy)
                        }
                    } else if let error = server.observed.error?.message, !error.isEmpty {
                        NoriNotice(
                            title: "Connection error",
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

                    NoriRows {
                        if server.transport == .http {
                            NoriRow(title: "Enabled", showDivider: true) {
                                Toggle(
                                    "Enabled",
                                    isOn: Binding(
                                        get: { server.enabled },
                                        set: { next in toggleEnabled(server, enabled: next) }
                                    )
                                )
                                .labelsHidden()
                                .tint(NoriColors.brandPrimary)
                                .disabled(isBusy)
                            }
                        }

                        if server.transport == .stdio {
                            let isConnected = server.observed.status == .connected
                            Button {
                                performLifecycleAction(
                                    serverID: server.id,
                                    action: isConnected ? "restart" : "start"
                                )
                            } label: {
                                NoriRow(
                                    title: isConnected ? "Restart" : "Start",
                                    showChevron: true,
                                    showDivider: isConnected
                                )
                            }
                            .buttonStyle(.plain)
                            .disabled(isBusy)

                            if isConnected {
                                Button {
                                    performLifecycleAction(serverID: server.id, action: "stop")
                                } label: {
                                    NoriRow(title: "Stop", showChevron: true, showDivider: false)
                                }
                                .buttonStyle(.plain)
                                .disabled(isBusy)
                            }
                        } else {
                            Button {
                                reconnect(serverID: server.id)
                            } label: {
                                NoriRow(
                                    title: isBusy ? "Reconnecting…" : "Reconnect",
                                    showChevron: true,
                                    showDivider: true
                                )
                            }
                            .buttonStyle(.plain)
                            .disabled(isBusy)

                            Button {
                                showEditSheet = true
                            } label: {
                                NoriRow(title: "Settings", showChevron: true, showDivider: false)
                            }
                            .buttonStyle(.plain)
                            .disabled(isBusy)
                        }
                    }

                    if server.auth == .configured {
                        credentialCard(for: server)
                    }

                    NoriSectionHeader("Actions")

                    if tools.isEmpty {
                        NoriEmptyState(
                            title: "No actions yet",
                            message: "Reconnect or finish sign-in to discover actions."
                        )
                    } else {
                        NoriRows {
                            ForEach(Array(tools.enumerated()), id: \.element.id) { index, tool in
                                let isLast = index == tools.count - 1
                                let isOn = tool.exposure != .disabled
                                NoriRow(title: tool.title, showDivider: !isLast) {
                                    Toggle(
                                        tool.title,
                                        isOn: Binding(
                                            get: { isOn },
                                            set: { next in
                                                Task {
                                                    await store.setToolExposure(
                                                        serverID: server.id,
                                                        toolName: tool.name,
                                                        exposure: next ? .discoverable : .disabled
                                                    )
                                                }
                                            }
                                        )
                                    )
                                    .labelsHidden()
                                    .tint(NoriColors.brandPrimary)
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
                        Text("Loading app…")
                            .font(NoriTypography.meta)
                            .foregroundStyle(NoriColors.ink2)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(16)
            } else {
                NoriEmptyState(
                    title: "App not found",
                    message: "This app may have been removed."
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
        .navigationTitle(server?.name ?? "App")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .toolbar {
            if let server, !server.enabled {
                ToolbarItem(placement: .primaryAction) {
                    NoriPill("Off", tone: .neutral)
                }
            } else if let server, server.observed.status != .connected {
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
                    Text("Access token")
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

                HStack(spacing: 8) {
                    Group {
                        if isTokenVisible {
                            TextField("Paste access token", text: $accessToken)
                        } else {
                            SecureField("Paste access token", text: $accessToken)
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

                Button {
                    saveCredential(for: server)
                } label: {
                    if isSavingCredential {
                        ProgressView().tint(NoriColors.disabled)
                    } else {
                        Text(hasExistingToken ? "Replace token" : "Save token")
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, compact: true))
                .disabled(!canSave)
            }
        }
    }

    private func toggleEnabled(_ server: MCPServer, enabled: Bool) {
        isBusy = true
        Task {
            defer { isBusy = false }
            _ = try? await store.updateMCPServer(
                serverID: server.id,
                name: server.name,
                endpoint: server.endpoint,
                auth: server.auth ?? .oauth,
                enabled: enabled,
                defaultExposure: server.exposure?.default ?? .discoverable
            )
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
    @State private var isSaving = false
    @State private var errorMessage: String?

    init(store: NoriStore, server: MCPServer) {
        self.store = store
        self.server = server
        _name = State(initialValue: server.name)
        _endpoint = State(initialValue: server.endpoint ?? "")
        _auth = State(initialValue: server.auth ?? .oauth)
    }

    private var canSave: Bool {
        let cleanName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanName.isEmpty, !isSaving else { return false }
        if server.transport == .http {
            return !endpoint.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
        return true
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let errorMessage {
                        NoriNotice(
                            title: "Couldn’t update app",
                            detail: errorMessage,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Name")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("App name", text: $name)
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
                        Text("Authentication")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        Picker("Authentication", selection: $auth) {
                            Text("OAuth").tag(MCPAuthKind.oauth)
                            Text("Access token").tag(MCPAuthKind.configured)
                            Text("None").tag(MCPAuthKind.none)
                        }
                        .pickerStyle(.segmented)
                        .tint(NoriColors.accent)
                    }

                    Button {
                        save()
                    } label: {
                        if isSaving {
                            ProgressView().tint(NoriColors.disabled)
                        } else {
                            Text("Save")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!canSave)
                    .padding(.top, 4)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Settings")
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
                    enabled: server.enabled,
                    defaultExposure: server.exposure?.default ?? .discoverable
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
