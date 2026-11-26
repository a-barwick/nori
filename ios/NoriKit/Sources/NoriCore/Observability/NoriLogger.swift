import Foundation
import OSLog

public enum NoriLogCategory: String, Sendable, CaseIterable {
    case network
    case sse
    case auth
    case store
    case ui
    case lifecycle
    case telemetry
}

/// Structured Apple Unified Logging (`os.Logger`) wrapper that automatically
/// mirrors safe breadcrumbs and warn/error structured logs to `NoriTelemetryRuntime`.
public struct NoriLogger: Sendable {
    public static let subsystem = "com.nori.ios"

    public let category: NoriLogCategory
    private let logger: Logger

    public init(category: NoriLogCategory) {
        self.category = category
        self.logger = Logger(subsystem: Self.subsystem, category: category.rawValue)
    }

    public static let network = NoriLogger(category: .network)
    public static let sse = NoriLogger(category: .sse)
    public static let auth = NoriLogger(category: .auth)
    public static let store = NoriLogger(category: .store)
    public static let ui = NoriLogger(category: .ui)
    public static let lifecycle = NoriLogger(category: .lifecycle)
    public static let telemetry = NoriLogger(category: .telemetry)

    public func debug(_ message: String, attributes: [String: String] = [:]) {
        let formatted = formatMessage(message, attributes: attributes)
        logger.debug("\(formatted, privacy: .public)")
    }

    public func info(_ message: String, attributes: [String: String] = [:], breadcrumb: Bool = false) {
        let formatted = formatMessage(message, attributes: attributes)
        logger.info("\(formatted, privacy: .public)")
        if breadcrumb {
            NoriTelemetryRuntime.shared.addBreadcrumb(
                category: "nori.\(category.rawValue)",
                message: message,
                level: .info,
                data: attributes
            )
        }
    }

    public func warning(_ message: String, attributes: [String: String] = [:], remote: Bool = true) {
        let formatted = formatMessage(message, attributes: attributes)
        logger.warning("\(formatted, privacy: .public)")
        let telemetry = NoriTelemetryRuntime.shared
        telemetry.addBreadcrumb(
            category: "nori.\(category.rawValue)",
            message: message,
            level: .warning,
            data: attributes
        )
        if remote {
            var attrs = attributes
            attrs["nori.log.category"] = category.rawValue
            attrs["nori.client.platform"] = "ios"
            telemetry.log(level: .warning, message: message, attributes: attrs)
        }
    }

    public func error(_ message: String, attributes: [String: String] = [:], remote: Bool = true) {
        let formatted = formatMessage(message, attributes: attributes)
        logger.error("\(formatted, privacy: .public)")
        let telemetry = NoriTelemetryRuntime.shared
        telemetry.addBreadcrumb(
            category: "nori.\(category.rawValue)",
            message: message,
            level: .error,
            data: attributes
        )
        if remote {
            var attrs = attributes
            attrs["nori.log.category"] = category.rawValue
            attrs["nori.client.platform"] = "ios"
            telemetry.log(level: .error, message: message, attributes: attrs)
        }
    }

    private func formatMessage(_ message: String, attributes: [String: String]) -> String {
        guard !attributes.isEmpty else { return message }
        let pairs = attributes
            .sorted { $0.key < $1.key }
            .map { "\($0.key)=\($0.value)" }
            .joined(separator: " ")
        return "\(message) | \(pairs)"
    }
}
