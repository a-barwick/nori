import Foundation


public enum AutomationStatus: String, Sendable, Codable, Hashable {
    case enabled
    case paused
    case cancelled

    public var displayLabel: String {
        switch self {
        case .enabled: return "Enabled"
        case .paused: return "Paused"
        case .cancelled: return "Cancelled"
        }
    }
}

public enum MissedRun: String, Sendable, Codable, Hashable {
    case skip
    case latest
    case all
}

public struct AutomationTrigger: Sendable, Codable, Hashable {
    public var kind: String
    public var at: String?
    public var localStart: String?
    public var timezone: String?
    public var rrule: String?
    public var event: ResourceRef?
    public var offsetSeconds: Int?
    public var source: ResourceRef?
    public var predicateRef: String?
    public var minIntervalSeconds: Int?

    enum CodingKeys: String, CodingKey {
        case kind
        case at
        case localStart = "local_start"
        case timezone
        case rrule
        case event
        case offsetSeconds = "offset_seconds"
        case source
        case predicateRef = "predicate_ref"
        case minIntervalSeconds = "min_interval_seconds"
    }

    public init(
        kind: String = "recurring",
        at: String? = nil,
        localStart: String? = nil,
        timezone: String? = "America/Los_Angeles",
        rrule: String? = nil,
        event: ResourceRef? = nil,
        offsetSeconds: Int? = nil,
        source: ResourceRef? = nil,
        predicateRef: String? = nil,
        minIntervalSeconds: Int? = nil
    ) {
        self.kind = kind
        self.at = at
        self.localStart = localStart
        self.timezone = timezone
        self.rrule = rrule
        self.event = event
        self.offsetSeconds = offsetSeconds
        self.source = source
        self.predicateRef = predicateRef
        self.minIntervalSeconds = minIntervalSeconds
    }

    private static let weekdayLabels: [String: String] = [
        "MO": "Mon", "TU": "Tue", "WE": "Wed", "TH": "Thu", "FR": "Fri", "SA": "Sat", "SU": "Sun"
    ]

    public var summaryLabel: String {
        if kind == "event_relative" {
            if let offset = offsetSeconds {
                let minutes = Int((Double(abs(offset)) / 60.0).rounded())
                let span = (minutes >= 60 && minutes % 60 == 0) ? "\(minutes / 60) h" : "\(minutes) min"
                return "\(span) \(offset <= 0 ? "before" : "after") an event"
            }
            return "Relative to an event"
        }
        if kind == "watch" {
            if let interval = minIntervalSeconds {
                let mins = max(1, Int((Double(interval) / 60.0).rounded()))
                return "Checks for changes every \(mins) min or more"
            }
            return "Watching for changes"
        }
        if let rrule, !rrule.isEmpty {
            return Self.formatRecurrence(rrule: rrule, localStart: localStart)
        }
        if let at, !at.isEmpty {
            return "Once, \(Self.formatOnceTimestamp(at))"
        }
        return kind.replacingOccurrences(of: "_", with: " ").capitalized
    }

    private static func formatOnceTimestamp(_ iso: String) -> String {
        guard let date = parseISO8601Date(iso) else { return iso }
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "EEE, MMM d 'at' h:mm a"
        return formatter.string(from: date)
    }

    static func parseISO8601Date(_ iso: String) -> Date? {
        let withFrac = ISO8601DateFormatter()
        withFrac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = withFrac.date(from: iso) { return date }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: iso)
    }

    public static func formatRecurrence(rrule: String, localStart: String?) -> String {
        let timePart: String = {
            guard let localStart,
                  let tIndex = localStart.firstIndex(of: "T") else { return "" }
            let afterT = localStart[localStart.index(after: tIndex)...]
            let hhmm = String(afterT.prefix(5))
            return hhmm.contains(":") ? hhmm : ""
        }()
        let atSuffix = timePart.isEmpty ? "" : " at \(timePart)"

        var parts: [String: String] = [:]
        for token in rrule.uppercased().split(separator: ";") {
            let kv = token.split(separator: "=", maxSplits: 1)
            if kv.count == 2 {
                parts[String(kv[0]).trimmingCharacters(in: .whitespaces)] =
                    String(kv[1]).trimmingCharacters(in: .whitespaces)
            }
        }
        let freq = parts["FREQ"] ?? ""
        let days = parts["BYDAY"]?
            .split(separator: ",")
            .map { String($0).trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        let weekdaysSet: Set<String> = ["MO", "TU", "WE", "TH", "FR"]
        let isWeekdays = days?.count == 5 && Set(days ?? []) == weekdaysSet

        switch freq {
        case "DAILY":
            return "Every day\(atSuffix)"
        case "WEEKLY":
            if isWeekdays {
                return "Weekdays\(atSuffix)"
            }
            if let days, !days.isEmpty {
                let labels = days.map { weekdayLabels[$0] ?? $0.capitalized }
                return "\(labels.joined(separator: ", "))\(atSuffix)"
            }
            return "Weekly\(atSuffix)"
        case "MONTHLY":
            return "Monthly\(atSuffix)"
        case "YEARLY":
            return "Yearly\(atSuffix)"
        case "HOURLY":
            return "Every hour\(atSuffix)"
        default:
            return timePart.isEmpty ? "Recurring" : "Recurring\(atSuffix)"
        }
    }
}

