import Foundation
import Testing
@testable import NoriCore


private final class StubURLProtocol: URLProtocol, @unchecked Sendable {
    typealias Handler = @Sendable (URLRequest) throws -> (HTTPURLResponse, Data)

    private static let lock = NSLock()
    nonisolated(unsafe) private static var handler: Handler?

    static func setHandler(_ newHandler: Handler?) {
        lock.lock()
        handler = newHandler
        lock.unlock()
    }

    private static func currentHandler() -> Handler? {
        lock.lock()
        defer { lock.unlock() }
        return handler
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let handler = Self.currentHandler() else {
            client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse))
            return
        }
        do {
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            if !data.isEmpty {
                client?.urlProtocol(self, didLoad: data)
            }
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}

    static func makeSession() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        return URLSession(configuration: config)
    }
}

private func requestBodyData(_ request: URLRequest) -> Data {
    if let body = request.httpBody {
        return body
    }
    guard let stream = request.httpBodyStream else { return Data() }
    stream.open()
    defer { stream.close() }
    var data = Data()
    var buffer = [UInt8](repeating: 0, count: 4096)
    while stream.hasBytesAvailable {
        let read = stream.read(&buffer, maxLength: buffer.count)
        if read <= 0 { break }
        data.append(buffer, count: read)
    }
    return data
}

