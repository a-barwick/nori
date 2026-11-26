package postgres

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

var _ docstore.UserDataResetter = (*backend)(nil)

func (b *backend) LockUserData(ctx context.Context, userID string, exclusive bool) (func(), error) {
	// The protected work also needs connections from the pool. Reserve at
	// least half of the pool for that work and use a separate connection if
	// the pool is configured to have a single connection.
	select {
	case b.userDataLocks <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	releaseSlot := func() { <-b.userDataLocks }
	begin := b.pool.Begin
	closeConn := func(context.Context) {}
	if b.pool.Config().MaxConns == 1 {
		conn, err := pgx.ConnectConfig(ctx, b.pool.Config().ConnConfig)
		if err != nil {
			releaseSlot()
			return nil, err
		}
		begin = conn.Begin
		closeConn = func(ctx context.Context) { _ = conn.Close(ctx) }
	}
	tx, err := begin(ctx)
	if err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		closeConn(closeCtx)
		releaseSlot()
		return nil, err
	}
	release := sync.OnceFunc(func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(closeCtx)
		closeConn(closeCtx)
		releaseSlot()
	})
	query := "SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1, 0))"
	if exclusive {
		query = "SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))"
	}
	var locked bool
	if err := tx.QueryRow(ctx, query, "nori-user-data:"+userID).Scan(&locked); err != nil {
		release()
		return nil, err
	}
	if !locked {
		release()
		return nil, docstore.ErrUserDataBusy
	}
	return release, nil
}

func (b *backend) BeginUserDataReset(ctx context.Context, userID string) error {
	return b.readCommitted(ctx, false, func(q executor) error {
		// Lock all existing claims before deciding the user is idle. Workers
		// skip locked rows when claiming work, including on other replicas.
		for _, query := range []string{
			`WITH locked AS MATERIALIZED
				(SELECT owner FROM nori_task_leases WHERE user_id = $1 FOR UPDATE)
				SELECT EXISTS (SELECT 1 FROM locked WHERE owner <> '')`,
			`WITH locked AS MATERIALIZED
				(SELECT owner FROM nori_user_wakes WHERE user_id = $1 FOR UPDATE)
				SELECT EXISTS (SELECT 1 FROM locked WHERE owner <> '')`,
		} {
			var active bool
			if err := q.QueryRow(ctx, query, userID).Scan(&active); err != nil {
				return err
			}
			if active {
				return docstore.ErrUserDataBusy
			}
		}
		result, err := q.Exec(ctx, `UPDATE nori_records SET data = jsonb_set(data, '{reset_pending}', 'true')
			WHERE shard = 'users' AND collection = 'users' AND id = $1`, userID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return docstore.ErrNotFound
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_task_leases WHERE user_id = $1", userID); err != nil {
			return err
		}
		_, err = q.Exec(ctx, "DELETE FROM nori_user_wakes WHERE user_id = $1", userID)
		return err
	})
}

func (b *backend) FinishUserDataReset(ctx context.Context, userID, owner string) error {
	shard := "owners/" + owner
	events := shard + "/events"
	return b.readCommitted(ctx, false, func(q executor) error {
		result, err := q.Exec(ctx, `UPDATE nori_records
			SET data = (data - 'reset_pending') || jsonb_build_object(
				'data_reset_at', clock_timestamp(), 'display_name', '', 'time_zone', 'UTC')
			WHERE shard = 'users' AND collection = 'users' AND id = $1 AND data->'reset_pending' = 'true'`, userID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("user %s has no pending reset", userID)
		}
		if _, err := q.Exec(ctx, "SELECT nori_purge_home_ai_account($1)", userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_records WHERE shard = $1 AND owner_id = $2", shard, owner); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_memory_chunks WHERE owner = $1", owner); err != nil {
			return err
		}
		seq, err := advanceSequence(ctx, q, shard+"/event_head/head", 1)
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `INSERT INTO nori_records(shard, collection, id, data) VALUES
			($1, $2, $3, jsonb_build_object('seq', $4::bigint, 'kind', $5::text, 'created_at', clock_timestamp()))`,
			shard, events, fmt.Sprintf("%020d", seq), seq, state.EventDataReset)
		return err
	})
}

func (b *backend) PublishUserDataReset(ctx context.Context, owner string) {
	b.emitWatchCollections(ctx, []string{"owners/" + owner + "/events"})
}
