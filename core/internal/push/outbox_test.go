package push

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestOutboxIdempotencyAndRetry(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	if _, err := store.RegisterDevice(ctx, "user-1", "tok-1", "iphone", EnvProduction); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	fake := NewFakeSender()
	fake.Now = func() time.Time { return now }

	worker := &OutboxWorker{
		Store:  store,
		Sender: fake,
		Now:    func() time.Time { return now },
	}

	// Enqueue twice with the same delivery_id: second call must be a no-op.
	item1, created1, err := worker.Enqueue(ctx, EnqueueRequest{
		DeliveryID:    "delivery-abc",
		UserID:        "user-1",
		InteractionID: "int-100",
		Kind:          KindApproval,
	})
	if err != nil || !created1 {
		t.Fatalf("first Enqueue = (%+v, %v, %v), want created=true", item1, created1, err)
	}
	item2, created2, err := worker.Enqueue(ctx, EnqueueRequest{
		DeliveryID:    "delivery-abc",
		UserID:        "user-1",
		InteractionID: "int-100",
		Kind:          KindApproval,
	})
	if err != nil || created2 {
		t.Fatalf("second Enqueue = (%+v, %v, %v), want created=false", item2, created2, err)
	}

	// Inject a transient 503 error on the first attempt.
	fake.NextErr = &APNSError{StatusCode: http.StatusServiceUnavailable, Reason: "ServiceUnavailable"}
	stats, err := worker.ProcessPending(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessPending transient: %v", err)
	}
	if stats.Retried != 1 || stats.Sent != 0 || fake.Count() != 0 {
		t.Fatalf("unexpected transient stats=%+v sent=%d", stats, fake.Count())
	}

	// Next pass succeeds and sends exactly once.
	stats, err = worker.ProcessPending(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessPending retry: %v", err)
	}
	if stats.Sent != 1 || fake.Count() != 1 {
		t.Fatalf("unexpected retry stats=%+v sent=%d", stats, fake.Count())
	}

	// Running ProcessPending again must NOT re-send.
	stats, err = worker.ProcessPending(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessPending idempotent: %v", err)
	}
	if stats.Processed != 0 || fake.Count() != 1 {
		t.Fatalf("expected 0 processed after sent, got stats=%+v totalSent=%d", stats, fake.Count())
	}

	sent := fake.Sent()[0]
	if sent.Notification.Title != DefaultTitle || sent.Notification.Body != "Approval ready to review" {
		t.Fatalf("unexpected lock-screen copy: %+v", sent.Notification)
	}
	if sent.Notification.InteractionID != "int-100" || sent.Notification.CollapseID != "delivery-abc" {
		t.Fatalf("unexpected notification metadata: %+v", sent.Notification)
	}
}

