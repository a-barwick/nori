import AuthenticationServices
import SwiftUI
import NoriCore
import NoriDesign
#if os(iOS)
import UIKit
#endif

private enum GoogleService: String, CaseIterable, Identifiable {
    case calendar, drive, gmail

    var id: String { rawValue }
    var title: String {
        switch self {
        case .calendar: return "Google Calendar"
        case .drive: return "Google Drive"
        case .gmail: return "Gmail"
        }
    }
    var brand: NoriBrandKind {
        switch self {
        case .calendar: return .googleCalendar
        case .drive: return .googleDrive
        case .gmail: return .gmail
        }
    }
    var capabilities: [String] {
        switch self {
        case .calendar: return ["calendar.read", "calendar.write"]
        case .drive: return ["files.read", "files.write"]
        case .gmail: return ["mail.send"]
        }
    }
    var disclosure: String {
        switch self {
        case .calendar:
            return "Nori uses your calendar to read and manage the events you request. Relevant event details may be processed by your selected AI model provider."
        case .drive:
            return "Nori reads and manages the Google Drive files you select. Relevant file contents may be processed by your selected AI model provider."
        case .gmail:
            return "Nori sends the messages you approve, including their recipients and attachments. Relevant message details may be processed by your selected AI model provider."
        }
    }
}

public struct AppsView: View {
    @Bindable private var store: NoriStore
    @Environment(\.webAuthenticationSession) private var webAuthenticationSession
    @State private var showingAddMCP = false
    @State private var connectingConnectionID: String?
    @State private var pendingGoogleService: GoogleService?
    @State private var isGoogleBusy = false
    @State private var showingDisconnectGoogle = false
    @State private var googleDriveFiles: [ConnectionResource] = []

    public init(store: NoriStore) {
        self.store = store
    }

    private var appReadError: String? {
        store.integrationsRead.error ?? store.connectionsRead.error ?? store.mcpServersRead.error
    }

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                if let appReadError {
                    NoriRetryNotice("Couldn’t load all apps", detail: appReadError) {
                        await store.refreshApps()
                        await refreshGoogleDriveFiles()
                    }
                    .padding(.top, 10)
                }

