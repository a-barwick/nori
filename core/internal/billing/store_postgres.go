package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Executor matches pgx.Tx and *pgxpool.Pool so entitlement helpers can run
// inside either standalone billing transactions or caller-owned transactions
// (such as atomic user creation and ReserveUsage).
type Executor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PostgresStore implements Store on Postgres via pgxpool.Pool.
// AllowSandbox defaults to false (production-safe: rejects sandbox provider events unless opted in).
type PostgresStore struct {
	pool         *pgxpool.Pool
	Now          func() time.Time
	AllowSandbox bool
}

var _ Store = (*PostgresStore)(nil)

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func scanEntitlement(row pgx.Row) (Entitlement, error) {
	var ent Entitlement
	var tier, status, provider string
	var pendingTier *string
	err := row.Scan(
		&ent.UserID,
		&tier,
		&pendingTier,
		&status,
		&provider,
		&ent.PeriodStart,
		&ent.PeriodEnd,
		&ent.GraceUntil,
		&ent.CreditOverrideMicros,
		&ent.AppAccountToken,
		&ent.SuspendedAt,
		&ent.SuspendedReason,
		&ent.ProviderUpdatedAt,
		&ent.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entitlement{}, ErrEntitlementMissing
	}
	if err != nil {
		return Entitlement{}, err
	}
	ent.Tier = Tier(tier)
	ent.Status = Status(status)
	ent.BillingProvider = Provider(provider)
	if pendingTier != nil && *pendingTier != "" {
		pt := Tier(*pendingTier)
		ent.PendingTier = &pt
	}
	return ent, nil
}

const selectEntitlementCols = `
	user_id, tier, pending_tier, status, billing_provider,
	period_start, period_end, grace_until, credit_override_micros,
	app_account_token::text, suspended_at, suspended_reason,
	provider_updated_at, updated_at
`

// SelectEntitlementForUpdateTx locks and loads a user's entitlement row and applies
// lazy manual monthly rollover (and deterministic grant insertion) within tx.
func SelectEntitlementForUpdateTx(ctx context.Context, q Executor, userID string, now time.Time) (Entitlement, error) {
	now = now.UTC()
	ent, err := scanEntitlement(q.QueryRow(ctx,
		`SELECT `+selectEntitlementCols+` FROM nori_user_entitlements WHERE user_id = $1 FOR UPDATE`,
		userID,
	))
	if err != nil {
		return Entitlement{}, err
	}
	updated, grant, changed := RolloverManualEntitlement(ent, now)
	if !changed {
		return ent, nil
	}
	var pendingStr *string
	if updated.PendingTier != nil {
		v := string(*updated.PendingTier)
		pendingStr = &v
	}
	if _, err := q.Exec(ctx, `
		UPDATE nori_user_entitlements
		SET tier = $2, pending_tier = $3, status = $4, period_start = $5, period_end = $6, updated_at = $7
		WHERE user_id = $1`,
		userID, string(updated.Tier), pendingStr, string(updated.Status), updated.PeriodStart, updated.PeriodEnd, now,
	); err != nil {
		return Entitlement{}, err
	}
	if grant != nil {
		if err := InsertCreditGrantTx(ctx, q, *grant); err != nil {
			return Entitlement{}, err
		}
	}
	return updated, nil
}

// InsertCreditGrantTx idempotently inserts a credit grant row (ON CONFLICT (id) DO NOTHING).
func InsertCreditGrantTx(ctx context.Context, q Executor, grant CreditGrant) error {
	provider := grant.SourceProvider
	if provider == "" {
		provider = ProviderManual
	}
	createdAt := grant.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	validFrom := grant.ValidFrom
	if validFrom.IsZero() {
		validFrom = createdAt
	}
	_, err := q.Exec(ctx, `
		INSERT INTO nori_credit_grants
			(id, user_id, kind, amount_micros, valid_from, valid_until, source_provider, source_event_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO NOTHING`,
		grant.ID, grant.UserID, string(grant.Kind), grant.AmountMicros,
		validFrom, grant.ValidUntil, string(provider), grant.SourceEventID, createdAt,
	)
	return err
}

