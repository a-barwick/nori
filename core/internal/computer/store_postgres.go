package computer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/billing"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

const computerSelectCols = `
	user_id, computer_id, provider, provider_id, status,
	baseline_checkpoint_id, bootstrap_version, last_active_at,
	last_activity_at, awake_since, last_metered_at, sleeping_since, provider_owner,
	created_at, updated_at
`

const fleetCapLockKey = "nori-computer-fleet-cap"

// PostgresStore backs computer lifecycle, desktop sessions, and file links in Postgres.
type PostgresStore struct {
	pool    *pgxpool.Pool
	ownPool bool
}

// NewPostgresStore wraps an existing pgxpool.Pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// OpenPostgresStore opens a dedicated pool for computer metadata tables.
func OpenPostgresStore(ctx context.Context, dsn string, maxConns int32) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &PostgresStore{pool: pool, ownPool: true}, nil
}

func (s *PostgresStore) Close() {
	if s != nil && s.ownPool && s.pool != nil {
		s.pool.Close()
	}
}

func scanComputerRecord(row pgx.Row) (ComputerRecord, error) {
	var rec ComputerRecord
	var status string
	var lastActivity time.Time
	err := row.Scan(
		&rec.UserID, &rec.ComputerID, &rec.Provider, &rec.ProviderID, &status,
		&rec.BaselineCheckpointID, &rec.BootstrapVersion, &rec.LastActiveAt,
		&lastActivity, &rec.AwakeSince, &rec.LastMeteredAt, &rec.SleepingSince, &rec.ProviderOwner,
		&rec.CreatedAt, &rec.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ComputerRecord{}, ErrNotFound
	}
	if err != nil {
		return ComputerRecord{}, err
	}
	rec.Status = Status(status)
	if !lastActivity.IsZero() {
		t := lastActivity.UTC()
		rec.LastActivityAt = &t
	}
	if rec.ProviderOwner == "" {
		rec.ProviderOwner = DefaultProviderOwner
	}
	return rec, nil
}

func isAwakeStatus(st Status) bool {
	return st == StatusReady || st == StatusProvisioning || st == StatusResetting
}

func (s *PostgresStore) GetComputer(ctx context.Context, userID string) (ComputerRecord, error) {
	return scanComputerRecord(s.pool.QueryRow(ctx,
		`SELECT `+computerSelectCols+` FROM nori_user_computers WHERE user_id = $1`, userID,
	))
}

func (s *PostgresStore) ReserveComputer(ctx context.Context, rec ComputerRecord) (ComputerRecord, bool, error) {
	return s.ReserveComputerWithCap(ctx, rec, 0)
}

