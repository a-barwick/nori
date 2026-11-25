import Foundation
import Testing
@testable import NoriCore

// MARK: - Thread-Safe Stub URLProtocol

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

// MARK: - Auth & LiveNoriTransport HTTP/SSE Tests (Serialized for URLProtocol isolation)

@Suite("Auth Refresh & LiveNoriTransport Resilience", .serialized)
struct AuthAndLiveTransportTests {

    private func makeProvider(session: URLSession) -> AuthTokenProvider {
        AuthTokenProvider(
            keychain: KeychainStore(service: "com.nori.ios.tests.\(UUID().uuidString)"),
            urlSession: session,
            clientID: "test-client-id.apps.googleusercontent.com"
        )
    }

    // MARK: 1. AuthTokenProvider Token Refresh Resilience

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

        // 1) Transient URLError(.timedOut) must NOT clear session.
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

        // 2) Transient HTTP 503 from Google must NOT clear session.
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

        // 3) Transient HTTP 429 must NOT clear session.
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

        // HTTP 400 invalid_grant -> clears session.
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

        // HTTP 401 -> also clears session.
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

        // Without forceRefresh, returns cached token without network call.
        let cachedToken = try await provider.validIDToken(forceRefresh: false)
        #expect(cachedToken == "cached-still-valid")
        #expect(callCounter.value == 0)

        // With forceRefresh: true, hits Google token endpoint and updates session.
        let refreshedToken = try await provider.validIDToken(forceRefresh: true)
        #expect(refreshedToken == newJWT)
        #expect(callCounter.value == 1)
        #expect(await provider.loadSession()?.idToken == newJWT)
        #expect(await provider.loadSession()?.refreshToken == "refresh-123")

        StubURLProtocol.setHandler(nil)
    }

    // MARK: 2. SSE Resilience (401 retry, terminal 401/403, reset backward cursor, connection states)

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
        // Exactly 2 SSE attempts: initial 401 + single retry after force refresh.
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

        // 1) 403 terminates immediately without looping.
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

        // 2) 401 followed by invalid_grant from Google clears session & terminates stream.
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
                    // Transient 503 -> should emit .reconnecting
                    return (httpResponse(url: url, statusCode: 503), Data())
                case 2:
                    // Offline URLError -> should emit .offline
                    throw URLError(.notConnectedToInternet)
                case 3:
                    // Server sends reset moving cursor backward from 50 -> 12
                    let body = "event: reset\ndata: {\"position\":12}\n\n"
                    return (httpResponse(url: url, statusCode: 200, headers: ["Content-Type": "text/event-stream"]), Data(body.utf8))
                default:
                    // Next reconnect must request after=12
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

    // MARK: 3. LiveNoriTransport Endpoints: File Fetch, Revise 202, MCP Credential, OAuth return_to, Summary, Parallel MCP Tools

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

        // File fetch
        let payload = try await transport.fetchLibraryItemFile(id: "doc-1", format: "md")
        #expect(payload.utf8Text == "# Quarterly Report\n\nFull markdown body.")
        #expect(payload.mediaType == "text/markdown; charset=utf-8")
        #expect(payload.filename == "Quarterly Report.md")
        #expect(capturedQueries.get("/users/usr-1/v1/library/doc-1/file") == "format=md")

        // Queued revision HTTP 202
        let revReceipt = try await transport.reviseLibraryItem(id: "doc-1", expectedRevision: 1, instruction: "Make concise", requestID: "req-rev")
        #expect(revReceipt.status == .queued)
        #expect(revReceipt.work?.taskID == "task-rev-42")

        // MCP credential matches Go mcp.CredentialInput (`request_id` + `token`)
        let credReceipt = try await transport.setMCPCredential(serverID: "mcp-1", expectedRevision: 1, secret: "sk-live-secret", requestID: "req-cred")
        #expect(credReceipt.resource.observed.credential == "present")
        let credJSON = try JSONSerialization.jsonObject(with: capturedBodies.get("/users/usr-1/v1/mcp/mcp-1/credential") ?? Data()) as? [String: Any]
        #expect(credJSON?["request_id"] as? String == "req-cred")
        #expect(credJSON?["token"] as? String == "sk-live-secret")
        #expect(credJSON?["value"] == nil)

        // MCP OAuth action includes return_to
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

        // Connection OAuth action includes return_to
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

        // Assistant summary
        let summary = try await transport.fetchAssistantSummary()
        #expect(summary.revision == 99)
        #expect(summary.needsYou == 2)
        #expect(summary.working == 3)
        #expect(summary.appsAttention == 1)
        #expect(summary.inboxUnread == 4)
        #expect(summary.nextScheduledAt == "2026-11-25T09:00:00Z")

        // Full snapshot fetches MCP tools for all servers via TaskGroup
        let snap = try await transport.fetchFullSnapshot()
        #expect(snap.mcpServers.count == 2)
        #expect(Set(snap.mcpTools.keys) == Set(["s1", "s2"]))
        #expect(Set(toolFetchIDs.values) == Set(["s1", "s2"]))

        StubURLProtocol.setHandler(nil)
    }
}

