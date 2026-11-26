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
            keychain: KeychainStore(service: "com.nori.ios.tests.\(UUID().uuidString)"),
            urlSession: session,
            clientID: "test-client-id.apps.googleusercontent.com"
        )
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
}


@Suite("Memory Receipts & NoriStore Domain Logic")
struct MemoryReceiptsAndStoreTests {

    @Test("Sign out clears the saved Google session and current user state")
    @MainActor
    func signOutClearsPersistentSession() async {
        let service = "com.nori.ios.tests.\(UUID().uuidString)"
        let provider = AuthTokenProvider(keychain: KeychainStore(service: service))
        let session = AuthSession(idToken: "test-token", refreshToken: "refresh", expiresAt: Date().addingTimeInterval(3600), email: "alex@example.com")
        await provider.saveSession(session)
        let initiallyReloadedProvider = AuthTokenProvider(keychain: KeychainStore(service: service))
        #expect(await initiallyReloadedProvider.loadSession() == session)
        let store = NoriStore(transport: FullMockTransport(), authProvider: provider)
        store.authState = .signedIn(email: session.email)
        store.session = Session(messages: [Message(id: "private", role: .user, text: "Private", createdAt: "2026-11-24T10:00:00Z")])

        await store.signOut()

        let reloadedProvider = AuthTokenProvider(keychain: KeychainStore(service: service))
        #expect(await reloadedProvider.loadSession() == nil)
        #expect(store.authState == .signedOut)
        #expect(store.messages.isEmpty)
    }

    @Test("Reset preserves sign-in, clears cached data, and ignores stale local onboarding")
    @MainActor
    func resetClearsStateAndStartsOnboarding() async throws {
        let mock = FullMockTransport()
        let email = "reset-\(UUID().uuidString)@example.com"
        let localKey = "nori.onboarding.done." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: localKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let provider = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.ios.tests.\(UUID().uuidString)"))
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
        let mock = FullMockTransport()
        let email = "reset-race-\(UUID().uuidString)@example.com"
        let localKey = "nori.onboarding.done." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: localKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let store = NoriStore(transport: mock, resetMinimumDuration: .zero)
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

    @Test("A reset event clears a second device's stale state and shows onboarding")
    @MainActor
    func remoteResetEvent() async {
        let mock = FullMockTransport()
        let email = "reset-remote-\(UUID().uuidString)@example.com"
        let localKey = "nori.onboarding.done." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: localKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let store = NoriStore(transport: mock)
        store.authState = .signedIn(email: email)
        store.session = Session(messages: [Message(id: "old", role: .user, text: "Private", createdAt: "2026-11-24T10:00:00Z")])
        store.hasLoadedSnapshot = true
        UserDefaults.standard.set(true, forKey: localKey)
        await mock.setPreferences(Preferences(dataResetAt: "2026-11-25T20:00:00Z"))
        await mock.setStreamEvents([.reset(position: 42)], terminalError: nil)
        store.startEventStream()

        for _ in 0..<1_000 {
            if store.shouldShowOnboarding { break }
            await Task.yield()
        }
        #expect(store.shouldShowOnboarding)
        #expect(store.messages.isEmpty)
        #expect(store.authState == .signedIn(email: email))
        await store.signOut()
    }

    @Test("A fresh launch reads onboarding state after a reset on another device")
    @MainActor
    func bootstrapDetectsRemoteReset() async {
        let mock = FullMockTransport()
        let email = "reset-launch-\(UUID().uuidString)@example.com"
        let localKey = "nori.onboarding.done." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: localKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        UserDefaults.standard.set(true, forKey: localKey)
        await mock.setPreferences(Preferences(dataResetAt: "2026-11-25T20:00:00Z"))
        let provider = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.ios.tests.\(UUID().uuidString)"))
        await provider.saveSession(AuthSession(idToken: "test-token", refreshToken: nil, expiresAt: Date().addingTimeInterval(3600), email: email))
        let store = NoriStore(transport: mock, authProvider: provider)
        await store.bootstrapIfNeeded()

        for _ in 0..<1_000 {
            if store.shouldShowOnboarding { break }
            await Task.yield()
        }
        #expect(store.shouldShowOnboarding)
        #expect(store.authState == .signedIn(email: email))
        await store.signOut()
    }

    @Test("An interrupted reset offers recovery and confirms successful completion")
    @MainActor
    func resetRecoveryAndCompletion() async throws {
        let mock = FullMockTransport()
        let email = "reset-recovery-\(UUID().uuidString)@example.com"
        let completionKey = "nori.data-reset.completion." + email.lowercased()
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: completionKey)
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let provider = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.ios.tests.\(UUID().uuidString)"))
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
        let mock = FullMockTransport()
        let email = "reset-stale-\(UUID().uuidString)@example.com"
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer { UserDefaults.standard.removeObject(forKey: markerKey) }
        let store = NoriStore(transport: mock, resetMinimumDuration: .zero)
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
        let mock = FullMockTransport()
        let email = "reset-spinner-\(UUID().uuidString)@example.com"
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let store = NoriStore(transport: mock, resetMinimumDuration: .milliseconds(80))
        store.authState = .signedIn(email: email)
        await mock.suspendNextReset()
        let started = ContinuousClock.now
        let resetting = Task { try await store.resetUserData(confirmation: "RESET") }
        await mock.waitForSuspendedReset()
        #expect(store.isResettingData)
        try await Task.sleep(for: .milliseconds(100))
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
        let mock = FullMockTransport()
        let email = "reset-fast-\(UUID().uuidString)@example.com"
        let markerKey = "nori.data-reset.observed." + email.lowercased()
        defer {
            UserDefaults.standard.removeObject(forKey: markerKey)
        }
        let store = NoriStore(transport: mock, resetMinimumDuration: .milliseconds(120))
        store.authState = .signedIn(email: email)
        let resetting = Task { try await store.resetUserData(confirmation: "RESET") }
        for _ in 0..<1_000 {
            if store.authState == .signedOut { break }
            await Task.yield()
        }
        #expect(store.authState == .signedOut)
        #expect(store.isResettingData)
        try await Task.sleep(for: .milliseconds(25))
        #expect(store.isResettingData)
        try await resetting.value
        #expect(!store.isResettingData)
        #expect(store.authState == .signedOut)
    }

