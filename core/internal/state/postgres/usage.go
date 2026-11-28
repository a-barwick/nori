package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"nori/internal/billing"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

var (
	_ docstore.AtomicBillingReserver       = (*backend)(nil)
	_ docstore.UsageFinalizationReconciler = (*backend)(nil)
	_ docstore.TrialProvisioner            = (*transaction)(nil)
	_ users.TrialCapSetter                 = (*backend)(nil)
)

var (
	trialCapMu sync.RWMutex
	trialCaps  = map[*backend]int{}
)

func (b *backend) SetTrialMaxSignupsPerDay(maxPerDay int) {
	if b == nil {
		return
	}
	trialCapMu.Lock()
	if maxPerDay <= 0 {
		delete(trialCaps, b)
	} else {
		trialCaps[b] = maxPerDay
	}
	trialCapMu.Unlock()
}

func (b *backend) trialMaxSignupsPerDay() int {
	if b == nil {
		return 0
	}
	trialCapMu.RLock()
	v := trialCaps[b]
	trialCapMu.RUnlock()
	return v
}

const usageWindowSQL = `
SELECT count(*) FILTER (WHERE idx_created_at >= $3::jsonb AND COALESCE(data->>'category', 'llm') IN ('', 'llm')),
       coalesce(sum(cost) FILTER (WHERE idx_created_at >= $4::jsonb AND cost > 0), 0),
       coalesce(sum(coalesce(nullif(exposure, 0), $5)) FILTER (WHERE idx_created_at >= $4::jsonb AND unknown), 0),
       count(*) FILTER (WHERE idx_created_at >= $4::jsonb AND unknown)
FROM (
	SELECT idx_created_at,
	       data,
	       (data->>'cost_usd')::double precision AS cost,
	       greatest((data->>'estimated_exposure_usd')::double precision, 0) AS exposure,
	       coalesce(data->>'cost_status','') NOT IN ('provider', 'calculated', 'local')
	         AND coalesce((data->>'cost_usd')::double precision, 0) <= 0 AS unknown
	FROM nori_records
	WHERE shard = $1 AND collection = $2 AND idx_created_at IS NOT NULL AND idx_created_at >= least($3::jsonb, $4::jsonb)
	  AND data->>'request_status' IS DISTINCT FROM 'not_sent'
) usage
`

func readUsageWindow(ctx context.Context, q executor, collection string, hour, day time.Time, unknown float64) (docstore.UsageWindow, error) {
	hourValue, err := json.Marshal(docstore.JSONValue(hour))
	if err != nil {
		return docstore.UsageWindow{}, err
	}
	dayValue, err := json.Marshal(docstore.JSONValue(day))
	if err != nil {
		return docstore.UsageWindow{}, err
	}
	var window docstore.UsageWindow
	err = q.QueryRow(ctx, usageWindowSQL, recordShard(collection), collection, hourValue, dayValue, unknown).Scan(
		&window.Calls, &window.ActualUSD, &window.ExposureUSD, &window.Unknown)
	return window, err
}

func (b *backend) ReadUsageWindow(ctx context.Context, collection string, hour, day time.Time, unknown float64) (docstore.UsageWindow, error) {
	return readUsageWindow(ctx, b.pool, collection, hour, day, unknown)
}

func (t *transaction) ReadUsageWindow(ctx context.Context, collection string, hour, day time.Time, unknown float64) (docstore.UsageWindow, error) {
	if err := t.ensureTx(ctx, recordShard(collection)); err != nil {
		return docstore.UsageWindow{}, err
	}
	return readUsageWindow(ctx, t.q, collection, hour, day, unknown)
}

func (t *transaction) ProvisionTrial(ctx context.Context, userID string, now time.Time, maxTrialsPerDay int) error {
	if err := t.ensureTx(ctx, usersCollection); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if maxTrialsPerDay <= 0 && t.b != nil {
		maxTrialsPerDay = t.b.trialMaxSignupsPerDay()
	}
	_, err := billing.ProvisionTrialTx(ctx, t.tx, userID, now, maxTrialsPerDay)
	return err
}