private func makeFakeJWT(email: String) -> String {
    let header = Data(#"{"alg":"RS256","typ":"JWT"}"#.utf8).base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
    let payload = Data(#"{"email":"\#(email)"}"#.utf8).base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
    return "\(header).\(payload).sig"
}

private func httpResponse(
    url: URL,
    statusCode: Int,
    headers: [String: String] = ["Content-Type": "application/json"]
) -> HTTPURLResponse {
    HTTPURLResponse(url: url, statusCode: statusCode, httpVersion: "HTTP/1.1", headerFields: headers)!
}


@Suite("Auth Refresh & LiveNoriTransport Resilience", .serialized)
struct AuthAndLiveTransportTests {

    private func makeProvider(session: URLSession) -> AuthTokenProvider {
        AuthTokenProvider(
            tokenStore: InMemoryTokenStore(),
            urlSession: session,
            clientID: "test-client-id.apps.googleusercontent.com"
        )
    }

    @Test("Google connection requests scoped access and opens the Drive picker for the signed-in user")
    func googleConnectionRequests() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "alex@example.com"), refreshToken: "refresh",
            expiresAt: Date().addingTimeInterval(3_600), email: "alex@example.com"
        ))
        defer { StubURLProtocol.setHandler(nil) }
        StubURLProtocol.setHandler { request in
            let url = try #require(request.url)
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            #expect(url.path.hasPrefix("/users/usr-1/v1/"))
            switch url.path {
            case "/users/usr-1/v1/integrations":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"items":[{"id":"google","title":"Google","availability":"available"}]}"#.utf8))
            case "/users/usr-1/v1/connections":
                let body = try #require(JSONSerialization.jsonObject(with: requestBodyData(request)) as? [String: Any])
                let value = try #require(body["value"] as? [String: Any])
                #expect(value["integration_id"] as? String == "google")
            case "/users/usr-1/v1/connections/conn-google/actions":
                let body = try #require(JSONSerialization.jsonObject(with: requestBodyData(request)) as? [String: Any])
                let value = try #require(body["value"] as? [String: Any])
                #expect(value["return_to"] as? String == "nori://oauth/callback")
                #expect(value["capabilities"] as? [String] == ["files.read", "files.write"])
            case "/users/usr-1/v1/connections/conn-google/google-drive/picker":
                let body = try #require(JSONSerialization.jsonObject(with: requestBodyData(request)) as? [String: Any])
                #expect(body["return_to"] as? String == "nori://oauth/callback")
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"picker-1","authorize_url":"https://example.com/picker","expires_at":"2026-11-26T00:00:00Z"}"#.utf8))
            case "/users/usr-1/v1/connections/conn-google/resources":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"items":[{"ref":{"connection_id":"conn-google","kind":"drive_file","provider_id":"file-1"},"title":"Report","kind":"drive_file","selected":true,"writable":true}]}"#.utf8))
            default:
                Issue.record("Unexpected request \(url.path)")
                return (httpResponse(url: url, statusCode: 404), Data())
            }
            let response = #"{"request_id":"req-1","accepted":true,"status":"applied","resource":{"id":"conn-google","revision":1,"integration_id":"google","title":"Google","status":"not_connected","granted_scopes":[],"selected_resource_ids":[],"health":{},"defaults":[]},"auth":{"id":"attempt-1","authorize_url":"https://example.com/oauth","expires_at":"2026-11-26T00:00:00Z"}}"#
            return (httpResponse(url: url, statusCode: 200), Data(response.utf8))
        }
        let transport = LiveNoriTransport(baseURL: URL(string: "https://nori.example")!, authProvider: provider, urlSession: urlSession)
        #expect(try await transport.fetchIntegrations().first?.id == "google")
        let created = try await transport.createConnection(integrationID: "google", requestID: "req-1")
        let connected = try await transport.performConnectionAction(
            connectionID: created.resource.id, expectedRevision: created.resource.revision, action: "connect",
            returnTo: "nori://oauth/callback", capabilities: ["files.read", "files.write"], requestID: "req-2"
        )
        #expect(connected.auth?.id == "attempt-1")
        #expect(try await transport.startGoogleDrivePicker(connectionID: created.resource.id, returnTo: "nori://oauth/callback").id == "picker-1")
        #expect(try await transport.fetchConnectionResources(connectionID: created.resource.id).first?.title == "Report")
    }

    @Test("API costs use the authenticated owner and requested range")
    func apiCostsWireRequest() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "alex@example.com"),
            refreshToken: "refresh",
            expiresAt: Date().addingTimeInterval(3_600),
            email: "alex@example.com"
        ))
        defer { StubURLProtocol.setHandler(nil) }
        StubURLProtocol.setHandler { request in
            let url = try #require(request.url)
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            #expect(url.path == "/users/usr-1/v1/settings/api-costs")
            #expect(URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems == [URLQueryItem(name: "days", value: "7")])
            #expect(request.httpMethod == "GET")
            #expect(request.value(forHTTPHeaderField: "Authorization")?.hasPrefix("Bearer ") == true)
            let json = #"{"days":7,"timezone":"America/Los_Angeles","start_at":"2026-11-19T08:00:00Z","end_at":"2026-11-26T08:00:00Z","bucket_size":"day","totals":{"cost_usd":0.12,"priced_calls":1,"unpriced_calls":0,"local_calls":0,"pending_calls":0},"models":[],"buckets":[]}"#
            return (httpResponse(url: url, statusCode: 200), Data(json.utf8))
        }
        let transport = LiveNoriTransport(baseURL: URL(string: "https://nori.test")!, authProvider: provider, urlSession: urlSession)
        let response = try await transport.fetchAPICosts(days: 7)
        #expect(response.days == 7)
        #expect(response.totals.costUSD == 0.12)
    }

    @Test("Service keys wire endpoints (GET/PUT/DELETE) and 402/403 structured error decoding")
    func serviceKeysAndBillingErrorWireContract() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "alex@example.com"),
            refreshToken: "refresh",
            expiresAt: Date().addingTimeInterval(3_600),
            email: "alex@example.com"
        ))
        defer { StubURLProtocol.setHandler(nil) }

        let capturedMethods = LockedArray<String>()
        StubURLProtocol.setHandler { request in
            let url = try #require(request.url)
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            capturedMethods.append("\(request.httpMethod ?? "GET") \(url.path)")
            switch (request.httpMethod, url.path) {
            case ("GET", "/users/usr-1/v1/settings/service-keys"):
                let json = #"{"services":[{"service":"exa","label":"Exa Web Search","configured":false,"source":"platform"}]}"#
                return (httpResponse(url: url, statusCode: 200), Data(json.utf8))
            case ("PUT", "/users/usr-1/v1/settings/service-keys/exa"):
                let body = try #require(JSONSerialization.jsonObject(with: requestBodyData(request)) as? [String: Any])
                if body["api_key"] as? String == "trial_blocked" {
                    return (
                        httpResponse(url: url, statusCode: 402),
                        Data(#"{"error":"Bring-your-own API keys require a paid plan.","code":"byok_not_allowed"}"#.utf8)
                    )
                }
                let json = #"{"service":"exa","label":"Exa Web Search","configured":true,"last4":"4321","masked_key":"••••4321","source":"byok","updated_at":"2026-11-28T01:00:00Z","validated_at":"2026-11-28T01:00:00Z"}"#
                return (httpResponse(url: url, statusCode: 200), Data(json.utf8))
            case ("DELETE", "/users/usr-1/v1/settings/service-keys/exa"):
                let json = #"{"service":"exa","label":"Exa Web Search","configured":false,"source":"platform"}"#
                return (httpResponse(url: url, statusCode: 200), Data(json.utf8))
            case ("POST", "/users/usr-1/api/turn"):
                let body = try #require(JSONSerialization.jsonObject(with: requestBodyData(request)) as? [String: Any])
                switch body["message"] as? String {
                case "exhausted":
                    return (httpResponse(url: url, statusCode: 402), Data(#"{"error":"","code":"credit_exhausted"}"#.utf8))
                case "inactive":
                    return (httpResponse(url: url, statusCode: 402), Data(#"{"error":"","code":"subscription_inactive"}"#.utf8))
                default:
                    return (httpResponse(url: url, statusCode: 403), Data(#"{"error":"Your account has been suspended.","code":"account_suspended"}"#.utf8))
                }
            default:
                return (httpResponse(url: url, statusCode: 404), Data())
            }
        }

        let transport = LiveNoriTransport(baseURL: URL(string: "https://nori.test")!, authProvider: provider, urlSession: urlSession)

        let listed = try await transport.fetchServiceKeys()
        #expect(listed.services.count == 1)
        #expect(listed.services[0].service == "exa")
        #expect(listed.services[0].source == "platform")

        do {
            _ = try await transport.saveServiceKey(service: "exa", apiKey: "trial_blocked")
            Issue.record("Expected 402 byok_not_allowed")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 402)
            #expect(err.isBYOKNotAllowed)
        }

        let saved = try await transport.saveServiceKey(service: "exa", apiKey: "exa_live_4321")
        #expect(saved.configured == true)
        #expect(saved.maskedKeyDisplay == "••••4321")
        #expect(saved.source == "byok")

        let deleted = try await transport.deleteServiceKey(service: "exa")
        #expect(deleted.configured == false)
        #expect(deleted.source == "platform")

        do {
            _ = try await transport.sendTurn(id: "t1", message: "exhausted", attachmentIDs: [])
            Issue.record("Expected 402 credit_exhausted")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 402)
            #expect(err.isCreditExhausted)
            #expect(err.billingAccessBlock?.code == .creditExhausted)
        }

        do {
            _ = try await transport.sendTurn(id: "t2", message: "inactive", attachmentIDs: [])
            Issue.record("Expected 402 subscription_inactive")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 402)
            #expect(err.isSubscriptionInactive)
            #expect(err.billingAccessBlock?.code == .subscriptionInactive)
        }

        do {
            _ = try await transport.sendTurn(id: "t3", message: "suspended", attachmentIDs: [])
            Issue.record("Expected 403 account_suspended")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 403)
            #expect(err.isAccountSuspended)
            #expect(!err.isAuthError)
            #expect(err.billingAccessBlock?.code == .accountSuspended)
        }
    }

    @Test("SSE preserves 403 account_suspended code and message from server body")
    func ssePreservesAccountSuspendedCode() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: "jwt-1",
            refreshToken: "refresh-1",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))
        defer { StubURLProtocol.setHandler(nil) }

        StubURLProtocol.setHandler { req in
            let url = req.url!
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            return (
                httpResponse(url: url, statusCode: 403),
                Data(#"{"error":"Account suspended by admin.","code":"account_suspended"}"#.utf8)
            )
        }

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession,
            sseInitialBackoffSeconds: 0.01,
            sseRolloverDelayMilliseconds: 5
        )
        do {
            for try await _ in transport.eventStream(after: 0) {}
            Issue.record("Expected 403 account_suspended error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 403)
            #expect(err.isAccountSuspended)
            #expect(err.message == "Account suspended by admin.")
        }
    }

    @Test("Reset sends confirmation to the authenticated user and accepts an empty response")
    func resetDataWirePayload() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let resumeFlags = LockedArray<Bool>()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "alex@example.com"),
            refreshToken: "refresh",
            expiresAt: Date().addingTimeInterval(3_600),
            email: "alex@example.com"
        ))
        defer { StubURLProtocol.setHandler(nil) }
        StubURLProtocol.setHandler { request in
            let url = try #require(request.url)
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            #expect(url.path == "/users/usr-1/v1/settings/reset")
            #expect(request.httpMethod == "POST")
            #expect(request.value(forHTTPHeaderField: "Authorization")?.hasPrefix("Bearer ") == true)
            let body = try #require(JSONSerialization.jsonObject(with: requestBodyData(request)) as? [String: Any])
            #expect(body["confirmation"] as? String == "RESET")
            resumeFlags.append(body["resume_pending"] as? Bool ?? false)
            return (httpResponse(url: url, statusCode: 204), Data())
        }
        let transport = LiveNoriTransport(baseURL: URL(string: "https://nori.test")!, authProvider: provider, urlSession: urlSession)
        try await transport.resetUserData(confirmation: "RESET")
        try await transport.resumePendingDataReset()
        #expect(resumeFlags.values == [false, true])
    }


    @Test("Sends the advertised model on the authenticated turn request")
    func selectedModelTurnWirePayload() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "alex@example.com"),
            refreshToken: "refresh",
            expiresAt: Date().addingTimeInterval(3_600),
            email: "alex@example.com"
        ))
        defer { StubURLProtocol.setHandler(nil) }
        StubURLProtocol.setHandler { request in
            let url = try #require(request.url)
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            #expect(url.path == "/users/usr-1/api/turn")
            #expect(request.httpMethod == "POST")
            let body = try #require(JSONSerialization.jsonObject(with: requestBodyData(request)) as? [String: Any])
            #expect(body["id"] as? String == "turn-one")
            #expect(body["message"] as? String == "Hello")
            #expect(body["model"] as? [String: String] == ["chat_profile_id": "chat-fixture", "chat_reasoning": "high", "task_profile_id": "task-fixture"])
            return (httpResponse(url: url, statusCode: 202), Data(#"{"messages":[],"runs":[],"results":[],"paused":false,"position":7}"#.utf8))
        }
        let transport = LiveNoriTransport(baseURL: URL(string: "https://nori.test")!, authProvider: provider, urlSession: urlSession)
        let session = try await transport.sendTurn(
            id: "turn-one",
            message: "Hello",
            attachmentIDs: [],
            modelSelection: ModelSelection(chatProfileID: "chat-fixture", chatReasoning: "high", taskProfileID: "task-fixture")
        )
        #expect(session.position == 7)
    }

    @Test("Preserves session on transient network timeout and 5xx token endpoint errors")
    func authRefreshPreservesSessionOnTransientErrors() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        let expiredSession = AuthSession(
            idToken: "old-token",
            refreshToken: "refresh-abc",
            expiresAt: Date().addingTimeInterval(-60),
            email: "alex@example.com"
        )
        await provider.saveSession(expiredSession)

        StubURLProtocol.setHandler { _ in
            throw URLError(.timedOut)
        }
        do {
            _ = try await provider.validIDToken()
            Issue.record("Expected timeout error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 0)
            #expect(err.retryable == true)
        }
        #expect(await provider.loadSession() == expiredSession)

        StubURLProtocol.setHandler { req in
            (httpResponse(url: req.url!, statusCode: 503), Data(#"{"error":"temporarily_unavailable"}"#.utf8))
        }
        do {
            _ = try await provider.validIDToken()
            Issue.record("Expected 503 error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 503)
            #expect(err.retryable == true)
        }
        #expect(await provider.loadSession() == expiredSession)

        StubURLProtocol.setHandler { req in
            (httpResponse(url: req.url!, statusCode: 429), Data("{}".utf8))
        }
        do {
            _ = try await provider.validIDToken()
            Issue.record("Expected 429 error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 429)
            #expect(err.retryable == true)
        }
        #expect(await provider.loadSession() == expiredSession)

        StubURLProtocol.setHandler(nil)
    }

    @Test("Clears session only when Google token endpoint explicitly rejects refresh token (400/401)")
    func authRefreshClearsSessionOnTerminalRejection() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        let expiredSession = AuthSession(
            idToken: "old-token",
            refreshToken: "revoked-refresh",
            expiresAt: Date().addingTimeInterval(-60),
            email: "alex@example.com"
        )
        await provider.saveSession(expiredSession)

        StubURLProtocol.setHandler { req in
            (httpResponse(url: req.url!, statusCode: 400), Data(#"{"error":"invalid_grant","error_description":"Token has been expired or revoked."}"#.utf8))
        }
        do {
            _ = try await provider.validIDToken()
            Issue.record("Expected terminal auth error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 401)
            #expect(err.code == "invalid_grant")
            #expect(err.retryable == false)
        }
        #expect(await provider.loadSession() == nil)

        await provider.saveSession(expiredSession)
        StubURLProtocol.setHandler { req in
            (httpResponse(url: req.url!, statusCode: 401), Data(#"{"error":"unauthorized_client"}"#.utf8))
        }
        do {
            _ = try await provider.validIDToken()
            Issue.record("Expected terminal 401 error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 401)
            #expect(err.retryable == false)
        }
        #expect(await provider.loadSession() == nil)

        StubURLProtocol.setHandler(nil)
    }

    @Test("validIDToken(forceRefresh: true) refreshes even when cached token is not expired")
    func authForceRefresh() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        let freshSession = AuthSession(
            idToken: "cached-still-valid",
            refreshToken: "refresh-123",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        )
        await provider.saveSession(freshSession)

        let newJWT = makeFakeJWT(email: "alex@example.com")
        let callCounter = LockedCounter()
        StubURLProtocol.setHandler { req in
            callCounter.increment()
            return (
                httpResponse(url: req.url!, statusCode: 200),
                Data(#"{"id_token":"\#(newJWT)","expires_in":3600}"#.utf8)
            )
        }

        let cachedToken = try await provider.validIDToken(forceRefresh: false)
        #expect(cachedToken == "cached-still-valid")
        #expect(callCounter.value == 0)

        let refreshedToken = try await provider.validIDToken(forceRefresh: true)
        #expect(refreshedToken == newJWT)
        #expect(callCounter.value == 1)
        #expect(await provider.loadSession()?.idToken == newJWT)
        #expect(await provider.loadSession()?.refreshToken == "refresh-123")

        StubURLProtocol.setHandler(nil)
    }


    @Test("SSE retries once with force-refreshed token on 401 and succeeds on 200")
    func sseRetriesOn401WithForceRefresh() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: "stale-jwt",
            refreshToken: "refresh-ok",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))

        let freshJWT = makeFakeJWT(email: "alex@example.com")
        let sseAttempts = LockedCounter()
        let seenAuthHeaders = LockedArray<String>()

        StubURLProtocol.setHandler { req in
            let url = req.url!
            if url.host == "oauth2.googleapis.com" {
                return (
                    httpResponse(url: url, statusCode: 200),
                    Data(#"{"id_token":"\#(freshJWT)","expires_in":3600}"#.utf8)
                )
            }
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            if url.path.hasSuffix("/v1/events") {
                let attempt = sseAttempts.incrementAndGet()
                seenAuthHeaders.append(req.value(forHTTPHeaderField: "Authorization") ?? "")
                if attempt == 1 {
                    return (httpResponse(url: url, statusCode: 401), Data())
                }
                let sseBody = """
                id: 5
                event: memory
                data: {"ref":"mem-5"}

                """
                return (
                    httpResponse(url: url, statusCode: 200, headers: ["Content-Type": "text/event-stream"]),
                    Data(sseBody.utf8)
                )
            }
            return (httpResponse(url: url, statusCode: 404), Data())
        }

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession,
            sseInitialBackoffSeconds: 0.01,
            sseRolloverDelayMilliseconds: 5
        )

        var received: [NoriStreamEvent] = []
        for try await event in transport.eventStream(after: 0) {
            received.append(event)
            if case .memory(let seq, let ref) = event {
                #expect(seq == 5)
                #expect(ref == "mem-5")
                break
            }
        }

        #expect(received.contains(.connectionState(.connecting)))
        #expect(received.contains(.connectionState(.connected)))
        #expect(seenAuthHeaders.values == ["Bearer stale-jwt", "Bearer \(freshJWT)"])

        StubURLProtocol.setHandler(nil)
    }

    @Test("SSE terminates with 401 error if retry after force-refresh still returns 401")
    func sseTerminatesOnPersistent401() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: "stale-jwt",
            refreshToken: "refresh-ok",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))

        let freshJWT = makeFakeJWT(email: "alex@example.com")
        let sseAttempts = LockedCounter()

        StubURLProtocol.setHandler { req in
            let url = req.url!
            if url.host == "oauth2.googleapis.com" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id_token":"\#(freshJWT)","expires_in":3600}"#.utf8))
            }
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            if url.path.hasSuffix("/v1/events") {
                sseAttempts.increment()
                return (httpResponse(url: url, statusCode: 401), Data())
            }
            return (httpResponse(url: url, statusCode: 404), Data())
        }

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession,
            sseInitialBackoffSeconds: 0.01,
            sseRolloverDelayMilliseconds: 5
        )

        do {
            for try await _ in transport.eventStream(after: 0) {}
            Issue.record("Expected stream to throw 401 error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 401)
            #expect(err.retryable == false)
        }
        #expect(sseAttempts.value == 2)

        StubURLProtocol.setHandler(nil)
    }

    @Test("SSE terminates immediately on 403 and on terminal invalid_grant during 401 refresh")
    func sseTerminatesOn403AndInvalidGrant() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: "jwt-1",
            refreshToken: "revoked-refresh",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession,
            sseInitialBackoffSeconds: 0.01,
            sseRolloverDelayMilliseconds: 5
        )

        let attempts403 = LockedCounter()
        StubURLProtocol.setHandler { req in
            let url = req.url!
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            attempts403.increment()
            return (httpResponse(url: url, statusCode: 403), Data())
        }
        do {
            for try await _ in transport.eventStream(after: 0) {}
            Issue.record("Expected 403 error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 403)
        }
        #expect(attempts403.value == 1)

        let attemptsInvalidGrant = LockedCounter()
        StubURLProtocol.setHandler { req in
            let url = req.url!
            if url.host == "oauth2.googleapis.com" {
                return (httpResponse(url: url, statusCode: 400), Data(#"{"error":"invalid_grant"}"#.utf8))
            }
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            attemptsInvalidGrant.increment()
            return (httpResponse(url: url, statusCode: 401), Data())
        }
        do {
            for try await _ in transport.eventStream(after: 0) {}
            Issue.record("Expected 401 terminal error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 401)
            #expect(err.code == "invalid_grant")
        }
        #expect(attemptsInvalidGrant.value == 1)
        #expect(await provider.loadSession() == nil)

        StubURLProtocol.setHandler(nil)
    }

    @Test("SSE reset event moves cursor backward and emits connection state transitions on transient failures")
    func sseResetBackwardCursorAndConnectionStates() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: "valid-jwt",
            refreshToken: "refresh-ok",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))

        let observedAfterParams = LockedArray<String>()
        let sseAttempt = LockedCounter()

        StubURLProtocol.setHandler { req in
            let url = req.url!
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            if url.path.hasSuffix("/v1/events") {
                let n = sseAttempt.incrementAndGet()
                let after = URLComponents(url: url, resolvingAgainstBaseURL: false)?
                    .queryItems?.first(where: { $0.name == "after" })?.value ?? ""
                observedAfterParams.append(after)

                switch n {
                case 1:
                    return (httpResponse(url: url, statusCode: 503), Data())
                case 2:
                    throw URLError(.notConnectedToInternet)
                case 3:
                    let body = "event: reset\ndata: {\"position\":12}\n\n"
                    return (httpResponse(url: url, statusCode: 200, headers: ["Content-Type": "text/event-stream"]), Data(body.utf8))
                default:
                    let body = "id: 13\nevent: memory\ndata: {\"ref\":\"m13\"}\n\n"
                    return (httpResponse(url: url, statusCode: 200, headers: ["Content-Type": "text/event-stream"]), Data(body.utf8))
                }
            }
            return (httpResponse(url: url, statusCode: 404), Data())
        }

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession,
            sseInitialBackoffSeconds: 0.01,
            sseRolloverDelayMilliseconds: 5
        )

        var states: [ConnectionState] = []
        var sawResetPos: Int?
        for try await event in transport.eventStream(after: 50) {
            switch event {
            case .connectionState(let state):
                states.append(state)
            case .reset(let pos):
                sawResetPos = pos
            case .memory(let seq, _):
                #expect(seq == 13)
                break
            default:
                continue
            }
            if case .memory = event { break }
        }

        #expect(sawResetPos == 12)
        #expect(states == [.connecting, .reconnecting, .offline, .connected, .connected])
        #expect(observedAfterParams.values == ["50", "50", "50", "12"])

        StubURLProtocol.setHandler(nil)
    }


    @Test("LiveNoriTransport handles library file fetch, MCP credential payload, OAuth return_to, summary, and parallel MCP tools")
    func liveTransportEndpointsAndPayloads() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: "valid-jwt",
            refreshToken: "refresh-ok",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))

        let capturedBodies = LockedDict<String, Data>()
        let capturedQueries = LockedDict<String, String>()
        let toolFetchIDs = LockedArray<String>()

        StubURLProtocol.setHandler { req in
            let url = req.url!
            let path = url.path
            let body = requestBodyData(req)
            if !body.isEmpty {
                capturedBodies.set(path, value: body)
            }
            if let q = url.query {
                capturedQueries.set(path, value: q)
            }

            switch path {
            case "/api/users/resolve":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            case "/users/usr-1/v1/library/doc-1/file":
                return (
                    httpResponse(url: url, statusCode: 200, headers: [
                        "Content-Type": "text/markdown; charset=utf-8",
                        "Content-Disposition": "attachment; filename=\"Quarterly Report.md\""
                    ]),
                    Data("# Quarterly Report\n\nFull markdown body.".utf8)
                )
            case "/users/usr-1/v1/library/doc-1/actions":
                let receiptJSON = """
                {
                  "request_id": "req-rev",
                  "accepted": true,
                  "status": "queued",
                  "resource": {
                    "id": "doc-1",
                    "root_id": "doc-1",
                    "version": 1,
                    "kind": "document",
                    "title": "Quarterly Report",
                    "provenance": {"content": "generated"},
                    "created_at": "2026-11-24T10:00:00Z",
                    "revision": 1
                  },
                  "work": {"task_id": "task-rev-42"}
                }
                """
                return (httpResponse(url: url, statusCode: 202), Data(receiptJSON.utf8))
            case "/users/usr-1/v1/mcp/mcp-1/credential":
                let receiptJSON = """
                {
                  "request_id": "req-cred",
                  "accepted": true,
                  "status": "applied",
                  "resource": {
                    "id": "mcp-1",
                    "revision": 2,
                    "source": {"kind": "user"},
                    "name": "Linear",
                    "transport": "http",
                    "endpoint": "https://mcp.linear.app",
                    "enabled": true,
                    "observed": {"status": "connected", "credential": "present"},
                    "auth": "configured"
                  }
                }
                """
                return (httpResponse(url: url, statusCode: 200), Data(receiptJSON.utf8))
            case "/users/usr-1/v1/mcp/mcp-1/actions":
                let receiptJSON = """
                {
                  "request_id": "req-mcp-oauth",
                  "accepted": true,
                  "status": "queued",
                  "resource": {
                    "id": "mcp-1",
                    "revision": 2,
                    "source": {"kind": "user"},
                    "name": "Linear",
                    "transport": "http",
                    "endpoint": "https://mcp.linear.app",
                    "enabled": true,
                    "observed": {"status": "needs_sign_in"},
                    "auth": "oauth"
                  },
                  "auth": {
                    "id": "att-mcp-1",
                    "authorize_url": "https://linear.app/oauth/authorize",
                    "expires_at": "2026-11-24T12:00:00Z"
                  }
                }
                """
                return (httpResponse(url: url, statusCode: 200), Data(receiptJSON.utf8))
            case "/users/usr-1/v1/connections/conn-1/actions":
                let receiptJSON = """
                {
                  "request_id": "req-conn-oauth",
                  "accepted": true,
                  "status": "queued",
                  "resource": {
                    "id": "conn-1",
                    "revision": 2,
                    "integration_id": "gmail",
                    "title": "Work Gmail",
                    "status": "connecting",
                    "granted_scopes": [],
                    "selected_resource_ids": [],
                    "health": {"code": "ready"},
                    "defaults": []
                  },
                  "auth": {
                    "id": "att-conn-1",
                    "authorize_url": "https://accounts.google.com/o/oauth2/auth",
                    "expires_at": "2026-11-24T12:00:00Z"
                  }
                }
                """
                return (httpResponse(url: url, statusCode: 200), Data(receiptJSON.utf8))
            case "/users/usr-1/v1/assistant/summary":
                return (
                    httpResponse(url: url, statusCode: 200),
                    Data(#"{"revision":99,"needs_you":2,"working":3,"apps_attention":1,"inbox_unread":4,"next_scheduled_at":"2026-11-25T09:00:00Z"}"#.utf8)
                )
            case "/users/usr-1/v1/capabilities":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"contract":"nori-chat-2","service":"nori","conversationModes":["assistant"],"features":{}}"#.utf8))
            case "/users/usr-1/v1/snapshot":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"messages":[],"runs":[],"position":7}"#.utf8))
            case "/users/usr-1/v1/mcp":
                let servers = """
                {"items":[
                  {"id":"s1","revision":1,"source":{"kind":"user"},"name":"S1","transport":"http","enabled":true,"observed":{"status":"connected"}},
                  {"id":"s2","revision":1,"source":{"kind":"user"},"name":"S2","transport":"http","enabled":true,"observed":{"status":"connected"}}
                ]}
                """
                return (httpResponse(url: url, statusCode: 200), Data(servers.utf8))
            case "/users/usr-1/v1/mcp/s1/tools", "/users/usr-1/v1/mcp/s2/tools":
                let sid = path.contains("/s1/") ? "s1" : "s2"
                toolFetchIDs.append(sid)
                let tools = #"{"items":[{"name":"tool_\#(sid)","title":"Tool \#(sid)","exposure":"discoverable","definition":{"id":"tool_\#(sid)","version":"1","digest":"sha256:1"},"availability":"ready"}]}"#
                return (httpResponse(url: url, statusCode: 200), Data(tools.utf8))
            case "/users/usr-1/v1/memory/working":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"revision":1,"items":[],"gist":"","turns":0,"core_bytes":0,"core_budget_bytes":24576}"#.utf8))
            case "/users/usr-1/v1/preferences":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"revision":1,"timezone":"UTC","working_hours":[],"default_destination_ids":[],"account_defaults":{},"budgets":{"task_usd":1.0}}"#.utf8))
            default:
                return (httpResponse(url: url, statusCode: 200), Data(#"{"items":[]}"#.utf8))
            }
        }

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession
        )

        let payload = try await transport.fetchLibraryItemFile(id: "doc-1", format: "md")
        #expect(payload.utf8Text == "# Quarterly Report\n\nFull markdown body.")
        #expect(payload.mediaType == "text/markdown; charset=utf-8")
        #expect(payload.filename == "Quarterly Report.md")
        #expect(capturedQueries.get("/users/usr-1/v1/library/doc-1/file") == "format=md")

        let revReceipt = try await transport.reviseLibraryItem(id: "doc-1", expectedRevision: 1, instruction: "Make concise", requestID: "req-rev")
        #expect(revReceipt.status == .queued)
        #expect(revReceipt.work?.taskID == "task-rev-42")

        let credReceipt = try await transport.setMCPCredential(serverID: "mcp-1", expectedRevision: 1, secret: "sk-live-secret", requestID: "req-cred")
        #expect(credReceipt.resource.observed.credential == "present")
        let credJSON = try JSONSerialization.jsonObject(with: capturedBodies.get("/users/usr-1/v1/mcp/mcp-1/credential") ?? Data()) as? [String: Any]
        #expect(credJSON?["request_id"] as? String == "req-cred")
        #expect(credJSON?["token"] as? String == "sk-live-secret")
        #expect(credJSON?["value"] == nil)

        let mcpAuthReceipt = try await transport.performMCPAction(
            serverID: "mcp-1",
            expectedRevision: 1,
            action: "sign_in",
            returnTo: NoriConfig.oauthCallbackURL,
            requestID: "req-mcp-oauth"
        )
        #expect(mcpAuthReceipt.auth?.id == "att-mcp-1")
        let mcpActionBody = try JSONSerialization.jsonObject(with: capturedBodies.get("/users/usr-1/v1/mcp/mcp-1/actions") ?? Data()) as? [String: Any]
        let mcpActionVal = mcpActionBody?["value"] as? [String: Any]
        #expect(mcpActionVal?["action"] as? String == "sign_in")
        #expect(mcpActionVal?["return_to"] as? String == "nori://oauth/callback")

        let connAuthReceipt = try await transport.performConnectionAction(
            connectionID: "conn-1",
            expectedRevision: 1,
            action: "connect",
            returnTo: NoriConfig.oauthCallbackURL,
            requestID: "req-conn-oauth"
        )
        #expect(connAuthReceipt.auth?.id == "att-conn-1")
        let connActionBody = try JSONSerialization.jsonObject(with: capturedBodies.get("/users/usr-1/v1/connections/conn-1/actions") ?? Data()) as? [String: Any]
        let connActionVal = connActionBody?["value"] as? [String: Any]
        #expect(connActionVal?["action"] as? String == "connect")
        #expect(connActionVal?["return_to"] as? String == "nori://oauth/callback")

        let summary = try await transport.fetchAssistantSummary()
        #expect(summary.revision == 99)
        #expect(summary.needsYou == 2)
        #expect(summary.working == 3)
        #expect(summary.appsAttention == 1)
        #expect(summary.inboxUnread == 4)
        #expect(summary.nextScheduledAt == "2026-11-25T09:00:00Z")

        let snap = try await transport.fetchFullSnapshot()
        #expect(snap.mcpServers.count == 2)
        #expect(Set(snap.mcpTools.keys) == Set(["s1", "s2"]))
        #expect(Set(toolFetchIDs.values) == Set(["s1", "s2"]))

        StubURLProtocol.setHandler(nil)
    }

    @Test("Concurrent validIDToken refreshes coalesce into a single OAuth token POST")
    func concurrentValidIDTokenCoalescesRefresh() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: "expired-jwt",
            refreshToken: "refresh-coalesce",
            expiresAt: Date().addingTimeInterval(-120),
            email: "alex@example.com"
        ))

        let refreshedJWT = makeFakeJWT(email: "alex@example.com")
        let tokenPostCount = LockedCounter()
        defer { StubURLProtocol.setHandler(nil) }

        StubURLProtocol.setHandler { req in
            let count = tokenPostCount.incrementAndGet()
            Thread.sleep(forTimeInterval: 0.03)
            return (
                httpResponse(url: req.url!, statusCode: 200),
                Data(#"{"id_token":"\#(refreshedJWT)-\#(count)","expires_in":3600}"#.utf8)
            )
        }

        let tokens = try await withThrowingTaskGroup(of: String.self) { group in
            for _ in 0..<5 {
                group.addTask {
                    try await provider.validIDToken()
                }
            }
            var results: [String] = []
            for try await tok in group {
                results.append(tok)
            }
            return results
        }

        #expect(tokenPostCount.value == 1)
        #expect(tokens.count == 5)
        #expect(Set(tokens) == Set(["\(refreshedJWT)-1"]))
    }

    @Test("Concurrent LiveNoriTransport requests coalesce userID resolution into one /api/users/resolve call")
    func concurrentEnsureUserIDCoalesces() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "alex@example.com"),
            refreshToken: "refresh-ok",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))

        let resolveCount = LockedCounter()
        defer { StubURLProtocol.setHandler(nil) }

        StubURLProtocol.setHandler { req in
            let url = req.url!
            if url.path == "/api/users/resolve" {
                resolveCount.increment()
                Thread.sleep(forTimeInterval: 0.03)
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-coalesced"}"#.utf8))
            }
            return (httpResponse(url: url, statusCode: 200), Data(#"{"revision":1,"needs_you":0,"working":0,"apps_attention":0,"inbox_unread":0}"#.utf8))
        }

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession
        )

        try await withThrowingTaskGroup(of: AssistantSummary.self) { group in
            for _ in 0..<5 {
                group.addTask {
                    try await transport.fetchAssistantSummary()
                }
            }
            for try await _ in group {}
        }

        #expect(resolveCount.value == 1)
    }

    @Test("IOS-11: LiveNoriTransport caches resolvedUserID per account, clears on sign-out, and re-resolves on 404")
    func liveTransportCachesAndInvalidatesResolvedUserID() async throws {
        let suiteName = "nori.test.userIDCache.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suiteName)!
        defer {
            StubURLProtocol.setHandler(nil)
            defaults.removePersistentDomain(forName: suiteName)
        }

        let resolveCount = LockedCounter()
        let currentValidUserID = LockedDict<String, String>()
        currentValidUserID.set("id", value: "usr-v1")

        let urlSession = StubURLProtocol.makeSession()
        StubURLProtocol.setHandler { req in
            let url = req.url!
            let path = url.path
            if path == "/api/users/resolve" {
                resolveCount.increment()
                let id = currentValidUserID.get("id") ?? "usr-v1"
                return (httpResponse(url: url, statusCode: 200), Data("{\"id\":\"\(id)\"}".utf8))
            }
            let expectedID = currentValidUserID.get("id") ?? "usr-v1"
            if path.contains("/users/\(expectedID)/v1/assistant/summary") {
                let body = #"{"revision":1,"needs_you":0,"working":0,"apps_attention":0,"inbox_unread":0}"#
                return (httpResponse(url: url, statusCode: 200), Data(body.utf8))
            }
            return (httpResponse(url: url, statusCode: 404), Data("{\"error\":\"not found\"}".utf8))
        }

        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "cache-user@example.com"),
            refreshToken: "refresh-ok",
            expiresAt: Date().addingTimeInterval(3600),
            email: "cache-user@example.com"
        ))

        let baseURL = URL(string: "https://cache-test.nori.local")!

        // 1. First transport resolves via /api/users/resolve and persists to UserDefaults.
        let transport1 = LiveNoriTransport(baseURL: baseURL, authProvider: provider, urlSession: urlSession, userDefaultsSuiteName: suiteName)
        _ = try await transport1.fetchAssistantSummary()
        #expect(resolveCount.value == 1)

        // 2. Fresh transport instance (simulating cold launch) uses cached userID with 0 /api/users/resolve calls.
        let transport2 = LiveNoriTransport(baseURL: baseURL, authProvider: provider, urlSession: urlSession, userDefaultsSuiteName: suiteName)
        _ = try await transport2.fetchAssistantSummary()
        #expect(resolveCount.value == 1)

        // 3. Stale cached userID returns 404 -> invalidates cache, re-resolves to usr-v2, and retries successfully.
        currentValidUserID.set("id", value: "usr-v2")
        let transport3 = LiveNoriTransport(baseURL: baseURL, authProvider: provider, urlSession: urlSession, userDefaultsSuiteName: suiteName)
        _ = try await transport3.fetchAssistantSummary()
        #expect(resolveCount.value == 2)

        // 4. Clearing identity removes the persisted userID so next transport resolves again.
        await transport3.clearCachedIdentity(email: "cache-user@example.com")
        let transport4 = LiveNoriTransport(baseURL: baseURL, authProvider: provider, urlSession: urlSession, userDefaultsSuiteName: suiteName)
        _ = try await transport4.fetchAssistantSummary()
        #expect(resolveCount.value == 3)
    }

    @Test("LiveNoriTransport.fetchFullSnapshot records per-domain partial errors instead of wiping failed domains")
    func liveSnapshotPreservesPartialErrorMetadata() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "alex@example.com"),
            refreshToken: "refresh-ok",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))
        defer { StubURLProtocol.setHandler(nil) }

        StubURLProtocol.setHandler { req in
            let url = req.url!
            switch url.path {
            case "/api/users/resolve":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            case "/users/usr-1/v1/snapshot":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"messages":[],"runs":[],"position":10}"#.utf8))
            case "/users/usr-1/v1/memory", "/users/usr-1/v1/automations":
                return (httpResponse(url: url, statusCode: 503), Data(#"{"error":"temporarily unavailable"}"#.utf8))
            case "/users/usr-1/v1/capabilities":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"contract":"nori-chat-2","service":"nori","conversationModes":["assistant"],"features":{}}"#.utf8))
            case "/users/usr-1/v1/assistant/summary":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"revision":1,"needs_you":0,"working":0,"apps_attention":0,"inbox_unread":0}"#.utf8))
            case "/users/usr-1/v1/memory/working":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"revision":1,"items":[],"gist":"","turns":0,"core_bytes":0,"core_budget_bytes":24576}"#.utf8))
            case "/users/usr-1/v1/preferences":
                return (httpResponse(url: url, statusCode: 200), Data(#"{"revision":1,"timezone":"UTC","working_hours":[],"default_destination_ids":[],"account_defaults":{},"budgets":{"task_usd":1.0}}"#.utf8))
            default:
                return (httpResponse(url: url, statusCode: 200), Data(#"{"items":[]}"#.utf8))
            }
        }

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession
        )
        let snap = try await transport.fetchFullSnapshot()
        #expect(snap.didLoadCoreMemories == false)
        #expect(snap.didLoadAutomations == false)
        #expect(snap.didLoadWorkItems == true)
        #expect(snap.partialErrors["core_memories"] != nil)
        #expect(snap.partialErrors["automations"] != nil)
    }

    @Test("LiveNoriTransport preserves APIErrorPayload.current on 409 conflict responses")
    func liveTransportPreservesCurrentOn409() async throws {
        let urlSession = StubURLProtocol.makeSession()
        let provider = makeProvider(session: urlSession)
        await provider.saveSession(AuthSession(
            idToken: makeFakeJWT(email: "alex@example.com"),
            refreshToken: "refresh-ok",
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))
        defer { StubURLProtocol.setHandler(nil) }

        StubURLProtocol.setHandler { req in
            let url = req.url!
            if url.path == "/api/users/resolve" {
                return (httpResponse(url: url, statusCode: 200), Data(#"{"id":"usr-1"}"#.utf8))
            }
            let payload = """
            {
              "error": "Revision mismatch",
              "code": "revision_conflict",
              "current": {
                "id": "int-conflict",
                "revision": 3,
                "kind": "approval",
                "status": "resolved",
                "prompt": "Send email?",
                "payload": {
                  "summary": "Send email",
                  "effect": "sensitive_send",
                  "account": {"connection_id": "conn-1", "label": "alex@example.com"},
                  "resources": []
                },
                "resolution": {"kind": "approve", "at": "2026-11-26T10:00:00Z"}
              }
            }
            """
            return (httpResponse(url: url, statusCode: 409), Data(payload.utf8))
        }

        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://nori.test")!,
            authProvider: provider,
            urlSession: urlSession
        )

        do {
            _ = try await transport.respondToInteraction(
                id: "int-conflict",
                expectedRevision: 1,
                answer: .approve(actionDigest: "sha256:1"),
                requestID: "req-1"
            )
            Issue.record("Expected 409 conflict error")
        } catch let err as NoriAPIError {
            #expect(err.statusCode == 409)
            #expect(err.isRevisionConflict)
            let current = err.decodedCurrent(as: Interaction.self)
            #expect(current?.id == "int-conflict")
            #expect(current?.revision == 3)
            #expect(current?.status == .resolved)
        }
    }
}