// ProvisionTrialTx atomically provisions a new user's trial entitlement, one-time $5.00 credit grant,
// and daily trials_created counter check within the caller's transaction.
func ProvisionTrialTx(ctx context.Context, q Executor, userID string, now time.Time, maxTrialsPerDay int) (Entitlement, error) {
	now = now.UTC()
	// Check if entitlement already exists (idempotent on retry).
	existing, err := scanEntitlement(q.QueryRow(ctx,
		`SELECT `+selectEntitlementCols+` FROM nori_user_entitlements WHERE user_id = $1`,
		userID,
	))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrEntitlementMissing) {
		return Entitlement{}, err
	}

	if err := CheckAndIncrementGlobalBudgetTx(ctx, q, now, ScopeTrialsCreated, 0, 0, 1, maxTrialsPerDay); err != nil {
		return Entitlement{}, err
	}

	ent, err := scanEntitlement(q.QueryRow(ctx, `
		INSERT INTO nori_user_entitlements
			(user_id, tier, status, billing_provider, period_start, provider_updated_at, updated_at)
		VALUES ($1, 'trial', 'active', 'manual', $2, to_timestamp(0), $2)
		ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
		RETURNING `+selectEntitlementCols,
		userID, now,
	))
	if err != nil {
		return Entitlement{}, err
	}

	grant := CreditGrant{
		ID:             TrialGrantID(userID),
		UserID:         userID,
		Kind:           GrantTrial,
		AmountMicros:   MustPolicy(TierTrial).HostedCreditMicros,
		ValidFrom:      now,
		SourceProvider: ProviderManual,
		CreatedAt:      now,
	}
	if err := InsertCreditGrantTx(ctx, q, grant); err != nil {
		return Entitlement{}, err
	}
	return ent, nil
}

// CheckAndIncrementGlobalBudgetTx atomically increments nori_global_budget_daily for (day, scope)
// and rejects with ErrGlobalBudgetExceeded / ErrTrialLimitExceeded if the cap is exceeded.
// Because the check runs inside the caller's transaction, returning an error rolls back the increment.
func CheckAndIncrementGlobalBudgetTx(
	ctx context.Context,
	q Executor,
	day time.Time,
	scope BudgetScope,
	deltaMicros, maxMicros int64,
	deltaCount, maxCount int,
) error {
	if !ValidBudgetScope(scope) {
		return fmt.Errorf("%w: invalid budget scope %q", ErrInvalidChange, scope)
	}
	dayStr := day.UTC().Format("2006-01-02")
	var spentMicros int64
	var count int
	err := q.QueryRow(ctx, `
		INSERT INTO nori_global_budget_daily (day, scope, spent_micros, count)
		VALUES ($1::date, $2, $3, $4)
		ON CONFLICT (day, scope) DO UPDATE
		SET spent_micros = nori_global_budget_daily.spent_micros + EXCLUDED.spent_micros,
		    count = nori_global_budget_daily.count + EXCLUDED.count
		RETURNING spent_micros, count`,
		dayStr, string(scope), deltaMicros, deltaCount,
	).Scan(&spentMicros, &count)
	if err != nil {
		return err
	}
	if maxMicros > 0 && spentMicros > maxMicros {
		return ErrGlobalBudgetExceeded
	}
	if maxCount > 0 && count > maxCount {
		if scope == ScopeTrialsCreated {
			return ErrTrialLimitExceeded
		}
		return ErrGlobalBudgetExceeded
	}
	return nil
}

// ReconcileGlobalBudgetTx adjusts spent_micros by deltaMicros (actual - reserved), clamped at 0.
func ReconcileGlobalBudgetTx(ctx context.Context, q Executor, day time.Time, scope BudgetScope, deltaMicros int64) error {
	if !ValidBudgetScope(scope) || deltaMicros == 0 {
		return nil
	}
	dayStr := day.UTC().Format("2006-01-02")
	_, err := q.Exec(ctx, `
		INSERT INTO nori_global_budget_daily (day, scope, spent_micros, count)
		VALUES ($1::date, $2, GREATEST($3::bigint, 0), 0)
		ON CONFLICT (day, scope) DO UPDATE
		SET spent_micros = GREATEST(nori_global_budget_daily.spent_micros + $3::bigint, 0)`,
		dayStr, string(scope), deltaMicros,
	)
	return err
}