public struct AutomationLastOccurrence: Sendable, Codable, Hashable {
    public var id: String
    public var status: String
    public var scheduledAt: String
    public var summary: String?

    enum CodingKeys: String, CodingKey {
        case id
        case status
        case scheduledAt = "scheduled_at"
        case summary
    }

    public init(id: String, status: String, scheduledAt: String, summary: String? = nil) {
        self.id = id
        self.status = status
        self.scheduledAt = scheduledAt
        self.summary = summary
    }
}

public struct Automation: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var revision: Int
    public var title: String
    public var instruction: String
    public var status: AutomationStatus
    public var trigger: AutomationTrigger
    public var connectionIDs: [String]
    public var budgetUSD: Double
    public var maxActiveRuns: Int
    public var missedRun: MissedRun
    public var executionWindowSeconds: Int
    public var destinationIDs: [String]
    public var nextAt: String?
    public var endAt: String?
    public var maxOccurrences: Int?
    public var lastOccurrenceID: String?
    public var createdAt: String?
    public var updatedAt: String?
    public var authorityRuleIDs: [String]
    public var snoozedUntil: String?
    public var lastOccurrence: AutomationLastOccurrence?

    enum CodingKeys: String, CodingKey {
        case id
        case revision
        case title
        case instruction
        case status
        case trigger
        case connectionIDs = "connection_ids"
        case budgetUSD = "budget_usd"
        case maxActiveRuns = "max_active_runs"
        case missedRun = "missed_run"
        case executionWindowSeconds = "execution_window_seconds"
        case destinationIDs = "destination_ids"
        case nextAt = "next_at"
        case endAt = "end_at"
        case maxOccurrences = "max_occurrences"
        case lastOccurrenceID = "last_occurrence_id"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case authorityRuleIDs = "authority_rule_ids"
        case snoozedUntil = "snoozed_until"
        case lastOccurrence = "last_occurrence"
    }

    public init(
        id: String,
        revision: Int = 1,
        title: String,
        instruction: String = "",
        status: AutomationStatus = .enabled,
        trigger: AutomationTrigger = AutomationTrigger(),
        connectionIDs: [String] = [],
        budgetUSD: Double = 0.50,
        maxActiveRuns: Int = 1,
        missedRun: MissedRun = .skip,
        executionWindowSeconds: Int = 3600,
        destinationIDs: [String] = [],
        nextAt: String? = nil,
        endAt: String? = nil,
        maxOccurrences: Int? = nil,
        lastOccurrenceID: String? = nil,
        createdAt: String? = nil,
        updatedAt: String? = nil,
        authorityRuleIDs: [String] = [],
        snoozedUntil: String? = nil,
        lastOccurrence: AutomationLastOccurrence? = nil
    ) {
        self.id = id
        self.revision = revision
        self.title = title
        self.instruction = instruction
        self.status = status
        self.trigger = trigger
        self.connectionIDs = connectionIDs
        self.budgetUSD = budgetUSD
        self.maxActiveRuns = maxActiveRuns
        self.missedRun = missedRun
        self.executionWindowSeconds = executionWindowSeconds
        self.destinationIDs = destinationIDs
        self.nextAt = nextAt
        self.endAt = endAt
        self.maxOccurrences = maxOccurrences
        self.lastOccurrenceID = lastOccurrenceID
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.authorityRuleIDs = authorityRuleIDs
        self.snoozedUntil = snoozedUntil
        self.lastOccurrence = lastOccurrence
    }

    public var isSpentOneTime: Bool {
        status == .enabled && (nextAt?.isEmpty ?? true) && trigger.kind == "once"
    }

    public var isSnoozed: Bool {
        guard status == .enabled,
              let raw = snoozedUntil, !raw.isEmpty,
              let date = AutomationTrigger.parseISO8601Date(raw) else { return false }
        return date > Date()
    }

    public var lastRunFailed: Bool {
        lastOccurrence?.status == "failed"
    }

    public var editable: AutomationEditable {
        AutomationEditable(
            title: title,
            instruction: instruction,
            trigger: trigger,
            connectionIDs: connectionIDs,
            budgetUSD: budgetUSD,
            maxActiveRuns: maxActiveRuns,
            missedRun: missedRun,
            executionWindowSeconds: executionWindowSeconds,
            destinationIDs: destinationIDs,
            endAt: endAt,
            maxOccurrences: maxOccurrences
        )
    }
}

