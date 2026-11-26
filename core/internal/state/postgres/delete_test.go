package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/state/docstore"
)

func TestAccountDeletePostgresBackend(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	raw, err := OpenBackend(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	b := raw.(*backend)
	t.Cleanup(func() { _ = b.Close() })

	userID := uuid.NewString()
	shard := "owners/" + userID
	if _, err := b.pool.Exec(ctx, `INSERT INTO nori_records(shard, collection, id, data) VALUES
		('users', 'users', $1, jsonb_build_object('id', $1::text, 'email', $2::text, 'time_zone', 'UTC')),
		($3, $4, 'doc-1', '{"hello":"world"}'::jsonb)`,
		userID, "pg-delete-"+userID+"@example.com", shard, shard+"/messages"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pool.Exec(ctx, `INSERT INTO nori_task_leases(user_id, run_id, owner, runnable, wake_at)
		VALUES ($1, 'run-1', 'worker-a', true, now())`, userID); err != nil {
		t.Fatal(err)
	}

	// Active lease blocks BeginAccountDelete.
	if err := b.BeginAccountDelete(ctx, userID); !errors.Is(err, docstore.ErrUserDataBusy) {
		t.Fatalf("BeginAccountDelete with active lease = %v, want ErrUserDataBusy", err)
	}

	// Release lease; BeginAccountDelete succeeds and sets delete_pending.
	if _, err := b.pool.Exec(ctx, `UPDATE nori_task_leases SET owner = '' WHERE user_id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if err := b.BeginAccountDelete(ctx, userID); err != nil {
		t.Fatalf("BeginAccountDelete = %v", err)
	}
	pending, err := b.IsAccountDeletePending(ctx, userID)
	if err != nil || !pending {
		t.Fatalf("IsAccountDeletePending = (%v, %v), want (true, nil)", pending, err)
	}

	// FinishAccountDelete deletes owner records, leases, wakes, and the users directory row.
	if err := b.FinishAccountDelete(ctx, userID, userID); err != nil {
		t.Fatalf("FinishAccountDelete = %v", err)
	}
	var remaining int
	if err := b.pool.QueryRow(ctx, `SELECT count(*) FROM nori_records WHERE shard = $1 OR (shard = 'users' AND collection = 'users' AND id = $2)`, shard, userID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("remaining nori_records = %d, want 0", remaining)
	}
	if _, err := b.IsAccountDeletePending(ctx, userID); !errors.Is(err, docstore.ErrNotFound) {
		t.Fatalf("IsAccountDeletePending after finish = %v, want ErrNotFound", err)
	}
}
