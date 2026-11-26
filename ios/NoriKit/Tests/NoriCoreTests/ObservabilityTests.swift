import Foundation
import XCTest
@testable import NoriCore

// MARK: - Spy Telemetry Backend

private final class SpyTelemetrySpan: NoriTelemetrySpan, @unchecked Sendable {
    private let lock = NSLock()
    let name: String
    let operation: String
    let traceContext: NoriTraceContext
    private var _tags: [String: String]
    private var _finishedStatus: NoriSpanStatus?

    init(name: String, operation: String, traceContext: NoriTraceContext, tags: [String: String]) {
        self.name = name
        self.operation = operation
        self.traceContext = traceContext
        self._tags = tags
    }

    var tags: [String: String] { lock.withLock { _tags } }
    var finishedStatus: NoriSpanStatus? { lock.withLock { _finishedStatus } }

    func setTag(_ key: String, value: String) {
        lock.withLock { _tags[key] = value }
    }

    func setData(_ key: String, value: String) {}

    func finish(status: NoriSpanStatus) {
        lock.withLock { _finishedStatus = status }
    }
}

private final class SpyTelemetry: NoriTelemetry, @unchecked Sendable {
    struct CapturedError: Sendable {
        let category: String
        let tags: [String: String]
    }

    struct LoggedMessage: Sendable {
        let level: NoriLogLevel
        let message: String
        let attributes: [String: String]
    }

    private let lock = NSLock()
    private var _lastTraceID: String?
    private var _currentUserID: String?
    private var _spans: [SpyTelemetrySpan] = []
    private var _errors: [CapturedError] = []
    private var _logs: [LoggedMessage] = []

    let isEnabled: Bool = true
    let environmentName: String = "test"

    var lastTraceID: String? { lock.withLock { _lastTraceID } }
    var currentUserID: String? { lock.withLock { _currentUserID } }
    var spans: [SpyTelemetrySpan] { lock.withLock { _spans } }
    var errors: [CapturedError] { lock.withLock { _errors } }
    var logs: [LoggedMessage] { lock.withLock { _logs } }

    func setUser(id: String?) {
        lock.withLock { _currentUserID = id }
    }

    func startSpan(name: String, operation: String, tags: [String: String]) -> any NoriTelemetrySpan {
        let child = NoriTraceContext.random(sampled: true)
        let ctx = NoriTelemetryRuntime.parentSpan.map {
            NoriTraceContext(traceID: $0.traceContext.traceID, spanID: child.spanID, sampled: $0.traceContext.sampled)
        } ?? child
        recordTraceID(ctx.traceID)
        let span = SpyTelemetrySpan(name: name, operation: operation, traceContext: ctx, tags: tags)
        lock.withLock { _spans.append(span) }
        return span
    }

    func currentTraceContext() -> NoriTraceContext? {
        NoriTelemetryRuntime.parentSpan?.traceContext
    }

    func recordTraceID(_ traceID: String) {
        lock.withLock { _lastTraceID = traceID }
    }

    func addBreadcrumb(category: String, message: String, level: NoriLogLevel, data: [String: String]) {}

    func log(level: NoriLogLevel, message: String, attributes: [String: String]) {
        lock.withLock { _logs.append(LoggedMessage(level: level, message: message, attributes: attributes)) }
    }

    func captureError(_ error: Error, category: String, tags: [String: String]) {
        lock.withLock { _errors.append(CapturedError(category: category, tags: tags)) }
    }
}

// MARK: - Mock URLProtocol for Header Inspection

