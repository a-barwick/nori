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

    public var summaryLabel: String {
        if let rrule, !rrule.isEmpty {
            return rrule
        }
        if let at, !at.isEmpty {
            return "Once at \(at)"
        }
        if kind == "event_relative" {
            if let offset = offsetSeconds {
                let mins = abs(offset) / 60
                return offset < 0 ? "\(mins)m before event" : "\(mins)m after event"
            }
            return "Relative to event"
        }
        if kind == "watch" {
            return "Watching for changes"
        }
        return kind.capitalized
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