public struct AutomationEditable: Sendable, Codable, Hashable {
    public var title: String
    public var instruction: String
    public var trigger: AutomationTrigger
    public var connectionIDs: [String]
    public var budgetUSD: Double
    public var maxActiveRuns: Int
    public var missedRun: MissedRun?
    public var executionWindowSeconds: Int
    public var destinationIDs: [String]
    public var endAt: String?
    public var maxOccurrences: Int?

    enum CodingKeys: String, CodingKey {
        case title
        case instruction
        case trigger
        case connectionIDs = "connection_ids"
        case budgetUSD = "budget_usd"
        case maxActiveRuns = "max_active_runs"
        case missedRun = "missed_run"
        case executionWindowSeconds = "execution_window_seconds"
        case destinationIDs = "destination_ids"
        case endAt = "end_at"
        case maxOccurrences = "max_occurrences"
    }

    public init(
        title: String,
        instruction: String,
        trigger: AutomationTrigger,
        connectionIDs: [String] = [],
        budgetUSD: Double = 0.50,
        maxActiveRuns: Int = 1,
        missedRun: MissedRun? = nil,
        executionWindowSeconds: Int = 3600,
        destinationIDs: [String] = [],
        endAt: String? = nil,
        maxOccurrences: Int? = nil
    ) {
        self.title = title
        self.instruction = instruction
        self.trigger = trigger
        self.connectionIDs = connectionIDs
        self.budgetUSD = budgetUSD
        self.maxActiveRuns = maxActiveRuns
        self.missedRun = missedRun
        self.executionWindowSeconds = executionWindowSeconds
        self.destinationIDs = destinationIDs
        self.endAt = endAt
        self.maxOccurrences = maxOccurrences
    }
}


public struct AutomationOccurrence: Sendable, Codable, Hashable, Identifiable {
    public var id: String
    public var automationID: String
    public var automationRevision: Int
    public var scheduledAt: String
    public var status: String
    public var taskIDs: [String]
    public var deliveryIDs: [String]
    public var startedAt: String?
    public var finishedAt: String?
    public var summary: String?
    public var error: ErrorDetail?
    public var reason: String?

    enum CodingKeys: String, CodingKey {
        case id
        case automationID = "automation_id"
        case automationRevision = "automation_revision"
        case scheduledAt = "scheduled_at"
        case status
        case taskIDs = "task_ids"
        case deliveryIDs = "delivery_ids"
        case startedAt = "started_at"
        case finishedAt = "finished_at"
        case summary
        case error
        case reason
    }

    public init(
        id: String,
        automationID: String,
        automationRevision: Int = 1,
        scheduledAt: String,
        status: String,
        taskIDs: [String] = [],
        deliveryIDs: [String] = [],
        startedAt: String? = nil,
        finishedAt: String? = nil,
        summary: String? = nil,
        error: ErrorDetail? = nil,
        reason: String? = nil
    ) {
        self.id = id
        self.automationID = automationID
        self.automationRevision = automationRevision
        self.scheduledAt = scheduledAt
        self.status = status
        self.taskIDs = taskIDs
        self.deliveryIDs = deliveryIDs
        self.startedAt = startedAt
        self.finishedAt = finishedAt
        self.summary = summary
        self.error = error
        self.reason = reason
    }
}

public struct HealthCheckResult: Sendable, Codable, Hashable {
    public var ok: Bool
    public var status: String
    public var model: String?
    public var latencyMs: Int
    public var checkedAt: Date
    public var errorMessage: String?

    public init(
        ok: Bool,
        status: String,
        model: String? = nil,
        latencyMs: Int,
        checkedAt: Date = Date(),
        errorMessage: String? = nil
    ) {
        self.ok = ok
        self.status = status
        self.model = model
        self.latencyMs = latencyMs
        self.checkedAt = checkedAt
        self.errorMessage = errorMessage
    }
}
