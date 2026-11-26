import Foundation
#if canImport(OSLog)
import OSLog
#endif

public struct NoriDiagnosticMetadata: Sendable {
    public let appVersion: String
    public let buildNumber: String
    public let osVersion: String
    public let connectionState: String
    public let userID: String?
    public let lastTraceID: String?
    public let sentryEnabled: Bool
    public let sentryEnvironment: String

    public init(
        appVersion: String,
        buildNumber: String,
        osVersion: String,
        connectionState: String,
        userID: String?,
        lastTraceID: String?,
        sentryEnabled: Bool,
        sentryEnvironment: String
    ) {
        self.appVersion = appVersion
        self.buildNumber = buildNumber
        self.osVersion = osVersion
        self.connectionState = connectionState
        self.userID = userID
        self.lastTraceID = lastTraceID
        self.sentryEnabled = sentryEnabled
        self.sentryEnvironment = sentryEnvironment
    }
}

/// Exports recent `com.nori.ios` Unified Logging (`OSLogStore`) entries for the
/// current process with secret/token redaction so users or beta testers can attach
/// a diagnostic bundle when reporting an issue.
public enum OSLogDiagnosticExporter {
    private static let secretPatterns: [NSRegularExpression] = {
        let patterns = [
            #"(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]+"#,
            #"(?i)\b(?:authorization|password|passphrase|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|id[_ -]?token|secret)["']?\s*[:=]\s*["']?[^\s"',;]+"#,
            #"\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{16,}|AIza[A-Za-z0-9_-]{20,})\b"#,
            #"\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b"#
        ]
        return patterns.compactMap { try? NSRegularExpression(pattern: $0) }
    }()

    public static func redactSecrets(_ input: String) -> String {
        var result = input
        for regex in secretPatterns {
            let range = NSRange(result.startIndex..<result.endIndex, in: result)
            result = regex.stringByReplacingMatches(in: result, options: [], range: range, withTemplate: "[redacted]")
        }
        return result
    }

    public static func exportCurrentSessionReport(
        metadata: NoriDiagnosticMetadata,
        maxEntries: Int = 500
    ) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]

        var lines: [String] = [
            "=== Nori iOS Diagnostic Report ===",
            "Generated: \(formatter.string(from: Date()))",
            "App: com.nori.ios@\(metadata.appVersion)+\(metadata.buildNumber)",
            "OS: \(metadata.osVersion)",
            "Connection: \(metadata.connectionState)",
            "User ID: \(metadata.userID ?? "none")",
            "Last Trace ID: \(metadata.lastTraceID ?? "none")",
            "Sentry: \(metadata.sentryEnabled ? "enabled" : "disabled") (\(metadata.sentryEnvironment))",
            "=== Recent Logs (\(NoriLogger.subsystem)) ==="
        ]

        #if canImport(OSLog)
        do {
            let store = try OSLogStore(scope: .currentProcessIdentifier)
            let predicate = NSPredicate(format: "subsystem == %@", NoriLogger.subsystem)
            let position = store.position(date: Date().addingTimeInterval(-3600))
            let entries = try store.getEntries(at: position, matching: predicate)
            var collected: [String] = []
            for entry in entries {
                guard let logEntry = entry as? OSLogEntryLog else { continue }
                let ts = formatter.string(from: logEntry.date)
                let level = levelLabel(logEntry.level)
                let msg = redactSecrets(logEntry.composedMessage)
                collected.append("\(ts) [\(logEntry.category)] [\(level)] \(msg)")
            }
            if collected.isEmpty {
                lines.append("(no log entries recorded in this session)")
            } else {
                lines.append(contentsOf: collected.suffix(max(0, maxEntries)))
            }
        } catch {
            lines.append("(could not read OSLogStore: \(error.localizedDescription))")
        }
        #else
        lines.append("(OSLogStore unavailable on this platform)")
        #endif

        return lines.joined(separator: "\n")
    }

    #if canImport(OSLog)
    private static func levelLabel(_ level: OSLogEntryLog.Level) -> String {
        switch level {
        case .debug: return "DEBUG"
        case .info: return "INFO"
        case .notice: return "NOTICE"
        case .error: return "ERROR"
        case .fault: return "FAULT"
        default: return "LOG"
        }
    }
    #endif
}
