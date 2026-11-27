package postgres

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

const (
	wakeWaitingFanoutLimit = 32
	wakeNotifyTimeout      = 2 * time.Second
	wakeFlushTimeout       = 5 * time.Second
	logRateLimitInterval   = 30 * time.Second
)

var (
	listenInitialBackoff = 100 * time.Millisecond
	listenMaxBackoff     = 5 * time.Second
)

type logLimiter struct {
	mu         sync.Mutex
	interval   time.Duration
	lastLogged time.Time
	suppressed int64
	failing    bool
}

func newLogLimiter(interval time.Duration) *logLimiter {
	return &logLimiter{interval: interval}
}

func (l *logLimiter) allowWarn(now time.Time) (bool, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.failing || now.Sub(l.lastLogged) >= l.interval {
		suppressed := l.suppressed
		l.suppressed = 0
		l.lastLogged = now
		l.failing = true
		return true, suppressed
	}
	l.suppressed++
	return false, 0
}

func (l *logLimiter) recover() (bool, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.failing {
		return false, 0
	}
	suppressed := l.suppressed
	l.suppressed = 0
	l.failing = false
	l.lastLogged = time.Time{}
	return true, suppressed
}

var (
	emitWakeLogLimiter  = newLogLimiter(logRateLimitInterval)
	flushWakeLogLimiter = newLogLimiter(logRateLimitInterval)
)

var (
	_ docstore.LeaseStore       = (*backend)(nil)
	_ docstore.ScopedLeaseStore = (*backend)(nil)
	_ docstore.WakeListener     = (*backend)(nil)
	_ docstore.WakeWriter       = (*backend)(nil)
	_ docstore.WakeWriter       = (*transaction)(nil)
)