func (s *PostgresStore) ReserveComputerWithCap(ctx context.Context, rec ComputerRecord, maxFleet int) (ComputerRecord, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ComputerRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	// Always lock fleet cap before per-user advisory lock so lock order is deterministic.
	if maxFleet > 0 {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fleetCapLockKey); err != nil {
			return ComputerRecord{}, false, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-computer:"+rec.UserID); err != nil {
		return ComputerRecord{}, false, err
	}
	var deletePending bool
	_ = tx.QueryRow(ctx, `SELECT COALESCE((data->'delete_pending') = 'true'::jsonb, false)
		FROM nori_records WHERE shard = 'users' AND collection = 'users' AND id = $1`, rec.UserID).Scan(&deletePending)
	if deletePending {
		return ComputerRecord{}, false, ErrDeleting
	}
	existing, qErr := scanComputerRecord(tx.QueryRow(ctx,
		`SELECT `+computerSelectCols+` FROM nori_user_computers WHERE user_id = $1 FOR UPDATE`, rec.UserID,
	))
	if qErr == nil {
		if existing.Status == StatusDeleting {
			return existing, false, ErrDeleting
		}
		if existing.Status != StatusDeleted {
			if maxFleet > 0 && !isAwakeStatus(existing.Status) {
				var awake int
				if err := tx.QueryRow(ctx, `
					SELECT COUNT(*) FROM nori_user_computers
					WHERE status IN ('ready', 'provisioning', 'resetting') AND user_id <> $1`, rec.UserID,
				).Scan(&awake); err != nil {
					return ComputerRecord{}, false, err
				}
				if awake >= maxFleet {
					return ComputerRecord{}, false, ErrFleetCapReached
				}
			}
			if err := tx.Commit(ctx); err != nil {
				return ComputerRecord{}, false, err
			}
			return existing, false, nil
		}
	} else if !errors.Is(qErr, ErrNotFound) {
		return ComputerRecord{}, false, qErr
	}
	if maxFleet > 0 {
		var awake int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM nori_user_computers
			WHERE status IN ('ready', 'provisioning', 'resetting') AND user_id <> $1`, rec.UserID,
		).Scan(&awake); err != nil {
			return ComputerRecord{}, false, err
		}
		if awake >= maxFleet {
			return ComputerRecord{}, false, ErrFleetCapReached
		}
	}
	owner := rec.ProviderOwner
	if owner == "" {
		owner = DefaultProviderOwner
	}
	rec.ProviderOwner = owner
	lastAct := rec.LastActivityAt
	if lastAct == nil {
		lastAct = rec.LastActiveAt
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO nori_user_computers (
			user_id, computer_id, provider, provider_id, status,
			baseline_checkpoint_id, bootstrap_version, last_active_at,
			last_activity_at, awake_since, last_metered_at, sleeping_since, provider_owner,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9, clock_timestamp()), $10, $11, NULL, $12, clock_timestamp(), clock_timestamp())
		ON CONFLICT (user_id) DO UPDATE SET
			computer_id = EXCLUDED.computer_id,
			provider = EXCLUDED.provider,
			provider_id = EXCLUDED.provider_id,
			status = EXCLUDED.status,
			baseline_checkpoint_id = EXCLUDED.baseline_checkpoint_id,
			bootstrap_version = EXCLUDED.bootstrap_version,
			last_active_at = COALESCE(EXCLUDED.last_active_at, nori_user_computers.last_active_at),
			last_activity_at = COALESCE(EXCLUDED.last_activity_at, clock_timestamp()),
			awake_since = EXCLUDED.awake_since,
			last_metered_at = EXCLUDED.last_metered_at,
			sleeping_since = NULL,
			provider_owner = EXCLUDED.provider_owner,
			updated_at = clock_timestamp()`,
		rec.UserID, rec.ComputerID, rec.Provider, rec.ProviderID, string(rec.Status),
		rec.BaselineCheckpointID, rec.BootstrapVersion, rec.LastActiveAt,
		lastAct, rec.AwakeSince, rec.LastMeteredAt, owner,
	)
	if err != nil {
		return ComputerRecord{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ComputerRecord{}, false, err
	}
	return rec, true, nil
}

func (s *PostgresStore) CheckFleetCapAndMarkWaking(ctx context.Context, userID string, maxFleet int, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if maxFleet > 0 {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fleetCapLockKey); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-computer:"+userID); err != nil {
		return err
	}
	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM nori_user_computers WHERE user_id = $1 FOR UPDATE`, userID).Scan(&currentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	cur := Status(currentStatus)
	if cur == StatusDeleting {
		return ErrDeleting
	}
	if isAwakeStatus(cur) {
		return tx.Commit(ctx)
	}
	if maxFleet > 0 {
		var awake int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM nori_user_computers
			WHERE status IN ('ready', 'provisioning', 'resetting') AND user_id <> $1`, userID,
		).Scan(&awake); err != nil {
			return err
		}
		if awake >= maxFleet {
			return ErrFleetCapReached
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE nori_user_computers
		SET status = 'provisioning',
		    last_active_at = $2,
		    last_activity_at = $2,
		    sleeping_since = NULL,
		    updated_at = clock_timestamp()
		WHERE user_id = $1`, userID, now,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) UpsertComputer(ctx context.Context, rec ComputerRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Coordinate with BeginAccountDelete on the same advisory lock.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-computer:"+rec.UserID); err != nil {
		return err
	}
	var deletePending bool
	_ = tx.QueryRow(ctx, `SELECT COALESCE((data->'delete_pending') = 'true'::jsonb, false)
		FROM nori_records WHERE shard = 'users' AND collection = 'users' AND id = $1`, rec.UserID).Scan(&deletePending)
	if deletePending && rec.Status != StatusDeleting && rec.Status != StatusDeleted {
		return ErrDeleting
	}
	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM nori_user_computers WHERE user_id = $1 FOR UPDATE`, rec.UserID).Scan(&currentStatus)
	if err == nil && Status(currentStatus) == StatusDeleting && rec.Status != StatusDeleting && rec.Status != StatusDeleted {
		return ErrDeleting
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	owner := rec.ProviderOwner
	if owner == "" {
		owner = DefaultProviderOwner
	}
	lastAct := rec.LastActivityAt
	if lastAct == nil {
		lastAct = rec.LastActiveAt
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO nori_user_computers (
			user_id, computer_id, provider, provider_id, status,
			baseline_checkpoint_id, bootstrap_version, last_active_at,
			last_activity_at, awake_since, last_metered_at, sleeping_since, provider_owner,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9, clock_timestamp()), $10, $11, $12, $13, clock_timestamp(), clock_timestamp())
		ON CONFLICT (user_id) DO UPDATE SET
			computer_id = EXCLUDED.computer_id,
			provider = EXCLUDED.provider,
			provider_id = EXCLUDED.provider_id,
			status = EXCLUDED.status,
			baseline_checkpoint_id = EXCLUDED.baseline_checkpoint_id,
			bootstrap_version = EXCLUDED.bootstrap_version,
			last_active_at = COALESCE(EXCLUDED.last_active_at, nori_user_computers.last_active_at),
			last_activity_at = COALESCE(EXCLUDED.last_activity_at, nori_user_computers.last_activity_at),
			awake_since = CASE
				WHEN EXCLUDED.status IN ('sleeping', 'deleted', 'error') THEN NULL
				WHEN nori_user_computers.awake_since IS NOT NULL THEN nori_user_computers.awake_since
				ELSE EXCLUDED.awake_since
			END,
			last_metered_at = CASE
				WHEN EXCLUDED.status IN ('sleeping', 'deleted', 'error') THEN COALESCE(EXCLUDED.last_metered_at, nori_user_computers.last_metered_at)
				WHEN nori_user_computers.awake_since IS NOT NULL THEN COALESCE(nori_user_computers.last_metered_at, EXCLUDED.last_metered_at)
				ELSE EXCLUDED.last_metered_at
			END,
			sleeping_since = CASE
				WHEN EXCLUDED.status = 'sleeping' THEN COALESCE(EXCLUDED.sleeping_since, nori_user_computers.sleeping_since, clock_timestamp())
				WHEN EXCLUDED.status IN ('ready', 'provisioning', 'resetting') THEN NULL
				ELSE nori_user_computers.sleeping_since
			END,
			provider_owner = COALESCE(NULLIF(EXCLUDED.provider_owner, ''), nori_user_computers.provider_owner),
			updated_at = clock_timestamp()`,
		rec.UserID, rec.ComputerID, rec.Provider, rec.ProviderID, string(rec.Status),
		rec.BaselineCheckpointID, rec.BootstrapVersion, rec.LastActiveAt,
		lastAct, rec.AwakeSince, rec.LastMeteredAt, rec.SleepingSince, owner,
	)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) UpdateComputerStatus(ctx context.Context, userID string, status Status, lastActive *time.Time) error {
	res, err := s.pool.Exec(ctx, `
		UPDATE nori_user_computers
		SET status = $2,
		    last_active_at = COALESCE($3, last_active_at),
		    last_activity_at = COALESCE($3, last_activity_at),
		    awake_since = CASE
		        WHEN $2 IN ('sleeping', 'deleted', 'error') THEN NULL
		        WHEN $2 = 'ready' AND awake_since IS NULL THEN COALESCE($3, clock_timestamp())
		        ELSE awake_since
		    END,
		    last_metered_at = CASE
		        WHEN $2 = 'ready' AND awake_since IS NULL THEN COALESCE($3, clock_timestamp())
		        ELSE last_metered_at
		    END,
		    sleeping_since = CASE
		        WHEN $2 = 'sleeping' THEN COALESCE(sleeping_since, clock_timestamp())
		        WHEN $2 IN ('ready', 'provisioning', 'resetting') THEN NULL
		        ELSE sleeping_since
		    END,
		    updated_at = clock_timestamp()
		WHERE user_id = $1 AND (status <> 'deleting' OR $2 IN ('deleting', 'deleted'))`,
		userID, string(status), lastActive,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		var cur string
		if qErr := s.pool.QueryRow(ctx, `SELECT status FROM nori_user_computers WHERE user_id = $1`, userID).Scan(&cur); qErr == nil && Status(cur) == StatusDeleting {
			return ErrDeleting
		}
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) TouchComputerActivity(ctx context.Context, userID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	t := at.UTC()
	_, err := s.pool.Exec(ctx, `
		UPDATE nori_user_computers
		SET last_active_at = GREATEST(COALESCE(last_active_at, $2), $2),
		    last_activity_at = GREATEST(last_activity_at, $2)
		WHERE user_id = $1 AND status NOT IN ('deleting', 'deleted')`,
		userID, t,
	)
	return err
}

func (s *PostgresStore) CountAwakeComputers(ctx context.Context, excludeUserID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM nori_user_computers
		WHERE status IN ('ready', 'provisioning', 'resetting')
		  AND ($1 = '' OR user_id <> $1)`,
		excludeUserID,
	).Scan(&count)
	return count, err
}

