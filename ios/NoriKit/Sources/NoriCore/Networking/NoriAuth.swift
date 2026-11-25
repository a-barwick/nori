import CryptoKit
import Foundation
import Security

// MARK: - Live Production Configuration

public enum NoriConfig {
    /// Live Nori Core production endpoint on Cloud Run.
    public static let serverURL = URL(string: "https://nori-core-8273801370.us-central1.run.app")!

    /// iOS deep-link callback target for Connections and MCP OAuth sign-in.
    public static let oauthCallbackURL = "nori://oauth/callback"

    /// Google OAuth 2.0 iOS Client ID configured in Info.plist (`GOOGLE_OAUTH_CLIENT_ID`).
    public static var googleOAuthClientID: String {
        (Bundle.main.object(forInfoDictionaryKey: "GOOGLE_OAUTH_CLIENT_ID") as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }

    /// Reversed Google Client ID URL scheme (e.g. `com.googleusercontent.apps.<id>`).
    public static func reversedClientIDScheme(for clientID: String = googleOAuthClientID) -> String {
        let suffix = ".apps.googleusercontent.com"
        guard clientID.hasSuffix(suffix) else { return "nori" }
        let prefix = String(clientID.dropLast(suffix.count))
        return "com.googleusercontent.apps.\(prefix)"
    }

    public static func googleRedirectURI(for clientID: String = googleOAuthClientID) -> String {
        "\(reversedClientIDScheme(for: clientID)):/oauth2redirect"
    }
}

// MARK: - Auth Session & State

public enum AuthState: Sendable, Equatable {
    case signedOut
    case signingIn
    case signedIn(email: String)

    public var isSignedIn: Bool {
        if case .signedIn = self { return true }
        return false
    }

    public var email: String? {
        if case .signedIn(let email) = self { return email }
        return nil
    }
}

public struct AuthSession: Codable, Sendable, Equatable {
    public var idToken: String
    public var refreshToken: String?
    public var expiresAt: Date
    public var email: String

    public init(idToken: String, refreshToken: String?, expiresAt: Date, email: String) {
        self.idToken = idToken
        self.refreshToken = refreshToken
        self.expiresAt = expiresAt
        self.email = email
    }

    public var isExpiringSoon: Bool {
        Date().addingTimeInterval(60) >= expiresAt
    }
}

// MARK: - Google OAuth 2.0 + PKCE

public struct GoogleOAuthPKCE: Sendable {
    public let clientID: String
    public let redirectScheme: String
    public let redirectURI: String
    public let codeVerifier: String
    public let codeChallenge: String
    public let state: String

    public init(clientID: String = NoriConfig.googleOAuthClientID) {
        self.clientID = clientID
        self.redirectScheme = NoriConfig.reversedClientIDScheme(for: clientID)
        self.redirectURI = NoriConfig.googleRedirectURI(for: clientID)
        let verifier = Self.randomBase64URL(byteCount: 32)
        self.codeVerifier = verifier
        let digest = SHA256.hash(data: Data(verifier.utf8))
        self.codeChallenge = Self.base64URL(Data(digest))
        self.state = Self.randomBase64URL(byteCount: 16)
    }

    public func authorizationURL() throws -> URL {
        guard !clientID.isEmpty, !clientID.contains("REPLACE_WITH") else {
            throw NoriAPIError(
                message: "Google Sign-In is not configured yet. Set GOOGLE_OAUTH_CLIENT_ID in Info.plist.",
                statusCode: 0
            )
        }
        var components = URLComponents(string: "https://accounts.google.com/o/oauth2/v2/auth")!
        components.queryItems = [
            URLQueryItem(name: "client_id", value: clientID),
            URLQueryItem(name: "redirect_uri", value: redirectURI),
            URLQueryItem(name: "response_type", value: "code"),
            URLQueryItem(name: "scope", value: "openid email profile"),
            URLQueryItem(name: "code_challenge", value: codeChallenge),
            URLQueryItem(name: "code_challenge_method", value: "S256"),
            URLQueryItem(name: "state", value: state),
            URLQueryItem(name: "access_type", value: "offline"),
            URLQueryItem(name: "prompt", value: "select_account")
        ]
        guard let url = components.url else {
            throw NoriAPIError(message: "Could not build Google authorization URL.")
        }
        return url
    }