@Suite("Memory Receipts & NoriStore Domain Logic")
struct MemoryReceiptsAndStoreTests {

    @Test("Sign out clears the saved Google session and current user state")
    @MainActor
    func signOutClearsPersistentSession() async {
        let tokenStore = InMemoryTokenStore()
        let provider = AuthTokenProvider(tokenStore: tokenStore)
        let session = AuthSession(idToken: "test-token", refreshToken: "refresh", expiresAt: Date().addingTimeInterval(3600), email: "alex@example.com")
        await provider.saveSession(session)
        let initiallyReloadedProvider = AuthTokenProvider(tokenStore: tokenStore)
        #expect(await initiallyReloadedProvider.loadSession() == session)
        let store = NoriStore(transport: MockNoriTransport(), authProvider: provider)
        store.authState = .signedIn(email: session.email)
        store.session = Session(messages: [Message(id: "private", role: .user, text: "Private", createdAt: "2026-11-24T10:00:00Z")])

        await store.signOut()

        let reloadedProvider = AuthTokenProvider(tokenStore: tokenStore)
        #expect(await reloadedProvider.loadSession() == nil)
        #expect(store.authState == .signedOut)
        #expect(store.messages.isEmpty)
    }

    @Test("Reset preserves sign-in, clears cached data, and ignores stale local onboarding")
    @MainActor
    func resetClearsStateAndStartsOnboarding() async throws {
        let mock = MockNoriTransport()
        let email = "reset-\(UUID().uuidString)@example.com"
        let localKey = "nori.onboarding.done." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: localKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let provider = AuthTokenProvider(tokenStore: InMemoryTokenStore())
        let session = AuthSession(idToken: "test-token", refreshToken: "refresh", expiresAt: Date().addingTimeInterval(3600), email: email)
        await provider.saveSession(session)
        let store = NoriStore(transport: mock, authProvider: provider, resetMinimumDuration: .zero)
        store.authState = .signedIn(email: email)
        store.session = Session(messages: [Message(id: "old", role: .user, text: "Private", createdAt: "2026-11-24T10:00:00Z")])
        store.preferences = Preferences(onboardingCompletedAt: "2026-11-24T10:00:00Z")
        store.hasLoadedSnapshot = true
        UserDefaults.standard.set(true, forKey: localKey)

        await mock.setResetFails(true)
        await #expect(throws: NoriAPIError.self) { try await store.resetUserData(confirmation: "RESET") }
        #expect(store.messages.count == 1)
        #expect(!store.shouldShowOnboarding)

        await mock.setResetFails(false)
        try await store.resetUserData(confirmation: "RESET")
        #expect(store.authState == .signedOut)
        #expect(await provider.loadSession() == nil)
        #expect(store.messages.isEmpty)
        #expect(!UserDefaults.standard.bool(forKey: localKey))
    }