// ComputeHostedCreditBalanceTx computes a user's exact micro-USD hosted credit balance inside tx.
// Usage is read from nori_records for the user's owner collection (owners/<dataOwner>/model_usage).
// Only request_status == "not_sent" is free; stale pending (>15m), stale_aborted, and unpriced
// non-pending rows are charged at CEIL(estimated_exposure_usd) (falling back to DefaultUnknownExposureMicros).
// Each usage row is deterministically assigned to at most ONE expired positive grant ordered by
// (valid_until ASC, id ASC) in a single SQL pass so overlapping expired windows never double-absorb.
func ComputeHostedCreditBalanceTx(ctx context.Context, q Executor, ent Entitlement, dataOwner string, now time.Time) (CreditBalance, error) {
	now = now.UTC()
	staleCutoff := now.Add(-StalePendingTTL)
	if dataOwner == "" {
		dataOwner = ent.UserID
	}
	shard := "owners/" + dataOwner
	collection := shard + "/model_usage"

	var activeGranted int64
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_micros), 0)
		FROM nori_credit_grants
		WHERE user_id = $1
		  AND valid_from <= $2
		  AND (valid_until IS NULL OR valid_until > $2)`,
		ent.UserID, now,
	).Scan(&activeGranted); err != nil {
		return CreditBalance{}, err
	}

	var totalSpentMicros, totalPendingMicros, absorbedByExpired int64
	staleCutoffRFC := staleCutoff.UTC().Format(time.RFC3339Nano)
	err := q.QueryRow(ctx, `
		WITH expired_grants AS (
			SELECT id, valid_from, valid_until, amount_micros
			FROM nori_credit_grants
			WHERE user_id = $1
			  AND valid_until IS NOT NULL
			  AND valid_until <= $4
			  AND valid_until > valid_from
			  AND amount_micros > 0
		),
		hosted_usage AS (
			SELECT
				(idx_created_at #>> '{}')::timestamptz AS ts,
				CASE
					WHEN (data->>'cost_usd')::double precision > 0
						THEN ROUND(((data->>'cost_usd')::double precision) * 1000000)::bigint
					WHEN COALESCE(data->>'request_status', '') = 'pending'
					     AND idx_created_at < to_jsonb($5::text)
						THEN COALESCE(
							NULLIF(CEIL(GREATEST(COALESCE((data->>'estimated_exposure_usd')::double precision, 0), 0) * 1000000)::bigint, 0),
							$6::bigint
						)
					WHEN COALESCE(data->>'request_status', '') = 'stale_aborted'
						THEN COALESCE(
							NULLIF(CEIL(GREATEST(COALESCE((data->>'estimated_exposure_usd')::double precision, 0), 0) * 1000000)::bigint, 0),
							$6::bigint
						)
					WHEN COALESCE(data->>'request_status', '') <> 'pending'
					     AND COALESCE(data->>'cost_status', '') NOT IN ('provider', 'calculated', 'local')
						THEN COALESCE(
							NULLIF(CEIL(GREATEST(COALESCE((data->>'estimated_exposure_usd')::double precision, 0), 0) * 1000000)::bigint, 0),
							$6::bigint
						)
					ELSE 0
				END AS spent_micros,
				CASE
					WHEN COALESCE(data->>'request_status', '') = 'pending'
					     AND COALESCE((data->>'cost_usd')::double precision, 0) <= 0
					     AND idx_created_at >= to_jsonb($5::text)
						THEN COALESCE(
							NULLIF(CEIL(GREATEST(COALESCE((data->>'estimated_exposure_usd')::double precision, 0), 0) * 1000000)::bigint, 0),
							$6::bigint
						)
					ELSE 0
				END AS pending_micros
			FROM nori_records
			WHERE shard = $2
			  AND collection = $3
			  AND idx_created_at IS NOT NULL
			  AND COALESCE(data->>'request_status', '') <> 'not_sent'
			  AND (
					data->>'billing' = 'hosted'
					OR (COALESCE(data->>'billing', '') = '' AND COALESCE(data->>'cost_status', '') <> 'local')
			  )
		),
		assigned AS (
			SELECT
				u.spent_micros,
				u.pending_micros,
				g.id AS grant_id,
				g.amount_micros AS grant_cap
			FROM hosted_usage u
			LEFT JOIN LATERAL (
				SELECT eg.id, eg.amount_micros
				FROM expired_grants eg
				WHERE u.ts >= eg.valid_from AND u.ts < eg.valid_until
				ORDER BY eg.valid_until ASC, eg.id ASC
				LIMIT 1
			) g ON true
		),
		per_grant AS (
			SELECT grant_id, LEAST(MAX(grant_cap), SUM(spent_micros + pending_micros)) AS absorbed
			FROM assigned
			WHERE grant_id IS NOT NULL
			GROUP BY grant_id
		)
		SELECT
			(SELECT COALESCE(SUM(spent_micros), 0) FROM hosted_usage),
			(SELECT COALESCE(SUM(pending_micros), 0) FROM hosted_usage),
			(SELECT COALESCE(SUM(absorbed), 0) FROM per_grant)`,
		ent.UserID, shard, collection, now, staleCutoffRFC, DefaultUnknownExposureMicros,
	).Scan(&totalSpentMicros, &totalPendingMicros, &absorbedByExpired)
	if err != nil {
		return CreditBalance{}, err
	}

	netConsumed := (totalSpentMicros + totalPendingMicros) - absorbedByExpired
	if netConsumed < 0 {
		netConsumed = 0
	}
	netSpent := totalSpentMicros - absorbedByExpired
	if netSpent < 0 {
		netSpent = 0
	}

	return CreditBalance{
		GrantedMicros:   activeGranted,
		SpentMicros:     netSpent,
		PendingMicros:   totalPendingMicros,
		AvailableMicros: activeGranted - netConsumed,
		PeriodStart:     ent.PeriodStart,
		PeriodEnd:       ent.PeriodEnd,
	}, nil
}

// NotifyEntitlementChangedTx sends pg_notify('nori_wake', 'entitlement:' || user_id).
func NotifyEntitlementChangedTx(ctx context.Context, q Executor, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return nil
	}
	_, err := q.Exec(ctx, `SELECT pg_notify($1, $2)`, EntitlementNotifyChannel, EntitlementNotifyPayload(userID))
	return err
}

func (s *PostgresStore) GetEntitlement(ctx context.Context, userID string) (Entitlement, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Entitlement{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	ent, err := SelectEntitlementForUpdateTx(ctx, tx, userID, s.now())
	if err != nil {
		return Entitlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entitlement{}, err
	}
	return ent, nil
}

func (s *PostgresStore) ProvisionTrial(ctx context.Context, userID string, maxTrialsPerDay int) (Entitlement, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Entitlement{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	ent, err := ProvisionTrialTx(ctx, tx, userID, s.now(), maxTrialsPerDay)
	if err != nil {
		return Entitlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entitlement{}, err
	}
	return ent, nil
}

func (s *PostgresStore) SetTier(ctx context.Context, userID string, tier Tier, opts SetTierOptions) (Entitlement, error) {
	if !ValidTier(tier) {
		return Entitlement{}, ErrInvalidTier
	}
	if opts.Pending && !ValidPaidTier(tier) {
		return Entitlement{}, ErrInvalidTier
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Entitlement{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	now := s.now()
	ent, err := scanEntitlement(tx.QueryRow(ctx,
		`SELECT `+selectEntitlementCols+` FROM nori_user_entitlements WHERE user_id = $1 FOR UPDATE`,
		userID,
	))
	if errors.Is(err, ErrEntitlementMissing) {
		if opts.Pending {
			return Entitlement{}, ErrEntitlementMissing
		}
		pStart := now
		if opts.PeriodStart != nil {
			pStart = opts.PeriodStart.UTC()
		}
		var pEnd *time.Time
		if opts.PeriodEnd != nil {
			t := opts.PeriodEnd.UTC()
			pEnd = &t
		} else if tier != TierTrial {
			t := pStart.AddDate(0, 1, 0)
			pEnd = &t
		}
		status := opts.Status
		if status == "" {
			status = StatusActive
		}
		if !ValidStatus(status) {
			return Entitlement{}, ErrInvalidChange
		}
		ent, err = scanEntitlement(tx.QueryRow(ctx, `
			INSERT INTO nori_user_entitlements
				(user_id, tier, status, billing_provider, period_start, period_end, credit_override_micros, provider_updated_at, updated_at)
			VALUES ($1, $2, $3, 'manual', $4, $5, $6, to_timestamp(0), $7)
			RETURNING `+selectEntitlementCols,
			userID, string(tier), string(status), pStart, pEnd, opts.CreditOverrideMicros, now,
		))
		if err != nil {
			return Entitlement{}, err
		}
	} else if err != nil {
		return Entitlement{}, err
	} else if opts.Pending {
		pt := string(tier)
		ent, err = scanEntitlement(tx.QueryRow(ctx, `
			UPDATE nori_user_entitlements
			SET pending_tier = $2, updated_at = $3
			WHERE user_id = $1
			RETURNING `+selectEntitlementCols,
			userID, pt, now,
		))
		if err != nil {
			return Entitlement{}, err
		}
		if err := NotifyEntitlementChangedTx(ctx, tx, userID); err != nil {
			return Entitlement{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Entitlement{}, err
		}
		return ent, nil
	} else {
		pStart := now
		if opts.PeriodStart != nil {
			pStart = opts.PeriodStart.UTC()
		}
		var pEnd *time.Time
		if opts.PeriodEnd != nil {
			t := opts.PeriodEnd.UTC()
			pEnd = &t
		} else if tier != TierTrial {
			t := pStart.AddDate(0, 1, 0)
			pEnd = &t
		}
		status := opts.Status
		if status == "" {
			status = StatusActive
		}
		if !ValidStatus(status) {
			return Entitlement{}, ErrInvalidChange
		}
		creditOverride := ent.CreditOverrideMicros
		if opts.ClearCreditOverride {
			creditOverride = nil
		} else if opts.CreditOverrideMicros != nil {
			v := *opts.CreditOverrideMicros
			creditOverride = &v
		}
		// Preserve provider_updated_at on manual updates so subsequent provider events
		// are not falsely rejected as stale.
		ent, err = scanEntitlement(tx.QueryRow(ctx, `
			UPDATE nori_user_entitlements
			SET tier = $2, pending_tier = NULL, status = $3, billing_provider = 'manual',
			    period_start = $4, period_end = $5, credit_override_micros = $6,
			    updated_at = $7
			WHERE user_id = $1
			RETURNING `+selectEntitlementCols,
			userID, string(tier), string(status), pStart, pEnd, creditOverride, now,
		))
		if err != nil {
			return Entitlement{}, err
		}
	}

	// Close any prior active period grants at ent.PeriodStart so re-setting or upgrading a paid tier
	// never leaves overlapping active period grants that stack free credit.
	if _, err := tx.Exec(ctx, `
		UPDATE nori_credit_grants
		SET valid_until = GREATEST(valid_from, $2)
		WHERE user_id = $1
		  AND kind = 'period'
		  AND (valid_until IS NULL OR valid_until > $2)`,
		userID, ent.PeriodStart,
	); err != nil {
		return Entitlement{}, err
	}

	if !opts.SkipGrant {
		if tier == TierTrial {
			if amt := ent.EffectiveHostedCreditGrantMicros(); amt > 0 {
				if err := InsertCreditGrantTx(ctx, tx, CreditGrant{
					ID:             TrialGrantID(userID),
					UserID:         userID,
					Kind:           GrantTrial,
					AmountMicros:   amt,
					ValidFrom:      ent.PeriodStart,
					SourceProvider: ProviderManual,
					CreatedAt:      now,
				}); err != nil {
					return Entitlement{}, err
				}
			}
		} else if tier == TierStandard || tier == TierPower {
			if amt := ent.EffectiveHostedCreditGrantMicros(); amt > 0 {
				grantID := ManualPeriodGrantID(userID, ent.PeriodStart)
				var baseExists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nori_credit_grants WHERE id = $1)`, grantID).Scan(&baseExists); err != nil {
					return Entitlement{}, err
				}
				if baseExists {
					grantID = ManualSetTierGrantID(userID, tier, ent.PeriodStart)
				}
				if err := InsertCreditGrantTx(ctx, tx, CreditGrant{
					ID:             grantID,
					UserID:         userID,
					Kind:           GrantPeriod,
					AmountMicros:   amt,
					ValidFrom:      ent.PeriodStart,
					ValidUntil:     ent.PeriodEnd,
					SourceProvider: ProviderManual,
					CreatedAt:      now,
				}); err != nil {
					return Entitlement{}, err
				}
			}
		}
	}

	if err := NotifyEntitlementChangedTx(ctx, tx, userID); err != nil {
		return Entitlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entitlement{}, err
	}
	return ent, nil
}

