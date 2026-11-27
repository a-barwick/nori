package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"nori/internal/state/docstore"
)

func (b *backend) IsAccountDeletePending(ctx context.Context, userID string) (bool, error) {
	var pending bool
	err := b.pool.QueryRow(ctx, `SELECT COALESCE((data->'delete_pending') = 'true'::jsonb, false)
		FROM nori_records
		WHERE shard = 'users' AND collection = 'users' AND id = $1`, userID).Scan(&pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, docstore.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return pending, nil
}

func (b *backend) ListAccountDeletePending(ctx context.Context) ([]string, error) {
	rows, err := b.pool.Query(ctx, `SELECT id FROM nori_records
		WHERE shard = 'users' AND collection = 'users' AND (data->'delete_pending') = 'true'::jsonb
		ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (b *backend) BeginAccountDelete(ctx context.Context, userID string) error {
	return b.readCommitted(ctx, false, func(q executor) error {
		// Pairing, redemption, and computer provisioning use these locks, so no device or VM
		// can be authorized/provisioned concurrently after deletion is marked pending.
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-home-ai:"+userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-computer:"+userID); err != nil {
			return err
		}
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
		result, err := q.Exec(ctx, `UPDATE nori_records SET data = jsonb_set(data, '{delete_pending}', 'true')
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
		if _, err := q.Exec(ctx, "DELETE FROM nori_user_wakes WHERE user_id = $1", userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE nori_user_computers SET status = 'deleting', updated_at = clock_timestamp()
			WHERE user_id = $1 AND status <> 'deleted'`, userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE nori_computer_sessions SET ended_at = COALESCE(ended_at, clock_timestamp())
			WHERE user_id = $1 AND ended_at IS NULL`, userID); err != nil {
			return err
		}
		for _, query := range []string{
			`SELECT pg_notify('nori_home_ai_events', 'd:' || id) FROM nori_home_ai_devices WHERE user_id = $1`,
			`SELECT pg_notify('nori_home_ai_events', 'r:' || id) FROM nori_home_ai_requests WHERE user_id = $1`,
		} {
			if _, err := q.Exec(ctx, query, userID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *backend) FinishAccountDelete(ctx context.Context, userID, owner string) error {
	shard := "owners/" + owner
	return b.readCommitted(ctx, false, func(q executor) error {
		var marker int
		err := q.QueryRow(ctx, `SELECT 1 FROM nori_records
			WHERE shard = 'users' AND collection = 'users' AND id = $1 AND data->'delete_pending' = 'true'
			FOR UPDATE`, userID).Scan(&marker)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("user %s has no pending account deletion", userID)
		}
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "SELECT nori_purge_home_ai_account($1)", userID); err != nil {
			return err
		}
		var undestroyed int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM nori_user_computers WHERE user_id = $1 AND status <> 'deleted'`, userID).Scan(&undestroyed); err != nil {
			return err
		}
		if undestroyed > 0 {
			return fmt.Errorf("user %s computer has not completed remote destruction", userID)
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_computer_sessions WHERE user_id = $1", userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_computer_file_links WHERE user_id = $1", userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_user_computers WHERE user_id = $1 AND status = 'deleted'", userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_records WHERE shard = $1", shard); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_sequences WHERE path LIKE $1", shard+"/%"); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_memory_chunks WHERE owner = $1", owner); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_task_leases WHERE user_id = $1", userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DELETE FROM nori_user_wakes WHERE user_id = $1", userID); err != nil {
			return err
		}
		result, err := q.Exec(ctx, `DELETE FROM nori_records
			WHERE shard = 'users' AND collection = 'users' AND id = $1 AND data->'delete_pending' = 'true'`, userID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("user %s directory row could not be deleted", userID)
		}
		return nil
	})
}