    @Test("Chat restores its snapshot and opens the stream without waiting for other screens", .timeLimit(.minutes(1)))
    @MainActor
    func bootstrapUsesChatSnapshotAndCursor() async {
        let mock = FullMockTransport()
        let provider = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.ios.tests.\(UUID().uuidString)"))
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
        #expect(await mock.fetchSummaryCallCount == 0)
        await mock.finishSuspendedSession(Session(messages: [
            Message(id: "welcome", role: .assistant, text: "Welcome back", createdAt: "2026-11-24T10:00:00Z"),
        ], position: 42))
        await bootstrap.value

        var cursors = mock.streamOpens.makeAsyncIterator()
        #expect(await cursors.next() == 42)
        #expect(store.messages.map(\.id) == ["welcome"])
        #expect(!store.isLoading)
        #expect(await mock.fetchSessionCallCount == 1)
        #expect(await mock.fetchFullSnapshotCallCount == 0)
        #expect(await mock.fetchWorkItemsCallCount == 0)
        #expect(await mock.fetchMCPServersCallCount == 0)
        #expect(await mock.fetchConnectionsCallCount == 0)
        await store.signOut()
    }

    @Test("Cancelling a newer Work refresh preserves pending approvals from the earlier read", .timeLimit(.minutes(1)))
    @MainActor
    func cancelledWorkRefreshPreservesPendingApprovals() async {
        let mock = FullMockTransport()
        let store = NoriStore(transport: mock)
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
        let transport = FullMockTransport()
        let provider = AuthTokenProvider(keychain: KeychainStore(service: "com.nori.ios.tests.\(UUID().uuidString)"))
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
        let transport = FullMockTransport()
        let store = NoriStore(transport: transport)
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
        let mock = FullMockTransport()
        let store = NoriStore(transport: mock)

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
        let mock = FullMockTransport()
        let store = NoriStore(transport: mock)

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
        let mock = FullMockTransport()
        let store = NoriStore(transport: mock)

        let loaded = await store.loadLibraryItem(id: "deep-doc-1")
        #expect(loaded?.id == "deep-doc-1")
        #expect(store.libraryItems.contains(where: { $0.id == "deep-doc-1" }))

        let file = try await store.loadLibraryItemFile(id: "deep-doc-1", format: "md")
        #expect(file.utf8Text == "# Full Doc\n\nBody text.")
        #expect(store.libraryItems.first(where: { $0.id == "deep-doc-1" })?.bodyText == "# Full Doc\n\nBody text.")

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
        let mock = FullMockTransport()
        let store = NoriStore(transport: mock)

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

    @Test("NoriStore SSE stream updates connectionState, moves cursor backward on reset, signs out on 401, and supports background lifecycle")
    @MainActor
    func storeStreamStatesResetAuthErrorAndLifecycle() async throws {
        let mock = FullMockTransport()
        let store = NoriStore(transport: mock)
        store.authState = .signedIn(email: "alex@example.com")
        store.session = Session(position: 50)

        await mock.setStreamEvents([
            .connectionState(.connecting),
            .connectionState(.connected),
            .connectionState(.reconnecting),
            .connectionState(.offline)
        ], terminalError: nil)
        store.startEventStream()
        try await Task.sleep(for: .milliseconds(50))
        #expect(store.connectionState == .offline)

        await mock.setShouldFailSnapshot(true)
        await mock.setStreamEvents([.reset(position: 8)], terminalError: nil)
        store.startEventStream()
        try await Task.sleep(for: .milliseconds(50))
        #expect(store.session.position == 8)
        await mock.setShouldFailSnapshot(false)

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
        try await Task.sleep(for: .milliseconds(50))
        #expect(store.authState == .signedOut)
        #expect(store.authError != nil)
    }

    @Test("NoriStore AssistantSummary drives badges and oauthCallback uses targeted refresh instead of fetchFullSnapshot")
    @MainActor
    func storeSummaryAndTargetedRefresh() async throws {
        let mock = FullMockTransport()
        let store = NoriStore(transport: mock)

        await store.refreshSummary()
        #expect(store.assistantSummary?.needsYou == 4)
        #expect(store.needsYouCount == 4)
        #expect(store.activeTasksCount == 2)
        #expect(store.appsAttentionCount == 1)

        store.handleIncomingURL(URL(string: "nori://oauth/callback?result=connected")!)
        try await Task.sleep(for: .milliseconds(60))
        #expect(await mock.fetchFullSnapshotCallCount == 0)
        #expect(await mock.fetchMCPServersCallCount >= 1)
        #expect(await mock.fetchConnectionsCallCount >= 1)
        #expect(await mock.fetchWorkItemsCallCount >= 1)
        #expect(await mock.fetchSummaryCallCount >= 2)
    }

    @Test("NoriStore retryMessage, updateReminder, and cancelAutomation update state cleanly")
    @MainActor
    func storeRetryAndReminderActions() async {
        let mock = FullMockTransport()
        let store = NoriStore(transport: mock)

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


actor FullMockTransport: NoriTransport {
    var fetchFullSnapshotCallCount = 0
    var fetchSummaryCallCount = 0
    var fetchSessionCallCount = 0
    private var delaySession = false
    private var suspendedSession: CheckedContinuation<Session, Never>?
    private var sessionStarted: CheckedContinuation<Void, Never>?
    private var resetFails = false
    private var failAfterStartingReset = false
    private var failResume = false
    private(set) var ordinaryResetCount = 0
    private(set) var resumeResetCount = 0
    private var delayReset = false
    private var suspendedReset: CheckedContinuation<Void, Never>?
    private var resetStarted: CheckedContinuation<Void, Never>?
    private var mockPreferences = Preferences()

    func suspendNextSession() { delaySession = true }

    func waitForSuspendedSession() async {
        if suspendedSession != nil { return }
        await withCheckedContinuation { sessionStarted = $0 }
    }

    func finishSuspendedSession(_ session: Session) {
        suspendedSession?.resume(returning: session)
        suspendedSession = nil
    }

    func setResetFails(_ fail: Bool) { resetFails = fail }
    func setFailAfterStartingReset(_ fail: Bool) { failAfterStartingReset = fail }
    func setFailResume(_ fail: Bool) { failResume = fail }
    func setPreferences(_ preferences: Preferences) { mockPreferences = preferences }
    func suspendNextReset() { delayReset = true }

    func waitForSuspendedReset() async {
        if suspendedReset != nil { return }
        await withCheckedContinuation { resetStarted = $0 }
    }

    func finishSuspendedReset() {
        suspendedReset?.resume()
        suspendedReset = nil
    }
    var fetchWorkItemsCallCount = 0
    var fetchMCPServersCallCount = 0
    var fetchConnectionsCallCount = 0

    var shouldFailSendTurn = false
    var shouldFailSnapshot = false
    var dismissReturnsIDOnly = false
    var reviseStatus: MutationReceipt<LibraryItem>.Status = .queued
    var lastConnectionReturnTo: String?
    var lastMCPReturnTo: String?
    var lastMCPCredentialSecret: String?

    private nonisolated(unsafe) var streamEvents: [NoriStreamEvent] = []
    private nonisolated(unsafe) var streamTerminalError: Error?
    private let streamLock = NSLock()
    nonisolated let streamOpens: AsyncStream<Int>
    private nonisolated let streamOpenContinuation: AsyncStream<Int>.Continuation
    nonisolated let interactionReads: AsyncStream<Int>
    private let interactionReadContinuation: AsyncStream<Int>.Continuation
    private var delayInteractionReads = false
    private var shouldFailInteractionReads = false
    private var nextInteractionRead = 0
    private var suspendedInteractionReads: [Int: CheckedContinuation<[Interaction], Never>] = [:]

    init() {
        (streamOpens, streamOpenContinuation) = AsyncStream.makeStream(bufferingPolicy: .bufferingNewest(1))
        (interactionReads, interactionReadContinuation) = AsyncStream.makeStream()
    }

    func setShouldFailSendTurn(_ fail: Bool) { shouldFailSendTurn = fail }
    func setShouldFailSnapshot(_ fail: Bool) { shouldFailSnapshot = fail }
    func setDismissReturnsIDOnly(_ flag: Bool) { dismissReturnsIDOnly = flag }
    func setReviseStatus(_ status: MutationReceipt<LibraryItem>.Status) { reviseStatus = status }
    func setStreamEvents(_ events: [NoriStreamEvent], terminalError: Error?) {
        streamLock.lock()
        streamEvents = events
        streamTerminalError = terminalError
        streamLock.unlock()
    }

    func ping() async -> HealthCheckResult { HealthCheckResult(ok: true, status: "ok", latencyMs: 1) }
    func fetchCapabilities() async throws -> Capabilities { Capabilities() }
    func fetchAssistantSummary() async throws -> AssistantSummary {
        fetchSummaryCallCount += 1
        return AssistantSummary(revision: 5, needsYou: 4, working: 2, appsAttention: 1, inboxUnread: 0)
    }
    func fetchSession(before cursor: String?) async throws -> Session {
        fetchSessionCallCount += 1
        if delaySession {
            delaySession = false
            return await withCheckedContinuation {
                suspendedSession = $0
                sessionStarted?.resume()
                sessionStarted = nil
            }
        }
        return Session()
    }
    func fetchFullSnapshot() async throws -> NoriFullSnapshot {
        fetchFullSnapshotCallCount += 1
        if shouldFailSnapshot {
            throw NoriAPIError(message: "Temporary snapshot failure", statusCode: 503, retryable: true)
        }
        return NoriFullSnapshot(preferences: mockPreferences, didLoadInteractions: !shouldFailInteractionReads)
    }
    nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
        streamOpenContinuation.yield(position)
        streamLock.lock()
        let events = streamEvents
        let terminalErr = streamTerminalError
        streamLock.unlock()

        return AsyncThrowingStream { continuation in
            for event in events {
                continuation.yield(event)
            }
            if let terminalErr {
                continuation.finish(throwing: terminalErr)
            } else {
                continuation.finish()
            }
        }
    }

    func sendTurn(id: String, message: String, attachmentIDs: [String], modelSelection: ModelSelection?) async throws -> Session {
        if shouldFailSendTurn {
            throw NoriAPIError(message: "Network dropped", statusCode: 503, retryable: true)
        }
        return Session(messages: [Message(id: "\(id)-user", turnID: id, role: .user, text: message, createdAt: "2026-11-24T10:00:00Z")])
    }
    func retryTurn(turnID: String?, messageID: String?) async throws -> Session {
        if shouldFailSendTurn {
            throw NoriAPIError(message: "Network dropped", statusCode: 503, retryable: true)
        }
        return Session(
            messages: [
                Message(
                    id: messageID ?? "m-err",
                    turnID: turnID,
                    role: .assistant,
                    status: .superseded,
                    text: "Retrying",
                    createdAt: "2026-11-24T10:00:00Z"
                )
            ],
            conversation: Conversation(pending: true, processing: true)
        )
    }
    func sendTaskCommand(_ command: TaskCommand) async throws -> TaskCommandResponse { TaskCommandResponse(accepted: true) }
    func uploadFile(id: String, filename: String, mediaType: String, data: Data) async throws -> NoriCore.Attachment {
        NoriCore.Attachment(id: id, name: filename, mediaType: mediaType, size: data.count)
    }
    func fetchWorkItems(bucket: WorkBucket?) async throws -> [WorkItem] {
        fetchWorkItemsCallCount += 1
        return []
    }
    func fetchWorkDetail(taskID: String) async throws -> WorkDetail { throw NoriAPIError(message: "unimplemented") }
    func stopWork(taskID: String, requestID: String) async throws -> MutationReceipt<WorkItem> { throw NoriAPIError(message: "unimplemented") }
    func continueWork(taskID: String, budgetUSD: Double, requestID: String) async throws -> MutationReceipt<WorkItem> { throw NoriAPIError(message: "unimplemented") }
    func suspendInteractionReads() { delayInteractionReads = true }

    func failInteractionReads() { shouldFailInteractionReads = true }

    func finishInteractionRead(_ index: Int, with interactions: [Interaction]) {
        suspendedInteractionReads.removeValue(forKey: index)?.resume(returning: interactions)
    }

    func fetchInteractions() async throws -> [Interaction] {
        if shouldFailInteractionReads {
            throw NoriAPIError(message: "Interactions unavailable", statusCode: 503)
        }
        guard delayInteractionReads else { return [] }
        let index = nextInteractionRead
        nextInteractionRead += 1
        return await withCheckedContinuation {
            suspendedInteractionReads[index] = $0
            interactionReadContinuation.yield(index)
        }
    }
    func respondToInteraction(id: String, expectedRevision: Int, answer: InteractionAnswer, requestID: String) async throws -> MutationReceipt<Interaction> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchMCPServers() async throws -> [MCPServer] {
        fetchMCPServersCallCount += 1
        return []
    }
    func fetchMCPTools(serverID: String) async throws -> [MCPServerTool] {
        [MCPServerTool(name: "issues_list", title: "List Issues")]
    }
    func createMCPServer(input: MCPServerInput, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "unimplemented")
    }
    func performMCPAction(serverID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<MCPServer> {
        lastMCPReturnTo = returnTo
        let server = MCPServer(id: serverID, revision: expectedRevision + 1, name: "Linear", observed: MCPObservedState(status: .needsSignIn), auth: .oauth)
        let auth = action == "sign_in" ? AuthAttempt(id: "att-mcp-1", authorizeURL: "https://linear.app/oauth", expiresAt: "2026-11-25T00:00:00Z") : nil
        return MutationReceipt(requestID: requestID, status: auth != nil ? .queued : .applied, resource: server, auth: auth)
    }
    func setMCPCredential(serverID: String, expectedRevision: Int, secret: String, requestID: String) async throws -> MutationReceipt<MCPServer> {
        lastMCPCredentialSecret = secret
        let server = MCPServer(id: serverID, revision: expectedRevision + 1, name: "Linear", observed: MCPObservedState(status: .connected, credential: "present"), auth: .configured)
        return MutationReceipt(requestID: requestID, status: .applied, resource: server)
    }
    func setMCPToolExposure(serverID: String, expectedRevision: Int, toolName: String, exposure: MCPToolExposure, requestID: String) async throws -> MutationReceipt<MCPServer> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchConnections() async throws -> [Connection] {
        fetchConnectionsCallCount += 1
        return []
    }
    func performConnectionAction(connectionID: String, expectedRevision: Int, action: String, returnTo: String?, requestID: String) async throws -> MutationReceipt<Connection> {
        lastConnectionReturnTo = returnTo
        let conn = Connection(id: connectionID, revision: expectedRevision + 1, integrationID: "gmail", title: "Work Gmail", status: .connecting)
        let auth = AuthAttempt(id: "att-conn-1", authorizeURL: "https://accounts.google.com/o/oauth2/auth", expiresAt: "2026-11-25T00:00:00Z")
        return MutationReceipt(requestID: requestID, status: .queued, resource: conn, auth: auth)
    }
    func fetchLibrary() async throws -> [LibraryItem] { [] }
    func fetchLibraryItem(id: String) async throws -> LibraryItem {
        LibraryItem(id: id, version: 1, kind: .document, title: "Deep Document", revision: 1)
    }
    func fetchLibraryItemFile(id: String, format: String?) async throws -> LibraryFilePayload {
        LibraryFilePayload(data: Data("# Full Doc\n\nBody text.".utf8), mediaType: "text/markdown; charset=utf-8", filename: "Full Doc.md")
    }
    func search(query: String) async throws -> SearchResponse { SearchResponse(items: []) }
    func reviseLibraryItem(id: String, expectedRevision: Int, instruction: String, requestID: String) async throws -> MutationReceipt<LibraryItem> {
        if reviseStatus == .queued {
            let item = LibraryItem(id: id, version: 1, kind: .document, title: "Deep Document", revision: expectedRevision)
            return MutationReceipt(requestID: requestID, status: .queued, resource: item, work: WorkRef(taskID: "rev-task-1"))
        } else {
            let item = LibraryItem(id: id, version: 2, kind: .document, title: "Deep Document", revision: expectedRevision + 1)
            return MutationReceipt(requestID: requestID, status: .applied, resource: item)
        }
    }
    func fetchCoreMemories() async throws -> [MemoryFact] { [] }
    func fetchMemoryLogs() async throws -> [MemoryLog] {
        [MemoryLog(date: "2026-11-24", revision: 21, entries: [MemoryLogEntry(id: "le-1", text: "Promote me", promoted: true)])]
    }
    private var workingMemoryFailure = false
    func setWorkingMemoryFailure(_ value: Bool) { workingMemoryFailure = value }
    func fetchWorkingMemory() async throws -> WorkingMemory {
        if workingMemoryFailure {
            throw NoriAPIError(message: "Working memory unavailable", statusCode: 503)
        }
        return WorkingMemory(revision: 35, items: [WorkingMemoryItem(id: "wm-2", kind: .fact, quote: "Dismiss me")], coreBytes: 512)
    }
    func createMemory(text: String, section: String?, about: String?, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryFact> {
        MutationReceipt(requestID: requestID, resource: MemoryFact(id: "new-mem", text: text, pinned: pinned))
    }
    func performMemoryAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<MemoryActionResource> {
        switch action {
        case "pin":
            return MutationReceipt(requestID: requestID, resource: .fact(MemoryFact(id: id, revision: expectedRevision + 1, text: "Fact", pinned: true)))
        case "unpin":
            return MutationReceipt(requestID: requestID, resource: .fact(MemoryFact(id: id, revision: expectedRevision + 1, text: "Fact", pinned: false)))
        case "demote":
            return MutationReceipt(requestID: requestID, resource: .demoted(MemoryDemoteResult(
                demoted: MemoryFact(id: id, text: "Fact"),
                logEntry: MemoryLogEntry(id: "demote_\(id)", text: "Fact"),
                note: "Moved"
            )))
        default:
            return MutationReceipt(requestID: requestID, resource: .forgotten(MemoryForgetResult(
                note: "Forgotten",
                forgotten: MemoryFact(id: id, text: "Fact")
            )))
        }
    }
    func performMemoryLogEntryAction(date: String, entryID: String, expectedRevision: Int, action: String, pinned: Bool, requestID: String) async throws -> MutationReceipt<MemoryLogEntryActionResource> {
        if action == "promote" {
            return MutationReceipt(requestID: requestID, resource: .promoted(MemoryFact(id: "promoted-\(entryID)", text: "Promoted", pinned: pinned)))
        }
        return MutationReceipt(requestID: requestID, resource: .forgotten(MemoryLogEntryForgetResult(
            note: "Removed",
            forgotten: MemoryLogEntry(id: entryID, text: "Removed")
        )))
    }
    func performWorkingMemoryAction(itemID: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<WorkingMemoryActionResource> {
        if action == "promote" {
            return MutationReceipt(requestID: requestID, resource: .promoted(MemoryFact(id: "promoted-\(itemID)", text: "Promoted working")))
        }
        if dismissReturnsIDOnly {
            return MutationReceipt(requestID: requestID, resource: .dismissed(WorkingMemoryDismissResult(dismissedItemID: itemID)))
        }
        return MutationReceipt(requestID: requestID, resource: .workingMemory(WorkingMemory(revision: expectedRevision + 1, items: [])))
    }
    func fetchAutomations() async throws -> [Automation] { [] }
    func fetchAutomation(id: String) async throws -> Automation {
        Automation(
            id: id,
            revision: 1,
            title: "Check supernino updates",
            instruction: "Check supernino updates",
            status: .enabled,
            trigger: AutomationTrigger(kind: "once", at: "2026-11-25T20:00:00Z", timezone: nil),
            missedRun: .latest
        )
    }
    func updateAutomation(id: String, expectedRevision: Int, value: AutomationEditable, requestID: String) async throws -> MutationReceipt<Automation> {
        let updated = Automation(
            id: id,
            revision: expectedRevision + 1,
            title: value.title,
            instruction: value.instruction,
            status: .enabled,
            trigger: value.trigger,
            connectionIDs: value.connectionIDs,
            budgetUSD: value.budgetUSD,
            maxActiveRuns: value.maxActiveRuns,
            missedRun: value.missedRun ?? .latest,
            executionWindowSeconds: value.executionWindowSeconds,
            destinationIDs: value.destinationIDs,
            nextAt: value.trigger.at
        )
        return MutationReceipt(requestID: requestID, resource: updated)
    }
    func performAutomationAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<Automation> {
        let status: AutomationStatus = action == "cancel" ? .cancelled : (action == "pause" ? .paused : .enabled)
        let updated = Automation(
            id: id,
            revision: expectedRevision + 1,
            title: "Check supernino updates",
            instruction: "Check supernino updates",
            status: status,
            trigger: AutomationTrigger(kind: "once", at: "2026-11-25T20:00:00Z", timezone: nil),
            missedRun: .latest
        )
        return MutationReceipt(requestID: requestID, resource: updated)
    }
    func fetchGoals() async throws -> [Goal] { [] }
    func fetchPermissions() async throws -> [PermissionRule] { [] }
    func createPermissionRule(_ rule: PermissionRuleEditable, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        throw NoriAPIError(message: "unimplemented")
    }
    func revokePermissionRule(id: String, expectedRevision: Int, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchPreferences() async throws -> Preferences { mockPreferences }
    func resetUserData(confirmation: String) async throws {
        ordinaryResetCount += 1
        guard !resetFails, confirmation == "RESET" else {
            throw NoriAPIError(message: "Reset could not be completed.", statusCode: 409)
        }
        if failAfterStartingReset {
            mockPreferences.dataResetPending = true
            throw NoriAPIError(message: "Finish your reset.", statusCode: 502, code: "data_reset_pending")
        }
        if delayReset {
            delayReset = false
            await withCheckedContinuation {
                suspendedReset = $0
                resetStarted?.resume()
                resetStarted = nil
            }
        } else {
            mockPreferences = Preferences(dataResetAt: "2026-11-25T20:00:00Z")
        }
    }
    func resumePendingDataReset() async throws {
        resumeResetCount += 1
        guard mockPreferences.dataResetPending == true else {
            throw NoriAPIError(message: "Reset is no longer pending.", statusCode: 409, code: "data_reset_not_pending")
        }
        if failResume {
            throw NoriAPIError(message: "Try again.", statusCode: 502, code: "data_reset_pending")
        }
        mockPreferences = Preferences(dataResetAt: "2026-11-25T20:00:00Z")
    }
    func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        MutationReceipt(requestID: requestID, resource: preferences)
    }
}