private final class TraceCaptureURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let handler = Self.handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.badURL))
            return
        }
        do {
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

// MARK: - Tests

final class ObservabilityTests: XCTestCase {
    override func tearDown() {
        NoriTelemetryRuntime.reset()
        TraceCaptureURLProtocol.handler = nil
        super.tearDown()
    }

    func testTraceContextFormattingAndParsing() {
        let ctx = NoriTraceContext(
            traceID: "0123456789abcdef0123456789abcdef",
            spanID: "0123456789abcdef",
            sampled: true
        )
        XCTAssertTrue(ctx.isValid)
        XCTAssertEqual(ctx.traceparentHeader, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
        XCTAssertEqual(ctx.sentryTraceHeader, "0123456789abcdef0123456789abcdef-0123456789abcdef-1")

        let parsed = NoriTraceContext.parseTraceparent(ctx.traceparentHeader)
        XCTAssertEqual(parsed, ctx)

        XCTAssertNil(NoriTraceContext.parseTraceparent("00-00000000000000000000000000000000-0123456789abcdef-01"))
        XCTAssertNil(NoriTraceContext.parseTraceparent("invalid"))
        XCTAssertNil(NoriTraceContext.parseTraceparent(ctx.traceparentHeader + "-"))

        let randomCtx = NoriTraceContext.random(sampled: false)
        XCTAssertTrue(randomCtx.isValid)
        XCTAssertTrue(randomCtx.traceparentHeader.hasSuffix("-00"))
    }

    func testSecretRedactionInDiagnosticExporter() {
        let raw = "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig api_key=sk-proj-1234567890abcdef123456"
        let cleaned = OSLogDiagnosticExporter.redactSecrets(raw)
        XCTAssertFalse(cleaned.contains("eyJhbGci"))
        XCTAssertFalse(cleaned.contains("sk-proj-"))
        XCTAssertTrue(cleaned.contains("[redacted]"))
    }

    func testDecodingErrorDiagnosticsExtractsCodingPathWithoutPayload() {
        struct Sample: Decodable {
            struct Nested: Decodable { let count: Int }
            let nested: Nested
        }
        let badJSON = Data(#"{"nested":{"count":"not-an-int"}}"#.utf8)
        do {
            _ = try JSONDecoder().decode(Sample.self, from: badJSON)
            XCTFail("Expected decoding error")
        } catch {
            let tags = NoriErrorDiagnostics.tags(for: error)
            XCTAssertEqual(tags["nori.error.kind"], "decoding.type_mismatch")
            XCTAssertEqual(tags["nori.decoding.path"], "nested.count")
            XCTAssertEqual(tags["nori.client.platform"], "ios")
        }
    }

    @MainActor
    func testRepliesFinishOnlyTheirMatchingSubmitSpan() async throws {
        let telemetry = SpyTelemetry()
        NoriTelemetryRuntime.configure(telemetry)
        let transport = MockNoriTransport()
        let store = NoriStore(transport: transport, authProvider: AuthTokenProvider(tokenStore: InMemoryTokenStore()))
        store.authState = .signedIn(email: "test@example.test")
        defer { store.stopEventStream() }

        _ = await store.sendMessage("First turn")
        _ = await store.sendMessage("Second turn")
        XCTAssertEqual(telemetry.spans.count, 2)
        let first = try XCTUnwrap(telemetry.spans.first)
        let second = try XCTUnwrap(telemetry.spans.last)
        let firstID = try XCTUnwrap(first.tags["nori.turn.id"])
        let secondID = try XCTUnwrap(second.tags["nori.turn.id"])
        XCTAssertNotEqual(firstID, secondID)
        XCTAssertNil(first.finishedStatus)
        XCTAssertNil(second.finishedStatus)

        await transport.setStreamEvents([
            .message(seq: 1, message: Message(id: "replayed", turnID: "previous", role: .assistant, text: "Replay")),
            .message(seq: 2, message: Message(id: "second", turnID: secondID, role: .assistant, text: "Reply"))
        ], terminalError: nil)
        store.startEventStream()
        await store.waitForEventStreamCompletion()
        XCTAssertNil(first.finishedStatus)
        XCTAssertEqual(second.finishedStatus, .ok)

        await transport.setStreamEvents([
            .message(seq: 3, message: Message(id: "first", turnID: firstID, role: .assistant, status: .failed, text: "Failed"))
        ], terminalError: nil)
        store.startEventStream()
        await store.waitForEventStreamCompletion()
        XCTAssertEqual(first.finishedStatus, .error)
        XCTAssertEqual(first.tags["nori.outcome"], "error")
        XCTAssertEqual(second.tags["nori.outcome"], "reply")
    }

    func testLiveTransportInjectsTraceHeadersAndSetsUserID() async throws {
        let spy = SpyTelemetry()
        NoriTelemetryRuntime.configure(spy)

        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [TraceCaptureURLProtocol.self]
        let session = URLSession(configuration: config)

        let capturedHeadersLock = NSLock()
        nonisolated(unsafe) var seenHeaders: [[String: String]] = []

        TraceCaptureURLProtocol.handler = { req in
            capturedHeadersLock.withLock {
                seenHeaders.append(req.allHTTPHeaderFields ?? [:])
            }
            let url = req.url!
            if url.path.hasSuffix("/api/users/resolve") {
                let data = Data(#"{"id":"usr_ios_99"}"#.utf8)
                let resp = HTTPURLResponse(url: url, statusCode: 200, httpVersion: nil, headerFields: nil)!
                return (resp, data)
            }
            if url.path.hasSuffix("/v1/assistant/summary") {
                let data = Data(#"{"needs_you":0,"working":0,"apps_attention":0}"#.utf8)
                let resp = HTTPURLResponse(url: url, statusCode: 200, httpVersion: nil, headerFields: nil)!
                return (resp, data)
            }
            let data = Data(#"{"broken":true}"#.utf8)
            let resp = HTTPURLResponse(url: url, statusCode: 200, httpVersion: nil, headerFields: nil)!
            return (resp, data)
        }

        let authProvider = AuthTokenProvider(
            tokenStore: InMemoryTokenStore(),
            urlSession: session,
            clientID: "test-client-id.apps.googleusercontent.com"
        )
        await authProvider.saveSession(
            AuthSession(
                idToken: "test-id-token",
                refreshToken: nil,
                expiresAt: Date().addingTimeInterval(3600),
                email: "tester@example.com"
            )
        )
        let transport = LiveNoriTransport(
            baseURL: URL(string: "https://example.test")!,
            authProvider: authProvider,
            urlSession: session
        )

        let parent = spy.startSpan(name: "nori.submit_to_reply", operation: "nori.turn", tags: [:])
        _ = try await NoriTelemetryRuntime.$parentSpan.withValue(parent) {
            try await transport.fetchAssistantSummary()
        }

        XCTAssertEqual(spy.currentUserID, "usr_ios_99")
        XCTAssertNotNil(spy.lastTraceID)

        let headersSnapshot = capturedHeadersLock.withLock { seenHeaders }
        XCTAssertEqual(headersSnapshot.count, 2)
        for headers in headersSnapshot {
            XCTAssertEqual(headers["X-Nori-Client"], "ios")
            let traceparent = headers["traceparent"] ?? ""
            let context = try XCTUnwrap(NoriTraceContext.parseTraceparent(traceparent))
            XCTAssertEqual(context.traceID, parent.traceContext.traceID)
            XCTAssertNotEqual(context.spanID, parent.traceContext.spanID)
            XCTAssertEqual(headers["sentry-trace"], context.sentryTraceHeader)
        }

        // Verify DecodingError capture reports coding path & route to telemetry.
        do {
            _ = try await transport.fetchCapabilities()
            XCTFail("Expected decoding error for malformed capabilities payload")
        } catch {
            XCTAssertTrue(spy.errors.contains { $0.category == "network.decode" && $0.tags["http.route"] == "/v1/capabilities" })
        }
    }
}