const usageCostsSQL = `
SELECT idx_created_at #>> '{}', coalesce(data->>'model', ''),
       coalesce(data->>'billing', ''), coalesce(data->>'category', ''),
       (data->>'cost_usd')::double precision, coalesce(data->>'cost_status', ''),
       coalesce(data->>'request_status', '')
FROM nori_records
WHERE shard = $1 AND collection = $2
  AND idx_created_at IS NOT NULL
  AND idx_created_at >= to_jsonb($3::text) AND idx_created_at < to_jsonb($4::text)
  AND data->>'request_status' IS DISTINCT FROM 'not_sent'
`

func (b *backend) ReadUsageCosts(ctx context.Context, collection string, start, end time.Time, visit func(docstore.UsageCostRecord)) error {
	rows, err := b.pool.Query(ctx, usageCostsSQL, recordShard(collection), collection, docstore.JSONValue(start), docstore.JSONValue(end))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record docstore.UsageCostRecord
		var createdAt string
		if err := rows.Scan(
			&createdAt, &record.Model, &record.Billing, &record.Category,
			&record.CostUSD, &record.CostStatus, &record.RequestStatus,
		); err != nil {
			return err
		}
		record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return fmt.Errorf("decode model usage timestamp: %w", err)
		}
		visit(record)
	}
	return rows.Err()
}

func effectiveUsageBilling(rawBilling, costStatus string) string {
	switch rawBilling {
	case "hosted", "byok", "local":
		return rawBilling
	default:
		if costStatus == "local" {
			return "local"
		}
		// Post-016 empty billing fails closed as hosted.
		return "hosted"
	}
}

func effectiveUsageCategory(rawCategory string) string {
	switch rawCategory {
	case "llm", "search", "computer":
		return rawCategory
	default:
		return "llm"
	}
}

func hostedBudgetScopeForCategory(category string) billing.BudgetScope {
	switch category {
	case "search":
		return billing.ScopeHostedExa
	case "computer":
		return billing.ScopeHostedSprites
	default:
		return billing.ScopeHostedLLM
	}
}

