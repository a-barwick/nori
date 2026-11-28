import Foundation

public struct APICostAmount: Codable, Sendable, Hashable {
    public let costUSD: Double
    public let pricedCalls: Int
    public let unpricedCalls: Int
    public let localCalls: Int
    public let pendingCalls: Int

    enum CodingKeys: String, CodingKey {
        case costUSD = "cost_usd"
        case pricedCalls = "priced_calls"
        case unpricedCalls = "unpriced_calls"
        case localCalls = "local_calls"
        case pendingCalls = "pending_calls"
    }

    public static let zero = APICostAmount(costUSD: 0, pricedCalls: 0, unpricedCalls: 0, localCalls: 0, pendingCalls: 0)

    public init(costUSD: Double, pricedCalls: Int, unpricedCalls: Int, localCalls: Int, pendingCalls: Int) {
        self.costUSD = costUSD
        self.pricedCalls = pricedCalls
        self.unpricedCalls = unpricedCalls
        self.localCalls = localCalls
        self.pendingCalls = pendingCalls
    }

    public var totalCalls: Int { pricedCalls + unpricedCalls + localCalls + pendingCalls }
    public var hasKnownCost: Bool { pricedCalls > 0 || (localCalls > 0 && unpricedCalls == 0 && pendingCalls == 0) }
}

public struct APICostByBilling: Codable, Sendable, Hashable {
    public let hosted: APICostAmount
    public let byok: APICostAmount
    public let local: APICostAmount

    public init(hosted: APICostAmount = .zero, byok: APICostAmount = .zero, local: APICostAmount = .zero) {
        self.hosted = hosted
        self.byok = byok
        self.local = local
    }

    public var totalCalls: Int {
        hosted.totalCalls + byok.totalCalls + local.totalCalls
    }
}

public struct APICostByCategory: Codable, Sendable, Hashable {
    public let llm: APICostAmount
    public let search: APICostAmount
    public let computer: APICostAmount

    public init(llm: APICostAmount = .zero, search: APICostAmount = .zero, computer: APICostAmount = .zero) {
        self.llm = llm
        self.search = search
        self.computer = computer
    }

    public var totalCalls: Int {
        llm.totalCalls + search.totalCalls + computer.totalCalls
    }
}

public typealias EntitlementCostSummary = OpenAPI.EntitlementCostSummary

public enum BillingTierLabel {
    public static func displayName(for rawTier: String) -> String {
        switch rawTier.lowercased() {
        case "trial": return "Trial"
        case "byok": return "BYOK"
        case "standard": return "Standard"
        case "power": return "Power"
        default: return rawTier.isEmpty ? "Plan" : rawTier.capitalized
        }
    }
}

public extension OpenAPI.EntitlementCostSummary {
    var tierDisplayName: String {
        BillingTierLabel.displayName(for: tier)
    }

    var pendingTierDisplayName: String? {
        pendingTier.map { BillingTierLabel.displayName(for: $0) }
    }

    var isInactive: Bool {
        status == "past_due" || status == "expired"
    }

    var isCreditsExhausted: Bool {
        creditGrantUSD > 0 && creditRemainingUSD <= 0
    }

    var creditsUsedRatio: Double {
        let grant = max(0, creditGrantUSD)
        guard grant > 0 else { return 0 }
        let remaining = max(0, creditRemainingUSD)
        return min(1.0, max(0.0, (grant - remaining) / grant))
    }
}

public struct APICostModel: Decodable, Sendable, Hashable, Identifiable {
    public let model: String
    public let amount: APICostAmount

    public var id: String { model }

    enum CodingKeys: String, CodingKey { case model }

    public init(model: String, amount: APICostAmount) {
        self.model = model
        self.amount = amount
    }

    public init(from decoder: any Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        model = try container.decode(String.self, forKey: .model)
        amount = try APICostAmount(from: decoder)
    }
}

public struct APICostBucket: Decodable, Sendable, Hashable, Identifiable {
    public let startAt: String
    public let endAt: String
    public let totals: APICostAmount
    public let models: [APICostModel]

    public var id: String { startAt }

    enum CodingKeys: String, CodingKey {
        case startAt = "start_at"
        case endAt = "end_at"
        case totals, models
    }

    public init(startAt: String, endAt: String, totals: APICostAmount, models: [APICostModel]) {
        self.startAt = startAt
        self.endAt = endAt
        self.totals = totals
        self.models = models
    }

    public func amount(for model: String?) -> APICostAmount {
        guard let model else { return totals }
        return models.first(where: { $0.model == model })?.amount ?? .zero
    }
}

public struct APICostResponse: Decodable, Sendable, Hashable {
    public let days: Int
    public let timezone: String
    public let startAt: String
    public let endAt: String
    public let bucketSize: String
    public let totals: APICostAmount
    public let byBilling: APICostByBilling?
    public let byCategory: APICostByCategory?
    public let entitlement: EntitlementCostSummary?
    public let models: [APICostModel]
    public let buckets: [APICostBucket]

    enum CodingKeys: String, CodingKey {
        case days, timezone
        case startAt = "start_at"
        case endAt = "end_at"
        case bucketSize = "bucket_size"
        case totals
        case byBilling = "by_billing"
        case byCategory = "by_category"
        case entitlement
        case models, buckets
    }

    public init(
        days: Int,
        timezone: String,
        startAt: String,
        endAt: String,
        bucketSize: String,
        totals: APICostAmount,
        byBilling: APICostByBilling? = nil,
        byCategory: APICostByCategory? = nil,
        entitlement: EntitlementCostSummary? = nil,
        models: [APICostModel] = [],
        buckets: [APICostBucket] = []
    ) {
        self.days = days
        self.timezone = timezone
        self.startAt = startAt
        self.endAt = endAt
        self.bucketSize = bucketSize
        self.totals = totals
        self.byBilling = byBilling
        self.byCategory = byCategory
        self.entitlement = entitlement
        self.models = models
        self.buckets = buckets
    }

    public func amount(for model: String?) -> APICostAmount {
        guard let model else { return totals }
        return models.first(where: { $0.model == model })?.amount ?? .zero
    }
}
