import Foundation

public struct APICostAmount: Decodable, Sendable, Hashable {
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

public struct APICostModel: Decodable, Sendable, Hashable, Identifiable {
    public let model: String
    public let amount: APICostAmount

    public var id: String { model }

    enum CodingKeys: String, CodingKey { case model }

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
    public let models: [APICostModel]
    public let buckets: [APICostBucket]

    enum CodingKeys: String, CodingKey {
        case days, timezone
        case startAt = "start_at"
        case endAt = "end_at"
        case bucketSize = "bucket_size"
        case totals, models, buckets
    }

    public func amount(for model: String?) -> APICostAmount {
        guard let model else { return totals }
        return models.first(where: { $0.model == model })?.amount ?? .zero
    }
}