// MARK: - Memory Mutation Receipt Decoding & NoriStore Tests

@Suite("Memory Receipts & NoriStore Domain Logic")
struct MemoryReceiptsAndStoreTests {

    @Test("Decodes all 6 backend memory action receipt shapes from core/internal/api/memory.go")
    func decodeAllMemoryActionReceipts() throws {
        let decoder = JSONDecoder()

        // 1. Core pin / unpin -> MemoryFact
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

        // 2. Core forget -> {"note", "forgotten"}
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

        // 3. Core demote -> {"demoted", "log_entry", "note"}
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

        // 4. Log entry promote -> MemoryFact
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

        // 5. Log entry forget -> {"note", "forgotten"}
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

        // 6a. Working memory promote -> MemoryFact
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

        // 6b. Working memory dismiss -> WorkingMemory
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

        // 6c. Working memory dismiss fallback -> {"dismissed_item_id"}
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

        // 1. Pin m1
        await store.togglePinMemory(memoryID: "m1")
        #expect(store.coreMemories.first(where: { $0.id == "m1" })?.pinned == true)

        // 2. Demote m2
        await store.demoteMemory(memoryID: "m2")
        #expect(store.coreMemories.contains(where: { $0.id == "m2" }) == false)

        // 3. Forget m3
        await store.forgetMemory(memoryID: "m3")
        #expect(store.coreMemories.contains(where: { $0.id == "m3" }) == false)

        // 4. Promote log entry le-1
        await store.promoteLogEntry(date: "2026-11-24", entryID: "le-1", pinned: true)
        #expect(store.coreMemories.contains(where: { $0.id == "promoted-le-1" && $0.pinned == true }))

        // 5. Forget log entry le-2
        await store.forgetLogEntry(date: "2026-11-24", entryID: "le-2")
        #expect(store.memoryLogs.flatMap(\.entries).contains(where: { $0.id == "le-2" }) == false)

        // 6. Promote working memory item wm-1
        await store.promoteWorkingMemoryItem(itemID: "wm-1")
        #expect(store.coreMemories.contains(where: { $0.id == "promoted-wm-1" }))

        // 7a. Dismiss working memory item (WorkingMemory payload)
        await store.dismissWorkingMemoryItem(itemID: "wm-2")
        #expect(store.workingMemory.items.contains(where: { $0.id == "wm-2" }) == false)

        // 7b. Dismiss working memory item (dismissed_item_id fallback payload)
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

        // Empty input returns false without hitting transport.
        let emptyResult = await store.sendMessage("   \n  ")
        #expect(emptyResult == false)

        // Failure returns false and removes optimistic bubble.
        await mock.setShouldFailSendTurn(true)
        let failedResult = await store.sendMessage("Draft to keep")
        #expect(failedResult == false)
        #expect(store.messages.isEmpty)
        #expect(store.isThinking == false)

        // Success returns true.
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

        // Hydrate an uncached document via loadLibraryItem
        let loaded = await store.loadLibraryItem(id: "deep-doc-1")
        #expect(loaded?.id == "deep-doc-1")
        #expect(store.libraryItems.contains(where: { $0.id == "deep-doc-1" }))

        // Fetch full markdown file payload
        let file = try await store.loadLibraryItemFile(id: "deep-doc-1", format: "md")
        #expect(file.utf8Text == "# Full Doc\n\nBody text.")
        #expect(store.libraryItems.first(where: { $0.id == "deep-doc-1" })?.bodyText == "# Full Doc\n\nBody text.")

        // Queued revision (HTTP 202) -> toast "Revision started", refreshes work items, returns taskID
        await mock.setReviseStatus(.queued)
        let queuedResult = await store.reviseDocument(itemID: "deep-doc-1", instruction: "Shorten")
        #expect(queuedResult?.status == .queued)
        #expect(queuedResult?.isQueued == true)
        #expect(queuedResult?.taskID == "rev-task-1")
        #expect(store.toastMessage == "Revision started")
        #expect(await mock.fetchWorkItemsCallCount >= 1)

        // Synchronous revision -> toast "Revision saved (v2)"
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

        // Connection OAuth returns AuthAttempt and does NOT toast "Connected ..." while pending OAuth
        let connAttempt = await store.connectConnection(connectionID: "conn-1")
        #expect(connAttempt?.id == "att-conn-1")
        #expect(await mock.lastConnectionReturnTo == "nori://oauth/callback")
        #expect(store.toastMessage == nil)

        // MCP OAuth start passes return_to and returns AuthAttempt
        let mcpAttempt = await store.startMCPOAuth(serverID: "mcp-1")
        #expect(mcpAttempt?.id == "att-mcp-1")
        #expect(await mock.lastMCPReturnTo == "nori://oauth/callback")

        // MCP credential save
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

        // 1. Stream emits .offline -> store.connectionState reflects .offline
        await mock.setStreamEvents([
            .connectionState(.connecting),
            .connectionState(.connected),
            .connectionState(.reconnecting),
            .connectionState(.offline)
        ], terminalError: nil)
        store.startEventStream()
        try await Task.sleep(for: .milliseconds(50))
        #expect(store.connectionState == .offline)

        // 2. Stream emits .reset(position: 8) when session.position is 50 -> moves backward to 8 even if snapshot fails
        await mock.setShouldFailSnapshot(true)
        await mock.setStreamEvents([.reset(position: 8)], terminalError: nil)
        store.startEventStream()
        try await Task.sleep(for: .milliseconds(50))
        #expect(store.session.position == 8)
        await mock.setShouldFailSnapshot(false)

        // 3. pauseForBackground keeps user signed in; resumeFromBackground refreshes lightweight endpoints
        store.pauseForBackground()
        #expect(store.authState.isSignedIn == true)

        let snapshotBeforeResume = await mock.fetchFullSnapshotCallCount
        await mock.setStreamEvents([.connectionState(.connected)], terminalError: nil)
        await store.resumeFromBackground()
        #expect(await mock.fetchFullSnapshotCallCount == snapshotBeforeResume)
        #expect(await mock.fetchSummaryCallCount >= 1)
        #expect(await mock.fetchSessionCallCount >= 1)
        #expect(await mock.fetchWorkItemsCallCount >= 1)

        // 4. Terminal 401 from SSE signs out and sets authError
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

        // Targeted refresh for OAuth callback does NOT call fetchFullSnapshot
        store.handleIncomingURL(URL(string: "nori://oauth/callback?result=connected")!)
        try await Task.sleep(for: .milliseconds(60))
        #expect(await mock.fetchFullSnapshotCallCount == 0)
        #expect(await mock.fetchMCPServersCallCount >= 1)
        #expect(await mock.fetchConnectionsCallCount >= 1)
        #expect(await mock.fetchWorkItemsCallCount >= 1)
        #expect(await mock.fetchSummaryCallCount >= 2)
    }
}