func (b *backend) readCommitted(ctx context.Context, readOnly bool, fn func(q executor) error) error {
	return b.readCommittedRaw(ctx, readOnly, func(tx pgx.Tx) error {
		if ownerID := users.ID(ctx); ownerID != "" {
			if _, err := tx.Exec(ctx, "SELECT set_config('app.current_owner', $1, true)", ownerID); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func (b *backend) readCommittedRaw(ctx context.Context, readOnly bool, fn func(tx pgx.Tx) error) error {
	opts := pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	if readOnly {
		opts.AccessMode = pgx.ReadOnly
	}
	const maxAttempts = 3
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = b.runReadCommittedAttempt(ctx, opts, fn)
		if err == nil || !retryable(err) {
			return err
		}
		if !sleepBackoff(ctx, time.Duration(attempt+1)*5*time.Millisecond) {
			return ctx.Err()
		}
	}
	return err
}

func (b *backend) runReadCommittedAttempt(ctx context.Context, opts pgx.TxOptions, fn func(tx pgx.Tx) error) error {
	tx, err := b.pool.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func dedupeWakeEvents(events []docstore.WakeEvent) (channels, payloads []string) {
	if len(events) == 0 {
		return nil, nil
	}
	seen := make(map[docstore.WakeEvent]struct{}, len(events))
	channels = make([]string, 0, len(events))
	payloads = make([]string, 0, len(events))
	for _, ev := range events {
		if ev.Channel == "" || ev.Payload == "" {
			continue
		}
		if _, dup := seen[ev]; dup {
			continue
		}
		seen[ev] = struct{}{}
		channels = append(channels, ev.Channel)
		payloads = append(payloads, ev.Payload)
	}
	return channels, payloads
}

func (b *backend) emitWakes(ctx context.Context, events []docstore.WakeEvent) {
	if len(events) == 0 || b.pool == nil {
		return
	}
	channels, payloads := dedupeWakeEvents(events)
	if len(channels) == 0 {
		return
	}
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wakeNotifyTimeout)
	defer cancel()
	_, err := b.pool.Exec(
		notifyCtx,
		"SELECT pg_notify(ch, payload) FROM unnest($1::text[], $2::text[]) AS t(ch, payload)",
		channels,
		payloads,
	)
	if err != nil {
		if emit, suppressed := emitWakeLogLimiter.allowWarn(time.Now()); emit {
			slog.WarnContext(ctx, "postgres emitWakes failed",
				"count", len(channels),
				"suppressed", suppressed,
				"err", err,
			)
		}
		return
	}
	emitWakeLogLimiter.recover()
}

func leaseMicros(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	us := d.Microseconds()
	if us == 0 {
		return 1
	}
	return us
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func normalizeTaskLease(l *docstore.TaskLease) {
	l.WakeAt = l.WakeAt.UTC()
	l.UpdatedAt = l.UpdatedAt.UTC()
	if l.LeaseUntil != nil {
		u := l.LeaseUntil.UTC()
		l.LeaseUntil = &u
	}
}

func normalizeUserWake(w *docstore.UserWake) {
	w.WakeAt = w.WakeAt.UTC()
	w.UpdatedAt = w.UpdatedAt.UTC()
	if w.LeaseUntil != nil {
		u := w.LeaseUntil.UTC()
		w.LeaseUntil = &u
	}
}

func wakeTaskSQL(ctx context.Context, q executor, userID, runID string, wakeAt time.Time) ([]docstore.WakeEvent, error) {
	if userID == "" || runID == "" {
		return nil, errors.New("wake task requires user_id and run_id")
	}
	now := time.Now().UTC()
	_, err := q.Exec(ctx, `
		INSERT INTO nori_task_leases (
			user_id, run_id, runnable, waiting_reason, owner, wake_seq, lease_until, wake_at, updated_at
		) VALUES (
			$1, $2, true, '', '', 1, NULL, COALESCE($3::timestamptz, now()), now()
		)
		ON CONFLICT (user_id, run_id) DO UPDATE SET
			runnable = true,
			waiting_reason = '',
			wake_seq = nori_task_leases.wake_seq + 1,
			wake_at = CASE
				WHEN nori_task_leases.runnable THEN LEAST(nori_task_leases.wake_at, EXCLUDED.wake_at)
				ELSE EXCLUDED.wake_at
			END,
			updated_at = now()
	`, userID, runID, nullableTime(wakeAt))
	if err != nil {
		return nil, err
	}
	if wakeAt.IsZero() || !wakeAt.After(now) {
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.TaskWakePayload(userID, runID),
		}}, nil
	}
	return nil, nil
}

func wakeWaitingTasksSQL(ctx context.Context, q executor, userID, reason string) ([]docstore.WakeEvent, error) {
	var rows pgx.Rows
	var err error
	if userID == "" {
		rows, err = q.Query(ctx, `
			UPDATE nori_task_leases
			SET runnable = true,
				waiting_reason = '',
				wake_seq = wake_seq + 1,
				wake_at = CASE WHEN runnable THEN LEAST(wake_at, now()) ELSE now() END,
				updated_at = now()
			WHERE (runnable = false AND waiting_reason = $1)
			   OR runnable = true
			RETURNING user_id, run_id
		`, reason)
	} else {
		rows, err = q.Query(ctx, `
			UPDATE nori_task_leases
			SET runnable = true,
				waiting_reason = '',
				wake_seq = wake_seq + 1,
				wake_at = CASE WHEN runnable THEN LEAST(wake_at, now()) ELSE now() END,
				updated_at = now()
			WHERE user_id = $1
			  AND ((runnable = false AND waiting_reason = $2)
			   OR runnable = true)
			RETURNING user_id, run_id
		`, userID, reason)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var woken []docstore.TaskKey
	for rows.Next() {
		var key docstore.TaskKey
		if err := rows.Scan(&key.UserID, &key.RunID); err != nil {
			return nil, err
		}
		woken = append(woken, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(woken) == 0 {
		return nil, nil
	}
	if userID == "" {
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.WakePayloadAllTask,
		}}, nil
	}
	if len(woken) > wakeWaitingFanoutLimit {
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.UserTasksWakePayload(userID),
		}}, nil
	}
	events := make([]docstore.WakeEvent, 0, len(woken))
	for _, key := range woken {
		events = append(events, docstore.WakeEvent{
			Channel: docstore.WakeChannel,
			Payload: docstore.TaskWakePayload(key.UserID, key.RunID),
		})
	}
	return events, nil
}

func wakeUserSQL(ctx context.Context, q executor, userID, lane string, wakeAt time.Time) ([]docstore.WakeEvent, error) {
	if userID == "" || lane == "" {
		return nil, errors.New("wake user requires user_id and lane")
	}
	now := time.Now().UTC()
	_, err := q.Exec(ctx, `
		INSERT INTO nori_user_wakes (
			user_id, lane, runnable, wake_seq, wake_at, owner, lease_until, updated_at
		) VALUES (
			$1, $2, true, 1, COALESCE($3::timestamptz, now()), '', NULL, now()
		)
		ON CONFLICT (user_id, lane) DO UPDATE SET
			runnable = true,
			wake_seq = nori_user_wakes.wake_seq + 1,
			wake_at = CASE
				WHEN nori_user_wakes.runnable THEN LEAST(nori_user_wakes.wake_at, EXCLUDED.wake_at)
				ELSE EXCLUDED.wake_at
			END,
			updated_at = now()
	`, userID, lane, nullableTime(wakeAt))
	if err != nil {
		return nil, err
	}
	if wakeAt.IsZero() || !wakeAt.After(now) {
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.UserWakePayload(userID, lane),
		}}, nil
	}
	return nil, nil
}

