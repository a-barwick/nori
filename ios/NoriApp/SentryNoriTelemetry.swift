import Foundation
import NoriCore
#if canImport(Sentry)
import Sentry
#endif

/// Bridges `NoriCore`'s zero-dependency `NoriTelemetry` protocol to Sentry Cocoa
/// (`sentry-cocoa`) with crash reporting, watchdog/OOM tracking, app hangs,
/// Release Health, MetricKit, distributed tracing, and Sentry Structured Logs.
final class SentryNoriTelemetry: NoriTelemetry, @unchecked Sendable {
    private let lock = NSLock()
    private var _lastTraceID: String?
    private var _currentUserID: String?

    let isEnabled: Bool
    let environmentName: String

    private init(isEnabled: Bool, environmentName: String) {
        self.isEnabled = isEnabled
        self.environmentName = environmentName
    }

    var lastTraceID: String? {
        lock.withLock { _lastTraceID }
    }

    var currentUserID: String? {
        lock.withLock { _currentUserID }
    }

    /// Initializes Sentry Cocoa from `Info.plist` build settings (`SENTRY_DSN`,
    /// `SENTRY_ENVIRONMENT`, `SENTRY_TRACES_SAMPLE_RATE`) unless running in preview mode.
    static func bootstrap(isPreviewMode: Bool) {
        guard !isPreviewMode else {
            NoriTelemetryRuntime.configure(NoopNoriTelemetry())
            return
        }

        let info = Bundle.main.infoDictionary ?? [:]
        let rawDSN = (info["SENTRY_DSN"] as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let environment = (info["SENTRY_ENVIRONMENT"] as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let resolvedEnv = environment.isEmpty ? "development" : environment

        guard !rawDSN.isEmpty else {
            NoriTelemetryRuntime.configure(NoopNoriTelemetry())
            NoriLogger.telemetry.info("sentry.disabled_empty_dsn", attributes: ["env": resolvedEnv])
            return
        }

        #if canImport(Sentry)
        let sampleRateRaw = (info["SENTRY_TRACES_SAMPLE_RATE"] as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let sampleRate = Double(sampleRateRaw).flatMap { ($0 >= 0 && $0 <= 1) ? $0 : nil } ?? 0.2
        let shortVersion = (info["CFBundleShortVersionString"] as? String) ?? "1.0"
        let buildNumber = (info["CFBundleVersion"] as? String) ?? "1"
        let releaseName = "com.nori.ios@\(shortVersion)+\(buildNumber)"

        SentrySDK.start { options in
            options.dsn = rawDSN
            options.environment = resolvedEnv
            options.releaseName = releaseName

            // Crash, Watchdog/OOM, App Hang, Release Health & MetricKit
            options.enableCrashHandler = true
            options.enableWatchdogTerminationTracking = true
            options.enableAppHangTracking = true
            options.appHangTimeoutInterval = 2.0
            options.enableAutoSessionTracking = true
            options.enableMetricKit = true

            // Distributed Tracing & Performance
            options.tracesSampleRate = NSNumber(value: sampleRate)
            options.enableAutoPerformanceTracing = true
            // LiveNoriTransport reports normalized routes without query strings.
            options.enableNetworkTracking = false
            options.enableNetworkBreadcrumbs = false
            options.enableCaptureFailedRequests = false

            options.experimental.enableLogs = true
            options.beforeSendLog = { log in
                if let traceID = NoriTelemetryRuntime.parentSpan?.traceContext.traceID {
                    log.traceId = SentryId(uuidString: traceID)
                }
                return log
            }

            // Baseline security hygiene: never ship raw headers/cookies/screenshots by default
            options.sendDefaultPii = false
            options.attachScreenshot = false
            options.attachViewHierarchy = false

            options.beforeSend = { event in
                Self.sanitizeEvent(event)
            }

            options.beforeBreadcrumb = { crumb in
                Self.sanitizeBreadcrumb(crumb)
            }
        }

        guard SentrySDK.isEnabled else {
            NoriTelemetryRuntime.configure(NoopNoriTelemetry())
            NoriLogger.telemetry.warning("sentry.start_failed", remote: false)
            return
        }
        let backend = SentryNoriTelemetry(isEnabled: true, environmentName: resolvedEnv)
        NoriTelemetryRuntime.configure(backend)
        NoriLogger.telemetry.info(
            "sentry.initialized",
            attributes: [
                "env": resolvedEnv,
                "release": releaseName,
                "traces_sample_rate": String(sampleRate)
            ]
        )
        #else
        NoriTelemetryRuntime.configure(NoopNoriTelemetry())
        #endif
    }

    // MARK: - NoriTelemetry

    func setUser(id: String?) {
        lock.withLock { _currentUserID = id }
        #if canImport(Sentry)
        guard isEnabled else { return }
        if let id, !id.isEmpty {
            let user = User(userId: id)
            SentrySDK.setUser(user)
            SentrySDK.configureScope { scope in
                scope.setTag(value: id, key: "nori.user.id")
                scope.setTag(value: "ios", key: "nori.client.platform")
            }
        } else {
            SentrySDK.setUser(nil)
            SentrySDK.configureScope { scope in
                scope.removeTag(key: "nori.user.id")
            }
        }
        #endif
    }

    func startSpan(name: String, operation: String, tags: [String: String]) -> any NoriTelemetrySpan {
        #if canImport(Sentry)
        guard isEnabled else {
            let ctx = NoriTraceContext.random(sampled: false)
            recordTraceID(ctx.traceID)
            return NoopNoriTelemetrySpan(traceContext: ctx)
        }
        let span: Span
        if let parent = NoriTelemetryRuntime.parentSpan as? SentrySpanAdapter {
            span = parent.span.startChild(operation: operation, description: name)
        } else {
            span = SentrySDK.startTransaction(name: name, operation: operation, bindToScope: false)
        }
        span.setTag(value: "ios", key: "nori.client.platform")
        if let uid = currentUserID {
            span.setTag(value: uid, key: "nori.user.id")
        }
        for (key, value) in tags {
            span.setTag(value: value, key: key)
        }
        let traceCtx = Self.extractTraceContext(from: span)
        recordTraceID(traceCtx.traceID)
        return SentrySpanAdapter(span: span, traceContext: traceCtx)
        #else
        let ctx = NoriTraceContext.random(sampled: false)
        recordTraceID(ctx.traceID)
        return NoopNoriTelemetrySpan(traceContext: ctx)
        #endif
    }

    func currentTraceContext() -> NoriTraceContext? {
        #if canImport(Sentry)
        return NoriTelemetryRuntime.parentSpan?.traceContext
        #else
        return nil
        #endif
    }

    func recordTraceID(_ traceID: String) {
        lock.withLock { _lastTraceID = traceID }
    }

    func addBreadcrumb(category: String, message: String, level: NoriLogLevel, data: [String: String]) {
        #if canImport(Sentry)
        guard isEnabled else { return }
        let crumb = Breadcrumb(level: Self.sentryLevel(level), category: category)
        crumb.message = OSLogDiagnosticExporter.redactSecrets(message)
        if !data.isEmpty {
            crumb.data = data.mapValues { OSLogDiagnosticExporter.redactSecrets($0) }
        }
        SentrySDK.addBreadcrumb(crumb)
        #endif
    }

    func log(level: NoriLogLevel, message: String, attributes: [String: String]) {
        #if canImport(Sentry)
        guard isEnabled else { return }
        let safeMessage = OSLogDiagnosticExporter.redactSecrets(message)
        var attrs: [String: Any] = attributes.mapValues { OSLogDiagnosticExporter.redactSecrets($0) }
        attrs["nori.client.platform"] = "ios"
        if let uid = currentUserID, attrs["nori.user.id"] == nil {
            attrs["nori.user.id"] = uid
        }
        switch level {
        case .debug:
            SentrySDK.logger.debug(safeMessage, attributes: attrs)
        case .info:
            SentrySDK.logger.info(safeMessage, attributes: attrs)
        case .warning:
            SentrySDK.logger.warn(safeMessage, attributes: attrs)
        case .error:
            SentrySDK.logger.error(safeMessage, attributes: attrs)
        }
        #endif
    }

    func captureError(_ error: Error, category: String, tags: [String: String]) {
        #if canImport(Sentry)
        guard isEnabled else { return }
        // Build an error from operational metadata so the SDK never serializes
        // arbitrary NSError userInfo, underlying errors, or server responses.
        let diagnosticError = NSError(
            domain: "Nori.\(category)",
            code: 1,
            userInfo: [NSLocalizedDescriptionKey: category]
        )
        let parent = NoriTelemetryRuntime.parentSpan as? SentrySpanAdapter
        let userID = currentUserID
        SentrySDK.capture(error: diagnosticError) { scope in
            if let parent { scope.span = parent.span }
            scope.setTag(value: category, key: "nori.error.category")
            scope.setTag(value: "ios", key: "nori.client.platform")
            if let uid = userID {
                scope.setTag(value: uid, key: "nori.user.id")
            }
            for (key, value) in tags {
                scope.setTag(value: OSLogDiagnosticExporter.redactSecrets(value), key: key)
            }
        }
        #endif
    }

    // MARK: - Helpers

    #if canImport(Sentry)
    private static func extractTraceContext(from span: Span) -> NoriTraceContext {
        let traceID = span.traceId.sentryIdString
        let spanID = span.spanId.sentrySpanIdString
        let sampled = (span.sampled == .yes)
        let ctx = NoriTraceContext(traceID: traceID, spanID: spanID, sampled: sampled)
        return ctx.isValid ? ctx : NoriTraceContext.random(sampled: sampled)
    }

    private static func sentryLevel(_ level: NoriLogLevel) -> SentryLevel {
        switch level {
        case .debug: return .debug
        case .info: return .info
        case .warning: return .warning
        case .error: return .error
        }
    }

    private static func sanitizeEvent(_ event: Event) -> Event? {
        event.request = nil
        if let message = event.message?.formatted {
            event.message = SentryMessage(formatted: OSLogDiagnosticExporter.redactSecrets(message))
        }
        return event
    }

    private static func sanitizeBreadcrumb(_ crumb: Breadcrumb) -> Breadcrumb? {
        if let message = crumb.message {
            crumb.message = OSLogDiagnosticExporter.redactSecrets(message)
        }
        if let data = crumb.data {
            var cleaned: [String: Any] = [:]
            for (key, value) in data {
                let lower = key.lowercased()
                if lower.contains("authorization") || lower.contains("token") || lower.contains("cookie") || lower.contains("secret") {
                    continue
                }
                if let str = value as? String {
                    cleaned[key] = OSLogDiagnosticExporter.redactSecrets(str)
                } else {
                    cleaned[key] = value
                }
            }
            crumb.data = cleaned
        }
        return crumb
    }
    #endif
}

#if canImport(Sentry)
private final class SentrySpanAdapter: NoriTelemetrySpan, @unchecked Sendable {
    let span: Span
    let traceContext: NoriTraceContext

    init(span: Span, traceContext: NoriTraceContext) {
        self.span = span
        self.traceContext = traceContext
    }

    func setTag(_ key: String, value: String) {
        span.setTag(value: value, key: key)
    }

    func setData(_ key: String, value: String) {
        span.setData(value: value, key: key)
    }

    func finish(status: NoriSpanStatus) {
        let sentryStatus: SentrySpanStatus
        switch status {
        case .ok: sentryStatus = .ok
        case .error: sentryStatus = .internalError
        case .cancelled: sentryStatus = .cancelled
        case .unknown: sentryStatus = .unknownError
        }
        span.finish(status: sentryStatus)
    }
}
#endif