// MARK: - Test Concurrency Helpers

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

// MARK: - Full Mock Transport for NoriStore Unit Tests

private actor FullMockTransport: NoriTransport {
    var fetchFullSnapshotCallCount = 0
    var fetchSummaryCallCount = 0
    var fetchSessionCallCount = 0
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
        return Session()
    }
    func fetchFullSnapshot() async throws -> NoriFullSnapshot {
        fetchFullSnapshotCallCount += 1
        if shouldFailSnapshot {
            throw NoriAPIError(message: "Temporary snapshot failure", statusCode: 503, retryable: true)
        }
        return NoriFullSnapshot()
    }
    nonisolated func eventStream(after position: Int) -> AsyncThrowingStream<NoriStreamEvent, Error> {
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

    func sendTurn(id: String, message: String, attachmentIDs: [String]) async throws -> Session {
        if shouldFailSendTurn {
            throw NoriAPIError(message: "Network dropped", statusCode: 503, retryable: true)
        }
        return Session(messages: [Message(id: "\(id)-user", turnID: id, role: .user, text: message, createdAt: "2026-11-24T10:00:00Z")])
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
    func fetchInteractions() async throws -> [Interaction] { [] }
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
    func fetchWorkingMemory() async throws -> WorkingMemory {
        WorkingMemory(revision: 35, items: [WorkingMemoryItem(id: "wm-2", kind: .fact, quote: "Dismiss me")], coreBytes: 512)
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
    func performAutomationAction(id: String, expectedRevision: Int, action: String, requestID: String) async throws -> MutationReceipt<Automation> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchGoals() async throws -> [Goal] { [] }
    func fetchPermissions() async throws -> [PermissionRule] { [] }
    func createPermissionRule(_ rule: PermissionRuleEditable, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        throw NoriAPIError(message: "unimplemented")
    }
    func revokePermissionRule(id: String, expectedRevision: Int, requestID: String) async throws -> MutationReceipt<PermissionRule> {
        throw NoriAPIError(message: "unimplemented")
    }
    func fetchPreferences() async throws -> Preferences { Preferences() }
    func savePreferences(_ preferences: Preferences, requestID: String) async throws -> MutationReceipt<Preferences> {
        MutationReceipt(requestID: requestID, resource: preferences)
    }
}