    public func extractAuthorizationCode(from callbackURL: URL) throws -> String {
        guard let components = URLComponents(url: callbackURL, resolvingAgainstBaseURL: false) else {
            throw NoriAPIError(message: "Invalid OAuth callback URL.")
        }
        let items = Dictionary(uniqueKeysWithValues: (components.queryItems ?? []).compactMap { item in
            item.value.map { (item.name, $0) }
        })
        if let err = items["error"] {
            throw NoriAPIError(message: err == "access_denied" ? "Sign-in was cancelled." : "Google Sign-In failed (\(err)).")
        }
        guard items["state"] == state else {
            throw NoriAPIError(message: "OAuth state verification failed. Please try again.")
        }
        guard let code = items["code"], !code.isEmpty else {
            throw NoriAPIError(message: "Missing authorization code from Google.")
        }
        return code
    }

    public func exchangeCode(_ code: String, session: URLSession = .shared) async throws -> AuthSession {
        var req = URLRequest(url: URL(string: "https://oauth2.googleapis.com/token")!)
        req.httpMethod = "POST"
        req.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        req.httpBody = Self.formEncode([
            ("client_id", clientID),
            ("code", code),
            ("code_verifier", codeVerifier),
            ("grant_type", "authorization_code"),
            ("redirect_uri", redirectURI)
        ])

        let (data, response) = try await session.data(for: req)
        return try Self.decodeTokenResponse(data: data, response: response, existingRefreshToken: nil)
    }

    public static func refreshIDToken(
        refreshToken: String,
        clientID: String = NoriConfig.googleOAuthClientID,
        session: URLSession = .shared
    ) async throws -> AuthSession {
        guard !clientID.isEmpty else {
            throw NoriAPIError(
                message: "Google Sign-In client ID is missing.",
                statusCode: 0,
                code: "missing_client_id",
                retryable: false
            )
        }
        var req = URLRequest(url: URL(string: "https://oauth2.googleapis.com/token")!)
        req.httpMethod = "POST"
        req.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        req.httpBody = formEncode([
            ("client_id", clientID),
            ("refresh_token", refreshToken),
            ("grant_type", "refresh_token")
        ])

        let (data, response) = try await session.data(for: req)
        return try decodeTokenResponse(data: data, response: response, existingRefreshToken: refreshToken)
    }

    private static func decodeTokenResponse(
        data: Data,
        response: URLResponse,
        existingRefreshToken: String?
    ) throws -> AuthSession {
        guard let http = response as? HTTPURLResponse else {
            throw NoriAPIError(
                message: "Invalid response from Google token endpoint.",
                statusCode: 0,
                code: "invalid_response",
                retryable: true
            )
        }
        guard (200..<300).contains(http.statusCode) else {
            struct OAuthErrorResponse: Decodable {
                let error: String?
                let error_description: String?
            }
            let oauthErr = try? JSONDecoder().decode(OAuthErrorResponse.self, from: data)
            if http.statusCode == 400 || http.statusCode == 401 {
                throw NoriAPIError(
                    message: oauthErr?.error_description ?? "Google token exchange failed. Please sign in again.",
                    statusCode: http.statusCode,
                    code: oauthErr?.error ?? "invalid_grant",
                    retryable: false
                )
            }
            throw NoriAPIError(
                message: oauthErr?.error_description ?? "Google authentication service temporarily unavailable (\(http.statusCode)).",
                statusCode: http.statusCode,
                code: oauthErr?.error ?? "token_endpoint_unavailable",
                retryable: true
            )
        }
        struct TokenResponse: Decodable {
            let id_token: String?
            let refresh_token: String?
            let expires_in: Int?
        }
        let decoded = try JSONDecoder().decode(TokenResponse.self, from: data)
        guard let idToken = decoded.id_token, !idToken.isEmpty else {
            throw NoriAPIError(
                message: "Google did not return an ID token.",
                statusCode: 502,
                code: "missing_id_token",
                retryable: true
            )
        }
        let email = extractEmailFromJWT(idToken) ?? ""
        let expiresIn = TimeInterval(decoded.expires_in ?? 3500)
        return AuthSession(
            idToken: idToken,
            refreshToken: decoded.refresh_token ?? existingRefreshToken,
            expiresAt: Date().addingTimeInterval(expiresIn),
            email: email
        )
    }

    public static func extractEmailFromJWT(_ jwt: String) -> String? {
        let parts = jwt.split(separator: ".")
        guard parts.count >= 2 else { return nil }
        var base64 = String(parts[1])
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        while base64.count % 4 != 0 { base64.append("=") }
        guard let data = Data(base64Encoded: base64),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let email = json["email"] as? String else {
            return nil
        }
        return email.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }

    private static func formEncode(_ pairs: [(String, String)]) -> Data {
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._~")
        let body = pairs.map { key, value in
            let k = key.addingPercentEncoding(withAllowedCharacters: allowed) ?? key
            let v = value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
            return "\(k)=\(v)"
        }.joined(separator: "&")
        return Data(body.utf8)
    }

    private static func randomBase64URL(byteCount: Int) -> String {
        var bytes = [UInt8](repeating: 0, count: byteCount)
        _ = SecRandomCopyBytes(kSecRandomDefault, byteCount, &bytes)
        return base64URL(Data(bytes))
    }

    private static func base64URL(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

// MARK: - Auth Token Provider (Keychain-backed, Auto-Refreshing)

public actor AuthTokenProvider {
    private let keychain: KeychainStore
    private let account = "nori.google.auth.session.v1"
    private let urlSession: URLSession
    private let clientID: String?
    private var cached: AuthSession?

    public init(
        keychain: KeychainStore = KeychainStore(),
        urlSession: URLSession = .shared,
        clientID: String? = nil
    ) {
        self.keychain = keychain
        self.urlSession = urlSession
        self.clientID = clientID
    }

    public func loadSession() -> AuthSession? {
        if let cached { return cached }
        guard let raw = keychain.loadToken(account: account),
              let data = raw.data(using: .utf8),
              let decoded = try? JSONDecoder().decode(AuthSession.self, from: data) else {
            return nil
        }
        cached = decoded
        return decoded
    }

    public func saveSession(_ session: AuthSession) {
        cached = session
        if let data = try? JSONEncoder().encode(session),
           let raw = String(data: data, encoding: .utf8) {
            keychain.saveToken(raw, account: account)
        }
    }

    public func clearSession() {
        cached = nil
        keychain.deleteToken(account: account)
    }

    public func validIDToken(forceRefresh: Bool = false) async throws -> String {
        guard let current = loadSession() else {
            throw NoriAPIError(message: "Sign in required.", statusCode: 401, code: "no_session", retryable: false)
        }
        if !forceRefresh && !current.isExpiringSoon {
            return current.idToken
        }
        guard let refreshToken = current.refreshToken, !refreshToken.isEmpty else {
            clearSession()
            throw NoriAPIError(
                message: "Session expired. Please sign in again.",
                statusCode: 401,
                code: "missing_refresh_token",
                retryable: false
            )
        }
        do {
            var refreshed = try await GoogleOAuthPKCE.refreshIDToken(
                refreshToken: refreshToken,
                clientID: clientID ?? NoriConfig.googleOAuthClientID,
                session: urlSession
            )
            if refreshed.email.isEmpty {
                refreshed.email = current.email
            }
            saveSession(refreshed)
            return refreshed.idToken
        } catch let apiErr as NoriAPIError where apiErr.statusCode == 400 || apiErr.statusCode == 401 {
            // Only clear persisted session when Google explicitly rejects the refresh token (HTTP 400/401).
            clearSession()
            throw NoriAPIError(
                message: "Session expired. Please sign in again.",
                statusCode: 401,
                code: apiErr.code ?? "invalid_grant",
                retryable: false
            )
        } catch let apiErr as NoriAPIError {
            // Preserve session on transient 5xx/429 or configuration errors.
            throw apiErr
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            // Preserve session on transient URLSession/network/DNS/timeout failures.
            throw NoriAPIError(
                message: error.localizedDescription,
                statusCode: 0,
                code: "network_error",
                retryable: true
            )
        }
    }
}

// MARK: - Keychain Helper

public struct KeychainStore: Sendable {
    public let service: String

    public init(service: String = "com.nori.ios") {
        self.service = service
    }

    @discardableResult
    public func saveToken(_ token: String, account: String) -> Bool {
        let data = Data(token.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        SecItemDelete(query as CFDictionary)
        var attrs = query
        attrs[kSecValueData as String] = data
        attrs[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(attrs as CFDictionary, nil)
        return status == errSecSuccess
    }

    public func loadToken(account: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne
        ]
        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        guard status == errSecSuccess, let data = result as? Data else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    @discardableResult
    public func deleteToken(account: String) -> Bool {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        let status = SecItemDelete(query as CFDictionary)
        return status == errSecSuccess || status == errSecItemNotFound
    }
}