func (s *PostgresStore) ListAwakeComputers(ctx context.Context, limit int) ([]ComputerRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+computerSelectCols+`
		FROM nori_user_computers
		WHERE status IN ('ready', 'provisioning', 'resetting')
		   OR (awake_since IS NOT NULL AND status NOT IN ('sleeping', 'deleted'))
		ORDER BY last_activity_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComputerRecord
	for rows.Next() {
		rec, err := scanComputerRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// MeterAwakeTime locks the user's nori_user_computers row FOR UPDATE, calculates elapsed
// awake time since last_metered_at (or awake_since), advances last_metered_at (and applies
// any status transition such as sleeping), inserts the finalized state.Usage row into
// nori_records, and updates nori_global_budget_daily ('hosted_all' + 'hosted_sprites')
// ALL in a SINGLE Postgres transaction so concurrent janitor ticks, explicit Sleep, or crashes
// can never double-bill.
func (s *PostgresStore) MeterAwakeTime(ctx context.Context, req MeterAwakeRequest) (MeterAwakeResult, error) {
	now := req.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return MeterAwakeResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rec, err := scanComputerRecord(tx.QueryRow(ctx,
		`SELECT `+computerSelectCols+` FROM nori_user_computers WHERE user_id = $1 FOR UPDATE`,
		req.UserID,
	))
	if err != nil {
		return MeterAwakeResult{}, err
	}

	isAwake := rec.AwakeSince != nil && rec.Status != StatusSleeping && rec.Status != StatusDeleted
	var start, end time.Time
	var elapsed time.Duration
	var costMicros int64
	var costUSD float64
	if isAwake {
		start = rec.AwakeSince.UTC()
		if rec.LastMeteredAt != nil && rec.LastMeteredAt.UTC().After(start) {
			start = rec.LastMeteredAt.UTC()
		}
		end = now
		if req.MeterEndAt != nil && !req.MeterEndAt.IsZero() && req.MeterEndAt.UTC().Before(end) {
			end = req.MeterEndAt.UTC()
		}
		if end.After(start) {
			elapsed = end.Sub(start)
			isPlatform := rec.ProviderOwner == "" || rec.ProviderOwner == DefaultProviderOwner
			if isPlatform && req.RatePerHourUSD > 0 {
				costMicros = billing.USDToMicros((elapsed.Seconds() / 3600.0) * req.RatePerHourUSD)
				costUSD = billing.MicrosToUSD(costMicros)
			}
		}
	}

	// Compute next lifecycle/metering columns.
	switch req.TransitionStatus {
	case StatusSleeping:
		if rec.Status != StatusDeleting && rec.Status != StatusDeleted {
			rec.Status = StatusSleeping
		}
		rec.AwakeSince = nil
		sleepAt := now
		rec.SleepingSince = &sleepAt
		if isAwake && end.After(start) {
			mAt := end
			rec.LastMeteredAt = &mAt
		} else if rec.LastMeteredAt == nil {
			mAt := now
			rec.LastMeteredAt = &mAt
		}
	case StatusDeleting, StatusDeleted:
		rec.Status = req.TransitionStatus
		rec.AwakeSince = nil
		if isAwake && end.After(start) {
			mAt := end
			rec.LastMeteredAt = &mAt
		}
	default:
		if req.TransitionStatus != "" && rec.Status != StatusDeleting && rec.Status != StatusDeleted {
			rec.Status = req.TransitionStatus
		}
		if isAwake && costMicros > 0 {
			mAt := end
			rec.LastMeteredAt = &mAt
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE nori_user_computers
		SET status = $2,
		    awake_since = $3,
		    last_metered_at = $4,
		    sleeping_since = $5,
		    updated_at = clock_timestamp()
		WHERE user_id = $1`,
		rec.UserID, string(rec.Status), rec.AwakeSince, rec.LastMeteredAt, rec.SleepingSince,
	); err != nil {
		return MeterAwakeResult{}, err
	}

	var usageRow *state.Usage
	if costMicros > 0 {
		dataOwner := strings.TrimSpace(req.DataOwner)
		if dataOwner == "" {
			dataOwner = resolveComputerDataOwnerTx(ctx, tx, rec.UserID)
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_owner', $1, true)", dataOwner); err != nil {
			return MeterAwakeResult{}, err
		}
		usageID := strings.TrimSpace(req.UsageID)
		if usageID == "" {
			usageID = "sprites_" + rec.ComputerID + "_" + uuid.NewString()
		}
		costVal := costUSD
		u := state.Usage{
			ID:            usageID,
			InputID:       rec.ComputerID,
			Provider:      "sprites",
			Model:         "sprites-compute",
			Billing:       state.UsageBillingHosted,
			Category:      state.UsageCategoryComputer,
			CostUSD:       &costVal,
			CostStatus:    "calculated",
			RequestStatus: "completed",
			CreatedAt:     now,
		}
		fields, err := docstore.Encode(u)
		if err != nil {
			return MeterAwakeResult{}, err
		}
		raw, err := json.Marshal(docstore.JSONValue(fields))
		if err != nil {
			return MeterAwakeResult{}, err
		}
		shard := "owners/" + dataOwner
		collection := shard + "/model_usage"
		tag, err := tx.Exec(ctx, `
			INSERT INTO nori_records(shard, collection, id, data)
			VALUES ($1, $2, $3, $4::jsonb)
			ON CONFLICT (shard, collection, id) DO NOTHING`,
			shard, collection, u.ID, raw,
		)
		if err != nil {
			return MeterAwakeResult{}, err
		}
		if tag.RowsAffected() > 0 {
			if err := billing.CheckAndIncrementGlobalBudgetTx(ctx, tx, now, billing.ScopeHostedAll, costMicros, 0, 1, 0); err != nil {
				return MeterAwakeResult{}, err
			}
			if err := billing.CheckAndIncrementGlobalBudgetTx(ctx, tx, now, billing.ScopeHostedSprites, costMicros, 0, 1, 0); err != nil {
				return MeterAwakeResult{}, err
			}
			usageRow = &u
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return MeterAwakeResult{}, err
	}
	return MeterAwakeResult{
		Record:     rec,
		Metered:    usageRow != nil,
		Elapsed:    elapsed,
		CostUSD:    costUSD,
		CostMicros: costMicros,
		Usage:      usageRow,
	}, nil
}