func (t *transaction) WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error {
	events, err := wakeTaskSQL(ctx, t.q, userID, runID, wakeAt)
	if err != nil {
		return err
	}
	t.wakes = append(t.wakes, events...)
	return nil
}

func (t *transaction) WakeWaitingTasks(_ context.Context, userID, reason string) error {
	t.wakeWaitings = append(t.wakeWaitings, pendingWakeWaiting{
		userID: userID,
		reason: reason,
	})
	return nil
}

func (t *transaction) WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error {
	events, err := wakeUserSQL(ctx, t.q, userID, lane, wakeAt)
	if err != nil {
		return err
	}
	t.wakes = append(t.wakes, events...)
	return nil
}

func (t *transaction) NotifyPause(_ context.Context) error {
	t.wakes = append(t.wakes, docstore.WakeEvent{
		Channel: docstore.WakeChannel,
		Payload: docstore.WakePayloadPause,
	})
	return nil
}

func (b *backend) WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error {
	var events []docstore.WakeEvent
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		events, err = wakeTaskSQL(ctx, q, userID, runID, wakeAt)
		return err
	})
	if err != nil {
		return err
	}
	b.emitWakes(ctx, events)
	return nil
}

func (b *backend) WakeWaitingTasks(ctx context.Context, userID, reason string) error {
	var events []docstore.WakeEvent
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		events, err = wakeWaitingTasksSQL(ctx, q, userID, reason)
		return err
	})
	if err != nil {
		return err
	}
	b.emitWakes(ctx, events)
	return nil
}

func (b *backend) WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error {
	var events []docstore.WakeEvent
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		events, err = wakeUserSQL(ctx, q, userID, lane, wakeAt)
		return err
	})
	if err != nil {
		return err
	}
	b.emitWakes(ctx, events)
	return nil
}

func (b *backend) NotifyPause(ctx context.Context) error {
	b.emitWakes(ctx, []docstore.WakeEvent{{
		Channel: docstore.WakeChannel,
		Payload: docstore.WakePayloadPause,
	}})
	return nil
}

func scanTaskLease(row pgx.Row) (docstore.TaskLease, bool, error) {
	var l docstore.TaskLease
	err := row.Scan(
		&l.UserID,
		&l.RunID,
		&l.Runnable,
		&l.WaitingReason,
		&l.Owner,
		&l.WakeSeq,
		&l.LeaseUntil,
		&l.WakeAt,
		&l.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return docstore.TaskLease{}, false, nil
	}
	if err != nil {
		return docstore.TaskLease{}, false, err
	}
	normalizeTaskLease(&l)
	return l, true, nil
}

func scanUserWake(row pgx.Row) (docstore.UserWake, bool, error) {
	var w docstore.UserWake
	err := row.Scan(
		&w.UserID,
		&w.Lane,
		&w.Runnable,
		&w.WakeSeq,
		&w.WakeAt,
		&w.Owner,
		&w.LeaseUntil,
		&w.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return docstore.UserWake{}, false, nil
	}
	if err != nil {
		return docstore.UserWake{}, false, err
	}
	normalizeUserWake(&w)
	return w, true, nil
}

func (b *backend) ClaimTask(ctx context.Context, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	if owner == "" {
		return docstore.TaskLease{}, false, errors.New("claim task requires owner")
	}
	var out docstore.TaskLease
	var ok bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		out, ok, err = scanTaskLease(q.QueryRow(ctx, `
			WITH candidate AS (
				SELECT user_id, run_id
				FROM nori_task_leases
				WHERE runnable = true
				  AND wake_at <= now()
				  AND (lease_until IS NULL OR lease_until <= now())
				ORDER BY wake_at, updated_at
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE nori_task_leases AS t
			SET owner = $1,
				lease_until = now() + ($2::bigint * interval '1 microsecond'),
				updated_at = now()
			FROM candidate
			WHERE t.user_id = candidate.user_id AND t.run_id = candidate.run_id
			RETURNING t.user_id, t.run_id, t.runnable, t.waiting_reason, t.owner, t.wake_seq, t.lease_until, t.wake_at, t.updated_at
		`, owner, leaseMicros(lease)))
		return err
	})
	return out, ok, err
}

