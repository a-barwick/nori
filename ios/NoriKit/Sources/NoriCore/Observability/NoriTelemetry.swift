import Foundation

// MARK: - Log Level & Span Status

public enum NoriLogLevel: String, Sendable, Hashable {
    case debug
    case info
    case warning
    case error
}

public enum NoriSpanStatus: String, Sendable, Hashable {
    case ok
    case error
    case cancelled
    case unknown
}

// MARK: - Distributed Trace Context (W3C traceparent + Sentry sentry-trace)

public struct NoriTraceContext: Sendable, Hashable {
    public let traceID: String
    public let spanID: String
    public let sampled: Bool

    public init(traceID: String, spanID: String, sampled: Bool) {
        self.traceID = traceID.lowercased()
        self.spanID = spanID.lowercased()
        self.sampled = sampled
    }

    /// W3C Trace Context header: `00-<32-hex-trace-id>-<16-hex-span-id>-<01|00>`
    public var traceparentHeader: String {
        "00-\(traceID)-\(spanID)-\(sampled ? "01" : "00")"
    }

    /// Sentry wire header: `<32-hex-trace-id>-<16-hex-span-id>-<1|0>`
    public var sentryTraceHeader: String {
        "\(traceID)-\(spanID)-\(sampled ? "1" : "0")"
    }

    public var isValid: Bool {
        Self.isValidHex(traceID, length: 32) && Self.isValidHex(spanID, length: 16)
    }

    /// Generates a fresh, valid W3C trace context (useful as an unsampled correlation ID
    /// when Sentry tracing is disabled or no active span exists).
    public static func random(sampled: Bool = false) -> NoriTraceContext {
        NoriTraceContext(
            traceID: randomHex(byteCount: 16),
            spanID: randomHex(byteCount: 8),
            sampled: sampled
        )
    }

    /// Parses a W3C `traceparent` header (`00-<trace_id>-<span_id>-<flags>`).
    public static func parseTraceparent(_ header: String) -> NoriTraceContext? {
        let parts = header.trimmingCharacters(in: .whitespacesAndNewlines).split(separator: "-", omittingEmptySubsequences: false)
        guard parts.count == 4, parts[0] == "00" else { return nil }
        let traceID = String(parts[1]).lowercased()
        let spanID = String(parts[2]).lowercased()
        let flags = String(parts[3]).lowercased()
        guard flags == "00" || flags == "01" else { return nil }
        let ctx = NoriTraceContext(traceID: traceID, spanID: spanID, sampled: flags == "01")
        return ctx.isValid ? ctx : nil
    }

    private static func randomHex(byteCount: Int) -> String {
        var rng = SystemRandomNumberGenerator()
        var bytes = [UInt8](repeating: 0, count: byteCount)
        for i in 0..<byteCount {
            bytes[i] = UInt8.random(in: 0...255, using: &rng)
        }
        // Ensure non-zero per W3C Trace Context spec.
        if bytes.allSatisfy({ $0 == 0 }) {
            bytes[byteCount - 1] = 1
        }
        return bytes.map { String(format: "%02x", $0) }.joined()
    }

    private static func isValidHex(_ value: String, length: Int) -> Bool {
        guard value.count == length else { return false }
        var nonZero = false
        for scalar in value.unicodeScalars {
            let v = scalar.value
            let isDigit = (0x30...0x39).contains(v)
            let isLowerHex = (0x61...0x66).contains(v)
            guard isDigit || isLowerHex else { return false }
            if v != 0x30 { nonZero = true }
        }
        return nonZero
    }
}

// MARK: - Telemetry Span & Backend Protocols

public protocol NoriTelemetrySpan: AnyObject, Sendable {
    var traceContext: NoriTraceContext { get }
    func setTag(_ key: String, value: String)
    func setData(_ key: String, value: String)
    func finish(status: NoriSpanStatus)
}

public protocol NoriTelemetry: AnyObject, Sendable {
    var isEnabled: Bool { get }
    var environmentName: String { get }
    var lastTraceID: String? { get }
    var currentUserID: String? { get }

    func setUser(id: String?)
    func startSpan(name: String, operation: String, tags: [String: String]) -> any NoriTelemetrySpan
    func currentTraceContext() -> NoriTraceContext?
    func recordTraceID(_ traceID: String)
    func addBreadcrumb(category: String, message: String, level: NoriLogLevel, data: [String: String])
    func log(level: NoriLogLevel, message: String, attributes: [String: String])
    func captureError(_ error: Error, category: String, tags: [String: String])
}

public extension NoriTelemetry {
    func startSpan(name: String, operation: String) -> any NoriTelemetrySpan {
        startSpan(name: name, operation: operation, tags: [:])
    }