func (s *PostgresStore) Suspend(ctx context.Context, userID, reason string) (Entitlement, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Entitlement{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	now := s.now()
	ent, err := scanEntitlement(tx.QueryRow(ctx, `
		UPDATE nori_user_entitlements
		SET suspended_at = $2, suspended_reason = $3, updated_at = $2
		WHERE user_id = $1
		RETURNING `+selectEntitlementCols,
		userID, now, strings.TrimSpace(reason),
	))
	if err != nil {
		return Entitlement{}, err
	}
	if err := NotifyEntitlementChangedTx(ctx, tx, userID); err != nil {
		return Entitlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entitlement{}, err
	}
	return ent, nil
}

func (s *PostgresStore) Unsuspend(ctx context.Context, userID string) (Entitlement, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Entitlement{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	now := s.now()
	ent, err := scanEntitlement(tx.QueryRow(ctx, `
		UPDATE nori_user_entitlements
		SET suspended_at = NULL, suspended_reason = '', updated_at = $2
		WHERE user_id = $1
		RETURNING `+selectEntitlementCols,
		userID, now,
	))
	if err != nil {
		return Entitlement{}, err
	}
	if err := NotifyEntitlementChangedTx(ctx, tx, userID); err != nil {
		return Entitlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entitlement{}, err
	}
	return ent, nil
}

func (s *PostgresStore) AddCreditGrant(ctx context.Context, grant CreditGrant) error {
	isExternal := grant.SourceProvider == ProviderStripe || grant.SourceProvider == ProviderAppleIAP
	if err := ValidateCreditGrant(grant, isExternal); err != nil {
		return err
	}
	return InsertCreditGrantTx(ctx, s.pool, grant)
}

func (s *PostgresStore) ListCreditGrants(ctx context.Context, userID string) ([]CreditGrant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, kind, amount_micros, valid_from, valid_until, source_provider, source_event_id, created_at
		FROM nori_credit_grants
		WHERE user_id = $1
		ORDER BY valid_from ASC, id ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CreditGrant{}
	for rows.Next() {
		var g CreditGrant
		var kind, provider string
		if err := rows.Scan(&g.ID, &g.UserID, &kind, &g.AmountMicros, &g.ValidFrom, &g.ValidUntil, &provider, &g.SourceEventID, &g.CreatedAt); err != nil {
			return nil, err
		}
		g.Kind = GrantKind(kind)
		g.SourceProvider = Provider(provider)
		out = append(out, g)
	}
	return out, rows.Err()
}

func resolveDataOwnerTx(ctx context.Context, q Executor, userID string) string {
	if userID != "owner" {
		return userID
	}
	var email string
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(data->>'email', ''), 'owner')
		FROM nori_records
		WHERE shard = 'users' AND collection = 'users' AND id = 'owner'
		LIMIT 1`,
	).Scan(&email); err == nil && email != "" {
		return email
	}
	return userID
}

func (s *PostgresStore) CreditBalance(ctx context.Context, userID string) (CreditBalance, error) {
	return s.CreditBalanceForOwner(ctx, userID, "")
}

// CreditBalanceForOwner computes hosted credit balance using an explicit dataOwner (matching users.User.DataOwner()).
// When dataOwner is empty, LegacyID ("owner") is resolved via nori_records.
func (s *PostgresStore) CreditBalanceForOwner(ctx context.Context, userID, dataOwner string) (CreditBalance, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CreditBalance{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	now := s.now()
	ent, err := SelectEntitlementForUpdateTx(ctx, tx, userID, now)
	if err != nil {
		return CreditBalance{}, err
	}
	if dataOwner == "" {
		dataOwner = resolveDataOwnerTx(ctx, tx, userID)
	}
	bal, err := ComputeHostedCreditBalanceTx(ctx, tx, ent, dataOwner, now)
	if err != nil {
		return CreditBalance{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreditBalance{}, err
	}
	return bal, nil
}

func (s *PostgresStore) ApplyEvent(ctx context.Context, change Change) (Entitlement, error) {
	if err := ValidateChange(change, !s.AllowSandbox); err != nil {
		return Entitlement{}, err
	}
	env := change.Environment
	if env == "" {
		env = EnvProduction
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Entitlement{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Resolve user by UserID or Apple AppAccountToken, locking the entitlement row.
	var ent Entitlement
	if uid := strings.TrimSpace(change.UserID); uid != "" {
		ent, err = scanEntitlement(tx.QueryRow(ctx,
			`SELECT `+selectEntitlementCols+` FROM nori_user_entitlements WHERE user_id = $1 FOR UPDATE`,
			uid,
		))
	} else {
		ent, err = scanEntitlement(tx.QueryRow(ctx,
			`SELECT `+selectEntitlementCols+` FROM nori_user_entitlements WHERE app_account_token = $1::uuid FOR UPDATE`,
			strings.TrimSpace(change.AppAccountToken),
		))
	}
	if err != nil {
		return Entitlement{}, err
	}
	userID := ent.UserID

	// Idempotency check on (provider, event_id).
	var alreadyApplied bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM nori_entitlement_events WHERE provider = $1 AND event_id = $2)`,
		string(change.Provider), change.EventID,
	).Scan(&alreadyApplied); err != nil {
		return Entitlement{}, err
	}
	if alreadyApplied {
		if err := tx.Commit(ctx); err != nil {
			return Entitlement{}, err
		}
		return ent, nil
	}

	now := s.now()
	if change.Subscription != nil {
		sub := change.Subscription
		// Reject stale out-of-order updates and cross-user subscription hijacks.
		var existingUserID string
		var existingUpdated time.Time
		err := tx.QueryRow(ctx, `
			SELECT user_id, provider_updated_at FROM nori_subscriptions
			WHERE provider = $1 AND environment = $2 AND external_subscription_id = $3
			FOR UPDATE`,
			string(change.Provider), string(env), sub.ExternalSubscriptionID,
		).Scan(&existingUserID, &existingUpdated)
		if err == nil {
			if existingUserID != userID {
				return Entitlement{}, ErrSubscriptionConflict
			}
			if change.ProviderUpdatedAt.UTC().Before(existingUpdated.UTC()) {
				return Entitlement{}, ErrStaleEvent
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Entitlement{}, err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO nori_subscriptions
				(user_id, provider, environment, external_customer_id, external_subscription_id,
				 external_product_id, tier, status, period_start, period_end, grace_until, provider_updated_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (provider, environment, external_subscription_id) DO UPDATE SET
				user_id = EXCLUDED.user_id,
				external_customer_id = EXCLUDED.external_customer_id,
				external_product_id = EXCLUDED.external_product_id,
				tier = EXCLUDED.tier,
				status = EXCLUDED.status,
				period_start = EXCLUDED.period_start,
				period_end = EXCLUDED.period_end,
				grace_until = EXCLUDED.grace_until,
				provider_updated_at = EXCLUDED.provider_updated_at,
				updated_at = EXCLUDED.updated_at`,
			userID, string(change.Provider), string(env), sub.ExternalCustomerID, sub.ExternalSubscriptionID,
			sub.ExternalProductID, string(sub.Tier), string(sub.Status),
			sub.PeriodStart.UTC(), sub.PeriodEnd, sub.GraceUntil, change.ProviderUpdatedAt.UTC(), now,
		); err != nil {
			return Entitlement{}, err
		}

		// Load all subscriptions for userID to apply multi-subscription precedence.
		subRows, err := tx.Query(ctx, `
			SELECT id::text, user_id, provider, environment, external_customer_id, external_subscription_id,
			       external_product_id, tier, status, period_start, period_end, grace_until, provider_updated_at, updated_at
			FROM nori_subscriptions
			WHERE user_id = $1`,
			userID,
		)
		if err != nil {
			return Entitlement{}, err
		}
		var allSubs []Subscription
		for subRows.Next() {
			var row Subscription
			var prov, envStr, tierStr, statusStr string
			if err := subRows.Scan(
				&row.ID, &row.UserID, &prov, &envStr, &row.ExternalCustomerID, &row.ExternalSubscriptionID,
				&row.ExternalProductID, &tierStr, &statusStr, &row.PeriodStart, &row.PeriodEnd, &row.GraceUntil,
				&row.ProviderUpdatedAt, &row.UpdatedAt,
			); err != nil {
				subRows.Close()
				return Entitlement{}, err
			}
			row.Provider = Provider(prov)
			row.Environment = Environment(envStr)
			row.Tier = Tier(tierStr)
			row.Status = Status(statusStr)
			allSubs = append(allSubs, row)
		}
		if err := subRows.Err(); err != nil {
			subRows.Close()
			return Entitlement{}, err
		}
		subRows.Close()

		if winner, ok := SelectWinningSubscription(allSubs, now); ok {
			pendingTier := ent.PendingTier
			if winner.Provider == change.Provider && winner.ExternalSubscriptionID == sub.ExternalSubscriptionID {
				pendingTier = sub.PendingTier
			}
			var pendingStr *string
			if pendingTier != nil {
				v := string(*pendingTier)
				pendingStr = &v
			}
			provUpdated := ent.ProviderUpdatedAt
			if winner.ProviderUpdatedAt.After(provUpdated) {
				provUpdated = winner.ProviderUpdatedAt
			}
			// NEVER touch suspended_at or suspended_reason here!
			ent, err = scanEntitlement(tx.QueryRow(ctx, `
				UPDATE nori_user_entitlements
				SET tier = $2, pending_tier = $3, status = $4, billing_provider = $5,
				    period_start = $6, period_end = $7, grace_until = $8,
				    provider_updated_at = $9, updated_at = $10
				WHERE user_id = $1
				RETURNING `+selectEntitlementCols,
				userID, string(winner.Tier), pendingStr, string(winner.Status), string(winner.Provider),
				winner.PeriodStart, winner.PeriodEnd, winner.GraceUntil, provUpdated, now,
			))
			if err != nil {
				return Entitlement{}, err
			}
		}
	} else {
		if change.ProviderUpdatedAt.UTC().Before(ent.ProviderUpdatedAt.UTC()) {
			return Entitlement{}, ErrStaleEvent
		}
		ent, err = scanEntitlement(tx.QueryRow(ctx, `
			UPDATE nori_user_entitlements
			SET provider_updated_at = $2, updated_at = $3
			WHERE user_id = $1
			RETURNING `+selectEntitlementCols,
			userID, change.ProviderUpdatedAt.UTC(), now,
		))
		if err != nil {
			return Entitlement{}, err
		}
	}

	if change.CreditDeltaMicros != 0 {
		kind := change.CreditGrantKind
		if kind == "" {
			if change.CreditDeltaMicros < 0 {
				kind = GrantClawback
			} else {
				kind = GrantPeriod
			}
		}
		validFrom := ent.PeriodStart
		if validFrom.IsZero() {
			validFrom = now
		}
		validUntil := change.CreditValidUntil
		if validUntil == nil && kind == GrantPeriod {
			validUntil = ent.PeriodEnd
		}
		if err := InsertCreditGrantTx(ctx, tx, CreditGrant{
			ID:             EventGrantID(change.Provider, change.EventID, change.CreditDeltaMicros),
			UserID:         userID,
			Kind:           kind,
			AmountMicros:   change.CreditDeltaMicros,
			ValidFrom:      validFrom,
			ValidUntil:     validUntil,
			SourceProvider: change.Provider,
			SourceEventID:  change.EventID,
			CreatedAt:      now,
		}); err != nil {
			return Entitlement{}, err
		}
	}

	payload := change.Payload
	if len(payload) == 0 || !json.Valid(payload) {
		payload = json.RawMessage(`{}`)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO nori_entitlement_events
			(provider, event_id, user_id, event_type, environment, provider_updated_at, payload, applied_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)`,
		string(change.Provider), change.EventID, userID, change.EventType, string(env),
		change.ProviderUpdatedAt.UTC(), []byte(payload), now,
	); err != nil {
		return Entitlement{}, err
	}

	if err := NotifyEntitlementChangedTx(ctx, tx, userID); err != nil {
		return Entitlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entitlement{}, err
	}
	return ent, nil
}

func (s *PostgresStore) CheckAndIncrementGlobalBudget(ctx context.Context, day time.Time, scope BudgetScope, deltaMicros, maxMicros int64, deltaCount, maxCount int) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := CheckAndIncrementGlobalBudgetTx(ctx, tx, day, scope, deltaMicros, maxMicros, deltaCount, maxCount); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ReconcileGlobalBudget(ctx context.Context, day time.Time, scope BudgetScope, deltaMicros int64) error {
	return ReconcileGlobalBudgetTx(ctx, s.pool, day, scope, deltaMicros)
}

func (s *PostgresStore) GetGlobalBudget(ctx context.Context, day time.Time, scope BudgetScope) (GlobalBudgetUsage, error) {
	dayStr := day.UTC().Format("2006-01-02")
	var out GlobalBudgetUsage
	out.Day = dayStr
	out.Scope = scope
	err := s.pool.QueryRow(ctx, `
		SELECT spent_micros, count
		FROM nori_global_budget_daily
		WHERE day = $1::date AND scope = $2`,
		dayStr, string(scope),
	).Scan(&out.SpentMicros, &out.Count)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	return out, err
}

func (s *PostgresStore) NotifyEntitlementChanged(ctx context.Context, userID string) error {
	return NotifyEntitlementChangedTx(ctx, s.pool, userID)
}