func (b *backend) ClaimTaskForUser(ctx context.Context, userID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	if userID == "" || owner == "" {
		return docstore.TaskLease{}, false, errors.New("claim task for user requires user_id and owner")
	}
	var out docstore.TaskLease
	var ok bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		out, ok, err = scanTaskLease(q.QueryRow(ctx, `
			WITH candidate AS (
				SELECT user_id, run_id
				FROM nori_task_leases
				WHERE user_id = $1
				  AND runnable = true
				  AND wake_at <= now()
				  AND (lease_until IS NULL OR lease_until <= now())
				ORDER BY wake_at, updated_at
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE nori_task_leases AS t
			SET owner = $2,
				lease_until = now() + ($3::bigint * interval '1 microsecond'),
				updated_at = now()
			FROM candidate
			WHERE t.user_id = candidate.user_id AND t.run_id = candidate.run_id
			RETURNING t.user_id, t.run_id, t.runnable, t.waiting_reason, t.owner, t.wake_seq, t.lease_until, t.wake_at, t.updated_at
		`, userID, owner, leaseMicros(lease)))
		return err
	})
	return out, ok, err
}

func (b *backend) ClaimTaskByID(ctx context.Context, userID, runID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	if userID == "" || runID == "" || owner == "" {
		return docstore.TaskLease{}, false, errors.New("claim task by id requires user_id, run_id, and owner")
	}
	var out docstore.TaskLease
	var ok bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		out, ok, err = scanTaskLease(q.QueryRow(ctx, `
			WITH candidate AS (
				SELECT user_id, run_id
				FROM nori_task_leases
				WHERE user_id = $1
				  AND run_id = $2
				  AND runnable = true
				  AND wake_at <= now()
				  AND (lease_until IS NULL OR lease_until <= now())
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE nori_task_leases AS t
			SET owner = $3,
				lease_until = now() + ($4::bigint * interval '1 microsecond'),
				updated_at = now()
			FROM candidate
			WHERE t.user_id = candidate.user_id AND t.run_id = candidate.run_id
			RETURNING t.user_id, t.run_id, t.runnable, t.waiting_reason, t.owner, t.wake_seq, t.lease_until, t.wake_at, t.updated_at
		`, userID, runID, owner, leaseMicros(lease)))
		return err
	})
	return out, ok, err
}

func (b *backend) RefreshTaskClaim(ctx context.Context, userID, runID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	if userID == "" || runID == "" || owner == "" {
		return docstore.TaskLease{}, false, errors.New("refresh task claim requires user_id, run_id, and owner")
	}
	var out docstore.TaskLease
	var ok bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		out, ok, err = scanTaskLease(q.QueryRow(ctx, `
			UPDATE nori_task_leases
			SET lease_until = now() + ($4::bigint * interval '1 microsecond'),
				updated_at = now()
			WHERE user_id = $1
			  AND run_id = $2
			  AND owner = $3
			  AND runnable = true
			  AND lease_until IS NOT NULL
			  AND lease_until > now()
			RETURNING user_id, run_id, runnable, waiting_reason, owner, wake_seq, lease_until, wake_at, updated_at
		`, userID, runID, owner, leaseMicros(lease)))
		return err
	})
	return out, ok, err
}

func (b *backend) RenewTaskLeases(ctx context.Context, owner string, keys []docstore.TaskKey, lease time.Duration) (int, error) {
	if owner == "" || len(keys) == 0 {
		return 0, nil
	}
	userIDs := make([]string, len(keys))
	runIDs := make([]string, len(keys))
	for i, key := range keys {
		userIDs[i] = key.UserID
		runIDs[i] = key.RunID
	}
	var renewed int
	err := b.readCommitted(ctx, false, func(q executor) error {
		tag, err := q.Exec(ctx, `
			WITH targets AS (
				SELECT user_id, run_id
				FROM nori_task_leases
				WHERE owner = $1
				  AND runnable = true
				  AND lease_until IS NOT NULL
				  AND lease_until > now()
				  AND (user_id, run_id) IN (SELECT unnest($3::text[]), unnest($4::text[]))
				ORDER BY user_id, run_id
				FOR UPDATE
			)
			UPDATE nori_task_leases AS t
			SET lease_until = now() + ($2::bigint * interval '1 microsecond'),
				updated_at = now()
			FROM targets
			WHERE t.user_id = targets.user_id AND t.run_id = targets.run_id
		`, owner, leaseMicros(lease), userIDs, runIDs)
		if err != nil {
			return err
		}
		renewed = int(tag.RowsAffected())
		return nil
	})
	return renewed, err
}