func resolveComputerDataOwnerTx(ctx context.Context, tx pgx.Tx, userID string) string {
	if userID != users.LegacyID {
		return userID
	}
	var email string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(data->>'email', ''), 'owner')
		FROM nori_records
		WHERE shard = 'users' AND collection = 'users' AND id = 'owner'
		LIMIT 1`,
	).Scan(&email); err == nil && email != "" {
		return email
	}
	return userID
}

func (s *PostgresStore) MarkComputerDeleting(ctx context.Context, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-computer:"+userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE nori_user_computers SET status = 'deleting', awake_since = NULL, updated_at = clock_timestamp() WHERE user_id = $1 AND status <> 'deleted'`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE nori_computer_sessions SET ended_at = COALESCE(ended_at, clock_timestamp()) WHERE user_id = $1 AND ended_at IS NULL`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) MarkComputerDeleted(ctx context.Context, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM nori_computer_sessions WHERE user_id = $1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM nori_computer_file_links WHERE user_id = $1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE nori_user_computers SET status = 'deleted', awake_since = NULL, updated_at = clock_timestamp() WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ListDeletingComputers(ctx context.Context, limit int) ([]ComputerRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+computerSelectCols+`
		FROM nori_user_computers
		WHERE status = 'deleting'
		ORDER BY updated_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComputerRecord
	for rows.Next() {
		rec, err := scanComputerRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateSession(ctx context.Context, sess SessionRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO nori_computer_sessions (
			id, user_id, computer_id, mode, task_id, interaction_id,
			token_hash, expires_at, ended_at, created_at, last_activity_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, clock_timestamp(), clock_timestamp())`,
		sess.ID, sess.UserID, sess.ComputerID, string(sess.Mode), sess.TaskID, sess.InteractionID,
		sess.TokenHash, sess.ExpiresAt, sess.EndedAt,
	)
	return err
}

func (s *PostgresStore) GetSession(ctx context.Context, userID, sessionID string) (SessionRecord, error) {
	var sess SessionRecord
	var mode string
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, computer_id, mode, task_id, interaction_id,
		       token_hash, expires_at, ended_at, created_at, last_activity_at
		FROM nori_computer_sessions
		WHERE id = $1 AND user_id = $2`, sessionID, userID).Scan(
		&sess.ID, &sess.UserID, &sess.ComputerID, &mode, &sess.TaskID, &sess.InteractionID,
		&sess.TokenHash, &sess.ExpiresAt, &sess.EndedAt, &sess.CreatedAt, &sess.LastActivityAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionRecord{}, ErrNotFound
	}
	if err != nil {
		return SessionRecord{}, err
	}
	sess.Mode = SessionMode(mode)
	return sess, nil
}

