# Postmortem: INC-4419 — Multi-Region Redis Sentinel Split-Brain & PgBouncer Connection Storm

- **Incident ID**: `INC-4419`
- **Severity**: SEV-1 (47 minutes total customer-facing degradation)
- **Date / Window**: Saturday 2 January 2027, 03:14 – 04:01 PST (`us-west-2` primary cluster)
- **Incident Commander**: Elena Rostova (Staff SRE)
- **Engineering Manager**: Marcus Vance (Platform Engineering)
- **Executive Sponsor**: Dana Chen (VP of Infrastructure)
- **Impacted Enterprise Tenants**: Northstar Logistics, Alder Health, Puget FinTech

## 1. Executive Summary
A routine kernel security patch on `redis-sentinel-usw2-02` triggered an asymmetric network partition caused by stale security-group rules drifted out of Terraform (later traced to a legacy Core Networking module). Two Sentinel nodes briefly failed to reach quorum while cache-miss retries stampeded through PgBouncer into the primary Postgres 17 cluster (`pg-prod-usw2-01`). API p99 latency spiked from 48 ms to 14.2 seconds, and 18.4% of checkout/ledger writes returned HTTP 503 before Elena Rostova manually fenced the stale Redis primary and throttled PgBouncer pool saturation.

## 2. Detailed Timeline (PST)
- **03:14**: Automated host reboot on `redis-sentinel-usw2-02`; Sentinel quorum temporarily dropped from 3/3 to 1/3 due to asymmetric SG drift.
- **03:18**: Application pods began failing Redis session lookups and falling back synchronously to Postgres (`pg-prod-usw2-01`).
- **03:21**: PgBouncer `default_pool_size=200` saturated; waiting client queue crossed 4,800 connections; p99 latency hit 14.2s.
- **03:24**: PagerDuty SEV-1 triggered; Elena Rostova assumed Incident Command, Devon Brooks and Mei-Ling Zhou joined bridge.
- **03:36**: Mei-Ling enabled emergency PgBouncer query timeout (3s) and shed non-critical batch workers.
- **03:49**: Elena fenced stale Redis master `redis-usw2-01a` and promoted replica `redis-usw2-01c` with clean Sentinel epoch.
- **04:01**: Error rate < 0.05%, p99 back to 52 ms; incident declared mitigated.

## 3. Root Causes & Contributing Factors
1. **Unmonitored Terraform drift** in legacy Core Networking security group allowed TCP 26379 inbound on 2 of 3 Sentinel hosts only.
2. **Missing Sentinel quorum pre-flight alert** before rolling node reboots (`INFRA-8109`).
3. **Unbounded PgBouncer fallback storm**: no circuit breaker between cache-miss surge and Postgres connection pools (`INFRA-8102`).
4. **Stale regional failover runbook** hadn't been exercised in a live game-day since Q2 2026 (`INFRA-8114`).

## 4. Action Items
| Ticket | Priority | Owner | Summary | Target |
| :--- | :--- | :--- | :--- | :--- |
| `INFRA-8102` | P1 | Mei-Ling Zhou (pair: Tariq Al-Mansoor) | Implement PgBouncer pool cap & Redis-fallback circuit breaker | 2027-01-22 |
| `INFRA-8109` | P1 | Devon Brooks | Add Prometheus Sentinel quorum alert + Terraform drift gate | 2027-01-20 |
| `INFRA-8114` | P2 | Owen Gallagher | Update & run regional Redis/Postgres failover game-day runbook | 2027-02-05 |
