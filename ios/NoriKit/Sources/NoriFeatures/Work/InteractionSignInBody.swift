import SwiftUI
import AuthenticationServices
import NoriCore
import NoriDesign

struct InteractionSignInBody: View {
    @Bindable var store: NoriStore
    let payload: SignInPayload
    @Binding var isSubmitting: Bool
    @Binding var submittingAction: String?
    @Binding var errorMessage: String?
    let onDecline: () -> Void
    let onHaptic: (InteractionHapticOutcome) -> Void

    @Environment(\.webAuthenticationSession) private var webAuthenticationSession

    var body: some View {
        let isMCP = payload.integrationID == "mcp"
        let server = isMCP
            ? store.mcpServers.first(where: { $0.id == payload.connectionID })
            : nil
        let connection = !isMCP
            ? store.connections.first(where: { $0.id == payload.connectionID || $0.integrationID == payload.integrationID })
            : nil
        let displayTitle = server?.name ?? connection?.title ?? payload.integrationID.capitalized
        let safeURL = payload.authorizeURL.flatMap { Self.safeAuthorizeURL($0) }
        let blockedUnsafeURL = payload.integrationID != "google" && payload.authorizeURL != nil && safeURL == nil

        let purposeText: String = {
            if let accountLabel = connection?.account?.label, !accountLabel.isEmpty {
                return "Reconnect \(accountLabel)."
            }
            if payload.integrationID == "google" {
                let scopes = payload.requestedScopes ?? []
                if scopes.contains(where: { $0.hasSuffix("/drive.file") }) {
                    return "To access your selected Drive files."
                }
                if scopes.contains(where: { $0.contains("/calendar") }) {
                    return "To read your calendar."
                }
                if scopes.contains(where: { $0.hasSuffix("/gmail.send") }) {
                    return "To send email for you."
                }
            }
            return "To use \(displayTitle) for this request."
        }()

        VStack(alignment: .leading, spacing: 10) {
            Text(purposeText)
                .font(.system(size: 13.5))
                .foregroundStyle(NoriColors.ink2)
                .fixedSize(horizontal: false, vertical: true)

            if payload.integrationID == "google" {
                Text(googleDisclosure(for: payload.requestedScopes ?? []))
                    .font(.system(size: 11.5))
                    .foregroundStyle(NoriColors.ink3)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if blockedUnsafeURL {
                NoriNotice(
                    title: "This sign-in link isn’t one Nori issued, so it won’t open.",
                    tone: .danger,
                    systemImage: "exclamationmark.shield.fill"
                )
            }

            HStack(spacing: 8) {
                Button {
                    startSignInFlow(
                        safeURL: safeURL,
                        server: server,
                        connection: connection,
                        displayTitle: displayTitle
                    )
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: "key")
                            .font(.system(size: 13, weight: .semibold))
                        Text(submittingAction == "sign_in" ? "Opening sign-in…" : "Sign in")
                    }
                }
                .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
                .disabled(isSubmitting || blockedUnsafeURL)

                Button {
                    onDecline()
                } label: {
                    Text(submittingAction == "decline" ? "Declining…" : "Not now")
                }
                .buttonStyle(NoriButtonStyle(.neutral, fullWidth: true))
                .disabled(isSubmitting)
            }
            .padding(.top, 2)
        }
    }

    private func googleDisclosure(for scopes: [String]) -> String {
        let service: String
        if scopes.contains(where: { $0.hasSuffix("/drive.file") }) {
            service = "your selected Drive files"
        } else if scopes.contains(where: { $0.contains("/calendar") }) {
            service = "your Calendar events"
        } else if scopes.contains(where: { $0.hasSuffix("/gmail.send") }) {
            service = "your Gmail messages and attachments"
        } else {
            service = "your Google data"
        }
        return "Nori uses \(service) for your requests. Relevant content may be processed by your selected AI model provider."
    }

    private func googleCapabilities(for scopes: [String], existing: [String]) -> [String] {
        var capabilities = Set(existing)
        for scope in scopes {
            switch scope {
            case "https://www.googleapis.com/auth/calendar.readonly": capabilities.insert("calendar.read")
            case "https://www.googleapis.com/auth/calendar.events": capabilities.formUnion(["calendar.read", "calendar.write"])
            case "https://www.googleapis.com/auth/drive.file": capabilities.formUnion(["files.read", "files.write"])
            case "https://www.googleapis.com/auth/gmail.send": capabilities.insert("mail.send")
            default: break
            }
        }
        return capabilities.sorted()
    }

    private static func safeAuthorizeURL(_ raw: String) -> URL? {
        guard let url = URL(string: raw), let scheme = url.scheme?.lowercased() else { return nil }
        return scheme == "https" ? url : nil
    }

    private func startSignInFlow(
        safeURL: URL?,
        server: MCPServer?,
        connection: Connection?,
        displayTitle: String
    ) {
        errorMessage = nil
        isSubmitting = true
        submittingAction = "sign_in"

        Task {
            defer {
                isSubmitting = false
                submittingAction = nil
            }

            let targetURL: URL
            if payload.integrationID == "google" {
                let capabilities = googleCapabilities(for: payload.requestedScopes ?? [], existing: connection?.capabilities ?? [])
                guard let attempt = await store.connectGoogle(capabilities: capabilities),
                      let attemptURL = Self.safeAuthorizeURL(attempt.authorizeURL) else {
                    await refreshAfterSignIn()
                    return
                }
                targetURL = attemptURL
            } else if let safeURL {
                targetURL = safeURL
            } else if let server {
                let attempt = await store.startMCPOAuth(serverID: server.id)
                guard let attempt else {
                    await refreshAfterSignIn()
                    if store.lastError == nil {
                        onHaptic(.success)
                    }
                    return
                }
                guard let attemptURL = Self.safeAuthorizeURL(attempt.authorizeURL) else {
                    errorMessage = "The server returned an invalid sign-in link."
                    return
                }
                targetURL = attemptURL
            } else if let connID = connection?.id ?? payload.connectionID {
                let attempt = await store.connectConnection(connectionID: connID)
                guard let attempt else {
                    await refreshAfterSignIn()
                    if store.lastError == nil {
                        onHaptic(.success)
                    }
                    return
                }
                guard let attemptURL = Self.safeAuthorizeURL(attempt.authorizeURL) else {
                    errorMessage = "The server returned an invalid sign-in link."
                    return
                }
                targetURL = attemptURL
            } else {
                errorMessage = "Sign-in link expired. Reconnect from More → Apps & MCP to resume this task."
                return
            }

            do {
                let callbackURL = try await webAuthenticationSession.authenticate(
                    using: targetURL,
                    callback: .customScheme("nori"),
                    preferredBrowserSession: payload.integrationID == "google" ? .shared : .ephemeral,
                    additionalHeaderFields: [:]
                )
                let outcome = Self.parseOAuthCallback(callbackURL)
                switch outcome.result {
                case "connected":
                    store.showToast(server != nil ? "Signed in to \(displayTitle)" : "Connected \(displayTitle)")
                    onHaptic(.success)
                case "cancelled", "canceled":
                    store.showToast("Sign-in cancelled")
                    onHaptic(.warning)
                case "failed":
                    let message = outcome.reason.map { "Sign-in failed: \($0)" } ?? "Sign-in failed for \(displayTitle)"
                    errorMessage = message
                    store.showToast(message)
                    onHaptic(.warning)
                default:
                    onHaptic(.success)
                }
                await refreshAfterSignIn()
            } catch {
                let nsError = error as NSError
                if nsError.domain == ASWebAuthenticationSessionErrorDomain,
                   nsError.code == ASWebAuthenticationSessionError.canceledLogin.rawValue {
                    store.showToast("Sign-in cancelled")
                    return
                }
                errorMessage = error.localizedDescription
            }
        }
    }

    private func refreshAfterSignIn() async {
        async let appsTask: Void = store.refreshApps()
        async let workTask: Void = store.refreshWorkAndInteractions()
        async let summaryTask: Void = store.refreshSummary()
        _ = await (appsTask, workTask, summaryTask)
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