    func addBreadcrumb(category: String, message: String, level: NoriLogLevel = .info) {
        addBreadcrumb(category: category, message: message, level: level, data: [:])
    }

    func log(level: NoriLogLevel, message: String) {
        log(level: level, message: message, attributes: [:])
    }

    func captureError(_ error: Error, category: String) {
        captureError(error, category: category, tags: [:])
    }
}

// MARK: - No-op Implementation (Tests, Previews, Disabled Builds)

public final class NoopNoriTelemetrySpan: NoriTelemetrySpan, @unchecked Sendable {
    public let traceContext: NoriTraceContext

    public init(traceContext: NoriTraceContext = .random(sampled: false)) {
        self.traceContext = traceContext
    }

    public func setTag(_ key: String, value: String) {}
    public func setData(_ key: String, value: String) {}
    public func finish(status: NoriSpanStatus) {}
}

public final class NoopNoriTelemetry: NoriTelemetry, @unchecked Sendable {
    private let lock = NSLock()
    private var _lastTraceID: String?
    private var _currentUserID: String?

    public let isEnabled: Bool = false
    public let environmentName: String = "disabled"

    public init() {}

    public var lastTraceID: String? {
        lock.withLock { _lastTraceID }
    }

    public var currentUserID: String? {
        lock.withLock { _currentUserID }
    }

    public func setUser(id: String?) {
        lock.withLock { _currentUserID = id }
    }

    public func startSpan(name: String, operation: String, tags: [String: String]) -> any NoriTelemetrySpan {
        var ctx = NoriTraceContext.random()
        if let parent = currentTraceContext() {
            ctx = NoriTraceContext(traceID: parent.traceID, spanID: ctx.spanID, sampled: parent.sampled)
        }
        recordTraceID(ctx.traceID)
        return NoopNoriTelemetrySpan(traceContext: ctx)
    }

    public func currentTraceContext() -> NoriTraceContext? {
        NoriTelemetryRuntime.parentSpan?.traceContext
    }

    public func recordTraceID(_ traceID: String) {
        lock.withLock { _lastTraceID = traceID }
    }

    public func addBreadcrumb(category: String, message: String, level: NoriLogLevel, data: [String: String]) {}
    public func log(level: NoriLogLevel, message: String, attributes: [String: String]) {}
    public func captureError(_ error: Error, category: String, tags: [String: String]) {}
}

// MARK: - Shared Runtime Registry

public enum NoriTelemetryRuntime {
    @TaskLocal public static var parentSpan: (any NoriTelemetrySpan)?

    private static let lock = NSLock()
    private nonisolated(unsafe) static var _shared: any NoriTelemetry = NoopNoriTelemetry()

    public static var shared: any NoriTelemetry {
        lock.withLock { _shared }
    }

    public static func configure(_ backend: any NoriTelemetry) {
        lock.withLock { _shared = backend }
    }

    public static func reset() {
        lock.withLock { _shared = NoopNoriTelemetry() }
    }
}

// MARK: - Hourly Telemetry Quota Limiter

/// Enforces independent sliding-window hourly caps on Sentry events,
/// root transactions, and structured logs so a client loop can never flood quota.
public final class NoriTelemetryQuotaLimiter: @unchecked Sendable {
    public struct Limits: Sendable, Hashable {
        public let maxEventsPerHour: Int
        public let maxTransactionsPerHour: Int
        public let maxLogsPerHour: Int
        public let windowSeconds: TimeInterval

        public static let `default` = Limits(
            maxEventsPerHour: 60,
            maxTransactionsPerHour: 120,
            maxLogsPerHour: 300,
            windowSeconds: 3600
        )

        public init(
            maxEventsPerHour: Int = 60,
            maxTransactionsPerHour: Int = 120,
            maxLogsPerHour: Int = 300,
            windowSeconds: TimeInterval = 3600
        ) {
            self.maxEventsPerHour = max(0, maxEventsPerHour)
            self.maxTransactionsPerHour = max(0, maxTransactionsPerHour)
            self.maxLogsPerHour = max(0, maxLogsPerHour)
            self.windowSeconds = max(1, windowSeconds)
        }
    }

    public let limits: Limits
    private let lock = NSLock()
    private let nowProvider: @Sendable () -> Date
    private var eventTimestamps: [TimeInterval] = []
    private var transactionTimestamps: [TimeInterval] = []
    private var logTimestamps: [TimeInterval] = []

    public init(
        limits: Limits = .default,
        now: @escaping @Sendable () -> Date = { Date() }
    ) {
        self.limits = limits
        self.nowProvider = now
    }

    public func allowEvent() -> Bool {
        allow(in: \.eventTimestamps, limit: limits.maxEventsPerHour)
    }