func (b *backend) ParkTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string) (bool, error) {
	if owner == "" {
		return false, nil
	}
	var parked bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		tag, err := q.Exec(ctx, `
			UPDATE nori_task_leases
			SET runnable = false,
				waiting_reason = $5,
				owner = '',
				lease_until = NULL,
				updated_at = now()
			WHERE user_id = $1
			  AND run_id = $2
			  AND owner = $3
			  AND owner <> ''
			  AND wake_seq = $4
			  AND lease_until IS NOT NULL
			  AND lease_until > now()
		`, userID, runID, owner, wakeSeq, reason)
		if err != nil {
			return err
		}
		parked = tag.RowsAffected() == 1
		return nil
	})
	return parked, err
}

func (b *backend) ReleaseTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, nextWake time.Time) error {
	if owner == "" {
		return nil
	}
	return b.readCommitted(ctx, false, func(q executor) error {
		if nextWake.IsZero() {
			_, err := q.Exec(ctx, `
				UPDATE nori_task_leases
				SET owner = '',
					lease_until = NULL,
					updated_at = now()
				WHERE user_id = $1 AND run_id = $2 AND owner = $3 AND owner <> ''
			`, userID, runID, owner)
			return err
		}
		_, err := q.Exec(ctx, `
			UPDATE nori_task_leases
			SET owner = '',
				lease_until = NULL,
				wake_at = CASE
					WHEN $4::bigint <= 0 OR wake_seq = $4::bigint THEN $5::timestamptz
					ELSE wake_at
				END,
				updated_at = now()
			WHERE user_id = $1 AND run_id = $2 AND owner = $3 AND owner <> ''
		`, userID, runID, owner, wakeSeq, nextWake.UTC())
		return err
	})
}

func (b *backend) DeleteTaskLease(ctx context.Context, userID, runID string, wakeSeq int64) (bool, error) {
	var deleted bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		if wakeSeq > 0 {
			tag, err := q.Exec(ctx, `
				DELETE FROM nori_task_leases
				WHERE user_id = $1 AND run_id = $2 AND wake_seq = $3
			`, userID, runID, wakeSeq)
			if err != nil {
				return err
			}
			deleted = tag.RowsAffected() == 1
			return nil
		}
		tag, err := q.Exec(ctx, `
			DELETE FROM nori_task_leases
			WHERE user_id = $1 AND run_id = $2
		`, userID, runID)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected() == 1
		return nil
	})
	return deleted, err
}

func (b *backend) EnsureTaskLease(ctx context.Context, userID, runID string, wakeAt time.Time) (bool, error) {
	if userID == "" || runID == "" {
		return false, errors.New("ensure task lease requires user_id and run_id")
	}
	now := time.Now().UTC()
	var created bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		tag, err := q.Exec(ctx, `
			INSERT INTO nori_task_leases (
				user_id, run_id, runnable, waiting_reason, owner, wake_seq, lease_until, wake_at, updated_at
			) VALUES (
				$1, $2, true, '', '', 1, NULL, COALESCE($3::timestamptz, now()), now()
			)
			ON CONFLICT (user_id, run_id) DO NOTHING
		`, userID, runID, nullableTime(wakeAt))
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	if created && (wakeAt.IsZero() || !wakeAt.After(now)) {
		b.emitWakes(ctx, []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.TaskWakePayload(userID, runID),
		}})
	}
	return created, nil
}