func (s *PostgresStore) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (SessionRecord, error) {
	var sess SessionRecord
	var mode string
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, computer_id, mode, task_id, interaction_id,
		       token_hash, expires_at, ended_at, created_at, last_activity_at
		FROM nori_computer_sessions
		WHERE token_hash = $1`, tokenHash).Scan(
		&sess.ID, &sess.UserID, &sess.ComputerID, &mode, &sess.TaskID, &sess.InteractionID,
		&sess.TokenHash, &sess.ExpiresAt, &sess.EndedAt, &sess.CreatedAt, &sess.LastActivityAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionRecord{}, ErrNotFound
	}
	if err != nil {
		return SessionRecord{}, err
	}
	sess.Mode = SessionMode(mode)
	return sess, nil
}

// ConsumeSessionToken atomically validates and rotates a desktop session's token_hash to a
// unique consumed sentinel in a single UPDATE so a stolen or replayed token cannot open a second socket.
func (s *PostgresStore) ConsumeSessionToken(ctx context.Context, userID, sessionID string, tokenHash, consumedHash []byte, now time.Time) (SessionRecord, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	var sess SessionRecord
	var mode string
	err := s.pool.QueryRow(ctx, `
		UPDATE nori_computer_sessions
		SET token_hash = $4,
		    last_activity_at = $5
		WHERE id = $1
		  AND user_id = $2
		  AND token_hash = $3
		  AND ended_at IS NULL
		  AND expires_at > $5
		RETURNING id, user_id, computer_id, mode, task_id, interaction_id,
		          token_hash, expires_at, ended_at, created_at, last_activity_at`,
		sessionID, userID, tokenHash, consumedHash, now,
	).Scan(
		&sess.ID, &sess.UserID, &sess.ComputerID, &mode, &sess.TaskID, &sess.InteractionID,
		&sess.TokenHash, &sess.ExpiresAt, &sess.EndedAt, &sess.CreatedAt, &sess.LastActivityAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gErr := s.GetSession(ctx, userID, sessionID)
		if gErr != nil {
			return SessionRecord{}, ErrNotFound
		}
		if existing.EndedAt != nil {
			return SessionRecord{}, ErrSessionRevoked
		}
		if !existing.ExpiresAt.After(now) {
			_ = s.EndSession(ctx, userID, sessionID, now)
			return SessionRecord{}, ErrSessionExpired
		}
		return SessionRecord{}, ErrSessionTokenUsed
	}
	if err != nil {
		return SessionRecord{}, err
	}
	sess.Mode = SessionMode(mode)
	return sess, nil
}

func (s *PostgresStore) ListActiveSessions(ctx context.Context, userID string) ([]SessionRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, computer_id, mode, task_id, interaction_id,
		       token_hash, expires_at, ended_at, created_at, last_activity_at
		FROM nori_computer_sessions
		WHERE user_id = $1 AND ended_at IS NULL AND expires_at > clock_timestamp()
		ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var sess SessionRecord
		var mode string
		if err := rows.Scan(
			&sess.ID, &sess.UserID, &sess.ComputerID, &mode, &sess.TaskID, &sess.InteractionID,
			&sess.TokenHash, &sess.ExpiresAt, &sess.EndedAt, &sess.CreatedAt, &sess.LastActivityAt,
		); err != nil {
			return nil, err
		}
		sess.Mode = SessionMode(mode)
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateSessionMode(ctx context.Context, userID, sessionID string, mode SessionMode) (SessionRecord, error) {
	var sess SessionRecord
	var outMode string
	err := s.pool.QueryRow(ctx, `
		UPDATE nori_computer_sessions
		SET mode = $3, last_activity_at = clock_timestamp()
		WHERE id = $1 AND user_id = $2 AND ended_at IS NULL
		RETURNING id, user_id, computer_id, mode, task_id, interaction_id,
		          token_hash, expires_at, ended_at, created_at, last_activity_at`,
		sessionID, userID, string(mode),
	).Scan(
		&sess.ID, &sess.UserID, &sess.ComputerID, &outMode, &sess.TaskID, &sess.InteractionID,
		&sess.TokenHash, &sess.ExpiresAt, &sess.EndedAt, &sess.CreatedAt, &sess.LastActivityAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gErr := s.GetSession(ctx, userID, sessionID)
		if gErr == nil && existing.EndedAt != nil {
			return SessionRecord{}, ErrSessionRevoked
		}
		return SessionRecord{}, ErrNotFound
	}
	if err != nil {
		return SessionRecord{}, err
	}
	sess.Mode = SessionMode(outMode)
	return sess, nil
}

func (s *PostgresStore) TouchSession(ctx context.Context, sessionID string, activityAt, newExpiresAt time.Time) error {
	var userID string
	err := s.pool.QueryRow(ctx, `
		UPDATE nori_computer_sessions
		SET last_activity_at = $2,
		    expires_at = LEAST(created_at + interval '2 hours', GREATEST(expires_at, $3))
		WHERE id = $1 AND ended_at IS NULL
		RETURNING user_id`,
		sessionID, activityAt.UTC(), newExpiresAt.UTC(),
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionRevoked
	}
	if err != nil {
		return err
	}
	_ = s.TouchComputerActivity(ctx, userID, activityAt)
	return nil
}

func (s *PostgresStore) EndSession(ctx context.Context, userID, sessionID string, endedAt time.Time) error {
	res, err := s.pool.Exec(ctx, `
		UPDATE nori_computer_sessions
		SET ended_at = COALESCE(ended_at, $3)
		WHERE id = $1 AND user_id = $2`,
		sessionID, userID, endedAt.UTC(),
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RevokeUserSessions(ctx context.Context, userID string, endedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE nori_computer_sessions
		SET ended_at = COALESCE(ended_at, $2)
		WHERE user_id = $1 AND ended_at IS NULL`,
		userID, endedAt.UTC(),
	)
	return err
}

