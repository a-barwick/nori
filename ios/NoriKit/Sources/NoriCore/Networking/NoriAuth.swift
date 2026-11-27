import CryptoKit
import Foundation
import Security

public enum NoriConfig {
    private static func makeSafeURL(host: String) -> URL {
        var components = URLComponents()
        components.scheme = "https"
        components.host = host
        return components.url ?? URL(fileURLWithPath: "/")
    }

    private static let defaultServerURL = makeSafeURL(host: "nori-core-8273801370.us-central1.run.app")
    private static let defaultWebURL = makeSafeURL(host: "www.noribot.chat")

    private static func configuredURL(infoKey: String, fallback: URL) -> URL {
        if let raw = (Bundle.main.object(forInfoDictionaryKey: infoKey) as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines),
           !raw.isEmpty,
           let url = URL(string: raw),
           let scheme = url.scheme?.lowercased(),
           (scheme == "https" || scheme == "http"),
           url.host != nil {
            return url
        }
        return fallback
    }

    public static var serverURL: URL {
        configuredURL(infoKey: "NORI_SERVER_URL", fallback: defaultServerURL)
    }

    public static var webURL: URL {
        configuredURL(infoKey: "NORI_WEB_URL", fallback: defaultWebURL)
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

public enum UserResolveOutcome: Sendable, Equatable {
    case resolved(userID: String)
    case inviteRequired(message: String)
    case forbidden(message: String)
}

public struct GoogleOAuthPKCE: Sendable {
    public let clientID: String
    public let redirectScheme: String
    public let redirectURI: String
    public let codeVerifier: String
    public let codeChallenge: String
    public let state: String

    private static let tokenEndpointURL: URL = {
        var c = URLComponents()
        c.scheme = "https"
        c.host = "oauth2.googleapis.com"
        c.path = "/token"
        return c.url ?? URL(fileURLWithPath: "/")
    }()

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
                message: "Google Sign-In is unavailable in this build. Please update or reinstall Nori.",
                statusCode: 0,
                code: "oauth_not_configured"
            )
        }
        var components = URLComponents()
        components.scheme = "https"
        components.host = "accounts.google.com"
        components.path = "/o/oauth2/v2/auth"
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
            throw NoriAPIError(message: "Invalid OAuth callback URL.", statusCode: 400, code: "invalid_oauth_callback")
        }
        var values: [String: String] = [:]
        var seenKeys: Set<String> = []
        let strictSingleKeys: Set<String> = ["state", "code", "error"]
        for item in components.queryItems ?? [] {
            if strictSingleKeys.contains(item.name) {
                if !seenKeys.insert(item.name).inserted {
                    throw NoriAPIError(
                        message: "Invalid OAuth callback URL: duplicate \(item.name) parameter.",
                        statusCode: 400,
                        code: "duplicate_oauth_param",
                        retryable: false
                    )
                }
            }
            if let value = item.value {
                values[item.name] = value
            }
        }
        if let err = values["error"] {
            throw NoriAPIError(message: err == "access_denied" ? "Sign-in was cancelled." : "Google Sign-In failed (\(err)).")
        }
        guard values["state"] == state else {
            throw NoriAPIError(message: "OAuth state verification failed. Please try again.")
        }
        guard let code = values["code"], !code.isEmpty else {
            throw NoriAPIError(message: "Missing authorization code from Google.")
        }
        return code
    }

    public func exchangeCode(_ code: String, session: URLSession = .shared) async throws -> AuthSession {
        var req = URLRequest(url: Self.tokenEndpointURL)
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
                message: "Google Sign-In is unavailable in this build. Please update or reinstall Nori.",
                statusCode: 0,
                code: "missing_client_id",
                retryable: false
            )
        }
        var req = URLRequest(url: tokenEndpointURL)
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
        let status = SecRandomCopyBytes(kSecRandomDefault, byteCount, &bytes)
        if status != errSecSuccess {
            NoriLogger.auth.error("auth.sec_random_failed", attributes: ["osstatus": String(status)])
            var rng = SystemRandomNumberGenerator()
            for index in bytes.indices {
                bytes[index] = UInt8.random(in: .min ... .max, using: &rng)
            }
        }
        return base64URL(Data(bytes))
    }

    private static func base64URL(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

public protocol TokenStoring: Sendable {
    @discardableResult
    func saveToken(_ token: String, account: String) -> Bool
    func loadToken(account: String) -> String?
    @discardableResult
    func deleteToken(account: String) -> Bool
}

public actor AuthTokenProvider {
    private let keychain: any TokenStoring
    private let account = "nori.google.auth.session.v1"
    private let urlSession: URLSession
    private let clientID: String?
    private var cached: AuthSession?
    private var generation: UInt64 = 0
    private var inFlightRefresh: (generation: UInt64, task: Task<String, Error>)?

    public init(
        keychain: any TokenStoring = KeychainStore(),
        urlSession: URLSession = .shared,
        clientID: String? = nil
    ) {
        self.keychain = keychain
        self.urlSession = urlSession
        self.clientID = clientID
    }

    public init(
        tokenStore: any TokenStoring,
        urlSession: URLSession = .shared,
        clientID: String? = nil
    ) {
        self.keychain = tokenStore
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
        inFlightRefresh?.task.cancel()
        inFlightRefresh = nil
        persistSession(session)
    }

    @discardableResult
    private func persistSession(_ session: AuthSession) -> Bool {
        cached = session
        guard let data = try? JSONEncoder().encode(session),
              let raw = String(data: data, encoding: .utf8) else {
            NoriLogger.auth.error("auth.session_encode_failed", remote: false)
            return false
        }
        let saved = keychain.saveToken(raw, account: account)
        if !saved {
            NoriLogger.auth.warning("auth.keychain_save_failed", remote: false)
        }
        return saved
    }

    public func clearSession() {
        generation &+= 1
        inFlightRefresh?.task.cancel()
        inFlightRefresh = nil
        cached = nil
        if !keychain.deleteToken(account: account) {
            NoriLogger.auth.warning("auth.keychain_delete_failed", remote: false)
        }
    }

    public func validIDToken(forceRefresh: Bool = false) async throws -> String {
        guard let current = loadSession() else {
            throw NoriAPIError(message: "Sign in required.", statusCode: 401, code: "no_session", retryable: false)
        }
        if !forceRefresh && !current.isExpiringSoon {
            return current.idToken
        }
        if let existing = inFlightRefresh, existing.generation == generation {
            return try await existing.task.value
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
        let resolvedClientID = clientID ?? NoriConfig.googleOAuthClientID
        let session = urlSession
        let task = Task<String, Error> {
            try await self.performRefresh(
                current: current,
                refreshToken: refreshToken,
                clientID: resolvedClientID,
                urlSession: session,
                activeGeneration: activeGeneration
            )
        }
        inFlightRefresh = (generation: activeGeneration, task: task)
        defer {
            if inFlightRefresh?.generation == activeGeneration {
                inFlightRefresh = nil
            }
        }
        return try await task.value
    }

    private func performRefresh(
        current: AuthSession,
        refreshToken: String,
        clientID: String,
        urlSession: URLSession,
        activeGeneration: UInt64
    ) async throws -> String {
        do {
            var refreshed = try await GoogleOAuthPKCE.refreshIDToken(
                refreshToken: refreshToken,
                clientID: clientID,
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

public struct KeychainStore: TokenStoring, Sendable {
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
        let attributesToUpdate: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        ]
        let updateStatus = SecItemUpdate(query as CFDictionary, attributesToUpdate as CFDictionary)
        if updateStatus == errSecSuccess {
            return true
        }
        if updateStatus == errSecItemNotFound {
            var addAttrs = query
            addAttrs[kSecValueData as String] = data
            addAttrs[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            let addStatus = SecItemAdd(addAttrs as CFDictionary, nil)
            return addStatus == errSecSuccess
        }
        return false
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
