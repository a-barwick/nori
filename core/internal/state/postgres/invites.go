package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"nori/internal/users"
)

const (
	usersCollection   = "users"
	invitesCollection = "invites"
)

var (
	_ users.AutoProvisioner = (*backend)(nil)
	_ users.InviteStore     = (*backend)(nil)
)

func (b *backend) FindOrCreateByEmail(ctx context.Context, email, displayName string) (users.User, bool, error) {
	normalized := users.Email(email)
	candidate := users.User{
		ID:          uuid.NewString(),
		Email:       normalized,
		DisplayName: strings.TrimSpace(displayName),
		TimeZone:    "UTC",
	}
	if err := candidate.Validate(); err != nil {
		return users.User{}, false, err
	}

	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return users.User{}, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "nori:user:"+normalized); err != nil {
		return users.User{}, false, err
	}

	existing, found, err := findUserByEmailTx(ctx, tx, normalized)
	if err != nil {
		return users.User{}, false, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return users.User{}, false, err
		}
		return existing, false, nil
	}

	raw, err := json.Marshal(candidate)
	if err != nil {
		return users.User{}, false, err
	}
	_, err = tx.Exec(
		ctx,
		"INSERT INTO nori_records(shard,collection,id,data) VALUES($1,$2,$3,$4::jsonb)",
		usersCollection, usersCollection, candidate.ID, raw,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			existing, found, lookupErr := findUserByEmailPool(ctx, b.pool, normalized)
			if lookupErr == nil && found {
				return existing, false, nil
			}
		}
		return users.User{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return users.User{}, false, err
	}
	return candidate, true, nil
}

func (b *backend) CreateInvite(ctx context.Context, opts users.CreateInviteOptions) (users.Invite, error) {
	inv, err := users.PrepareInvite(opts, time.Now().UTC())
	if err != nil {
		return users.Invite{}, err
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		return users.Invite{}, err
	}
	_, err = b.pool.Exec(
		ctx,
		"INSERT INTO nori_records(shard,collection,id,data) VALUES($1,$2,$3,$4::jsonb)",
		invitesCollection, invitesCollection, inv.Code, raw,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return users.Invite{}, fmt.Errorf("%w: invite code already exists", users.ErrInviteInvalid)
		}
		return users.Invite{}, err
	}
	return inv, nil
}

