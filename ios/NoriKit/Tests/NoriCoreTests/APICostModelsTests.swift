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
        #expect(report.byBilling == nil)
        #expect(report.byCategory == nil)
        #expect(report.entitlement == nil)
    }

    @Test("Decodes upgraded CostReport with by_billing, by_category, and entitlement summary")
    func decodesUpgradedCostReport() throws {
        let json = """
        {
          "days": 30,
          "timezone": "America/Los_Angeles",
          "start_at": "2026-10-29T07:00:00Z",
          "end_at": "2026-11-28T08:00:00Z",
          "bucket_size": "day",
          "totals": {"cost_usd": 4.25, "priced_calls": 18, "unpriced_calls": 0, "local_calls": 3, "pending_calls": 1},
          "by_billing": {
            "hosted": {"cost_usd": 3.50, "priced_calls": 12, "unpriced_calls": 0, "local_calls": 0, "pending_calls": 1},
            "byok": {"cost_usd": 0.75, "priced_calls": 6, "unpriced_calls": 0, "local_calls": 0, "pending_calls": 0},
            "local": {"cost_usd": 0, "priced_calls": 0, "unpriced_calls": 0, "local_calls": 3, "pending_calls": 0}
          },
          "by_category": {
            "llm": {"cost_usd": 3.80, "priced_calls": 14, "unpriced_calls": 0, "local_calls": 3, "pending_calls": 1},
            "search": {"cost_usd": 0.13, "priced_calls": 10, "unpriced_calls": 0, "local_calls": 0, "pending_calls": 0},
            "computer": {"cost_usd": 0.32, "priced_calls": 4, "unpriced_calls": 0, "local_calls": 0, "pending_calls": 0}
          },
          "entitlement": {
            "tier": "standard",
            "pending_tier": "byok",
            "status": "active",
            "billing_provider": "manual",
            "period_start": "2026-11-01T00:00:00Z",
            "period_end": "2026-12-01T00:00:00Z",
            "grace_until": null,
            "suspended": false,
            "suspended_reason": "",
            "allow_byok": true,
            "credit_grant_usd": 10.0,
            "credit_used_usd": 3.5,
            "credit_pending_usd": 0.25,
            "credit_remaining_usd": 6.25,
            "byok_max_usd_per_day": 15.0,
            "storage_used_bytes": 1048576,
            "storage_limit_bytes": 524288000
          },
          "models": [],
          "buckets": []
        }
        """

        let report = try JSONDecoder().decode(APICostResponse.self, from: Data(json.utf8))
        let byBilling = try #require(report.byBilling)
        #expect(byBilling.hosted.costUSD == 3.50)
        #expect(byBilling.hosted.pendingCalls == 1)
        #expect(byBilling.byok.costUSD == 0.75)
        #expect(byBilling.local.localCalls == 3)
        #expect(byBilling.totalCalls == 22)

        let byCategory = try #require(report.byCategory)
        #expect(byCategory.llm.costUSD == 3.80)
        #expect(byCategory.search.costUSD == 0.13)
        #expect(byCategory.computer.costUSD == 0.32)
        #expect(byCategory.totalCalls == 32)

        let entitlement = try #require(report.entitlement)
        #expect(entitlement.tier == "standard")
        #expect(entitlement.tierDisplayName == "Standard")
        #expect(entitlement.pendingTier == "byok")
        #expect(entitlement.pendingTierDisplayName == "BYOK")
        #expect(entitlement.status == "active")
        #expect(entitlement.billingProvider == "manual")
        #expect(entitlement.periodStart == "2026-11-01T00:00:00Z")
        #expect(entitlement.periodEnd == "2026-12-01T00:00:00Z")
        #expect(entitlement.graceUntil == nil)
        #expect(entitlement.suspended == false)
        #expect(entitlement.allowBYOK == true)
        #expect(entitlement.creditGrantUSD == 10.0)
        #expect(entitlement.creditUsedUSD == 3.5)
        #expect(entitlement.creditPendingUSD == 0.25)
        #expect(entitlement.creditRemainingUSD == 6.25)
        #expect(entitlement.byokMaxUSDPerDay == 15.0)
        #expect(entitlement.storageUsedBytes == 1_048_576)
        #expect(entitlement.storageLimitBytes == 524_288_000)
        #expect(!entitlement.isInactive)
        #expect(!entitlement.isCreditsExhausted)
        #expect(abs(entitlement.creditsUsedRatio - 0.375) < 0.0001)
    }

    @Test("Decodes Capabilities entitlement and service_keys summaries")
    func decodesCapabilitiesEntitlementAndServiceKeys() throws {
        let json = """
        {
          "contract": "nori-chat-2",
          "service": "nori",
          "conversationModes": ["assistant"],
          "features": {"backgroundTasks": true},
          "entitlement": {
            "tier": "trial",
            "status": "active",
            "allow_byok": false,
            "suspended": false,
            "credit_remaining_usd": 0.0,
            "credit_grant_usd": 5.0,
            "storage_used_bytes": 1024,
            "storage_limit_bytes": 104857600,
            "max_routines": 3,
            "max_concurrent_tasks": 2,
            "max_desktop_ws": 1,
            "max_checkpoints": 1
          },
          "service_keys": {
            "exa_configured": true,
            "platform_exa_available": true
          }
        }
        """

        let caps = try JSONDecoder().decode(Capabilities.self, from: Data(json.utf8))
        let ent = try #require(caps.entitlement)
        #expect(ent.tier == "trial")
        #expect(ent.tierDisplayName == "Trial")
        #expect(ent.allowBYOK == false)
        #expect(ent.isTrialCreditsExhausted == true)
        #expect(ent.maxRoutines == 3)
        #expect(ent.maxConcurrentTasks == 2)
        #expect(ent.maxDesktopWs == 1)
        #expect(ent.maxCheckpoints == 1)

        let serviceKeys = try #require(caps.serviceKeys)
        #expect(serviceKeys.exaConfigured == true)
        #expect(serviceKeys.platformExaAvailable == true)
    }
}