    public func allowTransaction() -> Bool {
        allow(in: \.transactionTimestamps, limit: limits.maxTransactionsPerHour)
    }

    public func allowLog() -> Bool {
        allow(in: \.logTimestamps, limit: limits.maxLogsPerHour)
    }

    public var isEventCapReached: Bool {
        isAtCap(in: \.eventTimestamps, limit: limits.maxEventsPerHour)
    }

    public var isLogCapReached: Bool {
        isAtCap(in: \.logTimestamps, limit: limits.maxLogsPerHour)
    }

    public func reset() {
        lock.withLock {
            eventTimestamps.removeAll()
            transactionTimestamps.removeAll()
            logTimestamps.removeAll()
        }
    }

    private func allow(
        in keyPath: ReferenceWritableKeyPath<NoriTelemetryQuotaLimiter, [TimeInterval]>,
        limit: Int
    ) -> Bool {
        guard limit > 0 else { return false }
        let now = nowProvider().timeIntervalSinceReferenceDate
        let cutoff = now - limits.windowSeconds
        return lock.withLock {
            var list = self[keyPath: keyPath]
            list.removeAll { $0 <= cutoff }
            guard list.count < limit else {
                self[keyPath: keyPath] = list
                return false
            }
            list.append(now)
            self[keyPath: keyPath] = list
            return true
        }
    }

    private func isAtCap(
        in keyPath: ReferenceWritableKeyPath<NoriTelemetryQuotaLimiter, [TimeInterval]>,
        limit: Int
    ) -> Bool {
        guard limit > 0 else { return true }
        let now = nowProvider().timeIntervalSinceReferenceDate
        let cutoff = now - limits.windowSeconds
        return lock.withLock {
            var list = self[keyPath: keyPath]
            list.removeAll { $0 <= cutoff }
            self[keyPath: keyPath] = list
            return list.count >= limit
        }
    }
}

// MARK: - Safe Error Metadata Extraction

public enum NoriErrorDiagnostics {
    /// Extracts low-cardinality, payload-free diagnostic tags for Swift errors
    /// (such as `DecodingError` coding paths, `NoriAPIError` status/codes, or `URLError` codes).
    public static func tags(for error: Error) -> [String: String] {
        var out: [String: String] = [
            "error.type": String(describing: type(of: error)),
            "nori.client.platform": "ios"
        ]

        if let decodingError = error as? DecodingError {
            switch decodingError {
            case .keyNotFound(let key, let context):
                out["nori.error.kind"] = "decoding.key_not_found"
                out["nori.decoding.path"] = formatCodingPath(context.codingPath + [key])
            case .typeMismatch(let expected, let context):
                out["nori.error.kind"] = "decoding.type_mismatch"
                out["nori.decoding.expected"] = String(describing: expected)
                out["nori.decoding.path"] = formatCodingPath(context.codingPath)
            case .valueNotFound(let expected, let context):
                out["nori.error.kind"] = "decoding.value_not_found"
                out["nori.decoding.expected"] = String(describing: expected)
                out["nori.decoding.path"] = formatCodingPath(context.codingPath)
            case .dataCorrupted(let context):
                out["nori.error.kind"] = "decoding.data_corrupted"
                out["nori.decoding.path"] = formatCodingPath(context.codingPath)
            @unknown default:
                out["nori.error.kind"] = "decoding.unknown"
            }
        } else if let apiError = error as? NoriAPIError {
            out["nori.error.kind"] = "api_error"
            if apiError.statusCode > 0 {
                out["http.status_code"] = String(apiError.statusCode)
            }
            if let code = apiError.code, isSafeIdentifier(code) {
                out["nori.error.code"] = code
            }
            if let requestID = apiError.requestID, isSafeIdentifier(requestID) {
                out["nori.request.id"] = requestID
            }
        } else if let urlError = error as? URLError {
            out["nori.error.kind"] = "url_error"
            out["nori.url_error.code"] = String(urlError.code.rawValue)
        }

        return out
    }

    static func isSafeIdentifier(_ value: String) -> Bool {
        value.range(of: #"\A[A-Za-z0-9_.:-]{1,128}\z"#, options: .regularExpression) != nil
    }

    private static func formatCodingPath(_ path: [CodingKey]) -> String {
        guard !path.isEmpty else { return "<root>" }
        let formatted = path.map { key in
            if let idx = key.intValue {
                return "[\(idx)]"
            }
            if String(describing: type(of: key)) == "_DictionaryCodingKey" { return "<key>" }
            return isSafeIdentifier(key.stringValue) ? key.stringValue : "<key>"
        }.joined(separator: ".")
        return String(formatted.prefix(160))
    }
}
