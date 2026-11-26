import AuthenticationServices
import SwiftUI
import NoriCore
import NoriDesign

public struct SignInView: View {
    private struct PendingInvite {
        let callbackURL: URL
        let pkce: GoogleOAuthPKCE
        let session: AuthSession
    }

    @Bindable private var store: NoriStore
    @Environment(\.webAuthenticationSession) private var webAuthenticationSession
    @State private var isAuthenticating = false
    @State private var localError: String?
    @State private var pendingInvite: PendingInvite?

    public init(store: NoriStore) {
        self.store = store
    }

    public var body: some View {
        if let pending = pendingInvite {
            InviteRedeemView(
                session: pending.session,
                onRedeemed: {
                    pendingInvite = nil
                    await store.completeGoogleSignIn(callbackURL: pending.callbackURL, pkce: pending.pkce)
                },
                onCancel: {
                    pendingInvite = nil
                    localError = nil
                    store.authError = nil
                }
            )
        } else {
            signInContent
        }
    }

    private var signInContent: some View {
        VStack(spacing: 24) {
            Spacer()

            VStack(spacing: 14) {
                NoriMark(size: 52, state: isAuthenticating ? .thinking : .idle)
                Text("Nori")
                    .font(.system(size: 32, weight: .bold))
                    .foregroundStyle(NoriColors.ink)
                Text("Sign in with your Google account to continue.")
                    .font(NoriTypography.meta)
                    .foregroundStyle(NoriColors.ink2)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 32)
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
                    if isAuthenticating || store.authState == .signingIn {
                        ProgressView()
                            .tint(.white)
                    } else {
                        Image(systemName: "person.badge.key.fill")
                    }
                    Text("Sign in with Google")
                        .font(.system(size: 16, weight: .semibold))
                }
                .frame(maxWidth: .infinity)
                .frame(height: 48)
            }
            .buttonStyle(NoriButtonStyle(.primary, fullWidth: true))
            .disabled(isAuthenticating || store.authState == .signingIn)
            .padding(.horizontal, 24)

            Spacer()

            HStack(spacing: 16) {
                Link("Privacy Policy", destination: NoriConfig.legalURL(path: "/privacy"))
                Link("Terms of Service", destination: NoriConfig.legalURL(path: "/terms"))
            }
            .font(.system(size: 13))
            .foregroundStyle(NoriColors.ink3)
            .padding(.bottom, 20)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(NoriColors.canvas)
    }

    private func startGoogleSignIn() {
        localError = nil
        store.authError = nil
        let pkce = GoogleOAuthPKCE()
        let authorizeURL: URL
        do {
            authorizeURL = try pkce.authorizationURL()
        } catch {
            localError = error.localizedDescription
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
                let code = try pkce.extractAuthorizationCode(from: callbackURL)
                let authSession = try await pkce.exchangeCode(code)
                let preexchangedPKCE = pkce.withExchangedSession(authSession)
                let client = NoriProvisioningClient()
                let name = GoogleOAuthPKCE.extractNameFromJWT(authSession.idToken)
                let outcome = try await client.resolveUser(
                    idToken: authSession.idToken,
                    email: authSession.email,
                    name: name
                )
                switch outcome {
                case .resolved:
                    await store.completeGoogleSignIn(callbackURL: callbackURL, pkce: preexchangedPKCE)
                case .inviteRequired:
                    pendingInvite = PendingInvite(
                        callbackURL: callbackURL,
                        pkce: preexchangedPKCE,
                        session: authSession
                    )
                case .forbidden(let message):
                    localError = message
                }
            } catch let authErr as ASWebAuthenticationSessionError where authErr.code == .canceledLogin {
            } catch {
                localError = error.localizedDescription
            }
        }
    }
}