func (b *backend) ListInvites(ctx context.Context) ([]users.Invite, error) {
	rows, err := b.pool.Query(
		ctx,
		"SELECT data FROM nori_records WHERE shard=$1 AND collection=$2 ORDER BY (data->>'created_at') DESC, id ASC",
		invitesCollection, invitesCollection,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []users.Invite{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var inv users.Invite
		if err := json.Unmarshal(raw, &inv); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (b *backend) RevokeInvite(ctx context.Context, code string) (users.Invite, error) {
	normalized := users.NormalizeInviteCode(code)
	if normalized == "" {
		return users.Invite{}, users.ErrInviteNotFound
	}

	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return users.Invite{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var raw []byte
	err = tx.QueryRow(
		ctx,
		"SELECT data FROM nori_records WHERE shard=$1 AND collection=$2 AND id=$3 FOR UPDATE",
		invitesCollection, invitesCollection, normalized,
	).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return users.Invite{}, users.ErrInviteNotFound
	}
	if err != nil {
		return users.Invite{}, err
	}

	var inv users.Invite
	if err := json.Unmarshal(raw, &inv); err != nil {
		return users.Invite{}, err
	}
	if inv.RevokedAt == nil {
		now := time.Now().UTC()
		inv.RevokedAt = &now
		updated, err := json.Marshal(inv)
		if err != nil {
			return users.Invite{}, err
		}
		if _, err := tx.Exec(
			ctx,
			"UPDATE nori_records SET data=$4::jsonb WHERE shard=$1 AND collection=$2 AND id=$3",
			invitesCollection, invitesCollection, normalized, updated,
		); err != nil {
			return users.Invite{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return users.Invite{}, err
	}
	return inv, nil
}

func (b *backend) RedeemInviteAndCreateUser(ctx context.Context, code, email, displayName string) (users.User, users.Invite, error) {
	normalizedCode := users.NormalizeInviteCode(code)
	normalizedEmail := users.Email(email)
	candidate := users.User{
		ID:          uuid.NewString(),
		Email:       normalizedEmail,
		DisplayName: strings.TrimSpace(displayName),
		TimeZone:    "UTC",
	}
	if err := candidate.Validate(); err != nil {
		return users.User{}, users.Invite{}, err
	}
	if normalizedCode == "" {
		return users.User{}, users.Invite{}, users.ErrInviteNotFound
	}

	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return users.User{}, users.Invite{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "nori:user:"+normalizedEmail); err != nil {
		return users.User{}, users.Invite{}, err
	}

	existing, found, err := findUserByEmailTx(ctx, tx, normalizedEmail)
	if err != nil {
		return users.User{}, users.Invite{}, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return users.User{}, users.Invite{}, err
		}
		return existing, users.Invite{}, nil
	}

	var rawInvite []byte
	err = tx.QueryRow(
		ctx,
		"SELECT data FROM nori_records WHERE shard=$1 AND collection=$2 AND id=$3 FOR UPDATE",
		invitesCollection, invitesCollection, normalizedCode,
	).Scan(&rawInvite)
	if errors.Is(err, pgx.ErrNoRows) {
		return users.User{}, users.Invite{}, users.ErrInviteNotFound
	}
	if err != nil {
		return users.User{}, users.Invite{}, err
	}

	var inv users.Invite
	if err := json.Unmarshal(rawInvite, &inv); err != nil {
		return users.User{}, users.Invite{}, err
	}
	now := time.Now().UTC()
	if err := inv.ValidateForRedemption(normalizedEmail, now); err != nil {
		return users.User{}, users.Invite{}, err
	}

	inv.UseCount++
	inv.LastUsedAt = &now
	updatedInvite, err := json.Marshal(inv)
	if err != nil {
		return users.User{}, users.Invite{}, err
	}
	if _, err := tx.Exec(
		ctx,
		"UPDATE nori_records SET data=$4::jsonb WHERE shard=$1 AND collection=$2 AND id=$3",
		invitesCollection, invitesCollection, normalizedCode, updatedInvite,
	); err != nil {
		return users.User{}, users.Invite{}, err
	}

	rawUser, err := json.Marshal(candidate)
	if err != nil {
		return users.User{}, users.Invite{}, err
	}
	if _, err := tx.Exec(
		ctx,
		"INSERT INTO nori_records(shard,collection,id,data) VALUES($1,$2,$3,$4::jsonb)",
		usersCollection, usersCollection, candidate.ID, rawUser,
	); err != nil {
		return users.User{}, users.Invite{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return users.User{}, users.Invite{}, err
	}
	return candidate, inv, nil
}

func findUserByEmailTx(ctx context.Context, q executor, normalizedEmail string) (users.User, bool, error) {
	var raw []byte
	err := q.QueryRow(
		ctx,
		"SELECT data FROM nori_records WHERE shard=$1 AND collection=$2 AND data->>'email'=$3 LIMIT 1",
		usersCollection, usersCollection, normalizedEmail,
	).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return users.User{}, false, nil
	}
	if err != nil {
		return users.User{}, false, err
	}
	var row users.User
	if err := json.Unmarshal(raw, &row); err != nil {
		return users.User{}, false, err
	}
	if err := row.Validate(); err != nil {
		return users.User{}, false, err
	}
	return row, true, nil
}

func findUserByEmailPool(ctx context.Context, q executor, normalizedEmail string) (users.User, bool, error) {
	return findUserByEmailTx(ctx, q, normalizedEmail)
}