func (b *backend) ListTaskLeases(ctx context.Context, userID string) ([]docstore.TaskLease, error) {
	out := []docstore.TaskLease{}
	err := b.readCommitted(ctx, true, func(q executor) error {
		var rows pgx.Rows
		var err error
		if userID == "" {
			rows, err = q.Query(ctx, `
				SELECT user_id, run_id, runnable, waiting_reason, owner, wake_seq, lease_until, wake_at, updated_at
				FROM nori_task_leases
				ORDER BY user_id, run_id
			`)
		} else {
			rows, err = q.Query(ctx, `
				SELECT user_id, run_id, runnable, waiting_reason, owner, wake_seq, lease_until, wake_at, updated_at
				FROM nori_task_leases
				WHERE user_id = $1
				ORDER BY run_id
			`, userID)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l docstore.TaskLease
			if err := rows.Scan(
				&l.UserID,
				&l.RunID,
				&l.Runnable,
				&l.WaitingReason,
				&l.Owner,
				&l.WakeSeq,
				&l.LeaseUntil,
				&l.WakeAt,
				&l.UpdatedAt,
			); err != nil {
				return err
			}
			normalizeTaskLease(&l)
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

func (b *backend) ClaimUserWake(ctx context.Context, lane, owner string, lease time.Duration) (docstore.UserWake, bool, error) {
	if lane == "" || owner == "" {
		return docstore.UserWake{}, false, errors.New("claim user wake requires lane and owner")
	}
	var out docstore.UserWake
	var ok bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		out, ok, err = scanUserWake(q.QueryRow(ctx, `
			WITH candidate AS (
				SELECT user_id, lane
				FROM nori_user_wakes
				WHERE lane = $1
				  AND runnable = true
				  AND wake_at <= now()
				  AND (lease_until IS NULL OR lease_until <= now())
				ORDER BY wake_at, updated_at
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE nori_user_wakes AS u
			SET owner = $2,
				lease_until = now() + ($3::bigint * interval '1 microsecond'),
				updated_at = now()
			FROM candidate
			WHERE u.user_id = candidate.user_id AND u.lane = candidate.lane
			RETURNING u.user_id, u.lane, u.runnable, u.wake_seq, u.wake_at, u.owner, u.lease_until, u.updated_at
		`, lane, owner, leaseMicros(lease)))
		return err
	})
	return out, ok, err
}

func (b *backend) ClaimUserWakeForUser(ctx context.Context, userID, lane, owner string, lease time.Duration) (docstore.UserWake, bool, error) {
	if userID == "" || lane == "" || owner == "" {
		return docstore.UserWake{}, false, errors.New("claim user wake for user requires user_id, lane, and owner")
	}
	var out docstore.UserWake
	var ok bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		var err error
		out, ok, err = scanUserWake(q.QueryRow(ctx, `
			WITH candidate AS (
				SELECT user_id, lane
				FROM nori_user_wakes
				WHERE user_id = $1
				  AND lane = $2
				  AND runnable = true
				  AND wake_at <= now()
				  AND (lease_until IS NULL OR lease_until <= now())
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE nori_user_wakes AS u
			SET owner = $3,
				lease_until = now() + ($4::bigint * interval '1 microsecond'),
				updated_at = now()
			FROM candidate
			WHERE u.user_id = candidate.user_id AND u.lane = candidate.lane
			RETURNING u.user_id, u.lane, u.runnable, u.wake_seq, u.wake_at, u.owner, u.lease_until, u.updated_at
		`, userID, lane, owner, leaseMicros(lease)))
		return err
	})
	return out, ok, err
}

func (b *backend) RenewUserWake(ctx context.Context, userID, lane, owner string, lease time.Duration) (bool, error) {
	if userID == "" || lane == "" || owner == "" {
		return false, nil
	}
	var renewed bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		tag, err := q.Exec(ctx, `
			UPDATE nori_user_wakes
			SET lease_until = now() + ($4::bigint * interval '1 microsecond'),
				updated_at = now()
			WHERE user_id = $1
			  AND lane = $2
			  AND owner = $3
			  AND runnable = true
			  AND lease_until IS NOT NULL
			  AND lease_until > now()
		`, userID, lane, owner, leaseMicros(lease))
		if err != nil {
			return err
		}
		renewed = tag.RowsAffected() == 1
		return nil
	})
	return renewed, err
}

func (b *backend) ParkUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64) (bool, error) {
	if owner == "" {
		return false, nil
	}
	var parked bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		tag, err := q.Exec(ctx, `
			UPDATE nori_user_wakes
			SET runnable = false,
				owner = '',
				lease_until = NULL,
				updated_at = now()
			WHERE user_id = $1
			  AND lane = $2
			  AND owner = $3
			  AND owner <> ''
			  AND wake_seq = $4
			  AND lease_until IS NOT NULL
			  AND lease_until > now()
		`, userID, lane, owner, wakeSeq)
		if err != nil {
			return err
		}
		parked = tag.RowsAffected() == 1
		return nil
	})
	return parked, err
}

func (b *backend) ReleaseUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64, nextWake time.Time) error {
	if owner == "" {
		return nil
	}
	return b.readCommitted(ctx, false, func(q executor) error {
		if nextWake.IsZero() {
			_, err := q.Exec(ctx, `
				UPDATE nori_user_wakes
				SET owner = '',
					lease_until = NULL,
					updated_at = now()
				WHERE user_id = $1 AND lane = $2 AND owner = $3 AND owner <> ''
			`, userID, lane, owner)
			return err
		}
		_, err := q.Exec(ctx, `
			UPDATE nori_user_wakes
			SET owner = '',
				lease_until = NULL,
				wake_at = CASE
					WHEN $4::bigint <= 0 OR wake_seq = $4::bigint THEN $5::timestamptz
					ELSE LEAST(wake_at, $5::timestamptz)
				END,
				updated_at = now()
			WHERE user_id = $1 AND lane = $2 AND owner = $3 AND owner <> ''
		`, userID, lane, owner, wakeSeq, nextWake.UTC())
		return err
	})
}

