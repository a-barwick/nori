package push_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"nori/internal/push"
	"nori/internal/state/postgres"
)

func TestPostgresStorePushDevicesAndOutbox(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Opening the postgres docstore backend runs embedded migrations including 015_push_devices.sql.
	rawBackend, err := postgres.OpenBackend(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenBackend: %v", err)
	}
	t.Cleanup(func() { _ = rawBackend.Close() })

	store, err := push.OpenPostgresStore(ctx, dsn, 4)
	if err != nil {
		t.Fatalf("OpenPostgresStore: %v", err)
	}
	t.Cleanup(store.Close)

	userA := uuid.NewString()
	userB := uuid.NewString()
	token := "pg-apns-token-" + uuid.NewString()

	// Register for userA, then reassign to userB via same token.
	devA, err := store.RegisterDevice(ctx, userA, token, "iphone-1", push.EnvSandbox)
	if err != nil {
		t.Fatalf("RegisterDevice userA: %v", err)
	}
	devB, err := store.RegisterDevice(ctx, userB, token, "iphone-1", push.EnvProduction)
	if err != nil {
		t.Fatalf("RegisterDevice userB: %v", err)
	}
	if devA.ID == devB.ID {
		t.Fatalf("expected reassigned token to get a new ID, got %q", devB.ID)
	}
	listA, err := store.ListDevices(ctx, userA)
	if err != nil || len(listA) != 0 {
		t.Fatalf("userA devices = %+v, err=%v; want 0", listA, err)
	}
	if err := store.DeleteDevice(ctx, userA, devB.ID); !errors.Is(err, push.ErrNotFound) {
		t.Fatalf("expected ErrNotFound when userA deletes userB's device, got %v", err)
	}
	// Stale DeleteDeviceByToken scoped to userA must NOT delete userB's reassigned token.
	if err := store.DeleteDeviceByToken(ctx, userA, token); err != nil {
		t.Fatalf("DeleteDeviceByToken(userA): %v", err)
	}
	listB, err := store.ListDevices(ctx, userB)
	if err != nil || len(listB) != 1 || listB[0].ID != devB.ID {
		t.Fatalf("userB devices after stale userA token delete = %+v, err=%v; want 1", listB, err)
	}

	// Enqueue outbox idempotently and process via FakeSender.
	fake := push.NewFakeSender()
	worker := &push.OutboxWorker{Store: store, Sender: fake}
	deliveryID := "pg-delivery-" + uuid.NewString()
	_, created1, err := worker.Enqueue(ctx, push.EnqueueRequest{
		DeliveryID:    deliveryID,
		UserID:        userB,
		InteractionID: "int-pg-1",
		Kind:          push.KindApproval,
	})
	if err != nil || !created1 {
		t.Fatalf("first Enqueue created=%v err=%v", created1, err)
	}
	_, created2, err := worker.Enqueue(ctx, push.EnqueueRequest{
		DeliveryID:    deliveryID,
		UserID:        userB,
		InteractionID: "int-pg-1",
		Kind:          push.KindApproval,
	})
	if err != nil || created2 {
		t.Fatalf("second Enqueue created=%v err=%v; want false", created2, err)
	}

	stats, err := worker.ProcessPending(ctx, 20)
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if stats.Sent < 1 || fake.Count() != 1 {
		t.Fatalf("unexpected stats=%+v sent=%d", stats, fake.Count())
	}

	sentCount, err := store.CountSentSince(ctx, userB, time.Now().UTC().Add(-time.Hour))
	if err != nil || sentCount != 1 {
		t.Fatalf("CountSentSince = (%d, %v), want 1", sentCount, err)
	}

	if err := store.DeleteDevice(ctx, userB, devB.ID); err != nil {
		t.Fatalf("DeleteDevice userB: %v", err)
	}
}
