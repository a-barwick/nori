package computer_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/computer"
	"nori/internal/computer/fake"
	"nori/internal/state/postgres"
)

func TestPostgresStoreAndAccountDeleteIntegration(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Opening the postgres docstore backend runs all embedded migrations including 014_user_computers.sql.
	rawBackend, err := postgres.OpenBackend(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenBackend: %v", err)
	}
	t.Cleanup(func() { _ = rawBackend.Close() })

	store, err := computer.OpenPostgresStore(ctx, dsn, 4)
	if err != nil {
		t.Fatalf("OpenPostgresStore: %v", err)
	}
	t.Cleanup(store.Close)

	fp := fake.New()
	mgr := computer.NewManager(fp, store, computer.Config{MemoryLimitMB: 3072, MaxDiskGB: 10})
	userID := uuid.NewString()

	rec, err := mgr.Ensure(ctx, userID)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if rec.ProviderID == "" || rec.BaselineCheckpointID == "" {
		t.Fatalf("unexpected computer record: %+v", rec)
	}

	// Create session, toggle mode, validate, and end.
	created, err := mgr.CreateSession(ctx, userID, computer.SessionModeView, "task-pg-1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	validated, err := mgr.ValidateSession(ctx, userID, created.Session.ID, created.RawToken)
	if err != nil || validated.Mode != computer.SessionModeView {
		t.Fatalf("ValidateSession = (%+v, %v)", validated, err)
	}
	if _, err := mgr.SetSessionMode(ctx, userID, created.Session.ID, computer.SessionModeControl); err != nil {
		t.Fatalf("SetSessionMode: %v", err)
	}

	// Link workspace file to a Library result and entity tag, then purge by result ID.
	if err := mgr.WriteFile(ctx, userID, "out/report.pdf", []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := mgr.LinkFile(ctx, userID, "out/report.pdf", "res-pg-1", []string{"Project Alpha"}); err != nil {
		t.Fatalf("LinkFile: %v", err)
	}
	if err := mgr.PurgeLinkedResultFiles(ctx, userID, "res-pg-1"); err != nil {
		t.Fatalf("PurgeLinkedResultFiles: %v", err)
	}
	if _, err := mgr.ReadFile(ctx, userID, "out/report.pdf", 1024); !errors.Is(err, computer.ErrNotFound) {
		t.Fatalf("expected file purged from workspace, got %v", err)
	}

	// Simulate account delete teardown + reaper recovery.
	fp.Faults.DestroyErr = errors.New("temporary network blip")
	if err := mgr.DestroyForAccountDelete(ctx, userID); err == nil {
		t.Fatalf("expected DestroyForAccountDelete error under fault")
	}
	// Session must already be revoked when marked deleting.
	if _, err := mgr.ValidateSession(ctx, userID, created.Session.ID, created.RawToken); !errors.Is(err, computer.ErrSessionRevoked) {
		t.Fatalf("expected ErrSessionRevoked after MarkComputerDeleting, got %v", err)
	}
	// Clear fault and reap.
	fp.Faults.DestroyErr = nil
	if err := mgr.ReapDeletingComputers(ctx); err != nil {
		t.Fatalf("ReapDeletingComputers: %v", err)
	}
	after, err := store.GetComputer(ctx, userID)
	if err != nil || after.Status != computer.StatusDeleted {
		t.Fatalf("after reap = (%+v, %v), want StatusDeleted", after, err)
	}
}