func (b *backend) ListUserWakes(ctx context.Context, userID string) ([]docstore.UserWake, error) {
	out := []docstore.UserWake{}
	err := b.readCommitted(ctx, true, func(q executor) error {
		var rows pgx.Rows
		var err error
		if userID == "" {
			rows, err = q.Query(ctx, `
				SELECT user_id, lane, runnable, wake_seq, wake_at, owner, lease_until, updated_at
				FROM nori_user_wakes
				ORDER BY user_id, lane
			`)
		} else {
			rows, err = q.Query(ctx, `
				SELECT user_id, lane, runnable, wake_seq, wake_at, owner, lease_until, updated_at
				FROM nori_user_wakes
				WHERE user_id = $1
				ORDER BY lane
			`, userID)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w docstore.UserWake
			if err := rows.Scan(
				&w.UserID,
				&w.Lane,
				&w.Runnable,
				&w.WakeSeq,
				&w.WakeAt,
				&w.Owner,
				&w.LeaseUntil,
				&w.UpdatedAt,
			); err != nil {
				return err
			}
			normalizeUserWake(&w)
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func parseListenConfig(rawURL string) (*pgx.ConnConfig, error) {
	poolCfg, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		return nil, err
	}
	connCfg := poolCfg.ConnConfig
	if connCfg.RuntimeParams != nil {
		delete(connCfg.RuntimeParams, "statement_timeout")
	}
	return connCfg, nil
}

func connectListen(ctx context.Context, rawURL string) (*pgx.Conn, error) {
	connCfg, err := parseListenConfig(rawURL)
	if err != nil {
		return nil, err
	}
	return pgx.ConnectConfig(ctx, connCfg)
}

func closeConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

func (b *backend) ListenWakes(ctx context.Context, onEvent func(docstore.WakeEvent)) error {
	return b.listenWakes(ctx, onEvent, nil)
}

func (b *backend) ListenWakesReady(ctx context.Context, onEvent func(docstore.WakeEvent), ready chan<- struct{}) error {
	return b.listenWakes(ctx, onEvent, ready)
}

func (b *backend) listenWakes(ctx context.Context, onEvent func(docstore.WakeEvent), ready chan<- struct{}) error {
	url := b.listenEndpoint()
	if url == "" {
		return errors.New("postgres connection URL is not configured")
	}
	backoff := listenInitialBackoff
	connectedOnce := false
	limiter := newLogLimiter(logRateLimitInterval)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := connectListen(ctx, url)
		phase := "connect"
		if err == nil {
			phase = "listen"
			_, err = conn.Exec(ctx, "LISTEN "+docstore.WakeChannel)
			if err != nil {
				closeConn(conn)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.wakeErrors.Add(1)
			if emit, suppressed := limiter.allowWarn(time.Now()); emit {
				slog.WarnContext(ctx, "postgres wake listener error",
					"channel", docstore.WakeChannel,
					"phase", phase,
					"suppressed", suppressed,
					"err", err,
				)
			}
			if !sleepBackoff(ctx, backoff) {
				return ctx.Err()
			}
			backoff = min(backoff*2, listenMaxBackoff)
			continue
		}

		if connectedOnce {
			reconnects := b.wakeReconnects.Add(1)
			if wasFailing, suppressed := limiter.recover(); wasFailing {
				slog.InfoContext(ctx, "postgres wake listener recovered",
					"channel", docstore.WakeChannel,
					"reconnects", reconnects,
					"suppressed_errors", suppressed,
				)
			}
			if onEvent != nil {
				onEvent(docstore.WakeEvent{
					Channel: docstore.WakeChannel,
					Payload: docstore.WakePayloadResync,
				})
			}
		} else {
			if wasFailing, suppressed := limiter.recover(); wasFailing {
				slog.InfoContext(ctx, "postgres wake listener connected after errors",
					"channel", docstore.WakeChannel,
					"suppressed_errors", suppressed,
				)
			}
			if ready != nil {
				close(ready)
			}
		}
		connectedOnce = true

		connectedAt := time.Now()
		for {
			notification, err := conn.WaitForNotification(ctx)
			if err != nil {
				closeConn(conn)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				b.wakeErrors.Add(1)
				if emit, suppressed := limiter.allowWarn(time.Now()); emit {
					slog.WarnContext(ctx, "postgres wake listener error",
						"channel", docstore.WakeChannel,
						"phase", "wait",
						"suppressed", suppressed,
						"err", err,
					)
				}
				break
			}
			backoff = listenInitialBackoff
			if notification != nil && onEvent != nil {
				onEvent(docstore.WakeEvent{
					Channel: notification.Channel,
					Payload: notification.Payload,
				})
			}
		}
		if time.Since(connectedAt) >= listenMaxBackoff {
			backoff = listenInitialBackoff
		}
		if !sleepBackoff(ctx, backoff) {
			return ctx.Err()
		}
		backoff = min(backoff*2, listenMaxBackoff)
	}
}

func (b *backend) flushWakeWaitings(ctx context.Context, pending []pendingWakeWaiting) []docstore.WakeEvent {
	if len(pending) == 0 || b.pool == nil {
		return nil
	}
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wakeFlushTimeout)
	defer cancel()

	seen := make(map[pendingWakeWaiting]struct{}, len(pending))
	var all []docstore.WakeEvent
	var hadErr bool
	for _, p := range pending {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		var events []docstore.WakeEvent
		err := b.readCommitted(flushCtx, false, func(q executor) error {
			var err error
			events, err = wakeWaitingTasksSQL(flushCtx, q, p.userID, p.reason)
			return err
		})
		if err != nil {
			hadErr = true
			if emit, suppressed := flushWakeLogLimiter.allowWarn(time.Now()); emit {
				slog.WarnContext(ctx, "postgres post-commit WakeWaitingTasks failed",
					"reason", p.reason,
					"global", p.userID == "",
					"suppressed", suppressed,
					"err", err,
				)
			}
			continue
		}
		all = append(all, events...)
	}
	if !hadErr {
		flushWakeLogLimiter.recover()
	}
	return all
}

func (b *backend) QueueMetrics(ctx context.Context) (docstore.QueueMetrics, error) {
	if b == nil || b.pool == nil {
		return docstore.QueueMetrics{}, errors.New("postgres pool is not open")
	}
	var m docstore.QueueMetrics
	err := b.pool.QueryRow(ctx, `
		WITH task_stats AS (
			SELECT
				COUNT(*) FILTER (WHERE runnable = true AND (lease_until IS NULL OR lease_until <= now()))::bigint AS task_runnable,
				COUNT(*) FILTER (WHERE runnable = false)::bigint AS task_parked,
				COUNT(*) FILTER (WHERE owner <> '' AND lease_until > now())::bigint AS task_leased,
				GREATEST(
					0::double precision,
					COALESCE(
						EXTRACT(EPOCH FROM (
							now() - MIN(wake_at) FILTER (
								WHERE runnable = true
								  AND wake_at <= now()
								  AND (lease_until IS NULL OR lease_until <= now())
							)
						)),
						0
					)::double precision
				) AS task_oldest_lag_sec
			FROM nori_task_leases
		),
		wake_stats AS (
			SELECT
				COUNT(*) FILTER (WHERE lane = $1)::bigint AS conv_runnable,
				GREATEST(
					0::double precision,
					COALESCE(
						EXTRACT(EPOCH FROM (
							now() - MIN(wake_at) FILTER (
								WHERE lane = $1
								  AND wake_at <= now()
								  AND (lease_until IS NULL OR lease_until <= now())
							)
						)),
						0
					)::double precision
				) AS conv_oldest_lag_sec,
				COUNT(*) FILTER (WHERE lane = $2)::bigint AS sched_runnable,
				GREATEST(
					0::double precision,
					COALESCE(
						EXTRACT(EPOCH FROM (
							now() - MIN(wake_at) FILTER (
								WHERE lane = $2
								  AND wake_at <= now()
								  AND (lease_until IS NULL OR lease_until <= now())
							)
						)),
						0
					)::double precision
				) AS sched_oldest_lag_sec
			FROM nori_user_wakes
			WHERE runnable = true
		)
		SELECT
			t.task_runnable,
			t.task_parked,
			t.task_leased,
			t.task_oldest_lag_sec,
			w.conv_runnable,
			w.conv_oldest_lag_sec,
			w.sched_runnable,
			w.sched_oldest_lag_sec
		FROM task_stats t
		CROSS JOIN wake_stats w
	`, docstore.LaneConversation, docstore.LaneScheduler).Scan(
		&m.TaskRunnable,
		&m.TaskParked,
		&m.TaskLeased,
		&m.TaskOldestLagSec,
		&m.ConvRunnable,
		&m.ConvOldestLagSec,
		&m.SchedRunnable,
		&m.SchedOldestLagSec,
	)
	if err != nil {
		return docstore.QueueMetrics{}, err
	}
	return m, nil
}

func sleepBackoff(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