// ReserveUsageWithBilling atomically locks the user's entitlement row FOR UPDATE,
// checks suspension/status/lazy-rollover, enforces hosted credit ledger + fleet daily
// budget caps (or BYOK 24h safety rail and trial BYOK block) + LLM-only hourly call limit,
// and inserts the pending usage record in the SAME transaction.
func (b *backend) ReserveUsageWithBilling(ctx context.Context, req docstore.UsageReserveRequest) (docstore.UsageReserveResult, error) {
	now := req.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	shard := recordShard(req.Collection)

	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return docstore.UsageReserveResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if ownerID := users.ID(ctx); ownerID != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_owner', $1, true)", ownerID); err != nil {
			return docstore.UsageReserveResult{}, err
		}
	}

	// Step 1: Lock user entitlement row FOR UPDATE to serialize concurrent reservations for this user.
	ent, err := billing.SelectEntitlementForUpdateTx(ctx, tx, req.UserID, now)
	if errors.Is(err, billing.ErrEntitlementMissing) {
		if !req.EnforceEntitlement {
			return docstore.UsageReserveResult{UsedEntitlement: false}, nil
		}
		return docstore.UsageReserveResult{UsedEntitlement: true}, billing.ErrEntitlementMissing
	}
	if err != nil {
		return docstore.UsageReserveResult{}, err
	}

	// Step 2: Check suspension (403) and billing status (402).
	if err := ent.CheckAccess(now); err != nil {
		return docstore.UsageReserveResult{UsedEntitlement: true}, err
	}

	// Idempotency: if this usage ID already exists, admit without double-charging.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM nori_records WHERE shard = $1 AND collection = $2 AND id = $3)`,
		shard, req.Collection, req.UsageID,
	).Scan(&exists); err != nil {
		return docstore.UsageReserveResult{}, err
	}
	if exists {
		if err := tx.Commit(ctx); err != nil {
			return docstore.UsageReserveResult{}, err
		}
		return docstore.UsageReserveResult{
			Allowed:             true,
			UsedEntitlement:     true,
			ReservedExposureUSD: req.EstimatedExposureUSD,
			EffectiveBilling:    effectiveUsageBilling(req.Billing, req.CostStatus),
			EffectiveCategory:   effectiveUsageCategory(req.Category),
		}, nil
	}

	effBilling := effectiveUsageBilling(req.Billing, req.CostStatus)
	effCategory := effectiveUsageCategory(req.Category)
	policy := ent.EffectivePolicy()

	incomingExposure := req.EstimatedExposureUSD
	if effBilling == "local" || req.CostStatus == "local" {
		incomingExposure = 0
	} else if incomingExposure <= 0 {
		incomingExposure = req.UnknownEstimateUSD
	}

	// Step 3: Rolling 1h LLM-only call cap (excludes search & computer).
	if effCategory == "llm" && policy.MaxCallsPerHour > 0 {
		var llmCalls int
		hourRFC := req.HourCutoff.UTC().Format(time.RFC3339Nano)
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM nori_records
			WHERE shard = $1
			  AND collection = $2
			  AND idx_created_at IS NOT NULL
			  AND idx_created_at >= to_jsonb($3::text)
			  AND COALESCE(data->>'request_status', '') <> 'not_sent'
			  AND COALESCE(data->>'category', 'llm') IN ('', 'llm')`,
			shard, req.Collection, hourRFC,
		).Scan(&llmCalls); err != nil {
			return docstore.UsageReserveResult{}, err
		}
		if llmCalls+1 > policy.MaxCallsPerHour {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, nil
		}
	}

	// Step 4: Billing-specific admission checks.
	switch effBilling {
	case "hosted":
		bal, err := billing.ComputeHostedCreditBalanceTx(ctx, tx, ent, req.DataOwner, now)
		if err != nil {
			return docstore.UsageReserveResult{}, err
		}
		incomingMicros := billing.USDToMicrosCeil(incomingExposure)
		if bal.AvailableMicros <= 0 || bal.AvailableMicros < incomingMicros {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, billing.ErrCreditExhausted
		}
		// Atomically check & increment fleet-wide daily hosted budget.
		if err := billing.CheckAndIncrementGlobalBudgetTx(
			ctx, tx, now, billing.ScopeHostedAll, incomingMicros, req.HostedAllMaxMicrosPerDay, 1, 0,
		); err != nil {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, err
		}
		catScope := hostedBudgetScopeForCategory(effCategory)
		var catCap int64
		if catScope == billing.ScopeHostedExa {
			catCap = req.HostedExaMaxMicrosPerDay
		}
		if err := billing.CheckAndIncrementGlobalBudgetTx(
			ctx, tx, now, catScope, incomingMicros, catCap, 1, 0,
		); err != nil {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, err
		}

	case "byok":
		if !policy.AllowBYOK {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, billing.ErrBYOKNotAllowed
		}
		if policy.MaxBYOKMicrosPerDay > 0 {
			dayRFC := req.DayCutoff.UTC().Format(time.RFC3339Nano)
			fallbackUSD := req.UnknownEstimateUSD
			if fallbackUSD <= 0 {
				fallbackUSD = billing.DefaultUnknownExposureUSD
			}
			var byokUsedMicros int64
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(SUM(
					CASE
						WHEN (data->>'cost_usd')::double precision > 0
							THEN ROUND(((data->>'cost_usd')::double precision) * 1000000)::bigint
						WHEN COALESCE(data->>'request_status', '') IN ('pending', 'stale_aborted')
							THEN CEIL(GREATEST(COALESCE(NULLIF((data->>'estimated_exposure_usd')::double precision, 0), $4), 0) * 1000000)::bigint
						WHEN COALESCE(data->>'cost_status', '') NOT IN ('provider', 'calculated', 'local')
							THEN CEIL(GREATEST(COALESCE(NULLIF((data->>'estimated_exposure_usd')::double precision, 0), $4), 0) * 1000000)::bigint
						ELSE 0
					END
				), 0)
				FROM nori_records
				WHERE shard = $1
				  AND collection = $2
				  AND idx_created_at IS NOT NULL
				  AND idx_created_at >= to_jsonb($3::text)
				  AND COALESCE(data->>'request_status', '') <> 'not_sent'
				  AND data->>'billing' = 'byok'`,
				shard, req.Collection, dayRFC, fallbackUSD,
			).Scan(&byokUsedMicros); err != nil {
				return docstore.UsageReserveResult{}, err
			}
			incomingMicros := billing.USDToMicrosCeil(incomingExposure)
			if byokUsedMicros+incomingMicros > policy.MaxBYOKMicrosPerDay {
				return docstore.UsageReserveResult{
					Allowed:           false,
					UsedEntitlement:   true,
					EffectiveBilling:  effBilling,
					EffectiveCategory: effCategory,
				}, nil
			}
		}

	case "local":
		if !policy.AllowHomeAI {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, billing.ErrBYOKNotAllowed
		}
	}

	// Step 5: Persist usage document in the SAME transaction.
	fields := make(map[string]any, len(req.UsageFields)+3)
	for k, v := range req.UsageFields {
		fields[k] = v
	}
	fields["billing"] = effBilling
	fields["category"] = effCategory
	fields["estimated_exposure_usd"] = incomingExposure

	raw, err := json.Marshal(docstore.JSONValue(fields))
	if err != nil {
		return docstore.UsageReserveResult{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO nori_records(shard, collection, id, data) VALUES ($1, $2, $3, $4::jsonb)
		 ON CONFLICT (shard, collection, id) DO UPDATE SET data = EXCLUDED.data`,
		shard, req.Collection, req.UsageID, raw,
	); err != nil {
		return docstore.UsageReserveResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return docstore.UsageReserveResult{}, err
	}
	return docstore.UsageReserveResult{
		Allowed:             true,
		UsedEntitlement:     true,
		ReservedExposureUSD: incomingExposure,
		EffectiveBilling:    effBilling,
		EffectiveCategory:   effCategory,
	}, nil
}

// RecordAndReconcileUsage writes the usage document and, when finalizing a previously pending
// hosted usage record, reconciles nori_global_budget_daily by (actual - reserved).
func (b *backend) RecordAndReconcileUsage(ctx context.Context, req docstore.UsageReconcileRequest) error {
	shard := recordShard(req.Collection)
	raw, err := json.Marshal(docstore.JSONValue(req.UsageFields))
	if err != nil {
		return err
	}

	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if ownerID := users.ID(ctx); ownerID != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_owner', $1, true)", ownerID); err != nil {
			return err
		}
	}

	var prevRaw []byte
	err = tx.QueryRow(ctx,
		`SELECT data FROM nori_records WHERE shard = $1 AND collection = $2 AND id = $3 FOR UPDATE`,
		shard, req.Collection, req.UsageID,
	).Scan(&prevRaw)
	hasPrev := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO nori_records(shard, collection, id, data) VALUES ($1, $2, $3, $4::jsonb)
		 ON CONFLICT (shard, collection, id) DO UPDATE SET data = EXCLUDED.data`,
		shard, req.Collection, req.UsageID, raw,
	); err != nil {
		return err
	}

	if hasPrev && req.RequestStatus != "pending" {
		var prev struct {
			Billing              string    `json:"billing"`
			Category             string    `json:"category"`
			CostStatus           string    `json:"cost_status"`
			RequestStatus        string    `json:"request_status"`
			EstimatedExposureUSD float64   `json:"estimated_exposure_usd"`
			CreatedAt            time.Time `json:"created_at"`
		}
		if json.Unmarshal(prevRaw, &prev) == nil && prev.RequestStatus == "pending" {
			effBilling := effectiveUsageBilling(prev.Billing, prev.CostStatus)
			if effBilling == "hosted" {
				prevMicros := billing.USDToMicrosCeil(prev.EstimatedExposureUSD)
				var newMicros int64
				switch {
				case req.RequestStatus == "not_sent":
					newMicros = 0
				case req.RequestStatus == "stale_aborted":
					// Aborted/stale calls retain their reserved exposure (no refund).
					newMicros = prevMicros
				case req.CostUSD != nil && *req.CostUSD >= 0:
					newMicros = billing.USDToMicros(*req.CostUSD)
				default:
					newMicros = prevMicros
				}
				if delta := newMicros - prevMicros; delta != 0 {
					day := prev.CreatedAt
					if day.IsZero() {
						day = req.CreatedAt
					}
					if day.IsZero() {
						day = time.Now().UTC()
					}
					cat := effectiveUsageCategory(prev.Category)
					if err := billing.ReconcileGlobalBudgetTx(ctx, tx, day, billing.ScopeHostedAll, delta); err != nil {
						return err
					}
					if err := billing.ReconcileGlobalBudgetTx(ctx, tx, day, hostedBudgetScopeForCategory(cat), delta); err != nil {
						return err
					}
				}
			}
		}
	} else if !hasPrev && req.RequestStatus != "pending" && req.RequestStatus != "not_sent" {
		// Direct-final hosted usage written without a prior pending reservation (e.g. Sprites janitor
		// metering or direct Exa writes) must still account against nori_global_budget_daily.
		effBilling := effectiveUsageBilling(req.Billing, req.CostStatus)
		if effBilling == "hosted" {
			var actualMicros int64
			switch {
			case req.CostUSD != nil && *req.CostUSD > 0:
				actualMicros = billing.USDToMicros(*req.CostUSD)
			case req.CostStatus != "provider" && req.CostStatus != "calculated" && req.CostStatus != "local":
				if req.EstimatedExposureUSD > 0 {
					actualMicros = billing.USDToMicrosCeil(req.EstimatedExposureUSD)
				} else {
					actualMicros = billing.DefaultUnknownExposureMicros
				}
			}
			if actualMicros > 0 {
				day := req.CreatedAt
				if day.IsZero() {
					day = time.Now().UTC()
				}
				cat := effectiveUsageCategory(req.Category)
				if err := billing.CheckAndIncrementGlobalBudgetTx(ctx, tx, day, billing.ScopeHostedAll, actualMicros, 0, 1, 0); err != nil {
					return err
				}
				if err := billing.CheckAndIncrementGlobalBudgetTx(ctx, tx, day, hostedBudgetScopeForCategory(cat), actualMicros, 0, 1, 0); err != nil {
					return err
				}
			}
		}
	}

	return tx.Commit(ctx)
}

// ReapStalePendingUsage marks unfinalized pending usage rows older than staleBefore
// as request_status = 'stale_aborted' so abandoned reservations are durably cleaned up.
func (b *backend) ReapStalePendingUsage(ctx context.Context, staleBefore time.Time) (int64, error) {
	cutoffRFC := staleBefore.UTC().Format(time.RFC3339Nano)
	tag, err := b.pool.Exec(ctx, `
		UPDATE nori_records
		SET data = jsonb_set(data, '{request_status}', '"stale_aborted"'::jsonb, true)
		WHERE collection LIKE 'owners/%/model_usage'
		  AND idx_created_at IS NOT NULL
		  AND idx_created_at < to_jsonb($1::text)
		  AND data->>'request_status' = 'pending'`,
		cutoffRFC,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