func (s *PostgresStore) ExpireIdleSessions(ctx context.Context, now time.Time, idleCutoff time.Time) (int64, error) {
	res, err := s.pool.Exec(ctx, `
		UPDATE nori_computer_sessions
		SET ended_at = $1
		WHERE ended_at IS NULL AND (expires_at <= $1 OR last_activity_at < $2)`,
		now.UTC(), idleCutoff.UTC(),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

func (s *PostgresStore) UpsertFileLink(ctx context.Context, link FileLink) error {
	tags := link.EntityTags
	if tags == nil {
		tags = []string{}
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO nori_computer_file_links (user_id, workspace_path, result_id, entity_tags, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, clock_timestamp(), clock_timestamp())
		ON CONFLICT (user_id, workspace_path) DO UPDATE SET
			result_id = CASE WHEN EXCLUDED.result_id <> '' THEN EXCLUDED.result_id ELSE nori_computer_file_links.result_id END,
			entity_tags = EXCLUDED.entity_tags,
			updated_at = clock_timestamp()`,
		link.UserID, link.WorkspacePath, link.ResultID, string(raw),
	)
	return err
}

func (s *PostgresStore) ListFileLinksByResult(ctx context.Context, userID, resultID string) ([]FileLink, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, workspace_path, result_id, entity_tags, created_at, updated_at
		FROM nori_computer_file_links
		WHERE user_id = $1 AND result_id = $2
		ORDER BY workspace_path ASC`, userID, resultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFileLinks(rows)
}

func (s *PostgresStore) ListFileLinksByEntityTag(ctx context.Context, userID, tag string) ([]FileLink, error) {
	needle := strings.ToLower(strings.TrimSpace(tag))
	if needle == "" {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, workspace_path, result_id, entity_tags, created_at, updated_at
		FROM nori_computer_file_links
		WHERE user_id = $1
		  AND result_id <> $3
		  AND EXISTS (
		      SELECT 1 FROM jsonb_array_elements_text(entity_tags) AS elem
		      WHERE lower(btrim(elem)) = $2
		  )
		ORDER BY workspace_path ASC`, userID, needle, PurgedLinkResultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFileLinks(rows)
}

func scanFileLinks(rows pgx.Rows) ([]FileLink, error) {
	var out []FileLink
	for rows.Next() {
		var link FileLink
		var rawTags []byte
		if err := rows.Scan(&link.UserID, &link.WorkspacePath, &link.ResultID, &rawTags, &link.CreatedAt, &link.UpdatedAt); err != nil {
			return nil, err
		}
		if len(rawTags) > 0 {
			_ = json.Unmarshal(rawTags, &link.EntityTags)
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteFileLink(ctx context.Context, userID, workspacePath string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM nori_computer_file_links WHERE user_id = $1 AND workspace_path = $2`, userID, workspacePath)
	return err
}

func (s *PostgresStore) DeleteFileLinksByUser(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM nori_computer_file_links WHERE user_id = $1`, userID)
	return err
}