func TestOutboxHardDailyCap(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	if _, err := store.RegisterDevice(ctx, "user-cap", "tok-cap", "iphone", EnvProduction); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	fake := NewFakeSender()
	worker := &OutboxWorker{
		Store:    store,
		Sender:   fake,
		DailyCap: 10,
		Now:      func() time.Time { return now },
	}

	// Enqueue 12 notifications for the same user within 24 hours.
	for i := 0; i < 12; i++ {
		if _, _, err := worker.Enqueue(ctx, EnqueueRequest{
			DeliveryID:    fmt.Sprintf("cap-deliv-%02d", i),
			UserID:        "user-cap",
			InteractionID: fmt.Sprintf("int-%02d", i),
			Kind:          KindUrgent,
		}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	stats, err := worker.ProcessPending(ctx, 50)
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if stats.Sent != 10 || stats.Suppressed != 2 || fake.Count() != 10 {
		t.Fatalf("cap enforcement failed: stats=%+v sent=%d, want 10 sent and 2 suppressed", stats, fake.Count())
	}

	// Verify the 11th item was marked suppressed with daily_cap_reached.
	suppressed, err := store.GetOutbox(ctx, "cap-deliv-10")
	if err != nil {
		t.Fatalf("GetOutbox: %v", err)
	}
	if suppressed.Status != StatusSuppressed || suppressed.LastError != "daily_cap_reached" {
		t.Fatalf("unexpected suppressed item: %+v", suppressed)
	}

	// Advance clock by 24h + 1m: rolling window resets and a new push succeeds.
	now = now.Add(24*time.Hour + time.Minute)
	if _, _, err := worker.Enqueue(ctx, EnqueueRequest{
		DeliveryID: "cap-deliv-next-day",
		UserID:     "user-cap",
		Kind:       KindBrief,
	}); err != nil {
		t.Fatalf("Enqueue next day: %v", err)
	}
	stats, err = worker.ProcessPending(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessPending next day: %v", err)
	}
	if stats.Sent != 1 || fake.Count() != 11 {
		t.Fatalf("expected next-day push to succeed, got stats=%+v sent=%d", stats, fake.Count())
	}
}

func TestOutboxInvalidTokenCleanupAndSanitization(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if _, err := store.RegisterDevice(ctx, "user-2", "tok-dead-410", "old-iphone", EnvProduction); err != nil {
		t.Fatalf("register dead token: %v", err)
	}
	if _, err := store.RegisterDevice(ctx, "user-2", "tok-live", "new-iphone", EnvProduction); err != nil {
		t.Fatalf("register live token: %v", err)
	}

	fake := NewFakeSender()
	fake.TokenErrors["tok-dead-410"] = &APNSError{StatusCode: http.StatusGone, Reason: "Unregistered"}

	worker := &OutboxWorker{Store: store, Sender: fake}

	// Bypass Enqueue to insert a row with sensitive text directly in the store;
	// ProcessPending must still sanitize it before handing to Sender.
	if _, _, err := store.EnqueueOutbox(ctx, OutboxItem{
		DeliveryID:    "sensitive-check",
		UserID:        "user-2",
		InteractionID: "int-secret",
		Title:         "Wire transfer to Bob",
		Body:          "Account 123456789 balance is $42,000",
		Status:        StatusPending,
	}); err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}

	stats, err := worker.ProcessPending(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if stats.Sent != 1 || fake.Count() != 1 {
		t.Fatalf("expected 1 sent to live token, got stats=%+v sent=%d", stats, fake.Count())
	}
	got := fake.Sent()[0]
	if got.Device.Token != "tok-live" {
		t.Fatalf("sent to token %q, want tok-live", got.Device.Token)
	}
	if got.Notification.Title != DefaultTitle || got.Notification.Body != DefaultBody {
		t.Fatalf("sensitive body leaked: %+v", got.Notification)
	}

	// Dead 410 token must be automatically deleted.
	devices, err := store.ListDevices(ctx, "user-2")
	if err != nil || len(devices) != 1 || devices[0].Token != "tok-live" {
		t.Fatalf("devices after 410 cleanup = %+v, err=%v; want only tok-live", devices, err)
	}
}

func TestOutboxAllTokensInvalidSuppressesItem(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if _, err := store.RegisterDevice(ctx, "user-3", "tok-bad-400", "iphone", EnvSandbox); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	fake := NewFakeSender()
	fake.TokenErrors["tok-bad-400"] = &APNSError{StatusCode: http.StatusBadRequest, Reason: "BadDeviceToken"}

	worker := &OutboxWorker{Store: store, Sender: fake}
	if _, _, err := worker.Enqueue(ctx, EnqueueRequest{
		DeliveryID: "deliv-all-bad",
		UserID:     "user-3",
		Kind:       KindUpdate,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	stats, err := worker.ProcessPending(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if stats.Suppressed != 1 || fake.Count() != 0 {
		t.Fatalf("expected 1 suppressed, got stats=%+v sent=%d", stats, fake.Count())
	}
	item, err := store.GetOutbox(ctx, "deliv-all-bad")
	if err != nil || item.Status != StatusSuppressed || item.LastError != "all_tokens_invalid" {
		t.Fatalf("unexpected outbox state: %+v err=%v", item, err)
	}
	devices, _ := store.ListDevices(ctx, "user-3")
	if len(devices) != 0 {
		t.Fatalf("expected bad token deleted, got %+v", devices)
	}
}

func TestOutboxPermanentFailureStopsRetrying(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if _, err := store.RegisterDevice(ctx, "user-4", "tok-ok", "iphone", EnvProduction); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	fake := NewFakeSender()
	fake.SendFn = func(context.Context, Device, Notification) error {
		return errors.New("network timeout")
	}

	worker := &OutboxWorker{Store: store, Sender: fake, MaxAttempts: 2}
	if _, _, err := worker.Enqueue(ctx, EnqueueRequest{
		DeliveryID: "deliv-retry-limit",
		UserID:     "user-4",
		Kind:       KindUpdate,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Attempt 1 -> stays pending.
	stats, err := worker.ProcessPending(ctx, 10)
	if err != nil || stats.Retried != 1 {
		t.Fatalf("attempt 1 = (%+v, %v), want Retried=1", stats, err)
	}
	// Attempt 2 -> hits MaxAttempts, marked failed.
	stats, err = worker.ProcessPending(ctx, 10)
	if err != nil || stats.Failed != 1 {
		t.Fatalf("attempt 2 = (%+v, %v), want Failed=1", stats, err)
	}
	item, err := store.GetOutbox(ctx, "deliv-retry-limit")
	if err != nil || item.Status != StatusFailed || item.Attempts != 2 {
		t.Fatalf("unexpected final item: %+v err=%v", item, err)
	}
}
