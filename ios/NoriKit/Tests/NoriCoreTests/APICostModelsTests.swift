import Foundation
import Testing
@testable import NoriCore

@Suite("API cost response")
struct APICostModelsTests {
    @Test("Decodes model amounts and preserves an empty model filter")
    func decodesModelAmounts() throws {
        let json = """
        {
          "days": 1,
          "timezone": "America/Los_Angeles",
          "start_at": "2026-11-01T07:00:00Z",
          "end_at": "2026-11-02T08:00:00Z",
          "bucket_size": "hour",
          "totals": {"cost_usd":0.00032,"priced_calls":1,"unpriced_calls":1,"local_calls":1,"pending_calls":1},
          "models": [
            {"model":"","cost_usd":0.00032,"priced_calls":1,"unpriced_calls":0,"local_calls":0,"pending_calls":0},
            {"model":"local","cost_usd":0,"priced_calls":0,"unpriced_calls":1,"local_calls":1,"pending_calls":1}
          ],
          "buckets": [
            {
              "start_at":"2026-11-01T08:00:00Z","end_at":"2026-11-01T09:00:00Z",
              "totals":{"cost_usd":0.00032,"priced_calls":1,"unpriced_calls":0,"local_calls":0,"pending_calls":0},
              "models":[{"model":"","cost_usd":0.00032,"priced_calls":1,"unpriced_calls":0,"local_calls":0,"pending_calls":0}]
            },
            {
              "start_at":"2026-11-01T09:00:00Z","end_at":"2026-11-01T10:00:00Z",
              "totals":{"cost_usd":0,"priced_calls":0,"unpriced_calls":1,"local_calls":1,"pending_calls":1},
              "models":[{"model":"local","cost_usd":0,"priced_calls":0,"unpriced_calls":1,"local_calls":1,"pending_calls":1}]
            }
          ]
        }
        """
        let report = try JSONDecoder().decode(APICostResponse.self, from: Data(json.utf8))
        #expect(report.timezone == "America/Los_Angeles")
        #expect(report.buckets.map(\.startAt) == ["2026-11-01T08:00:00Z", "2026-11-01T09:00:00Z"])
        #expect(report.amount(for: nil).totalCalls == 4)
        #expect(report.amount(for: "").costUSD == 0.00032)
        #expect(report.buckets[1].amount(for: "") == .zero)
        #expect(report.buckets[0].amount(for: "").hasKnownCost)
        #expect(!report.amount(for: "local").hasKnownCost)
        #expect(report.amount(for: "local").pendingCalls == 1)
        #expect(APICostAmount(costUSD: 0, pricedCalls: 0, unpricedCalls: 0, localCalls: 1, pendingCalls: 0).hasKnownCost)
    }
}
