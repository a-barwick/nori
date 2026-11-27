import AuthenticationServices
import SwiftUI
import NoriCore
import NoriDesign

public struct SignInView: View {
    @Bindable private var store: NoriStore
    @Environment(\.webAuthenticationSession) private var webAuthenticationSession
    @State private var isAuthenticating = false
    @State private var localError: String?
    @State private var pendingInviteSession: AuthSession?

    public init(store: NoriStore) {
        self.store = store
    }

    public var body: some View {
        if let pendingSession = pendingInviteSession {
            InviteRedeemView(
                store: store,
                session: pendingSession,
                onRedeemed: {
                    pendingInviteSession = nil
                },
                onCancel: {
                    pendingInviteSession = nil
                    localError = nil
                    store.authError = nil
                }
            )
        } else {
            signInContent
        }
    }

    private var isBusy: Bool {
        isAuthenticating || store.authState == .signingIn
    }

    private var signInContent: some View {
        GeometryReader { proxy in
            ScrollView {
                VStack(spacing: 24) {
                    Spacer(minLength: 24)

                    VStack(spacing: 14) {
                        NoriMark(size: 56, state: isBusy ? .thinking : .idle)
                        Text("Nori")
                            .font(NoriTypography.display)
                            .foregroundStyle(NoriColors.ink)
                            .accessibilityAddTraits(.isHeader)
                    }

                    if let errorMessage = localError ?? store.authError {
                        NoriNotice(
                            title: "Sign-in failed",
                            detail: errorMessage,
                            tone: .danger,
                            systemImage: "exclamationmark.triangle.fill"
                        )
                        .padding(.horizontal, 24)
                    }

                    Button {
                        startGoogleSignIn()
                    } label: {
                        HStack(spacing: 10) {
                            if isBusy {
                                ProgressView()
                                    .tint(NoriColors.disabled)
                            } else {
                                Image(systemName: "person.badge.key.fill")
                            }
                            Text("Sign in with Google")
                        }
                    }
                    .buttonStyle(NoriButtonStyle(.primary, fullWidth: true, large: true))
                    .disabled(isBusy)
                    .padding(.horizontal, 24)

                    Spacer(minLength: 24)

                    HStack(spacing: 16) {
                        Link("Privacy Policy", destination: NoriConfig.legalURL(path: "/privacy"))
                        Link("Terms of Service", destination: NoriConfig.legalURL(path: "/terms"))
                    }
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink3)
                    .padding(.bottom, 20)
                }
                .frame(maxWidth: 420)
                .frame(maxWidth: .infinity, minHeight: proxy.size.height)
            }
        }
        .background(NoriColors.canvas.ignoresSafeArea())
    }

    private func friendlySignInError(_ error: Error, fallback: String) -> String {
        if let apiErr = error as? NoriAPIError {
            if apiErr.isAuthError {
                return "This Google account is not authorized to access Nori."
            }
            if apiErr.statusCode >= 500 || apiErr.statusCode == 0 {
                return "Nori is temporarily unavailable. Try again in a moment."
            }
            if !apiErr.message.isEmpty {
                return apiErr.message
            }
            return fallback
        }
        if let urlErr = error as? URLError {
            switch urlErr.code {
            case .notConnectedToInternet, .networkConnectionLost:
                return "You appear to be offline. Check your connection and try again."
            case .timedOut:
                return "Sign-in timed out. Check your connection and try again."
            default:
                return "Could not reach Nori. Check your connection and try again."
            }
        }
        return fallback
    }

    private func startGoogleSignIn() {
        localError = nil
        store.authError = nil
        let pkce = GoogleOAuthPKCE()
        let authorizeURL: URL
        do {
            authorizeURL = try pkce.authorizationURL()
        } catch {
            localError = friendlySignInError(error, fallback: "Could not start Google sign-in. Try again.")
            return
        }

        isAuthenticating = true
        Task {
            defer { isAuthenticating = false }
            do {
                let callbackURL = try await webAuthenticationSession.authenticate(
                    using: authorizeURL,
                    callback: .customScheme(pkce.redirectScheme),
                    preferredBrowserSession: .shared,
                    additionalHeaderFields: [:]
                )
                let outcome = try await store.beginGoogleSignIn(callbackURL: callbackURL, pkce: pkce)
                switch outcome {
                case .signedIn:
                    break
                case .inviteRequired(let session):
                    pendingInviteSession = session
                case .forbidden(let message):
                    localError = message
                }
            } catch let authErr as ASWebAuthenticationSessionError where authErr.code == .canceledLogin {
                // User cancelled Google sign-in sheet.
            } catch {
                localError = friendlySignInError(error, fallback: "Could not complete sign-in. Try again.")
            }
        }
    }
}
