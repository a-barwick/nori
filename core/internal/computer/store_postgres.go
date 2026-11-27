package computer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func (s *PostgresStore) GetComputer(ctx context.Context, userID string) (ComputerRecord, error) {
	var rec ComputerRecord
	var status string
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, computer_id, provider, provider_id, status,
		       baseline_checkpoint_id, bootstrap_version, last_active_at, created_at, updated_at
		FROM nori_user_computers
		WHERE user_id = $1`, userID).Scan(
		&rec.UserID, &rec.ComputerID, &rec.Provider, &rec.ProviderID, &status,
		&rec.BaselineCheckpointID, &rec.BootstrapVersion, &rec.LastActiveAt, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ComputerRecord{}, ErrNotFound
	}
	if err != nil {
		return ComputerRecord{}, err
	}
	rec.Status = Status(status)
	return rec, nil
}

func (s *PostgresStore) ReserveComputer(ctx context.Context, rec ComputerRecord) (ComputerRecord, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ComputerRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-computer:"+rec.UserID); err != nil {
		return ComputerRecord{}, false, err
	}
	var deletePending bool
	_ = tx.QueryRow(ctx, `SELECT COALESCE((data->'delete_pending') = 'true'::jsonb, false)
		FROM nori_records WHERE shard = 'users' AND collection = 'users' AND id = $1`, rec.UserID).Scan(&deletePending)
	if deletePending {
		return ComputerRecord{}, false, ErrDeleting
	}
	var existing ComputerRecord
	var statusStr string
	qErr := tx.QueryRow(ctx, `
		SELECT user_id, computer_id, provider, provider_id, status,
		       baseline_checkpoint_id, bootstrap_version, last_active_at, created_at, updated_at
		FROM nori_user_computers
		WHERE user_id = $1 FOR UPDATE`, rec.UserID).Scan(
		&existing.UserID, &existing.ComputerID, &existing.Provider, &existing.ProviderID,
		&statusStr, &existing.BaselineCheckpointID, &existing.BootstrapVersion,
		&existing.LastActiveAt, &existing.CreatedAt, &existing.UpdatedAt,
	)
	if qErr == nil {
		existing.Status = Status(statusStr)
		if existing.Status == StatusDeleting {
			return existing, false, ErrDeleting
		}
		if existing.Status != StatusDeleted {
			if err := tx.Commit(ctx); err != nil {
				return ComputerRecord{}, false, err
			}
			return existing, false, nil
		}
	} else if !errors.Is(qErr, pgx.ErrNoRows) {
		return ComputerRecord{}, false, qErr
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO nori_user_computers (
			user_id, computer_id, provider, provider_id, status,
			baseline_checkpoint_id, bootstrap_version, last_active_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, clock_timestamp(), clock_timestamp())
		ON CONFLICT (user_id) DO UPDATE SET
			computer_id = EXCLUDED.computer_id,
			provider = EXCLUDED.provider,
			provider_id = EXCLUDED.provider_id,
			status = EXCLUDED.status,
			baseline_checkpoint_id = EXCLUDED.baseline_checkpoint_id,
			bootstrap_version = EXCLUDED.bootstrap_version,
			last_active_at = COALESCE(EXCLUDED.last_active_at, nori_user_computers.last_active_at),
			updated_at = clock_timestamp()`,
		rec.UserID, rec.ComputerID, rec.Provider, rec.ProviderID, string(rec.Status),
		rec.BaselineCheckpointID, rec.BootstrapVersion, rec.LastActiveAt,
	)
	if err != nil {
		return ComputerRecord{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ComputerRecord{}, false, err
	}
	return rec, true, nil
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
	_, err = tx.Exec(ctx, `
		INSERT INTO nori_user_computers (
			user_id, computer_id, provider, provider_id, status,
			baseline_checkpoint_id, bootstrap_version, last_active_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, clock_timestamp(), clock_timestamp())
		ON CONFLICT (user_id) DO UPDATE SET
			computer_id = EXCLUDED.computer_id,
			provider = EXCLUDED.provider,
			provider_id = EXCLUDED.provider_id,
			status = EXCLUDED.status,
			baseline_checkpoint_id = EXCLUDED.baseline_checkpoint_id,
			bootstrap_version = EXCLUDED.bootstrap_version,
			last_active_at = COALESCE(EXCLUDED.last_active_at, nori_user_computers.last_active_at),
			updated_at = clock_timestamp()`,
		rec.UserID, rec.ComputerID, rec.Provider, rec.ProviderID, string(rec.Status),
		rec.BaselineCheckpointID, rec.BootstrapVersion, rec.LastActiveAt,
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

func (s *PostgresStore) MarkComputerDeleting(ctx context.Context, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "nori-computer:"+userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE nori_user_computers SET status = 'deleting', updated_at = clock_timestamp() WHERE user_id = $1 AND status <> 'deleted'`, userID); err != nil {
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
	if _, err := tx.Exec(ctx, `UPDATE nori_user_computers SET status = 'deleted', updated_at = clock_timestamp() WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ListDeletingComputers(ctx context.Context, limit int) ([]ComputerRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, computer_id, provider, provider_id, status,
		       baseline_checkpoint_id, bootstrap_version, last_active_at, created_at, updated_at
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
		var rec ComputerRecord
		var status string
		if err := rows.Scan(
			&rec.UserID, &rec.ComputerID, &rec.Provider, &rec.ProviderID, &status,
			&rec.BaselineCheckpointID, &rec.BootstrapVersion, &rec.LastActiveAt, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, err
		}
		rec.Status = Status(status)
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
	res, err := s.pool.Exec(ctx, `
		UPDATE nori_computer_sessions
		SET last_activity_at = $2,
		    expires_at = GREATEST(expires_at, $3)
		WHERE id = $1 AND ended_at IS NULL`,
		sessionID, activityAt.UTC(), newExpiresAt.UTC(),
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrSessionRevoked
	}
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