    @Test("Reset notification arriving before its request completes keeps the reset successful")
    @MainActor
    func resetEventBeforeResponse() async throws {
        let mock = MockNoriTransport()
        let email = "reset-race-\(UUID().uuidString)@example.com"
        let localKey = "nori.onboarding.done." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: localKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()), resetMinimumDuration: .zero)
        store.authState = .signedIn(email: email)
        store.session = Session(messages: [Message(id: "old", role: .user, text: "Private", createdAt: "2026-11-24T10:00:00Z")])
        store.hasLoadedSnapshot = true
        UserDefaults.standard.set(true, forKey: localKey)
        await mock.suspendNextReset()
        let resetting = Task { try await store.resetUserData(confirmation: "RESET") }
        await mock.waitForSuspendedReset()
        await mock.setPreferences(Preferences(dataResetAt: "2026-11-25T20:00:00Z"))
        await store.refreshPermissionsAndPreferences()
        #expect(store.shouldShowOnboarding)
        #expect(store.messages.isEmpty)
        await mock.finishSuspendedReset()
        try await resetting.value
        #expect(store.authState == .signedOut)
    }

    @Test("Nori's first welcome keeps onboarding open until the user participates")
    @MainActor
    func onboardingWithWelcome() {
        let store = NoriStore(transport: MockNoriTransport(), authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "welcome-\(UUID().uuidString)@example.com")
        store.hasLoadedSnapshot = true
        store.session = Session(messages: [Message(id: "welcome", role: .assistant, text: "Welcome", createdAt: "2026-11-27T10:00:00Z")])
        #expect(store.shouldShowOnboarding)

        store.session.messages.append(Message(id: "reply", role: .user, text: "Hi", createdAt: "2026-11-27T10:01:00Z"))
        #expect(!store.shouldShowOnboarding)
    }

    @Test("A reset event clears a second device's stale state and shows onboarding")
    @MainActor
    func remoteResetEvent() async {
        let mock = MockNoriTransport()
        let email = "reset-remote-\(UUID().uuidString)@example.com"
        let localKey = "nori.onboarding.done." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: localKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: email)
        store.session = Session(messages: [Message(id: "old", role: .user, text: "Private", createdAt: "2026-11-24T10:00:00Z")])
        store.hasLoadedSnapshot = true
        UserDefaults.standard.set(true, forKey: localKey)
        await mock.setPreferences(Preferences(dataResetAt: "2026-11-25T20:00:00Z"))
        await mock.setStreamEvents([.reset(position: 42)], terminalError: nil)
        store.startEventStream()

        await store.waitForEventStreamCompletion()
        await store.waitForBackgroundRefreshes()
        #expect(store.shouldShowOnboarding)
        #expect(store.messages.isEmpty)
        #expect(store.authState == .signedIn(email: email))
        await store.signOut()
    }

    @Test("A fresh launch reads onboarding state after a reset on another device")
    @MainActor
    func bootstrapDetectsRemoteReset() async {
        let mock = MockNoriTransport()
        let email = "reset-launch-\(UUID().uuidString)@example.com"
        let localKey = "nori.onboarding.done." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: localKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        UserDefaults.standard.set(true, forKey: localKey)
        await mock.setPreferences(Preferences(dataResetAt: "2026-11-25T20:00:00Z"))
        let provider = AuthTokenProvider(tokenStore: InMemoryTokenStore())
        await provider.saveSession(AuthSession(idToken: "test-token", refreshToken: nil, expiresAt: Date().addingTimeInterval(3600), email: email))
        let store = NoriStore(transport: mock, authProvider: provider)
        await store.bootstrapIfNeeded()
        await store.waitForChatContextCompletion()

        #expect(store.shouldShowOnboarding)
        #expect(store.authState == .signedIn(email: email))
        await store.signOut()
    }

    @Test("An interrupted reset offers recovery and confirms successful completion")
    @MainActor
    func resetRecoveryAndCompletion() async throws {
        let mock = MockNoriTransport()
        let email = "reset-recovery-\(UUID().uuidString)@example.com"
        let completionKey = "nori.data-reset.completion." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: completionKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let provider = AuthTokenProvider(tokenStore: InMemoryTokenStore())
        let session = AuthSession(idToken: "token", refreshToken: "refresh", expiresAt: Date().addingTimeInterval(3_600), email: email)
        await provider.saveSession(session)
        let store = NoriStore(transport: mock, authProvider: provider, resetMinimumDuration: .zero)
        store.authState = .signedIn(email: email)
        store.session = Session(messages: [Message(id: "old", role: .user, text: "Private")])
        await mock.setFailAfterStartingReset(true)

        await #expect(throws: NoriAPIError.self) { try await store.resetUserData(confirmation: "RESET") }
        #expect(store.dataResetRecoveryRequired)
        #expect(store.messages.isEmpty)
        #expect(store.authState == .signedIn(email: email))

        await mock.setFailResume(true)
        await #expect(throws: NoriAPIError.self) { try await store.finishPendingDataReset() }
        #expect(store.dataResetRecoveryRequired)
        await mock.setFailResume(false)
        try await store.finishPendingDataReset()
        #expect(!store.dataResetRecoveryRequired)
        #expect(store.authState == .signedOut)
        #expect(await mock.ordinaryResetCount == 1)
        #expect(await mock.resumeResetCount == 2)
        #expect(await provider.loadSession() == nil)
    }

    @Test("A stale recovery never starts a new reset or claims completion")
    @MainActor
    func staleResetRecovery() async throws {
        let mock = MockNoriTransport()
        let email = "reset-stale-\(UUID().uuidString)@example.com"
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer { UserDefaults.standard.removeObject(forKey: markerKey) }
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()), resetMinimumDuration: .zero)
        store.authState = .signedIn(email: email)
        await mock.setPreferences(Preferences(dataResetAt: "2026-11-24T20:00:00Z", dataResetPending: true))
        try await store.refreshDataResetStatus()
        #expect(store.dataResetRecoveryRequired)
        await mock.setPreferences(Preferences(dataResetAt: "2026-11-24T20:00:00Z"))
        try await store.finishPendingDataReset()
        #expect(await mock.resumeResetCount == 1)
        #expect(await mock.ordinaryResetCount == 0)
        #expect(!store.dataResetRecoveryRequired)
        #expect(store.dataResetStatusRequired)
        await store.signOut()
    }

    @Test("Reset loading lasts for the minimum and while the server is working")
    @MainActor
    func resetLoadingDuration() async throws {
        let mock = MockNoriTransport()
        let email = "reset-spinner-\(UUID().uuidString)@example.com"
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()), resetMinimumDuration: .milliseconds(80))
        store.authState = .signedIn(email: email)
        await mock.suspendNextReset()
        let started = ContinuousClock.now
        let resetting = Task { try await store.resetUserData(confirmation: "RESET") }
        await mock.waitForSuspendedReset()
        #expect(store.isResettingData)
        await mock.finishSuspendedReset()
        try await resetting.value
        #expect(ContinuousClock.now - started >= .milliseconds(80))
        #expect(!store.isResettingData)
        #expect(store.authState == .signedOut)
    }

    @Test("A fast reset still shows loading before signing out")
    @MainActor
    func fastResetLoadingDuration() async throws {
        let mock = MockNoriTransport()
        let email = "reset-fast-\(UUID().uuidString)@example.com"
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()), resetMinimumDuration: .milliseconds(80))
        store.authState = .signedIn(email: email)
        let started = ContinuousClock.now
        let resetting = Task { try await store.resetUserData(confirmation: "RESET") }
        await mock.waitForResetCompleted()
        #expect(store.isResettingData)
        try await resetting.value
        #expect(ContinuousClock.now - started >= .milliseconds(80))
        #expect(!store.isResettingData)
        #expect(store.authState == .signedOut)
    }

    @Test("Chat restores its snapshot and opens the stream without waiting for other screens", .timeLimit(.minutes(1)))
    @MainActor
    func bootstrapUsesChatSnapshotAndCursor() async {
        let mock = MockNoriTransport()
        let provider = AuthTokenProvider(tokenStore: InMemoryTokenStore())
        await provider.saveSession(AuthSession(
            idToken: "test-token",
            refreshToken: nil,
            expiresAt: Date().addingTimeInterval(3600),
            email: "alex@example.com"
        ))
        let store = NoriStore(transport: mock, authProvider: provider)
        await mock.suspendNextSession()
        let bootstrap = Task { await store.bootstrapIfNeeded() }
        await mock.waitForSuspendedSession()
        #expect(store.isLoading)
        #expect(await mock.fetchFullSnapshotCallCount == 0)
        await mock.finishSuspendedSession(Session(messages: [
            Message(id: "welcome", role: .assistant, text: "Welcome back", createdAt: "2026-11-24T10:00:00Z"),
        ], position: 42))
        await bootstrap.value
        await store.waitForChatContextCompletion()

        var cursors = mock.streamOpens.makeAsyncIterator()
        #expect(await cursors.next() == 42)
        #expect(store.messages.map(\.id) == ["welcome"])
        #expect(!store.isLoading)
        #expect(await mock.fetchSessionCallCount == 1)
        #expect(await mock.fetchSummaryCallCount == 1)
        #expect(await mock.fetchFullSnapshotCallCount == 0)
        #expect(await mock.fetchWorkItemsCallCount == 0)
        #expect(await mock.fetchMCPServersCallCount == 0)
        #expect(await mock.fetchConnectionsCallCount == 0)
        await store.signOut()
    }

    @Test("Cancelling a newer Work refresh preserves pending approvals from the earlier read", .timeLimit(.minutes(1)))
    @MainActor
    func cancelledWorkRefreshPreservesPendingApprovals() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        let approval = Interaction(
            id: "approval-1", kind: .approval, prompt: "Approve action",
            payload: .approval(ApprovalPayload(
                summary: "Approve action", effect: .unknown,
                account: ApprovalAccount(connectionID: "conn", label: "Connection")
            ))
        )
        await mock.suspendInteractionReads()
        var started = mock.interactionReads.makeAsyncIterator()
        let first = Task { await store.refreshWorkAndInteractions() }
        #expect(await started.next() == 0)
        #expect(!store.hasLoadedWorkOverview)
        let second = Task { await store.refreshWorkAndInteractions() }
        #expect(await started.next() == 1)
        second.cancel()
        await mock.finishInteractionRead(1, with: [])
        await second.value
        #expect(!store.hasLoadedWorkOverview)

        await mock.finishInteractionRead(0, with: [approval])
        await first.value
        #expect(store.hasLoadedWorkOverview)
        #expect(store.pendingInteractions == [approval])
        await store.refreshSummary()
        #expect(store.needsYouCount == 1)

        let older = Task { await store.refreshWorkAndInteractions() }
        #expect(await started.next() == 2)
        await mock.failInteractionReads()
        await store.refreshWorkAndInteractions()
        await mock.finishInteractionRead(2, with: [])
        await older.value
        #expect(!store.hasLoadedWorkOverview)
        #expect(store.pendingInteractions == [approval])
        #expect(store.needsYouCount == 4)

        await store.refreshAll()
        #expect(!store.hasLoadedWorkOverview)
        #expect(store.pendingInteractions == [approval])
    }

    @Test("A delayed session response cannot restore a previous identity's state")
    @MainActor
    func ignoresSessionResponseAfterSignOut() async {
        let transport = MockNoriTransport()
        let provider = AuthTokenProvider(tokenStore: InMemoryTokenStore())
        let store = NoriStore(transport: transport, authProvider: provider)
        store.authState = .signedIn(email: "first@example.com")
        await transport.suspendNextSession()
        let refresh = Task { await store.refreshSession() }
        await transport.waitForSuspendedSession()
        await store.signOut()
        store.authState = .signedIn(email: "second@example.com")
        store.session = Session(position: 3)
        await transport.finishSuspendedSession(Session(messages: [
            Message(id: "private", role: .assistant, text: "First account history", createdAt: "2026-11-24T10:00:00Z"),
        ], position: 99))
        await refresh.value
        #expect(store.messages.isEmpty)
        #expect(store.session.position == 3)
    }

    @Test("Partial memory refresh reports the failure and preserves the last working memory")
    @MainActor
    func preservesMemoryWhenOneSourceFails() async {
        let transport = MockNoriTransport()
        let store = NoriStore(transport: transport, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.workingMemory = WorkingMemory(revision: 12, items: [WorkingMemoryItem(id: "kept", kind: .fact, quote: "Keep this")])
        await transport.setWorkingMemoryFailure(true)
        await store.refreshMemory()
        #expect(store.memoryLogs.first?.revision == 21)
        #expect(store.workingMemory.items.first?.id == "kept")
        #expect(store.memoryRefreshError == "Working memory unavailable")

        await transport.setWorkingMemoryFailure(false)
        await store.refreshMemory()
        #expect(store.workingMemory.revision == 35)
        #expect(store.memoryRefreshError == nil)
    }

    @Test("Opening Apps after a startup snapshot fetches the integration catalog and retries partial failures")
    @MainActor
    func appsCatalogRefreshAfterSnapshot() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        let server = MCPServer(id: "app", name: "Custom app", observed: MCPObservedState(status: .connected))
        let google = IntegrationSummary(id: "google", title: "Google", availability: "available")
        let connection = Connection(id: "account", integrationID: "google", title: "Personal Google", status: .connected)
        await mock.setCustomSnapshot(NoriFullSnapshot())
        await mock.setApps(servers: [server], connections: [connection], integrations: [google])
        await mock.setFailingLists([.integrations])

        await store.refreshAll()
        #expect(store.integrationsRead == .pending)
        await store.refreshApps(force: false)
        #expect(store.integrationsRead == .failed("integrations unavailable"))
        #expect(store.connections == [connection])
        #expect(store.mcpServers == [server])
        #expect(store.mcpTools[server.id]?.first?.name == "issues_list")

        await mock.setFailingLists([.mcpServers])
        await store.refreshApps(force: false)
        #expect(store.integrations == [google])
        #expect(store.integrationsRead == .loaded)
        #expect(store.mcpServers == [server])
        #expect(store.mcpServersRead == .failed("mcpServers unavailable"))

        await mock.setFailingLists([.mcpTools, .connections, .integrations])
        await store.refreshApps(force: false)
        #expect(store.mcpTools[server.id]?.first?.name == "issues_list")
        #expect(store.connections == [connection])
        #expect(store.integrations == [google])

        await mock.setApps(servers: [], connections: [], integrations: [])
        await mock.setFailingLists([])
        await store.refreshApps(force: false)
        #expect(store.mcpServers.isEmpty)
        #expect(store.mcpTools.isEmpty)
        #expect(store.mcpServersRead == .loaded)
        #expect(store.connectionsRead == .loaded)
        #expect(store.integrationsRead == .loaded)
    }

    @Test("Partial fetchFullSnapshot preserves cached domains that failed while updating domains that succeeded")
    @MainActor
    func partialSnapshotPreservesCachedDomains() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        let cachedFact = MemoryFact(id: "fact-keep", text: "Keep this cached fact")
        let cachedItem = LibraryItem(id: "artifact-keep", title: "Keep this artifact")
        let cachedRule = PermissionRule(id: "rule-keep", effect: .ask)
        let cachedAuto = Automation(
            id: "auto-keep",
            revision: 1,
            title: "Morning brief",
            instruction: "Summarize calendar",
            status: .enabled,
            trigger: AutomationTrigger(kind: "cron", rrule: "FREQ=DAILY")
        )
        await mock.setCustomSnapshot(NoriFullSnapshot(
            libraryItems: [cachedItem],
            coreMemories: [cachedFact],
            automations: [cachedAuto],
            permissions: [cachedRule]
        ))
        await store.refreshAll()
        #expect(store.libraryRead == .loaded)
        #expect(store.coreMemoriesRead == .loaded)
        #expect(store.automationsRead == .loaded)
        #expect(store.permissionsRead == .loaded)

        await mock.setCustomSnapshot(NoriFullSnapshot(
            didLoadLibraryItems: false,
            didLoadCoreMemories: false,
            didLoadAutomations: false,
            didLoadPermissions: false,
            partialErrors: ["library": "503", "core_memories": "503", "automations": "503", "permissions": "503"]
        ))
        await store.refreshAll()
        #expect(store.libraryItems == [cachedItem])
        #expect(store.coreMemories == [cachedFact])
        #expect(store.automations == [cachedAuto])
        #expect(store.permissions == [cachedRule])
        #expect(store.libraryRead == .failed("503"))
        #expect(store.coreMemoriesRead == .failed("503"))
        #expect(store.memoryRefreshError == "503")
        #expect(store.automationsRead == .failed("503"))
        #expect(store.permissionsRead == .failed("503"))

        await mock.setFailingLists([.library, .automations, .permissions])
        await store.refreshLibrary(force: false)
        await store.refreshAutomations(force: false)
        await store.refreshPermissionsAndPreferences(force: false)
        #expect(store.libraryItems == [cachedItem])
        #expect(store.automations == [cachedAuto])
        #expect(store.permissions == [cachedRule])
        #expect(store.libraryRead == .failed("library unavailable"))
        #expect(store.automationsRead == .failed("automations unavailable"))
        #expect(store.permissionsRead == .failed("permissions unavailable"))

        await mock.setFailingLists([])
        await mock.setPermissions([])
        await store.refreshLibrary(force: false)
        await store.refreshAutomations(force: false)
        try? await store.refreshPermissionsThrowing()
        await store.refreshMemory(force: false)
        #expect(store.libraryItems.isEmpty)
        #expect(store.coreMemories.isEmpty)
        #expect(store.automations.isEmpty)
        #expect(store.permissions.isEmpty)
        #expect(store.libraryRead == .loaded)
        #expect(store.coreMemoriesRead == .loaded)
        #expect(store.memoryRefreshError == nil)
        #expect(store.automationsRead == .loaded)
        #expect(store.permissionsRead == .loaded)
    }

    @Test("Decodes all 6 backend memory action receipt shapes from core/internal/api/memory.go")
    func decodeAllMemoryActionReceipts() throws {
        let decoder = JSONDecoder()

        let pinJSON = """
        {
          "request_id": "r1",
          "accepted": true,
          "status": "applied",
          "resource": {
            "id": "mem-1",
            "revision": 1002,
            "text": "Prefer concise updates",
            "section": "Preferences",
            "pinned": true,
            "provenance": "user_explicit",
            "scope": "user",
            "origin": {},
            "created_at": "2026-11-24T10:00:00Z"
          }
        }
        """
        let pinReceipt = try decoder.decode(MutationReceipt<MemoryActionResource>.self, from: Data(pinJSON.utf8))
        #expect(pinReceipt.resource.fact?.id == "mem-1")
        #expect(pinReceipt.resource.fact?.pinned == true)

        let forgetCoreJSON = """
        {
          "request_id": "r2",
          "accepted": true,
          "status": "applied",
          "resource": {
            "note": "Nori will no longer use this.",
            "forgotten": {
              "id": "mem-1",
              "revision": 1002,
              "text": "Prefer concise updates",
              "scope": "user",
              "origin": {},
              "created_at": "2026-11-24T10:00:00Z"
            }
          }
        }
        """
        let forgetCoreReceipt = try decoder.decode(MutationReceipt<MemoryActionResource>.self, from: Data(forgetCoreJSON.utf8))
        #expect(forgetCoreReceipt.resource.forgetResult?.note == "Nori will no longer use this.")
        #expect(forgetCoreReceipt.resource.forgetResult?.forgotten?.id == "mem-1")

        let demoteJSON = """
        {
          "request_id": "r3",
          "accepted": true,
          "status": "applied",
          "resource": {
            "demoted": {
              "id": "mem-2",
              "revision": 1003,
              "text": "Meeting moved to Friday",
              "scope": "user",
              "origin": {},
              "created_at": "2026-11-24T10:00:00Z"
            },
            "log_entry": {
              "id": "demote_mem-2",
              "text": "Meeting moved to Friday",
              "provenance": "user_explicit",
              "created_at": "2026-11-24T11:00:00Z"
            },
            "note": "Moved to daily log."
          }
        }
        """
        let demoteReceipt = try decoder.decode(MutationReceipt<MemoryActionResource>.self, from: Data(demoteJSON.utf8))
        #expect(demoteReceipt.resource.demoteResult?.demoted?.id == "mem-2")
        #expect(demoteReceipt.resource.demoteResult?.logEntry?.id == "demote_mem-2")
        #expect(demoteReceipt.resource.demoteResult?.note == "Moved to daily log.")

        let promoteLogJSON = """
        {
          "request_id": "r4",
          "accepted": true,
          "status": "applied",
          "resource": {
            "id": "mem-promoted-1",
            "revision": 2001,
            "text": "Use UTC timestamps",
            "pinned": true,
            "provenance": "user_explicit",
            "scope": "user",
            "origin": {},
            "created_at": "2026-11-24T12:00:00Z"
          }
        }
        """
        let promoteLogReceipt = try decoder.decode(MutationReceipt<MemoryLogEntryActionResource>.self, from: Data(promoteLogJSON.utf8))
        #expect(promoteLogReceipt.resource.promotedFact?.id == "mem-promoted-1")
        #expect(promoteLogReceipt.resource.promotedFact?.pinned == true)

        let forgetLogJSON = """
        {
          "request_id": "r5",
          "accepted": true,
          "status": "applied",
          "resource": {
            "note": "Nori removed this daily log entry.",
            "forgotten": {
              "id": "entry-9",
              "text": "Temporary note",
              "provenance": "pre_compact_flush",
              "created_at": "2026-11-24T09:00:00Z"
            }
          }
        }
        """
        let forgetLogReceipt = try decoder.decode(MutationReceipt<MemoryLogEntryActionResource>.self, from: Data(forgetLogJSON.utf8))
        #expect(forgetLogReceipt.resource.forgetResult?.note == "Nori removed this daily log entry.")
        #expect(forgetLogReceipt.resource.forgetResult?.forgotten?.id == "entry-9")

        let promoteWorkingJSON = """
        {
          "request_id": "r6a",
          "accepted": true,
          "status": "applied",
          "resource": {
            "id": "mem-from-working",
            "revision": 3001,
            "text": "Keep 24 KiB core budget.",
            "scope": "user",
            "origin": {},
            "created_at": "2026-11-24T12:30:00Z"
          }
        }
        """
        let promoteWorkingReceipt = try decoder.decode(MutationReceipt<WorkingMemoryActionResource>.self, from: Data(promoteWorkingJSON.utf8))
        #expect(promoteWorkingReceipt.resource.promotedFact?.id == "mem-from-working")

        let dismissWorkingFullJSON = """
        {
          "request_id": "r6b",
          "accepted": true,
          "status": "applied",
          "resource": {
            "revision": 4002,
            "items": [],
            "gist": "Active thread",
            "turns": 5,
            "core_bytes": 120,
            "core_budget_bytes": 24576
          }
        }
        """
        let dismissFullReceipt = try decoder.decode(MutationReceipt<WorkingMemoryActionResource>.self, from: Data(dismissWorkingFullJSON.utf8))
        #expect(dismissFullReceipt.resource.workingMemory?.revision == 4002)
        #expect(dismissFullReceipt.resource.workingMemory?.items.isEmpty == true)

        let dismissWorkingIDJSON = """
        {
          "request_id": "r6c",
          "accepted": true,
          "status": "applied",
          "resource": {
            "dismissed_item_id": "wm-item-1"
          }
        }
        """
        let dismissIDReceipt = try decoder.decode(MutationReceipt<WorkingMemoryActionResource>.self, from: Data(dismissWorkingIDJSON.utf8))
        #expect(dismissIDReceipt.resource.dismissResult?.dismissedItemID == "wm-item-1")
    }

    @Test("NoriStore memory mutations update coreMemories, memoryLogs, and workingMemory cleanly")
    @MainActor
    func storeMemoryMutationsEndToEnd() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))

        let fact1 = MemoryFact(id: "m1", revision: 10, text: "Fact 1", pinned: false)
        let fact2 = MemoryFact(id: "m2", revision: 11, text: "Fact 2", pinned: false)
        let fact3 = MemoryFact(id: "m3", revision: 12, text: "Fact 3", pinned: false)
        store.coreMemories = [fact1, fact2, fact3]

        let logEntry1 = MemoryLogEntry(id: "le-1", text: "Promote me")
        let logEntry2 = MemoryLogEntry(id: "le-2", text: "Forget me")
        store.memoryLogs = [MemoryLog(date: "2026-11-24", revision: 20, entries: [logEntry1, logEntry2])]

        let wm1 = WorkingMemoryItem(id: "wm-1", kind: .decision, quote: "Decision 1")
        let wm2 = WorkingMemoryItem(id: "wm-2", kind: .fact, quote: "Dismiss me")
        store.workingMemory = WorkingMemory(revision: 30, items: [wm1, wm2])

        await store.togglePinMemory(memoryID: "m1")
        #expect(store.coreMemories.first(where: { $0.id == "m1" })?.pinned == true)

        await store.demoteMemory(memoryID: "m2")
        #expect(store.coreMemories.contains(where: { $0.id == "m2" }) == false)

        await store.forgetMemory(memoryID: "m3")
        #expect(store.coreMemories.contains(where: { $0.id == "m3" }) == false)

        await store.promoteLogEntry(date: "2026-11-24", entryID: "le-1", pinned: true)
        #expect(store.coreMemories.contains(where: { $0.id == "promoted-le-1" && $0.pinned == true }))

        await store.forgetLogEntry(date: "2026-11-24", entryID: "le-2")
        #expect(store.memoryLogs.flatMap(\.entries).contains(where: { $0.id == "le-2" }) == false)

        await store.promoteWorkingMemoryItem(itemID: "wm-1")
        #expect(store.coreMemories.contains(where: { $0.id == "promoted-wm-1" }))

        await store.dismissWorkingMemoryItem(itemID: "wm-2")
        #expect(store.workingMemory.items.contains(where: { $0.id == "wm-2" }) == false)

        store.workingMemory = WorkingMemory(revision: 31, items: [WorkingMemoryItem(id: "wm-3", kind: .fact, quote: "Dismiss fallback")])
        await mock.setDismissReturnsIDOnly(true)
        await store.dismissWorkingMemoryItem(itemID: "wm-3")
        #expect(store.workingMemory.items.contains(where: { $0.id == "wm-3" }) == false)
    }

    @Test("NoriStore sendMessage returns Bool and preserves/reverts optimistic state on failure")
    @MainActor
    func storeSendMessageBoolReturn() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))

        let emptyResult = await store.sendMessage("   \n  ")
        #expect(emptyResult == false)

        await mock.setShouldFailSendTurn(true)
        let failedResult = await store.sendMessage("Draft to keep")
        #expect(failedResult == false)
        #expect(store.messages.isEmpty)
        #expect(store.isThinking == false)

        await mock.setShouldFailSendTurn(false)
        let successResult = await store.sendMessage("Hello Nori")
        #expect(successResult == true)
        #expect(store.messages.contains(where: { $0.text == "Hello Nori" }))
    }

    @Test("NoriStore document hydration, file loading, and queued vs applied reviseDocument")
    @MainActor
    func storeDocumentLoadAndRevise() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))

        let loaded = await store.loadLibraryItem(id: "deep-doc-1")
        #expect(loaded?.id == "deep-doc-1")
        #expect(store.libraryItems.contains(where: { $0.id == "deep-doc-1" }))

        let file = try await store.loadLibraryItemFile(id: "deep-doc-1", format: "md")
        #expect(file.utf8Text == "# Full Doc\n\nBody text.")
        #expect(store.cachedBodyText(for: "deep-doc-1") == "# Full Doc\n\nBody text.")

        await mock.setReviseStatus(.queued)
        let queuedResult = await store.reviseDocument(itemID: "deep-doc-1", instruction: "Shorten")
        #expect(queuedResult?.status == .queued)
        #expect(queuedResult?.isQueued == true)
        #expect(queuedResult?.taskID == "rev-task-1")
        #expect(store.toastMessage == "Revision started")
        #expect(await mock.fetchWorkItemsCallCount >= 1)

        await mock.setReviseStatus(.applied)
        let appliedResult = await store.reviseDocument(itemID: "deep-doc-1", instruction: "Polish")
        #expect(appliedResult?.status == .applied)
        #expect(appliedResult?.item?.version == 2)
        #expect(store.toastMessage == "Revision saved (v2)")
    }

    @Test("NoriStore OAuth return_to, AuthAttempt return, and saveMCPCredential")
    @MainActor
    func storeOAuthAndMCPCredential() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))

        store.connections = [Connection(id: "conn-1", revision: 2, integrationID: "gmail", title: "Work Gmail", status: .notConnected)]
        store.mcpServers = [MCPServer(id: "mcp-1", revision: 3, name: "Linear", endpoint: "https://mcp.linear.app", observed: MCPObservedState(status: .needsSignIn), auth: .oauth)]

        let connAttempt = await store.connectConnection(connectionID: "conn-1")
        #expect(connAttempt?.id == "att-conn-1")
        #expect(await mock.lastConnectionReturnTo == "nori://oauth/callback")
        #expect(store.toastMessage == nil)

        let mcpAttempt = await store.startMCPOAuth(serverID: "mcp-1")
        #expect(mcpAttempt?.id == "att-mcp-1")
        #expect(await mock.lastMCPReturnTo == "nori://oauth/callback")

        let emptyCred = await store.saveMCPCredential(serverID: "mcp-1", secret: "   ")
        #expect(emptyCred == false)

        let savedCred = await store.saveMCPCredential(serverID: "mcp-1", secret: "sk-token-99")
        #expect(savedCred == true)
        #expect(await mock.lastMCPCredentialSecret == "sk-token-99")
        #expect(store.mcpServers.first?.observed.credential == "present")
        #expect(store.mcpTools["mcp-1"]?.count == 1)
    }

    @Test("NoriStore SSE stream updates connectionState, moves cursor backward on reset, signs out on 401, merges runs monotonically, and supports background lifecycle")
    @MainActor
    func storeStreamStatesResetAuthErrorAndLifecycle() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "alex@example.com")
        store.session = Session(position: 50)

        await mock.setStreamEvents([
            .connectionState(.connecting),
            .connectionState(.connected),
            .connectionState(.reconnecting),
            .connectionState(.offline)
        ], terminalError: nil)
        store.startEventStream()
        await store.waitForEventStreamCompletion()
        #expect(store.connectionState == .offline)

        await mock.setShouldFailSnapshot(true)
        await mock.setStreamEvents([.reset(position: 8)], terminalError: nil)
        store.startEventStream()
        await store.waitForEventStreamCompletion()
        #expect(store.session.position == 8)
        await mock.setShouldFailSnapshot(false)

        // Monotonic .task merge (terminal run not reverted by stale .running) + .result updates session.results
        let completedRun = Run(id: "run-1", status: .completed, progress: "Done", startedAt: "2026-11-26T10:00:00Z", finishedAt: "2026-11-26T10:01:00Z")
        let staleRunningRun = Run(id: "run-1", status: .running, progress: "Working", startedAt: "2026-11-26T10:00:00Z")
        let resultItem = SavedResult(id: "res-1", rootID: "res-1", runID: "run-1", kind: "document", title: "Brief", createdAt: "2026-11-26T10:01:00Z")
        await mock.setStreamEvents([
            .task(seq: 9, run: completedRun),
            .task(seq: 10, run: staleRunningRun),
            .result(seq: 11, result: resultItem)
        ], terminalError: nil)
        store.startEventStream()
        await store.waitForEventStreamCompletion()
        #expect(store.runs.first(where: { $0.id == "run-1" })?.status == .completed)
        #expect(store.session.results?.first(where: { $0.id == "res-1" })?.title == "Brief")

        store.pauseForBackground()
        #expect(store.authState.isSignedIn == true)

        let snapshotBeforeResume = await mock.fetchFullSnapshotCallCount
        await mock.setStreamEvents([.connectionState(.connected)], terminalError: nil)
        await store.resumeFromBackground()
        #expect(await mock.fetchFullSnapshotCallCount == snapshotBeforeResume)
        #expect(await mock.fetchSummaryCallCount >= 1)
        #expect(await mock.fetchSessionCallCount >= 1)
        #expect(await mock.fetchWorkItemsCallCount >= 1)

        await mock.setStreamEvents([], terminalError: NoriAPIError(message: "Unauthorized", statusCode: 401))
        store.startEventStream()
        await store.waitForEventStreamCompletion()
        #expect(store.authState == .signedOut)
        #expect(store.authError != nil)
    }

    @Test("NoriStore.resolveInteraction returns conflict outcome with current interaction for inline recovery")
    @MainActor
    func storeInteractionConflictRecovery() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        let stale = Interaction(
            id: "int-409",
            revision: 1,
            kind: .approval,
            status: .pending,
            prompt: "Send email?",
            payload: .approval(ApprovalPayload(
                summary: "Send email",
                effect: .sensitiveSend,
                account: ApprovalAccount(connectionID: "conn-1", label: "alex@example.com")
            )),
            actionDigest: "sha256:old"
        )
        let latest = Interaction(
            id: "int-409",
            revision: 2,
            kind: .approval,
            status: .resolved,
            prompt: "Send email?",
            payload: stale.payload,
            resolution: InteractionResolution(kind: .approve, at: "2026-11-26T10:00:00Z")
        )
        let currentJSON = try JSONDecoder().decode(AnyJSON.self, from: try JSONEncoder().encode(latest))
        await mock.setNextInteractionRespondError(
            NoriAPIError(message: "Revision mismatch", statusCode: 409, code: "revision_conflict", current: currentJSON)
        )
        store.interactions = [stale]

        let outcome = await store.approveInteraction(stale)
        guard case .conflict(let current, let message) = outcome else {
            Issue.record("Expected conflict outcome")
            return
        }
        #expect(current == latest)
        #expect(!message.isEmpty)

        if let current {
            store.applyCurrentInteraction(current)
        }
        #expect(store.interactions.first(where: { $0.id == "int-409" }) == latest)
    }

    @Test("NoriStore AssistantSummary drives badges and oauthCallback uses targeted refresh instead of fetchFullSnapshot")
    @MainActor
    func storeSummaryAndTargetedRefresh() async throws {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))

        await store.refreshSummary()
        #expect(store.assistantSummary?.needsYou == 4)
        #expect(store.needsYouCount == 4)
        #expect(store.activeTasksCount == 2)
        #expect(store.appsAttentionCount == 1)

        store.handleIncomingURL(URL(string: "nori://oauth/callback?result=connected")!)
        await store.waitForOAuthCallbackCompletion()
        #expect(await mock.fetchFullSnapshotCallCount == 0)
        #expect(await mock.fetchMCPServersCallCount >= 1)
        #expect(await mock.fetchConnectionsCallCount >= 1)
        #expect(await mock.fetchWorkItemsCallCount >= 1)
        #expect(await mock.fetchSummaryCallCount >= 2)
    }

    @Test("NoriStore retryMessage, updateReminder, and cancelAutomation update state cleanly")
    @MainActor
    func storeRetryAndReminderActions() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))

        let failedMsg = Message(
            id: "turn-9-assistant",
            turnID: "turn-9",
            role: .assistant,
            status: .failed,
            text: "Model busy",
            error: ErrorDetail(code: "provider_overloaded", message: "Model busy", retryable: true),
            createdAt: "2026-11-25T07:01:18Z"
        )
        store.session = Session(messages: [failedMsg])

        let retried = await store.retryMessage(failedMsg)
        #expect(retried == true)
        #expect(store.isThinking == true)
        #expect(store.messages.first?.status == .superseded)

        let targetDate = Date(timeIntervalSince1970: 1_795_640_400)
        let updated = await store.updateReminder(
            automationID: "auto_1234",
            title: "Check supernino updates at 2 PM",
            scheduledAt: targetDate
        )
        #expect(updated?.id == "auto_1234")
        #expect(updated?.title == "Check supernino updates at 2 PM")
        #expect(store.automations.first(where: { $0.id == "auto_1234" })?.title == "Check supernino updates at 2 PM")

        let cancelled = await store.cancelAutomation(automationID: "auto_1234")
        #expect(cancelled == true)
        #expect(store.automations.first(where: { $0.id == "auto_1234" })?.status == .cancelled)
    }

    @Test("IOS-1: Streaming assistant deltas publish first token immediately, coalesce rapid updates, and flush on completion or message")
    @MainActor
    func streamingAssistantDeltaCoalescing() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "stream@example.com")
        store.streamingFlushInterval = .milliseconds(40)

        await store.handleStreamEvent(.assistantDelta(turnID: "turn-1", text: "Hello"))
        #expect(store.streamingAssistantText == "Hello")
        #expect(store.isStreamingAssistant == true)

        await store.handleStreamEvent(.assistantDelta(turnID: "turn-1", text: "Hello world"))
        await store.handleStreamEvent(.assistantDelta(turnID: "turn-1", text: "Hello world!"))
        // Coalesced inside the 40ms window: still shows first token until flush fires.
        #expect(store.streamingAssistantText == "Hello")

        let deadline = ContinuousClock.now.advanced(by: .seconds(1))
        while store.streamingAssistantText != "Hello world!", ContinuousClock.now < deadline {
            try? await Task.sleep(for: .milliseconds(15))
        }
        #expect(store.streamingAssistantText == "Hello world!")

        // Final assistant message clears streaming immediately.
        await store.handleStreamEvent(.message(seq: 1, message: Message(
            id: "msg-1",
            turnID: "turn-1",
            role: .assistant,
            text: "Hello world!",
            createdAt: "2026-11-27T10:00:00Z"
        )))
        #expect(store.streamingAssistantText == nil)
        #expect(store.isStreamingAssistant == false)
    }

    @Test("IOS-3: SSE .task event dispatches background refresh without blocking subsequent stream events")
    @MainActor
    func sseTaskEventDoesNotBlockConsumerLoop() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "sse@example.com")

        let run = Run(id: "run-async", status: .running, progress: "Working", startedAt: "2026-11-27T10:00:00Z")
        let reply = Message(id: "msg-after-task", turnID: "t-1", role: .assistant, text: "Done", createdAt: "2026-11-27T10:00:01Z")
        await mock.setStreamEvents([
            .task(seq: 1, run: run),
            .task(seq: 2, run: run),
            .message(seq: 3, message: reply)
        ], terminalError: nil)

        store.startEventStream()
        await store.waitForEventStreamCompletion()
        #expect(store.messages.contains(where: { $0.id == "msg-after-task" }))
        #expect(store.runs.contains(where: { $0.id == "run-async" }))

        await store.waitForBackgroundRefreshes()
        #expect(await mock.fetchWorkItemsCallCount >= 1)
    }

    @Test("IOS-9: refreshForTab(.more) scopes to hub-visible data and respects TTL unless forced")
    @MainActor
    func moreTabRefreshScopeAndTTL() async {
        let mock = MockNoriTransport()
        let store = NoriStore(transport: mock, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "tabs@example.com")

        await store.refreshForTab(.more, force: false)
        #expect(await mock.fetchSummaryCallCount == 1)
        #expect(await mock.fetchMCPServersCallCount == 0)
        #expect(await mock.fetchConnectionsCallCount == 0)

        // Second immediate switch within 20s TTL makes zero additional requests.
        await store.refreshForTab(.more, force: false)
        #expect(await mock.fetchSummaryCallCount == 1)

        // Forced pull-to-refresh bypasses TTL.
        await store.refreshForTab(.more, force: true)
        #expect(await mock.fetchSummaryCallCount == 2)
    }
}


private final class LockedCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0
    var value: Int {
        lock.lock(); defer { lock.unlock() }
        return count
    }
    func increment() {
        lock.lock(); count += 1; lock.unlock()
    }
    func incrementAndGet() -> Int {
        lock.lock(); defer { lock.unlock() }
        count += 1
        return count
    }
}

private final class LockedArray<Element: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [Element] = []
    var values: [Element] {
        lock.lock(); defer { lock.unlock() }
        return items
    }
    func append(_ element: Element) {
        lock.lock(); items.append(element); lock.unlock()
    }
}

private final class LockedDict<Key: Hashable & Sendable, Value: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var storage: [Key: Value] = [:]
    func set(_ key: Key, value: Value) {
        lock.lock(); storage[key] = value; lock.unlock()
    }
    func get(_ key: Key) -> Value? {
        lock.lock(); defer { lock.unlock() }
        return storage[key]
    }
}

