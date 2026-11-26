import CryptoKit
import Foundation
import Security


public enum NoriConfig {
    public static let serverURL = URL(string: "https://nori-core-8273801370.us-central1.run.app")!

    public static var webURL: URL {
        if let raw = (Bundle.main.object(forInfoDictionaryKey: "NORI_WEB_URL") as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines),
           !raw.isEmpty,
           let url = URL(string: raw) {
            return url
        }
        return URL(string: "https://nori-docs-elaoqxqkuq-uc.a.run.app")!
    }

    public static func legalURL(path: String) -> URL {
        let normalized = path.hasPrefix("/") ? String(path.dropFirst()) : path
        return webURL.appendingPathComponent(normalized)
    }

    public static let oauthCallbackURL = "nori://oauth/callback"

    public static var googleOAuthClientID: String {
        (Bundle.main.object(forInfoDictionaryKey: "GOOGLE_OAUTH_CLIENT_ID") as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }

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


public struct GoogleOAuthPKCE: Sendable {
    public let clientID: String
    public let redirectScheme: String
    public let redirectURI: String
    public let codeVerifier: String
    public let codeChallenge: String
    public let state: String
    private let preexchangedSession: AuthSession?

    public init(clientID: String = NoriConfig.googleOAuthClientID) {
        self.clientID = clientID
        self.redirectScheme = NoriConfig.reversedClientIDScheme(for: clientID)
        self.redirectURI = NoriConfig.googleRedirectURI(for: clientID)
        let verifier = Self.randomBase64URL(byteCount: 32)
        self.codeVerifier = verifier
        let digest = SHA256.hash(data: Data(verifier.utf8))
        self.codeChallenge = Self.base64URL(Data(digest))
        self.state = Self.randomBase64URL(byteCount: 16)
        self.preexchangedSession = nil
    }

    private init(
        clientID: String,
        redirectScheme: String,
        redirectURI: String,
        codeVerifier: String,
        codeChallenge: String,
        state: String,
        preexchangedSession: AuthSession?
    ) {
        self.clientID = clientID
        self.redirectScheme = redirectScheme
        self.redirectURI = redirectURI
        self.codeVerifier = codeVerifier
        self.codeChallenge = codeChallenge
        self.state = state
        self.preexchangedSession = preexchangedSession
    }

    public func withExchangedSession(_ session: AuthSession) -> GoogleOAuthPKCE {
        GoogleOAuthPKCE(
            clientID: clientID,
            redirectScheme: redirectScheme,
            redirectURI: redirectURI,
            codeVerifier: codeVerifier,
            codeChallenge: codeChallenge,
            state: state,
            preexchangedSession: session
        )
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
        if let preexchangedSession {
            return preexchangedSession
        }
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
        guard let payload = decodeJWTPayload(jwt),
              let email = payload["email"] as? String else {
            return nil
        }
        return email.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }

    public static func extractNameFromJWT(_ jwt: String) -> String? {
        guard let payload = decodeJWTPayload(jwt),
              let name = payload["name"] as? String else {
            return nil
        }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    private static func decodeJWTPayload(_ jwt: String) -> [String: Any]? {
        let parts = jwt.split(separator: ".")
        guard parts.count >= 2 else { return nil }
        var base64 = String(parts[1])
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        while base64.count % 4 != 0 { base64.append("=") }
        guard let data = Data(base64Encoded: base64),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return nil
        }
        return json
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


public actor AuthTokenProvider {
    private let keychain: KeychainStore
    private let account = "nori.google.auth.session.v1"
    private let urlSession: URLSession
    private let clientID: String?
    private var cached: AuthSession?
    private var generation: UInt64 = 0

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
        generation &+= 1
        persistSession(session)
    }

    private func persistSession(_ session: AuthSession) {
        cached = session
        if let data = try? JSONEncoder().encode(session),
           let raw = String(data: data, encoding: .utf8) {
            keychain.saveToken(raw, account: account)
        }
    }

    public func clearSession() {
        generation &+= 1
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
        let activeGeneration = generation
        do {
            var refreshed = try await GoogleOAuthPKCE.refreshIDToken(
                refreshToken: refreshToken,
                clientID: clientID ?? NoriConfig.googleOAuthClientID,
                session: urlSession
            )
            guard activeGeneration == generation else { throw CancellationError() }
            if let latest = cached, latest != current { return latest.idToken }
            if refreshed.email.isEmpty {
                refreshed.email = current.email
            }
            persistSession(refreshed)
            return refreshed.idToken
        } catch let apiErr as NoriAPIError where apiErr.statusCode == 400 || apiErr.statusCode == 401 {
            guard activeGeneration == generation else { throw CancellationError() }
            if let latest = cached, latest != current { return latest.idToken }
            clearSession()
            throw NoriAPIError(
                message: "Session expired. Please sign in again.",
                statusCode: 401,
                code: apiErr.code ?? "invalid_grant",
                retryable: false
            )
        } catch let apiErr as NoriAPIError {
            throw apiErr
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            throw NoriAPIError(
                message: error.localizedDescription,
                statusCode: 0,
                code: "network_error",
                retryable: true
            )
        }
    }
}


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


public enum UserResolveOutcome: Sendable, Equatable {
    case resolved(userID: String)
    case inviteRequired(message: String)
    case forbidden(message: String)
}

public struct NoriProvisioningClient: Sendable {
    public let serverURL: URL
    private let urlSession: URLSession

    public init(serverURL: URL = NoriConfig.serverURL, urlSession: URLSession = .shared) {
        self.serverURL = serverURL
        self.urlSession = urlSession
    }

    public func resolveUser(
        idToken: String,
        email: String? = nil,
        name: String? = nil
    ) async throws -> UserResolveOutcome {
        let url = serverURL.appendingPathComponent("api/users/resolve")
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if !idToken.isEmpty {
            req.setValue("Bearer \(idToken)", forHTTPHeaderField: "Authorization")
        }
        var payload: [String: String] = [:]
        if let email, !email.isEmpty { payload["email"] = email }
        if let name, !name.isEmpty { payload["name"] = name }
        req.httpBody = try JSONEncoder().encode(payload)

        let (data, response) = try await urlSession.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw NoriAPIError(message: "Invalid response from server.", statusCode: 0, code: "invalid_response", retryable: true)
        }
        if (200..<300).contains(http.statusCode) {
            struct ResolveResponse: Decodable { let id: String }
            let decoded = try JSONDecoder().decode(ResolveResponse.self, from: data)
            return .resolved(userID: decoded.id)
        }
        let body = decodeErrorBody(data)
        if http.statusCode == 403 {
            if body.code == "invite_required" {
                return .inviteRequired(message: body.error ?? "An invite code is required to access Nori.")
            }
            return .forbidden(message: body.error ?? "This Google account is not authorized to access Nori.")
        }
        throw NoriAPIError(
            message: body.error ?? "Nori could not check your account. Try again shortly.",
            statusCode: http.statusCode,
            code: body.code,
            retryable: http.statusCode == 429 || http.statusCode >= 500
        )
    }

    public func redeemInvite(
        idToken: String,
        inviteCode: String,
        email: String? = nil,
        name: String? = nil
    ) async throws -> String {
        let trimmed = inviteCode.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            throw NoriAPIError(message: "Enter an invite code.", statusCode: 400, code: "invite_code_required", retryable: false)
        }
        let url = serverURL.appendingPathComponent("api/users/redeem")
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if !idToken.isEmpty {
            req.setValue("Bearer \(idToken)", forHTTPHeaderField: "Authorization")
        }
        var payload: [String: String] = ["invite_code": trimmed]
        if let email, !email.isEmpty { payload["email"] = email }
        if let name, !name.isEmpty { payload["name"] = name }
        req.httpBody = try JSONEncoder().encode(payload)

        let (data, response) = try await urlSession.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw NoriAPIError(message: "Invalid response from server.", statusCode: 0, code: "invalid_response", retryable: true)
        }
        if (200..<300).contains(http.statusCode) {
            struct RedeemResponse: Decodable { let id: String }
            let decoded = try JSONDecoder().decode(RedeemResponse.self, from: data)
            return decoded.id
        }
        let body = decodeErrorBody(data)
        throw NoriAPIError(
            message: body.error ?? "Invalid invite code.",
            statusCode: http.statusCode,
            code: body.code,
            retryable: http.statusCode == 429 || http.statusCode >= 500
        )
    }

    private func decodeErrorBody(_ data: Data) -> (error: String?, code: String?) {
        struct ErrorEnvelope: Decodable {
            let error: String?
            let code: String?
        }
        guard let decoded = try? JSONDecoder().decode(ErrorEnvelope.self, from: data) else {
            return (nil, nil)
        }
        return (decoded.error, decoded.code)
    }
}
