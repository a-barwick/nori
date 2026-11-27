package push

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore backs push device tokens and the idempotent push outbox in Postgres.
type PostgresStore struct {
	pool    *pgxpool.Pool
	ownPool bool
}

// NewPostgresStore wraps an existing pgxpool.Pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// OpenPostgresStore opens a dedicated pool for push device and outbox tables.
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

func (s *PostgresStore) RegisterDevice(ctx context.Context, userID, token, deviceID, env string) (Device, error) {
	var err error
	userID, token, deviceID, env, err = normalizeRegisterArgs(userID, token, deviceID, env)
	if err != nil {
		return Device{}, err
	}

	newID := uuid.NewString()
	var dev Device
	err = s.pool.QueryRow(ctx, `
		INSERT INTO nori_push_devices (id, user_id, device_id, token, environment, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp(), clock_timestamp())
		ON CONFLICT (token) DO UPDATE SET
			id = CASE WHEN nori_push_devices.user_id = EXCLUDED.user_id THEN nori_push_devices.id ELSE EXCLUDED.id END,
			user_id = EXCLUDED.user_id,
			device_id = EXCLUDED.device_id,
			environment = EXCLUDED.environment,
			created_at = CASE WHEN nori_push_devices.user_id = EXCLUDED.user_id THEN nori_push_devices.created_at ELSE clock_timestamp() END,
			updated_at = clock_timestamp()
		RETURNING id, user_id, device_id, token, environment, created_at, updated_at`,
		newID, userID, deviceID, token, env,
	).Scan(
		&dev.ID, &dev.UserID, &dev.DeviceID, &dev.Token, &dev.Environment, &dev.CreatedAt, &dev.UpdatedAt,
	)
	if err != nil {
		return Device{}, err
	}
	return dev, nil
}

func (s *PostgresStore) DeleteDevice(ctx context.Context, userID, id string) error {
	userID = strings.TrimSpace(userID)
	id = strings.TrimSpace(id)
	if userID == "" || id == "" {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM nori_push_devices WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteDeviceByToken(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM nori_push_devices WHERE token = $1`, token)
	return err
}

func (s *PostgresStore) DeleteUserDevices(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM nori_push_devices WHERE user_id = $1`, userID)
	return err
}

func (s *PostgresStore) ListDevices(ctx context.Context, userID string) ([]Device, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, device_id, token, environment, created_at, updated_at
		FROM nori_push_devices
		WHERE user_id = $1
		ORDER BY updated_at DESC, id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var dev Device
		if err := rows.Scan(&dev.ID, &dev.UserID, &dev.DeviceID, &dev.Token, &dev.Environment, &dev.CreatedAt, &dev.UpdatedAt); err != nil {
			return nil, err
		}
		devices = append(devices, dev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return devices, nil
}

func (s *PostgresStore) EnqueueOutbox(ctx context.Context, item OutboxItem) (OutboxItem, bool, error) {
	item.DeliveryID = strings.TrimSpace(item.DeliveryID)
	item.UserID = strings.TrimSpace(item.UserID)
	if item.DeliveryID == "" {
		return OutboxItem{}, false, ErrInvalidDeliveryID
	}
	if item.UserID == "" {
		return OutboxItem{}, false, ErrInvalidUserID
	}
	if item.CollapseID == "" {
		item.CollapseID = item.DeliveryID
	}
	if item.Status == "" {
		item.Status = StatusPending
	}

	var inserted OutboxItem
	var status string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO nori_push_outbox (
			delivery_id, user_id, interaction_id, collapse_id, title, body,
			status, attempts, last_error, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 0, '', clock_timestamp(), clock_timestamp())
		ON CONFLICT (delivery_id) DO NOTHING
		RETURNING delivery_id, user_id, interaction_id, collapse_id, title, body,
		          status, attempts, last_error, sent_at, created_at, updated_at`,
		item.DeliveryID, item.UserID, item.InteractionID, item.CollapseID, item.Title, item.Body, string(item.Status),
	).Scan(
		&inserted.DeliveryID, &inserted.UserID, &inserted.InteractionID, &inserted.CollapseID,
		&inserted.Title, &inserted.Body, &status, &inserted.Attempts, &inserted.LastError,
		&inserted.SentAt, &inserted.CreatedAt, &inserted.UpdatedAt,
	)
	if err == nil {
		inserted.Status = OutboxStatus(status)
		return inserted, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return OutboxItem{}, false, err
	}
	existing, err := s.GetOutbox(ctx, item.DeliveryID)
	if err != nil {
		return OutboxItem{}, false, err
	}
	return existing, false, nil
}

func (s *PostgresStore) GetOutbox(ctx context.Context, deliveryID string) (OutboxItem, error) {
	var item OutboxItem
	var status string
	err := s.pool.QueryRow(ctx, `
		SELECT delivery_id, user_id, interaction_id, collapse_id, title, body,
		       status, attempts, last_error, sent_at, created_at, updated_at
		FROM nori_push_outbox
		WHERE delivery_id = $1`, strings.TrimSpace(deliveryID),
	).Scan(
		&item.DeliveryID, &item.UserID, &item.InteractionID, &item.CollapseID,
		&item.Title, &item.Body, &status, &item.Attempts, &item.LastError,
		&item.SentAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutboxItem{}, ErrNotFound
	}
	if err != nil {
		return OutboxItem{}, err
	}
	item.Status = OutboxStatus(status)
	return item, nil
}

func (s *PostgresStore) ListPendingOutbox(ctx context.Context, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT delivery_id, user_id, interaction_id, collapse_id, title, body,
		       status, attempts, last_error, sent_at, created_at, updated_at
		FROM nori_push_outbox
		WHERE status = 'pending'
		ORDER BY created_at ASC, delivery_id ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []OutboxItem
	for rows.Next() {
		var item OutboxItem
		var status string
		if err := rows.Scan(
			&item.DeliveryID, &item.UserID, &item.InteractionID, &item.CollapseID,
			&item.Title, &item.Body, &status, &item.Attempts, &item.LastError,
			&item.SentAt, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Status = OutboxStatus(status)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *PostgresStore) MarkOutboxSent(ctx context.Context, deliveryID string, sentAt time.Time) error {
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE nori_push_outbox
		SET status = 'sent',
		    attempts = attempts + 1,
		    last_error = '',
		    sent_at = $2,
		    updated_at = $2
		WHERE delivery_id = $1`, deliveryID, sentAt.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) MarkOutboxFailed(ctx context.Context, deliveryID string, lastErr string, permanent bool) error {
	nextStatus := string(StatusPending)
	if permanent {
		nextStatus = string(StatusFailed)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE nori_push_outbox
		SET status = $2,
		    attempts = attempts + 1,
		    last_error = $3,
		    updated_at = clock_timestamp()
		WHERE delivery_id = $1`, deliveryID, nextStatus, lastErr)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) MarkOutboxSuppressed(ctx context.Context, deliveryID string, reason string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE nori_push_outbox
		SET status = 'suppressed',
		    last_error = $2,
		    updated_at = clock_timestamp()
		WHERE delivery_id = $1`, deliveryID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) CountSentSince(ctx context.Context, userID string, since time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM nori_push_outbox
		WHERE user_id = $1
		  AND status = 'sent'
		  AND sent_at IS NOT NULL
		  AND sent_at >= $2`, userID, since.UTC()).Scan(&count)
	return count, err
}