                if store.integrationsRead == .pending && store.mcpServersRead == .pending
                    && store.connectionsRead == .pending && store.mcpServers.isEmpty && store.connections.isEmpty {
                    NoriLoadingState("Loading apps…")
                        .padding(.top, 10)
                } else {
                    let google = store.integrations.first(where: { $0.id == "google" })
                    if google != nil || googleConnection != nil {
                        googleSection(google)
                    }

                    NoriSectionHeader("Custom apps") {
                        Button("+ Add") {
                            showingAddMCP = true
                        }
                        .font(.system(size: 12.5, weight: .semibold))
                        .foregroundStyle(NoriColors.accent)
                        .buttonStyle(.plain)
                    }

                    if store.mcpServers.isEmpty && store.mcpServersRead == .loaded {
                        NoriEmptyState(
                            title: "No custom apps yet",
                            message: "Add a custom app to give Nori access to more tools."
                        )
                    } else if store.mcpServers.isEmpty && store.mcpServersRead == .pending {
                        NoriLoadingState("Loading custom apps…")
                    } else if !store.mcpServers.isEmpty {
                        NoriRows {
                            ForEach(Array(store.mcpServers.enumerated()), id: \.element.id) { index, server in
                                let isLast = index == store.mcpServers.count - 1
                                NavigationLink(value: MoreRoute.mcpDetail(server.id)) {
                                    NoriBrandRow(
                                        title: server.name,
                                        brand: NoriBrandKind.forApp(name: server.name, endpoint: server.endpoint),
                                        showChevron: true,
                                        showDivider: !isLast
                                    ) {
                                        if !server.enabled {
                                            NoriPill("Off", tone: .neutral)
                                        } else if server.observed.status != .connected {
                                            NoriPill(
                                                server.observed.status.displayLabel,
                                                tone: mcpPillTone(for: server)
                                            )
                                        }
                                    }
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }

                    let otherConnections = store.connections.filter { $0.integrationID != "google" && $0.status != .disconnected }
                    if !otherConnections.isEmpty {
                        NoriSectionHeader("Connected accounts")

                        NoriRows {
                            ForEach(Array(otherConnections.enumerated()), id: \.element.id) { index, connection in
                                let isLast = index == otherConnections.count - 1
                                let state = connectionHealthDisplay(for: connection)
                                NoriBrandRow(
                                    title: connection.title,
                                    brand: NoriBrandKind.forApp(name: connection.title),
                                    showChevron: false,
                                    showDivider: !isLast
                                ) {
                                    if let actionLabel = connectionActionLabel(for: connection) {
                                        let isConnectingThis = connectingConnectionID == connection.id
                                        Button(isConnectingThis ? "Connecting…" : actionLabel) {
                                            startConnectionOAuth(for: connection)
                                        }
                                        .buttonStyle(NoriButtonStyle(.primary, compact: true))
                                        .disabled(connectingConnectionID != nil)
                                    } else if state.tone != .ok {
                                        NoriPill(state.label, tone: state.tone)
                                    }
                                }
                            }
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 4)
            .padding(.bottom, 96)
        }
        .background(NoriColors.canvas)
        .task {
            await store.refreshApps(force: false)
            await refreshGoogleDriveFiles()
        }
        .refreshable {
            await store.refreshApps()
            await refreshGoogleDriveFiles()
        }
        .task(id: googleConnection?.revision) {
            await refreshGoogleDriveFiles()
        }
        .navigationTitle("Apps")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        #endif
        .sheet(isPresented: $showingAddMCP) {
            AddMCPServerSheet(store: store)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
                .presentationBackground(NoriColors.sheetBackground)
                .presentationCornerRadius(NoriRadius.sheet)
        }
        .alert("Connect Google", isPresented: Binding(
            get: { pendingGoogleService != nil },
            set: { if !$0 { pendingGoogleService = nil } }
        ), presenting: pendingGoogleService) { service in
            Button("Continue") { startGoogleOAuth(service) }
            Button("Cancel", role: .cancel) {}
        } message: { service in
            Text(service.disclosure)
        }
        .confirmationDialog("Disconnect Google?", isPresented: $showingDisconnectGoogle) {
            Button("Disconnect", role: .destructive) {
                guard let connection = googleConnection else { return }
                Task { await store.disconnectGoogle(connectionID: connection.id) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Nori will lose access to this Google account.")
        }
    }

    private var googleConnection: Connection? {
        store.connections.first { $0.integrationID == "google" && $0.status != .disconnected }
    }

    @ViewBuilder
    private func googleSection(_ integration: IntegrationSummary?) -> some View {
        let connection = googleConnection
        let needsReconnect = connection != nil && (connection?.status == .needsAttention || connection?.status == .disabled)
        NoriSectionHeader("Google") {
            if let connection, connection.account != nil {
                HStack(spacing: 12) {
                    if needsReconnect {
                        Button("Reconnect") { reconnectGoogle(connection) }
                            .font(.system(size: 12.5, weight: .semibold))
                            .foregroundStyle(NoriColors.accent)
                            .buttonStyle(.plain)
                            .disabled(isGoogleBusy)
                    }
                    Button("Disconnect") { showingDisconnectGoogle = true }
                        .font(.system(size: 12.5, weight: .medium))
                        .foregroundStyle(NoriColors.ink3)
                        .buttonStyle(.plain)
                        .disabled(isGoogleBusy)
                }
            }
        }
        if let connection {
            let state = connectionHealthDisplay(for: connection)
            if state.tone != .ok {
                HStack {
                    NoriPill(state.label, tone: state.tone)
                    Spacer()
                }
                .padding(.bottom, 8)
            }
        }
        if let integration, integration.availability != "available" {
            NoriNotice(
                title: integration.availabilityReason ?? "Google connections are unavailable.",
                tone: .warning,
                systemImage: "exclamationmark.triangle.fill"
            )
        } else if connection != nil || store.connectionsRead == .loaded {
            NoriRows {
                ForEach(GoogleService.allCases) { service in
                    let granted = googleServiceIsGranted(service)
                    let showDriveRow = service == .drive && granted
                    NoriBrandRow(
                        title: service.title,
                        brand: service.brand,
                        showDivider: service != .gmail || showDriveRow
                    ) {
                        Toggle(
                            service.title,
                            isOn: Binding(
                                get: { granted },
                                set: { next in
                                    if next && !granted {
                                        pendingGoogleService = service
                                    }
                                }
                            )
                        )
                        .labelsHidden()
                        .tint(NoriColors.brandPrimary)
                        .disabled(granted || isGoogleBusy || integration == nil)
                    }

                    if showDriveRow {
                        Button {
                            startGoogleDrivePicker()
                        } label: {
                            VStack(spacing: 0) {
                                HStack(spacing: 11) {
                                    Text("Choose Drive files")
                                        .font(NoriTypography.rowTitle)
                                        .foregroundStyle(NoriColors.accent)
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                        .padding(.leading, 47)
                                    Image(systemName: "chevron.right")
                                        .font(.system(size: 12, weight: .semibold))
                                        .foregroundStyle(NoriColors.ink3)
                                }
                                .padding(.horizontal, 13)
                                .padding(.vertical, 12)
                                .contentShape(Rectangle())

                                Rectangle()
                                    .fill(NoriColors.hairline)
                                    .frame(height: 1)
                                    .padding(.leading, 60)
                            }
                        }
                        .buttonStyle(.plain)
                        .disabled(isGoogleBusy)
                    }
                }
            }

            if googleServiceIsGranted(.drive), !googleDriveFiles.isEmpty {
                NoriRows {
                    ForEach(Array(googleDriveFiles.enumerated()), id: \.element.id) { index, file in
                        NoriRow(
                            title: file.title,
                            systemImage: "doc",
                            showDivider: index < googleDriveFiles.count - 1,
                            wrapTitle: true
                        )
                    }
                }
                .padding(.top, 10)
            }
        } else if store.connectionsRead == .pending {
            NoriLoadingState("Loading Google account…")
        }
    }

    private func googleServiceIsGranted(_ service: GoogleService) -> Bool {
        guard let connection = googleConnection,
              connection.status == .connected || connection.status == .needsAttention else { return false }
        guard connection.health.code != "needs_sign_in", connection.health.code != "revoked" else { return false }
        let capabilities = Set(connection.capabilities ?? [])
        return service.capabilities.allSatisfy { capabilities.contains($0) }
    }

    private func refreshGoogleDriveFiles() async {
        guard let connection = googleConnection, googleServiceIsGranted(.drive) else {
            googleDriveFiles = []
            return
        }
        if let files = await store.loadGoogleDriveFiles(connectionID: connection.id) {
            googleDriveFiles = files
        }
    }

    private func startGoogleOAuth(_ service: GoogleService) {
        guard !isGoogleBusy else { return }
        isGoogleBusy = true
        Task {
            defer { isGoogleBusy = false }
            guard let attempt = await store.connectGoogle(capabilities: service.capabilities) else { return }
            await completeGoogleBrowserFlow(attempt, successMessage: "Connected \(service.title)")
        }
    }

    private func reconnectGoogle(_ connection: Connection) {
        guard !isGoogleBusy else { return }
        isGoogleBusy = true
        Task {
            defer { isGoogleBusy = false }
            guard let attempt = await store.repairGoogleConnection(
                connectionID: connection.id,
                capabilities: connection.capabilities ?? []
            ) else { return }
            await completeGoogleBrowserFlow(attempt, successMessage: "Reconnected Google")
        }
    }

    private func startGoogleDrivePicker() {
        guard !isGoogleBusy, let connection = googleConnection else { return }
        isGoogleBusy = true
        Task {
            defer { isGoogleBusy = false }
            guard let attempt = await store.startGoogleDrivePicker(connectionID: connection.id) else { return }
            await completeGoogleBrowserFlow(attempt, successMessage: "Google Drive files selected")
        }
    }

    private func completeGoogleBrowserFlow(_ attempt: AuthAttempt, successMessage: String) async {
        guard let url = URL(string: attempt.authorizeURL), url.scheme?.lowercased() == "https" else {
            store.showToast("Invalid Google sign-in link")
            return
        }
        do {
            let callback = try await webAuthenticationSession.authenticate(
                using: url,
                callback: .customScheme("nori"),
                preferredBrowserSession: .shared,
                additionalHeaderFields: [:]
            )
            let outcome = Self.parseOAuthCallback(callback)
            switch outcome.result {
            case "connected", "drive_files_selected":
                store.showToast(successMessage)
            case "cancelled", "canceled":
                store.showToast("Cancelled")
            case "failed":
                store.showToast(outcome.reason ?? "Google connection failed")
            default:
                store.showToast("Google connection updated")
            }
            await store.refreshApps()
            await refreshGoogleDriveFiles()
            await store.refreshSummary()
        } catch {
            let nsError = error as NSError
            if nsError.domain == ASWebAuthenticationSessionErrorDomain,
               nsError.code == ASWebAuthenticationSessionError.canceledLogin.rawValue {
                store.showToast("Cancelled")
            } else {
                store.showToast(error.localizedDescription)
            }
        }
    }

    private func mcpPillTone(for server: MCPServer) -> NoriTone {
        switch server.observed.status {
        case .connected: return .ok
        case .needsSignIn, .failed: return .danger
        case .connecting: return .working
        default: return .neutral
        }
    }

    private func connectionActionLabel(for connection: Connection) -> String? {
        switch connection.status {
        case .notConnected, .disconnected:
            return "Connect"
        case .needsAttention:
            return "Reconnect"
        case .connected:
            switch connection.health.code {
            case "needs_sign_in", "revoked", "needs_more_access":
                return "Reconnect"
            default:
                return nil
            }
        default:
            return nil
        }
    }

    private func startConnectionOAuth(for connection: Connection) {
        guard connectingConnectionID == nil else { return }
        connectingConnectionID = connection.id
        Task {
            defer { connectingConnectionID = nil }
            let attempt = await store.connectConnection(connectionID: connection.id)
            guard let attempt else {
                await store.refreshSummary()
                return
            }

            guard let authorizeURL = URL(string: attempt.authorizeURL),
                  authorizeURL.scheme?.lowercased() == "https" else {
                store.showToast("Invalid sign-in link for \(connection.title)")
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
                    store.showToast("Connected \(connection.title)")
                    #if os(iOS)
                    UINotificationFeedbackGenerator().notificationOccurred(.success)
                    #endif
                case "cancelled", "canceled":
                    store.showToast("Sign-in cancelled")
                case "failed":
                    if let reason = outcome.reason {
                        store.showToast("Connection failed: \(reason)")
                    } else {
                        store.showToast("Failed to connect \(connection.title)")
                    }
                default:
                    store.showToast("Updated \(connection.title)")
                }
                async let appsTask: Void = store.refreshApps()
                async let summaryTask: Void = store.refreshSummary()
                _ = await (appsTask, summaryTask)
            } catch {
                let nsError = error as NSError
                if nsError.domain == ASWebAuthenticationSessionErrorDomain,
                   nsError.code == ASWebAuthenticationSessionError.canceledLogin.rawValue {
                    store.showToast("Sign-in cancelled")
                } else {
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

    private func connectionHealthDisplay(for connection: Connection) -> (label: String, tone: NoriTone) {
        switch connection.status {
        case .connected:
            switch connection.health.code {
            case "ready", nil:
                return ("Ready", .ok)
            case "needs_sign_in", "revoked":
                return ("Needs sign-in", .danger)
            case "needs_more_access", "rate_limited", "temporarily_failing", "provider_unavailable":
                return ("Attention", .warning)
            default:
                return ("Ready", .ok)
            }
        case .needsAttention:
            return ("Needs sign-in", .danger)
        case .connecting:
            return ("Connecting", .working)
        case .disabled:
            return ("Off", .neutral)
        default:
            return (connection.status.displayLabel, .neutral)
        }
    }
}

struct AddMCPServerSheet: View {
    @Bindable var store: NoriStore
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var endpoint = ""
    @State private var auth: MCPAuthKind = .oauth
    @State private var showSignInOptions = false
    @State private var isSubmitting = false

    private var isEndpointValid: Bool {
        let trimmed = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !trimmed.contains(" "), let url = URL(string: trimmed), let host = url.host else {
            return false
        }
        if url.scheme == "https" { return true }
        if url.scheme == "http" {
            return host == "127.0.0.1" || host == "localhost" || host == "::1" || host.hasSuffix(".local")
        }
        return false
    }

    private var canSubmit: Bool {
        !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && isEndpointValid && !isSubmitting
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Name")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("Home Notes", text: $name)
                            .textFieldStyle(.plain)
                            .foregroundStyle(NoriColors.ink)
                            .tint(NoriColors.accent)
                            .padding(.horizontal, 12)
                            .frame(height: 42)
                            .background(NoriColors.elevated, in: RoundedRectangle(cornerRadius: NoriRadius.tile, style: .continuous))
                    }

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Server URL")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.ink)
                        TextField("https://mcp.example.com/mcp", text: $endpoint)
                            .textFieldStyle(.plain)
                            .font(NoriTypography.mono)
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

                        if !endpoint.isEmpty && !isEndpointValid {
                            Text("Use an https:// URL, or http://localhost / 127.0.0.1 / .local for a local server.")
                                .font(NoriTypography.caption)
                                .foregroundStyle(NoriColors.danger)
                        }
                    }

                    Button {
                        showSignInOptions.toggle()
                    } label: {
                        Text(showSignInOptions ? "Hide sign-in options" : "Sign-in options")
                            .font(NoriTypography.metaSemibold)
                            .foregroundStyle(NoriColors.accent)
                    }
                    .buttonStyle(.plain)

                    if showSignInOptions {
                        NoriCard(padding: 12, backgroundColor: NoriColors.elevated) {
                            HStack {
                                Text("Authentication")
                                    .font(NoriTypography.metaSemibold)
                                    .foregroundStyle(NoriColors.ink)
                                Spacer()
                                Picker("Authentication", selection: $auth) {
                                    Text("OAuth").tag(MCPAuthKind.oauth)
                                    Text("Access token").tag(MCPAuthKind.configured)
                                    Text("None").tag(MCPAuthKind.none)
                                }
                                .pickerStyle(.menu)
                                .tint(NoriColors.accent)
                            }
                        }
                    }

                    Button {
                        submit()
                    } label: {
                        if isSubmitting {
                            ProgressView().tint(NoriColors.disabled)
                        } else {
                            Text("Add app")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                    .disabled(!canSubmit)
                    .padding(.top, 4)
                }
                .padding(16)
            }
            .background(NoriColors.sheetBackground)
            .navigationTitle("Add app")
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

    private func submit() {
        guard canSubmit else { return }
        isSubmitting = true
        Task {
            await store.addMCPServer(
                name: name,
                endpoint: endpoint,
                auth: auth,
                defaultExposure: .discoverable
            )
            isSubmitting = false
            dismiss()
        }
    }
}
